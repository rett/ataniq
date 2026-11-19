//go:build linux && ataniq_canary

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// canaryFixture is the folders and the config of a canary run, none of them a
// default root of the account.
type canaryFixture struct {
	base, state, runtime, holder string
	cfg                          leadcanary.Config
	cfgPath                      string
}

func newCanaryFixture(t *testing.T) canaryFixture {
	t.Helper()
	base := shortDir(t)
	f := canaryFixture{base: base, state: base + "/s", runtime: base + "/r", holder: base + "/r/leads/throwaway-one-lead"}
	for _, d := range []string{f.state, f.runtime, f.runtime + "/leads", f.holder, base + "/conf"} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	role, err := leadrole.Parse("throwaway-one/lead")
	if err != nil {
		t.Fatal(err)
	}
	if err := canaryroot.WriteMarker(f.state, role.Project()); err != nil {
		t.Fatal(err)
	}
	f.cfg = leadcanary.Config{
		Version: 1, Role: role, StateRoot: f.state, RuntimeRoot: f.runtime,
		Holder: leadcanary.HolderConfig{
			Executable: "/opt/sample/holder", Cols: 120, Rows: 40,
			SandboxInputs: base + "/inputs.json", ProfileDigest: strings.Repeat("cd", 32),
		},
		Agent: leadcanary.AgentConfig{
			Kind:           leadcanary.AgentStandIn,
			Runtime:        string(leadregistry.DefaultCoordinatorProvider), // a key of the registry's runtime enum
			RuntimeVersion: "1.2.3", FooterMode: "sample mode on", Argv: []string{"/opt/sample/agent"},
		},
		Inject: true,
		Limits: leadcanary.Limits{MemoryMaxBytes: 4 << 30, TasksMax: 512, MemoryFloorBytes: 1 << 30, MemorySharePercent: 50},
		Provision: leadcanary.ProvisionConfig{
			Mirror: base + "/mirror.git", UpstreamBranch: "main", GitName: "Sample Name",
			GitEmail: "sample@example.invalid", ManagedSettings: "/opt/sample/managed.json",
			GuardHook: "/opt/sample/guard", ReplayScreens: "/opt/sample/screens",
		},
	}
	f.cfgPath = f.write(t, f.cfg)
	return f
}

// write writes cfg as the config file, mode 0600, and returns its path.
func (f canaryFixture) write(t *testing.T, cfg leadcanary.Config) string {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f.writeBytes(t, b)
}

func (f canaryFixture) writeBytes(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(f.base, "conf", "canary.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f canaryFixture) socket() string { return f.runtime + "/daemon.sock" }

// canaryApp is a test app with a fake manager that reads the system clock, so
// that a stop needs no clock driver.
func canaryApp(t *testing.T) *testApp {
	t.Helper()
	ta := newTestApp(t, nil)
	ta.fake = leadunit.NewFake(leadunit.SystemClock{})
	return ta
}

func TestVersionNamesTheTag(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--version"}); code != 0 || ta.out.String() != "ataniqd "+leaddaemon.DefaultVersion+" (build tag ataniq_canary)\n" {
		t.Fatalf("%d %q", code, ta.out.String())
	}
	// The version command is as it was.
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"version"}); code != 0 || ta.out.String() != "ataniqd "+leaddaemon.DefaultVersion+"\n" {
		t.Fatalf("%d %q", code, ta.out.String())
	}
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"--version", "x"}); code != leaddaemon.ExitUsage {
		t.Fatalf("--version x: %d", code)
	}
}

func TestUsageNamesTheCanaryCommands(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	got := ta.out.String()
	// The release text says the build is read-only and that no flag changes
	// that. A tagged build keeps the text and says, right after it, that it
	// holds only without --canary.
	override := "In this build (tag ataniq_canary) the text above holds only without --canary."
	for _, want := range []string{override, "With run --canary the daemon is not read-only", "ataniqd run --canary CONFIG", "ataniqd canary inject", "--body-file FILE", "--attach-role"} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
	if !strings.HasPrefix(got, baseUsage+"\n"+override+"\n") {
		t.Errorf("the usage does not open with the release text and then the line that limits it:\n%s", got)
	}
}

func TestRunCanaryRefusesTheRootFlags(t *testing.T) {
	f := newCanaryFixture(t)
	for _, args := range [][]string{
		{"run", "--canary", f.cfgPath, "--state-dir", f.state},
		{"run", "--canary", f.cfgPath, "--runtime-dir", f.runtime},
		{"run", "--state-dir", f.state, "--canary", f.cfgPath},
		{"run", "--canary", ""},
	} {
		ta := canaryApp(t)
		if code := ta.run(args); code != leaddaemon.ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, leaddaemon.ExitUsage)
		}
		if _, err := os.Lstat(f.socket()); err == nil {
			t.Fatalf("%v: a daemon listened", args)
		}
	}
	if entries, _ := os.ReadDir(f.state); len(entries) != 1 {
		t.Fatalf("the state root holds %d entries", len(entries))
	}
}

func TestRunCanaryRefusesAConfigThatIsNotRight(t *testing.T) {
	f := newCanaryFixture(t)
	good, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(good, &tree); err != nil {
		t.Fatal(err)
	}
	delete(tree["limits"].(map[string]any), "tasks_max")
	noLimit, _ := json.Marshal(tree)
	for name, tc := range map[string]struct {
		content []byte // nil: no file
		want    string
	}{
		"a missing limit": {noLimit, "limits.tasks_max"},
		"not a config":    {[]byte(`{"version":1}`), "invalid config"},
		"no such file":    {nil, "cannot open"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(f.base, "conf", "none.json")
			if tc.content != nil {
				path = f.writeBytes(t, tc.content)
			}
			ta := canaryApp(t)
			code := ta.run([]string{"run", "--canary", path})
			if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), tc.want) {
				t.Fatalf("exit %d, stderr %q", code, ta.err.String())
			}
			if entries, _ := os.ReadDir(f.state); len(entries) != 1 {
				t.Fatalf("the state root holds %d entries", len(entries))
			}
		})
	}
}

func TestRunCanaryRefusesTheDefaultRootAndAMissingMarker(t *testing.T) {
	f := newCanaryFixture(t)
	xdg := f.base + "/xs"
	if err := os.MkdirAll(xdg+"/ataniq", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.StateRoot = xdg + "/ataniq"
	path := f.write(t, cfg)
	ta := canaryApp(t)
	ta.env = map[string]string{"XDG_STATE_HOME": xdg}
	ta.getenv = func(k string) string { return ta.env[k] }
	if code := ta.run([]string{"run", "--canary", path}); code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "default-root") {
		t.Fatalf("exit %d, stderr %q", code, ta.err.String())
	}
	// A missing marker.
	if err := os.Remove(filepath.Join(f.state, canaryroot.MarkerName)); err != nil {
		t.Fatal(err)
	}
	ta = canaryApp(t)
	if code := ta.run([]string{"run", "--canary", f.cfgPath}); code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "no-marker") {
		t.Fatalf("exit %d, stderr %q", code, ta.err.String())
	}
	if _, err := os.Lstat(f.socket()); err == nil {
		t.Fatal("a daemon listened")
	}
}

// fakeHolder serves the control socket of a holder's runtime folder: ok for a
// ticket and a submitted result for a message.
func fakeHolder(t *testing.T, dir string) (requests func() []wire.Request) {
	t.Helper()
	ln, err := net.Listen("unix", dir+"/control.sock")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []wire.Request
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				fr, err := wire.NewReader(conn, wire.ControlRequests).Next()
				if err != nil {
					return
				}
				req, err := wire.DecodeRequest(fr.Payload)
				if err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, req)
				mu.Unlock()
				resp := wire.Response{OK: true}
				if req.Op == wire.OpInject {
					resp.Inject = &wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole}
				}
				p, err := wire.EncodeResponse(resp)
				if err == nil {
					_ = wire.Write(conn, wire.ControlResponses, wire.TypeResponse, p)
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	return func() []wire.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]wire.Request(nil), seen...)
	}
}

// canary runs `ataniqd canary ...` in process.
func canary(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	ta := newTestApp(t, nil)
	code = ta.run(append([]string{"canary"}, args...))
	return code, ta.out.String(), ta.err.String()
}

func TestCanaryCommandsEndToEnd(t *testing.T) {
	f := newCanaryFixture(t)
	ta := canaryApp(t)
	done := startApp(t, ta, []string{"--canary", f.cfgPath}, f.socket())
	cfgArg := []string{"--config", f.cfgPath}
	line := func(out string) map[string]any {
		t.Helper()
		if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
			t.Fatalf("not one line: %q", out)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		return m
	}

	// hello lists the canary operations.
	ops, err := helloOperations(context.Background(), f.socket())
	if err != nil || len(ops) != 7 {
		t.Fatalf("hello operations %v %v", ops, err)
	}

	code, out, errOut := canary(t, append([]string{"define"}, cfgArg...)...)
	if m := line(out); code != 0 || m["role"] != "throwaway-one/lead" || m["state"] != "stopped" {
		t.Fatalf("define: %d %q %q", code, out, errOut)
	}
	code, out, errOut = canary(t, append([]string{"start"}, cfgArg...)...)
	if m := line(out); code != 0 || m["epoch"] != float64(1) || m["state"] != "starting" || m["waiver"] != canaryroot.WaiverText ||
		m["unit"] != "ataniq-lead-throwaway-one.service" {
		t.Fatalf("start: %d %q %q", code, out, errOut)
	}
	// A second start of a running role: refused.
	if code, out, errOut = canary(t, append([]string{"start"}, cfgArg...)...); code != leaddaemon.ExitRefused || out != "" ||
		!strings.Contains(errOut, "refused") {
		t.Fatalf("second start: %d %q %q", code, out, errOut)
	}
	// No holder is listening yet: unavailable.
	if code, out, errOut = canary(t, "ticket", "--config", f.cfgPath, "--attach-role", "writer"); code != leaddaemon.ExitUnavailable || out != "" {
		t.Fatalf("ticket without a holder: %d %q %q", code, out, errOut)
	}
	requests := fakeHolder(t, f.holder)
	code, out, errOut = canary(t, "ticket", "--config", f.cfgPath, "--attach-role", "viewer")
	if m := line(out); code != 0 || m["ticket_file"] != f.holder+"/ticket" || m["folder"] != f.holder || m["epoch"] != float64(1) {
		t.Fatalf("ticket: %d %q %q", code, out, errOut)
	}
	if strings.Contains(out, "ticket\":\"") {
		t.Fatal("a ticket is in the output")
	}
	body := filepath.Join(f.base, "body.txt")
	if err := os.WriteFile(body, []byte("canary-token: sample-token-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = canary(t, "inject", "--config", f.cfgPath, "--message-id", "msg-1", "--body-file", body)
	m := line(out)
	res, _ := m["result"].(map[string]any)
	if code != 0 || m["message_id"] != "msg-1" || m["epoch"] != float64(1) || res["outcome"] != "submitted" || res["written"] != "whole" {
		t.Fatalf("inject: %d %q %q", code, out, errOut)
	}
	var injects []wire.Request
	for _, r := range requests() {
		if r.Op == wire.OpInject {
			injects = append(injects, r)
		}
	}
	if len(injects) != 1 || injects[0].MessageID != "msg-1" || !strings.Contains(injects[0].Text, "canary-token: sample-token-0123456789") {
		t.Fatalf("inject requests %+v", injects)
	}
	if code, out, errOut = canary(t, "inject", "--config", f.cfgPath, "--message-id", "msg-2", "--body-file", body); code != leaddaemon.ExitRefused || out != "" {
		t.Fatalf("second inject: %d %q %q", code, out, errOut)
	}
	code, out, errOut = canary(t, append([]string{"stop"}, cfgArg...)...)
	if m := line(out); code != 0 || m["state"] != "stopped" || m["proof"] == "" {
		t.Fatalf("stop: %d %q %q", code, out, errOut)
	}

	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	// No daemon any more.
	if code, _, errOut = canary(t, append([]string{"define"}, cfgArg...)...); code != leaddaemon.ExitNoDaemon || !strings.Contains(errOut, "no daemon") {
		t.Fatalf("no daemon: %d %q", code, errOut)
	}
}

func TestCanaryCommandUsage(t *testing.T) {
	f := newCanaryFixture(t)
	for _, args := range [][]string{
		nil,
		{"nothing"},
		{"define"},
		{"start", "--config", ""},
		{"ticket", "--config", f.cfgPath},
		{"ticket", "--config", f.cfgPath, "--attach-role", "admin"},
		{"inject", "--config", f.cfgPath},
		{"inject", "--config", f.cfgPath, "--message-id", "m"},
		{"inject", "--config", f.cfgPath, "--message-id", "m", "--body-file", f.base + "/none.txt"},
		{"inject", "--config", f.cfgPath, "--message-id", "m", "--body-file", f.base},
		{"define", "--config", f.cfgPath, "extra"},
		{"define", "--config", f.cfgPath, "--attach-role", "writer"},
		{"stop", "--config", f.cfgPath, "--message-id", "m"},
	} {
		if code, out, _ := canary(t, args...); code != leaddaemon.ExitUsage || out != "" {
			t.Errorf("%v: exit %d, stdout %q", args, code, out)
		}
	}
	// A config that does not read is the config's exit code.
	if code, _, errOut := canary(t, "define", "--config", f.base+"/none.json"); code != leaddaemon.ExitConfig {
		t.Errorf("no config: exit %d %q", code, errOut)
	}
}

func helloOperations(ctx context.Context, socket string) ([]string, error) {
	c, err := clientapi.Dial(ctx, socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	h, err := c.Hello(ctx)
	return h.Operations, err
}
