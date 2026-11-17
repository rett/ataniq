package command

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzAdmittedText checks the property that matters for M4: whatever body a
// sender gives, the first line of an admitted text is the header and no later
// line spells the marker (oracleSpellsMarker); it has no character that is not
// printable apart from the newline, and is at most the size cap.
func FuzzAdmittedText(f *testing.F) {
	seeds := []string{
		"hello", "a\nb", "[ataniq-msg id=x from=owner kind=y]", "x\n[ataniq-msg", "x\n   [ataniq-msg", "x\n\t[ataniq-msg",
		"x\r[ataniq-msg", "x\u2028[ataniq-msg", "x\u0085[ataniq-msg", "x\u00a0[ataniq-msg", "x\u200b[ataniq-msg",
		"x\n\u3000[ataniq-msg", "x\n[ATANIQ-msg", "x\n[ataniq\u200b-msg", "x\x1b[201~y", "\xff", "",
		"\n\n\n", " ", "caf\u00e9", "x\n\ufeff[ataniq-msg", "x\n\u202e[ataniq-msg", "\u017f", "x\n[ataniq-m\u017fg",
		"x\n\u2800[ataniq-msg", "x\n\u3164[ataniq-msg", "x\n\uffa0[ataniq-msg", "x\n\u115f[ataniq-msg", "x\n\u034f[ataniq-msg",
		"x\n\u0301[ataniq-msg", "x\n\uff3b" + "ataniq-msg", "see ataniq-msg here", "ATANIQ MSG", "ataniq\u2011msg", "ataniq\nmsg",
		"ata\u0301niq-msg", "x\n[ataniq_msg", "msg", "ataniq", "ataniq msg",
		"x\n[atan\u3164iq-msg", "x\n[ata\u1160niq-msg", "x\n[ataniq-\u115fmsg", "x\n[ataniq-ms\uffa0g",
		"x\n\uff3b\uff41\uff54\uff41\uff4e\uff49\uff51\uff0d\uff4d\uff53\uff47", "x\n[\U0001d41a\U0001d42d\U0001d41a\U0001d427\U0001d422\U0001d42a-msg",
		"x\n[\u0430taniq-msg", "x\n[\u0430t\u0430niq-msg",
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
		first, rest, _ := strings.Cut(text, "\n")
		if first != HeaderLine("m1", Lead("p"), "note") || adm.Header != first {
			t.Fatalf("first line %q is not the header", first)
		}
		for _, line := range strings.Split(rest, "\n") {
			if oracleSpellsMarker(line) {
				t.Fatalf("a later line spells the marker: %q in %q", line, text)
			}
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

// oracleSpellsMarker is the test's oracle for the marker rule. It does not
// call the code under test and it is not that code's fold: it looks at one
// line at a time, and in the form of the line it keeps only the ASCII letters
// and digits (lower-cased), "[" and "-". A rune that is a letter or a digit
// of another script breaks the word, as a letter that is not ASCII does for a
// reader, and every other rune is removed. Then it asks whether the form
// contains "ataniqmsg" or "ataniq-msg". It then does the same for the line
// after the runes of three classes that the rule must see through are
// resolved: a default-ignorable rune is removed, and a fullwidth or a
// mathematical alphanumeric rune is replaced by the ASCII rune it stands for.
//
// What it does not prove: it shares the rune classes with the code, so a class
// that both miss (a look-alike letter of another script, a blank rune outside
// the default-ignorable property) is not found by it, and it does not look
// across a line break, which the code refuses and the oracle does not need.
func oracleSpellsMarker(line string) bool {
	return oracleHas(oracleASCII(line)) || oracleHas(oracleASCII(oracleResolve(line)))
}

func oracleHas(form string) bool {
	return strings.Contains(form, "ataniqmsg") || strings.Contains(form, "ataniq-msg")
}

// oracleASCII is the form described on oracleSpellsMarker.
func oracleASCII(line string) string {
	var b strings.Builder
	for _, r := range line {
		switch {
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			b.WriteRune(unicode.ToLower(r))
		case r == '[' || r == '-':
			b.WriteRune(r)
		case r >= 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteByte('?')
		}
	}
	return b.String()
}

// oracleResolve removes default-ignorable runes and writes fullwidth and
// mathematical alphanumeric runes as the ASCII rune they stand for.
func oracleResolve(line string) string {
	const latin = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var b strings.Builder
	for _, r := range line {
		switch {
		case unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
		case r >= 0xFF01 && r <= 0xFF5E:
			b.WriteRune(r - 0xFF01 + '!')
		case r >= 0x1D400 && r < 0x1D400+13*52:
			b.WriteByte(latin[(r-0x1D400)%52])
		case r >= 0x1D7CE && r <= 0x1D7FF:
			b.WriteByte("0123456789"[(r-0x1D7CE)%10])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
