package main

import (
	"errors"
	"flag"
	"io"
	"strings"
	"unicode/utf8"

	"ataniq/internal/leadholder/wire"
)

// usageError is a wrong-usage diagnostic. Its text never repeats the text of
// an argument: the flag package does, so its errors are not shown.
type usageError string

func (e usageError) Error() string { return string(e) }

// helpRequest is the error of a request for help.
type helpRequest struct{}

func (helpRequest) Error() string { return "help requested" }

const (
	defaultCols = 100
	defaultRows = 30

	// maxEnv, maxEnvKey and maxEnvValue bound the --env flags: they are
	// copied to the holder's command line.
	maxEnv      = 32
	maxEnvKey   = 64
	maxEnvValue = 4096

	// maxNameLen is the longest NAME, which also keeps the unit name and the
	// socket path short.
	maxNameLen = 31
)

// Fixed parts of the agent's environment.
const (
	agentTerm    = "TERM=xterm-256color"
	agentPath    = "PATH=/usr/local/bin:/usr/bin:/bin"
	defaultLang  = "C.UTF-8"
	reservedHome = "HOME"
)

// validName reports whether s is [a-z0-9][a-z0-9-]{0,30}.
func validName(s string) bool {
	if len(s) < 1 || len(s) > maxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// startConfig is what start's arguments select.
type startConfig struct {
	name   string
	cols   int
	rows   int
	env    []string // KEY=VALUE, in the order given
	holder string   // empty: next to this executable
	direct bool
	argv   []string
}

type envList []string

func (l *envList) String() string { return strings.Join(*l, " ") }
func (l *envList) Set(s string) error {
	*l = append(*l, s)
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	// The flag package puts the raw argument text in its errors. Show none.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
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

func isHelp(s string) bool { return s == "-h" || s == "--help" }

// parseStart reads the arguments of start. getenv is the caller's
// environment, for LANG.
func parseStart(args []string, getenv func(string) string) (startConfig, error) {
	var cfg startConfig
	if len(args) == 0 {
		return cfg, usageError("start expects a NAME")
	}
	if isHelp(args[0]) {
		return cfg, helpRequest{}
	}
	if !validName(args[0]) {
		return cfg, usageError("NAME must be 1 to 31 characters of a-z, 0-9 and -, and must not start with -")
	}
	cfg.name = args[0]
	before, after, found := splitDash(args[1:])
	var env envList
	fs := newFlagSet("start")
	fs.IntVar(&cfg.cols, "cols", defaultCols, "")
	fs.IntVar(&cfg.rows, "rows", defaultRows, "")
	fs.Var(&env, "env", "")
	fs.StringVar(&cfg.holder, "holder", "", "")
	fs.BoolVar(&cfg.direct, "direct", false, "")
	if err := fs.Parse(before); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, helpRequest{}
		}
		return cfg, usageError("invalid flags")
	}
	if fs.NArg() != 0 {
		return cfg, usageError("start takes one NAME, then flags, then -- and the agent command")
	}
	if !found || len(after) == 0 {
		return cfg, usageError("start expects -- and the agent command")
	}
	caps := wire.DefaultCaps()
	if cfg.cols < caps.MinColumns || cfg.cols > caps.MaxColumns || cfg.rows < caps.MinRows || cfg.rows > caps.MaxRows {
		return cfg, usageError("--cols and --rows must be within the holder's screen limits (20 by 5 to 500 by 200)")
	}
	fixed := map[string]bool{"TERM": true, reservedHome: true, "PATH": true, "LANG": true}
	if len(env) > maxEnv {
		return cfg, usageError("too many --env flags")
	}
	for _, kv := range env {
		key, ok := checkEnvPair(kv)
		if !ok {
			return cfg, usageError("--env expects KEY=VALUE: KEY of letters, digits and _ and not starting with a digit, VALUE of printable text")
		}
		if fixed[key] {
			return cfg, usageError("--env cannot set a key that is fixed or repeated (TERM, HOME, PATH, LANG, or a key given twice)")
		}
		fixed[key] = true
	}
	cfg.env = env
	if cfg.holder != "" && !strings.HasPrefix(cfg.holder, "/") {
		return cfg, usageError("--holder must be an absolute path")
	}
	cfg.argv = after
	return cfg, nil
}

// checkEnvPair checks KEY=VALUE and returns the key.
func checkEnvPair(kv string) (string, bool) {
	key, val, ok := strings.Cut(kv, "=")
	if !ok || len(key) < 1 || len(key) > maxEnvKey || len(val) > maxEnvValue || !utf8.ValidString(val) {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
			return "", false
		}
	}
	for _, r := range val {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return "", false
		}
	}
	return key, true
}

// langFrom returns the LANG for the agent: the caller's when it is plain
// text of the usual locale-name characters, else the default.
func langFrom(getenv func(string) string) string {
	v := getenv("LANG")
	if v == "" || len(v) > 64 {
		return defaultLang
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '@') {
			return defaultLang
		}
	}
	return v
}

// ticketConfig is what ticket's arguments select.
type ticketConfig struct {
	name   string
	viewer bool
}

func parseTicket(args []string) (ticketConfig, error) {
	var cfg ticketConfig
	name, err := parseNamed("ticket", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&cfg.viewer, "viewer", false, "")
	})
	cfg.name = name
	return cfg, err
}

// parseNamed reads NAME and then the flags that define adds.
func parseNamed(cmd string, args []string, define func(*flag.FlagSet)) (string, error) {
	if len(args) == 0 {
		return "", usageError(cmd + " expects a NAME")
	}
	if isHelp(args[0]) {
		return "", helpRequest{}
	}
	if !validName(args[0]) {
		return "", usageError("NAME must be 1 to 31 characters of a-z, 0-9 and -, and must not start with -")
	}
	fs := newFlagSet(cmd)
	if define != nil {
		define(fs)
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", helpRequest{}
		}
		return "", usageError("invalid flags")
	}
	if fs.NArg() != 0 {
		return "", usageError(cmd + " takes one NAME and its flags")
	}
	return args[0], nil
}
