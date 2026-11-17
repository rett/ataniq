package main

import (
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// started is a scratch agent that "was started": an epoch file and a fake
// control server.
func started(t *testing.T, name string, epoch uint64) (*testEnv, layout, *fakeHolder) {
	t.Helper()
	e := newTestEnv(t)
	l := e.layout(t, name)
	h := newFakeHolder(t, l, epoch)
	if err := os.WriteFile(l.epochFile, []byte(strconv.FormatUint(epoch, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return e, l, h
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestTicketWritesTheFileAndNeverPrintsTheTicket(t *testing.T) {
	e, l, h := started(t, "tk", 1)
	a, out, errout := e.newApp(&fakeLauncher{})
	if code := a.run([]string{"ticket", "tk"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	if got := strings.TrimSpace(out.String()); got != l.ticketFile {
		t.Errorf("printed %q, want the path %q", got, l.ticketFile)
	}
	b, err := os.ReadFile(l.ticketFile)
	if err != nil {
		t.Fatal(err)
	}
	ticket := string(b)
	if !hex64.MatchString(ticket) {
		t.Errorf("ticket file holds %q", ticket)
	}
	if strings.Contains(out.String(), ticket) || strings.Contains(errout.String(), ticket) {
		t.Error("the ticket was printed")
	}
	if m := modeOf(t, l.ticketFile); m.Perm() != 0o600 || !m.IsRegular() {
		t.Errorf("ticket file mode %v", m)
	}
	// The holder got the same ticket for the writer, with the epoch and a
	// command id.
	reqs := h.requests()
	if len(reqs) != 1 || reqs[0].Op != wire.OpTicket || reqs[0].Role != wire.RoleWriter || reqs[0].Ticket != ticket || reqs[0].Epoch != 1 || reqs[0].CommandID == "" {
		t.Errorf("requests %+v", reqs)
	}
	// No temporary file is left behind.
	entries, _ := os.ReadDir(l.runtime)
	for _, en := range entries {
		if strings.Contains(en.Name(), "tmp") {
			t.Errorf("left %s", en.Name())
		}
	}
}

func TestTicketViewerAndBusyRefusal(t *testing.T) {
	e, l, h := started(t, "tk", 1)
	a, out, errout := e.newApp(&fakeLauncher{})
	if code := a.run([]string{"ticket", "tk", "--viewer"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	if reqs := h.requests(); reqs[0].Role != wire.RoleViewer {
		t.Errorf("role %q", reqs[0].Role)
	}
	first, _ := os.ReadFile(l.ticketFile)

	// A second viewer is refused: the code is printed, the status is 1, the
	// old ticket file is untouched and the new ticket is printed nowhere.
	a2, out2, errout2 := e.newApp(&fakeLauncher{})
	if code := a2.run([]string{"ticket", "tk", "--viewer"}); code != exitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errout2.String(), "refused: busy: ") {
		t.Errorf("stderr %q", errout2.String())
	}
	if out2.String() != "" {
		t.Errorf("stdout %q", out2.String())
	}
	if again, _ := os.ReadFile(l.ticketFile); string(again) != string(first) {
		t.Error("a refused ticket replaced the ticket file")
	}
	rejected := h.requests()[1].Ticket
	if strings.Contains(errout2.String(), rejected) || strings.Contains(out.String()+errout.String(), rejected) {
		t.Error("a ticket was printed")
	}
}

func TestTicketReplacesTheFileAtomically(t *testing.T) {
	e, l, _ := started(t, "tk", 1)
	os.WriteFile(l.ticketFile, []byte("old"), 0o644)
	os.Chmod(l.ticketFile, 0o644)
	a, _, errout := e.newApp(&fakeLauncher{})
	if code := a.run([]string{"ticket", "tk"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	if m := modeOf(t, l.ticketFile); m.Perm() != 0o600 {
		t.Errorf("mode %v", m)
	}
	if b, _ := os.ReadFile(l.ticketFile); !hex64.Match(b) {
		t.Errorf("file %q", b)
	}
}

func TestCommandsNeedAStartedAgent(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"ticket", "nope"}, {"status", "nope"}, {"release-writer", "nope"}, {"stop", "nope"}} {
		fl := &fakeLauncher{}
		a, _, errout := e.newApp(fl)
		if code := a.run(args); code != exitFail || !strings.Contains(errout.String(), "never started") {
			t.Errorf("%q: exit %d, stderr %q", args, code, errout.String())
		}
		if len(fl.runList()) != 0 {
			t.Errorf("%q ran %q", args, fl.runList())
		}
	}
}

func TestCommandsWithoutAHolder(t *testing.T) {
	e := newTestEnv(t)
	l := e.layout(t, "gone")
	os.MkdirAll(l.state, 0o700)
	os.MkdirAll(l.runtime, 0o700)
	os.WriteFile(l.epochFile, []byte("1\n"), 0o600)
	for _, args := range [][]string{{"ticket", "gone"}, {"status", "gone"}, {"release-writer", "gone"}} {
		a, out, errout := e.newApp(&fakeLauncher{})
		if code := a.run(args); code != exitFail || !strings.Contains(errout.String(), "control socket") || out.String() != "" {
			t.Errorf("%q: exit %d, stdout %q stderr %q", args, code, out.String(), errout.String())
		}
	}
	if _, err := os.Lstat(l.ticketFile); err == nil {
		t.Error("a ticket file was written without a holder")
	}
}

func TestWrongEpochIsARefusal(t *testing.T) {
	e, l, _ := started(t, "we", 1)
	os.WriteFile(l.epochFile, []byte("4\n"), 0o600) // the record says 4, the holder runs 1
	for _, args := range [][]string{{"ticket", "we"}, {"status", "we"}, {"release-writer", "we"}} {
		a, _, errout := e.newApp(&fakeLauncher{})
		if code := a.run(args); code != exitFail || !strings.Contains(errout.String(), "refused: wrong-epoch: ") {
			t.Errorf("%q: exit %d, stderr %q", args, code, errout.String())
		}
	}
	if _, err := os.Lstat(l.ticketFile); err == nil {
		t.Error("a ticket file was written for a refused ticket")
	}
}

func TestStatusOutput(t *testing.T) {
	e, _, h := started(t, "st", 1)
	a, out, errout := e.newApp(&fakeLauncher{})
	if code := a.run([]string{"status", "st"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	want := `lead:           scratch/st
runtime:        scratch
epoch:          1
size:           100x30
writer present: no
viewer present: no
stream ended:   no
tui exited:     no
tree gone:      unknown
idle:           unknown
title:          "a title"
counters:       2
  alpha: 1
  zeta: 2
`
	if out.String() != want {
		t.Errorf("status output:\n%s\nwant:\n%s", out.String(), want)
	}
	if reqs := h.requests(); len(reqs) != 1 || reqs[0].Op != wire.OpStatus || reqs[0].CommandID == "" {
		t.Errorf("requests %+v", reqs)
	}
	// A second call has a fresh command id.
	a.run([]string{"status", "st"})
	if reqs := h.requests(); len(reqs) != 2 || reqs[0].CommandID == reqs[1].CommandID {
		t.Errorf("command ids %+v", reqs)
	}
}

func TestReleaseWriter(t *testing.T) {
	e, _, h := started(t, "rw", 1)
	a, out, errout := e.newApp(&fakeLauncher{})
	a.run([]string{"ticket", "rw"})
	if h.ticketOf(wire.RoleWriter) == "" {
		t.Fatal("no writer ticket")
	}
	if code := a.run([]string{"release-writer", "rw"}); code != exitOK || !strings.Contains(out.String(), "writer released") {
		t.Errorf("exit %d, out %q, err %q", code, out.String(), errout.String())
	}
	if h.ticketOf(wire.RoleWriter) != "" {
		t.Error("the writer was not released")
	}
	if reqs := h.requests(); reqs[len(reqs)-1].Op != wire.OpReleaseWriter {
		t.Errorf("requests %+v", reqs)
	}
}

func TestStopAskedAndTheUnitEnded(t *testing.T) {
	e, _, h := started(t, "sp", 1)
	fl := &fakeLauncher{}
	active := 2 // is-active answers "active" twice, then "inactive"
	fl.runFn = func(argv []string) (int, error) {
		if argv[2] != "is-active" {
			t.Errorf("unexpected %q", argv)
		}
		if active > 0 {
			active--
			return 0, nil
		}
		return 3, nil
	}
	a, out, errout := e.newApp(fl)
	a.lim.stopWait = 5 * time.Second
	if code := a.run([]string{"stop", "sp"}); code != exitOK {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	select {
	case <-h.stopped:
	default:
		t.Error("the holder was not asked to stop")
	}
	for _, s := range []string{"asked to stop", "is not active"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("output lacks %q: %q", s, out.String())
		}
	}
	for _, argv := range fl.runList() {
		if argv[2] == "stop" {
			t.Errorf("systemctl stop was run: %q", argv)
		}
	}
}

func TestStopStopsAUnitThatStaysActive(t *testing.T) {
	e, _, _ := started(t, "sp", 1)
	fl := &fakeLauncher{}
	fl.runFn = func(argv []string) (int, error) { return 0, nil } // always active, stop succeeds
	a, out, _ := e.newApp(fl)
	begin := time.Now()
	if code := a.run([]string{"stop", "sp"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if time.Since(begin) < a.lim.stopWait {
		t.Error("the unit was stopped before the wait was over")
	}
	runs := fl.runList()
	last := runs[len(runs)-1]
	if want := []string{"systemctl", "--user", "stop", "ataniq-scratch-sp"}; !reflect.DeepEqual(last, want) {
		t.Errorf("last run %q, want %q", last, want)
	}
	if !strings.Contains(out.String(), "still active") {
		t.Errorf("output %q", out.String())
	}
}

func TestStopSystemctlFailures(t *testing.T) {
	cases := []struct {
		name     string
		run      func(argv []string) (int, error)
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"systemctl stop fails", func(argv []string) (int, error) {
			if argv[2] == "stop" {
				return 5, nil
			}
			return 0, nil
		}, exitFail, "", "failed with status 5"},
		{"systemctl stop cannot run", func(argv []string) (int, error) {
			if argv[2] == "stop" {
				return -1, os.ErrPermission
			}
			return 0, nil
		}, exitFail, "", "cannot run systemctl"},
		// The holder was asked; the unit could not be checked. That is said,
		// and is not a failure.
		{"is-active cannot run", func(argv []string) (int, error) { return -1, os.ErrNotExist }, exitOK, "was not checked", ""},
	}
	for _, tc := range cases {
		e, _, _ := started(t, "sp", 1)
		a, out, errout := e.newApp(&fakeLauncher{runFn: tc.run})
		code := a.run([]string{"stop", "sp"})
		if code != tc.wantCode || !strings.Contains(out.String(), tc.wantOut) || !strings.Contains(errout.String(), tc.wantErr) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", tc.name, code, out.String(), errout.String())
		}
	}
}

func TestStopWhenTheHolderDoesNotAnswerStillLooksAtTheUnit(t *testing.T) {
	e := newTestEnv(t)
	l := e.layout(t, "sp")
	os.MkdirAll(l.state, 0o700)
	os.MkdirAll(l.runtime, 0o700)
	os.WriteFile(l.epochFile, []byte("1\n"), 0o600)
	fl := &fakeLauncher{runFn: func(argv []string) (int, error) { return 0, nil }}
	a, _, errout := e.newApp(fl)
	if code := a.run([]string{"stop", "sp"}); code != exitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	runs := fl.runList()
	if len(runs) == 0 || runs[len(runs)-1][2] != "stop" {
		t.Errorf("the unit was not stopped: %q", runs)
	}
	if !strings.Contains(errout.String(), "did not take the stop request") {
		t.Errorf("stderr %q", errout.String())
	}
}

func TestStopRefusedByTheHolder(t *testing.T) {
	e, _, _ := started(t, "sp", 1)
	l := e.layout(t, "sp")
	os.WriteFile(l.epochFile, []byte("6\n"), 0o600)
	fl := &fakeLauncher{runFn: func(argv []string) (int, error) { return 3, nil }}
	a, _, errout := e.newApp(fl)
	if code := a.run([]string{"stop", "sp"}); code != exitFail || !strings.Contains(errout.String(), "refused: wrong-epoch") {
		t.Errorf("exit %d stderr %q", code, errout.String())
	}
}

func TestEveryRequestCarriesTheEpochOfTheEpochFile(t *testing.T) {
	e, _, h := started(t, "ep", 3)
	fl := &fakeLauncher{runFn: func([]string) (int, error) { return 3, nil }}
	a, _, errout := e.newApp(fl)
	for _, args := range [][]string{{"status", "ep"}, {"ticket", "ep"}, {"ticket", "ep", "--viewer"}, {"release-writer", "ep"}, {"stop", "ep"}} {
		if code := a.run(args); code != exitOK {
			t.Fatalf("%q: exit %d %q", args, code, errout.String())
		}
	}
	ops := []wire.Operation{}
	for _, r := range h.requests() {
		ops = append(ops, r.Op)
		if r.Epoch != 3 || r.CommandID == "" {
			t.Errorf("request %+v", r)
		}
	}
	if want := []wire.Operation{wire.OpStatus, wire.OpTicket, wire.OpTicket, wire.OpReleaseWriter, wire.OpStop}; !reflect.DeepEqual(ops, want) {
		t.Errorf("ops %v", ops)
	}
}
