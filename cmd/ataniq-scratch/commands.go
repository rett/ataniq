package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"ataniq/internal/leadholder/wire"
)

// resolveHolder returns the holder binary: the one named, or ataniq-holder
// next to this executable. It must be an absolute path to an existing
// executable file.
func (a *app) resolveHolder(given string) (string, error) {
	path := given
	if path == "" {
		exe, err := a.executable()
		if err != nil {
			return "", fmt.Errorf("cannot find this executable to look for ataniq-holder next to it: %w", err)
		}
		path = filepath.Join(filepath.Dir(exe), "ataniq-holder")
	}
	if !isAbs(path) {
		return "", fmt.Errorf("the holder path is not absolute")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("holder binary: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("holder binary %s is not an executable file", path)
	}
	return path, nil
}

type exitResult struct {
	code int
	err  error
}

type readyKind int

const (
	readyOK readyKind = iota
	readyRefused
	readyExited
	readyTimeout
)

type readyResult struct {
	kind readyKind
	resp wire.Response // readyRefused
	exit exitResult    // readyExited
	last error         // readyTimeout: the last failure
}

// waitReady waits for the holder to answer a status request. exited, when not
// nil, reports a direct holder that ended.
func (a *app) waitReady(l layout, epoch uint64, exited <-chan exitResult) readyResult {
	deadline := time.Now().Add(a.lim.startWait)
	var last error
	for {
		req, err := newRequest(wire.OpStatus, epoch)
		if err != nil {
			return readyResult{kind: readyTimeout, last: err}
		}
		resp, err := callControl(l.controlSock, l.daemonLock, req, a.lim)
		if err == nil {
			if resp.OK && resp.Status != nil {
				return readyResult{kind: readyOK, resp: resp}
			}
			return readyResult{kind: readyRefused, resp: resp}
		}
		last = err
		select {
		case r := <-exited:
			return readyResult{kind: readyExited, exit: r}
		default:
		}
		if !time.Now().Before(deadline) {
			return readyResult{kind: readyTimeout, last: last}
		}
		select {
		case r := <-exited:
			return readyResult{kind: readyExited, exit: r}
		case <-time.After(a.lim.startPoll):
		}
	}
}

// terminate ends a direct holder that this command started: SIGTERM, then
// SIGKILL when it has not ended within lim.termWait.
func (a *app) terminate(c child, exited <-chan exitResult) {
	_ = c.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(a.lim.termWait):
	}
	_ = c.Signal(syscall.SIGKILL)
	select {
	case <-exited:
	case <-time.After(a.lim.termWait):
	}
}

func exitText(r exitResult) string {
	switch {
	case r.err != nil:
		return "it could not be waited for"
	case r.code < 0:
		return "it was ended by a signal"
	}
	return fmt.Sprintf("exit status %d", r.code)
}

func (a *app) cmdStart(cfg startConfig) int {
	l, err := a.layoutFor(cfg.name)
	if err != nil {
		return a.fail("%v", err)
	}
	holder, err := a.resolveHolder(cfg.holder)
	if err != nil {
		return a.fail("%v", err)
	}
	if err := a.makeFolders(l); err != nil {
		return a.fail("cannot make the scratch folders: %v", err)
	}
	hold, err := nextEpoch(l.epochFile, a.lim.lockWait)
	if err != nil {
		return a.fail("cannot take the next epoch: %v", err)
	}
	// The lock on the epoch file is held until the holder has answered or the
	// start has failed.
	defer hold.Release()
	intent, err := randomHex(16)
	if err != nil {
		hold.Undo()
		return a.fail("cannot make a launch intent: %v", err)
	}
	lang := langFrom(a.getenv)
	hArgs := holderArgs(l, hold.N, intent, cfg.cols, cfg.rows, agentEnv(l.home, lang, cfg.env), cfg.argv)

	var (
		proc   child
		exited chan exitResult
	)
	if cfg.direct {
		holderEnv := []string{agentPath, "LANG=" + lang}
		proc, err = a.launch.Start(append([]string{holder}, hArgs...), holderEnv, a.errout, a.errout)
		if err != nil {
			hold.Undo()
			return a.fail("cannot start the holder: %v", err)
		}
		exited = make(chan exitResult, 1)
		go func() {
			code, err := proc.Wait()
			exited <- exitResult{code, err}
		}()
	} else {
		code, err := a.launch.Run(systemdRunArgv(l.name, holder, hArgs), io.Discard, a.errout)
		if err != nil {
			hold.Undo()
			return a.fail("cannot run systemd-run: %v", err)
		}
		if code != 0 {
			hold.Undo()
			return a.fail("systemd-run failed with status %d; a unit named %s may already exist (systemctl --user status %s)", code, unitName(l.name), unitName(l.name))
		}
	}
	if err := os.Remove(l.ticketFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(a.errout, "warning: cannot remove an old ticket file: %v\n", err)
	}

	res := a.waitReady(l, hold.N, exited)
	switch res.kind {
	case readyExited:
		hold.Undo()
		return a.fail("the holder ended before it answered (%s)", exitText(res.exit))
	case readyRefused:
		if cfg.direct {
			a.terminate(proc, exited)
			hold.Undo()
		}
		return a.fail("the holder answered with a refusal (%s: %s); another holder may be running for this name", res.resp.Code, res.resp.Text)
	case readyTimeout:
		if cfg.direct {
			a.terminate(proc, exited)
			hold.Undo()
			return a.fail("the holder did not answer within %s (last: %v); it was ended", a.lim.startWait, res.last)
		}
		return a.fail("the holder did not answer within %s (last: %v); look at the journal of %s, or run: ataniq-scratch stop %s", a.lim.startWait, res.last, unitName(l.name), l.name)
	}
	hold.Release()

	fmt.Fprintln(a.out, "scratch agent started (scratch only: a development launcher, not for a real lead)")
	fmt.Fprintf(a.out, "name:        %s\n", l.name)
	fmt.Fprintf(a.out, "role:        scratch/%s\n", l.name)
	fmt.Fprintf(a.out, "epoch:       %d\n", hold.N)
	fmt.Fprintf(a.out, "runtime dir: %s\n", l.runtime)
	fmt.Fprintf(a.out, "state dir:   %s\n", l.state)
	if !cfg.direct {
		fmt.Fprintf(a.out, "holder:      transient systemd user unit %s\n", unitName(l.name))
		return exitOK
	}
	fmt.Fprintln(a.out, "holder:      direct child of this command, in the foreground until it exits")
	fmt.Fprintln(a.out, "note:        with --direct no unit cgroup holds the process tree, so the process-tree proof is the weaker one")
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	for {
		select {
		case s := <-sigs:
			_ = proc.Signal(s)
		case r := <-exited:
			if r.err == nil && r.code == 0 {
				fmt.Fprintln(a.out, "the holder exited with status 0")
				return exitOK
			}
			fmt.Fprintf(a.errout, "the holder ended (%s)\n", exitText(r))
			return exitFail
		}
	}
}

// target returns the folders of NAME and the epoch of its last start.
func (a *app) target(name string) (layout, uint64, error) {
	l, err := a.layoutFor(name)
	if err != nil {
		return l, 0, err
	}
	epoch, err := readEpoch(l.epochFile, a.lim.lockWait)
	return l, epoch, err
}

// do sends one request with a fresh command id to the holder of l.
func (a *app) do(l layout, epoch uint64, op wire.Operation, tweak func(*wire.Request)) (wire.Response, error) {
	req, err := newRequest(op, epoch)
	if err != nil {
		return wire.Response{}, err
	}
	if tweak != nil {
		tweak(&req)
	}
	return callControl(l.controlSock, l.daemonLock, req, a.lim)
}

// refusal prints a refusal of the holder and returns the failure status.
func (a *app) refusal(r wire.Response) int {
	fmt.Fprintf(a.errout, "refused: %s: %s\n", r.Code, r.Text)
	return exitFail
}

func (a *app) cmdTicket(cfg ticketConfig) int {
	l, epoch, err := a.target(cfg.name)
	if err != nil {
		return a.fail("%v", err)
	}
	ticket, err := wire.NewTicket()
	if err != nil {
		return a.fail("cannot mint a ticket: %v", err)
	}
	role := wire.RoleWriter
	if cfg.viewer {
		role = wire.RoleViewer
	}
	resp, err := a.do(l, epoch, wire.OpTicket, func(r *wire.Request) { r.Role, r.Ticket = role, ticket })
	if err != nil {
		return a.fail("%v", err)
	}
	if !resp.OK {
		return a.refusal(resp)
	}
	if err := writeTicketFile(l.ticketFile, ticket); err != nil {
		return a.fail("the holder took the ticket but it could not be written: %v", err)
	}
	fmt.Fprintln(a.out, l.ticketFile)
	return exitOK
}

// writeTicketFile writes the ticket (and no newline) to path with mode 0600,
// through a temporary file in the same folder and a rename.
func writeTicketFile(path, ticket string) error {
	suffix, err := randomHex(6)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".ticket.tmp-"+suffix)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(ticket)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func (a *app) cmdStatus(name string) int {
	l, epoch, err := a.target(name)
	if err != nil {
		return a.fail("%v", err)
	}
	resp, err := a.do(l, epoch, wire.OpStatus, nil)
	if err != nil {
		return a.fail("%v", err)
	}
	if !resp.OK {
		return a.refusal(resp)
	}
	if resp.Status == nil {
		return a.fail("the holder answered ok without a status")
	}
	s := resp.Status
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	fmt.Fprintf(a.out, "lead:           %s\n", s.Lead)
	fmt.Fprintf(a.out, "runtime:        %s\n", s.Runtime)
	if s.Epoch == epoch {
		fmt.Fprintf(a.out, "epoch:          %d\n", s.Epoch)
	} else {
		fmt.Fprintf(a.out, "epoch:          %d (the recorded epoch is %d)\n", s.Epoch, epoch)
	}
	fmt.Fprintf(a.out, "size:           %dx%d\n", s.Size.Columns, s.Size.Rows)
	fmt.Fprintf(a.out, "writer present: %s\n", yes(s.WriterPresent))
	fmt.Fprintf(a.out, "viewer present: %s\n", yes(s.ViewerPresent))
	fmt.Fprintf(a.out, "stream ended:   %s\n", s.Facts.StreamEnded)
	fmt.Fprintf(a.out, "tui exited:     %s\n", s.Facts.TUIExited)
	fmt.Fprintf(a.out, "tree gone:      %s\n", s.Facts.TreeGone)
	fmt.Fprintf(a.out, "idle:           %s\n", s.Idle)
	fmt.Fprintf(a.out, "title:          %q\n", s.Title)
	names := make([]string, 0, len(s.Counters))
	for k := range s.Counters {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Fprintf(a.out, "counters:       %d\n", len(names))
	for _, k := range names {
		fmt.Fprintf(a.out, "  %s: %d\n", k, s.Counters[k])
	}
	return exitOK
}

func (a *app) cmdReleaseWriter(name string) int {
	l, epoch, err := a.target(name)
	if err != nil {
		return a.fail("%v", err)
	}
	resp, err := a.do(l, epoch, wire.OpReleaseWriter, nil)
	if err != nil {
		return a.fail("%v", err)
	}
	if !resp.OK {
		return a.refusal(resp)
	}
	fmt.Fprintln(a.out, "writer released")
	return exitOK
}

// unitActive asks systemd whether the unit of name is active.
func (a *app) unitActive(name string) (bool, error) {
	code, err := a.launch.Run(systemctlIsActiveArgv(name), io.Discard, io.Discard)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

func (a *app) cmdStop(name string) int {
	l, epoch, err := a.target(name)
	if err != nil {
		return a.fail("%v", err)
	}
	status := exitOK
	resp, err := a.do(l, epoch, wire.OpStop, nil)
	switch {
	case err != nil:
		fmt.Fprintf(a.errout, "error: the holder did not take the stop request: %v\n", err)
		status = exitFail
	case !resp.OK:
		status = a.refusal(resp)
	default:
		fmt.Fprintln(a.out, "the holder was asked to stop")
	}

	unit := unitName(name)
	deadline := time.Now().Add(a.lim.stopWait)
	for {
		active, err := a.unitActive(name)
		if err != nil {
			fmt.Fprintf(a.out, "cannot ask systemd about %s (%v); the unit was not checked\n", unit, err)
			return status
		}
		if !active {
			fmt.Fprintf(a.out, "unit %s is not active (it has ended, or the holder was started with --direct)\n", unit)
			return status
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(a.lim.stopPoll)
	}
	code, err := a.launch.Run(systemctlStopArgv(name), io.Discard, a.errout)
	switch {
	case err != nil:
		fmt.Fprintf(a.errout, "error: cannot run systemctl: %v\n", err)
		return exitFail
	case code != 0:
		fmt.Fprintf(a.errout, "error: systemctl --user stop %s failed with status %d\n", unit, code)
		return exitFail
	}
	fmt.Fprintf(a.out, "unit %s was still active after %s: stopped it with systemctl\n", unit, a.lim.stopWait)
	return status
}
