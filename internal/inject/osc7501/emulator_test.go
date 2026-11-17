package osc7501

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// replyStream drains an emulator's reply pipe the way the session pane does:
// a goroutine reads it, so a handler writing during the emulator's Write never
// waits on the test.
type replyStream struct {
	pipe io.WriteCloser
	ch   chan []byte
	done chan struct{}
}

func newEmulator(t *testing.T, handler vt.OscHandler) (*vt.Emulator, *replyStream) {
	t.Helper()
	emu := vt.NewEmulator(80, 24)
	if handler != nil {
		emu.RegisterOscHandler(7501, handler)
	}
	pipe, ok := emu.InputPipe().(io.WriteCloser)
	if !ok {
		t.Fatal("the emulator's reply pipe cannot be closed")
	}
	rs := &replyStream{pipe: pipe, ch: make(chan []byte, 16), done: make(chan struct{})}
	go func() {
		defer close(rs.done)
		buf := make([]byte, 256)
		for {
			n, err := emu.Read(buf)
			if n > 0 {
				rs.ch <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = rs.pipe.Close()
		<-rs.done
	})
	return emu, rs
}

// collect returns what the emulator has replied once it has been quiet for a
// short while, or fails after a longer wait when nothing came and want > 0.
func (rs *replyStream) collect(t *testing.T, want int) []byte {
	t.Helper()
	var got []byte
	deadline := time.After(5 * time.Second)
	for len(got) < want {
		select {
		case b := <-rs.ch:
			got = append(got, b...)
		case <-deadline:
			t.Fatalf("timed out with %q, want %d bytes", got, want)
		}
	}
	for {
		select {
		case b := <-rs.ch:
			got = append(got, b...)
		case <-time.After(100 * time.Millisecond):
			return got
		}
	}
}

// holderHandler is what a holder registers: it answers a query while the
// emulator is still parsing it.
func holderHandler(emu **vt.Emulator, tr *Tracker, seen *[][]byte) vt.OscHandler {
	return func(data []byte) bool {
		*seen = append(*seen, append([]byte(nil), data...))
		tr.Observe(data, time.Now())
		if reply, ok := Reply(data); ok {
			_, _ = (*emu).InputPipe().Write(reply)
		}
		return true
	}
}

func TestReplyPrecedesDA1Reply(t *testing.T) {
	var (
		emu  *vt.Emulator
		tr   Tracker
		seen [][]byte
		rs   *replyStream
	)
	emu, rs = newEmulator(t, holderHandler(&emu, &tr, &seen))

	// The agent's query and its DA1 sentinel arrive in one write.
	if _, err := emu.Write([]byte("\x1b]7501;?\a\x1b[c")); err != nil {
		t.Fatal(err)
	}
	da1 := ansi.PrimaryDeviceAttributes(62, 1, 6, 22)
	got := rs.collect(t, len(QueryReply)+len(da1))

	if !bytes.HasPrefix(got, []byte(QueryReply)) {
		t.Fatalf("replies = %q, want them to start with %q", got, QueryReply)
	}
	rest := got[len(QueryReply):]
	if !strings.HasPrefix(string(rest), "\x1b[?") || !strings.HasSuffix(string(rest), "c") {
		t.Fatalf("after the query reply: %q, want the DA1 reply", rest)
	}
	if bytes.Count(got, []byte(QueryReply)) != 1 {
		t.Fatalf("the query was answered more than once: %q", got)
	}
	if c := tr.Counts(); c.Queries != 1 || c.Reports != 0 || c.Invalid != 0 {
		t.Fatalf("Counts = %+v", c)
	}
	// Whether the emulator hands the handler the data with or without the
	// command number, it is recognised as a query.
	if len(seen) != 1 || !IsQuery(seen[0]) {
		t.Fatalf("handler saw %q", seen)
	}
}

func TestReplyOrderWithST(t *testing.T) {
	var (
		emu  *vt.Emulator
		tr   Tracker
		seen [][]byte
		rs   *replyStream
	)
	emu, rs = newEmulator(t, holderHandler(&emu, &tr, &seen))
	if _, err := emu.Write([]byte("\x1b]7501;?\x1b\\\x1b[c")); err != nil {
		t.Fatal(err)
	}
	got := rs.collect(t, len(QueryReply)+1)
	if !bytes.HasPrefix(got, []byte(QueryReply)) || len(got) == len(QueryReply) {
		t.Fatalf("replies = %q", got)
	}
}

// Without the handler the emulator gives only the DA1 reply, so the agent
// would see no answer to its query: the control for the tests above.
func TestNoHandlerNoReply(t *testing.T) {
	emu, rs := newEmulator(t, nil)
	if _, err := emu.Write([]byte("\x1b]7501;?\a\x1b[c")); err != nil {
		t.Fatal(err)
	}
	got := rs.collect(t, 1)
	if bytes.Contains(got, []byte("7501")) {
		t.Fatalf("replies = %q, want only the DA1 reply", got)
	}
}

func TestReportsGetNoReply(t *testing.T) {
	var (
		emu  *vt.Emulator
		tr   Tracker
		seen [][]byte
		rs   *replyStream
	)
	emu, rs = newEmulator(t, holderHandler(&emu, &tr, &seen))
	in := "\x1b]7501;state=working:app=codex\a\x1b]7501;state=blocked:app=codex:kind=permission\x1b\\\x1b[c"
	if _, err := emu.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	got := rs.collect(t, 1)
	if bytes.Contains(got, []byte("7501")) {
		t.Fatalf("replies = %q, want only the DA1 reply", got)
	}
	r, _, ok := tr.Last()
	if !ok || r.State != StateBlocked || r.Kind != "permission" || r.App != "codex" {
		t.Fatalf("Last = %+v, %v", r, ok)
	}
	if c := tr.Counts(); c.Reports != 2 || c.Queries != 0 {
		t.Fatalf("Counts = %+v", c)
	}
}
