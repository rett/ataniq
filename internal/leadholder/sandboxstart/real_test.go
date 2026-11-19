//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// realSys is the real System over a world's runtime folder.
func realSys(t *testing.T) *realSystem {
	t.Helper()
	s, err := NewSystem(SystemConfig{RoleLock: &fakeLock{held: true}})
	if err != nil {
		t.Fatal(err)
	}
	return s.(*realSystem)
}

func statOf(t *testing.T, p string) unix.Stat_t {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatalf("lstat %s: %v", p, err)
	}
	return st
}

func gone(t *testing.T, p string) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); !errors.Is(err, unix.ENOENT) {
		t.Errorf("%s is still there (%v)", p, err)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// probeLine is the report line a probe writes for a digest.
func probeLine(digest string) []byte {
	rep := (&fakeChannel{s: &fakeSystem{probeStatus: map[int]assert.Status{}}}).line(digest)
	return assert.EncodeLine(rep)
}

// openForWriting plays the plan's pass-through step: the FIFO opened
// write-only, without creating it.
func openForWriting(t *testing.T, p string) *os.File {
	t.Helper()
	fd, err := unix.Open(p, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open the FIFO for writing: %v", err)
	}
	return os.NewFile(uintptr(fd), p)
}

func TestNewSystemNeedsTheLock(t *testing.T) {
	if _, err := NewSystem(SystemConfig{}); err == nil {
		t.Fatal("a real system without the role lock was made")
	}
}

// TestRealReportChannel makes the FIFO for real, writes the probe's line through
// it as the pass-through step would, and removes it.
func TestRealReportChannel(t *testing.T) {
	w := newWorld(t)
	s := realSys(t)
	ch, err := s.OpenReport(w.run, sampleRole)
	if err != nil {
		t.Fatal(err)
	}
	dir := ReportFolder(w.run, sampleRole)
	fifo := ReportFIFO(w.run, sampleRole)
	if st := statOf(t, dir); st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o7777 != 0o700 {
		t.Errorf("the report folder is %o", st.Mode)
	}
	if st := statOf(t, fifo); st.Mode&unix.S_IFMT != unix.S_IFIFO || st.Mode&0o7777 != 0o600 {
		t.Errorf("the FIFO is %o", st.Mode)
	}
	if got := names(t, dir); len(got) != 1 || got[0] != "probe.fifo" {
		t.Errorf("the report folder holds %v", got)
	}
	// Nothing came yet and a question about trailing bytes is refused.
	if err := ch.Trailing(); err == nil {
		t.Error("Trailing answered before a line was read")
	}
	digest := strings.Repeat("ab", 32)
	wr := openForWriting(t, fifo)
	if _, err := wr.Write(probeLine(digest)); err != nil {
		t.Fatal(err)
	}
	rep, err := ch.ReadLine(context.Background(), make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Digest != digest || len(rep.Results) == 0 {
		t.Fatalf("report %+v", rep)
	}
	if err := ch.Trailing(); err != nil {
		t.Errorf("nothing followed the line, but: %v", err)
	}
	// A byte that follows is a failure of the start.
	if _, err := wr.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := ch.Trailing(); err == nil {
		t.Error("a trailing byte was not reported")
	}
	wr.Close()
	if err := ch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Close(); err != nil {
		t.Errorf("the second Close: %v", err)
	}
	gone(t, fifo)
	gone(t, dir)
	if got := names(t, RuntimeFolder(w.run, sampleRole)); len(got) != 0 {
		t.Errorf("the runtime folder holds %v after Close", got)
	}
}

// TestRealReportChannelEnds shows the three ways a read of the line ends
// without a line.
func TestRealReportChannelEnds(t *testing.T) {
	w := newWorld(t)
	s := realSys(t)
	t.Run("the time limit", func(t *testing.T) {
		ch, err := s.OpenReport(w.run, sampleRole)
		if err != nil {
			t.Fatal(err)
		}
		defer ch.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err = ch.ReadLine(ctx, make(chan struct{}))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error %v, want the deadline", err)
		}
	})
	t.Run("the first process exits", func(t *testing.T) {
		ch, err := s.OpenReport(w.run, sampleRole)
		if err != nil {
			t.Fatal(err)
		}
		defer ch.Close()
		exited := make(chan struct{})
		close(exited)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = ch.ReadLine(ctx, exited)
		if err == nil || !strings.Contains(err.Error(), "exited") {
			t.Fatalf("error %v, want the first process exited", err)
		}
	})
	t.Run("a line is refused", func(t *testing.T) {
		ch, err := s.OpenReport(w.run, sampleRole)
		if err != nil {
			t.Fatal(err)
		}
		defer ch.Close()
		wr := openForWriting(t, ReportFIFO(w.run, sampleRole))
		defer wr.Close()
		if _, err := wr.Write([]byte("not a report\n")); err != nil {
			t.Fatal(err)
		}
		_, err = ch.ReadLine(context.Background(), make(chan struct{}))
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("error %v, want a refused line", err)
		}
	})
	t.Run("a line before the exit is still read", func(t *testing.T) {
		ch, err := s.OpenReport(w.run, sampleRole)
		if err != nil {
			t.Fatal(err)
		}
		defer ch.Close()
		wr := openForWriting(t, ReportFIFO(w.run, sampleRole))
		wr.Write(probeLine(strings.Repeat("cd", 32)))
		wr.Close()
		exited := make(chan struct{})
		close(exited)
		rep, err := ch.ReadLine(context.Background(), exited)
		if err != nil || rep.Digest != strings.Repeat("cd", 32) {
			t.Fatalf("report %+v, error %v", rep, err)
		}
	})
}

// TestRealReportFolderRules checks how the folder is made and what is refused.
func TestRealReportFolderRules(t *testing.T) {
	s := realSys(t)
	t.Run("a leftover FIFO of a killed holder is removed", func(t *testing.T) {
		w := newWorld(t)
		dir := ReportFolder(w.run, sampleRole)
		mkdir(t, dir, 0o755)
		if err := unix.Mkfifo(dir+"/probe.fifo", 0o644); err != nil {
			t.Fatal(err)
		}
		ch, err := s.OpenReport(w.run, sampleRole)
		if err != nil {
			t.Fatal(err)
		}
		if st := statOf(t, dir); st.Mode&0o7777 != 0o700 {
			t.Errorf("the leftover folder has mode %o", st.Mode&0o7777)
		}
		ch.Close()
		gone(t, dir)
	})
	t.Run("anything else in the folder refuses the start", func(t *testing.T) {
		w := newWorld(t)
		dir := ReportFolder(w.run, sampleRole)
		mkdir(t, dir, 0o700)
		if err := os.WriteFile(dir+"/other", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := s.OpenReport(w.run, sampleRole)
		if err == nil || !strings.Contains(err.Error(), "other") {
			t.Fatalf("error %v, want the folder refused for what it holds", err)
		}
		// Not ours to remove: the folder and what is in it stay.
		if got := names(t, dir); len(got) != 1 || got[0] != "other" {
			t.Errorf("the folder holds %v", got)
		}
	})
	t.Run("a FIFO of the wrong type is refused", func(t *testing.T) {
		w := newWorld(t)
		dir := ReportFolder(w.run, sampleRole)
		mkdir(t, dir, 0o700)
		if err := os.WriteFile(dir+"/probe.fifo", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenReport(w.run, sampleRole); err == nil {
			t.Fatal("a regular file was taken for the FIFO")
		}
	})
	t.Run("a link in place of the folder", func(t *testing.T) {
		w := newWorld(t)
		elsewhere := w.base + "/elsewhere"
		mkdir(t, elsewhere, 0o700)
		if err := os.Symlink(elsewhere, ReportFolder(w.run, sampleRole)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenReport(w.run, sampleRole); err == nil {
			t.Fatal("a link was followed")
		}
		if got := names(t, elsewhere); len(got) != 0 {
			t.Errorf("something was made through the link: %v", got)
		}
	})
	t.Run("a file in place of the folder", func(t *testing.T) {
		w := newWorld(t)
		if err := os.WriteFile(ReportFolder(w.run, sampleRole), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenReport(w.run, sampleRole); err == nil {
			t.Fatal("a file was taken for the folder")
		}
	})
	t.Run("the runtime folder is missing", func(t *testing.T) {
		w := newWorld(t)
		if err := os.Remove(RuntimeFolder(w.run, sampleRole)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenReport(w.run, sampleRole); err == nil {
			t.Fatal("no runtime folder, no error")
		}
	})
	t.Run("the runtime folder is open to others", func(t *testing.T) {
		w := newWorld(t)
		if err := os.Chmod(RuntimeFolder(w.run, sampleRole), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := s.OpenReport(w.run, sampleRole)
		if err == nil || !strings.Contains(err.Error(), "mode") {
			t.Fatalf("error %v, want the mode refused", err)
		}
	})
	t.Run("a link on the path of the runtime folder", func(t *testing.T) {
		w := newWorld(t)
		linked := w.base + "/linked"
		if err := os.Symlink(w.run, linked); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenReport(linked, sampleRole); err == nil {
			t.Fatal("a link above the runtime folder was followed")
		}
		if _, err := s.MakeBound(linked, sampleRole); err == nil {
			t.Fatal("a link above the runtime folder was followed by MakeBound")
		}
	})
}

// TestRealBound makes the bound folder, writes the two files and removes them.
func TestRealBound(t *testing.T) {
	w := newWorld(t)
	s := realSys(t)
	b, err := s.MakeBound(w.run, sampleRole)
	if err != nil {
		t.Fatal(err)
	}
	dir := w.run + "/leads/" + sampleRole + "/bound"
	if got := boundResolv(w.run, sampleRole); got != dir+"/resolv.conf" {
		t.Errorf("boundResolv = %s", got)
	}
	if got := boundSpecDir(w.run, sampleRole); got != dir+"/spec" {
		t.Errorf("boundSpecDir = %s", got)
	}
	for _, d := range []string{dir, dir + "/spec"} {
		if st := statOf(t, d); st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o7777 != 0o700 {
			t.Errorf("%s is %o", d, st.Mode)
		}
	}
	if err := b.WriteResolv([]byte("nameserver 192.0.2.1\n")); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteSpec([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if st := statOf(t, dir+"/resolv.conf"); st.Mode&0o7777 != 0o644 {
		t.Errorf("resolv.conf is %o", st.Mode&0o7777)
	}
	if st := statOf(t, dir+"/spec/spec.json"); st.Mode&0o7777 != 0o600 {
		t.Errorf("spec.json is %o", st.Mode&0o7777)
	}
	if data, _ := os.ReadFile(dir + "/resolv.conf"); string(data) != "nameserver 192.0.2.1\n" {
		t.Errorf("resolv.conf holds %q", data)
	}
	// Created exclusively: a second write is refused.
	if err := b.WriteResolv([]byte("x")); err == nil {
		t.Error("resolv.conf was written twice")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("the second Close: %v", err)
	}
	gone(t, dir)
	if err := b.WriteSpec([]byte("{}")); err == nil {
		t.Error("a closed folder took a write")
	}
	if got := names(t, RuntimeFolder(w.run, sampleRole)); len(got) != 0 {
		t.Errorf("the runtime folder holds %v", got)
	}
}

// TestRealBoundLeftovers shows the leftover of a killed holder is cleared, and
// anything of another name or type refuses the start.
func TestRealBoundLeftovers(t *testing.T) {
	s := realSys(t)
	t.Run("what an earlier start made", func(t *testing.T) {
		w := newWorld(t)
		dir := w.run + "/leads/" + sampleRole + "/bound"
		mkdir(t, dir+"/spec", 0o755)
		os.WriteFile(dir+"/resolv.conf", []byte("old"), 0o644)
		os.WriteFile(dir+"/spec/spec.json", []byte("old"), 0o600)
		b, err := s.MakeBound(w.run, sampleRole)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.WriteResolv([]byte("new")); err != nil {
			t.Fatal(err)
		}
		if err := b.WriteSpec([]byte("new")); err != nil {
			t.Fatal(err)
		}
		b.Close()
		gone(t, dir)
	})
	t.Run("another name", func(t *testing.T) {
		w := newWorld(t)
		dir := w.run + "/leads/" + sampleRole + "/bound"
		mkdir(t, dir+"/spec", 0o700)
		os.WriteFile(dir+"/spec/extra", nil, 0o600)
		if _, err := s.MakeBound(w.run, sampleRole); err == nil {
			t.Fatal("a folder with a stranger in it was cleared")
		}
		if got := names(t, dir+"/spec"); len(got) != 1 {
			t.Errorf("the stranger's folder was touched: %v", got)
		}
	})
	t.Run("a link in place of the resolv.conf", func(t *testing.T) {
		w := newWorld(t)
		dir := w.run + "/leads/" + sampleRole + "/bound"
		mkdir(t, dir, 0o700)
		target := w.base + "/victim"
		os.WriteFile(target, []byte("keep"), 0o600)
		os.Symlink(target, dir+"/resolv.conf")
		if _, err := s.MakeBound(w.run, sampleRole); err == nil {
			t.Fatal("a link was cleared as a file")
		}
		if data, _ := os.ReadFile(target); string(data) != "keep" {
			t.Error("the link's target changed")
		}
	})
}

// TestRealLingering reads the login manager's state from a folder.
func TestRealLingering(t *testing.T) {
	dir := realpath(t, t.TempDir())
	os.WriteFile(dir+"/alice", nil, 0o644)
	s, err := NewSystem(SystemConfig{RoleLock: &fakeLock{held: true}, LingerDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	l := s.Lingering()
	if on, err := l.Enabled("alice"); !on || err != nil {
		t.Errorf("alice: %v, %v", on, err)
	}
	if on, err := l.Enabled("bob"); on || err != nil {
		t.Errorf("bob: %v, %v", on, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../alice"} {
		if _, err := l.Enabled(bad); err == nil {
			t.Errorf("%q was asked about", bad)
		}
	}
	// No folder: the question cannot be answered, which is not "no".
	s2, _ := NewSystem(SystemConfig{RoleLock: &fakeLock{}, LingerDir: dir + "/none"})
	if on, err := s2.Lingering().Enabled("alice"); on || err == nil {
		t.Errorf("no login manager: %v, %v", on, err)
	}
	if DefaultLingerDir != "/var/lib/systemd/linger" {
		t.Errorf("DefaultLingerDir = %s", DefaultLingerDir)
	}
}

// TestRealToolVersions asks for versions through the seam, and once through
// the real question with a script that is not a sandbox tool.
func TestRealToolVersions(t *testing.T) {
	var asked []string
	s, _ := NewSystem(SystemConfig{RoleLock: &fakeLock{}, RunVersion: func(ctx context.Context, p string) (string, error) {
		asked = append(asked, p)
		if strings.HasSuffix(p, "pasta") {
			return "", errors.New("no such tool")
		}
		return "bubblewrap 0.13.1\x1b[0m", nil
	}})
	v := s.ToolVersions(context.Background(), ToolPaths{Pasta: "/x/pasta", Bwrap: "/x/bwrap"})
	if len(asked) != 2 || v.Kernel == "" {
		t.Fatalf("asked %v, versions %+v", asked, v)
	}
	if v.Bwrap != `bubblewrap 0.13.1\u001b[0m` {
		t.Errorf("the version line is not escaped: %q", v.Bwrap)
	}
	if len(v.Errors) != 1 || !strings.Contains(v.Errors[0], "pasta --version") {
		t.Errorf("errors %v", v.Errors)
	}

	dir := realpath(t, t.TempDir())
	tool := dir + "/tool"
	script := "#!/bin/sh\necho \"tool 1.2 $HOME\"\necho second line\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := runVersion(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	if got != "tool 1.2 " {
		t.Errorf("version line %q: the environment must be empty and only the first line kept", got)
	}
	if _, err := runVersion(context.Background(), "relative/tool"); err == nil {
		t.Error("a relative path was run")
	}
	if _, err := runVersion(context.Background(), dir+"/missing"); err == nil {
		t.Error("a missing tool was run")
	}
}

// checkHostWorld makes the host paths CheckHost looks at, over a world.
func checkHostWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	l := w.in.layout()
	mkdir(t, l.QargiRecords(), 0o700)
	gi := w.base + "/owned/git"
	mkdir(t, gi, 0o755)
	os.WriteFile(gi+"/config", []byte("[user]\n"), 0o644)
	for _, p := range []string{w.in.Executables.Pasta, w.in.Executables.Bwrap, w.in.Executables.Probe} {
		mkdir(t, filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("exe "+p), 0o755)
	}
	body := []byte(`{"hooks":[]}`)
	mkdir(t, filepath.Dir(w.in.Sources.ManagedSettings), 0o755)
	os.WriteFile(w.in.Sources.ManagedSettings, body, 0o644)
	sum := sha256.Sum256(body)
	w.in.Sources.ManagedSettingsSHA256 = hex.EncodeToString(sum[:])
	return w
}

func TestRealCheckHost(t *testing.T) {
	s := realSys(t)
	t.Run("a host that is as the inputs say", func(t *testing.T) {
		w := checkHostWorld(t)
		exes, err := s.CheckHost(w.in)
		if err != nil {
			t.Fatal(err)
		}
		if len(exes) != 3 || exes[0].Name != "pasta" || len(exes[0].SHA256) != 64 || exes[0].Size == 0 {
			t.Errorf("executables %+v", exes)
		}
	})
	faults := []struct {
		name string
		want string
		set  func(t *testing.T, w *world)
	}{
		{"an executable writable by the group", "writable by its group", func(t *testing.T, w *world) { os.Chmod(w.in.Executables.Bwrap, 0o775) }},
		{"an executable that is a link", "cannot open", func(t *testing.T, w *world) {
			os.Remove(w.in.Executables.Probe)
			os.Symlink(w.in.Executables.Pasta, w.in.Executables.Probe)
		}},
		{"a missing executable", "cannot open", func(t *testing.T, w *world) { os.Remove(w.in.Executables.Pasta) }},
		{"a managed-settings file that is not the digest", "managed_settings_sha256", func(t *testing.T, w *world) {
			os.WriteFile(w.in.Sources.ManagedSettings, []byte("changed"), 0o644)
		}},
		{"a seccomp file that is not the digest", "seccomp.sha256", func(t *testing.T, w *world) {
			f := w.base + "/owned/filter.bpf"
			os.WriteFile(f, []byte("bpf"), 0o644)
			w.in.Sources.Seccomp = &Seccomp{File: f, SHA256: strings.Repeat("0", 64), ReviewedBPF: true}
		}},
		{"another file in the git identity folder", "besides config", func(t *testing.T, w *world) {
			os.WriteFile(w.base+"/owned/git/credentials", nil, 0o644)
		}},
		{"no git identity file", "git_identity_dir", func(t *testing.T, w *world) { os.Remove(w.base + "/owned/git/config") }},
		{"a records folder that is open", "mode", func(t *testing.T, w *world) { os.Chmod(w.in.layout().QargiRecords(), 0o755) }},
		{"no records folder", "records", func(t *testing.T, w *world) { os.Remove(w.in.layout().QargiRecords()) }},
		{"an open runtime folder", "mode", func(t *testing.T, w *world) { os.Chmod(RuntimeFolder(w.run, sampleRole), 0o755) }},
		{"no runtime folder", "runtime", func(t *testing.T, w *world) { os.Remove(RuntimeFolder(w.run, sampleRole)) }},
		{"a state root that is a link", "real path", func(t *testing.T, w *world) {
			l := w.base + "/statelink"
			os.Symlink(w.state, l)
			w.in.StateRoot = l
		}},
	}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			w := checkHostWorld(t)
			f.set(t, w)
			_, err := s.CheckHost(w.in)
			if err == nil {
				t.Fatal("accepted")
			}
			wantInputsError(t, err, f.want)
		})
	}
}

// TestRealIDs checks the ids are the caller's.
func TestRealIDs(t *testing.T) {
	s := realSys(t)
	uid, gid := s.IDs()
	if uid != uint32(unix.Geteuid()) || gid != uint32(unix.Getegid()) {
		t.Errorf("ids %d %d", uid, gid)
	}
	if s.Getenv("PATH") != os.Getenv("PATH") {
		t.Error("Getenv")
	}
	if time.Since(s.Now()) > time.Minute {
		t.Error("Now")
	}
}
