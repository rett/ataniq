//go:build linux

package session

import (
	"bytes"
	"fmt"
	"testing"

	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The holder's pane keeps the ring of recent output by default, and the
// bound is a parameter of the configuration.
func TestCF04RecentRowsOfTheHoldersPane(t *testing.T) {
	const size = 60000
	stream := fakeagent.FloodBytes(1, size)
	reference := func(rows int) sessionpane.Recent {
		ref := sessionpane.New(100, 30, sessionpane.Options{RecentRows: rows})
		defer ref.Close()
		ref.Write(stream)
		return ref.Recent()
	}
	recent := func(t *testing.T, mod func(*Config)) sessionpane.Recent {
		e := newEnv(t)
		s := e.start(fmt.Sprintf("raw; flood 0 %d 1", size), mod)
		waitDone(t, s)
		var r sessionpane.Recent
		s.Screen(func(p *sessionpane.Pane, _ uint64) { r = p.Recent() })
		return r
	}
	same := func(t *testing.T, got, want sessionpane.Recent) {
		t.Helper()
		if got.Oldest != want.Oldest || got.Next != want.Next || len(got.Entries) != len(want.Entries) {
			t.Fatalf("ring oldest %d next %d entries %d; want %d, %d, %d", got.Oldest, got.Next, len(got.Entries), want.Oldest, want.Next, len(want.Entries))
		}
		for i := range got.Entries {
			if !bytes.Equal(got.Entries[i].Row, want.Entries[i].Row) || got.Entries[i].Omitted != want.Entries[i].Omitted {
				t.Fatalf("entry %d differs", i)
			}
		}
	}
	t.Run("default", func(t *testing.T) {
		want := reference(sessionpane.DefaultRecentRows)
		if want.Next == 0 {
			t.Fatal("the flood scrolled no row: this test needs updating")
		}
		same(t, recent(t, func(*Config) {}), want)
	})
	t.Run("configured", func(t *testing.T) {
		want := reference(25)
		got := recent(t, func(c *Config) { c.RecentRows = 25 })
		if len(want.Entries) != 25 {
			t.Fatalf("the reference kept %d rows", len(want.Entries))
		}
		same(t, got, want)
	})
	t.Run("none", func(t *testing.T) {
		if r := recent(t, func(c *Config) { c.RecentRows = -1 }); r.Next != 0 || len(r.Entries) != 0 {
			t.Fatalf("a holder with the ring off kept %+v", r)
		}
	})
}
