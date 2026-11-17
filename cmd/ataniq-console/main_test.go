package main

import (
	"bytes"
	"strings"
	"testing"

	"ataniq/internal/leadattach"
	"ataniq/internal/terminalconsole"
)

func TestFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 1, "backend is unavailable"}, {[]string{"--help"}, 0, "--demo"},
		{[]string{"--theme", "blue"}, 2, "dark or light"}, {[]string{"--demo", "extra"}, 2, "positional"},
		{[]string{"--unknown"}, 2, "invalid flags"},
		{[]string{"--demo", "--return-key", "ctrl+c"}, 2, "--return-key"},
		{[]string{"--demo", "--return-key", "ctrl+enter"}, 2, "--return-key"},
		{[]string{"--demo", "--recording", "nothing.jsonl"}, 2, "--recording must be one of: "},
		{[]string{"--demo", "--linger", "-1s"}, 2, "--linger"},
		{[]string{"--demo", "--linger", "2h"}, 2, "--linger"},
		{[]string{"--help"}, 0, "--return-key"},
	} {
		var out, err bytes.Buffer
		got := run(tc.args, &out, &err)
		if got != tc.code || !strings.Contains(err.String(), tc.want) {
			t.Fatalf("%v: %d %s", tc.args, got, err.String())
		}
	}
}

func TestHelpShowsADefaultTheFlagAccepts(t *testing.T) {
	var out, err bytes.Buffer
	if code := run([]string{"--help"}, &out, &err); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(err.String(), `(default ctrl+\)`) || strings.Contains(err.String(), `(default "ctrl+`) {
		t.Fatal(err.String())
	}
	// An empty value is a value, not the default.
	if code := run([]string{"--demo", "--return-key", ""}, &out, &err); code != 2 {
		t.Fatal(code)
	}
}

func TestFlagDiagnosticsDoNotEchoControlSequences(t *testing.T) {
	var out, err bytes.Buffer
	if code := run([]string{"--bad\x1b]52;c;payload\a"}, &out, &err); code != 2 {
		t.Fatal(code)
	}
	if strings.ContainsAny(err.String(), "\x1b\a") || strings.Contains(err.String(), "payload") || !strings.Contains(err.String(), "Usage:") {
		t.Fatal(err.String())
	}
}

func TestInvalidFlagPrintsUsageOnce(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"--theme", "blue"}, {"--demo", "extra"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
		if n := strings.Count(err.String(), "Usage:"); n != 1 {
			t.Fatalf("%v: usage printed %d times: %s", args, n, err.String())
		}
	}
}

func TestFlagsSelectTheConsoleOptions(t *testing.T) {
	var errout bytes.Buffer
	cfg, code, ok := parse([]string{"--demo", "--theme", "light", "--return-key", "ctrl+g", "--recording", "modes.jsonl", "--linger", "0"}, &errout)
	if !ok || code != 0 || !cfg.demo || errout.Len() != 0 {
		t.Fatal(ok, code, errout.String())
	}
	o := cfg.options
	if o.Theme != "light" || o.ReturnKey.String() != "Ctrl+G" || o.Recording != "modes.jsonl" || o.Linger != 0 {
		t.Fatalf("%+v", o)
	}
	// The defaults: the return key is Ctrl+\\, and the session stays open a while.
	cfg, _, ok = parse([]string{"--demo"}, &errout)
	if !ok || cfg.options.ReturnKey != terminalconsole.DefaultReturnKey || cfg.options.Recording != terminalconsole.DefaultRecording || cfg.options.Linger != defaultLinger {
		t.Fatalf("%+v", cfg.options)
	}
}

func TestReturnKeyDiagnosticDoesNotEchoTheArgument(t *testing.T) {
	var out, err bytes.Buffer
	if code := run([]string{"--demo", "--return-key", "ctrl+\x1b]52;c;payload\a"}, &out, &err); code != 2 {
		t.Fatal(code)
	}
	if strings.ContainsAny(err.String(), "\x1b\a") || strings.Contains(err.String(), "payload") || !strings.Contains(err.String(), "Usage:") {
		t.Fatal(err.String())
	}
}

func TestAttachFlagsSelectTheAttachConfig(t *testing.T) {
	var errout bytes.Buffer
	cfg, code, ok := parse([]string{"--attach", "/run/leads/x", "--ticket-file", "/run/ticket"}, &errout)
	if !ok || code != 0 || errout.Len() != 0 || cfg.attach == nil || cfg.demo {
		t.Fatal(ok, code, errout.String())
	}
	// The defaults: a writer, Shift+Enter as in the client, the cockpit's own.
	a := *cfg.attach
	if a.socket != "/run/leads/x/attach.sock" || a.ticketFile != "/run/ticket" || a.viewer || a.shiftEnter != leadattach.ShiftEnterESCCR {
		t.Fatalf("%+v", a)
	}
	if cfg.options.Theme != "dark" || cfg.options.ReturnKey != terminalconsole.DefaultReturnKey {
		t.Fatalf("%+v", cfg.options)
	}
	// A folder with a trailing slash names the same socket.
	cfg, _, ok = parse([]string{"--attach", "/run/leads/x/", "--ticket-file", "t"}, &errout)
	if !ok || cfg.attach.socket != "/run/leads/x/attach.sock" || cfg.attach.ticketFile != "t" {
		t.Fatalf("%+v", cfg.attach)
	}
	for name, want := range map[string]leadattach.ShiftEnter{
		"esc-cr": leadattach.ShiftEnterESCCR, "lf": leadattach.ShiftEnterLF,
		"csi-u": leadattach.ShiftEnterCSIu, "backslash-cr": leadattach.ShiftEnterBackslashCR,
	} {
		cfg, code, ok := parse([]string{"--attach", "d", "--ticket-file", "t", "--shift-enter", name}, &errout)
		if !ok || code != 0 || cfg.attach.shiftEnter != want {
			t.Fatalf("--shift-enter %s: %v %d %+v %s", name, ok, code, cfg.attach, errout.String())
		}
	}
	cfg, code, ok = parse([]string{"--attach", "d", "--ticket-file", "t", "--viewer", "--theme", "light", "--return-key", "ctrl+g"}, &errout)
	if !ok || code != 0 || !cfg.attach.viewer || cfg.options.Theme != "light" || cfg.options.ReturnKey.String() != "Ctrl+G" {
		t.Fatalf("%v %d %+v %+v", ok, code, cfg.attach, cfg.options)
	}
	if errout.Len() != 0 {
		t.Fatal(errout.String())
	}
}

func TestAttachSocketPathMustFitAUnixAddress(t *testing.T) {
	// The socket path is the folder, a slash and attach.sock: 107 bytes fit.
	fit := "/" + strings.Repeat("d", maxSocketPath-len("/"+attachSocketName)-1)
	var errout bytes.Buffer
	cfg, code, ok := parse([]string{"--attach", fit, "--ticket-file", "t"}, &errout)
	if !ok || code != 0 || len(cfg.attach.socket) != maxSocketPath {
		t.Fatal(ok, code, errout.String())
	}
	errout.Reset()
	if _, code, ok := parse([]string{"--attach", fit + "d", "--ticket-file", "t"}, &errout); ok || code != 2 ||
		!strings.Contains(errout.String(), "--attach: the socket path is 108 bytes; a unix socket address holds at most 107") ||
		strings.Contains(errout.String(), "ddddd") {
		t.Fatal(ok, code, errout.String())
	}
}

func TestAttachRefusals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--attach", "d"}, "--attach and --ticket-file are used together"},
		{[]string{"--ticket-file", "t"}, "--attach and --ticket-file are used together"},
		{[]string{"--attach", "", "--ticket-file", "t"}, "need a path"},
		{[]string{"--attach", "d", "--ticket-file", ""}, "need a path"},
		{[]string{"--attach", "d\x00e", "--ticket-file", "t"}, "NUL"},
		{[]string{"--attach", "d", "--ticket-file", "t\x00"}, "NUL"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--demo"}, "cannot be used with --demo, --recording or --linger"},
		{[]string{"--demo", "--attach", "d", "--ticket-file", "t"}, "cannot be used with --demo, --recording or --linger"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--recording", "title.jsonl"}, "cannot be used with --demo, --recording or --linger"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--linger", "30s"}, "cannot be used with --demo, --recording or --linger"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--linger", "0"}, "cannot be used with --demo, --recording or --linger"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--shift-enter", "enter"}, "--shift-enter must be one of: esc-cr, lf, csi-u, backslash-cr"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--shift-enter", ""}, "--shift-enter must be one of"},
		{[]string{"--viewer"}, "--viewer and --shift-enter need --attach"},
		{[]string{"--demo", "--viewer"}, "--viewer and --shift-enter need --attach"},
		{[]string{"--demo", "--shift-enter", "lf"}, "--viewer and --shift-enter need --attach"},
		{[]string{"--attach", "d", "--ticket-file", "t", "extra"}, "positional"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--theme", "blue"}, "dark or light"},
		{[]string{"--attach", "d", "--ticket-file", "t", "--return-key", "ctrl+c"}, "--return-key"},
		{[]string{"--attach"}, "invalid flags"},
	} {
		var out, err bytes.Buffer
		if got := run(tc.args, &out, &err); got != 2 || !strings.Contains(err.String(), tc.want) || strings.Count(err.String(), "Usage:") != 1 {
			t.Fatalf("%q: %d %s", tc.args, got, err.String())
		}
		if out.Len() != 0 {
			t.Fatalf("%q: wrote %q to the terminal", tc.args, out.String())
		}
	}
}

func TestAttachDiagnosticsDoNotEchoArguments(t *testing.T) {
	secret := "\x1b]52;c;payload\a"
	long := "/" + strings.Repeat("d", 200) + secret
	for _, args := range [][]string{
		{"--attach", long, "--ticket-file", "t"},
		{"--attach", "d" + secret, "--ticket-file", "t", "--shift-enter", secret},
		{"--attach", secret},
		{"--ticket-file", secret},
		{"--attach", secret, "--ticket-file", secret, "--demo"},
		{"--attach", secret, "--ticket-file", secret, "--linger", secret},
		{"--attach", secret, "--ticket-file", secret, "--bad" + secret},
		{"--viewer" + secret},
		{"--shift-enter", secret},
	} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("%q: %d", args, code)
		}
		if strings.ContainsAny(err.String(), "\x1b\a") || strings.Contains(err.String(), "payload") || strings.Contains(err.String(), "ddddd") {
			t.Fatalf("%q: %s", args, err.String())
		}
	}
}

func TestHelpDescribesTheAttachFlags(t *testing.T) {
	var out, err bytes.Buffer
	if code := run([]string{"--help"}, &out, &err); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{
		"ataniq-console --attach DIR --ticket-file FILE [--viewer] [--shift-enter esc-cr|lf|csi-u|backslash-cr]",
		"-attach", "-ticket-file", "-viewer", "-shift-enter", "the ticket decides", "esc-cr, lf, csi-u, backslash-cr",
	} {
		if !strings.Contains(err.String(), want) {
			t.Fatalf("the help does not say %q:\n%s", want, err.String())
		}
	}
}

func TestWithoutDemoOrAttachTheBackendIsUnavailable(t *testing.T) {
	// Flags that need --attach do not make the command start anything.
	for _, args := range [][]string{nil, {"--theme", "light"}, {"--return-key", "ctrl+g"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 1 || err.String() != "Terminal console backend is unavailable. Use --demo to inspect the read-only fixture.\n" || out.Len() != 0 {
			t.Fatalf("%q: %d %q", args, code, err.String())
		}
	}
}
