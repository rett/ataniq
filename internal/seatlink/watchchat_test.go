package seatlink

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// chatWaitDesk is the daemon's side of both forms of seat.wait for the tests:
// a pending set and a chat cursor the test changes (0 is a daemon with no
// cursor to give), and the gate's hold. The plain params get the plain result;
// the extended ones, unless refuseExt, the extended result. It records the
// params of every wait.
type chatWaitDesk struct {
	mu        sync.Mutex
	cards     []ownerwire.SeatWaitCard
	cursor    int64
	wake      chan struct{}
	hold      time.Duration
	refuseExt bool
	seen      []ownerwire.SeatWaitParams
	held      chan struct{}
}

func newChatWaitDesk(cursor int64) *chatWaitDesk {
	return &chatWaitDesk{cursor: cursor, wake: make(chan struct{}), hold: 10 * time.Second, held: make(chan struct{}, 64)}
}

func (d *chatWaitDesk) change(f func(d *chatWaitDesk)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
	close(d.wake)
	d.wake = make(chan struct{})
}

func (d *chatWaitDesk) params() []ownerwire.SeatWaitParams {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.seen)
}

func (d *chatWaitDesk) resultLocked(ext bool) json.RawMessage {
	cards := slices.Clone(d.cards)
	if cards == nil {
		cards = []ownerwire.SeatWaitCard{}
	}
	r := ownerwire.SeatWaitResult{Version: ownerwire.PendingVersion(d.cards), Waiting: len(cards), Cards: cards}
	if !ext {
		out, err := ownerwire.EncodeSeatWaitResult(r)
		if err != nil {
			panic(err)
		}
		return out
	}
	if d.cursor != 0 {
		c := d.cursor
		r.Chat = &c
	}
	out, err := ownerwire.EncodeSeatWaitChatResult(r)
	if err != nil {
		panic(err)
	}
	return out
}

func (d *chatWaitDesk) handle(ctx context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	if op != ownerwire.OpSeatWait {
		return nil, seatgate.NewCodedError("unknown-op")
	}
	p, err := ownerwire.DecodeSeatWaitParams(params)
	ext := false
	if err != nil {
		d.mu.Lock()
		refuse := d.refuseExt
		d.mu.Unlock()
		if refuse {
			d.mu.Lock()
			d.seen = append(d.seen, ownerwire.SeatWaitParams{Since: "refused"})
			d.mu.Unlock()
			return nil, seatgate.NewCodedError("bad-params")
		}
		if p, err = ownerwire.DecodeSeatWaitChatParams(params); err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		ext = true
	}
	d.mu.Lock()
	d.seen = append(d.seen, p)
	for {
		moved := ownerwire.PendingVersion(d.cards) != p.Since
		if ext && d.cursor != 0 && d.cursor != *p.Chat {
			moved = true
		}
		if moved {
			defer d.mu.Unlock()
			return d.resultLocked(ext), nil
		}
		wake := d.wake
		d.mu.Unlock()
		select {
		case d.held <- struct{}{}:
		default:
		}
		timer := time.NewTimer(d.hold)
		select {
		case <-wake:
			timer.Stop()
			d.mu.Lock()
		case <-timer.C:
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.resultLocked(ext), nil
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

var fullCaps = []string{"pair", "read", "watch", "chat", "wait-chat"}

// nextHint waits for the chat hint to satisfy ok, the channel first and the
// hint after, as a page does.
func nextHint(t *testing.T, d *Daemon, what string, ok func(ChatHint) bool) ChatHint {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		ch := d.ChatHintChanged()
		if h := d.ChatHint(); ok(h) {
			return h
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out: %s (hint %+v)", what, d.ChatHint())
		}
	}
}

func chatRig(t *testing.T, desk *chatWaitDesk, caps []string) (*Daemon, *seatgate.Client, *gate) {
	t.Helper()
	seat := newIdentity(t, "seat")
	g := serveGateCaps(t, []string{seat.Fingerprint()}, caps, desk.handle)
	d, c := dial(t, g, seat)
	t.Cleanup(func() { d.Close() })
	d.wt = fastTimes(nil)
	return d, c, g
}

// With wait-chat in the hello the one watcher sends the extended wait: the
// first carries 0 and is answered with the cursor, which the hint publishes
// with the connection it was learned on, and the next carries it. A chat
// change wakes the hint and not the Cards' state; a Card change and a chat
// change together publish both. Every gate call is a wait.
func TestSeatLinkWatchChatHint(t *testing.T) {
	desk := newChatWaitDesk(7)
	desk.cards = []ownerwire.SeatWaitCard{waitCard(1)}
	d, c, g := chatRig(t, desk, fullCaps)
	if !slices.Contains(Caps(), CapWaitChat) {
		t.Fatalf("the seat's hello lacks %q: %v", CapWaitChat, Caps())
	}
	if h := d.ChatHint(); h.Live {
		t.Fatalf("a hint before the watcher: %+v", h)
	}
	d.StartWatch()
	conn, _ := c.Connected()
	h := nextHint(t, d, "the first cursor", func(h ChatHint) bool { return h.Live })
	if h.Cursor != 7 || !h.Connection.Equal(conn) {
		t.Fatalf("the hint is %+v, want cursor 7 on %v", h, conn)
	}
	v1 := ownerwire.PendingVersion([]ownerwire.SeatWaitCard{waitCard(1)})
	next(t, d, "the cards", func(s WatchState) bool { return s.Version == v1 })
	<-desk.held

	cards := d.WatchChanged()
	desk.change(func(d *chatWaitDesk) { d.cursor = 9 })
	nextHint(t, d, "the moved cursor", func(h ChatHint) bool { return h.Cursor == 9 })
	select {
	case <-cards:
		t.Fatalf("a chat change woke the Cards' state: %+v", d.Watch())
	case <-time.After(50 * time.Millisecond):
	}
	<-desk.held

	desk.change(func(d *chatWaitDesk) {
		d.cursor = 11
		d.cards = append(d.cards, waitCard(2))
	})
	v2 := ownerwire.PendingVersion([]ownerwire.SeatWaitCard{waitCard(1), waitCard(2)})
	next(t, d, "the Card change", func(s WatchState) bool { return s.Version == v2 })
	nextHint(t, d, "the chat change", func(h ChatHint) bool { return h.Cursor == 11 })
	<-desk.held

	ps := desk.params()
	if len(ps) < 4 || ps[0].Chat == nil || *ps[0].Chat != 0 {
		t.Fatalf("the waits' params: %+v", ps)
	}
	var sent []int64
	for _, p := range ps {
		if p.Chat == nil {
			t.Fatalf("a plain wait on a gate that lists wait-chat: %+v", ps)
		}
		sent = append(sent, *p.Chat)
	}
	if !slices.Contains(sent, 7) || !slices.Contains(sent, 9) || sent[len(sent)-1] != 11 {
		t.Fatalf("the cursors sent: %v", sent)
	}
	if n := int(g.calls.Load()); n != len(ps) {
		t.Fatalf("the gate had %d calls for %d waits", n, len(ps))
	}
}

// A gate that does not list wait-chat (an older daemon), or lists it without
// chat, gets exactly the plain params, and the hint is never live.
func TestSeatLinkWatchChatNotAskedOfAGateWithout(t *testing.T) {
	for name, caps := range map[string][]string{
		"no wait-chat":         {"pair", "read", "watch", "chat"},
		"wait-chat, no chat":   {"pair", "read", "watch", "wait-chat"},
		"neither, a plain one": {"pair", "read", "watch"},
	} {
		t.Run(name, func(t *testing.T) {
			desk := newChatWaitDesk(7)
			d, _, _ := chatRig(t, desk, caps)
			d.StartWatch()
			next(t, d, "watching", func(s WatchState) bool { return s.Watching && s.Version != "" })
			<-desk.held
			for _, p := range desk.params() {
				if p.Chat != nil {
					t.Fatalf("the extended wait was sent: %+v", desk.params())
				}
			}
			if h := d.ChatHint(); h.Live {
				t.Fatalf("a hint from a plain wait: %+v", h)
			}
		})
	}
}

// A gate that lists wait-chat and refuses the extended params bad-params gets
// the plain wait for the rest of the connection, and the Cards are watched.
func TestSeatLinkWatchChatFallsBackToThePlainWait(t *testing.T) {
	desk := newChatWaitDesk(7)
	desk.refuseExt = true
	desk.cards = []ownerwire.SeatWaitCard{waitCard(3)}
	d, _, _ := chatRig(t, desk, fullCaps)
	d.StartWatch()
	v := ownerwire.PendingVersion([]ownerwire.SeatWaitCard{waitCard(3)})
	next(t, d, "the cards by the plain wait", func(s WatchState) bool { return s.Version == v })
	<-desk.held
	ps := desk.params()
	if len(ps) < 2 || ps[0].Since != "refused" {
		t.Fatalf("the waits: %+v", ps)
	}
	for _, p := range ps[1:] {
		if p.Chat != nil || p.Since == "refused" {
			t.Fatalf("after the refusal the extended wait was sent again: %+v", ps)
		}
	}
	if h := d.ChatHint(); h.Live {
		t.Fatalf("a hint after the refusal: %+v", h)
	}
}

// An answer with no cursor (the daemon's source failed) drops the hint: what
// a page held of the chat may be stale. The next wait carries 0, so the
// gate's next cursor is a fresh one.
func TestSeatLinkWatchChatAnswerWithoutACursorPurges(t *testing.T) {
	desk := newChatWaitDesk(7)
	d, _, _ := chatRig(t, desk, fullCaps)
	d.StartWatch()
	nextHint(t, d, "the cursor", func(h ChatHint) bool { return h.Live && h.Cursor == 7 })
	<-desk.held
	desk.change(func(d *chatWaitDesk) { d.cursor = 0; d.cards = []ownerwire.SeatWaitCard{waitCard(4)} })
	nextHint(t, d, "the hint dropped", func(h ChatHint) bool { return !h.Live })
	if h := d.ChatHint(); h != (ChatHint{}) {
		t.Fatalf("the dropped hint is %+v", h)
	}
	<-desk.held
	ps := desk.params()
	if last := ps[len(ps)-1]; last.Chat == nil || *last.Chat != 0 {
		t.Fatalf("the wait after the drop carries %+v", last)
	}
	desk.change(func(d *chatWaitDesk) { d.cursor = 8 })
	nextHint(t, d, "a fresh cursor", func(h ChatHint) bool { return h.Live && h.Cursor == 8 })
}

// A lost link drops the hint at once; a new connection never carries the
// cursor of the last one: its first wait carries 0, and the hint names the
// new connection. A gate that comes back without wait-chat (a downgrade)
// leaves the hint not live and gets the plain wait.
func TestSeatLinkWatchChatReconnectAndDowngrade(t *testing.T) {
	desk := newChatWaitDesk(7)
	seat := newIdentity(t, "seat")
	g := &restartable{t: t, id: newIdentity(t, "gate"), pins: []string{seat.Fingerprint()}, caps: fullCaps, h: desk.handle}
	g.start()
	t.Cleanup(g.stop)
	d, c := g.link(t, seat)
	d.wt = fastTimes(nil)
	d.StartWatch()
	first := nextHint(t, d, "the cursor", func(h ChatHint) bool { return h.Live && h.Cursor == 7 })
	<-desk.held

	g.stop()
	nextHint(t, d, "the hint dropped with the link", func(h ChatHint) bool { return !h.Live })
	n := len(desk.params())
	g.start()
	second := nextHint(t, d, "a cursor of the new connection", func(h ChatHint) bool { return h.Live })
	conn, _ := c.Connected()
	if second.Connection.Equal(first.Connection) || !second.Connection.Equal(conn) {
		t.Fatalf("the hint's connection %v, first %v, now %v", second.Connection, first.Connection, conn)
	}
	ps := desk.params()
	if p := ps[n]; p.Chat == nil || *p.Chat != 0 {
		t.Fatalf("the first wait of the new connection carried %+v, not 0", p)
	}
	<-desk.held

	g.stop()
	nextHint(t, d, "dropped again", func(h ChatHint) bool { return !h.Live })
	g.mu.Lock()
	g.caps = []string{"pair", "read", "watch", "chat"}
	g.mu.Unlock()
	n = len(desk.params())
	g.start()
	next(t, d, "watching again", func(s WatchState) bool { return !s.Offline && s.Watching })
	eventually(t, "a wait on the downgraded gate", func() bool { return len(desk.params()) > n })
	for _, p := range desk.params()[n:] {
		if p.Chat != nil {
			t.Fatalf("the extended wait was sent to a gate that no longer lists it: %+v", p)
		}
	}
	if h := d.ChatHint(); h.Live {
		t.Fatalf("a hint on the downgraded gate: %+v", h)
	}
}

// Close drops the hint as it drops the Cards' state.
func TestSeatLinkWatchChatClose(t *testing.T) {
	desk := newChatWaitDesk(7)
	d, _, _ := chatRig(t, desk, fullCaps)
	d.StartWatch()
	nextHint(t, d, "the cursor", func(h ChatHint) bool { return h.Live })
	ch := d.ChatHintChanged()
	d.Close()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not wake the hint's subscribers")
	}
	if h := d.ChatHint(); h.Live {
		t.Fatalf("a hint after Close: %+v", h)
	}
}
