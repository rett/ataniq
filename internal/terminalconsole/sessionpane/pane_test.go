package sessionpane_test

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/terminalconsole/sessionpane"
)

// replies collects what a pane's drain goroutine delivers.
type replies struct {
	mu  sync.Mutex
	buf bytes.Buffer
	n   int
}

func (r *replies) add(p []byte) {
	r.mu.Lock()
	r.buf.Write(p)
	r.n++
	r.mu.Unlock()
}

func (r *replies) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *replies) reset() {
	r.mu.Lock()
	r.buf.Reset()
	r.mu.Unlock()
}

// await waits until the collected replies contain want.
func (r *replies) await(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(r.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no reply %q; got %q", want, r.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func newPane(t *testing.T, cols, rows int, o sessionpane.Options) *sessionpane.Pane {
	t.Helper()
	p := sessionpane.New(cols, rows, o)
	t.Cleanup(p.Close)
	return p
}

// within fails the test when f has not returned after d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s blocked for %v", what, d)
	}
}

func TestReplyDrainDeliversToPolicy(t *testing.T) {
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add})
	p.Write([]byte("\x1b[c"))
	r.await(t, "\x1b[?62;1;6;22c")
	p.Write([]byte("\x1b[3;7H\x1b[6n"))
	r.await(t, "\x1b[3;7R")
}

func TestReplyDrainNeverBlocksWriteOrResize(t *testing.T) {
	queries := bytes.Repeat([]byte("\x1b[c\x1b[>c\x1b[6n\x1b[?2004$p"), 20000)
	var r replies
	for _, o := range []struct {
		name string
		opts sessionpane.Options
	}{
		{"discard", sessionpane.Options{}},
		{"policy", sessionpane.Options{Replies: r.add}},
	} {
		t.Run(o.name, func(t *testing.T) {
			p := newPane(t, 80, 24, o.opts)
			within(t, 30*time.Second, "Write", func() { p.Write(queries) })
			// In-band resize reports (mode 2048) answer every resize.
			p.Write([]byte("\x1b[?2048h"))
			within(t, 30*time.Second, "Resize", func() {
				for i := 0; i < 5000; i++ {
					p.Resize(40+i%50, 10+i%20)
				}
			})
		})
	}
	if r.String() == "" {
		t.Fatal("the policy received no replies")
	}
}

func TestReplyDrainDiscardsByDefault(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	within(t, 30*time.Second, "Write", func() {
		for i := 0; i < 1000; i++ {
			p.Write([]byte("\x1b[c"))
		}
	})
	p.Write([]byte("still drawing"))
	if got := sessionpane.ScreenText(p); got != "still drawing" {
		t.Fatalf("screen %q", got)
	}
}

func TestCloseStopsDrainGoroutine(t *testing.T) {
	p := sessionpane.New(80, 24, sessionpane.Options{})
	select {
	case <-sessionpane.Drained(p):
		t.Fatal("drain goroutine ended before Close")
	default:
	}
	p.Close()
	select {
	case <-sessionpane.Drained(p):
	default:
		t.Fatal("Close returned before the drain goroutine ended")
	}
	p.Close() // again: no panic, no wait
	p.Write([]byte("after close"))
	p.Resize(100, 30)
	p.End(1, true)
	if got := sessionpane.ScreenText(p); got != "" {
		t.Fatalf("write after Close drew %q", got)
	}
	if c, r := p.Size(); c != 80 || r != 24 {
		t.Fatalf("resize after Close: %dx%d", c, r)
	}
	if ended, _, _ := p.Ended(); ended {
		t.Fatal("End after Close counted")
	}
}

// TestCloseConcurrentWithWrites closes a pane while other goroutines write
// queries and resize it. Run under -race it checks that the drain, the
// writers and Close share no unsynchronised state, and that no reply is
// delivered after Close returns.
func TestCloseConcurrentWithWrites(t *testing.T) {
	for i := 0; i < 20; i++ {
		var mu sync.Mutex
		closed, late := false, 0
		p := sessionpane.New(80, 24, sessionpane.Options{Replies: func([]byte) {
			mu.Lock()
			if closed {
				late++
			}
			mu.Unlock()
		}})
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for j := 0; j < 200; j++ {
					p.Write([]byte("\x1b[c\x1b[6nx"))
					p.Resize(40+(w+j)%40, 10+j%10)
					_, _ = p.Size()
				}
			}(w)
		}
		time.Sleep(time.Duration(i%4) * time.Millisecond)
		p.Close()
		mu.Lock()
		closed = true
		mu.Unlock()
		wg.Wait()
		select {
		case <-sessionpane.Drained(p):
		default:
			t.Fatal("drain goroutine still running after Close")
		}
		mu.Lock()
		if late != 0 {
			t.Fatalf("%d replies delivered after Close returned", late)
		}
		mu.Unlock()
	}
}

func TestResizeFollowsTheGivenSize(t *testing.T) {
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add})
	for _, c := range []struct{ cols, rows int }{{100, 30}, {80, 24}, {120, 40}, {2, 1}, {500, 200}} {
		p.Resize(c.cols, c.rows)
		if cols, rows := p.Size(); cols != c.cols || rows != c.rows {
			t.Fatalf("Resize(%d, %d): size %dx%d", c.cols, c.rows, cols, rows)
		}
		// The emulator follows: the cursor stops at the bottom right corner.
		r.reset()
		p.Write([]byte("\x1b[9999;9999H\x1b[6n"))
		r.await(t, fmt.Sprintf("\x1b[%d;%dR", c.rows, c.cols))
	}
}

func TestResizeClamps(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	for _, c := range []struct{ cols, rows, wantCols, wantRows int }{
		{501, 201, 500, 200},
		{10000, 10000, 500, 200},
		{math.MaxInt, math.MaxInt, 500, 200},
		{1, 0, 2, 1},
		{0, 0, 2, 1},
		{-5, -5, 2, 1},
		{math.MinInt, math.MinInt, 2, 1},
		{math.MinInt, math.MaxInt, 2, 200},
	} {
		p.Resize(c.cols, c.rows)
		if cols, rows := p.Size(); cols != c.wantCols || rows != c.wantRows {
			t.Errorf("Resize(%d, %d): size %dx%d, want %dx%d", c.cols, c.rows, cols, rows, c.wantCols, c.wantRows)
		}
	}
}

func TestBoundsNewClampsTheSize(t *testing.T) {
	for _, c := range []struct{ cols, rows, wantCols, wantRows int }{
		{80, 24, 80, 24},
		{math.MaxInt, math.MaxInt, sessionpane.MaxCols, sessionpane.MaxRows},
		{math.MinInt, -1, sessionpane.MinCols, sessionpane.MinRows},
	} {
		p := newPane(t, c.cols, c.rows, sessionpane.Options{})
		if cols, rows := p.Size(); cols != c.wantCols || rows != c.wantRows {
			t.Errorf("New(%d, %d): size %dx%d, want %dx%d", c.cols, c.rows, cols, rows, c.wantCols, c.wantRows)
		}
	}
	if sessionpane.MaxCols != 500 || sessionpane.MaxRows != 200 {
		t.Fatalf("cap %dx%d, want 500x200", sessionpane.MaxCols, sessionpane.MaxRows)
	}
}

func TestEndedWithStatus(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	if ended, _, _ := p.Ended(); ended {
		t.Fatal("ended before End")
	}
	p.Write([]byte("last line"))
	p.End(3, true)
	if ended, status, known := p.Ended(); !ended || status != 3 || !known {
		t.Fatalf("Ended() = %v, %d, %v", ended, status, known)
	}
	p.End(5, true)
	if _, status, _ := p.Ended(); status != 3 {
		t.Fatalf("a second End changed the status to %d", status)
	}
	p.Write([]byte("\r\x1b[2Kignored"))
	p.Resize(40, 10)
	if got := sessionpane.ScreenText(p); got != "last line" {
		t.Fatalf("screen after End %q", got)
	}
	if cols, rows := p.Size(); cols != 80 || rows != 24 {
		t.Fatalf("resize after End: %dx%d", cols, rows)
	}
}

func TestEndedWithUnknownStatus(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	p.End(7, false)
	if ended, status, known := p.Ended(); !ended || status != 0 || known {
		t.Fatalf("Ended() = %v, %d, %v", ended, status, known)
	}
}
