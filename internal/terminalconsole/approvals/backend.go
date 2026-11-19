package approvals

import (
	"image/color"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
)

// Backend is where the view reads requests from and sends decisions to. The
// sample backend and, later, the daemon's client satisfy it by their method
// set.
//
// A view calls a backend only from a command (tea.Cmd), so that a slow
// backend cannot stop the keys. Methods may be called from several
// goroutines at once.
type Backend interface {
	List() (card.Listing, error)
	Open(id string) (card.Card, error)
	Page(id string, kind card.PageKind, n int) (card.Page, error)
	Decide(d card.Decision) (card.Outcome, error)
	Decider() (ok bool, reason string)
	Limit(project string) (limits.View, error)
	SetLimit(project string, c limits.Change) (limits.View, error)
	Activity(at time.Time)
	Looked(at time.Time)
}

// Colors are the colours the view draws with. The parent fills them from its
// theme, so that the view has no palette of its own. A nil colour is the
// terminal's default.
type Colors struct {
	// Fg and Bg are the colours of the text and of the region.
	Fg, Bg color.Color
	// Dim is the colour of labels and key hints.
	Dim color.Color
	// Accent is the colour of headings and of the cursor.
	Accent color.Color
	// Warn is the colour of refusals, marks and the line that says why a
	// control is off.
	Warn color.Color
}

// Options configure a Model.
type Options struct {
	// Backend is where requests come from. It must not be nil.
	Backend Backend
	Colors  Colors
	// Now is the clock. It decides the first 400 ms of a card, the burst
	// rule of the confirm field and every time the view compares. Nil
	// means time.Now.
	Now func() time.Time
	// Random is the source of the confirm code. The parent gives it a
	// cryptographic reader. With none, or when it fails, no code can be
	// made and Approve is not offered, with that reason.
	Random io.Reader
	// After makes a command that sends a message once d has passed. Nil
	// means tea.Tick. A test gives its own clock.
	After func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
	// GraphemeWidths says that the outer terminal measures whole grapheme
	// clusters (mode 2027); the pager follows it.
	GraphemeWidths bool
}

// Kind says what the parent is asked to do after an Update.
type Kind int

const (
	// None asks for nothing.
	None Kind = iota
	// Close asks the parent to close the view: Esc was pressed in the list.
	Close
	// Quit asks the parent to quit: ctrl+c was pressed. A field that was
	// open has been dropped.
	Quit
	// Limit asks the parent to open the limit's screen for Action.Project.
	Limit
	// Lead asks the parent to open the lead that asked the card
	// Action.ID of project Action.Project.
	Lead
)

// Action is what Update returns. Project is set for Limit and Lead, and ID
// for Lead.
type Action struct {
	Kind    Kind
	Project string
	ID      string
}
