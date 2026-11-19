package arrival

import (
	"strconv"
	"time"
)

// Life is the life of one waiting card: when it was asked and when it
// expires. The cockpit takes both from the card's record.
type Life struct {
	Asked   time.Time
	Expires time.Time
}

// Low reports whether the card has under 20% of its life left at the given
// time. A card that has expired is low, and so is one whose life cannot be
// measured (it expires no later than it was asked): when in doubt the
// status takes the alert style.
func (l Life) Low(now time.Time) bool {
	total := l.Expires.Sub(l.Asked)
	if total <= 0 {
		return true
	}
	left := l.Expires.Sub(now)
	// left/total < 1/5, without dividing.
	return left < total/5
}

// Waiting returns how many cards wait and the age of the oldest at the given
// time: the time since the earliest Asked. The age is never negative, and it
// is zero when no card waits.
func Waiting(cards []Life, now time.Time) (n int, oldest time.Duration) {
	for i, c := range cards {
		if age := now.Sub(c.Asked); i == 0 || age > oldest {
			oldest = age
		}
	}
	return len(cards), max(0, oldest)
}

// Status is the approvals element of the status line.
type Status struct {
	// Text is what the status line shows, for example
	// "approvals 2 (oldest 3 min)". It is empty when no card waits: the
	// element is then left out.
	Text string
	// Alert says that Text is drawn in the alert style: a waiting card has
	// under 20% of its life left.
	Alert bool
}

// StatusElement returns the approvals element for the cards that wait at the
// given time: "approvals N" with the age of the oldest card. It is the zero
// Status when no card waits. The text is made of this package's words and of
// numbers only.
func StatusElement(cards []Life, now time.Time) Status {
	n, oldest := Waiting(cards, now)
	if n == 0 {
		return Status{}
	}
	st := Status{Text: "approvals " + strconv.Itoa(n) + " (oldest " + Age(oldest) + ")"}
	for _, c := range cards {
		if c.Low(now) {
			st.Alert = true
			break
		}
	}
	return st
}

// Age writes a length of time the short way the console shows ages: "under
// 1 min", "3 min", "2 h", "4 d". It rounds down, shows minutes below one
// hour, hours below two days and days from there. A negative length reads as
// "under 1 min".
func Age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under 1 min"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + " d"
}
