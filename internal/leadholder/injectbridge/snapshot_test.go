//go:build linux && ataniq_canary

package injectbridge

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/inject"
	"ataniq/internal/inject/screen"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/terminalconsole/sessionpane"
)

// Styles and moves of the synthetic screens.
const (
	faintOn  = "\x1b[2m"
	faintOff = "\x1b[22m"
	clear    = "\x1b[2J\x1b[H"
)

func at(row, col int) string { // one-based, as the terminal counts
	return "\x1b[" + itoa(row) + ";" + itoa(col) + "H"
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// rows joins the given rows and pads to the screen's height with empty rows.
func rows(height int, first ...string) string {
	all := make([]string, height)
	copy(all, first)
	return strings.Join(all, "\n")
}

// The synthetic round trip: a known screen, with faint runs after and across
// characters that are two cells wide, is written by the agent into the
// session's real pane, and the bridge's Snapshot is that screen in text form:
// the text, the faint runs as offsets in the text of their row (not cell
// columns), the cursor as a cell, the title, the size and the two flags.
func TestSnapshotEqualsTheScreenThatWasWritten(t *testing.T) {
	const cols, height = 60, 12
	cases := []struct {
		name    string
		written string
		want    screen.Snapshot
	}{
		{
			name: "faint after and across wide characters",
			written: "\x1b]2;sample title\x07" + clear +
				"plain 日本語 text" +
				at(2, 1) + "界 " + faintOn + "ask anything" + faintOff + " tail" +
				at(3, 1) + "日" + faintOn + "本語" + faintOff + "x" +
				at(5, 3) + faintOn + "dim" + "\x1b[0m" +
				at(6, 1) + faintOn + "Ａ wide and faint 終" + faintOff +
				at(4, 7),
			want: screen.Snapshot{
				Text: rows(height,
					"plain 日本語 text",
					"界 ask anything tail",
					"日本語x",
					"",
					"  dim",
					"Ａ wide and faint 終",
				),
				Cols: cols, Rows: height,
				// Row 3 holds nothing, so the cursor's cell is past its text.
				CursorX: 6, CursorY: 3,
				Title: "sample title",
				Faint: []screen.Span{
					// "界" is one character and two cells: the run starts at
					// offset 2 of the text, which is cell 3.
					{Row: 1, From: 2, To: 14},
					{Row: 2, From: 1, To: 3},
					{Row: 4, From: 2, To: 5},
					{Row: 5, From: 0, To: 18},
				},
			},
		},
		{
			name: "the alternate screen with the cursor hidden",
			written: "\x1b[?1049h" + clear + "\x1b[?25l" + "\x1b]2;other title\x07" +
				at(2, 5) + "二 " + faintOn + "hint 語" + faintOff +
				at(2, 3),
			want: screen.Snapshot{
				Text: rows(height, "", "    二 hint 語"),
				Cols: cols, Rows: height,
				CursorX: 2, CursorY: 1, CursorHidden: true, AltScreen: true,
				Title: "other title",
				Faint: []screen.Span{{Row: 1, From: 6, To: 12}},
			},
		},
		{
			name:    "no faint run",
			written: clear + "plain 日本" + at(1, 1),
			want: screen.Snapshot{
				Text: rows(height, "plain 日本"),
				Cols: cols, Rows: height,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := startSession(t, "raw; print "+hx(c.written)+"; wait", func(cfg *session.Config) {
				cfg.Cols, cfg.Rows = cols, height
			})
			b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion})

			var got screen.Snapshot
			read := func() {
				inFrozen(t, b, func(v inject.View, _ func([]byte) error) { got = v.Snapshot() })
			}
			poll(t, func() string {
				return fmt.Sprintf("the snapshot to be the written screen;\n got %+v\nwant %+v", got, c.want)
			}, func() bool {
				read()
				return reflect.DeepEqual(got, c.want)
			})

			// The same screen, asked of the pane itself: every field of its
			// text snapshot is in the bridge's.
			var pane sessionpane.TextSnapshot
			s.Screen(func(p *sessionpane.Pane, _ uint64) { pane = p.TextSnapshot() })
			if got.Text != pane.Text || got.Cols != pane.Cols || got.Rows != pane.Rows ||
				got.CursorX != pane.CursorX || got.CursorY != pane.CursorY ||
				got.CursorHidden != pane.CursorHidden || got.AltScreen != pane.AltScreen || got.Title != pane.Title {
				t.Errorf("the bridge's snapshot %+v is not the pane's %+v", got, pane)
			}
			if len(got.Faint) != len(pane.Faint) {
				t.Fatalf("%d faint runs, the pane has %d", len(got.Faint), len(pane.Faint))
			}
			for i, f := range pane.Faint {
				if got.Faint[i] != (screen.Span{Row: f.Row, From: f.From, To: f.To}) {
					t.Errorf("faint run %d = %+v, the pane's is %+v", i, got.Faint[i], f)
				}
			}
			// What the offsets mean: the runs cut out of the text are the
			// faint words that were written.
			for i, sp := range got.Faint {
				row := []rune(strings.Split(got.Text, "\n")[sp.Row])
				if w := string(row[sp.From:sp.To]); !strings.Contains(c.written, faintOn+w) {
					t.Errorf("faint run %d is %q, which was not written faint", i, w)
				}
			}
		})
	}
}

// The conversion alone, field by field, on values that differ from each other
// so that no two fields can be swapped unseen.
func TestSnapshotOfCopiesEveryField(t *testing.T) {
	in := sessionpane.TextSnapshot{
		Text: "one\ntwo", Cols: 41, Rows: 17, CursorX: 5, CursorY: 9,
		CursorHidden: true, AltScreen: true, Title: "a title",
		Faint: []sessionpane.FaintSpan{{Row: 1, From: 2, To: 3}, {Row: 4, From: 5, To: 6}},
	}
	want := screen.Snapshot{
		Text: "one\ntwo", Cols: 41, Rows: 17, CursorX: 5, CursorY: 9,
		CursorHidden: true, AltScreen: true, Title: "a title",
		Faint: []screen.Span{{Row: 1, From: 2, To: 3}, {Row: 4, From: 5, To: 6}},
	}
	got := snapshotOf(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshotOf = %+v, want %+v", got, want)
	}
	got.Faint[0].Row = 99
	if in.Faint[0].Row != 1 {
		t.Error("the faint runs share memory with the pane's")
	}
	for _, flags := range []sessionpane.TextSnapshot{{CursorHidden: true}, {AltScreen: true}} {
		got := snapshotOf(flags)
		if got.CursorHidden != flags.CursorHidden || got.AltScreen != flags.AltScreen || got.Faint != nil {
			t.Errorf("snapshotOf(%+v) = %+v", flags, got)
		}
	}
	// Both types have the same number of fields: a field added to either is
	// a conversion to write.
	if a, b := reflect.TypeOf(in).NumField(), reflect.TypeOf(want).NumField(); a != 9 || b != 9 {
		t.Errorf("the pane's snapshot has %d fields and the library's %d; the conversion copies 9", a, b)
	}
}
