package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/seatgate"
)

// gate-init writes a loadable identity and prints its fingerprint, one line
// and nothing else; the key file is private and the folder is made 0700.
func TestGateInitPrintsOnlyTheFingerprint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seat")
	var out, errout bytes.Buffer
	if code := dispatch(context.Background(), []string{"gate-init", "--identity", dir}, &out, &errout); code != 0 {
		t.Fatalf("code %d: %q", code, errout.String())
	}
	id, err := seatgate.LoadIdentity(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.String() != id.Fingerprint()+"\n" {
		t.Errorf("stdout %q, want the fingerprint %q alone", out.String(), id.Fingerprint())
	}
	if errout.Len() != 0 {
		t.Errorf("stderr %q", errout.String())
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v %v", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Join(dir, seatgate.KeyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v %v", fi.Mode().Perm(), err)
	}
	key, _ := os.ReadFile(filepath.Join(dir, seatgate.KeyFile))
	if bytes.Contains(out.Bytes(), key) || strings.Contains(out.String(), "PRIVATE") {
		t.Error("the key is printed")
	}
}

// A second gate-init into the same folder writes nothing, prints no
// fingerprint and leaves the first key as it was.
func TestGateInitNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seat")
	if code := dispatch(context.Background(), []string{"gate-init", "--identity", dir}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("first gate-init failed")
	}
	before, _ := os.ReadFile(filepath.Join(dir, seatgate.KeyFile))
	var out, errout bytes.Buffer
	if code := dispatch(context.Background(), []string{"gate-init", "--identity", dir}, &out, &errout); code != 1 {
		t.Errorf("second gate-init: code %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout %q", out.String())
	}
	if !strings.Contains(errout.String(), "already exists") {
		t.Errorf("stderr %q", errout.String())
	}
	if strings.Contains(errout.String(), dir) {
		t.Errorf("the error names the path: %q", errout.String())
	}
	after, _ := os.ReadFile(filepath.Join(dir, seatgate.KeyFile))
	if !bytes.Equal(before, after) {
		t.Error("the key was overwritten")
	}
}

// A folder that others can write is refused, and nothing is written in it.
func TestGateInitRefusesAnUnsafeFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seat")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := dispatch(context.Background(), []string{"gate-init", "--identity", dir}, &out, &errout); code != 1 || out.Len() != 0 {
		t.Errorf("code %d, stdout %q", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, seatgate.KeyFile)); err == nil {
		t.Error("a key was written")
	}
}

func TestGateInitUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no identity":    {"gate-init"},
		"empty identity": {"gate-init", "--identity", ""},
		"stray argument": {"gate-init", "--identity", "/x", "more"},
		"unknown flag":   {"gate-init", "--identity", "/x", "--force"},
		"a server flag":  {"gate-init", "--identity", "/x", "--origin", "https://seat.example"},
	} {
		var out, errout bytes.Buffer
		if code := dispatch(context.Background(), args, &out, &errout); code != 2 {
			t.Errorf("%s: code %d", name, code)
		}
		if out.Len() != 0 || !strings.Contains(errout.String(), "Usage: ataniq-seat gate-init") {
			t.Errorf("%s: stdout %q, stderr %q", name, out.String(), errout.String())
		}
	}
}
