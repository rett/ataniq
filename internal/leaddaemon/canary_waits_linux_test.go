//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// holderInjectTimeout is the bound of the holder's inject handler on one
// delivery: InjectTimeout of internal/leadholder/serve, 15 s. It is a literal
// here because the handler is a tagged part of that package that this package
// does not import (it names the control socket itself for the same reason, see
// canary_control_linux.go); the number is the one of the slice's shared names.
const holderInjectTimeout = 15 * time.Second

// TestCanaryWaits pins the waits of an inject and their order: the holder's
// handler gives up first, then the daemon, then the command's client, so that
// each side's answer, the uncertain one included, reaches the next.
func TestCanaryWaits(t *testing.T) {
	if DefaultInjectWait != 20*time.Second {
		t.Errorf("the daemon waits %s for the holder, want 20 s", DefaultInjectWait)
	}
	if CanaryInjectTimeout != 30*time.Second {
		t.Errorf("the command's client waits %s, want 30 s", CanaryInjectTimeout)
	}
	if DefaultDialTimeout != 5*time.Second {
		t.Errorf("the dial and the write take up to %s, want 5 s", DefaultDialTimeout)
	}
	if holderInjectTimeout >= DefaultInjectWait {
		t.Errorf("the daemon's wait (%s) is not longer than the holder's (%s)", DefaultInjectWait, holderInjectTimeout)
	}
	// The daemon's whole exchange: one deadline for the dial and the write,
	// then the wait.
	if whole := DefaultDialTimeout + DefaultInjectWait; whole >= CanaryInjectTimeout {
		t.Errorf("the daemon's exchange (%s) is not shorter than the client's wait (%s)", whole, CanaryInjectTimeout)
	}
	if CanaryCallTimeout <= CanaryInjectTimeout {
		t.Errorf("the other calls wait %s, not longer than an inject's %s", CanaryCallTimeout, CanaryInjectTimeout)
	}
}

// TestCanaryModeTakesTheDefaultWaits: a Canary that names no wait gets the
// defaults, each in its own place.
func TestCanaryModeTakesTheDefaultWaits(t *testing.T) {
	c := newCanaryEnv(t)
	cfg := c.daemonConfig()
	cfg.Canary.InjectWait, cfg.Canary.DialTimeout = 0, 0
	m, err := (&daemon{Config: cfg, log: io.Discard}).canarySetup()
	if err != nil {
		t.Fatal(err)
	}
	if m.injectFor != DefaultInjectWait || m.dialFor != DefaultDialTimeout {
		t.Fatalf("waits %s and %s, want %s and %s", m.injectFor, m.dialFor, DefaultInjectWait, DefaultDialTimeout)
	}
	x := newCanaryExt(m, nil, nil)
	if x.wait != DefaultInjectWait || x.dial != DefaultDialTimeout {
		t.Fatalf("the extension waits %s and %s", x.wait, x.dial)
	}
}

// TestCallHolderWaitsForTheAnswerByTheWait: the wait for the answer is the
// wait, not the bound of the dial and the write, in both directions.
func TestCallHolderWaitsForTheAnswerByTheWait(t *testing.T) {
	req := wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "msg-one", Text: "sample text\n", CommandID: "canary-inject-1-1"}

	t.Run("an answer later than the dial bound is taken", func(t *testing.T) {
		c := newCanaryEnv(t)
		newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) {
			time.Sleep(700 * time.Millisecond)
			submitted(req, conn)
		})
		resp, wrote, err := callHolder(context.Background(), c.holderDir, req, 250*time.Millisecond, 20*time.Second)
		if err != nil || !wrote || !resp.OK || resp.Inject == nil || resp.Inject.Outcome != wire.InjectOutcomeSubmitted {
			t.Fatalf("%+v wrote %v: %v", resp, wrote, err)
		}
	})

	t.Run("no answer ends at the wait", func(t *testing.T) {
		c := newCanaryEnv(t)
		newFakeHolder(t, c.holderDir, silent)
		start := time.Now()
		_, wrote, err := callHolder(context.Background(), c.holderDir, req, 20*time.Second, 200*time.Millisecond)
		took := time.Since(start)
		if err == nil || !wrote {
			t.Fatalf("wrote %v: %v", wrote, err)
		}
		if took < 150*time.Millisecond || took > 10*time.Second {
			t.Fatalf("the call took %s with a wait of 200 ms", took)
		}
	})

	t.Run("a dial bound that is over writes nothing", func(t *testing.T) {
		c := newCanaryEnv(t)
		h := newFakeHolder(t, c.holderDir, submitted)
		// The caller's own deadline is the earlier one, and it is past.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, wrote, err := callHolder(ctx, c.holderDir, req, 20*time.Second, 20*time.Second)
		if err == nil || wrote {
			t.Fatalf("wrote %v: %v", wrote, err)
		}
		if n := len(h.requests("")); n != 0 {
			t.Fatalf("%d requests reached the holder", n)
		}
	})
}
