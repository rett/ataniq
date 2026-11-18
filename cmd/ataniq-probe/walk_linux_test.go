//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/mountpoints"
)

// The tests of the walks run the test binary again as the probe in its
// pass-through mode, with the test binary once more as the command, on
// folders under t.TempDir(). No sandbox tool is started and no namespace is
// made: the command is the test agent, which reports the descriptors it got.

// walkFixture is a state root and a folder of Ataniq's own, as the plan's
// walks name them: sources below the state root are walked from it, any other
// source from the folder that holds it.
type walkFixture struct {
	base, state, owned, outside string
	home, gitmeta, config       string // below the state root
	skills, settings, fifo      string // in the owned folder
}

func newWalkFixture(t *testing.T) walkFixture {
	t.Helper()
	// A link above the fixture would be refused like any other.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := walkFixture{base: base, state: filepath.Join(base, "state"), owned: filepath.Join(base, "owned"), outside: filepath.Join(base, "outside")}
	f.home = filepath.Join(f.state, "leads/a-lead/home")
	f.gitmeta = filepath.Join(f.state, "leads/a-lead/gitmeta")
	f.config = filepath.Join(f.gitmeta, "config")
	f.skills = filepath.Join(f.owned, "skills")
	f.settings = filepath.Join(f.owned, "settings.json")
	f.fifo = filepath.Join(f.owned, "a.fifo")
	for _, d := range []string{f.home, f.gitmeta, f.skills, f.outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for p, data := range map[string]string{f.config: "[core]\n", f.settings: "{}", filepath.Join(f.outside, "secret"): "secret"} {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(f.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// walkOpt is one walk option: the folder the walk starts from and the path
// beneath it.
func walkOpt(dir bool, fd int, root, rel string) []string {
	opt := optWalkFile
	if dir {
		opt = optWalkDir
	}
	return []string{opt, strconv.Itoa(fd), root, rel}
}

// below is the path of p beneath dir.
func below(t *testing.T, dir, p string) string {
	t.Helper()
	rel, err := filepath.Rel(dir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("%s is not below %s", p, dir)
	}
	return rel
}

func identity(t *testing.T, p string) (dev, ino uint64) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Dev), uint64(st.Ino)
}

// wantWalked checks one descriptor of the agent against the source it must
// name: an O_PATH descriptor at the number, inherited, on that very object.
func wantWalked(t *testing.T, a agentReport, fd int, source string, dir bool) {
	t.Helper()
	got, ok := fdByNumber(a, fd)
	if !ok {
		t.Errorf("descriptor %d is missing: %+v", fd, a.FDs)
		return
	}
	if got.Cloexec {
		t.Errorf("descriptor %d is close-on-exec", fd)
	}
	if got.Flags&unix.O_PATH == 0 {
		t.Errorf("descriptor %d has flags %#o, want an O_PATH descriptor", fd, got.Flags)
	}
	dev, ino := identity(t, source)
	if got.Dev != dev || got.Ino != ino {
		t.Errorf("descriptor %d names %d:%d (%s), want %s (%d:%d)", fd, got.Dev, got.Ino, got.Link, source, dev, ino)
	}
	want := uint32(unix.S_IFREG)
	if dir {
		want = unix.S_IFDIR
	}
	if got.Mode&unix.S_IFMT != want {
		t.Errorf("descriptor %d has mode %#o, want type %#o", fd, got.Mode, want)
	}
}

func TestPassFDsWalksPlaceTheSourcesAtTheirNumbers(t *testing.T) {
	f := newWalkFixture(t)
	write := filepath.Join(f.owned, "report.txt")
	if err := os.WriteFile(write, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	type want struct {
		source string
		dir    bool
	}
	home, config := below(t, f.state, f.home), below(t, f.state, f.config)
	for _, c := range []struct {
		name  string
		opts  [][]string
		walks map[int]want
		files int // the --read and --write descriptors among the options
		leak  int
	}{
		{"a folder and a file below the state root, one folder opened for both",
			[][]string{walkOpt(true, 10, f.state, home), walkOpt(false, 11, f.state, config)},
			map[int]want{10: {f.home, true}, 11: {f.config, false}}, 0, 3},
		{"sources walked from the folders that hold them",
			[][]string{walkOpt(true, 12, f.owned, "skills"), walkOpt(false, 13, f.owned, "settings.json"), walkOpt(true, 14, f.state, home)},
			map[int]want{12: {f.skills, true}, 13: {f.settings, false}, 14: {f.home, true}}, 0, 1},
		{"the folder itself", [][]string{walkOpt(true, 9, f.skills, ".")}, map[int]want{9: {f.skills, true}}, 0, 0},
		{"the numbers the opens land on, swapped",
			[][]string{walkOpt(true, 4, f.state, home), walkOpt(false, 3, f.state, config)},
			map[int]want{4: {f.home, true}, 3: {f.config, false}}, 0, 2},
		{"a chain of numbers",
			[][]string{walkOpt(true, 5, f.state, home), walkOpt(false, 3, f.state, config), walkOpt(true, 4, f.owned, "skills")},
			map[int]want{5: {f.home, true}, 3: {f.config, false}, 4: {f.skills, true}}, 0, 0},
		{"with the files of the other options, in any order",
			[][]string{{"--write", "4=" + write}, walkOpt(true, 3, f.state, home), {"--read", "7=" + f.settings}, walkOpt(false, 1023, f.owned, "settings.json")},
			map[int]want{3: {f.home, true}, 1023: {f.settings, false}}, 2, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			var opts []string
			for _, o := range c.opts {
				opts = append(opts, o...)
			}
			r := startPass(t, runOpts{mode: modeFake, leak: c.leak}, opts...)
			if code := r.wait(); code != 0 {
				t.Fatalf("exit %d: %s", code, r.stderr)
			}
			a := r.agent()
			for fd, w := range c.walks {
				wantWalked(t, a, fd, w.source, w.dir)
			}
			// Nothing else is inherited: neither a descriptor the probe was
			// started with, nor a folder a walk started from, nor a copy.
			if len(a.FDs) != len(c.walks)+c.files {
				t.Errorf("the command has %d descriptors above 2, want the %d asked for: %+v", len(a.FDs), len(c.walks)+c.files, a.FDs)
			}
			if !slices.Equal(sorted(a.Env), sorted(r.env)) {
				t.Errorf("environment %q, want %q", a.Env, r.env)
			}
		})
	}
}

// The cap is hard and is enough for the whole table: 128 descriptors are
// placed, each at its number, and nothing else reaches the command.
func TestPassFDsWalksUpToTheCap(t *testing.T) {
	f := newWalkFixture(t)
	write := filepath.Join(f.owned, "report.txt")
	if err := os.WriteFile(write, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := []string{"--read", "3=" + f.settings, "--write", "4=" + write}
	sources := map[int]string{}
	for i := 0; i < maxPassFDs-2; i++ {
		dir := filepath.Join(f.state, "rows", strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		fd := 10 + i
		sources[fd] = dir
		opts = append(opts, walkOpt(true, fd, f.state, below(t, f.state, dir))...)
	}
	r := startPass(t, runOpts{mode: modeFake, leak: 2}, opts...)
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d: %s", code, r.stderr)
	}
	a := r.agent()
	if len(a.FDs) != maxPassFDs {
		t.Fatalf("the command has %d descriptors above 2, want %d", len(a.FDs), maxPassFDs)
	}
	for fd, dir := range sources {
		wantWalked(t, a, fd, dir, true)
	}
	// One more is refused before anything is opened.
	r = startPass(t, runOpts{mode: modeFake}, append(opts, walkOpt(true, 900, f.owned, "skills")...)...)
	if code := r.wait(); code != exitSetup {
		t.Errorf("exit %d, want %d", code, exitSetup)
	}
	if !strings.Contains(r.stderr.String(), "more than 128 descriptors") {
		t.Errorf("stderr %q", r.stderr)
	}
	r.noAgent()
}

// Every refusal of a walk is exit 96 with one line that names the walk, and
// the command is not started.
func TestPassFDsWalkRefusals(t *testing.T) {
	f := newWalkFixture(t)
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	lead := filepath.Join(f.state, "leads/a-lead")
	// Links a lead could plant, each where a source is expected.
	link(f.outside, filepath.Join(lead, "linked-out"))                    // the last component, to a folder outside
	link("home", filepath.Join(lead, "linked-in"))                        // the last component, to a folder inside
	link("nowhere", filepath.Join(lead, "dangling"))                      // the last component, to nothing
	link(filepath.Join(f.outside, "secret"), filepath.Join(lead, "file")) // a file that is a link
	link(f.outside, filepath.Join(f.state, "leads/moved"))                // a middle component
	link(f.state, filepath.Join(f.base, "state-link"))                    // the folder the walk starts from
	if err := os.MkdirAll(filepath.Join(f.outside, "a-lead/home"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		opt  []string
		want []string
	}{
		{"the last component is a link to a folder outside", walkOpt(true, 10, f.state, "leads/a-lead/linked-out"),
			[]string{"symbolic link", `"leads/a-lead/linked-out"`, "never followed"}},
		{"the last component is a link to a folder inside", walkOpt(true, 10, f.state, "leads/a-lead/linked-in"),
			[]string{"symbolic link", `"leads/a-lead/linked-in"`}},
		{"the last component is a dangling link", walkOpt(true, 10, f.state, "leads/a-lead/dangling"), []string{"symbolic link"}},
		{"a file that is a link", walkOpt(false, 10, f.state, "leads/a-lead/file"), []string{"symbolic link", `"leads/a-lead/file"`}},
		{"a middle component is a link", walkOpt(true, 10, f.state, "leads/moved/a-lead/home"),
			[]string{"symbolic link at \"leads/moved\"", "never followed"}},
		{"a regular file where a folder is bound", walkOpt(true, 10, f.state, below(t, f.state, f.config)),
			[]string{"the source is a regular file, and the walk binds a folder"}},
		{"a folder where a regular file is bound", walkOpt(false, 10, f.state, below(t, f.state, f.home)),
			[]string{"the source is a folder, and the walk binds a regular file"}},
		{"a FIFO where a regular file is bound", walkOpt(false, 10, f.owned, "a.fifo"),
			[]string{"the source is a FIFO, and the walk binds a regular file"}},
		{"a FIFO where a folder is bound", walkOpt(true, 10, f.owned, "a.fifo"),
			[]string{"the source is a FIFO, and the walk binds a folder"}},
		{"a device where a regular file is bound", walkOpt(false, 10, "/dev", "null"),
			[]string{"the walk binds a regular file"}},
		{"a path below a regular file", walkOpt(true, 10, f.state, below(t, f.state, f.config)+"/x"),
			[]string{"not a folder"}},
		{"a missing entry", walkOpt(true, 10, f.state, "leads/a-lead/none"), []string{"the source does not exist"}},
		{"a missing entry below a missing folder", walkOpt(false, 10, f.state, "leads/none/config"), []string{"the source does not exist"}},
		{"a mount point below the folder", walkOpt(true, 10, "/", "proc"), []string{"mount point", "not crossed"}},
		{"the folder the walk starts from is a link", walkOpt(true, 10, filepath.Join(f.base, "state-link"), "leads/a-lead/home"),
			[]string{"the folder the walk starts from is reached through a symbolic link, or is one"}},
		{"a link above the folder the walk starts from", walkOpt(true, 10, filepath.Join(f.base, "state-link", "leads"), "a-lead/home"),
			[]string{"the folder the walk starts from is reached through a symbolic link, or is one"}},
		{"the folder the walk starts from does not exist", walkOpt(true, 10, filepath.Join(f.base, "none"), "home"),
			[]string{"the folder the walk starts from does not exist"}},
		{"the folder the walk starts from is a file", walkOpt(false, 10, f.settings, "x"),
			[]string{"the folder the walk starts from is not a folder"}},
		{"the second walk is refused", append(walkOpt(true, 10, f.state, below(t, f.state, f.home)), walkOpt(true, 11, f.state, "leads/a-lead/linked-out")...),
			[]string{"--walk-dir 11:", "symbolic link"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := startPass(t, runOpts{mode: modeFake}, c.opt...)
			if code := r.wait(); code != exitWalk {
				t.Errorf("exit %d, want %d (stderr %s)", code, exitWalk, r.stderr)
			}
			oneLine(t, r.stderr.String())
			if !strings.HasPrefix(r.stderr.String(), "ataniq-probe: pass-fds: --walk-") {
				t.Errorf("stderr %q does not name the walk", r.stderr)
			}
			for _, w := range append(c.want, "folder "+strconv.Quote(c.opt[len(c.opt)-2]), "path "+strconv.Quote(c.opt[len(c.opt)-1])) {
				if !strings.Contains(r.stderr.String(), w) {
					t.Errorf("stderr %q does not contain %q", r.stderr, w)
				}
			}
			r.noAgent()
		})
	}
	if exitWalk == exitSetup || exitWalk == exitFailed || exitWalk != 96 {
		t.Errorf("the status of a refused walk is %d: it must be 96, a status of its own", exitWalk)
	}
}

// A refused walk ends the step before anything else is done: the report
// channel is not opened for writing, and a file to append to is not touched.
func TestPassFDsRefusedWalkOpensNothingElse(t *testing.T) {
	f := newWalkFixture(t)
	if err := os.Symlink(f.outside, filepath.Join(f.state, "leads/a-lead/planted")); err != nil {
		t.Fatal(err)
	}
	rd, err := unix.Open(f.fifo, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rd)
	hungUp := func() bool {
		t.Helper()
		p := []unix.PollFd{{Fd: int32(rd), Events: unix.POLLIN}}
		if _, err := unix.Poll(p, 0); err != nil {
			t.Fatal(err)
		}
		return p[0].Revents&unix.POLLHUP != 0
	}
	if hungUp() {
		t.Fatal("the FIFO reports a writer that came and went before the test opened one")
	}
	r := startPass(t, runOpts{mode: modeFake}, append([]string{"--write", "4=" + f.fifo}, walkOpt(true, 10, f.state, "leads/a-lead/planted")...)...)
	if code := r.wait(); code != exitWalk {
		t.Fatalf("exit %d, want %d (stderr %s)", code, exitWalk, r.stderr)
	}
	r.noAgent()
	if hungUp() {
		t.Error("the report channel was opened for writing though a walk was refused")
	}
	// The other way round, to show the question can be answered here: a
	// writer that came and went is seen.
	wr, err := unix.Open(f.fifo, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(wr)
	if !hungUp() {
		t.Skip("this kernel does not report a writer that came and went: the check above shows nothing here")
	}
}

// The walk of the step is the one that counts: a source the holder's check
// found in order just before, and that is swapped for a link afterwards, is
// refused by the step; and what the step then hands on is what it walked to.
func TestPassFDsWalkAfterASwapSinceThePreCheck(t *testing.T) {
	f := newWalkFixture(t)
	rel := below(t, f.state, f.home)
	rows := []mountpoints.ResolvedSource{{
		Inside: "/home/lead", Source: f.home, Real: f.home, Role: "home",
		Walk: mountpoints.SourceWalk{Inside: "/home/lead", Root: f.state, Rel: rel, Dir: true, BelowState: true},
	}}
	if rep := mountpoints.CheckBindSources(rows); !rep.OK() {
		t.Fatalf("the pre-check refuses a clean source: %v", rep.Err())
	}
	// Between the pre-check and the step, the folder becomes a link to a
	// folder of the owner.
	if err := os.Rename(f.home, f.home+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, f.home); err != nil {
		t.Fatal(err)
	}
	r := startPass(t, runOpts{mode: modeFake}, walkOpt(true, 10, f.state, rel)...)
	if code := r.wait(); code != exitWalk {
		t.Fatalf("exit %d, want %d: the step accepted what the pre-check had seen and no longer is (stderr %s)", code, exitWalk, r.stderr)
	}
	if !strings.Contains(r.stderr.String(), "symbolic link") {
		t.Errorf("stderr %q", r.stderr)
	}
	r.noAgent()

	// And the other way: a pre-check that refused does not bind the step. Put
	// right again, the source is walked and handed on.
	if rep := mountpoints.CheckBindSources(rows); rep.OK() || !errors.Is(rep.Err(), mountpoints.ErrSymlink) {
		t.Fatalf("the pre-check accepts a link: %v", rep.Err())
	}
	if err := os.Remove(f.home); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.home+".old", f.home); err != nil {
		t.Fatal(err)
	}
	r = startPass(t, runOpts{mode: modeFake}, walkOpt(true, 10, f.state, rel)...)
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d: %s", code, r.stderr)
	}
	wantWalked(t, r.agent(), 10, f.home, true)
}

// The descriptor names the object the walk opened. The command waits before
// it looks; meanwhile the source is renamed and a link to a folder of the
// owner (and, for the file, another file) is put at its name. The command
// still holds the object that was walked to: same device and inode.
func TestPassFDsWalkedDescriptorNamesTheObjectAfterARename(t *testing.T) {
	f := newWalkFixture(t)
	homeDev, homeIno := identity(t, f.home)
	cfgDev, cfgIno := identity(t, f.config)
	out := filepath.Join(t.TempDir(), "agent.json")
	args := append([]string{"pass-fds"}, walkOpt(true, 10, f.state, below(t, f.state, f.home))...)
	args = append(args, walkOpt(false, 11, f.state, below(t, f.state, f.config))...)
	args = append(args, "--", testExe(t), "agent", out, "0", holdBefore)
	r := start(t, runOpts{mode: modeFake}, args, out, false)
	// The command is running: the probe has walked, placed and executed.
	deadline := time.Now().Add(guard)
	for {
		if _, err := os.Stat(out + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command did not start (stderr %s)", r.stderr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Rename(f.home, f.home+".renamed"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, f.home); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.config, f.config+".renamed"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config, []byte("[core]\n\thooksPath = /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".go", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d: %s", code, r.stderr)
	}
	a := r.agent()
	for _, c := range []struct {
		fd       int
		dev, ino uint64
		renamed  string
		name     string
	}{{10, homeDev, homeIno, f.home + ".renamed", f.home}, {11, cfgDev, cfgIno, f.config + ".renamed", f.config}} {
		got, ok := fdByNumber(a, c.fd)
		if !ok {
			t.Fatalf("descriptor %d is missing: %+v", c.fd, a.FDs)
		}
		if got.Dev != c.dev || got.Ino != c.ino {
			t.Errorf("descriptor %d names %d:%d, want the object that was walked to (%d:%d)", c.fd, got.Dev, got.Ino, c.dev, c.ino)
		}
		if nowDev, nowIno := identity(t, c.name); got.Dev == nowDev && got.Ino == nowIno {
			t.Errorf("descriptor %d names what the name holds now", c.fd)
		}
		// The kernel shows where the object is now: under its new name.
		if got.Link != c.renamed {
			t.Errorf("descriptor %d is at %q, want the renamed %q", c.fd, got.Link, c.renamed)
		}
	}
	if len(a.FDs) != 2 {
		t.Errorf("descriptors %+v, want the two walked ones only", a.FDs)
	}
}

// A walk's number is refused like a file's when the Go runtime holds it.
func TestPassFDsWalkRefusesTheRuntimesDescriptors(t *testing.T) {
	f := newWalkFixture(t)
	r := startPass(t, runOpts{mode: modeFake, nonblock: []int{0}}, walkOpt(true, 3, f.state, below(t, f.state, f.home))...)
	if code := r.wait(); code != exitSetup {
		t.Errorf("exit %d, want %d (stderr %s)", code, exitSetup, r.stderr)
	}
	oneLine(t, r.stderr.String())
	if !strings.HasPrefix(r.stderr.String(), "ataniq-probe: pass-fds: descriptor 3 ") || !strings.Contains(r.stderr.String(), "Go runtime") {
		t.Errorf("stderr %q", r.stderr)
	}
	r.noAgent()
}

// walkSources closes what it opened when a walk is refused, and on success
// leaves exactly one close-on-exec O_PATH descriptor per walk: the folders the
// walks started from are closed.
func TestWalkSourcesLeavesOnlyTheWalkedDescriptors(t *testing.T) {
	f := newWalkFixture(t)
	if err := os.Symlink(f.outside, filepath.Join(f.owned, "planted")); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		fds, err := hostListFDs()
		if err != nil {
			t.Fatal(err)
		}
		return len(fds)
	}
	good := []fdWalk{
		{FD: 10, Root: f.state, Rel: below(t, f.state, f.home), Dir: true},
		{FD: 11, Root: f.state, Rel: below(t, f.state, f.config)},
		{FD: 12, Root: f.owned, Rel: "skills", Dir: true},
	}
	before := count()
	for _, bad := range []fdWalk{
		{FD: 13, Root: f.owned, Rel: "planted", Dir: true},
		{FD: 13, Root: f.owned, Rel: "none"},
		{FD: 13, Root: filepath.Join(f.base, "none"), Rel: "x", Dir: true},
	} {
		got, err := walkSources(append(slices.Clone(good), bad))
		var refused *walkError
		if got != nil || !errors.As(err, &refused) {
			t.Fatalf("walkSources with %+v = %v, %v; want a refused walk", bad, got, err)
		}
		if !strings.HasPrefix(err.Error(), bad.option()+" 13: ") {
			t.Errorf("the refusal %q does not name the walk", err)
		}
		if after := count(); after != before {
			t.Errorf("%d descriptors before, %d after a refused walk", before, after)
		}
	}
	got, err := walkSources(good)
	if err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before+len(good) {
		t.Errorf("%d descriptors before, %d after %d walks: a folder was left open", before, after, len(good))
	}
	for i, o := range got {
		if o.target != good[i].FD || o.fd < minFD {
			t.Errorf("walk %d: descriptor %d for the number %d", i, o.fd, o.target)
		}
		if fl, err := unix.FcntlInt(uintptr(o.fd), unix.F_GETFD, 0); err != nil || fl&unix.FD_CLOEXEC == 0 {
			t.Errorf("descriptor %d is not close-on-exec before it is placed", o.fd)
		}
		if fl, err := unix.FcntlInt(uintptr(o.fd), unix.F_GETFL, 0); err != nil || fl&unix.O_PATH == 0 {
			t.Errorf("descriptor %d is not an O_PATH descriptor", o.fd)
		}
		unix.Close(o.fd)
	}
}

func TestParsePassFDsWalks(t *testing.T) {
	got, err := parsePassFDs([]string{"--walk-dir", "10", "/state", "leads/a/home", "--read", "5=/a/b", "--walk-file", "11", "/owned", "settings.json", "--walk-dir", "12", "/owned/skills", ".", "--", "/bin/bwrap", "--walk-dir", "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []fdWalk{{10, "/state", "leads/a/home", true}, {11, "/owned", "settings.json", false}, {12, "/owned/skills", ".", true}}
	if !slices.Equal(got.Walks, want) {
		t.Errorf("walks %+v, want %+v", got.Walks, want)
	}
	if len(got.Files) != 1 || got.Files[0] != (fdFile{5, "/a/b", false}) {
		t.Errorf("files %+v", got.Files)
	}
	if strings.Join(got.Command, " ") != "/bin/bwrap --walk-dir x" {
		t.Errorf("command %q", got.Command)
	}
	if !slices.Equal(got.targets(), []int{10, 11, 12, 5}) {
		t.Errorf("targets %v", got.targets())
	}
	// A root of "/" and a name that only looks like a parent are fine.
	if _, err := parsePassFDs([]string{"--walk-dir", "10", "/", "..data", "--walk-file", "11", "/", "a/..b", "--", "/bin/true"}); err != nil {
		t.Errorf("names that start with two dots: %v", err)
	}

	// The cap counts every kind together.
	var many []string
	for fd := 3; fd < 3+maxPassFDs-1; fd++ {
		many = append(many, "--walk-dir", strconv.Itoa(fd), "/state", "d")
	}
	if _, err := parsePassFDs(append(append(slices.Clone(many), "--write", "900=/f"), "--", "/bin/true")); err != nil {
		t.Errorf("%d descriptors: %v", maxPassFDs, err)
	}

	end := []string{"--", "/bin/true"}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no values", []string{"--walk-dir"}, "needs three values"},
		{"one value", []string{"--walk-dir", "10", "--", "/bin/true"}, "needs three values"},
		{"two values", append([]string{"--walk-file", "10", "/state"}, end...), "needs three values"},
		{"a value is --", append([]string{"--walk-dir", "10", "/state", "--", "/bin/true"}, end...), "needs three values"},
		{"three values and no command", []string{"--walk-dir", "10", "/state", "d"}, "required after the options"},
		{"fd 2", append([]string{"--walk-dir", "2", "/state", "d"}, end...), "descriptor is not a descriptor number"},
		{"fd 1024", append([]string{"--walk-file", "1024", "/state", "d"}, end...), "descriptor is not a descriptor number"},
		{"fd leading zero", append([]string{"--walk-dir", "010", "/state", "d"}, end...), "descriptor is not a descriptor number"},
		{"fd=path form", append([]string{"--walk-dir", "10=/state", "/state", "d"}, end...), "descriptor is not a descriptor number"},
		{"relative folder", append([]string{"--walk-dir", "10", "state", "d"}, end...), "must be an absolute, clean path"},
		{"empty folder", append([]string{"--walk-dir", "10", "", "d"}, end...), "must be an absolute, clean path"},
		{"unclean folder", append([]string{"--walk-dir", "10", "/state/../etc", "d"}, end...), "must be an absolute, clean path"},
		{"folder with a trailing slash", append([]string{"--walk-dir", "10", "/state/", "d"}, end...), "must be an absolute, clean path"},
		{"absolute path", append([]string{"--walk-dir", "10", "/state", "/etc"}, end...), "must be relative to the folder"},
		{"empty path", append([]string{"--walk-file", "10", "/state", ""}, end...), "must be relative to the folder"},
		{"path that leaves the folder", append([]string{"--walk-dir", "10", "/state", "../etc"}, end...), "must not leave the folder"},
		{"path that is the parent", append([]string{"--walk-dir", "10", "/state", ".."}, end...), "must not leave the folder"},
		{"path with a parent in the middle", append([]string{"--walk-dir", "10", "/state", "a/../../etc"}, end...), "must not leave the folder"},
		{"unclean path", append([]string{"--walk-file", "10", "/state", "a//b"}, end...), "must be clean"},
		{"path with a trailing slash", append([]string{"--walk-dir", "10", "/state", "a/"}, end...), "must be clean"},
		{"the folder itself as a file", append([]string{"--walk-file", "10", "/state", "."}, end...), "names the folder, not a file"},
		{"same fd for two walks", append([]string{"--walk-dir", "10", "/state", "a", "--walk-file", "10", "/state", "b"}, end...), "descriptor 10 is given twice"},
		{"same fd for a walk and a file", append([]string{"--read", "10=/f", "--walk-dir", "10", "/state", "a"}, end...), "descriptor 10 is given twice"},
		{"same fd for a file and a walk", append([]string{"--walk-dir", "10", "/state", "a", "--write", "10=/f"}, end...), "descriptor 10 is given twice"},
		{"one walk more than the cap", append(append(slices.Clone(many), "--walk-dir", "900", "/state", "d", "--walk-dir", "901", "/state", "d"), end...), "more than 128 descriptors"},
		{"one file more than the cap", append(append(slices.Clone(many), "--walk-dir", "900", "/state", "d", "--read", "901=/f"), end...), "more than 128 descriptors"},
		{"an option in another spelling", append([]string{"--walk", "10", "/state", "d"}, end...), "unknown option"},
	} {
		_, err := parsePassFDs(tc.args)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: got %v, want a usage error", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, err, tc.want)
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: the message has a newline", tc.name)
		}
	}
}

// A long folder or path does not push the reason out of the line.
func TestWalkRefusalKeepsTheReasonInALongLine(t *testing.T) {
	f := newWalkFixture(t)
	long := strings.Repeat("a-long-component/", 40) + "end"
	r := startPass(t, runOpts{mode: modeFake}, walkOpt(true, 10, f.state, long)...)
	if code := r.wait(); code != exitWalk {
		t.Fatalf("exit %d, want %d (stderr %s)", code, exitWalk, r.stderr)
	}
	oneLine(t, r.stderr.String())
	if !strings.Contains(r.stderr.String(), "--walk-dir 10: the source does not exist") || !strings.Contains(r.stderr.String(), strconv.Quote(f.state)) {
		t.Errorf("stderr %q", r.stderr)
	}
	if len(r.stderr.String()) > 600 {
		t.Errorf("the line is %d bytes", len(r.stderr.String()))
	}
}
