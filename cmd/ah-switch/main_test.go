package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

func call(args ...string) (int, string, string) {
	var out, errOut strings.Builder
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsage(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{},
		{"--root", root},
		{"--root", root, "0001-aaaaaaa", "0002-bbbbbbb"},
		{"--root", root, "--rollback-to", "previous", "0001-aaaaaaa"},
		{"--root", root, "--rollback-to", "latest"},
		{"--root", root, "--mode", "trust", "0001-aaaaaaa"},
		{"--root", root, "--mode", "signed", "0001-aaaaaaa"},
		{"--root", root, "--allowed-signers", "/x", "0001-aaaaaaa"},
		{"--root", root, "7-aaaaaaa"},
		{"--root", root, "--dry", "status"},
		{"--root", root, "--no-such-flag", "0001-aaaaaaa"},
		{"--root", "", "status"},
	} {
		code, _, errOut := call(args...)
		if code != switcher.ExitUsage || !strings.Contains(errOut, "ah-switch: ") {
			t.Errorf("%q: exit %d, stderr %q", args, code, errOut)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Errorf("a usage error wrote into the root: %v, %v", entries, err)
	}
	if code, out, _ := call("--help"); code != switcher.ExitOK || !strings.Contains(out, "usage: ah-switch") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}

func TestStatusOfEmptyRoot(t *testing.T) {
	code, out, errOut := call("status", "--root", t.TempDir())
	if code != switcher.ExitOK || !strings.Contains(out, "current: none") {
		t.Errorf("exit %d, %q, %q", code, out, errOut)
	}
}

func write(t *testing.T, path, content string, perm fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// release writes a release whose no-AI check and health command are
// scripts, with its SHA256SUMS.
func release(t *testing.T, root, id string, counter int, health string) {
	t.Helper()
	dir := filepath.Join(root, switcher.BaseDir, "releases", id)
	commit := strings.SplitN(id, "-", 2)[1]
	write(t, filepath.Join(dir, "MANIFEST"), "id: "+id+"\ncounter: "+strconv.Itoa(counter)+"\ncommit: "+commit+"\nbuilt_on: dev\ngo_version: go\npackage_snapshot: s\n", 0o644)
	write(t, filepath.Join(dir, "bin/ah-noaicheck"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(dir, "share/health.cmd"), health+"\n", 0o644)
	var lines []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(dir, path)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+rel)
		return nil
	})
	sort.Strings(lines)
	write(t, filepath.Join(dir, "SHA256SUMS"), strings.Join(lines, "\n")+"\n", 0o644)
}

func TestSwitchRollbackAndStatus(t *testing.T) {
	root := t.TempDir()
	health := filepath.Join(t.TempDir(), "health")
	write(t, health, "#!/bin/sh\nexit 0\n", 0o755)
	release(t, root, "0001-aaaaaaa", 1, health)
	release(t, root, "0002-bbbbbbb", 2, health)

	code, out, errOut := call("--root", root, "--dry", "0001-aaaaaaa")
	if code != switcher.ExitOK || !strings.Contains(out, "would point current at releases/0001-aaaaaaa") {
		t.Fatalf("dry: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(root, switcher.StateDir)); err == nil {
		t.Fatalf("--dry made the state folder")
	}
	for _, id := range []string{"0001-aaaaaaa", "0002-bbbbbbb"} {
		code, out, errOut := call(id, "--root", root)
		if code != switcher.ExitOK {
			t.Fatalf("switch to %s: exit %d\n%s%s", id, code, out, errOut)
		}
	}
	code, out, _ = call("--root", root, "0001-aaaaaaa")
	if code != switcher.ExitFailed || !strings.Contains(out, "not above the highest ever activated") {
		t.Errorf("lower counter: exit %d\n%s", code, out)
	}
	code, out, _ = call("--root", root, "--rollback-to", "previous")
	if code != switcher.ExitOK {
		t.Fatalf("rollback: exit %d\n%s", code, out)
	}
	code, out, _ = call("--root", root, "status")
	if code != switcher.ExitOK {
		t.Fatalf("status: exit %d", code)
	}
	for _, want := range []string{"current: 0001-aaaaaaa", "previous: 0002-bbbbbbb", "highest counter: 2", "last switch: rollback to 0001-aaaaaaa: ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}
