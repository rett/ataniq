package sessionpane_test

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"runtime"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// ringText is the text of an encoded row, with the trailing blanks cut.
func ringText(t *testing.T, row []byte) string {
	t.Helper()
	line, err := sessionpane.DecodeRecentRow(row)
	if err != nil {
		t.Fatalf("decode row: %v", err)
	}
	var s strings.Builder
	for _, c := range line {
		s.WriteString(c.Content)
	}
	return strings.TrimRight(s.String(), " ")
}

// ringRows returns the text of every kept row, with "<omitted N>" for an
// interval.
func ringRows(t *testing.T, r sessionpane.Recent) []string {
	t.Helper()
	var out []string
	for _, e := range r.Entries {
		if e.Row == nil {
			out = append(out, fmt.Sprintf("<omitted %d>", e.Omitted))
		} else {
			out = append(out, ringText(t, e.Row))
		}
	}
	return out
}

func ringRowCount(r sessionpane.Recent) uint64 {
	var n uint64
	for _, e := range r.Entries {
		n += uint64(e.Rows())
	}
	return n
}

func TestCF04Recent(t *testing.T) {
	t.Run("OldestSurvivingRow", func(t *testing.T) {
		const height, limit, lines = 4, 50, 400
		p := sessionpane.New(20, height, sessionpane.Options{RecentRows: limit})
		defer p.Close()
		var out bytes.Buffer
		for i := range lines {
			fmt.Fprintf(&out, "row-%03d\r\n", i)
		}
		// More than one slice of output, in chunks that cut sequences.
		b := out.Bytes()
		for len(b) > 0 {
			n := min(len(b), 4099)
			p.Write(b[:n])
			b = b[n:]
		}
		// The screen holds the last height-1 rows and the empty line after
		// them; every row before them scrolled off.
		scrolled := uint64(lines + 1 - height)
		r := p.Recent()
		rows := ringRows(t, r)
		if len(rows) != limit {
			t.Fatalf("%d rows kept, want the bound %d", len(rows), limit)
		}
		if r.Next != scrolled || r.Oldest != scrolled-limit || r.Approximate {
			t.Fatalf("oldest %d next %d approximate %v; want %d and %d", r.Oldest, r.Next, r.Approximate, scrolled-limit, scrolled)
		}
		if want := fmt.Sprintf("row-%03d", r.Oldest); rows[0] != want {
			t.Fatalf("oldest surviving row %q, want %q", rows[0], want)
		}
		if want := fmt.Sprintf("row-%03d", scrolled-1); rows[limit-1] != want {
			t.Fatalf("newest row %q, want %q", rows[limit-1], want)
		}
		// Keeps collecting under the cap: the oldest moves by the rows added.
		p.Write([]byte("more-a\r\nmore-b\r\n"))
		r2 := p.Recent()
		if r2.Oldest != r.Oldest+2 || r2.Next != r.Next+2 {
			t.Fatalf("after two rows: oldest %d next %d", r2.Oldest, r2.Next)
		}
		rows2 := ringRows(t, r2)
		if len(rows2) != limit || rows2[limit-1] != fmt.Sprintf("row-%03d", lines-2) {
			t.Fatalf("rows after more output: %q", rows2[limit-2:])
		}
		// The copy is a copy: it does not move with the ring.
		if r.Next != scrolled {
			t.Fatal("a copy of the ring changed")
		}
		// A sequence that scrolls more rows at once than the ring's window
		// holds keeps the newest rows and says the numbers are lower bounds.
		q := sessionpane.New(20, 20, sessionpane.Options{RecentRows: 5})
		defer q.Close()
		var burst bytes.Buffer
		for i := range 20 {
			fmt.Fprintf(&burst, "r%02d\r\n", i)
		}
		q.Write(bytes.TrimSuffix(burst.Bytes(), []byte("\r\n")))
		if r := q.Recent(); r.Next != 0 {
			t.Fatalf("rows scrolled before the burst: %d", r.Next)
		}
		q.Write([]byte("\x1b[20S"))
		rb := q.Recent()
		if got := ringRows(t, rb); len(got) != 5 || got[0] != "r15" || got[4] != "r19" || !rb.Approximate {
			t.Fatalf("burst: %q approximate %v", got, rb.Approximate)
		}
		// A pane without the option keeps nothing, and a closed one releases it.
		none := sessionpane.New(20, 4, sessionpane.Options{})
		none.Write(burst.Bytes())
		none.Close()
		if r := none.Recent(); r.Next != 0 || len(r.Entries) != 0 {
			t.Fatalf("a pane with no ring has %+v", r)
		}
		p.Close()
		if r := p.Recent(); len(r.Entries) != 0 {
			t.Fatal("a closed pane kept its ring")
		}
	})

	t.Run("NoAltScreenHistory", func(t *testing.T) {
		p := sessionpane.New(80, 10, sessionpane.Options{RecentRows: 100})
		defer p.Close()
		p.Write([]byte("before-1\r\nbefore-2\r\n"))
		p.Write([]byte("\x1b[?1049h"))
		var alt strings.Builder
		for i := range 5000 {
			fmt.Fprintf(&alt, "alt-%05d\r\n", i)
		}
		p.Write([]byte(alt.String()))
		p.Write([]byte("\x1b[?1049l"))
		if r := p.Recent(); r.Next != 0 || len(r.Entries) != 0 {
			t.Fatalf("the alternate screen gave the ring %+v", r)
		}
		if n := sessionpane.ScrollbackLen(p); n != 0 {
			t.Fatalf("%d rows left in the emulator's window", n)
		}
		// The main screen still collects after the alternate screen.
		p.Write([]byte(strings.Repeat("main\r\n", 20)))
		if r := p.Recent(); r.Next == 0 || ringRows(t, r)[0] != "before-1" {
			t.Fatalf("main screen after alt: %q", ringRows(t, r))
		}

		// The stock x/vt kept 10000 lines for the alternate screen, hundreds
		// of megabytes for a wide one. Scroll wide rows there and check that
		// the heap does not grow with them.
		w := sessionpane.New(200, 20, sessionpane.Options{RecentRows: 100})
		defer w.Close()
		w.Write([]byte("\x1b[?1049h"))
		row := strings.Repeat("\x1b[31mx\x1b[0mabcdefghi", 20) + "\r\n"
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range 6000 {
			w.Write([]byte(row))
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 8<<20 {
			t.Fatalf("heap grew by %d bytes while the alternate screen scrolled", grew)
		}
		if r := w.Recent(); r.Next != 0 {
			t.Fatalf("alternate screen rows reached the ring: %d", r.Next)
		}
	})

	t.Run("OmittedIntervalNeverReappears", func(t *testing.T) {
		const secret = "SECRET-TOKEN"
		p := sessionpane.New(30, 4, sessionpane.Options{RecentRows: 200})
		defer p.Close()
		scroll := func(prefix string, n int) {
			var b strings.Builder
			for i := range n {
				fmt.Fprintf(&b, "%s-%02d\r\n", prefix, i)
			}
			p.Write([]byte(b.String()))
		}
		scroll("pre", 6)
		before := p.Recent()
		if p.Sensitive() {
			t.Fatal("switch starts on")
		}
		p.SetSensitive(true)
		scroll(secret, 7)
		// Cells written under the switch are on the screen when it goes
		// off, and later scroll off the screen, or are cut by a resize.
		p.Write([]byte("\x1b[1;1H" + secret + "-LAST"))
		p.SetSensitive(false)
		p.SetSensitive(false)
		p.Resize(60, 6)
		scroll("post", 2)
		p.Resize(12, 3)
		p.Resize(80, 5)
		scroll("late", 30)
		r := p.Recent()
		if r.Oldest != 0 || r.Approximate {
			t.Fatalf("oldest %d approximate %v", r.Oldest, r.Approximate)
		}
		for i, e := range r.Entries {
			if e.Row != nil && bytes.Contains(e.Row, []byte("SECRET")) {
				t.Fatalf("entry %d holds the secret", i)
			}
		}
		rows := ringRows(t, r)
		for _, s := range rows {
			if strings.Contains(s, "SECRET") {
				t.Fatalf("a row shows the secret: %q", s)
			}
		}
		// The rows before the interval are as they were; the interval is one
		// entry with no content, as long as the rows that scrolled under the
		// switch and the screen that held its cells when it went off.
		if len(rows) < len(before.Entries) || rows[0] != "pre-00" {
			t.Fatalf("rows before the interval changed: %q", rows)
		}
		omitted := -1
		for i, s := range rows {
			if strings.HasPrefix(s, "<omitted") {
				if omitted >= 0 {
					t.Fatalf("two omitted entries: %q", rows)
				}
				omitted = i
			}
		}
		if omitted != len(before.Entries) {
			t.Fatalf("interval at entry %d, want %d: %q", omitted, len(before.Entries), rows)
		}
		got := r.Entries[omitted].Omitted
		if got < 7 || got > 7+4 {
			t.Fatalf("interval of %d rows, want the rows scrolled under the switch (7) and at most one screen (4) more", got)
		}
		if r.Entries[omitted].Row != nil {
			t.Fatal("an omitted interval has content")
		}
		// Whatever came after the interval is recorded again.
		if last := rows[len(rows)-1]; !strings.HasPrefix(last, "late-") {
			t.Fatalf("rows after the interval: %q", rows[omitted+1:])
		}
		if r.Next != ringRowCount(r) {
			t.Fatalf("next %d for %d rows", r.Next, ringRowCount(r))
		}

		// A row that was on the screen when the switch went on and scrolls off
		// under it is omitted too, and an interval keeps its count when the
		// ring evicts into it.
		q := sessionpane.New(10, 3, sessionpane.Options{RecentRows: 4})
		defer q.Close()
		q.Write([]byte("a\r\nb\r\nc\r\n"))
		q.SetSensitive(true)
		q.Write([]byte(strings.Repeat("s\r\n", 10)))
		rq := q.Recent()
		if got := ringRows(t, rq); len(got) != 1 || got[0] != "<omitted 4>" || rq.Next-rq.Oldest != 4 || rq.Oldest == 0 {
			t.Fatalf("evicting into an interval: %q oldest %d next %d", got, rq.Oldest, rq.Next)
		}
		// A snapshot restore drops the ring and the switch; a pane without a
		// ring ignores the switch.
		snap, err := q.AppendSnapshot(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Restore(snap); err != nil {
			t.Fatal(err)
		}
		if r := q.Recent(); len(r.Entries) != 0 || q.Sensitive() {
			t.Fatalf("restore kept %+v", r)
		}
		n := sessionpane.New(10, 3, sessionpane.Options{})
		defer n.Close()
		n.SetSensitive(true)
		if n.Sensitive() {
			t.Fatal("a pane with no ring has a switch")
		}
	})

	t.Run("FrameCappedAndVersioned", func(t *testing.T) {
		// The row format round-trips and is strict.
		line := uv.Line{
			{Content: "h", Width: 1},
			{Content: "界", Width: 2, Style: uv.Style{Attrs: uv.AttrBold}},
			{Content: "", Width: 0, Style: uv.Style{Attrs: uv.AttrBold}},
			{Content: "é", Width: 1, Link: uv.Link{URL: "https://example.invalid"}},
		}
		enc := sessionpane.EncodeRecentRow(line)
		dec, err := sessionpane.DecodeRecentRow(enc)
		if err != nil || len(dec) != len(line) {
			t.Fatalf("round trip: %v %d cells", err, len(dec))
		}
		for i := range line {
			want := line[i]
			want.Link = uv.Link{}
			if dec[i] != want {
				t.Fatalf("cell %d: %#v, want %#v", i, dec[i], want)
			}
		}
		for i := range enc {
			if _, err := sessionpane.DecodeRecentRow(enc[:i]); !errors.Is(err, sessionpane.ErrBadRecentRow) {
				t.Fatalf("a row cut at %d decoded: %v", i, err)
			}
		}
		if _, err := sessionpane.DecodeRecentRow(append(bytes.Clone(enc), 0)); !errors.Is(err, sessionpane.ErrBadRecentRow) {
			t.Fatal("bytes left over decoded")
		}
		// The largest row, of cells with the longest cluster each and a style
		// of their own, stays inside the row cap.
		wide := make(uv.Line, sessionpane.MaxCols)
		for i := range wide {
			wide[i] = uv.Cell{Content: strings.Repeat("a", 64), Width: 1, Style: uv.Style{
				Attrs:          byte(i),
				Bg:             colourOf(i),
				Fg:             colourOf(i + 1),
				UnderlineColor: colourOf(i + 2),
			}}
		}
		if n := len(sessionpane.EncodeRecentRow(wide)); n > sessionpane.MaxRecentRowBytes {
			t.Fatalf("largest row is %d bytes, cap %d", n, sessionpane.MaxRecentRowBytes)
		}

		// A byte budget bounds the ring, whatever the rows.
		p := sessionpane.New(80, 3, sessionpane.Options{RecentRows: 1000, RecentBytes: 4096})
		defer p.Close()
		var b strings.Builder
		for i := range 300 {
			fmt.Fprintf(&b, "\x1b[3%dmline %03d %s\x1b[0m\r\n", i%8, i, strings.Repeat("z", 60))
		}
		p.Write([]byte(b.String()))
		r := p.Recent()
		size := 0
		for _, e := range r.Entries {
			size += len(e.Row) + 32
		}
		if len(r.Entries) == 0 || len(r.Entries) >= 300 || size > 4096 || r.Oldest == 0 {
			t.Fatalf("%d entries of %d bytes, oldest %d", len(r.Entries), size, r.Oldest)
		}
	})
}

func colourOf(i int) color.Color {
	return color.RGBA{R: byte(i), G: byte(i >> 8), B: 7, A: 255}
}

func TestRecentCapsAgreeWithTheFrame(t *testing.T) {
	if sessionpane.MaxRecentRowBytes != wire.MaxHistoryRowBytes || sessionpane.MaxRecentRows != wire.MaxHistoryRows {
		t.Fatalf("pane caps %d, %d; frame caps %d, %d", sessionpane.MaxRecentRowBytes, sessionpane.MaxRecentRows, wire.MaxHistoryRowBytes, wire.MaxHistoryRows)
	}
	if sessionpane.DefaultRecentRows > wire.MaxHistoryRows {
		t.Fatal("the default ring does not fit a frame's row count")
	}
}
