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

// The steps, as printed and as recorded in failed_step. Step 0, the import,
// runs only with --from. A failure that comes before it (reading current and
// previous) is recorded as "state", one of the reload list as "reload".
var stepNames = [...]string{"import", "verify", "counter", "noaicheck", "validate", "packages", "switch", "health", "record"}

// Config is what a switch is run with. Zero fields take their defaults.
type Config struct {
	Root           string // default /
	Dry            bool   // print every step, run no program, change nothing
	Mode           string // ModeManual (default) or ModeSigned
	From           string // the incoming folder to import the release from; empty: it is imported already
	AllowedSigners string // the ssh allowed signers file, for ModeSigned only
	NoAICheck      string // the no-AI check program; default the release's bin/ah-noaicheck
	Runner         Runner
	Verifier       Verifier
	Owner          OwnerCheck // default DefaultOwnerCheck
	Out            io.Writer  // the steps
	Err            io.Writer  // refusals of the arguments and of the lock; default Out
	Now            func() time.Time
	Sleep          func(time.Duration)
}

// Switcher runs switches with one Config.
type Switcher struct {
	cfg Config
	// The pinned folders: var/lib/authority-host (nil in a dry run when it
	// does not exist), opt/authority-host and its releases/.
	stateRoot *os.Root
	baseRoot  *os.Root
	relRoot   *os.Root
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
	if cfg.Owner == nil {
		cfg.Owner = DefaultOwnerCheck
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
		return fmt.Errorf("--root %s is not an absolute path", q(s.cfg.Root))
	}
	if s.cfg.From != "" && !filepath.IsAbs(s.cfg.From) {
		return fmt.Errorf("--from %s is not an absolute path", q(s.cfg.From))
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
		return fmt.Errorf("--mode %s is neither manual nor signed", q(s.cfg.Mode))
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
			return ReleaseID{}, failf(CodeStateInvalid, "link target %s is not a folder of releases/", q(target))
		}
		t = rel
	}
	dir, name := filepath.Split(t)
	if dir != "releases/" {
		return ReleaseID{}, failf(CodeStateInvalid, "link target %s is not a folder of releases/", q(target))
	}
	id, err := ParseID(name)
	if err != nil {
		return ReleaseID{}, failf(CodeStateInvalid, "link target %s is not a release id", q(target))
	}
	return id, nil
}

// attempt is the state of one switch.
type attempt struct {
	rec        Record
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
		if s.cfg.From != "" {
			s.errorf("--rollback-to takes no --from")
			return ExitUsage
		}
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
	s.printf("ah-switch: %s (mode %s, root %s%s)", what, s.cfg.Mode, q(s.cfg.Root), dry)

	lk, code := s.lock(false)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()

	a := &attempt{
		rec: Record{
			ID:       arg,
			Mode:     s.cfg.Mode,
			Rollback: rollback,
			Check:    CheckNotRun,
		},
	}
	if code, done := s.openBase(a); done {
		return code
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
			return s.fail(a, 2, failf(CodeNoPrevious, "there is no previous release to go back to"))
		case arg != "previous" && arg != prevID:
			return s.fail(a, 2, failf(CodeRollbackRefused, "--rollback-to accepts only previous, which points at %s; %s is refused", prevID, arg))
		case prevID == a.rec.PreviousID:
			return s.fail(a, 2, failf(CodeNoPrevious, "previous and current both point at %s; there is nothing to go back to", prevID))
		}
		name = prevID
		a.rec.ID = name
		s.printf("rollback: previous points at %s", name)
	}
	id, _ := ParseID(name)
	a.rec.Counter = id.Counter
	relDir := filepath.Join(s.base(), "releases", id.Name)

	// 0. Import (with --from), 1. Verify.
	rel, code, done := s.openForSwitch(ctx, a, id, relDir)
	if done {
		return code
	}
	defer rel.Close()
	manifest, validators := rel.manifest, rel.validators
	a.reloads, a.health = rel.reloads, rel.health

	// 2. Counter.
	var highest uint64
	var err error
	if s.stateRoot != nil {
		if highest, err = readHighest(s.stateRoot); err != nil {
			return s.fail(a, 2, err)
		}
	}
	if rollback {
		s.step(2, "rollback to previous: the counter rule does not apply; the highest counter stays %d", highest)
	} else {
		if id.Counter <= highest {
			return s.fail(a, 2, failf(CodeCounterNotAbove, "counter %d is not above the highest ever activated, %d", id.Counter, highest))
		}
		s.step(2, "counter %d is above the highest ever activated, %d", id.Counter, highest)
	}

	// 3. The no-AI check of the new release.
	check := s.cfg.NoAICheck
	if check == "" {
		check = filepath.Join(rel.dir, NoAICheckProgram)
		ok, err := rel.exists(NoAICheckProgram)
		if err == nil && !ok {
			err = failf(CodeNoAICheckMissing, "%s missing in the release", NoAICheckProgram)
		}
		if err != nil {
			return s.fail(a, 3, err)
		}
	} else if info, err := os.Stat(check); err != nil || !info.Mode().IsRegular() {
		return s.fail(a, 3, failf(CodeNoAICheckMissing, "no-AI check %s is not a regular file", q(check)))
	}
	argv := []string{check, "--release", rel.dir}
	if s.cfg.Dry {
		s.step(3, "would run %s", qargv(argv))
	} else {
		_, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: NoAICheckTimeout})
		if err != nil {
			a.rec.Check = CheckFailed
			return s.fail(a, 3, failf(CodeNoAICheckFailed, "%s (run it by hand to see its hits)", summary(err)))
		}
		a.rec.Check = CheckPassed
		s.step(3, "passed")
	}

	// 4. Validators.
	if len(validators) == 0 {
		s.step(4, "no %s: nothing to validate", ValidatorsList)
	}
	for i, v := range validators {
		if s.cfg.Dry {
			s.step(4, "would run %s", qargv(v))
			continue
		}
		_, err := s.cfg.Runner.Run(ctx, Command{Argv: v, Timeout: ValidatorTimeout})
		if err != nil {
			return s.fail(a, 4, failf(CodeValidatorFailed, "validator %d of %d, %s: %s", i+1, len(validators), q(v[0]), summary(err)))
		}
		s.step(4, "ok: %s", qargv(v))
	}

	// 5. Packages.
	if manifest.PackageSnapshot == a.rec.PackageSnapshot {
		s.step(5, "package snapshot %s unchanged", q(manifest.PackageSnapshot))
	} else {
		ok, err := rel.exists(PackageStepProgram)
		if err != nil {
			return s.fail(a, 5, err)
		}
		switch {
		case !ok:
			s.step(5, "package snapshot %s -> %s; the release has no %s: nothing to do", q(a.rec.PackageSnapshot), q(manifest.PackageSnapshot), PackageStepProgram)
		case s.cfg.Dry:
			argv := []string{filepath.Join(rel.dir, PackageStepProgram), "--release", rel.dir, "--from", a.rec.PackageSnapshot, "--to", manifest.PackageSnapshot}
			s.step(5, "would run %s", qargv(argv))
		default:
			argv := []string{filepath.Join(rel.dir, PackageStepProgram), "--release", rel.dir, "--from", a.rec.PackageSnapshot, "--to", manifest.PackageSnapshot}
			_, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: PackageStepTimeout})
			if err != nil {
				return s.fail(a, 5, failf(CodePackageFailed, "%s", summary(err)))
			}
			a.rec.PackageSnapshot = manifest.PackageSnapshot
			s.step(5, "package step done: snapshot %s", q(manifest.PackageSnapshot))
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
			s.step(6, "would reload: %s", qargv(r))
		}
		s.step(7, "would run %s until it passes, every %s for up to %s", qargv(a.health), HealthInterval, HealthWindow)
		s.step(8, "would record the result in %s", q(filepath.Join(s.stateDir(), DeployFile)))
		s.printf("result: dry run passed; nothing changed")
		return ExitOK
	}
	if a.oldExists {
		if err := replaceLink(s.baseRoot, "previous", a.oldTarget); err != nil {
			return s.fail(a, 6, failf(CodeLinkFailed, "previous: %s", q(err.Error())))
		}
	}
	if s.afterPrevious != nil {
		if err := s.afterPrevious(); err != nil {
			return ExitFailed
		}
	}
	if err := replaceLink(s.baseRoot, "current", newTarget); err != nil {
		s.restorePrevious(a)
		return s.fail(a, 6, failf(CodeLinkFailed, "current: %s", q(err.Error())))
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
		if err := writeFileIn(s.stateRoot, HighestFile, []byte(fmt.Sprintf("%d\n", id.Counter)), 0o644); err != nil {
			s.setFailure(a, stepNames[8], failf(CodeRecordFailed, "%s not written: %s", HighestFile, q(err.Error())))
			s.step(8, "FAILED: %s: %s", a.rec.Error, a.rec.Detail)
			s.record(a)
			return ExitFailed
		}
		s.step(8, "highest counter %d", id.Counter)
	}
	if err := s.record(a); err != nil {
		return ExitFailed
	}
	s.step(8, "recorded in %s", q(filepath.Join(s.stateDir(), DeployFile)))
	s.printf("result: ok; current -> %s", id.Name)
	return ExitOK
}

// lock opens the state folder, checking it and every folder above it up to
// the root (see openTrusted), and takes the switch lock in it: exclusive, or
// shared for status. A dry run and status create nothing: without the state
// folder or its lock file nobody holds the lock. On failure it returns nil
// and the exit code; nothing is recorded, since the state folder is not
// trusted.
func (s *Switcher) lock(shared bool) (*lock, int) {
	create := !s.cfg.Dry && !shared
	state, err := s.openTrusted(StateDir, create)
	if !create && errors.Is(err, fs.ErrNotExist) {
		return &lock{}, ExitOK
	}
	if err != nil {
		s.errorf("state folder %s: %s", q(s.stateDir()), describe(err))
		return nil, ExitFailed
	}
	lk, err := takeLock(state, create, shared)
	if errors.Is(err, errLocked) {
		state.Close()
		s.errorf("%v (%s); nothing done", err, q(filepath.Join(s.stateDir(), LockFile)))
		return nil, ExitLocked
	}
	if err != nil {
		state.Close()
		s.errorf("lock: %s", describe(err))
		return nil, ExitFailed
	}
	s.stateRoot = state
	return lk, ExitOK
}

// openBase opens opt/authority-host and its releases/ folder, checked as the
// state folder is.
func (s *Switcher) openBase(a *attempt) (int, bool) {
	var err error
	if s.baseRoot, err = s.openTrusted(BaseDir, false); err == nil {
		s.relRoot, err = s.openTrusted(filepath.Join(BaseDir, "releases"), false)
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = failf(CodeStateInvalid, "%s or its releases folder is missing", BaseDir)
	}
	if err != nil {
		return s.failState(a, err), true
	}
	return 0, false
}

func (s *Switcher) closeRoots() {
	for _, r := range []*os.Root{s.stateRoot, s.baseRoot, s.relRoot} {
		if r != nil {
			r.Close()
		}
	}
	s.stateRoot, s.baseRoot, s.relRoot = nil, nil, nil
}

// readState reads current, previous and the last record.
func (s *Switcher) readState(a *attempt) (int, bool) {
	var err error
	a.oldTarget, a.oldExists, err = readLink(s.baseRoot, "current")
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
	a.prevTarget, a.prevExists, err = readLink(s.baseRoot, "previous")
	if err != nil {
		return s.failState(a, err), true
	}
	var last Record
	if s.stateRoot != nil {
		if last, _, err = readRecord(s.stateRoot); err != nil {
			s.printf("warning: %s; the package snapshot is taken as unknown", describe(err))
		}
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
	s.setFailure(a, "state", err)
	s.printf("state: FAILED: %s", describe(err))
	a.rec.Result = ResultFailed
	s.record(a)
	s.printf("result: failed before step 1; nothing changed")
	return ExitFailed
}

// openForSwitch opens and verifies the release to switch to. With --from it
// first imports the incoming folder into a new folder of releases/ and
// verifies only that copy; the copy becomes releases/<id>, read-only, once it
// is verified, and is removed when it is not. A dry run with --from imports
// nothing: it verifies the incoming folder in place, apart from the owner and
// the modes, which the import would set.
func (s *Switcher) openForSwitch(ctx context.Context, a *attempt, id ReleaseID, relDir string) (*release, int, bool) {
	if s.cfg.From == "" {
		rel, err := openRelease(id, s.relRoot, id.Name, relDir, s.entryTrusted)
		if err == nil {
			if err = s.checkRelease(ctx, rel); err != nil {
				rel.Close()
			}
		}
		if err != nil {
			if code, _ := codeOf(err); code == CodeReleaseMissing {
				err = failf(CodeReleaseMissing, "no release folder %s; import one with --from", id.Name)
			}
			return nil, s.fail(a, 1, err), true
		}
		return rel, 0, false
	}
	if _, err := s.relRoot.Lstat(id.Name); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = failf(CodeReleaseExists, "releases/%s exists; --from imports only a new release (switch to it without --from)", id.Name)
		}
		return nil, s.fail(a, 0, err), true
	}
	src, err := openIncoming(s.cfg.From)
	if err != nil {
		return nil, s.fail(a, 0, err), true
	}
	if s.cfg.Dry {
		// The release holds src open and closes it.
		s.step(0, "would copy %s into releases/%s as new read-only files", q(s.cfg.From), id.Name)
		rel, err := newRelease(id, src, relDir, s.cfg.From, nil)
		if err == nil {
			err = s.checkRelease(ctx, rel)
		}
		if err != nil {
			src.Close()
			return nil, s.fail(a, 1, err), true
		}
		return rel, 0, false
	}
	defer src.Close()
	tmp := importPrefix + id.Name
	tmpDir := filepath.Join(s.base(), "releases", tmp)
	s.dropImport(tmp)
	files, err := s.importTree(src, tmp)
	if err != nil {
		s.dropImport(tmp)
		return nil, s.fail(a, 0, err), true
	}
	s.step(0, "%d files of %s copied into releases/%s", files, q(s.cfg.From), tmp)
	rel, err := openRelease(id, s.relRoot, tmp, tmpDir, s.entryTrusted)
	if err != nil {
		s.dropImport(tmp)
		return nil, s.fail(a, 1, err), true
	}
	rel.path, rel.dir = tmpDir, relDir
	if err := s.checkRelease(ctx, rel); err != nil {
		rel.Close()
		s.dropImport(tmp)
		return nil, s.fail(a, 1, err), true
	}
	err = s.sealTree(tmp)
	if err == nil {
		if _, err = s.relRoot.Lstat(id.Name); err == nil {
			err = failf(CodeReleaseExists, "releases/%s appeared during the import", id.Name)
		} else if errors.Is(err, fs.ErrNotExist) {
			err = s.relRoot.Rename(tmp, id.Name)
		}
	}
	if err == nil {
		err = syncRoot(s.relRoot)
	}
	if err != nil {
		rel.Close()
		s.dropImport(tmp)
		return nil, s.fail(a, 0, err), true
	}
	rel.path = relDir
	s.step(0, "releases/%s imported and made read-only", id.Name)
	return rel, 0, false
}

// checkRelease checks what step 1 checks after the sums: the signature, the
// manifest and the command lists, keeping the parsed values in rel.
func (s *Switcher) checkRelease(ctx context.Context, rel *release) error {
	s.step(1, "releases/%s: %d files match %s", rel.id.Name, rel.files, SumsFile)
	if err := s.verifySignature(ctx, rel); err != nil {
		return err
	}
	data, err := rel.readSmall(ManifestFile)
	if err != nil {
		return err
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return err
	}
	if err := manifest.Check(rel.id); err != nil {
		return err
	}
	if rel.validators, rel.reloads, rel.health, err = rel.readLists(); err != nil {
		return err
	}
	rel.manifest = manifest
	s.step(1, "MANIFEST: commit %s, built on %s, %s, package snapshot %s", q(manifest.Commit), q(manifest.BuiltOn), q(manifest.GoVersion), q(manifest.PackageSnapshot))
	return nil
}

func (s *Switcher) verifySignature(ctx context.Context, rel *release) error {
	present, err := rel.exists(SignatureFile)
	if err != nil {
		return err
	}
	if s.cfg.Mode == ModeManual {
		note := ""
		if present {
			note = " (" + SignatureFile + " is there and is not read)"
		}
		s.step(1, "signature not checked: mode manual%s", note)
		return nil
	}
	if !present {
		return failf(CodeSignatureMissing, "%s missing; mode signed needs it", SignatureFile)
	}
	if s.cfg.Dry {
		s.step(1, "would verify %s against %s", SignatureFile, q(s.cfg.AllowedSigners))
		return nil
	}
	if err := s.cfg.Verifier.Verify(ctx, s.cfg.AllowedSigners, rel.sums, filepath.Join(rel.path, SignatureFile)); err != nil {
		_, detail := codeOf(err)
		return failf(CodeSignatureBad, "%s: %s", SignatureFile, detail)
	}
	s.step(1, "%s verified against %s", SignatureFile, q(s.cfg.AllowedSigners))
	return nil
}

// fail ends a switch that failed at step n before current was moved: current
// is untouched. Outside a dry run the outcome is recorded.
func (s *Switcher) fail(a *attempt, n int, err error) int {
	s.setFailure(a, stepNames[n], err)
	s.step(n, "FAILED: %s", describe(err))
	a.rec.Result = ResultFailed
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
		_, err := s.cfg.Runner.Run(ctx, Command{Argv: r, Timeout: ReloadTimeout})
		if err != nil {
			s.step(6, "reload FAILED: %s: %s", qargv(r), summary(err))
			return failf(CodeReloadFailed, "%s: %s", q(r[0]), summary(err))
		}
		s.step(6, "reloaded: %s", qargv(r))
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
		_, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: limit})
		if err == nil {
			s.step(7, "healthy after %d attempt(s): %s", attempts, qargv(argv))
			return nil
		}
		last = err
		if !s.cfg.Now().Add(HealthInterval).Before(deadline) {
			break
		}
		s.cfg.Sleep(HealthInterval)
	}
	return failf(CodeHealthFailed, "not healthy after %d attempts in %s: %s", attempts, HealthWindow, summary(last))
}

// goBack is called when the reload or the health check failed after current
// was moved: it points current back at the old release, puts previous back,
// runs the reload list again, checks health again and records the outcome.
func (s *Switcher) goBack(ctx context.Context, a *attempt, step string, cause error) int {
	n := 7
	if step == "reload" {
		n = 6
	}
	s.setFailure(a, step, cause)
	s.step(n, "FAILED: %s", describe(cause))
	if !a.oldExists {
		a.rec.Result = ResultFailed
		a.rec.Detail += "; there is no earlier release to go back to, so current stays at " + a.rec.CurrentID
		s.record(a)
		s.printf("result: failed at %s; no earlier release, current stays at %s", step, a.rec.CurrentID)
		return ExitFailed
	}
	if err := replaceLink(s.baseRoot, "current", a.oldTarget); err != nil {
		a.rec.Result = ResultFailed
		a.rec.Detail += "; going back failed: " + q(err.Error())
		s.record(a)
		s.printf("result: failed at %s and could not go back (%s); current stays at %s", step, q(err.Error()), a.rec.CurrentID)
		return ExitFailed
	}
	failedID := a.rec.CurrentID
	a.rec.CurrentID = a.rec.PreviousID
	s.restorePrevious(a)
	s.printf("back: current -> %s", q(a.oldTarget))
	if err := s.reload(ctx, a); err != nil {
		a.rec.Detail += "; reload after going back failed: " + describe(err)
	}
	if err := s.checkHealth(ctx, a.health); err != nil {
		a.rec.RestoredHealth = "failed"
		a.rec.Detail += "; the release gone back to is not healthy either: " + describe(err)
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
		err = replaceLink(s.baseRoot, "previous", a.prevTarget)
	} else {
		err = removeFileIn(s.baseRoot, "previous")
	}
	if err != nil {
		s.printf("warning: previous not put back: %s", q(err.Error()))
	}
}

// record writes deploy.json. A dry run records nothing.
func (s *Switcher) record(a *attempt) error {
	if s.cfg.Dry || s.stateRoot == nil {
		return nil
	}
	a.rec.Time = s.cfg.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(a.rec, "", "  ")
	if err == nil {
		err = writeFileIn(s.stateRoot, DeployFile, append(data, '\n'), 0o644)
	}
	if err != nil {
		s.printf("ah-switch: %s not written: %s", DeployFile, q(err.Error()))
		return err
	}
	return nil
}

// setFailure records err in the record as the failure of step.
func (s *Switcher) setFailure(a *attempt, step string, err error) {
	a.rec.FailedStep = step
	a.rec.Error, a.rec.Detail = codeOf(err)
}

// describe is err as a line may show it: its code and its detail.
func describe(err error) string {
	code, detail := codeOf(err)
	if detail == "" {
		return code
	}
	return code + ": " + detail
}

// Status prints current, previous, the highest counter and the last record
// to Out. It checks the state folder and the release folder as a switch does
// and changes nothing.
func (s *Switcher) Status() int {
	if !filepath.IsAbs(s.cfg.Root) {
		s.errorf("--root %s is not an absolute path", q(s.cfg.Root))
		return ExitUsage
	}
	lk, code := s.lock(true)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	if err := s.status(s.cfg.Out); err != nil {
		s.errorf("status: %s", describe(err))
		return ExitFailed
	}
	return ExitOK
}

func (s *Switcher) status(w io.Writer) error {
	base, err := s.openTrusted(BaseDir, false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.baseRoot = base
	for _, name := range []string{"current", "previous"} {
		var target string
		var ok bool
		if base != nil {
			if target, ok, err = readLink(base, name); err != nil {
				return err
			}
		}
		if !ok {
			fmt.Fprintf(w, "%s: none\n", name)
			continue
		}
		id, err := s.targetID(target)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(w, "%s: %s\n", name, id.Name)
	}
	var highest uint64
	var rec Record
	var ok bool
	if s.stateRoot != nil {
		if highest, err = readHighest(s.stateRoot); err != nil {
			return err
		}
		if rec, ok, err = readRecord(s.stateRoot); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "highest counter: %d\n", highest)
	if !ok {
		fmt.Fprintln(w, "last switch: none recorded")
		return nil
	}
	kind := "switch to"
	if rec.Rollback {
		kind = "rollback to"
	}
	fmt.Fprintf(w, "last switch: %s %s: %s at %s (mode %s)\n", kind, q(rec.ID), q(rec.Result), q(rec.Time), q(rec.Mode))
	fmt.Fprintf(w, "no-AI check: %s\n", q(rec.Check))
	if rec.FailedStep != "" {
		fmt.Fprintf(w, "failed step: %s\n", q(rec.FailedStep))
	}
	if rec.Error != "" {
		fmt.Fprintf(w, "error: %s\n", q(rec.Error))
	}
	if rec.Detail != "" {
		fmt.Fprintf(w, "detail: %s\n", q(rec.Detail))
	}
	if rec.RestoredHealth != "" {
		fmt.Fprintf(w, "health after going back: %s\n", q(rec.RestoredHealth))
	}
	fmt.Fprintf(w, "package snapshot: %s\n", q(rec.PackageSnapshot))
	return nil
}
