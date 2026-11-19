package arrival

import (
	"image/color"
	"strconv"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/approval/presence"
)

// Colors are the colours the strip draws with. The cockpit fills them from
// its theme, so that the strip has no palette of its own. A nil colour is
// the terminal's default.
type Colors struct {
	// Fg and Bg are the colours of the strip's lines and of its region.
	Fg, Bg color.Color
	// Dim is the colour of the key marks and of the hint in the title row.
	Dim color.Color
	// Warn is the colour of the line of alerts and stops.
	Warn color.Color
}

// Summary is what the strip says: what happened while the owner was away.
// The cockpit fills it from what it holds; every field is a count, an age or
// a line the host wrote. No field is for text a lead wrote.
type Summary struct {
	// Alerts are the alerts and stops, one line each, the most pressing
	// first. The strip shows how many there are and the first.
	Alerts []string
	// LimitReached is the number of projects whose day reached the daily
	// card limit. Such a day is an alert: it counts as one, with words of
	// the strip's own.
	LimitReached int
	// Waiting is the number of cards that wait for him, and OldestAge the
	// age of the oldest (see [Waiting]).
	Waiting   int
	OldestAge time.Duration
	// ByGrant and ByStanding count what was approved without him since the
	// mark he last looked at (presence's Newer and Advance keep that mark):
	// by a grant, and by a standing approval.
	ByGrant    int
	ByStanding int
	// Held are the notices that were held for his return, one line each,
	// the oldest first. The strip shows how many there are and the first.
	Held []string
}

// Empty reports whether the summary has nothing to say. No strip is shown
// for an empty summary. A count below zero counts as zero.
func (s Summary) Empty() bool {
	return len(s.Alerts) == 0 && s.LimitReached <= 0 && s.Waiting <= 0 &&
		s.ByGrant <= 0 && s.ByStanding <= 0 && len(s.Held) == 0
}

// Line names one line of the strip, for the cockpit to open.
type Line int

const (
	// NoLine is no line.
	NoLine Line = iota
	// LineAlerts is the line of alerts and stops.
	LineAlerts
	// LineWaiting is the line of the cards that wait.
	LineWaiting
	// LineApproved is the line of what was approved without him.
	LineApproved
	// LineHeld is the line of the held notices.
	LineHeld
)

// String returns "alerts", "waiting", "approved" or "held".
func (l Line) String() string {
	switch l {
	case NoLine:
		return "none"
	case LineAlerts:
		return "alerts"
	case LineWaiting:
		return "waiting"
	case LineApproved:
		return "approved"
	case LineHeld:
		return "held"
	}
	return "Line(" + strconv.Itoa(int(l)) + ")"
}

// Action is what a key meant to the strip.
type Action int

const (
	// None says the key is not the strip's. The strip did nothing with it,
	// and the cockpit delivers it where it was aimed.
	None Action = iota
	// Close says Esc closed the strip. The key is used up.
	Close
	// Open says a digit opened one of the strip's lines, which closed the
	// strip. The key is used up, and the cockpit opens the view of the
	// line returned with it.
	Open
)

// maxText is how many bytes of a line it is given the strip keeps. A line of
// the host is shorter; the bound is for a line that is not.
const maxText = 160

// row is one line of the strip as it is drawn: which line it is, its text,
// and whether it takes the alert colour. The text is the strip's own words
// and numbers, with at most maxText bytes of a line that was given.
type row struct {
	line  Line
	text  string
	alert bool
}

// Strip is the arrival summary. Make one with [NewStrip]. The cockpit hands
// it every arrival ([Strip.Arrive]), draws it while it is [Strip.Visible]
// into [Strip.Rows] rows ([Strip.Draw]), and asks it about each key press
// ([Strip.Key]). A Strip is used from one goroutine, as Bubble Tea uses a
// model. It returns no command and never ends the program.
type Strip struct {
	colors  Colors
	visible bool
	rows    []row
	// drawn is how many of rows the last Draw showed. Only those can be
	// opened: no key opens a line he was not shown.
	drawn int
}

// NewStrip returns a strip that shows nothing yet.
func NewStrip(c Colors) *Strip { return &Strip{colors: c} }

// SetColors changes the colours, for a change of theme.
func (s *Strip) SetColors(c Colors) { s.colors = c }

// Arrive is called with the answer of presence's Tracker.Activity for every
// action of the owner, and with the summary as it is now. When the event is
// an arrival and the summary is not empty, the strip is shown with it, and
// Arrive returns true. An arrival with an empty summary shows nothing and
// takes away a strip left from an earlier arrival. An event that is no
// arrival changes nothing, so the strip appears once per arrival: after it
// was closed, only the next arrival shows it again.
//
// Arrive takes no key. The action that was the arrival is delivered by the
// cockpit where it was aimed, and [Strip.Key] refuses it.
func (s *Strip) Arrive(ev presence.Event, sum Summary) bool {
	if !ev.Arrival {
		return false
	}
	s.rows, s.drawn = rowsOf(sum), 0
	s.visible = len(s.rows) > 0
	return s.visible
}

// Refresh gives a strip that is showing the summary as it is now, so that
// its numbers stay true while it shows. A summary that became empty closes
// it. Refresh never shows a strip: when none is showing it does nothing.
func (s *Strip) Refresh(sum Summary) {
	if !s.visible {
		return
	}
	s.rows = rowsOf(sum)
	s.drawn = min(s.drawn, len(s.rows))
	s.visible = len(s.rows) > 0
}

// Visible reports whether the strip is showing.
func (s *Strip) Visible() bool { return s.visible }

// Rows returns how many rows the strip asks for: its title row and one row
// for each line, at most five. It is zero while the strip is not showing.
func (s *Strip) Rows() int {
	if !s.visible {
		return 0
	}
	return 1 + len(s.rows)
}

// Dismiss closes the strip without a key, for a cockpit that opened one of
// its views another way. Only the next arrival shows the strip again.
func (s *Strip) Dismiss() { s.visible = false }

// Key says what a key press means to the strip. The cockpit passes the key,
// the answer presence's Tracker.Activity gave for this very key, and
// whether the strip has the focus.
//
// The answer is [None], and the strip does nothing, unless all of this
// holds: the strip is showing; the cockpit says it has the focus; the key is
// not the one that was the arrival (the key that woke the console goes
// where it was aimed); and the key is Esc or the digit of a line the last
// [Strip.Draw] showed. Esc gives [Close]. A digit gives [Open] and the
// line; the strip closes then too. No other key is the strip's: not Enter,
// not a letter, not a digit with a modifier.
//
// The cockpit says the strip has the focus only in console focus, and never
// while the session pane, an approvals view (the list, a card, the limit's
// screen), an overlay or a field (the confirm field, the deny reason) has
// the keys: the limit's screen takes digits, the confirm code holds 3 and 4,
// and Esc goes back in every view. It dismisses the strip when an approvals
// view opens. No key of the strip decides anything; opening a line only
// shows a view.
func (s *Strip) Key(msg tea.KeyPressMsg, ev presence.Event, focused bool) (Action, Line) {
	if !s.visible || !focused || ev.Arrival {
		return None, NoLine
	}
	key := msg.String()
	if key == "esc" {
		s.visible = false
		return Close, NoLine
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		if i := int(key[0] - '1'); i < s.drawn && i < len(s.rows) {
			s.visible = false
			return Open, s.rows[i].line
		}
	}
	return None, NoLine
}

// The words of the strip. They are its own: no text that was given stands
// in them.
const (
	title       = "while you were away"
	hintFocused = "a digit opens its line · Esc closes"
	sep         = " · "
)

// rowsOf returns the lines a summary gives, in the strip's order: alerts and
// stops, the cards that wait, what was approved without him, held notices.
// A part with nothing to say gives no line.
func rowsOf(sum Summary) []row {
	var rows []row
	limit := max(0, sum.LimitReached)
	if n := len(sum.Alerts) + min(limit, 1); n > 0 {
		text := "alerts " + strconv.Itoa(n)
		if limit > 0 {
			text += sep + "the daily card limit is reached for " + count(limit, "project", "projects")
		}
		if len(sum.Alerts) > 0 {
			text += sep + clip(sum.Alerts[0])
		}
		rows = append(rows, row{LineAlerts, text, true})
	}
	if sum.Waiting > 0 {
		rows = append(rows, row{LineWaiting,
			"approvals waiting " + strconv.Itoa(sum.Waiting) + sep + "oldest " + Age(sum.OldestAge), false})
	}
	grant, standing := max(0, sum.ByGrant), max(0, sum.ByStanding)
	if grant > 0 || standing > 0 {
		// A sum that would overflow is shown as the largest number.
		total := grant + standing
		if total < grant {
			total = int(^uint(0) >> 1)
		}
		text := "approved without you " + strconv.Itoa(total) + sep
		if grant > 0 {
			text += strconv.Itoa(grant) + " by grant"
		}
		if grant > 0 && standing > 0 {
			text += ", "
		}
		if standing > 0 {
			text += strconv.Itoa(standing) + " by standing approval"
		}
		rows = append(rows, row{LineApproved, text, false})
	}
	if n := len(sum.Held); n > 0 {
		rows = append(rows, row{LineHeld, "held notices " + strconv.Itoa(n) + sep + clip(sum.Held[0]), false})
	}
	return rows
}

// count returns "1 project" or "3 projects".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// clip returns the beginning of s, at most maxText bytes, cut before a
// character and not inside one. The strip keeps no more of a line it is
// given. The copy shares nothing with s, so a long s is not held.
func clip(s string) string {
	if len(s) > maxText {
		n := maxText
		for n > maxText-utf8.UTFMax && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return string([]byte(s))
}
