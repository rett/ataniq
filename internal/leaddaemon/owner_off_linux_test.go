//go:build linux && !ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/clientapi"
)

// TestNoOwnerOperationsWithoutTheTag: a build without the ataniq_ownerchannel
// tag serves what it served before: hello lists the two base operations and
// says it mutates nothing, no hook is set by the daemon, and an owner operation
// is unsupported. (The tagged build's hello is in owner_decider_linux_test.go.)
func TestNoOwnerOperationsWithoutTheTag(t *testing.T) {
	e := newEnv(t)
	cfg := e.config()
	if cfg.ServeHook != nil {
		t.Fatal("a hook is set in a config nobody set one in")
	}
	r := e.startDaemon(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, r.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.Operations, " ") != "hello status" || h.Mutations {
		t.Fatalf("operations %v mutations %v", h.Operations, h.Mutations)
	}
	for _, op := range []string{"owner.list", "owner.decide", "owner.decider"} {
		err := c.Call(ctx, op, nil, nil)
		var ce *clientapi.Error
		if !errors.As(err, &ce) || ce.Code != clientapi.CodeUnsupportedOperation {
			t.Errorf("%s: %v", op, err)
		}
	}
}
