package gatehost

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// chatWaiting is a service that answers both waits at once and records which
// it was asked. Its extended wait reads the cursor it was handed, as the
// service does, and answers it, or no chat when that fails.
type chatWaiting struct {
	fakePort
	mu     sync.Mutex
	plain  []string
	chats  []int64
	srcs   []ownerchannel.ChatCursor
	noChat bool
}

func (w *chatWaiting) result() ownerwire.SeatWaitResult {
	return ownerwire.SeatWaitResult{Version: ownerwire.PendingVersion(nil), Cards: []ownerwire.SeatWaitCard{}}
}

func (w *chatWaiting) WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.plain = append(w.plain, since)
	return w.result(), nil
}

func (w *chatWaiting) WaitPendingChat(ctx context.Context, since string, chat int64, src ownerchannel.ChatCursor) (ownerwire.SeatWaitResult, error) {
	w.mu.Lock()
	w.chats = append(w.chats, chat)
	w.srcs = append(w.srcs, src)
	noChat := w.noChat
	w.mu.Unlock()
	r := w.result()
	if v, err := src.ChatCursor(ctx); err == nil && !noChat {
		r.Chat = &v
	}
	return r, nil
}

func (w *chatWaiting) asked() (plain []string, chats []int64, srcs []ownerchannel.ChatCursor) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.plain), slices.Clone(w.chats), slices.Clone(w.srcs)
}

var (
	_ WaitPort     = (*chatWaiting)(nil)
	_ ChatWaitPort = (*chatWaiting)(nil)
)

// cursorChat is a chat port with a cursor, which counts its calls.
type cursorChat struct {
	fakeChat
	cmu     sync.Mutex
	cursors int
}

const theCursor = int64(4242)

func (c *cursorChat) ChatCursor(ctx context.Context) (int64, error) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	c.cursors++
	return theCursor, nil
}

func (c *cursorChat) ChatCursorChanged() <-chan struct{} { return nil }

func (c *cursorChat) cursorCalls() int {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return c.cursors
}

var _ ownerchannel.ChatCursor = (*cursorChat)(nil)

func waitChatParams(t testing.TB, since string, chat int64) json.RawMessage {
	t.Helper()
	b, err := ownerwire.EncodeSeatWaitChatParams(ownerwire.SeatWaitParams{Since: since, Chat: &chat})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The handler lists wait-chat last, and only when every part is there: a
// service that can wait and answer the extended wait, a chat port, and a
// cursor of that port.
func TestWaitChatIsListedOnlyWithEveryPart(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  Port
		chat ChatPort
		want []string
	}{
		{"everything", &chatWaiting{}, &cursorChat{}, []string{"pair", "read", "watch", "chat", "wait-chat"}},
		{"a chat port with no cursor", &chatWaiting{}, &fakeChat{}, []string{"pair", "read", "watch", "chat"}},
		{"no chat port", &chatWaiting{}, nil, []string{"pair", "read", "watch"}},
		{"a service with the plain wait only", &waitingPort{}, &cursorChat{}, []string{"pair", "read", "watch", "chat"}},
		{"a service that cannot wait", &fakePort{}, &cursorChat{}, []string{"pair", "read", "chat"}},
	} {
		var opts []Option
		if tc.chat != nil {
			opts = append(opts, WithChat(tc.chat))
		}
		h, err := New(tc.svc, pinnedOnly, nil, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(h.Caps(), tc.want) {
			t.Errorf("%s: the handler lists %q, want %q", tc.name, h.Caps(), tc.want)
		}
	}
	h, err := NewDeciding(&chatWaiting{}, &fakeSeat{}, theGate, pinnedOnly, nil, WithGenesis(&fakeGenesis{}), WithChat(&cursorChat{}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pair", "read", "login", "decide", "watch", "genesis", "chat", "wait-chat"}; !slices.Equal(h.Caps(), want) {
		t.Errorf("the fullest handler lists %q, want %q", h.Caps(), want)
	}
}

// A legacy seat on a gate that offers the extension sends the plain params
// and gets exactly the plain result: no chat, read by the plain decoder, from
// the plain wait. The cursor is not read for it.
func TestAPlainWaitOnAnExtendedGateIsThePlainWait(t *testing.T) {
	svc, c := &chatWaiting{}, &cursorChat{}
	h, err := New(svc, pinnedOnly, nil, WithChat(c))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, sinceParams(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "chat") {
		t.Fatalf("the plain result names chat: %s", out)
	}
	want, _ := ownerwire.EncodeSeatWaitResult(svc.result())
	if string(out) != string(want) {
		t.Fatalf("the plain result is %s, want %s", out, want)
	}
	if _, err := ownerwire.DecodeSeatWaitResult(out); err != nil {
		t.Fatalf("a legacy seat cannot read it: %v", err)
	}
	plain, chats, _ := svc.asked()
	if len(plain) != 1 || len(chats) != 0 || c.cursorCalls() != 0 {
		t.Fatalf("plain %d, extended %d, cursor reads %d", len(plain), len(chats), c.cursorCalls())
	}
}

// The extended params on a gate that does not offer the extension are
// bad-params, as at a gate of an earlier build, and reach no wait.
func TestAnExtendedWaitOnAPlainGateIsBadParams(t *testing.T) {
	for name, tc := range map[string]struct {
		svc  Port
		chat ChatPort
	}{
		"no cursor":      {&chatWaiting{}, &fakeChat{}},
		"no chat port":   {&chatWaiting{}, nil},
		"the plain wait": {&waitingPort{}, &cursorChat{}},
	} {
		var opts []Option
		if tc.chat != nil {
			opts = append(opts, WithChat(tc.chat))
		}
		h, err := New(tc.svc, pinnedOnly, nil, opts...)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.Handle(context.Background(), seat, ownerwire.OpSeatWait, waitChatParams(t, "", 0))
		if codeOfErr(err) != CodeBadParams {
			t.Errorf("%s: answered %v, want bad-params", name, err)
		}
		if cw, ok := tc.svc.(*chatWaiting); ok {
			if plain, chats, _ := cw.asked(); len(plain)+len(chats) != 0 {
				t.Errorf("%s: a wait was asked", name)
			}
		}
	}
}

// The extended params reach the extended wait with the seat's cursor and the
// chat port's cursor, and the answer is the extended result; one the service
// gives without chat (its source failed) is the extended result without it.
// Params that are neither form are bad-params.
func TestAnExtendedWaitIsHandedTheChatPortsCursor(t *testing.T) {
	svc, c := &chatWaiting{}, &cursorChat{}
	h, err := New(svc, pinnedOnly, nil, WithChat(c))
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, waitChatParams(t, "", 99))
	if err != nil {
		t.Fatal(err)
	}
	r, err := ownerwire.DecodeSeatWaitChatResult(out)
	if err != nil || r.Chat == nil || *r.Chat != theCursor {
		t.Fatalf("answered %s (%v)", out, err)
	}
	if _, err := ownerwire.DecodeSeatWaitResult(out); err == nil {
		t.Fatal("the plain decoder takes the extended result")
	}
	_, chats, srcs := svc.asked()
	if !slices.Equal(chats, []int64{99}) || len(srcs) != 1 || srcs[0] != ownerchannel.ChatCursor(c) {
		t.Fatalf("the extended wait was asked %v with %v", chats, srcs)
	}
	if len(c.seen()) != 0 {
		t.Fatal("the wait read the chat")
	}

	svc.mu.Lock()
	svc.noChat = true
	svc.mu.Unlock()
	out, err = h.Handle(context.Background(), seat, ownerwire.OpSeatWait, waitChatParams(t, "", 99))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ownerwire.DecodeSeatWaitChatResult(out); err != nil || r.Chat != nil {
		t.Fatalf("without a cursor: %s (%v)", out, err)
	}

	for _, bad := range []string{
		`{"since":"","chat":-1}`,
		`{"since":"","chat":9007199254740992}`,
		`{"since":"","chat":"1"}`,
		`{"chat":1}`,
		`{"since":"","chat":1,"lead":"daemon:alpha"}`,
	} {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, json.RawMessage(bad)); codeOfErr(err) != CodeBadParams {
			t.Errorf("%s: answered %v, want bad-params", bad, err)
		}
	}
}

// The extended wait holds the peer's wait slots as the plain one does.
func TestAnExtendedWaitTakesAWaitSlot(t *testing.T) {
	block := make(chan struct{})
	svc := &blockingChatWait{block: block}
	h, err := New(svc, pinnedOnly, nil, WithChat(&cursorChat{}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, MaxWaitsPerPeer)
	for i := 0; i < MaxWaitsPerPeer; i++ {
		go func() {
			_, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, waitChatParams(t, "", 1))
			done <- err
		}()
	}
	eventually(t, "the waits to be held", func() bool {
		h.waits.mu.Lock()
		defer h.waits.mu.Unlock()
		return h.waits.held[seatFP] == MaxWaitsPerPeer
	})
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, sinceParams(t, "")); codeOfErr(err) != ownerwire.CodeWaitFull {
		t.Fatalf("a third wait: %v", err)
	}
	close(block)
	for i := 0; i < MaxWaitsPerPeer; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

type blockingChatWait struct {
	fakePort
	block chan struct{}
}

func (b *blockingChatWait) WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	<-b.block
	return ownerwire.SeatWaitResult{Version: ownerwire.PendingVersion(nil), Cards: []ownerwire.SeatWaitCard{}}, nil
}

func (b *blockingChatWait) WaitPendingChat(ctx context.Context, since string, chat int64, src ownerchannel.ChatCursor) (ownerwire.SeatWaitResult, error) {
	return b.WaitPending(ctx, since)
}

// An extended wait reaches the wait and the cursor, and nothing else: not the
// service's pairing or reads, the login and decision, the thread store, or a
// read of the chat.
func TestAnExtendedWaitReachesNoOtherPort(t *testing.T) {
	svc, s, g, c := &chatWaiting{}, &fakeSeat{}, &fakeGenesis{}, &cursorChat{}
	h, err := NewDeciding(svc, s, theGate, pinnedOnly, nil, WithGenesis(g), WithChat(c))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, waitChatParams(t, "", int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	if n, m, k, ch := len(svc.seen()), len(s.seen()), len(g.seen()), len(c.seen()); n+m+k+ch != 0 {
		t.Fatalf("an extended wait reached another port: service %d, seat %d, thread store %d, chat %d", n, m, k, ch)
	}
	if _, chats, _ := svc.asked(); len(chats) != 5 || c.cursorCalls() != 5 {
		t.Fatalf("extended waits %d, cursor reads %d", len(chats), c.cursorCalls())
	}
}

// Over the real gate: a new seat that reads wait-chat in the hello gets the
// extended result, and a legacy seat on the same gate gets the plain one.
func TestWaitChatOverTheGateInBothDirections(t *testing.T) {
	gateID, seatID := newIdentity(t, "gate"), newIdentity(t, "seat")
	svc := &chatWaiting{}
	h, err := New(svc, func(fp string) bool { return fp == seatID.Fingerprint() }, nil, WithChat(&cursorChat{}))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(gateID, []string{seatID.Fingerprint()}), Caps: h.Caps()}, h)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcancel()
	cl, err := seatgate.Dial(dctx, ln.Addr().String(), seatgate.ClientConfig{TLS: seatgate.ClientTLS(seatID, gateID.Fingerprint(), "gate"), Caps: []string{"watch"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if !slices.Contains(cl.Caps(), CapWaitChat) {
		t.Fatalf("the hello lists %q", cl.Caps())
	}
	var raw json.RawMessage
	if err := cl.Call(dctx, ownerwire.OpSeatWait, waitChatParams(t, "", 0), &raw); err != nil {
		t.Fatal(err)
	}
	if r, err := ownerwire.DecodeSeatWaitChatResult(raw); err != nil || r.Chat == nil || *r.Chat != theCursor {
		t.Fatalf("the new seat got %s (%v)", raw, err)
	}
	raw = nil
	if err := cl.Call(dctx, ownerwire.OpSeatWait, sinceParams(t, ""), &raw); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerwire.DecodeSeatWaitResult(raw); err != nil {
		t.Fatalf("the legacy seat cannot read %s: %v", raw, err)
	}
}
