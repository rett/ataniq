//go:build linux

package assert

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The real System, on files under t.TempDir() and on the process's own
// environment, for the file and environment checks only.

func TestHostSystemFiles(t *testing.T) {
	dir := t.TempDir()
	h := HostSystem{}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := h.Lstat(file)
	if err != nil || !info.IsRegular() || info.Size != 5 || info.Describe() != "a regular file" {
		t.Errorf("Lstat file: %+v %v", info, err)
	}
	if info, err := h.Lstat(filepath.Join(dir, "sub")); err != nil || !info.IsDir() {
		t.Errorf("Lstat dir: %+v %v", info, err)
	}
	if info, err := h.Lstat(filepath.Join(dir, "link")); err != nil || !info.IsSymlink() || info.IsRegular() {
		t.Errorf("Lstat link: %+v %v", info, err)
	}
	if info, err := h.Lstat(filepath.Join(dir, "fifo")); err != nil || info.IsRegular() || info.Describe() != "a special file" {
		t.Errorf("Lstat fifo: %+v %v", info, err)
	}
	if _, err := h.Lstat(filepath.Join(dir, "none")); !absent(err) {
		t.Errorf("Lstat missing: %v", err)
	}
	if _, err := h.Lstat(filepath.Join(file, "below")); !absent(err) {
		t.Errorf("Lstat below a file: %v", err)
	}

	if data, err := h.ReadFile(file, 5); err != nil || string(data) != "hello" {
		t.Errorf("ReadFile at the limit: %q %v", data, err)
	}
	if _, err := h.ReadFile(file, 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadFile over the limit: %v", err)
	}
	if _, err := h.ReadFile(file, 0); err == nil {
		t.Error("ReadFile with no limit")
	}
	if _, err := h.ReadFile(filepath.Join(dir, "link"), 100); !errors.Is(err, ErrNotRegular) {
		t.Errorf("ReadFile of a link: %v", err)
	}
	if _, err := h.ReadFile(filepath.Join(dir, "sub"), 100); !errors.Is(err, ErrNotRegular) {
		t.Errorf("ReadFile of a folder: %v", err)
	}
	// A FIFO with no writer must not block.
	done := make(chan error, 1)
	go func() { _, err := h.ReadFile(filepath.Join(dir, "fifo"), 100); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadFile of a FIFO: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadFile of a FIFO blocked")
	}
	if _, err := h.ReadFile(filepath.Join(dir, "none"), 100); !absent(err) {
		t.Errorf("ReadFile of a missing file: %v", err)
	}
}

func TestHostSystemEnvironAndWritable(t *testing.T) {
	t.Setenv("ASSERT_TEST_VAR", "value")
	found := false
	for _, e := range (HostSystem{}).Environ() {
		found = found || e == "ASSERT_TEST_VAR=value"
	}
	if !found {
		t.Error("Environ does not hold a variable the test set")
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err := (HostSystem{}).Writable(file); err != nil || !w {
		t.Errorf("Writable of an own file: %v %v", w, err)
	}
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 { // root writes to a mode 0400 file
		if w, err := (HostSystem{}).Writable(file); err != nil || w {
			t.Errorf("Writable of a read-only file: %v %v", w, err)
		}
	}
	if _, err := (HostSystem{}).Writable(filepath.Join(dir, "none")); !absent(err) {
		t.Errorf("Writable of a missing file: %v", err)
	}
	// The check did not write.
	if fi, err := os.Stat(file); err != nil || fi.Size() != 0 {
		t.Errorf("the file changed: %v %v", fi, err)
	}
}

func TestHostSystemMounts(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(fixture, []byte(sampleMountInfo), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := (HostSystem{MountInfoPath: fixture}).Mounts()
	if err != nil || len(ms) != 5 {
		t.Errorf("fixture: %d mounts, %v", len(ms), err)
	}
	if err := os.WriteFile(fixture, []byte(strings.Repeat("x", MaxMountTableBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (HostSystem{MountInfoPath: fixture}).Mounts(); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized table: %v", err)
	}
	// The process's own table parses and has a root mount.
	ms, err = (HostSystem{}).Mounts()
	if err != nil {
		t.Skipf("no readable mount table here: %v", err)
	}
	if _, ok := mountFor(ms, "/"); !ok {
		t.Errorf("no mount holds /: %d mounts", len(ms))
	}
}

func TestHostSystemDial(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := HostSystem{}
	if err := h.Dial("tcp", l.Addr().String(), time.Second); err != nil {
		t.Errorf("dial a listener: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	err = h.Dial("tcp", addr, time.Second)
	if err == nil || !unreachable(err) {
		t.Errorf("dial a closed port: %v", err)
	}
	if err := h.Dial("udp", addr, time.Second); err == nil {
		t.Error("dial of udp allowed")
	}
	if err := h.Dial("unix", "@ataniq-no-such-socket-for-test", time.Second); err == nil || !unreachable(err) {
		t.Errorf("dial a missing abstract socket: %v", err)
	}
}

func TestHostSystemLookupHost(t *testing.T) {
	addrs, err := (HostSystem{}).LookupHost("localhost", 5*time.Second)
	if err != nil {
		t.Skipf("localhost does not resolve here: %v", err)
	}
	if len(addrs) == 0 {
		t.Error("no address")
	}
}

// The inside checks against real files: the environment and a managed
// settings file in a temporary folder.
func TestHostSystemWithChecks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed.json")
	if err := os.WriteFile(path, []byte(`{"hook":"example-guard-hook"}`), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSERT_ONLY", "1")
	s := testSpec("claude")
	s.ManagedSettings = path
	// The managed path of a claude spec is fixed, so check with the checker.
	c := &checker{spec: s, sys: HostSystem{}}
	var tl tally
	c.mustBeAbsent(&tl, "x", filepath.Join(dir, "none"))
	if len(tl.failed)+len(tl.unchecked) != 0 {
		t.Errorf("mustBeAbsent of a missing path: %+v", tl)
	}
	c.mustBeAbsent(&tl, "x", path)
	if len(tl.failed) != 1 {
		t.Errorf("mustBeAbsent of a present path: %+v", tl)
	}
	// The environment check, with the real environment as the allowlist.
	s.Env = nil
	for _, e := range os.Environ() {
		name, value, _ := strings.Cut(e, "=")
		if validEnvName(name) && len(value) <= MaxEnvValueBytes && !hasControl(value) && len(s.Env) < MaxEnvEntries {
			s.Env = append(s.Env, EnvEntry{name, value})
		}
	}
	c = &checker{spec: s, sys: HostSystem{}}
	if r := c.environment(); r.Status != StatusOK && len(os.Environ()) == len(s.Env) {
		t.Errorf("the real environment against itself: %+v", r)
	}
	s.Env = append(s.Env, EnvEntry{"ASSERT_NOT_SET", "x"})
	c = &checker{spec: s, sys: HostSystem{}}
	if r := c.environment(); r.Status != StatusFailed || !strings.Contains(r.Detail, "ASSERT_NOT_SET") {
		t.Errorf("a missing variable: %+v", r)
	}
}
