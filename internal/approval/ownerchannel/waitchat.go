package ownerchannel

import (
	"context"
	"time"

	"ataniq/internal/approval/ownerwire"
)

// This file is the daemon's side of the extended seat.wait: the wait of
// wait.go, which also answers when the chat cursor of the leads the desk
// switched on moved (ownerwire.ChatCursor). The cursor is the chat port's,
// read through [ChatCursor]; the service holds no chat of its own and reads
// nothing of a lead.
//
// What the extension changes, and what it does not:
//
//   - The Card side is the wait of wait.go: the same pokes, the same looks at
//     the door under the same one-second floor, the same version. A change of
//     the chat makes no look at the door: an answer for the chat carries the
//     Card result of the last look, which no poke has made stale. So the chat
//     cannot change when a Card change is noticed.
//   - The cursor is sampled when the wait is armed, after every wake of the
//     port's (ChatCursorChanged) and when the chat's gap ends, and again just
//     before every answer for the Cards. A sample is validated: the port's
//     change channel is taken first, the cursor read after, and a sample after
//     which the channel closed is taken again (see chatSource.sample). An
//     answer for the chat carries a sample after which the channel has not
//     closed, so an answer carries the cursor of the leads enabled at that
//     moment: a lead turned off meanwhile is not in it, and whatever its
//     activity was leaves no trace.
//   - A chat change answers no sooner than [WaitChatGap] after the wait was
//     armed; a Card change answers at once, as before. A busy lead therefore
//     costs the seat no more waits than an idle one (one about every 20
//     seconds with the seat's 2-second pause), and never delays a Card. The
//     gap is pacing, not concealment: a first change after it is answered
//     when it is seen, so the seat learns about when a change it may read
//     came; it learns no count of changes from the timing.
//   - A wake for which the cursor did not change does nothing the seat or
//     the door can see: no answer, no look, and no timer set or stopped. The
//     timer of the next Card expiry is kept across chat wakes (and re-armed
//     only when a look finds another expiry), and the timer of the chat's
//     gap is set once, when the cursor first differs from the seat's, and
//     stopped only when it no longer does.
//   - A cursor that cannot be read (an error, a panic, a value out of its
//     range) is no cursor: the wait holds and answers on the Cards alone, and
//     its answer carries no chat. A port whose change channel cannot be taken
//     (ChatCursorChanged panics) is no port at all for the rest of the wait:
//     the plain wait's behaviour, with no chat in any answer. It fails closed.

// ChatCursor is the chat port's cursor, as the extended wait reads it. The
// daemon's chat ports (the trial's stand-in and the scratch leads' LeadChat)
// are the real ones; a test gives a fake.
//
// ChatCursor answers the cursor of the leads the seat may read now
// (ownerwire.ChatCursor over the desk-enabled leads alone), 1 to
// ownerwire.MaxChatSeq. ChatCursorChanged answers a channel that is closed
// when the cursor may next have changed, and replaced then; nil is a source
// that never says so, which is then read on the wait's other wakes only. A
// source must not wake for a lead whose chat is off; a wake that finds the
// cursor unchanged answers nothing in any case.
type ChatCursor interface {
	ChatCursor(ctx context.Context) (int64, error)
	ChatCursorChanged() <-chan struct{}
}

// WaitChatGap is the least time between the start of an extended wait and
// an answer it gives for a change of the chat alone.
const WaitChatGap = 18 * time.Second

// chatSampleTries is how many times a sample is taken while the port's
// channel keeps closing under it; then the wait has no cursor for that
// sample.
const chatSampleTries = 4

// WaitPendingChat answers the extended seat.wait: as [Service.WaitPending],
// and also when the chat cursor of src is not chat. since and the Card
// result are those of WaitPending; chat is the cursor the seat last saw, 0
// when it has none.
//
// It answers at once when since is empty or is not the pending set's version,
// or when chat is 0 and src gives a cursor (with that one sample: it is not
// read twice). Otherwise it holds the call until the version changes, until
// the cursor is not chat and [WaitChatGap] has passed since the call began,
// until the hold ends, or until ctx is done (an error and no result). Every
// answer carries a validated sample of the cursor taken just before it, in
// Chat, or none when src cannot give one. The bounds of WaitPending hold, and
// the two kinds of wait count together against [Config].WaitMax.
func (s *Service) WaitPendingChat(ctx context.Context, since string, chat int64, src ChatCursor) (ownerwire.SeatWaitResult, error) {
	return s.held(ctx, func(ctx context.Context) (ownerwire.SeatWaitResult, error) {
		return s.waitPendingChat(ctx, since, chat, src)
	})
}

// readChatCursor reads src's cursor; false for an error, a panic or a value
// out of its range.
func readChatCursor(ctx context.Context, src ChatCursor) (v int64, ok bool) {
	if src == nil {
		return 0, false
	}
	defer func() {
		if recover() != nil {
			v, ok = 0, false
		}
	}()
	v, err := src.ChatCursor(ctx)
	if err != nil || v < 1 || v > ownerwire.MaxChatSeq {
		return 0, false
	}
	return v, true
}

// chatSource is the extended wait's hold of a port. src is nil when no port
// was given, and becomes nil when the port's change channel could not be
// taken: a wait that cannot know when the cursor changes does not use it.
type chatSource struct {
	src ChatCursor
}

// changed is the port's change channel. live is false when there is no port
// or the call panicked, which drops the port for the rest of the wait; a nil
// channel with live true is a port that never says it changed.
func (c *chatSource) changed() (ch <-chan struct{}, live bool) {
	if c.src == nil {
		return nil, false
	}
	defer func() {
		if recover() != nil {
			c.src = nil
			ch, live = nil, false
		}
	}()
	return c.src.ChatCursorChanged(), true
}

// closed reports whether ch is closed; a nil channel never is.
func closed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// sample takes one validated sample: the change channel first, the cursor
// after, and the channel again, which must not have closed meanwhile (a
// sample whose channel closed is taken again, at most chatSampleTries
// times). ch is the channel of the sample, which the wait selects on; ok is
// false when there is no cursor (no port, a failure, or a cursor that kept
// changing while it was read).
func (c *chatSource) sample(ctx context.Context) (ch <-chan struct{}, v int64, ok bool) {
	for i := 0; i < chatSampleTries; i++ {
		var live bool
		ch, live = c.changed()
		if !live {
			return nil, 0, false
		}
		v, ok = readChatCursor(ctx, c.src)
		if !ok {
			return ch, 0, false
		}
		if !closed(ch) {
			return ch, v, true
		}
	}
	return ch, 0, false
}

// withChat is r with a sample of the cursor taken now, or with none.
func (c *chatSource) withChat(ctx context.Context, r ownerwire.SeatWaitResult) ownerwire.SeatWaitResult {
	r.Chat = nil
	if _, v, ok := c.sample(ctx); ok {
		r.Chat = &v
	}
	return r
}

// answerWith is r with the cursor v, or with none when ok is false.
func answerWith(r ownerwire.SeatWaitResult, v int64, ok bool) ownerwire.SeatWaitResult {
	r.Chat = nil
	if ok {
		r.Chat = &v
	}
	return r
}

func (s *Service) waitPendingChat(ctx context.Context, since string, chat int64, src ChatCursor) (ownerwire.SeatWaitResult, error) {
	w := &s.wait
	clk := w.clock()
	cs := &chatSource{src: src}
	due := clk.Now().Add(WaitChatGap)
	snap, _, err := s.lookNow(ctx, nil)
	if err != nil {
		return ownerwire.SeatWaitResult{}, err
	}
	if since == "" || snap.result.Version != since {
		return cs.withChat(ctx, snap.result), nil
	}
	// One sample decides whether the seat, which has no cursor, gets one at
	// once, and is that cursor.
	chatCh, cur, ok := cs.sample(ctx)
	if ok && chat == 0 {
		return answerWith(snap.result, cur, true), nil
	}
	hold := s.cfg.WaitHold
	if dl, has := ctx.Deadline(); has {
		if left := time.Until(dl) - waitMargin; left < hold {
			hold = left
		}
	}
	if hold <= 0 {
		return answerWith(snap.result, cur, ok), nil
	}
	endAt := clk.Now().Add(hold)
	end := clk.NewTimer(hold)
	defer end.Stop()

	// gapT is the timer of the chat's gap, set while the cursor is not the
	// seat's and the gap has not passed; expT is the timer of the next Card
	// expiry, armed for expAt. Neither is touched by a wake that changes
	// nothing.
	var gapT, expT waitTimer
	var expAt time.Time
	defer func() {
		for _, t := range []waitTimer{gapT, expT} {
			if t != nil {
				t.Stop()
			}
		}
	}()
	fresh := true // cur, ok and chatCh are a sample of this round
	for {
		ch, seq := w.wakeChan()
		if snap.seq != seq {
			// A poke came after the look began: look again, as the floor
			// allows. A hold that ends while the floor is waited out
			// answers what was last seen.
			next, ended, err := s.lookNow(ctx, end.C())
			if err != nil {
				return ownerwire.SeatWaitResult{}, err
			}
			if ended {
				return cs.withChat(ctx, snap.result), nil
			}
			snap = next
			if snap.result.Version != since {
				return cs.withChat(ctx, snap.result), nil
			}
			fresh = false
			continue
		}
		if !fresh && closed(chatCh) {
			chatCh, cur, ok = cs.sample(ctx)
			fresh = true
		}
		if ok && cur != chat {
			if !clk.Now().Before(due) {
				if !fresh && chatCh == nil {
					// A port that never says it changed is read again
					// before its cursor is answered.
					chatCh, cur, ok = cs.sample(ctx)
					fresh = true
					continue
				}
				// The sample's channel has not closed since it was taken:
				// the answer carries it, with the Card result of the last
				// look, which no poke has made stale.
				return answerWith(snap.result, cur, true), nil
			}
			if gapT == nil {
				gapT = clk.NewTimer(due.Sub(clk.Now()))
			}
		} else if gapT != nil {
			gapT.Stop()
			gapT = nil
		}
		// The next Card expiry, as the plain wait arms it, but kept while
		// the look's expiry is the same, and set only when it falls before
		// the end of the hold.
		var want time.Time
		if !snap.expiry.IsZero() {
			want = snap.expiry.Add(waitExpirySlack)
		}
		if expT != nil && !want.Equal(expAt) {
			expT.Stop()
			expT = nil
		}
		if expT == nil && !want.IsZero() {
			d := want.Sub(s.now())
			if d < 0 {
				d = 0
			}
			if clk.Now().Add(d).Before(endAt) {
				expT, expAt = clk.NewTimer(d), want
			}
		}
		var gapC, expC <-chan time.Time
		if gapT != nil {
			gapC = gapT.C()
		}
		if expT != nil {
			expC = expT.C()
		}
		fresh = false
		if w.parked != nil {
			w.parked()
		}
		select {
		case <-ch:
		case <-chatCh:
			// Only the cursor is sampled again: a chat change makes no
			// look at the door.
			chatCh, cur, ok = cs.sample(ctx)
			fresh = true
		case <-gapC:
			gapT = nil
			chatCh, cur, ok = cs.sample(ctx)
			fresh = true
		case <-expC:
			// The door ends the card by the clock and writes nothing the
			// service sees: say it may have changed, and look.
			expT = nil
			s.poke()
		case <-end.C():
			last, _, err := s.lookNow(ctx, nil)
			if err != nil {
				return ownerwire.SeatWaitResult{}, err
			}
			return cs.withChat(ctx, last.result), nil
		case <-ctx.Done():
			return ownerwire.SeatWaitResult{}, ctx.Err()
		}
	}
}
