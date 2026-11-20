package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests of canary-prepare.sh --pass B (the live set-up), of the live-agent
// checks of canary-preflight.sh, of netcheck.sh and of the pass B checklist.
// Nothing here starts a unit, a sandbox, pasta, bubblewrap or a real claude:
// the claude of these tests is a shell script that prints a version, and
// netcheck.sh is written, parsed with sh -n and never run.

const (
	passBChecklistTemplate = "qualification/canary/run/checklist-b.txt.in"
	passBNetcheckTemplate  = "qualification/canary/run/netcheck.sh.in"
	passBVersion           = "2.1.325"
)

// passBClaude writes an executable shell script that stands for the tested
// claude and prints the given version line; it records that it ran, and its
// environment (export -p: there is no PATH to find env with) when envFile is
// not empty.
func passBClaude(t *testing.T, dir, name, versionLine, calls, envFile string) string {
	t.Helper()
	body := "#!/bin/sh\n"
	if calls != "" {
		body += "echo 'claude ran' >>'" + calls + "'\n"
	}
	if envFile != "" {
		body += "export -p >'" + envFile + "'\n"
	}
	body += "echo '" + versionLine + "'\n"
	p := canaryWrite(t, dir, name, body)
	if err := os.Chmod(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func passBGoodClaude(t *testing.T, dir string) string {
	t.Helper()
	return passBClaude(t, dir, "claude-file", passBVersion+" (Claude Code)", "", "")
}

// prepareB is the argument list of a pass B prepare with the stub kit.
func (e canaryHelperEnv) prepareB(t *testing.T, root, claude string, more ...string) []string {
	t.Helper()
	return append([]string{"--pass", "B", "--claude", claude}, e.prepareArgs(t, root, more...)...)
}

func passBJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", path, err, data)
	}
	return m
}

func passBGet(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("no %v in the JSON", path)
		}
		if cur, ok = obj[k]; !ok {
			t.Fatalf("no %v in the JSON", path)
		}
	}
	return cur
}

func passBStrings(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is not a list", v)
	}
	out := []string{}
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

// --dry says what pass B would make, writes nothing and runs nothing, claude
// included.
func TestCanaryRunKitPassBPrepareDryWritesNothing(t *testing.T) {
	e := canaryStubKit(t)
	claude := passBClaude(t, e.dir, "claude-file", passBVersion+" (Claude Code)", e.calls, "")
	r := filepath.Join(e.dir, "set-up-b")
	e.pasta(t)
	before := canaryTree(t, e.dir)
	for _, c := range []struct {
		more []string
		want []string
	}{
		{nil, []string{"no --model flag in the agent command"}},
		{[]string{"--model", "sample-model"}, []string{`"--model", "sample-model"`}},
	} {
		out, code := e.run(t, canaryPrepareScript, "", e.prepareB(t, r, claude, append([]string{"--dry"}, c.more...)...)...)
		if code != 0 {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		for _, want := range append(c.want, "[dry]", "pass B, live agent", r+"/agent-install/claude", "--version", "DISABLE_AUTOUPDATER=1",
			"footer_mode manual", "sources.agent_install "+r+"/agent-install", "terminal xterm-256color, truecolor, C.UTF-8", "the environment is part of the digest",
			r+"/netcheck.sh", "checklist-b.txt.in", "canary-reset.sh", ".canary-prepared") {
			if !strings.Contains(out, want) {
				t.Errorf("the dry run does not name %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "stand-in") {
			t.Errorf("the dry run of pass B names the stand-in:\n%s", out)
		}
	}
	if after := canaryTree(t, e.dir); !slices.Equal(before, after) {
		t.Errorf("the dry run changed the folder:\nbefore %v\nafter  %v", before, after)
	}
	if calls := e.callLines(t); len(calls) != 0 {
		t.Errorf("the dry run ran %v", calls)
	}
}

// What --claude and --model must be, refused before anything is made, in the
// dry run too.
func TestCanaryRunKitPassBPrepareRefuses(t *testing.T) {
	e := canaryStubKit(t)
	good := passBClaude(t, e.dir, "claude-file", passBVersion+" (Claude Code)", e.calls, "")
	link := filepath.Join(e.dir, "claude-link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(e.dir, "claude-folder")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	plain := canaryWrite(t, e.dir, "claude-plain", "#!/bin/sh\necho x\n") // mode 0600: not executable
	r := filepath.Join(e.dir, "set-up-b")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no --claude", e.prepareArgs(t, r, "--pass", "B"), "--pass B needs --claude"},
		{"--claude for pass A", e.prepareArgs(t, r, "--claude", good), "--claude and --model are for --pass B"},
		{"--model for pass A", e.prepareArgs(t, r, "--model", "x"), "--claude and --model are for --pass B"},
		{"a pass C", e.prepareArgs(t, r, "--pass", "C"), "--pass is A or B"},
		{"a relative path", e.prepareB(t, r, "claude-file"), "not an absolute path"},
		{"a link", e.prepareB(t, r, link), "is a link"},
		{"a folder", e.prepareB(t, r, folder), "is a folder"},
		{"a missing file", e.prepareB(t, r, filepath.Join(e.dir, "nothing")), "not a regular file"},
		{"a file that is not executable", e.prepareB(t, r, plain), "not executable"},
		{"a model that starts with a dash", e.prepareB(t, r, good, "--model", "-x"), "--model is a model name"},
		{"a model with a space", e.prepareB(t, r, good, "--model", "a b"), "--model is a model name"},
		{"a model with a quote", e.prepareB(t, r, good, "--model", `a"b`), "--model is a model name"},
		{"a model with a semicolon", e.prepareB(t, r, good, "--model", "a;b"), "--model is a model name"},
		{"a model that is too long", e.prepareB(t, r, good, "--model", strings.Repeat("m", 65)), "longer than 64"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, dry := range [][]string{nil, {"--dry"}} {
				out, code := e.run(t, canaryPrepareScript, "", append(slices.Clone(c.args), dry...)...)
				if code == 0 || !strings.Contains(out, c.want) {
					t.Errorf("dry %v: exit status %d, want a refusal naming %q:\n%s", dry, code, c.want, out)
				}
			}
			if calls := e.callLines(t); len(calls) != 0 {
				t.Errorf("a refusal ran %v", calls)
			}
			if _, err := os.Stat(r); err == nil {
				t.Error("a refusal made the root")
			}
		})
	}
}

// The copy's --version must name the tested version as a whole word; the output
// is shown, the root has no mark, and nothing else was made or run.
func TestCanaryRunKitPassBPrepareRefusesAWrongVersion(t *testing.T) {
	e := canaryStubKit(t)
	for _, c := range []struct{ name, body string }{
		{"a newer version", "#!/bin/sh\necho '2.1.329 (Claude Code)'\n"},
		{"a longer number", "#!/bin/sh\necho '12.1.3250 (Claude Code)'\n"},
		{"no output", "#!/bin/sh\nexit 0\n"},
		{"the version and a failure", "#!/bin/sh\necho '2.1.325 (Claude Code)'\nexit 3\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := canaryWrite(t, e.dir, "claude-"+strings.ReplaceAll(c.name, " ", "-"), c.body)
			if err := os.Chmod(f, 0o700); err != nil {
				t.Fatal(err)
			}
			r := filepath.Join(e.dir, "set-up-"+strings.ReplaceAll(c.name, " ", "-"))
			out, code := e.run(t, canaryPrepareScript, "", e.prepareB(t, r, f)...)
			if code == 0 || !strings.Contains(out, "does not name the tested version "+passBVersion) {
				t.Fatalf("exit status %d, want the refusal:\n%s", code, out)
			}
			if !strings.Contains(out, "claude --version (the copy "+r+"/agent-install/claude, exit status") {
				t.Errorf("the output of the copy's --version is not shown:\n%s", out)
			}
			if c.name == "a newer version" && !strings.Contains(out, "2.1.329 (Claude Code)") {
				t.Errorf("the printed version is not shown:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(r, ".canary-prepared")); err == nil {
				t.Error("a refused prepare left the mark")
			}
			if _, err := os.Stat(filepath.Join(r, "config.json")); err == nil {
				t.Error("the config was written before the version was known")
			}
			if calls := e.callLines(t); len(calls) != 0 {
				t.Errorf("the refusal ran %v", calls)
			}
		})
	}
}

// The live set-up: the config fields, the inputs, the copy, the modes, the pin.
func TestCanaryRunKitPassBPrepareMakesTheLiveSetUp(t *testing.T) {
	e := canaryStubKit(t)
	envFile := filepath.Join(e.dir, "claude-env")
	claude := passBClaude(t, e.dir, "claude-file", passBVersion+" (Claude Code)", "", envFile)
	// A pasta that records a call: netcheck.sh must never be run by prepare.
	pasta := canaryWrite(t, e.dir, "pasta-recording", "#!/bin/sh\necho \"pasta $*\" >>'"+e.calls+"'\n")
	if err := os.Chmod(pasta, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"", "sample-model"} {
		r := filepath.Join(e.dir, "set-up-b"+model)
		args := []string{"--pass", "B", "--claude", claude, "--root", r, "--bin", e.bin, "--pasta", pasta, "--bwrap", "/usr/bin/bwrap"}
		if model != "" {
			args = append(args, "--model", model)
		}
		out, code := e.run(t, canaryPrepareScript, "", args...)
		if code != 0 {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		for _, want := range []string{"claude copy: " + r + "/agent-install/claude names " + passBVersion, "sh " + r + "/netcheck.sh",
			"canary-preflight.sh --root " + r, "canary-run.sh --pass B --config " + r + "/config.json"} {
			if !strings.Contains(out, want) {
				t.Errorf("the output lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "--pass A") {
			t.Errorf("the output of pass B names --pass A:\n%s", out)
		}
		modes := map[string]os.FileMode{
			"":                           0o700,
			"agent-install/claude":       0o700,
			"config.json":                0o600,
			"conf/sandbox-inputs.json":   0o600,
			"conf/managed-settings.json": 0o600,
			"conf/guard-hook":            0o700,
			"conf/replay-screens/1.json": 0o600,
			"conf/replay-screens/2.txt":  0o600,
			"bin/ataniq-probe":           0o700,
			"netcheck.sh":                0o700,
			"CHECKLIST.txt":              0o600,
			"answers.hint":               0o600,
			"mirror-qual/config.json":    0o600,
			".canary-prepared":           0o600,
		}
		for name, want := range modes {
			if got := canaryMode(t, filepath.Join(r, name)); got != want {
				t.Errorf("%s has mode %o, want %o", name, got, want)
			}
		}
		if _, err := os.Lstat(filepath.Join(r, "agent-install/ataniq-canary-standin")); err == nil {
			t.Error("pass B copied the stand-in")
		}
		src, _ := os.ReadFile(claude)
		if got, err := os.ReadFile(filepath.Join(r, "agent-install/claude")); err != nil || string(got) != string(src) {
			t.Errorf("agent-install/claude is not a copy of the claude given: %v", err)
		}

		cfg := passBJSON(t, filepath.Join(r, "config.json"))
		if got := passBGet(t, cfg, "agent", "kind"); got != "live" {
			t.Errorf("agent.kind is %v", got)
		}
		wantArgv := []string{"/ataniq/agent/claude"}
		if model != "" {
			wantArgv = append(wantArgv, "--model", model)
		}
		if got := passBStrings(t, passBGet(t, cfg, "agent", "argv")); !slices.Equal(got, wantArgv) {
			t.Errorf("agent.argv is %v, want %v", got, wantArgv)
		}
		if got := passBStrings(t, passBGet(t, cfg, "agent", "env")); !slices.Equal(got, []string{"DISABLE_AUTOUPDATER=1"}) {
			t.Errorf("agent.env is %v", got)
		}
		for path, want := range map[string]any{"footer_mode": "manual", "runtime": "claude", "runtime_version": passBVersion} {
			if got := passBGet(t, cfg, "agent", path); got != want {
				t.Errorf("agent.%s is %v, want %v", path, got, want)
			}
		}
		if got := passBGet(t, cfg, "inject"); got != true {
			t.Errorf("inject is %v", got)
		}
		if got := passBGet(t, cfg, "holder", "profile_digest"); got != canaryStubDigest {
			t.Errorf("the pin is %v, not the digest the helper printed", got)
		}
		if got := passBGet(t, cfg, "provision", "replay_screens"); got != r+"/conf/replay-screens" {
			t.Errorf("provision.replay_screens is %v", got)
		}
		if got := passBGet(t, cfg, "limits", "memory_max_bytes"); got != float64(4294967296) {
			t.Errorf("limits.memory_max_bytes is %v", got)
		}

		in := passBJSON(t, filepath.Join(r, "conf/sandbox-inputs.json"))
		if got := passBGet(t, in, "sources", "agent_install"); got != r+"/agent-install" {
			t.Errorf("sources.agent_install is %v", got)
		}
		if got := passBGet(t, in, "runtime_version"); got != passBVersion {
			t.Errorf("the inputs declare version %v", got)
		}
		term := passBGet(t, in, "terminal").(map[string]any)
		if term["term"] != "xterm-256color" || term["colorterm"] != "truecolor" || term["lang"] != "C.UTF-8" {
			t.Errorf("terminal is %v", term)
		}
		ms, _ := os.ReadFile(filepath.Join(r, "conf/managed-settings.json"))
		if !strings.Contains(string(ms), `"defaultMode":"default"`) || !strings.Contains(string(ms), "guard-hook") {
			t.Errorf("the managed settings are %s", ms)
		}

		// The version was asked as step 9 asks it: no PATH, a home that is a new folder.
		envText, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatalf("the claude copy was not run: %v", err)
		}
		if strings.Contains(string(envText), "PATH=") || !strings.Contains(string(envText), "HOME=") {
			t.Errorf("claude --version ran with this environment:\n%s", envText)
		}
		os.Remove(envFile)
	}
	// Prepare never runs netcheck.sh: pasta is the first thing it would start.
	for _, c := range e.callLines(t) {
		if strings.HasPrefix(c, "pasta") {
			t.Errorf("prepare started pasta: %q", c)
		}
	}
}

// netcheck.sh: written, 0700, parseable by the dash shim's sh -n, one line per
// host and per version check, and never run by the kit.
func TestCanaryRunKitPassBNetcheckIsWrittenAndNeverRun(t *testing.T) {
	e := canaryStubKit(t)
	claude := passBGoodClaude(t, e.dir)
	r := filepath.Join(e.dir, "set-up-b")
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareB(t, r, claude)...); code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	path := filepath.Join(r, "netcheck.sh")
	if got := canaryMode(t, path); got != 0o700 {
		t.Errorf("netcheck.sh has mode %o, want 700", got)
	}
	if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Errorf("sh -n netcheck.sh: %v\n%s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"#!/bin/sh", "SELF='" + r + "/netcheck.sh'", "PASTA='" + e.dir + "/pasta-file'", "BWRAP='/usr/bin/bwrap'",
		"INSTALL='" + r + "/agent-install'", "VERSION='" + passBVersion + "'",
		"api.anthropic.com", "claude.ai", "platform.claude.com", "console.anthropic.com",
		"-f -q --config-net -t none -u none -T none -U none --no-map-gw", "--unshare-all --share-net --unshare-user --uid 0 --gid 0", "--cap-drop ALL", "--remount-ro /",
		"--ro-bind \"$INSTALL\" /ataniq/agent", "/ataniq/agent/claude --version",
		"systemd-run --user --wait --collect --pipe", "MemoryMax=4294967296", "MemorySwapMax=0", "TasksMax=512",
		"PASS     ", "FINDING  ", "curl -sS -m 15 -o /dev/null",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("netcheck.sh does not hold %q", want)
		}
	}
	// No request but the four GETs: no POST, no credential, no other host.
	for _, bad := range []string{"-X ", "--data", "-d ", "Authorization", "token", "-H "} {
		if strings.Contains(text, bad) {
			t.Errorf("netcheck.sh holds %q", bad)
		}
	}
	if hosts := regexp.MustCompile(`https?://[A-Za-z0-9.$"{}]+`).FindAllString(text, -1); len(hosts) != 1 || hosts[0] != `https://$h` {
		t.Errorf("the URLs in netcheck.sh are %v", hosts)
	}
	// No private name.
	for _, bad := range []string{"/home/", "/Users/", ".ssh"} {
		if strings.Contains(strings.ReplaceAll(text, e.dir, ""), bad) {
			t.Errorf("netcheck.sh holds %q", bad)
		}
	}
	// Neither prepare nor any other script of the kit runs it: only prepare
	// names it, to write it, parse it (sh -n) and say what the owner types.
	for _, rel := range []string{canaryRunScript, canaryPreflightScript, canaryResetScript, canaryLibScript} {
		if strings.Contains(readFileText(t, rel), "netcheck") {
			t.Errorf("%s names netcheck.sh", rel)
		}
	}
	runs := regexp.MustCompile(`([;&|{(]|\bthen|\bdo|\belse) *(sh|bash|dash|exec|\.)\s+"?\$(NETCHECK|ROOT/netcheck)`)
	for _, l := range strings.Split(readFileText(t, canaryPrepareScript), "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "netcheck") && !strings.HasPrefix(l, "#") && runs.MatchString(l) {
			t.Errorf("prepare may run netcheck.sh: %q", l)
		}
	}
}

// The reset takes a pass B root: it carries the same mark and holds the same
// folders; the copy of claude is kept.
func TestCanaryRunKitPassBRootCanBeReset(t *testing.T) {
	e := canaryStubKit(t)
	claude := passBGoodClaude(t, e.dir)
	r := filepath.Join(e.dir, "set-up-b")
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareB(t, r, claude)...); code != 0 {
		t.Fatalf("prepare: exit status %d:\n%s", code, out)
	}
	canaryWrite(t, r, "state/leads/throwaway-one-lead/home/file", "x")
	before := canaryTree(t, e.dir)
	out, code := e.run(t, canaryResetScript, "", "--root", r, "--bin", e.bin, "--dry")
	if code != 0 {
		t.Fatalf("reset --dry on a pass B root: exit status %d:\n%s", code, out)
	}
	for _, want := range []string{"DELETE", r + "/state", r + "/runtime", "[dry] nothing was deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	if after := canaryTree(t, e.dir); !slices.Equal(before, after) {
		t.Errorf("the dry reset changed the folder:\nbefore %v\nafter  %v", before, after)
	}
	// A root without the mark is still refused, as for pass A.
	if err := os.Remove(filepath.Join(r, ".canary-prepared")); err != nil {
		t.Fatal(err)
	}
	if out, code := e.run(t, canaryResetScript, "", "--root", r, "--bin", e.bin, "--dry"); code == 0 {
		t.Errorf("a root without the mark was accepted:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// The pass B checklist.

func TestCanaryRunKitPassBChecklistTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), passBChecklistTemplate))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > 60 {
		t.Errorf("the checklist has %d lines, want one page (60 at most)", len(lines))
	}
	for i, l := range lines {
		if len([]rune(l)) > 80 {
			t.Errorf("line %d is %d columns wide: %q", i+1, len([]rune(l)), l)
		}
	}
	for _, bad := range []string{"/home/", "/Users/", "/root/", "$HOME", "~/", ".ssh", ".claude", ".codex"} {
		if strings.Contains(text, bad) {
			t.Errorf("the template holds %q", bad)
		}
	}
	// The placeholders are the ones prepare fills.
	known := map[string]bool{"ROOT": true, "REPO": true, "BIN": true, "CONFIG": true, "EVIDENCE": true, "HINTS": true, "HOLDER_RUN": true,
		"RUN": true, "PREFLIGHT": true, "RESET": true, "NETCHECK": true, "MODELNOTE": true, "FOOTER": true}
	for _, m := range canaryPlaceholder.FindAllStringSubmatch(text, -1) {
		if !known[m[1]] {
			t.Errorf("the template holds @%s@, which prepare does not fill", m[1])
		}
	}
	// What it must tell the owner: the order, the pass B commands, the steps
	// pass B adds (one line each, with what he types) and the advice.
	for _, want := range []string{
		"sh @NETCHECK@", "sh @PREFLIGHT@ --root @ROOT@", "sh @RUN@ --pass B --config @CONFIG@ --dry", "sh @RUN@ --pass B --config @CONFIG@ --evidence @EVIDENCE@",
		"qualify-mirror for THIS root", "mirror-qual/report.json", "--mirror-report", "T3 the console", "confirmed", "logged-in", "logged-out", "revoked", "plain Yes", `never "do not ask again"`,
		"window large", "never \"log out", "@MODELNOTE@", `mode "@FOOTER@"`, "@RESET@",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the template does not say %q", want)
		}
	}
	for _, n := range []string{"5", "6", "7", "15", "16", "21", "23", "24", "25"} {
		if !regexp.MustCompile(`(?m)^ {1,2}` + n + ` {2,3}\S`).MatchString(text) {
			t.Errorf("the template has no line for step %s", n)
		}
	}
}

// Prepare fills it: no placeholder left, one page, the mode of the config.
func TestCanaryRunKitPassBPrepareWritesTheChecklist(t *testing.T) {
	e := canaryStubKit(t)
	claude := passBGoodClaude(t, e.dir)
	for _, model := range []string{"", "sample-model"} {
		r := filepath.Join(e.dir, "set-up-b"+model)
		args := e.prepareB(t, r, claude)
		if model != "" {
			args = append(args, "--model", model)
		}
		if out, code := e.run(t, canaryPrepareScript, "", args...); code != 0 {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		data, err := os.ReadFile(filepath.Join(r, "CHECKLIST.txt"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if canaryPlaceholder.MatchString(text) {
			t.Errorf("a placeholder is left:\n%s", text)
		}
		if n := len(strings.Split(strings.TrimSuffix(text, "\n"), "\n")); n > 60 {
			t.Errorf("the page has %d lines", n)
		}
		for _, want := range []string{"Root " + r, "sh " + r + "/netcheck.sh", "--pass B --config " + r + "/config.json", "mode \"manual\"",
			"rm -rf -- " + r + "/state " + r + "/runtime"} {
			if !strings.Contains(text, want) {
				t.Errorf("the page lacks %q:\n%s", want, text)
			}
		}
		if model == "" && !strings.Contains(text, "Model: none chosen. Prepare again with --model NAME") {
			t.Errorf("the page does not say that no model was chosen:\n%s", text)
		}
		if model != "" && !strings.Contains(text, "Model: "+model+" (--model)") {
			t.Errorf("the page does not name the model:\n%s", text)
		}
	}
}

// ---------------------------------------------------------------------------
// canary-preflight.sh for a pass B root, in the preflight's stub harness: the
// helper's show prints the live agent's names (never a value).

// passBPF makes the harness's prepared root a pass B one: a live agent in the
// show output, the install folder in the inputs, and a claude copy.
func passBPF(t *testing.T) (*canaryPF, string) {
	t.Helper()
	p := newCanaryPF(t)
	show := "holder_runtime_dir=" + p.root + "/runtime/leads/throwaway-one-lead\nproject=throwaway-one\nagent_kind=live\nagent_runtime_version=" + passBVersion +
		"\nagent_footer_mode=manual\nagent_argv_0=/ataniq/agent/claude\nagent_env_names=DISABLE_AUTOUPDATER\n"
	canaryWrite(t, p.dir, "show.out", show)
	canaryWrite(t, p.root, "conf/sandbox-inputs.json.fields", "runtime=claude\nsources.managed_marker=guard-hook\nsources.agent_install="+p.root+"/agent-install\n")
	claude := passBClaude(t, filepath.Join(p.root, "agent-install"), "claude", passBVersion+" (Claude Code)", "", "")
	p.age(time.Now())
	return p, claude
}

func TestCanaryRunKitPassBPreflightChecksTheLiveAgent(t *testing.T) {
	p, _ := passBPF(t)
	out, code := p.run()
	if code != 0 {
		t.Fatalf("exit status %d, want READY:\n%s", code, out)
	}
	for _, want := range []string{
		"ok:   agent.footer_mode is manual, a name the screen recogniser reports",
		"ok:   agent.env names DISABLE_AUTOUPDATER (names only; no value is read)",
		"ok:   the claude copy prints the tested version " + passBVersion,
		"READY", "canary-run.sh --pass B --config " + p.root + "/config.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the preflight lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--pass A") {
		t.Errorf("the preflight of a pass B root names --pass A:\n%s", out)
	}
}

func TestCanaryRunKitPassBPreflightFixes(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(p *canaryPF)
		want   string
	}{
		{"a footer text for a mode", func(p *canaryPF) {
			canaryWrite(t, p.dir, "show.out", strings.ReplaceAll(readPassB(t, p.dir, "show.out"), "agent_footer_mode=manual", "agent_footer_mode=manual mode on"))
		}, `agent.footer_mode is "manual mode on", not manual or auto`},
		{"the auto-update switch missing", func(p *canaryPF) {
			canaryWrite(t, p.dir, "show.out", strings.ReplaceAll(readPassB(t, p.dir, "show.out"), "DISABLE_AUTOUPDATER", "OTHER_NAME"))
		}, "agent.env does not name DISABLE_AUTOUPDATER"},
		{"a wrapper outside the bind", func(p *canaryPF) {
			canaryWrite(t, p.dir, "show.out", strings.ReplaceAll(readPassB(t, p.dir, "show.out"), "/ataniq/agent/claude", "/usr/bin/claude"))
		}, "agent.argv[0] is not a file under /ataniq/agent/"},
		{"no claude copy", func(p *canaryPF) { os.Remove(filepath.Join(p.root, "agent-install/claude")) }, "the claude copy is not an executable regular file"},
		{"a link as the claude copy", func(p *canaryPF) {
			target := passBClaude(t, p.dir, "elsewhere", passBVersion, "", "")
			os.Remove(filepath.Join(p.root, "agent-install/claude"))
			if err := os.Symlink(target, filepath.Join(p.root, "agent-install/claude")); err != nil {
				t.Fatal(err)
			}
		}, "the claude copy is not an executable regular file"},
		{"a copy that is not executable", func(p *canaryPF) { os.Chmod(filepath.Join(p.root, "agent-install/claude"), 0o600) }, "the claude copy is not an executable regular file"},
		{"the wrong version", func(p *canaryPF) {
			passBClaude(t, filepath.Join(p.root, "agent-install"), "claude", "2.1.329 (Claude Code)", "", "")
		}, "does not print the tested version " + passBVersion},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := passBPF(t)
			c.mutate(p)
			out, code := p.run()
			if code == 0 || !strings.Contains(out, "FIX:") || !strings.Contains(out, c.want) || !strings.Contains(out, "NOT READY (1 to fix)") {
				t.Errorf("exit status %d, want one FIX naming %q:\n%s", code, c.want, out)
			}
			if strings.Contains(out, "--pass A") {
				t.Errorf("names --pass A:\n%s", out)
			}
		})
	}
}

func readPassB(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A pass A root draws none of the live lines.
func TestCanaryRunKitPassBPreflightLeavesPassAAlone(t *testing.T) {
	p := newCanaryPF(t)
	out, code := p.run()
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	for _, bad := range []string{"agent.footer_mode", "DISABLE_AUTOUPDATER", "the claude copy"} {
		if strings.Contains(out, bad) {
			t.Errorf("the preflight of a pass A root says %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "--pass A") {
		t.Errorf("the preflight of a pass A root does not name --pass A:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// With the real tagged binaries: the pin is the digest the helper computes for
// the finished files, the environment is in it, and the live checks hold.

// passBRealKit is the real kit with what a pass B prepare also copies.
func passBRealKit(t *testing.T) *canaryPF {
	t.Helper()
	p := canaryRealKit(t)
	for _, name := range []string{"checklist-b.txt.in", "netcheck.sh.in"} {
		data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run", name))
		if err != nil {
			t.Fatal(err)
		}
		canaryWrite(t, p.runDir, name, string(data))
	}
	for _, name := range []string{"rmdir", "head"} {
		if _, err := os.Lstat(filepath.Join(p.tools, name)); err == nil {
			continue
		}
		if real, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(real, filepath.Join(p.tools, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return p
}

// passBTool runs one of the built tagged binaries and returns its output and
// exit status.
func passBTool(t *testing.T, p *canaryPF, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(p.bin, "ataniq-canary"), args...)
	cmd.Dir = p.dir
	cmd.Env = []string{"PATH=" + p.tools, "HOME=" + filepath.Join(p.dir, "home")}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

func TestCanaryRunKitPassBPrepareWithTheRealBinaries(t *testing.T) {
	p := passBRealKit(t)
	claudeDir := filepath.Join(p.dir, "claude-src")
	claude := passBClaude(t, claudeDir, "claude-file", passBVersion+" (Claude Code)", "", "")
	r := filepath.Join(p.dir, "real-root-b")
	cmd := exec.Command(filepath.Join(p.tools, "sh"), filepath.Join(p.runDir, "canary-prepare.sh"),
		"--pass", "B", "--claude", claude, "--model", "sample-model", "--root", r, "--pasta", filepath.Join(p.tools, "pasta"), "--bwrap", filepath.Join(p.tools, "bwrap"))
	cmd.Dir = p.dir
	cmd.Env = []string{"PATH=" + p.tools, "HOME=" + filepath.Join(p.dir, "home"), "TMPDIR=" + p.tmp}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare --pass B with the real binaries: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "check-inputs: the inputs match the config") {
		t.Errorf("prepare did not say the inputs match:\n%s", out)
	}
	config, inputs := filepath.Join(r, "config.json"), filepath.Join(r, "conf/sandbox-inputs.json")

	// The pin is the digest of the files as they are now.
	digest := func() string {
		out, code := passBTool(t, p, "profile-digest", "--config", config, "--inputs", inputs)
		if code != 0 {
			t.Fatalf("profile-digest: exit status %d:\n%s", code, out)
		}
		return strings.TrimSpace(out)
	}
	pin := passBGet(t, passBJSON(t, config), "holder", "profile_digest")
	if got := digest(); got != pin {
		t.Errorf("the pin is %v, the digest of the finished files is %s", pin, got)
	}
	if out, code := passBTool(t, p, "check-inputs", "--config", config, "--inputs", inputs); code != 0 {
		t.Errorf("check-inputs: exit status %d:\n%s", code, out)
	}
	// The environment is part of the digest: taking the switch out of agent.env
	// changes it, which is why the pin is last.
	original, _ := os.ReadFile(config)
	without := strings.Replace(string(original), `"env": ["DISABLE_AUTOUPDATER=1"]`, `"env": []`, 1)
	if without == string(original) {
		t.Fatal("the config does not hold the env line the test edits")
	}
	if err := os.WriteFile(config, []byte(without), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := digest(); got == pin {
		t.Error("the digest does not depend on agent.env")
	}
	if err := os.WriteFile(config, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// show prints the names, never the values.
	show, code := passBTool(t, p, "show", "--config", config)
	if code != 0 {
		t.Fatalf("show: exit status %d:\n%s", code, show)
	}
	for _, want := range []string{"agent_kind=live\n", "agent_footer_mode=manual\n", "agent_env_names=DISABLE_AUTOUPDATER\n", "agent_argv_0=/ataniq/agent/claude\n", "agent_argv_2=sample-model\n"} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	if strings.Contains(show, "DISABLE_AUTOUPDATER=1") {
		t.Errorf("show prints a value of the environment:\n%s", show)
	}

	// The refusals of the validation: a footer text, a wrapper, a file that is not there.
	edit := func(from, to string) string {
		t.Helper()
		bad := strings.Replace(string(original), from, to, 1)
		if bad == string(original) {
			t.Fatalf("the config does not hold %q", from)
		}
		path := filepath.Join(p.dir, "edited-config.json")
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct{ from, to, want string }{
		{`"footer_mode": "manual"`, `"footer_mode": "manual mode on"`, `"manual" or "auto"`},
		{`"footer_mode": "manual"`, `"footer_mode": "sample mode on"`, `"manual" or "auto"`},
		{`"/ataniq/agent/claude"`, `"/usr/bin/claude"`, "agent.argv[0]"},
	} {
		out, code := passBTool(t, p, "show", "--config", edit(c.from, c.to))
		if code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("show of a config with %s: exit status %d, want a refusal naming %q:\n%s", c.to, code, c.want, out)
		}
	}
	if out, code := passBTool(t, p, "check-inputs", "--config", edit(`"footer_mode": "manual"`, `"footer_mode": "auto"`), "--inputs", inputs); code != 0 {
		t.Errorf("footer_mode auto is a valid value: exit status %d:\n%s", code, out)
	}
	// A wrapper that is not the install folder's file is refused by check-inputs.
	wrapper := edit(`"/ataniq/agent/claude"`, `"/ataniq/agent/wrapper.sh"`)
	if out, code := passBTool(t, p, "check-inputs", "--config", wrapper, "--inputs", inputs); code == 0 || !strings.Contains(out, "agent.argv[0]") {
		t.Errorf("check-inputs of a config whose agent is not in the install folder: exit status %d:\n%s", code, out)
	}

	// The preflight on this root gets past the live checks.
	pre, _ := p.runRaw("--root", r)
	for _, want := range []string{
		"ok:   agent.footer_mode is manual",
		"ok:   agent.env names DISABLE_AUTOUPDATER",
		"ok:   the claude copy prints the tested version " + passBVersion,
	} {
		if !strings.Contains(pre, want) {
			t.Errorf("the preflight lacks %q:\n%s", want, pre)
		}
	}
	// A newer claude in the install folder, and an environment without the
	// switch pinned afresh: each is a FIX of its own.
	passBClaude(t, filepath.Join(r, "agent-install"), "claude", "2.1.329 (Claude Code)", "", "")
	if pre, _ := p.runRaw("--root", r); !strings.Contains(pre, "does not print the tested version "+passBVersion) {
		t.Errorf("the preflight does not refuse the newer claude:\n%s", pre)
	}
	passBClaude(t, filepath.Join(r, "agent-install"), "claude", passBVersion+" (Claude Code)", "", "")
	if err := os.WriteFile(config, []byte(without), 0o600); err != nil {
		t.Fatal(err)
	}
	newPin := digest()
	repinned := strings.Replace(without, `"profile_digest": "`+pin.(string)+`"`, `"profile_digest": "`+newPin+`"`, 1)
	if err := os.WriteFile(config, []byte(repinned), 0o600); err != nil {
		t.Fatal(err)
	}
	if pre, _ := p.runRaw("--root", r); !strings.Contains(pre, "agent.env does not name DISABLE_AUTOUPDATER") {
		t.Errorf("the preflight does not refuse an agent.env without the switch:\n%s", pre)
	}
}

// ---------------------------------------------------------------------------
// The docs.

func TestCanaryRunKitPassBDocs(t *testing.T) {
	readme := readFileText(t, "qualification/canary/run/README.md")
	slice := readFileText(t, "docs/canary-slice.md")
	for _, want := range []string{
		"### The set-up of pass B", "canary-prepare.sh --pass B --claude", "netcheck.sh", "canary-preflight.sh --root", "--pass B --config",
		"sources.agent_install", "pin comes last", "DISABLE_AUTOUPDATER", "no wrapper", "`manual` or `auto`", "defaultMode",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("the kit README does not mention %q", want)
		}
	}
	for _, doc := range map[string]string{"the kit README": readme, "docs/canary-slice.md": slice} {
		if strings.Contains(doc, `"footer_mode": "sample mode on"`) {
			t.Error("a document still has the footer_mode sample \"sample mode on\"")
		}
		if strings.Contains(doc, "the live agent's install folder, version") {
			t.Error("a document still says the config holds the install folder")
		}
	}
	if !strings.Contains(slice, "`manual` or `auto`") {
		t.Error("docs/canary-slice.md does not say what footer_mode takes")
	}
}

// The checklist of a fresh pass B root names qualify-mirror and the root's own
// report path, which the preflight wants.
func TestCanaryRunKitPassBChecklistNamesQualifyMirror(t *testing.T) {
	e := canaryStubKit(t)
	claude := passBGoodClaude(t, e.dir)
	r := filepath.Join(e.dir, "set-up-b")
	out, code := e.run(t, canaryPrepareScript, "", e.prepareB(t, r, claude)...)
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	pageData, err := os.ReadFile(filepath.Join(r, "CHECKLIST.txt"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageData)
	for name, text := range map[string]string{"the checklist": page, "the printed next steps": out} {
		for _, want := range []string{"qualify-mirror", r + "/mirror-qual/config.json", r + "/mirror-qual/report.json", "--mirror-report", "i_run_this_on_purpose"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, text)
			}
		}
		if strings.Contains(text, "same files") {
			t.Errorf("%s says the pass A files serve", name)
		}
	}
	if _, err := os.Stat(filepath.Join(r, "mirror-qual", "config.json")); err != nil {
		t.Errorf("prepare wrote no mirror-qual/config.json: %v", err)
	}
	if !strings.Contains(page, "the pin does not cover argv") {
		t.Errorf("the checklist does not say how to add a model:\n%s", page)
	}
}

// netcheck.sh binds each path the profile marks optional only where it exists.
func TestCanaryRunKitPassBNetcheckBindsOptionalPathsOnlyWhenPresent(t *testing.T) {
	text := readFileText(t, passBNetcheckTemplate)
	for _, p := range []string{"/etc/ssl", "/etc/ca-certificates", "/etc/nsswitch.conf", "/usr/lib64"} {
		if !strings.Contains(text, "[ -e "+p+" ] &&") {
			t.Errorf("the bind of %s is not conditional on the path", p)
		}
	}
	for _, bad := range []string{"--ro-bind /etc/ssl /etc/ssl \\", "--symlink usr/lib64 /lib64 \\"} {
		if strings.Contains(text, bad) {
			t.Errorf("an unconditional bind is left: %q", bad)
		}
	}
}

func readFileText(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
