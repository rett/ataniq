//go:build linux && ataniq_ownerchannel

package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/seatgate"
)

// decidingGateService is a gate's service that can take a decision from the
// phone, as the owner channel's service can: it records the gate's values
// each seat call is handed.
type decidingGateService struct {
	*gateService
	mu    sync.Mutex
	gates []ownerchannel.Gate
}

func (s *decidingGateService) saw(g ownerchannel.Gate) {
	s.mu.Lock()
	s.gates = append(s.gates, g)
	s.mu.Unlock()
}

func (s *decidingGateService) seen() []ownerchannel.Gate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ownerchannel.Gate(nil), s.gates...)
}

func (s *decidingGateService) SeatLoginChallenge(_ context.Context, _ ownerchannel.Source, g ownerchannel.Gate) (ownerwire.SeatLoginChallengeResult, error) {
	s.saw(g)
	return ownerwire.SeatLoginChallengeResult{ChallengeID: "0123456789abcdef", Challenge: bytes.Repeat([]byte{1}, 32), Nonce: bytes.Repeat([]byte{2}, 32), ExpiresAt: "2026-11-22T09:05:00Z", Allow: [][]byte{}}, nil
}

func (s *decidingGateService) SeatLogin(_ context.Context, _ ownerchannel.Source, g ownerchannel.Gate, _ ownerwire.SeatLoginParams) (ownerwire.SeatLoginResult, error) {
	s.saw(g)
	return ownerwire.SeatLoginResult{}, ownerchannel.ErrChallengeStale
}

func (s *decidingGateService) SeatChallenge(_ context.Context, _ ownerchannel.Source, g ownerchannel.Gate, _ ownerwire.SeatChallengeParams) (ownerwire.SeatChallengeResult, error) {
	s.saw(g)
	return ownerwire.SeatChallengeResult{}, ownerchannel.ErrChallengeStale
}

func (s *decidingGateService) SeatDecide(_ context.Context, _ ownerchannel.Source, g ownerchannel.Gate, _ ownerwire.SeatDecideParams) (ownerwire.DecideResult, error) {
	s.saw(g)
	return ownerwire.DecideResult{}, ownerchannel.ErrChallengeStale
}

var _ gatehost.SeatPort = (*decidingGateService)(nil)

// decidingSetup is gateSetup with a service that can decide from the phone.
func decidingSetup(t *testing.T) *decidingGateService {
	t.Helper()
	svc := &decidingGateService{gateService: gateSetup(t, "")}
	ownerServiceBuilder = func(string) (leaddaemon.OwnerService, func() error, error) {
		return svc, func() error { return nil }, nil
	}
	return svc
}

// writeOrigin writes the owner-local seat origin file under a configuration
// folder of the test's and returns the environment that names the folder.
func writeOrigin(t *testing.T, text string, mode os.FileMode) (map[string]string, string) {
	t.Helper()
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "ataniq", "owner-local")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "seat-origin")
	if text != "" {
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{"XDG_CONFIG_HOME": cfg}, path
}

// runGate starts the daemon with a gate and returns the start line, the gate's
// key and a connected seat; stop ends the daemon.
func runGate(t *testing.T, env map[string]string) (line, gateFP string, c *seatgate.Client, stop func()) {
	t.Helper()
	addr := freeLoopback(t)
	state, runtime := ownerCmdDirs(t)
	gateFP = gateInit(t, state)
	seat := newSeat(t, "seat")
	writePins(t, state, seat.Fingerprint()+"\n", 0o600)
	ta := newTestApp(t, env)
	errs := &gateBuf{}
	ta.stderr = errs
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel", "--seat-gate", addr}, runtime+"/daemon.sock")
	for _, l := range strings.Split(errs.String(), "\n") {
		if strings.HasPrefix(l, "ataniqd: seat gate: listening on "+addr+", key fingerprint "+gateFP) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no start line in:\n%s", errs.String())
	}
	c, err := dialGate(addr, seat, gateFP)
	if err != nil {
		t.Fatal(err)
	}
	return line, gateFP, c, func() {
		c.Close()
		ta.signals <- syscall.SIGTERM
		if code := <-done; code != 0 {
			t.Fatalf("exit %d: %s", code, errs.String())
		}
	}
}

// With a seat origin and a service that can decide, the gate serves login and
// decide as well, says so with the origin in its start line, and hands the
// service the gate's own key fingerprint, the origin and its host. The file
// is read once: a change while the daemon runs changes nothing.
func TestSeatGateWithAnOriginServesLoginAndDecide(t *testing.T) {
	svc := decidingSetup(t)
	env, path := writeOrigin(t, "https://seat.example:443\n", 0o600)
	line, gateFP, c, stop := runGate(t, env)
	defer stop()
	if want := ", seat origin https://seat.example; it serves pair, read, login, decide and genesis"; !strings.HasSuffix(line, want) {
		t.Fatalf("the start line %q", line)
	}
	if got := strings.Join(c.Caps(), " "); got != "pair read login decide genesis" {
		t.Fatalf("the gate's capabilities: %q", got)
	}
	if err := os.WriteFile(path, []byte("https://other.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := c.Call(ctx, ownerwire.OpSeatLoginChallenge, nil, &raw); err != nil {
		t.Fatal(err)
	}
	var re *seatgate.RemoteError
	if err := c.Call(ctx, ownerwire.OpSeatDecide, json.RawMessage(`{}`), nil); !errors.As(err, &re) || re.Code != gatehost.CodeBadParams {
		t.Fatalf("a decide without params: %v", err)
	}
	want := ownerchannel.Gate{Instance: gateFP, Origin: "https://seat.example", RPID: "seat.example"}
	if g := svc.seen(); len(g) != 1 || g[0] != want {
		t.Fatalf("the service was handed %+v", g)
	}
}

// watchingGateService is a gate's service that can also wait for a change of
// the pending cards: it answers the empty set at once.
type watchingGateService struct {
	*decidingGateService
	waits atomic.Int64
}

func (s *watchingGateService) WaitPending(_ context.Context, since string) (ownerwire.SeatWaitResult, error) {
	s.waits.Add(1)
	return ownerwire.SeatWaitResult{Version: ownerwire.PendingVersion(nil), Cards: []ownerwire.SeatWaitCard{}}, nil
}

var _ gatehost.WaitPort = (*watchingGateService)(nil)

// A service that can wait gives the gate the capability watch and the
// operation seat.wait, with or without a seat origin: it decides nothing. A
// service that cannot, as the others of these tests, has neither (their
// loops over the gate's operations expect unknown-op for seat.wait too).
func TestSeatGateServesWatchWhenTheServiceCanWait(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		caps   string
	}{
		{"with an origin", "https://seat.example\n", "pair read login decide watch genesis"},
		{"without an origin", "", "pair read watch genesis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &watchingGateService{decidingGateService: &decidingGateService{gateService: gateSetup(t, "")}}
			ownerServiceBuilder = func(string) (leaddaemon.OwnerService, func() error, error) {
				return svc, func() error { return nil }, nil
			}
			env, _ := writeOrigin(t, tc.origin, 0o600)
			_, _, c, stop := runGate(t, env)
			defer stop()
			if got := strings.Join(c.Caps(), " "); got != tc.caps {
				t.Fatalf("the gate's capabilities: %q, want %q", got, tc.caps)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var res ownerwire.SeatWaitResult
			if err := c.Call(ctx, ownerwire.OpSeatWait, json.RawMessage(`{"since":""}`), &res); err != nil || res.Version != ownerwire.PendingVersion(nil) {
				t.Fatalf("seat.wait: %+v %v", res, err)
			}
			var re *seatgate.RemoteError
			if err := c.Call(ctx, ownerwire.OpSeatWait, json.RawMessage(`{"since":"x"}`), nil); !errors.As(err, &re) || re.Code != gatehost.CodeBadParams {
				t.Fatalf("seat.wait with a bad version: %v", err)
			}
			if n := svc.waits.Load(); n != 1 {
				t.Fatalf("the service waited %d times", n)
			}
		})
	}
}

// Without a usable origin, or with a service that cannot decide, the gate
// serves pair, read and genesis (it decides nothing), says why in its start
// line, and refuses the seat's login and decision unknown-op; the daemon
// starts as before.
func TestSeatGateWithoutAnOriginServesPairAndReadOnly(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		mode     os.FileMode
		deciding bool
		why      string
	}{
		{"no file", "", 0o600, true, "no seat origin is configured (the owner-local seat-origin file is missing or empty)"},
		{"an empty file", "\n", 0o600, true, "no seat origin is configured (the owner-local seat-origin file is missing or empty)"},
		{"a file others may write", "https://seat.example\n", 0o666, true, "the owner-local seat-origin file is refused (ownerlocal: the file can be written by its group or by others)"},
		{"http", "http://seat.example\n", 0o600, true, "holds no canonical origin"},
		{"another port", "https://seat.example:8443\n", 0o600, true, "holds no canonical origin"},
		{"a path", "https://seat.example/\n", 0o600, true, "holds no canonical origin"},
		{"upper case", "https://Seat.example\n", 0o600, true, "holds no canonical origin"},
		{"an address", "https://192.0.2.10\n", 0o600, true, "holds no canonical origin"},
		{"a service that cannot decide", "https://seat.example\n", 0o600, false, "this service cannot take a decision from the phone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var svc *decidingGateService
			if tc.deciding {
				svc = decidingSetup(t)
			} else {
				gateSetup(t, "")
			}
			env, _ := writeOrigin(t, tc.text, tc.mode)
			line, _, c, stop := runGate(t, env)
			defer stop()
			if !strings.Contains(line, "; it serves pair, read and genesis only: ") || !strings.Contains(line, tc.why) {
				t.Fatalf("the start line %q, want the reason %q", line, tc.why)
			}
			if strings.Contains(line, "Seat.example") || strings.Contains(line, "192.0.2.10") {
				t.Fatalf("the start line holds the file's content: %q", line)
			}
			if got := strings.Join(c.Caps(), " "); got != "pair read genesis" {
				t.Fatalf("the gate's capabilities: %q", got)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var re *seatgate.RemoteError
			for _, op := range ownerwire.GateOperations() {
				if op == ownerwire.OpSeatGenesis {
					// Served: it decides nothing.
					var res ownerwire.SeatGenesisResult
					if err := c.Call(ctx, op, json.RawMessage(`{"verb":"start"}`), &res); err != nil || res.Round != 1 {
						t.Fatalf("%s: %+v %v", op, res, err)
					}
					continue
				}
				if err := c.Call(ctx, op, nil, nil); !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
					t.Fatalf("%s: %v", op, err)
				}
			}
			if svc != nil && len(svc.seen()) != 0 {
				t.Fatal("the service's seat operations were called")
			}
		})
	}
}

func TestCanonicalSeatOrigin(t *testing.T) {
	good := map[string]string{
		"https://seat.example":          "seat.example",
		"https://seat.example:443":      "seat.example",
		"https://a-b.c0.example":        "a-b.c0.example",
		"https://localhost":             "localhost",
		"https://xn--bcher-kva.example": "xn--bcher-kva.example",
	}
	for in, rp := range good {
		o, r, err := leaddaemon.CanonicalSeatOrigin(in)
		if err != nil || o != "https://"+rp || r != rp {
			t.Errorf("%q: %q %q %v", in, o, r, err)
		}
	}
	for _, in := range []string{
		"", "https://", "http://seat.example", "HTTPS://seat.example", "https://Seat.example", "https://seat.example:8443",
		"https://seat.example:", "https://seat.example/", "https://seat.example/x", "https://seat.example?x", "https://seat.example#x",
		"https://user@seat.example", "https://192.0.2.10", "https://[2001:db8::1]", "https://seat..example", "https://-seat.example",
		"https://seat-.example", "https://seat_x.example", "https://seat.example.", "https://.seat.example", " https://seat.example",
		"https://seat.example\n", "https://" + strings.Repeat("a", 64) + ".example", "https://" + strings.Repeat("abc.", 64) + "x",
		"https://seat.example:443:443", "https://0x7f.1",
	} {
		if o, r, err := leaddaemon.CanonicalSeatOrigin(in); err == nil || o != "" || r != "" {
			t.Errorf("%q was taken: %q %q", in, o, r)
		} else if strings.Contains(err.Error(), "example") || strings.Contains(err.Error(), "192.0") {
			t.Errorf("the error holds the input: %v", err)
		}
	}
}

// A gate whose origin is not canonical does not start: Start is given only
// what the command made canonical.
func TestSeatGateRefusesAnOriginThatIsNotCanonical(t *testing.T) {
	state, _ := ownerCmdDirs(t)
	gateInit(t, state)
	seat := newSeat(t, "seat")
	writePins(t, state, seat.Fingerprint()+"\n", 0o600)
	ta := newTestApp(t, nil)
	g, code := ta.seatGatePrepare(freeLoopback(t), state)
	if code >= 0 {
		t.Fatalf("prepare: %d", code)
	}
	g.Origin = "https://Seat.example"
	g.Listen = func(string, string) (net.Listener, error) {
		t.Fatal("a gate with a bad origin listened")
		return nil, nil
	}
	svc := &decidingGateService{gateService: &gateService{}}
	if stop, err := g.Start(svc); err == nil {
		stop()
		t.Fatal("the gate started")
	}
}
