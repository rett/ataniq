package sessionpane_test

import (
	"strings"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

const unlimited = 1 << 20

// textPane returns a pane of cols by rows showing s.
func textPane(t *testing.T, cols, rows int, s string) *sessionpane.Pane {
	t.Helper()
	p := newPane(t, cols, rows, sessionpane.Options{})
	p.Write([]byte(s))
	return p
}

func TestTextIsAStreamSelection(t *testing.T) {
	p := textPane(t, 20, 4, "hello world\r\nsecond row\r\nthird row")
	for _, tc := range []struct {
		from, to uv.Position
		want     string
	}{
		// From the start cell to the end of its row, whole rows between, and
		// the last row up to the end cell.
		{uv.Pos(6, 0), uv.Pos(4, 2), "world\nsecond row\nthird"},
		// The same cells given the other way round.
		{uv.Pos(4, 2), uv.Pos(6, 0), "world\nsecond row\nthird"},
		// On one row only the cells between the two are taken.
		{uv.Pos(2, 1), uv.Pos(5, 1), "cond"},
		{uv.Pos(3, 0), uv.Pos(3, 0), "l"},
		// Rows that end in blanks, and a row that is blank.
		{uv.Pos(0, 2), uv.Pos(19, 3), "third row\n"},
	} {
		got, ok := p.Text(tc.from, tc.to, unlimited)
		if !ok || got != tc.want {
			t.Errorf("%v-%v: got %q %v, want %q", tc.from, tc.to, got, ok, tc.want)
		}
	}
}

func TestTextTrimsTrailingBlanksOnly(t *testing.T) {
	p := textPane(t, 20, 2, "a   b     \r\n   c")
	got, ok := p.Text(uv.Pos(0, 0), uv.Pos(19, 1), unlimited)
	if want := "a   b\n   c"; !ok || got != want {
		t.Fatalf("got %q %v, want %q", got, ok, want)
	}
	// A selection of blanks alone is empty.
	if got, ok := p.Text(uv.Pos(10, 0), uv.Pos(19, 0), unlimited); !ok || got != "" {
		t.Fatalf("blanks: got %q %v", got, ok)
	}
}

func TestTextClampsToThePane(t *testing.T) {
	p := textPane(t, 10, 2, "abc\r\ndef")
	got, ok := p.Text(uv.Pos(-5, -5), uv.Pos(999, 999), unlimited)
	if want := "abc\ndef"; !ok || got != want {
		t.Fatalf("got %q %v, want %q", got, ok, want)
	}
}

// TestTextCopiesWideCharactersAndClustersOnce checks that a character that
// covers two columns, and a cluster of several code points, are copied once,
// from their first column.
func TestTextCopiesWideCharactersAndClustersOnce(t *testing.T) {
	const family = "\U0001F468‍\U0001F469‍\U0001F467"
	const accented = "é"
	p := textPane(t, 20, 1, "界x"+family+accented+"y")
	got, ok := p.Text(uv.Pos(0, 0), uv.Pos(19, 0), unlimited)
	if want := "界x" + family + accented + "y"; !ok || got != want {
		t.Fatalf("got %q %v, want %q", got, ok, want)
	}
	// A selection that starts on the second column of the wide character
	// leaves it out; one that ends on its first column takes it.
	if got, _ := p.Text(uv.Pos(1, 0), uv.Pos(2, 0), unlimited); got != "x" {
		t.Fatalf("from the second column: %q", got)
	}
	if got, _ := p.Text(uv.Pos(0, 0), uv.Pos(0, 0), unlimited); got != "界" {
		t.Fatalf("the first column alone: %q", got)
	}
}

func TestTextStopsAtTheLimit(t *testing.T) {
	p := textPane(t, 10, 2, "hello\r\nworld")
	all := "hello\nworld"
	if got, ok := p.Text(uv.Pos(0, 0), uv.Pos(9, 1), len(all)); !ok || got != all {
		t.Fatalf("at the limit: %q %v", got, ok)
	}
	if got, ok := p.Text(uv.Pos(0, 0), uv.Pos(9, 1), len(all)-1); ok || got != "" {
		t.Fatalf("over the limit: %q %v", got, ok)
	}
	// The newline counts too, also when nothing follows it.
	if _, ok := p.Text(uv.Pos(0, 0), uv.Pos(9, 1), len("hello")); ok {
		t.Fatal("a newline past the limit was taken")
	}
	r := textPane(t, 10, 2, "hello")
	if got, ok := r.Text(uv.Pos(0, 0), uv.Pos(9, 1), len("hello\n")); !ok || got != "hello\n" {
		t.Fatalf("a trailing newline at the limit: %q %v", got, ok)
	}
	if got, ok := r.Text(uv.Pos(0, 0), uv.Pos(9, 1), len("hello")); ok {
		t.Fatalf("a trailing newline past the limit was taken: %q", got)
	}
	// So do the blanks written before a character that follows them.
	b := textPane(t, 10, 1, "a   b")
	if got, ok := b.Text(uv.Pos(0, 0), uv.Pos(9, 0), len("a   b")); !ok || got != "a   b" {
		t.Fatalf("blanks at the limit: %q %v", got, ok)
	}
	if got, ok := b.Text(uv.Pos(0, 0), uv.Pos(9, 0), len("a   b")-1); ok {
		t.Fatalf("blanks past the limit were taken: %q", got)
	}
	// A single cell whose cluster is longer than the limit is refused.
	q := textPane(t, 10, 1, "a"+strings.Repeat("́", 600))
	if _, ok := q.Text(uv.Pos(0, 0), uv.Pos(9, 0), 1000); ok {
		t.Fatal("a 1201-byte cluster passed a 1000-byte limit")
	}
}

// TestTextReadsTheHeldFrame checks that Text reads what Draw shows: while a
// frame is held for synchronized output, that frame.
func TestTextReadsTheHeldFrame(t *testing.T) {
	c := newClock()
	p := newPane(t, 10, 1, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("before"))
	p.Write([]byte("\x1b[?2026h\x1b[2K\rafter"))
	if got, _ := p.Text(uv.Pos(0, 0), uv.Pos(9, 0), unlimited); got != "before" {
		t.Fatalf("held: %q", got)
	}
	p.Write([]byte("\x1b[?2026l"))
	if got, _ := p.Text(uv.Pos(0, 0), uv.Pos(9, 0), unlimited); got != "after" {
		t.Fatalf("released: %q", got)
	}
}

// TestTextCopiesAControlCharacterAsAPlaceholder puts control characters in
// cells, as the emulator itself does not, and checks that none reaches the
// text: each such cell is copied as a placeholder of its width.
func TestTextCopiesAControlCharacterAsAPlaceholder(t *testing.T) {
	p := textPane(t, 10, 1, "abcdefgh")
	sessionpane.SetCell(p, 1, 0, &uv.Cell{Content: "x\x1b[31m", Width: 1})
	sessionpane.SetCell(p, 2, 0, &uv.Cell{Content: "\x7f", Width: 1})
	sessionpane.SetCell(p, 3, 0, &uv.Cell{Content: "\u009b", Width: 1})
	sessionpane.SetCell(p, 4, 0, &uv.Cell{Content: "\u0085", Width: 2})
	got, ok := p.Text(uv.Pos(0, 0), uv.Pos(9, 0), unlimited)
	if want := "a???？gh"; !ok || got != want {
		t.Fatalf("got %q %v, want %q", got, ok, want)
	}
}

// TestTextConcurrentWithWritesAndResizes reads the text while other
// goroutines write to the pane and resize it. Run under -race it checks that
// Text reads the emulator under the pane's lock.
func TestTextConcurrentWithWritesAndResizes(t *testing.T) {
	p := newPane(t, 40, 10, sessionpane.Options{})
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				p.Write([]byte("\x1b[2J\x1b[Hline one\r\nline two 界\r\n"))
				p.Resize(20+(w+j)%40, 5+j%10)
			}
		}(w)
	}
	for j := 0; j < 400; j++ {
		if _, ok := p.Text(uv.Pos(0, 0), uv.Pos(79, 19), unlimited); !ok {
			t.Fatal("Text refused a small screen")
		}
	}
	wg.Wait()
}
