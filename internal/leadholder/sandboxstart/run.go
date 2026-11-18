//go:build linux && ataniq_canary

// Package sandboxstart is the holder's start sequence of the lead sandbox: the
// order in which the qualification harness
// (qualification/leadsandbox/internal/harness) prepares, checks, launches and
// judges one start, written again over the packages it drives (profile, plan,
// mountpoints, assert, sandboxnet), so that a holder can start a sandbox. No
// production package may import the harness. The sequence is therefore a copy,
// and it is debt: a passing qualification of the harness says nothing about
// this code until one of them drives the other.
//
// Run takes the inputs (one JSON file, read by ReadInputs), a [System] (every
// outside read and effect, with a real implementation, NewSystem, and a fake in
// tests) and a [Launcher] (which starts the plan's argument vector and returns
// the child). Its steps carry the harness's names (steps.go), and a step that
// fails refuses the start with a *StepError that names it. The order is:
//
//	validate-config   the inputs, the profile's own checks, the host
//	tool-versions     the tools' versions; a bubblewrap too old for --bind-fd
//	own-clone         the tree is the lead's own clone, the folders are its own
//	prepare           the mount points are made (only with Inputs.NoLeadHasRun)
//	check             the start walk of the bind destinations (assertion 7)
//	resolv-conf       the resolv.conf derived from the host's, and the bound folder
//	resolve-sources   every bind source with its links evaluated
//	report-channel    the report FIFO, its reading end opened first
//	canary            the target of assertion 3
//	build-plan        plan.Build
//	verify-plan       plan.Verify, the pinned digest, the folders the plan binds
//	write-spec        the probe's spec
//	recheck           the start walk and the sources again, right before the launch
//	check-bind-sources  every source walked to without a link (rule G1)
//	launch            the plan's argument vector, through the Launcher
//	read-report       the probe's one report line
//	outside-checks    the outside half of the assertions, the judgement, the waiver
//	stop              only after a failure: the child is stopped and reaped
//	report-trailing   nothing followed the report line
//	cleanup           the canary, the FIFO and its folder, the bound folder
//
// # The report FIFO
//
// The FIFO is <runtime root>/leads/<layout role>/report/probe.fifo, in a folder
// that holds nothing else, made 0700 by handle; the reading end is opened first
// (a write end of the sequence's own follows, so that the reader does not see
// the end of the file before the probe's descriptor is opened). It is never
// under <state root>/leads, which the plan refuses for a report path: the
// runtime root and the state root must not nest, which the root check of canary
// mode and Run's own check make sure of.
//
// # The waiver
//
// In canary mode, and only then, the judgement of the whole report excuses
// assertion 10 (no qualification record) and nothing else: any other result
// that is failed, not checked or missing refuses the start. Started says the
// waiver was applied and carries canaryroot.WaiverText. With canary false the
// waiver is not applied and the start is refused at the step outside-checks,
// on assertion 10.
//
// # Assertion 9
//
// The role lock is the one the holder holds: System.RoleLock gives it, Run
// does not take it. Lingering is read through System.Lingering, from the login
// manager's state.
//
// # A failed start leaves nothing behind
//
// After a failure the child, if it was launched, is stopped (SIGTERM, a wait,
// SIGKILL, a wait) and reaped through its handle; the FIFO and its folder, the
// bound folder and the canary are removed or closed; no descriptor is left.
package sandboxstart

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
)

// The descriptor numbers Run gives the plan. They are constants of the
// sequence, not values of the inputs: the pass-through step opens the files at
// these numbers.
const (
	// SeccompFD is the descriptor of the seccomp file, when one is named.
	SeccompFD = 3
	// ReportFD is the descriptor of the report channel.
	ReportFD = 4
)

// attempt is the state of one Run.
type attempt struct {
	ctx    context.Context
	in     Inputs
	sys    System
	launch Launcher
	rec    recorder

	uid, gid   uint32
	pin        profile.Inputs
	runtimeDir string
	reportDir  string
	fifo       string
	boundDir   string

	exes     []ExeRecord
	versions ToolVersions
	// walk is what the last check of the mount points found (the steps check
	// and recheck): the fact the outside half of assertion 7 is made from.
	walk assert.StartWalk
	// res is the first resolution of the sources, res2 the second, the one of
	// the recheck.
	res, res2 mountpoints.Resolution

	bound   Bound
	chn     ReportChannel
	canary  *assert.Canary
	pl      *plan.Plan
	planOpt plan.Options
	spec    assert.Spec

	child  Child
	report assert.Report

	results []assert.Result
	waived  bool

	closed bool
}

// stepDef is one step of the sequence.
type stepDef struct {
	name string
	// skip, when it says why, makes the step skipped.
	skip func() string
	fn   func() error
}

// Run runs the start sequence. It returns what the holder needs to serve, or an
// error: a *StepError that names the step that refused the start, or, in canary
// mode, the *canaryroot.RefusedError of the root check, which is made before
// any step (a project without the reserved prefix, a default root, a missing
// marker or a root that is not the marker's), or a plain refusal, also before
// any step, of a layout role that is not "<project>-lead". With canary false
// there is no root check here and no waiver.
//
// The holder owns the child of the result. After an error nothing of the start
// is left: the child, if one was launched, has been stopped and reaped, and the
// folders and descriptors of the sequence are gone.
func Run(ctx context.Context, in Inputs, sys System, launch Launcher) (Started, error) {
	if sys == nil {
		return Started{}, errors.New("sandboxstart: no system")
	}
	if launch == nil {
		return Started{}, errors.New("sandboxstart: no launcher")
	}
	if in.Canary {
		if err := canaryroot.Check(in.StateRoot, in.RuntimeRoot, in.Project, sys.Getenv); err != nil {
			return Started{}, fmt.Errorf("sandbox start refused before any step: %w", err)
		}
		// The layout role of the slice is the project's lead (seams section 1).
		if want := in.Project + "-lead"; in.LayoutRole != want {
			return Started{}, fmt.Errorf("sandbox start refused before any step: in canary mode the layout role is %q (the project's lead), not %q", want, clipText(in.LayoutRole))
		}
	}
	a := &attempt{ctx: ctx, in: in, sys: sys, launch: launch}
	return a.execute()
}

func (a *attempt) execute() (Started, error) {
	steps := []stepDef{
		{StepValidate, nil, a.validate},
		{StepVersions, nil, a.toolVersions},
		{StepOwnClone, nil, a.ownClone},
		{StepPrepare, func() string {
			if !a.in.NoLeadHasRun {
				return "the holder does not state that no lead has run: the mount points are checked, not made"
			}
			return ""
		}, a.prepare},
		{StepCheck, nil, func() error { return a.checkMountPoints(StepCheck) }},
		{StepResolv, nil, a.resolv},
		{StepResolve, nil, a.resolveSources},
		{StepChannel, nil, a.openChannel},
		{StepCanary, nil, a.makeCanary},
		{StepBuild, nil, a.buildPlan},
		{StepVerify, nil, a.verifyPlan},
		{StepSpec, nil, a.writeSpec},
		{StepRecheck, nil, a.recheck},
		{StepBindSources, nil, a.checkBindSources},
		{StepLaunch, nil, a.launchPlan},
		{StepReadReport, nil, a.readReport},
		{StepOutside, nil, a.outsideChecks},
		{StepStop, func() string { return "the start succeeded: the child is the holder's to serve" }, nil},
		{StepTrailing, nil, a.trailing},
		{StepCleanup, nil, a.cleanup},
	}
	for _, s := range steps {
		if s.skip != nil {
			if why := s.skip(); why != "" {
				a.rec.skip(s.name, why)
				continue
			}
		}
		if err := a.ctx.Err(); err != nil {
			a.rec.add(s.name, StepFailed, err)
			return a.abort(s.name, fmt.Errorf("interrupted: %w", err))
		}
		if err := s.fn(); err != nil {
			a.rec.add(s.name, StepFailed, err)
			return a.abort(s.name, err)
		}
		a.rec.add(s.name, StepOK, nil)
	}
	return Started{
		Child:          a.child,
		ProfileVersion: profile.Version,
		ProfileDigest:  a.pl.Digest,
		Waived:         a.waived,
		Waiver:         waiverText(a.waived),
		Results:        a.results,
		Steps:          a.rec.list(),
		Executables:    a.exes,
		Versions:       a.versions,
		Plan:           a.pl,
	}, nil
}

func durationSeconds(n int) time.Duration { return time.Duration(n) * time.Second }

func waiverText(waived bool) string {
	if waived {
		return canaryroot.WaiverText
	}
	return ""
}

// abort ends a start that failed at a step: the child is stopped and reaped, the
// sequence's own files and descriptors are released, and the refusal names the
// step. A failure of the stop or of the clean-up is added to the reason; it never
// replaces it.
func (a *attempt) abort(step string, cause error) (Started, error) {
	err := cause
	if a.child != nil {
		stopErr := a.stopChild()
		if stopErr != nil {
			a.rec.add(StepStop, StepFailed, stopErr)
			err = fmt.Errorf("%w; stop: %v", err, stopErr)
		} else {
			a.rec.add(StepStop, StepOK, nil)
		}
		a.child = nil
	}
	if step != StepCleanup {
		if cerr := a.closeAll(); cerr != nil {
			a.rec.add(StepCleanup, StepFailed, cerr)
			err = fmt.Errorf("%w; cleanup: %v", err, cerr)
		} else {
			a.rec.add(StepCleanup, StepOK, nil)
		}
	}
	return Started{}, &StepError{Step: step, Err: err, Steps: a.rec.list()}
}

// stopChild applies the stop rule of the design (section 3.8) to the child:
// SIGTERM, assert.StopTermWait, SIGKILL, and then it reaps the child. A tree
// that is still there afterwards is an error: the state is unknown.
func (a *attempt) stopChild() error {
	a.child.Stop(assert.StopTermWait)
	var errs []error
	if gone, proof := a.child.TreeGone(); !gone {
		errs = append(errs, fmt.Errorf("a process was still there after SIGKILL and the wait, so the state is unknown: %s", assert.EscapeDetail(proof)))
	}
	if err := a.child.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close the child: %w", err))
	}
	return errors.Join(errs...)
}

// closeAll closes what the sequence opened: the canary, the report channel
// (and with it the FIFO and its folder) and the bound folder. Each is closed
// once.
func (a *attempt) closeAll() error {
	if a.closed {
		return nil
	}
	a.closed = true
	var errs []error
	if a.canary != nil {
		errs = append(errs, a.canary.Close())
	}
	if a.chn != nil {
		errs = append(errs, a.chn.Close())
	}
	if a.bound != nil {
		errs = append(errs, a.bound.Close())
	}
	return errors.Join(errs...)
}

// cleanup is the step that closes what the sequence opened, once the report is
// in and judged.
func (a *attempt) cleanup() error { return a.closeAll() }

// The step bodies, in the order of the steps.

func (a *attempt) validate() error {
	if err := a.in.checkFields(); err != nil {
		return err
	}
	a.uid, a.gid = a.sys.IDs()
	a.runtimeDir = RuntimeFolder(a.in.RuntimeRoot, a.in.LayoutRole)
	a.reportDir = ReportFolder(a.in.RuntimeRoot, a.in.LayoutRole)
	a.fifo = ReportFIFO(a.in.RuntimeRoot, a.in.LayoutRole)
	a.boundDir = boundFolder(a.in.RuntimeRoot, a.in.LayoutRole)
	pin, err := a.in.profileInputs(a.uid, a.gid)
	if err != nil {
		return err
	}
	if err := pin.Validate(); err != nil {
		return err
	}
	a.pin = pin
	if a.in.ExpectedDigest != "" {
		if err := checkDigest("the pinned profile digest", a.in.ExpectedDigest); err != nil {
			return err
		}
	}
	if len(a.in.AgentArgv) == 0 {
		return errors.New("no agent command line was given")
	}
	for _, p := range a.in.HolderPaths {
		if err := profile.CheckPath("holder path", p); err != nil {
			return err
		}
	}
	// A trial of the spec, with the facts that hold before anything is made: the
	// provider host, the managed-settings marker and the names the spec checks.
	if _, err := assert.NewSpec(pin, []profile.EnvVar{{Name: "PWD", Value: profile.TreeInside(pin.Project)}}, assert.HostFacts{
		HolderPaths:    a.holderPaths(),
		LoopbackTCP:    "127.0.0.1:1",
		AbstractSocket: "ataniq-start-trial",
		ProviderHost:   a.in.ProviderHost,
		ManagedMarker:  a.in.Sources.ManagedMarker,
	}); err != nil {
		return err
	}
	exes, err := a.sys.CheckHost(a.in)
	if err != nil {
		return err
	}
	a.exes = exes
	return nil
}

func checkDigest(what, d string) error {
	if len(d) != 64 {
		return fmt.Errorf("%s must be 64 lower-case hex digits", what)
	}
	for i := 0; i < len(d); i++ {
		if c := d[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("%s must be 64 lower-case hex digits", what)
		}
	}
	return nil
}

// holderPaths are the paths the holder owns that must not be visible inside:
// the report folder and the FIFO, the lead's holder folder of the layout, and
// what the holder named. The runtime folder itself is not among them, because
// the plan binds two sources in its "bound" folder.
func (a *attempt) holderPaths() []string {
	out := []string{a.reportDir, a.fifo, a.pin.Layout().Holder()}
	return append(out, a.in.HolderPaths...)
}

func (a *attempt) toolVersions() error {
	a.versions = a.sys.ToolVersions(a.ctx, ToolPaths{Pasta: a.in.Executables.Pasta, Bwrap: a.in.Executables.Bwrap})
	// The plan binds every source Ataniq supplies from a descriptor: a
	// bubblewrap whose release is known to be too old for that is refused.
	if _, err := checkBwrapBindFD(a.versions.Bwrap); err != nil {
		return err
	}
	if len(a.versions.Errors) > 0 {
		return fmt.Errorf("%d tool version(s) could not be read: %s", len(a.versions.Errors), strings.Join(a.versions.Errors, "; "))
	}
	return nil
}

func (a *attempt) ownClone() error {
	rep, err := a.sys.CheckOwnClone(a.pin, a.uid)
	if err != nil {
		return err
	}
	return rep.Err()
}

func (a *attempt) prepare() error { return a.sys.PrepareMountPoints(a.pin) }

// checkMountPoints is the start walk of the design (section 3.6), made by the
// steps check and recheck. The last one made is what assertion 7 is judged on.
func (a *attempt) checkMountPoints(step string) error {
	rep, err := a.sys.CheckMountPoints(a.pin)
	a.recordWalk(step, rep, err)
	if err != nil {
		return err
	}
	return rep.Err()
}

// recordWalk keeps what a check of the mount points found as the fact the
// outside half of assertion 7 is made from, with the harness's words. The
// design wants the walk made with the previous instance proven stopped. This
// sequence has no unit and no control group, so it proves nothing about a
// process of an earlier lead: when the inputs state that no lead has run in
// these folders (Inputs.NoLeadHasRun) there is no previous instance, and when
// they state that the previous instance has stopped (Inputs.PreviousInstanceStopped) the
// holder proved it, and the fact says which. Without either statement the fact
// says the previous instance is not proven stopped, and the result of a walk
// that found nothing is not-checked.
func (a *attempt) recordWalk(step string, rep mountpoints.Report, err error) {
	if err != nil {
		a.walk = assert.StartWalk{Detail: fmt.Sprintf("step %s: the check of the mount points refused the inputs: %v", step, err)}
		return
	}
	w := assert.StartWalk{Made: true, Problems: len(rep.Problems), PreviousStopped: a.in.NoLeadHasRun || a.in.PreviousInstanceStopped}
	switch {
	case len(rep.Problems) > 0:
		first := rep.Problems[0]
		w.FirstPath = first.Path
		w.Detail = fmt.Sprintf("step %s: %s", step, first.Error())
		if rep.Truncated {
			w.Detail += " (and more problems than the check lists)"
		}
	case a.in.NoLeadHasRun:
		w.Detail = fmt.Sprintf("step %s; the start sequence has no unit and no control group: that there is no previous instance is the holder's statement that no lead has run in these folders (NoLeadHasRun)", step)
	case a.in.PreviousInstanceStopped:
		w.Detail = fmt.Sprintf("step %s; the start sequence has no unit and no control group: that the previous instance has stopped is the holder's statement (PreviousInstanceStopped)", step)
	default:
		w.Detail = fmt.Sprintf("step %s; the start sequence has no unit and no control group, and the holder states neither that no lead has run in these folders (NoLeadHasRun) nor that the previous instance has stopped (PreviousInstanceStopped): it does not prove that no process of an earlier lead remains", step)
	}
	a.walk = w
}

// resolv derives the resolv.conf from the host's and writes it into the bound
// folder. A derivation that is refused (only loopback servers) ends the start.
func (a *attempt) resolv() error {
	res, err := a.sys.DeriveResolv()
	if err != nil {
		return err
	}
	b, err := a.sys.MakeBound(a.in.RuntimeRoot, a.in.LayoutRole)
	if err != nil {
		return err
	}
	a.bound = b
	return b.WriteResolv(res.Content)
}

// resolveSources resolves every bind source with its links evaluated. A source
// Ataniq supplies must be a real path (mountpoints.ResolveSources refuses one
// with a link on it), so what the plan binds is what is checked; a row of the
// host (/usr, /etc/localtime and the like) may resolve through the host's own
// links, and the plan binds its real path (see buildPlan).
func (a *attempt) resolveSources() error {
	res, err := a.sys.ResolveSources(a.pin)
	if err != nil {
		return err
	}
	if err := res.Report.Err(); err != nil {
		return err
	}
	a.res = res
	return nil
}

func (a *attempt) openChannel() error {
	c, err := a.sys.OpenReport(a.in.RuntimeRoot, a.in.LayoutRole)
	if err != nil {
		return err
	}
	a.chn = c
	return nil
}

func (a *attempt) makeCanary() error {
	c, err := a.sys.NewCanary()
	a.canary = c
	return err
}

// hostPresent says which optional rows of the bind table the host has: a bind
// of a host path is present when its source resolved, a symbolic link when the
// host has a path of the same name.
func (a *attempt) hostPresent() (map[string]bool, error) {
	binds, err := a.pin.Binds()
	if err != nil {
		return nil, err
	}
	skipped := map[string]bool{}
	for _, s := range a.res.Sources {
		if s.Skipped {
			skipped[s.Inside] = true
		}
	}
	present := map[string]bool{}
	for _, b := range binds {
		if !b.Optional {
			continue
		}
		if b.Kind == profile.KindSymlink {
			present[b.Inside] = a.sys.HostHas(b.Inside)
		} else {
			present[b.Inside] = !skipped[b.Inside]
		}
	}
	return present, nil
}

func (a *attempt) buildPlan() error {
	present, err := a.hostPresent()
	if err != nil {
		return err
	}
	// The walk of every bind source Ataniq supplies, from the inputs alone: the
	// plan's pass-through step makes these walks and bubblewrap binds the
	// descriptors they return.
	walks, err := mountpoints.PlanWalks(a.pin)
	if err != nil {
		return err
	}
	a.planOpt = plan.Options{
		PastaPath: a.in.Executables.Pasta, BwrapPath: a.in.Executables.Bwrap,
		AgentArgv: append([]string(nil), a.in.AgentArgv...), HostPresent: present, HostSources: a.res.HostSources(),
		SourceWalks: walks,
		ReportPath:  a.fifo, ReportDir: a.reportDir, ReportFD: ReportFD,
	}
	if a.pin.SeccompEnabled() {
		a.planOpt.SeccompFD = SeccompFD
	}
	p, err := plan.Build(a.pin, a.planOpt)
	if err != nil {
		return err
	}
	a.pl = p
	return nil
}

// verifyPlan runs plan.Verify, checks the digest against the pin, and checks
// that nothing the plan binds comes from the runtime folder but the bound
// folder: the report channel lies in the same folder and must not be reachable.
func (a *attempt) verifyPlan() error {
	var errs []error
	if probs := plan.Verify(a.pl, a.pin, a.planOpt); len(probs) > 0 {
		show := probs
		if len(show) > 5 {
			show = show[:5]
		}
		parts := make([]string, len(show))
		for i, p := range show {
			parts[i] = p.String()
		}
		errs = append(errs, fmt.Errorf("the plan fails its check (%d problems): %s", len(probs), strings.Join(parts, "; ")))
	}
	for _, m := range a.pl.Mounts {
		if m.Source != "" && related(m.Source, a.runtimeDir) && !below(m.Source, a.boundDir) {
			errs = append(errs, fmt.Errorf("the plan binds %s from %s, in or above the runtime folder and not in the bound folder", m.Inside, m.Source))
		}
	}
	// The outside half of assertion 1 is made after the launch; a plan that
	// binds a holder path is refused here, before anything is started.
	if binds, err := a.pin.Binds(); err != nil {
		errs = append(errs, err)
	} else if r := assert.CheckHolderBinds(binds, a.holderPaths()); r.Status != assert.StatusOK {
		errs = append(errs, fmt.Errorf("the plan binds a path the holder owns: %s", r.Detail))
	}
	if want := a.in.ExpectedDigest; want != "" && a.pl.Digest != want {
		errs = append(errs, fmt.Errorf("the profile digest of this plan is %s, not the pinned %s", a.pl.Digest, want))
	}
	return errors.Join(errs...)
}

func (a *attempt) writeSpec() error {
	facts := assert.HostFacts{
		HolderPaths: a.holderPaths(),
		LoopbackTCP: a.canary.TCP, LoopbackTCP2: a.canary.TCP2, AbstractSocket: a.canary.Abstract,
		ProviderHost: a.in.ProviderHost, ManagedMarker: a.in.Sources.ManagedMarker,
	}
	sys := a.sys.Assert()
	// Without the identities of the owner and holder paths (and of their
	// ancestors) assertion 1 is not checked, so the list is collected here, on
	// the host, through the System seam.
	ids, err := assert.CollectIdentities(assert.IdentityPaths(a.pin, facts), sys.Identity)
	if err != nil {
		return fmt.Errorf("collect the identities of the owner and holder paths: %w", err)
	}
	facts.Identities = ids
	// Assertion 11 needs the identities of the coordinators' folders in the
	// same way.
	folders, err := assert.CoordinatorFolders(a.pin, sys.DirNames)
	if err != nil {
		return fmt.Errorf("list the coordinators' folders: %w", err)
	}
	coordIDs, err := assert.CollectIdentities(append(assert.CoordinatorIdentityPaths(a.pin, facts), folders...), sys.Identity)
	if err != nil {
		return fmt.Errorf("collect the identities of the coordinators' folders: %w", err)
	}
	facts.CoordinatorIdentities = coordIDs
	spec, err := assert.NewSpec(a.pin, plan.InsideEnvironment(a.pl), facts)
	if err != nil {
		return err
	}
	data, err := assert.EncodeSpec(spec)
	if err != nil {
		return err
	}
	a.spec = spec
	return a.bound.WriteSpec(data)
}

// recheck is the check right before the launch: the mount points again and the
// sources again, which must resolve as they did when the plan was built.
// Nothing that runs a lead process lies between this and the launch.
func (a *attempt) recheck() error {
	if err := a.checkMountPoints(StepRecheck); err != nil {
		return err
	}
	res, err := a.sys.ResolveSources(a.pin)
	if err != nil {
		return err
	}
	if err := res.Report.Err(); err != nil {
		return err
	}
	a.res2 = res
	if len(res.Sources) != len(a.res.Sources) {
		return errors.New("the set of sources changed since the plan was built")
	}
	for i, s := range res.Sources {
		if before := a.res.Sources[i]; s.Inside != before.Inside || s.Real != before.Real || s.Skipped != before.Skipped {
			return fmt.Errorf("the source of %s resolves differently than when the plan was built", s.Inside)
		}
	}
	return nil
}

// planBindRows pairs each mount of the plan that has a source with the row of
// the last resolution it comes from, in the order of the mounts, and refuses a
// plan that binds a path other than the one the check would open for the row
// (the row's Bound()): for every row the path that is bound must be the path
// that is checked. A row Ataniq supplies is bound from a descriptor, by a walk
// of the plan that is the walk the check makes for the row, and a row of the
// host is bound by path.
func (a *attempt) planBindRows() ([]mountpoints.ResolvedSource, error) {
	byInside := map[string]mountpoints.ResolvedSource{}
	for _, s := range a.res2.Sources {
		byInside[s.Inside] = s
	}
	walks := map[string]plan.Walk{}
	for _, w := range a.pl.Walks {
		walks[w.Inside] = w
	}
	var rows []mountpoints.ResolvedSource
	for _, m := range a.pl.Mounts {
		if m.Source == "" {
			continue
		}
		row, ok := byInside[m.Inside]
		switch {
		case !ok || row.Skipped:
			return nil, fmt.Errorf("the plan binds %s from %s and the last resolution has no source for that row", m.Inside, m.Source)
		case m.Source != row.Bound():
			return nil, fmt.Errorf("the plan binds %s from %s and the check would open %s: the path that is bound must be the path that is checked", m.Inside, m.Source, row.Bound())
		}
		w, walked := walks[m.Inside]
		switch host := row.Role == profile.RoleHost; {
		case host && (walked || m.FD != 0):
			return nil, fmt.Errorf("the plan binds the row of the host %s from a descriptor: a row of the host is bound by its real path", m.Inside)
		case host:
		case !walked || m.FD == 0 || m.FD != w.FD:
			return nil, fmt.Errorf("the plan does not bind %s from a descriptor of its pass-through step: a source Ataniq supplies is never bound by path", m.Inside)
		case w.Root != row.Walk.Root || w.Rel != row.Walk.Rel || w.Dir != row.Walk.Dir:
			return nil, fmt.Errorf("the plan walks to the source of %s from %s by %s and the check walks from %s by %s: the walk the step makes must be the walk that is checked", m.Inside, w.Root, w.Rel, row.Walk.Root, row.Walk.Rel)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// checkBindSources is the last check before the launch (rule G1): the path
// bound for every row of the plan is walked to without following a link, and a
// link anywhere on a path, a missing source and a source of another type than
// its row binds refuse the launch.
func (a *attempt) checkBindSources() error {
	rows, err := a.planBindRows()
	if err != nil {
		return err
	}
	return a.sys.CheckBindSources(rows).Err()
}

func (a *attempt) launchPlan() error {
	c, err := a.launch.Launch(append([]string(nil), a.pl.Argv...))
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("the launcher returned no child and no error")
	}
	a.child = c
	return nil
}

// readReport waits for the probe's one report line, which the probe writes after
// its inside checks and before it executes the agent. The line must be for the
// digest of the spec that was written.
func (a *attempt) readReport() error {
	limit := durationSeconds(a.in.TimeLimitSeconds)
	ctx, cancel := context.WithTimeout(a.ctx, limit)
	defer cancel()
	rep, err := a.chn.ReadLine(ctx, a.child.Exited())
	if err != nil {
		if ctx.Err() != nil && a.ctx.Err() == nil {
			return fmt.Errorf("no report line within %s: %w", limit, err)
		}
		return err
	}
	if rep.Digest != a.spec.Digest {
		return fmt.Errorf("the report line is for the profile digest %s, not the spec's %s", rep.Digest, a.spec.Digest)
	}
	a.report = rep
	return nil
}

// outsideChecks runs the outside half of the assertions, adds it to the probe's
// report and judges the whole. The probe's results have been read and the canary
// is still open, which the check of assertion 3 needs.
func (a *attempt) outsideChecks() error {
	binds, err := a.pin.Binds()
	if err != nil {
		return err
	}
	user := a.in.OwnerUser
	if user == "" {
		// A user that cannot be found is asked about as "": the lingering
		// source cannot answer, the result is not-checked and the start fails.
		user, _ = a.sys.Username()
	}
	results := assert.RunOutside(assert.OutsideInput{
		Spec: a.spec, Role: a.in.LayoutRole, User: user, Version: a.in.RuntimeVersion,
		Now: a.sys.Now(), Binds: binds, HolderPaths: a.spec.HolderPaths, Canary: a.canary, Walk: a.walk,
		Sys: a.sys.Assert(), Lock: a.sys.RoleLock(), Linger: a.sys.Lingering(),
	})
	full := a.report.With(results...)
	a.results = full.Results
	waived, problems := judge(full, a.spec, a.in.Canary)
	if len(problems) > 0 {
		return fmt.Errorf("the report does not allow a start: %s", strings.Join(problems, "; "))
	}
	a.waived = waived
	return nil
}

// trailing asks the report channel whether any byte followed the accepted line:
// a byte is a failure of the start.
func (a *attempt) trailing() error {
	if err := a.chn.Trailing(); err != nil {
		return fmt.Errorf("the report channel was not quiet after the report line: %w", err)
	}
	return nil
}

// checkBwrapBindFD decides from the version line of bubblewrap whether the plan
// can be launched with it. A release older than MinBwrapBindFD is refused. A
// line that does not give a release decides nothing: the note says so, and the
// launch goes on, where a bubblewrap without the options is expected to refuse
// the plan's arguments itself and to start nothing.
func checkBwrapBindFD(line string) (note string, err error) {
	min, _ := parseBwrapVersion("bubblewrap " + MinBwrapBindFD)
	got, ok := parseBwrapVersion(line)
	if !ok {
		return fmt.Sprintf("the release of bubblewrap could not be read from its version line, so it is not known whether it has --bind-fd and --ro-bind-fd (release %s is assumed to be the first that has them)", MinBwrapBindFD), nil
	}
	for i := range got {
		if got[i] != min[i] {
			if got[i] < min[i] {
				return "", fmt.Errorf("bubblewrap %d.%d.%d is older than %s, the first release assumed to have --bind-fd and --ro-bind-fd: the plan binds every source Ataniq supplies from a descriptor and has no form that binds one by path, so nothing is launched", got[0], got[1], got[2], MinBwrapBindFD)
			}
			break
		}
	}
	return "", nil
}

// MinBwrapBindFD is the oldest bubblewrap release a plan is launched with: the
// first one taken to have --bind-fd and --ro-bind-fd, which the plan uses for
// every bind source Ataniq supplies. It is the harness's assumption, to be
// confirmed at qualification, and is the one place to change.
const MinBwrapBindFD = "0.10.0"

// parseBwrapVersion reads the release out of the first line bubblewrap prints
// for --version, "bubblewrap MAJOR.MINOR[.PATCH]", and says whether the line is
// of that form.
func parseBwrapVersion(line string) (v [3]int, ok bool) {
	rest, found := strings.CutPrefix(line, "bubblewrap ")
	if !found {
		return v, false
	}
	end := 0
	for end < len(rest) && (rest[end] == '.' || rest[end] >= '0' && rest[end] <= '9') {
		end++
	}
	parts := strings.Split(rest[:end], ".")
	if len(parts) < 2 || len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || len(p) > 6 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
