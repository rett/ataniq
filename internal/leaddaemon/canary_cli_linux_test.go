//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunCanaryPrintsOneLineAndMapsTheErrors(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	run := func(call CanaryCall) (int, string, string) {
		var out, errOut bytes.Buffer
		if call.Socket == "" {
			call.Socket = c.running.socket
		}
		code := RunCanary(context.Background(), call, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	code, out, errOut := run(CanaryCall{Op: OpCanaryDefine, Role: canaryRoleText})
	if code != ExitOK || out != "{\"role\":\"throwaway-one/lead\",\"state\":\"stopped\"}\n" || errOut != "" {
		t.Fatalf("define: %d %q %q", code, out, errOut)
	}
	if code, out, _ = run(CanaryCall{Op: OpCanaryDefine, Role: "throwaway-two/lead"}); code != ExitRefused || out != "" {
		t.Fatalf("other role: %d %q", code, out)
	}
	// No holder: the ticket and the message are unavailable. The role is not
	// running yet, which is a refusal.
	if code, _, _ = run(CanaryCall{Op: OpCanaryTicket, Role: canaryRoleText, AttachRole: "writer"}); code != ExitRefused {
		t.Fatalf("ticket before start: %d", code)
	}
	if code, _, _ = run(CanaryCall{Op: OpCanaryStart, Role: canaryRoleText}); code != ExitOK {
		t.Fatalf("start: %d", code)
	}
	if code, out, errOut = run(CanaryCall{Op: OpCanaryTicket, Role: canaryRoleText, AttachRole: "writer"}); code != ExitUnavailable ||
		out != "" || !strings.Contains(errOut, "unavailable") {
		t.Fatalf("ticket without a holder: %d %q %q", code, out, errOut)
	}
	// The daemon's own invalid-request answer is a protocol error.
	if code, _, _ = run(CanaryCall{Op: OpCanaryTicket, Role: canaryRoleText, AttachRole: "admin"}); code != ExitProtocol {
		t.Fatalf("bad attach role: %d", code)
	}
	if code, _, _ = run(CanaryCall{Op: "canary.nothing", Role: canaryRoleText}); code != ExitUsage {
		t.Fatalf("unknown operation: %d", code)
	}
	if code, _, errOut = run(CanaryCall{Op: OpCanaryStop, Role: canaryRoleText, Socket: c.base + "/none.sock"}); code != ExitNoDaemon {
		t.Fatalf("no daemon: %d %q", code, errOut)
	}
}
