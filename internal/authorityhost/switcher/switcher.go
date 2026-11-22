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

// maxNFTListing bounds the firewall listing given to the no-AI check.
const maxNFTListing = 4 << 20

// Time limits.
const (
	NoAICheckTimeout     = 5 * time.Minute
	NFTListTimeout       = 30 * time.Second
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
var stepNames = [...]string{"import", "verify", "counter", "packages", "noaicheck", "validate", "switch", "health", "record"}

// Config is what a switch is run with. Zero fields take their defaults.
type Config struct {
	Root      string // default /
	Dry       bool   // print every step, run no program, change nothing
	AllowExec bool   // with a Root other than /: run programs all the same (an isolated test runner)
	Mode      string // ModeManual or ModeSigned, at least as strict as the policy; empty: the policy's
	From      string // the incoming folder to import the release from; empty: it is imported already
	NoAICheck string // the no-AI check program; default the release's bin/ah-noaicheck
	Runner    Runner
	Verifier  Verifier
	Owner     OwnerCheck // default DefaultOwnerCheck
	Limits    Limits     // zero fields take DefaultLimits
	Out       io.Writer  // the steps
	Err       io.Writer  // refusals of the arguments and of the lock; default Out
	Now       func() time.Time
	Sleep     func(time.Duration)
}

// Switcher runs switches with one Config.
type Switcher struct {
	cfg Config
	// The pinned folders: var/lib/authority-host (nil in a dry run when it
	// does not exist), opt/authority-host and its releases/.
	stateRoot *os.Root
	baseRoot  *os.Root
	relRoot   *os.Root
	// budget bounds the import and the hashing of one switch.
	budget *budget
	// The switch policy, the mode this switch uses and the signatures a
	// release must carry for it (more, while a journal is resumed: the
	// trust its transaction began with).
	policy Policy
	mode   string
	trust  trust
	// lockFile is the open lock file of this command, given to the
	// package step so that it holds the lock too.
	lockFile *os.File
	// procs looks at processes (procfs on the host).
	procs procs
	// The host's stop list, read once (see hostStopList).
	hostStops     [][]string
	hostStopsErr  error
	hostStopsRead bool
	// crashAt, when set, is called at each point of crash; an error stops
	// the switch there as a crash would.
	crashAt func(point string) error
}

// New returns a Switcher, filling in the defaults of cfg.
func New(cfg Config) *Switcher {
	if cfg.Root == "" {
		cfg.Root = "/"
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecRunner{}
	}
	cfg.Limits = cfg.Limits.withDefaults()
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
	return &Switcher{cfg: cfg, procs: procfs{}}
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
	if filepath.Clean(s.cfg.Root) != "/" && !s.cfg.Dry && !s.cfg.AllowExec {
		return fmt.Errorf("--root %s is not /: a switch there would run the release's programs on this host; use --dry, status, or --allow-exec in an isolated runner", q(s.cfg.Root))
	}
	if s.cfg.From != "" && !filepath.IsAbs(s.cfg.From) {
		return fmt.Errorf("--from %s is not an absolute path", q(s.cfg.From))
	}
	switch s.cfg.Mode {
	case "", ModeManual, ModeSigned:
	default:
		return fmt.Errorf("--mode %s is neither manual nor signed", q(s.cfg.Mode))
	}
	return nil
}

// targetID reads the release id out of the target of current or previous,
// which must be exactly releases/<id>, as the switch writes it: an absolute
// path, ./ or .. elements, a doubled slash or anything else is refused, never
// cleaned into a release.
func (s *Switcher) targetID(target string) (ReleaseID, error) {
	name, ok := strings.CutPrefix(target, "releases/")
	if !ok {
		return ReleaseID{}, failf(CodeStateInvalid, "link target %s is not releases/<id>", q(target))
	}
	id, err := ParseID(name)
	if err != nil {
		return ReleaseID{}, failf(CodeStateInvalid, "link target %s is not releases/<id>", q(target))
	}
	return id, nil
}

// attempt is the state of one switch before its transaction begins.
type attempt struct {
	rec        Record
	oldTarget  string // what current pointed at, as written in the link
	oldExists  bool
	prevTarget string // what previous pointed at
	prevExists bool
	prevThere  bool // the release previous points at is a folder
	// The release current pointed at, verified, to go back to; nil when
	// there is none or it cannot be used (fallbackErr says why).
	fallback    *release
	fallbackErr error
	// t is the transaction, once its journal is written.
	t *txn
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

	if code, done := s.applyPolicy(); done {
		return code
	}
	if _, err := s.hostStopList(); err != nil {
		s.errorf("%s; nothing done", describe(err))
		return ExitFailed
	}
	what := "switch to " + arg
	if rollback {
		what = "roll back to " + arg
	}
	dry := ""
	if s.cfg.Dry {
		dry = ", dry run: nothing is run or changed"
	}
	s.printf("ah-switch: %s (mode %s, root %s%s)", what, s.mode, q(s.cfg.Root), dry)

	lk, code := s.lock(false)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()

	a := &attempt{
		rec: Record{
			ID:        arg,
			Mode:      s.mode,
			Trust:     s.policy.Trust(),
			Rollback:  rollback,
			Check:     CheckNotRun,
			Requested: arg,
		},
	}
	if code, done := s.openBase(a); done {
		return code
	}
	if code, halt := s.reconcile(ctx, true); halt {
		return code
	}
	last, code, done := s.readState(a)
	if done {
		return code
	}
	if code, done := s.reportSettled(last, arg, rollback); done {
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
		case !a.prevThere:
			return s.fail(a, 2, failf(CodeNoPrevious, "previous points at %s, which is missing or not a folder", prevID))
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
	s.budget = &budget{lim: s.cfg.Limits, deadline: s.cfg.Now().Add(s.cfg.Limits.Time), now: s.cfg.Now}
	rel, code, done := s.openForSwitch(ctx, a, id, relDir)
	if done {
		return code
	}
	defer rel.Close()
	manifest, validators := rel.manifest, rel.validators
	s.openFallback(ctx, a)
	defer func() {
		if a.fallback != nil {
			a.fallback.Close()
		}
	}()

	// 2. Counter.
	var highest uint64
	var present bool
	var err error
	if s.stateRoot != nil {
		if highest, present, err = s.readHighest(); err != nil {
			return s.fail(a, 2, err)
		}
	}
	if !present && a.oldExists {
		return s.fail(a, 2, failf(CodeCounterMissing, "%s is missing although current points at %s; run ah-switch recover --counter N, N the highest counter this host ever switched to (at least %d)", HighestFile, a.rec.PreviousID, s.usedCounter(a)))
	}
	if rollback {
		s.step(2, "rollback to previous: the counter rule does not apply; the highest counter stays %d", highest)
	} else {
		if id.Counter <= highest {
			return s.fail(a, 2, failf(CodeCounterNotAbove, "counter %d is not above the highest ever activated, %d", id.Counter, highest))
		}
		s.step(2, "counter %d is above the highest ever activated, %d", id.Counter, highest)
	}

	// The transaction, written first when the package step runs and in any
	// case before any link moves.
	t := &txn{cand: rel, candSet: true, fb: a.fallback, fbErr: a.fallbackErr, fbSet: true}
	if a.fallback == nil && a.fallbackErr == nil {
		t.fbErr = errNoFallback
	}
	t.j = journal{
		Direction:    DirForward,
		Requested:    arg,
		ID:           id.Name,
		Counter:      id.Counter,
		Phase:        PhasePackages,
		PackageState: PackageNone,
		Check:        CheckNotRun,
	}
	if rollback {
		t.j.Direction = DirRollback
	}
	if a.oldExists {
		t.j.OldCurrent = a.oldTarget
	}
	if a.prevExists {
		t.j.OldPrevious = a.prevTarget
	}
	t.j.TrustMode = s.trust.mode()
	if len(s.trust.signed) > 0 {
		t.j.TrustSigners, t.j.TrustPrincipal = s.trust.signed[0].signers, s.trust.signed[0].principal
	}
	if !s.cfg.Dry {
		if t.j.Txn, err = newTxnID(); err != nil {
			return s.fail(a, 2, err)
		}
	}

	// 3. Packages, before the check, so that the check sees the host as it
	// will be. On a host whose package state is unknown only a release of
	// the snapshot the unfinished step went to may switch, by running that
	// step again; anything else waits for recover --package-state.
	from, unknown := a.rec.PackageSnapshot, a.rec.PackageState == PackageUnknown
	if unknown {
		if manifest.PackageSnapshot != a.rec.PackageTo {
			return s.fail(a, 3, packageUnknownError(a.rec.PackageFrom, a.rec.PackageTo))
		}
		from = a.rec.PackageFrom
		s.step(3, "the host's package snapshot is unknown: the step from %s to %s runs again", q(a.rec.PackageFrom), q(a.rec.PackageTo))
	}
	if manifest.PackageSnapshot == from && !unknown {
		s.step(3, "package snapshot %s unchanged", q(manifest.PackageSnapshot))
	} else {
		ok, err := rel.exists(PackageStepProgram)
		if err == nil && !ok {
			err = failf(CodePackageStepMissing, "package snapshot %s -> %s, and the release has no %s", q(from), q(manifest.PackageSnapshot), PackageStepProgram)
		}
		if err != nil {
			return s.fail(a, 3, err)
		}
		argv := []string{filepath.Join(rel.dir, PackageStepProgram), "--release", rel.dir, "--from", from, "--to", manifest.PackageSnapshot}
		if s.cfg.Dry {
			s.step(3, "would run %s", qargv(argv))
		} else {
			t.j.PackageFrom, t.j.PackageTo = from, manifest.PackageSnapshot
			a.t = t
			if err := s.runPackageStep(ctx, t, argv); err != nil {
				if errors.Is(err, errCrashed) {
					return ExitFailed
				}
				return s.fail(a, 3, err)
			}
			a.rec.PackageSnapshot = manifest.PackageSnapshot
			s.step(3, "package step done: snapshot %s", q(manifest.PackageSnapshot))
		}
	}

	// 4. The no-AI check of the new release, on the host as it now is.
	if err := s.noAICheck(ctx, a, rel); err != nil {
		return s.fail(a, 4, err)
	}
	t.j.Check = a.rec.Check

	// 5. Validators.
	for i, v := range validators {
		if s.cfg.Dry {
			s.step(5, "would run %s", qargv(v))
			continue
		}
		_, err := s.cfg.Runner.Run(ctx, Command{Argv: v, Timeout: ValidatorTimeout})
		if err != nil {
			return s.fail(a, 5, failf(CodeValidatorFailed, "validator %d of %d, %s: %s", i+1, len(validators), q(v[0]), summary(err)))
		}
		s.step(5, "ok: %s", qargv(v))
	}

	// 6. Switch, then reload.
	newTarget := filepath.Join("releases", id.Name)
	if s.cfg.Dry {
		if a.oldExists {
			s.step(6, "would point previous at %s", q(a.oldTarget))
		}
		s.step(6, "would point current at %s (a new link renamed over it)", q(newTarget))
		for _, r := range rel.reloads {
			s.step(6, "would reload: %s", qargv(r))
		}
		for _, h := range rel.health {
			s.step(7, "would run %s", qargv(h))
		}
		s.step(7, "until every health command passes, a round every %s, within %s in all", HealthInterval, HealthWindow)
		s.step(8, "would record the result in %s", q(filepath.Join(s.stateDir(), DeployFile)))
		s.printf("result: dry run passed; nothing changed")
		return ExitOK
	}
	a.t = t
	return s.activateNew(ctx, t, newTarget)
}

// activateNew moves the links of a verified, checked release: the journal
// says links (and, on a forward switch, that the counter is used) before
// the counter is written and before either link moves; previous is pointed
// at the old current, then current at the release, each renamed over the
// old link and synced. Then the journal says activating, and the
// transaction is driven from there. A link that cannot be written, or whose
// folder cannot be synced, puts both links back, as the resume of the links
// phase does.
func (s *Switcher) activateNew(ctx context.Context, t *txn, newTarget string) int {
	t.j.Phase = PhaseLinks
	t.j.CounterUsed = t.forward()
	if err := s.save(t); err != nil {
		s.step(6, "FAILED: %s", describe(err))
		s.printf("result: failed at switch; nothing moved; %s may stay and the next command settles it", JournalFile)
		return ExitFailed
	}
	if s.crash(CrashLinks) != nil {
		return ExitFailed
	}
	if t.forward() {
		if err := s.highestAtLeast(t.j.Counter); err != nil {
			code, _ := s.drive(ctx, t)
			return code
		}
		s.step(6, "highest counter %d (attempted)", t.j.Counter)
	}
	if s.crash(CrashCounter) != nil {
		return ExitFailed
	}
	linkFailed := func(name string, err error) int {
		s.step(6, "FAILED: %s: %s", name, q(err.Error()))
		if err := s.putBack(t, "switch", failf(CodeLinkFailed, "%s: %s", name, q(err.Error()))); err != nil {
			if !errors.Is(err, errCrashed) {
				s.printf("result: unresolved: %s; %s stays in phase %s, and the next switch, rollback or recover resumes it", describe(err), JournalFile, t.j.Phase)
			}
			return ExitFailed
		}
		code, _ := s.drive(ctx, t)
		return code
	}
	if t.j.OldCurrent != "" {
		if err := replaceLink(s.baseRoot, "previous", t.j.OldCurrent); err != nil {
			return linkFailed("previous", err)
		}
		s.step(6, "previous -> %s", q(t.j.OldCurrent))
	}
	if s.crash(CrashPrevious) != nil {
		return ExitFailed
	}
	if err := replaceLink(s.baseRoot, "current", newTarget); err != nil {
		return linkFailed("current", err)
	}
	s.step(6, "current -> %s", q(newTarget))
	if s.crash(CrashCurrent) != nil {
		return ExitFailed
	}
	t.j.Phase = PhaseActivating
	if err := s.save(t); err != nil {
		return linkFailed(JournalFile, err)
	}
	if s.crash(CrashActivating) != nil {
		return ExitFailed
	}
	code, _ := s.drive(ctx, t)
	return code
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
	lk, err := takeLock(state, create, shared, func(info fs.FileInfo) error {
		if err := s.trusted(filepath.Join(s.stateDir(), LockFile), info, false); err != nil {
			return err
		}
		if links(info) != 1 || info.Size() > maxLockFile {
			return failf(CodeUntrusted, "%s has more than one link or is not empty", LockFile)
		}
		return nil
	})
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
	s.lockFile = lk.f
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

// hasLink reports whether name of r is a symbolic link.
func hasLink(r *os.Root, name string) bool {
	_, ok, err := readLink(r, name)
	return ok && err == nil
}

func (s *Switcher) closeRoots() {
	for _, r := range []*os.Root{s.stateRoot, s.baseRoot, s.relRoot} {
		if r != nil {
			r.Close()
		}
	}
	s.stateRoot, s.baseRoot, s.relRoot = nil, nil, nil
}

// readState reads current, previous and the last record, which it returns.
func (s *Switcher) readState(a *attempt) (Record, int, bool) {
	var err error
	a.oldTarget, a.oldExists, err = readLink(s.baseRoot, "current")
	if err != nil {
		return Record{}, s.failState(a, err), true
	}
	if a.oldExists {
		id, err := s.targetID(a.oldTarget)
		if err != nil {
			return Record{}, s.failState(a, fmt.Errorf("current: %w", err)), true
		}
		a.rec.PreviousID = id.Name
		a.rec.CurrentID = id.Name
		if !s.releaseThere(id) {
			s.printf("warning: current points at %s, which is missing or not a folder", id.Name)
		}
	}
	a.prevTarget, a.prevExists, err = readLink(s.baseRoot, "previous")
	if err != nil {
		return Record{}, s.failState(a, err), true
	}
	if a.prevExists {
		id, err := s.targetID(a.prevTarget)
		if err != nil {
			return Record{}, s.failState(a, fmt.Errorf("previous: %w", err)), true
		}
		a.prevThere = s.releaseThere(id)
	}
	var last Record
	if s.stateRoot != nil {
		if last, _, err = s.readRecord(); err != nil {
			// Not recorded: the record is what cannot be trusted.
			s.printf("state: FAILED: %s", describe(err))
			s.printf("result: failed before step 1; nothing changed or recorded")
			return Record{}, ExitFailed, true
		}
	}
	a.rec.PackageSnapshot = last.PackageSnapshot
	a.rec.PackageState, a.rec.PackageFrom, a.rec.PackageTo, a.rec.PackageStatedAt = last.PackageState, last.PackageFrom, last.PackageTo, last.PackageStatedAt
	current := "none"
	if a.oldExists {
		current = a.rec.CurrentID
	}
	s.printf("state: current %s", current)
	return last, 0, false
}

// releaseThere reports whether releases/<id> is a folder (not a link).
func (s *Switcher) releaseThere(id ReleaseID) bool {
	info, err := s.relRoot.Lstat(id.Name)
	return err == nil && info.IsDir()
}

// usedCounter is the highest counter of the releases current and previous
// point at: the least highest-counter can be.
func (s *Switcher) usedCounter(a *attempt) uint64 {
	n := uint64(0)
	for _, t := range []string{a.oldTarget, a.prevTarget} {
		if id, err := s.targetID(t); t != "" && err == nil {
			n = max(n, id.Counter)
		}
	}
	return n
}

func (s *Switcher) failState(a *attempt, err error) int {
	s.setFailure(a, "state", err)
	s.printf("state: FAILED: %s", describe(err))
	a.rec.Result = ResultFailed
	s.record(a)
	s.printf("result: failed before step 1; nothing changed")
	return ExitFailed
}

// noAICheck runs the no-AI check of the release (or the --noaicheck program)
// on the root, with the firewall listing it compares against the release's
// pinned digest:
//
//	CHECK --root ROOT --release DIR --require-policy --nft-listing FILE
//
// FILE is what "nft list ruleset" printed, written to a private file in a
// private temporary folder that is removed afterwards. Any exit status but 0
// fails, including the check's "HIT incomplete" when it could not finish.
func (s *Switcher) noAICheck(ctx context.Context, a *attempt, rel *release) error {
	check := s.cfg.NoAICheck
	if check == "" {
		check = filepath.Join(rel.dir, NoAICheckProgram)
		ok, err := rel.exists(NoAICheckProgram)
		if err == nil && !ok {
			err = failf(CodeNoAICheckMissing, "%s missing in the release", NoAICheckProgram)
		}
		if err != nil {
			return err
		}
	} else if info, err := os.Stat(check); err != nil || !info.Mode().IsRegular() {
		return failf(CodeNoAICheckMissing, "no-AI check %s is not a regular file", q(check))
	}
	listArgv := []string{"nft", "list", "ruleset"}
	if s.cfg.Dry {
		s.step(4, "would run %s into a private file", qargv(listArgv))
		s.step(4, "would run %s", qargv([]string{check, "--root", s.cfg.Root, "--release", rel.dir, "--require-policy", "--nft-listing", "FILE"}))
		return nil
	}
	out, err := s.cfg.Runner.Run(ctx, Command{Argv: listArgv, Timeout: NFTListTimeout, MaxOutput: maxNFTListing})
	if err == nil && out.Truncated {
		err = errors.New("larger than the limit")
	}
	if err != nil {
		return failf(CodeNFTListingFailed, "%s: %s", qargv(listArgv), summary(err))
	}
	tmp, err := os.MkdirTemp("", "ah-switch-nft-")
	if err != nil {
		return failf(CodeNFTListingFailed, "temporary folder: %s", q(err.Error()))
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return failf(CodeNFTListingFailed, "temporary folder: %s", q(err.Error()))
	}
	listing := filepath.Join(tmp, "nft-ruleset")
	if err := os.WriteFile(listing, out.Stdout, 0o600); err != nil {
		return failf(CodeNFTListingFailed, "listing: %s", q(err.Error()))
	}
	argv := []string{check, "--root", s.cfg.Root, "--release", rel.dir, "--require-policy", "--nft-listing", listing}
	if _, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: NoAICheckTimeout}); err != nil {
		a.rec.Check = CheckFailed
		return failf(CodeNoAICheckFailed, "%s (run it by hand to see its hits)", summary(err))
	}
	a.rec.Check = CheckPassed
	s.step(4, "passed")
	return nil
}

// openForSwitch opens and verifies the release to switch to. With --from it
// first imports the incoming folder into a new folder of releases/ and
// verifies only that copy; the copy becomes releases/<id>, read-only, once it
// is verified, and is removed when it is not. A dry run with --from imports
// nothing: it verifies the incoming folder in place, apart from the owner and
// the modes, which the import would set.
func (s *Switcher) openForSwitch(ctx context.Context, a *attempt, id ReleaseID, relDir string) (*release, int, bool) {
	if s.cfg.From == "" {
		rel, err := openRelease(id, s.relRoot, id.Name, relDir, s.entryTrusted, s.budget)
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
		rel, err := newRelease(id, src, relDir, s.cfg.From, nil, s.budget)
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
	rel, err := openRelease(id, s.relRoot, tmp, tmpDir, s.entryTrusted, s.budget)
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
	if rel.lists, err = rel.readLists(manifest.Components == ComponentsStatic); err != nil {
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
	if len(s.trust.signed) == 0 {
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
	for _, sg := range s.trust.signed {
		if s.cfg.Dry {
			s.step(1, "would verify %s as signed by %s against %s", SignatureFile, q(sg.principal), q(sg.signers))
			continue
		}
		if err := s.cfg.Verifier.Verify(ctx, s.signersPath(sg.signers), sg.principal, rel.sums, filepath.Join(rel.path, SignatureFile)); err != nil {
			_, detail := codeOf(err)
			return failf(CodeSignatureBad, "%s: %s", SignatureFile, detail)
		}
		s.step(1, "%s verified as signed by %s against %s", SignatureFile, q(sg.principal), q(sg.signers))
	}
	return nil
}

// fail ends a switch that failed at step n before its links moved: current
// is untouched. Outside a dry run the outcome is recorded: through the
// transaction when its journal was written already (the package step ran),
// directly otherwise.
func (s *Switcher) fail(a *attempt, n int, err error) int {
	s.setFailure(a, stepNames[n], err)
	s.step(n, "FAILED: %s", describe(err))
	a.rec.Result = ResultFailed
	if s.cfg.Dry {
		s.printf("result: failed at %s (dry run: nothing recorded); current unchanged", stepNames[n])
		return ExitFailed
	}
	if t := a.t; t != nil {
		t.fail(stepNames[n], err)
		if t.j.Check == CheckNotRun {
			t.j.Check = a.rec.Check
		}
		if err := s.done(t, ResultFailed); err != nil {
			if !errors.Is(err, errCrashed) {
				s.printf("result: unresolved: %s; %s stays, and the next switch, rollback or recover settles it", describe(err), JournalFile)
			}
			return ExitFailed
		}
		code, _ := s.drive(context.Background(), t)
		return code
	}
	if err := s.record(a); err != nil {
		s.printf("result: failed at %s; current unchanged; the outcome is not recorded", stepNames[n])
		return ExitFailed
	}
	cur := a.rec.CurrentID
	if cur == "" {
		cur = "none"
	}
	s.printf("result: failed at %s; current unchanged (%s)", stepNames[n], cur)
	return ExitFailed
}

func (s *Switcher) reload(ctx context.Context, reloads [][]string) error {
	for _, r := range reloads {
		_, err := s.cfg.Runner.Run(ctx, Command{Argv: r, Timeout: ReloadTimeout})
		if err != nil {
			s.step(6, "reload FAILED: %s: %s", qargv(r), summary(err))
			return failf(CodeReloadFailed, "%s: %s", q(r[0]), summary(err))
		}
		s.step(6, "reloaded: %s", qargv(r))
	}
	return nil
}

// checkHealth runs the health commands until every one of them passes in
// one round, every HealthInterval. HealthWindow is one absolute deadline for
// all of it: each command gets at most HealthAttemptTimeout and never more
// than what is left of the window, a round that ends after the deadline does
// not count, and no command starts once the window is over. No command (a
// static release) is healthy at once.
func (s *Switcher) checkHealth(ctx context.Context, health [][]string) error {
	if len(health) == 0 {
		s.step(7, "no health command: a static release")
		return nil
	}
	deadline := s.cfg.Now().Add(HealthWindow)
	var last error
	var lastArgv []string
	attempts := 0
	for {
		attempts++
		last = nil
		for _, argv := range health {
			limit := min(HealthAttemptTimeout, deadline.Sub(s.cfg.Now()))
			if limit <= 0 {
				last, lastArgv = fixedError("the health window ended"), argv
				break
			}
			if _, err := s.cfg.Runner.Run(ctx, Command{Argv: argv, Timeout: limit}); err != nil {
				last, lastArgv = err, argv
				break
			}
		}
		if last == nil && s.cfg.Now().After(deadline) {
			last, lastArgv = fixedError("the round ended after the health window"), health[len(health)-1]
		}
		if last == nil {
			s.step(7, "healthy after %d attempt(s): %d health command(s) passed", attempts, len(health))
			return nil
		}
		if !s.cfg.Now().Add(HealthInterval).Before(deadline) {
			break
		}
		s.cfg.Sleep(HealthInterval)
	}
	return failf(CodeHealthFailed, "not healthy after %d attempts in %s: %s: %s", attempts, HealthWindow, q(lastArgv[0]), summary(last))
}

// record writes the record of a switch that failed before its transaction
// began. A dry run records nothing.
func (s *Switcher) record(a *attempt) error {
	if s.cfg.Dry || s.stateRoot == nil {
		return nil
	}
	if err := s.writeRecord(&a.rec); err != nil {
		s.printf("ah-switch: %s not written: %s", DeployFile, q(err.Error()))
		return err
	}
	return nil
}

// writeRecord writes deploy.json with the time of now.
func (s *Switcher) writeRecord(rec *Record) error {
	rec.Time = s.now()
	rec.Detail = boundDetail(rec.Detail)
	return writeJSONIn(s.stateRoot, DeployFile, rec, 0o644)
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

// Status prints current, previous, the highest counter, the trust, a pending
// journal and the last record to Out. It takes the lock shared, so that what
// it prints is one state between two switches (a running switch makes it
// exit with ExitLocked), checks the folders as a switch does, prints nothing
// when any of it cannot be read, and changes nothing.
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
	p, err := s.readPolicy()
	if err != nil {
		s.errorf("switch policy: %s", describe(err))
		return ExitFailed
	}
	s.policy = p
	var b strings.Builder
	if err := s.status(&b); err != nil {
		s.errorf("status: %s", describe(err))
		return ExitFailed
	}
	io.WriteString(s.cfg.Out, b.String())
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
	var present, ok, pending bool
	var rec Record
	var j journal
	if s.stateRoot != nil {
		if highest, present, err = s.readHighest(); err != nil {
			return err
		}
		if rec, ok, err = s.readRecord(); err != nil {
			return err
		}
		if j, pending, err = s.readJournal(); err != nil {
			return err
		}
	}
	switch {
	case present:
		fmt.Fprintf(w, "highest counter: %d\n", highest)
	case base != nil && hasLink(base, "current"):
		fmt.Fprintln(w, "highest counter: missing; run ah-switch recover --counter N before the next switch")
	default:
		fmt.Fprintln(w, "highest counter: 0")
	}
	if pending {
		t := &txn{j: j}
		fmt.Fprintf(w, "interrupted: %s %s in phase %s; the next switch, rollback or recover resumes it\n", t.kind(), j.ID, j.Phase)
		if j.StopFailures != "" {
			fmt.Fprintf(w, "stop failures: %s; fix their cause and run a command again, or run ah-switch recover --services-stopped once the decision services are stopped\n", j.StopFailures)
		}
	}
	switch {
	case !s.policy.present:
		fmt.Fprintln(w, "trust: manual: no switch policy, unsigned releases are accepted")
	case s.policy.MinMode == ModeManual:
		fmt.Fprintln(w, "trust: manual: the switch policy accepts unsigned releases")
	default:
		fmt.Fprintf(w, "trust: signed: releases must be signed by %s\n", q(s.policy.Principal))
	}
	if stops, err := s.hostStopList(); err != nil {
		fmt.Fprintf(w, "host stop list: %s; a switch is refused until it is there\n", describe(err))
	} else {
		fmt.Fprintf(w, "host stop list: %d command(s)\n", len(stops))
	}
	if !ok {
		fmt.Fprintln(w, "last switch: none recorded")
		return nil
	}
	kind := "switch to"
	if rec.Rollback {
		kind = "rollback to"
	}
	fmt.Fprintf(w, "last switch: %s %s: %s at %s (mode %s)\n", kind, q(rec.ID), q(rec.Result), q(rec.Time), q(rec.Mode))
	if rec.PendingReport {
		fmt.Fprintf(w, "settled after an interruption; the same request run again reports it once\n")
	}
	if rec.ServicesStopped {
		fmt.Fprintf(w, "decision services: stopped\n")
	}
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
	fmt.Fprintf(w, "package snapshot: %s\n", packageNote(rec))
	return nil
}
