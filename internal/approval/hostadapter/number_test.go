//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// The first number is the UTC time of day in seconds, times 1000, plus 1, in
// the time zone of the clock or not.
func TestStandInFirstNumberIsTheUTCTimeOfDay(t *testing.T) {
	zone := time.FixedZone("far", -9*3600)
	for _, c := range []struct {
		at   time.Time
		want int
	}{
		{time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2026, 11, 20, 0, 0, 1, 999_000_000, time.UTC), 1001},
		{time.Date(2026, 11, 20, 14, 2, 4, 0, time.UTC), 50_524_001},
		{time.Date(2026, 11, 20, 23, 59, 59, 0, time.UTC), 86_399_001},
		{time.Date(2026, 11, 20, 5, 2, 4, 0, zone), 50_524_001},
	} {
		if got := StandInFirstNumber(c.at); got != c.want {
			t.Errorf("StandInFirstNumber(%s) = %d, want %d", c.at, got, c.want)
		}
	}
	if a, b := StandInFirstNumber(start), StandInFirstNumber(start.Add(time.Second)); b-a != 1000 {
		t.Errorf("a second later the first number is %d more, want 1000", b-a)
	}
}

// listing is a journal that lists the entries it was given, a page at a time.
type listing []ownerjournal.Entry

func (l listing) Entries(after int64, max int) ([]ownerjournal.Entry, error) {
	var out []ownerjournal.Entry
	for _, e := range l {
		if e.Seq > after && len(out) < max {
			out = append(out, e)
		}
	}
	return out, nil
}

// raised records what the stand-in was raised to.
type raised struct{ n int }

func (r *raised) RaiseNumber(n int) {
	if n > r.n {
		r.n = n
	}
}

// The next number is one past the highest of the day's ids, read across pages,
// and only ids of the day and of the form count.
func TestTheNextNumberIsPastTheJournalsHighestOfTheDay(t *testing.T) {
	day := start.UTC().Format("20060102")
	var l listing
	add := func(id string) {
		l = append(l, ownerjournal.Entry{Seq: int64(len(l) + 1), Kind: ownerjournal.KindCount, PendingID: id})
	}
	for i := 0; i < 2*journalPage+7; i++ {
		add("ap-20200101-" + fmt.Sprint(900_000+i)) // another day
	}
	add("")
	add("ap-" + day + "-x12")
	add("ap-" + day + "--5")
	add("ap-" + day + "-0042")
	add("ap-" + day + "-17")
	for i := 0; i < journalPage; i++ {
		add("ap-" + day + "-" + fmt.Sprint(i%40))
	}
	n, err := nextNumber(l, start)
	if err != nil || n != 43 {
		t.Fatalf("the next number is %d (%v), want 43", n, err)
	}
	if n, err := nextNumber(listing{}, start); err != nil || n != 0 {
		t.Errorf("an empty journal gives %d (%v), want 0", n, err)
	}
	r := &raised{n: 100}
	if err := seedNumbers(r, l, start); err != nil || r.n != 100 {
		t.Errorf("a lower number lowers the stand-in: %d (%v)", r.n, err)
	}
}

// A stand-in restarted on the journal of an earlier run does not give a card
// the id of a card that was decided: the plain scratch pass's Open raises the
// numbers past the journal's. Without it the restarted stand-in counts from 1
// again, the new card has the old card's id, and the journal refuses to take a
// decision for it.
func TestARestartedStandInDoesNotReuseTheIdOfADecidedCard(t *testing.T) {
	r := newRig(t, nil)
	first := r.card()
	if res, err := r.decide(first, false); err != nil || res.State != record.StateDenied {
		t.Fatalf("the first card was not denied: %+v, %v", res, err)
	}

	// The stand-in keeps nothing: the restart makes another one that counts
	// from 1, on the same journal and the same clock.
	cfg := doortest.Sample(r.clock.Now)
	cfg.Command = "qargi"
	r.stand = doortest.New(cfg)
	r.restart()

	second := r.card()
	if second.ID == first.ID {
		t.Fatalf("the new card has the id %s of the card decided before the restart", second.ID)
	}
	if res, err := r.decide(second, false); err != nil || res.State != record.StateDenied {
		t.Errorf("the new card cannot be decided: %+v, %v", res, err)
	}
}

// The trial's cards after a restart in the same second, on the same state
// folder and clock, are not given the id of a card that was decided: the
// trial's numbers start past the journal's.
func TestATrialRestartedInTheSameSecondDoesNotReuseTheIdOfADecidedCard(t *testing.T) {
	clock := doortest.NewClock(start)
	state := filepath.Join(privateDir(t), "state")
	if _, err := WriteTrialScratch(state); err != nil {
		t.Fatal(err)
	}
	sc, err := LoadScratch(filepath.Join(state, OwnerDirName, ScratchName))
	if err != nil {
		t.Fatal(err)
	}
	open := func() *Trial {
		t.Helper()
		tr, err := OpenTrial(context.Background(), TrialConfig{StateDir: state, Scratch: sc, Now: clock.Now, Manual: true})
		if err != nil {
			t.Fatalf("OpenTrial: %v", err)
		}
		t.Cleanup(func() { _ = tr.Close() })
		return tr
	}
	pending := func(tr *Trial, c TrialCard) record.Pending {
		t.Helper()
		shown, err := tr.door.Show(context.Background(), c.PendingID)
		if err != nil {
			t.Fatal(err)
		}
		p, err := record.DecodePending(shown.Pending.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	deny := func(tr *Trial, c TrialCard) error {
		t.Helper()
		if err := tr.Service.Beat(src); err != nil {
			t.Fatal(err)
		}
		_, err := tr.Service.Decide(context.Background(), src, frame(pending(tr, c), false))
		return err
	}

	tr := open()
	first := submitTrial(t, tr, TrialFinish)
	if err := deny(tr, first); err != nil {
		t.Fatalf("the first card cannot be decided: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	tr = open() // the same second
	second := submitTrial(t, tr, TrialFinish)
	if second.PendingID == first.PendingID {
		t.Fatalf("the new card has the id %s of the card decided before the restart", second.PendingID)
	}
	if err := deny(tr, second); err != nil {
		t.Errorf("the new card cannot be decided: %v", err)
	}

	// After the clock was set back by an hour the numbers still start past
	// the journal's.
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(-time.Hour)
	tr = open()
	third := submitTrial(t, tr, TrialFinish)
	if third.PendingID == first.PendingID || third.PendingID == second.PendingID {
		t.Fatalf("after the clock was set back the new card has the id %s of an earlier one", third.PendingID)
	}
	if err := deny(tr, third); err != nil {
		t.Errorf("the third card cannot be decided: %v", err)
	}
}
