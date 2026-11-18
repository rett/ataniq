package presence

import (
	"errors"
	"testing"
	"time"
)

// t0 is the start of every test's time line. No test reads a clock.
var t0 = time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)

const testSession = "0123456789abcdef"

// at returns the time d after t0.
func at(d time.Duration) time.Time { return t0.Add(d) }

func newTracker(t *testing.T, window time.Duration) *Tracker {
	t.Helper()
	tr, err := New(window, SurfaceConsole, testSession)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestConstants(t *testing.T) {
	if DefaultWindow != 10*time.Minute {
		t.Errorf("DefaultWindow = %v, want 10m", DefaultWindow)
	}
	if MaxWindow != 30*time.Minute {
		t.Errorf("MaxWindow = %v, want 30m", MaxWindow)
	}
	if BeatInterval != time.Minute {
		t.Errorf("BeatInterval = %v, want 1m", BeatInterval)
	}
	if SurfaceConsole != "console" {
		t.Errorf("SurfaceConsole = %q, want console", SurfaceConsole)
	}
}

func TestWindowIsASettingWithBounds(t *testing.T) {
	cases := []struct {
		d  time.Duration
		ok bool
	}{
		{DefaultWindow, true},
		{time.Nanosecond, true},
		{MaxWindow, true},
		{MaxWindow + time.Nanosecond, false},
		{31 * time.Minute, false},
		{time.Hour, false},
		{0, false},
		{-time.Nanosecond, false},
		{-10 * time.Minute, false},
	}
	for _, c := range cases {
		err := CheckWindow(c.d)
		if (err == nil) != c.ok {
			t.Errorf("CheckWindow(%v) = %v, want ok %v", c.d, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrWindow) {
			t.Errorf("CheckWindow(%v) = %v, want an ErrWindow", c.d, err)
		}
		tr, err := New(c.d, SurfaceConsole, testSession)
		if (err == nil) != c.ok {
			t.Errorf("New(%v) error = %v, want ok %v", c.d, err, c.ok)
		}
		if err != nil && (tr != nil || !errors.Is(err, ErrWindow)) {
			t.Errorf("New(%v) = %v, %v, want nil and an ErrWindow", c.d, tr, err)
		}
		if err == nil && tr.Window() != c.d {
			t.Errorf("New(%v).Window() = %v", c.d, tr.Window())
		}
	}
}

func TestSetWindow(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(0))

	for _, d := range []time.Duration{0, -time.Second, MaxWindow + time.Second} {
		if err := tr.SetWindow(d); !errors.Is(err, ErrWindow) {
			t.Errorf("SetWindow(%v) = %v, want an ErrWindow", d, err)
		}
		if tr.Window() != DefaultWindow {
			t.Fatalf("a refused SetWindow(%v) changed the window to %v", d, tr.Window())
		}
	}

	if !tr.Present(at(10*time.Minute)) || tr.Present(at(11*time.Minute)) {
		t.Fatal("the default window is not 10 minutes")
	}
	if err := tr.SetWindow(MaxWindow); err != nil {
		t.Fatalf("SetWindow(MaxWindow): %v", err)
	}
	if !tr.Present(at(30*time.Minute)) || tr.Present(at(30*time.Minute+time.Second)) {
		t.Error("the longer window does not count from the last activity")
	}

	// A shorter window can make him away at once, and Tick reports it.
	if err := tr.SetWindow(time.Minute); err != nil {
		t.Fatalf("SetWindow(1m): %v", err)
	}
	if tr.Present(at(5 * time.Minute)) {
		t.Error("present 5 minutes after the last activity with a window of 1 minute")
	}
	c, ok := tr.Tick(at(5 * time.Minute))
	if !ok || c.Kind != Left || !c.At.Equal(at(time.Minute)) {
		t.Errorf("Tick after shortening = %+v, %v, want Left at the new window's end", c, ok)
	}
}

func TestNewRefusesSurfaceAndSession(t *testing.T) {
	for _, s := range []string{"", "Console", "con sole", "console\n", "web_app", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if ValidSurface(s) {
			t.Errorf("ValidSurface(%q) = true", s)
		}
		if _, err := New(DefaultWindow, s, testSession); !errors.Is(err, ErrSurface) {
			t.Errorf("New with surface %q: %v, want an ErrSurface", s, err)
		}
	}
	for _, s := range []string{"console", "web-app", "a", "surface-2"} {
		if !ValidSurface(s) {
			t.Errorf("ValidSurface(%q) = false", s)
		}
	}
	for _, s := range []string{"", "0123456789abcde", "0123456789abcdef0", "0123456789ABCDEF", "0123456789abcdeg"} {
		if _, err := New(DefaultWindow, SurfaceConsole, s); !errors.Is(err, ErrSession) {
			t.Errorf("New with session %q: %v, want an ErrSession", s, err)
		}
	}
	tr := newTracker(t, DefaultWindow)
	if tr.Surface() != SurfaceConsole || tr.Session() != testSession {
		t.Errorf("Surface, Session = %q, %q", tr.Surface(), tr.Session())
	}
}

func TestPresentWithinTheWindowOfTheLastActivity(t *testing.T) {
	tr := newTracker(t, DefaultWindow)

	if tr.Present(at(0)) {
		t.Error("present before any activity")
	}
	if _, ok := tr.LastActivity(); ok {
		t.Error("LastActivity reports one before any activity")
	}

	tr.Activity(at(0))
	for _, c := range []struct {
		d    time.Duration
		want bool
	}{
		{0, true},
		{time.Second, true},
		{10*time.Minute - time.Nanosecond, true},
		{10 * time.Minute, true},
		{10*time.Minute + time.Nanosecond, false},
		{time.Hour, false},
	} {
		if got := tr.Present(at(c.d)); got != c.want {
			t.Errorf("Present %v after the activity = %v, want %v", c.d, got, c.want)
		}
	}

	// The window counts from the last activity, not from the first.
	tr.Activity(at(8 * time.Minute))
	if !tr.Present(at(18 * time.Minute)) {
		t.Error("away 10 minutes after the second activity")
	}
	if tr.Present(at(18*time.Minute + time.Second)) {
		t.Error("present more than 10 minutes after the last activity")
	}
	if last, ok := tr.LastActivity(); !ok || !last.Equal(at(8*time.Minute)) {
		t.Errorf("LastActivity = %v, %v", last, ok)
	}
}

func TestPresentAsksAndChangesNothing(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(0))
	before := *tr
	tr.Present(at(time.Hour))
	tr.Present(at(-time.Hour))
	if _, err := tr.MayDecide(at(time.Hour)); err == nil {
		t.Fatal("MayDecide an hour later accepted")
	}
	if *tr != before {
		t.Errorf("Present or MayDecide changed the tracker: %+v, was %+v", *tr, before)
	}
}

func TestTheFirstActivityOfARunIsAnArrival(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	ev := tr.Activity(at(0))
	if !ev.Arrival {
		t.Fatal("the first activity of a run is not an arrival")
	}
	want := Change{Kind: Arrived, Stamp: Stamp{At: at(0), Surface: SurfaceConsole, Session: testSession}}
	if len(ev.Changes) != 1 || ev.Changes[0] != want {
		t.Errorf("changes = %+v, want %+v", ev.Changes, want)
	}
	if !ev.Changes[0].LastActivity.IsZero() {
		t.Error("the first arrival names a last activity before it")
	}
}

func TestAnArrivalIsReportedOnce(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	if !tr.Activity(at(0)).Arrival {
		t.Fatal("no first arrival")
	}
	// Activity inside the window, the same instant included, is ordinary.
	for _, d := range []time.Duration{0, time.Second, 5 * time.Minute, 15 * time.Minute} {
		ev := tr.Activity(at(d))
		if ev.Arrival || len(ev.Changes) != 0 {
			t.Errorf("activity at +%v is an arrival: %+v", d, ev)
		}
	}

	// Away for more than the window: the next activity is an arrival, and
	// the one after it is not.
	back := at(15*time.Minute + 10*time.Minute + time.Second)
	ev := tr.Activity(back)
	if !ev.Arrival {
		t.Fatal("the first activity after an away stretch is not an arrival")
	}
	if ev := tr.Activity(back.Add(time.Second)); ev.Arrival || len(ev.Changes) != 0 {
		t.Errorf("the arrival is reported a second time: %+v", ev)
	}
}

func TestAnArrivalAfterAnUnreportedStretchListsBothChanges(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(0))
	tr.Activity(at(3 * time.Minute))

	back := at(40 * time.Minute)
	ev := tr.Activity(back)
	if !ev.Arrival || len(ev.Changes) != 2 {
		t.Fatalf("event = %+v, want an arrival with two changes", ev)
	}
	left := Change{Kind: Left, Stamp: Stamp{
		At: at(13 * time.Minute), Surface: SurfaceConsole, Session: testSession, LastActivity: at(3 * time.Minute),
	}}
	arrived := Change{Kind: Arrived, Stamp: Stamp{
		At: back, Surface: SurfaceConsole, Session: testSession, LastActivity: at(3 * time.Minute),
	}}
	if ev.Changes[0] != left {
		t.Errorf("first change = %+v, want %+v", ev.Changes[0], left)
	}
	if ev.Changes[1] != arrived {
		t.Errorf("second change = %+v, want %+v", ev.Changes[1], arrived)
	}
}

func TestTickReportsLeavingOnce(t *testing.T) {
	tr := newTracker(t, DefaultWindow)

	if _, ok := tr.Tick(at(0)); ok {
		t.Error("Tick reports a change before any activity")
	}
	tr.Activity(at(0))
	for _, d := range []time.Duration{0, time.Minute, 10 * time.Minute} {
		if c, ok := tr.Tick(at(d)); ok {
			t.Errorf("Tick at +%v reports %+v while he is present", d, c)
		}
	}

	c, ok := tr.Tick(at(12 * time.Minute))
	want := Change{Kind: Left, Stamp: Stamp{
		At: at(10 * time.Minute), Surface: SurfaceConsole, Session: testSession, LastActivity: at(0),
	}}
	if !ok || c != want {
		t.Fatalf("Tick = %+v, %v, want %+v", c, ok, want)
	}
	for _, d := range []time.Duration{12 * time.Minute, 13 * time.Minute, time.Hour} {
		if c, ok := tr.Tick(at(d)); ok {
			t.Errorf("Tick at +%v reports leaving again: %+v", d, c)
		}
	}

	// Tick already reported the stretch, so the arrival carries one change.
	ev := tr.Activity(at(2 * time.Hour))
	if !ev.Arrival || len(ev.Changes) != 1 || ev.Changes[0].Kind != Arrived {
		t.Fatalf("event = %+v, want an arrival with one change", ev)
	}
	if !ev.Changes[0].LastActivity.Equal(at(0)) {
		t.Errorf("the arrival's last activity = %v, want the one before the stretch", ev.Changes[0].LastActivity)
	}
}

func TestChangesAlternate(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	var kinds []Kind
	note := func(cs ...Change) {
		for _, c := range cs {
			kinds = append(kinds, c.Kind)
			if c.Surface != SurfaceConsole || c.Session != testSession {
				t.Errorf("change %+v lacks the surface or the session", c)
			}
		}
	}
	// A mix of activity and ticks, some stretches seen by Tick and some not.
	for _, step := range []struct {
		d    time.Duration
		tick bool
	}{
		{0, false}, {time.Minute, true}, {20 * time.Minute, true}, {21 * time.Minute, true},
		{30 * time.Minute, false}, {31 * time.Minute, false}, {50 * time.Minute, false},
		{55 * time.Minute, false}, {90 * time.Minute, true}, {91 * time.Minute, false},
	} {
		if step.tick {
			if c, ok := tr.Tick(at(step.d)); ok {
				note(c)
			}
		} else {
			note(tr.Activity(at(step.d)).Changes...)
		}
	}
	if len(kinds) != 7 {
		t.Fatalf("changes = %v, want 7", kinds)
	}
	for i, k := range kinds {
		want := Arrived
		if i%2 == 1 {
			want = Left
		}
		if k != want {
			t.Fatalf("changes = %v: number %d is %v, want %v", kinds, i, k, want)
		}
	}
}

func TestAnArrivalAfterAReportedLeftAndALongerWindow(t *testing.T) {
	// He was reported away, and then the window is set longer, so that by
	// the window alone his old activity counts again. His next action is
	// still an arrival: the caller was told he left and must hear that he
	// came back.
	tr := newTracker(t, DefaultWindow)
	var kinds []Kind
	for _, c := range tr.Activity(at(0)).Changes {
		kinds = append(kinds, c.Kind)
	}
	c, ok := tr.Tick(at(11 * time.Minute))
	if !ok || c.Kind != Left {
		t.Fatalf("Tick = %+v, %v, want Left", c, ok)
	}
	kinds = append(kinds, c.Kind)
	if err := tr.SetWindow(MaxWindow); err != nil {
		t.Fatalf("SetWindow(MaxWindow): %v", err)
	}
	// Lengthening reports nothing by itself.
	if c, ok := tr.Tick(at(11*time.Minute + time.Second)); ok {
		t.Fatalf("Tick after lengthening = %+v, want no change", c)
	}

	ev := tr.Activity(at(12 * time.Minute))
	if !ev.Arrival {
		t.Fatal("the first activity after a reported Left is no arrival")
	}
	if len(ev.Changes) != 1 || ev.Changes[0].Kind != Arrived {
		t.Fatalf("changes = %+v, want one Arrived", ev.Changes)
	}
	want := Stamp{At: at(12 * time.Minute), Surface: SurfaceConsole, Session: testSession, LastActivity: at(0)}
	if ev.Changes[0].Stamp != want {
		t.Errorf("stamp = %+v, want %+v", ev.Changes[0].Stamp, want)
	}
	kinds = append(kinds, ev.Changes[0].Kind)
	if len(kinds) != 3 || kinds[0] != Arrived || kinds[1] != Left || kinds[2] != Arrived {
		t.Errorf("changes = %v, want arrived, left, arrived", kinds)
	}

	// It is reported once: the next action is ordinary activity.
	if ev := tr.Activity(at(12*time.Minute + time.Second)); ev.Arrival || len(ev.Changes) != 0 {
		t.Errorf("the next activity = %+v, want no arrival", ev)
	}
}

func TestActivityDoesNotConsumeTheAction(t *testing.T) {
	// The tracker is handed a time and nothing else, so it cannot hold a key
	// back. What it must not do is tell the caller to: the event of an
	// arrival and the event of ordinary activity differ only in what they
	// report. The caller delivers the key in both cases, as here.
	tr := newTracker(t, DefaultWindow)
	keys := []struct {
		key string
		d   time.Duration
	}{{"j", 0}, {"k", time.Second}, {"a", time.Hour}, {"q", time.Hour + time.Second}}

	var delivered []string
	arrivals := 0
	for _, k := range keys {
		ev := tr.Activity(at(k.d))
		if ev.Arrival {
			arrivals++
		}
		delivered = append(delivered, k.key)
	}
	if arrivals != 2 {
		t.Errorf("arrivals = %d, want 2", arrivals)
	}
	if len(delivered) != len(keys) {
		t.Errorf("delivered %v of %d keys", delivered, len(keys))
	}
	// The activity that was an arrival counts as activity like any other.
	if last, ok := tr.LastActivity(); !ok || !last.Equal(at(time.Hour+time.Second)) {
		t.Errorf("LastActivity = %v, %v", last, ok)
	}
	if !tr.Present(at(time.Hour + 2*time.Second)) {
		t.Error("not present after the arrival")
	}
}

func TestABeatIsDueAtMostOnceAMinute(t *testing.T) {
	tr := newTracker(t, DefaultWindow)

	if !tr.Activity(at(0)).BeatDue {
		t.Fatal("no beat due on the first activity")
	}
	for _, d := range []time.Duration{0, time.Second, 30 * time.Second, 59*time.Second + 999*time.Millisecond} {
		if tr.Activity(at(d)).BeatDue {
			t.Errorf("a second beat is due %v after the first", d)
		}
	}
	if !tr.Activity(at(time.Minute)).BeatDue {
		t.Error("no beat due one minute after the first")
	}
	if tr.Activity(at(time.Minute + 59*time.Second)).BeatDue {
		t.Error("a beat is due 59 seconds after the second")
	}

	// A key every second for ten minutes: one beat a minute, no more.
	tr = newTracker(t, DefaultWindow)
	beats := 0
	var last time.Time
	for s := 0; s < 600; s++ {
		now := at(time.Duration(s) * time.Second)
		if tr.Activity(now).BeatDue {
			if beats > 0 && now.Sub(last) < BeatInterval {
				t.Fatalf("two beats %v apart", now.Sub(last))
			}
			beats++
			last = now
		}
	}
	if beats != 10 {
		t.Errorf("beats in ten minutes of activity = %d, want 10", beats)
	}
}

func TestNoBeatWithoutActivity(t *testing.T) {
	// Only Activity can say a beat is due. Time passing does not.
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(0))
	before := *tr
	tr.Present(at(5 * time.Minute))
	if _, err := tr.MayDecide(at(5 * time.Minute)); err != nil {
		t.Fatalf("MayDecide: %v", err)
	}
	if _, ok := tr.Tick(at(5 * time.Minute)); ok {
		t.Fatal("Tick reports a change inside the window")
	}
	if *tr != before {
		t.Error("asking moved the beat's time")
	}
	// An arrival with a short window, less than a minute after the last
	// beat, does not earn a second beat.
	tr = newTracker(t, 10*time.Second)
	tr.Activity(at(0))
	ev := tr.Activity(at(30 * time.Second))
	if !ev.Arrival || ev.BeatDue {
		t.Errorf("event = %+v, want an arrival with no beat due", ev)
	}
}

func TestMayDecide(t *testing.T) {
	tr := newTracker(t, DefaultWindow)

	// No activity at all: away.
	_, err := tr.MayDecide(at(0))
	var away *Away
	if !errors.As(err, &away) {
		t.Fatalf("MayDecide before any activity = %v, want an *Away", err)
	}
	wantAway := Away{Stamp: Stamp{At: at(0), Surface: SurfaceConsole, Session: testSession}, Window: DefaultWindow}
	if *away != wantAway {
		t.Errorf("away = %+v, want %+v", *away, wantAway)
	}
	if away.Error() == "" {
		t.Error("an *Away with no last activity has no text")
	}

	tr.Activity(at(0))
	tr.Activity(at(2 * time.Minute))

	// Inside the window: accepted, with the journal's fields.
	got, err := tr.MayDecide(at(7 * time.Minute))
	if err != nil {
		t.Fatalf("MayDecide inside the window: %v", err)
	}
	want := Stamp{At: at(7 * time.Minute), Surface: SurfaceConsole, Session: testSession, LastActivity: at(2 * time.Minute)}
	if got != want {
		t.Errorf("stamp = %+v, want %+v", got, want)
	}
	if _, err := tr.MayDecide(at(12 * time.Minute)); err != nil {
		t.Errorf("MayDecide at the window's end: %v", err)
	}

	// Past the window: the typed no, with the same fields for the journal.
	got, err = tr.MayDecide(at(12*time.Minute + time.Second))
	if !errors.As(err, &away) {
		t.Fatalf("MayDecide past the window = %v, want an *Away", err)
	}
	if got != (Stamp{}) {
		t.Errorf("a refused decision returns the stamp %+v", got)
	}
	wantAway = Away{
		Stamp: Stamp{
			At: at(12*time.Minute + time.Second), Surface: SurfaceConsole, Session: testSession,
			LastActivity: at(2 * time.Minute),
		},
		Window: DefaultWindow,
	}
	if *away != wantAway {
		t.Errorf("away = %+v, want %+v", *away, wantAway)
	}
	if away.Error() == "" {
		t.Error("an *Away has no text")
	}

	// A decision is not an activity: asking again is refused again, and the
	// next activity is still an arrival.
	if _, err := tr.MayDecide(at(12*time.Minute + 2*time.Second)); !errors.As(err, &away) {
		t.Errorf("the second MayDecide past the window = %v, want an *Away", err)
	}
	if !tr.Activity(at(13 * time.Minute)).Arrival {
		t.Error("MayDecide used up the arrival")
	}
	if _, err := tr.MayDecide(at(13 * time.Minute)); err != nil {
		t.Errorf("MayDecide after he came back: %v", err)
	}
}

func TestAClockThatGoesBackNeverMakesHimPresent(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(time.Hour))

	// Every time before the last activity is away, near or far.
	for _, d := range []time.Duration{time.Hour - time.Nanosecond, 59 * time.Minute, 0, -24 * time.Hour} {
		if tr.Present(at(d)) {
			t.Errorf("present at +%v, before the last activity", d)
		}
		var away *Away
		if _, err := tr.MayDecide(at(d)); !errors.As(err, &away) {
			t.Errorf("MayDecide at +%v, before the last activity = %v, want an *Away", d, err)
		}
	}

	// The answer is about the time asked, whatever was asked before: after
	// a question about a late time, a time inside the window is present
	// again and a time before the last activity is still away.
	tr = newTracker(t, DefaultWindow)
	tr.Activity(at(0))
	if tr.Present(at(time.Hour)) {
		t.Fatal("present an hour after the activity")
	}
	if !tr.Present(at(5 * time.Minute)) {
		t.Error("away five minutes after the last activity")
	}
	if tr.Present(at(-time.Minute)) {
		t.Error("present before the last activity")
	}
}

func TestActivityOnAClockSetBackIsAnArrival(t *testing.T) {
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(time.Hour))

	// The clock is now 50 minutes earlier. Time passing does not make him
	// present; Tick reports that he left, at the time it saw it.
	c, ok := tr.Tick(at(10 * time.Minute))
	want := Change{Kind: Left, Stamp: Stamp{
		At: at(10 * time.Minute), Surface: SurfaceConsole, Session: testSession, LastActivity: at(time.Hour),
	}}
	if !ok || c != want {
		t.Fatalf("Tick on a clock set back = %+v, %v, want %+v", c, ok, want)
	}

	// His next action makes him present, as an arrival, and a beat is due
	// although the last one is "in the future".
	ev := tr.Activity(at(11 * time.Minute))
	if !ev.Arrival || !ev.BeatDue || len(ev.Changes) != 1 {
		t.Fatalf("event = %+v, want an arrival with a beat due", ev)
	}
	if !tr.Present(at(12 * time.Minute)) {
		t.Error("away a minute after his action")
	}
	if last, _ := tr.LastActivity(); !last.Equal(at(11 * time.Minute)) {
		t.Errorf("LastActivity = %v, want the new time", last)
	}

	// With no Tick in between, the activity lists both changes.
	tr = newTracker(t, DefaultWindow)
	tr.Activity(at(time.Hour))
	ev = tr.Activity(at(time.Minute))
	if !ev.Arrival || len(ev.Changes) != 2 || ev.Changes[0].Kind != Left || ev.Changes[1].Kind != Arrived {
		t.Fatalf("event = %+v, want Left then Arrived", ev)
	}
	if !ev.Changes[0].At.Equal(at(time.Minute)) {
		t.Errorf("Left at %v, want the time it was seen", ev.Changes[0].At)
	}
}

func TestEveryStepBackOfWallTimesIsAnArrivalAndABeat(t *testing.T) {
	// The times here are plain wall times, as stored or parsed ones are:
	// they carry no monotonic reading, so a step back reaches the tracker.
	// The package doc says what follows, and this holds it to that: each
	// action at an earlier time, by however little, is a Left and an
	// Arrived with a beat due, also inside one beat interval.
	tr := newTracker(t, DefaultWindow)
	tr.Activity(at(time.Hour))
	for i := 1; i <= 5; i++ {
		now := at(time.Hour - time.Duration(i)*time.Millisecond)
		ev := tr.Activity(now)
		if !ev.Arrival || !ev.BeatDue {
			t.Fatalf("step %d back: event = %+v, want an arrival with a beat due", i, ev)
		}
		if len(ev.Changes) != 2 || ev.Changes[0].Kind != Left || ev.Changes[1].Kind != Arrived {
			t.Fatalf("step %d back: changes = %+v, want Left then Arrived", i, ev.Changes)
		}
		if !ev.Changes[0].At.Equal(now) || !ev.Changes[1].At.Equal(now) {
			t.Errorf("step %d back: changes at %v and %v, want both at %v", i, ev.Changes[0].At, ev.Changes[1].At, now)
		}
	}
	// A step forward after them is ordinary activity again.
	if ev := tr.Activity(at(time.Hour)); ev.Arrival || ev.BeatDue || len(ev.Changes) != 0 {
		t.Errorf("a step forward = %+v, want ordinary activity with no beat", ev)
	}
}

func TestStampsCarryNoMonotonicReading(t *testing.T) {
	// A time from a running clock carries a monotonic reading, which makes
	// two equal instants differ under ==. The fields for the journal are
	// plain wall times. The one reading taken here is test input only: no
	// result depends on its value.
	now := time.Now()
	tr := newTracker(t, DefaultWindow)
	ev := tr.Activity(now)
	if ev.Changes[0].At != now.Round(0) {
		t.Errorf("the arrival's time %v carries a monotonic reading", ev.Changes[0].At)
	}
	s, err := tr.MayDecide(now.Add(time.Second))
	if err != nil {
		t.Fatalf("MayDecide: %v", err)
	}
	if s.At != now.Add(time.Second).Round(0) || s.LastActivity != now.Round(0) {
		t.Errorf("stamp %+v carries a monotonic reading", s)
	}
}

func TestKindString(t *testing.T) {
	if Arrived.String() != "arrived" || Left.String() != "left" {
		t.Errorf("names = %q, %q", Arrived, Left)
	}
	if Kind(0).String() != "Kind(0)" {
		t.Errorf("Kind(0) = %q", Kind(0))
	}
}

func TestNewer(t *testing.T) {
	mark := at(time.Hour)
	for _, c := range []struct {
		name string
		mark time.Time
		at   time.Time
		want bool
	}{
		{"after the mark", mark, mark.Add(time.Nanosecond), true},
		{"at the mark", mark, mark, false},
		{"before the mark", mark, mark.Add(-time.Second), false},
		{"never looked", time.Time{}, at(0), true},
		{"never looked, long ago", time.Time{}, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"the same instant in another zone", mark, mark.In(time.FixedZone("x", 3600)), false},
	} {
		if got := Newer(c.mark, c.at); got != c.want {
			t.Errorf("%s: Newer = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAdvance(t *testing.T) {
	// The caller stores the mark and hands it back.
	var mark time.Time
	mark = Advance(mark, at(time.Minute))
	if !mark.Equal(at(time.Minute)) {
		t.Fatalf("the first look sets the mark to %v", mark)
	}
	mark = Advance(mark, at(time.Hour))
	if !mark.Equal(at(time.Hour)) {
		t.Fatalf("a later look sets the mark to %v", mark)
	}
	// A look on a clock set back does not move it back.
	if got := Advance(mark, at(30*time.Minute)); !got.Equal(at(time.Hour)) {
		t.Errorf("an earlier look moved the mark back to %v", got)
	}
	if got := Advance(mark, at(time.Hour)); !got.Equal(at(time.Hour)) {
		t.Errorf("a look at the mark's own time moved it to %v", got)
	}

	// What is newer follows the stored mark.
	if Newer(mark, at(59*time.Minute)) || !Newer(mark, at(61*time.Minute)) {
		t.Error("Newer does not follow the advanced mark")
	}

	// The mark is a plain wall time, fit to store.
	now := time.Now()
	if got := Advance(time.Time{}, now); got != now.Round(0) {
		t.Errorf("the mark %v carries a monotonic reading", got)
	}
}
