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
//	var/lib/authority-host/deploy.json, owed.json, highest-counter, counters.log, decisions.inhibit, txn.json, switch.lock
package switcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
// runs only with --from. A failure that comes before it (reading releases/,
// current and previous) is reported as "state" and recorded nowhere (see
// failState); one of the reload list is recorded as "reload".
var stepNames = [...]string{"import", "verify", "counter", "packages", "noaicheck", "validate", "switch", "health", "postcheck", "record"}

// Config is what a switch is run with. Zero fields take their defaults.
type Config struct {
	Root      string // default /
	Dry       bool   // print every step, run no program, change nothing
	AllowExec bool   // with a Root other than /: run programs all the same (an isolated test runner)
	Mode      string // ModeManual or ModeSigned, at least as strict as the policy; empty: the policy's
	From      string // the incoming folder to import the release from; empty: it is imported already
	NoAICheck string // the no-AI check program, a path on the host (below Root), protected code; default the release's bin/ah-noaicheck
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
	// The host's stop list ran in this command (see hostStopsFirst), with
	// these failures: it is not run twice by one command.
	hostStopsRan      bool
	hostStopsFailures []string
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

// emit prints a line as printf does and says whether it was written: a
// writer that buffers (has Flush) is flushed. A line whose writing failed
// cannot be taken as shown.
func (s *Switcher) emit(format string, args ...any) error {
	_, err := fmt.Fprintf(s.cfg.Out, format+"\n", args...)
	if f, ok := s.cfg.Out.(interface{ Flush() error }); ok && err == nil {
		err = f.Flush()
	}
	return err
}

func (s *Switcher) errorf(format string, args ...any) {
	fmt.Fprintf(s.cfg.Err, "ah-switch: "+format+"\n", args...)
}

func (s *Switcher) step(n int, format string, args ...any) {
	s.printf("step %d/9 %s: %s", n, stepNames[n], fmt.Sprintf(format, args...))
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
	if c := s.cfg.NoAICheck; c != "" && (!path.IsAbs(c) || path.Clean(c) != c || c == "/") {
		return fmt.Errorf("--noaicheck %s is not a clean absolute path", q(c))
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

	// The policy and the host's stop list are read before the lock, but
	// what they lack is required only after it: a transaction stopping the
	// decision services is resumed first (see stopFirst).
	code, usage, blocked := s.applyPolicy()
	if usage {
		return code
	}
	// A host stop list that cannot be used (refused included) refuses new
	// work, but keeps no recovery from running: see stopFirst.
	_, hostErr := s.hostStopList()
	what := "switch to " + arg
	if rollback {
		what = "roll back to " + arg
	}
	dry := ""
	if s.cfg.Dry {
		dry = ", dry run: nothing is run or changed"
	}
	if blocked == nil && hostErr == nil {
		s.printf("ah-switch: %s (mode %s, root %s%s)", what, s.mode, q(s.cfg.Root), dry)
	}

	// Blocked, the lock is taken without creating anything: without a
	// state folder or its lock file there is no journal to resume.
	lk, code := s.lockWith(false, blocked == nil && hostErr == nil && !s.cfg.Dry)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	s.stopsOwed(ctx)
	if blocked != nil {
		return s.stopFirst(ctx, blocked, false)
	}
	if hostErr != nil {
		return s.stopFirst(ctx, hostErr, true)
	}

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
	if code, done := s.openBase(); done {
		return code
	}
	if code, halt := s.reconcile(ctx, true); halt {
		return code
	}
	if code, done := s.answerOwed(arg, rollback); done {
		return code
	}
	if _, code, done := s.readState(a); done {
		return code
	}
	if err := s.counterState(a); err != nil {
		// Not recorded: deploy.json is part of the evidence of the
		// counters used, and recover needs it as it is.
		s.printf("state: FAILED: %s", describe(err))
		s.printf("result: failed before step 1; nothing changed or recorded")
		return ExitFailed
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
	ev, err := s.counterEvidence(id.Name, true)
	if err != nil {
		return s.fail(a, 2, evidenceError(err))
	}
	if !present && a.oldExists {
		// Only a dry run without a state folder gets here.
		return s.fail(a, 2, failf(CodeCounterMissing, "%s is missing although current points at %s; run ah-switch recover --counter N", HighestFile, a.rec.PreviousID))
	}
	switch {
	case rollback:
		s.step(2, "rollback to previous: the counter rule does not apply; the highest counter stays %d", highest)
	case id.Counter <= highest:
		return s.fail(a, 2, failf(CodeCounterNotAbove, "counter %d is not above the highest ever attempted, %d", id.Counter, highest))
	case id.Counter <= ev.counter:
		return s.fail(a, 2, failf(CodeCounterNotAbove, "counter %d is not above %d, the counter of %s; build a release with a higher counter", id.Counter, ev.counter, ev.source))
	default:
		s.step(2, "counter %d is above the highest ever attempted, %d, and above every counter this host shows", id.Counter, highest)
	}

	// The no-AI check: an override is checked as protected code and bound
	// by its SHA-256 now, before anything runs; the release's own must be
	// in it.
	check, err := s.newCheckRef()
	if err == nil {
		_, err = s.checkPath(check, rel)
	}
	if err != nil {
		return s.fail(a, 4, err)
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
		CheckProgram: check.prog,
		CheckSHA256:  check.sum,
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
	s.claim(t)
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
	stepRan := false
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
		// 4, first: the release's own inputs are checked before the
		// package step changes the host; nothing has begun yet.
		if err := s.preCheck(ctx, a, check, rel, ", before the package step"); err != nil {
			return s.fail(a, 4, err)
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
		stepRan = true
	}

	// 4. The no-AI check of the new release (phase pre), on the host as it
	// now is: again after a package step, which changed it. A failure from
	// here on, after a package step, stops the decision services and keeps
	// decisions inhibited (see fail).
	when := ""
	if stepRan {
		when = ", on the host the package step changed"
	}
	if err := s.preCheck(ctx, a, check, rel, when); err != nil {
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
		checkProg, _ := s.checkPath(check, rel)
		s.step(8, "would run nft list ruleset into a private file, then %s", qargv([]string{checkProg, "--phase", CheckPhasePost, "--root", s.cfg.Root, "--release", rel.dir, "--require-policy", "--nft-listing", "FILE"}))
		s.step(9, "would record the result in %s, and remove %s (written before anything of the transaction) only if the release passed its health and postcheck", q(filepath.Join(s.stateDir(), DeployFile)), InhibitFile)
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
	if err := s.beginTxn(t); err != nil {
		s.step(6, "FAILED: %s", describe(err))
		s.printf("result: failed at switch; nothing moved, nothing recorded; decisions may stay inhibited")
		return ExitFailed
	}
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
		// The log first: it is the evidence no later record erases.
		id, _ := ParseID(t.j.ID)
		err := s.logCounter(id)
		if err == nil {
			err = s.highestAtLeast(t.j.Counter)
		}
		if err != nil {
			code, _ := s.drive(ctx, t)
			return code
		}
		s.step(6, "highest counter %d (attempted, and in %s)", t.j.Counter, CountersLog)
	}
	if s.crash(CrashCounter) != nil {
		return ExitFailed
	}
	linkFailed := func(name string, err error) int {
		s.step(6, "FAILED: %s: %s", name, q(err.Error()))
		if err := s.putBack(ctx, t, "switch", failf(CodeLinkFailed, "%s: %s", name, q(err.Error()))); err != nil {
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
	return s.lockWith(shared, !s.cfg.Dry && !shared)
}

// lockWith is lock, creating the state folder and the lock file only with
// create.
func (s *Switcher) lockWith(shared, create bool) (*lock, int) {
	state, err := s.openTrusted(StateDir, create)
	if !create && errors.Is(err, fs.ErrNotExist) {
		return &lock{}, ExitOK
	}
	if err != nil {
		s.errorf("state folder %s: %s", q(s.stateDir()), describe(err))
		return nil, ExitFailed
	}
	full := filepath.Join(s.stateDir(), LockFile)
	lk, err := takeLock(state, create, shared, func(info fs.FileInfo) error {
		if err := s.trusted(full, info, false); err != nil {
			return err
		}
		// flock needs only a descriptor open for reading: a lock file
		// another account can read is a lock it can hold.
		if info.Mode().Perm()&0o077 != 0 || info.Size() != 0 || links(info) != 1 {
			return failf(CodeUntrusted, "%s must be owner-only (mode 0600), empty and with one link, as ah-switch creates it; while no ah-switch runs, remove it and run the command again", q(full))
		}
		return nil
	})
	if errors.Is(err, errLocked) {
		s.errorf("%s (%s); nothing done", s.lockHolder(state), q(full))
		state.Close()
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

// lockHolder says who holds the lock of the state folder state, as far as
// the journal tells: the switch that drives its transaction, still running
// (its process id and start time match), or the package step it started,
// which outlived it and keeps the lock through its descriptor 3 and must
// be stopped by hand (or the host rebooted) before anything can settle the
// transaction; otherwise only that it is held. Nothing is killed: a
// process that holds the lock is never taken as gone.
func (s *Switcher) lockHolder(state *os.Root) string {
	j, ok, err := s.readJournalIn(state)
	if err != nil || !ok {
		return errLocked.Error()
	}
	matches := func(pid int, start uint64) bool {
		if pid <= 0 {
			return false
		}
		st, alive, err := s.procs.start(pid)
		return err == nil && alive && st == start
	}
	switch {
	case matches(j.SwitchPID, j.SwitchStart):
		return fmt.Sprintf("an active ah-switch (process %d) holds the lock, driving the %s %s", j.SwitchPID, (&txn{j: j}).kind(), j.ID)
	case j.PackageState == PackageStarted && matches(j.HelperPID, j.HelperStart):
		return fmt.Sprintf("surviving helper pid %d (start time matches): stop it or reboot; it is the package step of the %s %s, which outlived its switch and holds the lock", j.HelperPID, (&txn{j: j}).kind(), j.ID)
	}
	return errLocked.Error()
}

// stopsOwed runs, under the lock and before anything else, the host's stop
// list of a transaction left in its stopping phase: nothing that can fail
// on the way to its resume (releases/ that cannot be used, a counter or a
// record that cannot be read, a journal that cannot be checked against the
// host, a policy that cannot be used) keeps the stops it owes from
// running. The resume runs the rest (see stop) and records it; when it
// cannot, the journal stays in stopping and every command runs them
// again. A dry run runs nothing.
func (s *Switcher) stopsOwed(ctx context.Context) {
	if s.cfg.Dry || s.stateRoot == nil || s.lockFile == nil {
		return
	}
	j, ok, err := s.readJournal()
	if err != nil || !ok || j.Phase != PhaseStopping {
		return
	}
	s.printf("reconcile: the interrupted %s %s is stopping the decision services: the host's %s runs first", (&txn{j: j}).kind(), j.ID, HostStopList)
	s.hostStopsFirst(ctx)
}

// hostStopsFirst runs the host's stop list, once per command, and keeps
// its failures for the stopping phase (see stop): the list that depends on
// no release runs before anything is saved or read that could fail.
func (s *Switcher) hostStopsFirst(ctx context.Context) {
	if s.hostStopsRan {
		return
	}
	s.hostStopsRan = true
	stops, err := s.hostStopList()
	if err != nil {
		s.printf("stop: FAILED: the host's %s: %s", HostStopList, describe(err))
		s.hostStopsFailures = []string{"the host's " + HostStopList + ": " + describe(err)}
		return
	}
	s.hostStopsFailures = s.runStops(ctx, "host", stops)
}

// stopFirst is what a command does, under the lock, when the switch policy
// (its signers file included) or the host's stop list cannot be used: a
// transaction stopping the decision services is resumed first, with the
// trust it began with and whatever of the policy could be read, since
// stopping needs neither (the host's stop list runs when it can be read,
// each release's list when the release verifies). Then blocked, the
// error, is reported, and nothing else is done: no other phase is resumed
// and no new work starts without the policy.
//
// With anyPhase (the host's stop list is what cannot be used, a refused
// one included, and the policy is fine) a journal in any phase is resumed
// first, not only one in stopping: refusing to resume would leave a failed
// candidate's services running with no way back but editing the list by
// hand. The host's list fails in the steps that need it, and says so, and
// the rest of the recovery runs. New work is refused all the same.
func (s *Switcher) stopFirst(ctx context.Context, blocked error, anyPhase bool) int {
	if s.stateRoot != nil && s.lockFile != nil {
		if j, ok, err := s.readJournal(); err == nil && ok && (j.Phase == PhaseStopping || anyPhase) {
			if code, done := s.openBase(); done {
				s.errorf("%s; nothing else done", describe(blocked))
				return code
			}
			if anyPhase {
				s.printf("reconcile: FAILED: the host's stop list cannot be used (%s); the interrupted %s %s is resumed first, without it where it is not needed", describe(blocked), (&txn{j: j}).kind(), j.ID)
			} else {
				s.printf("reconcile: the policy or the host's stop list cannot be used (%s); the stops of the interrupted %s %s run first", describe(blocked), (&txn{j: j}).kind(), j.ID)
			}
			s.reconcile(ctx, false)
		}
	}
	s.errorf("%s; nothing else done", describe(blocked))
	return ExitFailed
}

// claim records in the journal of t that this command drives it, with
// its process id and start time (0 when that cannot be read).
func (s *Switcher) claim(t *txn) {
	t.j.SwitchPID, t.j.SwitchStart = 0, 0
	if st, ok, err := s.procs.start(os.Getpid()); err == nil && ok {
		t.j.SwitchPID, t.j.SwitchStart = os.Getpid(), st
	}
}

// openBase opens opt/authority-host and its releases/ folder, checked as the
// state folder is. A failure is reported and recorded nowhere (see
// failState).
func (s *Switcher) openBase() (int, bool) {
	var err error
	if s.baseRoot, err = s.openTrusted(BaseDir, false); err == nil {
		s.relRoot, err = s.openTrusted(filepath.Join(BaseDir, "releases"), false)
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = failf(CodeStateInvalid, "%s or its releases folder is missing", BaseDir)
	}
	if err != nil {
		return s.failState(err), true
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
		return Record{}, s.failState(err), true
	}
	if a.oldExists {
		id, err := s.targetID(a.oldTarget)
		if err != nil {
			return Record{}, s.failState(fmt.Errorf("current: %w", err)), true
		}
		a.rec.PreviousID = id.Name
		a.rec.CurrentID = id.Name
		if !s.releaseThere(id) {
			s.printf("warning: current points at %s, which is missing or not a folder", id.Name)
		}
	}
	a.prevTarget, a.prevExists, err = readLink(s.baseRoot, "previous")
	if err != nil {
		return Record{}, s.failState(err), true
	}
	if a.prevExists {
		id, err := s.targetID(a.prevTarget)
		if err != nil {
			return Record{}, s.failState(fmt.Errorf("previous: %w", err)), true
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
	a.rec.InhibitClearedAt, a.rec.InhibitClearedAsked = last.InhibitClearedAt, last.InhibitClearedAsked
	a.rec.OutcomeAckedAt, a.rec.OutcomeAckedAsked, a.rec.OutcomeAckedTxn = last.OutcomeAckedAt, last.OutcomeAckedAsked, last.OutcomeAckedTxn
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

// failState ends a command whose state (releases/, current, previous)
// cannot be read before anything else is done. It is reported and not
// recorded: deploy.json is the record of the host's package state, the
// counters it used and an outcome still to report, none of which this
// command read, so writing over it could only lose them.
func (s *Switcher) failState(err error) int {
	s.printf("state: FAILED: %s", describe(err))
	s.printf("result: failed before step 1; nothing changed or recorded")
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
		rel, err := openRelease(id, s.relRoot, id.Name, relDir, s.entryTrusted, s.budget)
		if err == nil {
			if err = s.checkRelease(ctx, rel); err == nil {
				err = s.checkProvenance(rel)
			}
			if err != nil {
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
	if err == nil {
		if err = s.incomingApart(s.cfg.From, src); err != nil {
			src.Close()
		}
	}
	if err != nil {
		return nil, s.fail(a, 0, err), true
	}
	s.reportLeftImports()
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
	// The staging folder is this import's own: a new name, made here
	// (mkdir fails on a name that is there), and the only folder the import
	// ever removes.
	tmp, err := s.newStaging(id)
	if err != nil {
		return nil, s.fail(a, 0, err), true
	}
	tmpDir := filepath.Join(s.base(), "releases", tmp)
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
		err = s.writeProvenance(rel)
	}
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
	s.step(0, "releases/%s imported and made read-only; its import record is %s", id.Name, q(filepath.Join(s.stateDir(), ImportedDir, id.Name)))
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
	if err := s.checkPrograms(rel); err != nil {
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
		// Every signers file is checked here, the policy's and one that
		// only a resumed transaction names, and read from the descriptor
		// that was checked.
		signers, err := s.readSigners(sg.signers)
		if err != nil {
			return err
		}
		if s.cfg.Dry {
			s.step(1, "would verify %s as signed by %s against %s", SignatureFile, q(sg.principal), q(sg.signers))
			continue
		}
		if err := s.verifyWith(ctx, signers, sg.principal, rel); err != nil {
			return err
		}
		s.step(1, "%s verified as signed by %s against %s", SignatureFile, q(sg.principal), q(sg.signers))
	}
	return nil
}

// verifyWith verifies the signature of rel by principal against signers,
// the content of the checked allowed signers file: the verifier is given a
// private copy of it, never the file's own path, which could name another
// file by the time the verifier opens it.
func (s *Switcher) verifyWith(ctx context.Context, signers []byte, principal string, rel *release) error {
	copyPath, done, err := privateCopy("allowed_signers", signers)
	if err != nil {
		return failf(CodeSignatureBad, "%s: the allowed signers file cannot be passed on: %s", SignatureFile, q(err.Error()))
	}
	defer done()
	if err := s.cfg.Verifier.Verify(ctx, copyPath, principal, rel.sums, filepath.Join(rel.path, SignatureFile)); err != nil {
		_, detail := codeOf(err)
		return failf(CodeSignatureBad, "%s: %s", SignatureFile, detail)
	}
	return nil
}

// fail ends a switch that failed at step n before its links moved: current
// is untouched. Outside a dry run the outcome is recorded: directly when
// no transaction began, and through the transaction when its journal was
// written already (the package step ran), which then stops the decision
// services first: the package step changed the host, which nothing
// qualified since, so decisions stay inhibited.
func (s *Switcher) fail(a *attempt, n int, err error) int {
	s.setFailure(a, stepNames[n], err)
	s.step(n, "FAILED: %s", describe(err))
	a.rec.Result = ResultFailed
	if s.cfg.Dry {
		s.printf("result: failed at %s (dry run: nothing recorded); current unchanged", stepNames[n])
		return ExitFailed
	}
	if t := a.t; t != nil && t.begun {
		t.fail(stepNames[n], err)
		if t.j.Check == CheckNotRun {
			t.j.Check = a.rec.Check
		}
		if err := s.toStopping(context.Background(), t, ResultFailed); err != nil {
			if !errors.Is(err, errCrashed) {
				s.printf("result: unresolved: %s; %s stays, and the next switch, rollback or recover resumes it", describe(err), JournalFile)
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
	return s.rewriteRecord(rec)
}

// rewriteRecord writes deploy.json as rec says, its time included.
func (s *Switcher) rewriteRecord(rec *Record) error {
	rec.Detail = boundDetail(rec.Detail)
	return writeJSONIn(s.stateRoot, DeployFile, rec, 0o644)
}

// setFailure records err in the record as the failure of step.
func (s *Switcher) setFailure(a *attempt, step string, err error) {
	a.rec.FailedStep = step
	a.rec.Error, a.rec.Detail = codeOf(err)
}

// readInhibitIfState is readInhibit when the state folder is open.
func (s *Switcher) readInhibitIfState() (inhibit, bool, error) {
	if s.stateRoot == nil {
		return inhibit{}, false, nil
	}
	return s.readInhibit()
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
	var owed []string
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
		if owed, err = s.owedStatus(); err != nil {
			return err
		}
	}
	var ev evidence
	if base != nil {
		rel, err := s.openTrusted(BaseDir+"/releases", false)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if rel != nil {
			s.relRoot = rel
			if ev, err = s.counterEvidence("", false); err != nil {
				return err
			}
		}
	}
	switch {
	case present:
		fmt.Fprintln(w, counterLine(highest, ev))
	case base != nil && hasLink(base, "current"), ev.counter > 0:
		fmt.Fprintln(w, "highest counter: missing; run ah-switch recover --counter N before the next switch")
	default:
		fmt.Fprintln(w, "highest counter: 0")
	}
	if in, ok, err := s.readInhibitIfState(); err != nil {
		return err
	} else if ok {
		fmt.Fprintf(w, "decisions: inhibited (%s) since %s by transaction %s: %s; decision-capable services refuse every decision while it is there; a transaction that ends healthy removes it, or ah-switch inhibit-clear --asked WORDS\n", InhibitFile, q(in.Time), q(in.Txn), q(in.Reason))
	} else {
		fmt.Fprintln(w, "decisions: not inhibited")
	}
	if pending {
		t := &txn{j: j}
		fmt.Fprintf(w, "interrupted: %s %s in phase %s (transaction %s); the next switch, rollback or recover resumes it\n", t.kind(), j.ID, j.Phase, j.Txn)
		if j.Phase == PhaseDone && journalHealthy(j) {
			cur := j.ID
			if j.Result != ResultOK && j.OldCurrent != "" {
				id, _ := s.targetID(j.OldCurrent)
				cur = id.Name
			}
			fmt.Fprintf(w, "requalify: it ended %s before the interruption; the next command runs the reload list and the health check of %s again before decisions are no longer inhibited\n", j.Result, cur)
		}
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
		fmt.Fprintf(w, "host stop list: %s; a switch is refused until it is usable\n", describe(err))
	} else {
		fmt.Fprintf(w, "host stop list: %d command(s)\n", len(stops))
	}
	if rec.InhibitClearedAt != "" {
		fmt.Fprintf(w, "inhibit last cleared by the owner at %s, asked: %s\n", q(rec.InhibitClearedAt), q(rec.InhibitClearedAsked))
	}
	for _, line := range owed {
		fmt.Fprintln(w, line)
	}
	if rec.OutcomeAckedAt != "" {
		fmt.Fprintf(w, "outcomes last acknowledged by the owner at %s (transactions %s), asked: %s\n", q(rec.OutcomeAckedAt), q(rec.OutcomeAckedTxn), q(rec.OutcomeAckedAsked))
	}
	if !ok || rec.Txn == "" && rec.ID == "" {
		fmt.Fprintln(w, "last switch: none recorded")
		return nil
	}
	kind := "switch to"
	if rec.Rollback {
		kind = "rollback to"
	}
	fmt.Fprintf(w, "last switch: %s %s: %s at %s (mode %s)\n", kind, q(rec.ID), q(rec.Result), q(rec.Time), q(rec.Mode))
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
