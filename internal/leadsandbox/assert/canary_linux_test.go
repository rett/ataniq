//go:build linux

package assert

import (
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// hit connects and waits for the canary to close its end, which it does after
// it has counted the connection.
// testCanary is a canary that the test closes.
func testCanary(t *testing.T) *Canary {
	t.Helper()
	c, err := NewCanary()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// waitHits waits, without a sleep, until the canary has counted n connections.
func waitHits(t *testing.T, c *Canary, n int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for c.Hits() < n {
		if time.Now().After(deadline) {
			t.Fatalf("the canary counted %d connections, want %d", c.Hits(), n)
		}
		runtime.Gosched()
	}
}

func hit(t *testing.T, network, addr string) {
	t.Helper()
	c, err := net.DialTimeout(network, addr, 10*time.Second)
	if err != nil {
		t.Fatalf("dial %s %s: %v", network, addr, err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if n, err := io.Copy(io.Discard, c); err != nil || n != 0 {
		t.Fatalf("the canary sent %d bytes or failed: %v", n, err)
	}
}

func TestCanaryCountsConnections(t *testing.T) {
	c, err := NewCanary()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !strings.HasPrefix(c.TCP, "127.0.0.1:") || strings.HasSuffix(c.TCP, ":0") {
		t.Errorf("TCP %q", c.TCP)
	}
	if !strings.HasPrefix(c.Abstract, "ataniq-canary-") || strings.HasPrefix(c.Abstract, "@") || !validAbstractName(c.Abstract) {
		t.Errorf("Abstract %q", c.Abstract)
	}
	if c.Hits() != 0 {
		t.Fatalf("hits before any connection: %d", c.Hits())
	}
	hit(t, "tcp", c.TCP)
	if c.TCP2 != "" {
		if !strings.HasPrefix(c.TCP2, "[::1]:") || c.TCP2 == c.TCP {
			t.Errorf("TCP2 %q", c.TCP2)
		}
	}
	if c.TCPHits() != 1 || c.AbstractHits() != 0 || c.Hits() != 1 {
		t.Errorf("after a tcp connection: %d %d", c.TCPHits(), c.AbstractHits())
	}
	hit(t, "unix", "@"+c.Abstract)
	hit(t, "unix", "@"+c.Abstract)
	if c.TCPHits() != 1 || c.AbstractHits() != 2 || c.Hits() != 3 {
		t.Errorf("after three connections: %d %d", c.TCPHits(), c.AbstractHits())
	}
	// The host system's dial is what the probe uses: it counts as a hit.
	if err := (HostSystem{}).Dial("tcp", c.TCP, time.Second); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Close waited for the loops: the count is final, and it counted the dial.
	if c.Hits() < 3 || c.Hits() > 4 {
		t.Errorf("final hits %d", c.Hits())
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := net.DialTimeout("tcp", c.TCP, time.Second); err == nil {
		t.Error("the TCP listener is still open")
	}
	if _, err := net.DialTimeout("unix", "@"+c.Abstract, time.Second); err == nil {
		t.Error("the abstract socket is still open")
	}
}

func TestCanaryNamesAreRandom(t *testing.T) {
	a, err := NewCanary()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewCanary()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Abstract == b.Abstract || a.TCP == b.TCP {
		t.Errorf("two canaries share an address: %s %s", a.Abstract, b.Abstract)
	}
}

// The probe's checks against a canary that is reachable fail, and against one
// that is closed pass.
func TestLoopbackCheckAgainstCanary(t *testing.T) {
	c, err := NewCanary()
	if err != nil {
		t.Fatal(err)
	}
	s := testSpec("claude")
	s.LoopbackTCP, s.AbstractSocket = c.TCP, c.Abstract
	ck := &checker{spec: s, sys: HostSystem{}}
	r := ck.loopback()
	if r.Status != StatusFailed || !strings.Contains(r.Detail, "reachable") {
		t.Errorf("an open canary: %+v", r)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if r := ck.loopback(); r.Status != StatusOK {
		t.Errorf("a closed canary: %+v", r)
	}
}

func TestCheckCanary(t *testing.T) {
	if r := CheckCanary(nil); r.Status != StatusNotChecked || r.ID != IDLoopbackUnreach+OutsideSuffix || r.Number != 3 {
		t.Errorf("no canary: %+v", r)
	}
	c := testCanary(t)
	if r := CheckCanary(c); r.Status != StatusOK || r.ID != IDLoopbackUnreach+OutsideSuffix {
		t.Errorf("a canary nobody reached: %+v", r)
	}
	// What the probe reports does not matter: a connection that reached the
	// canary is failed, on either listener.
	hit(t, "tcp", c.TCP)
	if r := CheckCanary(c); r.Status != StatusFailed || !strings.Contains(r.Detail, "accepted 1 connections") {
		t.Errorf("a loopback connection: %+v", r)
	}
	c2 := testCanary(t)
	hit(t, "unix", "@"+c2.Abstract)
	if r := CheckCanary(c2); r.Status != StatusFailed || !strings.Contains(r.Detail, "1 on the abstract socket") {
		t.Errorf("an abstract connection: %+v", r)
	}
	// A canary that has been closed cannot say that nothing came after.
	c3 := testCanary(t)
	if err := c3.Close(); err != nil {
		t.Fatal(err)
	}
	if r := CheckCanary(c3); r.Status != StatusNotChecked || !strings.Contains(r.Detail, "stopped") {
		t.Errorf("a closed canary: %+v", r)
	}
	// The probe's dial against the real canary: the probe sees the connection
	// and fails, and the holder's count agrees.
	c4 := testCanary(t)
	s := testSpec("claude")
	s.LoopbackTCP, s.AbstractSocket = c4.TCP, c4.Abstract
	if r := (&checker{spec: s, sys: HostSystem{}}).loopback(); r.Status != StatusFailed {
		t.Errorf("the probe against an open canary: %+v", r)
	}
	// The probe's dial returns when the kernel has completed the connection,
	// which is before the canary has accepted and counted it.
	waitHits(t, c4, 2)
	if r := CheckCanary(c4); r.Status != StatusFailed {
		t.Errorf("the holder against a reached canary: %+v", r)
	}
}

// listener is a net.Listener whose Accept the test drives.
type scriptedListener struct {
	errs    chan error    // what Accept returns next, in order, before it waits for a connection
	conns   chan net.Conn // connections to hand out
	closed  chan struct{}
	once    sync.Once
	release chan struct{} // when set, Accept waits on it after Close before it returns
	exiting chan struct{} // signalled when Accept starts to return after Close
}

func newScriptedListener() *scriptedListener {
	return &scriptedListener{
		errs: make(chan error, 16), conns: make(chan net.Conn, 16),
		closed: make(chan struct{}), exiting: make(chan struct{}, 4),
	}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	select {
	case err := <-l.errs:
		return nil, err
	default:
	}
	select {
	case err := <-l.errs:
		return nil, err
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		l.exiting <- struct{}{}
		if l.release != nil {
			<-l.release
		}
		return nil, net.ErrClosed
	}
}

func (l *scriptedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{} }

// scriptedCanary runs the accept loop of a canary on a scripted listener, with
// short back-off times.
func scriptedCanary(l net.Listener, min, max time.Duration) *Canary {
	c := &Canary{done: make(chan struct{}), backoffMin: min, backoffMax: max, lns: []net.Listener{l}}
	c.serve(l, &c.tcpHits)
	return c
}

// A transient error of Accept does not end the loop: it is counted, the loop
// goes on, a later connection is counted, and the holder's check says
// not-checked for a count that may be short, and failed for one above 0.
func TestCanaryAcceptSurvivesTransientErrors(t *testing.T) {
	l := newScriptedListener()
	for i := 0; i < 3; i++ {
		l.errs <- errors.New("accept: too many open files")
	}
	c := scriptedCanary(l, time.Microsecond, 4*time.Microsecond)
	defer c.Close()
	client, server := net.Pipe()
	l.conns <- server
	// The canary closes its end after it has counted the connection.
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if n, err := io.Copy(io.Discard, client); err != nil || n != 0 {
		t.Fatalf("pipe: %d %v", n, err)
	}
	if c.AcceptErrors() != 3 || c.Hits() != 1 {
		t.Errorf("accept errors %d, hits %d; want 3 and 1", c.AcceptErrors(), c.Hits())
	}
	if c.Stopped() {
		t.Error("the loop ended on a transient error")
	}
	if r := CheckCanary(c); r.Status != StatusFailed {
		t.Errorf("a hit with accept errors is failed: %+v", r)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !c.Stopped() {
		t.Error("Close did not stop the loop")
	}
}

func TestCanaryAcceptErrorsMakeAnEmptyCountNotChecked(t *testing.T) {
	l := newScriptedListener()
	l.errs <- errors.New("accept: too many open files")
	c := scriptedCanary(l, time.Microsecond, 2*time.Microsecond)
	defer c.Close()
	// Wait, without a sleep, until the loop has met the error.
	for c.AcceptErrors() == 0 {
		runtime.Gosched()
	}
	if r := CheckCanary(c); r.Status != StatusNotChecked || !strings.Contains(r.Detail, "errors of accept") {
		t.Errorf("accept errors and no hit: %+v", r)
	}
}

// Close ends a loop that is waiting out an error, however long the back-off.
func TestCanaryCloseEndsABackoff(t *testing.T) {
	l := newScriptedListener()
	l.errs <- errors.New("accept: too many open files")
	c := scriptedCanary(l, time.Hour, time.Hour)
	for c.AcceptErrors() == 0 {
		runtime.Gosched()
	}
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not end a loop waiting out an error")
	}
}

// Close waits for the loops: it does not return while one is still running.
func TestCanaryCloseWaitsForTheLoops(t *testing.T) {
	l := newScriptedListener()
	l.release = make(chan struct{})
	c := scriptedCanary(l, time.Millisecond, time.Millisecond)
	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	<-l.exiting // Close closed the listener and the loop is on its way out, held by the test
	for i := 0; i < 2000; i++ {
		select {
		case <-closed:
			t.Fatal("Close returned while a loop was still running")
		default:
			runtime.Gosched()
		}
	}
	close(l.release)
	<-closed
	if !c.Stopped() {
		t.Error("the loop did not end")
	}
}

// The canary also listens on ::1 when the host has it, and counts there; a
// host without it is no failure and TCP2 stays empty.
func TestCanaryListensOnIPv6Loopback(t *testing.T) {
	c := testCanary(t)
	if c.TCP2 == "" {
		t.Skip("this host has no IPv6 loopback")
	}
	hit(t, "tcp", c.TCP2)
	if c.TCP2Hits() != 1 || c.TCPHits() != 0 || c.AbstractHits() != 0 || c.Hits() != 1 {
		t.Errorf("hits after a ::1 connection: %d %d %d", c.TCP2Hits(), c.TCPHits(), c.AbstractHits())
	}
	if r := CheckCanary(c); r.Status != StatusFailed || !strings.Contains(r.Detail, "1 on loopback") {
		t.Errorf("a ::1 connection: %+v", r)
	}
	// The probe, with the second address in its spec, fails on the open
	// canary, and passes on a closed one.
	s := testSpec("claude")
	s.LoopbackTCP, s.LoopbackTCP2, s.AbstractSocket = c.TCP, c.TCP2, c.Abstract
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	ck := &checker{spec: s, sys: HostSystem{}}
	if r := ck.loopback(); r.Status != StatusFailed || !strings.Contains(r.Detail, "second host loopback port") {
		t.Errorf("an open canary: %+v", r)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if r := ck.loopback(); r.Status != StatusOK || !strings.Contains(r.Detail, "second host loopback port") {
		t.Errorf("a closed canary: %+v", r)
	}
}
