package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/exchange/sandboxplan/plantest"
)

// fakeRunner records the commands it is given and runs a hook instead of a
// process.
type fakeRunner struct {
	mu    sync.Mutex
	calls []boundedexec.Cmd
	hook  func(c boundedexec.Cmd)
	res   boundedexec.Result
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, c boundedexec.Cmd) (boundedexec.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.hook != nil {
		f.hook(c)
	}
	return f.res, f.err
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newWork makes a WorkRoot folder: a real folder, mode 0700.
func newWork(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func requireEmpty(t *testing.T, dir string) {
	t.Helper()
	if got := entries(t, dir); len(got) != 0 {
		t.Fatalf("%s is not empty: %v", dir, got)
	}
}

func fakePlanner(t *testing.T) *plantest.Fake {
	t.Helper()
	requireTool(t, envTool)
	return &plantest.Fake{VerifyPrefix: []string{envTool}, VerifyEnv: gitEnv()}
}

func request(m mirror, work string, p *plantest.Fake, r Runner, gates ...Gate) Request {
	return Request{
		Project: "proj", Mirror: m.Path, TipRef: testRef, Tip: m.Tip,
		Gates: gates, Planner: p, Runner: r, WorkRoot: work,
		Limits: Limits{Timeout: 60 * time.Second, MaxOutput: 1 << 20},
	}
}

func sh(name, script string) Gate {
	return Gate{Name: name, Argv: []string{"/bin/sh", "-c", script}}
}

func TestRunPasses(t *testing.T) {
	t.Setenv("ATANIQ_VERIFY_LEAK_TEST", "secret")
	m := newMirror(t)
	work := newWork(t)
	gates := []Gate{
		sh("files", "test -f hello.txt && test \"$(cat hello.txt)\" = hello"),
		sh("env", "env | sort"),
	}
	before := time.Now()
	res, err := Run(context.Background(), request(m, work, fakePlanner(t), ExecRunner{}, gates...))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Exit != 0 || res.Stopped != "" || res.OutputCut {
		t.Fatalf("result: %+v\n%s", res, res.Output)
	}
	if res.Tip != m.Tip || res.TipRef != testRef || res.PlanDigest != PlanDigest(gates) {
		t.Fatalf("result identity: %+v", res)
	}
	if res.Started.Before(before) || res.Duration <= 0 {
		t.Fatalf("timing: %v %v", res.Started, res.Duration)
	}
	out := string(res.Output)
	if !strings.Contains(out, MarkerPrefix+"files\n") || !strings.Contains(out, MarkerPrefix+"env\n") {
		t.Fatalf("no markers: %s", out)
	}
	if strings.Contains(out, "ATANIQ_VERIFY_LEAK_TEST") {
		t.Fatalf("the caller's environment reached the command:\n%s", out)
	}
	requireEmpty(t, work)
}

func TestRunFailingGateStopsTheLaterOnes(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	res, err := Run(context.Background(), request(m, work, fakePlanner(t), ExecRunner{},
		sh("one", "echo first"), sh("two", "echo failing; exit 3"), sh("three", "echo never")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Exit != 3 || res.Stopped != "" {
		t.Fatalf("result: %+v", res)
	}
	out := string(res.Output)
	if !strings.Contains(out, "first") || !strings.Contains(out, "failing") || strings.Contains(out, "never") ||
		strings.Contains(out, MarkerPrefix+"three") {
		t.Fatalf("output: %s", out)
	}
	requireEmpty(t, work)
}

func TestRunGateExitingWithAReservedStatusIsReportedAsIs(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	res, err := Run(context.Background(), request(m, work, fakePlanner(t), ExecRunner{}, sh("odd", "exit 90")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Exit != ExitTipMismatch || !strings.Contains(string(res.Output), MarkerPrefix+"odd") {
		t.Fatalf("result: %+v\n%s", res, res.Output)
	}
}

func TestRunTipMismatch(t *testing.T) {
	m := newMirror(t)
	m.advance(t) // the ref moved after the review
	work := newWork(t)
	res, err := Run(context.Background(), request(m, work, fakePlanner(t), ExecRunner{}, sh("one", "echo ran")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Exit != ExitTipMismatch || res.Stopped != "" {
		t.Fatalf("result: %+v", res)
	}
	if strings.Contains(string(res.Output), "ran") || strings.Contains(string(res.Output), MarkerPrefix) {
		t.Fatalf("a gate ran: %s", res.Output)
	}
	requireEmpty(t, work)
}

func TestRunTimeout(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	req := request(m, work, fakePlanner(t), ExecRunner{}, sh("slow", "sleep 60"))
	req.Limits = Limits{Timeout: 2 * time.Second, MaxOutput: 1 << 20}
	start := time.Now()
	res, err := Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Stopped != StoppedTimeout || res.Exit != -1 {
		t.Fatalf("result: %+v\n%s", res, res.Output)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	requireEmpty(t, work)
}

func TestRunOutputLimit(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	req := request(m, work, fakePlanner(t), ExecRunner{}, sh("loud", "yes ataniq"))
	req.Limits = Limits{Timeout: 60 * time.Second, MaxOutput: 2000}
	res, err := Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Stopped != StoppedOutputLimit || !res.OutputCut || int64(len(res.Output)) > 2000 {
		t.Fatalf("result: %+v (output %d bytes)", res, len(res.Output))
	}
	requireEmpty(t, work)
}

func TestRunContextEnds(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := Run(ctx, request(m, work, fakePlanner(t), ExecRunner{}, sh("slow", "sleep 60")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Stopped != StoppedError {
		t.Fatalf("result: %+v", res)
	}
	requireEmpty(t, work)
}

func TestRunDefaultRunnerAndLimits(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	fr := &fakeRunner{}
	req := request(m, work, fakePlanner(t), fr, sh("one", "true"))
	req.Limits = Limits{}
	if _, err := Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := fr.calls[0]; got.Timeout != 30*time.Minute || got.MaxStdout != 4<<20 || got.MaxStderr != 4<<20 {
		t.Fatalf("limits: %+v", got)
	}
	if d := DefaultLimits(); d.Timeout != 30*time.Minute || d.MaxOutput != 4<<20 {
		t.Fatalf("DefaultLimits: %+v", d)
	}

	// A nil runner is the real one.
	req.Runner = nil
	req.Limits = Limits{Timeout: 60 * time.Second, MaxOutput: 1 << 20}
	res, err := Run(context.Background(), req)
	if err != nil || !res.Passed {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestRunGivesTheRunnerExactlyThePlan(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	t.Setenv("ATANIQ_VERIFY_LEAK_TEST", "secret")
	p := &plantest.Fake{
		VerifyPrefix:  []string{envTool, "-i"},
		VerifyEnv:     []string{"ONLY=this", "PATH=/usr/bin:/bin"},
		VerifyMirror:  "/in/mirror.git",
		VerifyScratch: "/in/scratch",
		VerifyHome:    "/in/home",
	}
	var duringScratch, duringHome []string
	var scratchMode, homeMode os.FileMode
	fr := &fakeRunner{res: boundedexec.Result{Exit: 0}}
	fr.hook = func(boundedexec.Cmd) {
		reqs := p.VerifyRequests()
		r := reqs[len(reqs)-1]
		duringScratch, duringHome = entries(t, r.Scratch), entries(t, r.Home)
		si, _ := os.Stat(r.Scratch)
		hi, _ := os.Stat(r.Home)
		scratchMode, homeMode = si.Mode(), hi.Mode()
	}
	gates := []Gate{sh("a", "echo a"), {Name: "b", Argv: []string{"make", "b c"}}}
	req := request(m, work, p, fr, gates...)
	req.Limits = Limits{Timeout: 7 * time.Second, MaxOutput: 12345}
	res, err := Run(context.Background(), req)
	if err != nil || !res.Passed {
		t.Fatalf("%+v, %v", res, err)
	}
	if fr.count() != 1 {
		t.Fatalf("runner called %d times", fr.count())
	}
	c := fr.calls[0]
	script, err := Script(testRef, m.Tip, "/in/mirror.git", "/in/scratch", gates)
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{envTool, "-i", "/bin/sh", "-c", script}
	if !reflect.DeepEqual(c.Argv, wantArgv) {
		t.Fatalf("argv:\n%q\nwant\n%q", c.Argv, wantArgv)
	}
	if !reflect.DeepEqual(c.Env, []string{"ONLY=this", "PATH=/usr/bin:/bin"}) {
		t.Fatalf("env: %q", c.Env)
	}
	if c.Timeout != 7*time.Second || c.MaxStdout != 12345 || c.MaxStderr != 12345 {
		t.Fatalf("limits: %+v", c)
	}
	if c.Stdin != nil || c.Stdout != nil || c.Watch != nil || c.Dir != "" {
		t.Fatalf("extra fields set: %+v", c)
	}

	// The planner was asked about this run's own, fresh folders.
	reqs := p.VerifyRequests()
	if len(reqs) != 1 {
		t.Fatalf("planner calls: %d", len(reqs))
	}
	r := reqs[0]
	if r.Project != "proj" || r.Mirror != m.Path ||
		filepath.Dir(r.Scratch) != work || filepath.Dir(r.Home) != work || r.Scratch == r.Home {
		t.Fatalf("verify request: %+v", r)
	}
	if len(duringScratch) != 0 || len(duringHome) != 0 {
		t.Fatalf("folders were not empty: %v %v", duringScratch, duringHome)
	}
	if scratchMode.Perm() != 0o700 || homeMode.Perm() != 0o700 || !scratchMode.IsDir() || !homeMode.IsDir() {
		t.Fatalf("modes: %v %v", scratchMode, homeMode)
	}
	requireEmpty(t, work)

	// A second run gets other folder names.
	if _, err := Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if reqs2 := p.VerifyRequests(); reqs2[1].Scratch == r.Scratch || reqs2[1].Home == r.Home {
		t.Fatalf("folder names repeat: %+v %+v", r, reqs2[1])
	}
}

func TestRunMapsWhatTheRunnerReturned(t *testing.T) {
	m := newMirror(t)
	boom := errors.New("boom")
	cases := []struct {
		name    string
		res     boundedexec.Result
		err     error
		passed  bool
		exit    int
		stopped string
		cut     bool
		output  string
	}{
		{"clean", boundedexec.Result{Stdout: []byte("ok\n")}, nil, true, 0, "", false, "ok\n"},
		{"exit status", boundedexec.Result{Stdout: []byte("x"), Exit: 4}, &boundedexec.ExitError{Exit: 4}, false, 4, "", false, "x"},
		{"exit status without error", boundedexec.Result{Exit: 4}, nil, false, 4, "", false, ""},
		{"reserved status", boundedexec.Result{Exit: 95}, &boundedexec.ExitError{Exit: 95}, false, 95, "", false, ""},
		{"killed by a signal", boundedexec.Result{Exit: -1}, &boundedexec.ExitError{Exit: -1}, false, -1, StoppedError, false, ""},
		{"timeout", boundedexec.Result{Exit: -1, Stdout: []byte("t")}, boundedexec.ErrTimeout, false, -1, StoppedTimeout, false, "t"},
		{"output limit", boundedexec.Result{Exit: -1, Stdout: []byte("abc")}, boundedexec.ErrOutputLimit, false, -1, StoppedOutputLimit, true, "abc"},
		{"pipe held", boundedexec.Result{Exit: 0, Stdout: []byte("p")}, boundedexec.ErrPipeHeld, false, 0, StoppedError, false, "p"},
		{"context", boundedexec.Result{Exit: -1}, context.Canceled, false, -1, StoppedError, false, ""},
		{"never started", boundedexec.Result{Exit: -1}, boom, false, -1, StoppedError, false, ""},
		{"stderr joined", boundedexec.Result{Stdout: []byte("out"), Stderr: []byte("+err")}, nil, true, 0, "", false, "out+err"},
		{"stderr cut", boundedexec.Result{Stdout: []byte("out"), Stderr: []byte("e"), StderrCut: true}, nil, false, 0, "", true, "oute"},
		{"too much output from the runner", boundedexec.Result{Stdout: bytes.Repeat([]byte("a"), 20)}, nil, false, 0, "", true, strings.Repeat("a", 10)},
		{"stderr over the room", boundedexec.Result{Stdout: bytes.Repeat([]byte("a"), 8), Stderr: []byte("bcd")}, nil, false, 0, "", true, "aaaaaaaabc"},
	}
	for _, c := range cases {
		work := newWork(t)
		fr := &fakeRunner{res: c.res, err: c.err}
		req := request(m, work, fakePlanner(t), fr, sh("one", "true"))
		req.Limits = Limits{Timeout: time.Minute, MaxOutput: 10}
		res, err := Run(context.Background(), req)
		if err != nil {
			t.Errorf("%s: error %v", c.name, err)
			continue
		}
		if res.Passed != c.passed || res.Exit != c.exit || res.Stopped != c.stopped || res.OutputCut != c.cut || string(res.Output) != c.output {
			t.Errorf("%s: %+v", c.name, res)
		}
		requireEmpty(t, work)
	}
}

func TestRunRefusesBeforeRunningAnything(t *testing.T) {
	m := newMirror(t)
	ok := []Gate{sh("one", "true")}
	work := newWork(t)
	cases := map[string]func(r *Request){
		"project":          func(r *Request) { r.Project = "Proj" },
		"relative mirror":  func(r *Request) { r.Mirror = "mirror.git" },
		"unclean mirror":   func(r *Request) { r.Mirror = m.Path + "/../mirror.git" },
		"mirror in work":   func(r *Request) { r.Mirror = filepath.Join(work, "m.git") },
		"work in mirror":   func(r *Request) { r.WorkRoot = filepath.Join(m.Path, "work") },
		"ref outside":      func(r *Request) { r.TipRef = "refs/heads/main" },
		"ref bad":          func(r *Request) { r.TipRef = "refs/leads/a/heads/x y" },
		"short tip":        func(r *Request) { r.Tip = "abc" },
		"no gates":         func(r *Request) { r.Gates = nil },
		"bad gate":         func(r *Request) { r.Gates = []Gate{{Name: "x", Argv: []string{"a\nb"}}} },
		"builtin gate":     func(r *Request) { r.Gates = []Gate{{Name: "x", Argv: []string{"exit", "0"}}} },
		"relative gate":    func(r *Request) { r.Gates = []Gate{{Name: "x", Argv: []string{"./run.sh"}}} },
		"no planner":       func(r *Request) { r.Planner = nil },
		"negative timeout": func(r *Request) { r.Limits.Timeout = -1 },
		"negative output":  func(r *Request) { r.Limits.MaxOutput = -1 },
		"huge output":      func(r *Request) { r.Limits.MaxOutput = 1 << 40 },
		"relative work":    func(r *Request) { r.WorkRoot = "work" },
	}
	for name, mod := range cases {
		fr := &fakeRunner{}
		p := fakePlanner(t)
		req := request(m, work, p, fr, ok...)
		mod(&req)
		if name == "no planner" {
			req.Planner = nil
		}
		if _, err := Run(context.Background(), req); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if fr.count() != 0 || len(p.VerifyRequests()) != 0 {
			t.Errorf("%s: something ran", name)
		}
		requireEmpty(t, work)
	}
}

func TestRunRefusesAWorkRootThatIsNotOwnerOnly(t *testing.T) {
	m := newMirror(t)
	ok := []Gate{sh("one", "true")}
	base := t.TempDir()

	real0700 := filepath.Join(base, "real")
	groupWritable := filepath.Join(base, "group")
	worldReadable := filepath.Join(base, "world")
	for _, d := range []string{real0700, groupWritable, worldReadable} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(groupWritable, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worldReadable, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real0700, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	sticky := filepath.Join(base, "sticky")
	if err := os.Mkdir(sticky, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, 0o700|os.ModeSticky); err != nil {
		t.Fatal(err)
	}

	for name, w := range map[string]string{
		"symlink":        link,
		"group writable": groupWritable,
		"mode 0755":      worldReadable,
		"sticky":         sticky,
		"file":           file,
		"missing":        filepath.Join(base, "missing"),
		"root":           "/",
	} {
		fr := &fakeRunner{}
		p := fakePlanner(t)
		if _, err := Run(context.Background(), request(m, w, p, fr, ok...)); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if fr.count() != 0 || len(p.VerifyRequests()) != 0 {
			t.Errorf("%s: something ran", name)
		}
	}
	requireEmpty(t, real0700)
	requireEmpty(t, groupWritable)

	// The real, owner-only folder works.
	fr := &fakeRunner{}
	if _, err := Run(context.Background(), request(m, real0700, fakePlanner(t), fr, ok...)); err != nil {
		t.Fatal(err)
	}
	requireEmpty(t, real0700)
}

func TestRunPlannerRefusal(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	fr := &fakeRunner{}
	p := fakePlanner(t)
	p.VerifyErr = fmt.Errorf("verify sandbox: %w", sandboxplan.ErrNotConfigured)
	res, err := Run(context.Background(), request(m, work, p, fr, sh("one", "true")))
	if !errors.Is(err, sandboxplan.ErrNotConfigured) {
		t.Fatalf("error: %v", err)
	}
	if res.Passed || fr.count() != 0 {
		t.Fatalf("result %+v, runner called %d times", res, fr.count())
	}
	if len(p.VerifyRequests()) != 1 {
		t.Fatalf("planner calls: %d", len(p.VerifyRequests()))
	}
	requireEmpty(t, work)
}

func TestRunPlanThatTheCallerCannotUse(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	fr := &fakeRunner{}
	p := fakePlanner(t)
	p.VerifyScratch = "/in/scratch/"
	if _, err := Run(context.Background(), request(m, work, p, fr, sh("one", "true"))); err == nil {
		t.Fatal("accepted")
	}
	if fr.count() != 0 {
		t.Fatal("runner called")
	}
	requireEmpty(t, work)
}

func TestRunRemovesTheFoldersWhenTheRunnerPanics(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	fr := &fakeRunner{hook: func(boundedexec.Cmd) { panic("runner panic") }}
	func() {
		defer func() {
			if r := recover(); r != "runner panic" {
				t.Fatalf("recovered %v", r)
			}
		}()
		Run(context.Background(), request(m, work, fakePlanner(t), fr, sh("one", "true")))
	}()
	requireEmpty(t, work)
}

// outsideTree is a folder the sandboxed code must never change: it is read
// only, and a symlink in the scratch folder points at it.
type outsideTree struct{ root string }

func newOutside(t *testing.T) outsideTree {
	t.Helper()
	root := filepath.Join(t.TempDir(), "outside")
	for _, d := range []string{root, filepath.Join(root, "sub")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for p, mode := range map[string]os.FileMode{"keep.txt": 0o600, "sub/deep.txt": 0o400} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("keep "+p), mode); err != nil {
			t.Fatal(err)
		}
	}
	// Read-only, so that removing through the link would need a chmod.
	if err := os.Chmod(filepath.Join(root, "sub"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(root, 0o700)
		os.Chmod(filepath.Join(root, "sub"), 0o700)
	})
	return outsideTree{root}
}

// snapshot lists every path below the tree with its mode and content.
func (o outsideTree) snapshot(t *testing.T) []string {
	t.Helper()
	var out []string
	var walk func(rel string)
	walk = func(rel string) {
		full := filepath.Join(o.root, rel)
		fi, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("outside folder: %v", err)
		}
		desc := fmt.Sprintf("%s %v", rel, fi.Mode())
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			desc += " " + string(b)
		}
		out = append(out, desc)
		if fi.IsDir() {
			es, err := os.ReadDir(full)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range es {
				walk(filepath.Join(rel, e.Name()))
			}
		}
	}
	walk(".")
	sort.Strings(out)
	return out
}

func (o outsideTree) mustBeUntouched(t *testing.T, before []string) {
	t.Helper()
	if after := o.snapshot(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("the outside folder changed:\n%q\nwas\n%q", after, before)
	}
}

func TestRunRemovesWhatAGateLeftBehind(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	out := newOutside(t)
	before := out.snapshot(t)

	// The gate works in the scratch folder, which is the tree's parent. It
	// leaves a symlink to the outside folder, a read-only tree with a
	// read-only file, a folder nobody can enter, a FIFO and loops.
	script := `set -e
cd ..
ln -s "$1" link
mkdir -p ro/a/b
echo x > ro/a/b/f
chmod 0444 ro/a/b/f
ln -s "$1" ro/a/link
ln -s "$1/sub" ro/a/b/link2
ln -s .. ro/loop
chmod 0555 ro/a/b ro/a
mkfifo fifo
mkdir noperm
echo y > noperm/f
chmod 0000 noperm
ln -s /nonexistent dangling
chmod 0555 ro
echo planted`
	res, err := Run(context.Background(), request(m, work, fakePlanner(t), ExecRunner{},
		Gate{Name: "litter", Argv: []string{"/bin/sh", "-c", strings.ReplaceAll(script, "\n", "; "), "sh", out.root}}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || !strings.Contains(string(res.Output), "planted") {
		t.Fatalf("result: %+v\n%s", res, res.Output)
	}
	requireEmpty(t, work)
	out.mustBeUntouched(t, before)
}

// plant fills a folder with what hostile code could leave in it.
func plant(t *testing.T, dir string, outside outsideTree, depth int) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "ro", "a", "b"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "ro", "a", "b", "f"), []byte("x"), 0o400))
	must(os.WriteFile(filepath.Join(dir, "ro", "zero"), []byte("x"), 0o000))
	must(os.Symlink(outside.root, filepath.Join(dir, "ro", "a", "out")))
	must(os.Symlink(outside.root, filepath.Join(dir, "out")))
	must(os.Symlink(filepath.Join(outside.root, "sub"), filepath.Join(dir, "out-sub")))
	must(os.Symlink("..", filepath.Join(dir, "ro", "loop")))
	must(os.Symlink("/proc/self", filepath.Join(dir, "magic")))
	must(unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600))
	must(os.Chmod(filepath.Join(dir, "ro", "a", "b"), 0o500))
	must(os.Chmod(filepath.Join(dir, "ro", "a"), 0o000))
	must(os.Chmod(filepath.Join(dir, "ro"), 0o500))

	// A tree deeper than any path can be, built one step at a time from a
	// descriptor, with the last folder unreadable.
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	must(err)
	descend := func(name string) {
		must(unix.Mkdirat(fd, name, 0o700))
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		must(err)
		unix.Close(fd)
		fd = next
	}
	descend("deep")
	for i := 0; i < depth; i++ {
		must(unix.Symlinkat(outside.root, fd, "out"))
		descend("d")
	}
	must(unix.Fchmod(fd, 0o000))
	unix.Close(fd)
}

func TestRunRemovesHostileTreesFromBothFolders(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	out := newOutside(t)
	before := out.snapshot(t)
	p := fakePlanner(t)
	fr := &fakeRunner{}
	fr.hook = func(boundedexec.Cmd) {
		r := p.VerifyRequests()[0]
		plant(t, r.Scratch, out, 3000)
		plant(t, r.Home, out, 5)
	}
	res, err := Run(context.Background(), request(m, work, p, fr, sh("one", "true")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != 0 {
		t.Fatalf("result: %+v", res)
	}
	requireEmpty(t, work)
	out.mustBeUntouched(t, before)
}

func TestRunFolderThatTheSandboxReplacedWithASymlink(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	out := newOutside(t)
	before := out.snapshot(t)
	p := fakePlanner(t)
	fr := &fakeRunner{}
	fr.hook = func(boundedexec.Cmd) {
		r := p.VerifyRequests()[0]
		// Not possible for the sandbox (the work root is ours), but the
		// removal must take away the link and not follow it.
		if err := os.Remove(r.Home); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out.root, r.Home); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Run(context.Background(), request(m, work, p, fr, sh("one", "true"))); err != nil {
		t.Fatal(err)
	}
	requireEmpty(t, work)
	out.mustBeUntouched(t, before)
}

func TestRunReportsAFolderItCouldNotRemove(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	p := fakePlanner(t)
	fr := &fakeRunner{res: boundedexec.Result{Exit: 0}}
	fr.hook = func(boundedexec.Cmd) {
		r := p.VerifyRequests()[0]
		if err := os.WriteFile(filepath.Join(r.Scratch, "stuck"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	real := unlinkat
	unlinkat = func(fd int, path string, flags int) error {
		if path == "stuck" {
			return unix.EPERM
		}
		return real(fd, path, flags)
	}
	defer func() { unlinkat = real }()

	res, err := Run(context.Background(), request(m, work, p, fr, sh("one", "true")))
	if err == nil {
		t.Fatal("a leftover was not reported")
	}
	scratch := p.VerifyRequests()[0].Scratch
	if !strings.Contains(err.Error(), scratch) || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("the error does not name the folder: %v", err)
	}
	if res.Passed {
		t.Fatalf("a run that left a folder passed: %+v", res)
	}
	// The other folder is still removed.
	if got := entries(t, work); len(got) != 1 || got[0] != filepath.Base(scratch) {
		t.Fatalf("work root holds %v", got)
	}
}

// TestRemoveDeepTreeHoldsFewDescriptors lowers RLIMIT_NOFILE for the whole
// test binary, not for this test alone. No test of the package may run in
// parallel (t.Parallel), or it could fail for want of descriptors;
// TestNoTestRunsInParallel checks that.
func TestRemoveDeepTreeHoldsFewDescriptors(t *testing.T) {
	work := newWork(t)
	wr, err := openWorkRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer wr.close()
	name, err := wr.makeDir("scratch")
	if err != nil {
		t.Fatal(err)
	}
	plant(t, filepath.Join(work, name), newOutside(t), 5000)

	// With room for only a few hundred descriptors, a removal that held one
	// per level would fail on this tree.
	var old unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &old); err != nil {
		t.Fatal(err)
	}
	low := unix.Rlimit{Cur: 400, Max: old.Max}
	if old.Cur < low.Cur {
		low.Cur = old.Cur
	}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &low); err != nil {
		t.Fatal(err)
	}
	defer unix.Setrlimit(unix.RLIMIT_NOFILE, &old)
	if err := wr.removeDir(name); err != nil {
		t.Fatal(err)
	}
	requireEmpty(t, work)
	// Removing what is gone is not an error.
	if err := wr.removeDir(name); err != nil {
		t.Fatal(err)
	}
}

func TestShownQuotesAndShortensNamesFromTheSandbox(t *testing.T) {
	if got := shown("./a\nb\x1b[31m/\xff"); strings.ContainsAny(got, "\n\x1b\xff") || !strings.HasPrefix(got, `"`) {
		t.Fatalf("shown: %s", got)
	}
	if got := shown(strings.Repeat("d/", 5000)); len(got) > 300 || !strings.Contains(got, "...") {
		t.Fatalf("shown is %d bytes", len(got))
	}
}

// keepCreating makes every file removal create a new file in the same
// folder, as a process of the run that is still alive would. It stops after
// budget new files, so that a removal with no bound ends the test with no
// error and not with a hang. It returns the number of files created so far.
func keepCreating(t *testing.T, budget int, delay time.Duration) func() int {
	t.Helper()
	var mu sync.Mutex
	made := 0
	real := unlinkat
	unlinkat = func(fd int, path string, flags int) error {
		err := real(fd, path, flags)
		if err != nil || flags != 0 {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if made >= budget {
			return nil
		}
		made++
		if delay > 0 {
			time.Sleep(delay)
		}
		f, cerr := unix.Openat(fd, fmt.Sprintf("again-%d", made), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o600)
		if cerr != nil {
			return cerr
		}
		unix.Close(f)
		return nil
	}
	t.Cleanup(func() { unlinkat = real })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return made
	}
}

// setRemoveBounds changes the bounds of the removal for one test.
func setRemoveBounds(t *testing.T, passes int, timeout time.Duration) {
	t.Helper()
	oldPasses, oldTimeout := maxRemovePasses, removeTimeout
	maxRemovePasses, removeTimeout = passes, timeout
	t.Cleanup(func() { maxRemovePasses, removeTimeout = oldPasses, oldTimeout })
}

func plantFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveStopsWhenEntriesKeepAppearingPassLimit(t *testing.T) {
	setRemoveBounds(t, 20, time.Hour)
	created := keepCreating(t, 100000, 0)
	work := newWork(t)
	wr, err := openWorkRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer wr.close()
	name, err := wr.makeDir("scratch")
	if err != nil {
		t.Fatal(err)
	}
	plantFiles(t, filepath.Join(work, name), 5)

	err = wr.removeDir(name)
	if err == nil || !strings.Contains(err.Error(), "keep appearing") {
		t.Fatalf("removeDir: %v (created %d)", err, created())
	}
	if n := created(); n < 20 || n > 200 {
		t.Fatalf("the removal made %d passes' worth of files, want about 20 passes of 5", n)
	}
}

func TestRemoveStopsWhenEntriesKeepAppearingDeadline(t *testing.T) {
	setRemoveBounds(t, 1<<30, 150*time.Millisecond)
	// Each removal takes a millisecond, so the budget outlasts the deadline.
	created := keepCreating(t, 3000, time.Millisecond)
	work := newWork(t)
	wr, err := openWorkRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer wr.close()
	name, err := wr.makeDir("scratch")
	if err != nil {
		t.Fatal(err)
	}
	plantFiles(t, filepath.Join(work, name), 5)

	start := time.Now()
	err = wr.removeDir(name)
	if err == nil || !strings.Contains(err.Error(), "keep appearing") {
		t.Fatalf("removeDir: %v (created %d)", err, created())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the removal took %v", d)
	}
}

func TestRunReportsAFolderWhoseEntriesKeepAppearing(t *testing.T) {
	setRemoveBounds(t, 20, time.Hour)
	keepCreating(t, 100000, 0)
	m := newMirror(t)
	work := newWork(t)
	p := fakePlanner(t)
	fr := &fakeRunner{res: boundedexec.Result{Exit: 0}}
	fr.hook = func(boundedexec.Cmd) {
		plantFiles(t, p.VerifyRequests()[0].Scratch, 5)
	}
	res, err := Run(context.Background(), request(m, work, p, fr, sh("one", "true")))
	scratch := p.VerifyRequests()[0].Scratch
	if err == nil || !strings.Contains(err.Error(), scratch) || !strings.Contains(err.Error(), "keep appearing") {
		t.Fatalf("the error does not name the leftover folder: %v", err)
	}
	if res.Passed {
		t.Fatalf("a run that left a folder passed: %+v", res)
	}
	// The home folder had no files and is gone; the scratch folder is the leftover.
	if got := entries(t, work); len(got) != 1 || got[0] != filepath.Base(scratch) {
		t.Fatalf("work root holds %v", got)
	}
}
