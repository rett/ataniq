package limits

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// west is a location nine hours behind UTC, so that its calendar day and the
// UTC one differ for nine hours of every day.
var west = time.FixedZone("west", -9*60*60)

// east is a location thirteen hours ahead of UTC.
var east = time.FixedZone("east", 13*60*60)

func at(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

func kindOf(t *testing.T, err error) Kind {
	t.Helper()
	if err == nil {
		return ""
	}
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a *Refusal", err)
	}
	if r.Reason == "" || strings.ContainsAny(r.Reason, "\n\r") {
		t.Fatalf("reason %q is not one line", r.Reason)
	}
	if r.Error() != r.Reason {
		t.Fatalf("Error() = %q, want the reason %q", r.Error(), r.Reason)
	}
	return r.Kind
}

func TestNumbers(t *testing.T) {
	if Default != 12 || Max != 40 || Min != 1 || SuggestionMin != 6 || WindowDays != 14 {
		t.Fatalf("Default %d Max %d Min %d SuggestionMin %d WindowDays %d", Default, Max, Min, SuggestionMin, WindowDays)
	}
}

func TestMaxRaise(t *testing.T) {
	for _, c := range []struct{ present, want int }{
		{1, 1}, {2, 3}, {3, 4}, {4, 6}, {12, 18}, {18, 27}, {26, 39}, {27, 40},
		{28, 40}, {39, 40}, {40, 40}, {41, 40}, {0, 0}, {-3, -3},
	} {
		if got := MaxRaise(c.present); got != c.want {
			t.Errorf("MaxRaise(%d) = %d, want %d", c.present, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		name        string
		present     int
		raisedToday bool
		change      Change
		want        Kind
	}{
		{"lower by one key", 12, false, Change{To: 6}, ""},
		{"lower typed", 12, false, Change{To: 6, Typed: true}, ""},
		{"same number by one key", 12, false, Change{To: 12}, ""},
		{"lower to the minimum", 12, false, Change{To: 1}, ""},
		{"lower on a day with a raise", 18, true, Change{To: 12}, ""},
		{"same number on a day with a raise", 18, true, Change{To: 18, Typed: true}, ""},
		{"zero", 12, false, Change{To: 0}, UnderMin},
		{"negative, typed", 12, false, Change{To: -1, Typed: true}, UnderMin},
		{"above the maximum", 40, false, Change{To: 41, Typed: true}, AboveMax},
		{"above the maximum by one key", 12, false, Change{To: 41}, AboveMax},
		{"raise by one key", 12, false, Change{To: 13}, NotTyped},
		{"raise by one key within half", 12, false, Change{To: 18}, NotTyped},
		{"raise by one", 12, false, Change{To: 13, Typed: true}, ""},
		{"raise by exactly half", 12, false, Change{To: 18, Typed: true}, ""},
		{"raise by one more than half", 12, false, Change{To: 19, Typed: true}, RaiseTooLarge},
		{"18 to 27", 18, false, Change{To: 27, Typed: true}, ""},
		{"18 to 28", 18, false, Change{To: 28, Typed: true}, RaiseTooLarge},
		{"27 to 40", 27, false, Change{To: 40, Typed: true}, ""},
		{"39 to 40", 39, false, Change{To: 40, Typed: true}, ""},
		{"26 to 40", 26, false, Change{To: 40, Typed: true}, RaiseTooLarge},
		{"26 to 39", 26, false, Change{To: 39, Typed: true}, ""},
		{"half of an odd number rounds down", 13, false, Change{To: 19, Typed: true}, ""},
		{"half of an odd number rounds down, one more", 13, false, Change{To: 20, Typed: true}, RaiseTooLarge},
		{"1 cannot be raised", 1, false, Change{To: 2, Typed: true}, CannotRaise},
		{"1 stays 1", 1, false, Change{To: 1}, ""},
		{"2 to 3", 2, false, Change{To: 3, Typed: true}, ""},
		{"2 to 4", 2, false, Change{To: 4, Typed: true}, RaiseTooLarge},
		{"3 to 4", 3, false, Change{To: 4, Typed: true}, ""},
		{"3 to 5", 3, false, Change{To: 5, Typed: true}, RaiseTooLarge},
		{"second raise of the day", 18, true, Change{To: 19, Typed: true}, AlreadyRaised},
		{"second raise of the day, too large", 18, true, Change{To: 28, Typed: true}, AlreadyRaised},
		{"second raise of the day, from a limit that cannot rise", 1, true, Change{To: 2, Typed: true}, AlreadyRaised},
		{"second raise of the day, by one key", 18, true, Change{To: 19}, NotTyped},
		{"second raise of the day, above the maximum", 39, true, Change{To: 41, Typed: true}, AboveMax},
		{"present limit of zero", 0, false, Change{To: 1}, BadLimit},
		{"present limit above the maximum", 41, false, Change{To: 40}, BadLimit},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := kindOf(t, Check(c.present, c.raisedToday, c.change)); got != c.want {
				t.Fatalf("Check(%d, %v, %+v) = %q, want %q", c.present, c.raisedToday, c.change, got, c.want)
			}
		})
	}
}

// The reasons for the small numbers say what the rule allows.
func TestSmallNumberReasons(t *testing.T) {
	err := Check(1, false, Change{To: 2, Typed: true})
	if err == nil || !strings.Contains(err.Error(), "a limit of 1 cannot be raised") {
		t.Fatalf("reason for 1: %v", err)
	}
	err = Check(2, false, Change{To: 4, Typed: true})
	if err == nil || !strings.Contains(err.Error(), "from 2 the limit rises to 3 at most") {
		t.Fatalf("reason for 2: %v", err)
	}
	err = Check(12, false, Change{To: 19, Typed: true})
	if err == nil || !strings.Contains(err.Error(), "from 12 the limit rises to 18 at most") {
		t.Fatalf("reason for 12: %v", err)
	}
}

func TestLowerWarning(t *testing.T) {
	for _, c := range []struct {
		present, to int
		want        string
	}{
		{12, 1, "a limit of 1 cannot be raised again"},
		{12, 2, "a limit of 2 can be raised again only by one, to 3"},
		{2, 1, "a limit of 1 cannot be raised again"},
		{12, 3, "a limit of 3 can be raised again only by one, to 4"},
		{12, 4, ""},
		{12, 6, ""},
		{1, 1, ""},
		{1, 2, ""},
		{12, 0, ""},
	} {
		if got := LowerWarning(c.present, c.to); got != c.want {
			t.Errorf("LowerWarning(%d, %d) = %q, want %q", c.present, c.to, got, c.want)
		}
	}
}

func TestApply(t *testing.T) {
	v := View{Project: "project-a", Limit: 12, Suggestion: 6, Days: []Day{{Date: "2031-03-01", Uses: 2}}}

	raised, err := Apply(v, Change{To: 18, Typed: true})
	if err != nil {
		t.Fatal(err)
	}
	if raised.Limit != 18 || !raised.RaisedToday || raised.Project != "project-a" || raised.Suggestion != 6 || len(raised.Days) != 1 {
		t.Fatalf("after the raise: %+v", raised)
	}
	if v.Limit != 12 || v.RaisedToday {
		t.Fatalf("the view handed in was changed: %+v", v)
	}

	again, err := Apply(raised, Change{To: 27, Typed: true})
	if kindOf(t, err) != AlreadyRaised {
		t.Fatalf("second raise: %v", err)
	}
	if again.Limit != 18 || !again.RaisedToday {
		t.Fatalf("a refused change moved the view: %+v", again)
	}

	lowered, err := Apply(raised, Change{To: 5})
	if err != nil {
		t.Fatal(err)
	}
	if lowered.Limit != 5 || !lowered.RaisedToday {
		t.Fatalf("after lowering: %+v", lowered)
	}

	// Lowering does not use up the day's raise, and setting the same number
	// does not either.
	low, err := Apply(v, Change{To: 8})
	if err != nil || low.RaisedToday {
		t.Fatalf("lowering: %+v, %v", low, err)
	}
	same, err := Apply(low, Change{To: 8, Typed: true})
	if err != nil || same.RaisedToday {
		t.Fatalf("same number: %+v, %v", same, err)
	}
	up, err := Apply(same, Change{To: 12, Typed: true})
	if err != nil || up.Limit != 12 || !up.RaisedToday {
		t.Fatalf("raise after lowering: %+v, %v", up, err)
	}
}

func TestDateReadsTheClocksLocation(t *testing.T) {
	// 03:00 UTC on the 10th is 18:00 on the 9th in west.
	moment := at(time.UTC, 2031, 3, 10, 3, 0)
	if got := Date(moment, at(west, 2031, 3, 9, 20, 0)); got != "2031-03-09" {
		t.Fatalf("in west: %s", got)
	}
	if got := Date(moment, at(time.UTC, 2031, 3, 10, 4, 0)); got != "2031-03-10" {
		t.Fatalf("in UTC: %s", got)
	}
	// 12:00 UTC on the 10th is 01:00 on the 11th in east.
	if got := Date(at(time.UTC, 2031, 3, 10, 12, 0), at(east, 2031, 3, 11, 2, 0)); got != "2031-03-11" {
		t.Fatalf("in east: %s", got)
	}
	if got := Today(at(west, 2031, 3, 9, 23, 59)); got != "2031-03-09" {
		t.Fatalf("today in west: %s", got)
	}
}

func TestRaisedTodayAtMidnight(t *testing.T) {
	for _, loc := range []*time.Location{time.UTC, west, east} {
		raise := time.Date(2031, 3, 9, 23, 59, 59, 0, loc)
		before := time.Date(2031, 3, 9, 23, 59, 59, 999, loc)
		after := time.Date(2031, 3, 10, 0, 0, 0, 0, loc)
		if !RaisedToday(raise, before) {
			t.Errorf("%s: a raise at 23:59:59 is not seen in the same second", loc)
		}
		if RaisedToday(raise, after) {
			t.Errorf("%s: a raise at 23:59:59 still stands at midnight", loc)
		}
		first := time.Date(2031, 3, 10, 0, 0, 0, 0, loc)
		if !RaisedToday(first, time.Date(2031, 3, 10, 23, 59, 59, 0, loc)) {
			t.Errorf("%s: a raise at midnight is not seen at the end of that day", loc)
		}
		if RaisedToday(first, time.Date(2031, 3, 11, 0, 0, 0, 0, loc)) {
			t.Errorf("%s: a raise at midnight still stands a day later", loc)
		}
	}
}

// The day is the clock's, not UTC's and not the location the raise was
// written in.
func TestRaisedTodayInAnotherLocation(t *testing.T) {
	// The raise: 02:00 UTC on the 10th, which is 17:00 on the 9th in west.
	raise := at(time.UTC, 2031, 3, 10, 2, 0)

	// 23:30 on the 9th in west is 08:30 UTC on the 10th: same UTC date as
	// the raise, and the same day in west.
	if !RaisedToday(raise, at(west, 2031, 3, 9, 23, 30)) {
		t.Error("west, the evening of the raise: not seen")
	}
	// 00:30 on the 10th in west is 09:30 UTC on the 10th: still the UTC
	// date of the raise, and a new day in west.
	if RaisedToday(raise, at(west, 2031, 3, 10, 0, 30)) {
		t.Error("west, after its midnight: the raise still stands, as if the day were UTC's")
	}
	// A UTC clock at 23:30 on the 10th sees the raise today; a west clock at
	// the same instant (14:30 on the 10th) does not.
	if !RaisedToday(raise, at(time.UTC, 2031, 3, 10, 23, 30)) {
		t.Error("UTC, same date: not seen")
	}
	if RaisedToday(raise, at(time.UTC, 2031, 3, 10, 23, 30).In(west)) {
		t.Error("west, same instant as UTC 23:30: the raise still stands")
	}
}

func TestRaisedTodayEdges(t *testing.T) {
	now := at(west, 2031, 3, 9, 12, 0)
	if RaisedToday(time.Time{}, now) {
		t.Error("the zero time is a raise")
	}
	if RaisedToday(at(west, 2031, 3, 8, 23, 59), now) {
		t.Error("yesterday's raise stands")
	}
	// A raise dated tomorrow: the clock went back. It still stands.
	if !RaisedToday(at(west, 2031, 3, 10, 0, 1), now) {
		t.Error("a raise dated after today does not stand")
	}
}

func TestUsesToday(t *testing.T) {
	now := at(west, 2031, 3, 9, 12, 0)
	decided := []time.Time{
		time.Date(2031, 3, 8, 23, 59, 59, 0, west), // yesterday
		time.Date(2031, 3, 9, 0, 0, 0, 0, west),    // today, the first instant
		time.Date(2031, 3, 9, 23, 59, 59, 0, west), // today, the last second
		time.Date(2031, 3, 10, 0, 0, 0, 0, west),   // tomorrow: the clock was set back, counted
		at(time.UTC, 2031, 3, 10, 5, 0),            // 20:00 on the 9th in west: today
		at(time.UTC, 2031, 3, 9, 5, 0),             // 20:00 on the 8th in west: yesterday
	}
	if got := UsesToday(decided, now); got != 4 {
		t.Fatalf("UsesToday in west = %d, want 4", got)
	}
	// The same times under a UTC clock on the 9th: every one of them is on
	// the UTC 9th or after it. The earliest, 20:00 on the 8th in west, is
	// 05:00 UTC on the 9th.
	if got := UsesToday(decided, at(time.UTC, 2031, 3, 9, 12, 0)); got != 6 {
		t.Fatalf("UsesToday in UTC = %d, want 6", got)
	}
	if got := UsesToday(nil, now); got != 0 {
		t.Fatalf("no decisions: %d", got)
	}
}

// A clock set back across midnight leaves uses that are dated tomorrow. They
// still take room: the day must not get a second helping, and RaisedToday
// reads a raise dated after today the same way.
func TestUsesTodayCountsATimeAfterToday(t *testing.T) {
	now := at(west, 2031, 3, 9, 23, 59)
	for _, c := range []struct {
		name    string
		decided time.Time
		want    int
	}{
		{"the first instant of tomorrow", at(west, 2031, 3, 10, 0, 0), 1},
		{"a week ahead", at(west, 2031, 3, 16, 12, 0), 1},
		{"across a year's end", at(west, 2032, 1, 1, 0, 0), 1},
		{"the last minute of yesterday", at(west, 2031, 3, 8, 23, 59), 0},
		{"a year back", at(west, 2030, 3, 10, 0, 0), 0},
		{"a year whose date cannot be read back", at(west, 10000, 1, 1, 0, 0), 1},
	} {
		if got := UsesToday([]time.Time{c.decided}, now); got != c.want {
			t.Errorf("%s: UsesToday = %d, want %d", c.name, got, c.want)
		}
	}
	// The limit and the room agree: a day filled before the clock went back
	// stays full.
	var decided []time.Time
	for i := 0; i < 12; i++ {
		decided = append(decided, at(west, 2031, 3, 10, 0, i))
	}
	if got := Judge(true, UsesToday(decided, now), 12); got != Held {
		t.Fatalf("an approve after the clock went back is %s, want held", got)
	}
}

func TestJudge(t *testing.T) {
	for _, c := range []struct {
		name      string
		approve   bool
		uses, lim int
		want      Outcome
	}{
		{"first approve of the day", true, 0, 12, Use},
		{"last approve under the limit", true, 11, 12, Use},
		{"approve at the limit", true, 12, 12, Held},
		{"approve above the limit, after it was lowered", true, 12, 6, Held},
		{"approve with a limit of one, no use yet", true, 0, 1, Use},
		{"approve with a limit of one, one use", true, 1, 1, Held},
		{"limit above the maximum", true, 40, 41, Held},
		{"limit far above the maximum", true, 45, 1000, Held},
		{"limit above the maximum, the day has room", true, 39, 41, Use},
		{"limit of zero", true, 0, 0, Held},
		{"deny on an empty day", false, 0, 12, NotCounted},
		{"deny at the limit", false, 12, 12, NotCounted},
		{"deny above the limit", false, 30, 6, NotCounted},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Judge(c.approve, c.uses, c.lim); got != c.want {
				t.Fatalf("Judge(%v, %d, %d) = %s, want %s", c.approve, c.uses, c.lim, got, c.want)
			}
		})
	}
}

// A day played through as a caller would: only a Use adds to the count, so a
// held approve and a deny take no room, and a decision after a held one is
// judged by itself.
func TestJudgeEachDecisionByItself(t *testing.T) {
	limit, uses := 2, 0
	decide := func(approve bool) Outcome {
		o := Judge(approve, uses, limit)
		if o == Use {
			uses++
		}
		return o
	}
	want := []struct {
		approve bool
		o       Outcome
	}{
		{false, NotCounted}, // a deny first: no use
		{true, Use},
		{true, Use},
		{true, Held},        // the day is full
		{false, NotCounted}, // a deny after a held approve goes through
		{true, Held},        // held again: the first held one took no room and gave none
	}
	for i, w := range want {
		if got := decide(w.approve); got != w.o {
			t.Fatalf("decision %d: %s, want %s", i, got, w.o)
		}
	}
	if uses != 2 {
		t.Fatalf("uses = %d, want 2: a held approve or a deny was counted", uses)
	}

	// The owner raises the limit. The next approve is a use at once: nothing
	// waits behind the two held ones, and a held card decided again is judged
	// like any other.
	limit = MaxRaise(limit)
	if got := decide(true); got != Use {
		t.Fatalf("after the raise: %s, want use", got)
	}
	if got := decide(true); got != Held {
		t.Fatalf("at the raised limit: %s, want held", got)
	}

	// The next day starts from no uses.
	uses = 0
	if got := decide(true); got != Use {
		t.Fatalf("the next day: %s, want use", got)
	}
}

func TestOutcomeAndHeldReason(t *testing.T) {
	if NotCounted.String() != "not counted" || Use.String() != "use" || Held.String() != "held" || Outcome(9).String() != "outcome 9" {
		t.Fatal("outcome names")
	}
	if got := HeldReason(12, 12); got != "decided past the limit: 12 card uses today, and the limit is 12" {
		t.Fatalf("HeldReason: %q", got)
	}
	if !Reached(12, 12) || !Reached(13, 12) || Reached(11, 12) {
		t.Fatal("Reached")
	}
}

func TestSuggest(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	for _, c := range []struct {
		name string
		days []Day
		want int
	}{
		{"no days", nil, 6},
		{"a quiet fortnight", []Day{{Date: "2031-03-10", Uses: 2}}, 6},
		{"three uses give the floor", []Day{{Date: "2031-03-10", Uses: 3}}, 6},
		{"four uses", []Day{{Date: "2031-03-10", Uses: 4}}, 8},
		{"the busiest day counts", []Day{{Date: "2031-03-10", Uses: 4}, {Date: "2031-03-12", Uses: 9}, {Date: "2031-03-13", Uses: 5}}, 18},
		{"twenty uses give the maximum", []Day{{Date: "2031-03-10", Uses: 20}}, 40},
		{"twenty-one uses stay at the maximum", []Day{{Date: "2031-03-10", Uses: 21}}, 40},
		{"a huge count stays at the maximum", []Day{{Date: "2031-03-10", Uses: int(^uint(0) >> 1)}}, 40},
		{"a day that reached the limit is left out", []Day{{Date: "2031-03-10", Uses: 12, ReachedLimit: true}, {Date: "2031-03-11", Uses: 5}}, 10},
		{"a day with an alert is left out", []Day{{Date: "2031-03-10", Uses: 12, Alert: true}, {Date: "2031-03-11", Uses: 4}}, 8},
		{"only left-out days", []Day{{Date: "2031-03-10", Uses: 12, Alert: true}, {Date: "2031-03-11", Uses: 12, ReachedLimit: true}}, 6},
		{"today is not counted", []Day{{Date: "2031-03-15", Uses: 11}}, 6},
		{"yesterday is counted", []Day{{Date: "2031-03-14", Uses: 11}}, 22},
		{"the fourteenth day back is counted", []Day{{Date: "2031-03-01", Uses: 11}}, 22},
		{"the fifteenth day back is not", []Day{{Date: "2031-02-28", Uses: 11}}, 6},
		{"a day after today is not", []Day{{Date: "2031-03-16", Uses: 11}}, 6},
		{"a date that is no date is dropped", []Day{{Date: "2031-3-10", Uses: 11}, {Date: "", Uses: 11}, {Date: "2031-02-30", Uses: 11}}, 6},
		{"a day given twice is one day", []Day{{Date: "2031-03-10", Uses: 3}, {Date: "2031-03-10", Uses: 4}}, 14},
		{"an alert on one of two entries leaves the day out", []Day{{Date: "2031-03-10", Uses: 9}, {Date: "2031-03-10", Alert: true}}, 6},
		{"a sum that overflows stays at the maximum", []Day{{Date: "2031-03-10", Uses: int(^uint(0) >> 1)}, {Date: "2031-03-10", Uses: 5}}, 40},
		{"negative uses count as none", []Day{{Date: "2031-03-10", Uses: -5}}, 6},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Suggest(c.days, now); got != c.want {
				t.Fatalf("Suggest = %d, want %d", got, c.want)
			}
		})
	}
}

// The window moves at the clock's midnight, in the clock's location.
func TestSuggestAtMidnight(t *testing.T) {
	days := []Day{{Date: "2031-03-01", Uses: 10}, {Date: "2031-03-15", Uses: 7}}
	for _, loc := range []*time.Location{time.UTC, west, east} {
		// The 15th, last second: the 1st is 14 days back, the 15th is today.
		if got := Suggest(days, time.Date(2031, 3, 15, 23, 59, 59, 0, loc)); got != 20 {
			t.Errorf("%s, before midnight: %d, want 20", loc, got)
		}
		// The 16th, first instant: the 1st fell out, the 15th came in.
		if got := Suggest(days, time.Date(2031, 3, 16, 0, 0, 0, 0, loc)); got != 14 {
			t.Errorf("%s, at midnight: %d, want 14", loc, got)
		}
	}
	// One instant, two clocks: 05:00 UTC on the 16th is 20:00 on the 15th in
	// west.
	instant := at(time.UTC, 2031, 3, 16, 5, 0)
	if got := Suggest(days, instant); got != 14 {
		t.Errorf("UTC clock: %d, want 14", got)
	}
	if got := Suggest(days, instant.In(west)); got != 20 {
		t.Errorf("west clock: %d, want 20", got)
	}
}

func TestNewView(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	days := []Day{
		{Date: "2031-03-14", Uses: 5},
		{Date: "2031-03-01", Uses: 3},
		{Date: "2031-03-10", Uses: 12, ReachedLimit: true},
		{Date: "2031-03-12", Uses: 9, Alert: true},
		{Date: "2031-03-15", Uses: 30}, // today
		{Date: "2031-02-28", Uses: 30}, // too old
	}
	v, err := NewView("project-a", 12, days, at(west, 2031, 3, 15, 0, 5), now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Project != "project-a" || v.Limit != 12 || !v.RaisedToday {
		t.Fatalf("view: %+v", v)
	}
	if v.Suggestion != 10 {
		t.Fatalf("suggestion %d, want 10: twice the 5 of the 14th, the marked days left out", v.Suggestion)
	}
	if len(v.Days) != 14 {
		t.Fatalf("%d days, want 14", len(v.Days))
	}
	if v.Days[0].Date != "2031-03-01" || v.Days[13].Date != "2031-03-14" {
		t.Fatalf("first %s, last %s", v.Days[0].Date, v.Days[13].Date)
	}
	for i := 1; i < len(v.Days); i++ {
		if v.Days[i-1].Date >= v.Days[i].Date {
			t.Fatalf("days not oldest first: %s, %s", v.Days[i-1].Date, v.Days[i].Date)
		}
	}
	want := map[string]Day{
		"2031-03-01": {Date: "2031-03-01", Uses: 3},
		"2031-03-10": {Date: "2031-03-10", Uses: 12, ReachedLimit: true},
		"2031-03-12": {Date: "2031-03-12", Uses: 9, Alert: true},
		"2031-03-14": {Date: "2031-03-14", Uses: 5},
	}
	for _, d := range v.Days {
		w, ok := want[d.Date]
		if !ok {
			w = Day{Date: d.Date}
		}
		if d != w {
			t.Errorf("day %+v, want %+v", d, w)
		}
		if d.LeftOut() != (d.ReachedLimit || d.Alert) {
			t.Errorf("LeftOut of %+v", d)
		}
	}
	if days[0].Date != "2031-03-14" || len(days) != 6 {
		t.Fatal("the days handed in were changed")
	}
}

func TestNewViewEdges(t *testing.T) {
	now := at(east, 2031, 1, 3, 0, 0)

	// No days, no raise: fourteen empty days across the year's end.
	v, err := NewView("project-b", Default, nil, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Limit != 12 || v.Suggestion != 6 || v.RaisedToday || len(v.Days) != 14 {
		t.Fatalf("view: %+v", v)
	}
	if v.Days[0].Date != "2030-12-20" || v.Days[13].Date != "2031-01-02" {
		t.Fatalf("first %s, last %s", v.Days[0].Date, v.Days[13].Date)
	}

	// A raise yesterday does not stand.
	v, err = NewView("project-b", 18, nil, at(east, 2031, 1, 2, 23, 59), now)
	if err != nil || v.RaisedToday {
		t.Fatalf("raise yesterday: %+v, %v", v, err)
	}

	for _, limit := range []int{0, -1, 41} {
		if _, err := NewView("project-b", limit, nil, time.Time{}, now); kindOf(t, err) != BadLimit {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
	for _, limit := range []int{1, 40} {
		if _, err := NewView("project-b", limit, nil, time.Time{}, now); err != nil {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
}

// Two projects share nothing: each view is made from its own days, limit and
// raise.
func TestRulesArePerProject(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	a, err := NewView("project-a", 12, []Day{{Date: "2031-03-14", Uses: 9}}, at(west, 2031, 3, 15, 1, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewView("project-b", 6, nil, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Suggestion != 18 || b.Suggestion != 6 {
		t.Fatalf("suggestions %d and %d", a.Suggestion, b.Suggestion)
	}
	if _, err := Apply(a, Change{To: 13, Typed: true}); kindOf(t, err) != AlreadyRaised {
		t.Fatalf("project-a: %v", err)
	}
	if got, err := Apply(b, Change{To: 9, Typed: true}); err != nil || got.Limit != 9 {
		t.Fatalf("project-b: %+v, %v", got, err)
	}
}

// always gives a limitOn for DaysOf that answers with one limit for every day.
func always(limit int) func(string) int {
	return func(string) int { return limit }
}

func TestDaysOf(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	decided := []time.Time{
		at(west, 2031, 3, 14, 23, 59),
		at(west, 2031, 3, 13, 10, 0),
		at(time.UTC, 2031, 3, 14, 3, 0), // 18:00 on the 13th in west
		at(west, 2031, 3, 15, 0, 0),
	}
	got := DaysOf(decided, always(2), now)
	want := []Day{
		{Date: "2031-03-13", Uses: 2, ReachedLimit: true},
		{Date: "2031-03-14", Uses: 1},
		{Date: "2031-03-15", Uses: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("DaysOf: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("day %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(DaysOf(nil, always(12), now)) != 0 {
		t.Error("no decisions give days")
	}
}

// Each day is marked against the limit that was in force on it, and the
// function is asked for every day by its date.
func TestDaysOfUsesTheLimitOfEachDay(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	var decided []time.Time
	for i := 0; i < 3; i++ {
		decided = append(decided, at(west, 2031, 3, 12, 10, i), at(west, 2031, 3, 13, 10, i))
	}
	limits := map[string]int{"2031-03-12": 3, "2031-03-13": 4}
	got := DaysOf(decided, func(date string) int { return limits[date] }, now)
	want := []Day{
		{Date: "2031-03-12", Uses: 3, ReachedLimit: true},
		{Date: "2031-03-13", Uses: 3},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("DaysOf = %+v, want %+v", got, want)
	}
}

// The limit was 12 on the 10th and the day filled it. On the 11th the owner
// raised the limit to 18. The full day stays left out: marked against the
// present 18 it would count, and the suggestion would be 24, above the limit
// it was full under.
func TestDaysOfAfterARaiseTheSuggestionDoesNotRise(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	var decided []time.Time
	for i := 0; i < 12; i++ {
		decided = append(decided, at(west, 2031, 3, 10, 10, i))
	}
	limitOn := func(date string) int {
		if date < "2031-03-11" {
			return 12
		}
		return 18
	}
	days := DaysOf(decided, limitOn, now)
	if len(days) != 1 || !days[0].ReachedLimit {
		t.Fatalf("the full day is not marked: %+v", days)
	}
	if got := Suggest(days, now); got != SuggestionMin {
		t.Fatalf("Suggest = %d, want %d", got, SuggestionMin)
	}
	v, err := NewView("project-a", 18, days, at(west, 2031, 3, 11, 9, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Suggestion != SuggestionMin {
		t.Fatalf("the view suggests %d, want %d", v.Suggestion, SuggestionMin)
	}
}

// A day whose limit is not known is left out, whatever its uses.
func TestDaysOfWithoutALimitLeavesTheDayOut(t *testing.T) {
	now := at(west, 2031, 3, 15, 9, 0)
	decided := []time.Time{at(west, 2031, 3, 13, 10, 0)}
	for name, limitOn := range map[string]func(string) int{
		"no function":      nil,
		"a limit of zero":  always(0),
		"a negative limit": always(-3),
	} {
		days := DaysOf(decided, limitOn, now)
		if len(days) != 1 || !days[0].ReachedLimit {
			t.Errorf("%s: %+v", name, days)
		}
	}
}
