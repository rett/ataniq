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
//   - The Card side is the wait of wait.go, unchanged: the same pokes, the
//     same looks at the door under the same one-second floor, the same
//     version. A change of the chat makes no look at the door: an answer for
//     the chat carries the Card result of the last look, which no poke has
//     made stale. So the chat cannot change when a Card change is noticed.
//   - The cursor is read when the wait is armed, on every wake, and again
//     just before every answer, so an answer carries the cursor of the leads
//     enabled at that moment: a lead turned off meanwhile is not in it, and
//     whatever its activity was leaves no trace.
//   - A chat change answers no sooner than [WaitChatGap] after the wait was
//     armed; a Card change answers at once, as before. A busy lead therefore
//     costs the seat no more waits than an idle one (one about every 20
//     seconds with the seat's 2-second pause), and never delays a Card.
//   - A cursor that cannot be read (an error, a panic, a value out of its
//     range) is no cursor: the wait holds and answers on the Cards alone, and
//     its answer carries no chat. It fails closed.

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

// WaitPendingChat answers the extended seat.wait: as [Service.WaitPending],
// and also when the chat cursor of src is not chat. since and the Card
// result are those of WaitPending; chat is the cursor the seat last saw, 0
// when it has none.
//
// It answers at once when since is empty or is not the pending set's version,
// or when chat is 0 and src gives a cursor. Otherwise it holds the call until
// the version changes, until the cursor is not chat and [WaitChatGap] has
// passed since the call began, until the hold ends, or until ctx is done
// (an error and no result). Every answer carries the cursor read just before
// it, in Chat, or none when src cannot give one. The bounds of WaitPending
// hold, and the two kinds of wait count together against [Config].WaitMax.
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

// chatChanged is src's change channel; nil for a panic, which is a source
// that never says so.
func chatChanged(src ChatCursor) (ch <-chan struct{}) {
	if src == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			ch = nil
		}
	}()
	return src.ChatCursorChanged()
}

// withChat is r with the cursor read now, or with none.
func withChat(ctx context.Context, r ownerwire.SeatWaitResult, src ChatCursor) ownerwire.SeatWaitResult {
	r.Chat = nil
	if v, ok := readChatCursor(ctx, src); ok {
		r.Chat = &v
	}
	return r
}

func (s *Service) waitPendingChat(ctx context.Context, since string, chat int64, src ChatCursor) (ownerwire.SeatWaitResult, error) {
	w := &s.wait
	clk := w.clock()
	due := clk.Now().Add(WaitChatGap)
	snap, _, err := s.lookNow(ctx, nil)
	if err != nil {
		return ownerwire.SeatWaitResult{}, err
	}
	if since == "" || snap.result.Version != since {
		return withChat(ctx, snap.result, src), nil
	}
	if _, ok := readChatCursor(ctx, src); ok && chat == 0 {
		// The seat has no cursor yet: it gets one at once.
		return withChat(ctx, snap.result, src), nil
	}
	hold := s.cfg.WaitHold
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - waitMargin; left < hold {
			hold = left
		}
	}
	if hold <= 0 {
		return withChat(ctx, snap.result, src), nil
	}
	end := clk.NewTimer(hold)
	defer end.Stop()

	for {
		// The channels first, the reads after: a change between them closes
		// a channel this round selects on.
		ch, seq := w.wakeChan()
		chatCh := chatChanged(src)
		if snap.seq != seq {
			next, ended, err := s.lookNow(ctx, end.C())
			if err != nil {
				return ownerwire.SeatWaitResult{}, err
			}
			if ended {
				return withChat(ctx, snap.result, src), nil
			}
			snap = next
			if snap.result.Version != since {
				return withChat(ctx, snap.result, src), nil
			}
			continue
		}
		var timers []waitTimer
		var gap, expiry <-chan time.Time
		if cur, ok := readChatCursor(ctx, src); ok && cur != chat {
			left := due.Sub(clk.Now())
			if left <= 0 {
				// The cursor was read just now: the answer carries it, with
				// the Card result of the last look, which no poke has made
				// stale.
				r := snap.result
				r.Chat = &cur
				return r, nil
			}
			t := clk.NewTimer(left)
			timers, gap = append(timers, t), t.C()
		}
		if !snap.expiry.IsZero() {
			d := snap.expiry.Sub(s.now()) + waitExpirySlack
			if d < 0 {
				d = 0
			}
			if d < hold {
				t := clk.NewTimer(d)
				timers, expiry = append(timers, t), t.C()
			}
		}
		stop := func() {
			for _, t := range timers {
				t.Stop()
			}
		}
		select {
		case <-ch:
			stop()
		case <-chatCh:
			// Only the cursor is read again: a chat change makes no look
			// at the door.
			stop()
		case <-gap:
			stop()
		case <-expiry:
			stop()
			s.poke()
		case <-end.C():
			stop()
			last, _, err := s.lookNow(ctx, nil)
			if err != nil {
				return ownerwire.SeatWaitResult{}, err
			}
			return withChat(ctx, last.result, src), nil
		case <-ctx.Done():
			stop()
			return ownerwire.SeatWaitResult{}, ctx.Err()
		}
	}
}
