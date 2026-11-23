//go:build linux && ataniq_ownerchannel

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/seatlink"
)

// trialInit runs "ataniqd trial init" on a state folder and returns its exit
// code and its output.
func trialInit(t *testing.T, env map[string]string, state string) (int, string, string) {
	t.Helper()
	ta := newTestApp(t, env)
	code := ta.run([]string{"trial", "init", "--state-dir", state})
	return code, ta.out.String(), ta.err.String()
}

// "ataniqd trial init" writes the trial's settings, private, once: they read
// back as a trial's with the stand-in door and the three kinds of passkey, a
// second init is refused and changes nothing, and a state folder that is, lies
// inside or contains a default folder of the account is refused before
// anything is written. --state-dir is required.
func TestTrialInitWritesTheSettingsOnceInAScratchFolder(t *testing.T) {
	state, _ := ownerCmdDirs(t)
	code, out, errs := trialInit(t, nil, state)
	if code != 0 || !strings.Contains(out, "STAND-IN door, project trial-project") || !strings.Contains(out, "next: ataniqd seat-gate init --state-dir "+state) {
		t.Fatalf("trial init: exit %d, %q, %q", code, out, errs)
	}
	path := filepath.Join(state, "owner", hostadapter.ScratchName)
	st, err := os.Lstat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the settings file: %v, %v", st, err)
	}
	sc, err := hostadapter.LoadScratch(path)
	if err != nil || !sc.Trial || sc.Door != hostadapter.DoorStandIn || sc.Mirror != "" || sc.Drop != "" ||
		!slices.Equal(sc.Kinds, []string{"platform-passkey", "vault-passkey", "passkey"}) {
		t.Fatalf("the settings read back: %+v, %v", sc, err)
	}
	before, _ := os.ReadFile(path)
	if code, _, errs := trialInit(t, nil, state); code != leaddaemon.ExitFailure || !strings.Contains(errs, "never overwritten") {
		t.Errorf("a second init: exit %d, %q", code, errs)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("a second init changed the settings")
	}

	home := fakeAccount(t)
	def := filepath.Join(home, ".local", "state", "ataniq")
	for _, dir := range []string{def, filepath.Join(def, "trial"), filepath.Join(home, ".local", "state")} {
		code, _, errs := trialInit(t, map[string]string{"HOME": home}, dir)
		if code != leaddaemon.ExitConfig || !strings.Contains(errs, "default folder") {
			t.Errorf("init in %s: exit %d, %q", dir, code, errs)
		}
		if _, err := os.Lstat(filepath.Join(dir, "owner", hostadapter.ScratchName)); err == nil {
			t.Errorf("init in %s wrote settings", dir)
		}
	}
	for _, args := range [][]string{{"trial", "init"}, {"trial"}, {"trial", "start"}, {"trial", "init", "--state-dir", "rel"}} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage && code != leaddaemon.ExitConfig {
			t.Errorf("%q: exit %d", args, code)
		}
	}
}

// --trial needs --owner-channel; the trial's settings are refused without
// --trial, and other settings with it, each before the daemon takes a lock or
// makes a socket.
func TestTrialRunIsRefusedWithoutItsFlagsAndSettings(t *testing.T) {
	trialState, runtime := ownerCmdDirs(t)
	if code, _, errs := trialInit(t, nil, trialState); code != 0 {
		t.Fatal(errs)
	}
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"run", "--state-dir", trialState, "--runtime-dir", runtime, "--trial"}); code != leaddaemon.ExitUsage ||
		!strings.Contains(ta.err.String(), "--trial needs --owner-channel") {
		t.Errorf("--trial alone: exit %d, %q", code, ta.err.String())
	}
	useSuite(t, cmdSuite{})
	ta = newTestApp(t, nil)
	if code := runBounded(t, ta, []string{"run", "--state-dir", trialState, "--runtime-dir", runtime, "--owner-channel"}); code != leaddaemon.ExitConfig ||
		!strings.Contains(ta.err.String(), `a phone trial's ("trial": true)`) {
		t.Errorf("the trial's settings without --trial: exit %d, %q", code, ta.err.String())
	}
	realState, _ := scratchSettings(t, nil)
	ta = newTestApp(t, nil)
	if code := runBounded(t, ta, []string{"run", "--state-dir", realState, "--runtime-dir", runtime, "--owner-channel", "--trial"}); code != leaddaemon.ExitConfig ||
		!strings.Contains(ta.err.String(), "these are not a trial's") {
		t.Errorf("other settings with --trial: exit %d, %q", code, ta.err.String())
	}
	if _, err := os.Lstat(filepath.Join(runtime, "daemon.sock")); err == nil {
		t.Error("a socket was made for a refused configuration")
	}
	// The builder refuses the trial's settings too.
	if svc, closer, err := buildOwnerService(trialState); err == nil || svc != nil || closer != nil {
		t.Errorf("the scratch builder took the trial's settings")
	}
}

// The trial, run: the daemon serves the trial's settings with the gate and a
// seat origin, prints the STAND-IN banner and the TRIAL line, and lists
// trial.submit; "ataniqd trial submit" makes sample cards on its socket and
// prints one line for each card and each refusal; the cards are on the owner
// list of the socket and on the seat's side of the gate (a pinned seat lists
// and shows them over it; the gate's start line names login and decide, which
// the decision tests of package hostadapter run through the trial's service,
// not this one). A daemon that serves other settings, without --trial, serves
// no trial.submit.
func TestTrialRunSubmitsSampleCardsThatTheSeatSees(t *testing.T) {
	addr := freeLoopback(t)
	state, runtime := ownerCmdDirs(t)
	if code, _, errs := trialInit(t, nil, state); code != 0 {
		t.Fatal(errs)
	}
	gateFP := gateInit(t, state)
	seat := newSeat(t, "seat")
	writePins(t, state, seat.Fingerprint()+"\n", 0o600)
	env, _ := writeOrigin(t, "https://seat.example\n", 0o600)
	socket := runtime + "/daemon.sock"

	ta := newTestApp(t, env)
	errs := &gateBuf{}
	ta.stderr = errs
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel", "--trial", "--seat-gate", addr}, socket)
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		ta.signals <- syscall.SIGTERM
		if code := <-done; code != 0 {
			t.Fatalf("exit %d: %s", code, errs.String())
		}
	}
	t.Cleanup(stop)
	log := errs.String()
	if strings.Count(log, "ataniqd: "+ownerBanner+"\nataniqd: "+trialNote+"\n") != 1 {
		t.Errorf("the STAND-IN banner and the TRIAL line are not printed once, in order:\n%s", log)
	}
	if !strings.Contains(log, "seat origin https://seat.example; it serves pair, read, login, decide and watch") {
		t.Errorf("the gate's start line:\n%s", log)
	}
	h := ownerCmdHello(t, socket)
	if !slices.Contains(h.Operations, trialOpSubmit) || !slices.Contains(h.Operations, ownerwire.OpList) {
		t.Fatalf("operations %v", h.Operations)
	}

	submit := func(args ...string) (int, string, string) {
		tb := newTestApp(t, nil)
		code := tb.run(append([]string{"trial", "submit", "--socket", socket}, args...))
		return code, tb.out.String(), tb.err.String()
	}
	code, out, serr := submit("--kind", "finish", "--count", "2")
	if code != 0 || strings.Count(out, "card ap-") != 2 || !strings.Contains(out, "finish trial-login-retry-1") || !strings.Contains(out, "finish trial-cache-trim-2") {
		t.Fatalf("submit 2 finish: exit %d, %q, %q", code, out, serr)
	}
	code, out, serr = submit("--kind", "branch-prune", "--count", "2")
	if code != leaddaemon.ExitFailure || strings.Count(out, "card ap-") != 1 || !strings.Contains(out, "no card: branch-prune") || !strings.Contains(out, "request-duplicate") {
		t.Fatalf("submit 2 branch-prune: exit %d, %q, %q", code, out, serr)
	}
	for _, args := range [][]string{{"--kind", "merge"}, {"--kind", "prune"}, {"--kind", "Branch-Prune"}, {"--kind", "finish", "--count", "11"}, {"--kind", "finish", "--count", "0"}, {"--count", "1"}} {
		if code, _, _ := submit(args...); code != leaddaemon.ExitUsage {
			t.Errorf("submit %q: exit %d", args, code)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	var sock ownerwire.Listing
	if err := c.Call(ctx, ownerwire.OpList, nil, &sock); err != nil || len(sock.Pending) != 3 {
		t.Fatalf("owner.list on the socket: %+v, %v", sock, err)
	}
	for _, bad := range []any{map[string]any{"kind": "finish", "count": 1, "x": 1}, map[string]any{"kind": "finish", "count": 99}, nil} {
		var res json.RawMessage
		var ce *clientapi.Error
		if err := c.Call(ctx, trialOpSubmit, bad, &res); !errors.As(err, &ce) || ce.Code != clientapi.CodeInvalidRequest {
			t.Errorf("trial.submit %v: %v", bad, err)
		}
	}
	c.Close()

	g, err := dialGate(addr, seat, gateFP)
	if err != nil {
		t.Fatalf("the pinned seat: %v", err)
	}
	defer g.Close()
	for _, want := range []string{seatlink.CapRead} {
		if !slices.Contains(g.Caps(), want) {
			t.Errorf("the gate lists %q", g.Caps())
		}
	}
	var gl ownerwire.Listing
	if err := g.Call(ctx, ownerwire.OpList, nil, &gl); err != nil || len(gl.Pending) != 3 {
		t.Fatalf("owner.list over the gate: %+v, %v", gl, err)
	}
	for i, it := range gl.Pending {
		if it.ID != sock.Pending[i].ID || it.Project != "trial-project" {
			t.Errorf("the seat sees %+v, the socket %+v", it, sock.Pending[i])
		}
	}
	var sh ownerwire.ShowResult
	if err := g.Call(ctx, ownerwire.OpShow, ownerwire.ShowParams{ID: gl.Pending[0].ID}, &sh); err != nil ||
		!strings.Contains(string(sh.Record.Bytes), "TRIAL sample card 1") {
		t.Errorf("owner.show over the gate: %v, %q", err, sh.Record.Bytes)
	}
	g.Close()
	stop()

	// A daemon on other settings, started without --trial, serves no
	// trial.submit (the trial's own settings without --trial are refused,
	// which TestTrialRunIsRefusedWithoutItsFlagsAndSettings shows).
	useSuite(t, cmdSuite{})
	realState, _ := scratchSettings(t, nil)
	tr := newTestApp(t, nil)
	tr.stderr = &gateBuf{}
	runtime2 := filepath.Dir(realState) + "/r"
	if err := os.Mkdir(runtime2, 0o700); err != nil {
		t.Fatal(err)
	}
	done2 := startApp(t, tr, []string{"--state-dir", realState, "--runtime-dir", runtime2, "--owner-channel"}, runtime2+"/daemon.sock")
	tb := newTestApp(t, nil)
	if code := tb.run([]string{"trial", "submit", "--socket", runtime2 + "/daemon.sock", "--kind", "finish"}); code != leaddaemon.ExitProtocol ||
		!strings.Contains(tb.err.String(), "does not serve the trial") {
		t.Errorf("trial submit to a daemon that is not a trial's: exit %d, %q", code, tb.err.String())
	}
	tr.signals <- syscall.SIGTERM
	if code := <-done2; code != 0 {
		t.Fatalf("exit %d", code)
	}
	tb = newTestApp(t, nil)
	if code := tb.run([]string{"trial", "submit", "--socket", runtime2 + "/daemon.sock", "--kind", "finish"}); code != leaddaemon.ExitNoDaemon {
		t.Errorf("trial submit with no daemon: exit %d, %q", code, tb.err.String())
	}
}

// stubHandler is the daemon's base handler of a test server that only serves
// the extension.
type stubHandler struct{}

func (stubHandler) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: "9.9.9-test"}
}

func (stubHandler) Status(context.Context) (clientapi.StatusResult, error) {
	return clientapi.StatusResult{}, nil
}

// trialServer serves trial.submit with the sample cards of submit on a private
// socket, and returns the socket's path.
func trialServer(t *testing.T, submit func(context.Context, string) (hostadapter.TrialCard, error)) string {
	t.Helper()
	l, path, err := clientapi.Listen(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- clientapi.Serve(ctx, l, stubHandler{}, clientapi.Options{Extra: []clientapi.Extension{&trialExt{submit: submit}}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})
	return path
}

// A trial.submit that stops after some of its cards, because the call ended or
// a sample failed, returns the cards already made with the reason, so that the
// owner knows which cards exist on the phone: the command prints their lines,
// then says it stopped after how many of how many, and exits 1. With no card
// made at all it is the plain failure.
func TestTrialSubmitReportsTheCardsMadeBeforeItStopped(t *testing.T) {
	var notices []string
	old := ownerNotice
	ownerNotice = func(line string) { notices = append(notices, line) }
	t.Cleanup(func() { ownerNotice = old })

	made := 0
	socket := trialServer(t, func(ctx context.Context, kind string) (hostadapter.TrialCard, error) {
		made++
		if made%3 == 0 {
			return hostadapter.TrialCard{}, context.Canceled
		}
		return hostadapter.TrialCard{Kind: kind, RequestID: "rq-stub", State: "pending", PendingID: "ap-stub-" + string(rune('0'+made)), Feature: "stub"}, nil
	})
	run := func(count string) (int, string, string) {
		tb := newTestApp(t, nil)
		code := tb.run([]string{"trial", "submit", "--socket", socket, "--kind", "finish", "--count", count})
		return code, tb.out.String(), tb.err.String()
	}
	// Cards 1 and 2 are made, the third stops: the lines of the two, then the
	// reason.
	code, out, serr := run("5")
	if code != leaddaemon.ExitFailure || strings.Count(out, "card ap-stub-") != 2 || !strings.Contains(out, "card ap-stub-1:") || !strings.Contains(out, "card ap-stub-2:") ||
		!strings.Contains(serr, "stopped after 2 of 5 cards") || strings.Contains(serr, "protocol error") {
		t.Fatalf("a call that stopped after two: exit %d, %q, %q", code, out, serr)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "after 2 of 5 cards") {
		t.Errorf("the daemon noted %q", notices)
	}
	// A call that makes all its cards is as before.
	made = 0
	if code, out, serr = run("2"); code != leaddaemon.ExitOK || strings.Count(out, "card ap-stub-") != 2 || serr != "" {
		t.Errorf("a whole call: exit %d, %q, %q", code, out, serr)
	}
	// The first card failing is the plain failure, with no cards to show.
	made = 2
	if code, out, serr = run("3"); code != leaddaemon.ExitFailure || out != "" || !strings.Contains(serr, msgTrialFailed) || strings.Contains(serr, "stopped after") {
		t.Errorf("a call whose first card failed: exit %d, %q, %q", code, out, serr)
	}
}
