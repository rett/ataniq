package seatpair

import (
	"strings"
	"testing"
	"time"
)

// A code that was seen expired stays expired when the wall clock steps back:
// the phone that holds a ten-minute-old code gets nothing from a clock that
// someone set back to inside the ten minutes.
func TestAnExpiredCodeStaysExpiredWhenTheClockStepsBack(t *testing.T) {
	for _, seenBy := range []string{"Present", "Phase", "Start"} {
		t.Run(seenBy, func(t *testing.T) {
			p, clk := newTestPairing()
			code, _ := p.Start(false)
			clk.advance(CodeLifetime + time.Minute)
			switch seenBy {
			case "Present":
				if err := present(p, code, idA, spkiA, clk.now); err != ErrExpired {
					t.Fatalf("Present at 11 minutes = %v, want ErrExpired", err)
				}
			case "Phase":
				if ph := p.Phase(clk.now); ph != PhaseNone {
					t.Fatalf("Phase at 11 minutes = %v", ph)
				}
			case "Start":
				// A pairing that is over needs no replace, and the new start
				// replaces the old code, which is not looked at again.
				if _, err := p.Start(false); err != nil {
					t.Fatalf("Start at 11 minutes = %v", err)
				}
				return
			}
			clk.now = t0.Add(5 * time.Minute) // the wall clock is set back
			if err := present(p, code, idA, spkiA, clk.now); err != ErrExpired {
				t.Errorf("Present after the clock stepped back = %v, want ErrExpired", err)
			}
			if ph := p.Phase(clk.now); ph != PhaseNone {
				t.Errorf("Phase after the clock stepped back = %v, want none", ph)
			}
			if _, err := p.Start(false); err != nil {
				t.Errorf("Start after the clock stepped back = %v: the pairing is over", err)
			}
		})
	}
}

// A code that was never seen expired is judged by the wall clock when the
// reading carries no monotonic part, as a fake clock's does: a clock set back
// before the deadline gives the code more wall time, up to a deadline at the
// time of issue plus ten minutes of the clock, and no more.
func TestAClockSetBackBeforeTheDeadlineLetsAWallClockCodeLiveLonger(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	clk.advance(8 * time.Minute)
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Fatalf("Phase at 8 minutes = %v", ph)
	}
	clk.now = t0.Add(4 * time.Minute) // set back by four minutes
	clk.advance(5 * time.Minute)      // thirteen minutes of real time, nine of the clock
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Fatalf("Phase at 9 minutes of the clock = %v", ph)
	}
	clk.advance(time.Minute) // ten of the clock: over, for good
	if err := present(p, code, idA, spkiA, clk.now); err != ErrExpired {
		t.Fatalf("Present at 10 minutes of the clock = %v, want ErrExpired", err)
	}
	clk.now = t0.Add(time.Minute)
	if err := present(p, code, idA, spkiA, clk.now); err != ErrExpired {
		t.Fatalf("Present after a second step back = %v, want ErrExpired", err)
	}
}

// A clock set back to before the code was made refuses with ErrClock, counts
// no try, and does not make an expired code live.
func TestAClockSetBackBeforeTheIssueIsRefused(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	clk.now = t0.Add(-time.Hour)
	if err := present(p, code, idA, spkiA, clk.now); err != ErrClock {
		t.Fatalf("Present before the issue = %v, want ErrClock", err)
	}
	if err := present(p, code, idA, spkiA, t0.Add(time.Minute)); err != nil {
		t.Errorf("Present after the clock returned = %v", err)
	}
}

// A credential that was seen past its time stays past it when the clock
// steps back, for Confirm, Pending and Present alike.
func TestAnExpiredPendingCredentialStaysExpiredWhenTheClockStepsBack(t *testing.T) {
	for _, seenBy := range []string{"Confirm", "Pending", "Present"} {
		t.Run(seenBy, func(t *testing.T) {
			p, clk, _ := pendingPairing(t)
			id := p.pendingID
			clk.advance(PendingLifetime) // one minute in, so this is past it
			switch seenBy {
			case "Confirm":
				if _, err := p.Confirm(clk.now, id); err != ErrPendingExpired {
					t.Fatalf("Confirm = %v, want ErrPendingExpired", err)
				}
			case "Pending":
				if _, ok := p.Pending(clk.now); ok {
					t.Fatal("Pending at the end is true")
				}
			case "Present":
				if err := present(p, "00000000000000000000", idB, spkiB, clk.now); err == ErrPending {
					t.Fatal("a credential past its time is still pending for Present")
				}
			}
			clk.now = t0.Add(2 * time.Minute) // inside the ten minutes again
			if _, ok := p.Pending(clk.now); ok {
				t.Error("Pending after the clock stepped back is true")
			}
			if _, ok := p.PendingID(clk.now); ok {
				t.Error("PendingID after the clock stepped back is true")
			}
			if ph := p.Phase(clk.now); ph != PhaseNone {
				t.Errorf("Phase after the clock stepped back = %v", ph)
			}
			if c, err := p.Confirm(clk.now, id); err == nil || c.ID != nil {
				t.Errorf("Confirm after the clock stepped back = %v, %v", c, err)
			}
			if seenBy != "Confirm" {
				if _, err := p.Confirm(clk.now, id); err != ErrPendingExpired && err != ErrNotPending {
					t.Errorf("Confirm = %v", err)
				}
			}
		})
	}
}

// A pending credential that was never seen expired is judged by the clock it
// is handed: set back before the deadline, it lives to the deadline of the
// clock.
func TestAClockSetBackBeforeThePendingDeadlineLetsItLiveLonger(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	id := p.pendingID
	clk.advance(9 * time.Minute) // ten minutes after the issue, nine after the presentation
	if _, ok := p.Pending(clk.now); !ok {
		t.Fatal("not pending at 9 minutes")
	}
	clk.now = t0.Add(2 * time.Minute) // set back to one minute after the presentation
	clk.advance(8 * time.Minute)      // nine minutes of the clock after it
	if c, err := p.Confirm(clk.now, id); err != nil || c.ID == nil {
		t.Fatalf("Confirm inside the clock's ten minutes = %v, %v", c, err)
	}
}

// A used or burnt code is spent for good; the latch of expiry joins them.
func TestAUsedOrBurntCodeStaysSpentWhenTheClockStepsBack(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	for i := 0; i < MaxTries; i++ {
		_ = present(p, wrongCode(code), idA, spkiA, clk.now)
	}
	clk.now = t0
	if err := present(p, code, idA, spkiA, clk.now); err != ErrBurnt {
		t.Errorf("a burnt code at an earlier time = %v, want ErrBurnt", err)
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("a burnt code reads as phase %v", ph)
	}
}

// The package takes the time from the readings it is handed and keeps them as
// they are: a reading from time.Now carries a monotonic part, and the
// deadlines made from it (issue plus ten minutes, presentation plus ten
// minutes) keep it, so Before and Add judge them by it, not by the wall clock.
func TestTheDeadlinesKeepTheMonotonicReading(t *testing.T) {
	has := func(tm time.Time) bool { return strings.Contains(tm.String(), "m=") }
	now := time.Now()
	if !has(now) {
		t.Skip("this clock has no monotonic reading")
	}
	_, c := NewCode(&seq{}, now)
	if !has(c.expires) || !has(c.at) {
		t.Errorf("the code's times lost their monotonic reading: %v, %v", c.at, c.expires)
	}
	p := NewPairing(&seq{}, time.Now)
	code, err := p.Start(false)
	if err != nil {
		t.Fatal(err)
	}
	if !has(p.code.expires) {
		t.Errorf("Start's deadline lost its monotonic reading: %v", p.code.expires)
	}
	if _, err := p.Present(code, idA, spkiA, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !has(p.pendingEnds) || !has(p.presentedAt) {
		t.Errorf("the pending times lost their monotonic reading: %v, %v", p.presentedAt, p.pendingEnds)
	}
	// And a deadline made from a reading with one is passed, exactly, by the
	// same reading plus ten minutes.
	if p.open(p.presentedAt.Add(PendingLifetime)) {
		t.Error("the pending credential is open at its deadline")
	}
}
