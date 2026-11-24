package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/chat/standin"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

type chatTestPeer struct {
	since time.Time
	port  *standin.Port
	calls int
	code  string
}

func (p *chatTestPeer) Connected() (time.Time, bool) { return p.since, !p.since.IsZero() }
func (p *chatTestPeer) Caps() []string               { return []string{CapChat} }
func (p *chatTestPeer) Call(ctx context.Context, op string, params, result any) error {
	p.calls++
	if p.code != "" {
		return &seatgate.RemoteError{Code: p.code}
	}
	if op != ownerwire.OpSeatChat {
		return errors.New("unexpected operation")
	}
	c, err := ownerwire.DecodeSeatChatParams(params.(json.RawMessage))
	if err != nil {
		return err
	}
	r, err := p.port.Chat(ctx, c)
	if err != nil {
		return err
	}
	raw, err := ownerwire.EncodeSeatChatResult(r)
	*result.(*json.RawMessage) = raw
	return err
}

func TestSeatChatMemory(t *testing.T) {
	clock := &testClock{t: decideNow}
	d := Daemon{budget: budget{now: roomy()}, gnow: clock.now}
	p := chatTestPeer{since: clock.now(), port: standin.New(clock.now)}
	list := seatapp.ChatCall{Verb: seatapp.ChatLeads}
	read := seatapp.ChatCall{Verb: seatapp.ChatRead, Lead: "daemon:alpha"}
	ask := func(c seatapp.ChatCall, n int) {
		t.Helper()
		got, err := d.chat(context.Background(), c, &p)
		if err != nil || !got.ValidFor(c) || p.calls != n || !got.StandIn {
			t.Fatalf("call %s: count %d, want %d, error %v", c.Verb, p.calls, n, err)
		}
	}
	ask(list, 1)
	ask(read, 2)
	other := read
	other.After = 4
	ask(other, 3)
	ask(read, 3)
	ask(list, 3)
	clock.advance(10 * time.Second)
	ask(read, 4)
	ask(list, 4)
	clock.advance(10 * time.Second)
	ask(list, 5)
	ask(read, 6)
	p.code = ownerwire.CodeChatBusy
	if _, err := d.chat(context.Background(), other, &p); err != seatapp.ErrBusy {
		t.Fatal(err)
	}
	p.code = ""
	ask(list, 8)
	ask(read, 9)
	p.since = clock.now()
	ask(read, 10)
	p.since = time.Time{}
	if _, err := d.chat(context.Background(), read, &p); err != seatapp.ErrDaemonUnavailable {
		t.Fatal(err)
	}
}

func TestSeatChatWireBoundsAndCodes(t *testing.T) {
	if !slices.Contains(Caps(), CapChat) || seatapp.MaxChatTextBytes != ownerwire.MaxChatText || seatapp.MaxChatSeq != ownerwire.MaxChatSeq || seatapp.MaxChatKeyBytes != ownerwire.MaxChatKeyBytes || seatapp.MaxChatLeads != ownerwire.MaxChatLeads || seatapp.MaxChatEntries != ownerwire.MaxChatReadLimit {
		t.Fatal("seat and wire disagree")
	}
	for code, want := range map[string]error{"chat-off": seatapp.ErrChatOff, "chat-busy": seatapp.ErrBusy, "chat-unknown-lead": seatapp.ErrChatUnknownLead, "rate-limited": seatapp.ErrBusy, "too-many-refusals": seatapp.ErrBusy, "unknown-op": seatapp.ErrNotOffered} {
		if got := mapCall(context.Background(), ownerwire.OpSeatChat, &seatgate.RemoteError{Code: code}, chatCodes); got != want {
			t.Errorf("%s: %v", code, got)
		}
	}
}

func TestSeatChatRejectsMalformedAnswers(t *testing.T) {
	const row = `{"chat":"off","lead":"daemon:alpha"}`
	for _, r := range []string{strings.Replace(row, "off", "maybe", 1), strings.Replace(row, "alpha", "Later", 1), row + "," + row, strings.Repeat(row+",", 8) + row,
		strings.Replace(row, "}", `,"state":""}`, 1), strings.Replace(row, "}", `,"head":0}`, 1), strings.Replace(row, "}", `,"epoch":null}`, 1),
		strings.Replace(row, "alpha", `\ud800`, 1), strings.Replace(row, "alpha", "\xff", 1)} {
		if _, err := decodeChat(seatapp.ChatCall{Verb: "leads"}, []byte(`{"state":"leads","leads":[`+r+`]}`)); err != errChatAnswer {
			t.Error("accepted invalid row")
		}
	}
	const page = `{"state":"read","lead":"daemon:alpha","head":5,"entries":[{"seq":5,"who":"lead","at":"2026-01-02T03:04:05Z","text":"words","cut":true}]}`
	call := seatapp.ChatCall{Verb: "read", Lead: "daemon:alpha", After: 4}
	for _, pair := range [][2]string{{"read", "leads"}, {"alpha", "bravo"}, {"lead\",\"at", "seat\",\"at"}, {"03:04:05Z", "03:04:05+01:00"}, {"words", "\xff"}, {"words", "\u202e"}, {"words", strings.Repeat("x", 2001)}, {"words", strings.Repeat("x", ownerwire.MaxSeatChatResultBytes)}, {`"seq":5`, `"seq":4`}, {`"head":5`, `"head":4`}, {"true", "null"}} {
		if _, err := decodeChat(call, []byte(strings.Replace(page, pair[0], pair[1], 1))); err != errChatAnswer {
			t.Error("accepted invalid page")
		}
	}
	got, err := decodeChat(call, []byte(page))
	if err != nil || !got.Entries[0].Cut || got.Entries[0].Text != "words" || got.Entries[0].Seq != 5 || got.Head != 5 || got.Entries[0].Who != "lead" || got.Entries[0].At.Hour() != 3 {
		t.Fatal("lost read fields", err)
	}
}

// A six-round interview, with start and two corrections, at a tap every
// five seconds. Cards reloads every twenty seconds, including its projects
// read. The owner opens chat and refreshes it twice during the interview.
func TestSeatChatMixedBudget(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := budget{now: clock.now}
	take := func(k kind) {
		t.Helper()
		if !b.take(k) {
			t.Fatalf("normal use exhausted room %d", k)
		}
	}
	for tap := 0; tap < 9; tap++ {
		take(kindGenesis)
		if tap%4 == 0 {
			take(kindRead)
			take(kindGenesis)
			take(kindChat)
			take(kindChat)
		}
		clock.advance(5 * time.Second)
	}
}

func TestSeatChatStandInOverGate(t *testing.T) {
	seat := newIdentity(t, "seat")
	p := standin.New(time.Now)
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{CapChat}, func(ctx context.Context, _ seatgate.Peer, op string, raw json.RawMessage) (json.RawMessage, error) {
		if op != ownerwire.OpSeatChat {
			return nil, errors.New("unexpected operation")
		}
		c, err := ownerwire.DecodeSeatChatParams(raw)
		if err != nil {
			return nil, err
		}
		r, err := p.Chat(ctx, c)
		if err != nil {
			return nil, err
		}
		return ownerwire.EncodeSeatChatResult(r)
	})
	d := dialPinned(t, g, seat)
	if !d.ChatOffered() {
		t.Fatal("missing chat offer")
	}
	for _, call := range []seatapp.ChatCall{{Verb: "leads"}, {Verb: "read", Lead: "daemon:alpha"}} {
		got, err := d.Chat(context.Background(), call)
		if err != nil || !got.StandIn || !got.ValidFor(call) || len(got.Rows)+len(got.Entries) == 0 {
			t.Fatalf("stand-in %s: %v", call.Verb, err)
		}
	}
}
