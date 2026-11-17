package command

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// step is one call of the scenario, at a fixed time.
type step struct {
	name string
	do   func(m *Machine) error
}

func scenario() []step {
	n := 0
	next := func() time.Time { n++; return at(time.Duration(n) * 7 * time.Second) }
	var steps []step
	add := func(name string, f func(m *Machine, now time.Time) error) {
		now := next()
		steps = append(steps, step{name, func(m *Machine) error { return f(m, now) }})
	}
	submit := func(id string, from Sender, to Target) {
		add("submit "+id, func(m *Machine, now time.Time) error { _, err := m.Submit(msg(id, from, to), now); return err })
	}
	begin := func(target Target) {
		add("begin "+target.String(), func(m *Machine, now time.Time) error { _, err := m.BeginDispatch(target, now); return err })
	}
	fence := func(id string, w Written, o FenceOutcome) {
		add("fence "+id+" "+string(o), func(m *Machine, now time.Time) error {
			_, err := m.RecordFence(id, FenceResult{Written: w, Outcome: o, Readings: "readings " + string(o)}, now)
			return err
		})
	}
	confirm := func(id string) {
		add("confirm "+id, func(m *Machine, now time.Time) error { return m.ConfirmDelivery(id, "file:"+id+":100", now) })
	}
	resolve := func(r Resolution) {
		add("resolve "+r.ID+" "+string(r.Decision), func(m *Machine, now time.Time) error { _, err := m.Resolve(r, now); return err })
	}
	tickAfter := func(d time.Duration) {
		add("tick", func(m *Machine, now time.Time) error { _, err := m.Tick(now.Add(d)); return err })
	}

	submit("m1", Daemon(), leadA)
	submit("m2", Supervisor(), leadA)
	submit("m3", Lead("a"), SupervisorTarget())
	begin(leadA)
	fence("m1", WrittenNothing, OutcomeNotIdle)
	begin(leadA)
	fence("m1", WrittenNothing, OutcomeBlocked)
	begin(leadA)
	fence("m1", WrittenPasteAndEnter, OutcomeSubmitted)
	confirm("m1")
	begin(leadA)
	fence("m2", WrittenPaste, OutcomeNotSubmitted)
	resolve(Resolution{ID: "m2", Decision: DecisionResend, Operator: "alice", NewID: "m2b", Note: "again"})
	begin(leadA)
	fence("m2b", WrittenNothing, OutcomeUntestedVersion)
	begin(SupervisorTarget())
	fence("m3", WrittenPasteAndEnter, OutcomeSubmitted)
	tickAfter(31 * time.Second)
	resolve(Resolution{ID: "m3", Decision: DecisionDelivered, Operator: "bob"})
	submit("m4", Daemon(), leadB)
	submit("m5", Daemon(), leadB)
	tickAfter(20 * time.Minute)
	begin(leadB)
	fence("m5", WrittenUnknown, OutcomeDied)
	resolve(Resolution{ID: "m5", Decision: DecisionDrop, Operator: "alice"})
	submit("m6", Daemon(), leadA)
	submit("m7", Daemon(), SupervisorTarget())
	begin(leadA)
	fence("m6", WrittenNothing, OutcomeBlocked)
	begin(leadA)
	fence("m6", WrittenPasteAndEnter, OutcomeSubmitted)
	begin(SupervisorTarget())
	add("refused submit", func(m *Machine, now time.Time) error {
		_, err := m.Submit(Message{ID: "BAD", From: Daemon(), To: leadA, Kind: "note", Body: "x"}, now)
		var r *Refusal
		if errors.As(err, &r) && r.JournalErr == nil {
			return nil
		}
		return err
	})
	submit("m8", Daemon(), leadA)
	return steps
}

func TestEveryAppendFailureLeavesTheStateUnchanged(t *testing.T) {
	steps := scenario()
	clean, cj := newTest(t)
	for _, s := range steps {
		if err := s.do(clean); err != nil {
			t.Fatalf("clean run, %s: %v", s.name, err)
		}
	}
	for i, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			m, j := newTest(t)
			for _, p := range steps[:i] {
				if err := p.do(m); err != nil {
					t.Fatalf("%s: %v", p.name, err)
				}
			}
			before, n := snap(m), len(j.recs)
			j.failNext = true
			err := s.do(m)
			if !errors.Is(err, errDisk) {
				// The refused submission reports a journaling failure inside
				// its refusal and returns nil from the step; check it below.
				if s.name != "refused submit" {
					t.Fatalf("error %v is not the journal's", err)
				}
			}
			if j.failNext {
				// the step wrote no record at all (a refusal that is not journaled)
				j.failNext = false
			}
			sameSnap(t, s.name, before, snap(m))
			if len(j.recs) != n {
				t.Fatalf("a record was written")
			}
			// Done again, it works, and the rest of the scenario follows to the
			// same end as a clean run.
			for _, p := range steps[i:] {
				if err := p.do(m); err != nil {
					t.Fatalf("after the failure, %s: %v", p.name, err)
				}
			}
			if !reflect.DeepEqual(j.recs, cj.recs) {
				t.Errorf("the journal after a failed append differs from a clean run")
			}
			sameSnap(t, "the end", snap(clean), snap(m))
		})
	}
}

// normalize makes the view of a live machine comparable with a restored one:
// a dispatching command becomes the uncertain command that restore makes of
// it, and the details of that completion are cleared on both sides.
func normalize(s snapshot) snapshot {
	out := snapshot{Cmds: map[string]CommandView{}, Texts: s.Texts}
	fix := func(c CommandView) (CommandView, bool) {
		was := c.State == StateDispatching
		if was {
			c.State, c.Reason = StateUncertain, ReasonHolderDied
		}
		if c.State == StateUncertain && c.Reason == ReasonHolderDied {
			c.Detail, c.EvidenceKind, c.Evidence, c.Updated = "", "", "", time.Time{}
		}
		return c, was
	}
	for id, c := range s.Cmds {
		out.Cmds[id], _ = fix(c)
	}
	for _, tv := range s.View.Targets {
		nt := tv
		nt.Commands = nil
		changed := false
		for _, c := range tv.Commands {
			f, was := fix(c)
			changed = changed || was
			nt.Commands = append(nt.Commands, f)
		}
		nt.Frozen, nt.FrozenBy = false, ""
		for _, c := range nt.Commands {
			if c.State == StateUncertain {
				nt.Frozen, nt.FrozenBy = true, c.ID
				break
			}
		}
		if changed {
			nt.BlockedNotified = false
		}
		out.View.Targets = append(out.View.Targets, nt)
	}
	return out
}

func TestRestoreFromEveryPrefixMatchesTheLiveMachine(t *testing.T) {
	steps := scenario()
	m, j := newTest(t)
	boundaries := map[int]snapshot{0: snap(m)}
	for _, s := range steps {
		if err := s.do(m); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		boundaries[len(j.recs)] = snap(m)
	}
	restoreAt := at(24 * time.Hour)
	compared := 0
	for k := 0; k <= len(j.recs); k++ {
		rj := &fakeJournal{}
		r, err := Restore(testLimits(), rj, j.recs[:k], restoreAt)
		if err != nil {
			t.Fatalf("prefix of %d records: %v", k, err)
		}
		// What restore appended is a valid continuation: restoring again from
		// the longer journal changes nothing.
		again, err := Restore(testLimits(), &fakeJournal{}, append(append([]Record(nil), j.recs[:k]...), rj.recs...), restoreAt.Add(time.Hour))
		if err != nil {
			t.Fatalf("prefix of %d records and the completions: %v", k, err)
		}
		sameSnap(t, "a second restore", normalize(snap(r)), normalize(snap(again)))
		if live, ok := boundaries[k]; ok {
			compared++
			got, want := normalize(snap(r)), normalize(live)
			if !reflect.DeepEqual(got.View, want.View) || !reflect.DeepEqual(got.Cmds, want.Cmds) || !reflect.DeepEqual(got.Texts, want.Texts) {
				t.Errorf("prefix of %d records:\nrestored %+v\nlive     %+v", k, got, want)
			}
			// What restore appended is only completions.
			for _, x := range rj.recs {
				if x.To != StateUncertain && x.To != StateQueued {
					t.Errorf("restore appended %+v", x)
				}
			}
		}
		// Whatever the prefix, nothing is dispatching after a restore.
		for id, c := range r.cmds {
			if c.state == StateDispatching || c.state == StateAccepted {
				t.Errorf("prefix %d: command %s is %s after restore", k, id, c.state)
			}
		}
	}
	if compared < len(steps) {
		t.Errorf("compared only %d boundaries of %d steps", compared, len(steps))
	}
}

func TestRestoredFromJSON(t *testing.T) {
	m, j := newTest(t)
	for _, s := range scenario() {
		if err := s.do(m); err != nil {
			t.Fatal(err)
		}
	}
	var decoded []Record
	for _, r := range j.recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var d Record
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		if !d.At.Equal(r.At) {
			t.Fatalf("time %v != %v", d.At, r.At)
		}
		d.At = r.At
		decoded = append(decoded, d)
		if strings.ContainsAny(string(b), "\n\x1b") {
			t.Errorf("record encodes with a raw control character: %s", b)
		}
	}
	r, err := Restore(testLimits(), &fakeJournal{}, decoded, at(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalize(snap(r)).Cmds, normalize(snap(m)).Cmds) {
		t.Error("restored from JSON differs")
	}
}

func TestTextIsOnlyOnTheRecordsThatCreateACommand(t *testing.T) {
	m, j := newTest(t)
	for _, s := range scenario() {
		if err := s.do(m); err != nil {
			t.Fatal(err)
		}
	}
	creating := 0
	for _, r := range j.recs {
		if r.Text != "" {
			creating++
			if r.From != "" || (r.To != StateAccepted && r.To != StateQueued) {
				t.Errorf("text on %+v", r)
			}
		}
	}
	if creating == 0 {
		t.Fatal("no record has text")
	}
	for i, r := range j.recs {
		if r.Seq != uint64(i+1) {
			t.Errorf("record %d has sequence %d", i, r.Seq)
		}
	}
}
