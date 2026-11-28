package seatapp

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestSeatWatchReloadGapIsTwentySeconds: the card list reloads itself only
// once its own read (the page's load) is at least 20 seconds old, so that the
// reloads of three lists open in a busy period stay inside the 12 reads the
// seat has in 80 seconds as far as a gap can keep them. The script's timer
// is set for what is left of that age, never sooner; this reads the text,
// as the repository has no JavaScript engine.
func TestSeatWatchReloadGapIsTwentySeconds(t *testing.T) {
	js := watchScript(t)
	m := regexp.MustCompile(`RELOAD_GAP = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no RELOAD_GAP")
	}
	gap, _ := strconv.Atoi(m[1])
	if gap < 20000 {
		t.Errorf("the reload gap is %d ms, under 20 seconds", gap)
	}
	// The wait is what is left of the page's age, from its load, and
	// the reload is made by that timer alone.
	if !strings.Contains(js, "Math.max(0, loadedAt + RELOAD_GAP - Date.now())") {
		t.Error("the reload is not put off until the page's read is RELOAD_GAP old")
	}
	if n := strings.Count(js, "location.reload()"); n != 2 { // the timer's, and the banner's button
		t.Errorf("location.reload() appears %d times", n)
	}
	// The limit is stated, as a limit, in both documents, with the reads'
	// room and the owner's taps.
	for path, allowance := range map[string]string{
		"../../docs/design/seat-gate.md": "the reads have 12 calls in 80",
		"../../docs/seat-gate.md":        "the seat's reads share 12 calls in 80",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc := string(b)
		for _, want := range []string{"every 20 seconds", allowance} {
			if !strings.Contains(doc, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if !strings.Contains(doc, "several card lists open") && !strings.Contains(doc, "three card lists open") {
			t.Errorf("%s does not state the limit of several open lists", path)
		}
		if strings.Contains(doc, "every 10 seconds") || strings.Contains(doc, "its 10 seconds before") {
			t.Errorf("%s still says 10 seconds for the list's reload", path)
		}
	}
}
