package leadregistry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadrole"
)

// The transition table of doc.go, one letter per cell: Y is admitted, P is
// admitted with a proof and while no launch intent is pending, N is refused
// with ErrStateTransition. Columns are the target states in tableTargets
// order. This is written out separately from the code on purpose. A lead has
// no stalled row, and its stalled column is refused everywhere; the stalled
// column of a coordinator is admitted from starting, ready and degraded.
var tableTargets = []State{StateDesired, StateReady, StateDegraded, StateStalled, StateStopping, StateStopped, StateEnded, StateUnknown}

type tableRow struct {
	from  State
	cells string // one letter per tableTargets entry
}

var leadTable = []tableRow{
	//                 desired ready degraded stalled stopping stopped ended unknown
	{StateDesired /*  */, "YYYNYPPY"},
	{StateStarting /* */, "NYYNYPPY"},
	{StateReady /*    */, "NYYNYPPY"},
	{StateDegraded /* */, "NYYNYPPY"},
	{StateStopping /* */, "NYYNYPPY"},
	{StateStopped /*  */, "YYYNYPPY"},
	{StateEnded /*    */, "YYYNYPPY"},
	{StateUnknown /*  */, "NNNNNPPY"},
}

var coordinatorTable = []tableRow{
	//                 desired ready degraded stalled stopping stopped ended unknown
	{StateDesired /*  */, "YYYNYPPY"},
	{StateStarting /* */, "NYYYYPPY"},
	{StateReady /*    */, "NYYYYPPY"},
	{StateDegraded /* */, "NYYYYPPY"},
	{StateStalled /*  */, "NYYNYPPY"},
	{StateStopping /* */, "NYYNYPPY"},
	{StateStopped /*  */, "YYYNYPPY"},
	{StateEnded /*    */, "YYYNYPPY"},
	{StateUnknown /*  */, "NNNNNPPY"},
}

// tableKinds are the two kinds of role the table is tried for.
var tableKinds = []struct {
	name string
	role func(testing.TB) leadrole.Role
	rows []tableRow
}{
	{"lead", func(t testing.TB) leadrole.Role { return lead(t, "proj") }, leadTable},
	{"coordinator", func(t testing.TB) leadrole.Role { return coord(t, "proj", "feat") }, coordinatorTable},
}

// intentCases are the launch-intent conditions a role can be in.
var intentCases = []struct {
	name    string
	launch  bool    // BeginLaunch was called
	outcome Outcome // recorded outcome; empty leaves the intent pending
}{
	{"no intent", false, ""},
	{"pending", true, ""},
	{"launched", true, OutcomeLaunched},
	{"adopted", true, OutcomeAdopted},
	{"failed", true, OutcomeFailed},
	{"abandoned", true, OutcomeAbandoned},
}

// reach builds a registry with one ataniq lead in the given intent condition
// and shown state. It reports false when the combination cannot exist: a
// state set after a launch can be starting only until the first change, and
// with a pending intent no state that needs stopped, ended or desired exists.
func reach(t *testing.T, name string, launch bool, outcome Outcome, state State) (*Registry, string, bool) {
	t.Helper()
	return reachRole(t, lead(t, "proj"), name, launch, outcome, state)
}

// reachRole is reach for any role. A coordinator is defined under a lead that
// is launched and ready, as a launch of it needs.
func reachRole(t *testing.T, role leadrole.Role, name string, launch bool, outcome Outcome, state State) (*Registry, string, bool) {
	t.Helper()
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	if role.Kind() == leadrole.KindCoordinator {
		liveLead(t, r, role.Parent())
	}
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	var epoch uint64
	pending := false
	if launch {
		in, err := r.BeginLaunch(role)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		epoch = in.Epoch
		pending = true
		if outcome != "" {
			ok(r.RecordOutcome(role, in.ID, epoch, outcome, UnitEvidence{}))
			pending = false
		}
	}
	proof := func(st State) {
		ok(r.SetState(role, StateChange{Epoch: epoch, State: st, Evidence: EvidenceObservation,
			Reason: Reason{Code: "setup"}, Proof: "unit not loaded"}))
	}
	switch state {
	case StateStarting:
		if !launch {
			return nil, "", false
		}
	case StateReady, StateDegraded, StateStopping, StateUnknown:
		ok(r.SetState(role, setState(state, epoch)))
	case StateStalled:
		if !launch || role.Kind() != leadrole.KindCoordinator {
			return nil, "", false
		}
		ok(r.SetState(role, setState(state, epoch)))
	case StateStopped, StateEnded:
		if pending {
			return nil, "", false
		}
		proof(state)
	case StateDesired:
		if pending {
			return nil, "", false
		}
		proof(StateStopped)
		ok(r.SetState(role, setState(StateDesired, epoch)))
	}
	return r, dir, true
}

// TestTransitionTable tries every cell of the table, for a lead and for a
// coordinator, in every launch-intent condition a role can be in: allowed
// cells change the shown state and refused cells write nothing.
func TestTransitionTable(t *testing.T) {
	for _, kind := range tableKinds {
		role := kind.role(t)
		cells := 0
		for _, ic := range intentCases {
			for _, row := range kind.rows {
				if len(row.cells) != len(tableTargets) {
					t.Fatalf("%s: row %s has %d cells", kind.name, row.from, len(row.cells))
				}
				for i, to := range tableTargets {
					r, dir, reachable := reachRole(t, role, ic.name, ic.launch, ic.outcome, row.from)
					if !reachable {
						continue
					}
					cells++
					before, _ := r.Get(role)
					size := journalSize(t, dir)
					rev, _ := r.Revision()
					c := StateChange{Epoch: before.Epoch, State: to, Evidence: EvidenceObservation, Reason: Reason{Code: "table"}}
					if to == StateStopped || to == StateEnded {
						c.Proof = "unit not loaded and control group empty"
					}
					got, err := r.SetState(role, c)
					cell := kind.name + ", " + ic.name + ": " + string(row.from) + " -> " + string(to)
					pending := ic.launch && ic.outcome == ""
					switch row.cells[i] {
					case 'Y':
						if err != nil {
							t.Errorf("%s: refused: %v", cell, err)
							continue
						}
					case 'P':
						if pending {
							if !errors.Is(err, ErrOutcomePending) || !errors.Is(err, ErrStateTransition) {
								t.Errorf("%s: err = %v, want ErrOutcomePending", cell, err)
							}
						} else if err != nil {
							t.Errorf("%s: refused: %v", cell, err)
							continue
						}
					case 'N':
						if !errors.Is(err, ErrStateTransition) || errors.Is(err, ErrOutcomePending) || errors.Is(err, ErrInvalid) {
							t.Errorf("%s: err = %v, want ErrStateTransition", cell, err)
						}
					default:
						t.Fatalf("bad table letter %q", row.cells[i])
					}
					after, _ := r.Get(role)
					if err == nil {
						if got.Shown.State != to || after.Shown.State != to {
							t.Errorf("%s: shown state is %s", cell, after.Shown.State)
						}
						continue
					}
					if journalSize(t, dir) != size {
						t.Errorf("%s: a refused change wrote to the journal", cell)
					}
					if rv, _ := r.Revision(); rv != rev {
						t.Errorf("%s: a refused change moved the revision", cell)
					}
					if after.Shown != before.Shown {
						t.Errorf("%s: a refused change changed the shown state", cell)
					}
				}
			}
		}
		// Every state of the rows x 8 targets in the conditions that need no
		// restriction on the states, less the combinations that cannot exist.
		if cells < 200 {
			t.Fatalf("%s: only %d cells were tried", kind.name, cells)
		}
	}
}

// TestTransitionStartingIsNotASetting: starting is never a target.
func TestTransitionStartingIsNotASetting(t *testing.T) {
	r, _, _ := reach(t, "no intent", false, "", StateStopped)
	if _, err := r.SetState(lead(t, "proj"), setState(StateStarting, 0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("starting: %v", err)
	}
}

// TestDesiredCannotLaunderAState: the sequence of the review. A launched,
// ready role that is shown unknown cannot be made launchable by desired.
func TestDesiredCannotLaunderAState(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{InvocationID: "i"}))
	ok(r.SetState(role, setState(StateReady, 1)))
	ok(r.SetState(role, setState(StateUnknown, 1)))
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrLaunchState) {
		t.Fatalf("BeginLaunch from unknown: %v", err)
	}
	if _, err := r.SetState(role, setState(StateDesired, 1)); !errors.Is(err, ErrStateTransition) {
		t.Fatalf("desired from unknown: %v", err)
	}
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrLaunchState) {
		t.Fatalf("BeginLaunch after the refused desired: %v", err)
	}
}

// TestStoppedBeforeOutcomeIsRefused: the sequence of the review. A proof that
// predates the outcome cannot survive a later launched outcome.
func TestStoppedBeforeOutcomeIsRefused(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetState(role, stopProof(1)); !errors.Is(err, ErrOutcomePending) {
		t.Fatalf("stopped with a pending intent: %v", err)
	}
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{InvocationID: "i"}))
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrLaunchState) {
		t.Fatalf("second launch admitted over a launched unit: %v", err)
	}
	// After the outcome, a proof is fine, and the next launch is admitted.
	ok(r.SetState(role, stopProof(1)))
	if in2, err := r.BeginLaunch(role); err != nil || in2.Epoch != 2 {
		t.Fatalf("launch after proof: %+v %v", in2, err)
	}
}

// ownerDecision is a decision that resolves unknown.
func ownerDecision(epoch uint64, st State) OwnerDecision {
	return OwnerDecision{Epoch: epoch, State: st, Reason: Reason{Code: "owner-decision", Detail: "checked by hand"}, Decision: "the owner confirmed the unit is gone"}
}

// TestResolveUnknownTable tries ResolveUnknown from every shown state in
// every launch-intent condition: it is admitted from unknown without a
// pending intent only, and moves to stopped or ended only.
func TestResolveUnknownTable(t *testing.T) {
	role := lead(t, "proj")
	states := []State{StateDesired, StateStarting, StateReady, StateDegraded, StateStopping, StateStopped, StateEnded, StateUnknown}
	for _, ic := range intentCases {
		for _, from := range states {
			for _, to := range []State{StateStopped, StateEnded} {
				r, dir, reachable := reach(t, ic.name, ic.launch, ic.outcome, from)
				if !reachable {
					continue
				}
				cell := ic.name + ": " + string(from) + " -> " + string(to)
				before, _ := r.Get(role)
				size := journalSize(t, dir)
				got, err := r.ResolveUnknown(role, ownerDecision(before.Epoch, to))
				pending := ic.launch && ic.outcome == ""
				switch {
				case from != StateUnknown:
					if !errors.Is(err, ErrStateTransition) || errors.Is(err, ErrOutcomePending) {
						t.Errorf("%s: err = %v, want ErrStateTransition", cell, err)
					}
				case pending:
					if !errors.Is(err, ErrOutcomePending) || !errors.Is(err, ErrStateTransition) {
						t.Errorf("%s: err = %v, want ErrOutcomePending", cell, err)
					}
				default:
					if err != nil {
						t.Errorf("%s: refused: %v", cell, err)
						continue
					}
					if got.Shown.State != to || got.Shown.Evidence != EvidenceOwner ||
						got.Shown.Proof != "the owner confirmed the unit is gone" || got.Shown.Reason.Code != "owner-decision" {
						t.Errorf("%s: shown %+v", cell, got.Shown)
					}
					continue
				}
				if journalSize(t, dir) != size {
					t.Errorf("%s: a refused decision wrote to the journal", cell)
				}
				if after, _ := r.Get(role); after.Shown != before.Shown {
					t.Errorf("%s: a refused decision changed the shown state", cell)
				}
			}
		}
	}
}

func TestResolveUnknownInputs(t *testing.T) {
	ok := okf(t)
	r, dir, _ := reach(t, "launched", true, OutcomeLaunched, StateUnknown)
	role := lead(t, "proj")
	size := journalSize(t, dir)
	for name, c := range map[string]struct {
		d    OwnerDecision
		want error
	}{
		"empty decision":    {OwnerDecision{Epoch: 1, State: StateStopped, Reason: Reason{Code: "x"}}, ErrDecisionRequired},
		"long decision":     {OwnerDecision{Epoch: 1, State: StateStopped, Reason: Reason{Code: "x"}, Decision: strings.Repeat("d", MaxProofBytes+1)}, ErrInvalid},
		"control character": {OwnerDecision{Epoch: 1, State: StateStopped, Reason: Reason{Code: "x"}, Decision: "a\nb"}, ErrInvalid},
		"bad reason":        {OwnerDecision{Epoch: 1, State: StateStopped, Reason: Reason{Code: "X"}, Decision: "d"}, ErrInvalid},
		"to ready":          {OwnerDecision{Epoch: 1, State: StateReady, Reason: Reason{Code: "x"}, Decision: "d"}, ErrInvalid},
		"to desired":        {OwnerDecision{Epoch: 1, State: StateDesired, Reason: Reason{Code: "x"}, Decision: "d"}, ErrInvalid},
		"to unknown":        {OwnerDecision{Epoch: 1, State: StateUnknown, Reason: Reason{Code: "x"}, Decision: "d"}, ErrInvalid},
		"to starting":       {OwnerDecision{Epoch: 1, State: StateStarting, Reason: Reason{Code: "x"}, Decision: "d"}, ErrInvalid},
		"to nothing":        {OwnerDecision{Epoch: 1, Reason: Reason{Code: "x"}, Decision: "d"}, ErrInvalid},
		"stale epoch":       {OwnerDecision{Epoch: 2, State: StateStopped, Reason: Reason{Code: "x"}, Decision: "d"}, ErrStaleEpoch},
	} {
		if _, err := r.ResolveUnknown(role, c.d); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if _, err := r.ResolveUnknown(lead(t, "nope"), ownerDecision(1, StateStopped)); !errors.Is(err, ErrNoRole) {
		t.Errorf("unknown role: %v", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("refused decisions wrote")
	}
	// SetState with owner evidence does not resolve unknown: the named
	// operation does.
	for _, st := range []State{StateStopped, StateEnded} {
		_, err := r.SetState(role, StateChange{Epoch: 1, State: st, Evidence: EvidenceOwner, Reason: Reason{Code: "x"}, Proof: "p"})
		if !errors.Is(err, ErrStateTransition) {
			t.Errorf("SetState %s with owner evidence from unknown: %v", st, err)
		}
	}
	// Proof from the other evidence kinds still resolves it.
	for _, ev := range []Evidence{EvidenceObservation, EvidenceProvider} {
		r2, _, _ := reach(t, "launched", true, OutcomeLaunched, StateUnknown)
		ok(r2.SetState(role, StateChange{Epoch: 1, State: StateStopped, Evidence: ev, Reason: Reason{Code: "x"}, Proof: "p"}))
	}
	// An owner decision is journaled under its own kind and survives replay.
	e := ok(r.ResolveUnknown(role, ownerDecision(1, StateEnded)))
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"kind":"owner-decision"`) || !strings.Contains(string(b), "the owner confirmed the unit is gone") {
		t.Fatal("owner decision not journaled")
	}
	r.Close()
	r3 := openReg(t, dir)
	if got, _ := r3.Get(role); !sameEntries([]Entry{got}, []Entry{e}) {
		t.Fatalf("after replay: %+v, want %+v", got, e)
	}
}

// TestOwnerEvidenceOnlyThroughResolveUnknown: SetState refuses owner-decision
// evidence for stopped and ended from every state, in every launch-intent
// condition, for a lead and for a coordinator, and writes nothing. The same
// change with other evidence is the table's, and owner evidence for the other
// states is not affected.
func TestOwnerEvidenceOnlyThroughResolveUnknown(t *testing.T) {
	for _, kind := range tableKinds {
		role := kind.role(t)
		tried := 0
		for _, ic := range intentCases {
			for _, row := range kind.rows {
				for _, to := range []State{StateStopped, StateEnded} {
					r, dir, reachable := reachRole(t, role, ic.name, ic.launch, ic.outcome, row.from)
					if !reachable {
						continue
					}
					tried++
					cell := kind.name + ", " + ic.name + ": " + string(row.from) + " -> " + string(to)
					before, _ := r.Get(role)
					size := journalSize(t, dir)
					c := StateChange{Epoch: before.Epoch, State: to, Evidence: EvidenceOwner, Reason: Reason{Code: "owner"}, Proof: "the owner says so"}
					_, err := r.SetState(role, c)
					if !errors.Is(err, ErrStateTransition) || errors.Is(err, ErrOutcomePending) || errors.Is(err, ErrInvalid) {
						t.Errorf("%s: err = %v, want ErrStateTransition", cell, err)
					}
					if journalSize(t, dir) != size {
						t.Errorf("%s: a refused change wrote", cell)
					}
					if after, _ := r.Get(role); after.Shown != before.Shown {
						t.Errorf("%s: a refused change changed the shown state", cell)
					}
					// The proof of another kind of evidence is the table's.
					c.Evidence = EvidenceProvider
					if _, err := r.SetState(role, c); (err == nil) != (!ic.launch || ic.outcome != "") {
						t.Errorf("%s: with provider evidence: %v", cell, err)
					}
				}
			}
		}
		if tried < 80 {
			t.Errorf("%s: only %d cells", kind.name, tried)
		}
	}
	// Owner evidence for a state that needs no proof is still admitted.
	r, _, _ := reach(t, "launched", true, OutcomeLaunched, StateReady)
	role := lead(t, "proj")
	if _, err := r.SetState(role, StateChange{Epoch: 1, State: StateDegraded, Evidence: EvidenceOwner, Reason: Reason{Code: "owner"}}); err != nil {
		t.Errorf("owner evidence for degraded: %v", err)
	}
	// ResolveUnknown is the way, and it stays one.
	r, _, _ = reach(t, "launched", true, OutcomeLaunched, StateUnknown)
	if e, err := r.ResolveUnknown(role, ownerDecision(1, StateStopped)); err != nil || e.Shown.Evidence != EvidenceOwner {
		t.Errorf("ResolveUnknown: %+v %v", e, err)
	}
}
