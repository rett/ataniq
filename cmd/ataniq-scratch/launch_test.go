package main

import (
	"reflect"
	"testing"
)

func TestAgentEnvIsOnlyTheFixedKeysAndTheGivenOnes(t *testing.T) {
	got := agentEnv("/s/home", "de_DE.UTF-8", []string{"X=1", "Y=2"})
	want := []string{"TERM=xterm-256color", "HOME=/s/home", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=de_DE.UTF-8", "X=1", "Y=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHolderAndSystemdRunArgv(t *testing.T) {
	l := layout{
		name:    "demo",
		runtime: "/run/user/1000/ataniq/scratch/demo", state: "/st/ataniq/scratch/demo", home: "/st/ataniq/scratch/demo/home",
		daemonLock: "/st/ataniq/scratch/demo/daemon.lock", roleLock: "/st/ataniq/scratch/demo/role.lock",
	}
	env := agentEnv(l.home, "C.UTF-8", []string{"K=V"})
	h := holderArgs(l, 7, "intent123", 100, 30, env, []string{"agent", "--x", "--", "y"})
	wantH := []string{
		"--scratch", "--role", "scratch/demo", "--epoch", "7", "--launch-intent", "intent123",
		"--runtime", "scratch", "--runtime-version", "0", "--profile-digest", "none",
		"--tree", "/st/ataniq/scratch/demo/home", "--state-dir", "/st/ataniq/scratch/demo",
		"--runtime-dir", "/run/user/1000/ataniq/scratch/demo",
		"--role-lock", "/st/ataniq/scratch/demo/role.lock", "--daemon-lock", "/st/ataniq/scratch/demo/daemon.lock",
		"--cols", "100", "--rows", "30",
		"--env", "TERM=xterm-256color", "--env", "HOME=/st/ataniq/scratch/demo/home",
		"--env", "PATH=/usr/local/bin:/usr/bin:/bin", "--env", "LANG=C.UTF-8", "--env", "K=V",
		"--", "agent", "--x", "--", "y",
	}
	if !reflect.DeepEqual(h, wantH) {
		t.Errorf("holder args:\n got %q\nwant %q", h, wantH)
	}
	got := systemdRunArgv("demo", "/bin/ataniq-holder", h)
	want := append([]string{
		"systemd-run", "--user", "--unit=ataniq-scratch-demo", "--collect", "--quiet",
		"--expand-environment=no",
		"-p", "KillMode=control-group", "-p", "Restart=no", "-p", "TimeoutStopSec=2",
		"-p", "NoNewPrivileges=yes", "-p", "CollectMode=inactive-or-failed",
		"--", "/bin/ataniq-holder",
	}, wantH...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("systemd-run argv:\n got %q\nwant %q", got, want)
	}
	if got, want := systemctlStopArgv("demo"), []string{"systemctl", "--user", "stop", "ataniq-scratch-demo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("stop argv %q", got)
	}
	if got, want := systemctlIsActiveArgv("demo"), []string{"systemctl", "--user", "is-active", "--quiet", "ataniq-scratch-demo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("is-active argv %q", got)
	}
}
