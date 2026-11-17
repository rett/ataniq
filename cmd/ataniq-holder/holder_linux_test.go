//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// The whole life of one holder, end to end (case H10): start, status, a writer
// ticket and attach, the agent's output in the snapshot, typed input reaching
// the agent, the agent exiting with status 3, the Exit frame, the final record
// with the three facts and the status, and the holder exiting 0.
func TestEndToEnd(t *testing.T) {
	r := newRig(t)
	// printfile fails (the agent exits 70) when holder.json is missing at the
	// agent's first step: the state file is written before the agent starts.
	script := "printfile " + filepath.Join(r.state, StateFileName) + "; raw; say HELLO-FROM-AGENT; echo 2; say BYE; exit 3"
	r.start(r.spec(script).build())
	r.waitReady()

	st := r.status()
	if st.Lead != "scratch/t" || st.Epoch != testEpoch || st.Runtime != "scratch" || st.WriterPresent || st.ViewerPresent {
		t.Errorf("status %+v", st)
	}
	if st.Size.Columns != 100 || st.Size.Rows != 30 {
		t.Errorf("size %+v", st.Size)
	}
	if st.Facts.TUIExited != wire.FactNo {
		t.Errorf("facts %+v", st.Facts)
	}

	c := r.attach(wire.RoleWriter)
	c.waitFor("the Welcome and a snapshot", func() bool { return c.welcome != nil && c.snapshots > 0 })
	c.mu.Lock()
	w := *c.welcome
	c.mu.Unlock()
	if w.Role != wire.RoleWriter || w.Lead != "scratch/t" || w.Epoch != testEpoch || w.Runtime != "scratch" || w.RuntimeVersion != "0" {
		t.Errorf("welcome %+v", w)
	}
	c.waitText("HELLO-FROM-AGENT")
	if got := r.status(); !got.WriterPresent {
		t.Errorf("status after attach: %+v", got)
	}

	c.input("hi")
	c.waitText("RX:6869")
	x := c.waitExit()
	c.noProblems()
	if x.ExitStatus == nil || *x.ExitStatus != 3 {
		t.Errorf("exit frame status %v", x.ExitStatus)
	}
	if x.Facts.TUIExited != wire.FactYes || x.Facts.TreeGone != wire.FactYes {
		t.Errorf("exit frame facts %+v", x.Facts)
	}
	if !strings.Contains(x.LastScreen, "BYE") {
		t.Errorf("last screen %q", x.LastScreen)
	}

	if code := r.wait(); code != exitOK {
		t.Fatalf("the holder exited %d; stderr: %s", code, r.errbuf.String())
	}
	rec := r.readFinal()
	f := rec.Facts
	if f.StreamEnded != session.Yes || f.TUIExited != session.Yes || f.TreeGone != session.Yes {
		t.Errorf("three facts %+v", f)
	}
	if f.Status == nil || f.Status.How != "exit" || f.Status.Status != 3 {
		t.Errorf("status %+v", f.Status)
	}
	if rec.Role != "scratch/t" || rec.Epoch != "7" || !strings.Contains(rec.Screen.Text, "BYE") {
		t.Errorf("record %+v", rec)
	}
	// The agent read holder.json: it did not fail with status 70 before 3.
	if !strings.Contains(rec.Screen.Text, "scratch/t") {
		t.Errorf("the agent did not print holder.json: %q", rec.Screen.Text)
	}
	// The sockets are gone with the service.
	for _, n := range []string{serve.AttachSocket, serve.ControlSocket} {
		if _, err := os.Lstat(filepath.Join(r.runtime, n)); err == nil {
			t.Errorf("%s is left behind", n)
		}
	}
	evs, err := eventlog.Read(r.state)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	var names []string
	for _, e := range evs {
		names = append(names, string(e.Event))
	}
	hasExit := false
	for _, n := range names {
		hasExit = hasExit || n == "exit"
	}
	if len(names) == 0 || names[0] != "start" || !hasExit {
		t.Errorf("events %v", names)
	}
}

// Case H7: a second holder for the same role lock exits 3 and disturbs
// nothing. Case H8: a control request for another epoch is refused.
func TestSecondHolderForTheSameRoleExits3(t *testing.T) {
	r := newRig(t)
	r.start(r.spec("raw; say FIRST; wait").build())
	r.waitReady()

	beforeState, err := os.ReadFile(filepath.Join(r.state, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	beforeLog := r.logBytes()
	otherErr := &syncBuf{}
	d := r.deps()
	d.stderr = otherErr
	// Another launch (another epoch and intent) of the same role, same folders.
	args := r.spec("raw; say SECOND; wait").set("epoch", "8").set("launch-intent", "other-intent").build()
	if code := run(args, d); code != exitLocked {
		t.Fatalf("the second holder exited %d, want %d; stderr: %s", code, exitLocked, otherErr.String())
	}
	if !strings.Contains(otherErr.String(), "already held") {
		t.Errorf("the refusal is not clear: %q", otherErr.String())
	}
	afterState, _ := os.ReadFile(filepath.Join(r.state, StateFileName))
	if string(afterState) != string(beforeState) {
		t.Errorf("the state file changed:\n%s\n%s", beforeState, afterState)
	}
	if got := r.logBytes(); string(got) != string(beforeLog) {
		t.Errorf("the event log changed")
	}

	// The first goes on: status answers, its sockets work, a client attaches.
	if st := r.status(); st.Epoch != testEpoch {
		t.Errorf("status %+v", st)
	}
	c := r.attach(wire.RoleViewer)
	c.waitText("FIRST")

	// H8: another epoch is refused, whatever the operation.
	for _, op := range []wire.Operation{wire.OpStatus, wire.OpStop, wire.OpReleaseWriter} {
		resp := r.mustControl(wire.Request{Op: op, Epoch: testEpoch + 1, CommandID: "cmd-1"})
		if resp.OK || resp.Code != wire.CodeWrongEpoch {
			t.Errorf("%s with another epoch: %+v", op, resp)
		}
	}
	if st := r.status(); st.Facts.TUIExited != wire.FactNo {
		t.Errorf("the agent was disturbed: %+v", st.Facts)
	}
	poll(t, func() string { return "the wrong-epoch refusals in the event log" }, func() bool {
		n := 0
		evs, _ := eventlog.Read(r.state)
		for _, e := range evs {
			if e.Event == eventlog.EventRefusal && e.Reason == "wrong-epoch" {
				n++
			}
		}
		return n >= 3
	})

	// An accepted stop ends the first holder cleanly.
	if resp := r.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := r.wait(); code != exitOK {
		t.Fatalf("the holder exited %d; stderr: %s", code, r.errbuf.String())
	}
	if rec := r.readFinal(); rec.Facts.TUIExited != session.Yes {
		t.Errorf("facts %+v", rec.Facts)
	}
}

// Case H7, the interleaving: two holders for one role that start together. Both
// are held where they are about to take the role lock; they are let go together.
// Exactly one runs, the other exits 3 having changed nothing, and the state
// file and the event log are the winner's, whichever of them wins. (With the
// lock only looked at before the state file is written, both get that far, and
// the loser's state file replaces the winner's.)
func TestTwoHoldersStartedTogetherOnlyOneRuns(t *testing.T) {
	r := newRig(t)
	type holder struct {
		epoch  uint64
		errbuf *syncBuf
		sigc   chan os.Signal
		done   chan int
	}
	var atLock sync.WaitGroup
	atLock.Add(2)
	atSession := make(chan uint64, 2) // the epoch of a holder that got as far as the session
	release := make(chan struct{})
	launch := func(epoch uint64) *holder {
		h := &holder{epoch: epoch, errbuf: &syncBuf{}, sigc: make(chan os.Signal, 8), done: make(chan int, 1)}
		d := r.deps()
		d.stderr = h.errbuf
		d.signals = func() (<-chan os.Signal, func()) { return h.sigc, func() {} }
		d.beforeRoleLock = func() { atLock.Done(); atLock.Wait() }
		inner := d.tweakSession
		d.tweakSession = func(c *session.Config) {
			atSession <- epoch
			<-release
			inner(c)
		}
		args := r.spec("raw; say RUNNING; wait").
			set("epoch", strconv.FormatUint(epoch, 10)).set("launch-intent", "intent-"+strconv.FormatUint(epoch, 10)).build()
		go func() { h.done <- run(args, d) }()
		return h
	}
	a, b := launch(11), launch(12)

	// One of them is refused at the lock and ends; the other is held in the
	// session's start-up, its state file written.
	var loser, winner *holder
	deadline := time.After(patience)
	for loser == nil {
		select {
		case code := <-a.done:
			loser, winner = a, b
			if code != exitLocked {
				t.Fatalf("holder %d exited %d, want %d; stderr: %s", a.epoch, code, exitLocked, a.errbuf.String())
			}
		case code := <-b.done:
			loser, winner = b, a
			if code != exitLocked {
				t.Fatalf("holder %d exited %d, want %d; stderr: %s", b.epoch, code, exitLocked, b.errbuf.String())
			}
		case <-deadline:
			t.Fatalf("neither holder was refused (%d reached the session); stderr: %s | %s", len(atSession), a.errbuf.String(), b.errbuf.String())
		}
	}
	if got := <-atSession; got != winner.epoch || len(atSession) != 0 {
		t.Fatalf("the holder that reached the session is %d, want the winner %d alone", got, winner.epoch)
	}
	if !strings.Contains(loser.errbuf.String(), "already held") {
		t.Errorf("the refusal is not clear: %q", loser.errbuf.String())
	}
	if st := r.readState(); st.Epoch != winner.epoch || st.LaunchIntent != "intent-"+strconv.FormatUint(winner.epoch, 10) {
		t.Errorf("the state file after the loser has gone is %+v, want the winner's (epoch %d)", st, winner.epoch)
	}

	close(release)
	poll(t, func() string { return "the winner's ready line; stderr: " + winner.errbuf.String() }, func() bool {
		select {
		case code := <-winner.done:
			t.Fatalf("the winner exited %d; stderr: %s", code, winner.errbuf.String())
		default:
		}
		return strings.Contains(winner.errbuf.String(), "ready:")
	})
	if st := r.readState(); st.Epoch != winner.epoch {
		t.Errorf("the state file of the running holder is %+v, want epoch %d", st, winner.epoch)
	}
	evs, err := eventlog.Read(r.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Error("the winner's event log is empty")
	}
	for _, e := range evs {
		if e.Epoch != "" && e.Epoch != strconv.FormatUint(winner.epoch, 10) {
			t.Errorf("an event of epoch %q in the winner's log: %+v", e.Epoch, e)
		}
	}
	if strings.Contains(loser.errbuf.String(), "ready:") {
		t.Errorf("the loser says it is ready: %q", loser.errbuf.String())
	}

	winner.sigc <- syscall.SIGTERM
	select {
	case code := <-winner.done:
		if code != exitOK {
			t.Errorf("the winner exited %d; stderr: %s", code, winner.errbuf.String())
		}
	case <-time.After(patience):
		t.Fatal("the winner did not stop")
	}
	if rec := r.readFinal(); rec.Epoch != strconv.FormatUint(winner.epoch, 10) {
		t.Errorf("final record %+v", rec)
	}
}

// The agent's environment is exactly the --env list, in order, while the
// holder's own environment holds a marker.
func TestAgentEnvironmentIsExactlyTheEnvList(t *testing.T) {
	const marker = "ATANIQ_HOLDER_TEST_MARKER"
	t.Setenv(marker, "leaked-value")
	t.Setenv("HOME", "/holder/real/home")
	t.Setenv("PATH", "/holder/real/path")
	r := newRig(t)
	report := filepath.Join(r.agentFiles, "report")
	// A descriptor the holder was started with, without close-on-exec: the
	// holder marks it before it starts the agent, which must not get it.
	leak := filepath.Join(r.agentFiles, "inherited-descriptor")
	if err := os.WriteFile(leak, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lfd, err := unix.Open(leak, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(lfd)
	script := "report " + report + "; say REPORTED; exit 0"
	extra := []string{"TERM=xterm-256color", "HOME=" + r.tree, "ZED=last", "A_FIRST=1", "EMPTY="}
	spec := r.spec(script, extra...)
	r.start(spec.build())
	r.waitReady()
	c := r.attach(wire.RoleViewer)
	c.waitText("REPORTED")
	if code := r.wait(); code != exitOK {
		t.Fatalf("the holder exited %d; stderr: %s", code, r.errbuf.String())
	}
	rep, err := fakeagent.ReadReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rep.Env, "\x00") != strings.Join(spec.env, "\x00") {
		t.Errorf("the agent's environment is\n%q\nwant exactly\n%q", rep.Env, spec.env)
	}
	for _, e := range rep.Env {
		if strings.Contains(e, "leaked-value") || strings.Contains(e, "/holder/real") {
			t.Errorf("the holder's environment reached the agent: %q", e)
		}
	}
	if rep.Cwd != r.tree {
		t.Errorf("the agent's working folder is %q, want %q", rep.Cwd, r.tree)
	}
	if rep.Cols != 100 || rep.Rows != 30 {
		t.Errorf("the agent's terminal is %dx%d", rep.Cols, rep.Rows)
	}
	// Nothing the holder held open reaches the agent: of descriptors above 2
	// it has only its own runtime's (the agent is a Go program).
	for fd, target := range rep.Fds {
		if fd > 2 && (!strings.HasPrefix(target, "anon_inode:") || strings.Contains(target, "inherited-descriptor")) {
			t.Errorf("the agent has descriptor %d (%s)", fd, target)
		}
	}
}

// holder.json exists, with its fields, before the agent runs; the files and
// sockets have the modes of design 1.7 and 2.1.
func TestStateFileAndModes(t *testing.T) {
	r := newRig(t)
	// The agent prints the state file, which fails the script when it is
	// missing; the screen then shows its first bytes.
	script := "printfile " + filepath.Join(r.state, StateFileName) + "; raw; echo; wait"
	started := time.Now()
	r.start(r.spec(script).build())
	r.waitReady()
	c := r.attach(wire.RoleViewer)
	c.waitText(`{"version":1,"role":"scratch/t"`)

	st := r.readState()
	if st.Version != 1 || st.Role != "scratch/t" || st.Epoch != testEpoch || st.LaunchIntent != "intent-0123456789abcdef" ||
		st.Runtime != "scratch" || st.RuntimeVersion != "0" || st.ProfileDigest != "none" || st.Tree != r.tree ||
		st.Cols != 100 || st.Rows != 30 || !st.Scratch {
		t.Errorf("state file %+v", st)
	}
	if st.Pid != os.Getpid() {
		t.Errorf("pid %d, want %d", st.Pid, os.Getpid())
	}
	if want, err := selfStartTicks(); err != nil || st.PidStartTicks != want || want == 0 {
		t.Errorf("start ticks %d, want %d (%v)", st.PidStartTicks, want, err)
	}
	if st.Started.Before(started.Add(-time.Second)) || st.Started.After(time.Now().Add(time.Second)) {
		t.Errorf("started %v", st.Started)
	}
	exe, _ := os.Executable()
	if st.Agent != exe {
		t.Errorf("agent %q, want only ARGV[0] %q", st.Agent, exe)
	}
	raw, _ := os.ReadFile(filepath.Join(r.state, StateFileName))
	if strings.Contains(string(raw), "ATANIQ_FAKEAGENT_SCRIPT") || strings.Contains(string(raw), "printfile") {
		t.Errorf("the state file holds the agent's environment or arguments: %s", raw)
	}

	for _, f := range []struct {
		path string
		want os.FileMode
	}{
		{filepath.Join(r.state, StateFileName), 0o600},
		{filepath.Join(r.state, eventlog.FileName), 0o600},
		{r.roleLock, 0o600},
		{filepath.Join(r.runtime, serve.AttachSocket), os.ModeSocket | 0o600},
		{filepath.Join(r.runtime, serve.ControlSocket), os.ModeSocket | 0o600},
	} {
		if got := mode(t, f.path); got != f.want {
			t.Errorf("%s has mode %v, want %v", f.path, got, f.want)
		}
	}
	// No temporary file is left behind.
	ents, _ := os.ReadDir(r.state)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temporary file is left: %s", e.Name())
		}
	}

	if resp := r.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	if got := mode(t, filepath.Join(r.state, session.FinalName)); got != 0o600 {
		t.Errorf("final.json has mode %v", got)
	}
}

// SIGTERM to the holder ends an agent that ignores SIGTERM, after the grace,
// and the final record is still written. SIGHUP changes nothing.
func TestSigtermStopsAnAgentThatIgnoresIt(t *testing.T) {
	r := newRig(t)
	r.start(r.spec("ignore TERM; raw; say STUBBORN; wait").build())
	r.waitReady()
	c := r.attach(wire.RoleViewer)
	c.waitText("STUBBORN")

	// SIGHUP, SIGPIPE: the holder and the agent go on.
	r.sigc <- syscall.SIGHUP
	r.sigc <- syscall.SIGPIPE
	if st := r.status(); st.Facts.TUIExited != wire.FactNo {
		t.Fatalf("a SIGHUP ended the agent: %+v", st.Facts)
	}
	select {
	case code := <-r.done:
		t.Fatalf("a SIGHUP ended the holder with %d", code)
	default:
	}

	begin := time.Now()
	r.sigc <- syscall.SIGTERM
	x := c.waitExit()
	if code := r.wait(); code != exitOK {
		t.Fatalf("the holder exited %d; stderr: %s", code, r.errbuf.String())
	}
	if took := time.Since(begin); took < session.DefaultStopGrace-200*time.Millisecond {
		t.Errorf("the agent died after %v: SIGKILL came before the 2 s grace", took)
	}
	rec := r.readFinal()
	if rec.Facts.Status == nil || rec.Facts.Status.Signal != int(syscall.SIGKILL) || rec.Facts.TreeGone != session.Yes {
		t.Errorf("final facts %+v status %+v", rec.Facts, rec.Facts.Status)
	}
	if x.ExitStatus == nil || *x.ExitStatus != 128+int(syscall.SIGKILL) {
		t.Errorf("exit frame status %v", x.ExitStatus)
	}
	if !strings.Contains(r.errbuf.String(), "stopping the agent (SIGTERM)") {
		t.Errorf("stderr %q", r.errbuf.String())
	}
}

// SIGINT ends the agent the same way, once however many times it is sent.
func TestSigintStopsTheAgent(t *testing.T) {
	r := newRig(t)
	r.stopGrace = 300 * time.Millisecond
	r.start(r.spec("raw; say UP; wait").build())
	r.waitReady()
	c := r.attach(wire.RoleViewer)
	c.waitText("UP")
	r.sigc <- syscall.SIGINT
	r.sigc <- syscall.SIGINT
	r.sigc <- syscall.SIGTERM
	c.waitExit()
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	if rec := r.readFinal(); rec.Facts.TUIExited != session.Yes || rec.Facts.TreeGone != session.Yes {
		t.Errorf("facts %+v", rec.Facts)
	}
}

// SIGQUIT, SIGUSR1 and SIGUSR2 stop the agent like SIGTERM does, in process:
// the final record is written and the holder exits 0.
func TestEveryStopSignalStopsTheAgent(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2} {
		t.Run(signalName(sig), func(t *testing.T) {
			r := newRig(t)
			r.stopGrace = 300 * time.Millisecond
			r.start(r.spec("raw; say UP; wait").build())
			r.waitReady()
			c := r.attach(wire.RoleViewer)
			c.waitText("UP")
			r.sigc <- sig
			c.waitExit()
			if code := r.wait(); code != exitOK {
				t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
			}
			if rec := r.readFinal(); rec.Facts.TUIExited != session.Yes || rec.Facts.TreeGone != session.Yes {
				t.Errorf("facts %+v", rec.Facts)
			}
			if !strings.Contains(r.errbuf.String(), "stopping the agent ("+signalName(sig)+")") {
				t.Errorf("stderr %q", r.errbuf.String())
			}
		})
	}
}

// No agent output and no typed text reaches stderr or the event log.
func TestNoAgentOutputOrInputInStderrOrTheLog(t *testing.T) {
	const outMarker, typedMarker = "OUTPUT-MARKER-4f2a", "TYPED-MARKER-9c1b"
	r := newRig(t)
	r.start(r.spec("raw; say " + outMarker + "; echo " + strconvLen(typedMarker) + "; say DONE").build())
	r.waitReady()
	c := r.attach(wire.RoleWriter)
	c.waitText(outMarker)
	c.input(typedMarker)
	c.waitText("DONE")
	c.waitExit()
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	hexTyped := ""
	for _, b := range []byte(typedMarker) {
		hexTyped += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&15])
	}
	logged := string(r.logBytes())
	for _, where := range []struct{ name, text string }{{"stderr", r.errbuf.String()}, {"event log", logged}} {
		for _, m := range []string{outMarker, typedMarker, hexTyped, "RX:"} {
			if strings.Contains(where.text, m) {
				t.Errorf("%s holds %q:\n%s", where.name, m, where.text)
			}
		}
	}
	if logged == "" || r.errbuf.String() == "" {
		t.Errorf("an empty log or stderr proves nothing")
	}
	// The final record, which holds the last screen, is a file of its own.
	if rec := r.readFinal(); !strings.Contains(rec.Screen.Text, outMarker) {
		t.Errorf("the final screen is %q", rec.Screen.Text)
	}
}

func strconvLen(s string) string { return strconv.Itoa(len(s)) }
