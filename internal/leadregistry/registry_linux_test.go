package leadregistry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadrole"
)

func TestOpenCreatesFolderAndFile(t *testing.T) {
	old := unix.Umask(0)
	defer unix.Umask(old)
	dir := filepath.Join(t.TempDir(), "a", "b", "reg")
	r, err := Open(dir, testOpts(1))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("folder: %v %v", st, err)
	}
	fs, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || fs.Mode().Perm() != 0o600 || !fs.Mode().IsRegular() {
		t.Fatalf("file: %v %v", fs, err)
	}
	if rev, _ := r.Revision(); rev != 0 {
		t.Fatalf("revision of an empty registry = %d", rev)
	}
	if l, _ := r.List(); len(l) != 0 {
		t.Fatalf("roles in an empty registry: %v", l)
	}
	if r.Recovery() != (Recovery{}) {
		t.Fatalf("recovery %+v", r.Recovery())
	}
}

func TestDefineNewRole(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	e := ok(r.Define(role, ManagerAtaniq, goodProfile()))
	if e.Role != role || e.Manager != ManagerAtaniq || e.Requested != goodProfile() {
		t.Fatalf("entry %+v", e)
	}
	if e.Epoch != 0 || e.Desired != DesiredStopped || e.Intent != nil || e.Unit != (UnitEvidence{}) {
		t.Fatalf("entry %+v", e)
	}
	if e.Shown.State != StateStopped || e.Shown.Reason.Code != "defined" || e.Shown.Evidence != EvidenceOwner || e.Shown.At.IsZero() {
		t.Fatalf("shown %+v", e.Shown)
	}
	if e.Observed != (Observed{}) {
		t.Fatalf("observed %+v", e.Observed)
	}
	if e.Role.Project() != "proj" || e.Role.Kind() != leadrole.KindLead {
		t.Fatal("project and kind come from the role")
	}
	if rev, _ := r.Revision(); rev != 1 {
		t.Fatalf("revision %d", rev)
	}
	got, err := r.Get(role)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := r.Get(lead(t, "other")); !errors.Is(err, ErrNoRole) {
		t.Fatalf("Get unknown: %v", err)
	}
}

func TestDefineUpdateKeepsRuntimeState(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	ok(r.SetDesired(role, DesiredRunning))
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{InvocationID: "inv"}))
	ok(r.SetObserved(role, 1, Observed{Model: Seen("m")}))
	before, _ := r.Get(role)
	p2 := goodProfile()
	p2.Model = "model-b"
	after := ok(r.Define(role, ManagerTmux, p2))
	if after.Manager != ManagerTmux || after.Requested != p2 {
		t.Fatalf("update not applied: %+v", after)
	}
	if after.profileDigest != p2.Digest() || after.foreignProfile {
		t.Fatalf("the profile's identity is not the new profile's: %s %v", after.profileDigest, after.foreignProfile)
	}
	after.Manager, after.Requested = before.Manager, before.Requested
	after.profileDigest, after.foreignProfile = before.profileDigest, before.foreignProfile
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("update changed more than manager and profile:\n%+v\n%+v", after, before)
	}
}

func TestRequestedNeverLeaksIntoObserved(t *testing.T) {
	ok := okf(t)
	if reflect.TypeOf(Requested{}) == reflect.TypeOf(Observed{}) {
		t.Fatal("Requested and Observed must be distinct types")
	}
	ot := reflect.TypeOf(Observed{})
	for i := 0; i < ot.NumField(); i++ {
		if ot.Field(i).Type != reflect.TypeOf(Field{}) {
			t.Fatalf("observed field %s is not a Field", ot.Field(i).Name)
		}
	}
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	e, _ := r.Get(role)
	if e.Observed != (Observed{}) {
		t.Fatalf("define copied requested into observed: %+v", e.Observed)
	}
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	e, _ = r.Get(role)
	if e.Observed != (Observed{}) {
		t.Fatalf("launch copied requested into observed: %+v", e.Observed)
	}
	e = ok(r.SetObserved(role, in.Epoch, Observed{Model: Seen("model-actual"), Version: Seen("2.1.1")}))
	want := Observed{Model: Seen("model-actual"), Version: Seen("2.1.1")}
	if e.Observed != want {
		t.Fatalf("observed %+v", e.Observed)
	}
	if e.Observed.Effort.Observed || e.Observed.Provider.Observed || e.Observed.PermissionMode.Observed || e.Observed.AccessMode.Observed {
		t.Fatalf("unobserved fields claim an observation: %+v", e.Observed)
	}
	// A later patch merges; unobserved fields in a patch leave values alone.
	e = ok(r.SetObserved(role, in.Epoch, Observed{Effort: Seen("low")}))
	want.Effort = Seen("low")
	if e.Observed != want {
		t.Fatalf("merge: %+v", e.Observed)
	}
	// Updating the requested profile never touches observed.
	p2 := goodProfile()
	p2.Effort = "max"
	e = ok(r.Define(role, ManagerAtaniq, p2))
	if e.Observed != want {
		t.Fatalf("define changed observed: %+v", e.Observed)
	}
	// A new epoch starts with nothing observed.
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeFailed, UnitEvidence{}))
	ok(r.SetState(role, stopProof(1)))
	if _, err := r.BeginLaunch(role); err != nil {
		t.Fatal(err)
	}
	e, _ = r.Get(role)
	if e.Observed != (Observed{}) || e.Epoch != 2 {
		t.Fatalf("new epoch kept observed: %+v", e)
	}
	// Invalid observed values.
	for _, bad := range []Observed{
		{},
		{Model: Field{Value: "x"}},
		{Model: Field{Observed: true}},
		{Model: Seen("a\nb")},
		{Model: Seen(strings.Repeat("x", MaxFieldBytes+1))},
	} {
		if _, err := r.SetObserved(role, 2, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetObserved(%+v) = %v", bad, err)
		}
	}
}

func TestInvalidInputs(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	long := strings.Repeat("x", MaxFieldBytes+1)
	mut := func(f func(*Requested)) Requested {
		p := goodProfile()
		f(&p)
		return p
	}
	for name, p := range map[string]Requested{
		"provider":    mut(func(p *Requested) { p.Provider = "gpt" }),
		"no provider": mut(func(p *Requested) { p.Provider = "" }),
		"version":     mut(func(p *Requested) { p.Version = "" }),
		"model":       mut(func(p *Requested) { p.Model = long }),
		"effort":      mut(func(p *Requested) { p.Effort = "a\x00b" }),
		"permission":  mut(func(p *Requested) { p.PermissionMode = "a\nb" }),
		"access":      mut(func(p *Requested) { p.AccessMode = "\xff" }),
	} {
		before := journalSize(t, filepath.Dir(r.f.Name()))
		if _, err := r.Define(role, ManagerAtaniq, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if journalSize(t, filepath.Dir(r.f.Name())) != before {
			t.Errorf("%s: a refused call wrote", name)
		}
	}
	if _, err := r.Define(role, "other", goodProfile()); !errors.Is(err, ErrInvalid) {
		t.Errorf("manager: %v", err)
	}
	if _, err := r.Define(leadrole.Role{}, ManagerAtaniq, goodProfile()); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero role: %v", err)
	}
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	if _, err := r.SetDesired(role, "paused"); !errors.Is(err, ErrInvalid) {
		t.Errorf("desired: %v", err)
	}
	if _, err := r.SetDesired(lead(t, "nope"), DesiredRunning); !errors.Is(err, ErrNoRole) {
		t.Errorf("desired on unknown role: %v", err)
	}
	if _, err := r.BeginLaunch(lead(t, "nope")); !errors.Is(err, ErrNoRole) {
		t.Errorf("begin on unknown role: %v", err)
	}
	bad := []StateChange{
		{Epoch: 0, State: "weird", Evidence: EvidenceOwner, Reason: Reason{Code: "x"}},
		{Epoch: 0, State: StateReady, Evidence: "guess", Reason: Reason{Code: "x"}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "Bad"}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "-x"}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "x-"}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "x--y"}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: strings.Repeat("a", 65)}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "x", Detail: strings.Repeat("d", MaxReasonDetailBytes+1)}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "x", Detail: "a\nb"}},
		{Epoch: 0, State: StateReady, Evidence: EvidenceOwner, Reason: Reason{Code: "x"}, Proof: strings.Repeat("p", MaxProofBytes+1)},
		{Epoch: 0, State: StateStarting, Evidence: EvidenceObservation, Reason: Reason{Code: "x"}},
	}
	for i, c := range bad {
		if _, err := r.SetState(role, c); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad state change %d: %v", i, err)
		}
	}
	if _, err := r.RecordOutcome(role, "not-an-id", 0, OutcomeLaunched, UnitEvidence{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad intent id: %v", err)
	}
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordOutcome(role, in.ID, 1, OutcomePending, UnitEvidence{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("pending outcome: %v", err)
	}
	if _, err := r.RecordOutcome(role, in.ID, 1, "done", UnitEvidence{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad outcome: %v", err)
	}
	if _, err := r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{ControlGroupPath: strings.Repeat("p", MaxControlGroupPathBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("long cgroup path: %v", err)
	}
	if _, err := r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{BootID: "a\tb"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("control char in evidence: %v", err)
	}
}

func TestEpochMonotonicAndNeverReused(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	ids := map[string]bool{}
	for want := uint64(1); want <= 6; want++ {
		in, err := r.BeginLaunch(role)
		if err != nil {
			t.Fatal(err)
		}
		if in.Epoch != want || in.Outcome != OutcomePending || in.UnitName != "ataniq-lead-proj.service" ||
			in.ProfileDigest != goodProfile().Digest() || in.At.IsZero() || !in.OutcomeAt.IsZero() {
			t.Fatalf("intent %+v", in)
		}
		if len(in.ID) != 32 || ids[in.ID] {
			t.Fatalf("intent id %q (reused or malformed)", in.ID)
		}
		ids[in.ID] = true
		e, _ := r.Get(role)
		if e.Epoch != want || e.Shown.State != StateStarting || *e.Intent != in {
			t.Fatalf("entry %+v", e)
		}
		// Alternate failed and launched launches; epochs advance either way.
		outcome := OutcomeFailed
		if want%2 == 0 {
			outcome = OutcomeLaunched
		}
		ok(r.RecordOutcome(role, in.ID, want, outcome, UnitEvidence{InvocationID: "i"}))
		ok(r.SetState(role, stopProof(want)))
	}
	// The epoch survives a reopen and keeps growing.
	dir := filepath.Dir(r.f.Name())
	r.Close()
	r2 := openReg(t, dir)
	in, err := r2.BeginLaunch(role)
	if err != nil || in.Epoch != 7 {
		t.Fatalf("after reopen: %+v %v", in, err)
	}
}

func TestBeginLaunchRefusals(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))

	refused := func(want error) {
		t.Helper()
		rev, _ := r.Revision()
		size := journalSize(t, dir)
		before, _ := r.Get(role)
		_, err := r.BeginLaunch(role)
		if !errors.Is(err, want) || !errors.Is(err, ErrLaunchRefused) {
			t.Fatalf("BeginLaunch = %v, want %v", err, want)
		}
		if rev2, _ := r.Revision(); rev2 != rev || journalSize(t, dir) != size {
			t.Fatal("a refused launch wrote to the journal")
		}
		if after, _ := r.Get(role); !reflect.DeepEqual(before, after) {
			t.Fatal("a refused launch changed state")
		}
	}
	// Admitted from stopped (new role).
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	// With an intent pending: refused, whatever the state is.
	refused(ErrLaunchPending)
	for _, st := range []State{StateReady, StateDegraded, StateStopping} {
		ok(r.SetState(role, setState(st, 1)))
		refused(ErrLaunchPending)
	}
	// desired cannot be set from a live state, so it cannot launder the
	// pending intent either.
	if _, err := r.SetState(role, setState(StateDesired, 1)); !errors.Is(err, ErrStateTransition) {
		t.Fatalf("desired from stopping: %v", err)
	}
	refused(ErrLaunchPending)
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{InvocationID: "i"}))
	// No intent pending: refused from every live state and from unknown, and
	// from starting.
	for _, st := range []State{StateReady, StateDegraded, StateStopping, StateUnknown} {
		ok(r.SetState(role, setState(st, 1)))
		refused(ErrLaunchState)
	}
	// Back to starting by way of a fresh launch that fails.
	ok(r.SetState(role, stopProof(1)))
	in2, err := r.BeginLaunch(role)
	if err != nil || in2.Epoch != 2 {
		t.Fatalf("launch from stopped: %+v %v", in2, err)
	}
	ok(r.RecordOutcome(role, in2.ID, 2, OutcomeFailed, UnitEvidence{}))
	e, _ := r.Get(role)
	if e.Shown.State != StateStarting {
		t.Fatalf("a failed outcome must not change the shown state: %v", e.Shown.State)
	}
	refused(ErrLaunchState) // starting without a pending intent
	// Admitted from ended, then from desired.
	ended := StateChange{Epoch: 2, State: StateEnded, Evidence: EvidenceObservation, Reason: Reason{Code: "exited"}, Proof: "unit not loaded"}
	ok(r.SetState(role, ended))
	in3, err := r.BeginLaunch(role)
	if err != nil || in3.Epoch != 3 {
		t.Fatalf("launch from ended: %+v %v", in3, err)
	}
	ok(r.RecordOutcome(role, in3.ID, 3, OutcomeAbandoned, UnitEvidence{}))
	ok(r.SetState(role, StateChange{Epoch: 3, State: StateStopped, Evidence: EvidenceObservation, Reason: Reason{Code: "unit-gone"}, Proof: "unit not loaded"}))
	ok(r.SetState(role, setState(StateDesired, 3)))
	in4, err := r.BeginLaunch(role)
	if err != nil || in4.Epoch != 4 {
		t.Fatalf("launch from desired: %+v %v", in4, err)
	}
	// A tmux-manager role is refused.
	tm := lead(t, "tm")
	ok(r.Define(tm, ManagerTmux, goodProfile()))
	if _, err := r.BeginLaunch(tm); !errors.Is(err, ErrLaunchManager) || !errors.Is(err, ErrLaunchRefused) {
		t.Fatalf("tmux-manager launch: %v", err)
	}
	// And it is admitted once the owner moves it to ataniq.
	ok(r.Define(tm, ManagerAtaniq, goodProfile()))
	if _, err := r.BeginLaunch(tm); err != nil {
		t.Fatal(err)
	}
}

func TestBeginLaunchRefusedPendingFromUnknown(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	if _, err := r.BeginLaunch(role); err != nil {
		t.Fatal(err)
	}
	ok(r.SetState(role, setState(StateUnknown, 1)))
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrLaunchPending) {
		t.Fatalf("BeginLaunch with a pending intent from unknown: %v", err)
	}
}

func TestStaleEpochRefused(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	in1, _ := r.BeginLaunch(role)
	ok(r.RecordOutcome(role, in1.ID, 1, OutcomeFailed, UnitEvidence{}))
	ok(r.SetState(role, stopProof(1)))
	in2, _ := r.BeginLaunch(role)
	if in2.Epoch != 2 {
		t.Fatal("epoch")
	}
	size := journalSize(t, dir)
	for _, c := range []StateChange{setState(StateReady, 1), setState(StateUnknown, 1), stopProof(1), setState(StateReady, 3), setState(StateReady, 0)} {
		if _, err := r.SetState(role, c); !errors.Is(err, ErrStaleEpoch) {
			t.Errorf("SetState epoch %d: %v", c.Epoch, err)
		}
	}
	for _, ep := range []uint64{0, 1, 3} {
		if _, err := r.SetObserved(role, ep, Observed{Model: Seen("m")}); !errors.Is(err, ErrStaleEpoch) {
			t.Errorf("SetObserved epoch %d: %v", ep, err)
		}
		if _, err := r.RecordOutcome(role, in2.ID, ep, OutcomeLaunched, UnitEvidence{}); !errors.Is(err, ErrStaleEpoch) {
			t.Errorf("RecordOutcome epoch %d: %v", ep, err)
		}
	}
	// The old intent id for the right epoch is the wrong intent.
	if _, err := r.RecordOutcome(role, in1.ID, 2, OutcomeLaunched, UnitEvidence{}); !errors.Is(err, ErrIntentMismatch) {
		t.Errorf("old intent: %v", err)
	}
	if _, err := r.RecordOutcome(role, strings.Repeat("0", 32), 2, OutcomeLaunched, UnitEvidence{}); !errors.Is(err, ErrIntentMismatch) {
		t.Errorf("unknown intent: %v", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("refused calls wrote")
	}
	ev := UnitEvidence{InvocationID: "inv", ControlGroupPath: "/system.slice/x", ControlGroupID: "77", BootID: "boot"}
	e := ok(r.RecordOutcome(role, in2.ID, 2, OutcomeLaunched, ev))
	if e.Unit != ev || e.Intent.Outcome != OutcomeLaunched || e.Intent.OutcomeAt.IsZero() {
		t.Fatalf("outcome: %+v", e)
	}
	if _, err := r.RecordOutcome(role, in2.ID, 2, OutcomeFailed, UnitEvidence{}); !errors.Is(err, ErrIntentResolved) {
		t.Errorf("second outcome: %v", err)
	}
	// The unit evidence belongs to its epoch: a new launch clears it.
	ok(r.SetState(role, stopProof(2)))
	if _, err := r.BeginLaunch(role); err != nil {
		t.Fatal(err)
	}
	if e, _ := r.Get(role); e.Unit != (UnitEvidence{}) {
		t.Fatalf("evidence carried into a new epoch: %+v", e.Unit)
	}
}

func TestProofRules(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	in, _ := r.BeginLaunch(role)
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{}))
	size := journalSize(t, dir)
	for _, st := range []State{StateStopped, StateEnded} {
		if _, err := r.SetState(role, StateChange{Epoch: 1, State: st, Evidence: EvidenceObservation, Reason: Reason{Code: "x"}}); !errors.Is(err, ErrProofRequired) {
			t.Errorf("%s without proof: %v", st, err)
		}
	}
	if journalSize(t, dir) != size {
		t.Fatal("refused calls wrote")
	}
	// unknown is accepted without proof for the current epoch.
	ok(r.SetState(role, StateChange{Epoch: 1, State: StateUnknown, Evidence: EvidenceObservation, Reason: Reason{Code: "inspect-failed", Detail: "bus timeout"}}))
	e := ok(r.SetState(role, StateChange{Epoch: 1, State: StateEnded, Evidence: EvidenceProvider, Reason: Reason{Code: "provider-exit"}, Proof: "proof text one"}))
	if e.Shown.Proof != "proof text one" || e.Shown.Evidence != EvidenceProvider {
		t.Fatalf("shown %+v", e.Shown)
	}
	// The proof is in the journal.
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), "proof text one") {
		t.Fatal("proof not journaled")
	}
	r.Close()
	r2 := openReg(t, dir)
	if got, _ := r2.Get(role); got.Shown.Proof != "proof text one" || got.Shown.Reason.Code != "provider-exit" {
		t.Fatalf("after replay: %+v", got.Shown)
	}
}

func TestRoleLimitAndLineBounds(t *testing.T) {
	r := openReg(t, regDir(t))
	for i := 0; i < MaxRoles; i++ {
		role := lead(t, "p"+strings.Repeat("a", i%5)+string(rune('a'+i/26%26))+string(rune('a'+i%26))+string(rune('a'+i/676)))
		if _, err := r.Define(role, ManagerAtaniq, goodProfile()); err != nil {
			t.Fatalf("role %d (%s): %v", i, role, err)
		}
	}
	rev, _ := r.Revision()
	if _, err := r.Define(lead(t, "one-too-many"), ManagerAtaniq, goodProfile()); !errors.Is(err, ErrTooManyRoles) {
		t.Fatalf("role over the cap: %v", err)
	}
	if rev2, _ := r.Revision(); rev2 != rev {
		t.Fatal("refused define wrote")
	}
	// Updating an existing role is still fine at the cap.
	l, _ := r.List()
	if len(l) != MaxRoles {
		t.Fatalf("%d roles", len(l))
	}
	if _, err := r.Define(l[0].Role, ManagerAtaniq, goodProfile()); err != nil {
		t.Fatal(err)
	}
}

// TestWorstCaseLinesFit makes every text as long as allowed with characters
// that JSON must escape, and checks the line still fits.
func TestWorstCaseLinesFit(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	q := func(n int) string { return strings.Repeat(`"`, n) }
	role := lead(t, "proj")
	p := Requested{Provider: ProviderCodex, Version: q(MaxFieldBytes), Model: q(MaxFieldBytes), Effort: q(MaxFieldBytes), PermissionMode: q(MaxFieldBytes), AccessMode: q(MaxFieldBytes)}
	ok(r.Define(role, ManagerAtaniq, p))
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	ev := UnitEvidence{InvocationID: q(MaxInvocationIDBytes), ControlGroupPath: q(MaxControlGroupPathBytes), ControlGroupID: q(MaxControlGroupIdentityBytes), BootID: q(MaxBootIdentityBytes)}
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, ev))
	ok(r.SetState(role, StateChange{Epoch: 1, State: StateStopped, Evidence: EvidenceProvider,
		Reason: Reason{Code: strings.Repeat("a", MaxReasonCodeBytes), Detail: q(MaxReasonDetailBytes)}, Proof: q(MaxProofBytes)}))
	f := Seen(q(MaxFieldBytes))
	ok(r.SetObserved(role, 1, Observed{Provider: f, Version: f, Model: f, Effort: f, PermissionMode: f, AccessMode: f}))
}

func TestEncodeLineRefusesOversize(t *testing.T) {
	big := &headerPayload{Format: strings.Repeat("x", MaxLineBytes), Version: 1}
	if _, _, err := encodeLine(genesis, 0, (&fakeClock{}).Now(), kindHeader, leadrole.Role{}, big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestClosedAndBroken(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	// A failed write poisons the registry.
	r.f.Close()
	_, err := r.SetDesired(role, DesiredRunning)
	if err == nil {
		t.Fatal("write to a closed file succeeded")
	}
	// The failed call must not read as "nothing happened".
	if !strings.Contains(err.Error(), "may or may not be on disk") {
		t.Fatalf("failed write: %v", err)
	}
	if _, err := r.SetDesired(role, DesiredRunning); !errors.Is(err, ErrBroken) || !strings.Contains(err.Error(), "may or may not be on disk") {
		t.Fatalf("after a failed write: %v", err)
	}
	if e, _ := r.Get(role); e.Desired != DesiredStopped {
		t.Fatal("a failed write changed the state in memory")
	}
	r.closed = true
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	r2 := openReg(t, dir)
	if err := r2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := r2.Define(role, ManagerAtaniq, goodProfile()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := r2.Get(role); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := r2.List(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := r2.Revision(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestSecondOpenIsRefused(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	_, err := Open(dir, testOpts(2))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: %v", err)
	}
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	r.Close()
	r2, err := Open(dir, testOpts(2))
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	defer r2.Close()
	if _, err := r2.Get(role); err != nil {
		t.Fatal(err)
	}
}

func TestIntentIDsAreRandomAndFailureWritesNothing(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r, err := Open(dir, Options{Rand: strings.NewReader("short")})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	role := lead(t, "proj")
	ok(r.Define(role, ManagerAtaniq, goodProfile()))
	size := journalSize(t, dir)
	if _, err := r.BeginLaunch(role); err == nil {
		t.Fatal("launch without randomness")
	}
	if journalSize(t, dir) != size {
		t.Fatal("wrote after a randomness failure")
	}
	// The default source gives distinct 128-bit ids.
	r2, err := Open(regDir(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	ok(r2.Define(role, ManagerAtaniq, goodProfile()))
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		in, err := r2.BeginLaunch(role)
		if err != nil || seen[in.ID] || len(in.ID) != 32 {
			t.Fatalf("%+v %v", in, err)
		}
		seen[in.ID] = true
		ok(r2.RecordOutcome(role, in.ID, in.Epoch, OutcomeFailed, UnitEvidence{}))
		ok(r2.SetState(role, stopProof(in.Epoch)))
	}
}

func TestListIsSortedAndDeepCopies(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	for _, n := range []string{"zeta", "alpha", "mid"} {
		ok(r.Define(lead(t, n), ManagerAtaniq, goodProfile()))
	}
	ok(r.Define(leadrole.Supervisor(), ManagerAtaniq, goodProfile()))
	l, _ := r.List()
	var names []string
	for _, e := range l {
		names = append(names, e.Role.String())
	}
	if want := []string{"alpha/lead", "mid/lead", "supervisor", "zeta/lead"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("order %v", names)
	}
	role := lead(t, "alpha")
	in, _ := r.BeginLaunch(role)
	e, _ := r.Get(role)
	e.Intent.Outcome = OutcomeFailed
	e.Epoch = 99
	l, _ = r.List()
	l[0].Intent.ID = "tampered"
	l[0].Epoch = 77
	again, _ := r.Get(role)
	if again.Intent.ID != in.ID || again.Intent.Outcome != OutcomePending || again.Epoch != 1 {
		t.Fatalf("a returned entry aliases the registry: %+v", again)
	}
	got := ok(r.RecordOutcome(role, in.ID, 1, OutcomeLaunched, UnitEvidence{}))
	got.Intent.ID = "x"
	if cur, _ := r.Get(role); cur.Intent.ID != in.ID {
		t.Fatal("mutation result aliases the registry")
	}
}

func TestSupervisorRole(t *testing.T) {
	ok := okf(t)
	r := openReg(t, regDir(t))
	ok(r.Define(leadrole.Supervisor(), ManagerAtaniq, goodProfile()))
	in, err := r.BeginLaunch(leadrole.Supervisor())
	if err != nil || in.UnitName != "ataniq-supervisor.service" {
		t.Fatalf("%+v %v", in, err)
	}
}
