package leadregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBeginLaunchRecordsBootIdentity(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	role := lead(t, "proj")
	ok := okf(t)
	ok(r.Define(role, ManagerAtaniq, goodProfile()))

	// The old call records none, and the journal has no boot field.
	in, err := r.BeginLaunch(role)
	if err != nil || in.BootIdentity != "" {
		t.Fatalf("BeginLaunch: %+v %v", in, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, FileName)); bytes.Contains(b, []byte("boot_id")) {
		t.Fatalf("BeginLaunch journaled a boot identity:\n%s", b)
	}
	ok(r.RecordOutcome(role, in.ID, 1, OutcomeFailed, UnitEvidence{}))
	ok(r.SetState(role, stopProof(1)))

	// The new call records it, in the intent, and replay gives it back.
	boot := "5f3a9c1e-77d2-4b0a-9a53-0c1d2e3f4a5b"
	in, err = r.BeginLaunchWith(role, LaunchOptions{BootIdentity: boot})
	if err != nil || in.BootIdentity != boot || in.Epoch != 2 {
		t.Fatalf("BeginLaunchWith: %+v %v", in, err)
	}
	e, _ := r.Get(role)
	if e.Intent.BootIdentity != boot {
		t.Fatalf("entry %+v", e.Intent)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Contains(b, []byte(`"boot_id":"`+boot+`"`)) {
		t.Fatalf("not journaled:\n%s", b)
	}
	// It is not the unit evidence: the outcome does not touch it, and the next
	// launch without one records none.
	ok(r.RecordOutcome(role, in.ID, 2, OutcomeLaunched, UnitEvidence{BootID: "other"}))
	if e, _ := r.Get(role); e.Intent.BootIdentity != boot || e.Unit.BootID != "other" {
		t.Fatalf("entry %+v %+v", e.Intent, e.Unit)
	}
	want := snapshot(t, r)
	r.Close()
	r = openReg(t, dir)
	if !sameEntries(snapshot(t, r), want) {
		t.Fatal("replay differs")
	}
	ok(r.SetState(role, stopProof(2)))
	if in, err := r.BeginLaunch(role); err != nil || in.BootIdentity != "" {
		t.Fatalf("BeginLaunch after a boot identity: %+v %v", in, err)
	}
}

func TestBeginLaunchWithRefusals(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	role := lead(t, "proj")
	okf(t)(r.Define(role, ManagerAtaniq, goodProfile()))
	size := journalSize(t, dir)
	for name, boot := range map[string]string{
		"too long":          strings.Repeat("b", MaxBootIdentityBytes+1),
		"newline":           "a\nb",
		"NUL":               "a\x00",
		"tab":               "a\tb",
		"invalid UTF-8":     "\xff",
		"line separator":    "a b",
		"escape":            "\x1b[0m",
		"carriage return":   "a\r",
		"unicode line feed": "a\u0085",
	} {
		if _, err := r.BeginLaunchWith(role, LaunchOptions{BootIdentity: boot}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if journalSize(t, dir) != size {
		t.Fatal("a refused launch wrote")
	}
	if e, _ := r.Get(role); e.Epoch != 0 || e.Intent != nil {
		t.Fatalf("a refused launch took an epoch: %+v", e)
	}
	// The bound itself is accepted, and the epoch is the first.
	in, err := r.BeginLaunchWith(role, LaunchOptions{BootIdentity: strings.Repeat("b", MaxBootIdentityBytes)})
	if err != nil || in.Epoch != 1 {
		t.Fatalf("at the bound: %+v %v", in, err)
	}
	// Every other refusal of BeginLaunch holds, and records nothing.
	size = journalSize(t, dir)
	if _, err := r.BeginLaunchWith(role, LaunchOptions{BootIdentity: "x"}); !errors.Is(err, ErrLaunchPending) {
		t.Fatalf("pending: %v", err)
	}
	if _, err := r.BeginLaunchWith(lead(t, "nope"), LaunchOptions{BootIdentity: "x"}); !errors.Is(err, ErrNoRole) {
		t.Fatalf("undefined: %v", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("a refused launch wrote")
	}
	r.Close()
	if _, err := r.BeginLaunchWith(role, LaunchOptions{BootIdentity: "x"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

// TestBootIdentityOfACoordinatorLaunch: the two additions to the intent sit
// side by side.
func TestBootIdentityOfACoordinatorLaunch(t *testing.T) {
	g := newCoordRig(t)
	in, err := g.r.BeginLaunchWith(g.coord, LaunchOptions{BootIdentity: "boot-1"})
	if err != nil || in.BootIdentity != "boot-1" || in.Identity != g.coord.Identity(1, 1) || in.ParentEpoch != 1 {
		t.Fatalf("intent %+v %v", in, err)
	}
	want := snapshot(t, g.r)
	g.reopen()
	if !sameEntries(snapshot(t, g.r), want) {
		t.Fatal("replay differs")
	}
}

func TestBootIdentityCorruption(t *testing.T) {
	g := newCoordRig(t)
	g.launch()
	data, _ := os.ReadFile(filepath.Join(g.dir, FileName))
	lines := journalLines(data)
	// lines: 0 header, 1 define lead, 2 begin lead, ...; replace the lead's
	// launch by one with a bad boot identity.
	for name, boot := range map[string]string{
		"too long": strings.Repeat("b", MaxBootIdentityBytes+1),
		"control":  "a\nb",
	} {
		bad := &beginPayload{IntentID: strings.Repeat("ab", 16), Epoch: 1, UnitName: g.lead.UnitName(),
			ProfileDigest: goodProfile().Digest(), BootID: boot}
		line := craft(t, lines, 2, 2, kindLaunchBegin, g.lead, bad)
		var ce *CorruptError
		err := openErr(t, append(bytes.Join(lines[:2], nil), line...))
		if !errors.As(err, &ce) || ce.Line != 3 || !strings.Contains(ce.Reason, "boot identity") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// And the same record with a good boot identity opens.
	good := &beginPayload{IntentID: strings.Repeat("ab", 16), Epoch: 1, UnitName: g.lead.UnitName(),
		ProfileDigest: goodProfile().Digest(), BootID: "boot"}
	line := craft(t, lines, 2, 2, kindLaunchBegin, g.lead, good)
	if err := openErr(t, append(bytes.Join(lines[:2], nil), line...)); err != nil {
		t.Errorf("good boot identity: %v", err)
	}
}
