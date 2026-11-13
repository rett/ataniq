package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func root(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func fakePath(t *testing.T, goScript string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"git", "make", "sh", "dirname", "cat"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if goScript != "" {
		if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\n"+goScript), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDoctor(t *testing.T) {
	pin, err := os.ReadFile(filepath.Join(root(t), ".go-version"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(pin))
	// The driver simulates an older executable selecting a cached toolchain.
	selected := "[ \"$GOENV\" = off ] && [ \"$GOWORK\" = off ] && [ \"$GOPROXY\" = off ] && [ \"$GOSUMDB\" = off ] && [ \"$GOTOOLCHAIN\" = go" + version + "+auto ] || exit 7\nprintf 'go version go" + version + " linux/amd64\\n'\n"
	for _, tc := range []struct {
		name, driver, want string
		pass               bool
	}{
		{"selected cached toolchain", selected, "ok: go version go" + version, true},
		{"missing driver", "", "missing: go", false},
		{"mismatched driver", "printf 'go version go1.2 linux/amd64\\n'\n", "fail: selected Go", false},
		{"unavailable offline", "[ \"$GOPROXY\" = off ] || exit 8\nexit 1\n", "fail: selected Go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "scripts/go-tool.sh", "doctor")
			cmd.Dir = root(t)
			cmd.Env = append(os.Environ(), "PATH="+fakePath(t, tc.driver), "GOENV=hostile", "GOWORK=hostile", "GOTOOLCHAIN=local", "GOPROXY=https://invalid.example")
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.pass || !strings.Contains(string(out), tc.want) {
				t.Fatalf("output %s; error %v", out, err)
			}
		})
	}
}

func TestBootstrapPreview(t *testing.T) {
	dir := fakePath(t, "exit 99\n")
	cmd := exec.Command(filepath.Join(dir, "make"), "-n", "bootstrap")
	cmd.Dir = root(t)
	cmd.Env = append(os.Environ(), "PATH="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "go mod download") || !strings.Contains(string(out), "go version") {
		t.Fatalf("preview %s; error %v", out, err)
	}
}
