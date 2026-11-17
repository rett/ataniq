package systemdmgr

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// None of these tests runs a tool: they resolve files in temporary
// directories and check what is refused before anything would run.

func writeTool(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveToolFixedDirectoriesInOrder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	pb := writeTool(t, b, "busctl", 0o755)
	writeTool(t, b, "systemctl", 0o755)
	pa := writeTool(t, a, "busctl", 0o755)
	r, err := NewExecRunner([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if r.Path("busctl") != pa || r.Path("systemctl") != filepath.Join(b, "systemctl") || pb == pa {
		t.Fatalf("%q %q", r.Path("busctl"), r.Path("systemctl"))
	}
	if r.Path("systemd-run") != "" || r.Path("sh") != "" {
		t.Fatal("a tool outside the two resolved")
	}
}

func TestResolveToolSkipsWhatItDoesNotTrust(t *testing.T) {
	dir, good := t.TempDir(), t.TempDir()
	writeTool(t, dir, "busctl", 0o666)    // writable by others, not executable
	writeTool(t, dir, "systemctl", 0o775) // writable by the group
	if err := os.Mkdir(filepath.Join(dir, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecRunner([]string{dir}); err == nil {
		t.Fatal("untrusted tools resolved")
	}
	if _, err := resolveTool([]string{"relative", ""}, "busctl"); err == nil {
		t.Fatal("relative directory used")
	}
	if err := os.Mkdir(filepath.Join(dir, "busctl2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTool([]string{dir}, "busctl2"); err == nil {
		t.Fatal("a directory resolved as a tool")
	}
	writeTool(t, good, "busctl", 0o644) // not executable
	if _, err := resolveTool([]string{good}, "busctl"); err == nil {
		t.Fatal("a file without an execute bit resolved")
	}
	// An untrusted candidate does not hide a trusted one further on.
	writeTool(t, good, "busctl", 0o755)
	if p, err := resolveTool([]string{dir, good}, "busctl"); err != nil || p != filepath.Join(good, "busctl") {
		t.Fatal(p, err)
	}
}

func TestDefaultToolDirsAreFixedAndAbsolute(t *testing.T) {
	for _, d := range DefaultToolDirs {
		if !filepath.IsAbs(d) {
			t.Errorf("%q", d)
		}
	}
}

func TestExecRunnerRefusesWhatItWouldNotRun(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "busctl", 0o755)
	writeTool(t, dir, "systemctl", 0o755)
	r, err := NewExecRunner([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{nil, {}, {"sh", "-c", "true"}, {"/usr/bin/busctl"}, {"systemd-run"}} {
		if _, err := r.Run(context.Background(), argv); !errors.Is(err, ErrNotLaunched) {
			t.Errorf("%q: %v", argv, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, []string{"busctl", "--user"}); !errors.Is(err, ErrNotLaunched) || !errors.Is(err, context.Canceled) {
		t.Errorf("done context: %v", err)
	}
}

func TestNotLaunchedClassification(t *testing.T) {
	if !notLaunched(&fs.PathError{Op: "fork/exec", Path: "/x", Err: fs.ErrPermission}) {
		t.Error("fork/exec failure")
	}
	if !notLaunched(&exec.Error{Name: "x", Err: exec.ErrNotFound}) {
		t.Error("exec error")
	}
	if notLaunched(&exec.ExitError{}) {
		t.Error("an exit status means the tool ran")
	}
	if notLaunched(errors.New("signal: killed")) || notLaunched(context.DeadlineExceeded) {
		t.Error("a kill or a timeout means the tool ran")
	}
}

func TestSnippetIsBoundedAndPrintable(t *testing.T) {
	got := snippet("  a\x00b\n\x1b[31mé  ")
	if got != "a?b??[31m?" {
		t.Fatalf("%q", got)
	}
	long := snippet(string(make([]byte, 5000)))
	if len(long) > 210 {
		t.Fatal(len(long))
	}
}
