package boundedexec

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validCmd() Cmd {
	return Cmd{
		Argv:      []string{"/bin/sh", "-c", "true"},
		Timeout:   time.Second,
		MaxStdout: 10,
		MaxStderr: 10,
	}
}

func TestValidationRefusals(t *testing.T) {
	cases := map[string]func(c *Cmd){
		"empty argv":          func(c *Cmd) { c.Argv = nil },
		"relative argv0":      func(c *Cmd) { c.Argv[0] = "sh" },
		"dot argv0":           func(c *Cmd) { c.Argv[0] = "./sh" },
		"NUL in argv":         func(c *Cmd) { c.Argv = append(c.Argv, "a\x00b") },
		"NUL in env":          func(c *Cmd) { c.Env = []string{"A=b\x00c"} },
		"env without equals":  func(c *Cmd) { c.Env = []string{"AB"} },
		"env without name":    func(c *Cmd) { c.Env = []string{"=x"} },
		"env name twice":      func(c *Cmd) { c.Env = []string{"A=1", "A=2"} },
		"NUL in dir":          func(c *Cmd) { c.Dir = "/a\x00b" },
		"relative dir":        func(c *Cmd) { c.Dir = "tmp" },
		"no timeout":          func(c *Cmd) { c.Timeout = 0 },
		"negative timeout":    func(c *Cmd) { c.Timeout = -time.Second },
		"no stdout limit":     func(c *Cmd) { c.MaxStdout = 0 },
		"no stderr limit":     func(c *Cmd) { c.MaxStderr = 0 },
		"negative stderr cap": func(c *Cmd) { c.MaxStderr = -1 },
		"negative interval":   func(c *Cmd) { c.WatchInterval = -time.Millisecond },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validCmd()
			mutate(&c)
			called := false
			c.Watch = func() error { called = true; return nil }
			res, err := Run(context.Background(), c)
			if err == nil {
				t.Fatal("Run accepted an invalid command")
			}
			if res.Exit != -1 {
				t.Errorf("Exit = %d, want -1", res.Exit)
			}
			if called {
				t.Error("watchdog ran for a refused command")
			}
			if !strings.HasPrefix(err.Error(), "boundedexec:") {
				t.Errorf("error %q does not name the package", err)
			}
		})
	}
}

func TestValidCmdPasses(t *testing.T) {
	c := validCmd()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestErrorTexts(t *testing.T) {
	e := &ExitError{Exit: 2, Stderr: []byte("  bad thing\n")}
	if got, want := e.Error(), `boundedexec: command exited with status 2: "bad thing"`; got != want {
		t.Errorf("ExitError = %q, want %q", got, want)
	}
	e = &ExitError{Exit: 2, Stderr: []byte(strings.Repeat("x", 500))}
	got := e.Error()
	if want := `boundedexec: command exited with status 2: "` + strings.Repeat("x", 200) + `"...`; got != want {
		t.Errorf("long stderr = %q, want it cut at 200 bytes", got)
	}
	if got := (&ExitError{Exit: 1}).Error(); strings.Contains(got, ":  ") || strings.HasSuffix(got, ": ") {
		t.Errorf("empty stderr text = %q", got)
	}
	inner := errors.New("inner")
	w := &WatchError{Err: inner}
	if !errors.Is(w, inner) || !strings.Contains(w.Error(), "inner") {
		t.Errorf("WatchError does not unwrap: %v", w)
	}
}

func TestExitErrorTextIsQuoted(t *testing.T) {
	// Stderr is written by untrusted code: it must not forge a log line or
	// drive the terminal through the error text.
	raw := "x\n\x1b[2Jlevel=info msg=\"intake accepted\"\r\x00\x7f"
	e := &ExitError{Exit: 1, Stderr: []byte(raw)}
	got := e.Error()
	for _, bad := range []string{"\n", "\r", "\x1b", "\x00", "\x7f"} {
		if strings.Contains(got, bad) {
			t.Errorf("Error() holds the byte %q: %q", bad, got)
		}
	}
	for i := 0; i < len(got); i++ {
		if got[i] < 0x20 || got[i] >= 0x7f {
			t.Errorf("Error() holds the non-printable byte %#x: %q", got[i], got)
		}
	}
	if string(e.Stderr) != raw {
		t.Error("Stderr was changed; it must stay raw")
	}
}

func TestExitErrorCutKeepsRunesWhole(t *testing.T) {
	// "x" and 150 two-byte runes: byte 200 is the second byte of a rune.
	raw := "x" + strings.Repeat("\u00e9", 150)
	got := (&ExitError{Exit: 1, Stderr: []byte(raw)}).Error()
	if strings.Contains(got, `\x`) {
		t.Errorf("the cut split a rune: %q", got)
	}
	if !strings.HasSuffix(got, `\u00e9"...`) {
		t.Errorf("unexpected tail: %q", got)
	}
	// Broken UTF-8 is escaped and the cut still ends the text with "...".
	got = (&ExitError{Exit: 1, Stderr: bytes.Repeat([]byte{0xff}, 300)}).Error()
	if !strings.HasSuffix(got, `\xff"...`) {
		t.Errorf("broken UTF-8 text: %q", got)
	}
}
