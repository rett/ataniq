// Package switcher is ah-switch, the one switch of the authority host: it
// verifies a release folder, runs the release's checks, moves the current
// symbolic link atomically, checks health and goes back on failure, and
// records the outcome in deploy.json. It is used by hand on the manual path and
// by the updater later, and it never deletes a release.
//
// The layout below the root (/ on the host, a temporary folder in tests):
//
//	opt/authority-host/releases/<id>/   MANIFEST, SHA256SUMS, [SHA256SUMS.sig], bin/, etc/, share/
//	opt/authority-host/current  -> releases/<id>
//	opt/authority-host/previous -> releases/<id>
//	var/lib/authority-host/deploy.json, highest-counter, switch.lock
package switcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Exit codes of ah-switch.
const (
	ExitOK     = 0 // switched and healthy, or a dry run whose checks passed
	ExitFailed = 1 // failed; the outcome is recorded in deploy.json (not in a dry run)
	ExitUsage  = 2 // bad arguments; nothing was done
	ExitLocked = 3 // another switch holds the lock; nothing was done
)

// Modes.
const (
	ModeManual = "manual" // SHA256SUMS only
	ModeSigned = "signed" // SHA256SUMS and its signature
)

// Paths below the root.
const (
	BaseDir     = "opt/authority-host"
	StateDir    = "var/lib/authority-host"
	DeployFile  = "deploy.json"
	HighestFile = "highest-counter"
	LockFile    = "switch.lock"
)

// Programs of a release, below its folder.
const (
	NoAICheckProgram   = "bin/ah-noaicheck"
	PackageStepProgram = "bin/ah-pkgstep"
)

// Time limits.
const (
	NoAICheckTimeout     = 5 * time.Minute
	ValidatorTimeout     = 30 * time.Second
	PackageStepTimeout   = 30 * time.Minute
	ReloadTimeout        = 30 * time.Second
	HealthWindow         = 60 * time.Second
	HealthInterval       = 2 * time.Second
	HealthAttemptTimeout = 10 * time.Second
)

// The steps, as printed and as recorded in failed_step. A failure that comes
// before step 1 (reading current and previous) is recorded as "state", one of
// the reload list as "reload".
var stepNames = [...]string{"", "verify", "counter", "noaicheck", "validate", "packages", "switch", "health", "record"}

// Config is what a switch is run with. Zero fields take their defaults.
type Config struct {
	Root           string // default /
	Dry            bool   // print every step, run no program, change nothing
	Mode           string // ModeManual (default) or ModeSigned
	AllowedSigners string // the ssh allowed signers file, for ModeSigned only
	NoAICheck      string // the no-AI check program; default the release's bin/ah-noaicheck
	Runner         Runner
	Verifier       Verifier
	Out            io.Writer // the steps
	Err            io.Writer // refusals of the arguments and of the lock; default Out
	Now            func() time.Time
	Sleep          func(time.Duration)
}

// Switcher runs switches with one Config.
type Switcher struct {
	cfg Config
	// afterPrevious, when set, is called between writing previous and
	// renaming current; an error stops the switch there as a crash would.
	afterPrevious func() error
}

// New returns a Switcher, filling in the defaults of cfg.
func New(cfg Config) *Switcher {
	if cfg.Root == "" {
		cfg.Root = "/"
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeManual
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecRunner{}
	}
	if cfg.Verifier == nil {
		cfg.Verifier = SSHVerifier{Runner: cfg.Runner}
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Err == nil {
		cfg.Err = cfg.Out
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Switcher{cfg: cfg}
}

func (s *Switcher) base() string     { return filepath.Join(s.cfg.Root, BaseDir) }
func (s *Switcher) stateDir() string { return filepath.Join(s.cfg.Root, StateDir) }

func (s *Switcher) printf(format string, args ...any) {
	fmt.Fprintf(s.cfg.Out, format+"\n", args...)
}

func (s *Switcher) errorf(format string, args ...any) {
	fmt.Fprintf(s.cfg.Err, "ah-switch: "+format+"\n", args...)
}

func (s *Switcher) step(n int, format string, args ...any) {
	s.printf("step %d/8 %s: %s", n, stepNames[n], fmt.Sprintf(format, args...))
}

func (s *Switcher) checkConfig() error {
	if !filepath.IsAbs(s.cfg.Root) {
		return fmt.Errorf("--root %q is not an absolute path", s.cfg.Root)
	}
	switch s.cfg.Mode {
	case ModeManual:
		if s.cfg.AllowedSigners != "" {
			return errors.New("--allowed-signers is only for --mode signed")
		}
	case ModeSigned:
		if s.cfg.AllowedSigners == "" {
			return errors.New("--mode signed needs --allowed-signers FILE")
		}
	default:
		return fmt.Errorf("--mode %q is neither manual nor signed", s.cfg.Mode)
	}
	return nil
}

// targetID reads the release id out of the target of current or previous,
// which must be releases/<id> or the absolute path of that folder.
func (s *Switcher) targetID(target string) (ReleaseID, error) {
	t := filepath.Clean(target)
	if filepath.IsAbs(t) {
		rel, err := filepath.Rel(s.base(), t)
		if err != nil {
			return ReleaseID{}, err
		}
		t = rel
	}
	dir, name := filepath.Split(t)
	if dir != "releases/" {
		return ReleaseID{}, fmt.Errorf("link target %q is not a folder of releases/", target)
	}
	return ParseID(name)
}

// attempt is the state of one switch.
type attempt struct {
	rec        Record
	current    string // path of the current link
	previous   string // path of the previous link
	oldTarget  string // what current pointed at, as written in the link
	oldExists  bool
	prevTarget string // what previous pointed at
	prevExists bool
	reloads    [][]string
	health     []string
}

// Switch activates the release named id.
func (s *Switcher) Switch(ctx context.Context, id string) int {
	return s.run(ctx, id, false)
}

// RollBack goes back to the release previous points at. which is "previous"
// or the id of that release; any other release is refused.
func (s *Switcher) RollBack(ctx context.Context, which string) int {
	return s.run(ctx, which, true)
}

func (s *Switcher) run(ctx context.Context, arg string, rollback bool) int {
	if err := s.checkConfig(); err != nil {
		s.errorf("%v", err)
		return ExitUsage
	}
	if rollback {
		if arg != "previous" {
			if _, err := ParseID(arg); err != nil {
				s.errorf("--rollback-to takes previous")
				return ExitUsage
			}
		}
	} else if _, err := ParseID(arg); err != nil {
		s.errorf("%v", err)
		return ExitUsage
	}

	what := "switch to " + arg
	if rollback {
		what = "roll back to " + arg
	}
	dry := ""
	if s.cfg.Dry {
		dry = ", dry run: nothing is run or changed"
	}
	s.printf("ah-switch: %s (mode %s, root %s%s)", what, s.cfg.Mode, s.cfg.Root, dry)

	lk, code := s.lock()
	if lk == nil {
		return code
	}
	defer lk.release()

	a := &attempt{
		current:  filepath.Join(s.base(), "current"),
		previous: filepath.Join(s.base(), "previous"),
		rec: Record{
			ID:       arg,
			Mode:     s.cfg.Mode,
			Rollback: rollback,
			Check:    CheckNotRun,
		},
	}
	if code, done := s.readState(a); done {
		return code
	}

	// Which release.
	name := arg
	if rollback {
		prevID := ""
		if a.prevExists {
			id, err := s.targetID(a.prevTarget)
			if err != nil {
				return s.fail(a, 2, err)
			}
			prevID = id.Name
		}
		switch {
		case !a.prevExists:
			return s.fail(a, 2, errors.New("there is no previous release to go back to"))
		case arg != "previous" && arg != prevID:
			return s.fail(a, 2, fmt.Errorf("--rollback-to accepts only previous, which points at %s; %s is refused", prevID, arg))
		case prevID == a.rec.PreviousID:
			return s.fail(a, 2, fmt.Errorf("previous and current both point at %s; there is nothing to go back to", prevID))
		}
		name = prevID
		a.rec.ID = name
		s.printf("rollback: previous points at %s", name)
	}
	id, _ := ParseID(name)
	a.rec.Counter = id.Counter
	relDir := filepath.Join(s.base(), "releases", id.Name)

	// 1. Verify.
	rel, err := openRelease(id, relDir)
	if err != nil {
		return s.fail(a, 1, err)
	}
	defer rel.Close()
	s.step(1, "releases/%s: %d files match %s", id.Name, rel.files, SumsFile)
	if code, done := s.verifySignature(ctx, a, rel); done {
		return code
	}
	data, err := rel.readSmall(ManifestFile)
	if err != nil {
		return s.fail(a, 1, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return s.fail(a, 1, err)
	}
	if err := manifest.Check(id); err != nil {
		return s.fail(a, 1, err)
	}
	validators, reloads, health, err := rel.readLists()
	if err != nil {
		return s.fail(a, 1, err)
	}
	a.reloads, a.health = reloads, health
	s.step(1, "MANIFEST: commit %s, built on %s, %s, package snapshot %s", manifest.Commit, manifest.BuiltOn, manifest.GoVersion, manifest.PackageSnapshot)

	// 2. Counter.
	highestPath := filepath.Join(s.stateDir(), HighestFile)
	highest, err := readHighest(highestPath)
	if err != nil {
		return s.fail(a, 2, err)
	}
	if rollback {
		s.step(2, "rollback to previous: the counter rule does not apply; the highest counter stays %d", highest)
	} else {
		if id.Counter <= highest {
			return s.fail(a, 2, fmt.Errorf("counter %d is not above the highest ever activated, %d", id.Counter, highest))
		}
		s.step(2, "counter %d is above the highest ever activated, %d", id.Counter, highest)
	}

	// 3. The no-AI check of the new release.
	check := s.cfg.NoAICheck
	if check == "" {
		check = filepath.Join(rel.dir, NoAICheckProgram)
		ok, err := rel.exists(NoAICheckProgram)
		if err == nil && !ok {
			err = fmt.Errorf("%s missing in the release", NoAICheckProgram)
		}
		if err != nil {
			return s.fail(a, 3, err)
		}
	} else if info, err := os.Stat(check); err != nil || !info.Mode().IsRegular() {
		return s.fail(a, 3, fmt.Errorf("no-AI check %s is not a regular file", check))
	}
	argv := []string{check, "--release", rel.dir}
	if s.cfg.Dry {
		s.step(3, "would run %s", strings.Join(argv, " "))
	} else {
		out, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: NoAICheckTimeout})
		if err != nil {
			a.rec.Check = CheckFailed
			return s.fail(a, 3, withOutput(err, out))
		}
		a.rec.Check = CheckPassed
		s.step(3, "passed")
	}

	// 4. Validators.
	if len(validators) == 0 {
		s.step(4, "no %s: nothing to validate", ValidatorsList)
	}
	for _, v := range validators {
		if s.cfg.Dry {
			s.step(4, "would run %s", strings.Join(v, " "))
			continue
		}
		out, err := s.cfg.Runner.Run(ctx, Command{Argv: v, Timeout: ValidatorTimeout})
		if err != nil {
			return s.fail(a, 4, withOutput(err, out))
		}
		s.step(4, "ok: %s", strings.Join(v, " "))
	}

	// 5. Packages.
	if manifest.PackageSnapshot == a.rec.PackageSnapshot {
		s.step(5, "package snapshot %s unchanged", manifest.PackageSnapshot)
	} else {
		ok, err := rel.exists(PackageStepProgram)
		if err != nil {
			return s.fail(a, 5, err)
		}
		switch {
		case !ok:
			s.step(5, "package snapshot %q -> %q; the release has no %s: nothing to do", a.rec.PackageSnapshot, manifest.PackageSnapshot, PackageStepProgram)
		case s.cfg.Dry:
			s.step(5, "would run %s --release %s --from %q --to %q", filepath.Join(rel.dir, PackageStepProgram), rel.dir, a.rec.PackageSnapshot, manifest.PackageSnapshot)
		default:
			argv := []string{filepath.Join(rel.dir, PackageStepProgram), "--release", rel.dir, "--from", a.rec.PackageSnapshot, "--to", manifest.PackageSnapshot}
			out, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: PackageStepTimeout})
			if err != nil {
				return s.fail(a, 5, withOutput(err, out))
			}
			a.rec.PackageSnapshot = manifest.PackageSnapshot
			s.step(5, "package step done: snapshot %s", manifest.PackageSnapshot)
		}
	}

	// 6. Switch, then reload.
	newTarget := filepath.Join("releases", id.Name)
	if s.cfg.Dry {
		if a.oldExists {
			s.step(6, "would point previous at %s", a.oldTarget)
		}
		s.step(6, "would point current at %s (a new link renamed over it)", newTarget)
		for _, r := range a.reloads {
			s.step(6, "would reload: %s", strings.Join(r, " "))
		}
		s.step(7, "would run %s until it passes, every %s for up to %s", strings.Join(a.health, " "), HealthInterval, HealthWindow)
		s.step(8, "would record the result in %s", filepath.Join(s.stateDir(), DeployFile))
		s.printf("result: dry run passed; nothing changed")
		return ExitOK
	}
	if a.oldExists {
		if err := replaceLink(a.previous, a.oldTarget); err != nil {
			return s.fail(a, 6, fmt.Errorf("previous: %w", err))
		}
	}
	if s.afterPrevious != nil {
		if err := s.afterPrevious(); err != nil {
			return ExitFailed
		}
	}
	if err := replaceLink(a.current, newTarget); err != nil {
		s.restorePrevious(a)
		return s.fail(a, 6, fmt.Errorf("current: %w", err))
	}
	a.rec.CurrentID = id.Name
	if a.oldExists {
		s.step(6, "previous -> %s", a.oldTarget)
	}
	s.step(6, "current -> %s", newTarget)
	if err := s.reload(ctx, a); err != nil {
		return s.goBack(ctx, a, "reload", err)
	}

	// 7. Health.
	if err := s.checkHealth(ctx, a.health); err != nil {
		return s.goBack(ctx, a, stepNames[7], err)
	}

	// 8. Record.
	a.rec.Result = ResultOK
	if !rollback {
		if err := writeAtomic(highestPath, []byte(fmt.Sprintf("%d\n", id.Counter)), 0o644); err != nil {
			a.rec.FailedStep = stepNames[8]
			a.rec.Error = "highest-counter not written: " + err.Error()
			s.step(8, "FAILED: %s", a.rec.Error)
			s.record(a)
			return ExitFailed
		}
		s.step(8, "highest counter %d", id.Counter)
	}
	if err := s.record(a); err != nil {
		return ExitFailed
	}
	s.step(8, "recorded in %s", filepath.Join(s.stateDir(), DeployFile))
	s.printf("result: ok; current -> %s", id.Name)
	return ExitOK
}

// lock takes the switch lock. A dry run creates nothing: it takes the lock only
// when the lock file is there. On failure it returns nil and the exit code.
func (s *Switcher) lock() (*lock, int) {
	path := filepath.Join(s.stateDir(), LockFile)
	if !s.cfg.Dry {
		if err := os.MkdirAll(s.stateDir(), 0o755); err != nil {
			s.errorf("%v", err)
			return nil, ExitFailed
		}
	}
	lk, err := takeLock(path, !s.cfg.Dry)
	if errors.Is(err, errLocked) {
		s.errorf("%v (%s); nothing done", err, path)
		return nil, ExitLocked
	}
	if err != nil {
		s.errorf("lock: %v", err)
		return nil, ExitFailed
	}
	return lk, ExitOK
}

// readState reads current, previous and the last record.
func (s *Switcher) readState(a *attempt) (int, bool) {
	var err error
	a.oldTarget, a.oldExists, err = readLink(a.current)
	if err != nil {
		return s.failState(a, err), true
	}
	if a.oldExists {
		id, err := s.targetID(a.oldTarget)
		if err != nil {
			return s.failState(a, fmt.Errorf("current: %w", err)), true
		}
		a.rec.PreviousID = id.Name
		a.rec.CurrentID = id.Name
	}
	a.prevTarget, a.prevExists, err = readLink(a.previous)
	if err != nil {
		return s.failState(a, err), true
	}
	last, _, err := readRecord(filepath.Join(s.stateDir(), DeployFile))
	if err != nil {
		s.printf("warning: %v; the package snapshot is taken as unknown", err)
	}
	a.rec.PackageSnapshot = last.PackageSnapshot
	current := "none"
	if a.oldExists {
		current = a.rec.CurrentID
	}
	s.printf("state: current %s", current)
	return 0, false
}

func (s *Switcher) failState(a *attempt, err error) int {
	s.printf("state: FAILED: %v", err)
	a.rec.Result = ResultFailed
	a.rec.FailedStep = "state"
	a.rec.Error = err.Error()
	s.record(a)
	s.printf("result: failed before step 1; nothing changed")
	return ExitFailed
}

func (s *Switcher) verifySignature(ctx context.Context, a *attempt, rel *release) (int, bool) {
	present, err := rel.exists(SignatureFile)
	if err != nil {
		return s.fail(a, 1, err), true
	}
	if s.cfg.Mode == ModeManual {
		note := ""
		if present {
			note = " (" + SignatureFile + " is there and is not read)"
		}
		s.step(1, "signature not checked: mode manual%s", note)
		return 0, false
	}
	if !present {
		return s.fail(a, 1, fmt.Errorf("%s missing; mode signed needs it", SignatureFile)), true
	}
	if s.cfg.Dry {
		s.step(1, "would verify %s against %s", SignatureFile, s.cfg.AllowedSigners)
		return 0, false
	}
	if err := s.cfg.Verifier.Verify(ctx, s.cfg.AllowedSigners, rel.sums, filepath.Join(rel.dir, SignatureFile)); err != nil {
		return s.fail(a, 1, fmt.Errorf("%s: %w", SignatureFile, err)), true
	}
	s.step(1, "%s verified against %s", SignatureFile, s.cfg.AllowedSigners)
	return 0, false
}

// fail ends a switch that failed at step n before current was moved: current
// is untouched. Outside a dry run the outcome is recorded.
func (s *Switcher) fail(a *attempt, n int, err error) int {
	s.step(n, "FAILED: %v", err)
	a.rec.Result = ResultFailed
	a.rec.FailedStep = stepNames[n]
	a.rec.Error = err.Error()
	if s.cfg.Dry {
		s.printf("result: failed at %s (dry run: nothing recorded); current unchanged", stepNames[n])
		return ExitFailed
	}
	s.record(a)
	cur := a.rec.CurrentID
	if cur == "" {
		cur = "none"
	}
	s.printf("result: failed at %s; current unchanged (%s)", stepNames[n], cur)
	return ExitFailed
}

func (s *Switcher) reload(ctx context.Context, a *attempt) error {
	for _, r := range a.reloads {
		out, err := s.cfg.Runner.Run(ctx, Command{Argv: r, Timeout: ReloadTimeout})
		if err != nil {
			s.step(6, "reload FAILED: %s: %v", strings.Join(r, " "), withOutput(err, out))
			return withOutput(err, out)
		}
		s.step(6, "reloaded: %s", strings.Join(r, " "))
	}
	return nil
}

// checkHealth runs the health command until it passes, every HealthInterval,
// within HealthWindow.
func (s *Switcher) checkHealth(ctx context.Context, argv []string) error {
	deadline := s.cfg.Now().Add(HealthWindow)
	var last error
	attempts := 0
	for {
		attempts++
		limit := min(HealthAttemptTimeout, deadline.Sub(s.cfg.Now()))
		if limit < time.Second {
			limit = time.Second
		}
		out, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: limit})
		if err == nil {
			s.step(7, "healthy after %d attempt(s): %s", attempts, strings.Join(argv, " "))
			return nil
		}
		last = withOutput(err, out)
		if !s.cfg.Now().Add(HealthInterval).Before(deadline) {
			break
		}
		s.cfg.Sleep(HealthInterval)
	}
	return fmt.Errorf("not healthy after %d attempts in %s: %v", attempts, HealthWindow, last)
}

// goBack is called when the reload or the health check failed after current
// was moved: it points current back at the old release, puts previous back,
// runs the reload list again, checks health again and records the outcome.
func (s *Switcher) goBack(ctx context.Context, a *attempt, step string, cause error) int {
	n := 7
	if step == "reload" {
		n = 6
	}
	s.step(n, "FAILED: %v", cause)
	a.rec.FailedStep = step
	a.rec.Error = cause.Error()
	if !a.oldExists {
		a.rec.Result = ResultFailed
		a.rec.Error += "; there is no earlier release to go back to, so current stays at " + a.rec.CurrentID
		s.record(a)
		s.printf("result: failed at %s; no earlier release, current stays at %s", step, a.rec.CurrentID)
		return ExitFailed
	}
	if err := replaceLink(a.current, a.oldTarget); err != nil {
		a.rec.Result = ResultFailed
		a.rec.Error += "; going back failed: " + err.Error()
		s.record(a)
		s.printf("result: failed at %s and could not go back (%v); current stays at %s", step, err, a.rec.CurrentID)
		return ExitFailed
	}
	failedID := a.rec.CurrentID
	a.rec.CurrentID = a.rec.PreviousID
	s.restorePrevious(a)
	s.printf("back: current -> %s", a.oldTarget)
	if err := s.reload(ctx, a); err != nil {
		a.rec.Error += "; reload after going back failed: " + err.Error()
	}
	if err := s.checkHealth(ctx, a.health); err != nil {
		a.rec.RestoredHealth = "failed"
		a.rec.Error += "; the release gone back to is not healthy either: " + err.Error()
	} else {
		a.rec.RestoredHealth = "ok"
	}
	a.rec.Result = ResultRolledBack
	s.record(a)
	s.printf("result: rolled-back: %s failed at %s, current -> %s (health after going back: %s)", failedID, step, a.rec.CurrentID, a.rec.RestoredHealth)
	return ExitFailed
}

// restorePrevious puts previous back as it was before the switch.
func (s *Switcher) restorePrevious(a *attempt) {
	var err error
	if a.prevExists {
		err = replaceLink(a.previous, a.prevTarget)
	} else if err = os.Remove(a.previous); errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		s.printf("warning: previous not put back: %v", err)
	}
}

// record writes deploy.json. A dry run records nothing.
func (s *Switcher) record(a *attempt) error {
	if s.cfg.Dry {
		return nil
	}
	a.rec.Time = s.cfg.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(a.rec, "", "  ")
	if err == nil {
		err = writeAtomic(filepath.Join(s.stateDir(), DeployFile), append(data, '\n'), 0o644)
	}
	if err != nil {
		s.printf("ah-switch: %s not written: %v", DeployFile, err)
		return err
	}
	return nil
}

func withOutput(err error, out Output) error {
	if t := tail(out); t != "" {
		return fmt.Errorf("%w: %s", err, t)
	}
	return err
}

// Status prints current, previous, the highest counter and the last record.
func Status(root string, w io.Writer) error {
	s := New(Config{Root: root})
	for _, name := range []string{"current", "previous"} {
		target, ok, err := readLink(filepath.Join(s.base(), name))
		switch {
		case err != nil:
			return err
		case !ok:
			fmt.Fprintf(w, "%s: none\n", name)
		default:
			id, err := s.targetID(target)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			fmt.Fprintf(w, "%s: %s\n", name, id.Name)
		}
	}
	highest, err := readHighest(filepath.Join(s.stateDir(), HighestFile))
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "highest counter: %d\n", highest)
	rec, ok, err := readRecord(filepath.Join(s.stateDir(), DeployFile))
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w, "last switch: none recorded")
		return nil
	}
	kind := "switch to"
	if rec.Rollback {
		kind = "rollback to"
	}
	fmt.Fprintf(w, "last switch: %s %s: %s at %s (mode %s)\n", kind, rec.ID, rec.Result, rec.Time, rec.Mode)
	fmt.Fprintf(w, "no-AI check: %s\n", rec.Check)
	if rec.FailedStep != "" {
		fmt.Fprintf(w, "failed step: %s\n", rec.FailedStep)
	}
	if rec.Error != "" {
		fmt.Fprintf(w, "error: %s\n", rec.Error)
	}
	if rec.RestoredHealth != "" {
		fmt.Fprintf(w, "health after going back: %s\n", rec.RestoredHealth)
	}
	fmt.Fprintf(w, "package snapshot: %s\n", rec.PackageSnapshot)
	return nil
}
