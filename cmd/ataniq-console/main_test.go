package main

import (
	"bytes"
	"strings"
	"testing"
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
