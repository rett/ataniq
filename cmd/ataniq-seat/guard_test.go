package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/authorityhost/decisionguard"
	"ataniq/internal/seatapp"
)

// countingDaemon is a daemon port that counts the decision calls relayed to
// it and the challenges dropped, and answers every other call as the unwired
// port does.
type countingDaemon struct {
	unwiredDaemon
	mu         sync.Mutex
	challenges int
	decisions  int
	dropped    []string
}

func (d *countingDaemon) DecideChallenge(context.Context, seatapp.DecideChallengeRequest) (seatapp.DecideChallenge, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.challenges++
	return seatapp.DecideChallenge{ID: "0123456789abcdef"}, nil
}

func (d *countingDaemon) Decide(context.Context, seatapp.DecideRequest) (seatapp.DecideResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.decisions++
	return seatapp.DecideResult{State: "approved"}, nil
}

func (d *countingDaemon) DropChallenge(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropped = append(d.dropped, id)
}

func (d *countingDaemon) counts() (int, int, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.challenges, d.decisions, append([]string(nil), d.dropped...)
}

// stateDir is a state folder of the switch, without the guard file.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "authority-host")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// guarded puts a counting daemon behind the guard of dir and returns both,
// and the codes of the refusals logged.
func guarded(dir string) (guardedDaemon, *countingDaemon, *[]string) {
	inner := &countingDaemon{}
	var codes []string
	return newGuardedDaemon(inner, dir, func(code string) { codes = append(codes, code) }), inner, &codes
}

// relayed asks g for a challenge and a decision and reports whether each was
// relayed; a refusal must be ErrDecisionsInhibited.
func relayed(t *testing.T, g guardedDaemon) (challenge, decision bool) {
	t.Helper()
	_, err := g.DecideChallenge(context.Background(), seatapp.DecideChallengeRequest{})
	if err != nil && !errors.Is(err, seatapp.ErrDecisionsInhibited) {
		t.Errorf("challenge refused with %v", err)
	}
	challenge = err == nil
	_, err = g.Decide(context.Background(), seatapp.DecideRequest{ChallengeID: "0123456789abcdef"})
	if err != nil && !errors.Is(err, seatapp.ErrDecisionsInhibited) {
		t.Errorf("decision refused with %v", err)
	}
	return challenge, err == nil
}

// TestGuardRelaysOnlyWithoutTheInhibit: with the state folder there and no
// decisions.inhibit, a challenge and a decision are relayed; with the guard
// file there in any form, or the folder missing, a link or not open to the
// service, neither is, nothing reaches the daemon, the refused decision's
// challenge is dropped, and each refusal is logged with the guard's code.
func TestGuardRelaysOnlyWithoutTheInhibit(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		g, inner, codes := guarded(stateDir(t))
		if c, d := relayed(t, g); !c || !d {
			t.Errorf("refused without the inhibit: challenge %v, decision %v", c, d)
		}
		if c, d, dropped := inner.counts(); c != 1 || d != 1 || len(dropped) != 0 {
			t.Errorf("relayed %d challenges, %d decisions, dropped %v", c, d, dropped)
		}
		if len(*codes) != 0 {
			t.Errorf("refusals logged: %v", *codes)
		}
	})

	refusals := map[string]struct {
		make func(t *testing.T) string
		code string
	}{
		"present": {func(t *testing.T) string {
			dir := stateDir(t)
			if err := os.WriteFile(filepath.Join(dir, decisionguard.GuardFile), []byte("txn: 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		}, decisionguard.CodeInhibited},
		"present and empty": {func(t *testing.T) string {
			dir := stateDir(t)
			if err := os.WriteFile(filepath.Join(dir, decisionguard.GuardFile), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}, decisionguard.CodeInhibited},
		"present as a folder": {func(t *testing.T) string {
			dir := stateDir(t)
			if err := os.Mkdir(filepath.Join(dir, decisionguard.GuardFile), 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		}, decisionguard.CodeInhibited},
		"present as a dangling link": {func(t *testing.T) string {
			dir := stateDir(t)
			if err := os.Symlink("nowhere", filepath.Join(dir, decisionguard.GuardFile)); err != nil {
				t.Fatal(err)
			}
			return dir
		}, decisionguard.CodeInhibited},
		"state folder absent": {func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "authority-host")
		}, decisionguard.CodeStateDir},
		"state folder a link": {func(t *testing.T) string {
			dir := stateDir(t)
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, decisionguard.CodeStateDir},
		"unreadable": {func(t *testing.T) string {
			if os.Geteuid() == 0 {
				t.Skip("root reads a folder of any mode")
			}
			dir := stateDir(t)
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(dir, 0o755) })
			return dir
		}, decisionguard.CodeUnreadable},
	}
	for name, tc := range refusals {
		t.Run(name, func(t *testing.T) {
			g, inner, codes := guarded(tc.make(t))
			if c, d := relayed(t, g); c || d {
				t.Errorf("relayed: challenge %v, decision %v", c, d)
			}
			c, d, dropped := inner.counts()
			if c != 0 || d != 0 {
				t.Errorf("the daemon was asked %d challenges and %d decisions", c, d)
			}
			if len(dropped) != 1 || dropped[0] != "0123456789abcdef" {
				t.Errorf("dropped %v, want the refused decision's challenge", dropped)
			}
			if len(*codes) != 2 || (*codes)[0] != tc.code || (*codes)[1] != tc.code {
				t.Errorf("logged %v, want %s twice", *codes, tc.code)
			}
		})
	}
}

// TestGuardIsAskedEveryTime: the answer is never kept. The inhibit that
// comes after a challenge refuses its decision, and the one that goes lets
// the next through.
func TestGuardIsAskedEveryTime(t *testing.T) {
	dir := stateDir(t)
	guard := filepath.Join(dir, decisionguard.GuardFile)
	g, inner, _ := guarded(dir)
	if _, err := g.DecideChallenge(context.Background(), seatapp.DecideChallengeRequest{}); err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if err := os.WriteFile(guard, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Decide(context.Background(), seatapp.DecideRequest{ChallengeID: "0123456789abcdef"}); !errors.Is(err, seatapp.ErrDecisionsInhibited) {
		t.Errorf("a decision after the inhibit came: %v", err)
	}
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Decide(context.Background(), seatapp.DecideRequest{ChallengeID: "0123456789abcdef"}); err != nil {
		t.Errorf("a decision after the inhibit went: %v", err)
	}
	if c, d, _ := inner.counts(); c != 1 || d != 1 {
		t.Errorf("relayed %d challenges and %d decisions", c, d)
	}
}

// TestGuardLeavesTheReadsAlone: pairing, login and the reading of cards
// decide nothing, so they reach the daemon while decisions are inhibited.
func TestGuardLeavesTheReadsAlone(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, decisionguard.GuardFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	g, _, codes := guarded(dir)
	ctx := context.Background()
	// The unwired port answers each with ErrDaemonUnavailable: the call
	// reached it, and was not refused by the guard.
	for name, err := range map[string]error{
		"pair":            func() error { _, err := g.Pair(ctx, seatapp.PairRequest{}); return err }(),
		"login challenge": func() error { _, err := g.LoginChallenge(ctx); return err }(),
		"login":           func() error { _, err := g.Login(ctx, seatapp.LoginRequest{}); return err }(),
		"list":            func() error { _, err := g.List(ctx); return err }(),
		"show":            func() error { _, err := g.Show(ctx, "x"); return err }(),
		"pages":           func() error { _, err := g.Pages(ctx, "x", 1); return err }(),
	} {
		if !errors.Is(err, seatapp.ErrDaemonUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(*codes) != 0 {
		t.Errorf("refusals logged: %v", *codes)
	}
}

// TestGuardNotSatisfiedByAnotherError: any error of the guard refuses, one
// that is not the package's own too, with a fixed code.
func TestGuardNotSatisfiedByAnotherError(t *testing.T) {
	g, inner, codes := guarded(stateDir(t))
	g.check = func(string) error { return errors.New("something else") }
	if c, d := relayed(t, g); c || d {
		t.Errorf("relayed: challenge %v, decision %v", c, d)
	}
	if c, d, _ := inner.counts(); c != 0 || d != 0 {
		t.Errorf("the daemon was asked %d challenges and %d decisions", c, d)
	}
	if len(*codes) != 2 || (*codes)[0] != "decision-guard-error" {
		t.Errorf("logged %v", *codes)
	}
}

// TestRunPutsTheDaemonBehindTheGuard: the command hands the app its daemon
// port behind the guard of --decision-guard, says which folder it follows,
// and logs each refusal with its code and nothing else.
func TestRunPutsTheDaemonBehindTheGuard(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, decisionguard.GuardFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := make(chan seatapp.Daemon, 1)
	daemonHook = func(d seatapp.Daemon) { got <- d }
	t.Cleanup(func() { daemonHook = nil })
	_, stderr, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t), "--decision-guard", dir})
	d := <-got
	g, ok := d.(guardedDaemon)
	if !ok || g.dir != dir {
		t.Fatalf("the app's daemon is %T, not behind the guard of %s", d, dir)
	}
	if _, ok := g.Daemon.(unwiredDaemon); !ok {
		t.Errorf("behind the guard: %T", g.Daemon)
	}
	if _, err := d.DecideChallenge(context.Background(), seatapp.DecideChallengeRequest{}); !errors.Is(err, seatapp.ErrDecisionsInhibited) {
		t.Errorf("challenge with the inhibit there: %v", err)
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d", code)
	}
	out := stderr.String()
	for _, want := range []string{
		"ataniq-seat: decisions follow the guard in " + dir + "\n",
		"ataniq-seat: a decision was refused, decisions are paused on this host: " + decisionguard.CodeInhibited + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q: %q", want, out)
		}
	}
}

// TestPairingGoesOnWhileDecisionsAreInhibited: with the inhibit there, the
// phone still pairs over the gate.
func TestPairingGoesOnWhileDecisionsAreInhibited(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, decisionguard.GuardFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	key, fp := seatKey(t)
	g := startGate(t, fp, "")
	links := watchLink(t)
	addr, _, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", g.addr, "--gate-pin-file", pinFile(t, g.id.Fingerprint()+"\n", 0o600), "--identity", key,
		"--decision-guard", dir})
	waitConnected(t, <-links)
	status, m := pairPhone(t, addr.String())
	words, _ := json.Marshal(m["words"])
	want, _ := json.Marshal(gateWords)
	if status != 200 || !bytes.Equal(words, want) {
		t.Errorf("pair while inhibited: %d %v", status, m)
	}
	stop()
}

// TestParseDecisionGuard: the guard's folder is the authority host's state
// folder unless given, and must be absolute when given.
func TestParseDecisionGuard(t *testing.T) {
	base := []string{"--origin", seatOrigin, "--listen", "127.0.0.1:8080"}
	o, _, ok := parse(base, &bytes.Buffer{})
	if !ok || o.guardDir != "/var/lib/authority-host" {
		t.Errorf("default %q (%v)", o.guardDir, ok)
	}
	o, _, ok = parse(append(base, "--decision-guard", "/srv/state"), &bytes.Buffer{})
	if !ok || o.guardDir != "/srv/state" {
		t.Errorf("given %q (%v)", o.guardDir, ok)
	}
	for _, bad := range []string{"", "relative/state", "."} {
		var out bytes.Buffer
		if _, code, ok := parse(append(base, "--decision-guard", bad), &out); ok || code != 2 || !strings.Contains(out.String(), "--decision-guard must be an absolute folder") {
			t.Errorf("%q: accepted or wrong refusal (%d): %q", bad, code, out.String())
		}
	}
}
