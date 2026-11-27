//go:build linux && ataniq_canary

package main

import (
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/instanceid"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/injectbridge"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
)

// The flags canary mode adds are these five, and the scratch command line is
// the launcher's still.
func TestCanaryFlagSet(t *testing.T) {
	want := "canary expected-mode inject record-dir sandbox-inputs"
	if got := strings.Join(canaryFlagNames(), " "); got != want {
		t.Errorf("canary mode adds %q, want %q", got, want)
	}
}

// The rules of the two modes: exactly one of --scratch and --canary, the flags
// of canary mode only with --canary, and --canary needs --sandbox-inputs. Each
// refusal exits 2 and leaves nothing.
func TestCanaryFlagRules(t *testing.T) {
	tests := []struct {
		name  string
		setup func(c *canaryRig, a *argSpec)
		want  string
	}{
		{"--canary and --scratch", func(c *canaryRig, a *argSpec) { a.scratch = true }, "cannot be given together"},
		{"neither", func(c *canaryRig, a *argSpec) { a.extraBefore = nil; a.drop("sandbox-inputs").drop("expected-mode") }, "neither --scratch nor --canary"},
		{"--inject with --scratch", func(c *canaryRig, a *argSpec) {
			a.scratch, a.extraBefore = true, []string{"--inject"}
			a.drop("sandbox-inputs").drop("expected-mode")
		}, "need --canary"},
		{"--inject alone", func(c *canaryRig, a *argSpec) {
			a.extraBefore = []string{"--inject"}
			a.drop("sandbox-inputs").drop("expected-mode")
		}, "need --canary"},
		{"--sandbox-inputs with --scratch", func(c *canaryRig, a *argSpec) {
			a.scratch, a.extraBefore = true, nil
			a.drop("expected-mode")
		}, "need --canary"},
		{"--expected-mode with --scratch", func(c *canaryRig, a *argSpec) {
			a.scratch, a.extraBefore = true, nil
			a.drop("sandbox-inputs")
		}, "need --canary"},
		{"--canary without --sandbox-inputs", func(c *canaryRig, a *argSpec) { a.drop("sandbox-inputs") }, "--canary needs --sandbox-inputs"},
		{"an empty --sandbox-inputs", func(c *canaryRig, a *argSpec) { a.set("sandbox-inputs", "") }, "--canary needs --sandbox-inputs"},
		{"a relative --sandbox-inputs", func(c *canaryRig, a *argSpec) { a.set("sandbox-inputs", "inputs.json") }, "--sandbox-inputs must be an absolute path"},
		{"--sandbox-inputs twice", func(c *canaryRig, a *argSpec) {
			a.flags = append(a.flags, [2]string{"sandbox-inputs", c.inputsPath})
		}, "--sandbox-inputs was given twice"},
		{"an --expected-mode with a control byte", func(c *canaryRig, a *argSpec) { a.set("expected-mode", "a\x07b") }, "--expected-mode"},
		{"an --expected-mode too long", func(c *canaryRig, a *argSpec) { a.set("expected-mode", strings.Repeat("m", 257)) }, "--expected-mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			a := c.spec("")
			tc.setup(c, a)
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
			if c.systemsMade() != 0 || len(c.launches()) != 0 {
				t.Error("a refused command line reached the start sequence")
			}
		})
	}
}

// The help of a tagged build describes canary mode and its exit status.
func TestCanaryHelp(t *testing.T) {
	c := newCanaryRig(t)
	if code := c.runNow([]string{"--help"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"--canary", "--inject", "--sandbox-inputs FILE", "--expected-mode TEXT", "--record-dir DIR", "Exit status 4"} {
		if !strings.Contains(c.errbuf.String(), want) {
			t.Errorf("the help does not say %q", want)
		}
	}
}

// The refusals the holder owes before the start sequence: each exits 2 with
// the role lock held and released again, opens no event log, writes no state
// file and no mark, and never reaches the system or the launcher.
func TestCanaryRefusalsBeforeTheStartSequence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(c *canaryRig, a *argSpec)
		want  string
	}{
		{"canary: false", func(c *canaryRig, a *argSpec) { c.in.Canary = false; c.writeInputs() }, `"canary": false`},
		{"a layout role that is not the project's lead", func(c *canaryRig, a *argSpec) {
			c.in.LayoutRole = canaryProject + "-other"
			c.writeInputs()
		}, "layout role"},
		{"a --role of another project", func(c *canaryRig, a *argSpec) { a.set("role", "throwaway-two/lead") }, "--role is not the lead"},
		{"a --role that is not a lead", func(c *canaryRig, a *argSpec) { a.set("role", canaryProject+"/coord/x") }, "--role is not the lead"},
		{"a --role that is no role", func(c *canaryRig, a *argSpec) { a.set("role", "scratch/t") }, "--role is not the lead"},
		{"a project without the reserved prefix", func(c *canaryRig, a *argSpec) {
			c.in.Project, c.in.LayoutRole = "plain", "plain-lead"
			c.writeInputs()
			a.set("role", "plain/lead")
		}, "not-throwaway"},
		{"a state root without the marker", func(c *canaryRig, a *argSpec) {
			os.Remove(filepath.Join(c.stateRoot, canaryroot.MarkerName))
		}, "no-marker"},
		{"a --role-lock that is not the role's", func(c *canaryRig, a *argSpec) {
			a.set("role-lock", filepath.Join(c.stateRoot, "roles", "other.lock"))
		}, "--role-lock is not the lock of the role"},
		{"a --state-dir outside the state root", func(c *canaryRig, a *argSpec) {
			d := filepath.Join(c.base, "elsewhere")
			os.Mkdir(d, 0o700)
			a.set("state-dir", d)
		}, "--state-dir does not lie under the state root"},
		{"a --state-dir that is the state root", func(c *canaryRig, a *argSpec) { a.set("state-dir", c.stateRoot) }, "--state-dir does not lie under the state root"},
		{"a --runtime-dir outside the runtime root", func(c *canaryRig, a *argSpec) {
			d := filepath.Join(c.base, "elsewhere")
			os.Mkdir(d, 0o700)
			a.set("runtime-dir", d)
		}, "--runtime-dir does not lie under the runtime root"},
		{"a --runtime-dir under the state root", func(c *canaryRig, a *argSpec) {
			d := filepath.Join(c.stateRoot, "rt")
			os.Mkdir(d, 0o700)
			a.set("runtime-dir", d)
		}, "--runtime-dir does not lie under the runtime root"},
		{"a --state-dir reached through a link", func(c *canaryRig, a *argSpec) {
			outside := filepath.Join(c.base, "outside")
			os.MkdirAll(filepath.Join(outside, "holder"), 0o700)
			if err := os.Symlink(outside, filepath.Join(c.stateRoot, "link")); err != nil {
				t.Fatal(err)
			}
			a.set("state-dir", filepath.Join(c.stateRoot, "link", "holder"))
		}, "--state-dir does not lie under the state root"},
		{"an inputs file with a loose mode", func(c *canaryRig, a *argSpec) { os.Chmod(c.inputsPath, 0o644) }, "--sandbox-inputs cannot be used"},
		{"no inputs file", func(c *canaryRig, a *argSpec) { os.Remove(c.inputsPath) }, "--sandbox-inputs cannot be used"},
		{"an inputs file that names a per-epoch field", func(c *canaryRig, a *argSpec) {
			b, _ := os.ReadFile(c.inputsPath)
			b = []byte(strings.Replace(string(b), `{"version":1,`, `{"version":1,"no_lead_has_run":true,`, 1))
			os.Remove(c.inputsPath)
			if err := os.WriteFile(c.inputsPath, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "--sandbox-inputs cannot be used"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			a := c.spec("")
			tc.setup(c, a)
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), "refused: ") || !strings.Contains(c.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
			if c.systemsMade() != 0 || len(c.launches()) != 0 {
				t.Error("a refused start reached the start sequence")
			}
			if !c.lockFree() {
				t.Error("the role lock is still held")
			}
		})
	}
}

// Started with an environment like a unit's, with no HOME and no XDG_*
// variable, canary mode still refuses the default roots: the root check works
// them out from the account. The roots are only named here; nothing is made in
// them.
func TestCanaryUnitEnvironmentStillRefusesTheDefaultRoots(t *testing.T) {
	uid := strconv.Itoa(os.Geteuid())
	u, err := user.LookupId(uid)
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		t.Skipf("the account of this user cannot be looked up: %v", err)
	}
	none := func(string) string { return "" }
	tests := []struct {
		name  string
		roots func(c *canaryRig)
	}{
		{"the default state folder", func(c *canaryRig) { c.in.StateRoot = filepath.Join(u.HomeDir, ".local", "state", "ataniq") }},
		{"a folder in the default state folder", func(c *canaryRig) {
			c.in.StateRoot = filepath.Join(u.HomeDir, ".local", "state", "ataniq", "canary")
		}},
		{"the default runtime folder", func(c *canaryRig) { c.in.RuntimeRoot = filepath.Join("/run/user", uid, "ataniq") }},
		{"a folder that contains the default runtime folder", func(c *canaryRig) { c.in.RuntimeRoot = filepath.Join("/run/user", uid) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			c.getenv = none
			tc.roots(c)
			c.writeInputs()
			a := c.spec("")
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), "canary root refused (default-root)") {
				t.Errorf("stderr %q is not the refusal of a default root", c.errbuf.String())
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
			if c.systemsMade() != 0 || len(c.launches()) != 0 {
				t.Error("a refused start reached the start sequence")
			}
		})
	}
	// With the same bare environment the canary's own roots are accepted: the
	// refusal above is the roots', not the environment's.
	c := newCanaryRig(t)
	c.getenv = none
	if code := c.runNow(c.spec("").build()); code != exitSandbox || !strings.Contains(c.errbuf.String(), "verify-plan") {
		t.Errorf("with its own roots the start got to exit %d, want the refusal of the pinned zeros at verify-plan; stderr: %s", code, c.errbuf.String())
	}
}

func hexOSC(payload string) string {
	return hex.EncodeToString([]byte("\x1b]7501;" + payload + "\x1b\\"))
}

// Canary mode with the fake launcher: the start sequence runs in its order,
// the hook is in the session's configuration before the child starts, the
// child has no controlling terminal, the state file carries the waiver, the
// event is in the log, and the holder serves.
func TestCanaryStartServes(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	report := filepath.Join(c.agentFiles, "report.json")
	c.script = "report " + report + "; raw; print " + hexOSC("?") + "; echo"
	c.start(c.spec(digest).build())
	c.waitReady()

	// The whole sequence ran, in the harness's order.
	want := "CheckHost ToolVersions CheckOwnClone PrepareMountPoints CheckMountPoints DeriveResolv MakeBound WriteResolv " +
		"ResolveSources OpenReport NewCanary WriteSpec CheckMountPoints ResolveSources CheckBindSources ReadLine Trailing " +
		"Channel.Close Bound.Close"
	if got := strings.Join(c.sys.callList(), " "); got != want {
		t.Errorf("the system was called\n%s\nwant\n%s", got, want)
	}

	// The launcher: one session, of the plan's argument vector, with an empty
	// environment, the hook installed and no controlling terminal.
	ls := c.launches()
	if len(ls) != 1 {
		t.Fatalf("%d launches", len(ls))
	}
	l := ls[0]
	if len(l.Argv) == 0 || l.Argv[0] != c.in.Executables.Pasta {
		t.Errorf("the session was not given the plan's argument vector: %q", l.Argv)
	}
	if strings.Join(l.Argv[len(l.Argv)-2:], " ") != "/ataniq/agent/bin/stand-in --sample" {
		t.Errorf("the plan does not end with the agent's command line: %q", l.Argv[len(l.Argv)-2:])
	}
	if l.Env != nil {
		t.Errorf("the plan's first process got an environment: %q", l.Env)
	}
	if !l.PTY.NoControllingTerminal {
		t.Error("the session's terminal settings do not ask for no controlling terminal")
	}
	if len(l.OSC) != 1 || l.OSC[0].Cmd != injectbridge.OSCCommand || l.OSC[0].Fn == nil {
		t.Errorf("the hook of the status protocol was not in the configuration the session started with: %+v", l.OSC)
	}
	if l.HeldRoleLock == nil || l.Role != canaryRoleText || l.Epoch != strconv.Itoa(testEpoch) || l.Dir != "/" {
		t.Errorf("the session's configuration: role %q epoch %q dir %q lock %v", l.Role, l.Epoch, l.Dir, l.HeldRoleLock)
	}
	// The option reached the terminal host: the child leads a session and has
	// the terminal, but not as its controlling terminal.
	poll(t, func() string { return "the agent's report" }, func() bool { _, err := os.Stat(report); return err == nil })
	rep, err := fakeagent.ReadReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Ctty || !rep.StdinTTY || rep.Sid != rep.Pid {
		t.Errorf("the child: controlling terminal %v, stdin a terminal %v, session %d, pid %d", rep.Ctty, rep.StdinTTY, rep.Sid, rep.Pid)
	}

	// The fields no file can set.
	seen := c.sys.inputs()
	if len(seen) != 1 {
		t.Fatalf("%d starts", len(seen))
	}
	in := seen[0]
	if strings.Join(in.AgentArgv, " ") != "/ataniq/agent/bin/stand-in --sample" {
		t.Errorf("AgentArgv %q", in.AgentArgv)
	}
	if len(in.AgentEnv) != 1 || in.AgentEnv[0] != (profile.EnvVar{Name: "SAMPLE_FLAG", Value: "on"}) {
		t.Errorf("AgentEnv %+v", in.AgentEnv)
	}
	if in.ExpectedDigest != digest {
		t.Errorf("ExpectedDigest %q, want the pin %q", in.ExpectedDigest, digest)
	}
	wantPaths := filepath.Join(c.runtime, serve.AttachSocket) + " " + filepath.Join(c.runtime, serve.ControlSocket)
	if strings.Join(in.HolderPaths, " ") != wantPaths {
		t.Errorf("HolderPaths %q, want %q", in.HolderPaths, wantPaths)
	}
	// Assertion 9 was asked with the layout role, and the holder's own lock
	// answered.
	c.sys.mu.Lock()
	asked := strings.Join(c.sys.asked, " ")
	c.sys.mu.Unlock()
	if asked != canaryLayoutRole {
		t.Errorf("the role lock was asked about %q, want %q", asked, canaryLayoutRole)
	}

	// The state file: the waiver, in full.
	st := c.readState()
	if !st.Canary || st.Scratch || !st.SandboxStarted || st.Waiver != canaryroot.WaiverText ||
		st.ProfileVersion != profile.Version || st.ProfileDigest != digest || st.Role != canaryRoleText || st.Pid != os.Getpid() {
		t.Errorf("state file %+v", st)
	}
	if got := mode(t, filepath.Join(c.state, StateFileName)); got != 0o600 {
		t.Errorf("the state file has mode %v", got)
	}
	// The event log: the code, not the text.
	log := string(c.logBytes())
	if !strings.Contains(log, `"state":"sandbox-started"`) || !strings.Contains(log, `"reason":"assertion-10-waived"`) {
		t.Errorf("the event log has no sandbox-started entry with the waiver's code: %s", log)
	}
	if strings.Contains(log, canaryroot.WaiverText) {
		t.Error("the event log holds the waiver's text")
	}
	if !strings.Contains(c.errbuf.String(), canaryroot.WaiverText) {
		t.Errorf("stderr does not say the waiver: %s", c.errbuf.String())
	}

	// It serves: the query the agent sent at its start was answered by the
	// hook, and the agent read the reply.
	v := c.attach(wire.RoleViewer)
	v.waitText("RX:" + hex.EncodeToString([]byte(osc7501.QueryReply)))
	if s := c.status(); s.Lead != canaryRoleText || s.Epoch != testEpoch {
		t.Errorf("status %+v", s)
	}
	// Without --inject the gate is closed.
	resp := c.mustControl(wire.Request{Op: wire.OpInject, Epoch: testEpoch, MessageID: "m1", Text: "[ataniq-msg id=m1 from=sample]\nhello"})
	if resp.OK || resp.Code != wire.CodeDisabled {
		t.Errorf("inject without --inject: %+v, want %s", resp, wire.CodeDisabled)
	}

	if resp := c.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := c.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
	if !c.lockFree() {
		t.Error("the role lock is still held after the holder ended")
	}
	if rec := c.readFinal(); rec.Role != canaryRoleText {
		t.Errorf("final record %+v", rec)
	}
}

// With --inject the service has a deliverer: an inject request is answered ok
// with a result, not disabled. (The sample runtime is not one the injection
// library has tested, so the result is a refusal that wrote nothing.) The
// deliverer copies the 7501 counters before the delivery, here with a copy
// interval that never comes.
func TestCanaryInjectGivesTheServiceADeliverer(t *testing.T) {
	c := newCanaryRig(t)
	c.interval = time.Hour
	digest := c.digest()
	c.script = "raw; print " + hexOSC("?") + "; echo"
	c.start(c.spec(digest, "--inject").build())
	c.waitReady()
	v := c.attach(wire.RoleViewer)
	v.waitText("RX:" + hex.EncodeToString([]byte(osc7501.QueryReply)))
	if n := c.status().Counters[counterQueries]; n != 0 {
		t.Fatalf("the query was counted before any copy: %d", n)
	}
	resp := c.mustControl(wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: "cmd-1", MessageID: "m1", Text: "[ataniq-msg id=m1 from=sample]\nhello"})
	if !resp.OK || resp.Inject == nil {
		t.Fatalf("inject with --inject: %+v, want an ok answer with a result", resp)
	}
	if resp.Inject.Written != wire.InjectWrittenNothing || resp.Inject.Outcome == "" {
		t.Errorf("the result of a delivery to an untested runtime: %+v", resp.Inject)
	}
	if n := c.status().Counters[counterQueries]; n != 1 {
		t.Errorf("the deliverer did not copy the counters before the delivery: %s is %d, want 1", counterQueries, n)
	}
	if resp := c.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := c.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
}

// The three counters follow the tracker: the increases are copied at each
// interval, shown in the status answer, and after the child ends they are on
// the final record.
func TestCanaryCounters(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.script = "raw; print " + hexOSC("?") + "; print " + hexOSC("state=idle") + "; print " + hexOSC("?") +
		"; print " + hexOSC("not a report") + "; echo"
	c.start(c.spec(digest).build())
	c.waitReady()
	want := map[string]uint64{counterQueries: 2, counterReports: 1, counterInvalid: 1}
	var got map[string]uint64
	poll(t, func() string {
		return "the counters " + strings.TrimSpace(strings.Join([]string{strconv.Itoa(int(got[counterQueries])), strconv.Itoa(int(got[counterReports])), strconv.Itoa(int(got[counterInvalid]))}, " "))
	}, func() bool {
		got = c.status().Counters
		for k, v := range want {
			if got[k] != v {
				return false
			}
		}
		return true
	})
	// They only add: a later copy does not count anything twice.
	time.Sleep(5 * c.interval)
	if got := c.status().Counters; got[counterQueries] != 2 || got[counterReports] != 1 || got[counterInvalid] != 1 {
		t.Errorf("the counters changed with no new payload: %v", got)
	}
	if resp := c.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := c.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
	rec := c.readFinal()
	for k, v := range want {
		if rec.Counters[k] != v {
			t.Errorf("final record: %s is %d, want %d", k, rec.Counters[k], v)
		}
	}
}

func TestCanaryStartRequiresInstanceIdentity(t *testing.T) {
	c := newCanaryRig(t)
	if err := os.Remove(filepath.Join(c.stateRoot, instanceid.FileName)); err != nil {
		t.Fatal(err)
	}
	if code := c.runNow(c.spec("").build()); code != exitSandbox || !strings.Contains(c.errbuf.String(), instanceid.ErrMissing.Error()) {
		t.Fatalf("missing identity: exit %d, stderr %q", code, c.errbuf.String())
	}
	if calls := c.sys.callList(); len(calls) != 0 || len(c.launches()) != 0 {
		t.Fatalf("start continued after identity refusal: %v", calls)
	}
}

// A failed start leaves no agent and no claim: the child, when one was
// launched, is stopped and reaped, the session is closed, the role lock is
// free, the state file is gone, the exit status is 4 and the event log has the
// refusal.
func TestCanaryFailedStartLeavesNoAgentAndNoClaim(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(c *canaryRig)
		want     string
		launches int
	}{
		{"a refusal before any step", func(c *canaryRig) {
			// The system's environment puts a default state folder inside the
			// state root, so the sequence's own root check refuses.
			c.sys.env = map[string]string{"XDG_STATE_HOME": filepath.Join(c.stateRoot, "xdg")}
		}, "before any step", 0},
		{"a failed step before the launch", func(c *canaryRig) { c.sys.fail["CheckOwnClone"] = errors.New("sample failure") }, "at the step own-clone", 0},
		{"a launcher that cannot start the session", func(c *canaryRig) { c.startErr = syscall.ENOENT }, "at the step launch", 1},
		{"a failed step after the launch", func(c *canaryRig) { c.sys.fail["ReadLine"] = errors.New("sample failure") }, "at the step read-report", 1},
		{"a failed judgement of the probe's report", func(c *canaryRig) { c.sys.probeStatus[2] = assert.StatusFailed }, "at the step outside-checks", 1},
		{"a failed judgement of the outside checks", func(c *canaryRig) { c.sys.linger = false }, "at the step outside-checks", 1},
		{"trailing bytes after the report", func(c *canaryRig) { c.sys.fail["Trailing"] = errors.New("sample failure") }, "at the step report-trailing", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			c.stopGrace = 200 * time.Millisecond
			digest := c.digest()
			tc.setup(c)
			code := c.runNow(c.spec(digest, "--inject").build())
			if code != exitSandbox {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitSandbox, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), tc.want) || !strings.Contains(c.errbuf.String(), "no agent is left") {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			if strings.Contains(c.errbuf.String(), "ready:") {
				t.Error("a failed start said ready")
			}
			if n := len(c.launches()); n != tc.launches {
				t.Errorf("%d launches, want %d", n, tc.launches)
			}
			c.noAgentNoClaim()
			log := string(c.logBytes())
			if !strings.Contains(log, `"event":"refusal"`) || !strings.Contains(log, `"reason":"sandbox-start-failed"`) {
				t.Errorf("the event log has no refusal of the start: %s", log)
			}
			if strings.Contains(log, "sandbox-started") {
				t.Errorf("the event log says the sandbox started: %s", log)
			}
		})
	}
}

// A stop signal during the start sequence ends it: the launched child is
// stopped, nothing claims a running lead and the exit status is 2.
func TestCanaryStopSignalDuringTheStart(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	c.sys.block = true
	c.start(c.spec(digest).build())
	poll(t, func() string { return "the wait for the report line" }, func() bool { return c.sys.called("ReadLine") == 1 })
	c.sigc <- syscall.SIGHUP // changes nothing
	c.sigc <- syscall.SIGTERM
	if code := c.wait(); code != exitRefuse {
		t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
	}
	if !strings.Contains(c.errbuf.String(), "stopped by SIGTERM during the start of the sandbox") {
		t.Errorf("stderr: %s", c.errbuf.String())
	}
	if len(c.launches()) != 1 {
		t.Errorf("%d launches", len(c.launches()))
	}
	c.noAgentNoClaim()
	if log := string(c.logBytes()); !strings.Contains(log, `"reason":"signal-during-start"`) {
		t.Errorf("the event log: %s", log)
	}
}

// The per-epoch fields. The first start finds no mark: NoLeadHasRun is true,
// the mount points are made and the mark is written. The second finds it:
// NoLeadHasRun is false, and the start goes on only because the holder is
// alone in its control group (PreviousInstanceStopped).
func TestCanaryPerEpochFields(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	mark := filepath.Join(c.state, leadHasRunMark)
	if _, err := os.Lstat(mark); err == nil {
		t.Fatal("the mark is there before the first start")
	}
	c.fakeCgroup(true)

	runOnce := func() sandboxstart.Inputs {
		t.Helper()
		c.sys.reset()
		c.errbuf = &syncBuf{}
		c.done = make(chan int, 1)
		c.start(c.spec(digest).build())
		c.waitReady()
		if resp := c.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
			t.Fatalf("stop: %+v", resp)
		}
		if code := c.wait(); code != exitOK {
			t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
		}
		seen := c.sys.inputs()
		if len(seen) != 1 {
			t.Fatalf("%d starts", len(seen))
		}
		return seen[0]
	}

	first := runOnce()
	if !first.NoLeadHasRun || !first.PreviousInstanceStopped {
		t.Errorf("first start: NoLeadHasRun %v, PreviousInstanceStopped %v", first.NoLeadHasRun, first.PreviousInstanceStopped)
	}
	if c.sys.called("PrepareMountPoints") != 1 {
		t.Error("the first start did not make the mount points")
	}
	if got := mode(t, mark); got != 0o600 {
		t.Errorf("the mark has mode %v, want a regular file 0600", got)
	}

	second := runOnce()
	if second.NoLeadHasRun || !second.PreviousInstanceStopped {
		t.Errorf("second start: NoLeadHasRun %v, PreviousInstanceStopped %v", second.NoLeadHasRun, second.PreviousInstanceStopped)
	}
	if c.sys.called("PrepareMountPoints") != 0 {
		t.Error("the second start made the mount points again")
	}
	if got := mode(t, mark); got != 0o600 {
		t.Errorf("the mark has mode %v after the second start", got)
	}

	// A third start that shares its control group with another process proves
	// nothing about an earlier instance: the walk of assertion 7 is then not
	// checked, and the start is refused.
	c.fakeCgroup(false)
	c.sys.reset()
	c.errbuf = &syncBuf{}
	if code := c.runNow(c.spec(digest).build()); code != exitSandbox {
		t.Fatalf("exit %d, want %d; stderr: %s", code, exitSandbox, c.errbuf.String())
	}
	if seen := c.sys.inputs(); len(seen) != 1 || seen[0].NoLeadHasRun || seen[0].PreviousInstanceStopped {
		t.Errorf("third start: %+v", seen)
	}
	if !strings.Contains(c.errbuf.String(), "at the step outside-checks") {
		t.Errorf("stderr: %s", c.errbuf.String())
	}
	c.noAgentNoClaim()
}

// The mark is made exclusively and by the folder's handle: anything already at
// its name is the mark of an earlier start, and a link is never followed.
func TestMarkLeadHasRun(t *testing.T) {
	dir := t.TempDir()
	if first, err := markLeadHasRun(dir); err != nil || !first {
		t.Fatalf("the first mark: %v %v", first, err)
	}
	if first, err := markLeadHasRun(dir); err != nil || first {
		t.Fatalf("the second mark: %v %v", first, err)
	}
	linked := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Symlink(target, filepath.Join(linked, leadHasRunMark)); err != nil {
		t.Fatal(err)
	}
	if first, err := markLeadHasRun(linked); err != nil || first {
		t.Errorf("a link at the mark's name: %v %v", first, err)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("the link was followed")
	}
	if _, err := markLeadHasRun(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing folder is not an error")
	}
}

// The control-group check: alone only when the group lists this process and
// nothing else and has no child group; anything unreadable proves nothing.
func TestAloneInControlGroup(t *testing.T) {
	self := os.Getpid()
	build := func(t *testing.T, cgroupLine, procs string, child bool) (string, string) {
		root := t.TempDir()
		proc, cg := filepath.Join(root, "proc"), filepath.Join(root, "cg")
		os.MkdirAll(filepath.Join(proc, "self"), 0o755)
		if cgroupLine != "" {
			os.WriteFile(filepath.Join(proc, "self", "cgroup"), []byte(cgroupLine), 0o644)
		}
		dir := filepath.Join(cg, "unit")
		os.MkdirAll(dir, 0o755)
		if procs != "" {
			os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(procs), 0o644)
		}
		if child {
			os.Mkdir(filepath.Join(dir, "sub"), 0o755)
		}
		return proc, cg
	}
	me := strconv.Itoa(self) + "\n"
	tests := []struct {
		name, line, procs string
		child, want       bool
	}{
		{"alone", "0::/unit\n", me, false, true},
		{"alone, after a v1 line", "1:name=x:/y\n0::/unit\n", me, false, true},
		{"another process", "0::/unit\n", me + "1\n", false, false},
		{"only another process", "0::/unit\n", "1\n", false, false},
		{"a child group", "0::/unit\n", me, true, false},
		{"no v2 line", "1:name=x:/unit\n", me, false, false},
		{"a deleted group", "0::/unit (deleted)\n", me, false, false},
		{"a relative group", "0::unit\n", me, false, false},
		{"no cgroup file", "", me, false, false},
		{"no procs file", "0::/unit\n", "", false, false},
		{"an unreadable procs line", "0::/unit\n", "x\n", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proc, cg := build(t, tc.line, tc.procs, tc.child)
			if got := aloneInControlGroup(proc, cg, self); got != tc.want {
				t.Errorf("alone %v, want %v", got, tc.want)
			}
		})
	}
}

// The holder's answer to assertion 9: only for the layout role, only while the
// lock file at the path is the file it has open.
func TestHeldRoleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lead.lock")
	f, err := session.TakeRoleLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l := heldRoleLock{fd: int(f.Fd()), path: path, layoutRole: canaryLayoutRole}
	if held, err := l.Held(canaryLayoutRole); err != nil || !held {
		t.Errorf("the layout role: %v %v", held, err)
	}
	if held, err := l.Held(canaryRoleText); err != nil || held {
		t.Errorf("the daemon's role text: %v %v", held, err)
	}
	// Still held by this holder after the question, and by no one else.
	if _, err := session.TakeRoleLock(path); !errors.Is(err, session.ErrRoleLocked) {
		t.Errorf("a second taker: %v", err)
	}
	// Another file at the path is not the lock that is held.
	os.Remove(path)
	if held, err := l.Held(canaryLayoutRole); err != nil || held {
		t.Errorf("a removed lock file: %v %v", held, err)
	}
	os.WriteFile(path, nil, 0o600)
	if held, err := l.Held(canaryLayoutRole); err != nil || held {
		t.Errorf("another lock file: %v %v", held, err)
	}
}

// The adapter's Stop relies on the session's grace being the stop rule's.
func TestSessionGraceIsTheStopRules(t *testing.T) {
	if session.DefaultStopGrace != assert.StopTermWait {
		t.Errorf("the session's grace is %v and the stop rule's %v", session.DefaultStopGrace, assert.StopTermWait)
	}
}

// A reason on stderr is one bounded line of plain text.
func TestDiagnostic(t *testing.T) {
	if got := diagnostic("a\nb\x1b[1m\x7f"); got != "a?b?[1m?" {
		t.Errorf("%q", got)
	}
	if got := diagnostic(strings.Repeat("x", 3*maxDiagnostic)); len(got) > maxDiagnostic+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("a long reason is %d bytes", len(got))
	}
}

// Scratch mode in a tagged build never reaches canary mode's seams.
func TestScratchModeInATaggedBuildIsScratchMode(t *testing.T) {
	c := newCanaryRig(t)
	a := c.rig.spec("say READY; wait")
	c.start(a.build())
	c.waitReady()
	if st := c.readState(); !st.Scratch || st.Canary || st.Waiver != "" || st.SandboxStarted {
		t.Errorf("state file %+v", st)
	}
	resp := c.mustControl(wire.Request{Op: wire.OpInject, Epoch: testEpoch, MessageID: "m1", Text: "[ataniq-msg id=m1 from=sample]\nhello"})
	if resp.OK || resp.Code != wire.CodeDisabled {
		t.Errorf("inject in scratch mode: %+v", resp)
	}
	if c.systemsMade() != 0 || len(c.launches()) != 0 {
		t.Error("scratch mode reached the start sequence")
	}
	if _, err := os.Lstat(filepath.Join(c.state, leadHasRunMark)); err == nil {
		t.Error("scratch mode wrote the mark")
	}
	if resp := c.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := c.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
}

// The project and the names of a scratch lead under test: the reserved prefix
// of a scratch place, whose state root carries the scratch root marker.
const (
	scratchProject    = "scratch-alpha"
	scratchLayoutRole = "scratch-alpha-lead"
	scratchRoleText   = "scratch-alpha/lead"
)

// newScratchRig is a canary rig whose lead is a scratch lead: the project, the
// layout's folders and the role lock are the scratch lead's, and the state root
// carries the scratch root marker in place of the canary marker.
func newScratchRig(t *testing.T) *canaryRig {
	t.Helper()
	c := newCanaryRig(t)
	if err := os.Remove(filepath.Join(c.stateRoot, canaryroot.MarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := canaryroot.WriteScratchMarker(c.stateRoot); err != nil {
		t.Fatal(err)
	}
	lead := filepath.Join(c.stateRoot, "leads", scratchLayoutRole)
	c.state, c.tree = filepath.Join(lead, "holder"), filepath.Join(lead, "tree")
	c.runtime = filepath.Join(c.runRoot, "leads", scratchLayoutRole)
	c.roleLock = filepath.Join(c.stateRoot, "roles", scratchProject, "lead.lock")
	for _, d := range []string{c.state, c.tree, c.runtime, filepath.Dir(c.roleLock)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c.in.Project, c.in.LayoutRole = scratchProject, scratchLayoutRole
	c.writeInputs()
	return c
}

// scratchSpec is the command line of a scratch lead's holder: the canary's,
// with the scratch lead's role.
func (c *canaryRig) scratchSpec(digest string, bools ...string) *argSpec {
	return c.spec(digest, bools...).set("role", scratchRoleText)
}

// scratchDigest is canaryRig.digest for the scratch lead's command line: a
// start pinned to sixty-four zeros is refused at the step verify-plan, before
// anything is launched, and the refusal names the real digest.
func (c *canaryRig) scratchDigest() string {
	c.t.Helper()
	if code := c.runNow(c.scratchSpec("").build()); code != exitSandbox {
		c.t.Fatalf("the probing start exited %d; stderr: %s", code, c.errbuf.String())
	}
	m := pinnedDigest.FindStringSubmatch(c.errbuf.String())
	if m == nil {
		c.t.Fatalf("the probing start does not name the digest: %s", c.errbuf.String())
	}
	if n := len(c.launches()); n != 0 {
		c.t.Fatalf("the probing start launched %d times", n)
	}
	ents, err := os.ReadDir(c.state)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, e := range ents {
		os.RemoveAll(filepath.Join(c.state, e.Name()))
	}
	c.errbuf = &syncBuf{}
	c.sys.reset()
	return m[1]
}

// A scratch lead passes the root check on a root with the scratch root marker,
// goes through the same start sequence as the canary and serves, with the
// waiver in its state file.
func TestCanaryScratchLeadStartServes(t *testing.T) {
	c := newScratchRig(t)
	digest := c.scratchDigest()
	c.start(c.scratchSpec(digest).build())
	c.waitReady()
	if s := c.status(); s.Lead != scratchRoleText || s.Epoch != testEpoch {
		t.Errorf("status %+v", s)
	}
	if st := c.readState(); !st.Canary || st.Scratch || !st.SandboxStarted || st.Waiver != canaryroot.WaiverText || st.Role != scratchRoleText {
		t.Errorf("state file %+v", st)
	}
	c.stopAndWait()
	if !c.lockFree() {
		t.Error("the role lock is still held after the holder ended")
	}
}

// The refusals of the place gate for a scratch lead come before the role lock
// is taken and before anything is written, as the canary's do: the root marker
// missing, the canary marker in its place or beside it, and a throwaway lead
// on a scratch root.
func TestCanaryScratchRefusalsBeforeTheStartSequence(t *testing.T) {
	tests := []struct {
		name  string
		rig   func(t *testing.T) *canaryRig
		setup func(c *canaryRig)
		want  string
	}{
		{"no root marker", newScratchRig, func(c *canaryRig) {
			os.Remove(filepath.Join(c.stateRoot, canaryroot.ScratchMarkerName))
		}, "no-marker"},
		{"the canary marker in place of the root marker", newScratchRig, func(c *canaryRig) {
			os.Remove(filepath.Join(c.stateRoot, canaryroot.ScratchMarkerName))
			if err := canaryroot.WriteMarker(c.stateRoot, canaryProject); err != nil {
				c.t.Fatal(err)
			}
		}, "wrong-kind"},
		{"the canary marker beside the root marker", newScratchRig, func(c *canaryRig) {
			if err := os.WriteFile(filepath.Join(c.stateRoot, canaryroot.MarkerName), []byte("{}\n"), 0o600); err != nil {
				c.t.Fatal(err)
			}
		}, "wrong-kind"},
		{"a root marker with the wrong mode", newScratchRig, func(c *canaryRig) {
			os.Chmod(filepath.Join(c.stateRoot, canaryroot.ScratchMarkerName), 0o644)
		}, "bad-marker"},
		{"a throwaway lead on a scratch root", newCanaryRig, func(c *canaryRig) {
			if err := os.WriteFile(filepath.Join(c.stateRoot, canaryroot.ScratchMarkerName), []byte("{}\n"), 0o600); err != nil {
				c.t.Fatal(err)
			}
		}, "wrong-kind"},
		{"a layout role that is not the scratch lead's", newScratchRig, func(c *canaryRig) {
			c.in.LayoutRole = scratchProject + "-other"
			c.writeInputs()
		}, "layout role"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.rig(t)
			tc.setup(c)
			a := c.spec("")
			if c.in.Project == scratchProject {
				a = c.scratchSpec("")
			}
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), "refused: ") || !strings.Contains(c.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
			if c.systemsMade() != 0 || len(c.launches()) != 0 {
				t.Error("a refused start reached the start sequence")
			}
			if !c.lockFree() {
				t.Error("the role lock is still held")
			}
		})
	}
}

// With a project of neither prefix the refusal is the prefix's, whatever
// markers the root carries.
func TestCanaryRefusesAProjectOfNeitherPrefixOnAScratchRoot(t *testing.T) {
	c := newScratchRig(t)
	c.in.Project, c.in.LayoutRole = "plain", "plain-lead"
	c.writeInputs()
	if code := c.runNow(c.spec("").set("role", "plain/lead").build()); code != exitRefuse || !strings.Contains(c.errbuf.String(), "not-throwaway") {
		t.Errorf("exit %d, want %d with the not-throwaway code; stderr: %s", code, exitRefuse, c.errbuf.String())
	}
	nothingStarted(t, c.rig)
}
