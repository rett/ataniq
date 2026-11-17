//go:build linux

package session

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The position counts the bytes fed, and the subscriber sees every chunk in
// order, each at the position before it.
func TestPositionAndSubscriberOrderUnderLoad(t *testing.T) {
	const total = 256 << 10
	e := newEnv(t)
	s := e.start(fmt.Sprintf("raw; flood 0 %d 7", total))
	var c capture
	var first uint64
	unsub, err := s.SubscribeScreen(func(_ *sessionpane.Pane, pos uint64) { first = pos }, c.fn)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, s)
	unsub()
	want := fakeagent.FloodBytes(7, total)
	got := c.all()
	if string(got) != string(want[first:]) {
		t.Fatalf("the subscriber's bytes (%d) are not the stream from %d (%d bytes)", len(got), first, len(want[first:]))
	}
	next := first
	maxChunk := 0
	for i, p := range c.poss {
		if p != next {
			t.Fatalf("chunk %d at position %d, want %d", i, p, next)
		}
		next += uint64(len(c.chunks[i]))
		maxChunk = max(maxChunk, len(c.chunks[i]))
	}
	if next != total || position(s) != total {
		t.Fatalf("position %d (subscriber %d), want %d", position(s), next, uint64(total))
	}
	if maxChunk > 64<<10 {
		t.Errorf("a chunk of %d bytes: over the 64 KiB read", maxChunk)
	}
	if n := s.Counters()["output_bytes"]; n != total {
		t.Errorf("output_bytes %d", n)
	}
	t.Logf("%d chunks, first position %d", len(c.chunks), first)
}

func TestWithoutSubscriberTheSessionKeepsReading(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; flood 0 100000 3; say END")
	waitDone(t, s)
	if got := position(s); got != 100005 {
		t.Fatalf("position %d, want %d", got, 100005)
	}
	if !strings.Contains(screenText(s), "END") {
		t.Fatal("the emulator did not follow the output")
	}
}

func TestSubscribeRules(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	if _, err := s.Subscribe(nil); err == nil {
		t.Error("a nil subscriber was accepted")
	}
	var a, b capture
	unsub, err := s.Subscribe(a.fn)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	if _, err := s.SubscribeScreen(func(*sessionpane.Pane, uint64) { ran = true }, b.fn); !errors.Is(err, ErrSubscribed) {
		t.Errorf("second subscriber: %v, want ErrSubscribed", err)
	}
	if ran {
		t.Error("the snapshot ran although the subscriber was refused")
	}
	unsub()
	unsub() // idempotent
	unsub2, err := s.Subscribe(b.fn)
	if err != nil {
		t.Fatalf("after unsubscribe: %v", err)
	}
	unsub() // a stale unsubscribe does not remove the new subscriber
	if _, err := s.Subscribe(a.fn); !errors.Is(err, ErrSubscribed) {
		t.Errorf("the stale unsubscribe removed the new subscriber: %v", err)
	}
	unsub2()
}

// A snapshot taken under the lock and the chunks that follow reconstruct the
// stream: the text and the cursor at the position equal a reference emulator
// fed the same bytes, and the chunks start at that position.
func TestScreenSnapshotAndFollowingChunksReconstructTheStream(t *testing.T) {
	const a, b = 20000, 60000
	e := newEnv(t)
	s := e.start(fmt.Sprintf("raw; flood 0 %d 1; echo 1; flood 0 %d 2", a, b))
	poll(t, func() string { return fmt.Sprintf("position %d, now %d", a, position(s)) }, func() bool { return position(s) == a })

	var snapText string
	var snapCur uv.Position
	var snapPos uint64
	var c capture
	unsub, err := s.SubscribeScreen(func(p *sessionpane.Pane, pos uint64) {
		snapText, snapPos = paneText(p), pos
		snapCur, _ = p.Cursor()
	}, c.fn)
	if err != nil {
		t.Fatal(err)
	}
	if snapPos != a {
		t.Fatalf("snapshot position %d, want %d", snapPos, a)
	}
	if h := s.Input([]byte("x")); h.Err != nil {
		t.Fatal(h.Err)
	}
	waitDone(t, s)
	unsub()

	stream := append(fakeagent.FloodBytes(1, a), "RX:78\r\n"...)
	stream = append(stream, fakeagent.FloodBytes(2, b)...)
	ref := sessionpane.New(100, 30, sessionpane.Options{})
	defer ref.Close()
	ref.Write(stream[:a])
	refCur, _ := ref.Cursor()
	if got := paneText(ref); got != snapText || refCur != snapCur {
		t.Fatalf("the snapshot differs from a reference fed %d bytes: cursor %v vs %v", a, snapCur, refCur)
	}
	if c.poss[0] != a {
		t.Fatalf("the first chunk after the snapshot is at %d, want %d", c.poss[0], a)
	}
	if string(c.all()) != string(stream[a:]) {
		t.Fatal("the chunks after the snapshot are not the rest of the stream")
	}
	ref.Write(c.all())
	refCur, _ = ref.Cursor()
	var liveCur uv.Position
	var liveText string
	s.Screen(func(p *sessionpane.Pane, _ uint64) { liveText = paneText(p); liveCur, _ = p.Cursor() })
	if paneText(ref) != liveText || refCur != liveCur {
		t.Fatal("snapshot plus chunks do not reproduce the live screen")
	}
}

// Every snapshot taken while output is flowing equals a reference emulator fed
// the stream up to the snapshot's position.
func TestScreenIsConsistentWithItsPositionUnderOutput(t *testing.T) {
	const total = 128 << 10
	e := newEnv(t)
	s := e.start(fmt.Sprintf("raw; flood 0 %d 5", total))
	type snap struct {
		pos  uint64
		text string
		cur  uv.Position
	}
	var snaps []snap
	for {
		var sn snap
		s.Screen(func(p *sessionpane.Pane, pos uint64) {
			sn = snap{pos: pos, text: paneText(p)}
			sn.cur, _ = p.Cursor()
		})
		if len(snaps) == 0 || snaps[len(snaps)-1].pos != sn.pos {
			snaps = append(snaps, sn)
		}
		if sn.pos == total {
			break
		}
		select {
		case <-s.Done():
			if position(s) != total {
				t.Fatalf("position %d after the end", position(s))
			}
		default:
		}
		if len(snaps) > 4000 {
			break
		}
	}
	want := fakeagent.FloodBytes(5, total)
	checked := 0
	for i := 0; i < len(snaps); i += max(1, len(snaps)/5) {
		sn := snaps[i]
		ref := sessionpane.New(100, 30, sessionpane.Options{})
		ref.Write(want[:sn.pos])
		cur, _ := ref.Cursor()
		if paneText(ref) != sn.text || cur != sn.cur {
			t.Errorf("the snapshot at position %d differs from a reference fed that many bytes", sn.pos)
		}
		ref.Close()
		checked++
	}
	t.Logf("%d snapshots, %d checked", len(snaps), checked)
}

const osc11Query = "\x1b]11;?\x1b\\"

// The emulator's answers reach the agent and nothing else: the writer's
// colours, the device attributes, no clipboard answer, nothing in the output.
func TestRepliesReachTheAgentAndNothingElse(t *testing.T) {
	e := newEnv(t)
	got := filepath.Join(t.TempDir(), "answers")
	s := e.start("raw; say READY; echo 1; query osc10,osc11,osc12,da1 "+got+"; print 1b5d35323b633b6148656c6c6f1b5c; say AFTER; wait", func(c *Config) {
		c.Colours = sessionpane.Colours{Foreground: color.RGBA{R: 1, G: 2, B: 3, A: 255}}
	})
	waitScreen(t, s, "READY")
	// The writer's colours arrive after the start (design 1.2).
	s.SetColours(sessionpane.Colours{
		Foreground: color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 255},
		Background: color.RGBA{R: 0xaa, G: 0xbb, B: 0xcc, A: 255},
		Cursor:     color.RGBA{R: 0x44, G: 0x55, B: 0x66, A: 255},
	})
	var out capture
	unsub, err := s.Subscribe(out.fn)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	s.Input([]byte("x"))
	waitScreen(t, s, "AFTER")

	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b]10;rgb:1111/2222/3333", "\x1b]11;rgb:aaaa/bbbb/cccc", "\x1b]12;rgb:4444/5555/6666", "\x1b[?"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the agent's input %q lacks %q", b, want)
		}
	}
	if strings.Contains(string(b), "Hello") || strings.Contains(string(b), "]52") {
		t.Errorf("the agent's input %q carries a clipboard answer", b)
	}
	if o := string(out.all()); strings.Contains(o, "rgb:") {
		t.Errorf("a reply appeared in the output stream: %q", o)
	}
	if n := s.Counters()["clipboard_blocked"]; n != 1 {
		t.Errorf("clipboard_blocked = %d, want 1", n)
	}
	if strings.Contains(screenText(s), "Hello") {
		t.Error("the OSC 52 data reached the screen")
	}
}

// Replies wait for the input lock in a queue of at most ReplyQueueBytes; the
// rest is dropped and counted, and the session goes on.
func TestReplyQueueIsBounded(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "queries")
	if err := os.WriteFile(file, []byte(strings.Repeat("\x1b[c", 40000)), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; printfile "+file+"; say AFTER; wait", func(c *Config) {
		c.PTY.WriteTimeout = 200 * time.Millisecond
	})
	waitScreen(t, s, "READY")
	// Hold the input lock; under it, give the agent the byte that starts the
	// queries. The replies then pile up behind the lock.
	inLock, release := make(chan struct{}), make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- s.WithInputLock(func(w AgentWriter) error {
			if _, err := w.Write([]byte("x")); err != nil {
				return err
			}
			close(inLock)
			<-release
			return nil
		})
	}()
	<-inLock
	poll(t, func() string { return fmt.Sprintf("replies to be dropped; counters %v", s.Counters()) }, func() bool {
		return s.Counters()["reply_dropped_chunks"] > 0
	})
	if n := s.replies.pending(); n > ReplyQueueBytes {
		t.Errorf("%d bytes wait in the reply queue, bound %d", n, ReplyQueueBytes)
	}
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
	waitScreen(t, s, "AFTER")
	c := s.Counters()
	if c["reply_dropped_bytes"] == 0 || c["reply_dropped_chunks"] == 0 {
		t.Errorf("drops not counted: %v", c)
	}
	// The session is alive and still answers.
	if gone := s.Facts().TUIExited; gone != No {
		t.Errorf("the agent is gone: %v", gone)
	}
}

func TestSetColoursReachesOnlyTheAgent(t *testing.T) {
	e := newEnv(t)
	got := filepath.Join(t.TempDir(), "answers")
	s := e.start("raw; say READY; echo 1; query osc11,da1 "+got+"; say AFTER; wait", func(c *Config) {
		c.Colours = sessionpane.Colours{Background: color.RGBA{R: 9, A: 255}}
	})
	waitScreen(t, s, "READY")
	s.SetColours(sessionpane.Colours{Background: color.RGBA{R: 0xff, G: 0x80, A: 255}})
	s.Input([]byte("x"))
	waitScreen(t, s, "AFTER")
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1b]11;rgb:ffff/8080/0000"; !strings.Contains(string(b), want) {
		t.Errorf("answer %q lacks %q", b, want)
	}
}

func TestResizeClampsAndTheAgentSeesTheNewSize(t *testing.T) {
	e := newEnv(t)
	s := e.start("watchwinch; size; wait")
	waitScreen(t, s, "SIZE 100x30")
	cases := []struct{ in, out [2]int }{
		{[2]int{5, 2}, [2]int{20, 5}},
		{[2]int{9999, 9999}, [2]int{500, 200}},
		{[2]int{132, 43}, [2]int{132, 43}},
	}
	for _, c := range cases {
		cols, rows, err := s.Resize(c.in[0], c.in[1])
		if err != nil || cols != c.out[0] || rows != c.out[1] {
			t.Fatalf("Resize(%v) = %d, %d, %v; want %v", c.in, cols, rows, err, c.out)
		}
		waitScreen(t, s, fmt.Sprintf("WINCH %dx%d", c.out[0], c.out[1]))
		if pc, pr := s.Size(); pc != c.out[0] || pr != c.out[1] {
			t.Errorf("pane %dx%d, want %v", pc, pr, c.out)
		}
	}
	// The same size again is no change and no event.
	if _, _, err := s.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	var sizes []string
	for _, ev := range eventsNamed(e.events(), eventlog.EventResize) {
		sizes = append(sizes, fmt.Sprintf("%dx%d", ev.Cols, ev.Rows))
	}
	if got := strings.Join(sizes, " "); got != "20x5 500x200 132x43" {
		t.Errorf("resize events %q", got)
	}
}

func TestStartClampsTheInitialSize(t *testing.T) {
	e := newEnv(t)
	s := e.start("size; wait", func(c *Config) { c.Cols, c.Rows = 1, 1 })
	waitScreen(t, s, "SIZE 20x5")
	if c, r := s.Size(); c != 20 || r != 5 {
		t.Errorf("pane %dx%d", c, r)
	}
}
