package main

import (
	"bytes"
	"strings"
	"testing"

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
