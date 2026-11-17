package systemd

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunUsesOnlyTheGivenEnvironment(t *testing.T) {
	t.Setenv("SECRET_FOR_TEST", "leak")
	path := script(t, `echo "$SECRET_FOR_TEST|$ONE|$#|$1"`)
	got, err := Run(path, []string{"arg"}, time.Now().Add(5*time.Second), []string{"ONE=1"}, 4096)
	if err != nil || got != "|1|1|arg\n" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestRunCombinesOutputAndReportsFailure(t *testing.T) {
	path := script(t, `echo out; echo err >&2; exit 3`)
	got, err := Run(path, nil, time.Now().Add(5*time.Second), nil, 4096)
	if err == nil || !strings.Contains(got, "out\n") || !strings.Contains(got, "err\n") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestRunOutputCap(t *testing.T) {
	path := script(t, `printf '%s' 0123456789`)
	got, err := Run(path, nil, time.Now().Add(5*time.Second), nil, 10)
	if err != nil || got != "0123456789" {
		t.Fatalf("at the cap: %q %v", got, err)
	}
	got, err = Run(path, nil, time.Now().Add(5*time.Second), nil, 9)
	if !errors.Is(err, ErrOutputExcessive) || got != "" {
		t.Fatalf("over the cap: %q %v", got, err)
	}
}

func TestRunDeadline(t *testing.T) {
	path := script(t, `exec sleep 30`)
	start := time.Now()
	_, err := Run(path, nil, time.Now().Add(150*time.Millisecond), nil, 4096)
	if err == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("deadline not enforced: %v after %v", err, time.Since(start))
	}
}

func TestRunMissingExecutable(t *testing.T) {
	if _, err := Run(filepath.Join(t.TempDir(), "missing"), nil, time.Now().Add(time.Second), nil, 16); err == nil {
		t.Fatal("missing executable ran")
	}
}

func TestEnvironments(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_RUNTIME_DIR", "/private")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/private/bus")
	t.Setenv("SSH_AUTH_SOCK", "/private/agent")
	uid := strconv.Itoa(os.Getuid())

	got := strings.Join(ProcessEnvironment(), "\n")
	want := "LANG=C\nLC_ALL=C\nHOME=/home/test\nXDG_RUNTIME_DIR=/private\nDBUS_SESSION_BUS_ADDRESS=unix:path=/private/bus"
	if got != want {
		t.Fatalf("process environment:\n%s", got)
	}
	got = strings.Join(UserManagerEnvironment(), "\n")
	want = "LANG=C\nLC_ALL=C\nHOME=/home/test\nXDG_RUNTIME_DIR=/run/user/" + uid + "\nDBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + uid + "/bus"
	if got != want || strings.Contains(got, "/private") {
		t.Fatalf("user manager environment:\n%s", got)
	}
}
