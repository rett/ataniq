//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/approval/ownerjournal"
)

// The stand-in door keeps nothing across a restart and the journal keeps
// everything, so a stand-in that counted its card numbers from 1 again would
// make a card whose id the journal already holds a decision for, and that card
// could not be decided (the journal refuses a second decision for an id). The
// numbers of the stand-in's cards therefore start from the time of day, and,
// where the journal can be read, past the highest number it holds for the day.

// StandInFirstNumber is the first number of the pending ids of a stand-in door
// started at t: the seconds of the day, in UTC, times 1000, plus 1. A stand-in
// restarted a second or more later starts above every number the earlier one
// could have used (a stand-in makes far fewer than 1000 cards in a second).
// Two starts in the same second, or one after the clock was set back, can
// start at the same number; [seedNumbers] covers that where the journal can be
// read, and the trial and the scratch pass read it.
func StandInFirstNumber(t time.Time) int {
	t = t.UTC()
	return (t.Hour()*3600+t.Minute()*60+t.Second())*1000 + 1
}

// entryLister is the part of a journal that lists its entries.
type entryLister interface {
	Entries(after int64, max int) ([]ownerjournal.Entry, error)
}

// journalPage is how many entries one read of the journal takes.
const journalPage = 500

// nextNumber returns one more than the highest number of an id "ap-YYYYMMDD-N"
// of t's day (UTC) that any entry of the journal j names, and 0 when it names
// none. The journal is read whole, a page at a time.
func nextNumber(j entryLister, t time.Time) (int, error) {
	next := 0
	prefix := "ap-" + t.UTC().Format("20060102") + "-"
	var after int64
	for {
		es, err := j.Entries(after, journalPage)
		if err != nil {
			return 0, fmt.Errorf("hostadapter: the journal's card numbers: %w", err)
		}
		for _, e := range es {
			after = e.Seq
			rest, ok := strings.CutPrefix(e.PendingID, prefix)
			if !ok {
				continue
			}
			if n, err := strconv.ParseInt(rest, 10, 32); err == nil && n >= int64(next) {
				next = int(n) + 1
			}
		}
		if len(es) < journalPage {
			return next, nil
		}
	}
}

// numberRaiser is the part of the stand-in door that [seedNumbers] sets.
type numberRaiser interface{ RaiseNumber(n int) }

// seedNumbers makes the next card number of the stand-in d, started at t,
// start past every number of t's day that the journal j holds an entry for,
// and never lowers it. A journal that names no such id leaves it as it is.
func seedNumbers(d numberRaiser, j entryLister, t time.Time) error {
	n, err := nextNumber(j, t)
	if err != nil {
		return err
	}
	d.RaiseNumber(n)
	return nil
}
