package presence

import (
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultWindow is how long after his last activity the owner counts as
	// present, unless the window is set otherwise.
	DefaultWindow = 10 * time.Minute

	// MaxWindow is the longest window that can be set.
	MaxWindow = 30 * time.Minute

	// BeatInterval is the shortest time between two presence beats.
	BeatInterval = time.Minute

	// SurfaceConsole is the surface name of the terminal console.
	SurfaceConsole = "console"

	// maxSurfaceLen bounds a surface name, which is written to the journal.
	maxSurfaceLen = 32
)

// ErrWindow is the error, found with errors.Is, of a window that is refused:
// zero, negative, or longer than [MaxWindow].
var ErrWindow = errors.New("presence: window refused")

// ErrSurface is the error, found with errors.Is, of a surface name that is
// not in the form [ValidSurface] accepts.
var ErrSurface = errors.New("presence: surface name refused")

// ErrSession is the error, found with errors.Is, of a session id that is not
// in the form [ValidSession] accepts.
var ErrSession = errors.New("presence: session id refused")

// CheckWindow says whether d may be used as the presence window. It returns
// nil for a duration above zero and at most [MaxWindow], and otherwise an
// error that wraps [ErrWindow] and says why.
func CheckWindow(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%w: %v is not above zero", ErrWindow, d)
	}
	if d > MaxWindow {
		return fmt.Errorf("%w: %v is longer than %v", ErrWindow, d, MaxWindow)
	}
	return nil
}

// ValidSurface reports whether s is a surface name: 1 to 32 characters, each
// a lower-case letter, a digit or a hyphen. [SurfaceConsole] is one.
func ValidSurface(s string) bool {
	if len(s) == 0 || len(s) > maxSurfaceLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Stamp holds the fields the journal keeps about a moment of presence: with
// each decision, and with each change of state.
type Stamp struct {
	// At is the time of the moment itself.
	At time.Time
	// Surface is where it happened, for example [SurfaceConsole].
	Surface string
	// Session is the id of the run of that surface (see [NewSession]).
	Session string
	// LastActivity is the last activity before the moment. It is the zero
	// time when there was none, as for the first arrival of a run.
	LastActivity time.Time
}

// Kind names a change of state.
type Kind int

const (
	// Arrived is the change from away to present: the first activity after
	// an away stretch, or the first activity of a run.
	Arrived Kind = iota + 1
	// Left is the change from present to away: the window ran out with no
	// new activity.
	Left
)

// String returns "arrived" or "left".
func (k Kind) String() string {
	switch k {
	case Arrived:
		return "arrived"
	case Left:
		return "left"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Change is one change of state with the fields the journal keeps for it.
//
// For [Arrived], At is the time of the activity that brought him back and
// LastActivity is the activity before the away stretch. For [Left], At is the
// end of the window (or, when the clock was set back, the time at which that
// was seen) and LastActivity is the activity the window was counted from.
type Change struct {
	Kind Kind
	Stamp
}

// Event is the answer to one [Tracker.Activity] call.
type Event struct {
	// Arrival is true when this activity is the first after an away
	// stretch, or the first of the run. It is true for that one activity
	// only. The activity itself is not consumed: the caller delivers it
	// where it was aimed.
	Arrival bool
	// BeatDue is true when a presence beat should go out now. It is true at
	// most once in any [BeatInterval] of activity.
	BeatDue bool
	// Changes lists the changes of state this activity caused, in order.
	// It is empty unless Arrival is true. An arrival that follows a stretch
	// whose start [Tracker.Tick] never reported lists that [Left] first, so
	// that the changes a tracker reports always alternate.
	Changes []Change
}

// Away is the refusal of a decision: there was no activity inside the window
// before it. It is this package's typed "no"; a caller finds it with
// errors.As and maps it to the code "owner-away". Its Stamp is what the
// journal keeps with the refused decision.
type Away struct {
	Stamp
	// Window is the window that was in force.
	Window time.Duration
}

// Error says that the owner is away, with the time of his last activity when
// there was one.
func (a *Away) Error() string {
	if a.LastActivity.IsZero() {
		return "presence: the owner is away: no activity yet in this session"
	}
	return fmt.Sprintf("presence: the owner is away: no activity within %v (last at %s)",
		a.Window, a.LastActivity.UTC().Format(time.RFC3339))
}

// Tracker follows the presence of the owner at one surface in one session.
// Make one with [New]. It is not safe for use from several goroutines.
type Tracker struct {
	window  time.Duration
	surface string
	session string

	// seen is true once there was any activity; last is its time. The flag
	// is separate so that an activity at the zero time still counts.
	seen bool
	last time.Time

	// present is the state the caller was last told of: true from an
	// arrival until the Left that ends it is reported.
	present bool

	// beat is true once a beat was due; lastBeat is the time of the latest.
	beat     bool
	lastBeat time.Time
}

// New returns a tracker for the given surface and session with the given
// window. It refuses a window that [CheckWindow] refuses (there is no "zero
// means default": pass [DefaultWindow]), a surface that is not a
// [ValidSurface], and a session that is not a [ValidSession]. A new tracker
// has seen no activity, so the owner is away until the first one.
func New(window time.Duration, surface, session string) (*Tracker, error) {
	if err := CheckWindow(window); err != nil {
		return nil, err
	}
	if !ValidSurface(surface) {
		return nil, fmt.Errorf("%w: want 1 to %d lower-case letters, digits or hyphens", ErrSurface, maxSurfaceLen)
	}
	if !ValidSession(session) {
		return nil, fmt.Errorf("%w: want %d lower-case hexadecimal digits", ErrSession, sessionLen)
	}
	return &Tracker{window: window, surface: surface, session: session}, nil
}

// Window returns the window in force.
func (t *Tracker) Window() time.Duration { return t.window }

// Surface returns the surface the tracker was made for.
func (t *Tracker) Surface() string { return t.surface }

// Session returns the session id the tracker was made for.
func (t *Tracker) Session() string { return t.session }

// SetWindow changes the window. It refuses what [CheckWindow] refuses and
// then changes nothing. The new window counts from the last activity, so a
// shorter one can make the owner away at once; [Tracker.Tick] then reports
// it.
func (t *Tracker) SetWindow(d time.Duration) error {
	if err := CheckWindow(d); err != nil {
		return err
	}
	t.window = d
	return nil
}

// LastActivity returns the time of the last activity, and false when there
// was none yet.
func (t *Tracker) LastActivity() (time.Time, bool) {
	return t.last, t.seen
}

// Present reports whether the owner is present at the given time: there was
// an activity, it is not later than at, and at is no more than the window
// after it. A time before the last activity is never present, so a clock
// that was set back does not make him present. Present changes nothing.
func (t *Tracker) Present(at time.Time) bool {
	if !t.seen {
		return false
	}
	since := at.Sub(t.last)
	return since >= 0 && since <= t.window
}

// stamp returns the journal fields for a moment at the given time, with the
// last activity the tracker holds now. The times carry no monotonic clock
// reading, so they can be stored and compared as they are.
func (t *Tracker) stamp(at time.Time) Stamp {
	s := Stamp{At: at.Round(0), Surface: t.surface, Session: t.session}
	if t.seen {
		s.LastActivity = t.last.Round(0)
	}
	return s
}

// leave reports the pending change to away, if there is one: the caller was
// told the owner is present, and at the given time he is not.
func (t *Tracker) leave(at time.Time) (Change, bool) {
	if !t.present || t.Present(at) {
		return Change{}, false
	}
	t.present = false
	// The stretch began when the window ran out. When the clock was set
	// back that moment is in the clock's future, and the only honest time
	// is the one at which the tracker saw it.
	when := t.last.Add(t.window)
	if at.Before(t.last) {
		when = at
	}
	return Change{Kind: Left, Stamp: t.stamp(when)}, true
}

// Activity records an action of the owner at the given time and says what it
// means. When he was not present just before it, the action is an arrival
// and the event carries the change; that holds for the first action of a run
// too. An arrival is reported for that one action only: the next action
// inside the window is ordinary activity.
//
// The tracker does not take the action. The caller delivers the key or the
// pointer action where it was aimed, whatever the event says.
//
// A time earlier than the last activity is taken as the clock having been
// set back: the action is an arrival, the last activity becomes the new
// time, and a beat is due.
func (t *Tracker) Activity(at time.Time) Event {
	var ev Event
	if !t.Present(at) {
		if c, ok := t.leave(at); ok {
			ev.Changes = append(ev.Changes, c)
		}
		ev.Arrival = true
		ev.Changes = append(ev.Changes, Change{Kind: Arrived, Stamp: t.stamp(at)})
		t.present = true
	}
	t.seen = true
	t.last = at

	// A beat is due when none went out yet, when the latest is at least one
	// interval old, or when the clock was set back past it: its age cannot
	// be known then, and a backend that hears nothing would call him away.
	if since := at.Sub(t.lastBeat); !t.beat || since >= BeatInterval || since < 0 {
		ev.BeatDue = true
		t.beat = true
		t.lastBeat = at
	}
	return ev
}

// Tick lets the tracker notice that time has passed with no activity. It
// returns the change to away and true the first time it is called after the
// window ran out, and false every other time. A caller that never calls Tick
// loses nothing: the next [Tracker.Activity] reports the same change before
// its arrival.
func (t *Tracker) Tick(at time.Time) (Change, bool) {
	return t.leave(at)
}

// MayDecide says whether a decision may be accepted at the given time. When
// the owner is present it returns the fields the journal keeps with the
// decision: the time, the surface, the session and the last activity before
// it. When he is not, it returns an [*Away] carrying the same fields, to be
// journaled with the refusal.
//
// A decision is not an activity: MayDecide changes nothing, and the action
// that sent the decision is recorded through [Tracker.Activity] like any
// other.
func (t *Tracker) MayDecide(at time.Time) (Stamp, error) {
	s := t.stamp(at)
	if !t.Present(at) {
		return Stamp{}, &Away{Stamp: s, Window: t.window}
	}
	return s, nil
}

// Newer reports whether something that happened at the given time is newer
// than the "since he last looked" mark, and so belongs in what he has not
// seen. A zero mark means he never looked: everything is newer. Something at
// exactly the mark's time was there when he looked and is not newer.
func Newer(mark, at time.Time) bool {
	if mark.IsZero() {
		return true
	}
	return at.After(mark)
}

// Advance returns the mark to store after the owner looked at the given
// time. The mark never moves back: a look earlier than the mark (a clock set
// back) leaves it where it is, so that nothing he has seen is shown as new
// again. The value returned carries no monotonic clock reading, so it can be
// stored and compared as it is.
func Advance(mark, looked time.Time) time.Time {
	if !mark.IsZero() && !looked.After(mark) {
		return mark.Round(0)
	}
	return looked.Round(0)
}
