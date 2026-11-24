//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
	"ataniq/internal/leadchat"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadwire"
)

// These tests are the in-process proof behind the owner's attended proof of
// real replies (docs/scratch-seat-proof.md): the daemon of the build with both
// tags, run as
//
//	ataniqd run --scratch CONFIG --owner-channel --seat-gate IP:PORT
//
// on a scratch config whose state root is the phone trial's state folder, so
// that the gate's key, its seat pins and the paired phones' keys
// (owner/keys.log) are the trial's and the phone stays paired. The trial's
// state folder is made as the owner made his: "ataniqd trial init",
// "ataniqd seat-gate init", one pin, one paired key, and the trial daemon run
// with one sample card. Nothing here starts a unit, a sandbox or an agent:
// the unit manager is the fake one, and the leads are defined, not started.

// proofGoTool finds the Go executable as scripts/go-tool.sh does: the one on
// PATH when it is the pinned version, else the one the bootstrap recorded.
func proofGoTool(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	pin, err := os.ReadFile(filepath.Join(root, ".go-version"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(pin))
	exact := func(tool string) bool {
		cmd := exec.Command(tool, "version")
		cmd.Env = proofGoEnv()
		out, err := cmd.Output()
		return err == nil && strings.HasPrefix(string(out), "go version go"+version+" ")
	}
	if tool, err := exec.LookPath("go"); err == nil && exact(tool) {
		return tool
	}
	if record, err := os.ReadFile(filepath.Join(root, "build", "go-toolchain")); err == nil {
		lines := strings.Split(strings.TrimRight(string(record), "\n"), "\n")
		if len(lines) == 2 && lines[0] == version && filepath.IsAbs(lines[1]) && strings.HasSuffix(lines[1], "/bin/go") && exact(lines[1]) {
			return lines[1]
		}
	}
	t.Fatalf("no Go %s: run make bootstrap", version)
	return ""
}

// proofGoEnv is the environment of the helper script: offline, and no flag of
// the caller's.
func proofGoEnv() []string {
	return append(os.Environ(), "GOENV=off", "GOWORK=off", "GOFLAGS=", "GO111MODULE=on", "GOPRIVATE=", "GONOPROXY=none",
		"GONOSUMDB=none", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0")
}

// buildScratchInit builds ataniq-canary with the canary tag, offline, into a
// temporary folder, so that the owner's own command "ataniq-canary
// scratch-init" is what is run on the trial's state folder.
func buildScratchInit(t *testing.T) string {
	t.Helper()
	out := filepath.Join(shortDir(t), "ataniq-canary")
	cmd := exec.Command(proofGoTool(t), "build", "-tags", "ataniq_canary", "-o", out, "./cmd/ataniq-canary")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = proofGoEnv()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building ataniq-canary: %v\n%s", err, b)
	}
	return out
}

// proofEnv is the environment of a command the test runs as the owner would,
// from the world's getenv: the account's default folders lie in the world.
func proofEnv(getenv func(string) string) []string {
	env := []string{"PATH=/usr/bin:/bin"}
	for _, k := range []string{"HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		env = append(env, k+"="+getenv(k))
	}
	return env
}

// fileBytes reads a file or fails.
func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ownerKeys calls owner.keys on the daemon's socket.
func ownerKeys(t *testing.T, socket string) ownerwire.KeysView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var keys ownerwire.KeysView
	if err := c.Call(ctx, ownerwire.OpKeys, nil, &keys); err != nil {
		t.Fatalf("owner.keys: %v", err)
	}
	return keys
}

// The scratch daemon on the phone trial's state folder, step by step as the
// owner takes it (docs/scratch-seat-proof.md):
//
//  1. While the trial's settings ("trial": true) are in owner/scratch.json,
//     the scratch daemon refuses to start, at the config step (exit 10),
//     before it looks at the root marker or takes a lock: it makes no
//     runtime root and writes nothing in owner/. With only owner/scratch.json
//     moved aside (to scratch.json.trial) and the non-trial settings in its
//     place, it still refuses at the config step, now for the missing root
//     marker, while the trial runs.
//  2. "ataniq-canary scratch-init" takes the trial's private state folder as
//     it is ("kept folder"), writes the marker, and keeps it when run again;
//     it writes nothing in owner/. With the marker, the state folder's
//     daemon lock refuses the scratch daemon while the trial runs (exit 12).
//  3. Once the trial has stopped, the daemon starts: the gate has the
//     trial's key fingerprint, the trial's pinned seat reaches it, the paired
//     key is listed, the trial's journal opens, and a defined lead's reply is
//     read by the seat once the desk turns its chat on. The sandbox profile
//     of the lead takes owner/ of the state folder as a never-mounted path.
//  4. Rolled back (the non-trial settings moved aside to scratch.json.leads,
//     the trial's moved back), the trial daemon starts again on the same
//     folder with what the scratch daemon left there, with the same gate key
//     and the same paired key, and serves its STAND-IN samples.
//
// The gate's key and certificate, the pins, the list of keys and the trial's
// settings are the same bytes at the end as at the start.
func TestScratchSeatTakesThePhoneTrialsStateFolder(t *testing.T) {
	useSuite(t, cmdSuite{})
	tool := buildScratchInit(t)
	w := newScratchWorld(t)
	env := w.Getenv()
	addr := freeLoopback(t)
	seat := newSeat(t, "seat")

	// The phone trial's folders, as the owner has them.
	state := filepath.Join(w.Dir, "trial-state")
	trialRun := filepath.Join(w.Dir, "trial-run")
	if err := os.Mkdir(trialRun, 0o700); err != nil {
		t.Fatal(err)
	}
	ti := newTestApp(t, nil)
	ti.getenv = env
	if code := ti.run([]string{"trial", "init", "--state-dir", state}); code != 0 {
		t.Fatalf("trial init: %d %s", code, ti.err.String())
	}
	gateFP := gateInit(t, state)
	writePins(t, state, seat.Fingerprint()+"\n", 0o600)
	k, err := ownerjournal.OpenKeys(keysPathOf(state))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.AddKey(ownerjournal.NewKey{
		CredentialID: []byte(strings.Repeat("c", 16)), PublicKey: []byte("k"), Kind: "platform-passkey",
		PairedAt: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC), Words: keyWords(),
	}); err != nil {
		t.Fatal(err)
	}
	k.Close()
	owner := filepath.Join(state, hostadapter.OwnerDirName)
	kept := map[string][]byte{}
	for _, name := range []string{"gate/cert.pem", "gate/key.pem", "gate/" + leaddaemon.SeatPinsFileName, ownerjournal.KeysFileName, hostadapter.ScratchName} {
		kept[name] = fileBytes(t, filepath.Join(owner, name))
	}
	trialSettings := kept[hostadapter.ScratchName]

	startTrial := func() (*testApp, *gateBuf, <-chan int) {
		t.Helper()
		ta := canaryApp(t)
		ta.getenv = env
		errs := &gateBuf{}
		ta.stderr = errs
		done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", trialRun, "--owner-channel", "--trial", "--seat-gate", addr}, trialRun+"/daemon.sock")
		return ta, errs, done
	}
	ta, errs, done := startTrial()
	sub := newTestApp(t, nil)
	if code := sub.run([]string{"trial", "submit", "--socket", trialRun + "/daemon.sock", "--kind", "finish"}); code != 0 {
		t.Fatalf("trial submit: %d %s", code, sub.err.String())
	}

	// The scratch config names the trial's state folder as its state root,
	// a runtime root of its own, and owner/ of the state folder as a
	// never-mounted path.
	w.Config.StateRoot = state
	w.Config.Sandbox.NeverMounted = append(w.Config.Sandbox.NeverMounted, owner)
	w.WriteConfig(t)
	scratchRun := func() (int, string) {
		t.Helper()
		sa := canaryApp(t)
		sa.getenv = env
		code := sa.run([]string{"run", "--scratch", w.ConfigPath, "--owner-channel", "--seat-gate", addr})
		return code, sa.err.String()
	}
	ownerTree := treeOf(t, owner)
	unchanged := func(when string) {
		t.Helper()
		if !sameTreeOf(ownerTree, treeOf(t, owner)) {
			t.Fatalf("%s: the owner folder changed", when)
		}
	}

	// 1. The trial's settings are refused at the config step, before the
	// root marker is looked at and before any lock: nothing is written.
	const trialRefusal = `ataniqd run: --owner-channel: the scratch settings are a phone trial's ("trial": true)`
	code, msg := scratchRun()
	if code != leaddaemon.ExitConfig || !strings.HasPrefix(msg, trialRefusal) {
		t.Fatalf("before scratch-init: exit %d: %s", code, msg)
	}
	unchanged("before scratch-init")
	if _, err := os.Lstat(w.Runtime); err == nil {
		t.Fatal("a refused daemon made the runtime root")
	}

	// The owner's move: only the trial's settings go aside, in owner/, and the
	// non-trial settings take their name.
	base := filepath.Join(w.Dir, "owner-channel")
	for _, d := range []string{base, filepath.Join(base, "drop")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(owner, hostadapter.ScratchName), filepath.Join(owner, "scratch.json.trial")); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, state, base, func(sc *hostadapter.Scratch) {
		sc.Sandbox, sc.Work = filepath.Join(base, "no-template.json"), filepath.Join(base, "work")
		sc.Kinds = []string{"platform-passkey", "vault-passkey", "passkey"}
	})
	ownerTree = treeOf(t, owner)

	// Without the root marker: refused at the config step, before any lock
	// (the trial still runs), naming the marker; nothing is written.
	marker := filepath.Join(state, canaryroot.ScratchMarkerName)
	code, msg = scratchRun()
	if code != leaddaemon.ExitConfig || !strings.Contains(msg, "no marker "+marker) {
		t.Fatalf("without the marker: exit %d: %s", code, msg)
	}
	unchanged("without the marker")

	// 2. scratch-init on the trial's state folder, twice.
	initRun := func() string {
		t.Helper()
		cmd := exec.Command(tool, "scratch-init", "--config", w.ConfigPath)
		cmd.Env = proofEnv(env)
		var out, serr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &serr
		if err := cmd.Run(); err != nil {
			t.Fatalf("scratch-init: %v: %s", err, serr.String())
		}
		return out.String()
	}
	if got, want := initRun(), "kept folder "+state+"\nmade file "+marker+"\n"; got != want {
		t.Fatalf("scratch-init printed %q, want %q", got, want)
	}
	if got, want := initRun(), "kept folder "+state+"\nkept file "+marker+"\n"; got != want {
		t.Fatalf("scratch-init again printed %q, want %q", got, want)
	}
	unchanged("after scratch-init")

	// The trial still runs: the state folder's lock refuses the second daemon.
	code, msg = scratchRun()
	if code != leaddaemon.ExitLockHeld {
		t.Fatalf("while the trial runs: exit %d: %s", code, msg)
	}
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("the trial: exit %d: %s", code, errs.String())
	}

	// 3. The scratch daemon with the seat gate on the trial's state folder.
	s := &scratchSeatWorld{w: w, addr: addr, gateFP: gateFP, seat: seat, drop: filepath.Join(base, "drop"),
		env: env, socket: filepath.Join(w.Runtime, "daemon.sock")}
	old := ownerInspector
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerInspector = old })
	sa, serrs, sdone := s.start(t)
	log := serrs.String()
	if !strings.Contains(log, "ataniqd: seat gate: listening on "+addr+", key fingerprint "+gateFP) ||
		!strings.Contains(log, "ataniqd: seat gate: "+scratchChatNote+"\n") || strings.Contains(log, "TRIAL") ||
		strings.Contains(log, "cannot be opened") {
		t.Fatalf("the scratch daemon's start:\n%s", log)
	}
	if keys := ownerKeys(t, s.socket); len(keys.Keys) != 1 || keys.Keys[0].Kind != "platform-passkey" {
		t.Fatalf("the paired key on the scratch daemon: %+v", keys)
	}
	c := s.dial(t)
	leads, code2 := seatChat(t, c, `{"verb":"leads"}`)
	if code2 != "" || leads.StandIn || len(leads.Leads) != 2 || leads.Leads[0] != (ownerwire.ChatLeadRow{Lead: "daemon:alpha", Chat: ownerwire.ChatOff}) {
		t.Fatalf("leads on the scratch daemon: %+v %q", leads, code2)
	}
	s.define(t, "alpha")
	s.chatSet(t, "alpha", leadwire.ChatRead)
	const text = "Stand-in: handled 1234567."
	// The lead's reply folder is in the trial's state folder now.
	outbox := filepath.Join(state, "leads", "scratch-alpha-lead", "home", leadchat.OutboxName)
	writeReply(t, outbox, "0001-standin.txt", text)
	waitUntil(t, 20*time.Second, "the reply is taken", func() bool { return s.chatRow(t, "alpha").Entries == 2 })
	page, code2 := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`)
	if code2 != "" || page.StandIn || len(page.Entries) != 2 || string(page.Entries[1].Text) != text {
		t.Fatalf("the reply on the seat: %+v %q", page, code2)
	}
	s.chatSet(t, "alpha", leadwire.ChatOff)
	if _, code2 := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`); code2 != ownerwire.CodeChatOff {
		t.Fatalf("a read after off: %q", code2)
	}
	c.Close()
	stopApp(t, sa, serrs, sdone)
	if got := fileBytes(t, filepath.Join(owner, "scratch.json.trial")); !bytes.Equal(got, trialSettings) {
		t.Fatal("the trial's settings set aside changed")
	}

	// 4. Back to the trial, exactly by the owner's two moves.
	if err := os.Rename(filepath.Join(owner, hostadapter.ScratchName), filepath.Join(owner, "scratch.json.leads")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(owner, "scratch.json.trial"), filepath.Join(owner, hostadapter.ScratchName)); err != nil {
		t.Fatal(err)
	}
	ta, errs, done = startTrial()
	tlog := errs.String()
	if !strings.Contains(tlog, "key fingerprint "+gateFP) || !strings.Contains(tlog, trialNote) || strings.Contains(tlog, "cannot be opened") {
		t.Fatalf("the trial after the proof:\n%s", tlog)
	}
	if keys := ownerKeys(t, trialRun+"/daemon.sock"); len(keys.Keys) != 1 {
		t.Fatalf("the paired key on the trial after the proof: %+v", keys)
	}
	g, err := dialGate(addr, seat, gateFP)
	if err != nil {
		t.Fatalf("the pinned seat on the trial after the proof: %v", err)
	}
	var tl ownerwire.SeatChatResult
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := g.Call(ctx, ownerwire.OpSeatChat, ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads}, &tl); err != nil || !tl.StandIn {
		t.Fatalf("the trial's samples after the proof: %+v %v", tl, err)
	}
	g.Close()
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("the trial after the proof: exit %d: %s", code, errs.String())
	}
	for name, want := range kept {
		if got := fileBytes(t, filepath.Join(owner, name)); !bytes.Equal(got, want) {
			t.Errorf("owner/%s is not as it was", name)
		}
	}
}
