package seatlink

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/seatapp"
)

// logLines collects what a link tells its log.
type logLines struct {
	mu sync.Mutex
	s  []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s = append(l.s, fmt.Sprintf(format, args...))
}

func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.s...)
}

// A call refused for room is one line in 30 seconds for its kind, the next
// line after that counts the refusals held back, and the kinds are counted
// apart. The wait is never refused and never told.
func TestSeatLinkRefusalLog(t *testing.T) {
	for _, c := range []struct {
		name string
		k    kind
	}{{"public", kindPublic}, {"read", kindRead}, {"chat", kindChat}, {"genesis", kindGenesis}} {
		var l logLines
		clock := &testClock{t: decideNow}
		b := &budget{now: clock.now, logf: l.logf}
		for i := 0; i < c.k.room(); i++ {
			if !b.take(c.k) {
				t.Fatalf("%s: call %d refused", c.name, i+1)
			}
		}
		if len(l.all()) != 0 {
			t.Fatalf("%s: lines within the room: %q", c.name, l.all())
		}
		if b.take(c.k) {
			t.Fatalf("%s: a call past the room", c.name)
		}
		want := fmt.Sprintf("ataniq-seat: the link has no room for a call of kind %s (%d of %d in the last 80 seconds); the page says the desk is busy\n",
			c.name, c.k.room(), c.k.room())
		if got := l.all(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: the first line: %q, want %q", c.name, got, want)
		}
		// Within 30 seconds: counted, not told.
		clock.advance(29 * time.Second)
		b.take(c.k)
		b.take(c.k)
		if got := l.all(); len(got) != 1 {
			t.Fatalf("%s: lines within 30 seconds: %q", c.name, got)
		}
		// At 30 seconds the next one comes with the count. The calls
		// counted are still in the window (30 of 80 seconds).
		clock.advance(time.Second)
		b.take(c.k)
		got := l.all()
		if len(got) != 2 || got[1] != strings.TrimSuffix(want, "\n")+"; 2 more since the last line\n" {
			t.Fatalf("%s: the second line: %q", c.name, got)
		}
		// And the count starts again.
		clock.advance(RefusalLogEvery)
		b.take(c.k)
		got = l.all()
		if len(got) != 3 || got[2] != want {
			t.Fatalf("%s: the third line: %q", c.name, got)
		}
	}
}

// The decisions' challenge and answer are refused and told as decide, the
// logins' as public, and a line holds nothing of the challenge: no id.
func TestSeatLinkRefusalLogChallenges(t *testing.T) {
	var l logLines
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now, logf: l.logf}
	const id = "0123456789abcdef0123456789abcdef"
	var tokens []uint64
	for i := 0; i < decideRoom/2; i++ {
		tok, ok := b.reserve(kindDecide)
		if !ok {
			t.Fatalf("challenge %d refused", i+1)
		}
		b.bind(tok, id+fmt.Sprint(i), clock.now().Add(time.Hour))
		tokens = append(tokens, tok)
	}
	if _, ok := b.reserve(kindDecide); ok {
		t.Fatal("a challenge past the room")
	}
	// An answer to a challenge the link did not draw has no room.
	if b.answer(kindDecide, id+"unknown") {
		t.Fatal("an answer with no room")
	}
	got := l.all()
	if len(got) != 1 || !strings.Contains(got[0], "kind decide (8 of 8 in the last 80 seconds)") {
		t.Fatalf("lines: %q", got)
	}
	clock.advance(RefusalLogEvery)
	for i := 0; i < publicRoom; i++ {
		b.take(kindPublic)
	}
	if b.answer(kindPublic, id) {
		t.Fatal("a login answer with no room")
	}
	got = l.all()
	if len(got) != 2 {
		t.Fatalf("lines: %q", got)
	}
	if !strings.Contains(got[1], "kind public (12 of 12") {
		t.Fatalf("the public line: %q", got[1])
	}
	for _, line := range got {
		if strings.Contains(line, id) {
			t.Errorf("a line holds a challenge id: %q", line)
		}
	}
}

// The wait is a sleeper: it is never told, however often it finds no room.
func TestSeatLinkRefusalLogSkipsTheWait(t *testing.T) {
	var l logLines
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now, logf: l.logf}
	for i := 0; i < waitRoom; i++ {
		b.take(kindWait)
	}
	if b.take(kindWait) {
		t.Fatal("a wait past the room")
	}
	if ok, _ := b.takeOrWait(kindWait); ok {
		t.Fatal("a wait past the room")
	}
	if got := l.all(); len(got) != 0 {
		t.Fatalf("lines for the wait: %q", got)
	}
}

// A budget with no log, and a link made with WithLogf(nil), refuse as ever.
func TestSeatLinkRefusalLogNilIsSafe(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	for i := 0; i <= genesisRoom+1; i++ {
		b.take(kindGenesis)
	}
	if _, ok := b.reserve(kindDecide); !ok {
		t.Fatal("the decisions' room was taken")
	}
	_, d, _, _ := genesisGate(t, []string{"read", "genesis"})
	if err := WithLogf(nil)(d); err != nil {
		t.Fatal(err)
	}
	d.budget.now = clock.now
	for i := 0; i < genesisRoom; i++ {
		d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: "0123456789abcdef"})
	}
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: "0123456789abcdef"}); !errors.Is(err, seatapp.ErrBusy) {
		t.Fatalf("a call past the room: %v", err)
	}
}

// Through the link: a call of the interview with no room is busy, is not
// sent, and is told once, and the line names no thread.
func TestSeatLinkRefusalLogOfTheInterview(t *testing.T) {
	_, d, _, _ := genesisGate(t, []string{"read", "genesis"})
	var l logLines
	if err := WithLogf(l.logf)(d); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: decideNow}
	d.budget.now = clock.now
	const thread = "0123456789abcdef"
	for i := 0; i < genesisRoom; i++ {
		d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: thread})
	}
	for i := 0; i < 3; i++ {
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: thread}); !errors.Is(err, seatapp.ErrBusy) {
			t.Fatalf("a call past the room: %v", err)
		}
	}
	got := l.all()
	if len(got) != 1 || !strings.Contains(got[0], "kind genesis (10 of 10 in the last 80 seconds)") {
		t.Fatalf("lines: %q", got)
	}
	clock.advance(RefusalLogEvery)
	d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: thread})
	got = l.all()
	if len(got) != 2 || !strings.HasSuffix(got[1], "; 2 more since the last line\n") {
		t.Fatalf("lines: %q", got)
	}
	for _, line := range got {
		if strings.Contains(line, thread) {
			t.Errorf("a line holds the call: %q", line)
		}
	}
}
