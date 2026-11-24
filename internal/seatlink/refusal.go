package seatlink

import (
	"time"
)

// RefusalLogEvery is the least time between two journal lines of one kind
// of call the link had no room for. The calls refused in between are
// counted and told in the next line.
const RefusalLogEvery = 30 * time.Second

// kindWords are the fixed words a journal line names a kind by. The wait is
// a sleeper, never refused, and has none.
var kindWords = [numKinds]string{
	kindPublic:  "public",
	kindRead:    "read",
	kindDecide:  "decide",
	kindGenesis: "genesis",
}

// refusal is one line the budget has to tell: what it holds is a kind and
// two counts, never anything of the call refused.
type refusal struct {
	kind       kind
	used, room int
	more       int
}

// say writes the line of r with logf.
func (r refusal) say(logf func(format string, args ...any)) {
	if r.more > 0 {
		logf("ataniq-seat: the link has no room for a call of kind %s (%d of %d in the last %d seconds); the page says the desk is busy; %d more since the last line\n",
			kindWords[r.kind], r.used, r.room, int(BudgetWindow/time.Second), r.more)
		return
	}
	logf("ataniq-seat: the link has no room for a call of kind %s (%d of %d in the last %d seconds); the page says the desk is busy\n",
		kindWords[r.kind], r.used, r.room, int(BudgetWindow/time.Second))
}

// refusedLocked notes that a call of a kind was refused for room, and
// returns the line to tell when one is due, at most one for each kind in
// RefusalLogEvery. It returns nil when there is no log, for a kind that is
// never refused (the wait), and while a line of the kind is not due. It
// runs under mu; the caller says the line after letting mu go.
func (b *budget) refusedLocked(k kind, now time.Time) *refusal {
	if b.logf == nil || kindWords[k] == "" {
		return nil
	}
	if !b.logged[k].IsZero() && now.Sub(b.logged[k]) < RefusalLogEvery {
		b.held[k]++
		return nil
	}
	r := &refusal{kind: k, used: b.used(k), room: k.room(), more: b.held[k]}
	b.logged[k] = now
	b.held[k] = 0
	return r
}

// tell says a line the budget owes, outside mu.
func (b *budget) tell(r *refusal) {
	if r != nil {
		r.say(b.logf)
	}
}

// statsLine is the stats line of a period (see rollLocked): its length in
// whole minutes, and the calls taken and refused in it, by kind. It holds
// counts and nothing of any call.
type statsLine struct {
	minutes        int
	taken, refused [numKinds]int
}

// say writes the line of s with logf.
func (s statsLine) say(logf func(format string, args ...any)) {
	logf("ataniq-seat: calls of the link in the last %d minutes, taken/refused by kind: public %d/%d, read %d/%d, decide %d/%d, genesis %d/%d, wait %d/%d\n",
		s.minutes,
		s.taken[kindPublic], s.refused[kindPublic],
		s.taken[kindRead], s.refused[kindRead],
		s.taken[kindDecide], s.refused[kindDecide],
		s.taken[kindGenesis], s.refused[kindGenesis],
		s.taken[kindWait], s.refused[kindWait])
}

// tellStats says a stats line the budget owes, outside mu.
func (b *budget) tellStats(s *statsLine) {
	if s != nil {
		s.say(b.logf)
	}
}

// WithLogf gives the link a log: logf, which is called with a fixed-text
// line (ending in a newline) when the link has no room for a call of a
// kind and answers [seatapp.ErrBusy] without sending it. There is at most
// one line for each kind in [RefusalLogEvery]; the calls refused between
// two lines are counted in the second. A line names the kind and the room
// used, nothing of the call: no thread, card, credential or answer. When
// a period of at least [StatsLogEvery] refused any call, the link also
// writes one line of counts for the period, the calls taken and refused
// of each kind, at the first call after the period ends: at most one
// such line in StatsLogEvery, and none for a period that refused nothing.
// Nil is no log.
func WithLogf(logf func(format string, args ...any)) Option {
	return func(d *Daemon) error {
		d.budget.logf = logf
		return nil
	}
}
