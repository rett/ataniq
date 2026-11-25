package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Tests of qualification/scratch-seat/prepare.sh, the preparation of the
// owner-attended proof of real lead replies (docs/scratch-seat-proof.md).
// Nothing here starts a unit, a daemon, a sandbox or an agent: the binaries,
// pasta, claude and the daemon of these tests are recording stubs, the PATH
// puts recording stubs of systemctl, systemd-run, bwrap, claude and curl
// first, and the trial's state folder is a sample one in a private folder of
// the test. --write and --status run only in that folder.

const scratchSeatPrepare = "qualification/scratch-seat/prepare.sh"

// scratchSeatKit is the sample world of one run of the script.
type scratchSeatKit struct {
	dir, bin, tools, calls, state, home string
}

// scratchSeatStubs are the programs on PATH that the script must never run.
var scratchSeatStubs = []string{"systemctl", "systemd-run", "journalctl", "bwrap", "pasta", "claude", "ataniqd", "ataniq-console", "curl"}

// scratchSeatTmp is a private folder for a kit: in TMPDIR when that is not
// in /tmp or /var/tmp (which the script refuses for its root), else where
// the run kit's tests make theirs.
func scratchSeatTmp(t *testing.T) string {
	t.Helper()
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil && !strings.HasPrefix(real+"/", "/tmp/") && !strings.HasPrefix(real+"/", "/var/tmp/") {
		dir, err := os.MkdirTemp(real, "ataniq-seat-proof-")
		if err == nil {
			t.Cleanup(func() {
				filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
					if err == nil && d.IsDir() {
						os.Chmod(p, 0o700)
					}
					return nil
				})
				os.RemoveAll(dir)
			})
			return dir
		}
	}
	return canaryOffTmp(t)
}

// newScratchSeatKit makes a folder outside /tmp with the trial's state folder
// as the owner has it (the gate's key and pins, the trial's settings, the
// list of keys), the binaries of build/canary and the daemon as recording
// stubs, and the tools.
func newScratchSeatKit(t *testing.T) scratchSeatKit {
	t.Helper()
	dir := scratchSeatTmp(t)
	k := scratchSeatKit{dir: dir, bin: filepath.Join(dir, "bin"), tools: filepath.Join(dir, "tools"),
		calls: filepath.Join(dir, "calls"), state: filepath.Join(dir, "trial-state"), home: filepath.Join(dir, "home")}
	for _, d := range []string{k.bin, k.tools, k.home, k.state, filepath.Join(k.state, "owner"), filepath.Join(k.state, "owner", "gate")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"owner/gate/cert.pem": "sample certificate\n", "owner/gate/key.pem": "sample key\n",
		"owner/gate/seat-pins": strings.Repeat("ab", 32) + "\n", "owner/scratch.json": "{\n  \"trial\": true\n}\n",
		"owner/keys.log": "sample keys\n", "owner/keys.head": "sample head\n",
	} {
		if err := os.WriteFile(filepath.Join(k.state, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >>'" + k.calls + "'\nexit 97\n"
	for _, name := range []string{"ataniq-holder", "ataniq-canary", "ataniq-canary-standin", "ataniq-console", "ataniq-probe", "ataniqd", "pasta", "claude-file"} {
		if err := os.WriteFile(filepath.Join(k.bin, name), []byte(record), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range scratchSeatStubs {
		if err := os.WriteFile(filepath.Join(k.tools, name), []byte(record), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return k
}

// args are the flags of a run with the kit's binaries, the root given, the
// sample gate, sitting 3 and, with live, the claude stub and a model.
func (k scratchSeatKit) args(mode, root string, live bool, more ...string) []string {
	a := []string{mode, "--state", k.state, "--root", root, "--gate", "192.0.2.10:7443", "--sitting", "3",
		"--bin", k.bin, "--daemon", filepath.Join(k.bin, "ataniqd"), "--pasta", filepath.Join(k.bin, "pasta"), "--bwrap", filepath.Join(k.tools, "bwrap")}
	if live {
		a = append(a, "--claude", filepath.Join(k.bin, "claude-file"), "--model", "sample-model")
	}
	return append(a, more...)
}

// run runs the script with the environment given beside the kit's, and
// returns its output and exit status.
func (k scratchSeatKit) run(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), scratchSeatPrepare)}, args...)...)
	cmd.Dir = k.dir
	cmd.Env = append([]string{"PATH=" + k.tools + ":/usr/bin:/bin", "HOME=" + k.home, "XDG_RUNTIME_DIR=" + filepath.Join(k.dir, "run")}, env...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the script did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

// noCalls fails the test when a stub ran.
func (k scratchSeatKit) noCalls(t *testing.T) {
	t.Helper()
	if b, err := os.ReadFile(k.calls); err == nil {
		t.Fatalf("the script ran a program it must never run:\n%s", b)
	}
}

// scratchSeatFiles maps every path below dir to its mode and content, so
// that a change of either is seen.
func scratchSeatFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(b)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameScratchSeatFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The script and the network check's template are plain POSIX sh; the
// script sets -eu, and names neither build tag nor the make target that
// builds the daemon of both tags: it only names the binary,
// build/scratch-seat/ataniqd. It never deletes, moves or links a file and
// never starts anything itself: no line of it runs rm, mv, ln, unlink,
// systemctl, systemd-run, bwrap, pasta, claude or the console (they are only
// printed for the owner); git runs only through proof_git; and the daemon
// runs on one line, its --check, which binds, locks and starts nothing.
func TestScratchSeatPrepareIsPlainShell(t *testing.T) {
	canaryPlainShell(t, scratchSeatPrepare, true)
	canaryPlainShell(t, "qualification/scratch-seat/netcheck.sh.in", false)
	data, err := os.ReadFile(filepath.Join(root(t), scratchSeatPrepare))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, bad := range []string{"ataniq_" + "canary", "ataniq_" + "ownerchannel", "scratch-seat-" + "build", "go-tool.sh", "go build", "mkdir -p"} {
		if strings.Contains(text, bad) {
			t.Errorf("the script names %q", bad)
		}
	}
	if !strings.Contains(text, "build/scratch-seat/ataniqd") {
		t.Error("the script does not name the daemon's binary")
	}
	// A command line of the script (not a line of a here-document, which is
	// printed) that runs one of these programs.
	inDoc := false
	allowed := map[string]string{
		`"$DAEMON"`:    `"$DAEMON" run --scratch "$CONFIG" --owner-channel --seat-gate "$GATE" --owner-settings "$SETTINGS_DIR" --check || RC=$?`,
		"/usr/bin/git": `/usr/bin/git -c core.hooksPath=/dev/null -c commit.gpgSign=false -c tag.gpgSign=false -c core.fsmonitor=false \`,
	}
	seen := map[string]int{}
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if inDoc {
			if trimmed == "EOF" {
				inDoc = false
			}
			continue
		}
		if strings.Contains(line, "<<EOF") {
			inDoc = true
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, f := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == ';' || r == '|' || r == '&' || r == '(' }) {
			fields := strings.Fields(f)
			if len(fields) == 0 {
				continue
			}
			word := fields[0]
			switch word {
			case "rm", "rmdir", "mv", "ln", "unlink", "git", "systemctl", "journalctl", "systemd-run", "bwrap", "\"$BWRAP\"", "\"$PASTA_USE\"", "\"$CLAUDE_SRC\"":
				t.Errorf("line %d runs %s: %q", i+1, word, trimmed)
			case "\"$DAEMON\"", "/usr/bin/git":
				seen[word]++
				if !strings.Contains(trimmed, allowed[word]) {
					t.Errorf("line %d runs %s other than as allowed: %q", i+1, word, trimmed)
				}
			}
		}
	}
	for word := range allowed {
		if seen[word] != 1 {
			t.Errorf("%s is run on %d lines, want 1", word, seen[word])
		}
	}
	if !strings.Contains(text, "/usr/bin/env -i PATH=/usr/bin:/bin HOME=\"$ROOT\"") {
		t.Error("proof_git does not clear the environment")
	}
}

// --self-test with no name checks the script's own promises and prints one
// line; it reads, writes and runs nothing. The named entries compute only:
// the gate address, the config, the settings and the owner's steps.
func TestScratchSeatPrepareSelfTest(t *testing.T) {
	k := newScratchSeatKit(t)
	before := scratchSeatFiles(t, k.dir)
	out, code := k.run(t, nil, "--self-test")
	if code != 0 || !strings.Contains(out, "checks passed; nothing was read, written or run") || strings.Count(out, "\n") != 1 {
		t.Fatalf("--self-test: exit %d:\n%s", code, out)
	}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"gate", "192.0.2.10:7443"}, 0, "gate 192.0.2.10:7443\n"},
		{[]string{"gate", "[2001:db8::10]:7443"}, 1, "IPv4 gate address only"},
		{[]string{"gate", "[abc]:7443"}, 1, "IPv4 gate address only"},
		{[]string{"gate", "0.0.0.0:7443"}, 1, "wildcard"},
		{[]string{"gate", "224.0.0.1:7443"}, 1, "multicast"},
		{[]string{"gate", "255.255.255.255:7443"}, 1, "broadcast"},
		{[]string{"gate", "gate.example:7443"}, 1, "not an IPv4 address"},
		{[]string{"gate", "192.0.2.010:7443"}, 1, "not an IPv4 address"},
		{[]string{"gate", "192.0.2.10:0"}, 1, "port"},
		{[]string{"gate", "192.0.2.10"}, 1, "not IPv4:PORT"},
		{[]string{"gate", "192.0.2.256:7443"}, 1, "not an IPv4 address"},
		{[]string{"config", "relative", "/srv/proof", "3"}, 1, "not an absolute path"},
		{[]string{"config", "/srv/trial", "/srv/proof", "3", "-bad"}, 1, "--model"},
		{[]string{"config", "/srv/trial", "/srv/proof", "03"}, 1, "sitting"},
		{[]string{"settings", "/srv/a b"}, 1, "character"},
		{[]string{"nothing"}, 2, "usage"},
		{[]string{"gate"}, 2, "usage"},
		{[]string{"commands", "/srv/trial", "/srv/proof", "192.0.2.10:7443", "3", "more"}, 2, "usage"},
	} {
		out, code := k.run(t, nil, append([]string{"--self-test"}, c.args...)...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("--self-test %v: exit %d, want %d and %q:\n%s", c.args, code, c.code, c.want, out)
		}
	}
	for _, model := range []string{"", "sample-model"} {
		args := []string{"--self-test", "config", "/srv/sample/trial", "/srv/sample/proof", "3"}
		if model != "" {
			args = append(args, model)
		}
		out, code := k.run(t, nil, args...)
		var cfg struct {
			StateRoot   string `json:"state_root"`
			RuntimeRoot string `json:"runtime_root"`
			MaxRunning  int    `json:"max_running"`
			Sandbox     struct {
				NeverMounted []string `json:"never_mounted"`
			} `json:"sandbox"`
			Leads []struct {
				Key    string `json:"key"`
				Inject bool   `json:"inject"`
				Agent  struct {
					Kind string   `json:"kind"`
					Argv []string `json:"argv"`
				} `json:"agent"`
			} `json:"leads"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &cfg) != nil {
			t.Fatalf("--self-test config %q: exit %d:\n%s", model, code, out)
		}
		if cfg.StateRoot != "/srv/sample/trial" || cfg.RuntimeRoot != "/srv/sample/proof/runtime" || cfg.MaxRunning != 1 ||
			!slices.Contains(cfg.Sandbox.NeverMounted, "/srv/sample/trial/owner") || !slices.Contains(cfg.Sandbox.NeverMounted, root(t)) ||
			!slices.Contains(cfg.Sandbox.NeverMounted, "/srv/sample/proof/owner-settings") {
			t.Errorf("the config: %+v", cfg)
		}
		for _, p := range []string{".ssh", ".claude", ".codex"} {
			if !slices.Contains(cfg.Sandbox.NeverMounted, filepath.Join(k.home, p)) {
				t.Errorf("the config does not keep ~/%s unmounted: %v", p, cfg.Sandbox.NeverMounted)
			}
		}
		want := 1
		if model != "" {
			want = 2
		}
		if len(cfg.Leads) != want || cfg.Leads[0].Key != "standin-3" || cfg.Leads[0].Agent.Kind != "stand-in" || !cfg.Leads[0].Inject {
			t.Fatalf("the leads: %+v", cfg.Leads)
		}
		if model != "" && (cfg.Leads[1].Key != "live-3" || cfg.Leads[1].Agent.Kind != "live" || !slices.Equal(cfg.Leads[1].Agent.Argv, []string{"/ataniq/agent/claude", "--model", model})) {
			t.Errorf("the live lead: %+v", cfg.Leads[1])
		}
	}
	// A HOME that cannot give the never-mounted paths is refused.
	for _, home := range []string{"", "relative", "/a b", "/"} {
		cmd := exec.Command("/bin/sh", filepath.Join(root(t), scratchSeatPrepare), "--self-test", "config", "/srv/sample/trial", "/srv/sample/proof", "3")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "HOME") {
			t.Errorf("HOME %q: %v:\n%s", home, err, out)
		}
	}
	out, code = k.run(t, nil, "--self-test", "settings", "/srv/sample/proof")
	var sc map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &sc) != nil || sc["door"] != "stand-in" || sc["trial"] != nil ||
		sc["sandbox"] != "/srv/sample/proof/owner-channel/no-template.json" {
		t.Fatalf("--self-test settings: exit %d:\n%s", code, out)
	}
	for _, live := range []bool{false, true} {
		args := []string{"--self-test", "commands", "/srv/sample/trial", "/srv/sample/proof", "192.0.2.10:7443", "3"}
		if live {
			args = append(args, "live")
		}
		out, code = k.run(t, []string{"TRIAL_UNIT=sample-trial.service"}, args...)
		if code != 0 {
			t.Fatalf("--self-test commands: exit %d:\n%s", code, out)
		}
		scratchSeatCommandsHold(t, out, "/srv/sample/trial", "/srv/sample/proof", live)
	}
	if after := scratchSeatFiles(t, k.dir); !sameScratchSeatFiles(before, after) {
		t.Error("a self-test changed the folder")
	}
	k.noCalls(t)
}

// scratchSeatCommandsHold checks the owner's steps the script prints: every
// step is there in order; every step that starts a process, changes a unit
// or defines a lead says OWNER ONLY; nothing is deleted, moved, masked or
// swapped; the trial's unit is the quoted reference "$TRIAL_UNIT", never its
// value, and no line holds a placeholder; the daemon's check passes and the
// trial's digests are recorded before the trial is stopped, the daemon reads
// the settings folder, and the digests are compared before the trial starts
// again by the owner's own command; the daemon's exit status is printed; the
// live lead waits for the drop rule; the commands of the leads name only the
// leads configured, by their keys.
func scratchSeatCommandsHold(t *testing.T, out, state, root string, live bool) {
	t.Helper()
	i := strings.Index(out, "Your steps, in order")
	if i < 0 {
		t.Fatalf("no steps:\n%s", out)
	}
	out = out[i:]
	for n := 1; n <= 14; n++ {
		if (n >= 7 && n <= 10) && !live {
			continue
		}
		if !strings.Contains(out, fmt.Sprintf("%2d. ", n)) && !strings.Contains(out, fmt.Sprintf(" %d. ", n)) {
			t.Errorf("the steps lack %d:\n%s", n, out)
		}
	}
	prep := " --state " + state + " --root " + root + " --gate 192.0.2.10:7443"
	for _, want := range []string{
		"A for the daemon, B for the console, C for the desk",
		"Nothing of the trial is renamed, moved or masked: the proof's daemon reads its settings from " + root + "/owner-settings.",
		"OWNER ONLY (starts pasta, bubblewrap, curl and a throwaway user unit): { sh " + root + "/netcheck.sh; echo \"netcheck: exit status $?\"; } 2>&1 | tee -a " + root + "/evidence/netcheck.txt",
		"scratch-init --config " + root + "/scratch.json",
		"--status" + prep + "   (the daemon's check: it must end \"check: no refusal\")",
		"(cd " + state + "/owner && sha256sum -- gate/key.pem gate/cert.pem gate/seat-pins keys.log keys.head scratch.json) >" + root + "/evidence/trial-before.sha256",
		"cp -- " + state + "/owner/keys.log " + root + "/keys-before.log   (a private copy, not evidence: step 13 shows what was appended to it)",
		"--status" + prep + "   (the daemon's check, the same comparison, and the kind of each record appended to keys.log since g)",
		"OWNER ONLY: systemctl --user stop -- \"$TRIAL_UNIT\"",
		"(inactive; nothing is masked)",
		"sh -c 'trap : INT; \"$@\" 2>&1; echo \"ataniqd: exit status $?\"' sh ",
		" run --scratch " + root + "/scratch.json --owner-channel --seat-gate 192.0.2.10:7443 --owner-settings " + root + "/owner-settings | tee -ai " + root + "/evidence/daemon.log",
		"ataniq-console --daemon " + root + "/runtime",
		"On the phone and in a desktop browser: the seat's web app, its Leads page.",
		"OWNER ONLY (the daemon runs git to define it): d on standin-3",
		"chat set standin-3 read --config " + root + "/scratch.json",
		"canary-token: proof-standin-0001",
		"submitted: the fence put it in; no paste.",
		"NOT delivered (nothing was written)",
		"Ctrl+\\ then q",
		"no paste; write it in " + root + "/evidence/notes.txt; stop and reconcile",
		"(cd " + state + "/owner && sha256sum -c -- " + root + "/evidence/trial-before.sha256)",
		"OWNER ONLY: start the trial again with your own command (systemctl --user start -- \"$TRIAL_UNIT\" for a unit file, or your systemd-run)",
		"journalctl --user --unit \"$TRIAL_UNIT\" -I",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the steps lack %q:\n%s", want, out)
		}
	}
	if live != strings.Contains(out, "chat set live-3 read") || live != strings.Contains(out, "claude --version") ||
		live != strings.Contains(out, "d on live-3") || live != strings.Contains(out, "systemctl --user status -- ataniq-lead-scratch-live-3.service") ||
		live != strings.Contains(out, "The drop rule (a separate change, SB-1) is in place") {
		t.Errorf("the live lead's steps, live %v:\n%s", live, out)
	}
	if live && !strings.Contains(out, "H=$(mktemp -d "+root+"/version-home.XXXXXX) && env -i HOME=\"$H\" "+root+"/agent-install/claude --version") {
		t.Errorf("the version check does not make its home in the root and check it was made:\n%s", out)
	}
	if live && (!strings.Contains(out, "submitted: w, Enter, approve only the write of the one reply into its outbox") ||
		!strings.Contains(out, "stay for the approval of that one write") || !strings.Contains(out, "cannot-identify")) {
		t.Errorf("the live lead's approval, paste and revocation steps:\n%s", out)
	}
	if live && !strings.Contains(out, "chat set live-3 off") || !live && !strings.Contains(out, "chat set standin-3 off") {
		t.Errorf("the off step, live %v:\n%s", live, out)
	}
	order := []string{"scratch-init", "--status", "sha256sum --", "/keys-before.log", "systemctl --user stop", "--owner-settings " + root + "/owner-settings |",
		"d on standin-3", "sha256sum -c", "appended to keys.log since g", "start the trial again"}
	if live {
		order = append(order[:7], append([]string{"The drop rule", "d on live-3"}, order[7:]...)...)
	}
	for i := 1; i < len(order); i++ {
		if strings.Index(out, order[i-1]) > strings.Index(out, order[i]) {
			t.Errorf("%q comes after %q:\n%s", order[i-1], order[i], out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " rm ") || strings.Contains(line, "rm -") || strings.Contains(line, " mv ") || strings.Contains(line, "unlink") ||
			strings.Contains(line, "swap") || strings.Contains(line, "mask --") || strings.Contains(line, "record-trial") || strings.Contains(line, "trial-restart") {
			t.Errorf("a step deletes, moves, masks or swaps: %q", line)
		}
		if strings.Contains(line, "<") && strings.Contains(line, ">") && strings.Index(line, "<") < strings.LastIndex(line, ">") && !strings.Contains(line, "2>&1") {
			t.Errorf("a step holds a placeholder: %q", line)
		}
		if strings.Contains(line, "KEY") {
			t.Errorf("a step names a lead KEY: %q", line)
		}
		for _, starts := range []string{"systemctl --user stop", "systemctl --user start", "run --scratch", "ataniq-console", "--version", "d on ", "s on ", "netcheck.sh"} {
			if strings.Contains(line, starts) && !strings.Contains(line, "OWNER ONLY") {
				t.Errorf("a step starts a process, changes a unit or defines a lead and is not marked OWNER ONLY: %q", line)
			}
		}
	}
}

// --dry says what --write would make and prints the owner's steps from the
// values alone: it works on a state folder and a root that do not exist, so
// nothing of the owner's is read; it writes nothing and runs nothing.
func TestScratchSeatPrepareDryWritesNothing(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	before := scratchSeatFiles(t, k.dir)
	for _, live := range []bool{false, true} {
		out, code := k.run(t, nil, k.args("--dry", r, live)...)
		if code != 0 {
			t.Fatalf("--dry: exit %d:\n%s", code, out)
		}
		for _, want := range []string{
			"[dry]", r + "/scratch.json (0600)", "state_root " + k.state + " (the trial's state folder)", "runtime_root " + r + "/runtime",
			"max_running 1", "lead standin-3", r + "/owner-settings/scratch.json (0600, in a folder of 0700)", "--owner-settings " + r + "/owner-settings",
			"owner/scratch.json of the state folder is never read, moved or changed", "\"" + k.state + "/owner\"", "\"" + k.home + "/.claude\"",
			"it guards nothing", r + "/netcheck.sh (0700)", "--write refuses, before it makes anything",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the dry run does not say %q:\n%s", want, out)
			}
		}
		if live != strings.Contains(out, "lead live-3: live claude 2.1.325, argv [\"/ataniq/agent/claude\", \"--model\", \"sample-model\"]") {
			t.Errorf("the dry run's live lead, live %v:\n%s", live, out)
		}
		scratchSeatCommandsHold(t, out, k.state, r, live)
	}
	// From the values alone: a state folder, a root's parent and binaries
	// that are not there at all.
	gone := filepath.Join(k.dir, "not-there")
	out, code := k.run(t, nil, "--dry", "--state", gone+"/state", "--root", gone+"/a/proof", "--gate", "192.0.2.10:7443",
		"--sitting", "3", "--bin", gone+"/bin", "--daemon", gone+"/ataniqd", "--claude", gone+"/claude", "--model", "m")
	if code != 0 || !strings.Contains(out, "state_root "+gone+"/state") || !strings.Contains(out, "pasta: /usr/bin/pasta when it is") ||
		!strings.Contains(out, "bubblewrap: the bwrap on PATH at --write") {
		t.Errorf("--dry with nothing there: exit %d:\n%s", code, out)
	}
	// The values from the environment, as the guide sets them.
	out, code = k.run(t, []string{"OWNER_STATE_DIR=" + k.state, "ROOT=" + r, "SEAT_GATE=192.0.2.10:7443", "SITTING=3"},
		"--dry", "--bin", k.bin, "--daemon", filepath.Join(k.bin, "ataniqd"), "--pasta", filepath.Join(k.bin, "pasta"))
	if code != 0 || !strings.Contains(out, "state_root "+k.state) || !strings.Contains(out, "--seat-gate 192.0.2.10:7443") || !strings.Contains(out, "\"$TRIAL_UNIT\"") {
		t.Errorf("--dry from the environment: exit %d:\n%s", code, out)
	}
	if after := scratchSeatFiles(t, k.dir); !sameScratchSeatFiles(before, after) {
		t.Error("the dry run changed the folder")
	}
	k.noCalls(t)
}

// What the script refuses from the values alone, in --dry and --write
// alike, before anything is printed or made: no mode, a missing value, a
// sitting that is not 1 to 999, a root in /tmp, inside the state folder or
// the checkout, a claude without a model, a gate that is not one IPv4
// address and port, a HOME that cannot give the never-mounted paths, and the
// trial's unit or a mode of the retired settings swap, which the script no
// longer takes.
func TestScratchSeatPrepareRefusesValues(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	set := func(args []string, flag, value string) []string {
		out := slices.Clone(args)
		i := slices.Index(out, flag)
		out[i+1] = value
		return out
	}
	base := k.args("--dry", r, false)
	cases := []struct {
		name string
		args []string
		env  []string
		code int
		want string
	}{
		{"no mode", base[1:], nil, 2, "usage"},
		{"two modes", append([]string{"--write"}, base...), nil, 2, "usage"},
		{"no state", set(base, "--state", ""), nil, 1, "no state folder"},
		{"a relative state", set(base, "--state", "trial"), nil, 1, "not an absolute path"},
		{"no root", set(base, "--root", ""), nil, 1, "no root"},
		{"a root in /tmp", set(base, "--root", "/tmp/proof"), nil, 1, "/tmp"},
		{"a root in the state folder", set(base, "--root", k.state+"/proof"), nil, 1, "the state folder lie one in the other"},
		{"a root in the checkout", set(base, "--root", root(t)+"/proof"), nil, 1, "the checkout lie one in the other"},
		{"a wildcard gate", set(base, "--gate", "0.0.0.0:7443"), nil, 1, "wildcard"},
		{"an IPv6 gate", set(base, "--gate", "[2001:db8::10]:7443"), nil, 1, "IPv4 gate address only"},
		{"a host name", set(base, "--gate", "gate.example:7443"), nil, 1, "not an IPv4 address"},
		{"no sitting", set(base, "--sitting", ""), nil, 1, "no sitting"},
		{"a sitting 0", set(base, "--sitting", "0"), nil, 1, "sitting"},
		{"a sitting 1000", set(base, "--sitting", "1000"), nil, 1, "sitting"},
		{"a trial unit", append(slices.Clone(base), "--unit", "sample-trial.service"), nil, 2, "usage"},
		{"the swap in", append([]string{"--swap-in"}, base[1:]...), nil, 2, "usage"},
		{"the swap out", append([]string{"--swap-out"}, base[1:]...), nil, 2, "usage"},
		{"the trial's record", append([]string{"--record-trial"}, base[1:]...), nil, 2, "usage"},
		{"claude without a model", append(slices.Clone(base), "--claude", filepath.Join(k.bin, "claude-file")), nil, 1, "--claude needs --model"},
		{"a model without claude", append(slices.Clone(base), "--model", "sample-model"), nil, 1, "--model needs --claude"},
		{"a bad model", append(slices.Clone(base), "--claude", filepath.Join(k.bin, "claude-file"), "--model", "-x"), nil, 1, "--model is a model name"},
		{"a relative HOME", base, []string{"HOME=home"}, 1, "HOME"},
		{"a HOME with a space", base, []string{"HOME=/a b"}, 1, "HOME"},
	}
	before := scratchSeatFiles(t, k.dir)
	for _, mode := range []string{"--dry", "--write"} {
		for _, c := range cases {
			args := slices.Clone(c.args)
			if args[0] == "--dry" {
				args[0] = mode
			}
			out, code := k.run(t, c.env, args...)
			if code != c.code || !strings.Contains(out, c.want) {
				t.Errorf("%s %s: exit %d, want %d and %q:\n%s", mode, c.name, code, c.code, c.want, out)
			}
			if c.code == 1 && strings.Count(strings.TrimSpace(out), "\n") != 0 {
				t.Errorf("%s %s printed more than the refusal:\n%s", mode, c.name, out)
			}
		}
	}
	if after := scratchSeatFiles(t, k.dir); !sameScratchSeatFiles(before, after) {
		t.Error("a refused run changed the folder")
	}
	k.noCalls(t)
}

// What --write refuses after it has looked, before it makes anything: a state
// folder that is a link or has no owner/ folder, one with anything of this
// sitting's leads, a missing tool, and a root whose parent is missing or
// reached through a link, that is linked into the state folder, that cannot
// be read or listed, or that holds something. What only the daemon judges
// (the gate's material, the settings) is left to the daemon's check, which
// --status runs.
func TestScratchSeatPrepareWriteRefuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("modes do not bind root")
	}
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	full := filepath.Join(k.dir, "full")
	if err := os.MkdirAll(filepath.Join(full, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(k.dir, filepath.Join(k.dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(k.state, filepath.Join(k.dir, "into-state")); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(k.dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o200); err != nil {
		t.Fatal(err)
	}
	set := func(args []string, flag, value string) []string {
		out := slices.Clone(args)
		out[slices.Index(out, flag)+1] = value
		return out
	}
	base := k.args("--write", r, false)
	// change edits the state folder for one case and returns the undo.
	write := func(rel, body string, mode os.FileMode) func() {
		p := filepath.Join(k.state, rel)
		old, err := os.ReadFile(p)
		had := err == nil
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
		return func() {
			if had {
				os.WriteFile(p, old, 0o600)
				os.Chmod(p, 0o600)
			} else {
				os.Remove(p)
			}
		}
	}
	mkdir := func(rel string, mode os.FileMode) func() {
		p := filepath.Join(k.state, rel)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
		return func() { os.Chmod(p, 0o700); os.RemoveAll(filepath.Join(k.state, strings.Split(rel, "/")[0])) }
	}
	cases := []struct {
		name  string
		args  []string
		setup func() func()
		env   []string
		want  string
	}{
		{"no owner folder", base, func() func() {
			p := filepath.Join(k.state, "owner")
			os.Rename(p, p+".x")
			return func() { os.Rename(p+".x", p) }
		}, nil, "has no owner/ folder"},
		{"a state folder through a link", set(base, "--state", filepath.Join(k.dir, "linked", "trial-state")), nil, nil, "not at its physical path"},
		{"an earlier lead of the sitting", base, func() func() { return mkdir("leads/scratch-standin-3-lead", 0o700) }, nil, "earlier sitting"},
		{"an earlier role in the registry", base, func() func() {
			undo := mkdir("registry", 0o700)
			write("registry/registry.jsonl", "{\"role\":\"scratch-live-3/lead\"}\n", 0o600)
			return undo
		}, nil, "registry"},
		{"a folder that cannot be listed", base, func() func() { return mkdir("leads", 0o300) }, nil, "cannot list"},
		{"no daemon", set(base, "--daemon", filepath.Join(k.dir, "missing")), nil, nil, "no daemon of both tags"},
		{"no bubblewrap", set(base, "--bwrap", filepath.Join(k.dir, "missing")), nil, nil, "bubblewrap is not an executable"},
		{"a missing parent", set(base, "--root", filepath.Join(k.dir, "no-parent", "proof")), nil, nil, "parent folder does not exist"},
		{"a parent through a link", set(base, "--root", filepath.Join(k.dir, "linked", "proof")), nil, nil, "physical path"},
		{"a root linked into the state folder", set(base, "--root", filepath.Join(k.dir, "into-state", "proof")), nil, nil, "physical path"},
		{"a root in TMPDIR", base, nil, []string{"TMPDIR=" + k.dir}, "physically in or around"},
		{"a root that cannot be read", set(base, "--root", locked), nil, nil, "cannot be read"},
		{"a root that holds something", set(base, "--root", full), nil, nil, "not empty"},
		{"a HOME through a link", base, nil, []string{"HOME=" + filepath.Join(k.dir, "linked", "home")}, "physical path"},
		{"a HOME that is not there", base, nil, []string{"HOME=" + filepath.Join(k.dir, "no-home")}, "HOME is not a folder"},
	}
	before := scratchSeatFiles(t, k.state)
	for _, c := range cases {
		undo := func() {}
		if c.setup != nil {
			undo = c.setup()
		}
		out, code := k.run(t, c.env, c.args...)
		undo()
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, want 1 and %q:\n%s", c.name, code, c.want, out)
		}
		if strings.Contains(out, "making ") {
			t.Errorf("%s: a root was made:\n%s", c.name, out)
		}
		if _, err := os.Lstat(filepath.Join(k.dir, "no-parent")); err == nil {
			t.Fatalf("%s: a missing parent was made", c.name)
		}
	}
	if _, err := os.Lstat(r); err == nil {
		t.Error("a refused --write made the root")
	}
	if after := scratchSeatFiles(t, k.state); !sameScratchSeatFiles(before, after) {
		t.Error("a refused run changed the state folder")
	}
	k.noCalls(t)
}

// --write makes the root and writes in it only: the config and the settings
// of mode 0600 (valid JSON), the copies, the conf files, the throwaway
// repository, the network check and the folders, all private. The trial's
// state folder is the same bytes and modes after it; no stub ran (claude is
// copied, not run). Git runs in an environment of its own: the caller's git
// variables reach no repository and run no hook.
func TestScratchSeatPrepareWriteTouchesOnlyTheRoot(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	// A hostile git environment: another repository, an index, a work tree,
	// a hooks folder whose hooks record that they ran, and a global config.
	other := filepath.Join(k.dir, "other-repo")
	hooks := filepath.Join(k.dir, "hooks")
	ran := filepath.Join(k.dir, "hook-ran")
	for _, d := range []string{other, hooks} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{"pre-commit", "post-commit", "commit-msg"} {
		if err := os.WriteFile(filepath.Join(hooks, h), []byte("#!/bin/sh\necho "+h+" >>'"+ran+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gitconfig := filepath.Join(k.home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[core]\n\thooksPath = "+hooks+"\n[commit]\n\tgpgSign = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostile := []string{"GIT_DIR=" + other + "/.git", "GIT_WORK_TREE=" + other, "GIT_INDEX_FILE=" + other + "/index",
		"GIT_CONFIG_PARAMETERS='core.hooksPath'='" + hooks + "'", "GIT_CONFIG_GLOBAL=" + gitconfig, "GIT_OBJECT_DIRECTORY=" + other + "/objects"}
	beforeState := scratchSeatFiles(t, k.state)
	beforeOther := scratchSeatFiles(t, other)
	out, code := k.run(t, hostile, k.args("--write", r, true)...)
	if code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "nothing was started, and nothing in "+k.state+" was changed") {
		t.Errorf("--write does not say what it did not do:\n%s", out)
	}
	scratchSeatCommandsHold(t, out, k.state, r, true)
	if after := scratchSeatFiles(t, k.state); !sameScratchSeatFiles(beforeState, after) {
		t.Fatal("--write changed the trial's state folder")
	}
	if after := scratchSeatFiles(t, other); !sameScratchSeatFiles(beforeOther, after) {
		t.Fatal("--write changed the repository the caller's GIT_DIR names")
	}
	if b, err := os.ReadFile(ran); err == nil {
		t.Fatalf("a hook of the caller's ran: %s", b)
	}
	k.noCalls(t)
	for rel, mode := range map[string]os.FileMode{
		".": 0o700 | fs.ModeDir, "scratch.json": 0o600, "owner-settings": 0o700 | fs.ModeDir, "owner-settings/scratch.json": 0o600,
		"evidence":      0o700 | fs.ModeDir,
		"owner-channel": 0o700 | fs.ModeDir, "owner-channel/drop": 0o700 | fs.ModeDir, "conf/managed-settings.json": 0o600,
		"conf/guard-hook": 0o700, "agent-install/ataniq-canary-standin": 0o700, "agent-install/claude": 0o700,
		"bin/ataniq-probe": 0o700, "upstream/README.txt": 0o600, "conf/replay-screens/1.txt": 0o600, "netcheck.sh": 0o700,
	} {
		st, err := os.Lstat(filepath.Join(r, rel))
		if err != nil || st.Mode() != mode {
			t.Errorf("%s: %v, %v, want %v", rel, st, err, mode)
		}
	}
	for _, rel := range []string{"runtime", "owner-channel/work", "owner-channel/no-template.json", "owner-channel/mirror.git", "evidence/trial-before.sha256", "owner-scratch.json"} {
		if _, err := os.Lstat(filepath.Join(r, rel)); err == nil {
			t.Errorf("--write made %s, which is the daemon's or the owner's to make, or is never made", rel)
		}
	}
	if names, err := os.ReadDir(filepath.Join(r, "owner-settings")); err != nil || len(names) != 1 {
		t.Errorf("the settings folder holds more than the settings: %v %v", names, err)
	}
	for _, rel := range []string{"scratch.json", "owner-settings/scratch.json"} {
		var v map[string]any
		if err := json.Unmarshal(mustRead(t, filepath.Join(r, rel)), &v); err != nil {
			t.Errorf("%s is not JSON: %v", rel, err)
		}
	}
	if got := mustRead(t, filepath.Join(r, "agent-install", "claude")); !bytes.Equal(got, mustRead(t, filepath.Join(k.bin, "claude-file"))) {
		t.Error("the claude copy is not the file given")
	}
	// The network check parses, names the gate and is never run.
	nc := string(mustRead(t, filepath.Join(r, "netcheck.sh")))
	if out, err := exec.Command("/bin/sh", "-n", filepath.Join(r, "netcheck.sh")).CombinedOutput(); err != nil {
		t.Errorf("netcheck.sh does not parse: %v\n%s", err, out)
	}
	for _, want := range []string{"GATE_HOST='192.0.2.10'", "GATE_PORT='7443'", "BWRAP='" + filepath.Join(k.tools, "bwrap") + "'", "it guards nothing",
		"# Written by prepare.sh --write. Run it by hand while a daemon serves the gate: the trial at step e, the proof's daemon at step 7"} {
		if !strings.Contains(nc, want) {
			t.Errorf("netcheck.sh lacks %q", want)
		}
	}
	// Its header and its template say when it runs as step 7 does: with the
	// trial stopped and the proof's daemon serving the gate, not only while
	// the trial runs (review finding 6).
	if strings.Contains(nc, "with the trial running") || strings.Contains(nc, "with the phone trial running") || strings.Contains(nc, "while the trial runs") {
		t.Error("netcheck.sh says it runs only while the trial runs, which step 7 contradicts")
	}
	// The repository has one commit on develop, by the fixed author.
	cmd := exec.Command("/usr/bin/git", "-C", filepath.Join(r, "upstream"), "log", "--format=%an <%ae>", "refs/heads/develop")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + k.home}
	if b, err := cmd.Output(); err != nil || strings.TrimSpace(string(b)) != "Sample Author <author@example.invalid>" {
		t.Errorf("the throwaway repository: %q %v", b, err)
	}
	// A second --write into the same root is refused: it is not empty now.
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 1 || !strings.Contains(out, "not empty") {
		t.Errorf("a second --write: exit %d:\n%s", code, out)
	}
}

// --status runs the daemon's own check on the files --write made, with the
// flags the daemon is started with and --check, and passes its lines and its
// exit status through; it then compares the trial's gate key, certificate,
// pins, key list and settings with the digests the owner recorded at step g
// (sha256sum -c), says when none are recorded yet, and names a file that
// changed. It writes nothing anywhere and runs nothing but the daemon's
// check, here a stub that records its arguments.
func TestScratchSeatPrepareStatusRunsTheDaemonsCheck(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	args := filepath.Join(k.dir, "daemon-args")
	daemon := filepath.Join(k.dir, "check-daemon")
	stub := func(code int) {
		body := "#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done >'" + args + "'\necho 'check: from the stub'\nexit " + strconv.Itoa(code) + "\n"
		if err := os.WriteFile(daemon, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	status := []string{"--status", "--state", k.state, "--root", r, "--gate", "192.0.2.10:7443", "--daemon", daemon}
	beforeState, beforeRoot := scratchSeatFiles(t, k.state), scratchSeatFiles(t, r)

	stub(0)
	out, code := k.run(t, nil, status...)
	if code != 0 || !strings.Contains(out, "check: from the stub\n") || !strings.Contains(out, "status: the daemon's check found no refusal") ||
		!strings.Contains(out, "status: no "+r+"/evidence/trial-before.sha256 yet") {
		t.Fatalf("--status: exit %d:\n%s", code, out)
	}
	want := []string{"run", "--scratch", r + "/scratch.json", "--owner-channel", "--seat-gate", "192.0.2.10:7443", "--owner-settings", r + "/owner-settings", "--check"}
	if got := strings.Split(strings.TrimSpace(string(mustRead(t, args))), "\n"); !slices.Equal(got, want) {
		t.Fatalf("the daemon's check was run with %q, want %q", got, want)
	}
	stub(10)
	if out, code := k.run(t, nil, status...); code != 10 || !strings.Contains(out, "ended with exit status 10: the daemon would not start") {
		t.Fatalf("--status with a refusal: exit %d:\n%s", code, out)
	}

	// The digests the owner records at step g, then a file of the trial that
	// changes, and one that comes back.
	record := exec.Command("/bin/sh", "-c", "cd \"$1\" && sha256sum -- gate/key.pem gate/cert.pem gate/seat-pins keys.log keys.head scratch.json >\"$2\"",
		"sh", filepath.Join(k.state, "owner"), filepath.Join(r, "evidence", "trial-before.sha256"))
	if b, err := record.CombinedOutput(); err != nil {
		t.Fatalf("recording the digests: %v %s", err, b)
	}
	stub(0)
	out, code = k.run(t, nil, status...)
	if code != 0 || !strings.Contains(out, "status:   gate/key.pem: OK") ||
		!strings.Contains(out, "status: the trial's gate key, certificate, pins, key list and settings are the same bytes as recorded in "+r+"/evidence/trial-before.sha256") {
		t.Fatalf("--status with the same files: exit %d:\n%s", code, out)
	}
	pins := filepath.Join(k.state, "owner", "gate", "seat-pins")
	old := mustRead(t, pins)
	if err := os.WriteFile(pins, append(old, []byte(strings.Repeat("cd", 32)+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code = k.run(t, nil, status...)
	if code != 1 || !strings.Contains(out, "status:   gate/seat-pins: FAILED") || !strings.Contains(out, "status: CHANGED: a file of the trial other than the key list is not the bytes recorded") {
		t.Fatalf("--status with a changed pin file: exit %d:\n%s", code, out)
	}
	stub(10)
	if out, code := k.run(t, nil, status...); code != 10 || !strings.Contains(out, "status: CHANGED") {
		t.Fatalf("--status with a refusal and a changed file: exit %d:\n%s", code, out)
	}
	if err := os.WriteFile(pins, old, 0o600); err != nil {
		t.Fatal(err)
	}

	// The key list may only grow (review finding 4). With the private copy
	// of step g, --status checks that the copy is a byte prefix of the list
	// and names the kind of each record appended: counter records alone are
	// a phone login and pass; a pairing, a revocation, a refused pairing or
	// a line that is not a record is CHANGED; so is a list whose old bytes
	// changed, or a list that changed with no copy, or a copy that is not
	// the file of step g. A head that changed while the list did not is the
	// head catching up, and passes.
	stub(0)
	keys, head := filepath.Join(k.state, "owner", "keys.log"), filepath.Join(k.state, "owner", "keys.head")
	oldKeys, oldHead := mustRead(t, keys), mustRead(t, head)
	copyKeys := filepath.Join(r, "keys-before.log")
	hex := strings.Repeat("0123456789abcdef", 4)
	rec := func(seq int, typ, more string) string {
		return hex + ` {"at":"2026-11-20T14:00:00Z",` + more + `"id":"585cf66cd7e1ac88","seq":` + strconv.Itoa(seq) + `,"type":"` + typ + `"}` + "\n"
	}
	set := func(keysBody, headBody string) {
		t.Helper()
		if err := os.WriteFile(keys, []byte(keysBody), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(head, []byte(headBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status4 := func(name string, wantCode int, wants ...string) {
		t.Helper()
		out, code := k.run(t, nil, status...)
		if code != wantCode {
			t.Errorf("%s: exit %d, want %d:\n%s", name, code, wantCode, out)
		}
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s does not say %q:\n%s", name, w, out)
			}
		}
	}
	set(string(oldKeys)+rec(2, "counter", `"counter":5,`), "sample head 2\n")
	status4("a counter record without the copy", 1, "status:   keys.log: FAILED", "status: CHANGED: the key list or its head is not the bytes recorded, and there is no copy "+copyKeys+" of step g")
	if err := os.WriteFile(copyKeys, []byte("not the list of step g\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status4("a copy that is not the list of step g", 1, "status: CHANGED: the key list is not the bytes recorded, and "+copyKeys+" is not the keys.log of step g")
	if err := os.WriteFile(copyKeys, oldKeys, 0o600); err != nil {
		t.Fatal(err)
	}
	status4("a counter record", 0, "status:   keys.log is the list of step g with 1 record(s) appended: counter",
		"status:   only counter records were appended: a phone login with a passkey whose sign counter rises",
		"status: the trial's gate key, certificate, pins and settings are the same bytes as recorded in "+r+"/evidence/trial-before.sha256, and the key list holds the records of step g unchanged")
	set(string(oldKeys)+rec(2, "counter", `"counter":5,`)+rec(3, "counter", `"counter":6,`), "sample head 3\n")
	status4("two counter records", 0, "with 2 record(s) appended: counter counter", "only counter records were appended")
	for _, c := range []struct{ name, body, appended string }{
		{"a pairing", rec(2, "counter", `"counter":5,`) + rec(3, "added", `"credential_id":"Y2Nj","kind":"platform-passkey","public_key":"aw==",`), "2 record(s) appended: counter added"},
		{"a revocation", rec(2, "revoked", ""), "1 record(s) appended: revoked"},
		{"a refused pairing", rec(2, "rejected", ""), "1 record(s) appended: rejected"},
		{"a line that is not a record", "not a record\n", "1 record(s) appended: unreadable"},
		{"a record cut short", strings.TrimSuffix(rec(2, "counter", `"counter":5,`), "\n"), "1 record(s) appended: unreadable"},
	} {
		set(string(oldKeys)+c.body, "sample head 2\n")
		status4(c.name, 1, "status:   keys.log is the list of step g with "+c.appended, "status: CHANGED: a record that is not a counter was appended to the key list")
	}
	changed := append([]byte("S"), oldKeys[1:]...)
	set(string(changed)+rec(2, "counter", `"counter":5,`), "sample head 2\n")
	status4("a list whose old bytes changed", 1, "status: CHANGED: keys.log is not the list of step g with records appended: its copy "+copyKeys+" is not a byte prefix of it")
	set(string(oldKeys[:len(oldKeys)-1]), string(oldHead))
	status4("a list cut short", 1, "is not a byte prefix of it")
	set(string(oldKeys), "sample head caught up\n")
	status4("a head that caught up", 0, "status:   keys.head: FAILED", "status:   keys.log is the same bytes as at step g; only keys.head changed: the head caught up with the list",
		"and the key list holds the records of step g unchanged")
	set(string(oldKeys)+rec(2, "counter", `"counter":5,`), "sample head 2\n")
	if err := os.WriteFile(pins, append(old, []byte(strings.Repeat("cd", 32)+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	status4("a counter record and a changed pin file", 1, "status:   gate/seat-pins: FAILED",
		"status: CHANGED: a file of the trial other than the key list is not the bytes recorded", "only counter records were appended")
	if err := os.WriteFile(pins, old, 0o600); err != nil {
		t.Fatal(err)
	}
	set(string(oldKeys), string(oldHead))
	os.Remove(copyKeys)
	os.Remove(args)
	os.Remove(filepath.Join(r, "evidence", "trial-before.sha256"))
	if !sameScratchSeatFiles(beforeState, scratchSeatFiles(t, k.state)) || !sameScratchSeatFiles(beforeRoot, scratchSeatFiles(t, r)) {
		t.Error("--status changed the state folder or the root")
	}
	k.noCalls(t)

	// Without the config, or without the daemon, nothing is run.
	if out, code := k.run(t, nil, "--status", "--state", k.state, "--root", filepath.Join(k.dir, "other"), "--gate", "192.0.2.10:7443", "--daemon", daemon); code != 1 ||
		!strings.Contains(out, "run --write first") {
		t.Errorf("--status without a config: exit %d:\n%s", code, out)
	}
	if out, code := k.run(t, nil, "--status", "--state", k.state, "--root", r, "--gate", "192.0.2.10:7443", "--daemon", filepath.Join(k.dir, "missing")); code != 1 ||
		!strings.Contains(out, "no daemon of both tags") {
		t.Errorf("--status without a daemon: exit %d:\n%s", code, out)
	}
	if _, err := os.Lstat(args); err == nil {
		t.Error("a refused --status ran the daemon")
	}
}

// Every shell block of the runbook parses as sh and holds no placeholder in
// angle brackets; the block of the values is assignments only; the trial's
// unit is always the quoted reference "$TRIAL_UNIT"; the runbook marks both
// defines and every start and stop OWNER ONLY; the daemon reads the settings
// folder; the live lead waits for the drop rule; the version check makes its
// home in the root and runs claude only when it was made; the network
// check's exit status is kept; and nothing of the retired swap is left: no
// step masks, records, renames or swaps the trial.
func TestScratchSeatProofRunbookShellIsSafe(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), "docs", "scratch-seat-proof.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	var blocks []string
	for rest := text; ; {
		i := strings.Index(rest, "```sh\n")
		if i < 0 {
			break
		}
		rest = rest[i+len("```sh\n"):]
		j := strings.Index(rest, "```")
		if j < 0 {
			t.Fatal("an sh block does not end")
		}
		blocks = append(blocks, rest[:j])
		rest = rest[j+3:]
	}
	if len(blocks) < 10 {
		t.Fatalf("the runbook has %d sh blocks", len(blocks))
	}
	placeholder := regexp.MustCompile(`<[A-Za-z_:./ -]+>`)
	for _, b := range blocks {
		lines := strings.Split(strings.TrimSpace(b), "\n")
		body := strings.Join(lines, "\n")
		cmd := exec.Command("/bin/sh", "-n", "-c", body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("an sh block does not parse: %v %s\n%s", err, out, body)
		}
		if placeholder.MatchString(body) {
			t.Errorf("an sh block holds a placeholder:\n%s", body)
		}
		for _, l := range lines {
			if strings.Contains(l, "TRIAL_UNIT") && (strings.Contains(l, "systemctl") || strings.Contains(l, "journalctl")) && !strings.Contains(l, `"$TRIAL_UNIT"`) {
				t.Errorf("the unit is not quoted: %q", l)
			}
		}
	}
	values := blocks[0]
	for _, l := range strings.Split(strings.TrimSpace(values), "\n") {
		if !regexp.MustCompile(`^(export [A-Z_ ]+|[A-Z_]+=('[^'<>]*'|\$[A-Z_]+[^ ]*|[A-Za-z0-9_./-]+)( +[A-Z_]+=[^ <>]+)*)( +#.*)?$`).MatchString(l) {
			t.Errorf("the block of the values holds more than assignments: %q", l)
		}
	}
	flat := strings.Join(strings.Fields(text), " ")
	for _, want := range []string{
		"**OWNER ONLY (B): define it;", "**OWNER ONLY (B):** `d` on `live-N`", "**OWNER ONLY (C): stop the trial**, by your own command; nothing is masked or moved",
		"**OWNER ONLY (C): start the trial again**, by your own command", "A full quarantine ends the sitting",
		"the state folder's lock refuses whichever daemon comes second (exit 12)", "A transient trial does not come back after a reboot", "never archive the state folder",
		"`holder/final.json.previous`", "`config/claude`", "A fresh `ROOT` alone does not make a fresh Claude login",
		"exit status N", "Approve only the one expected operation", "A project's approvals go through Cards",
		"--owner-settings \"$SETTINGS\" | tee -ai \"$ROOT/evidence/daemon.log\"",
		"The rule comes from a separate change (SB-1): you install it as root at the start of sitting 1",
		"Without it, the sitting has no live lead",
		"H=$(mktemp -d \"$ROOT/version-home.XXXXXX\") && env -i HOME=\"$H\" \"$ROOT/agent-install/claude\" --version",
		"unset CLAUDE_FILE CLAUDE_MODEL",
		"{ sh \"$ROOT/netcheck.sh\"; echo \"netcheck: exit status $?\"; } 2>&1 | tee -a \"$ROOT/evidence/netcheck.txt\"",
		"(cd \"$S/owner\" && sha256sum -c -- \"$ROOT/evidence/trial-before.sha256\")",
		"byte-identical to step g",
		"open the seat's web app (the same seat, on the authority host)",
		"`submitted`: the fence put it in. claude will ask to approve the write of its reply. `w` attaches as the writer",
		"Stay attached: claude asks next.",
		"`cannot-identify:`", "`uncertain delivery:`",
		"in the leads view (not attached), `x` and `y` on `live-N`",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the runbook does not say %q", want)
		}
	}
	if i, j := strings.Index(flat, "### The live lead (only once the drop rule is in place)"), strings.Index(flat, "**OWNER ONLY (B):** `d` on `live-N`"); i < 0 || j < i ||
		!strings.Contains(flat[i:j], "7. **The drop rule is in place.**") {
		t.Error("the live lead is defined before the drop rule's step")
	}
	for _, stale := range []string{"chat purge", "plain Yes", "leads/scratch-live-lead/home", "test ! -e", "--swap-in", "--swap-out",
		"--record-trial", "trial-restart.sh", "mask --runtime", "swap-record", "scratch.json.trial", "owner-scratch.json", "env -i HOME=\"$(mktemp -d)\""} {
		if strings.Contains(text, stale) {
			t.Errorf("the runbook still says %q", stale)
		}
	}
}

// AGENTS.md and CLAUDE.md say, next to the canary prepare's rule, that an
// agent runs the scratch seat prepare script only with --dry or --self-test
// and that its other modes and its network check are the owner's.
func TestScratchSeatPrepareAgentRule(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, want := range []string{
			"`qualification/scratch-seat/prepare.sh`", "only with `--dry` or `--self-test`",
			"its `--write` and `--status` are the owner's", "the `ROOT/netcheck.sh` its `--write` writes",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q", name, want)
			}
		}
		if i, j := strings.Index(text, "canary-prepare.sh` only with"), strings.Index(text, "`qualification/scratch-seat/prepare.sh`"); name == "CLAUDE.md" && (i < 0 || j < i) {
			t.Errorf("%s does not put the rule after the canary prepare's", name)
		}
		for _, stale := range []string{"--swap-in", "--swap-out", "--record-trial"} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still names %s", name, stale)
			}
		}
	}
	agents := string(mustRead(t, filepath.Join(root(t), "AGENTS.md")))
	for _, want := range []string{"`sh scripts/go-tool.sh test ./tests/ -run ScratchSeat`",
		"`sh scripts/go-tool.sh test -tags ataniq_ownerchannel,ataniq_canary -run 'ScratchSeat|OwnerSettings|ScratchLeadReach' ./cmd/ataniqd/ ./internal/leaddaemon/`"} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md does not name the targeted check %s", want)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
