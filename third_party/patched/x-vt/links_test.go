package vt

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	uv "github.com/charmbracelet/ultraviolet"
)

// liveLinks returns the distinct links the cells of the screen reference.
func liveLinks(s *Screen) map[uv.Link]int {
	live := map[uv.Link]int{}
	for _, line := range s.buf.Lines {
		for _, c := range line {
			if c.Link.URL != "" {
				live[c.Link]++
			}
		}
	}
	return live
}

// printLinks prints one character under each of n distinct links, numbered
// from first, and closes the last.
func printLinks(e *Emulator, first, n int) {
	var b strings.Builder
	for i := first; i < first+n; i++ {
		b.WriteString(link("", fmt.Sprintf("http://x/%d", i)))
		b.WriteByte('x')
	}
	b.WriteString(link("", ""))
	e.WriteString(b.String())
}

func TestDistinctLiveLinksAreCapped(t *testing.T) {
	e := NewEmulator(80, 24)
	printLinks(e, 0, MaxLiveLinks+44)
	if got := len(liveLinks(e.scr)); got != MaxLiveLinks {
		t.Errorf("%d live links, want %d", got, MaxLiveLinks)
	}
	if got := e.Counters().LinksOverLimit; got != 44 {
		t.Errorf("LinksOverLimit = %d, want 44", got)
	}
	// The first MaxLiveLinks cells have their links, the rest none, drawn.
	for i := 0; i < MaxLiveLinks+44; i++ {
		c := e.CellAt(i%80, i/80)
		if c.Content != "x" {
			t.Fatalf("cell %d: %q", i, c.Content)
		}
		if want := i < MaxLiveLinks; (c.Link.URL != "") != want {
			t.Fatalf("cell %d: link %q, want a link %v", i, c.Link.URL, want)
		}
	}
	// Many cells under one link are one link: no count.
	e.WriteString("\x1b[2J\x1b[H")
	e.WriteString(link("", "http://one/") + strings.Repeat("y", 1000))
	if got := e.Counters().LinksOverLimit; got != 44 {
		t.Errorf("LinksOverLimit = %d after one link on many cells, want 44", got)
	}
	// The same URL with other parameters is another link.
	e = NewEmulator(80, 24)
	var b strings.Builder
	for i := 0; i < MaxLiveLinks+3; i++ {
		b.WriteString(link(fmt.Sprintf("id=%d", i), "http://same/") + "x")
	}
	e.WriteString(b.String())
	if got := len(liveLinks(e.scr)); got != MaxLiveLinks {
		t.Errorf("%d live links with distinct parameters, want %d", got, MaxLiveLinks)
	}
	if got := e.Counters().LinksOverLimit; got != 3 {
		t.Errorf("LinksOverLimit = %d, want 3", got)
	}
}

// A link whose cells were all overwritten, erased, scrolled away or cut off by
// a resize frees its slot.
func TestLiveLinkSlotsAreFreed(t *testing.T) {
	for _, tc := range []struct {
		name string
		free func(*Emulator)
	}{
		{"erased", func(e *Emulator) { e.WriteString("\x1b[2J") }},
		{"erased in a line", func(e *Emulator) { e.WriteString("\x1b[H\x1b[0J") }},
		{"overwritten", func(e *Emulator) { e.WriteString("\x1b[H" + strings.Repeat("o", 80*4)) }},
		{"scrolled away", func(e *Emulator) { e.WriteString("\x1b[24;1H" + strings.Repeat("\n", 40)) }},
		{"deleted lines", func(e *Emulator) { e.WriteString("\x1b[H\x1b[24M") }},
		{"resized away", func(e *Emulator) { e.Resize(80, 1); e.Resize(80, 24) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(80, 24)
			printLinks(e, 0, MaxLiveLinks+1) // the last one is refused: the table is full
			if got := e.Counters().LinksOverLimit; got != 1 {
				t.Fatalf("LinksOverLimit = %d, want 1", got)
			}
			tc.free(e)
			// A new link is taken again after at most one recount interval of
			// refusals, and the cap holds the whole time.
			interval := e.scr.Width() * e.scr.Height() / recountCost
			e.WriteString("\x1b[H")
			refused := 0
			for i := 0; ; i++ {
				if i > interval {
					t.Fatalf("still refused after %d cells (interval %d)", i, interval)
				}
				x, y := e.scr.CursorPosition()
				e.WriteString(link("", fmt.Sprintf("http://new/%d", i)) + "n")
				if e.CellAt(x, y).Link.URL != "" {
					break
				}
				refused++
			}
			t.Logf("refused %d cells of an interval of %d", refused, interval)
			if live := len(liveLinks(e.scr)); live > MaxLiveLinks {
				t.Errorf("%d live links", live)
			}
		})
	}
}

// The slots are kept per screen: the alternate screen has its own.
func TestLiveLinksArePerScreen(t *testing.T) {
	e := NewEmulator(80, 24)
	printLinks(e, 0, MaxLiveLinks)
	e.WriteString("\x1b[?1049h")
	printLinks(e, 1000, MaxLiveLinks+2)
	if got := len(liveLinks(e.scr)); got != MaxLiveLinks {
		t.Errorf("alternate screen: %d live links", got)
	}
	if got := e.Counters().LinksOverLimit; got != 2 {
		t.Errorf("LinksOverLimit = %d, want 2", got)
	}
	e.WriteString("\x1b[?1049l")
	if got := len(liveLinks(e.scr)); got != MaxLiveLinks {
		t.Errorf("main screen: %d live links", got)
	}
	// A full reset empties both and both take links again.
	e.WriteString("\x1bc")
	printLinks(e, 2000, MaxLiveLinks)
	if got := e.Counters().LinksOverLimit; got != 2 {
		t.Errorf("LinksOverLimit = %d after a reset, want 2", got)
	}
}

// Design case H15: every cell of a 500 x 200 screen has a cluster of 63 bytes
// and a link of its own. Every 5000th link is 64 KiB, which is over the length
// cap; the others are short and distinct, 100 000 of them in all. At most
// MaxLiveLinks are live, the memory stays under a fixed bound, the time spent
// recounting stays proportional to the cells printed, and the screen, as a
// snapshot would carry it, is far under 16 MiB.
func TestWorstCaseScreen(t *testing.T) {
	const cols, rows = 500, 200
	const cells = cols * rows
	const longEvery = 5000
	cluster := "e" + strings.Repeat("́", 31)
	longURL := "http://x/" + strings.Repeat("u", 64<<10)

	input := func() []byte {
		var b strings.Builder
		for i := 0; i < cells; i++ {
			if i%longEvery == 0 {
				b.WriteString(link("", longURL+fmt.Sprint(i)))
			} else {
				b.WriteString(link("", fmt.Sprintf("http://x/%d", i)))
			}
			b.WriteString(cluster)
		}
		return []byte(b.String())
	}()

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	e := NewEmulator(cols, rows)
	alloc := allocated(func() { e.Write(input) })
	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(input)
	t.Logf("%d bytes written, %d bytes allocated, heap %d MiB", len(input), alloc, (int64(m1.HeapAlloc)-int64(m0.HeapAlloc))>>20)

	if got := len(liveLinks(e.scr)); got > MaxLiveLinks {
		t.Errorf("%d live links, want at most %d", got, MaxLiveLinks)
	}
	c := e.Counters()
	if want := uint64(cells / longEvery); c.LinksOverLength != want {
		t.Errorf("LinksOverLength = %d, want %d", c.LinksOverLength, want)
	}
	if want := uint64(cells - cells/longEvery - MaxLiveLinks); c.LinksOverLimit != want {
		t.Errorf("LinksOverLimit = %d, want %d", c.LinksOverLimit, want)
	}
	if c.ClusterBytesDropped != 0 {
		t.Errorf("ClusterBytesDropped = %d", c.ClusterBytesDropped)
	}
	// Each recount scans the screen and the refusals pay for it.
	if limit := cells/(cells/recountCost) + 2; e.scr.links.recounts > limit {
		t.Errorf("%d recounts for %d refusals, want at most %d", e.scr.links.recounts, c.LinksOverLimit, limit)
	}

	// The live heap: two screens of 100 000 cells and the table. Uncapped,
	// the 64 KiB links alone would be 400 x 64 KiB and the short ones
	// 100 000 x 16 bytes of key in a table that grew with them.
	if grown := int64(m1.HeapAlloc) - int64(m0.HeapAlloc); grown > 128<<20 {
		t.Errorf("heap grew %d MiB", grown>>20)
	}
	// What a snapshot of the screen carries: the content of every cell and
	// each live link once.
	var snapshot int
	for _, line := range e.scr.buf.Lines {
		for _, c := range line {
			snapshot += len(c.Content)
		}
	}
	for l := range liveLinks(e.scr) {
		snapshot += len(l.URL) + len(l.Params)
	}
	t.Logf("snapshot payload %d bytes", snapshot)
	if snapshot > 16<<20 {
		t.Errorf("a snapshot would carry %d bytes", snapshot)
	}
	if len(e.scr.links.set) > MaxLiveLinks {
		t.Errorf("table holds %d links", len(e.scr.links.set))
	}
}

// Scanning for live links is bounded by the screen: a session that prints a
// new link on every cell of a full table pays one pass over the cells for
// every recount interval of cells, not one for each cell.
func TestLinkRecountsAreRateLimited(t *testing.T) {
	e := NewEmulator(200, 100)
	printLinks(e, 0, MaxLiveLinks)
	const n = 20000
	printLinks(e, 10000, n)
	interval := 200 * 100 / recountCost
	if got, limit := e.scr.links.recounts, n/interval+2; got > limit {
		t.Errorf("%d recounts for %d refused cells, want at most %d", got, n, limit)
	}
	// Each recount can free the slots that the printing overwrote, so some of
	// the cells keep their link; all the others are refused and counted.
	refused := e.Counters().LinksOverLimit
	if admitted := n - int(refused); admitted < 0 || admitted > e.scr.links.recounts*MaxLiveLinks {
		t.Errorf("%d of %d cells refused after %d recounts", refused, n, e.scr.links.recounts)
	}
	if live := len(liveLinks(e.scr)); live > MaxLiveLinks {
		t.Errorf("%d live links", live)
	}
}

// heapCeilingResent is the most the heap may grow for the test below.
const heapCeilingResent = 48 << 20

// maxLink is a link at both caps, which is the most a cell can reference.
func maxLink() (params, url string) {
	return "id=" + strings.Repeat("p", MaxLinkParams-len("id=")),
		"http://x/" + strings.Repeat("u", MaxLinkURL-len("http://x/"))
}

// A link that is sent again is one link: the cells printed under it share the
// table's copy of its strings, and do not each keep the copy the pen made for
// the OSC 8 that opened it.
func TestResentLinkIsOneCopy(t *testing.T) {
	e := NewEmulator(20, 4)
	params, url := maxLink()
	for i := 0; i < 30; i++ {
		e.WriteString(link(params, url) + "x")
	}
	// A scroll, an insert and a delete move cells, and none of them copies
	// the strings.
	e.WriteString("\x1b[2;1H\x1b[2L\x1b[3;1H\x1b[M\x1b[1;1H\x1b[3@")
	e.Resize(7, 6)
	e.WriteString("\x1b[?1049h\x1b[?1049l")
	var first *byte
	n := 0
	for _, line := range e.scr.buf.Lines {
		for _, c := range line {
			if c.Link.URL == "" {
				continue
			}
			if c.Link.URL != url || c.Link.Params != params {
				t.Fatalf("a cell holds link %q;%q", c.Link.Params, c.Link.URL)
			}
			p := unsafe.StringData(c.Link.URL)
			if first == nil {
				first = p
			}
			if p != first {
				t.Fatalf("cell %d holds its own copy of the URL", n)
			}
			n++
		}
	}
	if n == 0 {
		t.Fatal("no cell has a link")
	}
	if got := len(e.scr.links.set); got != 1 {
		t.Errorf("table holds %d links, want 1", got)
	}
	// A cell printed after the table was recounted shares a copy too.
	e.WriteString("\x1b[2J")
	e.WriteString(link(params, url) + "y")
	if got := e.scr.links.set[uv.Link{URL: url, Params: params}]; unsafe.StringData(got.URL) != unsafe.StringData(e.CellAt(0, 0).Link.URL) {
		t.Errorf("the cell does not hold the table's copy")
	}
}

// Sending one maximum-length link again before every character of a screen is
// one distinct link, and the screen must not hold a copy of it per cell: that
// is 100 000 x 2.3 KiB, 230 MiB, on a 500 x 200 screen. The text is written
// a row at a time so that the input is not held in memory.
func TestResentMaxLengthLinkOnEveryCell(t *testing.T) {
	const cols, rows = 500, 200
	params, url := maxLink()
	var b strings.Builder
	for x := 0; x < cols; x++ {
		b.WriteString(link(params, url))
		b.WriteByte('x')
	}
	row := []byte(b.String())
	b.Reset()

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	e := NewEmulator(cols, rows)
	for y := 0; y < rows; y++ {
		e.Write(row)
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(row)
	grown := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	t.Logf("heap grew %d MiB, %d live links", grown>>20, len(liveLinks(e.scr)))

	if got := len(liveLinks(e.scr)); got != 1 {
		t.Errorf("%d live links, want 1", got)
	}
	if c := e.Counters(); c.LinksOverLimit != 0 || c.LinksOverLength != 0 {
		t.Errorf("counters %+v", c)
	}
	// The cells themselves are about 20 MiB (the review measured 22). A copy
	// of the link in every cell would be 241 MiB.
	if grown > heapCeilingResent {
		t.Errorf("heap grew %d MiB, want at most %d MiB: cells keep their own copies of a re-sent link", grown>>20, heapCeilingResent>>20)
	}
	runtime.KeepAlive(e)
}
