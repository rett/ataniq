package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
	"golang.org/x/sys/unix"
)

func controlSetup(t *testing.T) (layout, *fakeHolder) {
	t.Helper()
	e := newTestEnv(t)
	l := e.layout(t, "c")
	return l, newFakeHolder(t, l, 1)
}

func TestCallControlConversation(t *testing.T) {
	l, h := controlSetup(t)
	req, _ := newRequest(wire.OpStatus, 1)
	resp, err := callControl(l.controlSock, l.daemonLock, req, fastLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Status == nil || resp.Status.Lead != "scratch/c" {
		t.Errorf("response %+v", resp)
	}
	if h.denied.Load() != 0 {
		t.Error("the peer rule refused the command")
	}
	// The lock file holds this process's pid and is private.
	b, err := os.ReadFile(l.daemonLock)
	if err != nil || string(b) != strconv.Itoa(os.Getpid())+"\n" {
		t.Errorf("lock file %q, %v", b, err)
	}
	if m := modeOf(t, l.daemonLock); m.Perm() != 0o600 {
		t.Errorf("lock file mode %v", m)
	}
	// The lock is released: it can be taken at once.
	f, _ := os.Open(l.daemonLock)
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Errorf("the lock was not released: %v", err)
	}
	if reqs := h.requests(); len(reqs) != 1 || reqs[0].Op != wire.OpStatus || reqs[0].Epoch != 1 || reqs[0].CommandID != req.CommandID {
		t.Errorf("requests %+v", reqs)
	}
}

func TestCallControlTruncatesALongerPid(t *testing.T) {
	l, _ := controlSetup(t)
	if err := os.WriteFile(l.daemonLock, []byte(strings.Repeat("9", 50)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, _ := newRequest(wire.OpStatus, 1)
	if _, err := callControl(l.controlSock, l.daemonLock, req, fastLimits()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.daemonLock); string(b) != strconv.Itoa(os.Getpid())+"\n" {
		t.Errorf("lock file %q", b)
	}
	if m := modeOf(t, l.daemonLock); m.Perm() != 0o600 {
		t.Errorf("lock file mode %v", m)
	}
}

func TestCallControlRefusalIsAResponse(t *testing.T) {
	l, _ := controlSetup(t)
	req, _ := newRequest(wire.OpStatus, 9)
	resp, err := callControl(l.controlSock, l.daemonLock, req, fastLimits())
	if err != nil || resp.OK || resp.Code != wire.CodeWrongEpoch {
		t.Errorf("%+v %v", resp, err)
	}
}

func TestFakeServerRefusesAPeerThatDoesNotHoldTheLock(t *testing.T) {
	l, h := controlSetup(t)
	// The pid is right, but nobody holds the flock.
	if err := os.WriteFile(l.daemonLock, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", l.controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload, _ := wire.EncodeRequest(wire.Request{Op: wire.OpStatus, Epoch: 1})
	wire.NewWriter(c, wire.ControlRequests).WriteFrame(wire.TypeRequest, payload)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := wire.NewReader(c, wire.ControlResponses).Next(); err == nil {
		t.Error("the fake server answered a peer that does not hold the lock")
	}
	if h.denied.Load() != 1 {
		t.Errorf("denied %d, want 1", h.denied.Load())
	}
}

func TestFakeServerRefusesAnotherPid(t *testing.T) {
	l, h := controlSetup(t)
	f, err := os.OpenFile(l.daemonLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	f.WriteString(strconv.Itoa(os.Getpid()+1) + "\n") // held, but another process's pid
	c, err := net.Dial("unix", l.controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload, _ := wire.EncodeRequest(wire.Request{Op: wire.OpStatus, Epoch: 1})
	wire.NewWriter(c, wire.ControlRequests).WriteFrame(wire.TypeRequest, payload)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := wire.NewReader(c, wire.ControlResponses).Next(); err == nil {
		t.Error("answered a peer whose pid is not in the lock file")
	}
	if h.denied.Load() != 1 {
		t.Errorf("denied %d", h.denied.Load())
	}
}

func TestCallControlWaitsForAnotherScratchThenTimesOut(t *testing.T) {
	l, _ := controlSetup(t)
	other, err := os.OpenFile(l.daemonLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := unix.Flock(int(other.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	lim := fastLimits()
	lim.lockWait = 100 * time.Millisecond
	req, _ := newRequest(wire.OpStatus, 1)
	start := time.Now()
	_, err = callControl(l.controlSock, l.daemonLock, req, lim)
	if !errors.Is(err, errLockTimeout) {
		t.Errorf("err %v", err)
	}
	if d := time.Since(start); d < 90*time.Millisecond {
		t.Errorf("gave up after %v", d)
	}
	// It waits and succeeds when the other lets go in time.
	lim.lockWait = 5 * time.Second
	done := make(chan error, 1)
	go func() {
		_, err := callControl(l.controlSock, l.daemonLock, req, lim)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	unix.Flock(int(other.Fd()), unix.LOCK_UN)
	if err := <-done; err != nil {
		t.Errorf("after the other let go: %v", err)
	}
}

func TestCallControlConcurrentCallersAreServedOneAtATime(t *testing.T) {
	l, h := controlSetup(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := newRequest(wire.OpStatus, 1)
			resp, err := callControl(l.controlSock, l.daemonLock, req, fastLimits())
			if err == nil && !resp.OK {
				err = errors.New("refused")
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if h.denied.Load() != 0 || len(h.requests()) != n {
		t.Errorf("denied %d, served %d", h.denied.Load(), len(h.requests()))
	}
	seen := map[string]bool{}
	for _, r := range h.requests() {
		if r.CommandID == "" || seen[r.CommandID] {
			t.Errorf("command id %q is empty or repeated", r.CommandID)
		}
		seen[r.CommandID] = true
	}
}

func TestCallControlFailures(t *testing.T) {
	e := newTestEnv(t)
	l := e.layout(t, "c")
	if err := os.MkdirAll(l.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.state, 0o700); err != nil {
		t.Fatal(err)
	}
	req, _ := newRequest(wire.OpStatus, 1)
	// No socket.
	if _, err := callControl(l.controlSock, l.daemonLock, req, fastLimits()); err == nil {
		t.Error("no socket: no error")
	}
	// A server that closes at once.
	ln, err := net.Listen("unix", l.controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := callControl(l.controlSock, l.daemonLock, req, fastLimits()); err == nil {
		t.Errorf("closed at once: %v", err)
	}
	// A request that cannot be encoded is not sent.
	if _, err := callControl(l.controlSock, l.daemonLock, wire.Request{Op: "nonsense", Epoch: 1}, fastLimits()); err == nil {
		t.Error("an invalid request was sent")
	}
	// A server that answers with garbage.
	bad := filepath.Join(l.runtime, "bad.sock")
	ln2, err := net.Listen("unix", bad)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	go func() {
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte{'r', 0, 0, 0, 3, '{', '}', 'x'})
	}()
	if _, err := callControl(bad, l.daemonLock, req, fastLimits()); err == nil {
		t.Error("a garbage answer was accepted")
	}
}

func TestCallControlTimesOutOnASilentServer(t *testing.T) {
	e := newTestEnv(t)
	l := e.layout(t, "c")
	os.MkdirAll(l.runtime, 0o700)
	os.MkdirAll(l.state, 0o700)
	ln, err := net.Listen("unix", l.controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var conns []net.Conn
	var mu sync.Mutex
	defer func() {
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	lim := fastLimits()
	lim.exchange = 100 * time.Millisecond
	req, _ := newRequest(wire.OpStatus, 1)
	if _, err := callControl(l.controlSock, l.daemonLock, req, lim); err == nil {
		t.Error("a silent server: no error")
	}
}
