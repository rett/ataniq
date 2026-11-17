package boundedexec

import (
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
	if got, want := e.Error(), "boundedexec: command exited with status 2: bad thing"; got != want {
		t.Errorf("ExitError = %q, want %q", got, want)
	}
	e = &ExitError{Exit: 2, Stderr: []byte(strings.Repeat("x", 500))}
	if got := e.Error(); len(got) > 300 || !strings.HasSuffix(got, "...") {
		t.Errorf("long stderr not cut: %d bytes", len(got))
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
