package sessionpane_test

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The size of a snapshot is bounded by the format and not by a guess: this
// file computes the largest snapshot a pane of a size can have, field by
// field, builds a state that has every field at its largest, and checks that
// the two agree. MaxSnapshotBytes is then the next whole MiB above the bound,
// less the 8 bytes of position a Snapshot frame carries before the snapshot.

// vlen is the length of a varint.
func vlen(v int) int {
	n := 1
	for ; v >= 0x80; v >>= 7 {
		n++
	}
	return n
}

// Largest values of the fields of the format, each the cap of the decoder.
const (
	maxStyleBytes = 17 // two bytes, and three colours of a tag and four bytes
	maxContent    = vt.MaxClusterBytes
)

// formatBound is the most bytes a snapshot of a pane of cols by rows has, at
// every field's largest: both screens, a cluster of 64 bytes in every cell,
// a style of its own for every cell and every pen, 256 links of the longest
// URL and parameters on each screen, all the modes, the longest title, an
// open cluster, and a number of the most bytes wherever a number is stored.
// It counts a style index in a cell at three bytes, which is the length of
// every index from 16384 up: the first 16383 styles are used in the order
// they are first used, so their indexes are shorter, and the exact worst case
// is a few thousand bytes under the bound (formatSlack).
func formatBound(cols, rows int) int {
	cells := cols * rows
	styles := 2*cells + 4
	n := 1 + vlen(cols) + vlen(rows) // version, size
	n += 1 + 3 + 4                   // flags, shifts, character sets
	n += vlen(0x7e)                  // last character
	n += 3 * (1 + 4*2)               // the three session colours
	modes := vt.RecordedModes()
	n += vlen(len(modes))
	for _, m := range modes {
		n += 1 + vlen(m.Mode()) + 1
	}
	n += (cols + 7) / 8                                                                // tab stops
	n += 1 + vlen(cols-1) + vlen(rows-1) + vlen(3) + 1 + vlen(maxContent) + maxContent // open cluster
	n += vlen(sessionpane.MaxTitle) + sessionpane.MaxTitle
	n += vlen(styles) + maxStyleBytes*styles

	link := vlen(vt.MaxLinkURL) + vt.MaxLinkURL + vlen(vt.MaxLinkParams) + vt.MaxLinkParams
	cursor := vlen(cols-1) + vlen(rows-1) + vlen(1<<31-1) + 1 + vlen(styles) + link
	screen := 2*vlen(cols) + 2*vlen(rows) // scroll region
	screen += 2 * cursor
	screen += vlen(vt.MaxLiveLinks) + vt.MaxLiveLinks*link + vlen(cells/256)
	screen += cells * (vlen(1) + vlen(maxContent) + maxContent + vlen(2) + vlen(styles) + vlen(vt.MaxLiveLinks))
	return n + 2*screen
}

// formatSlack is how far the bound is over the exact worst case: the style
// indexes under 16384 are one or two bytes and not three.
const formatSlack = 20000

// worstCaseState is a state with every field of the snapshot at its largest
// for a pane of cols by rows, that every part of it is one that output makes
// (a cell of 64 bytes, a style of three colours of four bytes, a link of the
// longest URL and parameters, the longest title and so on; the test of the
// output-built cell below shows it).
func worstCaseState(cols, rows int) vt.State {
	st := vt.NewEmulator(cols, rows).State()
	st.Modes = nil
	for _, m := range vt.RecordedModes() {
		st.Modes = append(st.Modes, vt.ModeState{Mode: m, Setting: 1})
	}
	st.GL, st.GR, st.GSingle = 3, 3, 3
	st.Charsets = [4]vt.CharSetID{vt.CharSetUK, vt.CharSetSpecialDrawing, vt.CharSetUK, vt.CharSetSpecialDrawing}
	st.LastChar = 0x7e
	st.AtPhantom = true
	st.Alt = true
	st.Foreground = color.RGBA64{R: 1, G: 2, B: 3, A: 4}
	st.Background = color.RGBA64{R: 1, G: 2, B: 3, A: 4}
	st.CursorColor = color.RGBA64{R: 1, G: 2, B: 3, A: 4}
	st.TabStops = nil
	for x := 0; x < cols; x++ {
		st.TabStops = append(st.TabStops, x)
	}
	st.OpenCluster = &vt.OpenClusterState{
		Pos: uv.Pos(cols-1, rows-1), Text: strings.Repeat("a", 1) + strings.Repeat("ั", 21), Capped: true, Single: 3,
	}

	links := make([]uv.Link, vt.MaxLiveLinks)
	for i := range links {
		url := "http://x.example/" + string(rune('0'+i/100)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + "/"
		links[i] = uv.Link{URL: url + strings.Repeat("u", vt.MaxLinkURL-len(url)), Params: strings.Repeat("p", vt.MaxLinkParams)}
	}
	links = slicesSorted(links)

	// A style of its own for every cell and every pen, each with the three
	// colours of the longest encoding.
	next := 0
	style := func() uv.Style {
		next++
		n := next
		return uv.Style{
			Fg:             color.RGBA{R: byte(n >> 16), G: byte(n), B: byte(n >> 8), A: 255},
			Bg:             color.CMYK{C: byte(n >> 8), M: byte(n >> 16), Y: byte(n), K: 1},
			UnderlineColor: color.RGBA{R: byte(n), G: byte(n >> 8), B: byte(n >> 16), A: 255},
			Attrs:          uv.AttrBold,
			Underline:      uv.UnderlineCurly,
		}
	}
	content := "a" + strings.Repeat("ั", 21)
	for s := range st.Screens {
		sc := &st.Screens[s]
		sc.Scroll = uv.Rect(cols-2, rows-2, 2, 2)
		sc.Links = links
		sc.LinkWait = cols * rows / 256
		for j, cur := range []*vt.Cursor{&sc.Cursor, &sc.Saved} {
			_ = j
			cur.X, cur.Y = cols-1, rows-1
			cur.Style = 1<<31 - 1
			cur.Steady, cur.Hidden = true, true
			cur.Pen = style()
			cur.Link = links[255]
		}
		for i := range sc.Cells {
			sc.Cells[i] = uv.Cell{
				Content: content,
				Width:   1,
				Style:   style(),
				// The links from the 128th on have a two-byte index.
				Link: links[127+i%129],
			}
		}
	}
	return st
}

func TestWorstCaseSnapshotIsTheBound(t *testing.T) {
	cols, rows := sessionpane.MaxCols, sessionpane.MaxRows
	if raceEnabled {
		cols, rows = cols/2, rows/2 // a quarter of the cells: the race detector is slow
	}
	p := newPane(t, cols, rows, sessionpane.Options{})
	p.Write([]byte("\x1b]0;" + strings.Repeat("t", sessionpane.MaxTitle) + "\x07"))
	if got := len(p.Title()); got != sessionpane.MaxTitle {
		t.Fatalf("a title of %d bytes", got)
	}
	must(t, sessionpane.SetEmulatorState(p, worstCaseState(cols, rows)))

	snap, err := sessionpane.AppendSnapshotLimit(p, nil, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	bound := formatBound(cols, rows)
	t.Logf("%dx%d: the snapshot of the worst case is %d bytes, the bound %d (%d over)", cols, rows, len(snap), bound, bound-len(snap))
	if len(snap) > bound {
		t.Errorf("the worst case, %d bytes, is over the bound of %d", len(snap), bound)
	}
	if bound-len(snap) > formatSlack {
		t.Errorf("the state is not the worst case: %d bytes under the bound of %d (at most %d)", bound-len(snap), bound, formatSlack)
	}

	// It is a snapshot like any other: it restores and restores to itself.
	q := newPane(t, 2, 1, sessionpane.Options{})
	must(t, q.Restore(snap))
	if again := snapshotOf2(t, q); !bytes.Equal(again, snap) {
		t.Fatal("the snapshot of the restored worst case differs")
	}
}

func snapshotOf2(t testing.TB, p *sessionpane.Pane) []byte {
	t.Helper()
	b, err := sessionpane.AppendSnapshotLimit(p, nil, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The cap is the bound of the largest pane, rounded up to a whole MiB less
// the position of a Snapshot frame, so that a frame that carries the largest
// snapshot is a whole number of MiB; and the frame cap of the wire package is
// that number. AppendSnapshot never fails for size on a state output makes.
func TestMaxSnapshotBytesIsTheBound(t *testing.T) {
	bound := formatBound(sessionpane.MaxCols, sessionpane.MaxRows)
	const mib = 1 << 20
	t.Logf("the bound of a pane of %dx%d is %d bytes (%.2f MiB); MaxSnapshotBytes is %d, the frame cap %d",
		sessionpane.MaxCols, sessionpane.MaxRows, bound, float64(bound)/mib, sessionpane.MaxSnapshotBytes, wire.MaxSnapshotPayload)
	if sessionpane.MaxSnapshotBytes < bound {
		t.Fatalf("MaxSnapshotBytes %d is under the bound %d: output can make a snapshot that AppendSnapshot refuses", sessionpane.MaxSnapshotBytes, bound)
	}
	frame := sessionpane.MaxSnapshotBytes + 8
	if frame%mib != 0 {
		t.Errorf("MaxSnapshotBytes+8 = %d is not a whole number of MiB", frame)
	}
	if frame-mib-8 >= bound {
		t.Errorf("MaxSnapshotBytes is a whole MiB too many: %d MiB less 8 holds the bound of %d", (frame-mib)/mib, bound)
	}
	if wire.MaxSnapshotData != sessionpane.MaxSnapshotBytes || wire.MaxSnapshotPayload != frame {
		t.Errorf("the frame carries %d bytes of snapshot in a payload of %d, the pane's cap is %d", wire.MaxSnapshotData, wire.MaxSnapshotPayload, sessionpane.MaxSnapshotBytes)
	}
}

// The model of the bound counts a cell of 64 bytes of content and width 1, a
// style of three colours of four bytes (17 bytes) and a link of the longest URL
// and parameters. Output makes all of them in one cell, which this test shows
// with a stream of the colour forms that have four bytes (two RGBA and one
// CMYK) and a cluster of a letter and 21 marks of three bytes each.
func TestWorstCaseCellIsWhatOutputMakes(t *testing.T) {
	const cols, rows = 6, 2
	p := newPane(t, cols, rows, sessionpane.Options{})
	var b strings.Builder
	url := "http://x.example/" + strings.Repeat("u", vt.MaxLinkURL-len("http://x.example/"))
	params := strings.Repeat("p", vt.MaxLinkParams-3) + "id="
	b.WriteString("\x1b]8;" + params + ";" + url + "\x1b\\")
	for i := 0; i < cols; i++ {
		fmt.Fprintf(&b, "\x1b[38;2;%d;2;3m\x1b[48:4::%d:2:3:4m\x1b[58;2;%d;2;3m", i+1, i+1, i+1)
		b.WriteString("a" + strings.Repeat("\u0e31", 21))
	}
	p.Write([]byte(b.String()))
	if c := p.Counters(); c.ClusterBytesDropped != 0 || c.LinksOverLength != 0 || c.LinksOverLimit != 0 {
		t.Fatalf("counters %+v", c)
	}
	styles := map[uv.Style]bool{}
	for x := 0; x < cols; x++ {
		c := sessionpane.CellAt(p, x, 0)
		if len(c.Content) != vt.MaxClusterBytes || c.Width != 1 || c.Link.URL != url || len(c.Link.Params) != vt.MaxLinkParams {
			t.Fatalf("cell %d: %d bytes, width %d, link of %d and %d bytes", x, len(c.Content), c.Width, len(c.Link.URL), len(c.Link.Params))
		}
		if _, ok := c.Style.Fg.(color.RGBA); !ok {
			t.Fatalf("cell %d: foreground %T", x, c.Style.Fg)
		}
		if _, ok := c.Style.Bg.(color.CMYK); !ok {
			t.Fatalf("cell %d: background %T", x, c.Style.Bg)
		}
		if _, ok := c.Style.UnderlineColor.(color.RGBA); !ok {
			t.Fatalf("cell %d: underline colour %T", x, c.Style.UnderlineColor)
		}
		styles[c.Style] = true
	}
	if len(styles) != cols {
		t.Errorf("%d different styles in %d cells", len(styles), cols)
	}
}

// What does not fit the limit is an error, and leaves dst alone. Output
// cannot make such a state with the limit MaxSnapshotBytes is (see above),
// so the test lowers the limit.
func TestSnapshotOverTheLimitFails(t *testing.T) {
	p := richPane(t)
	full := snapshotOf(t, p)
	prefix := []byte("prefix")
	for _, limit := range []int{0, 1, 100, len(full) / 2, len(full) - 1} {
		got, err := sessionpane.AppendSnapshotLimit(p, prefix, limit)
		if !errors.Is(err, sessionpane.ErrSnapshotTooLarge) {
			t.Fatalf("limit %d: AppendSnapshot = %d bytes, %v; want ErrSnapshotTooLarge", limit, len(got), err)
		}
		if !bytes.Equal(got, prefix) {
			t.Errorf("limit %d: dst changed to %d bytes", limit, len(got))
		}
	}
	got, err := sessionpane.AppendSnapshotLimit(p, prefix, len(full))
	if err != nil || !bytes.Equal(got, append(append([]byte(nil), prefix...), full...)) {
		t.Fatalf("a limit of exactly the size: %v", err)
	}
}
