package seatpair

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// fakeClock is a clock a test moves.
type fakeClock struct{ now time.Time }

func (f *fakeClock) read() time.Time { return f.now }

func (f *fakeClock) advance(d time.Duration) { f.now = f.now.Add(d) }

// seq is a random source that gives the bytes of n, n+1, ... in turn, so
// each Start makes a different, known code.
type seq struct{ next byte }

func (s *seq) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = s.next
		s.next++
	}
	return len(p), nil
}

var (
	idA   = bytes.Repeat([]byte{0xA1}, 32)
	spkiA = spkiOfScalar(scalarOf(0x11))
	idB   = bytes.Repeat([]byte{0xB2}, 32)
	spkiB = spkiOfScalar(scalarOf(0x12))
)

func newTestPairing() (*Pairing, *fakeClock) {
	clk := &fakeClock{now: t0}
	return NewPairing(&seq{}, clk.read), clk
}

func TestPairingStartAndPresent(t *testing.T) {
	p, clk := newTestPairing()
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Fatalf("new pairing in phase %v", ph)
	}
	code, err := p.Start(false)
	if err != nil {
		t.Fatal(err)
	}
	if code != countingCode {
		t.Fatalf("code = %q", code)
	}
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Fatalf("phase after Start = %v", ph)
	}
	clk.advance(time.Minute)
	if err := present(p, code, idA, spkiA, clk.now); err != nil {
		t.Fatal(err)
	}
	if ph := p.Phase(clk.now); ph != PhasePending {
		t.Fatalf("phase after Present = %v", ph)
	}
}

func TestPresentBeforeStart(t *testing.T) {
	p, clk := newTestPairing()
	if err := present(p, countingCode, idA, spkiA, clk.now); err != ErrNoPairing {
		t.Errorf("Present before Start = %v, want ErrNoPairing", err)
	}
}

func TestPresentWrongCodes(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	for i := 1; i <= MaxTries; i++ {
		if err := present(p, wrongCode(code), idA, spkiA, clk.now); err != ErrWrongCode {
			t.Fatalf("wrong try %d = %v", i, err)
		}
	}
	if err := present(p, wrongCode(code), idA, spkiA, clk.now); err != ErrBurnt {
		t.Errorf("sixth wrong try = %v, want ErrBurnt", err)
	}
	if err := present(p, code, idA, spkiA, clk.now); err != ErrBurnt {
		t.Errorf("right code after five wrong tries = %v, want ErrBurnt", err)
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("a burnt pairing is in phase %v, want none", ph)
	}
}

func TestPresentExpiry(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	clk.advance(CodeLifetime - time.Nanosecond)
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Errorf("phase just before 10 minutes = %v, want open", ph)
	}
	clk.advance(time.Nanosecond)
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("phase at 10 minutes = %v, want none", ph)
	}
	if err := present(p, code, idA, spkiA, clk.now); err != ErrExpired {
		t.Errorf("Present at exactly 10 minutes = %v, want ErrExpired", err)
	}
}

func TestPresentJustBeforeExpiry(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	clk.advance(CodeLifetime - time.Nanosecond)
	if err := present(p, code, idA, spkiA, clk.now); err != nil {
		t.Errorf("Present a nanosecond before 10 minutes = %v", err)
	}
}

func TestSecondPresentWhilePending(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	if err := present(p, code, idA, spkiA, clk.now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{code, wrongCode(code), ""} {
		if err := present(p, c, idA, spkiA, clk.now); err != ErrPending {
			t.Errorf("second Present(%q) = %v, want ErrPending", c, err)
		}
	}
	if p.code.Tries() != 0 {
		t.Errorf("a refused second Present counted %d tries", p.code.Tries())
	}
	if ph := p.Phase(clk.now); ph != PhasePending {
		t.Errorf("phase = %v, still pending expected", ph)
	}
}

func TestPresentCopiesItsInput(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	id := append([]byte(nil), idA...)
	spki := append([]byte(nil), spkiA...)
	if err := present(p, code, id, spki, clk.now); err != nil {
		t.Fatal(err)
	}
	id[0] ^= 0xFF
	spki[0] ^= 0xFF
	if !bytes.Equal(p.credentialID, idA) || !bytes.Equal(p.spki, spkiA) {
		t.Error("Present kept the caller's slices")
	}
}

func TestStartWhileOpen(t *testing.T) {
	p, clk := newTestPairing()
	first, _ := p.Start(false)
	if _, err := p.Start(false); err != ErrPairingOpen {
		t.Fatalf("second Start = %v, want ErrPairingOpen", err)
	}
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Errorf("a refused Start moved the phase to %v", ph)
	}
	second, err := p.Start(true)
	if err != nil {
		t.Fatalf("Start(true) = %v", err)
	}
	if second == first {
		t.Fatal("a replacing Start gave the same code")
	}
	if err := present(p, first, idA, spkiA, clk.now); err != ErrWrongCode {
		t.Errorf("the replaced code = %v, want ErrWrongCode", err)
	}
	if err := present(p, second, idA, spkiA, clk.now); err != nil {
		t.Errorf("the new code = %v", err)
	}
}

func TestStartWhilePending(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	_ = present(p, code, idA, spkiA, clk.now)
	if _, err := p.Start(false); err != ErrPairingOpen {
		t.Fatalf("Start while pending = %v, want ErrPairingOpen", err)
	}
	if ph := p.Phase(clk.now); ph != PhasePending {
		t.Errorf("a refused Start moved the phase to %v", ph)
	}
	if _, err := p.Start(true); err != nil {
		t.Fatalf("Start(true) while pending = %v", err)
	}
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Errorf("phase after replacing = %v, want open", ph)
	}
	if p.credentialID != nil || p.spki != nil {
		t.Error("a replaced pairing kept its credential")
	}
}

func TestStartWhenOver(t *testing.T) {
	// A code past its time, a burnt code and a pending credential past its
	// time are not open: Start needs no replace.
	p, clk := newTestPairing()
	if _, err := p.Start(false); err != nil {
		t.Fatal(err)
	}
	clk.advance(CodeLifetime)
	if _, err := p.Start(false); err != nil {
		t.Errorf("Start after expiry = %v", err)
	}
	for i := 0; i < MaxTries; i++ {
		_ = present(p, "nope", idA, spkiA, clk.now)
	}
	if _, err := p.Start(false); err != nil {
		t.Errorf("Start after a burnt code = %v", err)
	}
	if ph := p.Phase(clk.now); ph != PhaseOpen {
		t.Errorf("phase = %v, want open", ph)
	}
}

func TestPendingLifetimeExactly(t *testing.T) {
	for _, c := range []struct {
		name  string
		after time.Duration
		open  bool
	}{
		{"a nanosecond before", PendingLifetime - time.Nanosecond, true},
		{"exactly 10 minutes", PendingLifetime, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, clk := newTestPairing()
			code, _ := p.Start(false)
			clk.advance(5 * time.Minute)
			if err := present(p, code, idA, spkiA, clk.now); err != nil {
				t.Fatal(err)
			}
			clk.advance(c.after)
			ph := p.Phase(clk.now)
			if c.open && ph != PhasePending || !c.open && ph != PhaseNone {
				t.Errorf("phase = %v", ph)
			}
			_, err := p.Start(false)
			if c.open && err != ErrPairingOpen || !c.open && err != nil {
				t.Errorf("Start(false) = %v", err)
			}
		})
	}
}

func TestPresentAfterPendingEnds(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	_ = present(p, code, idA, spkiA, clk.now)
	clk.advance(PendingLifetime)
	// The code is used; a second presentation is refused as such.
	if err := present(p, code, idA, spkiA, clk.now); err != ErrUsed {
		t.Errorf("Present after the pending time = %v, want ErrUsed", err)
	}
	if p.credentialID != nil {
		t.Error("an ended pending credential was kept")
	}
}

func TestPairingClockEdges(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	if err := present(p, code, idA, spkiA, time.Time{}); err != ErrClock {
		t.Errorf("zero time = %v, want ErrClock", err)
	}
	if err := present(p, wrongCode(code), idA, spkiA, clk.now.Add(-time.Second)); err != ErrClock {
		t.Errorf("time before the start = %v, want ErrClock", err)
	}
	if p.code.Tries() != 0 {
		t.Error("a clock refusal counted a try")
	}
	clk.now = time.Time{}
	if _, err := p.Start(true); err != ErrClock {
		t.Errorf("Start with a zero clock = %v, want ErrClock", err)
	}
}

func TestStartRandomFailureKeepsThePairing(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := NewPairing(&seq{}, clk.read)
	code, _ := p.Start(false)
	p.rand = iotest.ErrReader(errors.New("no entropy"))
	if _, err := p.Start(true); err != ErrRandom {
		t.Fatalf("Start with a failing source = %v, want ErrRandom", err)
	}
	// Present draws the pending id from the same source, so it is given one.
	p.rand = &seq{next: 0x80}
	if err := present(p, code, idA, spkiA, clk.now); err != nil {
		t.Errorf("the old code after a failed replace = %v", err)
	}
}

func TestNewPairingDefaults(t *testing.T) {
	p := NewPairing(nil, nil)
	code, err := p.Start(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != CodeLength {
		t.Errorf("code %q", code)
	}
	if err := present(p, code, idA, spkiA, time.Now()); err != nil {
		t.Errorf("Present with the default clock = %v", err)
	}
}

func TestPairingTextsHideTheCode(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	var texts []string
	add := func(err error) { texts = append(texts, fmt.Sprint(err), fmt.Sprintf("%#v", err)) }
	add(present(p, wrongCode(code), idA, spkiA, clk.now))
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		texts = append(texts, fmt.Sprintf(verb, p), fmt.Sprintf(verb, p.code), fmt.Sprintf(verb, PhaseOpen))
	}
	add(present(p, code, idA, spkiA, clk.now))
	add(present(p, code, idA, spkiA, clk.now))
	_, err := p.Start(false)
	add(err)
	for i := 0; i < MaxTries+1; i++ {
		_, _ = p.Start(true)
		add(present(p, "WRONG", idA, spkiA, clk.now))
	}
	texts = append(texts, fmt.Sprintf("%v", p), fmt.Sprintf("%+v", p))
	for _, text := range texts {
		if strings.Contains(text, code) || strings.Contains(text, strings.ToLower(code)) {
			t.Errorf("text %q shows the code", text)
		}
	}
}

// present is Present without the fingerprint, for the tests of the code.
func present(p *Pairing, code string, id, spki []byte, now time.Time) error {
	_, err := p.Present(code, id, spki, now)
	return err
}
