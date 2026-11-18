//go:build linux

package fakeagent

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Report is what the agent saw of itself, written by the report step.
type Report struct {
	Pid, Ppid int
	// Sid and Pgid are the session and process group ids; Tpgid is the
	// foreground process group of the terminal on stdin, or -1.
	Sid, Pgid, Tpgid int
	// StdinTTY, StdoutTTY and StderrTTY say whether each is a terminal.
	StdinTTY, StdoutTTY, StderrTTY bool
	// Ctty is true when the process has a controlling terminal (it can open
	// /dev/tty).
	Ctty bool
	// TTYName is the target of /proc/self/fd/0.
	TTYName string
	// Cols and Rows are the terminal size on stdin, zero when it is none.
	Cols, Rows int
	Cwd        string
	Args       []string
	// Fds maps every open descriptor to its target (the readlink of
	// /proc/self/fd/N), to show what was inherited. The descriptor the
	// listing itself used is left out.
	Fds map[int]string
	// Env is the whole environment, in order, exactly as the agent got it.
	Env []string
}

func isTTY(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	return err == nil
}

func (a *agent) report(args []string) error {
	var b bytes.Buffer
	line := func(k string, v any) { fmt.Fprintf(&b, "%s %v\n", k, v) }
	line("pid", os.Getpid())
	line("ppid", os.Getppid())
	sid, _ := unix.Getsid(0)
	line("sid", sid)
	line("pgid", unix.Getpgrp())
	tpgid := -1
	if p, err := unix.IoctlGetInt(0, unix.TIOCGPGRP); err == nil {
		tpgid = p
	}
	line("tpgid", tpgid)
	line("stdin_tty", isTTY(0))
	line("stdout_tty", isTTY(1))
	line("stderr_tty", isTTY(2))
	ctty := false
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		ctty = true
		f.Close()
	}
	line("ctty", ctty)
	if n, err := os.Readlink("/proc/self/fd/0"); err == nil {
		line("tty_name", n)
	}
	if ws, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ); err == nil {
		line("cols", ws.Col)
		line("rows", ws.Row)
	}
	if wd, err := os.Getwd(); err == nil {
		line("cwd", wd)
	}
	if es, err := os.ReadDir("/proc/self/fd"); err == nil {
		self := "/proc/" + strconv.Itoa(os.Getpid()) + "/fd"
		for _, e := range es {
			n, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			if target, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && target != self {
				line("fd", strconv.Itoa(n)+" "+target)
			}
		}
	}
	for _, s := range os.Args {
		line("arg", strconv.Quote(s))
	}
	for _, s := range os.Environ() {
		line("env", strconv.Quote(s))
	}
	return writeFileAtomic(args[0], b.Bytes())
}

// ReadReport reads a file the report step wrote.
func ReadReport(path string) (Report, error) {
	b, err := readLimited(path, 1<<20)
	if err != nil {
		return Report{}, err
	}
	r := Report{Tpgid: -1}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), " ")
		num := func() int { n, _ := strconv.Atoi(v); return n }
		flag := func() bool { return v == "true" }
		switch k {
		case "pid":
			r.Pid = num()
		case "ppid":
			r.Ppid = num()
		case "sid":
			r.Sid = num()
		case "pgid":
			r.Pgid = num()
		case "tpgid":
			r.Tpgid = num()
		case "stdin_tty":
			r.StdinTTY = flag()
		case "stdout_tty":
			r.StdoutTTY = flag()
		case "stderr_tty":
			r.StderrTTY = flag()
		case "ctty":
			r.Ctty = flag()
		case "tty_name":
			r.TTYName = v
		case "cols":
			r.Cols = num()
		case "rows":
			r.Rows = num()
		case "cwd":
			r.Cwd = v
		case "fd":
			ns, target, _ := strings.Cut(v, " ")
			n, err := strconv.Atoi(ns)
			if err != nil {
				return Report{}, fmt.Errorf("fakeagent: bad fd line %q", v)
			}
			if r.Fds == nil {
				r.Fds = map[int]string{}
			}
			r.Fds[n] = target
		case "arg", "env":
			s, err := strconv.Unquote(v)
			if err != nil {
				return Report{}, fmt.Errorf("fakeagent: bad %s line %q", k, v)
			}
			if k == "arg" {
				r.Args = append(r.Args, s)
			} else {
				r.Env = append(r.Env, s)
			}
		}
	}
	return r, sc.Err()
}
