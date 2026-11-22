package seatpair

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
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
	c, err := confirm(p, clk.now)
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
	if _, err := confirm(p, clk.now); err != nil {
		t.Fatal(err)
	}
	if c, err := confirm(p, clk.now); err != ErrNotPending || c.ID != nil || c.SPKI != nil || c.Kind != "" {
		t.Errorf("second Confirm = %v, %v", c, err)
	}
	// The code is used: it brings no second credential.
	if err := present(p, code, idA, spkiA, clk.now); err != ErrUsed {
		t.Errorf("Present with the used code = %v, want ErrUsed", err)
	}
	if _, err := confirm(p, clk.now); err != ErrNotPending {
		t.Errorf("Confirm after that = %v, want ErrNotPending", err)
	}
}

func TestConfirmOnlyAfterPending(t *testing.T) {
	p, clk := newTestPairing()
	if _, err := confirm(p, clk.now); err != ErrNotPending {
		t.Errorf("Confirm with no pairing = %v, want ErrNotPending", err)
	}
	code, _ := p.Start(false)
	if _, err := confirm(p, clk.now); err != ErrNotPending {
		t.Errorf("Confirm of an open pairing = %v, want ErrNotPending", err)
	}
	// A wrong code and a bad key bring no pending credential.
	_ = present(p, wrongCode(code), idA, spkiA, clk.now)
	_ = present(p, code, idA, []byte("garbage"), clk.now)
	if c, err := confirm(p, clk.now); err != ErrNotPending {
		t.Errorf("Confirm after refused presentations = %v, %v", c, err)
	}
	if err := reject(p); err != ErrNotPending {
		t.Errorf("Reject of an open pairing = %v, want ErrNotPending", err)
	}
}

func TestConfirmExactlyAtTenMinutes(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	clk.advance(PendingLifetime - time.Nanosecond)
	if _, err := confirm(p, clk.now); err != nil {
		t.Errorf("Confirm a nanosecond before 10 minutes = %v", err)
	}

	p, clk, _ = pendingPairing(t)
	clk.advance(PendingLifetime)
	if c, err := confirm(p, clk.now); err != ErrPendingExpired || c.ID != nil {
		t.Errorf("Confirm at exactly 10 minutes = %v, %v, want ErrPendingExpired", c, err)
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("phase after an expired Confirm = %v", ph)
	}
	if _, err := confirm(p, clk.now); err != ErrNotPending {
		t.Errorf("Confirm after the expiry = %v, want ErrNotPending", err)
	}
}

func TestConfirmClockEdges(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	if _, err := confirm(p, time.Time{}); err != ErrClock {
		t.Errorf("Confirm with the zero time = %v, want ErrClock", err)
	}
	if _, err := confirm(p, clk.now.Add(-time.Nanosecond)); err != ErrClock {
		t.Errorf("Confirm before the presentation = %v, want ErrClock", err)
	}
	if ph := p.Phase(clk.now); ph != PhasePending {
		t.Fatalf("a clock refusal moved the phase to %v", ph)
	}
	if _, err := confirm(p, clk.now); err != nil {
		t.Errorf("Confirm at the time of the presentation = %v, want nil", err)
	}
}

func TestRejectDiscards(t *testing.T) {
	p, clk, code := pendingPairing(t)
	if err := reject(p); err != nil {
		t.Fatal(err)
	}
	if ph := p.Phase(clk.now); ph != PhaseNone {
		t.Errorf("phase after Reject = %v", ph)
	}
	if _, err := confirm(p, clk.now); err != ErrNotPending {
		t.Errorf("Confirm after Reject = %v, want ErrNotPending", err)
	}
	if p.credentialID != nil || p.spki != nil || p.fingerprint != (Fingerprint{}) {
		t.Error("Reject kept the credential")
	}
	if err := reject(p); err != ErrNotPending {
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
	if err := reject(p); err != nil {
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
	c, err := confirm(p, clk.now)
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
	if _, err := confirm(p, clk.now); err != ErrNotPending {
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
	c, _ := confirm(p, clk.now)
	var texts []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		texts = append(texts, fmt.Sprintf(verb, p), fmt.Sprintf(verb, c), fmt.Sprintf(verb, FingerprintOf(c.ID, c.SPKI)))
	}
	for _, e := range []error{reject(p), present(p, code, idA, spkiA, clk.now)} {
		texts = append(texts, fmt.Sprint(e))
	}
	_, e := confirm(p, clk.now)
	texts = append(texts, fmt.Sprint(e))
	for _, text := range texts {
		if strings.Contains(strings.ToUpper(text), code) {
			t.Errorf("text %q shows the code", text)
		}
	}
}

// confirm and reject are Confirm and Reject with the pending id the pairing
// holds, as the owner would give it back after reading it from the status.
func confirm(p *Pairing, now time.Time) (Credential, error) {
	return p.Confirm(now, p.pendingID)
}

func reject(p *Pairing) error { return p.Reject(p.pendingID) }

func TestPendingIDShapeAndRandomness(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	id, ok := p.PendingID(clk.now)
	if !ok || len(id) != PendingIDLength || PendingIDLength != 16 {
		t.Fatalf("PendingID = %q, %v", id, ok)
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("PendingID %q is not lower-case hexadecimal", id)
		}
	}
	// Nothing pending, no id.
	p2, clk2 := newTestPairing()
	if id, ok := p2.PendingID(clk2.now); ok || id != "" {
		t.Errorf("PendingID with nothing pending = %q, %v", id, ok)
	}
	if _, err := p2.Start(false); err != nil {
		t.Fatal(err)
	}
	if id, ok := p2.PendingID(clk2.now); ok || id != "" {
		t.Errorf("PendingID of an open pairing = %q, %v", id, ok)
	}
}

func TestEachPendingCredentialHasAnIDOfItsOwn(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	a, _ := p.PendingID(clk.now)
	code, err := p.Start(true)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := p.PendingID(clk.now); ok || id != "" {
		t.Errorf("a replaced pairing kept an id: %q, %v", id, ok)
	}
	if _, err := p.Present(code, idA, spkiA, clk.now); err != nil {
		t.Fatal(err)
	}
	b, _ := p.PendingID(clk.now)
	if a == b || a == "" || b == "" {
		t.Errorf("the same credential presented twice got ids %q and %q", a, b)
	}
}

func TestPendingIDFromARandomSourceThatFails(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	p.rand = iotest.ErrReader(errors.New("no entropy"))
	if _, err := p.Present(code, idA, spkiA, clk.now); err != ErrRandom {
		t.Fatalf("Present with a failing source = %v, want ErrRandom", err)
	}
	// Nothing was spent: the code is not used and nothing is pending.
	if p.code.Used() || p.Phase(clk.now) != PhaseOpen {
		t.Error("a failed draw spent the code or moved the pairing")
	}
	p.rand = &seq{next: 0x40}
	if _, err := p.Present(code, idA, spkiA, clk.now); err != nil {
		t.Errorf("Present after the source recovered = %v", err)
	}
}

func TestConfirmAndRejectNameTheCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(p *Pairing, now time.Time, id string) error
	}{
		{"confirm", func(p *Pairing, now time.Time, id string) error { _, err := p.Confirm(now, id); return err }},
		{"reject", func(p *Pairing, _ time.Time, id string) error { return p.Reject(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, clk, _ := pendingPairing(t)
			good := p.pendingID
			// The id of an earlier pending credential, or any other one.
			for _, other := range []string{"", "0000000000000000", good[:15], good + "0", strings.ToUpper(good) + "x", flip(good)} {
				if err := tc.run(p, clk.now, other); err != ErrPendingID {
					t.Errorf("id %q = %v, want ErrPendingID", other, err)
				}
				if ph := p.Phase(clk.now); ph != PhasePending {
					t.Fatalf("id %q moved the phase to %v", other, ph)
				}
				if id, ok := p.PendingID(clk.now); !ok || id != good {
					t.Fatalf("id %q changed the pending credential", other)
				}
			}
			if err := tc.run(p, clk.now, good); err != nil {
				t.Errorf("the right id = %v", err)
			}
			if ph := p.Phase(clk.now); ph != PhaseNone {
				t.Errorf("phase after the right id = %v", ph)
			}
		})
	}
}

func flip(id string) string {
	b := []byte(id)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}

// A replaced pairing's credential: the owner compared A, B replaced it, and
// the confirmation of A must not confirm B.
func TestAConfirmationOfAReplacedCredentialConfirmsNothing(t *testing.T) {
	p, clk, _ := pendingPairing(t)
	a := p.pendingID
	code, err := p.Start(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Present(code, idB, spkiB, clk.now); err != nil {
		t.Fatal(err)
	}
	if c, err := p.Confirm(clk.now, a); err != ErrPendingID || c.ID != nil {
		t.Fatalf("Confirm with A's id while B is pending = %v, %v", c, err)
	}
	if err := p.Reject(a); err != ErrPendingID {
		t.Fatalf("Reject with A's id while B is pending = %v", err)
	}
	if ph := p.Phase(clk.now); ph != PhasePending {
		t.Fatalf("phase = %v, want B still pending", ph)
	}
	if c, err := confirm(p, clk.now); err != nil || !bytes.Equal(c.ID, idB) {
		t.Fatalf("Confirm with B's own id = %v, %v", c, err)
	}
}

// With no credential pending the answer is the one for that, whatever id
// is given.
func TestConfirmAndRejectWithNothingPendingNameNoCredential(t *testing.T) {
	p, clk := newTestPairing()
	if _, err := p.Confirm(clk.now, "0123456789abcdef"); err != ErrNotPending {
		t.Errorf("Confirm = %v, want ErrNotPending", err)
	}
	if err := p.Reject("0123456789abcdef"); err != ErrNotPending {
		t.Errorf("Reject = %v, want ErrNotPending", err)
	}
}
