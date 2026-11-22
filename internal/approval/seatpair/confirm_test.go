package seatpair

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pendingPairing is a pairing with idA and spkiA presented at t0+1 minute.
func pendingPairing(t *testing.T) (*Pairing, *fakeClock, string) {
	t.Helper()
	p, clk := newTestPairing()
	code, err := p.Start(false)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	if _, err := p.Present(code, idA, spkiA, clk.now); err != nil {
		t.Fatal(err)
	}
	return p, clk, code
}

func TestPresentReturnsTheFingerprint(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	f, err := p.Present(code, idA, spkiA, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	if want := FingerprintOf(idA, spkiA); f != want {
		t.Errorf("fingerprint = %v, want %v", f, want)
	}
	got, ok := p.Pending(clk.now)
	if !ok || got != f {
		t.Errorf("Pending = %v, %v", got, ok)
	}
}

func TestPresentRefusedReturnsNoFingerprint(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	f, err := p.Present(wrongCode(code), idA, spkiA, clk.now)
	if err == nil || f != (Fingerprint{}) {
		t.Errorf("refused Present gave %v, %v", f, err)
	}
	if _, ok := p.Pending(clk.now); ok {
		t.Error("Pending is true with nothing presented")
	}
}

func TestConfirmMakesTheCredential(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	clk.advance(time.Minute)
	c, err := p.Confirm(clk.now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.ID, idA) || !bytes.Equal(c.SPKI, spkiA) {
		t.Error("the credential is not what was presented")
	}
	if c.Kind != KindUnverified || KindUnverified != "unverified" {
		t.Errorf("kind = %q", c.Kind)
	}
	if !c.PairedAt.Equal(clk.now) {
		t.Errorf("PairedAt = %v, want the time of the confirmation %v", c.PairedAt, clk.now)
	}
	if c.Fingerprint() != FingerprintOf(idA, spkiA) {
		t.Error("the credential's fingerprint differs")
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("phase after Confirm = %v, want none", ph)
	}
	if _, ok := p.Pending(clk.now); ok {
		t.Error("still pending after Confirm")
	}
}

func TestConfirmOnlyOnce(t *testing.T) {
	p, clk, code := pendingPairing(t)
	if _, err := p.Confirm(clk.now); err != nil {
		t.Fatal(err)
	}
	if c, err := p.Confirm(clk.now); err != ErrNotPending || c.ID != nil || c.SPKI != nil || c.Kind != "" {
		t.Errorf("second Confirm = %v, %v", c, err)
	}
	// The code is used: it brings no second credential.
	if err := present(p, code, idA, spkiA, clk.now); err != ErrUsed {
		t.Errorf("Present with the used code = %v, want ErrUsed", err)
	}
	if _, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm after that = %v, want ErrNotPending", err)
	}
}

func TestConfirmOnlyAfterPending(t *testing.T) {
	p, clk := newTestPairing()
	if _, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm with no pairing = %v, want ErrNotPending", err)
	}
	code, _ := p.Start(false)
	if _, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm of an open pairing = %v, want ErrNotPending", err)
	}
	// A wrong code and a bad key bring no pending credential.
	_ = present(p, wrongCode(code), idA, spkiA, clk.now)
	_ = present(p, code, idA, []byte("garbage"), clk.now)
	if c, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm after refused presentations = %v, %v", c, err)
	}
	if err := p.Reject(); err != ErrNotPending {
		t.Errorf("Reject of an open pairing = %v, want ErrNotPending", err)
	}
}

func TestConfirmExactlyAtTenMinutes(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	clk.advance(PendingLifetime - time.Nanosecond)
	if _, err := p.Confirm(clk.now); err != nil {
		t.Errorf("Confirm a nanosecond before 10 minutes = %v", err)
	}

	p, clk, _ = pendingPairing(t)
	clk.advance(PendingLifetime)
	if c, err := p.Confirm(clk.now); err != ErrPendingExpired || c.ID != nil {
		t.Errorf("Confirm at exactly 10 minutes = %v, %v, want ErrPendingExpired", c, err)
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("phase after an expired Confirm = %v", ph)
	}
	if _, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm after the expiry = %v, want ErrNotPending", err)
	}
}

func TestConfirmClockEdges(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	if _, err := p.Confirm(time.Time{}); err != ErrClock {
		t.Errorf("Confirm with the zero time = %v, want ErrClock", err)
	}
	if _, err := p.Confirm(clk.now.Add(-time.Nanosecond)); err != ErrClock {
		t.Errorf("Confirm before the presentation = %v, want ErrClock", err)
	}
	if ph := p.Phase(clk.now); ph != PhasePending {
		t.Fatalf("a clock refusal moved the phase to %v", ph)
	}
	if _, err := p.Confirm(clk.now); err != nil {
		t.Errorf("Confirm at the time of the presentation = %v, want nil", err)
	}
}

func TestRejectDiscards(t *testing.T) {
	p, clk, code := pendingPairing(t)
	if err := p.Reject(); err != nil {
		t.Fatal(err)
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("phase after Reject = %v", ph)
	}
	if _, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm after Reject = %v, want ErrNotPending", err)
	}
	if p.credentialID != nil || p.spki != nil || p.fingerprint != (Fingerprint{}) {
		t.Error("Reject kept the credential")
	}
	if err := p.Reject(); err != ErrNotPending {
		t.Errorf("second Reject = %v, want ErrNotPending", err)
	}
	// The rejected code is used and gives no new try; a new Start does.
	if err := present(p, code, idA, spkiA, clk.now); err != ErrUsed {
		t.Errorf("Present with the rejected code = %v, want ErrUsed", err)
	}
	code2, err := p.Start(false)
	if err != nil {
		t.Fatalf("Start after Reject = %v", err)
	}
	if err := present(p, code2, idA, spkiA, clk.now); err != nil {
		t.Errorf("Present with the new code = %v", err)
	}
}

func TestRejectAfterExpiry(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	clk.advance(time.Hour)
	if err := p.Reject(); err != nil {
		t.Errorf("Reject of an expired credential = %v", err)
	}
}

func TestPendingEndsWithTheTime(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	clk.advance(PendingLifetime - time.Nanosecond)
	if _, ok := p.Pending(clk.now); !ok {
		t.Error("not pending a nanosecond before 10 minutes")
	}
	clk.advance(time.Nanosecond)
	if _, ok := p.Pending(clk.now); ok {
		t.Error("still pending at exactly 10 minutes")
	}
}

func TestConfirmGivesCopies(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	c, err := p.Confirm(clk.now)
	if err != nil {
		t.Fatal(err)
	}
	c.ID[0] ^= 0xFF
	c.SPKI[0] ^= 0xFF
	if idA[0] == c.ID[0] || spkiA[0] == c.SPKI[0] {
		t.Error("the credential shares bytes with the caller's slices")
	}
}

func TestStartReplacesAPendingCredential(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	if _, err := p.Start(true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Confirm(clk.now); err != ErrNotPending {
		t.Errorf("Confirm after the pairing was replaced = %v, want ErrNotPending", err)
	}
}

func TestNewErrorsAreDistinct(t *testing.T) {
	if ErrNotPending == ErrPendingExpired || ErrNotPending == ErrPending || ErrPendingExpired == ErrPending {
		t.Error("errors of the pending state are not distinct")
	}
	for _, e := range []Error{ErrNotPending, ErrPendingExpired} {
		if !strings.HasPrefix(e.Error(), "seatpair: ") {
			t.Errorf("%q", e)
		}
	}
}

// TestOnlyConfirmMakesACredential reads the package's source: the composite
// literal of a Credential is in one place, the method Confirm, and nowhere
// else outside the tests.
func TestOnlyConfirmMakesACredential(t *testing.T) {
	found := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "Credential" {
					found++
					if fn.Name.Name != "Confirm" {
						t.Errorf("%s: Credential made in %s", filepath.ToSlash(path), fn.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// One in Confirm, and the empty ones of its refusals: Credential{}.
	if found < 1 {
		t.Error("no Credential literal found: the test reads nothing")
	}
}

func TestFullPairingTextsHideTheCode(t *testing.T) {
	p, clk, code := pendingPairing(t)
	c, _ := p.Confirm(clk.now)
	var texts []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		texts = append(texts, fmt.Sprintf(verb, p), fmt.Sprintf(verb, c), fmt.Sprintf(verb, FingerprintOf(c.ID, c.SPKI)))
	}
	for _, e := range []error{p.Reject(), present(p, code, idA, spkiA, clk.now)} {
		texts = append(texts, fmt.Sprint(e))
	}
	_, e := p.Confirm(clk.now)
	texts = append(texts, fmt.Sprint(e))
	for _, text := range texts {
		if strings.Contains(strings.ToUpper(text), code) {
			t.Errorf("text %q shows the code", text)
		}
	}
}
