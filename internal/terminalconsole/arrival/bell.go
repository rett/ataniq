package arrival

import (
	tea "charm.land/bubbletea/v2"
)

const (
	// bellMemory is how many cards a Bell remembers. A day's cards are far
	// fewer; the bound only keeps a Bell that runs for months small.
	bellMemory = 1024

	// maxCardID is the longest card id a Bell remembers. A longer id is not
	// an id the host made, and no bell rings for it.
	maxCardID = 128
)

// Bell decides whether the terminal's bell rings for a card that has just
// arrived. It rings once for a card that arrives while the owner is not
// present (away, or quiet for longer than the presence window), never while
// he is present, never twice for one card, and never while the setting is
// off.
//
// Bell writes nothing. [Bell.CardArrived] returns the decision and the
// command, and the cockpit sends the command. A Bell is used from one
// goroutine, as Bubble Tea uses a model.
type Bell struct {
	on bool
	// seen holds the ids of the cards that arrived, and order the same ids
	// from the oldest on, so that the oldest is forgotten when the memory
	// is full.
	seen  map[string]struct{}
	order []string
	next  int
}

// NewBell returns a bell with the setting on or off. The console's default
// is on.
func NewBell(on bool) *Bell {
	return &Bell{on: on, seen: map[string]struct{}{}}
}

// On reports the setting.
func (b *Bell) On() bool { return b.on }

// SetOn changes the setting. Turning the bell on rings for no card that
// arrived while it was off.
func (b *Bell) SetOn(on bool) { b.on = on }

// CardArrived is called once when a card arrives, with the card's id and
// whether the owner is present at that moment (presence's Tracker.Present).
// It returns the command that rings the bell and true when the bell should
// ring, and nil and false otherwise.
//
// The card is remembered whatever the answer is, so the bell never rings
// for it later: not when it is reported again, not when the owner has gone
// away since, and not when the setting was turned on since. A Bell
// remembers the last 1024 cards. No bell rings for an empty id or one
// longer than 128 bytes, since such a card could not be told from another.
func (b *Bell) CardArrived(id string, present bool) (tea.Cmd, bool) {
	if id == "" || len(id) > maxCardID {
		return nil, false
	}
	if _, ok := b.seen[id]; ok {
		return nil, false
	}
	b.remember(id)
	if !b.on || present {
		return nil, false
	}
	return Ring(), true
}

// remember keeps id, and forgets the oldest id when the memory is full.
func (b *Bell) remember(id string) {
	if len(b.order) < bellMemory {
		b.order = append(b.order, id)
	} else {
		delete(b.seen, b.order[b.next])
		b.order[b.next] = id
		b.next = (b.next + 1) % bellMemory
	}
	b.seen[id] = struct{}{}
}

// Ring returns the command that rings the terminal's bell once: it writes
// the one byte BEL to the outer terminal.
func Ring() tea.Cmd { return tea.Raw("\a") }
