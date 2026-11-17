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
//
// The holder builds the agent's environment from the --env list it is given
// and from nothing else: exactly this list, and the holder must not pass its
// own environment on. That matters in unit mode, where the holder itself runs
// with the user manager's environment (the real HOME and whatever the session
// imported), while with --direct it gets only PATH and LANG. The holder
// command's task asserts this rule.
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
//
// --expand-environment=no is not hardening. Without it the service manager
// expands $VAR and ${VAR} in every word after "--" from the user manager's
// environment (and turns $$ into $): in the holder's options, in each --env
// value and in the agent's argv. That would hand the caller's real HOME and
// session variables to the agent and rewrite the user's argv, and --direct
// does neither. The option needs systemd 254 or newer.
//
// --collect is the same property as -p CollectMode=inactive-or-failed (the
// systemd-run manual calls it a shortcut for it), so the property is not
// given a second time.
func systemdRunArgv(name, holder string, hArgs []string) []string {
	argv := []string{
		"systemd-run", "--user", "--unit=" + unitName(name), "--collect", "--quiet",
		"--expand-environment=no",
		"-p", "KillMode=control-group",
		"-p", "Restart=no",
		"-p", "TimeoutStopSec=2",
		"-p", "NoNewPrivileges=yes",
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
