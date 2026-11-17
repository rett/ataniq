package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
const RegistryDirName = "registry"

// Config is what [Run] needs. Every dependency that touches the host is
// injectable, so that a test runs the whole daemon in process.
type Config struct {
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
// another error for a server that stopped by itself. It starts, signals and
// stops no unit.
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
		if err := lock.Release(); err != nil {
			d.logf("release the daemon lock: %v", err)
		}
	}()
	startedAt := d.Clock.Now()
	if ctx.Err() != nil {
		return nil
	}

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
		if err := reg.Close(); err != nil {
			d.logf("close the registry: %v", err)
		}
	}()
	if ctx.Err() != nil {
		return nil
	}

	// manager and controller
	d.step(StepManager)
	boot, err := d.BootID()
	if err != nil {
		return &StartupError{Step: StepManager, Err: fmt.Errorf("read the boot identity: %w", err)}
	}
	mgr, err := d.NewManager(ctx)
	if err != nil {
		return &StartupError{Step: StepManager, Err: fmt.Errorf("build the unit manager: %w", err)}
	}
	ctl, err := leadlifecycle.New(leadlifecycle.Options{
		Registry: reg, Manager: mgr, Clock: d.Clock,
		Preflight: refusePreflight{}, Holder: refuseHolder{},
	})
	if err != nil {
		return &StartupError{Step: StepManager, Err: fmt.Errorf("build the lifecycle controller: %w", err)}
	}
	if ctx.Err() != nil {
		return nil
	}

	// reconcile, once
	d.step(StepReconcile)
	completed, err := d.reconcile(ctx, ctl, reg)
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

	// stale socket, only with the lock held
	d.step(StepStaleSocket)
	if _, err := os.Lstat(d.RuntimeDir); err == nil {
		removed, err := clientapi.RemoveStale(d.RuntimeDir)
		if err != nil {
			return &StartupError{Step: StepStaleSocket, Err: err}
		}
		if removed {
			d.logf("removed a stale socket in %s", d.RuntimeDir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return &StartupError{Step: StepStaleSocket, Err: err}
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
	if d.Ready != nil {
		d.Ready(socket)
	}

	// serve
	d.step(StepServe)
	serveCtx, serveCancel := context.WithCancel(ctx)
	defer serveCancel()
	done := make(chan error, 1)
	go func() {
		done <- clientapi.Serve(serveCtx, l, &handler{reg: reg, version: d.Version, info: info}, d.Server)
	}()
	var serveErr error
	select {
	case <-ctx.Done():
		serveCancel()
		d.step(StepShutdown)
		d.awaitServe(done)
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
	// The registry and the lock are released by the deferred calls, in that
	// order: the lock last.
	return serveErr
}

// awaitServe waits for the server to end, up to the grace.
func (d *daemon) awaitServe(done <-chan error) {
	grace := time.NewTimer(d.ShutdownGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		d.logf("connections did not end within %s; shutting down anyway", d.ShutdownGrace)
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

func cleanAbs(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }

// reconcile runs the one Reconcile and reports whether it completed. A
// manager that does not answer, a reconcile that ran out of time and an
// unacknowledged recovery of the registry make it not completed and are not
// fatal; a failure of the registry is.
func (d *daemon) reconcile(ctx context.Context, ctl *leadlifecycle.Controller, reg *leadregistry.Registry) (bool, error) {
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
	}
	unavailable := 0
	for _, r := range rep.Roles {
		if r.Action == leadlifecycle.ActionUnavailable {
			unavailable++
		}
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
