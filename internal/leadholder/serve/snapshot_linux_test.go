//go:build linux

package serve

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// Case A5, the middle of an escape sequence: the snapshot waits for the end of
// the string, the client is told it waits, and then the snapshot is of the
// emulator at a complete point: the client's digest equals the Check.
func TestAttachInTheMiddleOfAnEscapeSequence(t *testing.T) {
	// An OSC string is left open; the agent's own echo goes into it, and a BEL
	// ends it when the writer's input arrives.
	e := newEnv(t, "raw; say READY; print "+hex.EncodeToString([]byte("\x1b]0;ti"))+"; echo 1; print 07; say DONE; wait",
		func(o *envOpts) { o.cfg = func(c *Config) { c.SnapshotNotice = 150 * time.Millisecond } })
	waitScreen(t, e.sess, "READY")
	poll(t, func() string { return "the parser to be inside the string" }, func() bool {
		var q bool
		e.sess.Screen(func(p *sessionpane.Pane, _ uint64) { q = p.Quiescent() })
		return !q
	})

	w := e.attach(wire.RoleWriter)
	n := w.waitNotice(wire.ReasonSnapshotWaiting)
	if n.Text == "" {
		t.Error("an empty notice")
	}
	if w.noticeCount(wire.ReasonSnapshotWaiting) != 1 {
		t.Error("the notice is sent once")
	}
	w.mu.Lock()
	if w.snapshots != 0 {
		t.Errorf("a snapshot of an emulator in the middle of a string")
	}
	w.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	if w.noticeCount(wire.ReasonSnapshotWaiting) != 1 {
		t.Error("the notice came twice")
	}

	w.input("x") // the writer can type while its snapshot waits
	w.waitSnapshots(1)
	w.waitChecks(1)
	waitScreen(t, e.sess, "DONE")
	w.waitText("DONE")
	waitQuiet(t, e.sess)
	e.inSync(w)
	if e.count(cntSnapshotWaiting) != 1 {
		t.Errorf("counter %d", e.count(cntSnapshotWaiting))
	}
}

// Case A5, a 2026 frame: with the mode set the snapshot waits for the hold
// time, then it is the emulator's real state, which includes what the frame
// has drawn so far.
func TestAttachDuringASynchronizedFrame(t *testing.T) {
	const hold = 400 * time.Millisecond
	e := newEnv(t, "raw; say READY; frameopen "+hex.EncodeToString([]byte("INFRAME"))+"; echo 1; print "+hex.EncodeToString([]byte("\x1b[?2026l"))+"; say AFTER; wait",
		func(o *envOpts) { o.cfg = func(c *Config) { c.SnapshotHold = hold } })
	waitScreen(t, e.sess, "READY")
	waitQuiet(t, e.sess)

	start := time.Now()
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	if d := time.Since(start); d < hold-50*time.Millisecond {
		t.Errorf("the snapshot came after %v, before the hold of %v", d, hold)
	}
	w.waitChecks(1)
	w.mu.Lock()
	has2026 := false
	for _, m := range w.modes[0].DEC {
		has2026 = has2026 || m == 2026
	}
	w.mu.Unlock()
	if !has2026 {
		t.Errorf("mode 2026 is not in the first Modes frame")
	}
	if got := w.text(); !strings.Contains(got, "INFRAME") {
		t.Errorf("the snapshot is not of the real state: %q", got)
	}
	w.input("x")
	waitScreen(t, e.sess, "AFTER")
	w.waitText("AFTER")
	waitQuiet(t, e.sess)
	e.inSync(w)
	w.mu.Lock()
	last := w.modes[len(w.modes)-1]
	w.mu.Unlock()
	for _, m := range last.DEC {
		if m == 2026 {
			t.Errorf("mode 2026 is still set in the last Modes frame: %v", last.DEC)
		}
	}
}

// worstCaseFiles writes the bytes that fill the top rows of a 500 by 200 screen
// (all 200 rows, but under the race detector, which makes the emulator ten times
// slower, a part of them) with a styled cell each: a 24 bit colour, a cluster of about 20 bytes, and a link on every
// seventh cell, over a number of URLs above the live limit. Each file is under
// the fake agent's read limit.
func worstCaseFiles(t testing.TB, dir string) []string {
	t.Helper()
	const cols, perFile = 500, 17000
	rows := worstRows
	var files []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		name := filepath.Join(dir, fmt.Sprintf("fill%d", len(files)))
		if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, name)
		b.Reset()
	}
	b.WriteString("\x1b[H")
	for i := 0; i < cols*rows; i++ {
		if i%cols == 0 && i > 0 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", i%251, (i/7)%251, (i/13)%251)
		link := i%7 == 0
		if link {
			fmt.Fprintf(&b, "\x1b]8;;https://example.test/%d\x1b\\", i%300)
		}
		b.WriteRune(rune('a' + i%26))
		for k := 0; k < 10; k++ {
			b.WriteRune(rune(0x300 + (i+k)%16))
		}
		if link {
			b.WriteString("\x1b]8;;\x1b\\")
		}
		if (i+1)%perFile == 0 {
			flush()
		}
	}
	flush()
	if len(files) > 8 {
		t.Fatalf("%d files", len(files))
	}
	for _, f := range files {
		if st, _ := os.Stat(f); st.Size() > 1<<20 {
			t.Fatalf("%s is %d bytes: over the fake agent's limit", f, st.Size())
		}
	}
	return files
}

// worstScript fills the screen, limits the scroll region to the last two rows
// so that the output that follows is cheap to emulate, and floods at rate bytes
// a second.
func worstScript(files []string, rate int) string {
	var sb strings.Builder
	sb.WriteString("raw")
	for _, f := range files {
		sb.WriteString("; printfile " + f)
	}
	sb.WriteString("; print " + hex.EncodeToString([]byte("\x1b[0m\x1b[199;200r\x1b[200;1H")))
	sb.WriteString("; say FILLED")
	fmt.Fprintf(&sb, "; flood %d 0 5", rate)
	return sb.String()
}

// Case A13, first half: a client attaches to the worst-case screen under
// continuous output; the snapshot is over the output budget and the client
// still gets it, then follows the output, and its Checks match.
func TestAttachToTheWorstCaseScreenUnderOutput(t *testing.T) {
	files := worstCaseFiles(t, t.TempDir())
	e := newEnv(t, worstScript(files, 200_000), func(o *envOpts) {
		o.session = func(c *session.Config) { c.Cols, c.Rows = 500, 200 }
		o.cfg = func(c *Config) { c.CheckInterval = 200 * time.Millisecond; c.QueueBytes = worstQueue }
	})
	waitPosition(t, e.sess, filesSize(t, files)) // the screen is filled; the flood has begun
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	v.mu.Lock()
	size := v.snapSizes[0]
	v.mu.Unlock()
	t.Logf("the snapshot is %d bytes", size)
	if size <= worstQueue {
		t.Fatalf("the snapshot is %d bytes: not larger than the output budget of %d", size, worstQueue)
	}
	v.waitChecks(3)
	v.mu.Lock()
	out := v.outBytes
	v.mu.Unlock()
	if out == 0 {
		t.Error("no output followed the snapshot")
	}
	if p := e.sess.Counters(); p["pty_scan_errors"] != 0 {
		t.Log("scan errors")
	}
	// The pane's cap counters are in status: the links over the limit.
	st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}).Status
	if st.Counters["links_over_limit"] == 0 {
		t.Errorf("the links over the limit were not counted: %v", st.Counters)
	}
}

// filesSize is the total size of the files: the fill is over when the session
// has produced that many bytes.
func filesSize(t testing.TB, files []string) uint64 {
	t.Helper()
	var n uint64
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		n += uint64(st.Size())
	}
	return n
}
