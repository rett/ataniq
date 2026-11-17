//go:build linux && amd64

package harness

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only the real launcher starts a process. This guards that no other file of
// the harness, its command or its test support imports os/exec or names
// another way to start a process or to make a namespace.
func TestOnlyTheRealLauncherStartsProcesses(t *testing.T) {
	roots := []string{".", "../../cmd/qualify", "../harnesstest"}
	only := "launcher_exec.go"
	forbiddenText := []string{"os.StartProcess", "syscall.ForkExec", "syscall.Exec(", "unix.Exec(", "unix.Unshare", "unix.Setns", "unix.Mount(", "syscall.Unshare", "syscall.Mount(", "CLONE_NEW"}
	sawLauncher := false
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue // a folder this commit of the series does not have yet
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				if imp.Path.Value != `"os/exec"` {
					continue
				}
				if root == "." && name == only {
					sawLauncher = true
					continue
				}
				t.Errorf("%s imports os/exec; only %s may", path, only)
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range forbiddenText {
				if strings.Contains(string(src), w) {
					t.Errorf("%s names %s", path, w)
				}
			}
		}
	}
	if !sawLauncher {
		t.Errorf("%s no longer imports os/exec: the guard is looking in the wrong place", only)
	}
}
