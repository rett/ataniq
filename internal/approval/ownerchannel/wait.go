package ownerchannel

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// This file is the daemon's side of seat.wait: [Service.WaitPending] holds a
// call until the set of pending cards changes, so that the seat learns of a
// new, decided or ended card without asking the door every few seconds.
//
// What wakes a held wait:
//
//   - A poke from the journal and the handles. The service writes its journal
//     and its handles through the two decorators below, which poke after every
//     append (a decision, a held decision, a refusal, a count, a limit, an
//     alert, a delivery, a use and an outcome, and an ExpireHeld that closed a
//     card), whether the write went through or not, since an uncertain write
//     may have been made. A "looked" mark is not poked: no version covers it.
//   - A poke at the end of each [Service.Tick]. This is also how a change made
//     at the door that the daemon did not write, a void by the broker for
//     example, is noticed: the tick lists the door and pokes, so a held wait
//     looks again within one tick. Nothing watches the door itself.
//   - A timer at the earliest expiry of a pending card: the door ends the card
//     by the clock, and no write of the daemon says so.
//   - The end of the hold, which looks once more and answers.
//
// Bounds. The door is listed at most once a second for the whole service,
// however many waits are held and however many pokes come: a poke that comes
// within the second is answered by one look at the end of it, and a look that
// was made while a poke came (the look's own ExpireHeld pokes) is answered by
// one more look a second later, which finds nothing to poke, so a wait cannot
// spin on its own side effects. At most [Config].WaitMax waits are held at
// once; one more is refused with [ErrWaitFull].
//
// Locks. A wait takes no lock of the service while it waits: it sleeps in a
// select on channels and timers. Its own state is under waitState.mu, a leaf:
// nothing is taken, called or waited for while it is held, so poke can be
// called from anywhere, under dmu, mu or the journal's own locks, and never
// blocks the writer. The one look at the door is serialised by waitState.chk,
// a channel whose token a call takes with no other lock held, keeps while it
// waits out the floor and looks, and gives back before it returns (a call
// that waits for the token ends with its context or its hold); inside it the
// look takes what List takes (mu for a moment, the
// journal's locks, and dmu only through jfix when the journal answers
// uncertain, as List does). No path that holds dmu waits for chk, so the two
// cannot be taken against each other.

// ErrWaitFull says the service already holds as many waits as it may, see
// [Config].WaitMax. The gate maps it to the wait-full code.
var ErrWaitFull = errors.New("ownerchannel: too many waits are held")

const (
	// defaultWaitHold is the longest a wait is held: under the gate's 30
	// seconds for a request.
	defaultWaitHold = 25 * time.Second
	// defaultWaitMax is how many waits are held at once.
	defaultWaitMax = 8
	// waitFloor is the least time between two looks at the door.
	waitFloor = time.Second
	// waitMargin is how much of the caller's deadline a hold leaves: the
	// floor's second for the look at its end, and some more.
	waitMargin = 1500 * time.Millisecond
	// waitExpirySlack is added to a card's expiry before the timer fires, so
	// that the look finds the card ended.
	waitExpirySlack = 100 * time.Millisecond
)

// waitClock is the time the wait's floor, hold and expiry timers run on. The
// default is the real one; a test puts its own in waitState.clk before the
// first wait.
type waitClock interface {
	Now() time.Time
	NewTimer(d time.Duration) waitTimer
}

// waitTimer is a timer of a [waitClock].
type waitTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realWaitClock struct{}

func (realWaitClock) Now() time.Time { return time.Now() }
func (realWaitClock) NewTimer(d time.Duration) waitTimer {
	return realWaitTimer{time.NewTimer(d)}
}

type realWaitTimer struct{ t *time.Timer }

func (r realWaitTimer) C() <-chan time.Time { return r.t.C }
func (r realWaitTimer) Stop() bool          { return r.t.Stop() }

// waitSnap is one look at the pending set.
type waitSnap struct {
	ok     bool
	result ownerwire.SeatWaitResult
	// expiry is the earliest end of a pending card after the look, or zero.
	expiry time.Time
	// seq is the poke count when the look began: a poke after it may be a
	// change the look did not see.
	seq uint64
	// taken is when the look began, on the wait clock.
	taken time.Time
}

// waitState is the service's wait bookkeeping. mu guards every field but
// chk and clk; see the lock notes above.
type waitState struct {
	mu sync.Mutex
	// seq counts the pokes, and ch is closed and dropped by the next poke
	// (it is made when a wait asks for it).
	seq uint64
	ch  chan struct{}
	// active is how many waits are in a call.
	active int
	// lastStart is when the last look began, and cache is what it saw.
	lastStart time.Time
	cache     waitSnap
	// chk holds one token while a look is made.
	chk chan struct{}
	// clk is the wait clock; nil is the real one.
	clk waitClock
}

func newWaitState() waitState { return waitState{chk: make(chan struct{}, 1)} }

func (w *waitState) clock() waitClock {
	if w.clk != nil {
		return w.clk
	}
	return realWaitClock{}
}

// poke tells every held wait that something may have changed. It never
// blocks and takes only waitState.mu.
func (s *Service) poke() {
	w := &s.wait
	w.mu.Lock()
	w.seq++
	if w.ch != nil {
		close(w.ch)
		w.ch = nil
	}
	w.mu.Unlock()
}

// wakeChan returns the channel the next poke closes and the poke count.
func (w *waitState) wakeChan() (<-chan struct{}, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch == nil {
		w.ch = make(chan struct{})
	}
	return w.ch, w.seq
}

// WaitPending answers seat.wait. It builds the pending set as the listing
// does, and answers at once when since is empty or is not the set's version.
// Otherwise it holds the call until the version changes, the hold ends
// ([Config].WaitHold, and never more than the caller's deadline less a small
// margin), or ctx is done, which is an error and no result. A hold that ends
// with no change answers the current result, whose version is since: the
// caller takes an equal version as "same".
//
// The result lists at most [ownerwire.MaxSeatWaitCards] cards, in the order
// [ownerwire.PendingVersion] sorts them; Waiting is the whole count and More
// says there are more than listed. The version covers the whole set.
//
// More than [Config].WaitMax calls at once are refused with [ErrWaitFull]
// before they look at anything. A call that ends, by an answer or by its
// context, leaves no goroutine and no timer.
func (s *Service) WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.SeatWaitResult{}, err
	}
	defer done()
	w := &s.wait
	w.mu.Lock()
	if w.active >= s.cfg.WaitMax {
		w.mu.Unlock()
		return ownerwire.SeatWaitResult{}, ErrWaitFull
	}
	w.active++
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.active--
		w.mu.Unlock()
	}()

	res, err := s.waitPending(ctx, since)
	if err != nil && ctx.Err() != nil && s.ctx.Err() != nil {
		return ownerwire.SeatWaitResult{}, ErrClosed
	}
	return res, err
}

func (s *Service) waitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	w := &s.wait
	snap, _, err := s.lookNow(ctx, nil)
	if err != nil {
		return ownerwire.SeatWaitResult{}, err
	}
	if since == "" || snap.result.Version != since {
		return snap.result, nil
	}
	hold := s.cfg.WaitHold
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - waitMargin; left < hold {
			hold = left
		}
	}
	if hold <= 0 {
		return snap.result, nil
	}
	end := w.clock().NewTimer(hold)
	defer end.Stop()

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
				return snap.result, nil
			}
			snap = next
			if snap.result.Version != since {
				return snap.result, nil
			}
			continue
		}
		var expiry <-chan time.Time
		var expiryTimer waitTimer
		if !snap.expiry.IsZero() {
			d := snap.expiry.Sub(s.now()) + waitExpirySlack
			if d < 0 {
				d = 0
			}
			if d < hold {
				expiryTimer = w.clock().NewTimer(d)
				expiry = expiryTimer.C()
			}
		}
		stopExpiry := func() {
			if expiryTimer != nil {
				expiryTimer.Stop()
			}
		}
		select {
		case <-ch:
			stopExpiry()
		case <-expiry:
			// The door ends the card by the clock and writes nothing the
			// service sees: say it may have changed, and look.
			s.poke()
		case <-end.C():
			stopExpiry()
			// The hold ended: look once more, so that a change made at the
			// door and not yet noticed is not kept for the next hold.
			last, _, err := s.lookNow(ctx, nil)
			if err != nil {
				return ownerwire.SeatWaitResult{}, err
			}
			return last.result, nil
		case <-ctx.Done():
			stopExpiry()
			return ownerwire.SeatWaitResult{}, ctx.Err()
		}
	}
}

// lookNow returns a look at the pending set that is not older than the last
// poke, and not more than a second old. It is the cached look when there is
// one; otherwise it makes one, and not before the floor has passed since the
// last was begun. The look is made by one call at a time, which holds the
// token of waitState.chk through the floor's wait as well, so that the calls
// that come while it waits or looks take its look and do not look again.
// give, when it fires while the token or the floor is waited for, ends the
// wait: the answer is then (zero, true, nil).
func (s *Service) lookNow(ctx context.Context, give <-chan time.Time) (waitSnap, bool, error) {
	w := &s.wait
	clk := w.clock()
	fresh := func(c waitSnap, seq uint64, now time.Time) bool {
		return c.ok && c.seq == seq && now.Sub(c.taken) < waitFloor
	}
	w.mu.Lock()
	cache, seq := w.cache, w.seq
	w.mu.Unlock()
	if fresh(cache, seq, clk.Now()) {
		return cache, false, nil
	}
	select {
	case w.chk <- struct{}{}:
	case <-give:
		return waitSnap{}, true, nil
	case <-ctx.Done():
		return waitSnap{}, false, ctx.Err()
	}
	// This call holds the token. Another may have looked while it waited
	// for it.
	var now time.Time
	for {
		w.mu.Lock()
		cache, seq = w.cache, w.seq
		last := w.lastStart
		now = clk.Now()
		if fresh(cache, seq, now) {
			w.mu.Unlock()
			<-w.chk
			return cache, false, nil
		}
		if last.IsZero() || now.Sub(last) >= waitFloor {
			w.lastStart = now
			w.mu.Unlock()
			break
		}
		w.mu.Unlock()
		t := clk.NewTimer(waitFloor - now.Sub(last))
		select {
		case <-t.C():
		case <-give:
			t.Stop()
			<-w.chk
			return waitSnap{}, true, nil
		case <-ctx.Done():
			t.Stop()
			<-w.chk
			return waitSnap{}, false, ctx.Err()
		}
	}
	snap, err := s.takeWaitSnap(ctx)
	<-w.chk
	if err != nil {
		return waitSnap{}, false, err
	}
	snap.ok, snap.seq, snap.taken = true, seq, now
	w.mu.Lock()
	w.cache = snap
	w.mu.Unlock()
	return snap, false, nil
}

// takeWaitSnap lists the door as [Service.List] does, for the cards that are
// pending: it closes the held cards that expired, reads the door and the held
// marks, and makes the result. It takes the service's locks only as List
// does.
func (s *Service) takeWaitSnap(ctx context.Context) (waitSnap, error) {
	now := s.now()
	if _, err := s.journalIn(ctx).ExpireHeld(now); err != nil {
		return waitSnap{}, s.jfix(ctx, err)
	}
	entries, err := s.cfg.Door.List(ctx)
	if err != nil {
		return waitSnap{}, wrapDoor(err)
	}
	held := make(map[string]bool)
	seen := make(map[string]bool)
	var all []ownerwire.SeatWaitCard
	var expiry time.Time
	for _, e := range entries {
		if e.State != record.StatePending {
			continue
		}
		p, err := s.pendingFor(ctx, e)
		if err != nil {
			return waitSnap{}, wrapDoor(err)
		}
		if project := p.Binding.Project; !seen[project] {
			seen[project] = true
			cards, err := s.journalIn(ctx).HeldCards(project)
			if err != nil {
				return waitSnap{}, s.jfix(ctx, err)
			}
			for _, c := range cards {
				held[c.ID] = true
			}
		}
		all = append(all, ownerwire.SeatWaitCard{
			ID: p.ID, Kind: ownerwire.CardKindApproval, BindingSHA256: p.BindingSHA256, Held: held[p.ID],
		})
		if t, err := record.ParseTime(e.ExpiresAt); err == nil && t.After(now) && (expiry.IsZero() || t.Before(expiry)) {
			expiry = t
		}
	}
	return waitSnap{result: waitResultOf(all), expiry: expiry}, nil
}

// waitResultOf makes the result of a pending set. The version covers the
// whole set; the cards are the first of it, at most
// [ownerwire.MaxSeatWaitCards], in the order the version sorts by. It sorts
// all.
func waitResultOf(all []ownerwire.SeatWaitCard) ownerwire.SeatWaitResult {
	version := ownerwire.PendingVersion(all)
	sort.Slice(all, func(i, j int) bool { return waitCardLess(all[i], all[j]) })
	res := ownerwire.SeatWaitResult{Version: version, Waiting: len(all), Cards: all}
	if len(res.Cards) > ownerwire.MaxSeatWaitCards {
		res.Cards = res.Cards[:ownerwire.MaxSeatWaitCards]
	}
	if res.Cards == nil {
		res.Cards = []ownerwire.SeatWaitCard{}
	}
	res.More = res.Waiting > len(res.Cards)
	return res
}

// waitCardLess is the order [ownerwire.PendingVersion] sorts cards in.
func waitCardLess(a, b ownerwire.SeatWaitCard) bool {
	switch {
	case a.Kind != b.Kind:
		return a.Kind < b.Kind
	case a.ID != b.ID:
		return a.ID < b.ID
	case a.BindingSHA256 != b.BindingSHA256:
		return a.BindingSHA256 < b.BindingSHA256
	}
	return !a.Held && b.Held
}

// pokeJournal is the journal with a poke after each write. Reads are the
// embedded journal's.
type pokeJournal struct {
	Journal
	poke func()
}

// unwrapJournal returns the journal a service was given, under its decorator.
func unwrapJournal(j Journal) Journal {
	if p, ok := j.(*pokeJournal); ok {
		return p.Journal
	}
	return j
}

func (p *pokeJournal) AppendDecision(decision []byte, seen ownerjournal.Seen) (*ownerjournal.Decision, error) {
	defer p.poke()
	return p.Journal.AppendDecision(decision, seen)
}

func (p *pokeJournal) AppendHeld(decision, pending []byte, seen ownerjournal.Seen) (ownerjournal.HeldCard, error) {
	defer p.poke()
	return p.Journal.AppendHeld(decision, pending, seen)
}

func (p *pokeJournal) AppendRefused(r ownerjournal.Refused) error {
	defer p.poke()
	return p.Journal.AppendRefused(r)
}

func (p *pokeJournal) AppendCount(c ownerjournal.Count) error {
	defer p.poke()
	return p.Journal.AppendCount(c)
}

func (p *pokeJournal) AppendLimit(c ownerjournal.LimitChange) error {
	defer p.poke()
	return p.Journal.AppendLimit(c)
}

func (p *pokeJournal) AppendAlert(a ownerjournal.Alert) error {
	defer p.poke()
	return p.Journal.AppendAlert(a)
}

// ExpireHeld pokes when it closed a card, or when it failed uncertain and
// may have. A call that closed nothing writes nothing and pokes nothing, so
// a wait's own look does not wake the waits.
func (p *pokeJournal) ExpireHeld(now time.Time) ([]ownerjournal.HeldCard, error) {
	closed, err := p.Journal.ExpireHeld(now)
	if len(closed) > 0 || errors.Is(err, ownerjournal.ErrUncertain) {
		p.poke()
	}
	return closed, err
}

// pokeHandles is the handle writes with a poke after each.
type pokeHandles struct {
	HandleWrites
	poke func()
}

func (p pokeHandles) MarkDelivered(d *ownerjournal.Decision, at time.Time) (*ownerjournal.Delivery, error) {
	defer p.poke()
	return p.HandleWrites.MarkDelivered(d, at)
}

func (p pokeHandles) MarkRefusedByDoor(d *ownerjournal.Decision, at time.Time, code record.Code) error {
	defer p.poke()
	return p.HandleWrites.MarkRefusedByDoor(d, at, code)
}

func (p pokeHandles) AppendUse(u *ownerjournal.Delivery, at time.Time, code record.Code) error {
	defer p.poke()
	return p.HandleWrites.AppendUse(u, at, code)
}

func (p pokeHandles) AppendOutcome(u *ownerjournal.Delivery, at time.Time, state record.State, line string) error {
	defer p.poke()
	return p.HandleWrites.AppendOutcome(u, at, state, line)
}
