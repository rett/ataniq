//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// world is a fake host under a temporary folder: real state and runtime roots
// (the root check of canary mode reads them), a marker, and the inputs that
// name paths below them. The sources of the profile are names only: the fake
// system never looks at them.
type world struct {
	t     *testing.T
	base  string
	state string
	run   string
	in    Inputs
	sys   *fakeSystem
	lch   *fakeLauncher
}

// newWorld builds the fake host. The inputs are in canary mode and valid; the
// tests change them.
func newWorld(t *testing.T) *world {
	t.Helper()
	base := realpath(t, t.TempDir())
	w := &world{t: t, base: base, state: base + "/state", run: base + "/run"}
	mkdir(t, w.state, 0o700)
	root, err := instanceid.OpenRoot(w.state, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := instanceid.InitAt(root, "sample"); err != nil {
		t.Fatal(err)
	}
	mkdir(t, w.run, 0o700)
	mkdir(t, RuntimeFolder(w.run, sampleRole), 0o700)
	if err := canaryroot.WriteMarker(w.state, sampleProject); err != nil {
		t.Fatal(err)
	}
	w.in = sampleInputs(base)
	w.in.AgentArgv = []string{"/ataniq/agent/bin/stand-in"}
	w.sys = newFakeSystem(w)
	w.lch = &fakeLauncher{w: w}
	return w
}

// run runs the sequence against the fake host.
func (w *world) run_() (Started, error) {
	w.t.Helper()
	return Run(context.Background(), w.in, w.sys, w.lch)
}

// ---- the fake system ----

// fakeSystem is a System that touches nothing: it answers from the world, keeps
// a list of its calls and what it opened, and fails a call when told to.
type fakeSystem struct {
	w *world

	mu sync.Mutex
	// calls are the calls made, in order (Getenv and the accessors are not).
	calls []string
	// fail is an error per call name.
	fail map[string]error
	// failOn is an error for the n-th call (1-based) of a name, as "Name#n".
	failN map[string]error
	count map[string]int

	versions ToolVersions
	ownClone mountpoints.Report
	// checks are what the first and the second walk of the mount points find.
	checks []mountpoints.Report
	// bindRep is what CheckBindSources finds.
	bindRep mountpoints.Report
	// getenv answers the root check; nil means no variable is set.
	env map[string]string

	// resolve answers ResolveSources; the default resolves nothing through links.
	resolveShift bool

	lock      fakeLock
	linger    fakeLinger
	user      string
	sysctl    string
	assertSys *fakeAssertSystem
	// hitCanary makes a connection to the canary right after it is made, as
	// something that reached the host would.
	hitCanary bool

	// real, when set, makes the report channel and the bound folder the real
	// ones, over the real runtime folder of the world.
	real System

	// what was opened
	bounds   []*fakeBound
	channels []*fakeChannel
	canaries []*assert.Canary
	// digest is the digest of the spec the last WriteSpec saw.
	digest string
	// probe is the report the probe's line holds, set on the first read.
	probeStatus map[int]assert.Status
	readErr     error
	trailErr    error
}

func newFakeSystem(w *world) *fakeSystem {
	s := &fakeSystem{
		w: w, fail: map[string]error{}, failN: map[string]error{}, count: map[string]int{},
		versions: ToolVersions{Kernel: "6.0-fake", Bwrap: "bubblewrap " + MinBwrapBindFD, Pasta: "pasta fake"},
		lock:     fakeLock{held: true}, linger: fakeLinger{on: true}, user: "someone", sysctl: "0\n",
		probeStatus: map[int]assert.Status{},
	}
	s.assertSys = &fakeAssertSystem{s: s}
	return s
}

// hit counts a call, records it, and returns the error set for it.
func (s *fakeSystem) hit(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	s.count[name]++
	if err := s.failN[name+"#"+string(rune('0'+s.count[name]))]; err != nil {
		return err
	}
	return s.fail[name]
}

func (s *fakeSystem) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeSystem) Getenv(name string) string { return s.env[name] }
func (s *fakeSystem) Now() time.Time            { return time.Unix(1_700_000_000, 0) }
func (s *fakeSystem) IDs() (uint32, uint32)     { return uint32(unix.Geteuid()), uint32(unix.Getegid()) }
func (s *fakeSystem) Username() (string, error) { return s.user, nil }

func (s *fakeSystem) CheckHost(in Inputs) ([]ExeRecord, error) {
	if err := s.hit("CheckHost"); err != nil {
		return nil, err
	}
	return []ExeRecord{{Name: "pasta", Path: in.Executables.Pasta, SHA256: strings.Repeat("0", 64), Size: 1}}, nil
}

func (s *fakeSystem) ToolVersions(ctx context.Context, tools ToolPaths) ToolVersions {
	s.hit("ToolVersions")
	return s.versions
}

func (s *fakeSystem) CheckOwnClone(in profile.Inputs, uid uint32) (mountpoints.Report, error) {
	if err := s.hit("CheckOwnClone"); err != nil {
		return mountpoints.Report{}, err
	}
	return s.ownClone, nil
}

func (s *fakeSystem) PrepareMountPoints(in profile.Inputs) error { return s.hit("PrepareMountPoints") }

func (s *fakeSystem) CheckMountPoints(in profile.Inputs) (mountpoints.Report, error) {
	if err := s.hit("CheckMountPoints"); err != nil {
		return mountpoints.Report{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.count["CheckMountPoints"]; n >= 1 && n <= len(s.checks) {
		return s.checks[n-1], nil
	}
	return mountpoints.Report{}, nil
}

func (s *fakeSystem) DeriveResolv() (sandboxnet.Result, error) {
	if err := s.hit("DeriveResolv"); err != nil {
		return sandboxnet.Result{}, err
	}
	return sandboxnet.Result{Content: []byte("nameserver 192.0.2.53\n")}, nil
}

func (s *fakeSystem) MakeBound(runtimeRoot, layoutRole string) (Bound, error) {
	if err := s.hit("MakeBound"); err != nil {
		return nil, err
	}
	b := &fakeBound{s: s}
	if s.real != nil {
		rb, err := s.real.MakeBound(runtimeRoot, layoutRole)
		if err != nil {
			return nil, err
		}
		b.real = rb
	}
	s.mu.Lock()
	s.bounds = append(s.bounds, b)
	s.mu.Unlock()
	return b, nil
}

// ResolveSources resolves every source to itself, as a host with no link on any
// path does. The second call can be made to differ.
func (s *fakeSystem) ResolveSources(in profile.Inputs) (mountpoints.Resolution, error) {
	if err := s.hit("ResolveSources"); err != nil {
		return mountpoints.Resolution{}, err
	}
	res, err := mountpoints.ResolveSourcesWith(in,
		func(p string) (string, error) { return p, nil },
		func(string) (string, error) { return "", syscall.EINVAL })
	if err != nil {
		return res, err
	}
	s.mu.Lock()
	shift := s.resolveShift && s.count["ResolveSources"] == 2
	s.mu.Unlock()
	if shift {
		for i := range res.Sources {
			if res.Sources[i].Role == profile.RoleHost && !res.Sources[i].Skipped {
				res.Sources[i].Real += "-moved"
				break
			}
		}
	}
	return res, nil
}

func (s *fakeSystem) HostHas(string) bool { return true }

func (s *fakeSystem) OpenReport(runtimeRoot, layoutRole string) (ReportChannel, error) {
	if err := s.hit("OpenReport"); err != nil {
		return nil, err
	}
	c := &fakeChannel{s: s}
	if s.real != nil {
		rc, err := s.real.OpenReport(runtimeRoot, layoutRole)
		if err != nil {
			return nil, err
		}
		c.real = rc
	}
	s.mu.Lock()
	s.channels = append(s.channels, c)
	s.mu.Unlock()
	return c, nil
}

func (s *fakeSystem) NewCanary() (*assert.Canary, error) {
	if err := s.hit("NewCanary"); err != nil {
		return nil, err
	}
	c, err := assert.NewCanary()
	if err != nil {
		return nil, err
	}
	if s.hitCanary {
		if conn, err := net.Dial("tcp", c.TCP); err == nil {
			conn.Close()
		}
	}
	s.mu.Lock()
	s.canaries = append(s.canaries, c)
	s.mu.Unlock()
	return c, nil
}

func (s *fakeSystem) CheckBindSources(rows []mountpoints.ResolvedSource) mountpoints.Report {
	s.hit("CheckBindSources")
	return s.bindRep
}

func (s *fakeSystem) Assert() assert.System     { return s.assertSys }
func (s *fakeSystem) RoleLock() assert.RoleLock { return &s.lock }
func (s *fakeSystem) Lingering() assert.Lingering {
	return &s.linger
}

type fakeLock struct {
	held bool
	err  error
	// asked is the role the lock was asked about.
	asked string
}

func (l *fakeLock) Held(role string) (bool, error) {
	l.asked = role
	return l.held, l.err
}

type fakeLinger struct {
	on    bool
	err   error
	asked string
}

func (l *fakeLinger) Enabled(user string) (bool, error) {
	l.asked = user
	return l.on, l.err
}

// fakeAssertSystem answers the reads of the spec and of the outside checks: no
// path has an identity (so the lists are empty), and the sysctl reads as the
// world says.
type fakeAssertSystem struct{ s *fakeSystem }

func (f *fakeAssertSystem) Lstat(string) (assert.FileInfo, error) {
	return assert.FileInfo{}, fs.ErrNotExist
}
func (f *fakeAssertSystem) ReadFile(p string, limit int) ([]byte, error) {
	if p == "/proc/sys/dev/tty/legacy_tiocsti" {
		return []byte(f.s.sysctl), nil
	}
	return nil, fs.ErrNotExist
}
func (f *fakeAssertSystem) Mounts() ([]assert.Mount, error) { return nil, errors.New("not a sandbox") }
func (f *fakeAssertSystem) Environ() []string               { return nil }
func (f *fakeAssertSystem) Dial(string, string, time.Duration) error {
	return errors.New("no network in a test")
}
func (f *fakeAssertSystem) LookupHost(string, time.Duration) ([]string, error) {
	return nil, errors.New("no network in a test")
}
func (f *fakeAssertSystem) MountID(string) (int, error) { return 0, assert.ErrNoMountID }
func (f *fakeAssertSystem) Identity(string) (assert.FileIdentity, error) {
	return assert.FileIdentity{}, fs.ErrNotExist
}
func (f *fakeAssertSystem) DirEmpty(string) (bool, error) { return false, fs.ErrNotExist }
func (f *fakeAssertSystem) DirNames(string, int) ([]string, error) {
	return nil, fs.ErrNotExist
}
func (f *fakeAssertSystem) Writable(string) (bool, error) { return false, nil }

// fakeBound records what was written and closed; with a real folder behind it,
// it writes there too.
type fakeBound struct {
	s      *fakeSystem
	real   Bound
	resolv []byte
	spec   []byte
	closes int
}

func (b *fakeBound) WriteResolv(c []byte) error {
	if err := b.s.hit("WriteResolv"); err != nil {
		return err
	}
	b.resolv = c
	if b.real != nil {
		return b.real.WriteResolv(c)
	}
	return nil
}

func (b *fakeBound) WriteSpec(c []byte) error {
	if err := b.s.hit("WriteSpec"); err != nil {
		return err
	}
	b.spec = c
	if spec, err := assert.DecodeSpecBytes(c); err == nil {
		b.s.mu.Lock()
		b.s.digest = spec.Digest
		b.s.mu.Unlock()
	}
	if b.real != nil {
		return b.real.WriteSpec(c)
	}
	return nil
}

func (b *fakeBound) Close() error {
	b.closes++
	err := b.s.hit("Bound.Close")
	if b.real != nil {
		if rerr := b.real.Close(); err == nil {
			err = rerr
		}
	}
	return err
}

// fakeChannel plays the probe's line. With a real channel behind it, the line
// comes through the real FIFO (the fake launcher writes it).
type fakeChannel struct {
	s      *fakeSystem
	real   ReportChannel
	closes int
	reads  int
}

// line is the report the probe writes for the digest: every inside result, ok
// unless the world says otherwise.
func (c *fakeChannel) line(digest string) assert.Report {
	rep := assert.Report{Version: assert.ReportVersion, Digest: digest}
	for _, a := range assert.Assertions() {
		if !a.HasInside() {
			continue
		}
		st := assert.StatusOK
		if v, ok := c.s.probeStatus[a.Number]; ok {
			st = v
		}
		rep.Results = append(rep.Results, assert.Result{Number: a.Number, ID: a.ID, Status: st, Detail: "sample"})
	}
	return rep
}

func (c *fakeChannel) ReadLine(ctx context.Context, exited <-chan struct{}) (assert.Report, error) {
	c.reads++
	if err := c.s.hit("ReadLine"); err != nil {
		return assert.Report{}, err
	}
	if c.real != nil {
		return c.real.ReadLine(ctx, exited)
	}
	c.s.mu.Lock()
	digest := c.s.digest
	c.s.mu.Unlock()
	return c.line(digest), nil
}

func (c *fakeChannel) Trailing() error {
	if err := c.s.hit("Trailing"); err != nil {
		return err
	}
	if c.real != nil {
		return c.real.Trailing()
	}
	return nil
}

func (c *fakeChannel) Close() error {
	c.closes++
	c.s.hit("Channel.Close")
	if c.real != nil {
		return c.real.Close()
	}
	return nil
}

// ---- the fake launcher and child ----

type fakeLauncher struct {
	w    *world
	mu   sync.Mutex
	err  error
	argv [][]string
	kids []*fakeChild
	// onLaunch runs in the launch: the real report channel test writes the
	// probe's line there.
	onLaunch func(argv []string) error
}

func (l *fakeLauncher) Launch(argv []string) (Child, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.argv = append(l.argv, append([]string(nil), argv...))
	if l.err != nil {
		return nil, l.err
	}
	if l.onLaunch != nil {
		if err := l.onLaunch(argv); err != nil {
			return nil, err
		}
	}
	k := &fakeChild{exited: make(chan struct{}), gone: true}
	l.kids = append(l.kids, k)
	return k, nil
}

func (l *fakeLauncher) launches() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.argv)
}

type fakeChild struct {
	mu     sync.Mutex
	exited chan struct{}
	// gone is what TreeGone says after a stop.
	gone    bool
	stops   []time.Duration
	closes  int
	stopped bool
}

func (k *fakeChild) Exited() <-chan struct{} { return k.exited }

func (k *fakeChild) Stop(grace time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stops = append(k.stops, grace)
	k.stopped = true
}

func (k *fakeChild) TreeGone() (bool, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.stopped {
		return false, "fake: not stopped"
	}
	if !k.gone {
		return false, "fake: a process remains"
	}
	return true, "fake: control group empty"
}

func (k *fakeChild) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closes++
	return nil
}

var (
	_ System   = (*fakeSystem)(nil)
	_ Launcher = (*fakeLauncher)(nil)
	_ Child    = (*fakeChild)(nil)
)
