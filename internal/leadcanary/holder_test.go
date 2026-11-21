//go:build linux && ataniq_canary

package leadcanary_test

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

const sampleIntent = "0123456789abcdef0123456789abcdef"

func sampleEntryIntent(role leadrole.Role, epoch uint64) (leadregistry.Entry, leadregistry.Intent) {
	return leadregistry.Entry{Role: role, Epoch: epoch},
		leadregistry.Intent{ID: sampleIntent, Epoch: epoch, UnitName: role.UnitName()}
}

func newBuilder(t *testing.T, f fixture, mut func(*leadcanary.Config)) (*leadcanary.HolderCommand, leadcanary.Config) {
	t.Helper()
	cfg := f.config(t)
	if mut != nil {
		mut(&cfg)
	}
	h, err := leadcanary.NewHolderCommand(cfg, filepath.Join(f.state, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

// TestHolderCommandExactVector holds the argument vector against the list of
// section 5 of the seams note, written out here by hand.
func TestHolderCommandExactVector(t *testing.T) {
	f := newFixture(t)
	h, cfg := newBuilder(t, f, nil)
	role := cfg.Role
	entry, intent := sampleEntryIntent(role, 7)
	argv, workdir, _, err := h.Build(role, entry, intent)
	if err != nil {
		t.Fatal(err)
	}
	lead := filepath.Join(f.state, "leads", "throwaway-one-lead")
	want := []string{
		"/opt/sample/holder",
		"--role", "throwaway-one/lead",
		"--epoch", "7",
		"--launch-intent", sampleIntent,
		"--runtime", "sample-runtime",
		"--runtime-version", "1.2.3",
		"--profile-digest", sampleDigest,
		"--tree", filepath.Join(lead, "tree"),
		"--state-dir", filepath.Join(lead, "holder"),
		"--runtime-dir", filepath.Join(f.runtime, "leads", "throwaway-one-lead"),
		"--role-lock", filepath.Join(f.state, "roles", "throwaway-one", "lead.lock"),
		"--daemon-lock", filepath.Join(f.state, "daemon.lock"),
		"--cols", "120",
		"--rows", "40",
		"--canary",
		"--inject",
		"--sandbox-inputs", cfg.Holder.SandboxInputs,
		"--expected-mode", "sample mode on",
		"--env", "SAMPLE_ONE=1",
		"--env", "SAMPLE_TWO=two words",
		"--",
		"/opt/sample/agent", "--flag", "value with space",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", argv, want)
	}
	if workdir != filepath.Join(lead, "holder") {
		t.Fatalf("workdir %q, want the holder's state folder", workdir)
	}
	if !filepath.IsAbs(argv[0]) {
		t.Fatalf("the executable %q is not absolute", argv[0])
	}
}

func TestHolderCommandInjectOnlyWhenConfigured(t *testing.T) {
	f := newFixture(t)
	h, cfg := newBuilder(t, f, func(c *leadcanary.Config) { c.Inject = false })
	entry, intent := sampleEntryIntent(cfg.Role, 1)
	argv, _, _, err := h.Build(cfg.Role, entry, intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range argv {
		if a == "--inject" {
			t.Fatalf("--inject given although the config says inject: false: %q", argv)
		}
	}
	if !contains(argv, "--canary") {
		t.Fatalf("--canary missing: %q", argv)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestHolderCommandEnvironmentIsExplicitAndMinimal(t *testing.T) {
	f := newFixture(t)
	t.Setenv("HOME", "/nowhere/home")
	t.Setenv("XDG_STATE_HOME", "/nowhere/state")
	t.Setenv("XDG_RUNTIME_DIR", "/nowhere/run")
	t.Setenv("SAMPLE_INHERITED", "must not appear")
	h, cfg := newBuilder(t, f, nil)
	entry, intent := sampleEntryIntent(cfg.Role, 2)
	_, _, env, err := h.Build(cfg.Role, entry, intent)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env %q, want %q", env, want)
	}
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if _, inherited := os.LookupEnv(key); inherited && key != "PATH" && key != "LANG" {
			t.Fatalf("%s is inherited", key)
		}
		if strings.Contains(kv, "nowhere") || strings.Contains(kv, "inherited") {
			t.Fatalf("a variable of the caller's environment leaked: %q", kv)
		}
	}
	// The agent's variables are flags and not part of the holder's environment.
	for _, kv := range env {
		if strings.HasPrefix(kv, "SAMPLE_") {
			t.Fatalf("the agent's variable %q is in the holder's environment", kv)
		}
	}
	// What is returned is a copy.
	env[0] = "PATH=/tampered"
	if got := leadcanary.HolderEnvironment(); got[0] != want[0] {
		t.Fatalf("the shared environment was changed: %q", got)
	}
	_, _, env2, _ := h.Build(cfg.Role, entry, intent)
	if env2[0] != want[0] {
		t.Fatalf("a second Build saw a changed environment: %q", env2)
	}
}

func TestHolderCommandUsesTheDaemonsLock(t *testing.T) {
	f := newFixture(t)
	lock := filepath.Join(f.dir, "daemon-elsewhere.lock")
	cfg := f.config(t)
	h, err := leadcanary.NewHolderCommand(cfg, lock)
	if err != nil {
		t.Fatal(err)
	}
	entry, intent := sampleEntryIntent(cfg.Role, 3)
	argv, _, _, err := h.Build(cfg.Role, entry, intent)
	if err != nil {
		t.Fatal(err)
	}
	if flagValue(t, argv, "--daemon-lock") != lock {
		t.Fatalf("--daemon-lock %q, want %q", flagValue(t, argv, "--daemon-lock"), lock)
	}
	if flagValue(t, argv, "--role-lock") != cfg.Role.LockPath(cfg.StateRoot) {
		t.Fatalf("--role-lock %q", flagValue(t, argv, "--role-lock"))
	}
	if flagValue(t, argv, "--role-lock") == lock {
		t.Fatal("the two locks are the same file")
	}
}

func flagValue(t *testing.T, argv []string, name string) string {
	t.Helper()
	for i, a := range argv {
		if a == "--" {
			break
		}
		if a == name && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	t.Fatalf("no %s in %q", name, argv)
	return ""
}

func TestHolderCommandRefusals(t *testing.T) {
	f := newFixture(t)
	h, cfg := newBuilder(t, f, nil)
	role := cfg.Role
	other := mustRole(t, "throwaway-two/lead")
	good := func() (leadregistry.Entry, leadregistry.Intent) { return sampleEntryIntent(role, 4) }
	type mod func(role *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent)
	cases := map[string]mod{
		"another role":         func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { *r = other },
		"another record":       func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { e.Role = other },
		"epoch 0":              func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { e.Epoch, in.Epoch = 0, 0 },
		"record epoch differs": func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { e.Epoch = 9 },
		"another unit":         func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { in.UnitName = other.UnitName() },
		"empty intent id":      func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { in.ID = "" },
		"intent id with space": func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { in.ID = "a b" },
		"intent id like flag":  func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) { in.ID = "--" },
		"intent id too long": func(r *leadrole.Role, e *leadregistry.Entry, in *leadregistry.Intent) {
			in.ID = strings.Repeat("a", 129)
		},
	}
	for name, m := range cases {
		e, in := good()
		r := role
		m(&r, &e, &in)
		argv, wd, env, err := h.Build(r, e, in)
		if err == nil {
			t.Errorf("%s: Build succeeded: %q", name, argv)
			continue
		}
		if !errors.Is(err, leadcanary.ErrHolderCommand) {
			t.Errorf("%s: %v does not wrap ErrHolderCommand", name, err)
		}
		if argv != nil || wd != "" || env != nil {
			t.Errorf("%s: a refusal returned a command", name)
		}
	}
	// And the good one still builds.
	e, in := good()
	if _, _, _, err := h.Build(role, e, in); err != nil {
		t.Fatal(err)
	}
}

func TestNewHolderCommandRefusals(t *testing.T) {
	f := newFixture(t)
	for name, lock := range map[string]string{
		"relative lock": "daemon.lock",
		"unclean lock":  f.state + "/../daemon.lock",
		"empty lock":    "",
		"control lock":  f.state + "/da\nemon.lock",
	} {
		if _, err := leadcanary.NewHolderCommand(f.config(t), lock); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !errors.Is(err, leadcanary.ErrHolderCommand) {
			t.Errorf("%s: %v does not wrap ErrHolderCommand", name, err)
		}
	}
	bad := f.config(t)
	bad.Holder.Executable = "holder"
	if _, err := leadcanary.NewHolderCommand(bad, filepath.Join(f.state, "daemon.lock")); err == nil {
		t.Error("a config with a relative executable was accepted")
	}
	bad = f.config(t)
	bad.Role = mustRole(t, "demo/lead")
	if _, err := leadcanary.NewHolderCommand(bad, filepath.Join(f.state, "daemon.lock")); err == nil {
		t.Error("a config with a real project's role was accepted")
	}
	// A state root so long that a path under it is over the holder's bound.
	bad = f.config(t)
	bad.StateRoot = "/" + strings.Repeat("a", 4070)
	if _, err := leadcanary.NewHolderCommand(bad, "/lock"); err == nil {
		t.Error("a path over the holder's bound was accepted")
	}
}

// --- the holder's own rules for the shared flags ---
//
// cmd/ataniq-holder is package main and cannot be imported, so the rules of its
// parser (cmd/ataniq-holder/args_linux.go) for the flags the canary line shares
// with it are written out here, and the line Build gives must meet them. The
// canary flags (--canary, --inject, --sandbox-inputs, --expected-mode) are
// added by the holder's canary task and are parsed here as plain flags.

var holderValueFlags = []string{
	"role", "epoch", "launch-intent", "runtime", "runtime-version", "profile-digest",
	"tree", "state-dir", "runtime-dir", "role-lock", "daemon-lock", "cols", "rows",
}

type parsedLine struct {
	vals   map[string]string
	canary bool
	inject bool
	env    []string
	agent  []string
	counts map[string]int
}

type counter struct {
	name string
	p    *parsedLine
	s    string
}

func (c *counter) String() string { return c.s }
func (c *counter) Set(s string) error {
	c.p.counts[c.name]++
	c.s = s
	return nil
}

type envList struct {
	p *parsedLine
}

func (l envList) String() string { return "" }
func (l envList) Set(s string) error {
	l.p.env = append(l.p.env, s)
	return nil
}

func parseHolderLine(t *testing.T, args []string) (parsedLine, error) {
	t.Helper()
	p := parsedLine{vals: map[string]string{}, counts: map[string]int{}}
	fs := flag.NewFlagSet("holder", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&p.canary, "canary", false, "")
	fs.BoolVar(&p.inject, "inject", false, "")
	cs := map[string]*counter{}
	for _, n := range append(append([]string(nil), holderValueFlags...), "sandbox-inputs", "expected-mode") {
		c := &counter{name: n, p: &p}
		cs[n] = c
		fs.Var(c, n, "")
	}
	fs.Var(envList{&p}, "env", "")
	before, after := args, []string(nil)
	for i, a := range args {
		if a == "--" {
			before, after = args[:i], args[i+1:]
			break
		}
	}
	if err := fs.Parse(before); err != nil {
		return p, err
	}
	if fs.NArg() != 0 {
		return p, errors.New("positional arguments before --")
	}
	for n, c := range cs {
		if c.s != "" || p.counts[n] > 0 {
			p.vals[n] = c.s
		}
	}
	p.agent = after
	return p, nil
}

// checkHolderRules applies the checks of the holder's parse function to the
// shared flags.
func checkHolderRules(p parsedLine) error {
	for _, n := range holderValueFlags {
		if p.counts[n] != 1 {
			return errors.New("--" + n + " is not given exactly once")
		}
		if p.vals[n] == "" {
			return errors.New("--" + n + " is empty")
		}
	}
	epoch, err := strconv.ParseUint(p.vals["epoch"], 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != p.vals["epoch"] {
		return errors.New("--epoch")
	}
	cols, ok := smallInt(p.vals["cols"], 4)
	if !ok || cols < 20 || cols > 500 {
		return errors.New("--cols")
	}
	rows, ok := smallInt(p.vals["rows"], 4)
	if !ok || rows < 5 || rows > 200 {
		return errors.New("--rows")
	}
	if l := len(p.vals["role"]); l < 1 || l > 128 || !plainText(p.vals["role"]) {
		return errors.New("--role")
	}
	for _, n := range []string{"runtime", "runtime-version"} {
		if l := len(p.vals[n]); l < 1 || l > 64 || !plainText(p.vals[n]) {
			return errors.New("--" + n)
		}
	}
	if !tokenOK(p.vals["launch-intent"], 128, false) {
		return errors.New("--launch-intent")
	}
	if !tokenOK(p.vals["profile-digest"], 256, true) {
		return errors.New("--profile-digest")
	}
	for _, n := range []string{"tree", "state-dir", "runtime-dir", "role-lock", "daemon-lock"} {
		if !absPlain(p.vals[n]) {
			return errors.New("--" + n)
		}
	}
	if filepath.Clean(p.vals["state-dir"]) == filepath.Clean(p.vals["runtime-dir"]) {
		return errors.New("--state-dir and --runtime-dir are the same")
	}
	if len(p.env) > 64 {
		return errors.New("too many --env")
	}
	seen := map[string]bool{}
	for _, kv := range p.env {
		key, ok := envPair(kv)
		if !ok || seen[key] {
			return errors.New("--env")
		}
		seen[key] = true
	}
	if len(p.agent) == 0 || !absPlain(p.agent[0]) {
		return errors.New("the agent command")
	}
	return nil
}

func smallInt(s string, maxDigits int) (int, bool) {
	if len(s) < 1 || len(s) > maxDigits {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

func absPlain(s string) bool {
	return strings.HasPrefix(s, "/") && len(s) <= 4096 && plainText(s)
}

func tokenOK(s string, max int, wide bool) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		case wide && (c == '+' || c == '/' || c == '='):
		default:
			return false
		}
	}
	return true
}

func envPair(kv string) (string, bool) {
	key, val, ok := strings.Cut(kv, "=")
	if !ok || len(key) < 1 || len(key) > 128 || len(val) > 4096 || !plainText(val) {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return key, true
}

// TestHolderCommandMeetsTheHoldersRules runs Build over values at the edges of
// what the config allows and checks that the holder's parser rules for the
// shared flags accept every line.
func TestHolderCommandMeetsTheHoldersRules(t *testing.T) {
	f := newFixture(t)
	cases := map[string]func(*leadcanary.Config){
		"sample": nil,
		"smallest screen": func(c *leadcanary.Config) {
			c.Holder.Cols, c.Holder.Rows = leadcanary.MinCols, leadcanary.MinRows
		},
		"largest screen": func(c *leadcanary.Config) {
			c.Holder.Cols, c.Holder.Rows = leadcanary.MaxCols, leadcanary.MaxRows
		},
		"longest runtime texts": func(c *leadcanary.Config) {
			c.Agent.Runtime = strings.Repeat("r", 64)
			c.Agent.RuntimeVersion = strings.Repeat("v", 64)
		},
		"longest project": func(c *leadcanary.Config) {
			c.Role = mustRole(t, "throwaway-"+strings.Repeat("a", 48)+"/lead")
		},
		"no inject, no env": func(c *leadcanary.Config) { c.Inject, c.Agent.Env = false, nil },
		"most env": func(c *leadcanary.Config) {
			c.Agent.Env = nil
			for i := 0; i < 64; i++ {
				c.Agent.Env = append(c.Agent.Env, "K"+strconv.Itoa(i)+"="+strings.Repeat("v", 100))
			}
		},
		"non-ASCII text": func(c *leadcanary.Config) {
			c.Agent.Runtime, c.Agent.FooterMode = "runtime-é", "mode é"
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			h, cfg := newBuilder(t, f, mut)
			entry, intent := sampleEntryIntent(cfg.Role, 18446744073709551615)
			argv, _, _, err := h.Build(cfg.Role, entry, intent)
			if err != nil {
				t.Fatal(err)
			}
			p, err := parseHolderLine(t, argv[1:])
			if err != nil {
				t.Fatalf("the holder's flag set refuses the line: %v\n%q", err, argv)
			}
			if err := checkHolderRules(p); err != nil {
				t.Fatalf("the holder's rules refuse the line: %v\n%q", err, argv)
			}
			if !p.canary || p.inject != cfg.Inject {
				t.Fatalf("canary %v inject %v, config inject %v", p.canary, p.inject, cfg.Inject)
			}
			if p.counts["sandbox-inputs"] != 1 || p.vals["sandbox-inputs"] != cfg.Holder.SandboxInputs {
				t.Fatalf("--sandbox-inputs %q", p.vals["sandbox-inputs"])
			}
			if p.counts["expected-mode"] != 1 || p.vals["expected-mode"] != cfg.Agent.FooterMode {
				t.Fatalf("--expected-mode %q", p.vals["expected-mode"])
			}
			if !reflect.DeepEqual(p.env, cfg.Agent.Env) && !(len(p.env) == 0 && len(cfg.Agent.Env) == 0) {
				t.Fatalf("env flags %q, config %q", p.env, cfg.Agent.Env)
			}
			if !reflect.DeepEqual(p.agent, cfg.Agent.Argv) {
				t.Fatalf("agent %q, config %q", p.agent, cfg.Agent.Argv)
			}
		})
	}
}

// TestHolderRulesTestIsNotVacuous shows that the mirror of the holder's rules
// does refuse the faults it is meant to catch.
func TestHolderRulesTestIsNotVacuous(t *testing.T) {
	f := newFixture(t)
	h, cfg := newBuilder(t, f, nil)
	entry, intent := sampleEntryIntent(cfg.Role, 5)
	argv, _, _, _ := h.Build(cfg.Role, entry, intent)
	set := func(name, val string) []string {
		out := append([]string(nil), argv...)
		for i, a := range out {
			if a == name {
				out[i+1] = val
				break
			}
		}
		return out
	}
	for name, line := range map[string][]string{
		"relative tree":  set("--tree", "tree"),
		"epoch zero":     set("--epoch", "0"),
		"cols too big":   set("--cols", "501"),
		"intent with /":  set("--launch-intent", "a/b"),
		"same dirs":      set("--runtime-dir", flagValue(t, argv, "--state-dir")),
		"digest with !":  set("--profile-digest", "ab!"),
		"role with tab":  set("--role", "a\tb"),
		"runtime empty":  set("--runtime", ""),
		"agent relative": append(append([]string(nil), argv[:len(argv)-3]...), "agent", "--flag", "x"),
	} {
		p, err := parseHolderLine(t, line[1:])
		if err == nil {
			err = checkHolderRules(p)
		}
		if err == nil {
			t.Errorf("%s: the mirror of the holder's rules accepted the line", name)
		}
	}
	if utf8.RuneCountInString("é") != 1 {
		t.Fatal("source file is not UTF-8")
	}
}

// scratchBuilder returns the holder command of the first lead of the sample
// scratch config, with record_screens as given.
func scratchBuilder(t *testing.T, f fixture, record bool) (*leadcanary.HolderCommand, leadcanary.Config) {
	t.Helper()
	sc := f.scratchSample()
	sc.Entries[0].RecordScreens = record
	leads, err := sc.Leads()
	if err != nil {
		t.Fatal(err)
	}
	cfg := leads[0]
	if cfg.RecordScreens != record {
		t.Fatalf("the derived config records %v, the lead says %v", cfg.RecordScreens, record)
	}
	h, err := leadcanary.NewHolderCommand(cfg, filepath.Join(f.state, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

// The mutation check of record_screens: a scratch lead that records gets
// exactly "--record-dir <its runtime folder>/record" after --expected-mode, and
// the same lead that does not record gets the line it had before the field
// existed, written out here by hand. Nothing else of the line changes.
func TestHolderCommandRecordDirOnlyWhenTheLeadRecords(t *testing.T) {
	f := newFixture(t)
	hOff, cfg := scratchBuilder(t, f, false)
	hOn, _ := scratchBuilder(t, f, true)
	entry, intent := sampleEntryIntent(cfg.Role, 3)
	off, _, _, err := hOff.Build(cfg.Role, entry, intent)
	if err != nil {
		t.Fatal(err)
	}
	on, _, _, err := hOn.Build(cfg.Role, entry, intent)
	if err != nil {
		t.Fatal(err)
	}
	lead := filepath.Join(f.state, "leads", "scratch-alpha-lead")
	runtimeDir := filepath.Join(f.runtime, "leads", "scratch-alpha-lead")
	golden := []string{
		"/opt/sample/ataniq-holder",
		"--role", "scratch-alpha/lead",
		"--epoch", "3",
		"--launch-intent", sampleIntent,
		"--runtime", "claude",
		"--runtime-version", "1.2.3",
		"--profile-digest", leadcanary.UnpinnedDigest,
		"--tree", filepath.Join(lead, "tree"),
		"--state-dir", filepath.Join(lead, "holder"),
		"--runtime-dir", runtimeDir,
		"--role-lock", filepath.Join(f.state, "roles", "scratch-alpha", "lead.lock"),
		"--daemon-lock", filepath.Join(f.state, "daemon.lock"),
		"--cols", "120",
		"--rows", "40",
		"--canary",
		"--inject",
		"--sandbox-inputs", cfg.Holder.SandboxInputs,
		"--expected-mode", "manual",
		"--",
		"/ataniq/agent/sample-standin",
	}
	if !reflect.DeepEqual(off, golden) {
		t.Fatalf("argv without recording\n got %q\nwant %q", off, golden)
	}
	i := len(golden) - 2 // before "--" and the agent
	want := append(append(append([]string(nil), golden[:i]...), "--record-dir", filepath.Join(runtimeDir, "record")), golden[i:]...)
	if !reflect.DeepEqual(on, want) {
		t.Fatalf("argv with recording\n got %q\nwant %q", on, want)
	}
	if leadcanary.RecordDirName != "record" {
		t.Fatalf("RecordDirName %q", leadcanary.RecordDirName)
	}
	// The canary's line never carries the flag.
	h, ccfg := newBuilder(t, f, nil)
	e2, i2 := sampleEntryIntent(ccfg.Role, 1)
	argv, _, _, err := h.Build(ccfg.Role, e2, i2)
	if err != nil {
		t.Fatal(err)
	}
	if contains(argv, "--record-dir") {
		t.Fatalf("the canary's line records: %q", argv)
	}
}

// Only a scratch lead records: a throwaway config that says so is refused.
func TestRecordScreensIsForAScratchLeadOnly(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	cfg.RecordScreens = true
	wantConfigError(t, cfg.Validate(), "record_screens")
	if _, err := leadcanary.NewHolderCommand(cfg, filepath.Join(f.state, "daemon.lock")); !errors.Is(err, leadcanary.ErrHolderCommand) {
		t.Fatalf("NewHolderCommand: %v", err)
	}
	// A canary config file has no such key.
	m := tree(t, f.config(t))
	m["record_screens"] = true
	_, err := leadcanary.ReadConfig(f.writeTree(t, m))
	wantConfigError(t, err, "file")
}
