//go:build linux

package session

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/terminalconsole/sessionpane"
)

// termOf settles the fence and returns the view's Term.
func termOf(t *testing.T, f Fenced) (term TermState) {
	t.Helper()
	settle(t, f, 0, 100*time.Millisecond, 5*time.Second)
	frozen(t, f, func(v FenceView, _ func([]byte) error) { term = v.Term() })
	return term
}

func TestViewSnapshotHasTheTextCursorTitleAlternateScreenAndFaintSpansAsTextOffsets(t *testing.T) {
	e := newEnv(t)
	// A title, a wide character before a faint run, the cursor hidden.
	out := "\x1b]2;a title\x07\x1b[2J\x1b[1;1H世x\x1b[2mhint\x1b[0m end\x1b[?25l"
	s := e.start("raw; print "+hx(out)+"; wait", func(c *Config) { c.Cols, c.Rows = 60, 12 })
	waitScreen(t, s, "end")
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			snap := v.Snapshot()
			if want := "世xhint end"; strings.Split(snap.Text, "\n")[0] != want {
				t.Errorf("text %q", snap.Text)
			}
			// The characters of "hint" are 2 to 6 in the row's text; in cells
			// they are 3 to 7.
			if want := []sessionpane.FaintSpan{{Row: 0, From: 2, To: 6}}; !reflect.DeepEqual(snap.Faint, want) {
				t.Errorf("Faint %v, want %v", snap.Faint, want)
			}
			if snap.Cols != 60 || snap.Rows != 12 || snap.Title != "a title" || !snap.CursorHidden || snap.AltScreen {
				t.Errorf("snapshot %+v", snap)
			}
			if snap.CursorY != 0 || snap.CursorX != 11 {
				t.Errorf("cursor %d,%d, want the cell column 11 on row 0", snap.CursorX, snap.CursorY)
			}
			// Two calls see the same state.
			if again := v.Snapshot(); !reflect.DeepEqual(again, snap) {
				t.Errorf("a second Snapshot differs:\n%+v\n%+v", snap, again)
			}
		})
	})
}

func TestViewTermParserFrameAndBracketedPaste(t *testing.T) {
	for _, c := range []struct {
		name, script string
		want         TermState
	}{
		{"ground", "raw; say READY; wait", TermState{ParserGround: true}},
		{"mid sequence", "raw; say READY; print 1b5b; wait", TermState{}},
		{"frame open", "raw; say READY; mode sync; wait", TermState{ParserGround: true, FrameOpen: true}},
		{"bracketed paste on", "raw; say READY; mode paste; wait", TermState{ParserGround: true, BracketedPaste: true}},
		{"bracketed paste off again", "raw; say READY; mode paste; unmode paste; wait", TermState{ParserGround: true}},
		{"frame closed", "raw; say READY; mode sync; unmode sync; wait", TermState{ParserGround: true}},
		{"all three", "raw; say READY; mode paste sync; print 1b5b; wait", TermState{FrameOpen: true, BracketedPaste: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := e.start(c.script)
			fenced(t, s, func(f Fenced) {
				until(t, "the agent's output", func() bool {
					settle(t, f, 0, 50*time.Millisecond, 5*time.Second)
					return strings.Contains(viewText(t, f), "READY")
				})
				if got := termOf(t, f); got != c.want {
					t.Errorf("Term = %+v, want %+v", got, c.want)
				}
			})
		})
	}
}

// ScreenQuiet is the time since applied output last changed the screen.
// Output that changes nothing does not restart it.
func TestViewScreenQuietCountsFromTheLastChangeNotTheLastOutput(t *testing.T) {
	e := newEnv(t)
	// A change, then, 600 ms later, an OSC string nobody answers and that
	// changes nothing.
	s := e.start("raw; say ONE; sleep 600ms; print " + hx("\x1b]5555;noop\x07") + "; wait")
	waitScreen(t, s, "ONE")
	first := time.Now()
	until(t, "the second output to be applied", func() bool { return time.Since(first) > 900*time.Millisecond })
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			q := v.ScreenQuiet()
			elapsed := time.Since(first)
			// The screen changed about the time ONE was seen; the string at
			// 600 ms changed nothing. At least 700 ms, at most the truth.
			if q < 700*time.Millisecond || q > elapsed+500*time.Millisecond {
				t.Errorf("ScreenQuiet = %v, want between 700 ms and about %v", q, elapsed)
			}
		})
	})
}

func TestViewScreenQuietIsShortAfterAChangeAndGrows(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say ONE; wait")
	waitScreen(t, s, "ONE")
	fenced(t, s, func(f Fenced) {
		var q1, q2 time.Duration
		frozen(t, f, func(v FenceView, _ func([]byte) error) { q1 = v.ScreenQuiet() })
		time.Sleep(300 * time.Millisecond)
		frozen(t, f, func(v FenceView, _ func([]byte) error) { q2 = v.ScreenQuiet() })
		if q1 > 5*time.Second {
			t.Errorf("ScreenQuiet = %v just after output", q1)
		}
		if q2 < q1+250*time.Millisecond {
			t.Errorf("ScreenQuiet did not grow: %v then %v", q1, q2)
		}
	})
	// A resize changes the screen.
	time.Sleep(200 * time.Millisecond)
	if _, _, err := s.Resize(70, 20); err != nil {
		t.Fatal(err)
	}
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if q := v.ScreenQuiet(); q > 400*time.Millisecond {
				t.Errorf("ScreenQuiet = %v after a resize", q)
			}
		})
	})
}

func TestViewEnded(t *testing.T) {
	e := newEnv(t)
	live := e.start("raw; say READY; wait")
	waitScreen(t, live, "READY")
	fenced(t, live, func(f Fenced) {
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if v.Ended() {
				t.Error("Ended for an agent that runs")
			}
		})
	})

	e2 := newEnv(t)
	gone := e2.start("raw; say BYE; sleep 500ms; exit 0")
	fenced(t, gone, func(f Fenced) {
		until(t, "Ended", func() bool {
			ended := false
			frozen(t, f, func(v FenceView, _ func([]byte) error) { ended = v.Ended() })
			return ended
		})
	})
}

// Pending is asked at each call: false when nothing waits, true for the
// reader's buffer, true for bytes queued on the master that the reader has not
// taken, false after Settle.
func TestViewPendingIsAskedFreshForTheBufferAndForBytesQueuedOnTheMaster(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; sleep 400ms; say LATE; wait")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, write func([]byte) error) {
			if v.Pending() {
				t.Error("Pending with nothing written")
			}
			if err := write([]byte("g")); err != nil {
				t.Fatal(err)
			}
			// The agent writes "RX:67" at once: the reader reads it into the
			// buffer. Asked again, inside the same call, it is true now.
			until(t, "Pending to become true inside one Frozen call", v.Pending)
			// LATE, 400 ms later, is more of the buffer. Never cached: the
			// answer stays true, and nothing was applied.
			time.Sleep(600 * time.Millisecond)
			if !v.Pending() || strings.Contains(v.Snapshot().Text, "RX:") {
				t.Error("Pending is false with held output, or the output was applied")
			}
		})
		settle(t, f, 0, 100*time.Millisecond, 5*time.Second)
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if v.Pending() {
				t.Error("Pending after Settle")
			}
		})
	})
}

func TestViewPendingIsTrueForBytesQueuedOnTheMasterWhileTheReaderWaits(t *testing.T) {
	e := newEnv(t)
	// A small buffer: the reader fills it and waits, and the agent's output
	// queues on the master.
	s := e.start("raw; say READY; echo 1; flood 0 400000 2; wait", func(c *Config) { c.heldLimit = 4 << 10 })
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		until(t, "bytes to queue on the master", func() bool {
			n, err := s.host.Queued()
			return err == nil && n > 0
		})
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if !v.Pending() {
				t.Error("Pending is false with bytes queued on the master")
			}
		})
		// Settle takes the buffer; the flood goes on until it is all in.
		until(t, "everything to be applied", func() bool {
			settle(t, f, 0, 100*time.Millisecond, 10*time.Second)
			return s.Counters()["output_bytes"] >= uint64(len("READY\r\n")+7+400000)
		})
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if v.Pending() {
				t.Error("Pending after the last Settle")
			}
		})
	})
}
