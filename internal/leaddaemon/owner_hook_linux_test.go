//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadlock"
)

// TestOwnerTheServiceIsBuiltWithTheLocksHeldAndClosedAfterTheServer: Build runs
// once, after the daemon holds both locks and has bound the socket, and the
// function it returns runs once, when the server has ended (the socket is
// then gone) and while the daemon lock and the socket lock are still held; the shutdown is clean.
func TestOwnerTheServiceIsBuiltWithTheLocksHeldAndClosedAfterTheServer(t *testing.T) {
	r := newUnstartedOwnerRig(t)
	var inBuild, inClose []string
	free := func(path string) bool {
		l, err := leadlock.Acquire(path)
		if err != nil {
			return false
		}
		l.Release()
		return true
	}
	check := func(into *[]string, wantSocket bool) {
		if free(leadlock.DaemonPath(r.e.state)) {
			*into = append(*into, "the daemon lock is free")
		}
		if free(SocketLockPath(r.e.runtime)) {
			*into = append(*into, "the socket lock is free")
		}
		if _, err := os.Lstat(r.e.socket()); (err == nil) != wantSocket {
			*into = append(*into, "the socket is not as it should be")
		}
	}
	build := r.oc.Build
	r.oc.Build = func() (OwnerService, func() error, error) {
		check(&inBuild, true)
		svc, cl, err := build()
		return svc, func() error { check(&inClose, false); return cl() }, err
	}
	r.run = r.e.startDaemon(r.config())
	if err := r.run.stop(); err != nil {
		t.Fatal(err)
	}
	if len(inBuild) != 0 || len(inClose) != 0 {
		t.Fatalf("at build: %v; at close: %v", inBuild, inClose)
	}
	if r.built != 1 || r.closed != 1 {
		t.Fatalf("built %d, closed %d", r.built, r.closed)
	}
	r.e.lockFree()
}

// TestOwnerABuildThatFailsStopsTheDaemonAtTheServeStep: an error, a missing
// service and a missing piece of the channel each end the daemon before it
// serves, at the serve step with its exit code, with the socket gone and the
// locks free, and nothing is closed that was not built. The error text is the
// daemon's, with what the builder said through SafeText.
func TestOwnerABuildThatFailsStopsTheDaemonAtTheServeStep(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mutate   func(r *ownerRig, closed *atomic.Int32)
		want     string
		wantShut int32
	}{
		{"an error", func(r *ownerRig, _ *atomic.Int32) {
			r.oc.Build = func() (OwnerService, func() error, error) { return nil, nil, errors.New("no \x1b journal") }
		}, `the service could not be built: no \x1b journal`, 0},
		{"no service", func(r *ownerRig, closed *atomic.Int32) {
			r.oc.Build = func() (OwnerService, func() error, error) {
				return nil, func() error { closed.Add(1); return nil }, nil
			}
		}, "the service could not be built", 1},
		{"no builder", func(r *ownerRig, _ *atomic.Int32) { r.oc.Build = nil }, "no way to build the service", 0},
		{"no inspector", func(r *ownerRig, _ *atomic.Int32) { r.oc.Inspector = nil }, "no way to build the service", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newUnstartedOwnerRig(t)
			var closed atomic.Int32
			tt.mutate(r, &closed)
			cfg := r.config()
			err := r.e.runToEnd(cfg)
			se := mustFail(t, err, StepServe, ExitServe)
			if !strings.Contains(se.Error(), tt.want) {
				t.Fatalf("%q, want %q in it", se.Error(), tt.want)
			}
			if closed.Load() != tt.wantShut {
				t.Fatalf("closed %d times", closed.Load())
			}
			r.e.noSocketMade()
			r.e.lockFree()
		})
	}
}

// TestOwnerTheHookIsPassedTheSocketAndEveryOptionIsKept: the hook is given the
// daemon's own listener and options; extensions and settings of the options
// the caller made stay, with the owner channel's added after them.
func TestOwnerTheHookIsPassedTheSocketAndEveryOptionIsKept(t *testing.T) {
	r := newUnstartedOwnerRig(t)
	cfg := r.config()
	cfg.Server.MaxConns = 3
	inner := cfg.ServeHook
	var mu sync.Mutex
	var sawListener net.Listener
	var sawMax, sawExtra int
	cfg.ServeHook = func(l net.Listener, o *clientapi.Options) (net.Listener, func(), error) {
		mu.Lock()
		sawListener, sawMax, sawExtra = l, o.MaxConns, len(o.Extra)
		mu.Unlock()
		return inner(l, o)
	}
	r.run = r.e.startDaemon(cfg)
	// A hello answered is the proof that the hook has run.
	h := ownerHello(t, r.run.socket)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := sawListener.(*net.UnixListener); !ok || sawMax != 3 || sawExtra != 0 {
		t.Fatalf("%T max %d extras %d", sawListener, sawMax, sawExtra)
	}
	if h.Limits.MaxConnections != 3 {
		t.Fatalf("limits %+v", h.Limits)
	}
}
