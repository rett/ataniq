package limits

import "time"

// dateLayout is the form of Day.Date: YYYY-MM-DD.
const dateLayout = "2006-01-02"

// Date returns the calendar day of t as YYYY-MM-DD, read in the location of
// now. now is the injected clock's reading; only its location is used here.
// This is the day a decision counts on: the day of its time as the clock's
// location sees it, not the UTC date.
func Date(t, now time.Time) string {
	return t.In(now.Location()).Format(dateLayout)
}

// Today returns the calendar day of now, in its own location.
func Today(now time.Time) string {
	return now.Format(dateLayout)
}

// dayNumber turns a calendar date into a count of days, so that two dates can
// be compared and subtracted without regard to any location or to daylight
// saving time. It reports false when date is not a real YYYY-MM-DD date.
func dayNumber(date string) (int64, bool) {
	t, err := time.Parse(dateLayout, date)
	if err != nil {
		return 0, false
	}
	return t.Unix() / 86400, true
}

// todayNumber is the day number of now's calendar day in its own location.
func todayNumber(now time.Time) int64 {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// dateOf is the inverse of dayNumber.
func dateOf(number int64) string {
	return time.Unix(number*86400, 0).UTC().Format(dateLayout)
}

// UsesToday counts the times in decided that fall on today, the calendar day
// of now in its own location. The caller hands in the decision times of the
// project's card uses, that is, of its approves that were not held; a deny
// and a held approve are not among them.
//
// A time dated after today also counts, as it does in RaisedToday. That can
// happen when the clock was set back, or when it ran fast and was corrected
// later, and neither must give the day more room.
func UsesToday(decided []time.Time, now time.Time) int {
	today := todayNumber(now)
	n := 0
	for _, t := range decided {
		// A date that cannot be read counts too: the day gets no room from
		// a time that cannot be placed.
		if day, ok := dayNumber(Date(t, now)); !ok || day >= today {
			n++
		}
	}
	return n
}

// RaisedToday reports whether a raise made at lastRaise stands in the way of
// another raise now. It does when lastRaise is on today's calendar day, read
// in the location of now. The zero time means the limit was never raised.
//
// A raise dated after today also counts. That can happen when the clock was
// set back, or when it ran fast and was corrected later, and neither must
// open a second raise.
func RaisedToday(lastRaise, now time.Time) bool {
	if lastRaise.IsZero() {
		return false
	}
	raised, ok := dayNumber(Date(lastRaise, now))
	if !ok {
		return true
	}
	return raised >= todayNumber(now)
}
