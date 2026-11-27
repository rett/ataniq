package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
	"ataniq/internal/pidfd"
)

// DefaultVersion is the version the hello operation reports unless the
// config sets another. The repository's VERSION file cannot be embedded from
// the command's folder, and no file is read at run time.
const DefaultVersion = "0.0.0-dev"

// Defaults of [Config].
const (
	// DefaultReconcileTimeout bounds the one reconcile at start.
	DefaultReconcileTimeout = 30 * time.Second
	// DefaultShutdownGrace is how long shutdown waits for the connections.
	DefaultShutdownGrace = 5 * time.Second
)

// RegistryDirName is the name of the registry folder in the state folder.
const RegistryDirName = leadregistry.DirName

// Config is what [Run] needs. Every dependency that touches the host is
// injectable, so that a test runs the whole daemon in process.
type Config struct {
	// CanaryConfig is what a build with the ataniq_canary tag adds: the
	// canary parts. In a build without the tag it is an empty struct and
	// nothing can set it, so the daemon is the read-only skeleton.
	CanaryConfig

	// StateDir is the state folder, a clean absolute path. Required.
	StateDir string
	// RuntimeDir is the folder that holds daemon.sock, a clean absolute path
	// whose parent exists. Required.
	RuntimeDir string
	// NewManager builds the unit manager. Required. An error stops the
	// daemon; a command that wants to serve without systemd returns
	// [UnavailableManager] instead.
	NewManager func(ctx context.Context) (leadunit.Manager, error)
	// Clock is the time source of the registry, the controller and the daemon
	// block; default leadunit.SystemClock.
	Clock leadunit.Clock
	// Signals delivers the signals that end the daemon (the command registers
	// SIGTERM and SIGINT); any value ends it. Nil means only the context
	// does.
	Signals <-chan os.Signal
	// EUID returns the effective user id; default os.Geteuid. Root is refused.
	EUID func() int
	// BootID reads the boot identity; default pidfd.BootID. An error stops
	// the daemon.
	BootID func() (string, error)
	// Version is what hello reports; default [DefaultVersion].
	Version string
	// Log receives the daemon's lines, one per event; nil discards them.
	Log io.Writer
	// ReconcileTimeout bounds the reconcile; default
	// [DefaultReconcileTimeout]. A reconcile that runs out is not completed.
	ReconcileTimeout time.Duration
	// ShutdownGrace bounds the wait for the connections at shutdown, in real
	// time; default [DefaultShutdownGrace].
	ShutdownGrace time.Duration
	// Server holds the options of the socket server; the zero value is the
	// production one.
	Server clientapi.Options
	// ServeHook, when set, is called once, with both locks held and the socket
	// bound, just before the server starts. It may add extensions and set
	// options through opts, and may return a wrapper of the listener; the
	// returned function, when not nil, is called when the server has ended
	// (not after a server that is abandoned at shutdown). An error stops the
	// daemon at [StepServe]. Nothing in a release build sets it: the command
	// sets it only in a build with the ataniq_ownerchannel tag, and only for
	// the owner channel's flag.
	ServeHook func(l net.Listener, opts *clientapi.Options) (net.Listener, func(), error)
	// Observe, when set, is called as each step begins. A test records the
	// order with it.
	Observe func(Step)
	// Ready, when set, is called with the socket path once the daemon is
	// listening and before it serves.
	Ready func(socket string)
}

type daemon struct {
	Config
	log io.Writer
}

func (d *daemon) logf(format string, a ...any) {
	fmt.Fprintf(d.log, "ataniqd: "+format+"\n", a...)
}

func (d *daemon) step(s Step) {
	if d.Observe != nil {
		d.Observe(s)
	}
}

// Run is the daemon: it takes the steps in the package documentation and
// serves until the signal source delivers a signal or ctx ends, then shuts
// down. It returns nil after a clean shutdown (also one that was asked for
// before the daemon began to serve), a [StartupError] when a step failed, and
// another error for a server that stopped by itself. A server that has not
// ended within the shutdown grace makes it return a [StartupError] of
// [StepShutdown] ([ExitShutdown]) that says what is left: the registry is then
// left open, with the daemon lock and the socket lock held, not closed under a
// goroutine that may still use them, and all end with the process. It starts,
// signals and stops no unit itself; in canary mode (a build with the
// ataniq_canary tag) the operations of the canary extension do.
func Run(ctx context.Context, cfg Config) error {
	d := &daemon{Config: cfg, log: cfg.Log}
	if d.log == nil {
		d.log = io.Discard
	}
	if d.Clock == nil {
		d.Clock = leadunit.SystemClock{}
	}
	if d.EUID == nil {
		d.EUID = os.Geteuid
	}
	if d.BootID == nil {
		d.BootID = pidfd.BootID
	}
	if d.Version == "" {
		d.Version = DefaultVersion
	}
	if d.ReconcileTimeout <= 0 {
		d.ReconcileTimeout = DefaultReconcileTimeout
	}
	if d.ShutdownGrace <= 0 {
		d.ShutdownGrace = DefaultShutdownGrace
	}

	// abandoned is set when the server did not end within the grace: the
	// registry and the locks are then left to the end of the process.
	abandoned := false

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.Signals != nil {
		go func() {
			select {
			case <-d.Signals:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	// config
	d.step(StepConfig)
	if err := d.checkConfig(); err != nil {
		return err
	}
	// canary mode, when it is asked for in a build that has it: the config,
	// the roots and the marker are checked here, before anything is made. In
	// every other case canary is nil and its methods do nothing.
	canary, err := d.canarySetup()
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	// state
	d.step(StepState)
	if err := ensureStateDir(d.StateDir); err != nil {
		return &StartupError{Step: StepState, Err: err}
	}
	if ctx.Err() != nil {
		return nil
	}

	// lock: held until the function returns, released after everything else.
	d.step(StepLock)
	lock, err := leadlock.Acquire(leadlock.DaemonPath(d.StateDir))
	if err != nil {
		if errors.Is(err, leadlock.ErrHeld{}) {
			return &StartupError{Step: StepLock, Err: fmt.Errorf("%w: %w", errLockHeld, err)}
		}
		return &StartupError{Step: StepLock, Err: err}
	}
	defer func() {
		if abandoned {
			return
		}
		if err := lock.Release(); err != nil {
			d.logf("release the daemon lock: %v", err)
		}
	}()
	startedAt := d.Clock.Now()
	if ctx.Err() != nil {
		return nil
	}

	// socket lock, before the registry is opened and reconciled, so that a
	// daemon that is refused here has written nothing: another daemon with
	// another state folder and the same runtime folder holds it. The daemon
	// lock is per state folder and cannot tell a live socket from a stale one
	// across state folders; this lock is per runtime folder. It is released
	// after the registry is closed and before the daemon lock.
	d.step(StepSocketLock)
	if err := ensureRuntimeDir(d.RuntimeDir); err != nil {
		return &StartupError{Step: StepSocketLock, Err: err}
	}
	sockLock, err := leadlock.Acquire(SocketLockPath(d.RuntimeDir))
	if err != nil {
		if errors.Is(err, leadlock.ErrHeld{}) {
			return &StartupError{Step: StepSocketLock, Err: fmt.Errorf("another daemon serves the runtime folder %s (the pid in the lock is a hint, not proof): %w", d.RuntimeDir, err)}
		}
		return &StartupError{Step: StepSocketLock, Err: err}
	}
	defer func() {
		if abandoned {
			return
		}
		if err := sockLock.Release(); err != nil {
			d.logf("release the socket lock: %v", err)
		}
	}()
	if ctx.Err() != nil {
		return nil
	}

	// Read the managed launch identity and open the evidence under both locks.
	if err := canary.openState(); err != nil {
		return &StartupError{Step: StepState, Err: err}
	}
	defer func() {
		if abandoned {
			return
		}
		if err := canary.closeEvidence(); err != nil {
			d.logf("close the canary evidence: %v", err)
		}
	}()

	// registry
	d.step(StepRegistry)
	reg, err := leadregistry.Open(filepath.Join(d.StateDir, RegistryDirName), leadregistry.Options{Now: d.Clock.Now})
	if err != nil {
		if errors.Is(err, leadregistry.ErrCorrupt) {
			return &StartupError{Step: StepRegistry, Err: fmt.Errorf("the registry is corrupt and nothing was repaired: %w", err)}
		}
		return &StartupError{Step: StepRegistry, Err: err}
	}
	defer func() {
		if abandoned {
			return
		}
		if err := reg.Close(); err != nil {
			d.logf("close the registry: %v", err)
		}
	}()
	if ctx.Err() != nil {
		return nil
	}

	for _, warning := range reg.MissingHomeWarnings(canary.configuredRoles()) {
		d.logf("warning: %s", warning)
	}

	// manager and controller
	d.step(StepManager)
	boot, err := d.BootID()
	if err != nil {
		return &StartupError{Step: StepManager, Err: fmt.Errorf("read the boot identity: %w", err)}
	}
	built, err := d.NewManager(ctx)
	if err != nil {
		return &StartupError{Step: StepManager, Err: fmt.Errorf("build the unit manager: %w", err)}
	}
	// The lifecycle gets the counting wrapper, so that every call that fails
	// is known to the reconcile's verdict.
	mgr := &failureCounter{Manager: built}
	lifecycle := leadlifecycle.Options{
		Registry: reg, Manager: mgr, Clock: d.Clock,
		Preflight: refusePreflight{}, Holder: refuseHolder{},
	}
	canary.configure(&lifecycle)
	ctl, err := leadlifecycle.New(lifecycle)
	if err != nil {
		return &StartupError{Step: StepManager, Err: fmt.Errorf("build the lifecycle controller: %w", err)}
	}
	if ctx.Err() != nil {
		return nil
	}

	// reconcile, once
	d.step(StepReconcile)
	completed, err := d.reconcile(ctx, ctl, reg, mgr)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	info := daemonInfo{pid: os.Getpid(), startedAt: startedAt, bootID: boot, completed: completed, at: d.Clock.Now()}
	if ctx.Err() != nil {
		return nil
	}
	// The extensions of canary or scratch mode, before the socket is made:
	// scratch mode reads again the build of every lead it adopted shown
	// ready, and a state it found and could not record ends the start here,
	// with nothing served.
	extensions, err := canary.extensions(ctl, reg)
	if err != nil {
		return &StartupError{Step: StepReconcile, Err: err}
	}

	// stale socket, with both locks held (the socket lock was taken before the
	// registry was opened). The listener is closed, which unlinks the socket,
	// before the socket lock is released, so that the removal of a stale socket
	// and the unlink at close are both under it.
	d.step(StepStaleSocket)
	removed, err := clientapi.RemoveStale(d.RuntimeDir)
	if err != nil {
		return &StartupError{Step: StepStaleSocket, Err: err}
	}
	if removed {
		d.logf("removed a stale socket in %s", d.RuntimeDir)
	}
	if ctx.Err() != nil {
		return nil
	}

	// socket
	d.step(StepSocket)
	l, socket, err := clientapi.Listen(d.RuntimeDir)
	if err != nil {
		return &StartupError{Step: StepSocket, Err: err}
	}
	d.logf("listening on %s", socket)
	if err := canary.announce(); err != nil {
		l.Close()
		return &StartupError{Step: StepSocket, Err: err}
	}
	if d.Ready != nil {
		d.Ready(socket)
	}

	// serve
	d.step(StepServe)
	serveCtx, serveCancel := context.WithCancel(ctx)
	defer serveCancel()
	done := make(chan error, 1)
	srvOpts := d.Server
	srvOpts.Extra = append(slices.Clone(srvOpts.Extra), extensions...)
	if d.ServeHook != nil {
		wrapped, hookDone, err := d.ServeHook(l, &srvOpts)
		if err != nil {
			l.Close()
			return &StartupError{Step: StepServe, Err: err}
		}
		l = wrapped
		if hookDone != nil {
			defer func() {
				if !abandoned {
					hookDone()
				}
			}()
		}
	}
	go func() {
		done <- clientapi.Serve(serveCtx, l, &handler{reg: reg, version: d.Version, info: info}, srvOpts)
	}()
	// The background observers of scratch mode (none otherwise) run under the
	// server's context and are joined below, before the registry is closed.
	canary.startObservers(serveCtx)
	var serveErr error
	select {
	case <-ctx.Done():
		serveCancel()
		d.step(StepShutdown)
		if !d.awaitServe(done) {
			abandoned = true
			serveErr = &StartupError{Step: StepShutdown, Err: fmt.Errorf(
				"the server did not stop within %s; its goroutine and the connections it holds may still be running, the registry is left open and the daemon lock held, the socket lock too, and all end with this process",
				d.ShutdownGrace)}
		}
	case err := <-done:
		d.step(StepShutdown)
		// A server that returns because the context ended is the same as one
		// that was asked to stop.
		if ctx.Err() == nil {
			if err == nil {
				err = errors.New("the server returned with no error before it was asked to stop")
			}
			serveErr = &StartupError{Step: StepServe, Err: err}
		}
	}
	// A server that was abandoned has already used the grace and leaves the
	// registry open, so there is nothing an observer could still be waited
	// for: the observers are told to end, and not waited for a second grace.
	observerGrace := d.ShutdownGrace
	if abandoned {
		observerGrace = 0
	}
	if !canary.joinObservers(observerGrace) && !abandoned {
		abandoned = true
		d.logf("an observer did not stop within %s", d.ShutdownGrace)
		if serveErr == nil {
			serveErr = &StartupError{Step: StepShutdown, Err: fmt.Errorf(
				"an observer did not stop within %s; its goroutine may still be running, the registry is left open and the daemon lock held, the socket lock too, and all end with this process",
				d.ShutdownGrace)}
		}
	}
	// The registry, the socket lock and the daemon lock are released by the
	// deferred calls, in that order, the daemon lock last, and the listener is
	// closed (which unlinks the socket) by then, under the socket lock. A
	// server that is left running leaves all three to the end of the process,
	// and is reported by the returned error alone, which the command prints
	// once.
	return serveErr
}

// awaitServe waits for the server to end, up to the grace, and reports whether
// it ended.
func (d *daemon) awaitServe(done <-chan error) bool {
	grace := time.NewTimer(d.ShutdownGrace)
	defer grace.Stop()
	select {
	case <-done:
		return true
	case <-grace.C:
		return false
	}
}

func (d *daemon) checkConfig() error {
	switch {
	case d.EUID() == 0:
		return fail(StepConfig, "refusing to run as root")
	case !cleanAbs(d.StateDir):
		return fail(StepConfig, "the state folder %q is not a clean absolute path", d.StateDir)
	case !cleanAbs(d.RuntimeDir):
		return fail(StepConfig, "the runtime folder %q is not a clean absolute path", d.RuntimeDir)
	case d.NewManager == nil:
		return fail(StepConfig, "no unit manager factory")
	}
	return nil
}

// ensureRuntimeDir makes the runtime folder with mode 0700 when it is missing
// (its parent must exist, as for [clientapi.Listen]) and checks it as Listen
// does: a real folder, owned by the caller, with no access for group or
// others. The socket lock is made in it, so it is checked before that. A
// socket path that cannot be bound is refused first, before anything is made.
func ensureRuntimeDir(dir string) error {
	if err := clientapi.CheckSocketPath(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err == nil {
		// The umask can only narrow 0700; this sets it exactly.
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder (a symbolic link is refused too)", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read the owner of the runtime folder")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s is not owned by the caller", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 || perm&0o700 != 0o700 {
		return fmt.Errorf("%s is accessible to group or others or not usable by its owner (mode %04o); it must be 0700", dir, perm)
	}
	return nil
}

func cleanAbs(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }

// reconcile runs the one Reconcile and reports whether it completed. A
// manager that is known to be unavailable (also with nothing in the registry
// to ask it about), a manager call that failed (Inspect, Empty or any other;
// see [failureCounter] for the answers that are not failures), a reconcile
// whose context ended (also inside the last role, where no later step would
// see it) and an unacknowledged recovery of the registry make it not
// completed and are not fatal; a failure of the registry is.
func (d *daemon) reconcile(ctx context.Context, ctl *leadlifecycle.Controller, reg *leadregistry.Registry, mgr *failureCounter) (bool, error) {
	rctx, rcancel := context.WithTimeout(ctx, d.ReconcileTimeout)
	defer rcancel()
	rep, err := ctl.Reconcile(rctx)
	completed := true
	if err != nil {
		if ctx.Err() != nil {
			return false, err
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return false, &StartupError{Step: StepReconcile, Err: err}
		}
		completed = false
		d.logf("the reconcile did not finish within %s; it is shown as not completed", d.ReconcileTimeout)
	} else if rctx.Err() != nil && ctx.Err() == nil {
		// The deadline had passed when Reconcile returned, without an error: it
		// ended inside the last role's call, where no later step would have
		// noticed it, or just after the reconcile finished. The two cannot be
		// told apart here, and both are shown as not completed.
		completed = false
		d.logf("the reconcile's deadline (%s) had passed when it returned; it is shown as not completed", d.ReconcileTimeout)
	}
	unavailable := 0
	for _, r := range rep.Roles {
		if r.Action == leadlifecycle.ActionUnavailable {
			unavailable++
		}
	}
	if um, known := mgr.Manager.(unavailableManager); known {
		completed = false
		d.logf("the unit manager is unavailable (%s); the reconcile is shown as not completed and the roles as the registry has them", SafeText(fmt.Sprint(um.cause)))
	}
	if n := mgr.failures(); n > 0 {
		completed = false
		d.logf("%d unit manager call(s) failed during the reconcile; it is shown as not completed", n)
	}
	if unavailable > 0 {
		completed = false
		d.logf("the unit manager did not answer for %d role(s); the reconcile is shown as not completed and the roles as the registry has them", unavailable)
	}
	if rec := reg.Recovery(); rec.Cut() {
		completed = false
		d.logf("the registry journal was cut when it was opened (%d line(s), %d byte(s)) and the cut is not acknowledged; the reconcile is shown as not completed", rec.Lines, rec.Bytes)
	}
	return completed, nil
}

// ensureStateDir creates the state folder with mode 0700 when it is missing
// and checks it: a real folder (not a symbolic link), owned by the caller,
// with no permission for group or others.
func ensureStateDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err == nil {
		// The umask can only narrow 0700; this sets it exactly.
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder (a symbolic link is refused too)", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read the owner of the state folder")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s is not owned by the caller", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 || perm&0o700 != 0o700 {
		return fmt.Errorf("%s has mode %04o; it must be 0700 (no access for group or others)", dir, perm)
	}
	return nil
}
