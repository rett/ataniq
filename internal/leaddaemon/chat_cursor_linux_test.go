//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leadchat"
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
	// The daemon says it listens before it binds the port.
	waitFor(t, 10*time.Second, "the port is bound", func() bool { return port.src.Load() != nil })
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
	requireTestClock(t, port)
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

// A drop moves the cursor, and closes the broadcast, exactly when it writes
// an entry: the first notice of a drop is the lead's new head, with no reply
// taken; a drop within the minute of that notice writes nothing, and says
// nothing.
func TestLeadChatCursorMovesForADropNoticeOnly(t *testing.T) {
	l, port := newChatEnv(t)
	l.start()
	l.up("alpha", submitted)
	l.mustChatSet("alpha", leadwire.ChatRead)
	out := l.outboxOf("alpha")
	before := cursorOf(t, port)
	ch := port.ChatCursorChanged()
	reply(t, out, "0001-long.txt", strings.Repeat("x", leadchat.MaxReplyBytes+1))
	waitFor(t, 15*time.Second, "the first drop", func() bool { return l.chatRow("alpha").Dropped["too-long"] == 1 })
	waitFor(t, 5*time.Second, "the broadcast of the drop's notice", func() bool { return closed(ch) })
	noticed := cursorOf(t, port)
	if noticed == before || l.chatRow("alpha").Entries != 2 {
		t.Fatalf("the drop's notice: the cursor %d (was %d), %d entries", noticed, before, l.chatRow("alpha").Entries)
	}

	ch = port.ChatCursorChanged()
	reply(t, out, "0002-long.txt", strings.Repeat("y", leadchat.MaxReplyBytes+1))
	waitFor(t, 15*time.Second, "the second drop", func() bool { return l.chatRow("alpha").Dropped["too-long"] == 2 })
	time.Sleep(5 * l.scratch.ChatScanEvery)
	if closed(ch) {
		t.Fatal("a drop that wrote no entry closed the broadcast")
	}
	if c := cursorOf(t, port); c != noticed || l.chatRow("alpha").Entries != 2 {
		t.Fatalf("a drop that wrote no entry: the cursor %d (was %d), %d entries", c, noticed, l.chatRow("alpha").Entries)
	}
}

// rowOf is the seat's row of the lead key.
func rowOf(t *testing.T, port *LeadChat, key string) ownerwire.ChatLeadRow {
	t.Helper()
	for _, r := range seatLeads(t, port) {
		if r.Lead == "daemon:"+key {
			return r
		}
	}
	t.Fatalf("the seat lists no %q", key)
	return ownerwire.ChatLeadRow{}
}

// storeHead is the head of the lead's chat store.
func storeHead(t *testing.T, d *chatDesk, key string) int64 {
	t.Helper()
	cl := d.byKey[key]
	cl.mu.Lock()
	defer cl.mu.Unlock()
	st, err := cl.store.Status()
	if err != nil {
		t.Fatal(err)
	}
	return st.Head
}

// A scan admitted before a turn-off is published (the lead read when it was
// admitted) ingests and signals before the seat can see the lead off: the
// turn-off waits for it. Once the seat sees the lead off, no scan is
// admitted, so a reply written while the lead is off moves no head, and the
// next turn-on's head is the one the seat could last see, plus its turned-on
// entry. The same run with no scan in flight gives the same heads.
func TestChatScanAdmittedBeforeATurnOffEndsBeforeTheOffIsSeen(t *testing.T) {
	for _, paused := range []bool{false, true} {
		name := "no scan in flight"
		if paused {
			name = "a scan paused at its admission"
		}
		t.Run(name, func(t *testing.T) {
			l, port := newChatEnv(t)
			l.start()
			l.up("alpha", submitted)
			requireTestClock(t, port)
			d := deskOf(t, port)
			var armed atomic.Bool
			reached := make(chan struct{})
			release := make(chan struct{})
			d.atScan = func(cl *chatLead) {
				if cl.key == "alpha" && armed.CompareAndSwap(true, false) {
					close(reached)
					<-release
				}
			}
			l.mustChatSet("alpha", leadwire.ChatRead)
			out := l.outboxOf("alpha")
			reply(t, out, "0001-first.txt", "first")
			waitFor(t, 15*time.Second, "the first reply is taken", func() bool { return l.chatRow("alpha").Entries == 2 })

			var offDone chan struct{}
			if paused {
				armed.Store(true)
				<-reached
				// The scan is admitted and holds the lead's change lock; a
				// reply reaches the outbox now, and is settled for the
				// scan that is released (the test clock), so the turn-off
				// waits for the scan and not for the reply's age.
				reply(t, out, "0002-second.txt", "second")
				offDone = make(chan struct{})
				go func() {
					defer close(offDone)
					l.mustChatSet("alpha", leadwire.ChatOff)
				}()
				time.Sleep(10 * l.scratch.ChatScanEvery)
				select {
				case <-offDone:
					t.Fatal("the turn-off returned while an admitted scan had not ingested")
				default:
				}
				if r := rowOf(t, port, "alpha"); r.Chat != ownerwire.ChatRead {
					t.Fatalf("the seat sees the lead %s while an admitted scan has not ingested", r.Chat)
				}
				close(release)
				<-offDone
			} else {
				reply(t, out, "0002-second.txt", "second")
				waitFor(t, 15*time.Second, "the second reply is taken", func() bool { return l.chatRow("alpha").Entries == 3 })
				l.mustChatSet("alpha", leadwire.ChatOff)
			}
			if r := rowOf(t, port, "alpha"); r.Chat != ownerwire.ChatOff {
				t.Fatalf("after the turn-off the seat sees %s", r.Chat)
			}
			// The head the seat could last see: the first reply's entry, the
			// turned-on entry before it, and the second reply, which the
			// admitted scan took while the lead was read.
			offHead := storeHead(t, d, "alpha")
			if offHead != 3 {
				t.Fatalf("the head at the turn-off is %d, want 3", offHead)
			}

			// While the lead is off a reply moves nothing.
			reply(t, out, "0003-while-off.txt", "while off")
			time.Sleep(10 * l.scratch.ChatScanEvery)
			if h := storeHead(t, d, "alpha"); h != offHead {
				t.Fatalf("a reply written while the lead is off moved the head from %d to %d", offHead, h)
			}
			r := l.mustChatSet("alpha", leadwire.ChatRead)
			if r.Head != offHead+1 {
				t.Fatalf("the next turn-on's head is %d, want %d: the head the seat last saw and the turned-on entry", r.Head, offHead+1)
			}
		})
	}
}

// The cursor samples the leads as one state of the desk: a handoff (alpha
// turned off, then beta turned on) that comes while a cursor read has
// sampled alpha and not yet beta waits for the read, which answers the set
// as it was before the handoff, never alpha and beta both on.
func TestLeadChatCursorIsOneStateOfTheDesk(t *testing.T) {
	l, port := newChatEnv(t)
	l.start()
	l.up("alpha", submitted)
	l.up("beta", submitted)
	d := deskOf(t, port)
	ra := l.mustChatSet("alpha", leadwire.ChatRead)
	gen := func(key string) int64 {
		cl := d.byKey[key]
		cl.mu.Lock()
		defer cl.mu.Unlock()
		st, err := cl.store.Status()
		if err != nil {
			t.Fatal(err)
		}
		return st.Gen
	}
	alphaOn := ownerwire.ChatCursorLead{Lead: "daemon:alpha", Gen: gen("alpha"), Head: ra.Head}
	before := cursorFor(t, port, []ownerwire.ChatCursorLead{alphaOn})

	var armed atomic.Bool
	reached := make(chan struct{})
	release := make(chan struct{})
	d.atCursor = func(cl *chatLead) {
		if cl.key == "beta" && armed.CompareAndSwap(true, false) {
			close(reached)
			<-release
		}
	}
	armed.Store(true)
	type answer struct {
		v   int64
		err error
	}
	got := make(chan answer, 1)
	go func() {
		v, err := port.ChatCursor(context.Background())
		got <- answer{v, err}
	}()
	<-reached
	handoff := make(chan struct{})
	go func() {
		defer close(handoff)
		l.mustChatSet("alpha", leadwire.ChatOff)
		l.mustChatSet("beta", leadwire.ChatRead)
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-handoff:
		t.Fatal("the handoff came between two samples of one cursor read")
	default:
	}
	close(release)
	a := <-got
	<-handoff
	if a.err != nil || a.v != before {
		t.Fatalf("the read during the handoff answered %d (%v), want the set before it %d", a.v, a.err, before)
	}
	rb := l.chatRow("beta")
	after := cursorFor(t, port, []ownerwire.ChatCursorLead{{Lead: "daemon:beta", Gen: gen("beta"), Head: rb.Head}})
	if c := cursorOf(t, port); c != after {
		t.Fatalf("after the handoff the cursor is %d, want beta's alone %d", c, after)
	}
	both := cursorFor(t, port, []ownerwire.ChatCursorLead{alphaOn, {Lead: "daemon:beta", Gen: gen("beta"), Head: rb.Head}})
	if a.v == both {
		t.Fatal("the read answered alpha and beta both on, a set the desk never held")
	}
}

// A cursor read that is under way when the port is unbound, or bound to
// another source, discards its sample: it answers no cursor, or the new
// binding's, never the old binding's.
func TestLeadChatCursorDiscardsASampleOfAReplacedBinding(t *testing.T) {
	for _, unbind := range []bool{true, false} {
		name := "a new source"
		if unbind {
			name = "unbound"
		}
		t.Run(name, func(t *testing.T) {
			c := NewLeadChat()
			src := &blockingSource{reached: make(chan struct{}), release: make(chan struct{})}
			c.bind(src)
			old := cursorFor(t, c, []ownerwire.ChatCursorLead{{Lead: "daemon:alpha", Gen: 1, Head: 2}})
			type answer struct {
				v   int64
				err error
			}
			got := make(chan answer, 1)
			go func() {
				v, err := c.ChatCursor(context.Background())
				got <- answer{v, err}
			}()
			<-src.reached
			next := &fakeSource{}
			if unbind {
				c.bind(nil)
			} else {
				c.bind(next)
			}
			close(src.release)
			a := <-got
			switch {
			case unbind && a.err == nil:
				t.Fatalf("a read across an unbind answered %d", a.v)
			case !unbind && (a.err != nil || a.v != cursorFor(t, c, []ownerwire.ChatCursorLead{{Lead: "daemon:alpha", Gen: 1, Head: 2}})):
				t.Fatalf("a read across a new binding answered %d (%v), want the new binding's", a.v, a.err)
			}
			if a.err == nil && a.v == old {
				t.Fatal("the read answered the old binding's cursor")
			}
		})
	}
}

// blockingSource is a source whose first cursor sample stops until it is
// released.
type blockingSource struct {
	fakeSource
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func (b *blockingSource) seatCursor() ([]ownerwire.ChatCursorLead, error) {
	b.once.Do(func() {
		close(b.reached)
		<-b.release
	})
	return b.fakeSource.seatCursor()
}
