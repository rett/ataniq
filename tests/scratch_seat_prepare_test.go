package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Tests of qualification/scratch-seat/prepare.sh, the preparation of the
// owner-attended proof of real lead replies (docs/scratch-seat-proof.md).
// Nothing here starts a unit, a daemon, a sandbox or an agent: the binaries,
// pasta and claude of these tests are recording stubs, the PATH puts recording
// stubs of systemctl, systemd-run, bwrap and claude first (the systemctl stub
// answers "show" from a file of the test's and refuses every other verb), and
// the trial's state folder is a sample one in a private folder of the test.
// --write, --status and the swaps run only in that folder.

const scratchSeatPrepare = "qualification/scratch-seat/prepare.sh"

// scratchSeatKit is the sample world of one run of the script.
type scratchSeatKit struct {
	dir, bin, tools, calls, state, home, units string
}

// scratchSeatStubs are the programs on PATH that the script must never run;
// systemctl is a stub too, which answers "show" only.
var scratchSeatStubs = []string{"systemd-run", "bwrap", "pasta", "claude", "ataniqd", "ataniq-console", "curl"}

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
		calls: filepath.Join(dir, "calls"), state: filepath.Join(dir, "trial-state"), home: filepath.Join(dir, "home"),
		units: filepath.Join(dir, "units")}
	for _, d := range []string{k.bin, k.tools, k.home, k.state, filepath.Join(k.state, "owner"), filepath.Join(k.state, "owner", "gate")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"owner/gate/cert.pem": "sample certificate\n", "owner/gate/key.pem": "sample key\n",
		"owner/gate/seat-pins": strings.Repeat("ab", 32) + "\n", "owner/scratch.json": "{\n  \"trial\": true\n}\n",
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
	// systemctl --user show -p PROP --value -- UNIT answers from the units
	// file ("UNIT PROP VALUE" lines, the value the rest of the line); an
	// unlisted ActiveState is inactive and an unlisted LoadState not-found.
	// Every other call is refused.
	systemctl := "#!/bin/sh\necho \"systemctl $*\" >>'" + k.calls + "'\n" +
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
func (k scratchSeatKit) unitsSay(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.WriteFile(k.units, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// args are the flags of a run with the kit's binaries, the root given, the
// sample gate, sitting 3, the sample unit and, with live, the claude stub and
// a model.
func (k scratchSeatKit) args(mode, root string, live bool, more ...string) []string {
	a := []string{mode, "--state", k.state, "--root", root, "--gate", "192.0.2.10:7443", "--sitting", "3", "--unit", "sample-trial.service",
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

// onlyShows fails the test when a stub ran for anything but systemctl's
// read-only "show".
func (k scratchSeatKit) onlyShows(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(k.calls)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasPrefix(line, "systemctl --user show -p ") {
			t.Fatalf("the script ran %q", line)
		}
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
// build/scratch-seat/ataniqd. It never deletes a file and never starts
// anything itself: no line of it runs rm, systemd-run, bwrap, pasta, the
// daemon or the console (they are only printed for the owner); systemctl
// runs on one line, a read-only "show"; git runs only through proof_git;
// unlink runs only where a name of a file that has another is removed.
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
		"systemctl":    `systemctl --user show -p "$2" --value -- "$1"`,
		"unlink":       `unlink -- "$1"`,
		"ln":           `ln -P -T -- "$1" "$2"`,
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
			case "rm", "rmdir", "mv", "git", "systemd-run", "bwrap", "\"$BWRAP\"", "\"$PASTA_USE\"", "\"$DAEMON\"", "\"$CLAUDE_SRC\"":
				t.Errorf("line %d runs %s: %q", i+1, word, trimmed)
			case "systemctl", "unlink", "ln", "/usr/bin/git":
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
			!slices.Contains(cfg.Sandbox.NeverMounted, "/srv/sample/trial/owner") || !slices.Contains(cfg.Sandbox.NeverMounted, root(t)) {
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
// or defines a lead says OWNER ONLY; nothing is deleted or moved by hand (the
// swaps are prepare's modes); the trial's unit is the quoted reference
// "$TRIAL_UNIT", never its value, and no line holds a placeholder; the
// trial is stopped and masked before the swap and started after the way
// back; the daemon's exit status is printed; the commands of the leads name
// only the leads configured, by their keys.
func scratchSeatCommandsHold(t *testing.T, out, state, root string, live bool) {
	t.Helper()
	i := strings.Index(out, "Your steps, in order")
	if i < 0 {
		t.Fatalf("no steps:\n%s", out)
	}
	out = out[i:]
	for n := 1; n <= 16; n++ {
		if (n >= 9 && n <= 11) && !live {
			continue
		}
		if !strings.Contains(out, fmt.Sprintf("%2d. ", n)) && !strings.Contains(out, fmt.Sprintf(" %d. ", n)) {
			t.Errorf("the steps lack %d:\n%s", n, out)
		}
	}
	prep := " --state " + state + " --root " + root
	for _, want := range []string{
		"A for the daemon, B for the console, C for the desk",
		"OWNER ONLY (starts pasta, bubblewrap, curl and a throwaway user unit): { sh " + root + "/netcheck.sh; echo \"netcheck: exit status $?\"; } 2>&1 | tee -a " + root + "/evidence/netcheck.txt",
		"--record-trial" + prep + " --unit \"$TRIAL_UNIT\"",
		"--status" + prep,
		"OWNER ONLY: systemctl --user stop -- \"$TRIAL_UNIT\"",
		"a unit file only (step g names the kind): OWNER ONLY: systemctl --user mask --runtime -- \"$TRIAL_UNIT\"",
		"(inactive; not-found for a transient unit, masked for a unit file)",
		"a unit file still loaded (the mask did not take): OWNER ONLY: systemctl --user unmask --runtime -- \"$TRIAL_UNIT\"",
		"scratch-init --config " + root + "/scratch.json",
		"--swap-in" + prep + " --gate 192.0.2.10:7443 --sitting 3 --unit \"$TRIAL_UNIT\"",
		"sh -c 'trap : INT; \"$@\" 2>&1; echo \"ataniqd: exit status $?\"' sh ",
		" run --scratch " + root + "/scratch.json --owner-channel --seat-gate 192.0.2.10:7443 | tee -ai " + root + "/evidence/daemon.log",
		"ataniq-console --daemon " + root + "/runtime",
		"OWNER ONLY (the daemon runs git to define it): d on standin-3",
		"chat set standin-3 read --config " + root + "/scratch.json",
		"canary-token: proof-standin-0001",
		"Only after NOT delivered (nothing was written)",
		"Ctrl+\\ then q",
		"no paste; stop and reconcile",
		"--swap-out" + prep + " --gate 192.0.2.10:7443 --unit \"$TRIAL_UNIT\"",
		"a transient unit (step 14 printed the kind): OWNER ONLY: sh " + root + "/trial-restart.sh",
		"a unit file: OWNER ONLY: systemctl --user unmask --runtime -- \"$TRIAL_UNIT\"",
		"a unit file: OWNER ONLY: systemctl --user start -- \"$TRIAL_UNIT\"",
		"journalctl --user --unit \"$TRIAL_UNIT\" -I",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the steps lack %q:\n%s", want, out)
		}
	}
	if live != strings.Contains(out, "chat set live-3 read") || live != strings.Contains(out, "claude --version") ||
		live != strings.Contains(out, "d on live-3") || live != strings.Contains(out, "systemctl --user status -- ataniq-lead-scratch-live-3.service") {
		t.Errorf("the live lead's steps, live %v:\n%s", live, out)
	}
	if live && !strings.Contains(out, "chat set live-3 off") || !live && !strings.Contains(out, "chat set standin-3 off") {
		t.Errorf("the off step, live %v:\n%s", live, out)
	}
	order := []string{"--record-trial", "systemctl --user stop", "mask --runtime", "--swap-in", "run --scratch", "--swap-out",
		"OWNER ONLY: sh " + root + "/trial-restart.sh", "systemctl --user start"}
	for i := 1; i < len(order); i++ {
		if strings.Index(out, order[i-1]) > strings.Index(out, order[i]) {
			t.Errorf("%q comes after %q:\n%s", order[i-1], order[i], out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " rm ") || strings.Contains(line, "rm -") || strings.Contains(line, " mv ") || strings.Contains(line, "unlink") {
			t.Errorf("a step deletes or moves by hand: %q", line)
		}
		if strings.Contains(line, "sample-trial.service") || strings.Contains(line, "<") && strings.Contains(line, ">") && strings.Index(line, "<") < strings.LastIndex(line, ">") && !strings.Contains(line, "2>&1") {
			t.Errorf("a step names the unit's value or holds a placeholder: %q", line)
		}
		if strings.Contains(line, "KEY") {
			t.Errorf("a step names a lead KEY: %q", line)
		}
		for _, starts := range []string{"systemctl --user stop", "systemctl --user start", "mask --runtime", "run --scratch", "ataniq-console", "--version", "d on ", "s on ", "trial-restart.sh"} {
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
			"max_running 1", "lead standin-3", r + "/owner-scratch.json (0600)", "\"" + k.state + "/owner\"", "\"" + k.home + "/.claude\"",
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
		"--sitting", "3", "--unit", "sample-trial.service", "--bin", gone+"/bin", "--daemon", gone+"/ataniqd", "--claude", gone+"/claude", "--model", "m")
	if code != 0 || !strings.Contains(out, "state_root "+gone+"/state") || !strings.Contains(out, "pasta: /usr/bin/pasta when it is") ||
		!strings.Contains(out, "bubblewrap: the bwrap on PATH at --write") {
		t.Errorf("--dry with nothing there: exit %d:\n%s", code, out)
	}
	// The values from the environment, as the guide sets them.
	out, code = k.run(t, []string{"OWNER_STATE_DIR=" + k.state, "ROOT=" + r, "SEAT_GATE=192.0.2.10:7443", "SITTING=3", "TRIAL_UNIT=sample-trial.service"},
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
// trial unit that is not one NAME.service, a sitting that is not 1 to 999, a
// root in /tmp, inside the state folder or the checkout, a claude without a
// model, a gate that is not one IPv4 address and port, and a HOME that
// cannot give the never-mounted paths.
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
		{"no unit", set(base, "--unit", ""), nil, 1, "no trial unit"},
		{"two units", set(base, "--unit", "a.service b.service"), nil, 1, "one unit name"},
		{"a unit option", set(base, "--unit", "-x.service"), nil, 1, "starts with -"},
		{"a unit pattern", set(base, "--unit", "sample*.service"), nil, 1, "one unit name"},
		{"a unit with shell text", set(base, "--unit", "a;b.service"), nil, 1, "one unit name"},
		{"a socket unit", set(base, "--unit", "sample.socket"), nil, 1, "not a NAME.service"},
		{"a lead's unit", set(base, "--unit", "ataniq-lead-scratch-x.service"), nil, 1, "a lead's unit"},
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

// What --write refuses after it has looked, before it makes anything: a
// state folder without usable gate material or whose settings are not the
// trial's private settings, one with the trial's settings set aside or with
// anything of this sitting's leads, a missing tool, and a root whose parent
// is missing or reached through a link, that is linked into the state
// folder, that cannot be read or listed, or that holds something.
func TestScratchSeatPrepareWriteRefuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("modes do not bind root")
	}
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	owner := filepath.Join(k.state, "owner")
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
		{"no gate key", base, func() func() {
			p := filepath.Join(owner, "gate", "key.pem")
			os.Rename(p, p+".x")
			return func() { os.Rename(p+".x", p) }
		}, nil, "has no owner/gate/key.pem"},
		{"a key readable by others", base, func() func() { return write("owner/gate/key.pem", "sample key\n", 0o644) }, nil, "group or other"},
		{"empty pins", base, func() func() { return write("owner/gate/seat-pins", "", 0o600) }, nil, "empty"},
		{"no pin", base, func() func() { return write("owner/gate/seat-pins", "# none\n", 0o600) }, nil, "pins no seat key"},
		{"settings not the trial's", base, func() func() { return write("owner/scratch.json", "{\"door\": \"stand-in\"}\n", 0o600) }, nil, "not the trial's settings"},
		{"settings readable by others", base, func() func() { return write("owner/scratch.json", "{\"trial\": true}\n", 0o644) }, nil, "not private"},
		{"settings missing", base, func() func() {
			p := filepath.Join(owner, "scratch.json")
			os.Rename(p, p+".x")
			return func() { os.Rename(p+".x", p) }
		}, nil, "--status"},
		{"settings a link", base, func() func() {
			p := filepath.Join(owner, "scratch.json")
			os.Rename(p, p+".x")
			os.Symlink(p+".x", p)
			return func() { os.Remove(p); os.Rename(p+".x", p) }
		}, nil, "is a link"},
		{"settings set aside", base, func() func() { return write("owner/scratch.json.trial", "{}\n", 0o600) }, nil, "scratch.json.trial is there already"},
		{"a dangling link set aside", base, func() func() {
			p := filepath.Join(owner, "scratch.json.trial")
			os.Symlink(filepath.Join(k.dir, "nowhere"), p)
			return func() { os.Remove(p) }
		}, nil, "scratch.json.trial is there already"},
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
		".": 0o700 | fs.ModeDir, "scratch.json": 0o600, "owner-scratch.json": 0o600, "evidence": 0o700 | fs.ModeDir,
		"owner-channel": 0o700 | fs.ModeDir, "owner-channel/drop": 0o700 | fs.ModeDir, "conf/managed-settings.json": 0o600,
		"conf/guard-hook": 0o700, "agent-install/ataniq-canary-standin": 0o700, "agent-install/claude": 0o700,
		"bin/ataniq-probe": 0o700, "upstream/README.txt": 0o600, "conf/replay-screens/1.txt": 0o600, "netcheck.sh": 0o700,
	} {
		st, err := os.Lstat(filepath.Join(r, rel))
		if err != nil || st.Mode() != mode {
			t.Errorf("%s: %v, %v, want %v", rel, st, err, mode)
		}
	}
	for _, rel := range []string{"runtime", "owner-channel/work", "owner-channel/no-template.json", "owner-channel/mirror.git", "swap-record"} {
		if _, err := os.Lstat(filepath.Join(r, rel)); err == nil {
			t.Errorf("--write made %s, which is the daemon's or a swap's to make, or is never made", rel)
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
	// The network check parses, names the gate and is never run.
	nc := string(mustRead(t, filepath.Join(r, "netcheck.sh")))
	if out, err := exec.Command("/bin/sh", "-n", filepath.Join(r, "netcheck.sh")).CombinedOutput(); err != nil {
		t.Errorf("netcheck.sh does not parse: %v\n%s", err, out)
	}
	for _, want := range []string{"GATE_HOST='192.0.2.10'", "GATE_PORT='7443'", "BWRAP='" + filepath.Join(k.tools, "bwrap") + "'", "it guards nothing"} {
		if !strings.Contains(nc, want) {
			t.Errorf("netcheck.sh lacks %q", want)
		}
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

// freeTCPPort is a port no socket listens on a moment ago.
func freeTCPPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return port
}

// The settings swap, in a sample state folder: --status reads the three
// names and prints one next step; --swap-in refuses a running or unmasked
// trial unit, a gate port that something listens on, a lead's unit the user
// manager knows and a taken archive name (a dangling link too), then moves
// the trial's settings aside within owner/ and puts the proof's in their
// place, recorded in ROOT; --swap-out puts the same file back. A swap
// stopped half way is named by --status and finished by --swap-out; a
// layout that is not one of the three is refused with nothing moved. Only
// systemctl's read-only "show" runs, and nothing of the state folder is
// removed.
func TestScratchSeatPrepareSwaps(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
		t.Fatalf("--write: exit %d:\n%s", code, out)
	}
	owner := filepath.Join(k.state, "owner")
	canon, aside, archive := filepath.Join(owner, "scratch.json"), filepath.Join(owner, "scratch.json.trial"), filepath.Join(owner, "scratch.json.leads-3")
	trialBytes, trialID := mustRead(t, canon), inodeOf(t, canon)
	proofBytes := mustRead(t, filepath.Join(r, "owner-scratch.json"))
	port := freeTCPPort(t)
	gate := "192.0.2.10:" + port
	vals := []string{"--state", k.state, "--root", r}
	swapIn := append(append([]string{"--swap-in"}, vals...), "--gate", gate, "--sitting", "3", "--unit", "sample-trial.service")
	swapOut := append(append([]string{"--swap-out"}, vals...), "--gate", gate, "--unit", "sample-trial.service")
	status := func(want, next string) {
		t.Helper()
		out, code := k.run(t, nil, append([]string{"--status"}, vals...)...)
		if code != 0 || !strings.Contains(out, "status: "+want) || !strings.Contains(out, "next: ") || !strings.Contains(out, next) ||
			strings.Count(out, "next: ") != 1 {
			t.Fatalf("--status: exit %d, want %q and %q:\n%s", code, want, next, out)
		}
	}
	names := func() []string {
		t.Helper()
		e, err := os.ReadDir(owner)
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, x := range e {
			n = append(n, x.Name())
		}
		return n
	}
	ownerBefore := names()
	status("trial in place (not swapped)", "record the trial's unit while it runs (--record-trial, step f)")
	masked := "sample-trial.service LoadState masked"
	// Without a record of the trial's unit, --swap-in refuses.
	k.unitsSay(t, masked)
	if out, code := k.run(t, nil, swapIn...); code != 1 || !strings.Contains(out, "no record of the trial's unit") {
		t.Fatalf("--swap-in without a record: exit %d:\n%s", code, out)
	}
	k.recordTrial(t, r, k.fileUnit("UnitFileState enabled", "WantedBy default.target"))
	status("trial in place (not swapped)", "mask it for this boot (step 1)")
	enabled := []string{"sample-trial.service UnitFileState enabled", "sample-trial.service WantedBy default.target"}

	// Refusals before anything moves.
	for _, c := range []struct {
		name  string
		units []string
		setup func() func()
		want  string
	}{
		{"a running trial", []string{"sample-trial.service ActiveState active", masked}, nil, "not inactive"},
		{"an unmasked trial", append([]string{"sample-trial.service LoadState loaded"}, enabled...), nil, "can be started: WantedBy=default.target UnitFileState=enabled"},
		{"another unit", []string{masked}, func() func() {
			i := slices.Index(swapIn, "sample-trial.service")
			swapIn[i] = "other-trial.service"
			return func() { swapIn[i] = "sample-trial.service" }
		}, "recorded the trial's unit sample-trial.service, not other-trial.service"},
		{"a listener on the gate's port", []string{masked}, func() func() {
			l, err := net.Listen("tcp", "127.0.0.1:"+port)
			if err != nil {
				t.Fatal(err)
			}
			return func() { l.Close() }
		}, "listens on the gate's port"},
		{"a known lead unit", []string{masked, "ataniq-lead-scratch-live-3.service LoadState loaded"}, nil, "another scratch root uses the key live-3"},
		{"a taken archive name", []string{masked}, func() func() {
			os.Symlink(filepath.Join(k.dir, "nowhere"), archive)
			return func() { os.Remove(archive) }
		}, "scratch.json.leads-3 is taken"},
	} {
		k.unitsSay(t, c.units...)
		undo := func() {}
		if c.setup != nil {
			undo = c.setup()
		}
		out, code := k.run(t, nil, swapIn...)
		undo()
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("--swap-in with %s: exit %d, want %q:\n%s", c.name, code, c.want, out)
		}
		if !bytes.Equal(mustRead(t, canon), trialBytes) || inodeOf(t, canon) != trialID {
			t.Fatalf("--swap-in with %s moved the trial's settings", c.name)
		}
		if _, err := os.Lstat(filepath.Join(r, "swap-record")); err == nil {
			t.Fatalf("--swap-in with %s wrote the record", c.name)
		}
	}
	if out, code := k.run(t, nil, append(slices.Clone(swapIn[:len(swapIn)-4]), "--sitting", "4", "--unit", "sample-trial.service")...); code != 1 || !strings.Contains(out, "no lead standin-4") {
		t.Errorf("--swap-in with another sitting: exit %d:\n%s", code, out)
	}

	// The swap in.
	k.unitsSay(t, masked)
	out, code := k.run(t, nil, swapIn...)
	if code != 0 || !strings.Contains(out, "swapped in:") || !strings.Contains(out, "status: swapped in") {
		t.Fatalf("--swap-in: exit %d:\n%s", code, out)
	}
	if !bytes.Equal(mustRead(t, canon), proofBytes) || !bytes.Equal(mustRead(t, aside), trialBytes) || inodeOf(t, aside) != trialID {
		t.Fatal("after --swap-in the names do not hold the proof's and the trial's settings")
	}
	if st, err := os.Lstat(canon); err != nil || st.Mode() != 0o600 {
		t.Fatalf("owner/scratch.json after --swap-in: %v %v", st, err)
	}
	rec := string(mustRead(t, filepath.Join(r, "swap-record")))
	if !strings.Contains(rec, "trial_id "+trialID+"\n") || !strings.Contains(rec, "archive scratch.json.leads-3\n") {
		t.Fatalf("the record: %s", rec)
	}
	status("swapped in", "--swap-out (step 14)")
	if out, code := k.run(t, nil, swapIn...); code != 1 || !strings.Contains(out, "nothing was moved") {
		t.Errorf("a second --swap-in: exit %d:\n%s", code, out)
	}
	// --swap-out refuses while a daemon listens on the gate's port.
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	out, code = k.run(t, nil, swapOut...)
	l.Close()
	if code != 1 || !strings.Contains(out, "listens on the gate's port") || !bytes.Equal(mustRead(t, canon), proofBytes) {
		t.Fatalf("--swap-out with a listener: exit %d:\n%s", code, out)
	}

	// A swap-out stopped half way, after the proof's settings got their
	// archive name and before the old name went: one file, two names.
	if err := os.Link(canon, archive); err != nil {
		t.Fatal(err)
	}
	status("interrupted or ambiguous (a rename stopped half way", "--swap-out (step 14)")
	out, code = k.run(t, nil, swapOut...)
	if code != 0 || !strings.Contains(out, "swapped out:") || !strings.Contains(out, "status: trial in place (the way back is done)") {
		t.Fatalf("--swap-out from half way: exit %d:\n%s", code, out)
	}
	if !bytes.Equal(mustRead(t, canon), trialBytes) || inodeOf(t, canon) != trialID || !bytes.Equal(mustRead(t, archive), proofBytes) {
		t.Fatal("after --swap-out the trial's settings are not back as the same file")
	}
	if _, err := os.Lstat(aside); err == nil {
		t.Fatal("owner/scratch.json.trial is still there")
	}
	status("trial in place (the way back is done)", "unmask the trial's unit if it is masked and start it again (step 15)")
	if out, code := k.run(t, nil, swapOut...); code != 0 || !strings.Contains(out, "nothing to do") {
		t.Errorf("a second --swap-out: exit %d:\n%s", code, out)
	}
	ownerAfter := names()
	for _, n := range ownerBefore {
		if !slices.Contains(ownerAfter, n) {
			t.Errorf("owner/%s is gone after the cycle", n)
		}
	}
	if len(ownerAfter) != len(ownerBefore)+1 || !slices.Contains(ownerAfter, "scratch.json.leads-3") {
		t.Errorf("owner/ after the cycle: %v, before %v", ownerAfter, ownerBefore)
	}
	if g := string(mustRead(t, filepath.Join(r, "evidence", "trial-unit-guard.txt"))); g != "swap-in, sitting 3, unit sample-trial.service: kind file, ActiveState=inactive, LoadState=masked: the runtime mask took: accepted\n" {
		t.Errorf("the guard's evidence: %q", g)
	}
	k.onlyShows(t)
}

// A swap stopped in each other place: --status names it and --swap-out puts
// the trial's settings back; a layout of none of the three states (a
// dangling link at owner/scratch.json, a set-aside no record names) is
// refused by every mode with nothing moved.
func TestScratchSeatPrepareSwapRecovery(t *testing.T) {
	newSwapped := func(t *testing.T) (scratchSeatKit, string, []string, []string, string) {
		k := newScratchSeatKit(t)
		r := filepath.Join(k.dir, "proof")
		if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
			t.Fatalf("--write: exit %d:\n%s", code, out)
		}
		k.recordTrial(t, r, k.transientUnit("/srv/sample/bin/ataniqd", "Sample trial"))
		k.unitsSay(t)
		gate := "192.0.2.10:" + freeTCPPort(t)
		vals := []string{"--state", k.state, "--root", r}
		in := append(append([]string{"--swap-in"}, vals...), "--gate", gate, "--sitting", "3", "--unit", "sample-trial.service")
		out := append(append([]string{"--swap-out"}, vals...), "--gate", gate, "--unit", "sample-trial.service")
		return k, r, in, out, filepath.Join(k.state, "owner")
	}
	statusOf := func(t *testing.T, k scratchSeatKit, r string) string {
		out, code := k.run(t, nil, "--status", "--state", k.state, "--root", r)
		if code != 0 {
			t.Fatalf("--status: exit %d:\n%s", code, out)
		}
		return out
	}

	t.Run("stopped after the trial's settings went aside", func(t *testing.T) {
		k, r, in, out, owner := newSwapped(t)
		trial := mustRead(t, filepath.Join(owner, "scratch.json"))
		if o, code := k.run(t, nil, in...); code != 0 {
			t.Fatalf("--swap-in: %d\n%s", code, o)
		}
		// Undo the second rename's effect as if it never ran: the proof's
		// settings go back to a dot name (as the copy was).
		if err := os.Rename(filepath.Join(owner, "scratch.json"), filepath.Join(owner, ".scratch.json.proof.sample")); err != nil {
			t.Fatal(err)
		}
		if s := statusOf(t, k, r); !strings.Contains(s, "status: interrupted or ambiguous (owner/scratch.json is missing") ||
			!strings.Contains(s, "next: --swap-out (step 14)") || !strings.Contains(s, "other name in owner/: "+filepath.Join(owner, ".scratch.json.proof.sample")) {
			t.Fatalf("--status:\n%s", s)
		}
		if o, code := k.run(t, nil, in...); code != 1 || !strings.Contains(o, "nothing was moved") {
			t.Fatalf("--swap-in from there: %d\n%s", code, o)
		}
		if o, code := k.run(t, nil, out...); code != 0 || !strings.Contains(o, "swapped out:") {
			t.Fatalf("--swap-out: %d\n%s", code, o)
		}
		if !bytes.Equal(mustRead(t, filepath.Join(owner, "scratch.json")), trial) {
			t.Fatal("the trial's settings are not back")
		}
	})

	t.Run("stopped with the trial's settings under both names", func(t *testing.T) {
		k, r, in, out, owner := newSwapped(t)
		canon, aside := filepath.Join(owner, "scratch.json"), filepath.Join(owner, "scratch.json.trial")
		id := inodeOf(t, canon)
		if o, code := k.run(t, nil, in...); code != 0 {
			t.Fatalf("--swap-in: %d\n%s", code, o)
		}
		// Back to the moment after the first link: the proof's settings not
		// yet in place, the trial's file with both names.
		if err := os.Rename(canon, filepath.Join(owner, ".scratch.json.proof.sample")); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(aside, canon); err != nil {
			t.Fatal(err)
		}
		if s := statusOf(t, k, r); !strings.Contains(s, "rename stopped half way: the recorded trial settings have both names") {
			t.Fatalf("--status:\n%s", s)
		}
		if o, code := k.run(t, nil, out...); code != 0 || !strings.Contains(o, "status: trial in place") {
			t.Fatalf("--swap-out: %d\n%s", code, o)
		}
		if inodeOf(t, canon) != id {
			t.Fatal("owner/scratch.json is not the trial's file")
		}
		if _, err := os.Lstat(aside); err == nil {
			t.Fatal("the second name is still there")
		}
	})

	t.Run("layouts of no state", func(t *testing.T) {
		k, r, in, out, owner := newSwapped(t)
		canon := filepath.Join(owner, "scratch.json")
		if o, code := k.run(t, nil, in...); code != 0 {
			t.Fatalf("--swap-in: %d\n%s", code, o)
		}
		// The proof's settings replaced by a dangling link.
		if err := os.Rename(canon, filepath.Join(k.dir, "kept-proof")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(k.dir, "nowhere"), canon); err != nil {
			t.Fatal(err)
		}
		before := scratchSeatFiles(t, k.state)
		s := statusOf(t, k, r)
		if !strings.Contains(s, "status: interrupted or ambiguous") || !strings.Contains(s, "a link (a dangling one counts as taken)") ||
			!strings.Contains(s, "keep the trial stopped (a unit file masked as in step 1) and both daemons stopped") {
			t.Fatalf("--status:\n%s", s)
		}
		for _, args := range [][]string{in, out} {
			if o, code := k.run(t, nil, args...); code != 1 || !strings.Contains(o, "nothing was moved") {
				t.Errorf("%s: %d\n%s", args[0], code, o)
			}
		}
		if after := scratchSeatFiles(t, k.state); !sameScratchSeatFiles(before, after) {
			t.Fatal("a refused swap changed the state folder")
		}
	})

	t.Run("a set-aside of another sitting", func(t *testing.T) {
		k := newScratchSeatKit(t)
		r := filepath.Join(k.dir, "proof")
		if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
			t.Fatalf("--write: exit %d:\n%s", code, out)
		}
		if err := os.WriteFile(filepath.Join(k.state, "owner", "scratch.json.trial"), []byte("{\"trial\": true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if s := statusOf(t, k, r); !strings.Contains(s, "set aside by another sitting") {
			t.Fatalf("--status:\n%s", s)
		}
	})
}

// The swap record's numbers are what the swap found.
func TestScratchSeatPrepareSwapRecordIsPlain(t *testing.T) {
	k := newScratchSeatKit(t)
	r := filepath.Join(k.dir, "proof")
	if out, code := k.run(t, nil, k.args("--write", r, false)...); code != 0 {
		t.Fatalf("--write: %d\n%s", code, out)
	}
	k.recordTrial(t, r, k.fileUnit())
	k.unitsSay(t, "sample-trial.service LoadState masked")
	if out, code := k.run(t, nil, "--swap-in", "--state", k.state, "--root", r, "--gate", "192.0.2.10:"+freeTCPPort(t), "--sitting", "3", "--unit", "sample-trial.service"); code != 0 {
		t.Fatalf("--swap-in: %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, filepath.Join(r, "swap-record")))), "\n")
	if len(lines) != 7 || lines[0] != "swap-record 1" || lines[1] != "state "+k.state || lines[2] != "sitting 3" {
		t.Fatalf("the record: %q", lines)
	}
	for _, l := range lines[3:] {
		f := strings.Fields(l)
		if len(f) != 2 {
			t.Errorf("a record line: %q", l)
		}
		if strings.HasSuffix(f[0], "_sha") {
			if _, err := strconv.ParseUint(f[1][:8], 16, 64); err != nil || len(f[1]) != 64 {
				t.Errorf("a digest: %q", l)
			}
		}
	}
	if st, err := os.Lstat(filepath.Join(r, "swap-record")); err != nil || st.Mode() != 0o600 {
		t.Errorf("the record's mode: %v %v", st, err)
	}
}

// Every shell block of the runbook parses as sh and holds no placeholder in
// angle brackets; the block of the values is assignments only; the trial's
// unit is always the quoted reference "$TRIAL_UNIT"; and the runbook marks
// both defines and every unit change OWNER ONLY.
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
		"**OWNER ONLY (B): define it;", "**OWNER ONLY (B):** `d` on `live-N`", "**OWNER ONLY (C): stop the trial (and mask a unit file).**",
		"**OWNER ONLY (C): start the trial again.**", "A full quarantine ends the sitting",
		"a start during the sitting fails closed", "A transient trial does not come back after a reboot", "never archive the state folder",
		"`holder/final.json.previous`", "`config/claude`", "A fresh `ROOT` alone does not make a fresh Claude login",
		"exit status N", "Approve only the one expected operation", "A project's approvals go through Cards",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the runbook does not say %q", want)
		}
	}
	for _, stale := range []string{"chat purge", "plain Yes", "leads/scratch-live-lead/home", "test ! -e"} {
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
			"its `--write`, `--status`, `--swap-in` and `--swap-out` are the owner's", "the `ROOT/netcheck.sh` its `--write` writes",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q", name, want)
			}
		}
		if i, j := strings.Index(text, "canary-prepare.sh` only with"), strings.Index(text, "`qualification/scratch-seat/prepare.sh`"); name == "CLAUDE.md" && (i < 0 || j < i) {
			t.Errorf("%s does not put the rule after the canary prepare's", name)
		}
	}
	agents := string(mustRead(t, filepath.Join(root(t), "AGENTS.md")))
	if !strings.Contains(agents, "-run ScratchSeatPrepare` after doctor") {
		t.Error("AGENTS.md does not name the targeted check of the script")
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
