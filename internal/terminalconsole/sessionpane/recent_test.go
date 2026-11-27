package sessionpane_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

func TestPagedBackRecentRowsAreLocalAndBounded(t *testing.T) {
	const height = 4
	holder := sessionpane.New(20, height, sessionpane.Options{})
	client := sessionpane.New(20, height, sessionpane.Options{RecentOutput: true})
	defer holder.Close()
	defer client.Close()
	n := sessionpane.RecentRowLimit + 12
	for i := range n {
		line := []byte(fmt.Sprintf("row-%03d\r\n", i))
		holder.Write(line)
		client.Write(line)
	}
	if holder.PageRecent(-1, height) {
		t.Fatal("default holder retained history")
	}
	if client.PageRecent(-1, 0) || client.PageRecent(1, height) {
		t.Fatal("opened a page without a page-back request and room")
	}
	for range n {
		client.PageRecent(-1, height)
	}
	buf := uv.NewBuffer(20, height)
	page, pages := client.DrawViewport(buf, buf.Bounds())
	first := n + 1 - height - sessionpane.RecentRowLimit
	if pages != sessionpane.RecentRowLimit/height || page != pages || row(buf, 0) != fmt.Sprintf("row-%03d", first) {
		t.Fatalf("retained history: page %d/%d begins %q", page, pages, row(buf, 0))
	}
	if client.PageRecent(0, 0) {
		t.Fatal("return to live kept the page")
	}
	// A snapshot carries the holder's screen. Restoring it drops a
	// previously opened page and starts a fresh local history.
	client.PageRecent(-1, height)
	snapshot, err := holder.AppendSnapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := client.AppendSnapshot(nil)
	if err != nil || !bytes.Equal(local, snapshot) || client.Digest() != holder.Digest() {
		t.Fatal("local history changed the terminal snapshot or digest", err)
	}
	if err := client.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	page, pages = client.DrawViewport(buf, buf.Bounds())
	if page != 0 || pages != 0 || client.PageRecent(-1, height) {
		t.Fatal("restored history survived")
	}
	client.Write([]byte("\x1b[?1049h" + strings.Repeat("alternate\r\n", 20) + "\x1b[?1049l"))
	if client.PageRecent(-1, height) {
		t.Fatal("captured alternate-screen rows")
	}
	client.Write([]byte(strings.Repeat("main\r\n", 20)))
	if !client.PageRecent(-1, height) {
		t.Fatal("did not collect new rows after restore")
	}
	client.Close()
	if client.PageRecent(-1, height) {
		t.Fatal("closed client kept history")
	}
}

func TestPagedBackViewportRespectsSynchronizedOutput(t *testing.T) {
	p := sessionpane.New(8, 2, sessionpane.Options{
		RecentOutput: true,
		Clock:        func() time.Time { return time.Unix(1, 0) },
	})
	defer p.Close()
	p.Write([]byte("before\r\nsecond\x1b[?2026h\r\nafter"))
	if p.PageRecent(-1, 2) {
		t.Fatal("entered history during a held frame")
	}
	buf := uv.NewBuffer(8, 2)
	p.DrawViewport(buf, buf.Bounds())
	if row(buf, 0) != "before" || row(buf, 1) != "second" {
		t.Fatalf("partial update in viewport: %q", buf.String())
	}
	p.Write([]byte("\x1b[?2026l"))
	if !p.PageRecent(-1, 2) {
		t.Fatal("missing row after synchronized update ended")
	}
}

func TestPagedBackViewportContainsWideAndLinkedCells(t *testing.T) {
	p := sessionpane.New(8, 2, sessionpane.Options{RecentOutput: true})
	defer p.Close()
	p.Write([]byte("\x1b]8;;https://example.invalid\x1b\\AB界\x1b]8;;\x1b\\\r\nnext\r\nlive"))
	if !p.PageRecent(-1, 1) {
		t.Fatal("missing row")
	}
	buf := uv.NewBuffer(7, 3)
	buf.Fill(&uv.Cell{Content: "#", Width: 1})
	area := uv.Rect(2, 1, 3, 1)
	page, count := p.DrawViewport(buf, area)
	if page != 1 || count != 1 || row(buf, 0) != "#######" || row(buf, 1) != "##AB ##" || row(buf, 2) != "#######" {
		t.Fatalf("clipped page %d/%d: %q", page, count, buf.String())
	}
	if buf.CellAt(2, 1).Link != (uv.Link{}) {
		t.Fatal("viewport emitted a hyperlink")
	}
}
