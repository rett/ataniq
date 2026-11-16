package sessionreplay_test

import (
	"reflect"
	"testing"
	"time"

	"ataniq/internal/terminalconsole/sessionreplay"
)

const ms = time.Millisecond

func timed() *sessionreplay.Recording {
	return &sessionreplay.Recording{Cols: 80, Rows: 24, Events: []sessionreplay.Event{
		{At: 0, Kind: sessionreplay.KindOutput, Data: []byte("zero")},
		{At: 100 * ms, Kind: sessionreplay.KindOutput, Data: []byte("one")},
		{At: 100 * ms, Kind: sessionreplay.KindResize, Cols: 40, Rows: 10},
		{At: 100 * ms, Kind: sessionreplay.KindOutput, Data: []byte("two")},
		{At: 350 * ms, Kind: sessionreplay.KindOutput, Data: []byte("three")},
		{At: 500 * ms, Kind: sessionreplay.KindEnd, Status: 7, StatusKnown: true},
	}}
}

func player(t *testing.T) *sessionreplay.Player {
	t.Helper()
	p, err := sessionreplay.NewPlayer(timed())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func texts(evs []sessionreplay.Event) []string {
	var s []string
	for _, e := range evs {
		switch e.Kind {
		case sessionreplay.KindOutput:
			s = append(s, string(e.Data))
		default:
			s = append(s, e.Kind.String())
		}
	}
	return s
}

func TestAdvanceDeliversDueEventsInOrder(t *testing.T) {
	p := player(t)
	steps := []struct {
		at   time.Duration
		want []string
	}{
		{-5 * ms, nil},
		{0, []string{"zero"}},
		{0, nil},
		{99 * ms, nil},
		{100 * ms, []string{"one", "resize", "two"}},
		{100 * ms, nil},
		{349 * ms, nil},
		{350 * ms, []string{"three"}},
		{499 * ms, nil},
		{500 * ms, []string{"end"}},
		{time.Hour, nil},
	}
	for _, s := range steps {
		got := texts(p.Advance(s.at))
		if !reflect.DeepEqual(got, s.want) {
			t.Fatalf("at %v: got %v, want %v", s.at, got, s.want)
		}
	}
	if !p.Done() {
		t.Fatal("not done")
	}
}

func TestAdvanceIgnoresTimeGoingBack(t *testing.T) {
	p := player(t)
	p.Advance(120 * ms)
	if got := p.Advance(10 * ms); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := texts(p.Advance(400 * ms)); !reflect.DeepEqual(got, []string{"three"}) {
		t.Fatalf("got %v", got)
	}
}

func TestAdvanceOneBigJump(t *testing.T) {
	p := player(t)
	evs := p.Advance(time.Hour)
	if got := texts(evs); !reflect.DeepEqual(got, []string{"zero", "one", "resize", "two", "three", "end"}) {
		t.Fatalf("got %v", got)
	}
	last := evs[len(evs)-1]
	if last.Kind != sessionreplay.KindEnd || last.Status != 7 || !last.StatusKnown {
		t.Fatalf("last %+v", last)
	}
	if evs[2].Cols != 40 || evs[2].Rows != 10 {
		t.Fatalf("resize %+v", evs[2])
	}
}

func TestAdvanceInTicks(t *testing.T) {
	// A caller on a fixed tick sees the same events as one big jump.
	p := player(t)
	var all []sessionreplay.Event
	for at := time.Duration(0); at <= 600*ms; at += 33 * ms {
		all = append(all, p.Advance(at)...)
	}
	want := player(t).Advance(time.Hour)
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("got %v", texts(all))
	}
}

func TestNextAndDone(t *testing.T) {
	p := player(t)
	at, ok := p.Next()
	if !ok || at != 0 || p.Done() {
		t.Fatalf("%v %v", at, ok)
	}
	p.Advance(0)
	if at, _ := p.Next(); at != 100*ms {
		t.Fatalf("next %v", at)
	}
	p.Advance(100 * ms)
	if at, _ := p.Next(); at != 350*ms {
		t.Fatalf("next %v", at)
	}
	p.Advance(time.Hour)
	if _, ok := p.Next(); ok || !p.Done() {
		t.Fatal("not finished")
	}
}

func TestReset(t *testing.T) {
	p := player(t)
	first := p.Advance(time.Hour)
	p.Reset()
	if p.Done() {
		t.Fatal("done after reset")
	}
	if again := p.Advance(time.Hour); !reflect.DeepEqual(again, first) {
		t.Fatal("different events after reset")
	}
}

func TestSize(t *testing.T) {
	if c, r := player(t).Size(); c != 80 || r != 24 {
		t.Fatalf("%dx%d", c, r)
	}
}

func TestPlayerOwnsItsData(t *testing.T) {
	rec := timed()
	p, err := sessionreplay.NewPlayer(rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.Events[0].Data[0] = 'X'
	evs := p.Advance(0)
	evs[0].Data[1] = 'Y'
	p.Reset()
	if got := string(p.Advance(0)[0].Data); got != "zero" {
		t.Fatalf("got %q", got)
	}
}

func TestNewPlayerRejectsInvalid(t *testing.T) {
	rec := timed()
	rec.Events = rec.Events[:5]
	if _, err := sessionreplay.NewPlayer(rec); err == nil {
		t.Fatal("accepted")
	}
}
