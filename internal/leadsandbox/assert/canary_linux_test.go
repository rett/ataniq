//go:build linux

package assert

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// hit connects and waits for the canary to close its end, which it does after
// it has counted the connection.
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
