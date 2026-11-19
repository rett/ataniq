package arrival

import (
	"regexp"
	"testing"
	"time"
)

// t0 is the time the tests count from.
var t0 = time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)

// life returns a card asked the given time after t0 that lives for d.
func life(asked, d time.Duration) Life {
	return Life{Asked: t0.Add(asked), Expires: t0.Add(asked + d)}
}

func TestStatusElementIsNothingWhenNoCardWaits(t *testing.T) {
	for _, cards := range [][]Life{nil, {}} {
		if got := StatusElement(cards, t0); got != (Status{}) {
			t.Errorf("StatusElement of no cards = %+v, want the zero Status", got)
		}
	}
}

func TestStatusElementCountsAndShowsTheOldestAge(t *testing.T) {
	cards := []Life{
		life(10*time.Minute, time.Hour),
		life(2*time.Minute, time.Hour), // the oldest
		life(12*time.Minute, time.Hour),
	}
	got := StatusElement(cards, t0.Add(15*time.Minute))
	if want := "approvals 3 (oldest 13 min)"; got.Text != want {
		t.Errorf("Text = %q, want %q", got.Text, want)
	}
	if got.Alert {
		t.Error("Alert is set with more than 20% of every card's life left")
	}
	one := StatusElement(cards[:1], t0.Add(10*time.Minute+5*time.Second))
	if want := "approvals 1 (oldest under 1 min)"; one.Text != want {
		t.Errorf("Text = %q, want %q", one.Text, want)
	}
}

func TestStatusElementTakesTheAlertStyleUnderAFifthOfALife(t *testing.T) {
	// A life of 30 minutes: a fifth of it is 6 minutes.
	c := life(0, 30*time.Minute)
	for _, tc := range []struct {
		at    time.Duration
		alert bool
	}{
		{0, false},
		{23 * time.Minute, false},
		{24 * time.Minute, false}, // exactly 20% left is not under 20%
		{24*time.Minute + time.Second, true},
		{29 * time.Minute, true},
		{30 * time.Minute, true}, // expired
		{3 * time.Hour, true},
	} {
		if got := c.Low(t0.Add(tc.at)); got != tc.alert {
			t.Errorf("Low after %v = %v, want %v", tc.at, got, tc.alert)
		}
		if got := StatusElement([]Life{c}, t0.Add(tc.at)).Alert; got != tc.alert {
			t.Errorf("Alert after %v = %v, want %v", tc.at, got, tc.alert)
		}
	}
}

func TestOneLowCardAmongManyIsEnough(t *testing.T) {
	cards := []Life{
		life(0, 10*time.Hour),
		life(0, 10*time.Minute), // 1 minute left at t0+9min
		life(0, 10*time.Hour),
	}
	if !StatusElement(cards, t0.Add(9*time.Minute)).Alert {
		t.Error("Alert is not set though one card has under 20% of its life left")
	}
}

func TestALifeThatCannotBeMeasuredIsLow(t *testing.T) {
	for _, c := range []Life{
		{},
		{Asked: t0, Expires: t0},
		{Asked: t0.Add(time.Hour), Expires: t0},
	} {
		if !c.Low(t0) {
			t.Errorf("%+v is not low", c)
		}
	}
}

func TestWaiting(t *testing.T) {
	if n, age := Waiting(nil, t0); n != 0 || age != 0 {
		t.Errorf("Waiting(nil) = %d, %v", n, age)
	}
	// A card asked after now (a clock that differs) has no negative age.
	if n, age := Waiting([]Life{life(time.Hour, time.Hour)}, t0); n != 1 || age != 0 {
		t.Errorf("Waiting of a card from the future = %d, %v, want 1, 0", n, age)
	}
	cards := []Life{life(5*time.Minute, time.Hour), life(time.Minute, time.Hour)}
	if n, age := Waiting(cards, t0.Add(10*time.Minute)); n != 2 || age != 9*time.Minute {
		t.Errorf("Waiting = %d, %v, want 2, 9m", n, age)
	}
}

func TestAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Hour, "under 1 min"},
		{0, "under 1 min"},
		{59 * time.Second, "under 1 min"},
		{time.Minute, "1 min"},
		{59*time.Minute + 59*time.Second, "59 min"},
		{time.Hour, "1 h"},
		{47*time.Hour + 59*time.Minute, "47 h"},
		{48 * time.Hour, "2 d"},
		{24 * 400 * time.Hour, "400 d"},
	} {
		if got := Age(tc.d); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestStatusTextIsWordsAndNumbers holds the form of the element, whatever
// the times are.
func TestStatusTextIsWordsAndNumbers(t *testing.T) {
	form := regexp.MustCompile(`^approvals [0-9]+ \(oldest (under 1 min|[0-9]+ (min|h|d))\)$`)
	for _, age := range []time.Duration{-time.Hour, 0, time.Minute, 90 * time.Minute, 72 * time.Hour, 1 << 62} {
		cards := []Life{{Asked: t0.Add(-age), Expires: t0.Add(time.Hour)}}
		if got := StatusElement(cards, t0).Text; !form.MatchString(got) {
			t.Errorf("age %v: Text = %q", age, got)
		}
	}
}
