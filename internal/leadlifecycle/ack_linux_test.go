package leadlifecycle

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// cutJournal closes the registry, appends half a line to the journal as a
// write that never returned and opens the registry again, so that it reports
// a cut.
func (r *rig) cutJournal() {
	r.t.Helper()
	noErr(r.t, r.reg.Close())
	f, err := os.OpenFile(filepath.Join(r.dir, leadregistry.FileName), os.O_WRONLY|os.O_APPEND, 0)
	noErr(r.t, err)
	_, err = f.WriteString(`{"seq":99,"time":"2026`)
	noErr(r.t, err)
	noErr(r.t, f.Close())
	r.open()
	if !r.reg.Recovery().Cut() {
		r.t.Fatal("the registry did not report a cut")
	}
}

// cutWorld is a registry with a cut, a role that runs (live) and a role that
// is stopped with its unit proven absent (gone).
func cutWorld(t *testing.T) (r *rig, live, gone leadrole.Role) {
	r = newRig(t)
	live = r.define("alpha")
	gone = r.define("beta")
	r.mustStart(live)
	r.mustStart(gone)
	_, err := r.stop(gone)
	noErr(t, err)
	r.cutJournal()
	return r, live, gone
}

func TestReconcileAcknowledgesTheCutOnceItActedOnEveryRole(t *testing.T) {
	r, live, gone := cutWorld(t)
	rep := r.reconcile()
	if !rep.Recovered || !rep.Acknowledged {
		t.Fatalf("report recovered %v acknowledged %v", rep.Recovered, rep.Acknowledged)
	}
	if r.reg.Recovery().Cut() {
		t.Fatal("the registry still reports the cut")
	}
	if rr, _ := rep.Find(live); rr.After != leadregistry.StateUnknown || rr.Reason != "registry-recovered" {
		t.Fatalf("report %+v", rr)
	}
	// The next Reconcile has nothing to acknowledge and writes nothing.
	rev := r.revision()
	if rep := r.reconcile(); rep.Recovered || rep.Acknowledged || r.revision() != rev {
		t.Fatalf("second report %+v, revision %d to %d", rep, rev, r.revision())
	}

	// A role started after the cut runs healthily. The daemon restarts: the
	// cut was acknowledged, so the second Reconcile adopts the unit and does
	// not show the role unknown again.
	e := r.mustStart(gone)
	if e.Epoch != 2 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.restart()
	if r.reg.Recovery().Cut() {
		t.Fatal("the acknowledged cut is reported again after a restart")
	}
	rep = r.reconcile()
	if rep.Recovered {
		t.Fatal("the cut was treated again")
	}
	rr, _ := rep.Find(gone)
	if rr.Action != ActionAdopted || rr.After != leadregistry.StateStarting {
		t.Fatalf("report %+v", rr)
	}
	// The role the cut made unknown stays with the owner.
	if rr, _ := rep.Find(live); rr.Action != ActionUnchanged || rr.After != leadregistry.StateUnknown {
		t.Fatalf("report %+v", rr)
	}
	if n := r.callsOf(leadunit.MethodStart); n != 3 {
		t.Fatalf("%d Start calls", n)
	}
	if got := signalsOf(r.signalCalls()); len(got) != 1 { // the stop of the setup
		t.Fatalf("signals %v", got)
	}
}

func TestReconcileDoesNotAcknowledgeWhenTheManagerIsUnavailable(t *testing.T) {
	r, _, gone := cutWorld(t)
	// The first Inspect of the pass is for a role recorded stopped.
	r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
	rep := r.reconcile()
	if !rep.Recovered || rep.Acknowledged {
		t.Fatalf("report recovered %v acknowledged %v", rep.Recovered, rep.Acknowledged)
	}
	if !r.reg.Recovery().Cut() {
		t.Fatal("the cut was acknowledged although a role could not be acted on")
	}
	if e := r.get(gone); e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "registry-recovered" {
		t.Fatalf("entry %+v", e)
	}
	// A later Reconcile in the same process does not treat the cut again.
	if rep := r.reconcile(); rep.Recovered || rep.Acknowledged {
		t.Fatalf("report %+v", rep)
	}
	// The next daemon acts on the cut again, now with the manager answering,
	// and acknowledges it.
	r.restart()
	rep = r.reconcile()
	if !rep.Recovered || !rep.Acknowledged || r.reg.Recovery().Cut() {
		t.Fatalf("report recovered %v acknowledged %v", rep.Recovered, rep.Acknowledged)
	}
}

func TestReconcileDoesNotAcknowledgeWhenAPendingIntentCannotBeInspected(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunch(role)
	noErr(t, err)
	r.cutJournal()
	r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
	rep := r.reconcile()
	if !rep.Recovered || rep.Acknowledged || !r.reg.Recovery().Cut() {
		t.Fatalf("report recovered %v acknowledged %v", rep.Recovered, rep.Acknowledged)
	}
}

// A daemon that dies between acting on the cut and acknowledging it comes
// back to a registry that still reports the cut, and treats it again,
// conservatively: a role that was started in the meantime is shown unknown.
func TestACrashBetweenActingAndAcknowledgingRepeatsTheTreatment(t *testing.T) {
	run := func(t *testing.T, crashAt int) (*rig, leadrole.Role, *crashSignal) {
		r, _, gone := cutWorld(t)
		r.plan.at = crashAt
		r.plan.armed = true
		cs := r.drive(func() { _, _ = r.ctl.Reconcile(context.Background()) })
		r.plan.armed = false
		return r, gone, cs
	}
	// A dry run numbers the points of the pass; the acknowledgement is the
	// last registry write.
	dry, _, cs := run(t, -1)
	if cs != nil {
		t.Fatalf("killed in the dry run: %+v", cs)
	}
	last := -1
	for i, l := range dry.plan.labels {
		if l == "registry write" {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("labels %v", dry.plan.labels)
	}
	if dry.reg.Recovery().Cut() {
		t.Fatal("the dry run did not acknowledge")
	}

	r, gone, cs := run(t, last)
	if cs == nil {
		t.Fatal("the pass finished before the acknowledgement")
	}
	// The registry holds everything the pass did but the acknowledgement.
	r.restart()
	if !r.reg.Recovery().Cut() {
		t.Fatal("the cut is not reported after the crash")
	}
	// The daemon of the first run never got to start anything; the world moves
	// meanwhile: a unit with the name of the stopped role appears.
	noErr(t, r.fake.AddForeign(unitOf(gone), "someone else's unit"))
	signals := len(r.signalCalls())
	rep := r.reconcile()
	if !rep.Recovered || !rep.Acknowledged {
		t.Fatalf("report recovered %v acknowledged %v", rep.Recovered, rep.Acknowledged)
	}
	rr, _ := rep.Find(gone)
	if rr.Action != ActionUnknown || rr.Reason != "registry-recovered" {
		t.Fatalf("report %+v", rr)
	}
	if r.reg.Recovery().Cut() {
		t.Fatal("the cut is still reported")
	}
	if len(r.signalCalls()) != signals {
		t.Fatal("Reconcile signalled")
	}
}

// TestConcurrentReconcilesAcknowledgeTheCutOnce: two calls at the same time
// after a cut both act on it. The one that finds it acknowledged by the other
// is done and does not fail; exactly one of them wrote the acknowledgement.
func TestConcurrentReconcilesAcknowledgeTheCutOnce(t *testing.T) {
	r, _, _ := cutWorld(t)
	const calls = 2
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		reps  [calls]Report
		errs  [calls]error
	)
	r.drive(func() {
		for i := 0; i < calls; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				reps[i], errs[i] = r.ctl.Reconcile(context.Background())
			}(i)
		}
		close(start)
		wg.Wait()
	})
	acknowledged := 0
	for i := range reps {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if reps[i].Acknowledged {
			acknowledged++
		}
	}
	if acknowledged != 1 {
		t.Fatalf("%d of the calls report that they acknowledged the cut, want 1", acknowledged)
	}
	if r.reg.Recovery().Cut() {
		t.Fatal("the cut is still reported")
	}
	// Nothing is left to do for a later call.
	if rep := r.reconcile(); rep.Recovered || rep.Acknowledged {
		t.Fatalf("later report %+v", rep)
	}
}
