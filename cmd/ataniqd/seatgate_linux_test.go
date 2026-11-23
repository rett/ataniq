//go:build linux && ataniq_ownerchannel

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/seatgate"
)

// gateBuf is a standard error that the daemon's goroutines and the test may
// use at once.
type gateBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (g *gateBuf) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.b.Write(p)
}

func (g *gateBuf) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.b.String()
}

// gateService is the owner channel's service of the gate's tests: it counts
// the reads it serves, and when it is closed it records whether the gate's
// port still took a connection.
type gateService struct {
	ownerCmdService
	lists       atomic.Int64
	closes      atomic.Int64
	addr        string
	openAtClose atomic.Bool
}

func (s *gateService) List(ctx context.Context) (ownerwire.Listing, error) {
	s.lists.Add(1)
	return s.ownerCmdService.List(ctx)
}

// gateSetup puts a builder of a gateService in place of the command's, with
// no guard and an inspector that finds a terminal, and returns the service.
func gateSetup(t *testing.T, addr string) *gateService {
	t.Helper()
	svc := &gateService{addr: addr}
	oldBuild, oldInspect, oldGuard := ownerServiceBuilder, ownerInspector, ownerGuard
	ownerGuard = nil
	ownerServiceBuilder = func(string) (leaddaemon.OwnerService, func() error, error) {
		return svc, func() error {
			svc.closes.Add(1)
			if svc.addr != "" {
				if c, err := net.DialTimeout("tcp", svc.addr, time.Second); err == nil {
					c.Close()
					svc.openAtClose.Store(true)
				}
			}
			return nil
		}, nil
	}
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerServiceBuilder, ownerInspector, ownerGuard = oldBuild, oldInspect, oldGuard })
	return svc
}

// freeLoopback returns a loopback address with a port that was free a moment
// ago.
func freeLoopback(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// newSeat makes a seat's identity in memory.
func newSeat(t *testing.T, name string) seatgate.Identity {
	t.Helper()
	certPEM, keyPEM, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := seatgate.ParseIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// gateInit runs "ataniqd seat-gate init" for the state folder and returns the
// fingerprint it printed.
func gateInit(t *testing.T, state string) string {
	t.Helper()
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"seat-gate", "init", "--state-dir", state}); code != 0 {
		t.Fatalf("seat-gate init: exit %d: %s", code, ta.err.String())
	}
	fp := strings.TrimSuffix(ta.out.String(), "\n")
	if !seatgate.ValidFingerprint(fp) || ta.out.String() != fp+"\n" {
		t.Fatalf("seat-gate init printed %q", ta.out.String())
	}
	return fp
}

// writePins writes the pin file of the state folder with the given text.
func writePins(t *testing.T, state, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(seatGateDir(state), leaddaemon.SeatPinsFileName)
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// dialGate connects to the gate as seat, pinning the gate's key.
func dialGate(addr string, seat seatgate.Identity, gateFP string) (*seatgate.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return seatgate.Dial(ctx, addr, seatgate.ClientConfig{
		TLS:        seatgate.ClientTLS(seat, gateFP, "gate.example.invalid"),
		Caps:       gatehost.Caps(),
		BackoffMin: 10 * time.Millisecond,
		BackoffMax: 20 * time.Millisecond,
	})
}

// The gate is served on the address given, to a pinned seat only, with the
// capabilities pair and read (no seat origin is configured); a seat whose key
// is not pinned never reaches the service. The start line names the address
// and the gate's key, says why it serves pair and read only, and holds no
// pin. At shutdown the gate stops before the service is closed, and its port
// takes no connection after.
func TestSeatGateServesAPinnedSeatOnly(t *testing.T) {
	addr := freeLoopback(t)
	svc := gateSetup(t, addr)
	state, runtime := ownerCmdDirs(t)
	gateFP := gateInit(t, state)
	seat, other := newSeat(t, "seat"), newSeat(t, "other")
	writePins(t, state, "# the seat of the authority host\n"+seat.Fingerprint()+"\n\n", 0o600)

	ta := newTestApp(t, nil)
	errs := &gateBuf{}
	ta.stderr = errs
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel", "--seat-gate", addr}, runtime+"/daemon.sock")

	log := errs.String()
	if want := "ataniqd: seat gate: listening on " + addr + ", key fingerprint " + gateFP + "; it serves pair and read only: no configuration folder (XDG_CONFIG_HOME or HOME) to read the seat origin from\n"; !strings.Contains(log, want) {
		t.Fatalf("no start line %q in:\n%s", want, log)
	}
	if strings.Count(log, "seat gate:") != 1 || strings.Contains(log, seat.Fingerprint()) {
		t.Fatalf("the start line is not the one gate line, or a pin is in the log:\n%s", log)
	}

	c, err := dialGate(addr, seat, gateFP)
	if err != nil {
		t.Fatalf("the pinned seat: %v", err)
	}
	if got := strings.Join(c.Caps(), " "); got != strings.Join(gatehost.Caps(), " ") || got != "pair read" {
		t.Fatalf("the gate's capabilities: %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var listing ownerwire.Listing
	if err := c.Call(ctx, ownerwire.OpList, nil, &listing); err != nil {
		t.Fatalf("owner.list from the pinned seat: %v", err)
	}
	var re *seatgate.RemoteError
	if err := c.Call(ctx, ownerwire.OpDecide, map[string]string{}, nil); !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
		t.Fatalf("owner.decide over the gate: %v", err)
	}
	c.Close()
	if n := svc.lists.Load(); n != 1 {
		t.Fatalf("the service served %d lists, want 1", n)
	}

	// A seat whose key is not pinned, and one that pins another gate key.
	if c, err := dialGate(addr, other, gateFP); err == nil {
		c.Close()
		t.Fatal("a seat whose key is not pinned connected")
	}
	if c, err := dialGate(addr, seat, other.Fingerprint()); err == nil {
		c.Close()
		t.Fatal("the seat connected to a gate whose key it does not pin")
	}
	if n := svc.lists.Load(); n != 1 {
		t.Fatalf("the service served %d lists after the refused seats, want 1", n)
	}

	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if n := svc.closes.Load(); n != 1 {
		t.Fatalf("the service was closed %d times", n)
	}
	if svc.openAtClose.Load() {
		t.Fatal("the gate still took connections when the service was closed")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("the gate's port takes connections after the daemon stopped")
	}
}

// Without --seat-gate the daemon opens no gate, even with a key and a pin in
// place, and says nothing of one.
func TestSeatGateWithoutTheFlagListensNowhere(t *testing.T) {
	gateSetup(t, "")
	var listens atomic.Int64
	old := seatGateListen
	seatGateListen = func(network, address string) (net.Listener, error) {
		listens.Add(1)
		return net.Listen(network, address)
	}
	t.Cleanup(func() { seatGateListen = old })
	state, runtime := ownerCmdDirs(t)
	gateInit(t, state)
	writePins(t, state, newSeat(t, "seat").Fingerprint()+"\n", 0o600)

	ta := newTestApp(t, nil)
	errs := &gateBuf{}
	ta.stderr = errs
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel"}, runtime+"/daemon.sock")
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if n := listens.Load(); n != 0 {
		t.Fatalf("the gate listened %d times", n)
	}
	if strings.Contains(errs.String(), "seat gate") {
		t.Fatalf("the log speaks of a gate:\n%s", errs.String())
	}
}

// --seat-gate needs --owner-channel: alone it is a usage error, and nothing
// is made.
func TestSeatGateNeedsTheOwnerChannel(t *testing.T) {
	gateSetup(t, "")
	state, runtime := ownerCmdDirs(t)
	ta := newTestApp(t, nil)
	code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--seat-gate", "127.0.0.1:7443"})
	if code != leaddaemon.ExitUsage || !strings.Contains(ta.err.String(), "--seat-gate needs --owner-channel") {
		t.Fatalf("exit %d: %q", code, ta.err.String())
	}
	if _, err := os.Lstat(state); err == nil {
		t.Fatal("a state folder was made")
	}
}

// An address that is not one IP address of a host and a port is refused with
// the configuration's code before the daemon makes anything.
func TestSeatGateRefusesAddressesThatAreNotOneHostAndPort(t *testing.T) {
	gateSetup(t, "")
	for _, addr := range []string{
		"", "0.0.0.0:7443", "[::]:7443", "[::ffff:0.0.0.0]:7443", ":7443",
		"localhost:7443", "gate.example.invalid:7443", "127.0.0.1", "127.0.0.1:0",
		"224.0.0.1:7443", "[ff02::1]:7443", "255.255.255.255:7443", "[fe80::1%lo]:7443",
		"127.0.0.1:7443 ", "127.0.0.1:65536",
	} {
		state, runtime := ownerCmdDirs(t)
		ta := newTestApp(t, nil)
		code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel", "--seat-gate", addr})
		if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "ataniqd run: --seat-gate: the seat gate's address must") {
			t.Errorf("%q: exit %d: %q", addr, code, ta.err.String())
		}
		if _, err := os.Lstat(state); err == nil {
			t.Errorf("%q: a state folder was made", addr)
		}
		if _, err := os.Lstat(runtime + "/daemon.sock"); err == nil {
			t.Errorf("%q: a socket was made", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:7443", "[::1]:7443", "192.0.2.10:7443", "[2001:db8::10]:7443"} {
		if _, err := leaddaemon.ParseSeatGateAddr(addr); err != nil {
			t.Errorf("%q is refused: %v", addr, err)
		}
	}
	if ap, err := leaddaemon.ParseSeatGateAddr("[::ffff:192.0.2.10]:7443"); err != nil || ap.String() != "192.0.2.10:7443" {
		t.Errorf("a mapped address: %v %v", ap, err)
	}
}

// A gate whose key or pin file is missing or not safe is refused with the
// configuration's code before the daemon takes a lock or makes its socket.
func TestSeatGateRefusesUnsafeOrMissingFiles(t *testing.T) {
	gateSetup(t, "")
	pin := newSeat(t, "seat").Fingerprint()
	pins := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(newSeat(t, "seat").Fingerprint() + "\n")
		}
		return b.String()
	}
	for name, c := range map[string]struct {
		prepare func(t *testing.T, state string)
		want    string
	}{
		"no key": {func(*testing.T, string) {}, "make one with ataniqd seat-gate init"},
		"a key folder others may write": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, pin+"\n", 0o600)
			if err := os.Chmod(seatGateDir(state), 0o777); err != nil {
				t.Fatal(err)
			}
		}, "make one with ataniqd seat-gate init"},
		"no pin file": {func(t *testing.T, state string) { gateInit(t, state) }, "there is no seat pin file"},
		"an empty pin file": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, "# none yet\n", 0o600)
		}, "pins no seat key"},
		"a pin file others may read": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, pin+"\n", 0o644)
		}, "must be mode 0600"},
		"a pin file that is a link": {func(t *testing.T, state string) {
			gateInit(t, state)
			real := writePins(t, state, pin+"\n", 0o600)
			if err := os.Rename(real, real+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real+".real", real); err != nil {
				t.Fatal(err)
			}
		}, "is a symbolic link"},
		"a pin file with a second hard link": {func(t *testing.T, state string) {
			gateInit(t, state)
			real := writePins(t, state, pin+"\n", 0o600)
			if err := os.Link(real, real+".other"); err != nil {
				t.Fatal(err)
			}
		}, "has another link"},
		"a pin file that is a folder": {func(t *testing.T, state string) {
			gateInit(t, state)
			if err := os.Mkdir(filepath.Join(seatGateDir(state), leaddaemon.SeatPinsFileName), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "is not a regular file"},
		"five pins": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, pins(leaddaemon.MaxSeatPins+1), 0o600)
		}, "holds more than 4 pins"},
		"a line that is not a fingerprint": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, pin+"\n"+strings.ToUpper(pin)+"\n", 0o600)
		}, "line 2 of the seat pin file is not a key fingerprint"},
		"a pin twice": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, pin+"\n"+pin+"\n", 0o600)
		}, "line 2 of the seat pin file repeats a pin"},
		"a pin file too large": {func(t *testing.T, state string) {
			gateInit(t, state)
			writePins(t, state, pin+"\n"+strings.Repeat("#", 5000)+"\n", 0o600)
		}, "holds more than 4096 bytes"},
	} {
		state, runtime := ownerCmdDirs(t)
		c.prepare(t, state)
		ta := newTestApp(t, nil)
		code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel", "--seat-gate", "127.0.0.1:7443"})
		if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "ataniqd run: --seat-gate: ") || !strings.Contains(ta.err.String(), c.want) {
			t.Errorf("%s: exit %d: %q, want %q", name, code, ta.err.String(), c.want)
		}
		if _, err := os.Lstat(runtime + "/daemon.sock"); err == nil {
			t.Errorf("%s: a socket was made", name)
		}
		if strings.Contains(ta.err.String(), pin) {
			t.Errorf("%s: a pin is in the message: %q", name, ta.err.String())
		}
	}
}

// A gate that cannot listen (the port is taken) stops the daemon at its serve
// step, and the service it was built on is closed.
func TestSeatGateThatCannotListenStopsTheDaemon(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	svc := gateSetup(t, "")
	state, runtime := ownerCmdDirs(t)
	gateInit(t, state)
	writePins(t, state, newSeat(t, "seat").Fingerprint()+"\n", 0o600)
	ta := newTestApp(t, nil)
	code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel", "--seat-gate", taken.Addr().String()})
	if code != leaddaemon.ExitServe || !strings.Contains(ta.err.String(), "seat gate: cannot listen on "+taken.Addr().String()) {
		t.Fatalf("exit %d: %q", code, ta.err.String())
	}
	if n := svc.closes.Load(); n != 1 {
		t.Fatalf("the service was closed %d times", n)
	}
}

// seat-gate init makes the key in owner/gate/ (folder 0700, key 0600) and
// prints its fingerprint alone; a second init refuses and leaves the key.
// seat-gate pins prints the pins as the daemon reads them, and refuses a file
// the daemon would refuse.
func TestSeatGateCommands(t *testing.T) {
	state := filepath.Join(shortDir(t), "s")
	fp := gateInit(t, state)
	dir := seatGateDir(state)
	id, err := seatgate.LoadIdentity(dir)
	if err != nil || id.Fingerprint() != fp {
		t.Fatalf("the key made: %v, %v", id.Fingerprint(), err)
	}
	if st, err := os.Lstat(dir); err != nil || st.Mode().Perm() != 0o700 || !st.IsDir() {
		t.Fatalf("the gate's folder: %v %v", st.Mode(), err)
	}
	if st, err := os.Lstat(filepath.Join(dir, seatgate.KeyFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v %v", st.Mode(), err)
	}
	key, err := os.ReadFile(filepath.Join(dir, seatgate.KeyFile))
	if err != nil {
		t.Fatal(err)
	}

	ta := newTestApp(t, nil)
	if code := ta.run([]string{"seat-gate", "init", "--state-dir", state}); code != leaddaemon.ExitFailure ||
		!strings.Contains(ta.err.String(), "already holds a key, which is never overwritten") || ta.out.Len() != 0 {
		t.Fatalf("a second init: exit %d: %q %q", code, ta.out.String(), ta.err.String())
	}
	if again, _ := os.ReadFile(filepath.Join(dir, seatgate.KeyFile)); !bytes.Equal(again, key) {
		t.Fatal("a second init changed the key")
	}

	a, b := newSeat(t, "a").Fingerprint(), newSeat(t, "b").Fingerprint()
	path := writePins(t, state, "# old seat\n"+a+"\n\n  "+b+"  \n", 0o600)
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"seat-gate", "pins", "--state-dir", state}); code != 0 || ta.out.String() != a+"\n"+b+"\n" {
		t.Fatalf("pins: exit %d: %q %q", code, ta.out.String(), ta.err.String())
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"seat-gate", "pins", "--state-dir", state}); code != leaddaemon.ExitFailure ||
		!strings.Contains(ta.err.String(), "must be mode 0600") || ta.out.Len() != 0 {
		t.Fatalf("pins of an open file: exit %d: %q %q", code, ta.out.String(), ta.err.String())
	}

	for _, args := range [][]string{
		{"seat-gate"}, {"seat-gate", "show"}, {"seat-gate", "init", "extra"},
		{"seat-gate", "pins", "--no-such"}, {"seat-gate", "init", "--state-dir"},
	} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"seat-gate", "init", "--state-dir", "relative/s"}); code != leaddaemon.ExitConfig {
		t.Errorf("a relative state folder: exit %d", code)
	}
}

// The gate's frames hold the largest result the service answers, with room
// for the response around it, so that no read of a card is refused too-large.
func TestSeatGateFramesHoldTheLargestPage(t *testing.T) {
	if seatgate.MaxFrame < ownerwire.MaxPageBytes+1<<10 {
		t.Fatalf("seatgate.MaxFrame is %d, under ownerwire.MaxPageBytes (%d) and 1 KiB", seatgate.MaxFrame, ownerwire.MaxPageBytes)
	}
}

// The usage of the tagged build names the gate's flag and command.
func TestSeatGateUsage(t *testing.T) {
	for _, want := range []string{"ataniqd run --owner-channel --seat-gate ADDR", "ataniqd seat-gate init", "ataniqd seat-gate pins", "owner/gate/seat-pins"} {
		if !strings.Contains(ownerUsage, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
}
