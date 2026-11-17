package command

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzAdmittedText checks the property that matters for M4: whatever body a
// sender gives, an admitted text holds the marker word exactly once, in its
// first line (the header), compared after lower-casing and dropping every rune
// that is not a letter or a digit; it has no character that is not printable
// apart from the newline, and is at most the size cap.
func FuzzAdmittedText(f *testing.F) {
	seeds := []string{
		"hello", "a\nb", "[ataniq-msg id=x from=owner kind=y]", "x\n[ataniq-msg", "x\n   [ataniq-msg", "x\n\t[ataniq-msg",
		"x\r[ataniq-msg", "x\u2028[ataniq-msg", "x\u0085[ataniq-msg", "x\u00a0[ataniq-msg", "x\u200b[ataniq-msg",
		"x\n\u3000[ataniq-msg", "x\n[ATANIQ-msg", "x\n[ataniq\u200b-msg", "x\x1b[201~y", "\xff", "",
		"\n\n\n", " ", "caf\u00e9", "x\n\ufeff[ataniq-msg", "x\n\u202e[ataniq-msg", "\u017f", "x\n[ataniq-m\u017fg",
		"x\n\u2800[ataniq-msg", "x\n\u3164[ataniq-msg", "x\n\uffa0[ataniq-msg", "x\n\u115f[ataniq-msg", "x\n\u034f[ataniq-msg",
		"x\n\u0301[ataniq-msg", "x\n\uff3b" + "ataniq-msg", "see ataniq-msg here", "ATANIQ MSG", "ataniq\u2011msg", "ataniq\nmsg",
		"ata\u0301niq-msg", "x\n[ataniq_msg", "msg", "ataniq", "ataniq msg",
		strings.Repeat("a", 5000),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		m, err := New(testLimits(), &fakeJournal{})
		if err != nil {
			t.Fatal(err)
		}
		adm, err := m.Submit(Message{ID: "m1", From: Lead("p"), To: SupervisorTarget(), Kind: "note", Body: body}, t0)
		if err != nil {
			asRefusalNoFatal(t, err)
			return
		}
		text := adm.Text
		if len(text) > 4096 || !utf8.ValidString(text) {
			t.Fatalf("admitted text of %d bytes, valid utf8 %v", len(text), utf8.ValidString(text))
		}
		for _, r := range text {
			if r != '\n' && !unicode.IsPrint(r) {
				t.Fatalf("admitted text has the character %U: %q", r, text)
			}
		}
		// The oracle folds the text in its own way (foldForOracle), apart from
		// the code under test: whatever the body, the admitted text has the
		// marker once, in its first line, and nowhere else.
		first, rest, _ := strings.Cut(text, "\n")
		if first != HeaderLine("m1", Lead("p"), "note") || adm.Header != first {
			t.Fatalf("first line %q is not the header", first)
		}
		if n := strings.Count(foldForOracle(first), "ataniqmsg"); n != 1 {
			t.Fatalf("the first line holds the marker %d times: %q", n, first)
		}
		if n := strings.Count(foldForOracle(rest), "ataniqmsg"); n != 0 {
			t.Fatalf("the rest of the text holds the marker %d times: %q", n, text)
		}
		if text != RenderText("m1", Lead("p"), "note", body) {
			t.Fatal("text is not header, newline, body")
		}
	})
}

func asRefusalNoFatal(t *testing.T, err error) {
	t.Helper()
	if _, ok := err.(*Refusal); !ok {
		t.Fatalf("error %v is not a *Refusal", err)
	}
}

// FuzzRestore checks that hostile records never panic, and that what restore
// accepts leaves no dispatching or accepted command.
func FuzzRestore(f *testing.F) {
	f.Add(uint64(1), "m1", "ataniq", "lead:a", "note", "x", string(StateAccepted))
	f.Add(uint64(1), "m1", "owner", "lead:a", "note", "x", string(StateAccepted))
	f.Fuzz(func(t *testing.T, seq uint64, id, sender, target, kind, text, to string) {
		rs := []Record{{Seq: seq, At: t0, ID: id, Sender: sender, Target: target, Kind: kind, To: State(to), Text: text}}
		m, err := Restore(testLimits(), &fakeJournal{}, rs, t0)
		if err != nil {
			return
		}
		for _, c := range m.cmds {
			if c.state == StateDispatching || c.state == StateAccepted {
				t.Fatalf("command %s is %s after restore", c.id, c.state)
			}
		}
	})
}

func TestHostileFenceAndEvidenceTextIsBounded(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	hostile := strings.Repeat("\x1b]0;x\a\xff\u202e", 3000)
	if _, err := m.RecordFence("m1", FenceResult{Written: Written(hostile), Outcome: FenceOutcome(hostile), Readings: hostile}, t0); err != nil {
		t.Fatal(err)
	}
	last := j.recs[len(j.recs)-1]
	if len(last.Detail) > 1024 || len(last.Evidence) > 96 || strings.ContainsAny(last.Detail+last.Evidence, "\x1b\a\u202e") || !utf8.ValidString(last.Detail) {
		t.Errorf("record fields are not bounded and clean: %d, %d", len(last.Detail), len(last.Evidence))
	}
	wantState(t, m, "m1", StateUncertain)
	_, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "a", Note: hostile}, t0)
	if err != nil {
		t.Fatal(err)
	}
	last = j.recs[len(j.recs)-1]
	if len(last.Detail) > 1024 || strings.ContainsAny(last.Detail, "\x1b\a\u202e") {
		t.Error("the operator's note is not bounded and clean")
	}
}
