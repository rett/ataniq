package sessionpane_test

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"ataniq/internal/terminalconsole/sessionpane"
)

// richStream is a stream that leaves a pane with something in every part of
// the state a snapshot carries.
const richStream = "\x1b]0;a title\x07\x1b[2;5r\x1b[?69h\x1b[3;9s\x1b[?69l\x1b[r" +
	"\x1b[1;4;38;2;5;6;7;48;5;99;58;2;1;2;3mstyled\x1b[0m " +
	"\x1b]8;id=x;http://a.example/one\x1b\\link one\x1b]8;;\x1b\\ \x1b]8;;http://a.example/two\x1b\\two" +
	"\x1b(0lqk\x1b(B\x0e\x0f\x1b)A\x1b[3;3H\x1b7\x1b[5;1H\x1bH\x1b[3g\x1b[1;20H\x1bH" +
	"\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?25l\x1b[4 q\x1b[?7l\x1b[?9999h" +
	"\x1b]10;#ff0000\x07\x1b]11;rgb:00/ff/00\x07\x1b[?1049h\x1b[1;31malt \u4e16\u754c" +
	"\x1b]8;;http://a.example/alt\x1b\\x\x1b7\x1b[?1049l\x1b[?1049h\x1b[3;1Hz\u0301"

func richPane(t testing.TB) *sessionpane.Pane {
	t.Helper()
	p := sessionpane.New(30, 8, sessionpane.Options{})
	t.Cleanup(p.Close)
	p.Write([]byte(richStream))
	if !p.Quiescent() {
		t.Fatal("the rich stream ends in a sequence")
	}
	return p
}

func snapshotOf(t testing.TB, p *sessionpane.Pane) []byte {
	t.Helper()
	b, err := p.AppendSnapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pristine checks that a pane that failed to restore is as it was.
type pristine struct {
	t      testing.TB
	p      *sessionpane.Pane
	state  sessionpane.DeepStateData
	digest [32]byte
	snap   []byte
}

func keep(t testing.TB, p *sessionpane.Pane) *pristine {
	return &pristine{t: t, p: p, state: sessionpane.DeepState(p), digest: p.Digest(), snap: snapshotOf(t, p)}
}

func (k *pristine) unchanged(what string) {
	k.t.Helper()
	if !reflect.DeepEqual(sessionpane.DeepState(k.p), k.state) || k.p.Digest() != k.digest {
		k.t.Fatalf("%s changed the pane", what)
	}
	if got := snapshotOf(k.t, k.p); !bytes.Equal(got, k.snap) {
		k.t.Fatalf("%s changed the pane's snapshot", what)
	}
}

func TestSnapshotOfAPaneThatIsNotQuiescentFails(t *testing.T) {
	for _, half := range []string{
		"\x1b", "\x1b[", "\x1b[1;2", "\x1b]0;ti", "\x1b]0;ti\x1b", "\x1bP1$r", "\x1b_ab", "\x1b^ab", "\x1bXab",
		"\xe4", "\xe4\xb8", "\xf0\x9f\x91", "\x1b(", "\x1b[1 ",
	} {
		p := newPane(t, 20, 4, sessionpane.Options{})
		p.Write([]byte("abc" + half))
		prefix := []byte("keep me")
		got, err := p.AppendSnapshot(prefix)
		if !errors.Is(err, sessionpane.ErrNotQuiescent) {
			t.Errorf("after %q: error %v, want ErrNotQuiescent", half, err)
		}
		if !bytes.Equal(got, prefix) {
			t.Errorf("after %q: dst is %q", half, got)
		}
		// A cancel ends a sequence, and four bytes end a character.
		p.Write([]byte("\x18\x18\x18\x18"))
		if _, err := p.AppendSnapshot(nil); err != nil {
			t.Errorf("after %q and CAN: %v", half, err)
		}
	}
}

func TestSnapshotAppendsToDst(t *testing.T) {
	p := richPane(t)
	want := snapshotOf(t, p)
	prefix := append(make([]byte, 0, 4), "head"...)
	got, err := p.AppendSnapshot(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte("head")) || !bytes.Equal(got[4:], want) {
		t.Errorf("AppendSnapshot did not append the snapshot to dst")
	}
	// It works after the end and the close as well.
	p.End(0, true)
	p.Close()
	if again, err := p.AppendSnapshot(nil); err != nil || !bytes.Equal(again, want) {
		t.Errorf("snapshot after End and Close: %v, equal %v", err, bytes.Equal(again, want))
	}
}

// The snapshot is the real state, never the frame held for synchronized
// output; the restored pane has mode 2026 set and shows the real screen.
func TestSnapshotInTheMiddleOfAFrame(t *testing.T) {
	clock := newClock()
	a := newPane(t, 20, 3, sessionpane.Options{Clock: clock.Now})
	a.Write([]byte("old frame\x1b[?2026h\r\x1b[2Knew frame\x1b[?25l"))
	if !sessionpane.HeldFrameShown(a) {
		t.Fatal("the source does not hold a frame")
	}
	if got := sessionpane.ScreenText(a); got != "new frame" {
		t.Fatalf("the source's real screen is %q", got)
	}
	if !a.ModeSet().Has(2026, false) {
		t.Fatal("the source has no mode 2026")
	}

	snap := snapshotOf(t, a)
	b := newPane(t, 5, 5, sessionpane.Options{Clock: clock.Now})
	if err := b.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if got := sessionpane.ScreenText(b); got != "new frame" {
		t.Errorf("the restored pane's screen is %q: a held frame was carried", got)
	}
	if !b.ModeSet().Has(2026, false) {
		t.Error("the restored pane has no mode 2026")
	}
	if sessionpane.HeldFrameShown(b) {
		t.Error("the restored pane holds a frame")
	}
	if a.Digest() != b.Digest() {
		t.Error("the digests differ")
	}
	// It answers a query for mode 2026 as set.
	var r replies
	c := newPane(t, 5, 5, sessionpane.Options{Replies: r.add})
	must(t, c.Restore(snap))
	c.Write([]byte("\x1b[?2026$p"))
	r.await(t, "\x1b[?2026;1$y")

	// The session ends the frame: both panes agree, and neither holds one.
	for _, p := range []*sessionpane.Pane{a, b} {
		p.Write([]byte("\x1b[?2026l\x1b[?2026hnext"))
	}
	if a.Digest() != b.Digest() {
		t.Error("the digests differ after the frame ended")
	}
	if d := sameState(a, b); d != "" {
		t.Error(d)
	}
}

func TestBlankSnapshotMatchesTheSpec(t *testing.T) {
	for _, sz := range [][2]int{{2, 1}, {7, 3}, {8, 2}, {9, 2}, {80, 24}, {500, 200}} {
		p := newPane(t, sz[0], sz[1], sessionpane.Options{})
		got := snapshotOf(t, p)
		want := blankSpec(sz[0], sz[1]).bytes()
		if !bytes.Equal(got, want) {
			t.Errorf("%dx%d: the snapshot of a new pane is %d bytes, not the spec's %d bytes (first difference at %d)",
				sz[0], sz[1], len(got), len(want), firstDifference(got, want))
		}
	}
	// The spec is a snapshot Restore takes.
	p := newPane(t, 10, 5, sessionpane.Options{})
	p.Write([]byte(richStream))
	must(t, p.Restore(blankSpec(7, 3).bytes()))
	q := newPane(t, 7, 3, sessionpane.Options{})
	if p.Digest() != q.Digest() {
		t.Error("a pane restored from the blank spec differs from a new pane")
	}
	if d := sameState(p, q); d != "" {
		t.Error(d)
	}
}

func firstDifference(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// A snapshot that Restore refuses leaves the pane as it was.
func TestRestoreRefusesInvalidSnapshots(t *testing.T) {
	enc := func(s string, n int) string { return strings.Repeat(s, n) }
	// sty is a style of the table: its attributes, its underline and its
	// three colours, each a tag and its bytes; a missing colour is none.
	sty := func(attrs, underline byte, colours ...[]byte) []byte {
		b := []byte{attrs, underline}
		for i := 0; i < 3; i++ {
			if i < len(colours) {
				b = append(b, colours[i]...)
			} else {
				b = append(b, 0)
			}
		}
		return b
	}
	cases := []struct {
		name string
		edit func(*snapSpec)
	}{
		{"version 0", func(s *snapSpec) { s.version = 0 }},
		{"version 2", func(s *snapSpec) { s.version = 2 }},
		{"one column", func(s *snapSpec) { s.cols = 1 }},
		{"501 columns", func(s *snapSpec) { s.cols = 501 }},
		{"no rows", func(s *snapSpec) { s.rows = 0 }},
		{"201 rows", func(s *snapSpec) { s.rows = 201 }},
		{"huge columns", func(s *snapSpec) { s.cols = 1 << 40 }},
		{"flags", func(s *snapSpec) { s.flags = 4 }},
		{"GL 4", func(s *snapSpec) { s.gl = 4 }},
		{"GR 4", func(s *snapSpec) { s.gr = 4 }},
		{"single shift 1", func(s *snapSpec) { s.ss = 1 }},
		{"single shift 4", func(s *snapSpec) { s.ss = 4 }},
		{"character set 3", func(s *snapSpec) { s.charsets[2] = 3 }},
		{"last character below space", func(s *snapSpec) { s.lastChar = 0x1f }},
		{"last character DEL", func(s *snapSpec) { s.lastChar = 0x7f }},
		{"last character above ASCII", func(s *snapSpec) { s.lastChar = 0x4e16 }},
		{"mode kind 2", func(s *snapSpec) { s.modes[1].kind = 2 }},
		{"mode not in the table", func(s *snapSpec) { s.modes = append(s.modes, specMode{1, 9998, 1}) }},
		{"mode in the wrong order", func(s *snapSpec) { s.modes[0], s.modes[1] = s.modes[1], s.modes[0] }},
		{"mode twice", func(s *snapSpec) { s.modes[2] = s.modes[1] }},
		{"mode setting 0", func(s *snapSpec) { s.modes[0].setting = 0 }},
		{"mode setting 3", func(s *snapSpec) { s.modes[0].setting = 3 }},
		{"too many modes", func(s *snapSpec) {
			for i := 0; i < 20; i++ {
				s.modes = append(s.modes, specMode{1, 3000 + i, 1})
			}
		}},
		{"short tab bitmap", func(s *snapSpec) { s.tabs = s.tabs[:len(s.tabs)-1] }},
		{"open cluster column", func(s *snapSpec) { s.open = &specOpen{x: s.cols, text: "a"} }},
		{"open cluster row", func(s *snapSpec) { s.open = &specOpen{y: s.rows, text: "a"} }},
		{"open cluster single shift 1", func(s *snapSpec) { s.open = &specOpen{single: 1, text: "a"} }},
		{"open cluster capped 2", func(s *snapSpec) { s.open = &specOpen{capped: 2, text: "a"} }},
		{"open cluster empty", func(s *snapSpec) { s.open = &specOpen{} }},
		{"open cluster of 65 bytes", func(s *snapSpec) { s.open = &specOpen{text: enc("a", 65)} }},
		{"open cluster not UTF-8", func(s *snapSpec) { s.open = &specOpen{text: "\xff"} }},
		{"title of 257 bytes", func(s *snapSpec) { s.title = enc("t", 257) }},
		{"title with a control character", func(s *snapSpec) { s.title = "a\x01b" }},
		{"title with a bidirectional mark", func(s *snapSpec) { s.title = "a\u202eb" }},
		{"title not UTF-8", func(s *snapSpec) { s.title = "a\xffb" }},
		{"more styles than there are bytes", func(s *snapSpec) { s.styleCount = 1 << 30 }},
		{"more styles than any screen has", func(s *snapSpec) { s.styleCount = 2*500*200 + 5 }},
		{"a style missing", func(s *snapSpec) { s.styles = [][]byte{sty(1, 0)}; s.styleCount = 2 }},
		{"style with underline 6", func(s *snapSpec) { s.styles = [][]byte{sty(0, 6)} }},
		{"style with nothing set", func(s *snapSpec) { s.styles = [][]byte{sty(0, 0)} }},
		{"style twice", func(s *snapSpec) { s.styles = [][]byte{sty(1, 0), sty(1, 0)} }},
		{"basic colour 16", func(s *snapSpec) { s.styles = [][]byte{sty(0, 0, []byte{1, 16})} }},
		{"colour tag 6", func(s *snapSpec) { s.styles = [][]byte{sty(0, 0, []byte{6})} }},
		{"scroll region empty", func(s *snapSpec) { s.screens[0].scroll = [4]int{0, 1, s.cols, 1} }},
		{"scroll region beyond the screen", func(s *snapSpec) { s.screens[1].scroll = [4]int{0, 0, s.cols, s.rows + 1} }},
		{"scroll region inverted", func(s *snapSpec) { s.screens[1].scroll = [4]int{3, 0, 2, s.rows} }},
		{"cursor column outside", func(s *snapSpec) { s.screens[0].cursor.x = s.cols }},
		{"cursor row outside", func(s *snapSpec) { s.screens[1].saved.y = s.rows }},
		{"cursor flags", func(s *snapSpec) { s.screens[0].saved.flags = 4 }},
		{"cursor shape too large", func(s *snapSpec) { s.screens[0].cursor.shape = 1 << 31 }},
		{"pen style outside the table", func(s *snapSpec) { s.screens[0].cursor.pen = 1 }},
		{"pen parameters with no URL", func(s *snapSpec) { s.screens[0].cursor.params = "p" }},
		{"pen URL of 2049 bytes", func(s *snapSpec) { s.screens[0].cursor.url = enc("u", 2049) }},
		{"pen parameters of 257 bytes", func(s *snapSpec) { s.screens[0].cursor.url = "u"; s.screens[0].cursor.params = enc("p", 257) }},
		{"257 links", func(s *snapSpec) {
			for i := 0; i < 257; i++ {
				s.screens[0].links = append(s.screens[0].links, [2]string{fmt.Sprintf("http://x/%04d", i), ""})
			}
		}},
		{"link with no URL", func(s *snapSpec) { s.screens[0].links = [][2]string{{"", "p"}} }},
		{"link URL of 2049 bytes", func(s *snapSpec) { s.screens[0].links = [][2]string{{enc("u", 2049), ""}} }},
		{"link parameters of 257 bytes", func(s *snapSpec) { s.screens[0].links = [][2]string{{"u", enc("p", 257)}} }},
		{"links out of order", func(s *snapSpec) { s.screens[0].links = [][2]string{{"b", ""}, {"a", ""}} }},
		{"link twice", func(s *snapSpec) { s.screens[0].links = [][2]string{{"a", "p"}, {"a", "p"}} }},
		{"link wait over the limit", func(s *snapSpec) { s.screens[0].wait = 391 }},
		{"run of 0", func(s *snapSpec) { s.screens[0].cells = []specCell{{run: 0, content: " ", width: 1}} }},
		{"run past the end", func(s *snapSpec) { s.screens[0].cells[0].run++ }},
		{"cells missing", func(s *snapSpec) { s.screens[1].cells[0].run-- }},
		{"cell content of 65 bytes", func(s *snapSpec) { s.screens[0].cells[0].content = enc("a", 65) }},
		{"cell content not UTF-8", func(s *snapSpec) { s.screens[0].cells[0].content = "\xc3" }},
		{"cell width 3", func(s *snapSpec) { s.screens[0].cells[0].width = 3 }},
		{"cell width 3 in the alternate screen", func(s *snapSpec) { s.screens[1].cells[0].width = 3 }},
		{"cell width 65", func(s *snapSpec) { s.screens[0].cells[0].width = 65 }},
		{"cell style outside the table", func(s *snapSpec) { s.screens[0].cells[0].style = 1 }},
		{"cell link outside the table", func(s *snapSpec) { s.screens[0].cells[0].link = 1 }},
		{"trailing byte", func(s *snapSpec) { s.trailing = []byte{0} }},
	}
	target := richPane(t)
	k := keep(t, target)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := blankSpec(8, 3)
			tc.edit(s)
			err := target.Restore(s.bytes())
			if !errors.Is(err, sessionpane.ErrBadSnapshot) {
				t.Fatalf("Restore = %v, want ErrBadSnapshot", err)
			}
			k.unchanged("a refused snapshot")
		})
	}

	// A tab stop in a bit past the last column.
	s := blankSpec(4, 3)
	s.tabs[0] |= 0x10
	if err := target.Restore(s.bytes()); !errors.Is(err, sessionpane.ErrBadSnapshot) {
		t.Errorf("a tab stop past the screen: %v", err)
	}
	k.unchanged("a tab stop past the screen")

	// Every change above is the only fault of its snapshot: the unchanged
	// spec is valid.
	if err := newPane(t, 2, 2, sessionpane.Options{}).Restore(blankSpec(8, 3).bytes()); err != nil {
		t.Fatalf("the unchanged spec is refused: %v", err)
	}
}

func TestRestoreRefusesTruncatedOversizedAndNonMinimalInput(t *testing.T) {
	target := richPane(t)
	k := keep(t, target)
	for _, in := range [][]byte{nil, {}, {1}, {0}, {1, 2}, bytes.Repeat([]byte{0xff}, 40)} {
		if err := target.Restore(in); !errors.Is(err, sessionpane.ErrBadSnapshot) {
			t.Errorf("Restore(%x) = %v", in, err)
		}
	}
	k.unchanged("short inputs")

	over := make([]byte, sessionpane.MaxSnapshotBytes+1)
	over[0] = 1
	if err := target.Restore(over); !errors.Is(err, sessionpane.ErrBadSnapshot) {
		t.Errorf("an input over the limit: %v", err)
	}
	if err := target.Restore(append(blankSpec(8, 3).bytes(), make([]byte, sessionpane.MaxSnapshotBytes)...)); !errors.Is(err, sessionpane.ErrBadSnapshot) {
		t.Errorf("a snapshot with a megabyte after it: %v", err)
	}
	k.unchanged("oversized inputs")

	// A number written in more bytes than it needs is not the snapshot.
	good := blankSpec(2, 1).bytes()
	nonMinimal := append([]byte{good[0], 0x82, 0x00}, good[2:]...)
	if err := target.Restore(good); err != nil {
		t.Fatal(err)
	}
	k = keep(t, target)
	if err := target.Restore(nonMinimal); !errors.Is(err, sessionpane.ErrBadSnapshot) {
		t.Errorf("a number with a superfluous byte: %v", err)
	}
	k.unchanged("a non-minimal number")

	// Every proper prefix of a real snapshot is refused, and every refusal
	// leaves the pane as it was.
	real := snapshotOf(t, richPane(t))
	k = keep(t, target)
	for n := 0; n < len(real); n++ {
		if err := target.Restore(real[:n]); !errors.Is(err, sessionpane.ErrBadSnapshot) {
			t.Fatalf("the first %d of %d bytes: %v", n, len(real), err)
		}
	}
	k.unchanged("truncated snapshots")
	if err := target.Restore(real); err != nil {
		t.Fatal(err)
	}
}

// followsOutput writes, erases, inserts, deletes, scrolls and resizes the
// pane the way a session does after the pane was restored, at the edges and
// in the middle of the lines, with narrow and wide characters, under margins
// and on both screens: a restored pane that the next bytes make panic is a
// pane that crashes the client. Then it checks that the pane can still be
// snapshotted and restored. It returns when the pane survived.
func followsOutput(t testing.TB, p *sessionpane.Pane) {
	t.Helper()
	one := func() {
		cols, rows := p.Size()
		var b strings.Builder
		for _, y := range []int{1, (rows + 1) / 2, rows} {
			for _, x := range []int{cols, cols - 1, cols / 2, 2, 1} {
				if x < 1 {
					continue
				}
				fmt.Fprintf(&b, "\x1b[%d;%dHY\x1b[%d;%dH\u4e16\x1b[%d;%dH\x1b[3X\x1b[%d;%dH\x1b[2@",
					y, x, y, x, y, x, y, x)
				fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[2P\x1b[%d;%dH\x1b[K\x1b[1K\x1b[3b\u0301", y, x, y, x)
			}
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[L\x1b[M\x1b[2K", y)
		}
		b.WriteString("\x1b[2S\x1b[2T\x1b[I\x1b[Z\x1bD\x1bM\x1b[?1049h\u4e16a\x1b[2@\x1b[?1049l")
		b.WriteString("\x1b[?69h\x1b[2;3s\x1b[1;2H\u4e16\u4e16\x1b[2P\x1b[?69l\x1b[r")
		b.WriteString("\x1b[J\u4e16\u4e16\u4e16\x1b[1J\x1b[2J")
		p.Write([]byte(b.String()))
	}
	cols, rows := p.Size()
	one()
	p.Resize(cols+3, rows+2)
	one()
	p.Resize(max(2, cols/2), max(1, rows/2))
	one()
	p.Resize(cols, rows)
	one()
	if !p.Quiescent() {
		t.Fatal("the pane is not quiescent after the output")
	}
	snap, err := p.AppendSnapshot(nil)
	if err != nil {
		t.Fatalf("the snapshot after the output: %v", err)
	}
	q := sessionpane.New(2, 2, sessionpane.Options{})
	defer q.Close()
	if err := q.Restore(snap); err != nil {
		t.Fatalf("the restore of the snapshot after the output: %v", err)
	}
}

// touchesEveryCell restores data into p once for each cell of the first and
// of the last row and, as the first thing that happens after the restore,
// writes there, erases, inserts, deletes and repeats: a bad cell is only
// found by the first write to it or to its neighbour, since the write makes
// it good. A large screen is touched at a few cells only.
func touchesEveryCell(t testing.TB, p *sessionpane.Pane, data []byte) {
	t.Helper()
	cols, rows := p.Size()
	xs := []int{}
	for x := 1; x <= cols; x++ {
		if cols*rows <= 4000 || x <= 3 || x >= cols-2 {
			xs = append(xs, x)
		}
	}
	for _, y := range []int{1, rows} {
		for _, x := range xs {
			for _, seq := range []string{"Y", "\u4e16", "\x1b[2X", "\x1b[2@", "\x1b[2P", "\x1b[K", "\x1b[1K", "\x1b[2b", "\x1b[L", "\x1b[M", "\x1b[2J"} {
				if err := p.Restore(data); err != nil {
					t.Fatalf("Restore of what was restored: %v", err)
				}
				p.Write([]byte(fmt.Sprintf("\x1b[%d;%dH%s", y, x, seq)))
			}
		}
	}
}

// A snapshot with the cells that output makes and that a careless reader of
// snapshots could get wrong (a wide cell in the last column after a resize, a
// placeholder with no wide cell before it, a wide cell with no placeholder
// after it) is restored, and the writes, erases, inserts, deletes and resizes
// that follow do not panic. The cells of every one of them were made by
// output in a probe of the property streams; a snapshot of one of those
// screens must restore, which is why the decoder checks the width of a cell
// and not its neighbours.
func TestRestoreSurvivesOutputAfterwards(t *testing.T) {
	cell := func(content string, width int) specCell { return specCell{run: 1, content: content, width: width} }
	wide, place, narrow := cell("\u4e16", 2), cell("", 0), cell("a", 1)
	for name, row := range map[string][]specCell{
		"wide cell in the last column":    {narrow, narrow, narrow, narrow, narrow, wide},
		"wide cell with no placeholder":   {narrow, wide, narrow, narrow, narrow, narrow},
		"placeholder at the start":        {place, narrow, narrow, narrow, narrow, narrow},
		"placeholder after a narrow cell": {narrow, place, narrow, narrow, narrow, narrow},
		"placeholders in a row":           {wide, place, place, place, narrow, narrow},
		"wide cells in a row":             {wide, wide, wide, place, narrow, narrow},
		"zero-width cell with content":    {cell("\u0301", 0), narrow, narrow, narrow, narrow, narrow},
		"wide cell then a wide last":      {wide, place, narrow, narrow, narrow, wide},
	} {
		t.Run(name, func(t *testing.T) {
			s := blankSpec(6, 3)
			cells := append([]specCell(nil), row...)
			cells = append(cells, specCell{run: 12, content: " ", width: 1})
			s.screens[0].cells = cells
			s.screens[1].cells = cells
			data := s.bytes()
			p := newPane(t, 2, 2, sessionpane.Options{})
			must(t, p.Restore(data))
			touchesEveryCell(t, p, data)
			must(t, p.Restore(data))
			followsOutput(t, p)
		})
	}
}

// Single-bit changes of a real snapshot (half the bits of every byte, and
// those of every fourth byte under the race detector) are refused or taken,
// never a panic; when one is taken the pane holds a state it can snapshot,
// restore and follow, and when one is refused the pane is as it was.
func TestRestoreSurvivesBitFlips(t *testing.T) {
	real := snapshotOf(t, richPane(t))
	target := newPane(t, 30, 8, sessionpane.Options{})
	target.Write([]byte("something else"))
	k := keep(t, target)
	q := newPane(t, 2, 2, sessionpane.Options{})
	more := []byte("\x1b[2J\x1b[1;1Hx\u0301y\x1b[?1049l\x1b[3;3H\x1bHz\n\n\n\n\n\n\n\n\n\n")
	taken, refused := 0, 0
	stride := 1
	if raceEnabled {
		stride = 4 // every fourth byte: the race detector is slow
	}
	for i := 0; i < len(real); i += stride {
		for bit := 0; bit < 8; bit++ {
			if bit%2 == 1 && i%2 == 0 {
				continue // half of the bits of every byte: the test is long
			}
			flipped := bytes.Clone(real)
			flipped[i] ^= 1 << bit
			err := target.Restore(flipped)
			if err != nil {
				if !errors.Is(err, sessionpane.ErrBadSnapshot) {
					t.Fatalf("byte %d bit %d: %v", i, bit, err)
				}
				if target.Digest() != k.digest {
					t.Fatalf("byte %d bit %d: a refused snapshot changed the pane", i, bit)
				}
				refused++
				continue
			}
			taken++
			// Whatever was taken follows output without a panic.
			followsOutput(t, target)
			must(t, target.Restore(flipped))
			again, err := target.AppendSnapshot(nil)
			if err != nil {
				t.Fatalf("byte %d bit %d: the snapshot of a restored pane: %v", i, bit, err)
			}
			if err := q.Restore(again); err != nil {
				t.Fatalf("byte %d bit %d: the snapshot of a restored pane is refused: %v", i, bit, err)
			}
			if got := snapshotOf(t, q); !bytes.Equal(got, again) {
				t.Fatalf("byte %d bit %d: the snapshot of a restored pane is not stable", i, bit)
			}
			// Every tenth one follows more output beside its copy.
			if taken%10 == 0 {
				if d := sameState(target, q); d != "" {
					t.Fatalf("byte %d bit %d: %s", i, bit, d)
				}
				target.Write(more)
				q.Write(more)
				if d := sameState(target, q); d != "" {
					t.Fatalf("byte %d bit %d: after more output: %s", i, bit, d)
				}
			}
			// Back to the pane the loop started with.
			must(t, target.Restore(k.snap))
			if got := snapshotOf(t, target); !bytes.Equal(got, k.snap) {
				t.Fatalf("byte %d bit %d: the first snapshot does not restore to itself", i, bit)
			}
		}
	}
	t.Logf("%d bit flips of %d bytes: %d refused, %d taken", refused+taken, len(real), refused, taken)
	if refused == 0 || taken == 0 {
		t.Errorf("%d refused, %d taken", refused, taken)
	}
}

func TestRestoreIsRefusedAfterEndOrClose(t *testing.T) {
	snap := snapshotOf(t, richPane(t))
	for _, tc := range []struct {
		name string
		stop func(*sessionpane.Pane)
	}{
		{"End", func(p *sessionpane.Pane) { p.End(0, true) }},
		{"Close", func(p *sessionpane.Pane) { p.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPane(t, 10, 4, sessionpane.Options{})
			p.Write([]byte("kept"))
			tc.stop(p)
			k := keep(t, p)
			if err := p.Restore(snap); !errors.Is(err, sessionpane.ErrEnded) {
				t.Fatalf("Restore = %v, want ErrEnded", err)
			}
			k.unchanged("a refused restore")
			if err := p.Restore([]byte("junk")); !errors.Is(err, sessionpane.ErrEnded) {
				t.Fatalf("Restore of junk = %v, want ErrEnded", err)
			}
		})
	}
}

func TestRestoreMayBeCalledAnyNumberOfTimes(t *testing.T) {
	a := richPane(t)
	snap1 := snapshotOf(t, a)
	a.Write([]byte("\x1b[?1049l\x1b[2J\x1b[?1000l more \x1b]0;second\x07"))
	snap2 := snapshotOf(t, a)

	p := newPane(t, 12, 4, sessionpane.Options{})
	for i := 0; i < 6; i++ {
		snap := [][]byte{snap1, snap2}[i%2]
		// Each time in the middle of something else.
		p.Write([]byte("\x1b[3"))
		must(t, p.Restore(snap))
		want := [][]byte{snap1, snap2}[i%2]
		if got := snapshotOf(t, p); !bytes.Equal(got, want) {
			t.Fatalf("restore %d: the snapshot of the restored pane differs from the one it was restored from", i)
		}
	}
	if cols, rows := p.Size(); cols != 30 || rows != 8 {
		t.Errorf("size %dx%d after the restores", cols, rows)
	}
	if p.Title() != "second" {
		t.Errorf("title %q", p.Title())
	}

	// The half sequence was dropped: the parser starts at ground.
	q := newPane(t, 12, 4, sessionpane.Options{})
	q.Write([]byte("\x1b[3"))
	must(t, q.Restore(blankSpec(12, 4).bytes()))
	q.Write([]byte("4;5H"))
	if got := sessionpane.ScreenText(q); got != "4;5H" {
		t.Errorf("the screen is %q: the half sequence survived the restore", got)
	}
}

// A restore is not output: it makes no reply and keeps the pane's counters,
// handlers, options and the session's end unset.
func TestRestoreKeepsWhatIsNotState(t *testing.T) {
	var r replies
	p := newPane(t, 12, 4, sessionpane.Options{Replies: r.add})
	var seen []string
	must(t, p.HandleOSC(7777, func(d []byte) ([]byte, bool) {
		seen = append(seen, string(d))
		return []byte("pong"), true
	}))
	p.Write([]byte("\x1b[?9999h\x1b]52;c;aGk=\x07"))
	before := p.Counters()
	if before.ModesIgnored != 1 || before.ClipboardRequestsBlocked != 1 {
		t.Fatalf("counters %+v", before)
	}

	// A snapshot of a pane that asked for in-band resize reports, which a
	// resize answers: the restore does not.
	src := newPane(t, 20, 6, sessionpane.Options{})
	src.Write([]byte("\x1b[?2048h\x1b[?2026h"))
	must(t, p.Restore(snapshotOf(t, src)))
	// Replies come in order: the answer to a status report is the first thing
	// the pane says after the restore, or the restore made one.
	p.Write([]byte("\x1b[5n"))
	r.await(t, "\x1b[?0n")
	if s := r.String(); s != "\x1b[?0n" {
		t.Errorf("the restore made replies: %q", s)
	}
	r.reset()
	if p.Counters() != before {
		t.Errorf("counters %+v after the restore, were %+v", p.Counters(), before)
	}
	if p.ClipboardBlocked() != 1 {
		t.Errorf("ClipboardBlocked() = %d", p.ClipboardBlocked())
	}

	// The restored pane answers queries as any pane does (a client discards
	// the replies), and its handler is still there.
	p.Write([]byte("\x1b]7777;ask\x07\x1b[6n"))
	r.await(t, "pong")
	r.await(t, "R")
	if !reflect.DeepEqual(seen, []string{"7777;ask"}) {
		t.Errorf("handler saw %q", seen)
	}
	if ended, _, _ := p.Ended(); ended {
		t.Error("the pane ended")
	}
}

// The cells of a restored screen share one copy of each link, as the cells of
// a screen that was printed do, so that a screen of 100000 cells under 256
// links keeps 256 copies.
func TestRestoredCellsShareTheirLinks(t *testing.T) {
	src := newPane(t, 20, 4, sessionpane.Options{})
	for i := 0; i < 20; i++ {
		src.Write([]byte("\x1b]8;;http://a.example/shared\x1b\\x"))
	}
	p := newPane(t, 5, 5, sessionpane.Options{})
	must(t, p.Restore(snapshotOf(t, src)))
	first := sessionpane.CellAt(p, 0, 0).Link.URL
	if first != "http://a.example/shared" {
		t.Fatalf("link %q", first)
	}
	for x := 1; x < 20; x++ {
		if unsafe.StringData(sessionpane.CellAt(p, x, 0).Link.URL) != unsafe.StringData(first) {
			t.Fatalf("cell %d has its own copy of the link", x)
		}
	}
}

func TestDigestSeesEveryPartOfTheState(t *testing.T) {
	build := func() *sessionpane.Pane {
		p := newPane(t, 8, 3, sessionpane.Options{})
		p.Write([]byte("\x1b]0;t\x07ab\x1b[31mc\x1b]8;;http://a.example\x1b\\d"))
		return p
	}
	base := build().Digest()
	if build().Digest() != base {
		t.Fatal("two panes that received the same bytes have different digests")
	}

	changes := map[string]func(p *sessionpane.Pane){
		"another character":    func(p *sessionpane.Pane) { p.Write([]byte("x")) },
		"a style":              func(p *sessionpane.Pane) { p.Write([]byte("\x1b[1m\x1b[H\x1b[1;1H" + "q")) },
		"the cursor column":    func(p *sessionpane.Pane) { p.Write([]byte("\x1b[1;7H")) },
		"the cursor row":       func(p *sessionpane.Pane) { p.Write([]byte("\x1b[3;5H")) },
		"cursor visibility":    func(p *sessionpane.Pane) { p.Write([]byte("\x1b[?25l")) },
		"the cursor shape":     func(p *sessionpane.Pane) { p.Write([]byte("\x1b[4 q")) },
		"the cursor blink":     func(p *sessionpane.Pane) { p.Write([]byte("\x1b[2 q")) },
		"a mode":               func(p *sessionpane.Pane) { p.Write([]byte("\x1b[?2004h")) },
		"an ANSI mode":         func(p *sessionpane.Pane) { p.Write([]byte("\x1b[4h")) },
		"mode 2026":            func(p *sessionpane.Pane) { p.Write([]byte("\x1b[?2026h")) },
		"the alternate screen": func(p *sessionpane.Pane) { p.Write([]byte("\x1b[?1049h")) },
		"the title":            func(p *sessionpane.Pane) { p.Write([]byte("\x1b]2;u\x07")) },
		"the size":             func(p *sessionpane.Pane) { p.Resize(9, 3) },
		"the height":           func(p *sessionpane.Pane) { p.Resize(8, 4) },
		"a link":               func(p *sessionpane.Pane) { p.Write([]byte("\x1b[1;1H\x1b]8;;http://b.example\x1b\\z")) },
	}
	seen := map[[32]byte]string{base: "the base"}
	for name, change := range changes {
		p := build()
		change(p)
		d := p.Digest()
		if d == base {
			t.Errorf("a change of %s leaves the digest as it was", name)
		}
		seen[d] = name
	}

	// A cell changed through the API: every field of it (the pane's own
	// handlers cannot make a cell the output cannot), case A7 at the pane.
	cell := func(edit func(*uv.Cell)) [32]byte {
		p := build()
		c := sessionpane.CellAt(p, 1, 1)
		edit(&c)
		sessionpane.SetCell(p, 1, 1, &c)
		return p.Digest()
	}
	for name, edit := range map[string]func(*uv.Cell){
		"content":          func(c *uv.Cell) { c.Content = "q" },
		"a long content":   func(c *uv.Cell) { c.Content = "q\u0301" },
		"width":            func(c *uv.Cell) { c.Width = 2 },
		"foreground":       func(c *uv.Cell) { c.Style.Fg = ansi.BasicColor(3) },
		"foreground kind":  func(c *uv.Cell) { c.Style.Fg = ansi.IndexedColor(3) },
		"background":       func(c *uv.Cell) { c.Style.Bg = color.RGBA{R: 1, A: 255} },
		"underline colour": func(c *uv.Cell) { c.Style.UnderlineColor = color.CMYK{C: 1} },
		"attributes":       func(c *uv.Cell) { c.Style.Attrs |= uv.AttrBold },
		"underline":        func(c *uv.Cell) { c.Style.Underline = uv.UnderlineCurly },
		"link URL":         func(c *uv.Cell) { c.Link.URL = "http://c.example" },
		"link parameters":  func(c *uv.Cell) { c.Link = uv.Link{URL: "http://a.example", Params: "id=1"} },
	} {
		if cell(edit) == base {
			t.Errorf("a corrupted cell (%s) leaves the digest as it was", name)
		}
	}
	if cell(func(c *uv.Cell) { c.Link = uv.Link{Params: "id=1"} }) != base {
		t.Error("a link with no URL changed the digest: it is no link")
	}
	if cell(func(c *uv.Cell) {}) != base {
		t.Error("setting a cell to itself changed the digest")
	}

	// What is not the pane's state is not in the digest.
	p := build()
	p.SetColours(sessionpane.Colours{Foreground: color.RGBA{R: 9, A: 255}})
	p.SetGraphemeWidths(true)
	p.End(3, true)
	if p.Digest() != base {
		t.Error("the colours, the grapheme widths or the end changed the digest")
	}
}

// Every cell counts: a change anywhere on the screen changes the digest.
func TestDigestSeesEveryCell(t *testing.T) {
	p := newPane(t, 9, 4, sessionpane.Options{})
	p.Write([]byte("\x1b[31mhello\x1b[0m"))
	base := p.Digest()
	seen := map[[32]byte]bool{base: true}
	for y := 0; y < 4; y++ {
		for x := 0; x < 9; x++ {
			c := sessionpane.CellAt(p, x, y)
			orig := c
			c.Content = "Z"
			c.Width = 1
			sessionpane.SetCell(p, x, y, &c)
			d := p.Digest()
			if seen[d] {
				t.Fatalf("cell %d,%d: the digest was seen before", x, y)
			}
			seen[d] = true
			sessionpane.SetCell(p, x, y, &orig)
			if p.Digest() != base {
				t.Fatalf("cell %d,%d: the digest is not the base after the cell is put back", x, y)
			}
		}
	}
}

// Case H15: the worst-case screen. 500 by 200 cells, each a cluster of 63
// bytes and under a link, 256 live links of the longest URL and parameters
// the emulator keeps, on the main screen printed as output and, outside the
// race detector, the alternate screen too.
func TestWorstCaseScreenSnapshot(t *testing.T) {
	cols, rows := sessionpane.MaxCols, sessionpane.MaxRows
	if raceEnabled {
		cols, rows = cols/2, rows/2 // a quarter of the cells: the race detector is slow
	}
	fill := func(p *sessionpane.Pane) {
		cells := cols * rows
		var b bytes.Buffer
		flush := func() {
			p.Write(b.Bytes())
			b.Reset()
		}
		last := -1
		for i := 0; i < cells; i++ {
			// 256 links in runs of 390 cells.
			if l := i * 256 / cells; l != last {
				last = l
				url := fmt.Sprintf("http://x.example/%03d/", l)
				params := fmt.Sprintf("id=%03d", l)
				fmt.Fprintf(&b, "\x1b]8;%s;%s\x1b\\", params+strings.Repeat("p", vt.MaxLinkParams-len(params)), url+strings.Repeat("u", vt.MaxLinkURL-len(url)))
			}
			// "a" and 31 marks are 63 bytes; the last mark differs from cell
			// to cell, so that no two neighbours are equal.
			b.WriteString("a")
			for m := 0; m < 30; m++ {
				b.WriteString("\u0301")
			}
			b.WriteRune(rune(0x300 + i%112))
			if b.Len() > 1<<16 {
				flush()
			}
		}
		flush()
	}

	a := newPane(t, cols, rows, sessionpane.Options{})
	fill(a)
	screens := 1
	if !raceEnabled {
		// The alternate screen holds the same cells: set through the state, as
		// printing 6 MB again would only take time.
		st := sessionpane.EmulatorState(a)
		st.Screens[1] = st.Screens[0]
		must(t, sessionpane.SetEmulatorState(a, st))
		screens = 2
	}
	if c := a.Counters(); c.LinksOverLimit != 0 || c.LinksOverLength != 0 || c.ClusterBytesDropped != 0 {
		t.Fatalf("counters %+v: the screen is not the worst case", c)
	}
	if got := sessionpane.CellAt(a, 7, 7); len(got.Content) != 63 || got.Link.URL == "" {
		t.Fatalf("cell %q under %v", got.Content, got.Link)
	}

	snap, err := a.AppendSnapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot of %d screen(s) of %dx%d, each cell 63 bytes under a link, 256 links of 2 KiB: %d bytes (%.1f%% of the %d byte limit)",
		screens, cols, rows, len(snap), 100*float64(len(snap))/sessionpane.MaxSnapshotBytes, sessionpane.MaxSnapshotBytes)
	if len(snap) > sessionpane.MaxSnapshotBytes {
		t.Fatalf("%d bytes", len(snap))
	}

	b := newPane(t, 2, 1, sessionpane.Options{})
	if err := b.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if d := sameState(a, b); d != "" {
		t.Fatal(d)
	}
	if again := snapshotOf(t, b); !bytes.Equal(again, snap) {
		t.Fatal("the snapshot of the restored pane differs")
	}
	// The restored screen holds one copy of each link.
	first := sessionpane.CellAt(b, 0, 0).Link.URL
	if unsafe.StringData(sessionpane.CellAt(b, 1, 0).Link.URL) != unsafe.StringData(first) {
		t.Error("the cells of the restored screen do not share a link")
	}
	// Both follow the next bytes alike.
	for _, p := range []*sessionpane.Pane{a, b} {
		p.Write([]byte("\x1b[H\x1b[2Lmore\u0301\x1b[?1049l"))
	}
	if d := sameState(a, b); d != "" {
		t.Fatal(d)
	}
}

// What does not fit the limit is an error, and leaves dst alone: both screens
// of 63-byte clusters of different styles under 256 links.
func TestSnapshotOverTheLimitFails(t *testing.T) {
	const cols, rows = sessionpane.MaxCols, sessionpane.MaxRows
	links := make([]uv.Link, vt.MaxLiveLinks)
	for i := range links {
		url := fmt.Sprintf("http://x.example/%03d/", i)
		links[i] = uv.Link{URL: url + strings.Repeat("u", vt.MaxLinkURL-len(url)), Params: strings.Repeat("p", vt.MaxLinkParams)}
	}
	st := vt.NewEmulator(cols, rows).State()
	for s := range st.Screens {
		sc := &st.Screens[s]
		sc.Links = slicesSorted(links)
		for i := range sc.Cells {
			content := "a" + strings.Repeat("\u0301", 30)
			sc.Cells[i] = uv.Cell{
				Content: content + string(rune(0x300+i%112)),
				Width:   1,
				Style:   uv.Style{Fg: color.RGBA{R: byte(i), G: byte(i >> 8), B: byte(i>>16) + byte(2*s), A: 255}},
				Link:    sc.Links[i*vt.MaxLiveLinks/len(sc.Cells)],
			}
		}
	}
	p := newPane(t, cols, rows, sessionpane.Options{})
	if err := sessionpane.SetEmulatorState(p, st); err != nil {
		t.Fatal(err)
	}
	prefix := []byte("prefix")
	got, err := p.AppendSnapshot(prefix)
	if !errors.Is(err, sessionpane.ErrSnapshotTooLarge) {
		t.Fatalf("AppendSnapshot = %d bytes, %v; want ErrSnapshotTooLarge", len(got), err)
	}
	if !bytes.Equal(got, prefix) {
		t.Errorf("dst changed to %d bytes", len(got))
	}

	// The same screens with one style fit.
	for s := range st.Screens {
		for i := range st.Screens[s].Cells {
			st.Screens[s].Cells[i].Style = uv.Style{}
		}
	}
	if err := sessionpane.SetEmulatorState(p, st); err != nil {
		t.Fatal(err)
	}
	snap, err := p.AppendSnapshot(nil)
	if err != nil {
		t.Fatalf("with one style: %v", err)
	}
	t.Logf("both screens of different 63-byte clusters, one style: %d bytes; with a style per cell: over %d", len(snap), sessionpane.MaxSnapshotBytes)
}

func slicesSorted(links []uv.Link) []uv.Link {
	out := append([]uv.Link(nil), links...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (out[j].URL < out[j-1].URL || out[j].URL == out[j-1].URL && out[j].Params < out[j-1].Params); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// A state that only the API can make, which the format cannot carry, is an
// error and not a snapshot that restores to something else.
func TestSnapshotRefusesWhatOutputCannotMake(t *testing.T) {
	for name, edit := range map[string]func(*uv.Cell){
		"a link outside the table": func(c *uv.Cell) { c.Link = uv.Link{URL: "http://not-in-the-table"} },
		"a content over 64 bytes":  func(c *uv.Cell) { c.Content = strings.Repeat("e", 65) },
		"an unknown colour kind":   func(c *uv.Cell) { c.Style.Fg = color.Gray{Y: 3} },
	} {
		p := newPane(t, 6, 2, sessionpane.Options{})
		c := sessionpane.CellAt(p, 0, 0)
		edit(&c)
		sessionpane.SetCell(p, 0, 0, &c)
		if _, err := p.AppendSnapshot(nil); !errors.Is(err, sessionpane.ErrUnsupportedState) {
			t.Errorf("%s: %v, want ErrUnsupportedState", name, err)
		}
	}
}

// Every kind of colour a sequence can make survives the snapshot as the kind
// it was.
func TestSnapshotKeepsEveryKindOfColour(t *testing.T) {
	p := newPane(t, 40, 2, sessionpane.Options{})
	p.Write([]byte("\x1b[31mA\x1b[38;5;200mB\x1b[38;2;1;2;3mC\x1b[38:3::1:2:3mD\x1b[48:4::1:2:3:4mE\x1b[38:6::1:2:3:4mF\x1b[38;1mG\x1b[91mH\x1b[0m"))
	q := newPane(t, 2, 1, sessionpane.Options{})
	must(t, q.Restore(snapshotOf(t, p)))
	var kinds []string
	for x := 0; x < 8; x++ {
		a, b := sessionpane.CellAt(p, x, 0), sessionpane.CellAt(q, x, 0)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("cell %d: %#v and %#v", x, a, b)
		}
		kinds = append(kinds, fmt.Sprintf("%T", a.Style.Fg))
	}
	if !strings.Contains(strings.Join(kinds, " "), "RGBA") || !strings.Contains(strings.Join(kinds, " "), "CMYK") ||
		!strings.Contains(strings.Join(kinds, " "), "Alpha16") || !strings.Contains(strings.Join(kinds, " "), "IndexedColor") {
		t.Errorf("the test does not reach every kind: %v", kinds)
	}
}

// The inventory of the pane's own fields: whether a snapshot carries each,
// so that a field added later is decided on.
func TestPaneInventoryCoversEveryField(t *testing.T) {
	inventory := map[string]string{
		"mu":          "not state: the lock",
		"emu":         "carried: the emulator's state (vt.State)",
		"clock":       "not carried: the caller's option",
		"input":       "not carried: the caller's option",
		"cols":        "carried: the size",
		"rows":        "carried: the size",
		"graphemes":   "not carried: the outer terminal's, set by the caller",
		"dec":         "derived: from the modes, set again by Restore",
		"syncOut":     "carried as mode 2026 (on); the held frame is a presentation copy and is dropped",
		"title":       "carried",
		"clipboard":   "not carried: a counter",
		"titlesCut":   "not carried: a counter",
		"ended":       "not carried: Restore is refused after the end",
		"status":      "not carried: the end of the session",
		"statusKnown": "not carried: the end of the session",
		"closed":      "not carried: Restore is refused after the close",
		"pipe":        "not state: the reply pipe",
		"drained":     "not state: the drain goroutine",
	}
	typ := reflect.TypeOf((*sessionpane.Pane)(nil)).Elem()
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := inventory[name]; !ok {
			t.Errorf("Pane.%s is not in the inventory: decide whether a snapshot carries it", name)
		}
	}
	for name := range inventory {
		if !seen[name] {
			t.Errorf("the inventory lists Pane.%s, which does not exist", name)
		}
	}
}

func FuzzRestore(f *testing.F) {
	f.Add(snapshotOf(f, richPane(f)))
	f.Add(blankSpec(8, 3).bytes())
	f.Add(blankSpec(2, 1).bytes())
	f.Add([]byte{})
	f.Add([]byte{1, 2, 1})
	for seed := 0; seed < 5; seed++ {
		s := blankSpec(6, 2)
		switch seed {
		case 3: // a wide cell in the last column, and a placeholder after a narrow cell
			s.screens[0].cells = []specCell{{run: 5, content: "a", width: 1}, {run: 1, content: "\u4e16", width: 2}, {run: 1, content: "", width: 0}, {run: 5, content: " ", width: 1}}
		case 4: // a placeholder at the start of a line, and a wide cell with none after it
			s.screens[1].cells = []specCell{{run: 1, content: "", width: 0}, {run: 1, content: "\u4e16", width: 2}, {run: 10, content: " ", width: 1}}
		case 0:
			s.open = &specOpen{x: 1, text: "e\u0301"}
		case 1:
			s.screens[0].links = [][2]string{{"http://a", "id=1"}}
			s.screens[0].cells = []specCell{{run: 3, content: "a", width: 1, link: 1}, {run: 9, content: " ", width: 1}}
		case 2:
			s.styles = [][]byte{{1, 3, 1, 4, 3, 1, 2, 3, 255, 5, 0, 2}}
			s.screens[1].cursor = specCursor{x: 2, y: 1, shape: 4, flags: 3, pen: 1, url: "u", params: "p"}
		}
		f.Add(s.bytes())
	}
	target := sessionpane.New(30, 8, sessionpane.Options{})
	defer target.Close()
	target.Write([]byte(richStream))
	k := keep(f, target)
	f.Fuzz(func(t *testing.T, data []byte) {
		k.t = t
		err := target.Restore(data)
		if err != nil {
			if !errors.Is(err, sessionpane.ErrBadSnapshot) {
				t.Fatalf("Restore: %v", err)
			}
			k.unchanged("a refused snapshot")
			return
		}
		snap, err := target.AppendSnapshot(nil)
		if err != nil {
			t.Fatalf("snapshot of a restored pane: %v", err)
		}
		q := sessionpane.New(2, 1, sessionpane.Options{})
		defer q.Close()
		if err := q.Restore(snap); err != nil {
			t.Fatalf("restore of the snapshot of a restored pane: %v", err)
		}
		if d := sameState(target, q); d != "" {
			t.Fatal(d)
		}
		again, err := q.AppendSnapshot(nil)
		if err != nil || !bytes.Equal(again, snap) {
			t.Fatalf("the snapshot of a restored pane is not stable (%v)", err)
		}
		// A restored pane follows output without a panic.
		touchesEveryCell(t, target, data)
		must(t, target.Restore(data))
		followsOutput(t, target)
		// Back to the pane the fuzz function started from.
		k.t = t
		if err := target.Restore(k.snap); err != nil {
			t.Fatal(err)
		}
		k.unchanged("a restore of the first snapshot")
	})
}
