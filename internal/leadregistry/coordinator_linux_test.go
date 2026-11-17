package leadregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadrole"
)

// coordRig is a registry with the lead "proj" launched and ready (epoch 1)
// and the coordinator "proj/coord/feat" defined and never launched.
type coordRig struct {
	t     *testing.T
	r     *Registry
	dir   string
	lead  leadrole.Role
	coord leadrole.Role
}

func newCoordRig(t *testing.T) *coordRig {
	t.Helper()
	dir := regDir(t)
	r := openReg(t, dir)
	g := &coordRig{t: t, r: r, dir: dir, lead: lead(t, "proj"), coord: coord(t, "proj", "feat")}
	if e := liveLead(t, r, g.lead); e != 1 {
		t.Fatalf("lead epoch %d", e)
	}
	if _, err := r.Define(g.coord, ManagerAtaniq, Requested{}); err != nil {
		t.Fatal(err)
	}
	return g
}

// reopen closes the registry and opens the file again.
func (g *coordRig) reopen() {
	g.t.Helper()
	g.r.Close()
	g.r = openReg(g.t, g.dir)
}

// launch begins a launch of the coordinator and records it launched.
func (g *coordRig) launch() Intent {
	g.t.Helper()
	in, err := g.r.BeginLaunch(g.coord)
	if err != nil {
		g.t.Fatalf("BeginLaunch(%s): %v", g.coord, err)
	}
	if _, err := g.r.RecordOutcome(g.coord, in.ID, in.Epoch, OutcomeLaunched, UnitEvidence{InvocationID: "i"}); err != nil {
		g.t.Fatal(err)
	}
	return in
}

func (g *coordRig) set(role leadrole.Role, st State) {
	g.t.Helper()
	e, err := g.r.Get(role)
	if err != nil {
		g.t.Fatal(err)
	}
	c := setState(st, e.Epoch)
	if st == StateStopped || st == StateEnded {
		c = StateChange{Epoch: e.Epoch, State: st, Evidence: EvidenceObservation, Reason: Reason{Code: "unit-gone"}, Proof: "unit not loaded and control group empty"}
	}
	if _, err := g.r.SetState(role, c); err != nil {
		g.t.Fatalf("SetState(%s, %s): %v", role, st, err)
	}
}

// refused calls f and checks that it failed with every wanted error and wrote
// nothing.
func (g *coordRig) refused(what string, f func() error, want ...error) {
	g.t.Helper()
	size := journalSize(g.t, g.dir)
	before := snapshot(g.t, g.r)
	err := f()
	if err == nil {
		g.t.Errorf("%s: admitted", what)
		return
	}
	for _, w := range want {
		if !errors.Is(err, w) {
			g.t.Errorf("%s: err = %v, want %v", what, err, w)
		}
	}
	if journalSize(g.t, g.dir) != size || !sameEntries(before, snapshot(g.t, g.r)) {
		g.t.Errorf("%s: a refused call wrote or changed state", what)
	}
}

func TestCoordinatorDefine(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	c := coord(t, "proj", "feat")
	// A coordinator needs no parent defined to be defined, and defaults the
	// runtime and the model.
	e := ok(r.Define(c, ManagerAtaniq, Requested{}))
	if e.Requested != (Requested{Provider: ProviderClaude, Model: "fable"}) {
		t.Fatalf("defaults: %+v", e.Requested)
	}
	if e.Epoch != 0 || e.Shown.State != StateStopped || e.Desired != DesiredStopped || e.Intent != nil || e.Role != c {
		t.Fatalf("defined entry: %+v", e)
	}
	// The other fields are optional and kept; a given runtime and model win.
	e = ok(r.Define(c, ManagerAtaniq, Requested{Provider: ProviderCodex, Model: "m", Effort: "low"}))
	if e.Requested != (Requested{Provider: ProviderCodex, Model: "m", Effort: "low"}) {
		t.Fatalf("given: %+v", e.Requested)
	}
	e = ok(r.Define(c, ManagerAtaniq, Requested{Model: "m2", PermissionMode: "plan", AccessMode: "ro", Version: "1"}))
	if e.Requested.Provider != ProviderClaude || e.Requested.Model != "m2" || e.Requested.Version != "1" {
		t.Fatalf("partial: %+v", e.Requested)
	}
	// A lead still needs every field: the defaults are for coordinators.
	if _, err := r.Define(lead(t, "proj"), ManagerAtaniq, Requested{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lead with an empty profile: %v", err)
	}
	if _, err := r.Define(lead(t, "proj"), ManagerAtaniq, Requested{Provider: ProviderClaude, Model: "m"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lead with a partial profile: %v", err)
	}
	dir := regDir(t)
	r2 := openReg(t, dir)
	size := journalSize(t, dir)
	for name, req := range map[string]Requested{
		"unknown provider": {Provider: "gpt"},
		"long model":       {Model: strings.Repeat("m", MaxFieldBytes+1)},
		"model control":    {Model: "a\nb"},
		"effort control":   {Effort: "a\tb"},
		"long version":     {Version: strings.Repeat("v", MaxFieldBytes+1)},
		"access control":   {AccessMode: "a\x00"},
		"invalid UTF-8":    {PermissionMode: "\xff"},
	} {
		if _, err := r2.Define(c, ManagerAtaniq, req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := r2.Define(c, "other", Requested{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("manager: %v", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("a refused Define wrote")
	}
	// The defaults are journaled, and replay gives the same entry.
	ok(r2.Define(c, ManagerTmux, Requested{}))
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), `"provider":"claude"`) || !strings.Contains(string(b), `"model":"fable"`) {
		t.Fatalf("defaults not in the journal: %s", b)
	}
	want, _ := r2.Get(c)
	r2.Close()
	if got, _ := openReg(t, dir).Get(c); !sameEntries([]Entry{got}, []Entry{want}) {
		t.Fatalf("replay: %+v want %+v", got, want)
	}
}

func TestCoordinatorLaunchIntent(t *testing.T) {
	g := newCoordRig(t)
	in := g.launch()
	id := g.coord.Identity(1, 1)
	if id != "proj-lead@e1:coord:feat:1" {
		t.Fatalf("identity %q", id)
	}
	if in.Epoch != 1 || in.ParentEpoch != 1 || in.Identity != id || in.UnitName != "ataniq-coord-proj_feat.service" ||
		in.Outcome != OutcomePending || in.ProfileDigest != (Requested{Provider: ProviderClaude, Model: "fable"}).Digest() {
		t.Fatalf("intent %+v", in)
	}
	e, _ := g.r.Get(g.coord)
	if e.Epoch != 1 || e.Shown.State != StateStarting || e.Intent.Identity != id || e.Intent.ParentEpoch != 1 || e.Intent.Outcome != OutcomeLaunched {
		t.Fatalf("entry %+v", e)
	}
	role, pe, n, err := leadrole.ParseIdentity(e.Intent.Identity)
	if err != nil || role != g.coord || pe != 1 || n != 1 {
		t.Fatalf("ParseIdentity: %q %d %d %v", role, pe, n, err)
	}
	b, _ := os.ReadFile(filepath.Join(g.dir, FileName))
	if !bytes.Contains(b, []byte(`"identity":"`+id+`"`)) || !bytes.Contains(b, []byte(`"parent_epoch":1`)) {
		t.Fatalf("intent not journaled with the parent epoch and the identity:\n%s", b)
	}
	// The intent of a lead carries neither.
	le, _ := g.r.Get(g.lead)
	if le.Intent.ParentEpoch != 0 || le.Intent.Identity != "" {
		t.Fatalf("lead intent %+v", le.Intent)
	}
	// Instance 2 under the same parent epoch, then a new lead epoch and
	// instance 3 under it.
	g.set(g.coord, StateReady)
	g.set(g.coord, StateStopped)
	if in2 := g.launch(); in2.Epoch != 2 || in2.ParentEpoch != 1 || in2.Identity != "proj-lead@e1:coord:feat:2" {
		t.Fatalf("instance 2: %+v", in2)
	}
	g.set(g.coord, StateStopped)
	g.set(g.lead, StateStopped)
	if in := liveLeadAgain(t, g); in != 2 {
		t.Fatalf("lead epoch %d", in)
	}
	if in3 := g.launch(); in3.Epoch != 3 || in3.ParentEpoch != 2 || in3.Identity != "proj-lead@e2:coord:feat:3" {
		t.Fatalf("instance 3: %+v", in3)
	}
	// Replay reads the same.
	want := snapshot(t, g.r)
	g.reopen()
	if !sameEntries(snapshot(t, g.r), want) {
		t.Fatal("replay differs")
	}
}

// liveLeadAgain launches the stopped lead of g again and shows it ready.
func liveLeadAgain(t *testing.T, g *coordRig) uint64 {
	t.Helper()
	in, err := g.r.BeginLaunch(g.lead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.RecordOutcome(g.lead, in.ID, in.Epoch, OutcomeLaunched, UnitEvidence{InvocationID: "i2"}); err != nil {
		t.Fatal(err)
	}
	g.set(g.lead, StateReady)
	return in.Epoch
}

// TestCoordinatorNeedsALiveParent tries the launch of a coordinator under a
// lead in every shown state and intent condition.
func TestCoordinatorNeedsALiveParent(t *testing.T) {
	c := coord(t, "proj", "feat")
	for _, ic := range intentCases {
		for _, st := range allStates {
			r, dir, reachable := reach(t, ic.name, ic.launch, ic.outcome, st)
			if !reachable {
				continue
			}
			what := ic.name + ", parent " + string(st)
			if _, err := r.Define(c, ManagerAtaniq, Requested{}); err != nil {
				t.Fatal(err)
			}
			size := journalSize(t, dir)
			in, err := r.BeginLaunch(c)
			live := st == StateStarting || st == StateReady || st == StateDegraded
			// A lead that was never launched is not live, shown ready or not,
			// and neither is one whose launch failed or was abandoned.
			if live && ic.launch && ic.outcome != OutcomeFailed && ic.outcome != OutcomeAbandoned {
				if err != nil {
					t.Errorf("%s: refused: %v", what, err)
					continue
				}
				if in.ParentEpoch != 1 {
					t.Errorf("%s: parent epoch %d", what, in.ParentEpoch)
				}
			} else {
				if !errors.Is(err, ErrLaunchParentState) || !errors.Is(err, ErrLaunchRefused) {
					t.Errorf("%s: err = %v, want ErrLaunchParentState", what, err)
				}
				if journalSize(t, dir) != size {
					t.Errorf("%s: a refused launch wrote", what)
				}
			}
		}
	}
}

func TestCoordinatorParentRules(t *testing.T) {
	g := newCoordRig(t)
	other := coord(t, "ghost", "feat")
	if _, err := g.r.Define(other, ManagerAtaniq, Requested{}); err != nil {
		t.Fatal(err)
	}
	g.refused("parent not defined", func() error { _, err := g.r.BeginLaunch(other); return err }, ErrLaunchNoParent, ErrLaunchRefused)
	tm := lead(t, "tm")
	if _, err := g.r.Define(tm, ManagerTmux, goodProfile()); err != nil {
		t.Fatal(err)
	}
	g.set(tm, StateReady) // a tmux-run lead may be shown ready; ataniq still does not launch under it
	otm := coord(t, "tm", "feat")
	if _, err := g.r.Define(otm, ManagerAtaniq, Requested{}); err != nil {
		t.Fatal(err)
	}
	g.refused("parent run by tmux-manager", func() error { _, err := g.r.BeginLaunch(otm); return err }, ErrLaunchParentManager, ErrLaunchRefused)
	// The coordinator itself must be run by ataniq, and have no pending intent.
	ctm := coord(t, "proj", "tmuxed")
	if _, err := g.r.Define(ctm, ManagerTmux, Requested{}); err != nil {
		t.Fatal(err)
	}
	g.refused("coordinator run by tmux-manager", func() error { _, err := g.r.BeginLaunch(ctm); return err }, ErrLaunchManager, ErrLaunchRefused)
	in, err := g.r.BeginLaunch(g.coord)
	if err != nil {
		t.Fatal(err)
	}
	g.refused("pending intent", func() error { _, err := g.r.BeginLaunch(g.coord); return err }, ErrLaunchPending, ErrLaunchRefused)
	if _, err := g.r.RecordOutcome(g.coord, in.ID, 1, OutcomeLaunched, UnitEvidence{}); err != nil {
		t.Fatal(err)
	}
	g.refused("live coordinator", func() error { _, err := g.r.BeginLaunch(g.coord); return err }, ErrLaunchState, ErrLaunchRefused)
	// The parent of a coordinator is the lead of its project, not another
	// project's lead and not the supervisor.
	if got := g.coord.Parent(); got != g.lead {
		t.Fatalf("parent %q", got)
	}
	// A coordinator of a project cannot be launched under another project's
	// live lead.
	liveLead(t, g.r, lead(t, "ghost"))
	g.set(g.coord, StateStopped)
	if _, err := g.r.BeginLaunch(other); err != nil {
		t.Fatalf("launch of ghost's coordinator under ghost's lead: %v", err)
	}
}

// TestLeadLaunchWaitsForItsCoordinators is the other direction: a lead is not
// launched again while a coordinator recorded under an earlier epoch is not
// proven stopped.
func TestLeadLaunchWaitsForItsCoordinators(t *testing.T) {
	type cond struct {
		name  string
		build func(g *coordRig)
		ok    bool
	}
	conds := []cond{
		{"never launched", func(g *coordRig) {}, true},
		{"starting, pending", func(g *coordRig) {
			if _, err := g.r.BeginLaunch(g.coord); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"starting", func(g *coordRig) { g.launch() }, false},
		{"ready", func(g *coordRig) { g.launch(); g.set(g.coord, StateReady) }, false},
		{"degraded", func(g *coordRig) { g.launch(); g.set(g.coord, StateDegraded) }, false},
		{"stalled", func(g *coordRig) { g.launch(); g.set(g.coord, StateStalled) }, false},
		{"stopping", func(g *coordRig) { g.launch(); g.set(g.coord, StateStopping) }, false},
		{"unknown", func(g *coordRig) { g.launch(); g.set(g.coord, StateUnknown) }, false},
		{"stopped", func(g *coordRig) { g.launch(); g.set(g.coord, StateStopped) }, true},
		{"ended", func(g *coordRig) { g.launch(); g.set(g.coord, StateEnded) }, true},
		{"desired after a stop", func(g *coordRig) {
			g.launch()
			g.set(g.coord, StateStopped)
			g.set(g.coord, StateDesired)
		}, true},
		{"stopped after a stall", func(g *coordRig) { g.launch(); g.set(g.coord, StateStalled); g.set(g.coord, StateStopped) }, true},
		{"unknown, then proven", func(g *coordRig) { g.launch(); g.set(g.coord, StateUnknown); g.set(g.coord, StateEnded) }, true},
	}
	for _, c := range conds {
		t.Run(c.name, func(t *testing.T) {
			g := newCoordRig(t)
			c.build(g)
			g.set(g.lead, StateStopped)
			if c.ok {
				if in, err := g.r.BeginLaunch(g.lead); err != nil || in.Epoch != 2 {
					t.Fatalf("launch of the lead: %+v %v", in, err)
				}
				return
			}
			g.refused("launch of the lead", func() error { _, err := g.r.BeginLaunch(g.lead); return err },
				ErrLaunchCoordinator, ErrLaunchRefused)
			// No other launch error stands in for it.
			if err := func() error { _, err := g.r.BeginLaunch(g.lead); return err }(); errors.Is(err, ErrLaunchState) {
				t.Fatalf("refused for the wrong reason: %v", err)
			}
			// Proving the coordinator stopped admits the lead.
			if e, _ := g.r.Get(g.coord); e.Intent.Outcome == OutcomePending {
				if _, err := g.r.RecordOutcome(g.coord, e.Intent.ID, e.Epoch, OutcomeFailed, UnitEvidence{}); err != nil {
					t.Fatal(err)
				}
			}
			e, _ := g.r.Get(g.coord)
			if e.Shown.State == StateUnknown {
				if _, err := g.r.ResolveUnknown(g.coord, ownerDecision(e.Epoch, StateStopped)); err != nil {
					t.Fatal(err)
				}
			} else {
				g.set(g.coord, StateStopped)
			}
			if in, err := g.r.BeginLaunch(g.lead); err != nil || in.Epoch != 2 {
				t.Fatalf("launch of the lead after the proof: %+v %v", in, err)
			}
		})
	}
}

func TestLeadLaunchIgnoresOtherCoordinators(t *testing.T) {
	g := newCoordRig(t)
	// A coordinator of another lead, live, does not hold this lead.
	liveLead(t, g.r, lead(t, "other"))
	oc := coord(t, "other", "feat")
	if _, err := g.r.Define(oc, ManagerAtaniq, Requested{}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.BeginLaunch(oc); err != nil {
		t.Fatal(err)
	}
	// A project whose name holds the other's name as a prefix, or the feature
	// as a project, is not the same lead's coordinator.
	for _, p := range []string{"proj-x", "pro", "feat"} {
		l := lead(t, p)
		liveLead(t, g.r, l)
		cc := coord(t, p, "feat")
		if _, err := g.r.Define(cc, ManagerAtaniq, Requested{}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.r.BeginLaunch(cc); err != nil {
			t.Fatal(err)
		}
	}
	g.set(g.lead, StateStopped)
	if in, err := g.r.BeginLaunch(g.lead); err != nil || in.Epoch != 2 {
		t.Fatalf("launch: %+v %v", in, err)
	}
	// With two coordinators of the lead, one unproven holds it.
	g2 := newCoordRig(t)
	second := coord(t, "proj", "second")
	if _, err := g2.r.Define(second, ManagerAtaniq, Requested{}); err != nil {
		t.Fatal(err)
	}
	g2.launch()
	g2.set(g2.coord, StateStopped)
	in, err := g2.r.BeginLaunch(second)
	if err != nil {
		t.Fatal(err)
	}
	g2.set(g2.lead, StateStopped)
	g2.refused("one of two coordinators live", func() error { _, err := g2.r.BeginLaunch(g2.lead); return err }, ErrLaunchCoordinator)
	if _, err := g2.r.RecordOutcome(second, in.ID, 1, OutcomeFailed, UnitEvidence{}); err != nil {
		t.Fatal(err)
	}
	g2.set(second, StateStopped)
	if _, err := g2.r.BeginLaunch(g2.lead); err != nil {
		t.Fatalf("both proven: %v", err)
	}
}

// TestCoordinatorInstanceNumberIsNeverReused: failed launches and reopens
// never give an instance number out twice.
func TestCoordinatorInstanceNumberIsNeverReused(t *testing.T) {
	g := newCoordRig(t)
	seen := map[string]bool{}
	for want := uint64(1); want <= 6; want++ {
		in, err := g.r.BeginLaunch(g.coord)
		if err != nil {
			t.Fatalf("launch %d: %v", want, err)
		}
		if in.Epoch != want || seen[in.Identity] || in.Identity != g.coord.Identity(1, want) {
			t.Fatalf("instance %d: %+v", want, in)
		}
		seen[in.Identity] = true
		switch want % 3 {
		case 0: // a failed launch, then the proof
			if _, err := g.r.RecordOutcome(g.coord, in.ID, want, OutcomeFailed, UnitEvidence{}); err != nil {
				t.Fatal(err)
			}
		case 1: // abandoned
			if _, err := g.r.RecordOutcome(g.coord, in.ID, want, OutcomeAbandoned, UnitEvidence{}); err != nil {
				t.Fatal(err)
			}
		default:
			if _, err := g.r.RecordOutcome(g.coord, in.ID, want, OutcomeLaunched, UnitEvidence{}); err != nil {
				t.Fatal(err)
			}
		}
		// A refused launch takes no number.
		if _, err := g.r.BeginLaunch(g.coord); err == nil {
			t.Fatal("a second launch was admitted")
		}
		g.set(g.coord, StateStopped)
		if want%2 == 0 {
			g.reopen()
		}
		if e, _ := g.r.Get(g.coord); e.Epoch != want {
			t.Fatalf("epoch %d after instance %d", e.Epoch, want)
		}
	}
	// A launch that is refused for the parent takes no number either.
	g.set(g.lead, StateStopped)
	g.refused("parent stopped", func() error { _, err := g.r.BeginLaunch(g.coord); return err }, ErrLaunchParentState)
	if e, _ := g.r.Get(g.coord); e.Epoch != 6 {
		t.Fatalf("epoch %d", e.Epoch)
	}
}

func TestCoordinatorStalled(t *testing.T) {
	g := newCoordRig(t)
	g.launch()
	for _, from := range []State{StateStarting, StateReady, StateDegraded} {
		if from != StateStarting {
			g.set(g.coord, from)
		}
		g.set(g.coord, StateStalled)
		g.refused("launch from stalled", func() error { _, err := g.r.BeginLaunch(g.coord); return err }, ErrLaunchState, ErrLaunchRefused)
		// stalled does not stall again, and desired does not launder it.
		g.refused("stalled to stalled", func() error { _, err := g.r.SetState(g.coord, setState(StateStalled, 1)); return err }, ErrStateTransition)
		g.refused("stalled to desired", func() error { _, err := g.r.SetState(g.coord, setState(StateDesired, 1)); return err }, ErrStateTransition)
		g.refused("resolve from stalled", func() error { _, err := g.r.ResolveUnknown(g.coord, ownerDecision(1, StateStopped)); return err }, ErrStateTransition)
		g.refused("stalled without proof to stopped", func() error { _, err := g.r.SetState(g.coord, setState(StateStopped, 1)); return err }, ErrProofRequired)
	}
	// stalled is left to ready, and again.
	g.set(g.coord, StateReady)
	g.set(g.coord, StateStalled)
	g.set(g.coord, StateUnknown)
	g.refused("unknown to stalled", func() error { _, err := g.r.SetState(g.coord, setState(StateStalled, 1)); return err }, ErrStateTransition)
	// The replacement rule: the next instance waits for the proof.
	g.set(g.coord, StateStopped)
	g.refused("stalled for a stopped coordinator", func() error { _, err := g.r.SetState(g.coord, setState(StateStalled, 1)); return err }, ErrStateTransition)
	if in, err := g.r.BeginLaunch(g.coord); err != nil || in.Epoch != 2 {
		t.Fatalf("launch after the proof: %+v %v", in, err)
	}
	// Not for a lead, from any state.
	g.refused("stalled lead", func() error { _, err := g.r.SetState(g.lead, setState(StateStalled, 1)); return err }, ErrStateTransition)
	sup := leadrole.Supervisor()
	if _, err := g.r.Define(sup, ManagerAtaniq, goodProfile()); err != nil {
		t.Fatal(err)
	}
	g.refused("stalled supervisor", func() error { _, err := g.r.SetState(sup, setState(StateStalled, 0)); return err }, ErrStateTransition)
	// The shown state is stored and replayed.
	g.set(g.coord, StateStalled)
	g.reopen()
	if e, _ := g.r.Get(g.coord); e.Shown.State != StateStalled {
		t.Fatalf("after replay: %+v", e.Shown)
	}
}

func TestCoordinatorStopped(t *testing.T) {
	g := newCoordRig(t)
	stopped := func(n uint64) bool {
		t.Helper()
		b, err := g.r.CoordinatorStopped(g.coord, n)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// Never launched: instance 1 does not exist, so nothing is proven.
	if stopped(1) || stopped(7) {
		t.Fatal("proven stopped before any launch")
	}
	g.launch()
	for _, st := range []State{StateStarting, StateReady, StateDegraded, StateStalled, StateStopping, StateUnknown} {
		if st != StateStarting {
			g.set(g.coord, st)
		}
		if stopped(1) {
			t.Fatalf("proven stopped while %s", st)
		}
	}
	g.set(g.coord, StateEnded)
	if !stopped(1) || stopped(2) {
		t.Fatal("ended with proof: instance 1 only")
	}
	g.set(g.coord, StateDesired) // desired is not a proof for instance 1 any more
	if stopped(1) || stopped(2) {
		t.Fatal("desired is not proven stopped")
	}
	if _, err := g.r.BeginLaunch(g.coord); err != nil {
		t.Fatal(err)
	}
	// Instance 2 exists, so instance 1 is behind it, whatever the state.
	if !stopped(1) || stopped(2) || stopped(3) {
		t.Fatal("a later instance implies the earlier ones")
	}
	// A proof text is what counts: owner evidence resolved through
	// ResolveUnknown is a proof too.
	g.set(g.coord, StateUnknown)
	if _, err := g.r.ResolveUnknown(g.coord, ownerDecision(2, StateStopped)); err == nil {
		t.Fatal("ResolveUnknown with a pending intent")
	}
	if e, _ := g.r.Get(g.coord); true {
		if _, err := g.r.RecordOutcome(g.coord, e.Intent.ID, 2, OutcomeFailed, UnitEvidence{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.r.ResolveUnknown(g.coord, ownerDecision(2, StateStopped)); err != nil {
		t.Fatal(err)
	}
	if !stopped(2) || stopped(3) {
		t.Fatal("owner decision")
	}
	g.reopen()
	if !stopped(2) || !stopped(1) || stopped(3) {
		t.Fatal("after replay")
	}
	// Inputs.
	for name, f := range map[string]func() error{
		"lead":       func() error { _, err := g.r.CoordinatorStopped(g.lead, 1); return err },
		"supervisor": func() error { _, err := g.r.CoordinatorStopped(leadrole.Supervisor(), 1); return err },
		"zero role":  func() error { _, err := g.r.CoordinatorStopped(leadrole.Role{}, 1); return err },
		"instance 0": func() error { _, err := g.r.CoordinatorStopped(g.coord, 0); return err },
	} {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := g.r.CoordinatorStopped(coord(t, "proj", "nope"), 1); !errors.Is(err, ErrNoRole) {
		t.Errorf("undefined: %v", err)
	}
	g.r.Close()
	if _, err := g.r.CoordinatorStopped(g.coord, 1); !errors.Is(err, ErrClosed) {
		t.Errorf("closed: %v", err)
	}
}

func TestCoordinatorsListing(t *testing.T) {
	g := newCoordRig(t)
	for _, f := range []string{"zeta", "alpha", "mid"} {
		if _, err := g.r.Define(coord(t, "proj", f), ManagerAtaniq, Requested{}); err != nil {
			t.Fatal(err)
		}
	}
	liveLead(t, g.r, lead(t, "other"))
	if _, err := g.r.Define(coord(t, "other", "feat"), ManagerAtaniq, Requested{}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.Define(lead(t, "pro"), ManagerAtaniq, goodProfile()); err != nil {
		t.Fatal(err)
	}
	got, err := g.r.Coordinators(g.lead)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Role.String())
	}
	if want := "proj/coord/alpha proj/coord/feat proj/coord/mid proj/coord/zeta"; strings.Join(names, " ") != want {
		t.Fatalf("listing %q, want %q", names, want)
	}
	// The listing is a copy.
	got[0].Requested.Model = "changed"
	if again, _ := g.r.Coordinators(g.lead); again[0].Requested.Model == "changed" {
		t.Fatal("listing shares memory")
	}
	if none, err := g.r.Coordinators(lead(t, "pro")); err != nil || len(none) != 0 {
		t.Fatalf("lead without coordinators: %v %v", none, err)
	}
	for _, role := range []leadrole.Role{g.coord, leadrole.Supervisor(), {}} {
		if _, err := g.r.Coordinators(role); !errors.Is(err, ErrInvalid) {
			t.Errorf("Coordinators(%q): %v", role, err)
		}
	}
	// The list of roles holds them too, in the canonical order.
	all := snapshot(t, g.r)
	if len(all) != 8 {
		t.Fatalf("%d roles", len(all))
	}
	g.r.Close()
	if _, err := g.r.Coordinators(g.lead); !errors.Is(err, ErrClosed) {
		t.Errorf("closed: %v", err)
	}
}

// appendRecord returns data followed by one record with a valid chain.
func appendRecord(t *testing.T, data []byte, kind string, role leadrole.Role, body any) []byte {
	t.Helper()
	lines := journalLines(data)
	line := craft(t, lines, len(lines), uint64(len(lines)), kind, role, body)
	return append(append([]byte(nil), data...), line...)
}

// TestCoordinatorJournalCorruption: every rule a coordinator record must
// keep is checked on replay, and a journal that breaks one fails Open naming
// the line.
func TestCoordinatorJournalCorruption(t *testing.T) {
	g := newCoordRig(t)
	g.launch()
	g.set(g.coord, StateReady)
	data, _ := os.ReadFile(filepath.Join(g.dir, FileName))
	lines := journalLines(data)
	// 0 header, 1 define lead, 2 begin lead, 3 outcome lead, 4 ready lead,
	// 5 define coord, 6 begin coord, 7 outcome coord, 8 ready coord.
	if len(lines) != 9 || !bytes.Contains(lines[6], []byte(kindLaunchBegin)) {
		t.Fatalf("unexpected journal of %d lines", len(lines))
	}
	c, l := g.coord, g.lead
	prefix := func(n int) []byte { return bytes.Join(lines[:n], nil) }
	digest := Requested{Provider: ProviderClaude, Model: "fable"}.Digest()
	begin := func(role leadrole.Role, epoch, parentEpoch uint64, identity string) *beginPayload {
		d := digest
		if role.Kind() == leadrole.KindLead {
			d = goodProfile().Digest()
		}
		return &beginPayload{IntentID: strings.Repeat("ab", 16), Epoch: epoch, UnitName: role.UnitName(), ProfileDigest: d, ParentEpoch: parentEpoch, Identity: identity}
	}
	st := func(epoch uint64, state State, proof string) *statePayload {
		return &statePayload{Epoch: epoch, State: state, Evidence: EvidenceObservation, Reason: Reason{Code: "x"}, Proof: proof}
	}
	define := func(r Requested) *definePayload { return &definePayload{Manager: ManagerAtaniq, Requested: r} }

	// The journal with the coordinator stalled (line 10), with the lead then
	// stopped (11) and the coordinator then stopped (12).
	stalled := appendRecord(t, data, kindState, c, st(1, StateStalled, ""))
	leadStopped := appendRecord(t, stalled, kindState, l, st(1, StateStopped, "p"))
	bothStopped := appendRecord(t, leadStopped, kindState, c, st(1, StateStopped, "p"))
	// The journal of a lead alone, then a coordinator defined.
	leadOnly := prefix(5)
	orphan := appendRecord(t, prefix(1), kindDefine, c, define(Requested{Provider: ProviderClaude, Model: "fable"}))

	cases := []struct {
		name string
		data []byte
		line int
		why  string // text the reason must hold
	}{
		{"coordinator profile without a model", appendRecord(t, prefix(5), kindDefine, c, define(Requested{Provider: ProviderClaude})), 6, "requested model is empty"},
		{"coordinator profile without a provider", appendRecord(t, prefix(5), kindDefine, c, define(Requested{Model: "m"})), 6, "provider"},
		{"coordinator profile with another provider", appendRecord(t, prefix(5), kindDefine, c, define(Requested{Provider: "gpt", Model: "m"})), 6, "provider"},
		{"lead profile with a missing field", appendRecord(t, prefix(1), kindDefine, l, define(Requested{Provider: ProviderClaude, Model: "m"})), 2, "requested"},
		{"coordinator launch without a parent epoch", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 0, "")), 7, "without a parent epoch"},
		{"coordinator launch without an identity", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 1, "")), 7, "identity"},
		{"coordinator launch with another instance in the identity", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 1, c.Identity(1, 2))), 7, "identity"},
		{"coordinator launch with another parent epoch in the identity", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 1, c.Identity(2, 1))), 7, "identity"},
		{"coordinator launch with an identity and a suffix", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 1, c.Identity(1, 1)+":x")), 7, "identity"},
		{"coordinator launch with a stale parent epoch", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 2, c.Identity(2, 1))), 7, "stale epoch"},
		{"coordinator launch with the identity of another feature", appendRecord(t, prefix(6), kindLaunchBegin, c, begin(c, 1, 1, coord(t, "proj", "other").Identity(1, 1))), 7, "identity"},
		{"lead launch with a parent epoch", appendRecord(t, appendRecord(t, leadOnly, kindState, l, st(1, StateStopped, "p")), kindLaunchBegin, l, begin(l, 2, 1, "")), 7, "not a coordinator"},
		{"lead launch with an identity", appendRecord(t, appendRecord(t, leadOnly, kindState, l, st(1, StateStopped, "p")), kindLaunchBegin, l, begin(l, 2, 0, c.Identity(1, 1))), 7, "not a coordinator"},
		{"coordinator launch with no parent in the journal", appendRecord(t, orphan, kindLaunchBegin, c, begin(c, 1, 1, c.Identity(1, 1))), 3, "parent lead of the coordinator is not defined"},
		{"stalled for a lead", appendRecord(t, prefix(5), kindState, l, st(1, StateStalled, "")), 6, "coordinators only"},
		{"stalled from desired", appendRecord(t, prefix(6), kindState, c, st(0, StateStalled, "")), 7, "entered only from"},
		{"stalled from stalled", appendRecord(t, stalled, kindState, c, st(1, StateStalled, "")), 11, "entered only from"},
		{"launch from stalled", appendRecord(t, stalled, kindLaunchBegin, c, begin(c, 2, 1, c.Identity(1, 2))), 11, "shown state does not admit"},
		{"stalled to desired", appendRecord(t, stalled, kindState, c, st(1, StateDesired, "")), 11, "desired is set only from"},
		{"stalled to stopped without proof", appendRecord(t, stalled, kindState, c, st(1, StateStopped, "")), 11, "proof"},
		{"lead launch while a coordinator is stalled", appendRecord(t, leadStopped, kindLaunchBegin, l, begin(l, 2, 0, "")), 12, "not proven stopped"},
		{"coordinator launch while its parent is stopped", appendRecord(t, bothStopped, kindLaunchBegin, c, begin(c, 2, 1, c.Identity(1, 2))), 13, "not shown starting, ready or degraded"},
	}
	for _, tc := range cases {
		err := openErr(t, tc.data)
		var ce *CorruptError
		if !errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) {
			t.Errorf("%s: Open = %v, want ErrCorrupt", tc.name, err)
			continue
		}
		if ce.Line != tc.line {
			t.Errorf("%s: error names line %d, want %d (%v)", tc.name, ce.Line, tc.line, err)
		}
		if !strings.Contains(ce.Reason, tc.why) {
			t.Errorf("%s: reason %q does not hold %q", tc.name, ce.Reason, tc.why)
		}
		for _, other := range otherErrors {
			if errors.Is(err, other) {
				t.Errorf("%s: Open error also matches %v", tc.name, other)
			}
		}
	}
	// The same records, valid, open: the cases above fail for their rule only.
	valid := []struct {
		name string
		data []byte
	}{
		{"the journal itself", data},
		{"coordinator stalled", stalled},
		{"lead stopped, then the coordinator", bothStopped},
		{"lead launched again after both stopped", appendRecord(t, bothStopped, kindLaunchBegin, l, begin(l, 2, 0, ""))},
	}
	for _, v := range valid {
		if err := openErr(t, v.data); err != nil {
			t.Errorf("%s: %v", v.name, err)
		}
	}
}
