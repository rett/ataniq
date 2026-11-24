package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Tests of qualification/scratch-seat/prepare.sh, the preparation of the
// owner-attended proof of real lead replies (docs/scratch-seat-proof.md).
// Nothing here starts a unit, a daemon, a sandbox or an agent: the binaries,
// pasta and claude of these tests are recording stubs, the PATH puts recording
// stubs of systemctl, systemd-run, bwrap and claude first, and the trial's
// state folder is a sample one. --write is run only into a fresh folder, and
// --dry and --self-test only.

const scratchSeatPrepare = "qualification/scratch-seat/prepare.sh"

// scratchSeatKit is the sample world of one run of the script.
type scratchSeatKit struct {
	dir, bin, tools, calls, state, home string
}

// scratchSeatStubs are the programs on PATH that the script must never run.
var scratchSeatStubs = []string{"systemctl", "systemd-run", "bwrap", "pasta", "claude", "ataniqd", "ataniq-console", "curl"}

// newScratchSeatKit makes a folder outside /tmp with the trial's state folder
// as the owner has it (the gate's key and pins, the trial's settings), the
// binaries of build/canary and the daemon as recording stubs, and the tools.
func newScratchSeatKit(t *testing.T) scratchSeatKit {
	t.Helper()
	dir := canaryOffTmp(t)
	k := scratchSeatKit{dir: dir, bin: filepath.Join(dir, "bin"), tools: filepath.Join(dir, "tools"),
		calls: filepath.Join(dir, "calls"), state: filepath.Join(dir, "trial-state"), home: filepath.Join(dir, "home")}
	for _, d := range []string{k.bin, k.tools, k.home, filepath.Join(k.state, "owner", "gate")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"owner/gate/cert.pem": "sample certificate\n", "owner/gate/key.pem": "sample key\n",
		"owner/gate/seat-pins": strings.Repeat("ab", 32) + "\n", "owner/scratch.json": `{"trial": true}` + "\n",
		"owner/keys.log": "sample keys\n",
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
// sample gate and, with live, the claude stub and a model.
func (k scratchSeatKit) args(mode, root string, live bool, more ...string) []string {
	a := []string{mode, "--state", k.state, "--root", root, "--gate", "192.0.2.10:7443",
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

// The script is plain POSIX sh, sets -eu, and names neither build tag nor the
// make target that builds the daemon of both tags: it only names the binary,
// build/scratch-seat/ataniqd. It never deletes and never starts anything
// itself: no line of it runs rm, systemctl, systemd-run, bwrap, pasta, the
// daemon or the console (they are only printed for the owner).
func TestScratchSeatPrepareIsPlainShell(t *testing.T) {
	canaryPlainShell(t, scratchSeatPrepare, true)
	data, err := os.ReadFile(filepath.Join(root(t), scratchSeatPrepare))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, bad := range []string{"ataniq_" + "canary", "ataniq_" + "ownerchannel", "scratch-seat-" + "build", "go-tool.sh", "go build"} {
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
		for _, word := range []string{"rm", "rmdir", "systemctl", "systemd-run", "bwrap", "\"$BWRAP\"", "\"$PASTA_USE\"", "\"$DAEMON\"", "\"$CLAUDE_SRC\""} {
			for _, f := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == ';' || r == '|' || r == '&' || r == '(' }) {
				if fields := strings.Fields(f); len(fields) > 0 && fields[0] == word {
					t.Errorf("line %d runs %s: %q", i+1, word, trimmed)
				}
			}
		}
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
		{[]string{"gate", "[2001:db8::10]:7443"}, 0, "gate [2001:db8::10]:7443\n"},
		{[]string{"gate", "0.0.0.0:7443"}, 1, "wildcard"},
		{[]string{"gate", "[::]:7443"}, 1, "wildcard"},
		{[]string{"gate", "gate.example:7443"}, 1, "not an IP address"},
		{[]string{"gate", "192.0.2.10:0"}, 1, "port"},
		{[]string{"gate", "192.0.2.10"}, 1, "not IP:PORT"},
		{[]string{"gate", "192.0.2.256:7443"}, 1, "not an IP address"},
		{[]string{"config", "relative", "/srv/proof"}, 1, "not an absolute path"},
		{[]string{"config", "/srv/trial", "/srv/proof", "-bad"}, 1, "--model"},
		{[]string{"settings", "/srv/a b"}, 1, "character"},
		{[]string{"nothing"}, 2, "usage"},
		{[]string{"gate"}, 2, "usage"},
		{[]string{"commands", "/srv/trial", "/srv/proof", "192.0.2.10:7443", "more"}, 2, "usage"},
	} {
		out, code := k.run(t, nil, append([]string{"--self-test"}, c.args...)...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("--self-test %v: exit %d, want %d and %q:\n%s", c.args, code, c.code, c.want, out)
		}
	}
	for _, model := range []string{"", "sample-model"} {
		args := []string{"--self-test", "config", "/srv/sample/trial", "/srv/sample/proof"}
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
			!slices.Contains(cfg.Sandbox.NeverMounted, "/srv/sample/trial/owner") || !slices.Contains(cfg.Sandbox.NeverMounted, root(t)) {
			t.Errorf("the config: %+v", cfg)
		}
		want := 1
		if model != "" {
			want = 2
		}
		if len(cfg.Leads) != want || cfg.Leads[0].Key != "standin" || cfg.Leads[0].Agent.Kind != "stand-in" || !cfg.Leads[0].Inject {
			t.Fatalf("the leads: %+v", cfg.Leads)
		}
		if model != "" && (cfg.Leads[1].Key != "live" || cfg.Leads[1].Agent.Kind != "live" || !slices.Equal(cfg.Leads[1].Agent.Argv, []string{"/ataniq/agent/claude", "--model", model})) {
			t.Errorf("the live lead: %+v", cfg.Leads[1])
		}
	}
	out, code = k.run(t, nil, "--self-test", "settings", "/srv/sample/proof")
	var sc map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &sc) != nil || sc["door"] != "stand-in" || sc["trial"] != nil ||
		sc["sandbox"] != "/srv/sample/proof/owner-channel/no-template.json" {
		t.Fatalf("--self-test settings: exit %d:\n%s", code, out)
	}
	out, code = k.run(t, []string{"TRIAL_UNIT=sample-trial.service"}, "--self-test", "commands", "/srv/sample/trial", "/srv/sample/proof", "192.0.2.10:7443", "live")
	if code != 0 {
		t.Fatalf("--self-test commands: exit %d:\n%s", code, out)
	}
	scratchSeatCommandsHold(t, out, "/srv/sample/trial", "/srv/sample/proof", true)
	if after := scratchSeatFiles(t, k.dir); !sameScratchSeatFiles(before, after) {
		t.Error("a self-test changed the folder")
	}
	k.noCalls(t)
}

// scratchSeatCommandsHold checks the owner's steps the script prints: every
// step is there in order, every step that starts a process says OWNER ONLY,
// nothing is deleted, every move and the copy into the state folder go only
// to a free name, and the trial is stopped before and started after.
func scratchSeatCommandsHold(t *testing.T, out, state, root string, live bool) {
	t.Helper()
	last := 17
	for n := 1; n <= last; n++ {
		if (n >= 10 && n <= 12) && !live {
			continue
		}
		if !strings.Contains(out, fmt.Sprintf("%2d. ", n)) && !strings.Contains(out, fmt.Sprintf(" %d. ", n)) {
			t.Errorf("the steps lack %d:\n%s", n, out)
		}
	}
	for _, want := range []string{
		"OWNER ONLY: systemctl --user stop sample-trial.service",
		"scratch-init --config " + root + "/scratch.json",
		"test ! -e " + state + "/owner/scratch.json.trial && mv " + state + "/owner/scratch.json " + state + "/owner/scratch.json.trial",
		"test ! -e " + state + "/owner/scratch.json && cp " + root + "/owner-scratch.json " + state + "/owner/scratch.json && chmod 0600 " + state + "/owner/scratch.json",
		"run --scratch " + root + "/scratch.json --owner-channel --seat-gate 192.0.2.10:7443",
		"ataniq-console --daemon " + root + "/runtime",
		"chat set standin read --config " + root + "/scratch.json",
		"canary-token: proof-standin-0001",
		"chat set KEY off --config",
		"test ! -e " + state + "/owner/scratch.json.leads && mv " + state + "/owner/scratch.json " + state + "/owner/scratch.json.leads",
		"test ! -e " + state + "/owner/scratch.json && mv " + state + "/owner/scratch.json.trial " + state + "/owner/scratch.json",
		"OWNER ONLY: systemctl --user start sample-trial.service",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the steps lack %q:\n%s", want, out)
		}
	}
	if live != strings.Contains(out, "chat set live read") {
		t.Errorf("the live lead's steps, live %v:\n%s", live, out)
	}
	if strings.Index(out, "systemctl --user stop") > strings.Index(out, "run --scratch") ||
		strings.Index(out, "run --scratch") > strings.Index(out, "systemctl --user start") {
		t.Errorf("the trial is not stopped before the daemon and started after it:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " rm ") || strings.Contains(line, "rm -") {
			t.Errorf("a step deletes: %q", line)
		}
		if strings.Contains(line, " mv ") && !strings.Contains(line, "test ! -e ") {
			t.Errorf("a move is not to a free name: %q", line)
		}
		for _, starts := range []string{"systemctl --user stop", "systemctl --user start", "run --scratch", "ataniq-console", "--version"} {
			if strings.Contains(line, starts) && !strings.Contains(line, "OWNER ONLY") {
				t.Errorf("a step starts a process and is not marked OWNER ONLY: %q", line)
			}
		}
	}
}

// --dry says what --write would make and prints the owner's steps, and writes
// nothing, in the root, in the trial's state folder or anywhere else, and
// runs nothing.
func TestScratchSeatPrepareDryWritesNothing(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	before := scratchSeatFiles(t, k.dir)
	for _, live := range []bool{false, true} {
		out, code := k.run(t, []string{"TRIAL_UNIT=sample-trial.service"}, k.args("--dry", r, live)...)
		if code != 0 {
			t.Fatalf("--dry: exit %d:\n%s", code, out)
		}
		for _, want := range []string{
			"[dry]", r + "/scratch.json (0600)", "state_root " + k.state + " (the trial's state folder)", "runtime_root " + r + "/runtime",
			"max_running 1", "lead standin", r + "/owner-scratch.json (0600)", "\"" + k.state + "/owner\"",
			"nothing in " + k.state, "trial settings in owner/scratch.json: yes",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the dry run does not say %q:\n%s", want, out)
			}
		}
		if live != strings.Contains(out, "lead live: live claude 2.1.325, argv [\"/ataniq/agent/claude\", \"--model\", \"sample-model\"]") {
			t.Errorf("the dry run's live lead, live %v:\n%s", live, out)
		}
		scratchSeatCommandsHold(t, out, k.state, r, live)
	}
	// The values from the environment, as the guide sets them.
	out, code := k.run(t, []string{"OWNER_STATE_DIR=" + k.state, "ROOT=" + r, "SEAT_GATE=192.0.2.10:7443"},
		"--dry", "--bin", k.bin, "--daemon", filepath.Join(k.bin, "ataniqd"), "--pasta", filepath.Join(k.bin, "pasta"))
	if code != 0 || !strings.Contains(out, "state_root "+k.state) || !strings.Contains(out, "--seat-gate 192.0.2.10:7443") || !strings.Contains(out, "<TRIAL_UNIT>") {
		t.Errorf("--dry from the environment: exit %d:\n%s", code, out)
	}
	if after := scratchSeatFiles(t, k.dir); !sameScratchSeatFiles(before, after) {
		t.Error("the dry run changed the folder")
	}
	k.noCalls(t)
}

// What the script refuses, in --dry and --write alike, before anything is
// made: no mode, a missing value, a state folder without the gate's key or
// with the trial's settings already set aside, a root in /tmp, inside the
// state folder or holding something, a claude without a model or that is a
// link, and a gate that is not one address and port.
func TestScratchSeatPrepareRefuses(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	full := filepath.Join(k.dir, "full")
	if err := os.MkdirAll(filepath.Join(full, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(k.bin, "claude-file"), filepath.Join(k.dir, "claude-link")); err != nil {
		t.Fatal(err)
	}
	noKey := filepath.Join(k.dir, "no-key")
	if err := os.MkdirAll(filepath.Join(noKey, "owner", "gate"), 0o700); err != nil {
		t.Fatal(err)
	}
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
		code int
		want string
	}{
		{"no mode", base[1:], 2, "usage"},
		{"two modes", append([]string{"--write"}, base...), 2, "usage"},
		{"no state", set(base, "--state", ""), 1, "no state folder"},
		{"a relative state", set(base, "--state", "trial"), 1, "not an absolute path"},
		{"no gate key", set(base, "--state", noKey), 1, "has no owner/gate/cert.pem"},
		{"no root", set(base, "--root", ""), 1, "no root"},
		{"a root in /tmp", set(base, "--root", "/tmp/proof"), 1, "/tmp"},
		{"a root in the state folder", set(base, "--root", k.state+"/proof"), 1, "the state folder lie one in the other"},
		{"a root in the checkout", set(base, "--root", root(t)+"/proof"), 1, "the checkout lie one in the other"},
		{"a root that holds something", set(base, "--root", full), 1, "not empty"},
		{"a wildcard gate", set(base, "--gate", "0.0.0.0:7443"), 1, "wildcard"},
		{"a host name", set(base, "--gate", "gate.example:7443"), 1, "not an IP address"},
		{"claude without a model", append(slices.Clone(base), "--claude", filepath.Join(k.bin, "claude-file")), 1, "--claude needs --model"},
		{"a model without claude", append(slices.Clone(base), "--model", "sample-model"), 1, "--model needs --claude"},
		{"claude a link", append(slices.Clone(base), "--claude", filepath.Join(k.dir, "claude-link"), "--model", "m"), 1, "is a link"},
		{"a bad model", append(slices.Clone(base), "--claude", filepath.Join(k.bin, "claude-file"), "--model", "-x"), 1, "--model is a model name"},
		{"no daemon", set(base, "--daemon", filepath.Join(k.dir, "missing")), 1, "no daemon of both tags"},
	}
	before := scratchSeatFiles(t, k.dir)
	for _, mode := range []string{"--dry", "--write"} {
		for _, c := range cases {
			args := slices.Clone(c.args)
			if args[0] == "--dry" {
				args[0] = mode
			}
			out, code := k.run(t, nil, args...)
			if code != c.code || !strings.Contains(out, c.want) {
				t.Errorf("%s %s: exit %d, want %d and %q:\n%s", mode, c.name, code, c.code, c.want, out)
			}
		}
	}
	// The trial's settings set aside by an earlier proof and not put back.
	aside := filepath.Join(k.state, "owner", "scratch.json.trial")
	if err := os.WriteFile(aside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 1 || !strings.Contains(out, "scratch.json.trial is there already") {
		t.Errorf("with the trial's settings set aside: exit %d:\n%s", code, out)
	}
	if err := os.Remove(aside); err != nil {
		t.Fatal(err)
	}
	if after := scratchSeatFiles(t, k.dir); !sameScratchSeatFiles(before, after) {
		t.Error("a refused run changed the folder")
	}
	k.noCalls(t)
}

// --write makes the root and writes in it only: the config and the settings
// of mode 0600 (valid JSON), the copies, the conf files, the throwaway
// repository and the folders, all private. The trial's state folder is the
// same bytes and modes after it; no stub ran (claude is copied, not run).
func TestScratchSeatPrepareWriteTouchesOnlyTheRoot(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	before := scratchSeatFiles(t, k.state)
	out, code := k.run(t, []string{"TRIAL_UNIT=sample-trial.service"}, k.args("--write", r, true)...)
	if code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "nothing was started, and nothing in "+k.state+" was changed") {
		t.Errorf("--write does not say what it did not do:\n%s", out)
	}
	scratchSeatCommandsHold(t, out, k.state, r, true)
	if after := scratchSeatFiles(t, k.state); !sameScratchSeatFiles(before, after) {
		t.Fatal("--write changed the trial's state folder")
	}
	k.noCalls(t)
	for rel, mode := range map[string]os.FileMode{
		".": 0o700 | fs.ModeDir, "scratch.json": 0o600, "owner-scratch.json": 0o600, "evidence": 0o700 | fs.ModeDir,
		"owner-channel": 0o700 | fs.ModeDir, "owner-channel/drop": 0o700 | fs.ModeDir, "conf/managed-settings.json": 0o600,
		"conf/guard-hook": 0o700, "agent-install/ataniq-canary-standin": 0o700, "agent-install/claude": 0o700,
		"bin/ataniq-probe": 0o700, "upstream/README.txt": 0o600, "conf/replay-screens/1.txt": 0o600,
	} {
		st, err := os.Lstat(filepath.Join(r, rel))
		if err != nil || st.Mode() != mode {
			t.Errorf("%s: %v, %v, want %v", rel, st, err, mode)
		}
	}
	for _, rel := range []string{"runtime", "owner-channel/work", "owner-channel/no-template.json", "owner-channel/mirror.git"} {
		if _, err := os.Lstat(filepath.Join(r, rel)); err == nil {
			t.Errorf("--write made %s, which is the daemon's to make or is never made", rel)
		}
	}
	for _, rel := range []string{"scratch.json", "owner-scratch.json"} {
		var v map[string]any
		if err := json.Unmarshal(mustRead(t, filepath.Join(r, rel)), &v); err != nil {
			t.Errorf("%s is not JSON: %v", rel, err)
		}
	}
	if got := mustRead(t, filepath.Join(r, "agent-install", "claude")); !bytes.Equal(got, mustRead(t, filepath.Join(k.bin, "claude-file"))) {
		t.Error("the claude copy is not the file given")
	}
	// The repository has one commit on develop.
	cmd := exec.Command("git", "-C", filepath.Join(r, "upstream"), "rev-list", "--count", "refs/heads/develop")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + k.home}
	if b, err := cmd.Output(); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Errorf("the throwaway repository: %q %v", b, err)
	}
	// A second --write into the same root is refused: it is not empty now.
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 1 || !strings.Contains(out, "not empty") {
		t.Errorf("a second --write: exit %d:\n%s", code, out)
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
