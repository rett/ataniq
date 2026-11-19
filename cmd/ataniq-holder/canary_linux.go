//go:build linux && ataniq_canary

package main

// Canary mode of the holder (the canary slice, joint J4). It exists only in a
// build with the ataniq_canary tag: --canary starts the agent through the start
// sequence of the lead sandbox (package sandboxstart), which waives assertion
// 10, and --inject opens the path from a control request to the agent's
// terminal. Scratch mode is not changed by the tag.
//
// # The order of a start
//
// Before the role lock is taken, with nothing written: the inputs file is read;
// the holder refuses "canary": false, a layout role that is not
// "<project>-lead" and a --role that is not the lead of the inputs' project; the
// root check of canary mode (canaryroot.Check) is made with the process's real
// environment; the holder refuses a --role-lock that is not that role's lock
// under the state root, and a --state-dir or --runtime-dir that does not lie
// under the inputs' roots. Then the role lock is taken, and only then is
// anything else opened or written: the event log, the state file. The start
// sequence runs, and the launcher makes the mark lead-has-run just before the
// plan's first process starts; after the sequence the state file is written
// again with the waiver, the event is appended and the service starts.
//
// # The session and the start sequence
//
// The start sequence wants a launcher that starts the plan's argument vector
// and returns the child; the holder's child is owned by a session. The
// launcher here (sessionLauncher) calls session.Start with the plan's argument
// vector and returns an adapter over the session (sessionChild), so the
// session is the only owner of the terminal and of the process from the first
// moment, and the hook of the status protocol is in the session's
// configuration before the child exists. The adapter maps the four calls of
// the sequence's Child to the session's own: Exited is Done (closed after the
// first process has exited and the session's bounded waits, so the sequence
// learns of an early exit a few seconds late, never wrongly); Stop is Stop and
// a wait for Done; TreeGone is the tree fact of the concluded session; Close
// is Close, which also releases the session's hold of the role lock. The
// session's grace between SIGTERM and SIGKILL is the stop rule's
// (session.DefaultStopGrace is assert.StopTermWait; a test compares them).
//
// # A failed start
//
// When the sequence refuses or fails, it has stopped and reaped the child
// through the adapter, so the session is closed. The holder then removes the
// state file, appends a refusal to the event log and exits with exitSandbox.
// The mark lead-has-run is made by the launcher just before the plan's first
// process starts: it stays after a failure from the launch on, and a start
// that failed before the launch leaves none.
// When the child had been launched, final.json is the session's record of the
// sandbox's first process, which was stopped; it is not the record of an agent
// that served.

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/injectbridge"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/profile"
)

// canaryTag is the build tag the version line names.
const canaryTag = "ataniq_canary"

// versionSuffix follows the version in the line of --version. The run kit
// refuses a holder whose line does not name the tag.
const versionSuffix = " (build tag " + canaryTag + ")"

// notScratchText is the refusal of a command line with neither --scratch nor
// --canary.
const notScratchText = "neither --scratch nor --canary was given: outside canary mode this build can host only a scratch agent (--scratch), because for a real lead the sandbox is not built"

const modeUsageText = `
This build carries the tag ataniq_canary and has a second mode, for the canary
slice only:

  ataniq-holder --canary [--inject] --sandbox-inputs FILE [--expected-mode TEXT]
                and every flag of the first form but --scratch

--canary starts the agent in the lead sandbox, through the start sequence, and
waives assertion 10 (no qualification record). It needs --sandbox-inputs, the
inputs file of the start sequence, and refuses --scratch. The project must be a
throwaway project and the roots must not be the default ones. --inject opens
injection through the control socket; --expected-mode is the permission mode the
agent's footer must show for a delivery. The three are refused without --canary.

Exit status 4: the start sequence of the sandbox refused or failed; no agent is
left and holder.json is removed.
`

// The names of the flags of canary mode.
const (
	flagCanary       = "canary"
	flagInject       = "inject"
	flagInputs       = "sandbox-inputs"
	flagExpectedMode = "expected-mode"
)

// maxExpectedMode bounds --expected-mode.
const maxExpectedMode = 256

// modeFlags are the flags of canary mode.
type modeFlags struct {
	canary, inject       bool
	inputs, expectedMode *value
}

// modeConfig is what the flags of canary mode select.
type modeConfig struct {
	canary, inject       bool
	inputs, expectedMode string
}

// addModeFlags adds the flags of canary mode to the parser of a command line.
func addModeFlags(p *flagParser) {
	p.fs.BoolVar(&p.mode.canary, flagCanary, false, "")
	p.fs.BoolVar(&p.mode.inject, flagInject, false, "")
	p.mode.inputs = &value{p: p, name: flagInputs}
	p.mode.expectedMode = &value{p: p, name: flagExpectedMode}
	p.fs.Var(p.mode.inputs, flagInputs, "")
	p.fs.Var(p.mode.expectedMode, flagExpectedMode, "")
}

// canaryFlagNames lists the flags canary mode adds to the scratch command line
// (flagNames), sorted.
func canaryFlagNames() []string {
	scratch := map[string]bool{}
	for _, n := range flagNames() {
		scratch[n] = true
	}
	p := newParser()
	addModeFlags(p)
	var names []string
	p.fs.VisitAll(func(f *flag.Flag) {
		if !scratch[f.Name] {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}

// modeOf applies the rules of the two modes: exactly one of --scratch and
// --canary; --inject, --sandbox-inputs and --expected-mode only with --canary;
// --canary needs --sandbox-inputs.
func (p *flagParser) modeOf() (modeConfig, error) {
	m := p.mode
	if !m.canary && (m.inject || m.inputs.set || m.expectedMode.set) {
		return modeConfig{}, usageError("--inject, --sandbox-inputs and --expected-mode are flags of canary mode: they need --canary")
	}
	switch {
	case m.canary && p.scratch:
		return modeConfig{}, usageError("--canary and --scratch cannot be given together")
	case !m.canary && !p.scratch:
		// The guard that keeps a real lead off an unsandboxed holder.
		return modeConfig{}, notScratch{}
	case !m.canary:
		return modeConfig{}, nil
	}
	if !m.inputs.set || m.inputs.s == "" {
		return modeConfig{}, usageError("--canary needs --sandbox-inputs")
	}
	if !absPlain(m.inputs.s) {
		return modeConfig{}, usageError("--sandbox-inputs must be an absolute path of plain text")
	}
	if len(m.expectedMode.s) > maxExpectedMode || !plain(m.expectedMode.s) {
		return modeConfig{}, usageError("--expected-mode must be plain text of at most 256 bytes")
	}
	return modeConfig{canary: true, inject: m.inject, inputs: m.inputs.s, expectedMode: m.expectedMode.s}, nil
}

// canaryDeps are the seams of canary mode, for tests. The zero value is the
// real thing.
type canaryDeps struct {
	// getenv reads the environment for the root check; nil is os.Getenv.
	getenv func(string) string
	// newSystem makes the System of the start sequence; nil is
	// sandboxstart.NewSystem. A test gives a fake, so that no tool, namespace
	// or unit is started.
	newSystem func(sandboxstart.SystemConfig) (sandboxstart.System, error)
	// startSession starts the session of the launched plan; nil is
	// session.Start. It is the launcher's seam: a test looks at the
	// configuration and starts a fake agent in place of the plan.
	startSession func(session.Config) (*session.Session, error)
	// cgroupRoot is the cgroup v2 mount; empty is /sys/fs/cgroup. The file
	// self/cgroup is read under deps.procRoot.
	cgroupRoot string
	// counterInterval is how often the 7501 counters are copied; zero is
	// counterInterval.
	counterInterval time.Duration
}

// leadHasRunMark is the durable mark in the holder's state folder that says a
// lead may have run in the lead's folders (the seams note, section 8,
// "Per-epoch inputs").
const leadHasRunMark = "lead-has-run"

// counterInterval is how often the increases of the tracker's totals are
// copied into the session's counters.
const counterInterval = time.Second

// The session's counters of the status protocol.
const (
	counterQueries = "osc7501-queries"
	counterReports = "osc7501-reports"
	counterInvalid = "osc7501-invalid"
)

// maxDiagnostic bounds a reason printed on stderr.
const maxDiagnostic = 2048

// canaryMode is canary mode for one command line: the inputs that passed the
// holder's refusals, and what the start leaves for the service.
type canaryMode struct {
	cfg config
	d   deps
	in  sandboxstart.Inputs
	sys sandboxstart.System

	tracker *osc7501.Tracker
}

// vettedMode is what the checks of the command line before the role lock
// leave for selectMode.
type vettedMode struct {
	in sandboxstart.Inputs
}

// vetMode makes the refusals of canary mode that write nothing: it reads the
// inputs file and looks at the roots and the paths, so the role lock is not yet
// taken. A command line of scratch mode passes. A refusal has been said, and
// the exit status is returned.
func vetMode(cfg config, d deps, out holderOut) (vettedMode, int) {
	if !cfg.mode.canary {
		return vettedMode{}, exitOK
	}
	refuse := func(format string, args ...any) (vettedMode, int) {
		out.line("refused: "+format, args...)
		return vettedMode{}, exitRefuse
	}
	in, err := sandboxstart.ReadInputs(cfg.mode.inputs)
	if err != nil {
		return refuse("--sandbox-inputs cannot be used: %s", diagnostic(err.Error()))
	}
	if !in.Canary {
		return refuse(`the sandbox inputs say "canary": false, and this is canary mode (--canary)`)
	}
	if in.LayoutRole != in.Project+"-lead" {
		return refuse("the layout role of the sandbox inputs is not the lead of their project (<project>-lead)")
	}
	role, err := leadrole.Parse(cfg.role)
	if err != nil || role.Kind() != leadrole.KindLead || role.Project() != in.Project {
		return refuse("--role is not the lead of the project of the sandbox inputs (<project>/lead)")
	}
	// The root check, with the process's real environment: it also works the
	// default folders out from the account, so a unit's bare environment, with
	// no HOME and no XDG_* variable, does not hide them. It comes before the
	// checks of the holder's own folders, which exist only under roots that
	// were made for the canary.
	getenv := d.canary.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if err := canaryroot.Check(in.StateRoot, in.RuntimeRoot, in.Project, getenv); err != nil {
		return refuse("%s", diagnostic(err.Error()))
	}
	if cfg.roleLock != role.LockPath(in.StateRoot) {
		return refuse("--role-lock is not the lock of the role under the state root of the sandbox inputs")
	}
	for _, f := range []struct{ flag, dir, root, what string }{
		{"--state-dir", cfg.stateDir, in.StateRoot, "state root"},
		{"--runtime-dir", cfg.runtimeDir, in.RuntimeRoot, "runtime root"},
	} {
		if !realBelow(f.dir, f.root) {
			return refuse("%s does not lie under the %s of the sandbox inputs, as a real path with no link on it", f.flag, f.what)
		}
	}
	return vettedMode{in: in}, exitOK
}

// selectMode returns the mode of the command line, once vetMode passed it:
// scratch mode, or canary mode with its system. keep is the holder's own
// descriptor of the role lock. With no mode, the refusal has been said and the
// exit status is returned.
func selectMode(v vettedMode, cfg config, d deps, out holderOut, keep int) (holderMode, int) {
	if !cfg.mode.canary {
		return scratchMode{}, exitOK
	}
	in := v.in
	newSystem := d.canary.newSystem
	if newSystem == nil {
		newSystem = sandboxstart.NewSystem
	}
	// Assertion 9 asks the lock with the layout role, not with --role.
	sys, err := newSystem(sandboxstart.SystemConfig{RoleLock: heldRoleLock{fd: keep, path: cfg.roleLock, layoutRole: in.LayoutRole}})
	if err != nil {
		out.line("refused: the start sequence has no system: %s", diagnostic(err.Error()))
		return nil, exitRefuse
	}
	return &canaryMode{cfg: cfg, d: d, in: in, sys: sys}, exitOK
}

// realBelow says dir lies below root, both as given: dir is a clean path, no
// part of it is a symbolic link, and root is a proper prefix of it.
func realBelow(dir, root string) bool {
	if filepath.Clean(dir) != dir || !strings.HasPrefix(dir, root+"/") {
		return false
	}
	real, err := filepath.EvalSymlinks(dir)
	return err == nil && real == dir
}

// heldRoleLock answers assertion 9 for the lock this holder took: the role
// asked about is the layout role, the lock file at the path is still the file
// the holder has open, and the holder's open file description holds the lock
// (asking for a lock it holds changes nothing).
type heldRoleLock struct {
	fd         int
	path       string
	layoutRole string
}

func (l heldRoleLock) Held(role string) (bool, error) {
	if role != l.layoutRole {
		return false, nil
	}
	var mine, there unix.Stat_t
	if err := unix.Fstat(l.fd, &mine); err != nil {
		return false, err
	}
	if err := unix.Lstat(l.path, &there); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	if mine.Dev != there.Dev || mine.Ino != there.Ino {
		return false, nil
	}
	if err := unix.Flock(l.fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (m *canaryMode) state(st *stateFile) { st.Canary = true }

// sessionLauncher is the start sequence's launcher: it starts the plan's
// argument vector as the session's child. base is the session's configuration
// with the hook installed, no controlling terminal and an empty environment;
// the plan sets everything inside.
type sessionLauncher struct {
	base  session.Config
	start func(session.Config) (*session.Session, error)
	// mark makes the durable mark lead-has-run, exclusively and synced, right
	// before the session starts the plan's first process. A launch whose mark
	// cannot be made is refused, with nothing started.
	mark func() error

	// called is true once the session's start was called: it owns the role
	// lock from then on, whether it succeeded or not.
	called bool
	sess   *session.Session
}

func (l *sessionLauncher) Launch(argv []string) (sandboxstart.Child, error) {
	if l.called {
		return nil, errors.New("the holder launches one plan")
	}
	cfg := l.base
	cfg.Argv = argv
	if l.mark != nil {
		if err := l.mark(); err != nil {
			return nil, errors.New("the mark " + leadHasRunMark + " cannot be made: " + err.Error())
		}
	}
	l.called = true
	s, err := l.start(cfg)
	if err != nil {
		return nil, errors.New("the session of the plan did not start: " + startReason(err))
	}
	if s == nil {
		return nil, errors.New("the session of the plan did not start")
	}
	l.sess = s
	return sessionChild{s}, nil
}

// sessionChild is the start sequence's Child over the session that owns the
// first process (see the comment at the top of the file).
type sessionChild struct{ s *session.Session }

func (c sessionChild) Exited() <-chan struct{} { return c.s.Done() }

// Stop stops the tree with the session's grace, which is the stop rule's, and
// waits for the session's lifetime path, so that TreeGone reads a concluded
// fact.
func (c sessionChild) Stop(time.Duration) {
	c.s.Stop()
	<-c.s.Done()
}

func (c sessionChild) TreeGone() (bool, string) {
	f := c.s.Facts()
	if f.TreeGone != session.Yes {
		if f.TreeProof == "" {
			return false, "the session does not know that the process tree is gone"
		}
		return false, f.TreeProof
	}
	return true, f.TreeProof
}

func (c sessionChild) Close() error { return c.s.Close() }

// start runs the start sequence and returns the session of the sandbox's first
// process.
func (m *canaryMode) start(sc startContext) (*session.Session, int) {
	cfg, out := sc.cfg, sc.out
	epoch := strconv.FormatUint(cfg.epoch, 10)
	lock := sc.scfg.HeldRoleLock

	scfg := sc.scfg
	m.tracker = &osc7501.Tracker{}
	// The plan's first process gets no variable of the holder's and none of
	// the command line's: the plan sets the environment inside.
	scfg.Argv, scfg.Env = nil, nil
	scfg.OSC = append(append([]session.OSCHook(nil), scfg.OSC...), injectbridge.Hook(m.tracker, nil))
	scfg.PTY.NoControllingTerminal = true
	launch := &sessionLauncher{base: scfg, start: m.d.canary.startSession}
	if launch.start == nil {
		launch.start = session.Start
	}

	// fail ends a start that did not succeed: no agent, no lock held by a
	// session, no state file, and the refusal in the event log.
	fail := func(code int, reason, format string, args ...any) (*session.Session, int) {
		switch {
		case launch.sess != nil:
			_ = launch.sess.Close()
		case !launch.called && lock != nil:
			lock.Close()
		}
		_ = sc.log.Append(eventlog.KindHolder, eventlog.EventRefusal, eventlog.Fields{Role: cfg.role, Epoch: epoch, Reason: reason})
		if err := removeStateFile(cfg.stateDir); err != nil {
			out.line("cannot remove the state file: %v", err)
		}
		out.line(format, args...)
		return nil, code
	}

	// The fields no file can set.
	in := m.in
	in.AgentArgv = append([]string(nil), cfg.argv...)
	in.AgentEnv = nil
	for _, kv := range cfg.env {
		name, val, _ := strings.Cut(kv, "=")
		in.AgentEnv = append(in.AgentEnv, profile.EnvVar{Name: name, Value: val})
	}
	in.ExpectedDigest = cfg.profileDigest
	in.HolderPaths = []string{
		filepath.Join(cfg.runtimeDir, serve.AttachSocket),
		filepath.Join(cfg.runtimeDir, serve.ControlSocket),
	}
	if holder := (profile.Layout{StateRoot: in.StateRoot, Role: in.LayoutRole, Project: in.Project}).Holder(); cfg.stateDir != holder {
		// The sequence hides the layout's holder folder itself.
		in.HolderPaths = append(in.HolderPaths, cfg.stateDir)
	}
	in.PreviousInstanceStopped = aloneInControlGroup(m.d.procRoot, m.d.canary.cgroupRoot, os.Getpid())
	// NoLeadHasRun is decided here, before the sequence: no lead has run when
	// the mark is absent (the role lock keeps another holder from racing this).
	// The mark itself is made by the launcher, just before the plan's first
	// process starts, so a start that fails before the launch leaves none and
	// the next start prepares the folders again.
	first, err := leadHasRunAbsent(cfg.stateDir)
	if err != nil {
		return fail(exitRefuse, "lead-has-run-mark-failed", "refused: cannot read the mark %s in the state folder: %v", leadHasRunMark, err)
	}
	in.NoLeadHasRun = first
	if first {
		launch.mark = func() error {
			// The mark is made exclusively. One that is there now appeared
			// after the decision above, which nothing should do: the launch
			// is refused. When the mark was there at the decision, it is
			// there still and the launcher has nothing to make.
			made, err := markLeadHasRun(cfg.stateDir)
			if err == nil && !made {
				err = errors.New("it appeared during the start")
			}
			return err
		}
	}

	// A stop signal during the sequence ends it: the sequence stops the child
	// it launched.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		st  sandboxstart.Started
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := sandboxstart.Run(ctx, in, m.sys, launch)
		done <- result{st, err}
	}()
	var stop *syscall.Signal
	var res result
wait:
	for {
		select {
		case s := <-sc.sigc:
			if sg, ok := s.(syscall.Signal); ok && isStopSignal(sg) && stop == nil {
				stop = &sg
				cancel()
			}
		case res = <-done:
			break wait
		}
	}
	if stop != nil {
		return fail(exitRefuse, "signal-during-start", "stopped by %s during the start of the sandbox", signalName(*stop))
	}
	if res.err != nil {
		step := "before any step"
		var se *sandboxstart.StepError
		if errors.As(res.err, &se) {
			step = "at the step " + se.Step
		}
		return fail(exitSandbox, "sandbox-start-failed", "the start of the sandbox was refused %s; no agent is left: %s", step, diagnostic(res.err.Error()))
	}
	st := res.st
	if launch.sess == nil || st.Child == nil {
		return fail(exitSandbox, "sandbox-start-failed", "the start of the sandbox returned no child; no agent is left")
	}
	if st.ProfileDigest != cfg.profileDigest {
		return fail(exitSandbox, "sandbox-start-failed", "the profile digest of the started plan is not the pinned one (--profile-digest); the agent was stopped")
	}

	// The start succeeded: the state file says so, with the waiver.
	file := sc.st
	file.SandboxStarted = true
	file.ProfileVersion = st.ProfileVersion
	file.ProfileDigest = st.ProfileDigest
	file.StartNotes = append([]string(nil), st.Notes...)
	if st.Waived {
		file.Waiver = st.Waiver
	}
	if err := writeStateFile(cfg.stateDir, file); err != nil {
		return fail(exitSandbox, "state-file-failed", "cannot write the state file after the start of the sandbox; the agent was stopped: %v", err)
	}
	if st.Waived {
		// The event log holds codes, not the waiver's text.
		_ = sc.log.Append(eventlog.KindHolder, eventlog.EventState, eventlog.Fields{Role: cfg.role, Epoch: epoch, State: "sandbox-started", Reason: "assertion-10-waived"})
		out.line("%s", st.Waiver)
	}
	for _, n := range st.Notes {
		out.line("note of the start: %s", diagnostic(n))
	}
	return launch.sess, exitOK
}

// serve makes the bridge, starts the copy of the 7501 counters and, only with
// --inject, gives the service its deliverer.
func (m *canaryMode) serve(sess *session.Session, vcfg *serve.Config) {
	bridge := injectbridge.New(sess, injectbridge.Config{Runtime: m.cfg.runtime, Version: m.cfg.runtimeVersion, Tracker: m.tracker})
	pump := &counterPump{tracker: m.tracker, sess: sess}
	every := m.d.canary.counterInterval
	if every <= 0 {
		every = counterInterval
	}
	go pump.run(every)
	if m.cfg.mode.inject {
		vcfg.Deliverer = countingDeliverer{pump: pump, next: injectbridge.NewDeliverer(bridge, m.cfg.mode.expectedMode)}
	}
}

// counterPump copies the increases of the tracker's totals into the session's
// counters, where the control status answer, the exit event and the final
// record show them.
type counterPump struct {
	tracker *osc7501.Tracker
	sess    *session.Session

	mu   sync.Mutex
	last injectbridge.Totals
}

// copy adds what the totals grew by since the last copy.
func (p *counterPump) copy() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := injectbridge.Counts(p.tracker)
	for _, c := range []struct {
		name      string
		now, last uint64
	}{
		{counterQueries, now.Queries, p.last.Queries},
		{counterReports, now.Reports, p.last.Reports},
		{counterInvalid, now.Invalid, p.last.Invalid},
	} {
		if c.now > c.last {
			_ = p.sess.AddCount(c.name, c.now-c.last)
		}
	}
	p.last = now
}

// run copies every interval and stops with the session.
func (p *counterPump) run(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.sess.Done():
			return
		case <-t.C:
			p.copy()
		}
	}
}

// countingDeliverer copies the counters before each delivery.
type countingDeliverer struct {
	pump *counterPump
	next serve.Deliverer
}

func (d countingDeliverer) Deliver(ctx context.Context, messageID, text string) wire.InjectResult {
	d.pump.copy()
	return d.next.Deliver(ctx, messageID, text)
}

// leadHasRunAbsent says nothing is at the mark's name in the state folder,
// asked by the folder's handle without following a link: a link, or anything
// else, at the name is the mark of an earlier start.
func leadHasRunAbsent(dir string) (bool, error) {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(dfd)
	var st unix.Stat_t
	err = unix.Fstatat(dfd, leadHasRunMark, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	return false, err
}

// markLeadHasRun makes the mark lead-has-run in the state folder, by the
// folder's handle and exclusively. first is true when this call made it: no
// earlier start had. Anything already at the name, of any kind, is the mark of
// an earlier start.
func markLeadHasRun(dir string) (first bool, err error) {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(dfd)
	fd, err := unix.Openat(dfd, leadHasRunMark, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = unix.Fchmod(fd, 0o600)
	if err == nil {
		err = unix.Fsync(fd)
	}
	if cerr := unix.Close(fd); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Fsync(dfd)
	}
	// The mark exists whatever the error: a lead may have run from now on.
	return true, err
}

// The bounds of the control-group reading.
const (
	maxCgroupFile    = 64 << 10
	maxCgroupProcs   = 1 << 20
	maxCgroupEntries = 4096
)

// aloneInControlGroup says this process's cgroup v2 group, named in
// procRoot/self/cgroup, holds no process but self and has no child group (a
// process of a child group is not listed in the parent's cgroup.procs). It is
// the holder's proof that no process of an earlier instance of the lead
// remains: the unit's name is fixed, so an earlier instance ran in this group.
// Anything that cannot be read is false: nothing is proven.
func aloneInControlGroup(procRoot, cgroupRoot string, self int) bool {
	if procRoot == "" {
		procRoot = "/proc"
	}
	if cgroupRoot == "" {
		cgroupRoot = "/sys/fs/cgroup"
	}
	b, ok := readBoundedFile(filepath.Join(procRoot, "self", "cgroup"), maxCgroupFile)
	if !ok {
		return false
	}
	group := ""
	for _, line := range strings.Split(string(b), "\n") {
		if rest, found := strings.CutPrefix(line, "0::"); found {
			group = rest
			break
		}
	}
	if !strings.HasPrefix(group, "/") || strings.HasSuffix(group, " (deleted)") {
		return false
	}
	dir := filepath.Join(cgroupRoot, filepath.Clean(group))
	procs, ok := readBoundedFile(filepath.Join(dir, "cgroup.procs"), maxCgroupProcs)
	if !ok {
		return false
	}
	seen := false
	for _, f := range strings.Fields(string(procs)) {
		if pid, err := strconv.Atoi(f); err != nil || pid != self {
			return false
		}
		seen = true
	}
	if !seen {
		return false
	}
	d, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer d.Close()
	entries, err := d.ReadDir(maxCgroupEntries + 1)
	if (err != nil && len(entries) == 0) || len(entries) > maxCgroupEntries {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return false
		}
	}
	return true
}

// readBoundedFile reads a file of at most limit bytes; a longer one is not
// read.
func readBoundedFile(path string, limit int) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, limit+1)
	n := 0
	for n < len(buf) {
		m, err := f.Read(buf[n:])
		n += m
		if err != nil {
			break
		}
	}
	if n > limit {
		return nil, false
	}
	return buf[:n], true
}

// diagnostic makes a reason fit for the holder's stderr: one line of plain
// text, bounded. The reasons of the start sequence name paths of the canary's
// own roots, which the owner needs to see to repair an attended run.
func diagnostic(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len()+utf8.UTFMax > maxDiagnostic {
			b.WriteString("...")
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}
