package vt

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// replies collects what the emulator writes on its input pipe, which a write
// blocks on until something reads it, so that a test can set modes that
// answer (DECRQM, 2048) and read the answers when it wants them.
type replies struct {
	t  *testing.T
	ch chan string
}

func drainReplies(t *testing.T, e *Emulator) *replies {
	r := &replies{t: t, ch: make(chan string, 1024)}
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := e.Read(buf)
			if n > 0 {
				r.ch <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { e.Close() })
	return r
}

// expect waits for the reply want, which may arrive in pieces.
func (r *replies) expect(want string) {
	r.t.Helper()
	var got string
	deadline := time.After(5 * time.Second)
	for got != want {
		select {
		case s := <-r.ch:
			got += s
		case <-deadline:
			r.t.Fatalf("reply %q, want %q", got, want)
		}
		if !strings.HasPrefix(want, got) {
			r.t.Fatalf("reply %q, want %q", got, want)
		}
	}
}

func modeSequence(m ansi.Mode, final byte) string {
	if _, ok := m.(ansi.DECMode); ok {
		return fmt.Sprintf("\x1b[?%d%c", m.Mode(), final)
	}
	return fmt.Sprintf("\x1b[%d%c", m.Mode(), final)
}

// Every mode of the table is recorded when set and reset, calls the callbacks
// as it did before the table, and is answered by DECRQM.
func TestRecordedModesAreRecordedAndCalledBack(t *testing.T) {
	for mode := range recordedModes {
		t.Run(fmt.Sprintf("%T/%d", mode, mode.Mode()), func(t *testing.T) {
			e := NewEmulator(10, 3)
			r := drainReplies(t, e)
			var on, off []ansi.Mode
			e.SetCallbacks(Callbacks{
				EnableMode:  func(m ansi.Mode) { on = append(on, m) },
				DisableMode: func(m ansi.Mode) { off = append(off, m) },
			})
			e.WriteString(modeSequence(mode, 'h'))
			if mode == ansi.ModeInBandResize {
				// Setting it reports the size at once.
				r.expect(ansi.InBandResize(3, 10, 0, 0))
			}
			if got := e.modes[mode]; got != ansi.ModeSet {
				t.Fatalf("after set: %v", got)
			}
			if len(on) != 1 || on[0] != mode || len(off) != 0 {
				t.Fatalf("callbacks after set: on %v, off %v", on, off)
			}
			e.WriteString(modeSequence(mode, '$') + "p")
			r.expect(ansi.ReportMode(mode, ansi.ModeSet))
			e.WriteString(modeSequence(mode, 'l'))
			if got := e.modes[mode]; got != ansi.ModeReset {
				t.Fatalf("after reset: %v", got)
			}
			if len(on) != 1 || len(off) != 1 || off[0] != mode {
				t.Fatalf("callbacks after reset: on %v, off %v", on, off)
			}
			e.WriteString(modeSequence(mode, '$') + "p")
			r.expect(ansi.ReportMode(mode, ansi.ModeReset))
			if got := e.Counters().ModesIgnored; got != 0 {
				t.Errorf("ModesIgnored = %d", got)
			}
		})
	}
}

// The modes the emulator starts with are all in the table, and so are the
// modes the pane and the console follow.
func TestRecordedModesCoverTheDefaultsAndWhatTheConsoleFollows(t *testing.T) {
	e := NewEmulator(10, 3)
	for mode := range e.modes {
		if _, ok := recordedModes[mode]; !ok {
			t.Errorf("default mode %T(%d) is not in the table", mode, mode.Mode())
		}
	}
	for _, mode := range []ansi.Mode{
		ansi.ModeSynchronizedOutput, ansi.ModeUnicodeCore, ansi.ModeLightDark, ansi.ModeInBandResize,
		ansi.ModeBracketedPaste, ansi.ModeFocusEvent, ansi.ModeAltScreenSaveCursor,
		ansi.ModeMouseNormal, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent, ansi.ModeMouseExtSgr,
		ansi.ModeMouseExtUtf8, ansi.ModeMouseExtUrxvt, ansi.ModeMouseExtSgrPixel,
		ansi.DECMode(12), ansi.DECMode(1007),
		ansi.ANSIMode(2), ansi.ANSIMode(4), ansi.ANSIMode(12), ansi.ANSIMode(20),
	} {
		if _, ok := recordedModes[mode]; !ok {
			t.Errorf("%T(%d) is not in the table", mode, mode.Mode())
		}
	}
}

// A mode outside the table is not recorded, calls no callback, is counted
// each time it is set or reset, and DECRQM answers "not recognised".
func TestUnknownModesAreIgnoredAndCounted(t *testing.T) {
	e := NewEmulator(10, 3)
	r := drainReplies(t, e)
	called := 0
	e.SetCallbacks(Callbacks{
		EnableMode:  func(ansi.Mode) { called++ },
		DisableMode: func(ansi.Mode) { called++ },
	})
	before := len(e.modes)

	e.WriteString("\x1b[?5000h\x1b[?5000l\x1b[5000h\x1b[5000l")
	if got := e.Counters().ModesIgnored; got != 4 {
		t.Errorf("ModesIgnored = %d, want 4", got)
	}
	// Known modes of the same sequence still act, and the unknown one in the
	// middle counts once.
	e.WriteString("\x1b[?1;5001;1000h")
	if got := e.Counters().ModesIgnored; got != 5 {
		t.Errorf("ModesIgnored = %d, want 5", got)
	}
	if !e.isModeSet(ansi.ModeMouseNormal) || !e.isModeSet(ansi.ModeCursorKeys) || called != 2 {
		t.Errorf("known modes of the sequence: mouse %v, keys %v, %d callbacks", e.isModeSet(ansi.ModeMouseNormal), e.isModeSet(ansi.ModeCursorKeys), called)
	}
	if len(e.modes) != before {
		t.Errorf("%d modes recorded, want %d", len(e.modes), before)
	}

	e.WriteString("\x1b[?5000$p")
	r.expect(ansi.ReportMode(ansi.DECMode(5000), ansi.ModeNotRecognized))
	e.WriteString("\x1b[5000$p")
	r.expect(ansi.ReportMode(ansi.ANSIMode(5000), ansi.ModeNotRecognized))
	// A number that is a table mode in the other family is a different mode.
	e.WriteString("\x1b[?2h")
	e.WriteString("\x1b[1007h")
	if got := e.Counters().ModesIgnored; got != 7 {
		t.Errorf("ModesIgnored = %d, want 7", got)
	}
}

// A full reset (RIS) sets the defaults again and keeps the table bound.
func TestResetKeepsTheTable(t *testing.T) {
	e := NewEmulator(10, 3)
	drainReplies(t, e)
	e.WriteString("\x1b[?5000h\x1b[?2026h")
	n := len(e.modes)
	e.WriteString("\x1bc")
	if len(e.modes) >= n {
		t.Errorf("%d modes after RIS, %d before", len(e.modes), n)
	}
	if e.isModeSet(ansi.ModeSynchronizedOutput) {
		t.Error("2026 survived RIS")
	}
}

// Design case H2: setting 100 000 distinct mode numbers, in both families,
// records none of them and does not grow the emulator.
func TestHundredThousandModeNumbers(t *testing.T) {
	const n = 100000
	input := func() []byte {
		var b strings.Builder
		for i := 0; i < n; i++ {
			// Numbers the table does not hold, above the highest of it.
			fmt.Fprintf(&b, "\x1b[?%dh\x1b[%dh", 10000+i, 10000+i)
		}
		return []byte(b.String())
	}()
	e := NewEmulator(80, 24)
	drainReplies(t, e)
	before := len(e.modes)

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	e.Write(input)
	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(input)

	if len(e.modes) != before {
		t.Errorf("%d modes recorded, want %d", len(e.modes), before)
	}
	if got := e.Counters().ModesIgnored; got != 2*n {
		t.Errorf("ModesIgnored = %d, want %d", got, 2*n)
	}
	// A map holding 200 000 entries is over 10 MiB; the budget is far below
	// that and far above what an unchanged emulator costs.
	grown := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	t.Logf("%d bytes of input, heap grew %d bytes", len(input), grown)
	if grown > 256<<10 {
		t.Errorf("heap grew %d bytes", grown)
	}
}
