package doortest

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// closed is the closed list of states, written out here so that the test
// does not take the list from the code it checks.
var closed = []record.State{
	"pending", "approved", "approved-by-rule", "approved-by-grant", "denied",
	"withdrawn", "expired", "void", "consumed", "applied", "not-applied", "unknown",
}

// The table of moves holds the closed list and nothing else, and no move
// returns to pending or approved.
func TestMovesStayInTheClosedList(t *testing.T) {
	table := Moves()
	if len(table) != len(closed) {
		t.Errorf("the table has %d states, the closed list %d", len(table), len(closed))
	}
	for _, s := range closed {
		if !s.Valid() {
			t.Errorf("%q is not a state of the record package", s)
		}
		if _, ok := table[s]; !ok {
			t.Errorf("the table has no row for %q", s)
		}
	}
	for from, tos := range table {
		if !from.Valid() {
			t.Errorf("the table has a row for %q, which is no state", from)
		}
		for _, to := range tos {
			switch {
			case !to.Valid():
				t.Errorf("%s moves to %q, which is no state", from, to)
			case to == record.StatePending:
				t.Errorf("%s returns to pending", from)
			case to == record.StateApproved && from != record.StatePending:
				t.Errorf("%s returns to approved", from)
			case to == from:
				t.Errorf("%s moves to itself", from)
			}
		}
	}
	if _, ok := table["held"]; ok || record.State("held").Valid() {
		t.Error("there is a state called held")
	}

	final := []string{}
	for _, s := range closed {
		if Final(s) {
			final = append(final, string(s))
		}
	}
	if got, want := strings.Join(final, " "), "denied withdrawn expired void applied not-applied"; got != want {
		t.Errorf("final states = %s, want %s", got, want)
	}

	// Moves returns a copy.
	table[record.StateDenied] = []record.State{record.StatePending}
	table[record.StatePending][0] = "held"
	if !Final(record.StateDenied) || Moves()[record.StatePending][0] != record.StateApproved {
		t.Error("changing what Moves returned changed the table")
	}
}

// The stand-in keeps, for one pending record, the record, its decision and
// one state of the closed list, and nothing else: there is no mark beside
// the state (held, say) that a test of the host could come to lean on. A
// new field of entry or of Door fails here until it is named.
func TestStandInKeepsNothingBesideTheState(t *testing.T) {
	fields := func(v any) string {
		typ := reflect.TypeOf(v)
		var names []string
		for i := 0; i < typ.NumField(); i++ {
			names = append(names, typ.Field(i).Name+" "+typ.Field(i).Type.String())
		}
		sort.Strings(names)
		return strings.Join(names, "\n")
	}
	wantEntry := strings.Join([]string{
		"decided door.Decided",
		"decision *record.Decision",
		"decisionRec door.Record",
		"pending record.Pending",
		"pendingRec door.Record",
		"reason record.Code",
		"state record.State",
	}, "\n")
	if got := fields(entry{}); got != wantEntry {
		t.Errorf("entry has the fields\n%s\nwant\n%s", got, wantEntry)
	}
	wantRequest := strings.Join([]string{
		"ended bool",
		"pendingID string",
		"req record.Request",
		"sha256 string",
		"target string",
	}, "\n")
	if got := fields(request{}); got != wantRequest {
		t.Errorf("request has the fields\n%s\nwant\n%s", got, wantRequest)
	}
	wantDoor := strings.Join([]string{
		"admitted []time.Time",
		"asked map[string][]time.Time",
		"command string",
		"entries map[string]*doortest.entry",
		"mu sync.Mutex",
		"now func() time.Time",
		"number int",
		"order []string",
		"planToken string",
		"planner string",
		"requests map[string]*doortest.request",
		"seat doortest.Seat",
		"throttled map[string]time.Time",
	}, "\n")
	if got := fields(Door{}); got != wantDoor {
		t.Errorf("Door has the fields\n%s\nwant\n%s", got, wantDoor)
	}
}

// watcher follows the states the stand-in shows, and fails the test on a
// state outside the closed list or a change the table of moves lacks.
type watcher struct {
	f    *fixture
	last map[string]record.State
	seen map[record.State]bool
}

func (w *watcher) look() {
	w.f.t.Helper()
	list, err := w.f.d.List(w.f.ctx)
	if err != nil {
		w.f.t.Fatal(err)
	}
	for _, e := range list {
		if !e.State.Valid() {
			w.f.t.Errorf("%s is in the state %q, which is not in the closed list", e.ID, e.State)
		}
		before, known := w.last[e.ID]
		switch {
		case !known && e.State != record.StatePending:
			w.f.t.Errorf("%s was first seen as %s, not pending", e.ID, e.State)
		case known && before != e.State && !mayMove(before, e.State):
			w.f.t.Errorf("%s went from %s to %s, which the table does not allow", e.ID, before, e.State)
		}
		w.last[e.ID] = e.State
		w.seen[e.State] = true
		shown, err := w.f.d.Show(w.f.ctx, e.ID)
		if err != nil || shown.Entry != e {
			w.f.t.Errorf("show %s = %+v, %v; the list says %+v", e.ID, shown.Entry, err, e)
		}
	}
}

// Every way a record can go is walked, with each refusal of the table
// tried on the way, and the stand-in is looked at after every call: it
// shows only states of the closed list, and only moves of the table.
func TestEveryWalkStaysInTheClosedList(t *testing.T) {
	f := newFixture(t)
	w := &watcher{f: f, last: map[string]record.State{}, seen: map[record.State]bool{}}
	name := func(i int) string { return "feature-" + string(rune('a'+i)) }
	// Each walk takes a new feature and a later minute, so that neither the
	// duplicate rule nor a cap stands in the way.
	type step = func(p record.Pending)
	approve := func(p record.Pending) { f.decide(p, true, nil) }
	use := func(p record.Pending) { f.use(p, nil) }
	settle := func(o door.Outcome) step { return func(p record.Pending) { f.settle(p, o) } }
	walks := [][]step{
		{},
		{func(p record.Pending) { f.decide(p, false, nil) }},
		{func(p record.Pending) { f.d.Withdraw(p.ID) }},
		{func(p record.Pending) { f.d.Void(p.ID) }},
		{func(p record.Pending) { f.clock.Advance(PendingLife); f.d.Expire(p.ID) }},
		{approve},
		{approve, func(p record.Pending) { f.d.Withdraw(p.ID) }},
		{approve, func(p record.Pending) { f.d.Void(p.ID) }},
		{approve, func(p record.Pending) { f.clock.Advance(time.Hour) }},
		{approve, func(p record.Pending) { f.use(p, func(fr *door.Fresh) { fr.ReviewDigest = "" }) }},
		{approve, func(p record.Pending) { f.use(p, func(fr *door.Fresh) { fr.Evidence = nil }) }},
		{approve, use},
		{approve, use, settle(door.OutcomeApplied)},
		{approve, use, settle(door.OutcomeNotApplied)},
		{approve, use, settle(door.OutcomeUnknown)},
		{approve, use, settle(door.OutcomeUnknown), settle(door.OutcomeApplied)},
		{approve, use, settle(door.OutcomeUnknown), settle(door.OutcomeNotApplied)},
	}
	// again is every call that might move a record, thrown at it once more
	// after its walk: none of them may move it against the table.
	again := func(p record.Pending) {
		calls := []func(){
			func() { f.decide(p, true, nil) },
			func() { f.decide(p, false, nil) },
			func() { f.use(p, nil) },
			func() { f.settle(p, door.OutcomeApplied) },
			func() { f.settle(p, door.OutcomeNotApplied) },
			func() { f.settle(p, door.OutcomeUnknown) },
			func() { f.d.Withdraw(p.ID) },
			func() { f.d.Void(p.ID) },
			func() { f.d.Expire(p.ID) },
		}
		for _, call := range calls {
			call()
			w.look()
		}
	}
	for i, walk := range walks {
		f.clock.Set(start.Add(time.Duration(i) * 48 * time.Hour))
		p := f.pending(name(i))
		w.look()
		for _, do := range walk {
			do(p)
			w.look()
		}
		if i > 0 {
			again(p)
		}
		if got := f.state(p.ID); !Final(got) {
			// End what is left, so that the cap of live requests does
			// not stop the next walk.
			f.clock.Advance(PendingLife)
			w.look()
			if got := f.state(p.ID); !Final(got) && got != record.StateConsumed && got != record.StateUnknown {
				t.Errorf("walk %d: %s is still %s a day later", i, p.ID, got)
			}
		}
	}
	// The walks reach every state a verb of the stand-in leads to: all of
	// the closed list but the two a rule or a grant would give.
	for _, s := range closed {
		reached := w.seen[s]
		byGrant := s == record.StateApprovedByRule || s == record.StateApprovedByGrant
		if reached == byGrant {
			t.Errorf("state %s: reached = %v", s, reached)
		}
	}
}
