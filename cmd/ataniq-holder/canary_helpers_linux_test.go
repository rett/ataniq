//go:build linux && ataniq_canary

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// The project and the names of the canary under test. The project carries the
// reserved prefix.
const (
	canaryProject    = "throwaway-one"
	canaryLayoutRole = "throwaway-one-lead"
	canaryRoleText   = "throwaway-one/lead"
)

// canaryRig is a holder in canary mode over a fake host: real state and
// runtime roots under a temporary folder with the layout's folders and the
// marker, an inputs file, a fake System and a launcher seam that starts the
// fake agent in place of the plan. Nothing here starts a tool, a namespace or a
// unit.
type canaryRig struct {
	*rig
	base, stateRoot, runRoot string
	inputsPath               string
	in                       sandboxstart.Inputs
	sys                      *fakeSystem

	// script is the fake agent's script.
	script string
	// startErr makes the launcher seam fail.
	startErr error
	// getenv is the environment of the holder's root check; nil is the real one.
	getenv func(string) string
	// cgroupRoot is the fake cgroup mount, when the test made one.
	cgroupRoot string
	// interval is the copy interval of the counters.
	interval time.Duration

	mu sync.Mutex
	// launched are the session configurations the launcher seam was given, as
	// they were before the fake agent replaced the plan.
	launched []session.Config
	pids     []int
	systems  int
}

func newCanaryRig(t *testing.T) *canaryRig {
	t.Helper()
	r := newRig(t)
	root, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		t.Fatal(err)
	}
	c := &canaryRig{rig: r, base: filepath.Join(root, "c"), script: "wait", interval: 10 * time.Millisecond}
	c.stateRoot, c.runRoot = filepath.Join(c.base, "state"), filepath.Join(c.base, "run")
	lead := filepath.Join(c.stateRoot, "leads", canaryLayoutRole)
	r.state, r.tree = filepath.Join(lead, "holder"), filepath.Join(lead, "tree")
	r.runtime = filepath.Join(c.runRoot, "leads", canaryLayoutRole)
	r.roleLock = filepath.Join(c.stateRoot, "roles", canaryProject, "lead.lock")
	for _, d := range []string{c.stateRoot, c.runRoot, r.state, r.tree, r.runtime, filepath.Dir(r.roleLock)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := canaryroot.WriteMarker(c.stateRoot, canaryProject); err != nil {
		t.Fatal(err)
	}
	c.in = sampleInputs(c.base)
	c.inputsPath = filepath.Join(c.base, "inputs.json")
	c.writeInputs()
	c.sys = newFakeSystem()
	return c
}

// sampleInputs are valid inputs in canary mode for a host under base. The
// sources are names only: the fake system never looks at them.
func sampleInputs(base string) sandboxstart.Inputs {
	rt := profile.Runtimes()[0]
	return sandboxstart.Inputs{
		Version: sandboxstart.InputsVersion, Canary: true,
		Project: canaryProject, LayoutRole: canaryLayoutRole,
		StateRoot: base + "/state", RuntimeRoot: base + "/run",
		Runtime: string(rt), RuntimeVersion: "2.1.0",
		Executables: sandboxstart.Executables{Pasta: base + "/bin/pasta", Bwrap: base + "/bin/bwrap", Probe: base + "/bin/probe"},
		Sources: sandboxstart.Sources{
			AgentInstall:    base + "/owned/agent",
			ManagedSettings: base + "/owned/managed/settings.json", ManagedSettingsSHA256: strings.Repeat("ab", 32),
			ManagedMarker: "sample-guard",
			LeadSkills:    base + "/owned/skills", InstructionFile: base + "/owned/instructions/" + rt.InstructionFileName(),
			GitIdentityDir: base + "/owned/git", QargiInstall: base + "/owned/qargi",
			QargiRecordsInside: "/ataniq/records",
			AssignmentDir:      base + "/owned/assignment", ProjectionDir: base + "/owned/projection",
		},
		NeverMounted:     []string{base + "/owner/work", base + "/owner/.ssh"},
		ProviderHost:     "provider.example.test",
		Terminal:         sandboxstart.Terminal{Term: "xterm-256color", Lang: "C.UTF-8"},
		TimeLimitSeconds: 5,
	}
}

// writeInputs writes c.in as the inputs file (0600).
func (c *canaryRig) writeInputs() {
	c.t.Helper()
	b, err := json.Marshal(c.in)
	if err != nil {
		c.t.Fatal(err)
	}
	os.Remove(c.inputsPath)
	if err := os.WriteFile(c.inputsPath, b, 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// spec is the canary holder's command line, as the daemon builds it. The agent
// on the line is a name only: the launcher seam starts the fake agent. Without
// a digest the line pins sixty-four zeros.
func (c *canaryRig) spec(digest string, bools ...string) *argSpec {
	if digest == "" {
		digest = strings.Repeat("0", 64)
	}
	a := &argSpec{agent: []string{"/ataniq/agent/bin/stand-in", "--sample"}, env: []string{"SAMPLE_FLAG=on"}}
	for _, f := range [][2]string{
		{"role", canaryRoleText}, {"epoch", strconv.Itoa(testEpoch)}, {"launch-intent", "intent-0123456789abcdef"},
		{"runtime", c.in.Runtime}, {"runtime-version", c.in.RuntimeVersion}, {"profile-digest", digest},
		{"tree", c.tree}, {"state-dir", c.state}, {"runtime-dir", c.runtime},
		{"role-lock", c.roleLock}, {"daemon-lock", c.daemonLock}, {"cols", "100"}, {"rows", "30"},
		{"sandbox-inputs", c.inputsPath}, {"expected-mode", "sample mode on"},
	} {
		a.flags = append(a.flags, f)
	}
	a.extraBefore = append([]string{"--canary"}, bools...)
	return a
}

func (c *canaryRig) deps() deps {
	d := c.rig.deps()
	d.canary = canaryDeps{
		getenv:          c.getenv,
		cgroupRoot:      c.cgroupRoot,
		counterInterval: c.interval,
		newSystem: func(cfg sandboxstart.SystemConfig) (sandboxstart.System, error) {
			c.mu.Lock()
			c.systems++
			c.mu.Unlock()
			c.sys.setLock(cfg.RoleLock)
			return c.sys, nil
		},
		startSession: func(cfg session.Config) (*session.Session, error) {
			c.mu.Lock()
			c.launched = append(c.launched, cfg)
			err := c.startErr
			script := c.script
			c.mu.Unlock()
			if err != nil {
				if cfg.HeldRoleLock != nil {
					cfg.HeldRoleLock.Close()
				}
				return nil, err
			}
			cfg.Argv, cfg.Env = fakeagent.Command(script)
			s, err := session.Start(cfg)
			if err == nil {
				c.mu.Lock()
				c.pids = append(c.pids, s.Pid())
				c.mu.Unlock()
			}
			return s, err
		},
	}
	return d
}

func (c *canaryRig) start(args []string) {
	c.t.Helper()
	d := c.deps()
	go func() { c.done <- run(args, d) }()
}

func (c *canaryRig) runNow(args []string) int { return run(args, c.deps()) }

func (c *canaryRig) launches() []session.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.Config(nil), c.launched...)
}

func (c *canaryRig) systemsMade() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.systems
}

var pinnedDigest = regexp.MustCompile(`the profile digest of this plan is ([0-9a-f]{64}), not the pinned`)

// digest finds the profile digest of the plan for this rig's command line: a
// start pinned to sixty-four zeros is refused at the step verify-plan, before
// anything is launched, and the refusal names the real digest. What that start
// left in the state folder is removed; it left no mark, which is checked.
func (c *canaryRig) digest() string {
	c.t.Helper()
	if code := c.runNow(c.spec("").build()); code != exitSandbox {
		c.t.Fatalf("the probing start exited %d; stderr: %s", code, c.errbuf.String())
	}
	m := pinnedDigest.FindStringSubmatch(c.errbuf.String())
	if m == nil {
		c.t.Fatalf("the probing start does not name the digest: %s", c.errbuf.String())
	}
	if n := len(c.launches()); n != 0 {
		c.t.Fatalf("the probing start launched %d times", n)
	}
	if _, err := os.Lstat(filepath.Join(c.state, leadHasRunMark)); err == nil {
		c.t.Fatal("the probing start, which never launched, left the mark")
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

// fakeCgroup makes a fake proc and cgroup tree in which the holder is alone in
// its group, or not.
func (c *canaryRig) fakeCgroup(alone bool) {
	c.t.Helper()
	if err := os.MkdirAll(filepath.Join(c.fakeProc, "self"), 0o755); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.fakeProc, "self", "cgroup"), []byte("0::/sample.slice/lead.service\n"), 0o644); err != nil {
		c.t.Fatal(err)
	}
	c.cgroupRoot = filepath.Join(c.root, "cgroup")
	dir := filepath.Join(c.cgroupRoot, "sample.slice", "lead.service")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.t.Fatal(err)
	}
	procs := strconv.Itoa(os.Getpid()) + "\n"
	if !alone {
		procs += "1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(procs), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

// lockFree says the role lock can be taken: no holder and no session holds it.
func (c *canaryRig) lockFree() bool {
	f, err := session.TakeRoleLock(c.roleLock)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// noAgentNoClaim checks what a failed start must leave: no state file, no
// socket, a free role lock, and every process the launcher started gone.
func (c *canaryRig) noAgentNoClaim() {
	c.t.Helper()
	for _, p := range []string{
		filepath.Join(c.state, StateFileName),
		filepath.Join(c.runtime, "attach.sock"), filepath.Join(c.runtime, "control.sock"),
	} {
		if _, err := os.Lstat(p); err == nil {
			c.t.Errorf("a failed start left %s", p)
		}
	}
	if !c.lockFree() {
		c.t.Error("a failed start left the role lock held")
	}
	c.mu.Lock()
	pids := append([]int(nil), c.pids...)
	c.mu.Unlock()
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			c.t.Errorf("the launched process %d is still there after a failed start (%v)", pid, err)
		}
	}
}

// ---- the fake system ----

// fakeSystem is a sandboxstart.System that touches nothing but the canary of
// assertion 3 (two private sockets): it answers from its fields, keeps a list
// of its calls, and fails a call when told to.
type fakeSystem struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
	env   map[string]string

	lock   assert.RoleLock
	linger bool
	// probeStatus is the status the probe's line gives an inside result, by
	// assertion number; ok when absent.
	probeStatus map[int]assert.Status
	// block makes ReadLine wait for the end of its context.
	block bool
	// seen are the inputs CheckHost was given: the fields the holder filled.
	seen []sandboxstart.Inputs
	// asked is the role the lock was asked about.
	asked  []string
	digest string
	// hook runs, outside the lock, when a call of that name is made.
	hook map[string]func()
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{fail: map[string]error{}, linger: true, probeStatus: map[int]assert.Status{}}
}

func (s *fakeSystem) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls, s.seen, s.asked = nil, nil, nil
}

func (s *fakeSystem) setLock(l assert.RoleLock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lock = l
}

func (s *fakeSystem) hit(name string) error {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	err, h := s.fail[name], s.hook[name]
	s.mu.Unlock()
	if h != nil {
		h()
	}
	return err
}

func (s *fakeSystem) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeSystem) called(name string) int {
	n := 0
	for _, c := range s.callList() {
		if c == name {
			n++
		}
	}
	return n
}

func (s *fakeSystem) inputs() []sandboxstart.Inputs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sandboxstart.Inputs(nil), s.seen...)
}

func (s *fakeSystem) Getenv(name string) string { return s.env[name] }
func (s *fakeSystem) Now() time.Time            { return time.Unix(1_700_000_000, 0) }
func (s *fakeSystem) IDs() (uint32, uint32)     { return uint32(unix.Geteuid()), uint32(unix.Getegid()) }
func (s *fakeSystem) Username() (string, error) { return "someone", nil }

func (s *fakeSystem) CheckHost(in sandboxstart.Inputs) ([]sandboxstart.ExeRecord, error) {
	s.mu.Lock()
	s.seen = append(s.seen, in)
	s.mu.Unlock()
	if err := s.hit("CheckHost"); err != nil {
		return nil, err
	}
	return []sandboxstart.ExeRecord{{Name: "pasta", Path: in.Executables.Pasta, SHA256: strings.Repeat("0", 64), Size: 1}}, nil
}

func (s *fakeSystem) ToolVersions(context.Context, sandboxstart.ToolPaths) sandboxstart.ToolVersions {
	s.hit("ToolVersions")
	return sandboxstart.ToolVersions{Kernel: "6.0-fake", Bwrap: "bubblewrap " + sandboxstart.MinBwrapBindFD, Pasta: "pasta fake"}
}

func (s *fakeSystem) CheckOwnClone(profile.Inputs, uint32) (mountpoints.Report, error) {
	return mountpoints.Report{}, s.hit("CheckOwnClone")
}

func (s *fakeSystem) PrepareMountPoints(profile.Inputs) error { return s.hit("PrepareMountPoints") }

func (s *fakeSystem) CheckMountPoints(profile.Inputs) (mountpoints.Report, error) {
	return mountpoints.Report{}, s.hit("CheckMountPoints")
}

func (s *fakeSystem) DeriveResolv() (sandboxnet.Result, error) {
	if err := s.hit("DeriveResolv"); err != nil {
		return sandboxnet.Result{}, err
	}
	return sandboxnet.Result{Content: []byte("nameserver 192.0.2.53\n")}, nil
}

func (s *fakeSystem) MakeBound(string, string) (sandboxstart.Bound, error) {
	if err := s.hit("MakeBound"); err != nil {
		return nil, err
	}
	return &fakeBound{s: s}, nil
}

func (s *fakeSystem) ResolveSources(in profile.Inputs) (mountpoints.Resolution, error) {
	if err := s.hit("ResolveSources"); err != nil {
		return mountpoints.Resolution{}, err
	}
	return mountpoints.ResolveSourcesWith(in,
		func(p string) (string, error) { return p, nil },
		func(string) (string, error) { return "", syscall.EINVAL })
}

func (s *fakeSystem) HostHas(string) bool { return true }

func (s *fakeSystem) OpenReport(string, string) (sandboxstart.ReportChannel, error) {
	if err := s.hit("OpenReport"); err != nil {
		return nil, err
	}
	return &fakeChannel{s: s}, nil
}

func (s *fakeSystem) NewCanary() (*assert.Canary, error) {
	if err := s.hit("NewCanary"); err != nil {
		return nil, err
	}
	return assert.NewCanary()
}

func (s *fakeSystem) CheckBindSources([]mountpoints.ResolvedSource) mountpoints.Report {
	s.hit("CheckBindSources")
	return mountpoints.Report{}
}

func (s *fakeSystem) Assert() assert.System { return fakeAssertSystem{} }

func (s *fakeSystem) RoleLock() assert.RoleLock { return fakeLockProxy{s} }

func (s *fakeSystem) Lingering() assert.Lingering { return fakeLinger{s} }

// fakeLockProxy asks the holder's own answer, and notes the role asked about.
type fakeLockProxy struct{ s *fakeSystem }

func (l fakeLockProxy) Held(role string) (bool, error) {
	l.s.mu.Lock()
	l.s.asked = append(l.s.asked, role)
	lock := l.s.lock
	l.s.mu.Unlock()
	if lock == nil {
		return false, errors.New("the holder gave no role lock")
	}
	return lock.Held(role)
}

type fakeLinger struct{ s *fakeSystem }

func (l fakeLinger) Enabled(string) (bool, error) {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	return l.s.linger, nil
}

// fakeAssertSystem answers the reads of the spec and of the outside checks: no
// path has an identity, and the sysctl reads 0.
type fakeAssertSystem struct{}

func (fakeAssertSystem) Lstat(string) (assert.FileInfo, error) {
	return assert.FileInfo{}, fs.ErrNotExist
}
func (fakeAssertSystem) ReadFile(p string, limit int) ([]byte, error) {
	if p == "/proc/sys/dev/tty/legacy_tiocsti" {
		return []byte("0\n"), nil
	}
	return nil, fs.ErrNotExist
}
func (fakeAssertSystem) Mounts() ([]assert.Mount, error) { return nil, errors.New("not a sandbox") }
func (fakeAssertSystem) Environ() []string               { return nil }
func (fakeAssertSystem) Dial(string, string, time.Duration) error {
	return errors.New("no network in a test")
}
func (fakeAssertSystem) LookupHost(string, time.Duration) ([]string, error) {
	return nil, errors.New("no network in a test")
}
func (fakeAssertSystem) MountID(string) (int, error) { return 0, assert.ErrNoMountID }
func (fakeAssertSystem) Identity(string) (assert.FileIdentity, error) {
	return assert.FileIdentity{}, fs.ErrNotExist
}
func (fakeAssertSystem) DirEmpty(string) (bool, error)          { return false, fs.ErrNotExist }
func (fakeAssertSystem) DirNames(string, int) ([]string, error) { return nil, fs.ErrNotExist }
func (fakeAssertSystem) Writable(string) (bool, error)          { return false, nil }

type fakeBound struct{ s *fakeSystem }

func (b *fakeBound) WriteResolv([]byte) error { return b.s.hit("WriteResolv") }

func (b *fakeBound) WriteSpec(c []byte) error {
	if err := b.s.hit("WriteSpec"); err != nil {
		return err
	}
	if spec, err := assert.DecodeSpecBytes(c); err == nil {
		b.s.mu.Lock()
		b.s.digest = spec.Digest
		b.s.mu.Unlock()
	}
	return nil
}

func (b *fakeBound) Close() error { return b.s.hit("Bound.Close") }

// fakeChannel plays the probe's report line: every inside result, ok unless the
// system says otherwise.
type fakeChannel struct{ s *fakeSystem }

func (c *fakeChannel) ReadLine(ctx context.Context, exited <-chan struct{}) (assert.Report, error) {
	if err := c.s.hit("ReadLine"); err != nil {
		return assert.Report{}, err
	}
	c.s.mu.Lock()
	block, digest := c.s.block, c.s.digest
	status := map[int]assert.Status{}
	for k, v := range c.s.probeStatus {
		status[k] = v
	}
	c.s.mu.Unlock()
	if block {
		<-ctx.Done()
		return assert.Report{}, ctx.Err()
	}
	rep := assert.Report{Version: assert.ReportVersion, Digest: digest}
	for _, a := range assert.Assertions() {
		if !a.HasInside() {
			continue
		}
		st := assert.StatusOK
		if v, ok := status[a.Number]; ok {
			st = v
		}
		rep.Results = append(rep.Results, assert.Result{Number: a.Number, ID: a.ID, Status: st, Detail: "sample"})
	}
	return rep, nil
}

func (c *fakeChannel) Trailing() error { return c.s.hit("Trailing") }
func (c *fakeChannel) Close() error    { return c.s.hit("Channel.Close") }

var _ sandboxstart.System = (*fakeSystem)(nil)

// stopAndWait stops the serving holder through the control socket and waits
// for it to end cleanly.
func (c *canaryRig) stopAndWait() {
	c.t.Helper()
	if resp := c.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		c.t.Fatalf("stop: %+v", resp)
	}
	if code := c.wait(); code != exitOK {
		c.t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
}
