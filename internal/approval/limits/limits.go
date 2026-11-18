package limits

import (
	"fmt"
	"sort"
	"time"
)

const (
	// Default is the limit of a project until the owner sets one.
	Default = 12
	// Max is the highest limit. Code refuses any number above it.
	Max = 40
	// Min is the lowest limit.
	Min = 1
	// SuggestionMin is the lowest number Suggest gives.
	SuggestionMin = 6
	// WindowDays is how many calendar days before today the suggestion and
	// a View look at. Today is not one of them.
	WindowDays = 14
)

// Day is one calendar day of one project. Date is YYYY-MM-DD in the clock's
// location. Uses is the number of card uses on that day. ReachedLimit says
// that the uses reached the limit that was in force, and Alert that the day
// had an alert; either one leaves the day out of the suggestion.
type Day struct {
	Date         string
	Uses         int
	ReachedLimit bool
	Alert        bool
}

// LeftOut reports whether the day is left out of the suggestion.
func (d Day) LeftOut() bool { return d.ReachedLimit || d.Alert }

// View is what a screen shows of one project's limit: the present number, the
// 14 calendar days before today (oldest first, a day with nothing recorded
// shown with zero uses), the suggestion made from them, and whether the limit
// was already raised today.
type View struct {
	Project     string
	Limit       int
	Days        []Day
	Suggestion  int
	RaisedToday bool
}

// Change is a request to set the limit to To. Typed says that the owner typed
// the number; false means it was taken by one key.
type Change struct {
	To    int
	Typed bool
}

// MaxRaise returns the highest number the limit may be raised to from
// present in one raise: present times three, divided by two and rounded down,
// and at most Max. When the result equals present, present cannot be raised.
func MaxRaise(present int) int {
	if present < Min {
		return present
	}
	if present >= Max {
		return Max
	}
	top := present * 3 / 2
	if top > Max {
		top = Max
	}
	return top
}

// Check says whether the limit may go from present to c.To. raisedToday is
// what RaisedToday answered for the project. It returns nil for yes and a
// *Refusal for no.
//
// A number no higher than the present one is always taken, typed or not, and
// also on a day that had a raise: lowering is never refused. Setting the
// limit to the number it already has is taken as well and changes nothing.
func Check(present int, raisedToday bool, c Change) error {
	if present < Min || present > Max {
		return &Refusal{BadLimit, fmt.Sprintf("the present limit, %d, is not between %d and %d", present, Min, Max)}
	}
	if c.To < Min {
		return &Refusal{UnderMin, fmt.Sprintf("the limit cannot be under %d", Min)}
	}
	if c.To > Max {
		return &Refusal{AboveMax, fmt.Sprintf("the limit cannot be above %d", Max)}
	}
	if c.To <= present {
		return nil
	}
	if !c.Typed {
		return &Refusal{NotTyped, "one key only lowers the limit: a higher number has to be typed"}
	}
	top := MaxRaise(present)
	if top == present {
		return &Refusal{CannotRaise, fmt.Sprintf("a limit of %d cannot be raised: a raise is by half at most, rounded down, and half of %d adds nothing", present, present)}
	}
	if c.To > top {
		return &Refusal{RaiseTooLarge, fmt.Sprintf("a raise is by half at most, rounded down: from %d the limit rises to %d at most", present, top)}
	}
	if raisedToday {
		return &Refusal{AlreadyRaised, "the limit was already raised today: one raise a day"}
	}
	return nil
}

// Apply checks c against the view with Check and returns the view as it is
// after the change: the new limit, and RaisedToday set when the change was a
// raise. The days and the suggestion stay as they were; a day's ReachedLimit
// tells what happened under the limit of that day. When the change is
// refused, the view comes back unchanged with the *Refusal.
func Apply(v View, c Change) (View, error) {
	if err := Check(v.Limit, v.RaisedToday, c); err != nil {
		return v, err
	}
	if c.To > v.Limit {
		v.RaisedToday = true
	}
	v.Limit = c.To
	return v, nil
}

// LowerWarning returns the line a screen shows, and asks once more about,
// before the limit is lowered to a number from which it can hardly rise
// again: 1 cannot be raised at all, and 2 only to 3. It returns the empty
// string for any other change, and for a change that does not lower.
func LowerWarning(present, to int) string {
	if to >= present || to < Min {
		return ""
	}
	top := MaxRaise(to)
	switch top - to {
	case 0:
		return fmt.Sprintf("a limit of %d cannot be raised again", to)
	case 1:
		return fmt.Sprintf("a limit of %d can be raised again only by one, to %d", to, top)
	}
	return ""
}

// window returns the WindowDays calendar days before today, oldest first,
// built from days. A date that occurs more than once is joined into one day:
// its uses are added and its marks are kept, so that a day with an alert
// cannot be counted through a second entry without one. A day with nothing
// recorded has zero uses. Entries outside the window, and entries whose date
// is not a real YYYY-MM-DD date, are dropped: they cannot be placed on a day.
func window(days []Day, now time.Time) []Day {
	today := todayNumber(now)
	out := make([]Day, WindowDays)
	for i := range out {
		out[i].Date = dateOf(today - int64(WindowDays-i))
	}
	for _, d := range days {
		n, ok := dayNumber(d.Date)
		if !ok {
			continue
		}
		back := today - n
		if back < 1 || back > WindowDays {
			continue
		}
		slot := &out[WindowDays-int(back)]
		if d.Uses > 0 {
			slot.Uses += d.Uses
			if slot.Uses < 0 {
				// The sum overflowed; the day is as busy as a count can say.
				slot.Uses = int(^uint(0) >> 1)
			}
		}
		slot.ReachedLimit = slot.ReachedLimit || d.ReachedLimit
		slot.Alert = slot.Alert || d.Alert
	}
	return out
}

// Suggest returns the number the screen suggests for a project: twice the
// uses of its busiest day among the WindowDays calendar days before today,
// never under SuggestionMin and never above Max. Today is the calendar day of
// now in its own location and is not counted. A day that reached the limit or
// had an alert is left out.
//
// days may be in any order and may hold days outside the window.
func Suggest(days []Day, now time.Time) int {
	return suggest(window(days, now))
}

func suggest(win []Day) int {
	busiest := 0
	for _, d := range win {
		if d.LeftOut() {
			continue
		}
		if d.Uses > busiest {
			busiest = d.Uses
		}
	}
	// A count above Max is clamped before it is doubled, so that no number
	// from outside can overflow.
	if busiest > Max {
		busiest = Max
	}
	n := busiest * 2
	if n < SuggestionMin {
		n = SuggestionMin
	}
	if n > Max {
		n = Max
	}
	return n
}

// NewView builds the view of one project. limit is its present limit (the
// caller hands in Default when the owner never set one), days are its
// recorded days in any order, lastRaise is the time of its last raise (the
// zero time when there was none) and now is the injected clock's reading.
//
// The view's Days are exactly the WindowDays calendar days before today,
// oldest first; these are the days the suggestion is made from, so the screen
// shows what the suggestion counted and what it left out. It returns a
// *Refusal of kind BadLimit when limit is outside Min to Max.
func NewView(project string, limit int, days []Day, lastRaise, now time.Time) (View, error) {
	if limit < Min || limit > Max {
		return View{}, &Refusal{BadLimit, fmt.Sprintf("the present limit, %d, is not between %d and %d", limit, Min, Max)}
	}
	win := window(days, now)
	return View{
		Project:     project,
		Limit:       limit,
		Days:        win,
		Suggestion:  suggest(win),
		RaisedToday: RaisedToday(lastRaise, now),
	}, nil
}

// DaysOf turns the decision times of a project's card uses into days, oldest
// first, each on its calendar day in the location of now. limitOn returns the
// limit that was in force on a date (YYYY-MM-DD), and each day's ReachedLimit
// is marked against the limit of that day. The present limit is not a
// stand-in for it: after a raise, a day that filled the old limit would no
// longer look full, and a run of forged cards on that day would push the
// suggestion up.
//
// A limitOn that is nil, or that answers with a number under Min for a date,
// marks the day as having reached its limit: a day whose limit is not known
// is left out of the suggestion. DaysOf never sets Alert; a caller that
// records alerts sets it on the days it gets back, or builds its days itself.
func DaysOf(decided []time.Time, limitOn func(date string) int, now time.Time) []Day {
	uses := map[string]int{}
	for _, t := range decided {
		uses[Date(t, now)]++
	}
	out := make([]Day, 0, len(uses))
	for date, n := range uses {
		// Without a limit the day counts as full: no use is under a limit
		// of zero.
		limit := 0
		if limitOn != nil {
			limit = limitOn(date)
		}
		out = append(out, Day{Date: date, Uses: n, ReachedLimit: Reached(n, limit)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}
