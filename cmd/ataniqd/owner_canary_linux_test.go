//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"context"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leaddaemon"
)

// TestOwnerWithCanaryRunsBothExtensions: run --canary CONFIG --owner-channel in
// a build with both tags lists the canary's operations and then the owner's,
// with the roots the canary config's.
func TestOwnerWithCanaryRunsBothExtensions(t *testing.T) {
	_, builds := ownerCmdSetup(t)
	f := newCanaryFixture(t)
	ta := canaryApp(t)
	done := startApp(t, ta, []string{"--canary", f.cfgPath, "--owner-channel"}, f.socket())
	ops, err := helloOperations(context.Background(), f.socket())
	want := append(append([]string{"hello", "status"}, leaddaemon.CanaryOperations()...), ownerwire.Operations()...)
	if err != nil || strings.Join(ops, " ") != strings.Join(want, " ") {
		t.Fatalf("operations %v, %v; want %v", ops, err, want)
	}
	if builds() != 1 {
		t.Fatalf("built %d times", builds())
	}
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
}
