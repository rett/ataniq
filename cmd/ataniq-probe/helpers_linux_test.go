//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
)

// testEnv is the environment a test gives the probe: what the agent must see,
// too. modeEnv selects the test binary's role (see TestMain).
const (
	modeEnv  = "ATANIQ_PROBE_TEST_MODE"
	modeFake = "fake"
	modeReal = "real"
)

// testSpec is a valid spec of invented paths. For the fake system it is
// all that matters; for the real one it fails (the tree is not there), and the
// loopback port is one nothing listens on and the provider host is one that
// resolves from the hosts file, so that a real run touches nothing outside the
// machine.
func testSpec(t *testing.T) assert.Spec {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	rt := profile.Claude
	return assert.Spec{
		Version: assert.SpecVersion,
		Digest:  strings.Repeat("ab", 32),
		Runtime: rt,
		Env: []assert.EnvEntry{
			{Name: "HOME", Value: "/home/lead"}, {Name: "PATH", Value: "/usr/bin:/bin"}, {Name: "TERM", Value: "xterm-256color"},
		},
		HolderPaths:     []string{"/run/example/holder.sock", "/var/lib/example/holder"},
		MountSubstrings: []string{"/run/example", "/home/owner"},
		OwnerPaths:      []string{"/home/owner/.claude", "/home/owner/.ssh"},
		OtherConfigDir:  profile.ConfigInside(rt.Other()),
		LoopbackTCP:     addr,
		AbstractSocket:  fmt.Sprintf("ataniq-probe-test-%d-%d", os.Getpid(), time.Now().UnixNano()),
		ProviderHost:    "localhost",
		Tree:            "/work/ataniq",
		GitReadOnly:     []string{"/work/ataniq/.git/config", "/work/ataniq/.git/hooks", "/work/ataniq/.git/info"},
		ManagedSettings: profile.ClaudeManagedInside,
		ManagedMarker:   "example-guard-hook",

		ConnectTimeoutMS: 100,
		ResolveTimeoutMS: 100,
	}
}

// writeSpec writes the spec under t.TempDir() and returns its path.
func writeSpec(t *testing.T, s assert.Spec) string {
	t.Helper()
	data, err := assert.EncodeSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeSystem is an assert.System for testSpec's spec. With nothing changed it
// passes every inside check; a test makes one fail by changing a field. It is
// data only: the real checks run unchanged against it.
type fakeSystem struct {
	spec   assert.Spec
	env    []string
	nodes  map[string]fs.FileMode
	data   map[string]string
	mounts []assert.Mount
	ids    map[string]int
}

func passSystem(s assert.Spec) *fakeSystem {
	f := &fakeSystem{
		spec:  s,
		nodes: map[string]fs.FileMode{},
		data: map[string]string{
			"/proc/sys/dev/tty/legacy_tiocsti": "0\n",
			s.ManagedSettings:                  `{"hooks":"` + s.ManagedMarker + `"}`,
		},
		ids: map[string]int{},
	}
	for _, e := range s.Env {
		f.env = append(f.env, e.Name+"="+e.Value)
	}
	f.nodes[s.Tree] = fs.ModeDir | 0o755
	f.nodes[s.Tree+"/.git"] = fs.ModeDir | 0o755
	f.nodes[s.GitReadOnly[0]] = 0o644
	f.nodes[s.GitReadOnly[1]] = fs.ModeDir | 0o755
	f.nodes[s.GitReadOnly[2]] = fs.ModeDir | 0o755
	f.nodes[s.ManagedSettings] = 0o644
	f.mounts = []assert.Mount{{ID: 1, ParentID: 0, Root: "/", Point: "/", Options: "rw", FSType: "tmpfs", Source: "tmpfs", SuperOptions: "rw"}}
	for i, p := range append(append([]string(nil), s.GitReadOnly...), s.ManagedSettings) {
		id := 10 + i
		f.mounts = append(f.mounts, assert.Mount{ID: id, ParentID: 1, Root: "/", Point: p, Options: "ro,nosuid", FSType: "tmpfs", Source: "tmpfs", SuperOptions: "rw"})
		f.ids[p] = id
	}
	return f
}

func (f *fakeSystem) Lstat(p string) (assert.FileInfo, error) {
	mode, ok := f.nodes[p]
	if !ok {
		return assert.FileInfo{}, &fs.PathError{Op: "lstat", Path: p, Err: syscall.ENOENT}
	}
	return assert.FileInfo{Mode: mode}, nil
}

func (f *fakeSystem) ReadFile(p string, limit int) ([]byte, error) {
	if p == "/proc/self/status" {
		return []byte("Seccomp:\t0\n"), nil
	}
	d, ok := f.data[p]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
	}
	return []byte(d), nil
}

func (f *fakeSystem) Mounts() ([]assert.Mount, error) { return f.mounts, nil }
func (f *fakeSystem) Environ() []string               { return f.env }
func (f *fakeSystem) Dial(network, address string, _ time.Duration) error {
	return syscall.ECONNREFUSED
}
func (f *fakeSystem) LookupHost(string, time.Duration) ([]string, error) {
	return []string{"192.0.2.1"}, nil
}
func (f *fakeSystem) MountID(p string) (int, error) {
	if id, ok := f.ids[p]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("statx %s: %w", p, assert.ErrNoMountID)
}
func (f *fakeSystem) DirEmpty(string) (bool, error) { return true, nil }
func (f *fakeSystem) Writable(string) (bool, error) { return false, nil }

// agentFD is one descriptor above 2 as the test agent sees it.
type agentFD struct {
	FD      int    `json:"fd"`
	Flags   int    `json:"flags"`
	Cloexec bool   `json:"cloexec"`
	Link    string `json:"link"`
	Head    string `json:"head,omitempty"`
}

// agentReport is what the test agent writes about itself.
type agentReport struct {
	PID      int       `json:"pid"`
	SID      int       `json:"sid"`
	PGID     int       `json:"pgid"`
	Env      []string  `json:"env"`
	FDs      []agentFD `json:"fds"`
	Args     []string  `json:"args"`
	TIOCGSID int       `json:"tiocgsid"`
	DevTTY   bool      `json:"dev_tty"`
}

// agentMain is the trivial agent: the test binary again. It writes what it
// sees to the file OUT, with plain system calls only (no os.File, so no
// descriptor of the runtime's own), then, when HOLD is "1", says READY on
// descriptor 1 and waits for a byte on descriptor 0. It exits with EXIT. It
// reads the files that the pass-through mode opened for reading and appends
// "hi" to the ones opened for writing.
func agentMain(args []string) int {
	out, exit, hold := args[0], 0, args[2] == "1"
	fmt.Sscan(args[1], &exit)
	r := agentReport{PID: unix.Getpid(), PGID: unix.Getpgrp(), Env: os.Environ(), Args: args, TIOCGSID: -1}
	r.SID, _ = unix.Getsid(0)
	if sid, err := unix.IoctlGetInt(0, unix.TIOCGSID); err == nil {
		r.TIOCGSID = sid
	}
	if fd, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_NOCTTY, 0); err == nil {
		r.DevTTY = true
		unix.Close(fd)
	}
	for fd := 3; fd < 2000; fd++ {
		fdflags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			continue
		}
		flags, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		buf := make([]byte, 256)
		n, _ := unix.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd), buf)
		a := agentFD{FD: fd, Flags: flags, Cloexec: fdflags&unix.FD_CLOEXEC != 0, Link: string(buf[:n])}
		if strings.HasPrefix(a.Link, "/") {
			switch flags & unix.O_ACCMODE {
			case unix.O_RDONLY:
				h := make([]byte, 64)
				n, _ := unix.Read(fd, h)
				a.Head = string(h[:max(n, 0)])
			case unix.O_WRONLY:
				unix.Write(fd, []byte("hi"))
			}
		}
		r.FDs = append(r.FDs, a)
	}
	data, _ := json.Marshal(r)
	o, err := unix.Open(out, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC, 0o600)
	if err != nil {
		unix.Write(2, []byte("agent: "+err.Error()+"\n"))
		return 120
	}
	unix.Write(o, data)
	unix.Close(o)
	if hold {
		unix.Write(1, []byte("READY\n"))
		b := make([]byte, 1)
		unix.Read(0, b)
	}
	return exit
}

func TestMain(m *testing.M) {
	mode := os.Getenv(modeEnv)
	if mode == "" {
		os.Exit(m.Run())
	}
	// The test binary as the probe, or as the agent the probe executes. The
	// variable stays in the environment the agent gets; the tests compare
	// with the environment they gave.
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		os.Exit(agentMain(os.Args[2:]))
	}
	if mode == modeFake {
		newSystem = func(s assert.Spec) assert.System { return passSystem(s) }
	}
	os.Exit(execute(os.Args, os.Stderr))
}
