//go:build linux

package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
)

// Case H10: the agent exits with status 3; the three facts, the status and the
// last screen are in the final record.
func TestExitStatusThreeGivesTheThreeFactsAndTheFinalRecord(t *testing.T) {
	e := newEnv(t)
	s := e.start("say LASTWORDS; exit 3")
	if f := s.Facts(); f.TUIExited == Yes && f.Status == nil {
		t.Errorf("inconsistent live facts %+v", f)
	}
	if _, err := s.Final(); !errors.Is(err, ErrNotOver) && s.Facts().TUIExited != Yes {
		// Before the end the record is not there (unless it was fast).
		t.Errorf("Final before the end: %v", err)
	}
	waitDone(t, s)
	f := s.Facts()
	if f.StreamEnded != Yes || f.TUIExited != Yes || f.TreeGone != Yes {
		t.Fatalf("facts %+v", f)
	}
	if f.Status == nil || f.Status.How != "exit" || f.Status.Status != 3 || f.Status.Signal != 0 {
		t.Fatalf("status %+v", f.Status)
	}
	if f.TreeMethod != "session" || !f.TreeWeak || !strings.HasPrefix(f.TreeProof, "session ") || !strings.Contains(f.TreeProof, "weaker proof") {
		t.Errorf("tree proof is not labelled as the weaker session method: %+v", f)
	}

	rec, err := s.Final()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.Facts, f) {
		t.Errorf("Final().Facts differs from Facts()")
	}
	if rec.Role != "proj/lead" || rec.Epoch != "e1" || rec.Version != FinalVersion {
		t.Errorf("record identity %+v", rec)
	}
	if rec.Ended.Before(rec.Started) || rec.Started.IsZero() {
		t.Errorf("times %v %v", rec.Started, rec.Ended)
	}
	if !strings.Contains(rec.Screen.Text, "LASTWORDS") || rec.Screen.Cols != 100 || rec.Screen.Rows != 30 {
		t.Errorf("screen %+v", rec.Screen)
	}
	for _, name := range []string{"pty_scan_errors", "reply_dropped_bytes", "held_keys_suspend", "held_keys_signal", "clipboard_blocked", "output_bytes"} {
		if _, ok := rec.Counters[name]; !ok {
			t.Errorf("counter %s missing from %v", name, rec.Counters)
		}
	}

	path := filepath.Join(e.stateDir, FinalName)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("final.json mode %v", st.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temporary file is left")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk FinalRecord
	if err := json.Unmarshal(b, &disk); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.Facts, f) || disk.Facts.Status.Status != 3 || disk.Screen.Text != rec.Screen.Text {
		t.Errorf("final.json holds %+v", disk.Facts)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["facts"].(map[string]any)["stream_ended"] != "yes" {
		t.Errorf("facts on disk: %v", raw["facts"])
	}

	evs := eventsNamed(e.events(), eventlog.EventExit)
	if len(evs) != 1 {
		t.Fatalf("exit events %d", len(evs))
	}
	ev := evs[0]
	if ev.ExitStatus == nil || *ev.ExitStatus != 3 || ev.TUIExited == nil || !*ev.TUIExited ||
		ev.StreamEnded == nil || !*ev.StreamEnded || ev.TreeGone == nil || !*ev.TreeGone {
		t.Errorf("exit event %+v", ev)
	}
	if start := eventsNamed(e.events(), eventlog.EventStart); len(start) != 1 || start[0].Role != "proj/lead" || start[0].Epoch != "e1" || start[0].Cols != 100 {
		t.Errorf("start event %+v", start)
	}
}

func TestLiveFactsWhileTheAgentRuns(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	f := s.Facts()
	if f.StreamEnded != No || f.TUIExited != No || f.TreeGone != No || f.Status != nil {
		t.Errorf("live facts %+v", f)
	}
	if _, err := s.Final(); !errors.Is(err, ErrNotOver) {
		t.Errorf("Final while running: %v", err)
	}
	select {
	case <-s.Done():
		t.Fatal("Done is closed while the agent runs")
	default:
	}
}

func TestDeathBySignalIsStatus128PlusN(t *testing.T) {
	e := newEnv(t)
	s := e.start("say BYE; die INT")
	waitDone(t, s)
	f := s.Facts()
	if f.TUIExited != Yes || f.Status == nil || f.Status.How != "signal" || f.Status.Status != 128+2 ||
		f.Status.Signal != 2 || f.Status.SignalName != "SIGINT" {
		t.Fatalf("facts %+v status %+v", f, f.Status)
	}
	if f.StreamEnded != Yes || f.TreeGone != Yes {
		t.Errorf("facts %+v", f)
	}
	ev := eventsNamed(e.events(), eventlog.EventExit)[0]
	if ev.ExitStatus == nil || *ev.ExitStatus != 130 {
		t.Errorf("exit event %+v", ev)
	}
}

// An agent that ignores SIGTERM is killed after the grace.
func TestStopKillsAnAgentThatIgnoresSIGTERM(t *testing.T) {
	e := newEnv(t)
	s := e.start("ignore TERM; say READY; wait")
	waitScreen(t, s, "READY")
	start := time.Now()
	s.Stop()
	s.Stop() // again: nothing breaks
	waitDone(t, s)
	f := s.Facts()
	if f.Status == nil || f.Status.How != "signal" || f.Status.SignalName != "SIGKILL" || f.Status.Status != 137 {
		t.Fatalf("status %+v", f.Status)
	}
	if f.TreeGone != Yes {
		t.Errorf("facts %+v", f)
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Errorf("killed after %v: the grace was not given", d)
	}
	if n := len(eventsNamed(e.events(), eventlog.EventState)); n != 1 {
		t.Errorf("%d state events for two Stops", n)
	}
}

// A process that keeps the terminal open after the TUI exited: the TUI is
// gone, the stream did not end, the tree is not proven gone.
func TestGrandchildHoldingTheTerminalGivesTuiExitedStreamNotEnded(t *testing.T) {
	e := newEnv(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	s := e.start("spawn same "+pidFile+" ttl=60s; exit 0", func(c *Config) {
		c.StreamWait = 300 * time.Millisecond
		c.TreeWait = 300 * time.Millisecond
	})
	waitDone(t, s)
	f := s.Facts()
	if f.TUIExited != Yes || f.StreamEnded != No || f.TreeGone != Unknown {
		t.Fatalf("facts %+v", f)
	}
	if f.Status == nil || f.Status.Status != 0 {
		t.Errorf("status %+v", f.Status)
	}
	if !strings.Contains(f.TreeProof, "remain") {
		t.Errorf("proof %q does not say what remains", f.TreeProof)
	}
	rec, err := s.Final()
	if err != nil || !reflect.DeepEqual(rec.Facts, f) {
		t.Fatalf("Final: %v %+v", err, rec.Facts)
	}
	ev := eventsNamed(e.events(), eventlog.EventExit)[0]
	if ev.StreamEnded == nil || *ev.StreamEnded || ev.TreeGone != nil {
		t.Errorf("exit event %+v", ev)
	}

	pid := readPid(t, pidFile)
	if gone(pid) {
		t.Fatal("the grandchild is gone before Close")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	poll(t, func() string { return "the grandchild to be killed by Close" }, func() bool { return gone(pid) })
}

// Case H7: a second session for the same role lock is refused, the first is
// undisturbed, and the lock is free after Close.
func TestRoleLockRefusesASecondSessionAndIsFreeAfterClose(t *testing.T) {
	e := newEnv(t)
	reportFile := filepath.Join(t.TempDir(), "report")
	s := e.start("report " + reportFile + "; say READY; wait")
	waitScreen(t, s, "READY")

	st, err := os.Stat(e.lock)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("lock file %v %v", st, err)
	}
	_, err = Start(e.config("say SECOND; wait"))
	if !errors.Is(err, ErrRoleLocked) {
		t.Fatalf("second Start: %v, want ErrRoleLocked", err)
	}
	if !strings.Contains(err.Error(), "lead.lock") {
		t.Errorf("the error does not name the lock: %v", err)
	}
	if f := s.Facts(); f.TUIExited != No {
		t.Errorf("the first session was disturbed: %+v", f)
	}
	if strings.Contains(screenText(s), "SECOND") {
		t.Error("the second agent ran")
	}
	// The agent does not inherit the lock.
	rep, err := fakeagent.ReadReport(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	for fd, target := range rep.Fds {
		if target == e.lock {
			t.Errorf("the agent holds the role lock as descriptor %d", fd)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Start(e.config("say AGAIN; exit 0"))
	if err != nil {
		t.Fatalf("Start after Close: %v", err)
	}
	defer s2.Close()
	waitDone(t, s2)
}

func TestStartRefusesWithoutWhatItNeeds(t *testing.T) {
	e := newEnv(t)
	for name, mod := range map[string]func(*Config){
		"no log":          func(c *Config) { c.Log = nil },
		"no lock":         func(c *Config) { c.RoleLock = "" },
		"no state folder": func(c *Config) { c.StateDir = filepath.Join(e.stateDir, "missing") },
		"bad argv":        func(c *Config) { c.Argv = []string{"agent"} },
	} {
		cfg := e.config("exit 0")
		mod(&cfg)
		if s, err := Start(cfg); err == nil {
			s.Close()
			t.Errorf("%s: Start succeeded", name)
		}
	}
	// A failed start does not leave the lock held.
	s := e.start("exit 0")
	waitDone(t, s)
}

func TestCloseOfARunningSessionStopsItAndKeepsTheFinalRecord(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done is not closed after Close")
	}
	rec, err := s.Final()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Facts.Status == nil || rec.Facts.Status.SignalName != "SIGTERM" || rec.Facts.Status.Status != 143 {
		t.Errorf("status %+v", rec.Facts.Status)
	}
	if _, err := os.Stat(filepath.Join(e.stateDir, FinalName)); err != nil {
		t.Error(err)
	}
}

func TestFinalScreenIsCapped(t *testing.T) {
	// 30 rows of 500 cells, each a letter with 10 combining marks (21 bytes):
	// over 256 KiB as text.
	cell := "a" + strings.Repeat("́", 10)
	row := strings.Repeat(cell, 500) + "\r\n"
	file := filepath.Join(t.TempDir(), "screen")
	if err := os.WriteFile(file, []byte(strings.Repeat(row, 30)), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	s := e.start("raw; printfile "+file+"; exit 0", func(c *Config) { c.Cols, c.Rows = 500, 40 })
	waitDone(t, s)
	rec, err := s.Final()
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Screen.Truncated || len(rec.Screen.Text) == 0 || len(rec.Screen.Text) > MaxFinalScreenBytes {
		t.Fatalf("screen truncated=%v, %d bytes", rec.Screen.Truncated, len(rec.Screen.Text))
	}
	if rec.Counters["final_screen_truncated"] != 1 {
		t.Errorf("the cut is not counted: %v", rec.Counters)
	}
	if !strings.HasSuffix(rec.Screen.Text, cell) {
		t.Error("the text is not cut at a row boundary")
	}
}

func TestAddCountIsBounded(t *testing.T) {
	e := newEnv(t)
	s := e.start("exit 0")
	s.AddCount("service_thing", 2)
	s.AddCount("service_thing", 3)
	for i := range MaxExtraCounters + 5 {
		s.AddCount("n"+strings.Repeat("x", i), 1)
	}
	s.AddCount(strings.Repeat("y", MaxCounterName+1), 1)
	c := s.Counters()
	if c["service_thing"] != 5 || c["extra_counters_dropped"] == 0 {
		t.Errorf("counters %v", c)
	}
	waitDone(t, s)
	rec, _ := s.Final()
	if rec.Counters["service_thing"] != 5 {
		t.Errorf("final counters %v", rec.Counters)
	}
}

// Case H14: no typed and no printed text is in the event log.
func TestEventLogHoldsNeitherTypedNorPrintedText(t *testing.T) {
	const typed, printed, pasted = "TYPEDSECRETq7x", "PRINTEDSECRETk3m", "PASTEDSECRETw9z"
	e := newEnv(t)
	s := e.start("cooked; say " + printed + "; echo; wait")
	waitScreen(t, s, printed)
	s.Input([]byte(typed + "\x1a\x03\n"))
	s.Paste(pasted + "\x1c\n")
	waitRX(t, s, typed+"\n"+pasted+"\n")
	s.Resize(80, 24)
	_ = s.Inject(InjectRequest{MessageID: "m1", Sender: "owner", Digest: "sha256:00"})
	s.Stop()
	waitDone(t, s)
	s.Close()
	e.log.Sync()

	var all []byte
	files := 0
	ents, err := os.ReadDir(e.logDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(e.logDir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
		files++
	}
	if files == 0 || len(all) == 0 {
		t.Fatal("no log")
	}
	for _, marker := range []string{typed, printed, pasted, "TYPEDSECRET", "PRINTEDSECRET", "PASTEDSECRET"} {
		for _, form := range [][]byte{[]byte(marker), []byte(hexOf(marker))} {
			if bytes.Contains(all, form) {
				t.Errorf("the event log holds %q", form)
			}
		}
	}
	names := map[eventlog.EventName]bool{}
	for _, ev := range e.events() {
		names[ev.Event] = true
	}
	for _, want := range []eventlog.EventName{eventlog.EventStart, eventlog.EventKeyHeld, eventlog.EventResize, eventlog.EventInject, eventlog.EventExit} {
		if !names[want] {
			t.Errorf("no %s event; have %v", want, names)
		}
	}
}

func hexOf(s string) string {
	const digits = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteByte(digits[s[i]>>4])
		b.WriteByte(digits[s[i]&15])
	}
	return b.String()
}

// Nothing leaks: not goroutines, not descriptors, not the lock.
func TestNothingLeaksAfterClose(t *testing.T) {
	e := newEnv(t)
	cycle := func() {
		s, err := Start(e.config("say HI; exit 0"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Start(e.config("exit 0")); !errors.Is(err, ErrRoleLocked) {
			t.Fatalf("second Start: %v", err)
		}
		var c capture
		if _, err := s.Subscribe(c.fn); err != nil {
			t.Fatal(err)
		}
		<-s.Done()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		cfg := e.config("exit 0")
		cfg.Argv = []string{"/nonexistent/agent"}
		if _, err := Start(cfg); err == nil {
			t.Fatal("a missing program started")
		}
		running, err := Start(e.config("raw; wait"))
		if err != nil {
			t.Fatal(err)
		}
		if err := running.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cycle() // the runtime opens its poller once
	fds0, gos0 := countFDs(t), runtime.NumGoroutine()
	for range 3 {
		cycle()
	}
	settled(t, fds0, gos0)
}
