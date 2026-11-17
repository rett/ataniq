//go:build linux

package main

import (
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// usageError is a wrong-usage diagnostic. Its text never repeats the text of an
// argument: the flag package does, so its errors are not shown.
type usageError string

func (e usageError) Error() string { return string(e) }

// helpRequest is the error of a request for help.
type helpRequest struct{}

func (helpRequest) Error() string { return "help requested" }

// notScratch is the refusal to host anything but a scratch agent.
type notScratch struct{}

func (notScratch) Error() string { return "this build can host only a scratch agent" }

// Bounds on what the command line may carry.
const (
	maxEnv      = 64
	maxEnvKey   = 128
	maxEnvValue = 4096
	maxPath     = 4096
	maxIntent   = 128
	maxDigest   = 256
)

// config is what a valid command line selects.
type config struct {
	role, launchIntent, runtime, runtimeVersion, profileDigest string
	epoch                                                      uint64
	tree, stateDir, runtimeDir, roleLock, daemonLock           string
	cols, rows                                                 int
	env                                                        []string // KEY=VALUE, in the order given
	argv                                                       []string // argv[0] is an absolute path
}

// value is one string flag: it knows whether it was given, and refuses a second
// time.
type value struct {
	p    *flagParser
	name string
	set  bool
	s    string
}

func (v *value) String() string {
	if v == nil {
		return ""
	}
	return v.s
}

func (v *value) Set(s string) error {
	if v.set {
		v.p.dup = v.name
		return errors.New("given twice")
	}
	v.set, v.s = true, s
	return nil
}

// envList collects the --env flags in order.
type envList []string

func (l *envList) String() string { return strings.Join(*l, " ") }
func (l *envList) Set(s string) error {
	*l = append(*l, s)
	return nil
}

// flagParser holds the flag set of the command. The set is the fixed command line
// of the holder: the launcher builds exactly it.
type flagParser struct {
	fs      *flag.FlagSet
	dup     string
	scratch bool
	vals    map[string]*value
	env     envList
}

// valueFlags are the flags that take one value, each required.
var valueFlags = []string{
	"role", "epoch", "launch-intent", "runtime", "runtime-version", "profile-digest",
	"tree", "state-dir", "runtime-dir", "role-lock", "daemon-lock", "cols", "rows",
}

func newParser() *flagParser {
	p := &flagParser{vals: map[string]*value{}}
	fs := flag.NewFlagSet("ataniq-holder", flag.ContinueOnError)
	// The flag package puts the raw argument text in its errors. Show none.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.BoolVar(&p.scratch, "scratch", false, "")
	for _, n := range valueFlags {
		v := &value{p: p, name: n}
		p.vals[n] = v
		fs.Var(v, n, "")
	}
	fs.Var(&p.env, "env", "")
	p.fs = fs
	return p
}

// flagNames lists the flags the command accepts, sorted.
func flagNames() []string {
	var names []string
	newParser().fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	return names
}

// splitDash splits args at the first "--".
func splitDash(args []string) (before, after []string, found bool) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

// parse reads the command line. The error is a usageError, a helpRequest or a
// notScratch.
func parse(args []string) (config, error) {
	var cfg config
	before, after, found := splitDash(args)
	p := newParser()
	if err := p.fs.Parse(before); err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
			return cfg, helpRequest{}
		case p.dup != "":
			return cfg, usageError("--" + p.dup + " was given twice")
		}
		return cfg, usageError("invalid flags")
	}
	if p.fs.NArg() != 0 {
		return cfg, usageError("expected flags, then -- and the agent command")
	}
	// The guard that keeps a real lead off an unsandboxed holder comes before
	// any other check of the line.
	if !p.scratch {
		return cfg, notScratch{}
	}
	for _, n := range valueFlags {
		if v := p.vals[n]; !v.set || v.s == "" {
			return cfg, usageError("--" + n + " is required")
		}
	}
	if !found || len(after) == 0 {
		return cfg, usageError("expected -- and the agent command")
	}
	v := func(n string) string { return p.vals[n].s }

	epoch, err := strconv.ParseUint(v("epoch"), 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != v("epoch") {
		return cfg, usageError("--epoch must be a decimal number above 0")
	}
	cols, ok := smallInt(v("cols"), 4)
	if !ok || cols < session.MinCols || cols > session.MaxCols {
		return cfg, usageError("--cols must be a number from 20 to 500")
	}
	rows, ok := smallInt(v("rows"), 4)
	if !ok || rows < session.MinRows || rows > session.MaxRows {
		return cfg, usageError("--rows must be a number from 5 to 200")
	}
	for _, f := range []struct{ name, val string }{{"role", v("role")}, {"runtime", v("runtime")}, {"runtime-version", v("runtime-version")}} {
		if !wireAccepts(f.name, f.val) {
			return cfg, usageError("--" + f.name + " is not acceptable to the attach protocol: plain text of 1 to 128 bytes (64 for the runtime and its version)")
		}
	}
	if !tokenOK(v("launch-intent"), maxIntent, false) {
		return cfg, usageError("--launch-intent must be 1 to 128 characters of letters, digits and . _ : -")
	}
	if !tokenOK(v("profile-digest"), maxDigest, true) {
		return cfg, usageError("--profile-digest must be 1 to 256 characters of letters, digits and . _ : + / = -")
	}
	for _, n := range []string{"tree", "state-dir", "runtime-dir", "role-lock", "daemon-lock"} {
		if !absPlain(v(n)) {
			return cfg, usageError("--" + n + " must be an absolute path of plain text")
		}
	}
	if filepath.Clean(v("state-dir")) == filepath.Clean(v("runtime-dir")) {
		return cfg, usageError("--state-dir and --runtime-dir must be different folders")
	}
	if len(p.env) > maxEnv {
		return cfg, usageError("too many --env flags")
	}
	seen := map[string]bool{}
	for _, kv := range p.env {
		key, ok := checkEnvPair(kv)
		if !ok {
			return cfg, usageError("--env expects KEY=VALUE: KEY of letters, digits and _ and not starting with a digit, VALUE of printable text")
		}
		if seen[key] {
			return cfg, usageError("--env was given twice for one key")
		}
		seen[key] = true
	}
	if !absPlain(after[0]) {
		return cfg, usageError("the agent command must start with an absolute path of plain text")
	}
	return config{
		role: v("role"), launchIntent: v("launch-intent"), runtime: v("runtime"),
		runtimeVersion: v("runtime-version"), profileDigest: v("profile-digest"), epoch: epoch,
		tree: v("tree"), stateDir: v("state-dir"), runtimeDir: v("runtime-dir"),
		roleLock: v("role-lock"), daemonLock: v("daemon-lock"), cols: cols, rows: rows,
		env: []string(p.env), argv: after,
	}, nil
}

// smallInt reads 1 to maxDigits decimal digits.
func smallInt(s string, maxDigits int) (int, bool) {
	if len(s) < 1 || len(s) > maxDigits {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// wireAccepts reports whether the attach protocol accepts s as the lead, the
// runtime or the runtime version, by encoding the Welcome that carries it.
func wireAccepts(field, s string) bool {
	w := wire.Welcome{Version: wire.ProtocolVersion, Role: wire.RoleViewer, Lead: "x", Epoch: 1, Runtime: "x", RuntimeVersion: "x", Caps: wire.DefaultCaps()}
	switch field {
	case "role":
		w.Lead = s
	case "runtime":
		w.Runtime = s
	default:
		w.RuntimeVersion = s
	}
	_, err := wire.EncodeWelcome(w)
	return err == nil
}

// tokenOK reports whether s is 1 to max characters of letters, digits and
// . _ : - (and + / = when wide).
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

// plain reports valid UTF-8 without a control character.
func plain(s string) bool {
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

// absPlain reports an absolute path of plain text, within the length bound.
func absPlain(s string) bool {
	return strings.HasPrefix(s, "/") && len(s) <= maxPath && plain(s)
}

// checkEnvPair checks KEY=VALUE and returns the key.
func checkEnvPair(kv string) (string, bool) {
	key, val, ok := strings.Cut(kv, "=")
	if !ok || len(key) < 1 || len(key) > maxEnvKey || len(val) > maxEnvValue || !plain(val) {
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
