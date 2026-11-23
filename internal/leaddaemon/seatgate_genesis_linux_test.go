//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/thread"
	"ataniq/internal/seatgate"
)

// gateLog collects the gate's lines.
type gateLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *gateLog) logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *gateLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func newGateIdentity(t *testing.T, name string) seatgate.Identity {
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

// bareService is a service that can neither decide nor wait: the gate serves
// pair and read of it, and the thread store beside.
type bareService struct{ gatehost.Port }

// startGate starts a gate over a bare service on the loopback address, with
// the given thread store folder, and returns a dialed seat and the stop.
func startGate(t *testing.T, genesisDir string, lg *gateLog) (seat *seatgate.Client, stop func()) {
	t.Helper()
	gateID, seatID := newGateIdentity(t, "gate"), newGateIdentity(t, "seat")
	var ln net.Listener
	g := &SeatGate{
		Addr: netip.MustParseAddrPort("127.0.0.1:7443"), Identity: gateID, Pins: []string{seatID.Fingerprint()},
		GenesisDir: genesisDir, Log: lg.logf,
		Listen: func(network, address string) (net.Listener, error) {
			var err error
			ln, err = net.Listen("tcp", "127.0.0.1:0")
			return ln, err
		},
	}
	stop, err := g.Start(bareService{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := seatgate.Dial(ctx, ln.Addr().String(), seatgate.ClientConfig{TLS: seatgate.ClientTLS(seatID, gateID.Fingerprint(), "gate"), Caps: gatehost.Caps()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, stop
}

// A gate with a thread store folder serves the capability genesis, names it in
// its start line, makes the folder and keeps the threads; without one it serves
// none, and makes no folder.
func TestTheGateServesGenesisFromItsThreadStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "genesis")
	lg := &gateLog{}
	c, stop := startGate(t, dir, lg)
	if got := strings.Join(c.Caps(), " "); got != "pair read genesis" {
		t.Fatalf("capabilities %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res ownerwire.SeatGenesisResult
	if err := c.Call(ctx, ownerwire.OpSeatGenesis, json.RawMessage(`{"verb":"start"}`), &res); err != nil || res.State != ownerwire.GenesisStateQuestion {
		t.Fatalf("start: %+v %v", res, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v %v", fi, err)
	}
	var start string
	for _, l := range lg.all() {
		if strings.Contains(l, "seat gate: listening on") {
			start = l
		}
	}
	if !strings.HasSuffix(start, "; it serves pair, read and genesis only: no seat origin is configured") {
		t.Fatalf("the start line %q in %q", start, lg.all())
	}
	stop()
	stop()
	// The store was closed after the gate, and what it kept is there for the
	// next start.
	s, err := thread.Open(dir, thread.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Round(ctx, res.Thread); err != nil || got.Thread != res.Thread {
		t.Fatalf("after the stop: %+v %v", got, err)
	}

	// No folder, no genesis: the capability is not listed, the operation is
	// unknown-op, and nothing is made.
	lg2 := &gateLog{}
	c2, _ := startGate(t, "", lg2)
	if got := strings.Join(c2.Caps(), " "); got != "pair read" {
		t.Fatalf("capabilities without a store: %q", got)
	}
	err = c2.Call(ctx, ownerwire.OpSeatGenesis, json.RawMessage(`{"verb":"start"}`), nil)
	var re *seatgate.RemoteError
	if !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
		t.Fatalf("seat.genesis without a store: %v", err)
	}
	for _, l := range lg2.all() {
		if strings.Contains(l, "seat gate: listening on") && !strings.HasSuffix(l, "; it serves pair and read only: no seat origin is configured") {
			t.Fatalf("the start line %q", l)
		}
	}
}

// A thread store folder the store refuses ends the start before anything
// listens.
func TestTheGateDoesNotStartWithAThreadStoreItRefuses(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "genesis")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	gateID, seatID := newGateIdentity(t, "gate"), newGateIdentity(t, "seat")
	g := &SeatGate{
		Addr: netip.MustParseAddrPort("127.0.0.1:7443"), Identity: gateID, Pins: []string{seatID.Fingerprint()}, GenesisDir: link,
		Listen: func(string, string) (net.Listener, error) {
			t.Fatal("the gate listened")
			return nil, nil
		},
	}
	stop, err := g.Start(bareService{})
	if err == nil || stop != nil || !strings.Contains(err.Error(), "the guided interview's thread store cannot be opened") || strings.Contains(err.Error(), base) {
		t.Fatalf("Start: %v", err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("something was written through the link: %v", entries)
	}
}

func TestCapWords(t *testing.T) {
	for in, want := range map[string]string{
		"pair read":                            "pair and read",
		"pair read watch":                      "pair, read and watch",
		"pair read genesis":                    "pair, read and genesis",
		"pair read login decide":               "pair, read, login and decide",
		"pair read login decide watch genesis": "pair, read, login, decide, watch and genesis",
		"pair":                                 "pair",
		"":                                     "nothing",
	} {
		var caps []string
		if in != "" {
			caps = strings.Fields(in)
		}
		if got := capWords(caps); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
