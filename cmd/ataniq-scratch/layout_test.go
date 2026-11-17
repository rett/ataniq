package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLayoutPaths(t *testing.T) {
	e := newTestEnv(t)
	l := e.layout(t, "demo")
	if want := filepath.Join(e.runtime, "ataniq", "scratch", "demo"); l.runtime != want {
		t.Errorf("runtime %q, want %q", l.runtime, want)
	}
	if want := filepath.Join(e.state, "ataniq", "scratch", "demo"); l.state != want {
		t.Errorf("state %q, want %q", l.state, want)
	}
	if l.home != filepath.Join(l.state, "home") || l.epochFile != filepath.Join(l.state, "epoch") ||
		l.controlSock != filepath.Join(l.runtime, "control.sock") || l.ticketFile != filepath.Join(l.runtime, "ticket") {
		t.Errorf("layout %+v", l)
	}
}

func TestLayoutStateDefaultsToHome(t *testing.T) {
	e := newTestEnv(t)
	delete(e.vars, "XDG_STATE_HOME")
	l := e.layout(t, "demo")
	if want := filepath.Join(e.home, ".local", "state", "ataniq", "scratch", "demo"); l.state != want {
		t.Errorf("state %q, want %q", l.state, want)
	}
	e.vars["XDG_STATE_HOME"] = "relative/state"
	if l := e.layout(t, "demo"); !strings.HasPrefix(l.state, e.home) {
		t.Errorf("a relative XDG_STATE_HOME was used: %q", l.state)
	}
}

func TestLayoutNeedsAbsoluteBases(t *testing.T) {
	e := newTestEnv(t)
	e.vars["XDG_RUNTIME_DIR"] = ""
	a, _, _ := e.newApp(&fakeLauncher{})
	if _, err := a.layoutFor("demo"); err == nil {
		t.Error("no XDG_RUNTIME_DIR accepted")
	}
	e.vars["XDG_RUNTIME_DIR"] = e.runtime
	delete(e.vars, "XDG_STATE_HOME")
	e.vars["HOME"] = ""
	if _, err := a.layoutFor("demo"); err == nil {
		t.Error("no state home and no HOME accepted")
	}
}

func TestLayoutRefusesALongSocketPath(t *testing.T) {
	e := newTestEnv(t)
	// Find the longest runtime base that still fits, then one byte more.
	suffix := "/ataniq/scratch/demo/control.sock"
	base := e.runtime
	room := maxSocketPath - len(base) - len(suffix)
	if room < 2 {
		t.Skip("the test folder is itself too long")
	}
	e.vars["XDG_RUNTIME_DIR"] = base + "/" + strings.Repeat("d", room-1) // exactly the limit
	a, _, _ := e.newApp(&fakeLauncher{})
	if _, err := a.layoutFor("demo"); err != nil {
		t.Errorf("a path of exactly %d bytes was refused: %v", maxSocketPath, err)
	}
	e.vars["XDG_RUNTIME_DIR"] = base + "/" + strings.Repeat("d", room)
	if _, err := a.layoutFor("demo"); err == nil || !strings.Contains(err.Error(), "107") {
		t.Errorf("a path over the limit: err %v", err)
	}
}

func TestStartRefusesALongRuntimePathAndMakesNothing(t *testing.T) {
	e := newTestEnv(t)
	long := filepath.Join(e.runtime, strings.Repeat("d", 100))
	e.vars["XDG_RUNTIME_DIR"] = long
	fl := &fakeLauncher{}
	a, _, errout := e.newApp(fl)
	code := a.run([]string{"start", "demo", "--holder", e.holderFile(t), "--", "agent"})
	if code != exitFail || !strings.Contains(errout.String(), "too deep for a unix socket") {
		t.Errorf("exit %d, stderr %q", code, errout.String())
	}
	if _, err := os.Stat(long); err == nil {
		t.Error("the long runtime folder was created")
	}
	if _, err := os.Stat(filepath.Join(e.state, "ataniq")); err == nil {
		t.Error("the state folder was created")
	}
	if len(fl.runList()) != 0 {
		t.Error("something was started")
	}
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

func TestMakeFoldersModes(t *testing.T) {
	e := newTestEnv(t)
	// A parent that exists with a loose mode is tightened.
	if err := os.MkdirAll(filepath.Join(e.runtime, "ataniq"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(e.runtime, "ataniq"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, _, _ := e.newApp(&fakeLauncher{})
	l := e.layout(t, "demo")
	if err := a.makeFolders(l); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(e.runtime, "ataniq"), filepath.Join(e.runtime, "ataniq", "scratch"), l.runtime,
		filepath.Join(e.state, "ataniq"), filepath.Join(e.state, "ataniq", "scratch"), l.state, l.home,
	} {
		if m := modeOf(t, p); !m.IsDir() || m.Perm() != 0o700 {
			t.Errorf("%s: mode %v, want a folder with 0700", p, m)
		}
	}
	// Again: nothing changes, nothing fails.
	if err := a.makeFolders(l); err != nil {
		t.Errorf("second call: %v", err)
	}
}

func TestMakeFoldersRefusesALink(t *testing.T) {
	e := newTestEnv(t)
	target := filepath.Join(e.root, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.runtime, "ataniq", "scratch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(e.runtime, "ataniq", "scratch", "demo")); err != nil {
		t.Fatal(err)
	}
	a, _, _ := e.newApp(&fakeLauncher{})
	if err := a.makeFolders(e.layout(t, "demo")); err == nil {
		t.Error("a link in place of the runtime folder was accepted")
	}
}

func TestEpochCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch")
	if _, err := readEpoch(path, time.Second); err == nil {
		t.Error("an epoch that was never written was read")
	}
	for want := uint64(1); want <= 3; want++ {
		h, err := nextEpoch(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if h.N != want {
			t.Fatalf("epoch %d, want %d", h.N, want)
		}
		h.Release()
		got, err := readEpoch(path, time.Second)
		if err != nil || got != want {
			t.Fatalf("read %d, %v; want %d", got, err, want)
		}
	}
	if m := modeOf(t, path); m.Perm() != 0o600 {
		t.Errorf("epoch file mode %v", m)
	}
	if b, _ := os.ReadFile(path); string(b) != "3\n" {
		t.Errorf("epoch file %q", b)
	}
}

func TestEpochUndo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch")
	h, _ := nextEpoch(path, time.Second)
	h.Release()
	h, err := nextEpoch(path, time.Second)
	if err != nil || h.N != 2 {
		t.Fatalf("%v %v", h, err)
	}
	if err := h.Undo(); err != nil {
		t.Fatal(err)
	}
	h.Release()
	if got, _ := readEpoch(path, time.Second); got != 1 {
		t.Errorf("after undo: %d, want 1", got)
	}
	// Undo of the first epoch leaves no epoch, and the next start is 1 again.
	path2 := filepath.Join(t.TempDir(), "epoch")
	h, _ = nextEpoch(path2, time.Second)
	h.Undo()
	h.Release()
	if _, err := readEpoch(path2, time.Second); err == nil {
		t.Error("an undone first epoch can be read")
	}
	h, _ = nextEpoch(path2, time.Second)
	if h.N != 1 {
		t.Errorf("after undoing the first: %d, want 1", h.N)
	}
	h.Release()
}

func TestEpochRefusesAFileThatIsDamaged(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"text":     "abc\n",
		"negative": "-1\n",
		"padded":   "007\n",
		"two":      "1\n2\n",
		"huge":     strings.Repeat("9", 40) + "\n",
		"maxed":    "18446744073709551615\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if h, err := nextEpoch(p, time.Second); err == nil {
			h.Release()
			t.Errorf("%s: a start was allowed", name)
		}
		if b, _ := os.ReadFile(p); string(b) != content {
			t.Errorf("%s: the file was changed to %q", name, b)
		}
	}
}

func TestEpochConcurrentStartsNeverShareANumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch")
	const n = 16
	var wg sync.WaitGroup
	got := make([]uint64, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := nextEpoch(path, 10*time.Second)
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = h.N
			h.Release()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, v := range got {
		if v != uint64(i+1) {
			t.Fatalf("epochs %v, want 1..%d", got, n)
		}
	}
}

func TestEpochHeldLockBlocksAReaderAndAStarter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch")
	h, err := nextEpoch(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextEpoch(path, 50*time.Millisecond); err != errLockTimeout {
		t.Errorf("second starter: %v", err)
	}
	if _, err := readEpoch(path, 50*time.Millisecond); err != errLockTimeout {
		t.Errorf("reader: %v", err)
	}
	h.Release()
	if got, err := readEpoch(path, time.Second); err != nil || got != 1 {
		t.Errorf("after release: %d %v", got, err)
	}
}

func TestOpenPrivateTightensAndRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openPrivate(p, os.O_RDWR, true)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if m := modeOf(t, p); m.Perm() != 0o600 {
		t.Errorf("mode %v", m)
	}
	l := filepath.Join(dir, "link")
	if err := os.Symlink(p, l); err != nil {
		t.Fatal(err)
	}
	if f, err := openPrivate(l, os.O_RDWR, true); err == nil {
		f.Close()
		t.Error("a link was opened")
	}
}
