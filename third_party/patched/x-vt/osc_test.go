package vt

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// OSC 8 wire format per https://gist.github.com/egmontkov/eb6100b9 is
// ESC ] 8 ; <params> ; <uri> ST. handleHyperlink must store the
// <params> segment in Link.Params and the <uri> segment in Link.URL.
// Prior to the fix in this commit the two were swapped: Link.URL
// received parts[1] (params) and Link.Params received parts[2] (uri),
// which produced visible-but-unclickable links once Buffer.Render()
// emitted them back out via ansi.SetHyperlink(URL, Params).
func TestHandleHyperlink_StoresURLAndParamsInCorrectFields(t *testing.T) {
	t.Parallel()

	t.Run("empty params, populated uri (common case)", func(t *testing.T) {
		t.Parallel()
		term := newTestTerminal(t, 80, 24)
		// Feed an OSC 8 open + a single character + OSC 8 close so the
		// open's Link metadata sticks on the cell.
		_, _ = term.Write([]byte("\x1b]8;;https://example.com/\x1b\\X\x1b]8;;\x1b\\"))
		cell := term.CellAt(0, 0)
		if cell == nil {
			t.Fatal("cell at (0,0) is nil")
		}
		if got, want := cell.Link.URL, "https://example.com/"; got != want {
			t.Errorf("Link.URL = %q, want %q", got, want)
		}
		if got, want := cell.Link.Params, ""; got != want {
			t.Errorf("Link.Params = %q, want %q", got, want)
		}
	})

	t.Run("populated params and uri", func(t *testing.T) {
		t.Parallel()
		term := newTestTerminal(t, 80, 24)
		_, _ = term.Write([]byte("\x1b]8;id=42:tag=demo;https://example.com/path?x=1\x1b\\X\x1b]8;;\x1b\\"))
		cell := term.CellAt(0, 0)
		if cell == nil {
			t.Fatal("cell at (0,0) is nil")
		}
		if got, want := cell.Link.URL, "https://example.com/path?x=1"; got != want {
			t.Errorf("Link.URL = %q, want %q", got, want)
		}
		if got, want := cell.Link.Params, "id=42:tag=demo"; got != want {
			t.Errorf("Link.Params = %q, want %q", got, want)
		}
	})
}

func link(params, url string) string {
	return "\x1b]8;" + params + ";" + url + "\x1b\\"
}

// A link at the caps is kept whole; one byte over either cap is dropped and
// counted, and the pen gets no link, as if the link had been closed: the text
// after it is drawn without the earlier open link as well.
func TestHyperlinkLengthCaps(t *testing.T) {
	url := func(n int) string { return "http://x/" + strings.Repeat("u", n-len("http://x/")) }
	params := func(n int) string { return strings.Repeat("p", n) }

	for _, tc := range []struct {
		name       string
		params     string
		url        string
		wantKept   bool
		wantOverBy uint64
	}{
		{"at both caps", params(MaxLinkParams), url(MaxLinkURL), true, 0},
		{"url one over", "", url(MaxLinkURL + 1), false, 1},
		{"params one over", params(MaxLinkParams + 1), "http://x/", false, 1},
		{"both over", params(MaxLinkParams + 1), url(MaxLinkURL + 1), false, 1},
		{"far over", "", url(100 << 10), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := NewEmulator(80, 24)
			// An open link first: a dropped link closes it.
			term.WriteString(link("", "http://before/") + "a")
			term.WriteString(link(tc.params, tc.url) + "b")
			term.WriteString(link("", "") + "c")
			a, b, c := term.CellAt(0, 0), term.CellAt(1, 0), term.CellAt(2, 0)
			if a.Link.URL != "http://before/" || c.Link.URL != "" {
				t.Fatalf("cells a and c: %+v %+v", a.Link, c.Link)
			}
			if kept := b.Link.URL == tc.url && b.Link.Params == tc.params; kept != tc.wantKept {
				t.Errorf("link kept = %v, want %v (URL %d bytes, params %d bytes)", kept, tc.wantKept, len(b.Link.URL), len(b.Link.Params))
			}
			if !tc.wantKept && (b.Link.URL != "" || b.Link.Params != "") {
				t.Errorf("a dropped link left %d and %d bytes in the cell", len(b.Link.URL), len(b.Link.Params))
			}
			if got := term.Counters().LinksOverLength; got != tc.wantOverBy {
				t.Errorf("LinksOverLength = %d, want %d", got, tc.wantOverBy)
			}
			// The next link is taken as usual.
			term.WriteString(link("id=1", "http://after/") + "d")
			if d := term.CellAt(3, 0); d.Link.URL != "http://after/" || d.Link.Params != "id=1" {
				t.Errorf("link after the cap: %+v", d.Link)
			}
		})
	}
}

// A link over the cap costs nothing to refuse: 100 links of 64 KiB neither
// grow the emulator nor give a cell a link, and each is counted once.
func TestOverlongHyperlinksCostNothing(t *testing.T) {
	const n = 100
	long := []byte(link("", "http://x/"+strings.Repeat("u", 64<<10)))
	term := NewEmulator(80, 24)
	alloc := allocated(func() {
		for i := 0; i < n; i++ {
			term.Write(long)
			term.WriteString("x")
		}
	})
	t.Logf("%d bytes of links, %d bytes allocated", n*len(long), alloc)
	if alloc > 64<<10 {
		t.Errorf("allocated %d bytes", alloc)
	}
	if got := term.Counters().LinksOverLength; got != n {
		t.Errorf("LinksOverLength = %d, want %d", got, n)
	}
	for x := 0; x < 24; x++ {
		if c := term.CellAt(x, 0); c.Link != (uv.Link{}) {
			t.Fatalf("cell %d has a link of %d bytes", x, len(c.Link.URL))
		}
	}
}
