package sessionpane_test

import (
	"bytes"
	"image/color"
	"strings"
	"testing"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

func TestModeSetFollowsTheModesFixture(t *testing.T) {
	var (
		drag     = sessionpane.Modes{Mouse: uv.MouseModeDrag}
		motion   = sessionpane.Modes{Mouse: uv.MouseModeMotion}
		sgr      = sessionpane.Modes{Mouse: uv.MouseModeMotion, MouseEncoding: uv.MouseEncodingSGR}
		paste    = sessionpane.Modes{Mouse: uv.MouseModeMotion, MouseEncoding: uv.MouseEncodingSGR, BracketedPaste: true}
		all      = sessionpane.Modes{Mouse: uv.MouseModeMotion, MouseEncoding: uv.MouseEncodingSGR, BracketedPaste: true, FocusEvents: true}
		none     = sessionpane.Modes{}
		expected = []sessionpane.Modes{
			none, none, none, // clear, alternate screen, cursor hidden
			drag, drag, // 1000 and 1002; half of 1003
			motion, sgr, sgr, // 1003 and half of 1006; 1006; an ESC alone
			paste, all, all, // 2004; 1004; text
			sgr,                    // 1004 and 2004 off
			none, none, none, none, // mouse off; main screen; text; end
		}
	)
	rec := fixture(t, "modes.jsonl")
	if len(rec.Events) != len(expected) {
		t.Fatalf("fixture has %d events, the test expects %d", len(rec.Events), len(expected))
	}
	p := newPane(t, rec.Cols, rec.Rows, sessionpane.Options{})
	for i, e := range rec.Events {
		apply(p, e)
		if got := p.Modes(); got != expected[i] {
			t.Errorf("after event %d (%q): modes %+v, want %+v", i, e.Data, got, expected[i])
		}
	}
}

func TestModeSetSplitAtEveryByte(t *testing.T) {
	out := output(fixture(t, "modes.jsonl"))
	stop := bytes.Index(out, []byte("modes enabled"))
	if stop < 0 {
		t.Fatal("fixture changed: no \"modes enabled\"")
	}
	out = out[:stop]
	want := sessionpane.Modes{Mouse: uv.MouseModeMotion, MouseEncoding: uv.MouseEncodingSGR, BracketedPaste: true, FocusEvents: true}
	for k := 0; k <= len(out); k++ {
		p := sessionpane.New(80, 24, sessionpane.Options{})
		p.Write(out[:k])
		p.Write(out[k:])
		got := p.Modes()
		p.Close()
		if got != want {
			t.Fatalf("split at %d: modes %+v, want %+v", k, got, want)
		}
	}
}

func TestModeSetEachMouseMode(t *testing.T) {
	for _, c := range []struct {
		seq  string
		want uv.MouseMode
	}{
		{"\x1b[?9h", uv.MouseModePress},
		{"\x1b[?1000h", uv.MouseModeClick},
		{"\x1b[?1001h", uv.MouseModeClick},
		{"\x1b[?1002h", uv.MouseModeDrag},
		{"\x1b[?1003h", uv.MouseModeMotion},
		{"\x1b[?9;1000;1002h", uv.MouseModeDrag},
		{"\x1b[?1003h\x1b[?1003l", uv.MouseModeNone},
		{"\x1b[?1006h", uv.MouseModeNone},
	} {
		p := newPane(t, 80, 24, sessionpane.Options{})
		p.Write([]byte(c.seq))
		if got := p.Modes().Mouse; got != c.want {
			t.Errorf("%q: mouse mode %v, want %v", c.seq, got, c.want)
		}
	}
}

func TestModeSetResetAndEnd(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	p.Write([]byte("\x1b[?1003;1006;1004;2004h"))
	if p.Modes() == (sessionpane.Modes{}) {
		t.Fatal("modes not set")
	}
	p.Write([]byte("\x1bc")) // full reset
	if got := p.Modes(); got != (sessionpane.Modes{}) {
		t.Fatalf("modes after a full reset: %+v", got)
	}
	p.Write([]byte("\x1b[?1000;2004h"))
	p.End(0, true)
	if got := p.Modes(); got != (sessionpane.Modes{}) {
		t.Fatalf("an ended session asks for %+v", got)
	}
}

func TestTitleWholeWithMarks(t *testing.T) {
	// U+2733 and U+2705 both contain the byte 0x9C, which unpatched x/ansi
	// takes for a string terminator: the title is cut there and its tail is
	// printed on the screen.
	for _, mark := range []string{"✳", "✅"} {
		if !strings.Contains(mark, "\x9c") {
			t.Fatalf("%q has no 0x9C byte; the test proves nothing", mark)
		}
	}
	for _, c := range []struct{ seq, title string }{
		{"\x1b]0;✳ x\x07", "✳ x"},
		{"\x1b]2;✅ done\x1b\\", "✅ done"},
		{"\x1b]0;✳ ✅ both\x1b\\", "✳ ✅ both"},
		{"\x1b]2;a;b ✳\x07", "a;b ✳"},
	} {
		for k := 0; k <= len(c.seq); k++ {
			p := sessionpane.New(80, 24, sessionpane.Options{})
			p.Write([]byte(c.seq[:k]))
			p.Write([]byte(c.seq[k:]))
			title, screen := p.Title(), sessionpane.ScreenText(p)
			p.Close()
			if title != c.title || screen != "" {
				t.Fatalf("%q split at %d: title %q, screen %q", c.seq, k, title, screen)
			}
		}
	}
}

func TestTitleWholeInTheTitleFixture(t *testing.T) {
	want := map[int]string{
		1:  "✳ synthetic task",
		2:  "✅ synthetic task done",
		3:  "✅ synthetic task done", // the split title is not finished yet
		4:  "✳ split title",
		6:  "✳ split title",
		7:  "✳ ✅ both marks", // an ESC ends the string; the "\\" follows
		8:  "✳ ✅ both marks",
		9:  "✅ synthetic check",
		10: "✳ synthetic star",
	}
	rec := fixture(t, "title.jsonl")
	p := newPane(t, rec.Cols, rec.Rows, sessionpane.Options{})
	for i, e := range rec.Events {
		apply(p, e)
		if w, ok := want[i]; ok && p.Title() != w {
			t.Errorf("after event %d: title %q, want %q", i, p.Title(), w)
		}
	}
	if got := sessionpane.ScreenText(p); got != "title test\ntitle set" {
		t.Fatalf("screen %q", got)
	}
}

func TestBoundsTitle(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	p.Write([]byte("\x1b]2;" + strings.Repeat("é", 1<<20) + "\x07"))
	title := p.Title()
	if len(title) > sessionpane.MaxTitle || len(title) < sessionpane.MaxTitle-1 || !utf8.ValidString(title) {
		t.Fatalf("long title: %d bytes, valid %v", len(title), utf8.ValidString(title))
	}
	if strings.Trim(title, "é") != "" {
		t.Fatalf("long title changed: %q", title)
	}
	p.Write([]byte("\x1b]2;a‮b⁦c d‏e\x07"))
	if got := p.Title(); got != "abcde" {
		t.Fatalf("title with formatting characters: %q", got)
	}
	for _, c := range []struct{ in, want string }{
		{"plain", "plain"},
		{"a\x00b\x1bc\x7fd\te\nf", "abcdef"},
		{"a\u0085b\u009bc", "abc"},
		{"bad \xff\xfe utf8", "bad �� utf8"},
		{"👩‍💻 kept", "👩‍💻 kept"},
		{strings.Repeat("x", 4*sessionpane.MaxTitle) + "tail", strings.Repeat("x", sessionpane.MaxTitle)},
		{strings.Repeat("\x01", 4*sessionpane.MaxTitle) + "tail", ""},
	} {
		if got := sessionpane.CleanTitle([]byte(c.in)); got != c.want {
			t.Errorf("CleanTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOSC52BlockedInTheClipboardFixture(t *testing.T) {
	p := play(t, fixture(t, "clipboard.jsonl"), sessionpane.Options{})
	if n := p.ClipboardBlocked(); n != 1 {
		t.Fatalf("ClipboardBlocked() = %d, want 1", n)
	}
	if got := sessionpane.ScreenText(p); got != "clipboard test\nclipboard write is blocked" {
		t.Fatalf("screen %q", got)
	}
}

func TestOSC52BlockedQueryGetsNoReply(t *testing.T) {
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add})
	p.Write([]byte("\x1b]52;c;?\x07\x1b]52;p;aGk=\x1b\\\x1b[6n"))
	r.await(t, "\x1b[1;1R")
	if got := r.String(); got != "\x1b[1;1R" {
		t.Fatalf("replies %q", got)
	}
	if n := p.ClipboardBlocked(); n != 2 {
		t.Fatalf("ClipboardBlocked() = %d, want 2", n)
	}
}

func rgb(r, g, b uint8) color.Color { return color.RGBA{R: r, G: g, B: b, A: 0xff} }

func TestColourRepliesNeutralWhenUnknown(t *testing.T) {
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add})
	p.Write([]byte("\x1b]10;?\x07\x1b]11;?\x1b\\\x1b]12;?\x07\x1b[6n"))
	r.await(t, "R")
	want := "\x1b]10;rgb:ffff/ffff/ffff\x07\x1b]11;rgb:0000/0000/0000\x07\x1b]12;rgb:ffff/ffff/ffff\x07\x1b[1;1R"
	if got := r.String(); got != want {
		t.Fatalf("replies %q, want %q", got, want)
	}
}

// TestColourRepliesNeutralIsThePanesOwn checks that an unknown colour is
// reported as the pane's NeutralColours entry, not as whatever x/vt falls
// back to: the neutral colours are swapped for ones x/vt never uses, and
// SetColours with unknown colours goes back to them after known ones.
func TestColourRepliesNeutralIsThePanesOwn(t *testing.T) {
	saved := sessionpane.NeutralColours
	t.Cleanup(func() { sessionpane.NeutralColours = saved })
	sessionpane.NeutralColours = sessionpane.Colours{
		Foreground: rgb(0x10, 0x20, 0x30),
		Background: rgb(0x40, 0x50, 0x60),
		Cursor:     rgb(0x70, 0x80, 0x90),
	}
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add, Colours: sessionpane.Colours{Foreground: rgb(1, 2, 3)}})
	query := []byte("\x1b]10;?\x07\x1b]11;?\x07\x1b]12;?\x07\x1b[6n")
	p.Write(query)
	r.await(t, "R")
	want := "\x1b]10;rgb:0101/0202/0303\x07\x1b]11;rgb:4040/5050/6060\x07\x1b]12;rgb:7070/8080/9090\x07\x1b[1;1R"
	if got := r.String(); got != want {
		t.Fatalf("replies %q, want %q", got, want)
	}

	r.reset()
	p.SetColours(sessionpane.Colours{})
	p.Write(query)
	r.await(t, "R")
	want = "\x1b]10;rgb:1010/2020/3030\x07\x1b]11;rgb:4040/5050/6060\x07\x1b]12;rgb:7070/8080/9090\x07\x1b[1;1R"
	if got := r.String(); got != want {
		t.Fatalf("replies after SetColours with unknown colours %q, want %q", got, want)
	}
}

func TestColourRepliesFromTheOuterTerminal(t *testing.T) {
	var r replies
	p := newPane(t, 80, 24, sessionpane.Options{Replies: r.add, Colours: sessionpane.Colours{
		Foreground: rgb(0x12, 0x34, 0x56),
		Background: rgb(0xfe, 0xdc, 0xba),
		// Cursor unknown: neutral.
	}})
	query := []byte("\x1b]10;?\x07\x1b]11;?\x07\x1b]12;?\x07\x1b[6n")
	p.Write(query)
	r.await(t, "R")
	want := "\x1b]10;rgb:1212/3434/5656\x07\x1b]11;rgb:fefe/dcdc/baba\x07\x1b]12;rgb:ffff/ffff/ffff\x07\x1b[1;1R"
	if got := r.String(); got != want {
		t.Fatalf("replies %q, want %q", got, want)
	}

	r.reset()
	p.SetColours(sessionpane.Colours{Foreground: rgb(1, 2, 3), Background: rgb(4, 5, 6), Cursor: rgb(7, 8, 9)})
	p.Write(query)
	r.await(t, "R")
	want = "\x1b]10;rgb:0101/0202/0303\x07\x1b]11;rgb:0404/0505/0606\x07\x1b]12;rgb:0707/0808/0909\x07\x1b[1;1R"
	if got := r.String(); got != want {
		t.Fatalf("replies after SetColours %q, want %q", got, want)
	}
}

func TestColourRepliesInTheQueriesFixture(t *testing.T) {
	var r replies
	rec := fixture(t, "queries.jsonl")
	p := newPane(t, rec.Cols, rec.Rows, sessionpane.Options{Replies: r.add, Colours: sessionpane.Colours{
		Foreground: rgb(0xaa, 0xbb, 0xcc), Background: rgb(0x11, 0x22, 0x33), Cursor: rgb(0x44, 0x55, 0x66),
	}})
	p.Write(output(rec))
	// Replies arrive in order, so once the answer to a last query is in,
	// every answer before it is too.
	p.Write([]byte("\x1b[6n"))
	r.await(t, "R")
	got := r.String()
	for _, c := range []struct {
		reply string
		n     int
	}{
		{"\x1b]10;rgb:aaaa/bbbb/cccc\x07", 3},
		{"\x1b]11;rgb:1111/2222/3333\x07", 3},
		{"\x1b]12;rgb:4444/5555/6666\x07", 1},
		{"\x1b[?62;1;6;22c", 3},
		{"\x1b[>1;10;0c", 2},
	} {
		if n := strings.Count(got, c.reply); n != c.n {
			t.Errorf("%q answered %d times, want %d; replies %q", c.reply, n, c.n, got)
		}
	}
	if screen := sessionpane.ScreenText(p); screen != "query test\nanswers are discarded" {
		t.Fatalf("screen %q", screen)
	}
}
