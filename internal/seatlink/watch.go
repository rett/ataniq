package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// CapWatch is the capability of the long wait for a change of the pending
// cards, seat.wait (the gate's word for it; the gate lists it in its hello
// when its service can wait).
const CapWatch = "watch"

// The watcher's timings (docs/design/seat-gate.md, live refresh).
const (
	// WatchHold is the longest the gate holds a seat.wait: the daemon's
	// hold. The room of the waits (waitRoom) is sized on it.
	WatchHold = 25 * time.Second
	// watchCall is the context of one seat.wait: the hold, and 4 seconds
	// for the network. The gate cuts a request at 30 seconds.
	watchCall = 29 * time.Second
	// watchCoalesce is the least time between a change's answer and the
	// next wait, so that a burst of changes is one wake after another
	// rather than a wait for each.
	watchCoalesce = 2 * time.Second
	// watchBackMin and watchBackMax bound the wait after a gate error or a
	// lost link: 1 second, doubled at each failure in a row, to 30 seconds.
	watchBackMin = time.Second
	watchBackMax = 30 * time.Second
	// watchFullBack is the wait after the gate answered wait-full: it holds
	// as many waits for the seat as it allows.
	watchFullBack = 5 * time.Second
	// watchIdle is how often the watcher looks at the client when there is
	// nothing to wait on: a gate that is not connected, or one that does not
	// list "watch". The look is local and sends nothing.
	watchIdle = time.Second
)

// WatchCard is one pending card in the watcher's state: what tells one card
// from another, never card text. It is the app's type, so that the link's
// method set is the app's [seatapp.Watcher].
type WatchCard = seatapp.WatchCard

// WatchState is where the pending cards stand, as the watcher last learned
// from the gate: its fields are described at [seatapp.WatchState]. The zero
// state is a link that does not watch.
type WatchState = seatapp.WatchState

// watchSame reports whether two states say the same, At aside.
func watchSame(s, o WatchState) bool {
	return s.Watching == o.Watching && s.Version == o.Version &&
		s.Waiting == o.Waiting && s.More == o.More && s.Offline == o.Offline &&
		slices.Equal(s.Cards, o.Cards)
}

// WatchPort is the method set the link gives the seat app for the live
// refresh: the state, and a notice that it changed.
type WatchPort interface {
	// Watch returns a snapshot of the state. It never blocks on the gate.
	Watch() WatchState
	// WatchChanged returns a channel that is closed when the state next
	// changes (and replaced then, so each call gets the next one). To miss
	// no change, take the channel first and the state after:
	//
	//	ch := p.WatchChanged()
	//	s := p.Watch()
	//	select { case <-ch: ... }
	//
	// A port that does not watch returns a channel that is never closed.
	WatchChanged() <-chan struct{}
}

var (
	_ WatchPort       = (*Daemon)(nil)
	_ seatapp.Watcher = (*Daemon)(nil)
)

// watchHub holds the published state and the broadcast. Its zero value is a
// state that does not watch, with a channel that is never closed. Publishing
// never blocks and starts no goroutine: it closes the channel and puts a new
// one, under the mutex, and every subscriber that selects on the old one
// wakes by itself.
type watchHub struct {
	mu sync.Mutex
	st WatchState
	ch chan struct{}
}

// snapshot returns the state with its own copy of the cards.
func (h *watchHub) snapshot() WatchState {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.st
	s.Cards = slices.Clone(s.Cards)
	return s
}

// changed returns the channel the next change closes.
func (h *watchHub) changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ch == nil {
		h.ch = make(chan struct{})
	}
	return h.ch
}

// update applies f to a copy of the state and, when the result says
// something other than the state did, publishes it at time at and wakes the
// subscribers. It reports whether it published.
func (h *watchHub) update(at time.Time, f func(*WatchState)) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := h.st
	f(&next)
	if watchSame(next, h.st) {
		return false
	}
	next.At = at
	h.st = next
	if h.ch != nil {
		close(h.ch)
	}
	h.ch = make(chan struct{})
	return true
}

// Watch returns a snapshot of where the pending cards stand. See
// [WatchPort].
func (d *Daemon) Watch() WatchState { return d.hub.snapshot() }

// WatchChanged returns the channel that is closed when the state next
// changes. See [WatchPort].
func (d *Daemon) WatchChanged() <-chan struct{} { return d.hub.changed() }

// watchTimes are the watcher's timings; the zero value is the constants
// above. Tests set them before StartWatch.
type watchTimes struct {
	call, coalesce, backMin, backMax, full, idle time.Duration
	// sleep waits for d or until ctx is done, returning ctx's error then.
	sleep func(ctx context.Context, d time.Duration) error
	// now is the clock of the state's At.
	now func() time.Time
	// beforeSend, a test's hook, runs in a wait after its request is made
	// for the session named by conn, before the last check of the
	// connection and the call.
	beforeSend func(conn time.Time)
}

func (t watchTimes) withDefaults() watchTimes {
	d := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	d(&t.call, watchCall)
	d(&t.coalesce, watchCoalesce)
	d(&t.backMin, watchBackMin)
	d(&t.backMax, watchBackMax)
	d(&t.full, watchFullBack)
	d(&t.idle, watchIdle)
	if t.sleep == nil {
		t.sleep = sleepFor
	}
	if t.now == nil {
		t.now = time.Now
	}
	return t
}

func sleepFor(ctx context.Context, d time.Duration) error {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// errWatchAnswer is the error of an answer to seat.wait that is not of its
// form. It holds nothing of the answer.
var errWatchAnswer = errors.New("seatlink: the gate's answer to the wait is not of its form")

// watcher is the link's one goroutine for the live refresh: it keeps one
// seat.wait in flight at the gate, and publishes what each answer says.
type watcher struct {
	d      *Daemon
	t      watchTimes
	cancel context.CancelFunc
	done   chan struct{}
	// linkLost is whether Offline was published for a lost link (rather
	// than for a failed call), so that it is lifted when the link is back
	// without waiting for the answer of a wait the gate holds. Only the
	// goroutine reads it.
	linkLost bool
	// chat is what the watcher keeps of the chat cursor (watchchat.go).
	chat watchChat
}

// StartWatch starts the watcher, once; a second call, or a call after
// [Daemon.Close], does nothing. The watcher is the link's one goroutine: it
// asks nothing of the gate and spends no room until the gate's hello lists
// "watch", and with no hello that does, [Daemon.Watch] says it does not
// watch. It keeps one seat.wait in flight (29 seconds' context) with the
// version of its last answer as since, spends the room of kindWait on each,
// waits at least 2 seconds after a change so that a burst is one wake after
// another, and with no room sleeps until room frees. On a gate error or a
// lost link, or a call that fails without an answer of the gate's (a timeout, a
// bad answer), it publishes offline; after a refusal the gate answers with a
// code (the link is up) it publishes no offline and lifts one published
// before. It tries again after 1 second, doubled at each failure in a row to
// 30 seconds, keeping its version, and after wait-full it waits 5 seconds. It
// runs with no browser open.
func (d *Daemon) StartWatch() {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if d.w != nil || d.wclosed {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{d: d, t: d.wt.withDefaults(), cancel: cancel, done: make(chan struct{})}
	d.w = w
	go w.run(ctx)
}

// Close stops the watcher and waits for its goroutine to end, so nothing of
// the link is left running; the state then says it does not watch, and
// subscribers wake. It does not close the client, which the caller owns. It
// is safe to call more than once and at any time.
func (d *Daemon) Close() error {
	d.wmu.Lock()
	d.wclosed = true
	w := d.w
	d.wmu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
	d.hub.update(d.wt.withDefaults().now(), func(s *WatchState) { *s = WatchState{} })
	d.chub.set(ChatHint{})
	return nil
}

func (w *watcher) run(ctx context.Context) {
	defer close(w.done)
	d := w.d
	since := ""
	back := w.t.backMin
	for ctx.Err() == nil {
		sess, ok := d.session()
		if !ok {
			w.purgeChat()
			w.linkLost = true
			w.publish(func(s *WatchState) { s.Watching, s.Offline = d.offers(CapWatch), true })
			if w.t.sleep(ctx, w.t.idle) != nil {
				return
			}
			continue
		}
		if !sess.offers(CapWatch) {
			w.purgeChat()
			w.linkLost = false
			w.publish(func(s *WatchState) { *s = WatchState{} })
			if w.t.sleep(ctx, w.t.idle) != nil {
				return
			}
			continue
		}
		w.publish(func(s *WatchState) {
			s.Watching = true
			if w.linkLost {
				s.Offline = false
			}
		})
		w.linkLost = false
		ext := w.chatFor(sess)
		if !d.still(sess) {
			// The connection changed since it was read: start over on the
			// new one, before spending room.
			w.purgeChat()
			continue
		}
		ok, wait := d.budget.takeOrWait(kindWait)
		if !ok {
			if w.pause(ctx, wait) != nil {
				return
			}
			continue
		}
		res, err := w.wait(ctx, since, ext, sess)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errSessionGone) {
			// Nothing was sent: the wait is asked again on the new
			// connection, with what the watcher holds of it.
			w.purgeChat()
			continue
		}
		if err != nil {
			// A wait that failed says nothing of the chat: what was kept
			// of it may be stale.
			w.purgeChat()
			var re *seatgate.RemoteError
			if errors.As(err, &re) {
				if ext && re.Code == codeBadParams {
					// A gate that lists the extension but refuses its
					// params: the plain wait for the rest of this
					// connection.
					w.chat.refused, w.chat.refusedC = true, sess.conn
				}
				// The gate answered, with a code: the link is up, so
				// whatever offline was published for an earlier failure of
				// the link is lifted now, not when a wait is next held and
				// answered. A coded error says nothing of the link.
				w.linkLost = false
				w.publish(func(s *WatchState) {
					s.Watching = true
					s.Offline = false
				})
				pause := w.t.full
				if re.Code != ownerwire.CodeWaitFull {
					// Another refusal is a fault: the pause doubles.
					pause = back
					if back *= 2; back > w.t.backMax {
						back = w.t.backMax
					}
				}
				if w.t.sleep(ctx, pause) != nil {
					return
				}
				continue
			}
			w.linkLost = errors.Is(err, seatgate.ErrDisconnected)
			w.publish(func(s *WatchState) { s.Watching, s.Offline = true, true })
			if w.t.sleep(ctx, back) != nil {
				return
			}
			if back *= 2; back > w.t.backMax {
				back = w.t.backMax
			}
			continue
		}
		back = w.t.backMin
		changed := res.Version != since
		since = res.Version
		if ext && w.takeChat(sess.conn, res.Chat) {
			changed = true
		}
		cards := make([]WatchCard, len(res.Cards))
		for i, c := range res.Cards {
			cards[i] = WatchCard{ID: c.ID, Kind: c.Kind, BindingSHA256: c.BindingSHA256, Held: c.Held}
		}
		w.publish(func(s *WatchState) {
			*s = WatchState{Watching: true, Version: res.Version, Waiting: res.Waiting, Cards: cards, More: res.More}
		})
		if changed && w.pause(ctx, w.t.coalesce) != nil {
			return
		}
	}
}

// errSessionGone says the client's connection changed between the
// session the wait was made for and its sending: nothing was sent.
var errSessionGone = errors.New("seatlink: the connection changed before the wait was sent")

// wait sends one seat.wait with since and reads the answer: the plain one,
// or with ext the extended one, which carries the chat cursor the watcher
// holds of the session sess (0 for none). Each form's answer is read with its
// own decoder, so a plain answer that carries chat is not of its form. Just
// before it sends, it checks that the client's connection is still sess's,
// and sends nothing ([errSessionGone]) when it is not.
func (w *watcher) wait(ctx context.Context, since string, ext bool, sess session) (ownerwire.SeatWaitResult, error) {
	encode, decode := ownerwire.EncodeSeatWaitParams, ownerwire.DecodeSeatWaitResult
	p := ownerwire.SeatWaitParams{Since: since}
	if ext {
		encode, decode = ownerwire.EncodeSeatWaitChatParams, ownerwire.DecodeSeatWaitChatResult
		var cursor int64
		if w.chat.conn.Equal(sess.conn) {
			cursor = w.chat.cursor
		}
		p.Chat = &cursor
	}
	params, err := encode(p)
	if err != nil {
		return ownerwire.SeatWaitResult{}, errFailed
	}
	if w.t.beforeSend != nil {
		w.t.beforeSend(sess.conn)
	}
	if !w.d.still(sess) {
		return ownerwire.SeatWaitResult{}, errSessionGone
	}
	cctx, cancel := context.WithTimeout(ctx, w.t.call)
	defer cancel()
	var raw json.RawMessage
	if err := w.d.c.Call(cctx, ownerwire.OpSeatWait, json.RawMessage(params), &raw); err != nil {
		return ownerwire.SeatWaitResult{}, err
	}
	res, err := decode(raw)
	if err != nil {
		return ownerwire.SeatWaitResult{}, errWatchAnswer
	}
	return res, nil
}

// publish applies f to the state and wakes the subscribers if it changed.
func (w *watcher) publish(f func(*WatchState)) {
	w.d.hub.update(w.t.now(), f)
}
