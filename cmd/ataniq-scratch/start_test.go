package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// rig is a start in unit mode with a fake launcher whose "systemd-run" starts
// a fake control server at the epoch on the command line, as the holder
// would.
type rig struct {
	t  *testing.T
	e  *testEnv
	l  layout
	fl *fakeLauncher

	mu sync.Mutex
	h  *fakeHolder
}

func argAfter(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

func newRig(t *testing.T, name string) *rig {
	e := newTestEnv(t)
	r := &rig{t: t, e: e, l: e.layout(t, name), fl: &fakeLauncher{}}
	r.fl.runFn = func(argv []string) (int, error) {
		if argv[0] != "systemd-run" {
			return 0, nil
		}
		ep, err := strconv.ParseUint(argAfter(argv, "--epoch"), 10, 64)
		if err != nil {
			t.Errorf("no epoch in %q", argv)
			return 0, nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.h != nil {
			r.h.Close()
		}
		h, err := startFakeHolder(r.l.controlSock, r.l.daemonLock, "scratch/"+name, ep)
		if err != nil {
			t.Errorf("fake holder: %v", err)
			return 0, nil
		}
		r.h = h
		return 0, nil
	}
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.h != nil {
			r.h.Close()
		}
	})
	return r
}

func (r *rig) epochFile() string {
	b, _ := os.ReadFile(r.l.epochFile)
	return string(b)
}

// TestStartUnitModeKeepsDollarSignsInTheVector checks that $VAR and ${VAR} in
// the agent's argv and in an --env value reach the systemd-run vector
// unchanged, and that the vector tells the service manager not to expand them.
func TestStartUnitModeKeepsDollarSignsInTheVector(t *testing.T) {
	r := newRig(t, "demo")
	a, _, errout := r.e.newApp(r.fl)
	agent := []string{"sh", "-c", "echo $HOME ${X} $$"}
	code := a.run(append([]string{"start", "demo", "--env", "P=$HOME/x", "--env", "Q=${X}", "--holder", r.e.holderFile(t), "--"}, agent...))
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errout.String())
	}
	runs := r.fl.runList()
	if len(runs) != 1 {
		t.Fatalf("runs %q", runs)
	}
	argv := runs[0]
	expand, dash := -1, -1
	for i, s := range argv {
		if s == "--expand-environment=no" {
			expand = i
		}
		if s == "--" && dash < 0 {
			dash = i
		}
	}
	if expand < 0 || dash < 0 || expand > dash {
		t.Errorf("--expand-environment=no is not before --: %q", argv)
	}
	if tail := argv[len(argv)-len(agent):]; !reflect.DeepEqual(tail, agent) {
		t.Errorf("agent argv %q, want %q", tail, agent)
	}
	var envs []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--env" {
			envs = append(envs, argv[i+1])
		}
	}
	for _, want := range []string{"P=$HOME/x", "Q=${X}"} {
		found := false
		for _, e := range envs {
			found = found || e == want
		}
		if !found {
			t.Errorf("--env %q missing from %q", want, envs)
		}
	}
}

func TestStartUnitMode(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t)
	a, out, errout := r.e.newApp(r.fl)
	// A stale ticket from an earlier run is removed.
	os.MkdirAll(r.l.runtime, 0o700)
	os.WriteFile(r.l.ticketFile, []byte("old"), 0o600)

	code := a.run([]string{"start", "demo", "--cols", "120", "--rows", "40", "--env", "FOO=bar baz", "--holder", holder, "--", "agent", "--flag", "x"})
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errout.String())
	}
	runs := r.fl.runList()
	if len(runs) != 1 {
		t.Fatalf("runs %q", runs)
	}
	home := r.l.home
	want := []string{
		"systemd-run", "--user", "--unit=ataniq-scratch-demo", "--collect", "--quiet",
		"--expand-environment=no",
		"-p", "KillMode=control-group", "-p", "Restart=no", "-p", "TimeoutStopSec=2",
		"-p", "NoNewPrivileges=yes",
		"--", holder,
		"--scratch", "--role", "scratch/demo", "--epoch", "1", "--launch-intent", argAfter(runs[0], "--launch-intent"),
		"--runtime", "scratch", "--runtime-version", "0", "--profile-digest", "none",
		"--tree", home, "--state-dir", r.l.state, "--runtime-dir", r.l.runtime,
		"--role-lock", filepath.Join(r.l.state, "role.lock"), "--daemon-lock", filepath.Join(r.l.state, "daemon.lock"),
		"--cols", "120", "--rows", "40",
		"--env", "TERM=xterm-256color", "--env", "HOME=" + home, "--env", "PATH=/usr/local/bin:/usr/bin:/bin",
		"--env", "LANG=de_DE.UTF-8", "--env", "FOO=bar baz",
		"--", "agent", "--flag", "x",
	}
	if !reflect.DeepEqual(runs[0], want) {
		t.Errorf("systemd-run argv:\n got %q\nwant %q", runs[0], want)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(argAfter(runs[0], "--launch-intent")) {
		t.Errorf("launch intent %q", argAfter(runs[0], "--launch-intent"))
	}
	for _, s := range runs[0] {
		if strings.Contains(s, "must-not-reach-the-agent") || strings.Contains(s, "SECRET_INHERIT") {
			t.Errorf("the caller's environment leaked into %q", s)
		}
	}
	for _, line := range []string{
		"name:        demo", "role:        scratch/demo", "epoch:       1",
		"runtime dir: " + r.l.runtime, "state dir:   " + r.l.state, "ataniq-scratch-demo", "scratch only",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, out.String())
		}
	}
	if strings.Contains(out.String(), "weaker") {
		t.Error("the unit mode says its process-tree proof is weaker")
	}
	for _, p := range []string{r.l.runtime, r.l.state, r.l.home} {
		if m := modeOf(t, p); m.Perm() != 0o700 {
			t.Errorf("%s: %v", p, m)
		}
	}
	if b, _ := os.ReadFile(r.l.epochFile); string(b) != "1\n" {
		t.Errorf("epoch file %q", b)
	}
	if m := modeOf(t, r.l.epochFile); m.Perm() != 0o600 {
		t.Errorf("epoch file mode %v", m)
	}
	if _, err := os.Lstat(r.l.ticketFile); err == nil {
		t.Error("the stale ticket file was kept")
	}
	if got := r.h.requests(); len(got) != 1 || got[0].Op != "status" || got[0].Epoch != 1 {
		t.Errorf("the holder saw %+v", got)
	}

	// The next start has the next epoch and a fresh launch intent.
	a2, out2, errout2 := r.e.newApp(r.fl)
	if code := a2.run([]string{"start", "demo", "--holder", holder, "--", "agent"}); code != exitOK {
		t.Fatalf("second start: exit %d, stderr %q", code, errout2.String())
	}
	runs = r.fl.runList()
	if got := argAfter(runs[1], "--epoch"); got != "2" || !strings.Contains(out2.String(), "epoch:       2") {
		t.Errorf("second epoch %q, output %q", got, out2.String())
	}
	if argAfter(runs[1], "--launch-intent") == argAfter(runs[0], "--launch-intent") {
		t.Error("the launch intent was reused")
	}
	if argAfter(runs[1], "--cols") != "100" || argAfter(runs[1], "--rows") != "30" {
		t.Error("default size is not 100 by 30")
	}
}

func TestStartLangFromTheCaller(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{{"", "LANG=C.UTF-8"}, {"fr_FR.UTF-8", "LANG=fr_FR.UTF-8"}, {"bad lang", "LANG=C.UTF-8"}} {
		r := newRig(t, "demo")
		r.e.vars["LANG"] = tc.lang
		a, _, errout := r.e.newApp(r.fl)
		if code := a.run([]string{"start", "demo", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitOK {
			t.Fatalf("exit %d %q", code, errout.String())
		}
		argv := r.fl.runList()[0]
		var langs []string
		for i, s := range argv {
			if strings.HasPrefix(s, "LANG=") && argv[i-1] == "--env" {
				langs = append(langs, s)
			}
		}
		if !reflect.DeepEqual(langs, []string{tc.want}) {
			t.Errorf("LANG %q: got %q, want %q", tc.lang, langs, tc.want)
		}
	}
}

func TestStartUsesTheHolderNextToTheExecutable(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t) // root/ataniq-holder; the app's executable is root/ataniq-scratch
	a, _, errout := r.e.newApp(r.fl)
	if code := a.run([]string{"start", "demo", "--", "agent"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	// The holder is the word after the first "--" of the systemd-run vector.
	vec := r.fl.runList()[0]
	got := ""
	for i, s := range vec {
		if s == "--" {
			got = vec[i+1]
			break
		}
	}
	if got != holder {
		t.Errorf("holder %q, want %q", got, holder)
	}
}

func TestStartRefusesAHolderThatIsNotUsable(t *testing.T) {
	r := newRig(t, "demo")
	dir := filepath.Join(r.e.root, "dir")
	os.Mkdir(dir, 0o700)
	plain := filepath.Join(r.e.root, "plain")
	os.WriteFile(plain, []byte("x"), 0o644)
	for name, args := range map[string][]string{
		"missing":      {"--holder", filepath.Join(r.e.root, "absent")},
		"a folder":     {"--holder", dir},
		"not execable": {"--holder", plain},
		"default gone": nil, // no ataniq-holder next to the executable
	} {
		a, _, errout := r.e.newApp(r.fl)
		argv := append(append([]string{"start", "demo"}, args...), "--", "agent")
		if code := a.run(argv); code != exitFail {
			t.Errorf("%s: exit %d", name, code)
		}
		if !strings.HasPrefix(errout.String(), "error: ") {
			t.Errorf("%s: stderr %q", name, errout.String())
		}
	}
	if len(r.fl.runList()) != 0 {
		t.Error("something was started")
	}
	if _, err := os.Stat(r.l.epochFile); err == nil {
		t.Error("an epoch was taken for a start that could not begin")
	}
}

// TestRuntimeErrorsNeverPrintTheHolderArgument gives --holder a path with a
// control byte and a marker, and checks that no runtime error repeats it.
func TestRuntimeErrorsNeverPrintTheHolderArgument(t *testing.T) {
	const marker = "HOLDERARG"
	r := newRig(t, "demo")
	odd := filepath.Join(r.e.root, "\x1b[31m"+marker)
	plain := odd + "-plain"
	os.WriteFile(plain, []byte("x"), 0o644)
	exe := odd + "-exe"
	os.WriteFile(exe, []byte("x"), 0o755)
	check := func(what string, args []string, setup func()) {
		t.Helper()
		if setup != nil {
			setup()
		}
		a, _, errout := r.e.newApp(r.fl)
		if code := a.run(append([]string{"start", "demo"}, append(args, "--", "agent")...)); code != exitFail {
			t.Errorf("%s: exit %d, stderr %q", what, code, errout.String())
		}
		if strings.Contains(errout.String(), marker) || strings.Contains(errout.String(), "\x1b") {
			t.Errorf("%s: stderr repeats the holder argument: %q", what, errout.String())
		}
		if errout.String() == "" {
			t.Errorf("%s: no error printed", what)
		}
	}
	check("missing", []string{"--holder", odd}, nil)
	check("not executable", []string{"--holder", plain}, nil)
	r.fl.startFn = func(argv, env []string) (child, error) {
		return nil, &fs.PathError{Op: "fork/exec", Path: argv[0], Err: os.ErrPermission}
	}
	check("direct start fails", []string{"--direct", "--holder", exe}, nil)
}

func TestStartSystemdRunFailureGivesTheEpochBack(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t)
	fail := true
	inner := r.fl.runFn
	r.fl.runFn = func(argv []string) (int, error) {
		if fail {
			return 1, nil
		}
		return inner(argv)
	}
	a, out, errout := r.e.newApp(r.fl)
	if code := a.run([]string{"start", "demo", "--holder", holder, "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout.String(), "ataniq-scratch-demo") || strings.Contains(out.String(), "started") {
		t.Errorf("stderr %q stdout %q", errout.String(), out.String())
	}
	if got := r.epochFile(); got != "" {
		t.Errorf("epoch file %q after a failed first start", got)
	}
	fail = false
	a2, _, errout2 := r.e.newApp(r.fl)
	if code := a2.run([]string{"start", "demo", "--holder", holder, "--", "agent"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout2.String())
	}
	if got := argAfter(r.fl.runList()[1], "--epoch"); got != "1" {
		t.Errorf("epoch %q after a failed start, want 1", got)
	}
}

func TestStartSystemdRunThatCannotRun(t *testing.T) {
	r := newRig(t, "demo")
	r.fl.runFn = func([]string) (int, error) { return -1, os.ErrNotExist }
	a, _, errout := r.e.newApp(r.fl)
	if code := a.run([]string{"start", "demo", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail || !strings.Contains(errout.String(), "systemd-run") {
		t.Errorf("exit %d stderr %q", code, errout.String())
	}
	if r.epochFile() != "" {
		t.Error("epoch not given back")
	}
}

func TestStartHolderThatNeverAnswersInUnitMode(t *testing.T) {
	r := newRig(t, "demo")
	r.fl.runFn = func([]string) (int, error) { return 0, nil } // the unit starts, nothing listens
	a, _, errout := r.e.newApp(r.fl)
	a.lim.startWait = 100 * time.Millisecond
	begin := time.Now()
	if code := a.run([]string{"start", "demo", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if time.Since(begin) < 90*time.Millisecond {
		t.Error("gave up early")
	}
	if !strings.Contains(errout.String(), "ataniq-scratch stop demo") {
		t.Errorf("stderr %q", errout.String())
	}
	// The unit may be running with this epoch, so it is kept.
	if r.epochFile() != "1\n" {
		t.Errorf("epoch file %q", r.epochFile())
	}
}

func TestStartHolderThatRefuses(t *testing.T) {
	r := newRig(t, "demo")
	r.fl.runFn = func(argv []string) (int, error) { // a holder of another epoch is answering
		h, err := startFakeHolder(r.l.controlSock, r.l.daemonLock, "scratch/demo", 99)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		r.h = h
		return 0, nil
	}
	a, out, errout := r.e.newApp(r.fl)
	if code := a.run([]string{"start", "demo", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout.String(), "wrong-epoch") || strings.Contains(out.String(), "started") {
		t.Errorf("stderr %q stdout %q", errout.String(), out.String())
	}
}

func TestConcurrentStartsNeverReuseAnEpoch(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t)
	const n = 4
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, _, _ := r.e.newApp(r.fl)
			a.lim.lockWait = 20 * time.Second
			codes[i] = a.run([]string{"start", "demo", "--holder", holder, "--", "agent"})
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, argv := range r.fl.runList() {
		ep := argAfter(argv, "--epoch")
		if seen[ep] {
			t.Errorf("epoch %s used twice", ep)
		}
		seen[ep] = true
	}
	for i, c := range codes {
		if c != exitOK {
			t.Errorf("start %d: exit %d", i, c)
		}
	}
	if len(seen) != n || r.epochFile() != strconv.Itoa(n)+"\n" {
		t.Errorf("epochs %v, file %q", seen, r.epochFile())
	}
}

// directRig is a --direct start whose fake launcher starts a fake child and,
// when serve is set, a fake control server as the holder would.
func directRig(t *testing.T, serve bool, code int) (*rig, *fakeChild) {
	r := newRig(t, "demo")
	ch := newFakeChild(code)
	r.fl.startFn = func(argv, env []string) (child, error) {
		if serve {
			h, err := startFakeHolder(r.l.controlSock, r.l.daemonLock, "scratch/demo", 1)
			if err != nil {
				t.Error(err)
			}
			r.mu.Lock()
			r.h = h
			r.mu.Unlock()
		}
		return ch, nil
	}
	return r, ch
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestStartDirectRunsInTheForeground(t *testing.T) {
	for _, tc := range []struct {
		childCode int
		want      int
	}{{0, exitOK}, {5, exitFail}, {-1, exitFail}} {
		r, ch := directRig(t, true, tc.childCode)
		holder := r.e.holderFile(t)
		a, out, errout := r.e.newApp(r.fl)
		done := make(chan int, 1)
		go func() {
			done <- a.run([]string{"start", "demo", "--direct", "--env", "A=1", "--holder", holder, "--", "agent"})
		}()
		waitFor(t, "the start output", func() bool { return strings.Contains(out.String(), "weaker") })
		select {
		case c := <-done:
			t.Fatalf("start returned %d while the holder runs", c)
		case <-time.After(30 * time.Millisecond):
		}
		ch.finish()
		if got := <-done; got != tc.want {
			t.Errorf("child status %d: exit %d, want %d (stderr %q)", tc.childCode, got, tc.want, errout.String())
		}
		for _, s := range []string{"name:        demo", "epoch:       1", "direct child", "process-tree proof is the weaker one"} {
			if !strings.Contains(out.String(), s) {
				t.Errorf("output lacks %q:\n%s", s, out.String())
			}
		}
		if len(r.fl.runList()) != 0 {
			t.Errorf("direct mode ran %q", r.fl.runList())
		}
		starts := r.fl.starts
		if len(starts) != 1 || starts[0][0] != holder || argAfter(starts[0], "--epoch") != "1" {
			t.Errorf("starts %q", starts)
		}
		// The holder itself gets a minimal environment, not the caller's.
		if want := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=de_DE.UTF-8"}; !reflect.DeepEqual(r.fl.envs[0], want) {
			t.Errorf("holder env %q, want %q", r.fl.envs[0], want)
		}
	}
}

func TestStartDirectHolderEndsBeforeItAnswers(t *testing.T) {
	r, ch := directRig(t, false, 3)
	ch.finish()
	a, _, errout := r.e.newApp(r.fl)
	if code := a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout.String(), "exit status 3") {
		t.Errorf("stderr %q", errout.String())
	}
	if r.epochFile() != "" {
		t.Errorf("epoch kept: %q", r.epochFile())
	}
}

func TestStartDirectHolderThatStaysSilentIsEnded(t *testing.T) {
	r, ch := directRig(t, false, -1)
	a, _, errout := r.e.newApp(r.fl)
	a.lim.startWait = 80 * time.Millisecond
	if code := a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if len(ch.signaled) == 0 || ch.signaled[0] != syscall.SIGTERM {
		t.Errorf("signals %v, want SIGTERM first", ch.signaled)
	}
	if !strings.Contains(errout.String(), "did not answer") || r.epochFile() != "" {
		t.Errorf("stderr %q, epoch %q", errout.String(), r.epochFile())
	}
}

func TestStartDirectCannotStartTheHolder(t *testing.T) {
	r := newRig(t, "demo")
	r.fl.startFn = func(argv, env []string) (child, error) { return nil, os.ErrPermission }
	a, _, errout := r.e.newApp(r.fl)
	if code := a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout.String(), "cannot start the holder") || r.epochFile() != "" {
		t.Errorf("stderr %q, epoch %q", errout.String(), r.epochFile())
	}
}

// TestDirectWithTheTestBinaryAsHolder runs the whole conversation against a
// real child process: this test binary, re-executed with the holder's command
// line. It starts no unit and no real holder.
func TestDirectWithTheTestBinaryAsHolder(t *testing.T) {
	e := newTestEnv(t)
	l := e.layout(t, "dh")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fl := &fakeLauncher{realStart: true, runFn: func(argv []string) (int, error) {
		if argv[1] == "--user" && argv[2] == "is-active" {
			return 3, nil // no unit
		}
		t.Errorf("unexpected run %q", argv)
		return 0, nil
	}}
	a, out, errout := e.newApp(fl)
	done := make(chan int, 1)
	go func() {
		done <- a.run([]string{"start", "dh", "--direct", "--cols", "90", "--env", "ONE=1", "--holder", self, "--", "myagent", "-x"})
	}()
	waitFor(t, "the start output", func() bool { return strings.Contains(out.String(), "weaker") })

	cmd := func(args ...string) (int, string, string) {
		ca, cout, cerr := e.newApp(fl)
		code := ca.run(args)
		return code, cout.String(), cerr.String()
	}
	code, so, se := cmd("status", "dh")
	if code != exitOK || !strings.Contains(so, "lead:           scratch/dh") || !strings.Contains(so, "epoch:          1") {
		t.Fatalf("status: %d %q %q", code, so, se)
	}
	code, path, se := cmd("ticket", "dh")
	path = strings.TrimSpace(path)
	if code != exitOK || path != l.ticketFile {
		t.Fatalf("ticket: %d %q %q", code, path, se)
	}
	if code, _, se := cmd("ticket", "dh"); code != exitFail || !strings.Contains(se, "busy") {
		t.Errorf("second writer ticket: %d %q", code, se)
	}
	if code, _, se := cmd("ticket", "dh", "--viewer"); code != exitOK {
		t.Errorf("viewer ticket: %d %q", code, se)
	}
	if code, so, _ := cmd("release-writer", "dh"); code != exitOK || !strings.Contains(so, "released") {
		t.Errorf("release-writer: %d %q", code, so)
	}
	if code, _, se := cmd("ticket", "dh"); code != exitOK {
		t.Errorf("writer ticket after the release: %d %q", code, se)
	}
	if code, so, se := cmd("stop", "dh"); code != exitOK || !strings.Contains(so, "asked to stop") {
		t.Errorf("stop: %d %q %q", code, so, se)
	}
	select {
	case c := <-done:
		if c != exitOK {
			t.Errorf("start exit %d, stderr %q", c, errout.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("start did not return after the holder was stopped")
	}

	// What the child saw.
	b, err := os.ReadFile(filepath.Join(l.runtime, "fake-holder.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if argAfter(rec.Args, "--role") != "scratch/dh" || argAfter(rec.Args, "--cols") != "90" || argAfter(rec.Args, "--daemon-lock") != l.daemonLock {
		t.Errorf("holder args %q", rec.Args)
	}
	if tail := rec.Args[len(rec.Args)-3:]; !reflect.DeepEqual(tail, []string{"--", "myagent", "-x"}) {
		t.Errorf("agent argv %q", tail)
	}
	if !reflect.DeepEqual(rec.Env, []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=de_DE.UTF-8"}) {
		t.Errorf("holder env %q", rec.Env)
	}
	if got := argsOf(rec.Args, "--env"); !reflect.DeepEqual(got, []string{"TERM=xterm-256color", "HOME=" + l.home, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=de_DE.UTF-8", "ONE=1"}) {
		t.Errorf("agent env %q", got)
	}
}

func argsOf(argv []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			out = append(out, argv[i+1])
		}
	}
	return out
}

func TestDirectWithAHolderThatExitsAtOnce(t *testing.T) {
	e := newTestEnv(t)
	self, _ := os.Executable()
	fl := &fakeLauncher{realStart: true}
	a, _, errout := e.newApp(fl)
	if code := a.run([]string{"start", "dx", "--direct", "--holder", self, "--", "exit3"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout.String(), "exit status 3") {
		t.Errorf("stderr %q", errout.String())
	}
	if b, _ := os.ReadFile(e.layout(t, "dx").epochFile); len(b) != 0 {
		t.Errorf("epoch file %q", b)
	}
}

func TestDirectWithASilentHolderIsEnded(t *testing.T) {
	e := newTestEnv(t)
	self, _ := os.Executable()
	fl := &fakeLauncher{realStart: true}
	a, _, errout := e.newApp(fl)
	a.lim.startWait = 300 * time.Millisecond
	if code := a.run([]string{"start", "ds", "--direct", "--holder", self, "--", "silent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout.String(), "did not answer") {
		t.Errorf("stderr %q", errout.String())
	}
}

// stubbornChild is a direct holder that ignores every signal.
type stubbornChild struct {
	mu       sync.Mutex
	signaled []os.Signal
}

func (c *stubbornChild) Wait() (int, error) { select {} }
func (c *stubbornChild) Signal(s os.Signal) error {
	c.mu.Lock()
	c.signaled = append(c.signaled, s)
	c.mu.Unlock()
	return nil
}

// killOnlyChild is a direct holder that ignores SIGTERM and ends on SIGKILL.
type killOnlyChild struct{ *fakeChild }

func (c *killOnlyChild) Signal(s os.Signal) error {
	if s == syscall.SIGKILL {
		return c.fakeChild.Signal(s)
	}
	c.mu.Lock()
	c.signaled = append(c.signaled, s)
	c.mu.Unlock()
	return nil
}

// interruptedStart runs a --direct start whose holder never answers and whose
// signal channel already holds a SIGINT, so that the interrupt arrives while
// start waits for the holder.
func interruptedStart(t *testing.T, r *rig, ch child) (code int, errout string) {
	t.Helper()
	r.fl.startFn = func(argv, env []string) (child, error) { return ch, nil }
	a, _, eb := r.e.newApp(r.fl)
	a.lim.termWait = 50 * time.Millisecond
	sigs := make(chan os.Signal, 4)
	sigs <- syscall.SIGINT
	stopped := false
	a.signals = func() (<-chan os.Signal, func()) { return sigs, func() { stopped = true } }
	code = a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "agent"})
	if !stopped {
		t.Error("the signal subscription was not stopped")
	}
	return code, eb.String()
}

func TestStartDirectInterruptedSaysTheHolderWasEnded(t *testing.T) {
	r := newRig(t, "demo")
	ch := newFakeChild(-1)
	code, errout := interruptedStart(t, r, ch)
	if code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout, "interrupted") || !strings.Contains(errout, "the holder was ended") {
		t.Errorf("stderr %q", errout)
	}
	if strings.Contains(errout, "not running") || strings.Contains(errout, "outlive") {
		t.Errorf("stderr %q claims too much or too little", errout)
	}
	if len(ch.signaled) == 0 || ch.signaled[0] != syscall.SIGTERM {
		t.Errorf("signals %v, want SIGTERM first", ch.signaled)
	}
	if r.epochFile() != "" {
		t.Errorf("epoch kept after the holder ended: %q", r.epochFile())
	}
}

func TestStartDirectInterruptedSaysTheHolderMayStillRun(t *testing.T) {
	r := newRig(t, "demo")
	ch := &stubbornChild{}
	code, errout := interruptedStart(t, r, ch)
	if code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout, "interrupted") || !strings.Contains(errout, "may still be running") {
		t.Errorf("stderr %q", errout)
	}
	if len(ch.signaled) != 2 || ch.signaled[0] != syscall.SIGTERM || ch.signaled[1] != syscall.SIGKILL {
		t.Errorf("signals %v, want SIGTERM then SIGKILL", ch.signaled)
	}
	if r.epochFile() != "1\n" {
		t.Errorf("epoch %q, want it kept while the holder may run", r.epochFile())
	}
}

func TestStartDirectInterruptedAfterSigkillSaysTheAgentMayOutliveIt(t *testing.T) {
	r := newRig(t, "demo")
	ch := &killOnlyChild{newFakeChild(-1)}
	code, errout := interruptedStart(t, r, ch)
	if code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout, "the holder was ended") || !strings.Contains(errout, "agent may outlive it") {
		t.Errorf("stderr %q", errout)
	}
	if strings.Contains(errout, "not running") {
		t.Errorf("stderr %q says the holder is not running", errout)
	}
	if len(ch.signaled) != 2 || ch.signaled[1] != syscall.SIGKILL {
		t.Errorf("signals %v, want SIGTERM then SIGKILL", ch.signaled)
	}
	if r.epochFile() != "" {
		t.Errorf("epoch kept after the holder ended: %q", r.epochFile())
	}
}

// stubbornStart runs a --direct start with a holder that ignores every signal
// and either answers with a refusal (wrong epoch) or stays silent.
func stubbornStart(t *testing.T, refuse bool) (r *rig, ch *stubbornChild, code int, errout string) {
	t.Helper()
	r = newRig(t, "demo")
	ch = &stubbornChild{}
	r.fl.startFn = func(argv, env []string) (child, error) {
		if refuse {
			h, err := startFakeHolder(r.l.controlSock, r.l.daemonLock, "scratch/demo", 99)
			if err != nil {
				t.Error(err)
			}
			r.mu.Lock()
			r.h = h
			r.mu.Unlock()
		}
		return ch, nil
	}
	a, _, eb := r.e.newApp(r.fl)
	a.lim.termWait = 50 * time.Millisecond
	a.lim.startWait = 100 * time.Millisecond
	code = a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "agent"})
	return r, ch, code, eb.String()
}

func TestStartDirectRefusedWithAHolderThatWillNotEndKeepsTheEpoch(t *testing.T) {
	r, ch, code, errout := stubbornStart(t, true)
	if code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout, "wrong-epoch") || !strings.Contains(errout, "not seen to end") || !strings.Contains(errout, "may still be running") {
		t.Errorf("stderr %q", errout)
	}
	if strings.Contains(errout, "another holder may be running") {
		t.Errorf("stderr %q blames another holder", errout)
	}
	if len(ch.signaled) != 2 || ch.signaled[0] != syscall.SIGTERM || ch.signaled[1] != syscall.SIGKILL {
		t.Errorf("signals %v, want SIGTERM then SIGKILL", ch.signaled)
	}
	if r.epochFile() != "1\n" {
		t.Errorf("epoch %q, want it kept while the holder may run", r.epochFile())
	}
}

func TestStartDirectTimeoutWithAHolderThatWillNotEndKeepsTheEpoch(t *testing.T) {
	r, ch, code, errout := stubbornStart(t, false)
	if code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errout, "did not answer") || !strings.Contains(errout, "not seen to end") || !strings.Contains(errout, "may still be running") {
		t.Errorf("stderr %q", errout)
	}
	if strings.Contains(errout, "it was ended") {
		t.Errorf("stderr %q says the holder was ended", errout)
	}
	if len(ch.signaled) != 2 || ch.signaled[0] != syscall.SIGTERM || ch.signaled[1] != syscall.SIGKILL {
		t.Errorf("signals %v, want SIGTERM then SIGKILL", ch.signaled)
	}
	if r.epochFile() != "1\n" {
		t.Errorf("epoch %q, want it kept while the holder may run", r.epochFile())
	}
}

func TestStartDirectTimeoutAfterSigkillSaysTheAgentMayOutliveIt(t *testing.T) {
	r := newRig(t, "demo")
	ch := &killOnlyChild{newFakeChild(-1)}
	r.fl.startFn = func(argv, env []string) (child, error) { return ch, nil }
	a, _, eb := r.e.newApp(r.fl)
	a.lim.termWait = 50 * time.Millisecond
	a.lim.startWait = 100 * time.Millisecond
	if code := a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "agent"}); code != exitFail {
		t.Fatalf("exit %d", code)
	}
	if e := eb.String(); !strings.Contains(e, "it was ended") || !strings.Contains(e, "agent may outlive it") {
		t.Errorf("stderr %q", e)
	}
	if r.epochFile() != "" {
		t.Errorf("epoch kept after the holder ended: %q", r.epochFile())
	}
}
