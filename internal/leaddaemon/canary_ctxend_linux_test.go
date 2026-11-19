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
// ends inside the written callback (the evidence append takes time) still ends
// the wait, and the call does not wait out the holder.
func TestCallHolderThenHonoursAContextThatEndsWhileWrittenRuns(t *testing.T) {
	c := newCanaryEnv(t)
	release := make(chan struct{})
	newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) { <-release })
	t.Cleanup(func() { close(release) })
	req := wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "msg-one", Text: "body text\n", CommandID: "sample-1"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	_, wrote, err := callHolderThen(ctx, c.holderDir, req, 2*time.Second, 10*time.Second, func() {
		cancel()
		time.Sleep(20 * time.Millisecond)
	})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the call waited %v after the context ended", took)
	}
	if !wrote || err == nil {
		t.Fatalf("wrote %v, err %v", wrote, err)
	}
}
