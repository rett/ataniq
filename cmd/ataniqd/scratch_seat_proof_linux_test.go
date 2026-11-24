//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/chat/standin"
	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadchat"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadwire"
	"ataniq/internal/seatgate"
)

// These tests are the in-process proof behind the owner's attended proof of
// real replies (docs/scratch-seat-proof.md): the daemon of the build with both
// tags, run as
//
//	ataniqd run --scratch CONFIG --owner-channel --seat-gate IP:PORT
//
// on the scratch config qualification/scratch-seat/prepare.sh writes, whose
// state root is the phone trial's state folder, so that the gate's key, its
// seat pins and the paired phones' keys (owner/keys.log, owner/keys.head) are
// the trial's and the phone stays paired. The trial's state folder is made as
// the owner made his: "ataniqd trial init", "ataniqd seat-gate init", one
// pin, one paired key, and the trial daemon run with one sample card. Nothing
// here starts a unit, a sandbox or an agent: the unit manager is the fake
// one, the binaries prepare copies are stubs it never runs, the systemctl
// prepare asks is a stub that answers "show" only, and the leads are defined,
// not started.

const repoRoot = "../.."

// proofOffTmp is a private folder outside /tmp and /var/tmp, which prepare
// refuses for its root: TMPDIR when it lies elsewhere, else the user's cache
// folder, else /dev/shm.
func proofOffTmp(t *testing.T) string {
	t.Helper()
	bases := []string{os.TempDir()}
	if c, err := os.UserCacheDir(); err == nil {
		bases = append(bases, c)
	}
	bases = append(bases, "/dev/shm")
	for _, b := range bases {
		real, err := filepath.EvalSymlinks(b)
		if err != nil || strings.HasPrefix(real+"/", "/tmp/") || strings.HasPrefix(real+"/", "/var/tmp/") {
			continue
		}
		dir, err := os.MkdirTemp(real, "ataniq-seat-proof-")
		if err != nil {
			continue
		}
		t.Cleanup(func() {
			// A provisioned clone has read-only folders: open them first.
			filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					os.Chmod(p, 0o700)
				}
				return nil
			})
			os.RemoveAll(dir)
		})
		return dir
	}
	t.Fatal("no private folder outside /tmp and /var/tmp: set TMPDIR to one")
	return ""
}

// buildScratchInit builds ataniq-canary with the canary tag through the
// repository's offline Go wrapper, scripts/go-tool.sh, into a temporary
// folder, so that the owner's own command "ataniq-canary scratch-init" is
// what is run on the trial's state folder. The test runs what it builds
// there, so that folder must allow it.
func buildScratchInit(t *testing.T) string {
	t.Helper()
	dir := shortDir(t)
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err == nil && st.Flags&0x8 != 0 { // ST_NOEXEC
		t.Fatalf("TMPDIR (%s) is on a file system mounted noexec: this test builds ataniq-canary there and runs it; set TMPDIR to a folder that allows executing files", dir)
	}
	out := filepath.Join(dir, "ataniq-canary")
	cmd := exec.Command("/bin/sh", "scripts/go-tool.sh", "build", "-tags", "ataniq_canary", "-o", out, "./cmd/ataniq-canary")
	cmd.Dir = repoRoot
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building ataniq-canary with scripts/go-tool.sh: %v\n%s", err, b)
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

// inodeOf is the device and inode of a file.
func inodeOf(t *testing.T, p string) string {
	t.Helper()
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", s.Dev, s.Ino)
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

// proofScript runs the owner's prepare script with --self-test and the
// arguments, and returns what it printed.
func proofScript(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(repoRoot, "qualification", "scratch-seat", "prepare.sh"), "--self-test"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/srv/sample-home"}
	var out, serr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &serr
	if err := cmd.Run(); err != nil {
		t.Fatalf("prepare.sh --self-test %v: %v: %s", args, err, serr.String())
	}
	return out.Bytes()
}

// What qualification/scratch-seat/prepare.sh writes is what the daemon reads:
// its scratch config passes the scratch config's strict reader, with the
// trial's state folder as the state root, one lead running at a time, the
// stand-in lead and the live lead of the sitting with the model named, and
// owner/ of the state folder never mounted; its owner channel's settings pass
// the strict reader of the settings as non-trial settings of the stand-in
// door, whose suite names a sandbox template the script never writes.
func TestScratchSeatProofScriptWritesWhatTheDaemonReads(t *testing.T) {
	dir := shortDir(t)
	state, root := filepath.Join(dir, "trial-state"), filepath.Join(dir, "proof")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "scratch.json")
	if err := os.WriteFile(cfgPath, proofScript(t, "config", state, root, "3", "sample-model"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := leadcanary.ReadScratchConfig(cfgPath)
	if err != nil {
		t.Fatalf("the script's config: %v", err)
	}
	if cfg.StateRoot != state || cfg.RuntimeRoot != filepath.Join(root, "runtime") || cfg.MaxRunning != 1 || len(cfg.Entries) != 2 {
		t.Fatalf("the script's config: %+v", cfg)
	}
	sti, live := cfg.Entries[0], cfg.Entries[1]
	if sti.Key != "standin-3" || sti.Agent.Kind != leadcanary.AgentStandIn || !sti.Inject ||
		live.Key != "live-3" || live.Agent.Kind != leadcanary.AgentLive || !live.Inject ||
		strings.Join(live.Agent.Argv, " ") != "/ataniq/agent/claude --model sample-model" || sti.ProfileDigest != "" || live.ProfileDigest != "" {
		t.Fatalf("the script's leads: %+v", cfg.Entries)
	}
	for _, p := range []string{filepath.Join(state, hostadapter.OwnerDirName), "/srv/sample-home/.claude", "/srv/sample-home/.ssh", "/srv/sample-home/.codex"} {
		if !slices.Contains(cfg.Sandbox.NeverMounted, p) {
			t.Fatalf("%s is not never-mounted: %v", p, cfg.Sandbox.NeverMounted)
		}
	}
	for _, l := range cfg.Entries {
		if len(l.Key) > 32 {
			t.Fatalf("the seat cannot show the lead %q", l.Key)
		}
	}
	// Without a model the config has the stand-in lead only.
	if err := os.WriteFile(cfgPath, proofScript(t, "config", state, root, "3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := leadcanary.ReadScratchConfig(cfgPath); err != nil || len(cfg.Entries) != 1 || cfg.Entries[0].Key != "standin-3" {
		t.Fatalf("the script's config without a model: %+v %v", cfg, err)
	}

	setPath := filepath.Join(root, "owner-scratch.json")
	if err := os.WriteFile(setPath, proofScript(t, "settings", root), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := hostadapter.LoadScratch(setPath)
	if err != nil {
		t.Fatalf("the script's settings: %v", err)
	}
	oc := filepath.Join(root, "owner-channel")
	if sc.Trial || sc.Door != hostadapter.DoorStandIn || len(sc.Roots) != 1 || sc.Roots[0] != oc ||
		sc.Drop != filepath.Join(oc, "drop") || sc.Sandbox != filepath.Join(oc, "no-template.json") {
		t.Fatalf("the script's settings: %+v", sc)
	}
	if _, err := sc.VerifySuiteOf(); err != nil {
		t.Fatalf("the script's settings name no suite: %v", err)
	}
	if err := checkScratchRoots(state, sc, func(string) string { return "" }); err != nil {
		t.Fatalf("the script's settings and the trial's state folder: %v", err)
	}
}

// The gate addresses the script takes are ones the daemon takes, as the same
// address: one table, run through "prepare.sh --self-test gate" and
// leaddaemon.ParseSeatGateAddr. The script is stricter on purpose: IPv4
// only, and no leading zero in the port.
func TestScratchSeatProofGateCheckMatchesTheDaemon(t *testing.T) {
	for _, c := range []struct {
		addr           string
		script, daemon bool
	}{
		{"192.0.2.10:7443", true, true},
		{"10.0.0.1:1", true, true},
		{"127.0.0.1:65535", true, true},
		{"223.255.255.254:7443", true, true},
		{"240.0.0.1:7443", true, true},
		{"0.1.2.3:7443", true, true},
		{"0.0.0.0:7443", false, false},
		{"224.0.0.1:7443", false, false},
		{"239.255.255.255:7443", false, false},
		{"255.255.255.255:7443", false, false},
		{"192.0.2.10:0", false, false},
		{"192.0.2.10", false, false},
		{"192.0.2.300:7443", false, false},
		{"192.0.2.10:70000", false, false},
		{"192.0.2.010:7443", false, false},
		{"192.0.2.10:+7443", false, false},
		{"1.2.3:7443", false, false},
		{"1.2.3.4.5:7443", false, false},
		{":7443", false, false},
		{"", false, false},
		{"gate.example.invalid:7443", false, false},
		{"localhost:7443", false, false},
		{"192.0.2.10:07443", false, true},
		{"[2001:db8::10]:7443", false, true},
		{"[::ffff:192.0.2.10]:7443", false, true},
		{"[::]:7443", false, false},
		{"[0:0:0:0:0:0:0:0]:7443", false, false},
		{"[::ffff:0.0.0.0]:7443", false, false},
		{"[abc]:7443", false, false},
		{"[ff02::1]:7443", false, false},
		{"[fe80::1%lo]:7443", false, false},
	} {
		cmd := exec.Command("/bin/sh", filepath.Join(repoRoot, "qualification", "scratch-seat", "prepare.sh"), "--self-test", "gate", c.addr)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		script := err == nil
		var exit *exec.ExitError
		if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
			t.Errorf("%q: the script: %v %s", c.addr, err, out)
		}
		ap, derr := leaddaemon.ParseSeatGateAddr(c.addr)
		daemon := derr == nil
		if script != c.script || daemon != c.daemon {
			t.Errorf("%q: the script takes it %v (want %v), the daemon %v (want %v): %s %v", c.addr, script, c.script, daemon, c.daemon, out, derr)
		}
		if script && (!daemon || ap.String() != c.addr || string(out) != "gate "+c.addr+"\n") {
			t.Errorf("%q: the script takes it, the daemon reads %q %v", c.addr, ap, derr)
		}
		if !script && strings.HasPrefix(c.addr, "[") && !strings.Contains(string(out), "IPv4 gate address only") {
			t.Errorf("%q: the script's refusal does not say IPv4 only: %s", c.addr, out)
		}
	}
}

// gatePorts makes the gate of every daemon the test starts listen on a port
// the kernel gives on the loopback address, whatever --seat-gate names, and
// returns the address of the latest one: no port is picked and freed for the
// daemon to take.
func gatePorts(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	last := ""
	old := seatGateListen
	seatGateListen = func(network, _ string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err == nil {
			mu.Lock()
			last = ln.Addr().String()
			mu.Unlock()
		}
		return ln, err
	}
	t.Cleanup(func() { seatGateListen = old })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// proofKit is the owner's side of the proof in a private folder outside
// /tmp: stubs of the binaries prepare copies, a systemctl stub that answers
// "show" from a file of the test's ("UNIT PROP VALUE" lines, the value the
// rest of the line), a folder that stands for /proc, and a home.
type proofKit struct {
	dir, bin, tools, units, home string
}

func newProofKit(t *testing.T) proofKit {
	t.Helper()
	dir := proofOffTmp(t)
	k := proofKit{dir: dir, bin: filepath.Join(dir, "bin"), tools: filepath.Join(dir, "tools"), units: filepath.Join(dir, "units"), home: filepath.Join(dir, "home")}
	for _, d := range []string{k.bin, k.tools, k.home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stub := "#!/bin/sh\nexit 97\n"
	for _, name := range []string{"ataniq-holder", "ataniq-canary", "ataniq-canary-standin", "ataniq-console", "ataniq-probe", "ataniqd", "pasta", "bwrap", "claude-file"} {
		if err := os.WriteFile(filepath.Join(k.bin, name), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	systemctl := "#!/bin/sh\n" +
		"[ \"$1 $2 $3\" = '--user show -p' ] && [ \"$5 $6\" = '--value --' ] || exit 97\n" +
		"v=$(awk -v u=\"$7\" -v p=\"$4\" '$1 == u && $2 == p { s = $0; sub(/^[^ ]+ [^ ]+ ?/, \"\", s); print s; exit }' '" + k.units + "' 2>/dev/null)\n" +
		"if [ -z \"$v\" ]; then case $4 in ActiveState) v=inactive ;; LoadState) v=not-found ;; esac; fi\n" +
		"printf '%s\\n' \"$v\"\n"
	if err := os.WriteFile(filepath.Join(k.tools, "systemctl"), []byte(systemctl), 0o700); err != nil {
		t.Fatal(err)
	}
	return k
}

// unitsSay sets the answers of the systemctl stub.
func (k proofKit) unitsSay(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.WriteFile(k.units, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// prepare runs the owner's prepare script and returns its output and exit
// status.
func (k proofKit) prepare(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(repoRoot, "qualification", "scratch-seat", "prepare.sh")}, args...)...)
	cmd.Env = []string{"PATH=" + k.tools + ":/usr/bin:/bin", "HOME=" + k.home, "PREPARE_PROC=" + filepath.Join(k.dir, "proc")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("prepare.sh did not run: %v", err)
		}
		return string(out), exit.ExitCode()
	}
	return string(out), 0
}

// defineLead defines a lead on the daemon's socket, as the console's d does.
func defineLead(t *testing.T, socket, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var r leadwire.Receipt
	if err := cl.Call(ctx, leadwire.OpDefine, leadwire.DefineParams{Client: "test", CommandID: "def-" + key, Role: "scratch-" + key + "/lead"}, &r); err != nil || r.Status != leadwire.StatusDone {
		t.Fatalf("define %s: %+v %v", key, r, err)
	}
}

// chatOf runs "ataniqd chat list --config CONFIG" and returns the lead's row.
func chatOf(t *testing.T, config, key string) leadwire.ChatLead {
	t.Helper()
	code, out, errs := chatRun(t, "list", "--config", config)
	var list leadwire.ChatListResult
	if code != 0 || json.Unmarshal([]byte(out), &list) != nil {
		t.Fatalf("chat list: %d %q %s", code, out, errs)
	}
	for _, r := range list.Leads {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("chat list has no %s: %+v", key, list)
	return leadwire.ChatLead{}
}

// chatTo runs "ataniqd chat set KEY SWITCH --config CONFIG" and wants 0.
func chatTo(t *testing.T, config, key, sw string) {
	t.Helper()
	code, out, errs := chatRun(t, "set", key, sw, "--config", config)
	var row leadwire.ChatLead
	if code != 0 || json.Unmarshal([]byte(out), &row) != nil || row.Key != key || row.Chat != sw {
		t.Fatalf("chat set %s %s: %d %q %s", key, sw, code, out, errs)
	}
}

// dialSeat connects the pinned seat to a gate, with the capabilities the
// seat link names.
func dialSeat(t *testing.T, addr string, seat seatgate.Identity, gateFP string) *seatgate.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := seatgate.Dial(ctx, addr, seatgate.ClientConfig{
		TLS:        seatgate.ClientTLS(seat, gateFP, "gate.example.invalid"),
		Caps:       []string{"pair", "read", "login", "decide", "watch", "genesis", "chat"},
		BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("the pinned seat at %s: %v", addr, err)
	}
	return c
}

// The scratch daemon on the phone trial's state folder, step by step as the
// owner takes it (docs/scratch-seat-proof.md), on the files prepare.sh
// --write makes in a folder of the proof's own, with the settings swapped by
// prepare's --swap-in and --swap-out:
//
//  1. --write writes the config, the settings and the copies in ROOT and
//     nothing in the state folder. While the trial's settings ("trial":
//     true) are in owner/scratch.json the scratch daemon refuses to start at
//     the config step (exit 10), before it looks at the root marker or takes
//     a lock, and writes nothing. --status says the trial is in place;
//     --record-trial records the trial's unit, a transient one, from the
//     systemctl stub and a fake /proc entry of its main process; --swap-in
//     refuses the running unit and, with the unit gone, a gate port the
//     trial still listens on.
//  2. The daemon's own guards behind prepare's: with the settings put in
//     place by the test while the trial runs, the daemon refuses for the
//     missing root marker (exit 10); "ataniq-canary scratch-init" takes the
//     trial's private state folder as it is ("kept folder"), writes the
//     marker and keeps it when run again, writing nothing in owner/; with the
//     marker, the state folder's daemon lock refuses the scratch daemon (exit
//     12). The test's own move is undone.
//  3. Once the trial has stopped, --swap-in puts the proof's settings in
//     place; the daemon starts: the gate has the trial's key fingerprint,
//     the trial's pinned seat reaches it, the paired key is listed, both
//     leads of the sitting are defined (provisioned, not started) and a
//     reply of the stand-in is read by the seat once the desk turns its chat
//     on; the live lead's chat is turned on and off. Restarted with the
//     stand-in's chat on, the daemon comes back with every chat off.
//  4. --swap-out puts the same trial settings file back and names the
//     restart script as step 15; that script, run with a systemd-run stub,
//     gives the unit's name, description, --collect and the trial's command
//     line; the trial daemon starts again on the same folder with what the
//     scratch daemon left there, with the same gate key, the same paired
//     credential and its known STAND-IN sample rows.
//
// With no phone login in between, the gate's key and certificate, the pins,
// keys.log, keys.head and the trial's settings are the same bytes at the end
// as at the start, and the settings the same file. (A real login on the
// phone may advance a key's counter in the key journal; the sitting does not
// compare those bytes.)
func TestScratchSeatTakesThePhoneTrialsStateFolder(t *testing.T) {
	useSuite(t, cmdSuite{})
	tool := buildScratchInit(t)
	last := gatePorts(t)
	k := newProofKit(t)
	envMap := map[string]string{
		"HOME": k.home, "XDG_STATE_HOME": filepath.Join(k.dir, "xdg-state"), "XDG_RUNTIME_DIR": filepath.Join(k.dir, "xdg-run"),
	}
	env := func(key string) string { return envMap[key] }
	seat := newSeat(t, "seat")
	// The --seat-gate the owner gives; gatePorts binds a port of the kernel's.
	const nominal = "127.0.0.1:7443"

	// The phone trial's folders, as the owner has them.
	state := filepath.Join(k.dir, "trial-state")
	trialRun := filepath.Join(shortDir(t), "trial-run")
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
	credID := []byte(strings.Repeat("c", 16))
	keys, err := ownerjournal.OpenKeys(keysPathOf(state))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.AddKey(ownerjournal.NewKey{
		CredentialID: credID, PublicKey: []byte("k"), Kind: "platform-passkey",
		PairedAt: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC), Words: keyWords(),
	}); err != nil {
		t.Fatal(err)
	}
	keys.Close()
	owner := filepath.Join(state, hostadapter.OwnerDirName)
	canon := filepath.Join(owner, hostadapter.ScratchName)
	kept := map[string][]byte{}
	for _, name := range []string{"gate/cert.pem", "gate/key.pem", "gate/" + leaddaemon.SeatPinsFileName, ownerjournal.KeysFileName, ownerjournal.KeysHeadFileName, hostadapter.ScratchName} {
		kept[name] = fileBytes(t, filepath.Join(owner, name))
	}
	trialSettings, trialID := kept[hostadapter.ScratchName], inodeOf(t, canon)
	wantKey := func(when string, v ownerwire.KeysView) {
		t.Helper()
		if len(v.Keys) != 1 || v.Keys[0].ID != ownerjournal.KeyDisplayID(credID) || v.Keys[0].Kind != "platform-passkey" || v.Keys[0].Revoked {
			t.Fatalf("%s: the paired credential: %+v", when, v)
		}
	}

	startTrial := func() (*testApp, *gateBuf, <-chan int, string) {
		t.Helper()
		ta := canaryApp(t)
		ta.getenv = env
		errs := &gateBuf{}
		ta.stderr = errs
		done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", trialRun, "--owner-channel", "--trial", "--seat-gate", nominal}, trialRun+"/daemon.sock")
		return ta, errs, done, last()
	}
	ta, errs, done, trialAddr := startTrial()
	sub := newTestApp(t, nil)
	if code := sub.run([]string{"trial", "submit", "--socket", trialRun + "/daemon.sock", "--kind", "finish"}); code != 0 {
		t.Fatalf("trial submit: %d %s", code, sub.err.String())
	}

	// 1. prepare --write, as the owner runs it, into ROOT.
	root := filepath.Join(k.dir, "proof")
	config := filepath.Join(root, "scratch.json")
	stateTree := treeOf(t, owner)
	vals := []string{"--state", state, "--root", root}
	out, code := k.prepare(t, append(slices.Clone(vals), "--write", "--gate", nominal, "--sitting", "1", "--unit", "sample-trial.service",
		"--bin", k.bin, "--daemon", filepath.Join(k.bin, "ataniqd"), "--pasta", filepath.Join(k.bin, "pasta"), "--bwrap", filepath.Join(k.bin, "bwrap"),
		"--claude", filepath.Join(k.bin, "claude-file"), "--model", "sample-model")...)
	if code != 0 {
		t.Fatalf("prepare --write: exit %d:\n%s", code, out)
	}
	if !sameTreeOf(stateTree, treeOf(t, owner)) {
		t.Fatal("prepare --write changed the owner folder")
	}
	proofSettings := fileBytes(t, filepath.Join(root, "owner-scratch.json"))

	scratchRun := func() (int, string) {
		t.Helper()
		sa := canaryApp(t)
		sa.getenv = env
		code := sa.run([]string{"run", "--scratch", config, "--owner-channel", "--seat-gate", nominal})
		return code, sa.err.String()
	}
	runtimeRoot := filepath.Join(root, "runtime")
	const trialRefusal = `ataniqd run: --owner-channel: the scratch settings are a phone trial's ("trial": true)`
	code, msg := scratchRun()
	if code != leaddaemon.ExitConfig || !strings.HasPrefix(msg, trialRefusal) {
		t.Fatalf("on the trial's settings: exit %d: %s", code, msg)
	}
	if _, err := os.Lstat(runtimeRoot); err == nil {
		t.Fatal("a refused daemon made the runtime root")
	}
	if out, code := k.prepare(t, append([]string{"--status"}, vals...)...); code != 0 || !strings.Contains(out, "status: trial in place (not swapped)") {
		t.Fatalf("--status before: exit %d:\n%s", code, out)
	}
	// f. --record-trial while the trial runs. The trial's unit is a
	// transient one, as systemd-run makes it; its main process is a fake
	// /proc entry with the trial daemon's command line.
	trialProg := filepath.Join(k.bin, "ataniqd")
	trialArgv := []string{trialProg, "run", "--state-dir", state, "--runtime-dir", trialRun, "--owner-channel", "--trial", "--seat-gate", nominal}
	if err := os.MkdirAll(filepath.Join(k.dir, "proc", "4242"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k.dir, "proc", "4242", "cmdline"), []byte(strings.Join(trialArgv, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := "sample-trial.service "
	k.unitsSay(t, u+"Id sample-trial.service", u+"Description Sample phone trial", u+"Transient yes", u+"UnitFileState transient",
		u+"FragmentPath "+filepath.Join(k.dir, "xdg-run", "systemd", "transient", "sample-trial.service"), u+"CollectMode inactive-or-failed",
		u+"LoadState loaded", u+"ActiveState active", u+"SubState running", u+"InvocationID 00112233445566778899aabbccddeeff", u+"MainPID 4242",
		u+"Restart no", u+"ExecStart { path="+trialProg+" ; argv[]="+strings.Join(trialArgv, " ")+" ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=4242 ; code=(null) ; status=0/0 }")
	if out, code := k.prepare(t, append(append([]string{"--record-trial"}, vals...), "--unit", "sample-trial.service")...); code != 0 ||
		!strings.Contains(out, "recorded the trial's unit: transient") {
		t.Fatalf("--record-trial: exit %d:\n%s", code, out)
	}
	if out, code := k.prepare(t, append([]string{"--status"}, vals...)...); code != 0 || !strings.Contains(out, "next: stop the trial's transient unit (step 1; it is not masked") {
		t.Fatalf("--status after the record: exit %d:\n%s", code, out)
	}
	swapIn := func(gate string) (string, int) {
		return k.prepare(t, append(append([]string{"--swap-in"}, vals...), "--gate", gate, "--sitting", "1", "--unit", "sample-trial.service")...)
	}
	if out, code := swapIn(trialAddr); code != 1 || !strings.Contains(out, "not inactive") {
		t.Fatalf("--swap-in with the trial's unit active: exit %d:\n%s", code, out)
	}
	// Stopped, the transient unit is gone (inactive, not-found).
	k.unitsSay(t)
	if out, code := swapIn(trialAddr); code != 1 || !strings.Contains(out, "listens on the gate's port") {
		t.Fatalf("--swap-in while the trial's gate listens: exit %d:\n%s", code, out)
	}
	if !bytes.Equal(fileBytes(t, canon), trialSettings) || inodeOf(t, canon) != trialID {
		t.Fatal("a refused --swap-in moved the trial's settings")
	}

	// 2. The daemon's own guards, behind prepare's: the test puts the proof's
	// settings in place itself while the trial runs.
	aside := filepath.Join(owner, "scratch.json.trial")
	if err := os.Rename(canon, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canon, proofSettings, 0o600); err != nil {
		t.Fatal(err)
	}
	ownerTree := treeOf(t, owner)
	marker := filepath.Join(state, canaryroot.ScratchMarkerName)
	code, msg = scratchRun()
	if code != leaddaemon.ExitConfig || !strings.Contains(msg, "no marker "+marker) {
		t.Fatalf("without the marker: exit %d: %s", code, msg)
	}
	initRun := func() string {
		t.Helper()
		cmd := exec.Command(tool, "scratch-init", "--config", config)
		cmd.Env = proofEnv(env)
		var out, serr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &serr
		if err := cmd.Run(); err != nil {
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) {
				t.Fatalf("ataniq-canary, built in TMPDIR, cannot be run (a noexec mount?): set TMPDIR to a folder that allows executing files: %v", err)
			}
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
	code, msg = scratchRun()
	if code != leaddaemon.ExitLockHeld {
		t.Fatalf("while the trial runs: exit %d: %s", code, msg)
	}
	if !sameTreeOf(ownerTree, treeOf(t, owner)) {
		t.Fatal("the refused daemons or scratch-init changed the owner folder")
	}
	if err := os.Remove(canon); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(aside, canon); err != nil {
		t.Fatal(err)
	}
	stopApp(t, ta, errs, done)

	// 3. The trial is stopped (its transient unit gone): --swap-in, then
	// the scratch daemon with the seat gate on the trial's state folder.
	out, code = swapIn(trialAddr)
	if code != 0 || !strings.Contains(out, "status: swapped in") {
		t.Fatalf("--swap-in: exit %d:\n%s", code, out)
	}
	if !bytes.Equal(fileBytes(t, canon), proofSettings) || !bytes.Equal(fileBytes(t, aside), trialSettings) || inodeOf(t, aside) != trialID {
		t.Fatal("after --swap-in the settings are not in place")
	}
	old := ownerInspector
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerInspector = old })
	socket := filepath.Join(runtimeRoot, "daemon.sock")
	startScratch := func() (*testApp, *gateBuf, <-chan int, string) {
		t.Helper()
		sa := canaryApp(t)
		sa.getenv = env
		serrs := &gateBuf{}
		sa.stderr = serrs
		sdone := startApp(t, sa, []string{"--scratch", config, "--owner-channel", "--seat-gate", nominal}, socket)
		return sa, serrs, sdone, last()
	}
	sa, serrs, sdone, addr := startScratch()
	log := serrs.String()
	if !strings.Contains(log, "ataniqd: seat gate: listening on "+addr+", key fingerprint "+gateFP) ||
		!strings.Contains(log, "ataniqd: seat gate: "+scratchChatNote+"\n") || strings.Contains(log, "TRIAL") ||
		strings.Contains(log, "cannot be opened") {
		t.Fatalf("the scratch daemon's start:\n%s", log)
	}
	wantKey("the scratch daemon", ownerKeys(t, socket))
	c := dialSeat(t, addr, seat, gateFP)
	offRows := []ownerwire.ChatLeadRow{{Lead: "daemon:standin-1", Chat: ownerwire.ChatOff}, {Lead: "daemon:live-1", Chat: ownerwire.ChatOff}}
	leads, code2 := seatChat(t, c, `{"verb":"leads"}`)
	if code2 != "" || leads.StandIn || !slices.Equal(leads.Leads, offRows) {
		t.Fatalf("leads on the scratch daemon: %+v %q", leads, code2)
	}
	defineLead(t, socket, "standin-1")
	defineLead(t, socket, "live-1")
	for _, key := range []string{"standin-1", "live-1"} {
		if _, err := os.Stat(filepath.Join(state, leadcanary.ScratchDirName, "scratch-"+key+"-lead", "provisioned.json")); err != nil {
			t.Fatalf("the lead %s is not provisioned: %v", key, err)
		}
	}
	chatTo(t, config, "standin-1", leadwire.ChatRead)
	const text = "Stand-in: handled 1234567."
	outbox := filepath.Join(state, "leads", "scratch-standin-1-lead", "home", leadchat.OutboxName)
	writeReply(t, outbox, "0001-standin.txt", text)
	waitUntil(t, 20*time.Second, "the reply is taken", func() bool { return chatOf(t, config, "standin-1").Entries == 2 })
	page, code2 := seatChat(t, c, `{"verb":"read","lead":"daemon:standin-1","limit":16}`)
	if code2 != "" || page.StandIn || len(page.Entries) != 2 || string(page.Entries[1].Text) != text {
		t.Fatalf("the reply on the seat: %+v %q", page, code2)
	}
	chatTo(t, config, "live-1", leadwire.ChatRead)
	chatTo(t, config, "live-1", leadwire.ChatOff)
	if _, code2 := seatChat(t, c, `{"verb":"read","lead":"daemon:live-1","limit":16}`); code2 != ownerwire.CodeChatOff {
		t.Fatalf("a read after off: %q", code2)
	}
	c.Close()
	// A restart with the stopped stand-in's chat on: every chat comes back off.
	if r := chatOf(t, config, "standin-1"); r.Chat != leadwire.ChatRead {
		t.Fatalf("the stand-in's chat before the restart: %+v", r)
	}
	stopApp(t, sa, serrs, sdone)
	sa, serrs, sdone, addr = startScratch()
	if r := chatOf(t, config, "standin-1"); r.Chat != leadwire.ChatOff {
		t.Fatalf("the stand-in's chat after the restart: %+v", r)
	}
	c = dialSeat(t, addr, seat, gateFP)
	if leads, code2 := seatChat(t, c, `{"verb":"leads"}`); code2 != "" || !slices.Equal(leads.Leads, offRows) {
		t.Fatalf("leads after the restart: %+v %q", leads, code2)
	}
	c.Close()
	stopApp(t, sa, serrs, sdone)
	if !bytes.Equal(fileBytes(t, aside), trialSettings) {
		t.Fatal("the trial's settings set aside changed")
	}

	// 4. Back to the trial: --swap-out, then the trial daemon again.
	out, code = k.prepare(t, append(append([]string{"--swap-out"}, vals...), "--gate", addr, "--unit", "sample-trial.service")...)
	if code != 0 || !strings.Contains(out, "status: trial in place (the way back is done)") {
		t.Fatalf("--swap-out: exit %d:\n%s", code, out)
	}
	if inodeOf(t, canon) != trialID || !bytes.Equal(fileBytes(t, filepath.Join(owner, "scratch.json.leads-1")), proofSettings) {
		t.Fatal("after --swap-out the trial's settings file is not back, or the proof's is not archived")
	}
	restart := filepath.Join(root, "trial-restart.sh")
	if !strings.Contains(out, "next: re-create the trial's transient unit: OWNER ONLY: sh "+restart+" (step 15)") {
		t.Fatalf("--swap-out does not name the restart script:\n%s", out)
	}
	if g := string(fileBytes(t, filepath.Join(root, "evidence", "trial-unit-guard.txt"))); !strings.Contains(g, "kind transient, ActiveState=inactive, LoadState=not-found") {
		t.Fatalf("the guard's evidence: %q", g)
	}
	// Step 15, the owner's: the restart script, run here only with a
	// systemd-run stub that records its arguments, gives the trial's unit
	// its name, description, --collect and the command line recorded.
	stubs := filepath.Join(k.dir, "restart-stubs")
	gotArgs := filepath.Join(k.dir, "systemd-run-args")
	if err := os.Mkdir(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubs, "systemd-run"), []byte("#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done >'"+gotArgs+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rs := exec.Command("/bin/sh", restart)
	rs.Env = []string{"PATH=" + stubs + ":/usr/bin:/bin", "HOME=" + k.home}
	if b, err := rs.CombinedOutput(); err != nil {
		t.Fatalf("the restart script with a stub: %v\n%s", err, b)
	}
	wantArgs := append([]string{"--user", "--unit", "sample-trial.service", "--description", "Sample phone trial", "--collect", "--"}, trialArgv...)
	if got := string(fileBytes(t, gotArgs)); got != strings.Join(wantArgs, "\n")+"\n" {
		t.Fatalf("systemd-run got:\n%s\nwant:\n%s", got, strings.Join(wantArgs, "\n"))
	}
	ta, errs, done, trialAddr = startTrial()
	tlog := errs.String()
	if !strings.Contains(tlog, "listening on "+trialAddr+", key fingerprint "+gateFP) || !strings.Contains(tlog, trialNote) || strings.Contains(tlog, "cannot be opened") {
		t.Fatalf("the trial after the proof:\n%s", tlog)
	}
	wantKey("the trial after the proof", ownerKeys(t, trialRun+"/daemon.sock"))
	g, err := dialGate(trialAddr, seat, gateFP)
	if err != nil {
		t.Fatalf("the pinned seat on the trial after the proof: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var tl ownerwire.SeatChatResult
	if err := g.Call(ctx, ownerwire.OpSeatChat, ownerwire.SeatChatParams{Verb: ownerwire.ChatVerbLeads}, &tl); err != nil ||
		!tl.StandIn || len(tl.Leads) != 3 || tl.Leads[0].Lead != standin.LeadAlpha || tl.Leads[2].Lead != standin.LeadCharlie || tl.Leads[2].Chat != ownerwire.ChatOff {
		t.Fatalf("the trial's sample rows after the proof: %+v %v", tl, err)
	}
	var gl ownerwire.Listing
	if err := g.Call(ctx, ownerwire.OpList, nil, &gl); err != nil || gl.Count.Requests != 1 {
		t.Fatalf("the trial's sample cards after the proof: %+v %v", gl, err)
	}
	g.Close()
	stopApp(t, ta, errs, done)
	for name, want := range kept {
		if got := fileBytes(t, filepath.Join(owner, name)); !bytes.Equal(got, want) {
			t.Errorf("owner/%s is not as it was", name)
		}
	}
}
