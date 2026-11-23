package seatapp

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// lines collects the lines of a Logf.
type lines struct {
	mu sync.Mutex
	s  []string
}

func (l *lines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s = append(l.s, strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}

func (l *lines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.s...)
}

// A request past the project pages' limit of a credential is told to the
// log in one line, then at most one more in 30 seconds with the count of
// those held back; the lines hold no credential.
func TestGenesisLimitIsLoggedOncePerThirtySeconds(t *testing.T) {
	var l lines
	g := &fakeGenesis{}
	r := newRig(t, withGenesis(g), func(c *Config) { c.Logf = l.logf })
	s := r.login(t)
	for i := 0; i < genesisRequests; i++ {
		if w := r.getWith("/projects/"+testThread, s); w.Code != 200 {
			t.Fatalf("page %d: %d", i, w.Code)
		}
	}
	if len(l.all()) != 0 {
		t.Fatalf("lines within the limit: %q", l.all())
	}
	if w := r.getWith("/projects/"+testThread, s); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a page past the bound: %d", w.Code)
	}
	want := "ataniq-seat: the project pages' limit of 30 requests a minute was reached for one credential"
	if got := l.all(); len(got) != 1 || got[0] != want {
		t.Fatalf("the first refusal: %q", got)
	}
	// More refusals within 30 seconds are counted, not told: a page, a
	// post, and the discard step.
	r.clock.Advance(10 * time.Second)
	r.getWith("/projects/"+testThread, s)
	r.postForm("/projects/"+testThread+"/finish", nil, s)
	r.getWith("/projects/"+testThread+"/discard", s)
	if got := l.all(); len(got) != 1 {
		t.Fatalf("lines within 30 seconds: %q", got)
	}
	// After 30 seconds the next refusal tells how many were held back. The
	// minute has not passed for the credential's requests (they were all at
	// the clock's start), so move to the 31st second only.
	r.clock.Advance(21 * time.Second)
	r.getWith("/projects/"+testThread, s)
	got := l.all()
	if len(got) != 2 || got[1] != want+"; 3 more since the last line" {
		t.Fatalf("the second line: %q", got)
	}
	for _, line := range got {
		if strings.Contains(line, s.Value) || strings.Contains(line, "0123456789abcdef") || strings.Contains(line, testThread) {
			t.Errorf("a line holds an id: %q", line)
		}
	}
}

// A nil Logf is no log, and a refusal is the same.
func TestGenesisLimitWithoutALogger(t *testing.T) {
	r, _, s := genesisRig(t)
	for i := 0; i < genesisRequests; i++ {
		r.getWith("/projects/"+testThread, s)
	}
	if w := r.getWith("/projects/"+testThread, s); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a page past the bound: %d", w.Code)
	}
}
