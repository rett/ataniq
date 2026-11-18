//go:build linux && amd64

package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// GateError is a refusal to start: one of the two gates before a launch is
// closed, or the configuration is refused. Nothing was created.
type GateError struct{ Reason string }

// Error implements error.
func (e *GateError) Error() string { return "refused: " + e.Reason }

// StartError is the error of a run that could not begin: the configuration or
// the host is refused (Step is StepValidate), or the attempt folder could not
// be made (Step is StepAttempt). Nothing was written, so there is no evidence.
type StartError struct {
	Step string
	Err  error
}

// Error implements error.
func (e *StartError) Error() string { return e.Step + ": " + e.Err.Error() }

// Unwrap returns the cause.
func (e *StartError) Unwrap() error { return e.Err }

// Options is what the caller adds to the configuration. The zero value of
// every field but Run, Unattended, StdinIsTerminal and Launcher means the
// real thing; tests replace the seams.
type Options struct {
	// Run is the flag --run. Without it Execute is a dry run: it validates,
	// builds and verifies the plan and starts nothing.
	Run bool
	// Unattended allows a run when standard input is not a terminal.
	Unattended bool
	// StdinIsTerminal says whether standard input is a terminal; nil means it
	// is not.
	StdinIsTerminal func() bool
	// Launcher starts the plan. It is required for a run.
	Launcher Launcher

	// Now is the clock of the records; nil means time.Now.
	Now func() time.Time
	// NetFS is the host file system the resolv.conf is derived from; nil
	// means sandboxnet.HostFS.
	NetFS sandboxnet.FS
	// Resolver resolves bind sources; nil means mountpoints.RealResolver.
	Resolver mountpoints.Resolver
	// HostHas says whether the host has a path (for the optional symbolic
	// link rows of the bind table); nil means a no-follow stat.
	HostHas func(path string) bool
	// CheckBindSources checks the bind sources of the plan, right before the
	// launch, without following a link (rule G1); nil means
	// mountpoints.CheckBindSources. It gets the resolved row of every mount of
	// the plan that has a source, in the order of the mounts; the path it is
	// to open for a row is the row's Bound(), which the harness has checked is
	// the path the plan binds. The tests of the harness replace it so that the
	// host's own folders (/usr, /etc/localtime and the like) are not looked at.
	CheckBindSources func(rows []mountpoints.ResolvedSource) mountpoints.Report
	// System is what the outside checks read the sysctl through; nil means
	// assert.HostSystem.
	System assert.System
	// NewCanary makes the canary; nil means assert.NewCanary.
	NewCanary func() (*assert.Canary, error)
	// Rand is the source of the attempt's name; nil means crypto/rand.
	Rand io.Reader
	// UID is the user id the folders of the lead must belong to; zero means
	// the effective user id of the caller.
	UID uint32
	// TimeLimit overrides the configuration's time limit (tests).
	TimeLimit time.Duration
	// StopTermWait, StopKillWait and StopPoll override the stop rule's waits
	// (tests); zero means assert.StopTermWait, assert.StopKillWait and 20 ms.
	StopTermWait, StopKillWait, StopPoll time.Duration
}

func (o *Options) setDefaults() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NetFS == nil {
		o.NetFS = sandboxnet.HostFS{}
	}
	if o.Resolver == nil {
		o.Resolver = mountpoints.RealResolver
	}
	if o.CheckBindSources == nil {
		o.CheckBindSources = mountpoints.CheckBindSources
	}
	if o.HostHas == nil {
		o.HostHas = func(p string) bool { _, err := os.Lstat(p); return err == nil }
	}
	if o.System == nil {
		o.System = assert.HostSystem{}
	}
	if o.NewCanary == nil {
		o.NewCanary = assert.NewCanary
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.UID == 0 {
		o.UID = uint32(unix.Geteuid())
	}
	if o.StopTermWait == 0 {
		o.StopTermWait = assert.StopTermWait
	}
	if o.StopKillWait == 0 {
		o.StopKillWait = assert.StopKillWait
	}
	if o.StopPoll == 0 {
		o.StopPoll = 20 * time.Millisecond
	}
}

// Outcome is what Execute returns once it has made an attempt folder.
type Outcome struct {
	DryRun bool
	// Attempt is the random name of the attempt folder and Dir its path.
	Attempt string
	Dir     string
	// Plan is the plan as built; it is empty when no plan was built.
	Plan    PlanEvidence
	Summary Summary
	// StepFailed says some step failed, timed out or was interrupted. It is
	// about the harness's work: the facts in the evidence say nothing about
	// the profile.
	StepFailed bool
}

// attempt is the state of one Execute.
type attempt struct {
	cfg  *Config
	opt  Options
	rec  *recorder
	host *hostFacts

	start                                                    time.Time
	name, dir, bound, specDir, resolvPath, chanDir, fifoPath string

	in       profile.Inputs
	resolvEv ResolvEvidence
	res      mountpoints.Resolution
	// res2 is the second resolution, the one of the recheck: the last
	// resolution before the check of the bind sources.
	res2     mountpoints.Resolution
	versions ToolVersions
	notes    []string
	chn      *reportChannel
	chanMade bool
	canary   *assert.Canary
	pl       *plan.Plan
	planOpt  plan.Options
	planEv   PlanEvidence
	spec     assert.Spec
	// walk is what the last check of the mount points found (the steps check
	// and recheck): the fact the outside half of assertion 7 is made from.
	walk assert.StartWalk

	proc     Process
	launched bool
	broken   bool
	// refused is set when a step that is otherwise soft found a reason not to
	// launch at all (a bubblewrap too old for the plan): the attempt stays
	// broken.
	refused bool
	// lineRead is set once the one report line was accepted: only then is
	// the channel asked whether anything followed it.
	lineRead bool

	reportEv    ReportEvidence
	outsideEv   *OutsideEvidence
	exitEv      ExitEvidence
	stopEv      StopEvidence
	termEv      TerminalEvidence
	termData    []byte
	canaryEv    CanaryEvidence
	bindEv      BindSourcesEvidence
	files       []string
	evidenceErr error
}

// Execute runs the harness. Without opt.Run it is a dry run: it validates the
// configuration, builds and verifies the plan and starts nothing (no process,
// no listener, no FIFO). With opt.Run it needs the configuration's
// coordinator_reviewed statement and, unless opt.Unattended is set, a
// terminal on standard input, and it launches through opt.Launcher.
//
// A *GateError or a *StartError is returned, with no outcome, before
// anything is written. Once the attempt folder exists the outcome is returned
// with the evidence written; the error is set only when the evidence itself
// could not be written. A step that failed is in the outcome, not the error.
func Execute(ctx context.Context, cfg *Config, opt Options) (*Outcome, error) {
	opt.setDefaults()
	if opt.Run {
		if !cfg.CoordinatorReviewed {
			return nil, &GateError{Reason: "a run needs \"coordinator_reviewed\": true in the configuration, the statement that a coordinator reviewed it and the plan it gives"}
		}
		if !opt.Unattended && (opt.StdinIsTerminal == nil || !opt.StdinIsTerminal()) {
			return nil, &GateError{Reason: "standard input is not a terminal; a run needs one unless --unattended is given"}
		}
		if opt.Launcher == nil {
			return nil, errors.New("harness: a run needs a launcher")
		}
	}
	a := &attempt{cfg: cfg, opt: opt, start: opt.Now()}
	a.rec = newRecorder(opt.Now)
	return a.execute(ctx)
}

func (a *attempt) dry() bool { return !a.opt.Run }

func (a *attempt) limit() time.Duration {
	if a.opt.TimeLimit > 0 {
		return a.opt.TimeLimit
	}
	return time.Duration(a.cfg.TimeLimitSeconds) * time.Second
}

// step runs fn as a recorded step and says whether it succeeded. A failure
// marks the attempt as broken, so the steps that depend on it are skipped.
func (a *attempt) step(name string, fn func() error) bool {
	st := a.rec.run(name, func() (StepStatus, error) { return StepOK, fn() })
	if st != StepOK {
		a.broken = true
	}
	return st == StepOK
}

func (a *attempt) execute(ctx context.Context) (*Outcome, error) {
	// 1. Validate the configuration, the host and the executables. Nothing is
	// created yet, so a failure here has no evidence.
	var verr error
	if !a.step(StepValidate, func() error {
		if verr = a.cfg.Validate(); verr != nil {
			return verr
		}
		a.host, verr = checkHost(a.cfg)
		return verr
	}) {
		return nil, &StartError{Step: StepValidate, Err: verr}
	}
	// 2. The attempt folder.
	var aerr error
	if !a.step(StepAttempt, func() error { aerr = a.makeAttemptFolder(); return aerr }) {
		return nil, &StartError{Step: StepAttempt, Err: aerr}
	}
	in, err := a.cfg.inputs(a.resolvPath, a.specDir)
	if err != nil {
		// Validate has built these inputs already, so this is unreachable;
		// the steps are skipped rather than run on empty inputs.
		a.broken = true
	}
	a.in = in
	a.pre(ctx)
	switch {
	case a.dry():
		a.skipLaunch("dry run: nothing is started")
	case a.broken:
		a.skipLaunch("an earlier step failed")
	default:
		a.launchPhase(ctx)
	}
	a.cleanup()
	return a.finish(err)
}

// makeAttemptFolder makes the folder with a random name (exclusively, 0700) and
// the folders of what the plan binds from the harness.
func (a *attempt) makeAttemptFolder() error {
	for try := 0; try < 8; try++ {
		var b [16]byte
		if _, err := io.ReadFull(a.opt.Rand, b[:]); err != nil {
			return fmt.Errorf("random name: %w", err)
		}
		name := hex.EncodeToString(b[:])
		dir := a.cfg.Campaign + "/" + name
		err := unix.Mkdir(dir, 0o700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return fmt.Errorf("make the attempt folder: %w", err)
		}
		if err := unix.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("set the mode of the attempt folder: %w", err)
		}
		a.name, a.dir = name, dir
		a.bound = dir + "/" + DirBound
		a.specDir = a.bound + "/spec"
		a.resolvPath = a.bound + "/resolv.conf"
		a.chanDir = dir + "/" + DirChannel
		a.fifoPath = a.chanDir + "/" + FileReportFIFO
		for _, d := range []string{a.bound, a.specDir} {
			if err := unix.Mkdir(d, 0o700); err != nil {
				return fmt.Errorf("make %s: %w", d, err)
			}
		}
		return syncDir(dir)
	}
	return errors.New("no unused random name for the attempt folder after 8 tries")
}

// def is one step of the part before the launch.
type def struct {
	name string
	// runOnly steps are skipped in a dry run.
	runOnly bool
	// soft steps record a failure without stopping the later steps.
	soft bool
	fn   func(ctx context.Context) error
}

// pre runs the steps from the tool versions to the recheck.
func (a *attempt) pre(ctx context.Context) {
	defs := []def{
		{StepVersions, true, true, func(ctx context.Context) error {
			a.versions = a.opt.Launcher.ToolVersions(ctx, ToolPaths{Pasta: a.cfg.Executables.Pasta, Bwrap: a.cfg.Executables.Bwrap})
			// The plan binds every source Ataniq supplies from a descriptor:
			// a bubblewrap whose release is known to be too old for that is
			// refused here, and that failure is not a soft one.
			note, err := checkBwrapBindFD(a.versions.Bwrap)
			if err != nil {
				a.refused = true
				return err
			}
			if note != "" {
				a.notes = append(a.notes, note)
			}
			if len(a.versions.Errors) > 0 {
				return fmt.Errorf("%d tool version(s) could not be read: %s", len(a.versions.Errors), strings.Join(a.versions.Errors, "; "))
			}
			return nil
		}},
		{StepOwnClone, false, false, func(context.Context) error {
			rep, err := mountpoints.CheckOwnClone(a.in, a.opt.UID)
			if err != nil {
				return err
			}
			return rep.Err()
		}},
		{StepPrepare, true, false, func(context.Context) error {
			return mountpoints.Prepare(a.in, mountpoints.PrepareOptions{NoLeadHasRun: true})
		}},
		{StepCheck, false, false, func(context.Context) error {
			rep, err := mountpoints.Check(a.in)
			a.recordWalk(StepCheck, rep, err)
			if err != nil {
				return err
			}
			return rep.Err()
		}},
		{StepResolv, false, false, func(context.Context) error { return a.deriveResolv() }},
		{StepResolve, false, false, func(context.Context) error { return a.resolveSources() }},
		{StepChannel, true, false, func(context.Context) error {
			// A folder of the attempt for the channel alone: the plan wants
			// the report path directly inside a folder the holder owns.
			if err := unix.Mkdir(a.chanDir, 0o700); err != nil {
				return fmt.Errorf("make the report channel folder: %w", err)
			}
			a.chanMade = true
			c, err := openReportChannel(a.fifoPath)
			a.chn = c
			return err
		}},
		{StepCanary, true, false, func(context.Context) error {
			c, err := a.opt.NewCanary()
			a.canary = c
			return err
		}},
		{StepBuild, false, false, func(context.Context) error { return a.buildPlan() }},
		{StepVerify, false, false, func(context.Context) error { return a.verifyPlan() }},
		{StepSpec, true, false, func(context.Context) error { return a.writeSpec() }},
		{StepRecheck, true, false, func(context.Context) error { return a.recheck() }},
		{StepBindSources, true, false, func(context.Context) error { return a.checkBindSources() }},
	}
	for _, d := range defs {
		switch {
		case a.broken:
			a.rec.skip(d.name, "an earlier step failed")
			continue
		case a.dry() && d.runOnly:
			a.rec.skip(d.name, "dry run: nothing is started, opened or written into the scratch folders")
			continue
		case d.name == StepPrepare && !a.cfg.NoLeadHasRun:
			a.rec.skip(d.name, "the configuration does not state that no lead has run: the mount points are checked, not made")
			continue
		case d.name == StepCheck && a.dry() && a.cfg.NoLeadHasRun:
			a.rec.skip(d.name, "dry run: the mount points are made by the run, so they may not exist yet")
			continue
		}
		ok := a.step(d.name, func() error { return d.fn(ctx) })
		if !ok && d.soft && !a.refused {
			a.broken = false
		}
	}
}

// deriveResolv derives the resolv.conf from the host and writes it into the
// harness's folder. A derivation that is refused (only loopback servers) is the
// second half of case S4 and ends the run before a launch.
func (a *attempt) deriveResolv() error {
	res, err := sandboxnet.DeriveFromFS(a.opt.NetFS)
	a.resolvEv = ResolvEvidence{Source: string(res.Source), Nameservers: len(res.Nameservers), DroppedLoopback: res.DroppedLoopback, Notes: res.Notes}
	if err != nil {
		return err
	}
	return writeFileMode(a.bound, "resolv.conf", res.Content, 0o644)
}

// resolveSources resolves every bind source with its links evaluated. A source
// Ataniq supplies must be a real path (mountpoints.ResolveSources refuses one
// with a link on it), so what the plan binds is what is checked; a row of the
// host (/usr, /etc/localtime and the like) may resolve through the host's own
// links, and the plan binds its real path (see buildPlan).
func (a *attempt) resolveSources() error {
	res, err := mountpoints.ResolveSources(a.in, a.opt.Resolver)
	if err != nil {
		return err
	}
	a.allowUnmadeGitMeta(&res)
	if err := res.Report.Err(); err != nil {
		return err
	}
	a.notes = append(a.notes, hostRowNotes(res.Sources)...)
	a.res = res
	return nil
}

// allowUnmadeGitMeta drops, in a dry run of a configuration that states no lead
// has run, the "does not exist" problems of the three entries of the git
// metadata folder: the run would make the folder (the step prepare), a dry run
// makes nothing, and the plan is built from the names alone. Any other problem,
// and the same problem in a run, stays. A note says what was left out.
func (a *attempt) allowUnmadeGitMeta(res *mountpoints.Resolution) {
	if !a.dry() || !a.cfg.NoLeadHasRun {
		return
	}
	entries := map[string]bool{}
	for _, rel := range profile.GitReadOnlyRel() {
		entries[profile.GitMetaSource(a.in.GitMeta, rel)] = true
	}
	var kept []*mountpoints.Problem
	dropped := 0
	for _, p := range res.Problems {
		if errors.Is(p, mountpoints.ErrMissing) && entries[p.Path] {
			dropped++
			continue
		}
		kept = append(kept, p)
	}
	if dropped == 0 {
		return
	}
	res.Problems = kept
	a.notes = append(a.notes, fmt.Sprintf("dry run: %d of the 3 entries of the git metadata folder %s do not exist yet; a run makes the folder (step %s) and checks every source again", dropped, a.in.GitMeta, StepPrepare))
}

// hostRowNotes says, for each row of the host whose own name is a link of the
// host, which real path the plan binds in its place.
func hostRowNotes(srcs []mountpoints.ResolvedSource) []string {
	var notes []string
	for _, s := range srcs {
		if !s.Skipped && s.Role == profile.RoleHost && s.Real != s.Source {
			notes = append(notes, fmt.Sprintf("the host row %s is %s, which resolves to %s: the plan binds the real path, and the real path is what is checked", s.Inside, s.Source, s.Real))
		}
	}
	return notes
}

// hostPresent says which optional rows of the bind table the host has: a
// bind of a host path is present when its source resolved, a symbolic link
// when the host has a path of the same name.
func (a *attempt) hostPresent() (map[string]bool, error) {
	binds, err := a.in.Binds()
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
			present[b.Inside] = a.opt.HostHas(b.Inside)
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
	// The walk of every bind source Ataniq supplies, from the inputs alone:
	// the plan's pass-through step makes these walks and bubblewrap binds the
	// descriptors they return. They are the walks the check of the bind sources
	// makes on the same rows; none is built by hand.
	walks, err := mountpoints.PlanWalks(a.in)
	if err != nil {
		return err
	}
	a.planOpt = plan.Options{
		PastaPath: a.cfg.Executables.Pasta, BwrapPath: a.cfg.Executables.Bwrap,
		AgentArgv: a.cfg.Command(), HostPresent: present, HostSources: a.res.HostSources(),
		SourceWalks: walks,
		ReportPath:  a.fifoPath, ReportDir: a.chanDir, ReportFD: ReportFD,
	}
	if a.in.SeccompEnabled() {
		a.planOpt.SeccompFD = SeccompFD
	}
	p, err := plan.Build(a.in, a.planOpt)
	if err != nil {
		return err
	}
	a.pl = p
	a.planEv = planEvidence(p)
	return nil
}

// verifyPlan runs plan.Verify, checks that nothing the plan binds comes from
// the campaign but the harness's own folder, and writes plan.json (also when a
// check fails, so the plan can be read).
func (a *attempt) verifyPlan() error {
	var errs []error
	probs := plan.Verify(a.pl, a.in, a.planOpt)
	noWritable, byDescriptor := true, true
	for _, p := range probs {
		switch p.Kind {
		case plan.ProbLeadWritable:
			noWritable = false
		case plan.ProbBindByPath, plan.ProbBindFD, plan.ProbWalk:
			byDescriptor = false
		}
	}
	a.bindEv.NoSourceInReadWriteSource = &noWritable
	a.bindEv.NoAtaniqSourceBoundByPath = &byDescriptor
	if len(probs) > 0 {
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
		if m.Source != "" && related(m.Source, a.cfg.Campaign) && !below(m.Source, a.bound) {
			errs = append(errs, fmt.Errorf("the plan binds %s from %s, in or above the campaign folder and not in the harness's own folder", m.Inside, m.Source))
		}
	}
	if data, err := marshal(a.planEv); err != nil {
		errs = append(errs, err)
	} else if err := a.writeEvidenceFile(FilePlan, data); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// holderPaths are the harness's own paths that must not be visible inside:
// the report channel and the evidence files, by name. The bound folder is not
// among them because two sources of the plan lie in it.
func (a *attempt) holderPaths() []string {
	out := []string{a.chanDir, a.fifoPath}
	for _, n := range []string{FilePlan, FileSpec, FileReport, FileOutside, FileTerminal, FileSummary} {
		out = append(out, a.dir+"/"+n)
	}
	return out
}

func (a *attempt) writeSpec() error {
	facts := assert.HostFacts{
		HolderPaths: a.holderPaths(),
		LoopbackTCP: a.canary.TCP, LoopbackTCP2: a.canary.TCP2, AbstractSocket: a.canary.Abstract,
		ProviderHost: a.cfg.ProviderHost, ManagedMarker: a.cfg.Sources.ManagedMarker,
	}
	// Without the identities of the owner and holder paths (and of their
	// ancestors) assertion 1 is not checked, so the list is collected here, on
	// the host, through the System seam.
	ids, err := assert.CollectIdentities(assert.IdentityPaths(a.in, facts), a.opt.System.Identity)
	if err != nil {
		return fmt.Errorf("collect the identities of the owner and holder paths: %w", err)
	}
	facts.Identities = ids
	// Assertion 11 needs the identities of the coordinators' folders in the
	// same way: the two roots of the layout and their ancestors, and the
	// folder, tree and home of every coordinator and every shared login that
	// exists under them now.
	folders, err := assert.CoordinatorFolders(a.in, a.opt.System.DirNames)
	if err != nil {
		return fmt.Errorf("list the coordinators' folders: %w", err)
	}
	coordIDs, err := assert.CollectIdentities(append(assert.CoordinatorIdentityPaths(a.in, facts), folders...), a.opt.System.Identity)
	if err != nil {
		return fmt.Errorf("collect the identities of the coordinators' folders: %w", err)
	}
	facts.CoordinatorIdentities = coordIDs
	spec, err := assert.NewSpec(a.in, plan.InsideEnvironment(a.pl), facts)
	if err != nil {
		return err
	}
	data, err := assert.EncodeSpec(spec)
	if err != nil {
		return err
	}
	a.spec = spec
	if err := writeFile(a.specDir, profile.SpecFileName, data); err != nil {
		return err
	}
	return a.writeEvidenceFile(FileSpec, data)
}

// recheck is the check right before the launch: the mount points again and
// the sources again, which must resolve as they did when the plan was built.
// Nothing that runs a lead process lies between this and the launch.
func (a *attempt) recheck() error {
	rep, err := mountpoints.Check(a.in)
	a.recordWalk(StepRecheck, rep, err)
	if err != nil {
		return err
	}
	if err := rep.Err(); err != nil {
		return err
	}
	res, err := mountpoints.ResolveSources(a.in, a.opt.Resolver)
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

// recordWalk keeps what a check of the mount points found as the fact the
// outside half of assertion 7 is made from. The check is the holder's walk of
// the design (section 3.6): mountpoints.Check looks at every bind destination
// inside a lead-writable folder without following a link, and at the git
// metadata folder. The last one made counts, which in a run is the one of the
// step recheck, right before the launch.
//
// The design wants the walk made with the previous instance proven stopped.
// The harness makes no unit and has no control group, so it proves nothing
// about a process of an earlier lead. When the configuration states that no
// lead has run in these folders (no_lead_has_run, the statement the
// preparation of the mount points also rests on), there is no previous
// instance, and the fact says so in those words. Without the statement the
// fact says the previous instance is not proven stopped, and the result of a
// walk that found nothing is not-checked.
func (a *attempt) recordWalk(step string, rep mountpoints.Report, err error) {
	if err != nil {
		a.walk = assert.StartWalk{Detail: fmt.Sprintf("step %s: the check of the mount points refused the inputs: %v", step, err)}
		return
	}
	w := assert.StartWalk{Made: true, Problems: len(rep.Problems), PreviousStopped: a.cfg.NoLeadHasRun}
	switch {
	case len(rep.Problems) > 0:
		first := rep.Problems[0]
		w.FirstPath = first.Path
		w.Detail = fmt.Sprintf("step %s: %s", step, first.Error())
		if rep.Truncated {
			w.Detail += " (and more problems than the check lists)"
		}
	case a.cfg.NoLeadHasRun:
		w.Detail = fmt.Sprintf("step %s; the harness has no unit and no control group: that there is no previous instance is the configuration's statement that no lead has run in these folders (no_lead_has_run)", step)
	default:
		w.Detail = fmt.Sprintf("step %s; the harness has no unit and no control group, and the configuration does not state that no lead has run in these folders (no_lead_has_run): it does not prove that no process of an earlier lead remains", step)
	}
	a.walk = w
}

// planBindRows pairs each mount of the plan that has a source with the row of
// the last resolution it comes from, in the order of the mounts, and refuses a
// plan that binds a path other than the one the check would open for the row
// (the row's Bound()): for every row the path that is bound must be the path
// that is checked. A row Ataniq supplies is bound from its Source, which the
// resolution has found to be its real path; a row of the host from the real
// path the plan was given.
//
// It also refuses a plan whose way of binding a row is not the row's: a row
// Ataniq supplies must be bound from a descriptor, by a walk of the plan that
// is the walk the check makes for the row (the same folder, the same path
// beneath it, the same type), and a row of the host must be bound by path. So
// the walk the pass-through step makes is the walk that was checked.
func (a *attempt) planBindRows() ([]mountpoints.ResolvedSource, error) {
	byInside := map[string]mountpoints.ResolvedSource{}
	for _, s := range a.res2.Sources {
		byInside[s.Inside] = s
	}
	walks := a.planWalks()
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

// planWalks are the walks of the plan by the inside path of their row.
func (a *attempt) planWalks() map[string]plan.Walk {
	walks := map[string]plan.Walk{}
	for _, w := range a.pl.Walks {
		walks[w.Inside] = w
	}
	return walks
}

// checkBindSources is the last check before the launch (rule G1): the path
// bound for every row of the plan is opened from the root of the file system
// without following a link, and a link anywhere on a path, a missing source
// and a source of another type than its row binds refuse the launch. It comes
// after the second resolution and nothing that runs a lead process lies
// between it and the launch. For a source Ataniq supplies it is a check before
// the walk that counts: the plan's pass-through step, which the launch starts,
// walks to the source again by handle and gives bubblewrap the descriptor.
func (a *attempt) checkBindSources() error {
	rows, err := a.planBindRows()
	if err != nil {
		return err
	}
	rep := a.opt.CheckBindSources(rows)
	a.recordBindSources(rows, rep)
	return rep.Err()
}

// recordBindSources puts the facts of the check into the evidence: each source
// of the plan, with how the plan binds it (from a descriptor, with the walk of
// the pass-through step, or by path), and with the problem the check found
// about its path, or the facts that no component of it is a link and that it is
// of the type the row binds. A link and a wrong type are facts of their own; a
// source that is missing or cannot be read has neither. The path checked is the
// path bound, for every row. A report that was cut short, or that holds a
// problem naming no row, does not say about the sources it did not reach, so
// those have no fact.
func (a *attempt) recordBindSources(rows []mountpoints.ResolvedSource, rep mountpoints.Report) {
	byRow := map[string]*mountpoints.Problem{}
	unscoped := false
	for _, p := range rep.Problems {
		if p.Inside == "" {
			// About no row (the root could not be opened): no row was checked.
			unscoped = true
			continue
		}
		if _, ok := byRow[p.Inside]; !ok {
			byRow[p.Inside] = p
		}
	}
	a.bindEv.LinksChecked = true
	a.bindEv.Sources = []BindSourceFact{}
	walks := a.planWalks()
	for _, row := range rows {
		f := BindSourceFact{Inside: row.Inside, Source: row.Bound(), CheckedPath: row.Bound()}
		if w, ok := walks[row.Inside]; ok {
			f.BoundByDescriptor, f.Descriptor, f.WalkRoot, f.WalkRel, f.WalkType = true, w.FD, w.Root, w.Rel, walkType(w.Dir)
		}
		// The check reads the type of a source Ataniq supplies; it does not
		// look at the type of a row of the host.
		typed := row.Role != profile.RoleHost
		no, yes := false, true
		switch p, bad := byRow[row.Inside]; {
		case bad:
			f.Problem = assert.EscapeDetail(p.Error())
			switch {
			case errors.Is(p, mountpoints.ErrSymlink):
				f.NoSymbolicLinkComponent = &no
			case errors.Is(p, mountpoints.ErrWrongType) && typed:
				f.HasExpectedType = &no
			}
			// Any other problem (missing, unreadable) says nothing about
			// links or about the type, so no fact is given either way; and a
			// wrong type says nothing about links, as the walk stopped there.
		case rep.Truncated, unscoped:
			// The report was cut short, or a problem names no row: this source
			// may be among the problems not listed, or was never checked.
		default:
			f.NoSymbolicLinkComponent = &yes
			if typed {
				f.HasExpectedType = &yes
			}
		}
		a.bindEv.Sources = append(a.bindEv.Sources, f)
	}
}

func (a *attempt) writeEvidenceFile(name string, data []byte) error {
	if err := writeFile(a.dir, name, data); err != nil {
		a.evidenceErr = errors.Join(a.evidenceErr, err)
		return err
	}
	a.files = append(a.files, name)
	return nil
}

// skipLaunch records the steps from the launch to the stop as skipped.
func (a *attempt) skipLaunch(reason string) {
	for _, n := range []string{StepLaunch, StepReadReport, StepWaitExit, StepOutside, StepStop, StepTrailing} {
		a.rec.skip(n, reason)
	}
}

// launchPhase launches, reads the report, waits for the exit, runs the outside
// checks and stops what is left.
func (a *attempt) launchPhase(ctx context.Context) {
	limitCtx, cancel := context.WithTimeout(ctx, a.limit())
	defer cancel()
	if !a.step(StepLaunch, func() error {
		p, err := a.opt.Launcher.Launch(LaunchRequest{Plan: a.pl, Env: nil, Rows: uint16(a.cfg.Terminal.Rows), Cols: uint16(a.cfg.Terminal.Cols)})
		if err != nil {
			return err
		}
		a.proc = p
		return nil
	}) {
		a.reportEv = ReportEvidence{Format: EvidenceFormat, Outcome: ReportNotLaunched, Results: []assert.Result{}}
		for _, n := range []string{StepReadReport, StepWaitExit, StepOutside, StepStop, StepTrailing} {
			a.rec.skip(n, "nothing was launched")
		}
		return
	}
	a.launched = true
	a.exitEv.Launched = true
	a.rec.run(StepReadReport, func() (StepStatus, error) { return a.readReport(ctx, limitCtx) })
	a.rec.run(StepWaitExit, func() (StepStatus, error) { return a.waitExit(ctx, limitCtx) })
	a.rec.run(StepOutside, func() (StepStatus, error) { return StepOK, a.outside() })
	a.rec.run(StepStop, func() (StepStatus, error) {
		a.stopEv = stopProcess(a.proc, a.opt.StopTermWait, a.opt.StopKillWait, a.opt.StopPoll)
		a.recordEndAfterStop()
		t := a.proc.Terminal()
		a.termData = t.Data
		a.termEv = TerminalEvidence{CapturedBytes: len(t.Data), TotalBytes: t.Total, DroppedBytes: t.Dropped}
		if a.cfg.DefaultCommand() {
			seen := bytes.Contains(t.Data, []byte(ScratchMarker))
			a.termEv.MarkerSeen = &seen
		}
		if a.stopEv.State == StopUnknown {
			return StepFailed, errors.New("a process was still in the group after SIGKILL and the wait: the state is unknown")
		}
		return StepOK, nil
	})
	a.rec.run(StepTrailing, func() (StepStatus, error) { return a.checkTrailing() })
}

// recordEndAfterStop records how the first process ended when the wait for its
// exit gave up at the time limit (or at an interruption) and the stop has ended
// it since. A process that is still there leaves nothing to record.
func (a *attempt) recordEndAfterStop() {
	if a.exitEv.Exited {
		return
	}
	select {
	case <-a.proc.Done():
		e := a.proc.Exit()
		a.exitEv.AfterStop = &EndEvidence{Code: e.Code, Signal: e.Signal, Error: e.Err}
	default:
	}
}

// checkTrailing asks the report channel, after the stop (so the first process
// has ended and what is left of its group has been signalled), whether any
// byte followed the accepted line. A byte is a failure of the start and is
// recorded as a fact. Nothing is asked when no line was accepted: the read of
// the line may still be in progress, and the report evidence says why.
func (a *attempt) checkTrailing() (StepStatus, error) {
	if !a.lineRead {
		return StepSkipped, nil
	}
	err := a.chn.trailing()
	// A *Refusal says bytes were there; any other error says the question
	// could not be asked, which is a failure of the step but not a fact about
	// the channel.
	var refusal *assert.Refusal
	if more := errors.As(err, &refusal); more || err == nil {
		a.reportEv.TrailingBytes = &more
	}
	if err != nil {
		return StepFailed, fmt.Errorf("the report channel was not quiet after the report line: %w", err)
	}
	return StepOK, nil
}

// limitStatus names why a wait ended without its event.
func limitStatus(ctx context.Context) (StepStatus, string) {
	if ctx.Err() != nil {
		return StepInterrupted, "the run was interrupted"
	}
	return StepTimeout, "the time limit was reached"
}

func (a *attempt) readReport(ctx, limitCtx context.Context) (StepStatus, error) {
	a.reportEv = ReportEvidence{Format: EvidenceFormat, Results: []assert.Result{}}
	var lr lineRead
	got := false
	select {
	case lr = <-a.chn.out:
		got = true
	case <-a.proc.Done():
		// The process is gone: let the reader see the end of the channel. A
		// line written just before the exit is still in it.
		a.chn.closeWriter()
		select {
		case lr = <-a.chn.out:
			got = true
		case <-limitCtx.Done():
		}
	case <-limitCtx.Done():
	}
	if !got {
		st, why := limitStatus(ctx)
		a.reportEv.Outcome = ReportTimeLimit
		if st == StepInterrupted {
			a.reportEv.Outcome = ReportInterrupted
		}
		a.reportEv.Detail = why + " before a report line came"
		return st, errors.New(a.reportEv.Detail)
	}
	a.reportEv.LineBytes = len(lr.Data)
	if len(lr.Data) == 0 && lr.EOF {
		a.reportEv.Outcome = ReportExitedFirst
		a.reportEv.Detail = "the first process exited and the channel closed with no report line"
		return StepFailed, errors.New(a.reportEv.Detail)
	}
	if lr.Err != nil {
		a.reportEv.Outcome = ReportRefused
		a.reportEv.Detail = assert.EscapeDetail(lr.Err.Error())
		ex := lr.Data
		if len(ex) > 200 {
			ex = ex[:200]
		}
		a.reportEv.LineExcerpt = assert.EscapeDetail(string(ex))
		return StepFailed, errors.New("the report line was refused: " + a.reportEv.Detail)
	}
	match := lr.Report.Digest == a.spec.Digest
	a.reportEv.Outcome = ReportLineRead
	a.reportEv.Digest = lr.Report.Digest
	a.reportEv.DigestMatches = &match
	a.reportEv.Results = lr.Report.Results
	a.lineRead = true
	return StepOK, nil
}

func (a *attempt) waitExit(ctx, limitCtx context.Context) (StepStatus, error) {
	finish := func() (StepStatus, error) {
		e := a.proc.Exit()
		a.exitEv = ExitEvidence{Launched: true, Exited: true, Code: e.Code, Signal: e.Signal, Error: e.Err}
		return StepOK, nil
	}
	select {
	case <-a.proc.Done():
		return finish()
	default:
	}
	select {
	case <-a.proc.Done():
		return finish()
	case <-limitCtx.Done():
		a.exitEv.Code = -1
		st, why := limitStatus(ctx)
		return st, errors.New(why + " before the first process exited")
	}
}

func (a *attempt) outside() error {
	binds, err := a.in.Binds()
	if err != nil {
		return err
	}
	// The canary is read before it is closed (cleanup closes it).
	results := assert.RunOutside(assert.OutsideInput{
		Spec: a.spec, Role: a.cfg.State.Role, User: a.in.OwnerUser, Version: a.cfg.RuntimeVersion,
		Now: a.opt.Now(), Binds: binds, HolderPaths: a.spec.HolderPaths, Canary: a.canary, Walk: a.walk, Sys: a.opt.System,
	})
	a.canaryEv = canaryEvidence(a.canary)
	a.outsideEv = &OutsideEvidence{Format: EvidenceFormat, Results: results, Canary: a.canaryEv}
	return nil
}

func canaryEvidence(c *assert.Canary) CanaryEvidence {
	if c == nil {
		return CanaryEvidence{}
	}
	return CanaryEvidence{TCPHits: c.TCPHits(), TCP2Hits: c.TCP2Hits(), AbstractHits: c.AbstractHits(), AcceptErrors: c.AcceptErrors(), Stopped: c.Stopped()}
}

// cleanup closes what the run opened: the canary, the report channel (and its
// FIFO) and the pseudo-terminal.
func (a *attempt) cleanup() {
	if a.chn == nil && a.canary == nil && a.proc == nil {
		a.rec.skip(StepCleanup, "nothing was opened")
		return
	}
	a.rec.run(StepCleanup, func() (StepStatus, error) {
		var errs []error
		if a.canary != nil {
			if a.outsideEv == nil {
				a.canaryEv = canaryEvidence(a.canary)
			}
			errs = append(errs, a.canary.Close())
		}
		if a.chn != nil {
			errs = append(errs, a.chn.close())
		}
		if a.chanMade {
			if err := unix.Rmdir(a.chanDir); err != nil && !errors.Is(err, unix.ENOENT) {
				errs = append(errs, fmt.Errorf("remove the report channel folder: %w", err))
			}
		}
		if a.proc != nil {
			errs = append(errs, a.proc.Close())
		}
		return StepOK, errors.Join(errs...)
	})
}

// finish writes the evidence that remains and the summary.
func (a *attempt) finish(early error) (*Outcome, error) {
	var werrs []error
	a.rec.run(StepWriteEvidence, func() (StepStatus, error) {
		if a.launched || a.reportEv.Outcome == ReportNotLaunched {
			if data, err := marshal(a.reportEv); err != nil {
				werrs = append(werrs, err)
			} else if err := a.writeEvidenceFile(FileReport, data); err != nil {
				werrs = append(werrs, err)
			}
		}
		if a.outsideEv != nil {
			if data, err := marshal(a.outsideEv); err != nil {
				werrs = append(werrs, err)
			} else if err := a.writeEvidenceFile(FileOutside, data); err != nil {
				werrs = append(werrs, err)
			}
		}
		if a.launched {
			if err := a.writeEvidenceFile(FileTerminal, a.termData); err != nil {
				werrs = append(werrs, err)
			}
		}
		return StepOK, errors.Join(werrs...)
	})
	a.files = append(a.files, FileSummary)
	sum := a.summary()
	out := &Outcome{DryRun: a.dry(), Attempt: a.name, Dir: a.dir, Plan: a.planEv, Summary: sum, StepFailed: a.rec.anyFailed()}
	data, err := marshal(sum)
	if err == nil {
		err = writeFile(a.dir, FileSummary, data)
	}
	if err != nil {
		return out, errors.Join(early, a.evidenceErr, fmt.Errorf("write the summary: %w", err))
	}
	return out, errors.Join(early, a.evidenceErr)
}

func (a *attempt) summary() Summary {
	s := Summary{
		Format: EvidenceFormat, DryRun: a.dry(), Attempt: a.name,
		Start: a.start.UTC().Format(timeFormat), End: a.opt.Now().UTC().Format(timeFormat),
		Runtime: a.cfg.Runtime, RuntimeVersion: a.cfg.RuntimeVersion,
		Versions: a.versions, Steps: a.rec.list("the attempt did not get that far"),
		Exit: a.exitEv, Stop: a.stopEv, Report: a.reportEv.Outcome, Canary: a.canaryEv, Terminal: a.termEv,
		Resolv: a.resolvEv, Notes: a.notes, Files: a.files,
	}
	if a.pl != nil {
		s.ProfileDigest = a.pl.Digest
	}
	if a.host != nil {
		s.Executables = a.host.Executables
	}
	if s.Executables == nil {
		s.Executables = []ExeRecord{}
	}
	if s.Report == "" {
		s.Report = ReportNotLaunched
	}
	if s.Stop.State == "" {
		s.Stop.State = StopNotLaunched
	}
	var results []assert.Result
	results = append(results, a.reportEv.Results...)
	if a.outsideEv != nil {
		results = append(results, a.outsideEv.Results...)
	}
	s.BindSources = a.bindEv
	if s.BindSources.Sources == nil {
		s.BindSources.Sources = []BindSourceFact{}
	}
	s.Cases = casesFor(a.cfg.Cases, results, map[string][]CaseResult{"G1": g1Results(s.BindSources)})
	return s
}
