package seatlink

import (
	"sync"
	"time"
)

// This file is the seat's side of the extended seat.wait: the chat cursor the
// one watcher learns beside the Card version, and the notice that it changed
// (docs/design/seat-gate.md, "Chat hints on the wait"). The watcher asks for
// it only when the gate's hello lists [CapWaitChat] (and chat); it then sends
// the extended params on the same wait it always sends, so the hint costs no
// call of the gate and no room beyond the waits' 5. A gate that does not list
// it gets exactly the plain params, and a gate that lists it but refuses the
// extended params bad-params gets the plain wait for the rest of that
// connection.
//
// The hint is a cursor, never content: a page that is told the chat moved
// reads the directory and the conversation again through seat.chat, where the
// desk authorizes every read live. The cursor covers the leads whose chat the
// desk switched on and nothing else, so the seat never learns of a lead whose
// chat is off.
//
// What the watcher keeps of the chat is dropped, and the hint published as
// not live, whenever it may be stale: on a lost link, on a new connection
// (another connection's cursor is never sent or shown), when the gate no
// longer lists the capability, when an answer carries no cursor (the
// daemon's source failed), and after any failed or refused wait. The next
// extended wait then carries 0, and the gate answers it at once with a fresh
// cursor: one fresh authorized view.

// CapWaitChat is the capability of the extended seat.wait: the gate's hello
// lists it, after chat, when the wait can carry the chat cursor.
const CapWaitChat = "wait-chat"

// codeBadParams is the gate's word for params it does not take; a gate that
// lists the extension and answers it to the extended params gets the plain
// wait from then on, on that connection.
const codeBadParams = "bad-params"

// ChatHint is where the leads' chat stands, as the watcher last learned it
// from the extended wait. The zero hint is not live.
type ChatHint struct {
	// Live is true when the watcher holds a cursor of the current
	// connection, learned from the gate's last answer.
	Live bool
	// Cursor is that cursor, 1 to 2^53-1, a number a page's script can
	// carry; 0 when not Live. It changes when what the seat may read of the
	// chat changes, and says nothing else: no lead, no count, no text.
	Cursor int64
	// Connection is the connection the cursor was learned on, as
	// [seatapp.ChatView].Connection names it; zero when not Live.
	Connection time.Time
}

// chatHub holds the published hint and its broadcast, as watchHub does for
// the Cards. Its zero value is a hint that is not live, with a channel that is
// never closed until it changes.
type chatHub struct {
	mu sync.Mutex
	h  ChatHint
	ch chan struct{}
}

func (c *chatHub) snapshot() ChatHint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.h
}

func (c *chatHub) changed() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ch == nil {
		c.ch = make(chan struct{})
	}
	return c.ch
}

// set publishes h when it differs from the hint, and wakes the subscribers.
func (c *chatHub) set(h ChatHint) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h == c.h {
		return false
	}
	c.h = h
	if c.ch != nil {
		close(c.ch)
	}
	c.ch = make(chan struct{})
	return true
}

// ChatHint returns a snapshot of the chat hint. It never blocks on the gate.
func (d *Daemon) ChatHint() ChatHint { return d.chub.snapshot() }

// ChatHintChanged returns a channel that is closed when the chat hint next
// changes, beside [Daemon.WatchChanged], which is the Cards'. Take the channel
// first and the hint after, to miss no change. A hint that goes from live to
// not live is a change too: what a page held of the chat may be stale.
func (d *Daemon) ChatHintChanged() <-chan struct{} { return d.chub.changed() }

// watchChat is what the watcher keeps of the chat: the cursor it sends, the
// connection it was learned on, and whether that connection refused the
// extended params. Only the watcher's goroutine touches it.
type watchChat struct {
	cursor   int64
	conn     time.Time
	refused  bool
	refusedC time.Time
}

// purge drops the cursor and publishes the hint as not live.
func (w *watcher) purgeChat() {
	w.chat.cursor = 0
	w.d.chub.set(ChatHint{})
}

// chatFor returns whether the next wait is the extended one, on the
// connection conn: the gate lists wait-chat and chat, and this connection
// has not refused the extended params. A new connection drops the cursor of
// the last one.
func (w *watcher) chatFor(conn time.Time) bool {
	if !w.chat.conn.Equal(conn) {
		w.chat.conn = conn
		w.purgeChat()
	}
	if w.chat.refused && !w.chat.refusedC.Equal(conn) {
		w.chat.refused = false
	}
	ext := w.d.offers(CapWaitChat) && w.d.offers(CapChat) && !w.chat.refused
	if !ext && w.chat.cursor != 0 {
		w.purgeChat()
	}
	if !ext && w.d.chub.snapshot().Live {
		w.d.chub.set(ChatHint{})
	}
	return ext
}

// takeChat takes the cursor of an extended wait's answer, given on the
// connection conn, and reports whether it changed. The cursor is kept and
// published only while that connection is still the client's; an answer with
// no cursor, or one that came on a connection since replaced, drops it.
func (w *watcher) takeChat(conn time.Time, chat *int64) bool {
	now, ok := w.d.c.Connected()
	if chat == nil || !ok || !now.Equal(conn) {
		w.purgeChat()
		return false
	}
	changed := *chat != w.chat.cursor
	w.chat.cursor = *chat
	w.d.chub.set(ChatHint{Live: true, Cursor: *chat, Connection: conn})
	return changed
}
