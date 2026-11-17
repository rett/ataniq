package sessionpane_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/terminalconsole/sessionpane"
)

func TestCountersCountEveryCap(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	if c := p.Counters(); c != (sessionpane.Counters{}) {
		t.Fatalf("a new pane has counters %+v", c)
	}
	long := strings.Repeat("u", 3000)
	for _, tc := range []struct {
		name  string
		input string
		field func(sessionpane.Counters) uint64
		want  uint64
	}{
		// "a" and 31 marks are 63 bytes; the next two marks are 4 bytes.
		{"cluster bytes dropped", "a" + strings.Repeat("\u0301", 33), func(c sessionpane.Counters) uint64 { return c.ClusterBytesDropped }, 4},
		{"mode ignored", "\x1b[?9999h\x1b[?9998l", func(c sessionpane.Counters) uint64 { return c.ModesIgnored }, 2},
		{"link over length", "\x1b]8;;http://x/" + long + "\x1b\\", func(c sessionpane.Counters) uint64 { return c.LinksOverLength }, 1},
		{"string truncated", "\x1b]777;" + strings.Repeat("x", 70000) + "\x07", func(c sessionpane.Counters) uint64 { return c.StringsTruncated }, 1},
		{"repeat cut", "x\x1b[2147483646b", func(c sessionpane.Counters) uint64 { return c.RepeatsCut }, 1},
		{"repeat not cut", "x\x1b[10b", func(c sessionpane.Counters) uint64 { return c.RepeatsCut }, 1},
		{"clipboard write", "\x1b]52;c;aGk=\x1b\\", func(c sessionpane.Counters) uint64 { return c.ClipboardRequestsBlocked }, 1},
		{"clipboard query", "\x1b]52;c;?\x07", func(c sessionpane.Counters) uint64 { return c.ClipboardRequestsBlocked }, 2},
		{"title cut", "\x1b]2;" + strings.Repeat("t", 1000) + "\x07", func(c sessionpane.Counters) uint64 { return c.TitlesCut }, 1},
		{"title cut by a wide character", "\x1b]0;" + strings.Repeat("t", sessionpane.MaxTitle-1) + "\u4e16\x07", func(c sessionpane.Counters) uint64 { return c.TitlesCut }, 2},
		{"title exactly at the cap", "\x1b]0;" + strings.Repeat("t", sessionpane.MaxTitle) + "\x07", func(c sessionpane.Counters) uint64 { return c.TitlesCut }, 2},
		{"title of control characters", "\x1b]0;a\x01b\u202ec\x07", func(c sessionpane.Counters) uint64 { return c.TitlesCut }, 2},
	} {
		p.Write([]byte(tc.input))
		if got := tc.field(p.Counters()); got != tc.want {
			t.Errorf("%s: counter is %d, want %d (%+v)", tc.name, got, tc.want, p.Counters())
		}
	}
	if got := p.ClipboardBlocked(); got != 2 {
		t.Errorf("ClipboardBlocked() = %d, want 2", got)
	}

	// 257 distinct links: the last is over the limit of 256.
	q := newPane(t, 80, 24, sessionpane.Options{})
	var b strings.Builder
	for i := 0; i < 258; i++ {
		fmt.Fprintf(&b, "\x1b]8;;http://x/%d\x1b\\%c", i, 'a'+i%26)
	}
	q.Write([]byte(b.String()))
	if got := q.Counters().LinksOverLimit; got != 2 {
		t.Errorf("LinksOverLimit = %d, want 2", got)
	}

	// The counters stay readable after the end and the close.
	p.End(0, true)
	p.Close()
	if p.Counters().ModesIgnored != 2 {
		t.Errorf("counters after Close: %+v", p.Counters())
	}
}

func TestCountersList(t *testing.T) {
	c := sessionpane.Counters{
		ClusterBytesDropped: 1, ModesIgnored: 2, LinksOverLength: 3, LinksOverLimit: 4,
		StringsTruncated: 5, RepeatsCut: 6, ClipboardRequestsBlocked: 7, TitlesCut: 8,
	}
	list := c.List()
	var names []string
	for i, v := range list {
		names = append(names, v.Name)
		if v.Value != uint64(i+1) {
			t.Errorf("%s = %d, want %d", v.Name, v.Value, i+1)
		}
	}
	want := []string{
		"cluster_bytes_dropped", "modes_ignored", "links_over_length", "links_over_limit",
		"strings_truncated", "repeats_cut", "clipboard_requests_blocked", "titles_cut",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names %v, want %v", names, want)
	}
	// Every field of the struct is in the list.
	if n := reflect.TypeOf(c).NumField(); n != len(list) {
		t.Errorf("Counters has %d fields, List %d", n, len(list))
	}
}

func TestModeSet(t *testing.T) {
	p := newPane(t, 80, 24, sessionpane.Options{})
	// The defaults: auto wrap and a visible cursor.
	if got, want := p.ModeSet(), (sessionpane.ModeSet{DEC: []int{7, 25}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModeSet() = %+v, want %+v", got, want)
	}
	p.Write([]byte("\x1b[?2026h\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1049h\x1b[4h\x1b[20h\x1b[?25l\x1b[?9999h"))
	want := sessionpane.ModeSet{
		DEC:  []int{7, 1000, 1006, 1049, 2004, 2026},
		ANSI: []int{4, 20},
	}
	if got := p.ModeSet(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModeSet() = %+v, want %+v", got, want)
	}
	if !want.Has(2026, false) || want.Has(2026, true) || !want.Has(4, true) || want.Has(25, false) {
		t.Error("Has is wrong")
	}
	if m := p.Modes(); m.Mouse == 0 || !m.BracketedPaste {
		t.Errorf("Modes() = %+v", m)
	}

	// A mode that is reset leaves the set, and a full reset empties it to the
	// defaults, 2026 included.
	p.Write([]byte("\x1b[?1000l\x1b[4l\x1b[?25h"))
	if got, want := p.ModeSet(), (sessionpane.ModeSet{DEC: []int{7, 25, 1006, 1049, 2004, 2026}, ANSI: []int{20}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("after resets ModeSet() = %+v, want %+v", got, want)
	}
	p.Write([]byte("\x1bc"))
	if got, want := p.ModeSet(), (sessionpane.ModeSet{DEC: []int{7, 25}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("after RIS ModeSet() = %+v, want %+v", got, want)
	}

	// After the end the mirror modes are none, and the set is the state.
	p.Write([]byte("\x1b[?2004h\x1b[?2026h"))
	p.End(0, true)
	if m := p.Modes(); m != (sessionpane.Modes{}) {
		t.Errorf("Modes() after End = %+v", m)
	}
	if got := p.ModeSet(); !got.Has(2004, false) || !got.Has(2026, false) {
		t.Errorf("ModeSet() after End = %+v", got)
	}
}

func TestQuiescentInEveryParserState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  bool
	}{
		{"new", "", true},
		{"text", "abc", true},
		{"a finished CSI", "\x1b[1;2H", true},
		{"a finished OSC", "\x1b]0;t\x07", true},
		{"a finished OSC with ST", "\x1b]0;t\x1b\\", true},
		{"a finished DCS", "\x1bP1$r\x1b\\", true},
		{"a finished APC", "\x1b_x\x1b\\", true},
		{"a finished PM", "\x1b^x\x1b\\", true},
		{"a finished SOS", "\x1bXx\x1b\\", true},
		{"an open cluster", "e\u0301", true},
		{"an open cluster of a wide emoji", "\U0001F468\u200d", true},
		{"an escape", "\x1b", false},
		{"an escape intermediate", "\x1b(", false},
		{"CSI entry", "\x1b[", false},
		{"CSI parameter", "\x1b[1;", false},
		{"CSI intermediate", "\x1b[1 ", false},
		{"an open OSC", "\x1b]0;t", false},
		{"an OSC before its ST", "\x1b]0;t\x1b", false},
		{"a DCS entry", "\x1bP", false},
		{"a DCS parameter", "\x1bP1", false},
		{"a DCS intermediate", "\x1bP1$", false},
		{"a DCS string", "\x1bP1$rab", false},
		{"an open APC", "\x1b_ab", false},
		{"an open PM", "\x1b^ab", false},
		{"an open SOS", "\x1bXab", false},
		{"half a two-byte character", "a\xc3", false},
		{"half a three-byte character", "a\xe4\xb8", false},
		{"half a four-byte character", "a\xf0\x9f\x91", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPane(t, 20, 4, sessionpane.Options{})
			p.Write([]byte(tc.input))
			if got := p.Quiescent(); got != tc.want {
				t.Errorf("Quiescent() = %v after %q, want %v", got, tc.input, tc.want)
			}
			// The rest of the sequence brings it back.
			if !tc.want {
				p.Write([]byte("\x18"))
				if !p.Quiescent() {
					t.Errorf("not quiescent after CAN")
				}
			}
		})
	}

	// Split anywhere, a sequence is quiescent exactly after its last byte.
	seq := []byte("a\x1b[38;2;1;2;3m\x1b]8;;http://x/\x07\xe4\xb8\x96\x1bP1$rab\x1b\\z")
	p := newPane(t, 20, 4, sessionpane.Options{})
	for i := range seq {
		p.Write(seq[i : i+1])
	}
	if !p.Quiescent() {
		t.Error("not quiescent after the whole sequence")
	}
}

func TestHandleOSC(t *testing.T) {
	var r replies
	p := newPane(t, 20, 4, sessionpane.Options{Replies: r.add})
	var got []string
	err := p.HandleOSC(7777, func(data []byte) ([]byte, bool) {
		got = append(got, string(data))
		return []byte("\x1b]7777;pong\x07"), true
	})
	if err != nil {
		t.Fatal(err)
	}
	// The reply goes out in order with the emulator's own answers.
	p.Write([]byte("\x1b[c\x1b]7777;ping\x07\x1b[6n"))
	r.await(t, "\x1b[1;1R")
	if want := []string{"7777;ping"}; !reflect.DeepEqual(got, want) {
		t.Errorf("handler saw %q, want %q", got, want)
	}
	s := r.String()
	i, j, k := strings.Index(s, "\x1b[?62;1;6;22c"), strings.Index(s, "\x1b]7777;pong\x07"), strings.Index(s, "\x1b[1;1R")
	if !(0 <= i && i < j && j < k) {
		t.Errorf("replies %q are not in order", s)
	}

	// A string split across writes, and ended with ST, reaches the handler
	// whole; nothing of it reaches the screen.
	r.reset()
	got = nil
	p.Write([]byte("ab\x1b]77"))
	p.Write([]byte("77;spl"))
	p.Write([]byte("it\x1b"))
	p.Write([]byte("\\cd"))
	if want := []string{"7777;split"}; !reflect.DeepEqual(got, want) {
		t.Errorf("handler saw %q, want %q", got, want)
	}
	if text := sessionpane.ScreenText(p); text != "abcd" {
		t.Errorf("screen %q", text)
	}
	r.await(t, "\x1b]7777;pong\x07")

	// handled false leaves the string for the next handler, and then ignored.
	r.reset()
	var order []string
	for _, name := range []string{"first", "second"} {
		must(t, p.HandleOSC(7778, func(data []byte) ([]byte, bool) {
			order = append(order, name)
			return []byte("reply " + name), name == "first"
		}))
	}
	p.Write([]byte("\x1b]7778;x\x07"))
	if want := []string{"second", "first"}; !reflect.DeepEqual(order, want) {
		t.Errorf("handlers ran %v, want the last set first: %v", order, want)
	}
	r.await(t, "reply first")
	if strings.Contains(r.String(), "reply second") {
		t.Errorf("an unhandled string was answered: %q", r.String())
	}

	// A command nobody handles is ignored.
	p.Write([]byte("\x1b]7779;x\x07z"))
	if text := sessionpane.ScreenText(p); !strings.HasSuffix(text, "z") {
		t.Errorf("screen %q", text)
	}

	// A handler with no reply writes nothing.
	r.reset()
	must(t, p.HandleOSC(7780, func([]byte) ([]byte, bool) { return nil, true }))
	p.Write([]byte("\x1b]7780;x\x07\x1b[6n"))
	r.await(t, "R")
	if s := r.String(); strings.Count(s, "\x1b") != 1 {
		t.Errorf("replies %q", s)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestHandleOSCRefusesWhatThePaneHandles(t *testing.T) {
	p := newPane(t, 20, 4, sessionpane.Options{})
	fn := func([]byte) ([]byte, bool) { return nil, true }
	for _, cmd := range []int{0, 1, 2, 7, 8, 10, 11, 12, 52, 110, 111, 112} {
		if err := p.HandleOSC(cmd, fn); !errors.Is(err, sessionpane.ErrOSCReserved) {
			t.Errorf("HandleOSC(%d) = %v, want ErrOSCReserved", cmd, err)
		}
	}
	if err := p.HandleOSC(-1, fn); err == nil {
		t.Error("HandleOSC(-1) was accepted")
	}
	if err := p.HandleOSC(7777, nil); err == nil {
		t.Error("a nil function was accepted")
	}
	// The pane still handles them itself.
	p.Write([]byte("\x1b]2;title\x07\x1b]52;c;aGk=\x07"))
	if p.Title() != "title" || p.ClipboardBlocked() != 1 {
		t.Errorf("title %q, clipboard %d", p.Title(), p.ClipboardBlocked())
	}
	// Other numbers are free, 4 and 104 (the palette) among them.
	for _, cmd := range []int{3, 4, 9, 104, 133, 777, 1337, 99999} {
		if err := p.HandleOSC(cmd, fn); err != nil {
			t.Errorf("HandleOSC(%d) = %v", cmd, err)
		}
	}
}
