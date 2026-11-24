//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leadwire"
)

// The cursor of a port that is nil, zero, new or unbound, or whose source
// panics, is no cursor: an error, so the extended wait answers without one.
// A bound port answers the source's cursor, and a bind or an unbind closes
// the broadcast.
func TestLeadChatCursorNeedsABoundSource(t *testing.T) {
	ctx := context.Background()
	var nilPort *LeadChat
	for name, c := range map[string]*LeadChat{"nil": nilPort, "zero": {}, "new": NewLeadChat()} {
		if _, err := c.ChatCursor(ctx); err == nil {
			t.Errorf("%s port: a cursor", name)
		}
	}
	c := NewLeadChat()
	ch := c.ChatCursorChanged()
	src := &fakeSource{}
	c.bind(src)
	select {
	case <-ch:
	default:
		t.Fatal("a bind did not close the broadcast")
	}
	v, err := c.ChatCursor(ctx)
	if want := cursorFor(t, c, []ownerwire.ChatCursorLead{{Lead: "daemon:alpha", Gen: 1, Head: 2}}); err != nil || v != want {
		t.Fatalf("the cursor is %d (%v), want %d", v, err, want)
	}
	src.mu.Lock()
	src.panic = true
	src.mu.Unlock()
	if _, err := c.ChatCursor(ctx); err == nil {
		t.Fatal("a panicking source gave a cursor")
	}
	ch = c.ChatCursorChanged()
	c.bind(nil)
	select {
	case <-ch:
	default:
		t.Fatal("an unbind did not close the broadcast")
	}
	if _, err := c.ChatCursor(ctx); err == nil {
		t.Fatal("an unbound port gave a cursor")
	}
	// A new binding has a new key: the same set gives another cursor, so
	// the seat cannot recognise a set across a bind.
	c.bind(src)
	src.mu.Lock()
	src.panic = false
	src.mu.Unlock()
	if again, err := c.ChatCursor(ctx); err != nil || again == v {
		t.Fatalf("a new binding gave %d (%v), the old one's %d", again, err, v)
	}
	// The cursor is not counted against the seat's calls.
	c.bind(&fakeSource{})
	for i := 0; i < 2*ChatCallsPerMinute; i++ {
		if _, err := c.ChatCursor(ctx); err != nil {
			t.Fatalf("cursor %d: %v", i, err)
		}
	}
	if _, err := c.Chat(ctx, leadsParams); err != nil {
		t.Fatalf("a seat call after the cursors: %v", err)
	}
}

// cursorFor is the cursor of leads under the key of port's binding.
func cursorFor(t *testing.T, port *LeadChat, leads []ownerwire.ChatCursorLead) int64 {
	t.Helper()
	b := port.src.Load()
	if b == nil {
		t.Fatal("the port is not bound")
	}
	return ownerwire.ChatCursor(b.key, leads)
}

func cursorOf(t *testing.T, port *LeadChat) int64 {
	t.Helper()
	v, err := port.ChatCursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// The cursor covers the leads whose chat is read and nothing else. A lead
// whose chat is off moves nothing: a reply it writes, and its being started,
// leave the cursor as it was and close no broadcast, whether no lead or
// another lead is on. Turning a lead on and off, a reply taken from a lead
// that is on, and a new segment each move it, and the broadcast says so.
func TestLeadChatCursorCoversTheEnabledLeadsOnly(t *testing.T) {
	l, port := newChatEnv(t)
	l.start()
	none := cursorFor(t, port, nil)
	if c := cursorOf(t, port); c != none {
		t.Fatalf("with every lead off the cursor is %d, want that of no lead %d", c, none)
	}
	ch0 := port.ChatCursorChanged()
	l.up("alpha", submitted)
	l.up("beta", submitted)
	if c := cursorOf(t, port); c != none || closed(ch0) {
		t.Fatalf("the start of leads whose chat is off moved the cursor (%d) or the broadcast", c)
	}

	// beta is off: its activity moves nothing.
	offActivity := func(what string, n int) {
		t.Helper()
		before := cursorOf(t, port)
		ch := port.ChatCursorChanged()
		out := l.outboxOf("beta")
		if err := os.MkdirAll(out, 0o700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			reply(t, out, filepath.Base(t.Name())+"-"+what+"-"+string(rune('a'+i))+".txt", "an off lead's reply")
		}
		time.Sleep(10 * l.scratch.ChatScanEvery)
		if c := cursorOf(t, port); c != before {
			t.Fatalf("%s: an off lead's activity moved the cursor from %d to %d", what, before, c)
		}
		if closed(ch) {
			t.Fatalf("%s: an off lead's activity closed the broadcast", what)
		}
	}
	offActivity("all off", 3)

	ch := port.ChatCursorChanged()
	r := l.mustChatSet("alpha", leadwire.ChatRead)
	if !closed(ch) {
		t.Fatal("a turn-on did not close the broadcast")
	}
	d := deskOf(t, port)
	gen := func() int64 {
		cl := d.byKey["alpha"]
		cl.mu.Lock()
		defer cl.mu.Unlock()
		st, err := cl.store.Status()
		if err != nil {
			t.Fatal(err)
		}
		return st.Gen
	}
	g1 := gen()
	on := cursorOf(t, port)
	if want := cursorFor(t, port, []ownerwire.ChatCursorLead{{Lead: "daemon:alpha", Gen: g1, Head: r.Head}}); on != want {
		t.Fatalf("with alpha on the cursor is %d, want %d", on, want)
	}
	offActivity("alpha on", 2)

	ch = port.ChatCursorChanged()
	reply(t, l.outboxOf("alpha"), "0001-done.txt", "done")
	waitFor(t, 15*time.Second, "the reply is taken", func() bool { return l.chatRow("alpha").Entries == 2 })
	waitFor(t, 5*time.Second, "the broadcast", func() bool { return closed(ch) })
	moved := cursorOf(t, port)
	if moved == on || moved != cursorFor(t, port, []ownerwire.ChatCursorLead{{Lead: "daemon:alpha", Gen: g1, Head: l.chatRow("alpha").Head}}) {
		t.Fatalf("a reply taken: the cursor is %d (was %d)", moved, on)
	}

	ch = port.ChatCursorChanged()
	l.mustChatSet("alpha", leadwire.ChatOff)
	if !closed(ch) {
		t.Fatal("a turn-off did not close the broadcast")
	}
	if c := cursorOf(t, port); c != none {
		t.Fatalf("after the turn-off the cursor is %d, want that of no lead %d: the old head left a trace", c, none)
	}
	offActivity("alpha off again", 2)

	l.mustChatSet("alpha", leadwire.ChatRead)
	if g2 := gen(); g2 == g1 {
		t.Fatal("a new turn-on kept the generation")
	}
	if c := cursorOf(t, port); c == on || c == moved || c == none {
		t.Fatalf("a new segment's cursor %d is an old one", c)
	}
}

// A lead whose chat a scan stopped is not in the cursor: the seat cannot read
// it.
func TestLeadChatCursorLeavesOutASuspendedLead(t *testing.T) {
	l, port := newChatEnv(t)
	l.start()
	l.up("alpha", submitted)
	l.mustChatSet("alpha", leadwire.ChatRead)
	ch := port.ChatCursorChanged()
	out := l.outboxOf("alpha")
	if err := os.Rename(out, out+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the chat is suspended", func() bool { return l.chatRow("alpha").Suspended })
	waitFor(t, 5*time.Second, "the broadcast", func() bool { return closed(ch) })
	if c := cursorOf(t, port); c != cursorFor(t, port, nil) {
		t.Fatalf("a suspended lead is in the cursor: %d", c)
	}
}
