package arrival

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/approval/presence"
)

// rings fails the test unless cmd is the command that writes one BEL.
func rings(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("the command gives %T, want a tea.RawMsg", cmd())
	}
	if raw.Msg != "\a" {
		t.Fatalf("the command writes %q, want one BEL", raw.Msg)
	}
}

func TestBellRingsOnceForACardThatArrivesWhileHeIsAway(t *testing.T) {
	b := NewBell(true)
	cmd, ring := b.CardArrived("card-1", false)
	if !ring {
		t.Fatal("no bell for a card that arrived while he was away")
	}
	rings(t, cmd)
	for i := 0; i < 3; i++ {
		if cmd, ring := b.CardArrived("card-1", false); ring || cmd != nil {
			t.Fatal("the bell rang twice for one card")
		}
	}
	cmd, ring = b.CardArrived("card-2", false)
	if !ring {
		t.Fatal("no bell for a second card")
	}
	rings(t, cmd)
}

func TestBellNeverRingsWhileHeIsPresent(t *testing.T) {
	b := NewBell(true)
	if cmd, ring := b.CardArrived("card-1", true); ring || cmd != nil {
		t.Fatal("the bell rang while he was present")
	}
	// He has gone away since. The card arrived once, and it did not ring.
	if cmd, ring := b.CardArrived("card-1", false); ring || cmd != nil {
		t.Fatal("the bell rang later for a card that arrived while he was present")
	}
}

func TestBellNeverRingsWhenTheSettingIsOff(t *testing.T) {
	b := NewBell(false)
	if b.On() {
		t.Fatal("On() is true for a bell made off")
	}
	if cmd, ring := b.CardArrived("card-1", false); ring || cmd != nil {
		t.Fatal("the bell rang with the setting off")
	}
	b.SetOn(true)
	if !b.On() {
		t.Fatal("On() is false after SetOn(true)")
	}
	if _, ring := b.CardArrived("card-1", false); ring {
		t.Fatal("turning the bell on rang for a card that arrived while it was off")
	}
	if _, ring := b.CardArrived("card-2", false); !ring {
		t.Fatal("no bell for a new card after the setting was turned on")
	}
	b.SetOn(false)
	if _, ring := b.CardArrived("card-3", false); ring {
		t.Fatal("the bell rang after the setting was turned off")
	}
}

func TestBellRefusesAnIDItCannotRemember(t *testing.T) {
	b := NewBell(true)
	for _, id := range []string{"", strings.Repeat("a", maxCardID+1), strings.Repeat("\x1b", 1<<20)} {
		for i := 0; i < 2; i++ {
			if cmd, ring := b.CardArrived(id, false); ring || cmd != nil {
				t.Fatalf("the bell rang for an id of %d bytes", len(id))
			}
		}
	}
	if len(b.seen) != 0 || len(b.order) != 0 {
		t.Fatal("a refused id was remembered")
	}
	if _, ring := b.CardArrived(strings.Repeat("a", maxCardID), false); !ring {
		t.Fatal("no bell for an id of the greatest length")
	}
}

func TestBellMemoryIsBounded(t *testing.T) {
	b := NewBell(true)
	for i := 0; i < 3*bellMemory; i++ {
		if _, ring := b.CardArrived("card-"+strconv.Itoa(i), false); !ring {
			t.Fatalf("no bell for card %d", i)
		}
		if len(b.seen) > bellMemory || len(b.order) > bellMemory {
			t.Fatalf("after %d cards the bell holds %d ids", i+1, len(b.seen))
		}
	}
	if len(b.seen) != bellMemory {
		t.Fatalf("the bell holds %d ids, want %d", len(b.seen), bellMemory)
	}
	// The newest cards are all remembered.
	for i := 2 * bellMemory; i < 3*bellMemory; i++ {
		if _, ring := b.CardArrived("card-"+strconv.Itoa(i), false); ring {
			t.Fatalf("the bell rang twice for card %d", i)
		}
	}
}

// TestBellFollowsATracker drives the bell as the cockpit does: the presence
// state comes from a tracker of package presence.
func TestBellFollowsATracker(t *testing.T) {
	tr, err := presence.New(presence.DefaultWindow, presence.SurfaceConsole, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBell(true)
	// No activity yet: he is away.
	if _, ring := b.CardArrived("card-1", tr.Present(t0)); !ring {
		t.Fatal("no bell before the first activity")
	}
	tr.Activity(t0.Add(time.Minute))
	if _, ring := b.CardArrived("card-2", tr.Present(t0.Add(2*time.Minute))); ring {
		t.Fatal("the bell rang while he was present")
	}
	// Quiet for longer than the window.
	quiet := t0.Add(time.Minute + presence.DefaultWindow + time.Second)
	if _, ring := b.CardArrived("card-3", tr.Present(quiet)); !ring {
		t.Fatal("no bell for a card that arrived after he went quiet")
	}
	if _, ring := b.CardArrived("card-3", tr.Present(quiet.Add(time.Hour))); ring {
		t.Fatal("the bell rang twice for one card")
	}
}
