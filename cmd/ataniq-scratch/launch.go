package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// launcher starts the processes of the command: systemd-run, systemctl and the
// direct holder. Tests give it a fake, so that an ordinary test never starts a
// unit.
type launcher interface {
	// Run runs argv (argv[0] is the program, found on PATH) with this
	// command's environment, waits for it and returns its exit status. The
	// error is only for a program that could not be run.
	Run(argv []string, stdout, stderr io.Writer) (int, error)
	// Start starts argv with exactly env as its environment and does not
	// wait for it.
	Start(argv []string, env []string, stdout, stderr io.Writer) (child, error)
}

// child is a process that Start started.
type child interface {
	// Wait waits for the process and returns its exit status, or -1 when a
	// signal ended it.
	Wait() (int, error)
	Signal(os.Signal) error
}

type execLauncher struct{}

func (execLauncher) Run(argv []string, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (execLauncher) Start(argv []string, env []string, stdout, stderr io.Writer) (child, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Its own process group: a Ctrl+C at the terminal reaches this command,
	// which forwards it once.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execChild{cmd}, nil
}

type execChild struct{ cmd *exec.Cmd }

func (c execChild) Wait() (int, error) {
	err := c.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (c execChild) Signal(s os.Signal) error { return c.cmd.Process.Signal(s) }

// unitName is the name of the transient unit of a scratch agent. The name
// makes a second start for the same NAME fail at systemd.
func unitName(name string) string { return "ataniq-scratch-" + name }

// agentEnv is the whole environment of the agent: TERM, HOME (the disposable
// home), PATH, LANG and each --env. Nothing else is inherited.
func agentEnv(home, lang string, extra []string) []string {
	env := []string{agentTerm, "HOME=" + home, agentPath, "LANG=" + lang}
	return append(env, extra...)
}

// holderArgs is the argument vector of the holder, without its program: the
// fixed command line of ataniq-holder.
func holderArgs(l layout, epoch uint64, intent string, cols, rows int, env, agentArgv []string) []string {
	args := []string{
		"--scratch",
		"--role", "scratch/" + l.name,
		"--epoch", strconv.FormatUint(epoch, 10),
		"--launch-intent", intent,
		"--runtime", "scratch",
		"--runtime-version", "0",
		"--profile-digest", "none",
		"--tree", l.home,
		"--state-dir", l.state,
		"--runtime-dir", l.runtime,
		"--role-lock", l.roleLock,
		"--daemon-lock", l.daemonLock,
		"--cols", strconv.Itoa(cols),
		"--rows", strconv.Itoa(rows),
	}
	for _, kv := range env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--")
	return append(args, agentArgv...)
}

// systemdRunArgv is the command that runs the holder in a transient systemd
// user unit (the lead-hosting design, section 1.1). It sets no other
// hardening property: the design marks those for qualification.
func systemdRunArgv(name, holder string, hArgs []string) []string {
	argv := []string{
		"systemd-run", "--user", "--unit=" + unitName(name), "--collect", "--quiet",
		"-p", "KillMode=control-group",
		"-p", "Restart=no",
		"-p", "TimeoutStopSec=2",
		"-p", "NoNewPrivileges=yes",
		"-p", "CollectMode=inactive-or-failed",
		"--", holder,
	}
	return append(argv, hArgs...)
}

func systemctlStopArgv(name string) []string {
	return []string{"systemctl", "--user", "stop", unitName(name)}
}

func systemctlIsActiveArgv(name string) []string {
	return []string{"systemctl", "--user", "is-active", "--quiet", unitName(name)}
}
