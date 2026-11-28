//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"net"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// TestCallHolderThenHonoursAContextThatEndsWhileWrittenRuns: a context that
// ends inside the record callback (the evidence append takes time) ends the
// exchange before it starts: the dial that follows fails, nothing is sent, and
// the call does not wait out the holder. The line is recorded, so the stage is
// the unsent one.
func TestCallHolderThenHonoursAContextThatEndsWhileWrittenRuns(t *testing.T) {
	c := newCanaryEnv(t)
	release := make(chan struct{})
	holder := newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) { <-release })
	t.Cleanup(func() { close(release) })
	req := wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "msg-one", Text: "body text\n", CommandID: "sample-1"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	_, st, err := callHolderThen(ctx, c.holderDir, req, 2*time.Second, 10*time.Second, func() error {
		cancel()
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the call waited %v after the context ended", took)
	}
	if st != sendUnsent || err == nil {
		t.Fatalf("stage %v, err %v", st, err)
	}
	if n := len(holder.requests(wire.OpInject)); n != 0 {
		t.Fatalf("%d frames reached the holder", n)
	}
}
