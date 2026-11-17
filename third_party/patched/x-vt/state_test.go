package vt

import (
	"errors"
	"fmt"
	"image/color"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"unsafe"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// stateTokens are the pieces of the streams the state tests feed: text of
// every width, styles, cursor moves, edits, scroll regions, screens, modes,
// links, titles, colours, tab stops, character sets and resets. None of them
// asks the emulator a question, because the answer would wait for a reader.
var stateTokens = []string{
	"a", "bcd", "hello world ", "\n", "\r\n", "\r", "\t", "\b", "\x0b", "\x0c",
	"\u4e16", "\u754c", "\u0301", "e\u0301", "\u200d", "\U0001f468\u200d\U0001f469\u200d\U0001f467", "\U0001f1f3\U0001f1ff", "x\ufe0f", "\u1100\u1161",
	"\x1b[1m", "\x1b[0m", "\x1b[3;4m", "\x1b[31m", "\x1b[38;5;200m", "\x1b[48;2;1;2;3m",
	"\x1b[58;2;9;8;7m", "\x1b[4:3m", "\x1b[38;6;1;2;3;4m", "\x1b[48;3;1;2;3m",
	"\x1b[38;4;1;2;3;4m", "\x1b[91m", "\x1b[102m", "\x1b[39m", "\x1b[22m",
	"\x1b[H", "\x1b[3;4H", "\x1b[2J", "\x1b[0J", "\x1b[1J", "\x1b[K", "\x1b[1K", "\x1b[2K",
	"\x1b[A", "\x1b[3B", "\x1b[2C", "\x1b[4D", "\x1b[2E", "\x1b[F", "\x1b[5G", "\x1b[3d", "\x1b[2;3f",
	"\x1b[2;5r", "\x1b[r", "\x1b[3;99r", "\x1b[40;90r", "\x1b[2;7s", "\x1b[s", "\x1b[u",
	"\x1b[3L", "\x1b[2M", "\x1b[4@", "\x1b[3P", "\x1b[2S", "\x1b[2T", "\x1b[5X", "\x1b[3b",
	"\x1b7", "\x1b8", "\x1bD", "\x1bM", "\x1bE",
	"\x1b[?1049h", "\x1b[?1049l", "\x1b[?1047h", "\x1b[?1047l", "\x1b[?1048h", "\x1b[?1048l",
	"\x1b[?25l", "\x1b[?25h", "\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l",
	"\x1b[?69h", "\x1b[?69l", "\x1b[?1000h", "\x1b[?1006h", "\x1b[?1000l", "\x1b[?2004h",
	"\x1b[?2026h", "\x1b[?2026l", "\x1b[?2027h", "\x1b[?1h", "\x1b[?66h", "\x1b[?9999h",
	"\x1b[4h", "\x1b[4l", "\x1b[20h", "\x1b[20l", "\x1b=", "\x1b>",
	"\x1b(0", "\x1b(B", "\x1b)0", "\x1b)A", "\x1b*0", "\x1b+A", "\x0e", "\x0f", "\x1bn", "\x1bo",
	"\x1b|", "\x1b}", "\x1b~", "\x1bN", "\x1bO", "lqkxj", "$$",
	"\x1bH", "\x1b[g", "\x1b[3g", "\x1b[3I", "\x1b[2Z", "\x1b[?5W",
	"\x1b]8;;http://a.example/1\x1b\\", "\x1b]8;id=1;http://a.example/2\x1b\\",
	"\x1b]8;;http://a.example/3\x07", "\x1b]8;;\x1b\\", "\x1b]8;p=1;\x1b\\", "\x1b]8;bad\x1b\\",
	"\x1b]0;a title\x07", "\x1b]2;another;title\x1b\\", "\x1b]7;file:///tmp\x07",
	"\x1b]10;#ff0000\x1b\\", "\x1b]11;rgb:00/ff/00\x07", "\x1b]12;#123\x07", "\x1b]110\x07",
	"\x1b]111\x07", "\x1b]112\x07", "\x1b]10;rgba:ff/00/00/80\x07", "\x1b]4;1;rgb:ff/00/00\x07",
	"\x1b[2 q", "\x1b[6 q", "\x1b[ q", "\x1bc", "\x1bP1$rq\x1b\\", "\x1b_apc\x1b\\", "\x1b]52;c;aGk=\x07",
}

// stateOp is a write or, when resize is set, a change of size.
type stateOp struct {
	data []byte
	w, h int
}

// stateStream returns the operations of one seeded stream.
func stateStream(rng *rand.Rand, tokens int) (w, h int, ops []stateOp) {
	w, h = 2+rng.IntN(20), 1+rng.IntN(8)
	var sb strings.Builder
	flush := func() {
		if sb.Len() == 0 {
			return
		}
		b := []byte(sb.String())
		sb.Reset()
		for len(b) > 0 {
			n := 1 + rng.IntN(9)
			if rng.IntN(5) == 0 {
				n = 1 + rng.IntN(60)
			}
			n = min(n, len(b))
			ops = append(ops, stateOp{data: b[:n]})
			b = b[n:]
		}
	}
	for i := 0; i < tokens; i++ {
		switch r := rng.IntN(60); {
		case r == 0:
			flush()
			ops = append(ops, stateOp{w: 2 + rng.IntN(20), h: 1 + rng.IntN(8)})
		case r < 5:
			sb.WriteString(fmt.Sprintf("\x1b]8;;http://a.example/%d\x1b\\", rng.IntN(300)))
		case r < 8:
			sb.WriteString(strings.Repeat("\u0301", 1+rng.IntN(40)))
		default:
			sb.WriteString(stateTokens[rng.IntN(len(stateTokens))])
		}
	}
	flush()
	return w, h, ops
}

func applyStateOp(e *Emulator, op stateOp) {
	if op.data != nil {
		e.Write(op.data)
	} else {
		e.Resize(op.w, op.h)
	}
}

// diffState returns the path of the first difference of two values, or "".
func diffState(path string, a, b reflect.Value) string {
	if a.Kind() != b.Kind() || a.Type() != b.Type() {
		return path + ": types differ"
	}
	switch a.Kind() {
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return path + ": nil differs"
			}
			return ""
		}
		return diffState(path, a.Elem(), b.Elem())
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return path + ": nil differs"
			}
			return ""
		}
		return diffState(path, a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if d := diffState(path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i)); d != "" {
				return d
			}
		}
	case reflect.Slice:
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s: length %d != %d", path, a.Len(), b.Len())
		}
		for i := 0; i < a.Len(); i++ {
			if d := diffState(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); d != "" {
				return d
			}
		}
	case reflect.Array:
		for i := 0; i < a.Len(); i++ {
			if d := diffState(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); d != "" {
				return d
			}
		}
	default:
		if !a.Equal(b) {
			return fmt.Sprintf("%s: %v != %v", path, a, b)
		}
	}
	return ""
}

// stateDiff returns where two states differ, or "" when they are equal.
func stateDiff(a, b State) string {
	if reflect.DeepEqual(a, b) {
		return ""
	}
	return diffState("State", reflect.ValueOf(a), reflect.ValueOf(b))
}

// The point of State and SetState: an emulator set from the state of another,
// at any point between two writes, stays equal to it for every write that
// follows, in every field of the state, whichever way the writes are split.
func TestSetStateFollowsTheSource(t *testing.T) {
	copies, maxWidth, compared := 0, 0, 0
	for seed := uint64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewPCG(seed, 7))
		w, h, ops := stateStream(rng, 300)
		a := NewEmulator(w, h)
		type follower struct {
			e    *Emulator
			from int
		}
		var followers []follower
		for i, op := range ops {
			applyStateOp(a, op)
			// Never take a copy in the middle of a sequence.
			if a.Quiescent() && rng.IntN(30) == 0 && len(followers) < 4 {
				b := NewEmulator(3, 2)
				if err := b.SetState(a.State()); err != nil {
					t.Fatalf("seed %d, op %d: SetState: %v", seed, i, err)
				}
				if d := stateDiff(a.State(), b.State()); d != "" {
					t.Fatalf("seed %d, op %d: a copy differs at once: %s", seed, i, d)
				}
				followers = append(followers, follower{b, i})
				copies++
			}
			var state State
			if len(followers) > 0 {
				state = a.State()
			}
			for _, f := range followers {
				if f.from == i {
					continue
				}
				applyStateOp(f.e, op)
				if d := stateDiff(state, f.e.State()); d != "" {
					t.Fatalf("seed %d: the copy made at op %d differs after op %d: %s", seed, f.from, i, d)
				}
				compared++
			}
		}
		for _, c := range a.State().Screens[0].Cells {
			maxWidth = max(maxWidth, c.Width)
		}
	}
	t.Logf("%d copies, %d comparisons, widest cell %d", copies, compared, maxWidth)
	if copies < 12 {
		t.Errorf("only %d copies were made", copies)
	}
}

// Cutting and copying work in the middle of the active alternate screen, a
// pending wrap, an open cluster and a capped one, which the generic streams
// reach only by chance.
func TestSetStateCarriesTheHardCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		then  string
	}{
		{"pending wrap", "abcde", "f\r\ng"},
		{"open cluster", "xy\U0001F468", "\u200d\U0001F469z"},
		{"open cluster at the end of a line", "abcd\U0001F468", "\u200d\U0001F469z"},
		{"capped cluster", "a" + strings.Repeat("\u0301", 40), "\u0302\u0303b"},
		{"capped cluster then a short mark", "a" + strings.Repeat("\u0301", 31) + "\U0001F600", "\u0301x"},
		{"alternate screen and the main screen behind", "main\x1b7\x1b[?1049hALT\x1b[31m\x1b8", "\x1b[?1049lback\x1b8!"},
		{"saved cursor of the screen that is not active", "\x1b[3;3H\x1b[1m\x1b7\x1b[?1047h\x1b[2;2H\x1b7\x1b[?1047l", "\x1b8x\x1b[?1047h\x1b8y"},
		{"scroll region beyond the screen", "\x1b[3;99r\x1b[?6h\x1b[40;1H", "ab\n\nc\x1bM\x1bM"},
		{"charsets and shifts", "\x1b(0\x1b)A\x0elqk\x1bn\x1b|", "\x1bNq\x0f$q"},
		{"single shift pending", "\x1b*0\x1bN", "q"},
		{"repeat", "x\x1b[2b", "\x1b[3b"},
		{"tab stops", "\x1b[3g\x1b[1;5H\x1bH\x1b[1;12H\x1bH\x1b[1G", "\t\tz"},
		{"link and pen", "\x1b[1;4;38;2;5;6;7m\x1b]8;id=x;http://a.example/q\x1b\\ab", "cd\x1b]8;;\x1b\\ef"},
		{"params-only link", "\x1b]8;id=1;\x1b\\ab", "c"},
		{"session colours", "\x1b]10;#ff0000\x07\x1b]11;rgb:00/ff/00\x07\x1b]12;rgba:01/02/03/04\x07", "\x1b]111\x07x"},
		{"modes reset and set", "\x1b[?7l\x1b[?25l\x1b[?2026h\x1b[?1000h\x1b[?1000l", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewEmulator(6, 4)
			a.Write([]byte(tc.input))
			if !a.Quiescent() {
				t.Fatal("the first write ends mid-sequence")
			}
			b := NewEmulator(2, 2)
			if err := b.SetState(a.State()); err != nil {
				t.Fatal(err)
			}
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("copy differs: %s", d)
			}
			a.Write([]byte(tc.then))
			b.Write([]byte(tc.then))
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("after %q: %s", tc.then, d)
			}
			if a.Counters() != b.Counters() && tc.name != "capped cluster" && tc.name != "capped cluster then a short mark" {
				t.Errorf("counters differ: %+v and %+v", a.Counters(), b.Counters())
			}
		})
	}
}

// OpenCluster does not say whether the cluster is capped, and a copy needs to
// know: a mark after a capped cluster is dropped and counted, even a short
// one that would still fit, and a copy that forgot the cap would keep it.
func TestSetStateCarriesTheCapOfTheOpenCluster(t *testing.T) {
	// 61 bytes, then a skin tone modifier of 4 bytes that extends the cluster
	// and does not fit: the cluster is capped at 61 bytes.
	input := "a" + strings.Repeat("\u0301", 30) + "\U0001F3FB"
	a := NewEmulator(10, 2)
	a.Write([]byte(input))
	st := a.State()
	if st.OpenCluster == nil || !st.OpenCluster.Capped {
		t.Fatalf("open cluster %+v, want a capped one", st.OpenCluster)
	}
	if len(st.OpenCluster.Text) > MaxClusterBytes {
		t.Fatalf("open cluster of %d bytes", len(st.OpenCluster.Text))
	}
	b := NewEmulator(10, 2)
	if err := b.SetState(st); err != nil {
		t.Fatal(err)
	}
	// A shorter mark would fit in the 64 bytes, but the cluster is capped.
	a.Write([]byte("\u0301"))
	b.Write([]byte("\u0301"))
	if d := stateDiff(a.State(), b.State()); d != "" {
		t.Fatalf("the copy differs: %s", d)
	}
	if got, want := b.Counters().ClusterBytesDropped, uint64(2); got != want {
		t.Errorf("the copy dropped %d bytes of the short mark, want %d", got, want)
	}
	if got := a.Counters().ClusterBytesDropped; got != 4+2 {
		t.Errorf("the original dropped %d bytes, want 6", got)
	}
	if got, want := a.State().OpenCluster.Capped, true; got != want {
		t.Errorf("Capped = %v after the mark", got)
	}

	// An uncapped cluster says so.
	c := NewEmulator(10, 2)
	c.Write([]byte("a\u0301"))
	if st := c.State(); st.OpenCluster == nil || st.OpenCluster.Capped || st.OpenCluster.Text != "a\u0301" {
		t.Errorf("open cluster %+v, want a\u0301 uncapped", st.OpenCluster)
	}
}

// The cells of a copy share one copy of each link, as printed cells do, and
// the table of a copy is the table of the original, a link no cell
// references included.
func TestSetStateInternsLinks(t *testing.T) {
	a := NewEmulator(8, 2)
	for i := 0; i < 8; i++ {
		a.Write([]byte("\x1b]8;;http://a.example/one\x1b\\x"))
	}
	a.Write([]byte("\x1b]8;;http://a.example/two\x1b\\y\x1b]8;;\x1b\\\x1b[2J"))
	// Nothing references "one" now and the clear emptied the table, so a new
	// link starts a new table; then one that is printed over leaves a stale
	// entry behind.
	a.Write([]byte("\x1b[H\x1b]8;;http://a.example/three\x1b\\z\x1b]8;;http://a.example/four\x1b\\w\x1b[H\x1b]8;;\x1b\\ab"))
	st := a.State()
	if got := len(st.Screens[0].Links); got != 2 {
		t.Fatalf("table holds %d links, want 2 (three, four)", got)
	}

	// Copy the cells through new strings, as a decoder does.
	for i := range st.Screens[0].Cells {
		c := &st.Screens[0].Cells[i]
		c.Link.URL = strings.Clone(c.Link.URL)
		c.Link.Params = strings.Clone(c.Link.Params)
	}
	for i := range st.Screens[0].Links {
		l := &st.Screens[0].Links[i]
		l.URL = strings.Clone(l.URL)
	}
	b := NewEmulator(8, 2)
	if err := b.SetState(st); err != nil {
		t.Fatal(err)
	}
	if d := stateDiff(a.State(), b.State()); d != "" {
		t.Fatalf("copy differs: %s", d)
	}

	// A screen of cells under one link shares its string.
	c := NewEmulator(8, 2)
	for i := 0; i < 8; i++ {
		c.Write([]byte("\x1b]8;;http://a.example/one\x1b\\x"))
	}
	cst := c.State()
	for i := range cst.Screens[0].Cells {
		cst.Screens[0].Cells[i].Link.URL = strings.Clone(cst.Screens[0].Cells[i].Link.URL)
	}
	d := NewEmulator(8, 2)
	if err := d.SetState(cst); err != nil {
		t.Fatal(err)
	}
	first := unsafe.StringData(d.CellAt(0, 0).Link.URL)
	for x := 1; x < 8; x++ {
		if unsafe.StringData(d.CellAt(x, 0).Link.URL) != first {
			t.Fatalf("cell %d has its own copy of the link", x)
		}
	}
}

// A state that is refused leaves the emulator as it was.
func TestSetStateRefusesAndKeepsTheEmulator(t *testing.T) {
	good := func() State {
		e := NewEmulator(6, 3)
		e.Write([]byte("\x1b]8;;http://a.example/1\x1b\\ab\x1b]8;;\x1b\\\x1b[?1049hcd\x1b[?1049l\U0001F468"))
		return e.State()
	}
	long := strings.Repeat("u", MaxLinkURL+1)
	for _, tc := range []struct {
		name string
		edit func(*State)
	}{
		{"no width", func(s *State) { s.Width = 0 }},
		{"huge width", func(s *State) { s.Width = maxParam }},
		{"cells missing", func(s *State) { s.Screens[1].Cells = s.Screens[1].Cells[1:] }},
		{"cell content over the cap", func(s *State) { s.Screens[0].Cells[0].Content = strings.Repeat("e", MaxClusterBytes+1) }},
		{"negative cell width", func(s *State) { s.Screens[0].Cells[0].Width = -1 }},
		{"huge cell width", func(s *State) { s.Screens[0].Cells[0].Width = MaxClusterBytes + 1 }},
		{"cell link over the cap", func(s *State) { s.Screens[0].Cells[3].Link = uv.Link{URL: long} }},
		{"cell link not in the table", func(s *State) { s.Screens[0].Cells[3].Link = uv.Link{URL: "http://other"} }},
		{"table link without a URL", func(s *State) { s.Screens[1].Links = []uv.Link{{Params: "p"}} }},
		{"table link over the cap", func(s *State) { s.Screens[1].Links = []uv.Link{{URL: long}} }},
		{"table params over the cap", func(s *State) {
			s.Screens[1].Links = []uv.Link{{URL: "u", Params: strings.Repeat("p", MaxLinkParams+1)}}
		}},
		{"duplicate table link", func(s *State) { s.Screens[1].Links = []uv.Link{{URL: "u"}, {URL: "u"}} }},
		{"table over the cap", func(s *State) {
			for i := 0; i <= MaxLiveLinks; i++ {
				s.Screens[1].Links = append(s.Screens[1].Links, uv.Link{URL: fmt.Sprint("http://a/", i)})
			}
		}},
		{"negative link wait", func(s *State) { s.Screens[0].LinkWait = -1 }},
		{"cursor outside the screen", func(s *State) { s.Screens[0].Cursor.X = -1 }},
		{"cursor beyond the screen", func(s *State) { s.Screens[0].Cursor.Y = s.Height }},
		{"saved cursor outside the screen", func(s *State) { s.Screens[1].Saved.X = s.Width }},
		{"cursor style out of range", func(s *State) { s.Screens[1].Cursor.Style = maxParam + 1 }},
		{"pen link over the cap", func(s *State) { s.Screens[0].Cursor.Link.URL = long }},
		{"empty scroll region", func(s *State) { s.Screens[0].Scroll.Max.Y = s.Screens[0].Scroll.Min.Y }},
		{"negative scroll margin", func(s *State) { s.Screens[1].Scroll.Min.X = -1 }},
		{"scroll region beyond the screen", func(s *State) { s.Screens[1].Scroll.Max.Y = s.Height + 1 }},
		{"mode not in the table", func(s *State) { s.Modes = append(s.Modes, ModeState{ansi.DECMode(9998), ansi.ModeSet}) }},
		{"mode with a strange setting", func(s *State) { s.Modes[0].Setting = ansi.ModePermanentlySet }},
		{"modes out of order", func(s *State) { s.Modes[0], s.Modes[1] = s.Modes[1], s.Modes[0] }},
		{"unknown character set", func(s *State) { s.Charsets[2] = CharSetOther }},
		{"locking shift", func(s *State) { s.GL = 4 }},
		{"single shift", func(s *State) { s.GSingle = 1 }},
		{"last character", func(s *State) { s.LastChar = 0xd800 }},
		{"tab stop outside", func(s *State) { s.TabStops = append(s.TabStops, s.Width) }},
		{"tab stops out of order", func(s *State) { s.TabStops = []int{3, 2} }},
		{"open cluster outside", func(s *State) { s.OpenCluster = &OpenClusterState{Pos: uv.Pos(s.Width, 0), Text: "a"} }},
		{"open cluster empty", func(s *State) { s.OpenCluster = &OpenClusterState{Text: ""} }},
		{"open cluster too long", func(s *State) { s.OpenCluster = &OpenClusterState{Text: strings.Repeat("a", MaxClusterBytes+1)} }},
		{"open cluster not UTF-8", func(s *State) { s.OpenCluster = &OpenClusterState{Text: "\xff"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(7, 4)
			e.Write([]byte("keep \x1b[31mme\x1b[?1049hx"))
			before := e.State()
			counters := e.Counters()
			st := good()
			tc.edit(&st)
			err := e.SetState(st)
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("SetState = %v, want ErrInvalidState", err)
			}
			if d := stateDiff(before, e.State()); d != "" {
				t.Fatalf("the refused state changed the emulator: %s", d)
			}
			if e.Counters() != counters {
				t.Errorf("counters changed: %+v", e.Counters())
			}
		})
	}
}

// SetState drops a sequence that was half received and empties the
// scrollback, and keeps what is not state: the counters and the colours the
// caller set.
func TestSetStateResetsTheParserAndKeepsTheCallersSettings(t *testing.T) {
	e := NewEmulator(8, 3)
	e.SetDefaultForegroundColor(color.RGBA{R: 1, G: 2, B: 3, A: 255})
	e.SetScrollbackSize(5)
	e.Write([]byte("1\n2\n3\n4\n5\n6\n\x1b[?9999h"))
	if e.ScrollbackLen() == 0 {
		t.Fatal("no scrollback to empty")
	}
	if e.Counters().ModesIgnored != 1 {
		t.Fatalf("counters %+v", e.Counters())
	}
	e.Write([]byte("\x1b[3"))
	if e.Quiescent() {
		t.Fatal("a half received sequence is quiescent")
	}
	src := NewEmulator(8, 3)
	src.Write([]byte("src"))
	if err := e.SetState(src.State()); err != nil {
		t.Fatal(err)
	}
	if !e.Quiescent() {
		t.Error("not quiescent after SetState")
	}
	e.Write([]byte("A"))
	if got := e.CellAt(3, 0).Content; got != "A" {
		t.Errorf("the half sequence survived: cell is %q", got)
	}
	if n := e.ScrollbackLen(); n != 0 {
		t.Errorf("scrollback holds %d lines after SetState", n)
	}
	if e.Counters().ModesIgnored != 1 {
		t.Errorf("counters %+v changed", e.Counters())
	}
	if c := e.defaultFg; c != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("default foreground %v changed", c)
	}
}

// Quiescent is true at the parser's ground state and in no other state.
func TestQuiescent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  bool
	}{
		{"nothing", "", true},
		{"text", "abc", true},
		{"a control character", "a\r\n", true},
		{"a finished CSI", "\x1b[1;2H", true},
		{"a finished OSC", "\x1b]0;t\x07", true},
		{"a finished OSC with ST", "\x1b]0;t\x1b\\", true},
		{"a finished DCS", "\x1bP1$r\x1b\\", true},
		{"a finished APC", "\x1b_x\x1b\\", true},
		{"a finished PM", "\x1b^x\x1b\\", true},
		{"a finished SOS", "\x1bXx\x1b\\", true},
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
		{"a whole character", "a\xe4\xb8\x96", true},
		{"an open cluster", "e\u0301", true},
		{"half a character in a string", "\x1b]0;\xe4\xb8", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(8, 3)
			e.Write([]byte(tc.input))
			if got := e.Quiescent(); got != tc.want {
				t.Errorf("Quiescent() = %v after %q, want %v", got, tc.input, tc.want)
			}
			se := NewSafeEmulator(8, 3)
			se.Write([]byte(tc.input))
			if got := se.Quiescent(); got != tc.want {
				t.Errorf("SafeEmulator.Quiescent() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ModeSettings reports the modes with a setting in the order RecordedModes
// gives, and a mode that was set and then reset differs from one never set.
func TestModeSettings(t *testing.T) {
	recorded := RecordedModes()
	if len(recorded) != len(recordedModes) {
		t.Fatalf("RecordedModes has %d modes, the table %d", len(recorded), len(recordedModes))
	}
	if !slices.IsSortedFunc(recorded, compareModes) {
		t.Errorf("RecordedModes is not sorted: %v", recorded)
	}
	seenDEC := false
	for _, m := range recorded {
		_, isDEC := m.(ansi.DECMode)
		if seenDEC && !isDEC {
			t.Errorf("ANSI mode %v after a DEC mode", m)
		}
		seenDEC = seenDEC || isDEC
	}
	if !slices.Contains(recorded, ansi.Mode(ansi.ModeSynchronizedOutput)) {
		t.Error("mode 2026 is not recorded")
	}

	e := NewEmulator(8, 3)
	find := func(m ansi.Mode) (ansi.ModeSetting, bool) {
		for _, ms := range e.ModeSettings() {
			if ms.Mode == m {
				return ms.Setting, true
			}
		}
		return 0, false
	}
	if _, ok := find(ansi.ModeSynchronizedOutput); ok {
		t.Error("mode 2026 has a setting before it was set")
	}
	if s, ok := find(ansi.ModeAutoWrap); !ok || !s.IsSet() {
		t.Errorf("auto wrap is %v, %v at the start", s, ok)
	}
	e.Write([]byte("\x1b[?2026h\x1b[?2027h\x1b[?2027l\x1b[4h"))
	if s, _ := find(ansi.ModeSynchronizedOutput); !s.IsSet() {
		t.Error("mode 2026 is not set")
	}
	if s, ok := find(ansi.ModeUnicodeCore); !ok || !s.IsReset() {
		t.Errorf("mode 2027 is %v, %v after a set and a reset", s, ok)
	}
	if s, _ := find(ansi.ModeInsertReplace); !s.IsSet() {
		t.Error("ANSI mode 4 is not set")
	}
	ms := e.ModeSettings()
	if !slices.IsSortedFunc(ms, func(a, b ModeState) int { return compareModes(a.Mode, b.Mode) }) {
		t.Errorf("ModeSettings is not sorted: %v", ms)
	}
	se := NewSafeEmulator(8, 3)
	if !reflect.DeepEqual(se.ModeSettings(), NewEmulator(8, 3).ModeSettings()) {
		t.Error("SafeEmulator.ModeSettings differs")
	}
}

// What the state does not carry, nothing in the output can change: no
// sequence sets an indexed colour, and none puts text in the scrollback of a
// screen that keeps none.
func TestOutputCannotChangeWhatTheStateLeavesOut(t *testing.T) {
	e := NewEmulator(8, 3)
	e.SetScrollbackSize(0)
	var before [256]color.Color
	for i := range before {
		before[i] = e.IndexedColor(i)
	}
	// The answer to the colour query is read and dropped.
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := e.Read(buf); err != nil {
				return
			}
		}
	}()
	for _, s := range []string{
		"\x1b]4;1;rgb:ff/00/00\x07", "\x1b]4;1;?\x07", "\x1b]104\x07", "\x1b]104;1\x07", "\x1b]5;0;rgb:00/00/ff\x07",
		"\x1b[38;5;1m", "\x1bP+q\x1b\\", "\x1b]10;?\x07",
	} {
		e.Write([]byte(s))
	}
	for i := range before {
		if e.IndexedColor(i) != before[i] {
			t.Fatalf("indexed colour %d changed", i)
		}
	}
	e.Write([]byte("1\n2\n3\n4\n5\n"))
	if e.ScrollbackLen() != 0 {
		t.Errorf("scrollback holds %d lines", e.ScrollbackLen())
	}
}

// SafeEmulator reads and sets the state under its lock.
func TestSafeEmulatorState(t *testing.T) {
	se := NewSafeEmulator(8, 3)
	se.Write([]byte("hello \x1b[31mworld"))
	other := NewSafeEmulator(2, 2)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				st := se.State()
				if err := other.SetState(st); err != nil {
					t.Error(err)
					return
				}
				other.State()
				se.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()
	if err := other.SetState(se.State()); err != nil {
		t.Fatal(err)
	}
	if d := stateDiff(se.State(), other.State()); d != "" {
		t.Errorf("copy differs: %s", d)
	}
}

// inventory lists every field of the emulator and what the state does with
// it. A field that is added to the emulator, a screen or a cursor must be
// listed here, so that whoever adds it decides.
var inventory = map[string]map[string]string{
	"Emulator": {
		"handlers":   "not carried: the caller's registrations",
		"colors":     "not carried: only the API sets an indexed colour (TestOutputCannotChangeWhatTheStateLeavesOut)",
		"scrs":       "carried: State.Screens, State.Width, State.Height",
		"scr":        "carried: State.Alt",
		"charsets":   "carried: State.Charsets",
		"logger":     "not carried: the caller's",
		"defaultFg":  "not carried: the caller's default, set by the API",
		"defaultBg":  "not carried: the caller's default, set by the API",
		"defaultCur": "not carried: the caller's default, set by the API",
		"fgColor":    "carried: State.Foreground",
		"bgColor":    "carried: State.Background",
		"curColor":   "carried: State.CursorColor",
		"modes":      "carried: State.Modes",
		"lastChar":   "carried: State.LastChar",
		"grapheme":   "not carried: empty between writes, every write draws what it collected",
		"capped":     "not carried: false between writes, the open cluster has its own",
		"scratch":    "not carried: scratch space",
		"open":       "carried: State.OpenCluster",
		"counters":   "not carried: monitoring, not state",
		"parser":     "not carried: reset to ground by SetState, which a state between sequences does not differ from",
		"cb":         "not carried: the caller's callbacks",
		"iconName":   "not carried: nothing reads it, and the session pane handles OSC 0 and 1 itself",
		"title":      "not carried: nothing reads it, and the session pane handles OSC 0 and 2 itself",
		"cwd":        "not carried: nothing reads it",
		"tabstops":   "carried: State.TabStops",
		"pr":         "not carried: the input pipe",
		"pw":         "not carried: the input pipe",
		"gl":         "carried: State.GL",
		"gr":         "carried: State.GR",
		"gsingle":    "carried: State.GSingle",
		"closed":     "not carried: the emulator's own",
		"atPhantom":  "carried: State.AtPhantom",
	},
	"Screen": {
		"cb":         "not carried: the emulator's callbacks",
		"buf":        "carried: ScreenState.Cells (the damage of the buffer, Touched, is not state)",
		"cur":        "carried: ScreenState.Cursor",
		"saved":      "carried: ScreenState.Saved",
		"scroll":     "carried: ScreenState.Scroll",
		"scrollback": "not carried: the caller's size; emptied by SetState",
		"links":      "carried: ScreenState.Links and LinkWait (recounts and hashed are statistics)",
	},
	"Cursor": {
		"Pen":      "carried",
		"Link":     "carried",
		"Position": "carried",
		"Style":    "carried",
		"Steady":   "carried",
		"Hidden":   "carried",
	},
	"openCluster": {
		"ok":      "carried: State.OpenCluster is nil when false or not valid",
		"content": "carried: OpenClusterState.Text",
		"drawn":   "not carried: the content of the cell, read back by SetState",
		"x":       "carried: OpenClusterState.Pos",
		"y":       "carried: OpenClusterState.Pos",
		"curX":    "not carried: the cursor's, read back by SetState",
		"curY":    "not carried: the cursor's, read back by SetState",
		"phantom": "not carried: State.AtPhantom, read back by SetState",
		"single":  "carried: OpenClusterState.Single",
		"capped":  "carried: OpenClusterState.Capped",
	},
	"linkTable": {
		"set":      "carried: ScreenState.Links",
		"wait":     "carried: ScreenState.LinkWait",
		"recounts": "not carried: a statistic for tests",
		"hashed":   "not carried: a statistic for tests",
	},
	"State": {
		"Width": "", "Height": "", "Alt": "", "Screens": "", "Modes": "", "Charsets": "",
		"GL": "", "GR": "", "GSingle": "", "LastChar": "", "AtPhantom": "", "TabStops": "",
		"Foreground": "", "Background": "", "CursorColor": "", "OpenCluster": "",
	},
	"ScreenState": {
		"Cells": "", "Cursor": "", "Saved": "", "Scroll": "", "Links": "", "LinkWait": "",
	},
}

func TestStateInventoryCoversEveryField(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"Emulator":    reflect.TypeOf(Emulator{}),
		"Screen":      reflect.TypeOf(Screen{}),
		"Cursor":      reflect.TypeOf(Cursor{}),
		"openCluster": reflect.TypeOf(openCluster{}),
		"linkTable":   reflect.TypeOf(linkTable{}),
		"State":       reflect.TypeOf(State{}),
		"ScreenState": reflect.TypeOf(ScreenState{}),
	} {
		listed := inventory[name]
		seen := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			seen[f.Name] = true
			if _, ok := listed[f.Name]; !ok {
				t.Errorf("%s.%s is not in the inventory: decide whether State carries it", name, f.Name)
			}
		}
		for f := range listed {
			if !seen[f] {
				t.Errorf("the inventory lists %s.%s, which does not exist", name, f)
			}
		}
	}
}
