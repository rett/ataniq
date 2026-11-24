//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/chat/standin"
	"ataniq/internal/seatgate"
)

// These tests run seat.chat over the real gate on the loopback address, with
// the phone trial's service behind it (trial_e2e_test.go) and, as the daemon
// gives the trial's gate and no other, the STAND-IN chat port:
//
//	package chat/standin, sample leads in memory
//	  -> gatehost (seat.chat) -> seatgate.Serve, TLS 1.3 with pins
//	  -> seatgate.Client, which sends the frames as the seat's link will
//
// The seat link and the pages are not part of it. A gate of any other run is
// given no chat port and lists no chat.

// startChatGate serves a deciding gate over the world's service on a new
// listener on 127.0.0.1, pinning the world's seat key, with the chat port
// given, or none (nil), as the daemon makes the gate of the trial and of any
// other run. It returns the address and the handler.
func (w *decideWorld) startChatGate(chat gatehost.ChatPort) (string, *gatehost.Handler) {
	w.t.Helper()
	pinned := func(fp string) bool { return fp == w.seatID.Fingerprint() }
	var opts []gatehost.Option
	if chat != nil {
		opts = append(opts, gatehost.WithChat(chat))
	}
	h, err := gatehost.NewDeciding(w.svc, w.svc, w.gate, pinned, nil, opts...)
	if err != nil {
		w.t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(w.gateID, []string{w.seatID.Fingerprint()}), Caps: h.Caps()}, h)
	}()
	w.t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				w.t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			w.t.Error("Serve did not end")
		}
	})
	return ln.Addr().String(), h
}

// chatSeat is the raw frame sender: the seat's key, dialed to a gate.
type chatSeat struct {
	t *testing.T
	c *seatgate.Client
}

func (w *decideWorld) dialChat(addr string) *chatSeat {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pairCallTime)
	defer cancel()
	c, err := seatgate.Dial(ctx, addr, seatgate.ClientConfig{
		TLS: seatgate.ClientTLS(w.seatID, w.gateID.Fingerprint(), "gate"), Caps: []string{"pair", "read", "login", "decide", "chat"},
		DialTimeout: 5 * time.Second, HelloTimeout: 5 * time.Second,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	return &chatSeat{t: w.t, c: c}
}

// send sends seat.chat with the params, and returns the result as the wire's
// strict decoder reads it, the raw frame, or the gate's code.
func (s *chatSeat) send(p ownerwire.SeatChatParams) (ownerwire.SeatChatResult, json.RawMessage, string) {
	s.t.Helper()
	b, err := ownerwire.EncodeSeatChatParams(p)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.sendRaw(b)
}

func (s *chatSeat) sendRaw(b json.RawMessage) (ownerwire.SeatChatResult, json.RawMessage, string) {
	s.t.Helper()
	var raw json.RawMessage
	if err := s.c.Call(pairCtx(s.t), ownerwire.OpSeatChat, b, &raw); err != nil {
		var re *seatgate.RemoteError
		if errors.As(err, &re) {
			return ownerwire.SeatChatResult{}, nil, re.Code
		}
		s.t.Fatalf("seat.chat: %v", err)
	}
	res, err := ownerwire.DecodeSeatChatResult(raw)
	if err != nil {
		s.t.Fatalf("the result does not decode: %v", err)
	}
	return res, raw, ""
}

func (s *chatSeat) ok(p ownerwire.SeatChatParams) ownerwire.SeatChatResult {
	s.t.Helper()
	res, _, code := s.send(p)
	if code != "" {
		s.t.Fatalf("%+v was refused %s", p, code)
	}
	if !res.StandIn {
		s.t.Fatalf("%+v: the result is not marked stand_in", p)
	}
	return res
}

func cRead(lead string, after int64, limit int) ownerwire.SeatChatParams {
	return ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbRead, Lead: lead, After: after, Limit: limit}
}

// The trial's gate serves the STAND-IN chat: the hello lists chat last; leads
// lists the three sample leads, the off one bare on the wire; read pages
// through each whole sample conversation by its cursor; a cursor before what
// is retained resets; the off lead is chat-off and a lead the stand-in does
// not hold chat-unknown-lead, past the refusal bound each without shutting
// the seat out;
// a send is bad-params; and none of it makes a card.
func TestChatOverTheTrialsGate(t *testing.T) {
	w, _ := trialWorld(t)
	addr, h := w.startChatGate(standin.New(w.rig.clock.Now))
	s := w.dialChat(addr)
	// chat, and after it the extended wait: the trial's service can wait and
	// the stand-in gives a cursor.
	if caps := s.c.Caps(); len(caps) < 2 || caps[len(caps)-2] != gatehost.CapChat || caps[len(caps)-1] != gatehost.CapWaitChat || !slices.Equal(caps, h.Caps()) {
		t.Fatalf("the hello lists %q", caps)
	}

	leads, raw, code := s.send(ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads})
	if code != "" || !leads.StandIn || len(leads.Leads) != 3 {
		t.Fatalf("leads: %+v %s", leads, code)
	}
	if !strings.Contains(string(raw), `{"chat":"off","lead":"daemon:charlie"}`) {
		t.Fatalf("the off row is not bare on the wire: %s", raw)
	}
	for _, r := range leads.Leads[:2] {
		if r.State == "" || r.Epoch == nil || r.Head == nil || r.Chat != ownerwire.ChatRead {
			t.Fatalf("a read row: %+v", r)
		}
	}

	for _, row := range leads.Leads[:2] {
		whole := s.ok(cRead(row.Lead, 0, ownerwire.MaxChatReadLimit))
		if whole.More || whole.Head != *row.Head || whole.Epoch != *row.Epoch || len(whole.Entries) == 0 ||
			whole.Entries[len(whole.Entries)-1].Seq != *row.Head {
			t.Fatalf("%s whole: %+v", row.Lead, whole)
		}
		var got []ownerwire.ChatEntry
		after := int64(0)
		for pages := 0; ; pages++ {
			if pages > 2*len(whole.Entries) {
				t.Fatalf("%s: the cursor does not move on", row.Lead)
			}
			page := s.ok(cRead(row.Lead, after, 2))
			if len(page.Entries) > 2 || page.Reset {
				t.Fatalf("%s after %d: %+v", row.Lead, after, page)
			}
			got = append(got, page.Entries...)
			if !page.More {
				break
			}
			after = page.Entries[len(page.Entries)-1].Seq
		}
		if !reflect.DeepEqual(got, whole.Entries) {
			t.Fatalf("%s: the pages are not the conversation", row.Lead)
		}
	}
	if r := s.ok(cRead(standin.LeadAlpha, 1, 4)); !r.Reset || r.Entries[0].Seq != 4 || !r.More {
		t.Fatalf("a cursor before what is retained: %+v", r)
	}

	for lead, want := range map[string]string{standin.LeadCharlie: ownerwire.CodeChatOff, "daemon:delta": ownerwire.CodeChatUnknownLead} {
		// One more than the refusal bound each, all on one connection within
		// the gate's 60 requests a minute.
		for i := 0; i <= gatehost.RefusalBound; i++ {
			if _, _, code := s.send(cRead(lead, 0, 4)); code != want {
				t.Fatalf("%s %d: %q, want %s", lead, i, code, want)
			}
		}
	}
	if _, _, code := s.sendRaw(json.RawMessage(`{"verb":"send","lead":"daemon:alpha","body":"approve it"}`)); code != gatehost.CodeBadParams {
		t.Fatalf("a send: %q", code)
	}
	// Still served after the refusals, and nothing became a card.
	var l ownerwire.Listing
	if err := s.c.Call(pairCtx(t), ownerwire.OpList, nil, &l); err != nil || len(l.Pending) != 0 {
		t.Fatalf("owner.list after: %+v %v", l, err)
	}
	if n := h.Counts()[gatehost.CodeTooManyRefusals]; n != 0 {
		t.Fatalf("the seat was shut out %d times", n)
	}
}

// A gate of a run that is not the trial is given no chat port: its hello
// lists no chat, and seat.chat is unknown-op.
func TestAGateWithoutTheTrialListsNoChat(t *testing.T) {
	w := newDecideWorld(t, nil)
	addr, h := w.startChatGate(nil)
	s := w.dialChat(addr)
	if slices.Contains(s.c.Caps(), gatehost.CapChat) || slices.Contains(h.Caps(), gatehost.CapChat) || slices.Contains(h.Caps(), gatehost.CapWaitChat) {
		t.Fatalf("the hello lists %q", s.c.Caps())
	}
	if _, _, code := s.send(ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads}); code != gatehost.CodeUnknownOp {
		t.Fatalf("seat.chat: %q", code)
	}
}

// waitRaw sends seat.wait with the raw params and returns the raw result or
// the gate's code.
func (s *chatSeat) waitRaw(b json.RawMessage) (json.RawMessage, string) {
	s.t.Helper()
	var raw json.RawMessage
	if err := s.c.Call(pairCtx(s.t), ownerwire.OpSeatWait, b, &raw); err != nil {
		var re *seatgate.RemoteError
		if errors.As(err, &re) {
			return nil, re.Code
		}
		s.t.Fatalf("seat.wait: %v", err)
	}
	return raw, ""
}

// The extended wait over the trial's gate, with the real owner channel's
// service and the stand-in's cursor: a new seat that sends it gets the Card
// result and the stand-in's deterministic cursor; a legacy seat on the same
// gate sends the plain params and gets exactly the plain result; and a gate
// with no chat port (an older or a non-trial gate) refuses the extended params
// bad-params, so a seat that sent them anyway falls back to the plain wait.
func TestWaitChatOverTheTrialsGate(t *testing.T) {
	w, _ := trialWorld(t)
	port := standin.New(w.rig.clock.Now)
	addr, _ := w.startChatGate(port)
	s := w.dialChat(addr)
	want, err := port.ChatCursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	ext, err := ownerwire.EncodeSeatWaitChatParams(ownerwire.SeatWaitParams{Chat: &zero})
	if err != nil {
		t.Fatal(err)
	}
	raw, code := s.waitRaw(ext)
	if code != "" {
		t.Fatalf("the extended wait was refused %s", code)
	}
	r, err := ownerwire.DecodeSeatWaitChatResult(raw)
	if err != nil || r.Chat == nil || *r.Chat != want {
		t.Fatalf("the extended wait answered %s (%v), want the cursor %d", raw, err, want)
	}
	plain, err := ownerwire.EncodeSeatWaitParams(ownerwire.SeatWaitParams{})
	if err != nil {
		t.Fatal(err)
	}
	raw, code = s.waitRaw(plain)
	if code != "" {
		t.Fatalf("the plain wait was refused %s", code)
	}
	p, err := ownerwire.DecodeSeatWaitResult(raw)
	if err != nil || strings.Contains(string(raw), "chat") || p.Version != r.Version {
		t.Fatalf("the plain wait answered %s (%v)", raw, err)
	}

	other, h := w.startChatGate(nil)
	if slices.Contains(h.Caps(), gatehost.CapWaitChat) {
		t.Fatalf("a gate with no chat lists %q", h.Caps())
	}
	o := w.dialChat(other)
	if _, code := o.waitRaw(ext); code != gatehost.CodeBadParams {
		t.Fatalf("the extended wait at a gate without it: %q", code)
	}
	if _, code := o.waitRaw(plain); code != "" {
		t.Fatalf("the plain wait after it: %q", code)
	}
}
