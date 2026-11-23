package ownerchannel

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// A card whose decision is journaled and not yet handed to the door reads
// with the mark "on its way", and with no outcome: so a seat whose answer was
// lost can tell it from a card nobody decided. Once the door has taken it the
// mark is gone, and a card no one decided never has it. The mark is in what
// the socket and the gate encode, and left out when it is false.
func TestShowMarksADecisionOnItsWayToTheDoor(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	ctx := context.Background()

	shown, err := e.svc.Show(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if shown.OnItsWay {
		t.Fatal("an undecided card is marked on its way")
	}
	b, err := ownerwire.EncodeShowResult(shown)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ownerwire.DecodeShowResult(b); err != nil || got.OnItsWay {
		t.Fatalf("the unmarked card reads back %+v %v", got.OnItsWay, err)
	}
	if bytes.Contains(b, []byte("on_its_way")) {
		t.Fatalf("a false mark is written: %s", b)
	}

	e.door.failDecide = errors.New("connection refused")
	if _, err := e.decide(p, false); !errors.Is(err, ErrDoorUnavailable) {
		t.Fatalf("Decide returned %v", err)
	}
	shown, err = e.svc.Show(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !shown.OnItsWay || shown.Held || shown.Outcome != nil {
		t.Fatalf("a decision on its way reads as %+v", shown)
	}
	b, err = ownerwire.EncodeShowResult(shown)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ownerwire.DecodeShowResult(b); err != nil || !got.OnItsWay || !bytes.Contains(b, []byte(`"on_its_way":true`)) {
		t.Fatalf("the mark does not travel: %s %v", b, err)
	}

	// A restart keeps it: the journal holds the decision.
	e.restart()
	e.door.failDecide = errors.New("still down")
	if err := e.svc.Tick(ctx); err != nil && !errors.Is(err, ErrDoorUnavailable) {
		t.Fatal(err)
	}
	if shown, err = e.svc.Show(ctx, p.ID); err != nil || !shown.OnItsWay {
		t.Fatalf("after a restart: %+v %v", shown, err)
	}

	e.door.failDecide = nil
	if err := e.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateDenied {
		t.Fatalf("the door's state is %s", e.state(p.ID))
	}
	if shown, err = e.svc.Show(ctx, p.ID); err != nil || shown.OnItsWay || shown.Outcome == nil || shown.Outcome.State != record.StateDenied {
		t.Fatalf("a delivered decision reads as %+v %v", shown, err)
	}
}
