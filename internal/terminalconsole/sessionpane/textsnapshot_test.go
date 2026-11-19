package sessionpane_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

func TestTextSnapshotTextSizeCursorTitleAndScreen(t *testing.T) {
	p := newPane(t, 20, 5, sessionpane.Options{})
	p.Write([]byte("\x1b]2;a title\x07first\r\n  second   \x1b[4;3H"))
	ts := p.TextSnapshot()
	if want := "first\n  second\n\n\n"; ts.Text != want {
		t.Errorf("Text %q, want %q", ts.Text, want)
	}
	if ts.Cols != 20 || ts.Rows != 5 || ts.CursorX != 2 || ts.CursorY != 3 {
		t.Errorf("size %dx%d cursor %d,%d", ts.Cols, ts.Rows, ts.CursorX, ts.CursorY)
	}
	if ts.CursorHidden || ts.AltScreen || ts.Title != "a title" {
		t.Errorf("hidden %v alt %v title %q", ts.CursorHidden, ts.AltScreen, ts.Title)
	}
	if len(ts.Faint) != 0 {
		t.Errorf("faint spans %v on a screen with none", ts.Faint)
	}
	p.Write([]byte("\x1b[?25l\x1b[?1049h"))
	ts = p.TextSnapshot()
	if !ts.CursorHidden || !ts.AltScreen {
		t.Errorf("hidden %v alt %v after hiding the cursor and entering the alternate screen", ts.CursorHidden, ts.AltScreen)
	}
	if strings.TrimSpace(ts.Text) != "" {
		t.Errorf("the alternate screen shows %q", ts.Text)
	}
}

// The text is what Text gives for the whole screen, so that the two readings
// of one screen agree.
func TestTextSnapshotTextEqualsPaneText(t *testing.T) {
	p := newPane(t, 30, 6, sessionpane.Options{})
	p.Write([]byte("hello \x1b[2mfaint\x1b[0m 世界 x\r\n\r\n\x1b[1mbold\x1b[0m\x1b[6;1Hlast"))
	want, ok := p.Text(uv.Pos(0, 0), uv.Pos(29, 5), 1<<20)
	if !ok {
		t.Fatal("Text refused")
	}
	if got := p.TextSnapshot().Text; got != want {
		t.Errorf("TextSnapshot().Text %q, Text %q", got, want)
	}
}

// Faint spans are offsets in the characters of the row's text, not cell
// columns: a wide character before the span makes the two differ.
func TestTextSnapshotFaintSpansAreRuneOffsetsNotCells(t *testing.T) {
	p := newPane(t, 40, 5, sessionpane.Options{})
	// Two wide characters (four cells), x, then faint "hint" and plain text.
	p.Write([]byte("世界x\x1b[2mhint\x1b[0m end"))
	ts := p.TextSnapshot()
	row := strings.Split(ts.Text, "\n")[0]
	if want := "世界xhint end"; row != want {
		t.Fatalf("row %q, want %q", row, want)
	}
	// In characters: 世 0, 界 1, x 2, hint 3 to 6. In cells it would be 5 to 8.
	want := []sessionpane.FaintSpan{{Row: 0, From: 3, To: 7}}
	if !reflect.DeepEqual(ts.Faint, want) {
		t.Fatalf("Faint %v, want %v", ts.Faint, want)
	}
	runes := []rune(row)
	if got := string(runes[ts.Faint[0].From:ts.Faint[0].To]); got != "hint" {
		t.Errorf("the span covers %q, want %q", got, "hint")
	}
	// The cursor is a cell column: 2+2+1+4+4 cells, where the row has 11
	// characters.
	if ts.CursorX != 13 {
		t.Errorf("cursor column %d, want 13 cells", ts.CursorX)
	}
}

func TestTextSnapshotFaintSpansPerRowRunsBlanksAndTrailing(t *testing.T) {
	p := newPane(t, 30, 6, sessionpane.Options{})
	// Row 0: a faint run that holds blanks (one run), a plain gap, a second run.
	p.Write([]byte("\x1b[2mAsk  me\x1b[0m ok \x1b[2mmore\x1b[0m\r\n"))
	// Row 1: faint text with faint blanks after it, which the text drops.
	p.Write([]byte("\x1b[2mtail   \x1b[0m\r\n"))
	// Row 2: only faint blanks: no text, so no span.
	p.Write([]byte("\x1b[2m     \x1b[0m\r\n"))
	// Row 3: a faint run on a later row, after plain text.
	p.Write([]byte("plain \x1b[2mghost\x1b[0m"))
	ts := p.TextSnapshot()
	want := []sessionpane.FaintSpan{
		{Row: 0, From: 0, To: 7},
		{Row: 0, From: 11, To: 15},
		{Row: 1, From: 0, To: 4},
		{Row: 3, From: 6, To: 11},
	}
	if !reflect.DeepEqual(ts.Faint, want) {
		t.Errorf("Faint %v, want %v\ntext %q", ts.Faint, want, ts.Text)
	}
	rows := strings.Split(ts.Text, "\n")
	for _, sp := range ts.Faint {
		if n := len([]rune(rows[sp.Row])); sp.To > n {
			t.Errorf("span %v is past the end of its row's text (%d characters)", sp, n)
		}
	}
}

func TestTextSnapshotIsTheRealScreenNotTheHeldFrame(t *testing.T) {
	now := time.Unix(1000, 0)
	p := newPane(t, 20, 4, sessionpane.Options{Clock: func() time.Time { return now }})
	p.Write([]byte("old"))
	p.Write([]byte("\x1b[?2026h\x1b[1;1Hnew"))
	if got, _ := p.Text(uv.Pos(0, 0), uv.Pos(19, 0), 100); got != "old" {
		t.Fatalf("Text shows %q: the frame is not held, so this test proves nothing", got)
	}
	if got := strings.Split(p.TextSnapshot().Text, "\n")[0]; got != "new" {
		t.Errorf("the first row is %q, want the real screen's %q", got, "new")
	}
}

// SyncReplies returns only when every reply made so far has been handed to
// the receiver, however the drain goroutine is scheduled.
func TestSyncRepliesWaitsForTheLastReplyToReachTheReceiver(t *testing.T) {
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add})
	for i := 0; i < 300; i++ {
		r.reset()
		p.Write([]byte("\x1b[c"))
		p.SyncReplies()
		if got := r.String(); got != "\x1b[?62;1;6;22c" {
			t.Fatalf("round %d: %q after SyncReplies, want the device attributes reply", i, got)
		}
	}
	// With the handler of an OSC string.
	q := newPane(t, 80, 24, sessionpane.Options{Replies: r.add})
	if err := q.HandleOSC(7777, func(data []byte) ([]byte, bool) { return []byte("answer"), true }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		r.reset()
		q.Write([]byte("\x1b]7777;?\x07\x1b[c"))
		q.SyncReplies()
		if got := r.String(); got != "answer\x1b[?62;1;6;22c" {
			t.Fatalf("round %d: %q after SyncReplies, want the handler's reply, then the attributes", i, got)
		}
	}
}

func TestSyncRepliesChangesNothingAndReturnsAfterClose(t *testing.T) {
	var r replies
	p := sessionpane.New(80, 24, sessionpane.Options{Replies: r.add})
	before := p.Digest()
	p.SyncReplies()
	p.SyncReplies()
	if r.String() != "" || p.Digest() != before {
		t.Errorf("SyncReplies made a reply (%q) or changed the pane", r.String())
	}
	p.Close()
	done := make(chan struct{})
	go func() {
		p.SyncReplies()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SyncReplies blocked on a closed pane")
	}
}
