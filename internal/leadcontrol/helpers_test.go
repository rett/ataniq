//go:build linux && ataniq_canary

package leadcontrol

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadrole"
)

// clock is a fake clock that moves one second at every reading.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 123456789, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func mustRole(t testing.TB, s string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func digestOf(s string) [sha256.Size]byte { return sha256.Sum256([]byte(s)) }

// bind returns a binding of client "c1" for a start of the role scratch-alpha.
func bind(t testing.TB, id string) Binding {
	t.Helper()
	return Binding{
		Client: "c1", ID: id, Op: "lead.start", Role: mustRole(t, "scratch-alpha/lead"),
		Digest: digestOf("payload " + id),
	}
}

func tablePath(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "lead-commands.jsonl")
}

func openAt(t testing.TB, path string, o Options) *Table {
	t.Helper()
	if o.Now == nil {
		o.Now = newClock().now
	}
	tb, err := Open(path, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = tb.Close() })
	return tb
}

func admitFresh(t testing.TB, tb *Table, b Binding) Receipt {
	t.Helper()
	r, fresh, err := tb.Admit(b)
	if err != nil || !fresh {
		t.Fatalf("Admit(%s) = fresh %v, err %v; want fresh and no error", b.ID, fresh, err)
	}
	return r
}

func finish(t testing.TB, tb *Table, id string, st Status) Receipt {
	t.Helper()
	r, err := tb.Finish(id, st, "", "", nil)
	if err != nil {
		t.Fatalf("Finish(%s): %v", id, err)
	}
	return r
}

func fileLines(t testing.TB, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	if !strings.HasSuffix(s, "\n") {
		t.Fatalf("the file does not end with a newline: %q", s)
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sameReceipt(t testing.TB, got, want Receipt) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("receipt\n got  %s\n want %s", g, w)
	}
}

func isErr(t testing.TB, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
