//go:build linux && !race

package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// h15Files writes the output that fills a 500 by 200 screen as the lead-hosting
// design's worst case does: every cell holds a cluster of 63 bytes (a letter
// and 31 combining marks of two bytes each, one byte under the cell cap of 64)
// and 256 links of the longest URL and parameters the emulator keeps are drawn
// in runs of cells, so that the screen holds the most links it can. It is one
// file for every MiB the fake agent can print at once.
func h15Files(t testing.TB, dir string) []string {
	t.Helper()
	const cols, rows, links = 500, 200, 256
	const perLink = cols * rows / links
	var files []string
	var b strings.Builder
	flush := func() {
		name := filepath.Join(dir, fmt.Sprintf("h15-%d", len(files)))
		if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, name)
		b.Reset()
	}
	link := func(n int) string {
		params := fmt.Sprintf("id=l%03d", n)
		params += strings.Repeat("p", vt.MaxLinkParams-len(params))
		url := fmt.Sprintf("https://example.test/%03d/", n)
		url += strings.Repeat("u", vt.MaxLinkURL-len(url))
		return "\x1b]8;" + params + ";" + url + "\x1b\\"
	}
	b.WriteString("\x1b[H")
	for i := 0; i < cols*rows; i++ {
		if i%cols == 0 {
			if i > 0 {
				b.WriteString("\r\n")
			}
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", i%251, (i/7)%251, (i/13)%251)
		}
		if i%perLink == 0 {
			b.WriteString(link(min(i/perLink, links-1)))
		}
		b.WriteRune(rune('a' + i%26))
		for k := 0; k < 31; k++ {
			b.WriteRune(rune(0x300 + (i+k)%112))
		}
		if b.Len() > 1<<20-8192 { // a cell and a link fit in what is left
			flush()
		}
	}
	b.WriteString("\x1b]8;;\x1b\\")
	flush()
	if len(files) > 8 {
		t.Fatalf("%d files", len(files))
	}
	return files
}

// Case A13 with the screen of the design's H15 (500 by 200, clusters of 63
// bytes, 256 links of the longest URL): a client attaches, the snapshot is
// under the frame's cap, and the client's own pane digest is the one that the
// holder's Check carries. This is a test without the race detector: that
// screen takes the emulator too long under it.
func TestAttachToTheH15Screen(t *testing.T) {
	files := h15Files(t, t.TempDir())
	e := newEnv(t, "raw; "+strings.Join(prefixAll("printfile ", files), "; ")+"; wait", func(o *envOpts) {
		o.session = func(c *session.Config) { c.Cols, c.Rows = 500, 200 }
		o.cfg = func(c *Config) { c.CheckInterval = 100 * time.Millisecond }
	})
	waitPosition(t, e.sess, filesSize(t, files)) // the screen is filled
	waitQuiet(t, e.sess)

	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	v.mu.Lock()
	size := v.snapSizes[0]
	v.mu.Unlock()
	t.Logf("the snapshot is %d bytes (the frame holds at most %d)", size, wire.MaxSnapshotData)
	if size >= wire.MaxSnapshotData {
		t.Fatalf("the snapshot is %d bytes, not under %d", size, wire.MaxSnapshotData)
	}
	// 100 000 cells of 63 bytes: the screen really is the worst case.
	if size < 500*200*63 {
		t.Errorf("the snapshot is %d bytes: under the %d bytes of the clusters, so the screen is not the worst case", size, 500*200*63)
	}
	if n := e.count(cntSnapshotErrors); n != 0 {
		t.Errorf("snapshot errors %d", n)
	}
	st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}).Status
	if n := st.Counters["links_over_limit"]; n != 0 {
		t.Errorf("%d cells lost their link: the screen holds more than the 256 links", n)
	}

	// The Check after the snapshot carries the digest of the client's pane.
	v.waitChecks(2)
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.checks) < 2 {
		t.Fatalf("%d checks", len(v.checks))
	}
	ck := v.checks[0]
	if ck.Position != v.pos {
		t.Fatalf("the check is for position %d, the client is at %d: the output went on", ck.Position, v.pos)
	}
	if got := wire.DigestHex(v.pane.Digest()); got != ck.Digest {
		t.Errorf("the client's pane digest %s is not the check's %s", got, ck.Digest)
	}
}

func prefixAll(prefix string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = prefix + s
	}
	return out
}
