package command

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

var errDisk = errors.New("disk full")

// fakeJournal keeps records in memory. failNext makes the next append fail.
type fakeJournal struct {
	recs     []Record
	failNext bool
}

func (j *fakeJournal) Append(r Record) error {
	if j.failNext {
		j.failNext = false
		return errDisk
	}
	j.recs = append(j.recs, r)
	return nil
}

func testLimits() Limits {
	l := DefaultLimits()
	l.QueueTimeLimit = 10 * time.Minute
	return l
}

func newTest(t *testing.T) (*Machine, *fakeJournal) {
	t.Helper()
	return newTestLimits(t, testLimits())
}

func newTestLimits(t *testing.T, l Limits) (*Machine, *fakeJournal) {
	t.Helper()
	j := &fakeJournal{}
	m, err := New(l, j)
	if err != nil {
		t.Fatal(err)
	}
	return m, j
}

var (
	leadA = LeadTarget("a")
	leadB = LeadTarget("b")
)

func msg(id string, from Sender, to Target) Message {
	return Message{ID: id, From: from, To: to, Kind: "note", Body: "hello " + id}
}

func mustSubmit(t *testing.T, m *Machine, id string, from Sender, to Target, now time.Time) Admission {
	t.Helper()
	adm, err := m.Submit(msg(id, from, to), now)
	if err != nil {
		t.Fatalf("Submit(%s): %v", id, err)
	}
	return adm
}

func mustBegin(t *testing.T, m *Machine, target Target, now time.Time) Dispatch {
	t.Helper()
	d, err := m.BeginDispatch(target, now)
	if err != nil {
		t.Fatalf("BeginDispatch(%s): %v", target, err)
	}
	return d
}

func mustFence(t *testing.T, m *Machine, id string, w Written, o FenceOutcome, now time.Time) FenceUpdate {
	t.Helper()
	up, err := m.RecordFence(id, FenceResult{Written: w, Outcome: o, Readings: "readings of " + id}, now)
	if err != nil {
		t.Fatalf("RecordFence(%s): %v", id, err)
	}
	return up
}

// submitted drives a command from queued through dispatching to Enter written.
func submitted(t *testing.T, m *Machine, target Target, now time.Time) string {
	t.Helper()
	d := mustBegin(t, m, target, now)
	mustFence(t, m, d.ID, WrittenPasteAndEnter, OutcomeSubmitted, now)
	return d.ID
}

func asRefusal(t *testing.T, err error) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a *Refusal", err)
	}
	return r
}

func wantRefusal(t *testing.T, err error, reason Reason) *Refusal {
	t.Helper()
	r := asRefusal(t, err)
	if r.Reason != reason {
		t.Fatalf("refused with %q (%s), want %q", r.Reason, r.Detail, reason)
	}
	return r
}

func wantJournalError(t *testing.T, err error) {
	t.Helper()
	var je *JournalError
	if !errors.As(err, &je) || !errors.Is(err, errDisk) {
		t.Fatalf("error %v is not the journal's failure", err)
	}
}

func stateOf(t *testing.T, m *Machine, id string) State {
	t.Helper()
	c, ok := m.Command(id)
	if !ok {
		t.Fatalf("no command %s", id)
	}
	return c.State
}

func wantState(t *testing.T, m *Machine, id string, s State) {
	t.Helper()
	if got := stateOf(t, m, id); got != s {
		t.Fatalf("command %s is %s, want %s", id, got, s)
	}
}

// snapshot is everything observable and internal about a machine, for
// comparing "unchanged".
type snapshot struct {
	View    View
	Cmds    map[string]CommandView
	Rates   map[string][]time.Time
	Blocked map[string]bool
	Seq     uint64
	Texts   map[string]string
}

func snap(m *Machine) snapshot {
	s := snapshot{View: m.View(), Cmds: map[string]CommandView{}, Rates: map[string][]time.Time{}, Blocked: map[string]bool{}, Seq: m.seq, Texts: map[string]string{}}
	for id, c := range m.cmds {
		s.Cmds[id] = c.view()
		s.Texts[id] = c.text
	}
	for k, v := range m.rates {
		s.Rates[k] = append([]time.Time(nil), v...)
	}
	for k, v := range m.blocked {
		if v {
			s.Blocked[k] = v
		}
	}
	return s
}

func sameSnap(t *testing.T, what string, a, b snapshot) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%s changed the state:\nbefore %+v\nafter  %+v", what, a, b)
	}
}

func ids(m *Machine, target Target) []string {
	var out []string
	for _, c := range m.queues[target.String()] {
		out = append(out, c.id)
	}
	return out
}
