package approvals

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

// readyRig opens the first card, reads it through and signs off every
// flagged path, with the 400 ms gone.
func readyRig(t *testing.T, c card.Card, codes ...string) *rig {
	t.Helper()
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24, codes...)
	r.openFirst()
	r.signAll()
	return r
}

// withField returns a rig with the confirm field open.
func withField(t *testing.T) *rig {
	t.Helper()
	r := readyRig(t, testCard("p-1"))
	r.key("a")
	if !r.m.FieldOpen() {
		t.Fatalf("the confirm field did not open:\n%s", r.screen())
	}
	return r
}

func TestApproveFlow(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.seen("approve is ready")
	r.key("a")
	if got := r.code(); got != "7Q4K" {
		t.Fatalf("the code in the title is %q", got)
	}
	r.seen("code> ____")
	r.typ("7q4k") // either case
	r.seen("code> ****")
	// The code is drawn in the title, and nowhere else.
	for y, row := range r.rows() {
		if y != 0 && strings.Contains(row, "7Q4K") {
			t.Fatalf("the code is drawn on row %d: %q", y, row)
		}
	}
	cmd, act := r.m.Update(keyMsg("enter"))
	if act.Kind != None {
		t.Fatalf("Enter reported %+v", act)
	}
	r.seen("sending")
	if len(r.b.decisions) != 0 {
		t.Fatal("Decide was called from Update")
	}
	// Keys while the decision is under way send nothing more.
	r.key("enter")
	r.key("a")
	// x, a typed character and Enter open no deny field and send no deny.
	r.key("x")
	r.typ("n")
	r.key("enter")
	if r.m.FieldOpen() {
		t.Fatal("a field opened while a decision was being sent")
	}
	// No key acts on the card while the decision is under way.
	r.key("esc")
	if !r.m.CardOpen() {
		t.Fatal("Esc left the card while a decision was being sent")
	}
	r.run(cmd)
	if len(r.b.decisions) != 1 {
		t.Fatalf("Decide was called %d times", len(r.b.decisions))
	}
	d := r.b.decisions[0]
	if d.ID != "p-1" || d.BindingSHA256 != "binding-p-1" || !d.Approve || d.Reason != "" || len(d.SignedOff) != 2 {
		t.Fatalf("decision %+v", d)
	}
	if got := fmt.Sprintf("%+v %#v", d, r.actions); strings.Contains(got, "7Q4K") {
		t.Fatalf("the code is in a value handed out: %s", got)
	}
	r.seen("applied: develop moved", "Outcome · after the decision", "state  applied")
	if r.code() != "" {
		t.Fatal("the code is still drawn after the decision")
	}
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("a decided card opened the confirm field")
	}
	r.key("x")
	if r.m.FieldOpen() || len(r.b.decisions) != 1 {
		t.Fatal("a decided card can be denied")
	}
	r.key("esc")
	if r.m.CardOpen() {
		t.Fatal("Esc did not leave the card")
	}
}

func TestApproveWrongCodeClearsAndMakesANewCode(t *testing.T) {
	r := withField(t)
	r.typ("AAAA")
	r.key("enter")
	if len(r.b.decisions) != 0 {
		t.Fatal("a wrong code was sent")
	}
	if r.m.cs.typed != "" || !r.m.FieldOpen() {
		t.Fatalf("the field was not cleared and kept open: %q", r.m.cs.typed)
	}
	if got := r.code(); got != "ACDE" {
		t.Fatalf("the new code is %q, want ACDE", got)
	}
	r.seen("that is not the code: a new code is in the title")
	// The old code no longer works.
	r.typ("7Q4K")
	r.key("enter")
	if len(r.b.decisions) != 0 {
		t.Fatal("the old code was accepted")
	}
	r.typ("FGHJ")
	r.key("enter")
	if len(r.b.decisions) != 1 {
		t.Fatal("the new code was not accepted")
	}
}

func TestApproveShortOrEmptyCodeIsWrong(t *testing.T) {
	for _, typed := range []string{"", "7Q4", "7"} {
		r := withField(t)
		r.typ(typed)
		r.key("enter")
		if len(r.b.decisions) != 0 {
			t.Fatalf("%q was sent", typed)
		}
	}
}

func TestCodeIsRandomPerOpeningAndOnlyInTheTitle(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24, "7Q4K", "ACDE", "3467")
	r.openFirst()
	if r.code() != "7Q4K" {
		t.Fatalf("code %q", r.code())
	}
	r.key("esc")
	r.openFirst()
	if r.code() != "ACDE" {
		t.Fatalf("the second opening has the code %q", r.code())
	}
	// Every code is made of the alphabet.
	for _, c := range []string{"7Q4K", "ACDE", "3467"} {
		if strings.Trim(c, codeAlphabet) != "" {
			t.Fatalf("%q is not in the alphabet", c)
		}
	}
	// Nothing but the title holds it, on any screen of the card.
	for i := 0; i < len(r.m.cs.body)+2; i++ {
		for y, row := range r.rows() {
			if y != 0 && strings.Contains(row, "ACDE") {
				t.Fatalf("the code is on row %d: %q", y, row)
			}
		}
		r.key("down")
	}
}

func TestCodeAlphabetAndSampling(t *testing.T) {
	if len(codeAlphabet) != 26 || strings.Count("ACDEFGHJKLMNPQRTUVWXY34679", "") != len(codeAlphabet)+1 {
		t.Fatalf("alphabet %q", codeAlphabet)
	}
	// Bytes at or above 234 are refused, so every character is equally
	// likely; the reader gives bytes 233, 234, 255, 0, 1, 2 and then 3.
	m := New(Options{Backend: newBackend(t), Random: strings.NewReader(string([]byte{233, 234, 255, 0, 1, 2, 3, 250, 0, 0, 0, 0, 0, 0, 0, 0}))})
	if got := m.newCode(); got != string([]byte{codeAlphabet[233%26], codeAlphabet[0], codeAlphabet[1], codeAlphabet[2]}) {
		t.Fatalf("code %q", got)
	}
	// A reader that fails, or gives nothing usable, makes no code.
	if got := New(Options{Backend: newBackend(t), Random: strings.NewReader("ab")}).newCode(); got != "" {
		t.Fatalf("a short reader made %q", got)
	}
	if got := New(Options{Backend: newBackend(t)}).newCode(); got != "" {
		t.Fatalf("no reader made %q", got)
	}
}

func TestApproveNeedsACode(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24, "7Q4K")
	r.m.o.Random = nil
	r.openFirst()
	r.signAll()
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("the field opened with no code")
	}
	r.seen("approve is off: " + reasonNoCode)
	if r.code() != "" {
		t.Fatal("a code is drawn")
	}
}

// OC11: what a terminal answers by itself, and a paste, never reach the
// confirm field as input.
func TestOC11ConfirmFieldRefusals(t *testing.T) {
	type event struct {
		name string
		msgs []tea.Msg
		// early puts the field's card inside its first 400 ms.
		early bool
		// burst makes the keys 10 ms apart.
		burst bool
	}
	events := []event{
		{name: "bracketed paste", msgs: []tea.Msg{tea.PasteStartMsg{}, tea.PasteMsg{Content: "7Q4K"}, tea.PasteEndMsg{}}},
		{name: "paste end alone", msgs: []tea.Msg{tea.PasteEndMsg{}}},
		{name: "cursor position report", msgs: []tea.Msg{tea.CursorPositionMsg{X: 1, Y: 1}}},
		{name: "background colour reply", msgs: []tea.Msg{tea.BackgroundColorMsg{}}},
		{name: "foreground colour reply", msgs: []tea.Msg{tea.ForegroundColorMsg{}}},
		{name: "mode report", msgs: []tea.Msg{tea.ModeReportMsg{}}},
		{name: "capability reply", msgs: []tea.Msg{tea.CapabilityMsg{}}},
		{name: "terminal version", msgs: []tea.Msg{tea.TerminalVersionMsg{}}},
		{name: "keyboard enhancements", msgs: []tea.Msg{tea.KeyboardEnhancementsMsg{}}},
		{name: "unknown csi", msgs: []tea.Msg{uv.UnknownCsiEvent("\x1b[?62;4c")}},
		{name: "unknown osc", msgs: []tea.Msg{uv.UnknownOscEvent("\x1b]11;rgb:0000/0000/0000\x07")}},
		{name: "text of several characters", msgs: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyExtended, Text: "7Q4K"}}},
		{name: "text that spells a key", msgs: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyExtended, Text: "enter"}}},
		{name: "alt chord", msgs: []tea.Msg{tea.KeyPressMsg{Code: 'a', Text: "a", Mod: tea.ModAlt}}},
		{name: "ctrl chord", msgs: []tea.Msg{tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}}},
		{name: "tab", msgs: []tea.Msg{keyMsg("tab")}},
		{name: "arrow", msgs: []tea.Msg{keyMsg("up")}},
		{name: "backspace", msgs: []tea.Msg{keyMsg("backspace")}},
		{name: "function key", msgs: []tea.Msg{tea.KeyPressMsg{Code: tea.KeyF1}}},
		{name: "a key with no text", msgs: []tea.Msg{tea.KeyPressMsg{Code: 'A'}}},
		{name: "a burst", msgs: []tea.Msg{keyMsg("7"), keyMsg("Q")}, burst: true},
		{name: "the first 400 ms", msgs: []tea.Msg{keyMsg("7")}, early: true},
	}
	for _, ev := range events {
		t.Run(ev.name, func(t *testing.T) {
			r := withField(t)
			r.typ("7Q")
			if r.m.cs.typed != "7Q" {
				t.Fatalf("typed %q before the event", r.m.cs.typed)
			}
			if ev.early {
				r.m.cs.openedAt = r.clk.Now().Add(-100 * time.Millisecond)
			}
			r.clk.Advance(200 * time.Millisecond)
			for _, msg := range ev.msgs {
				if ev.burst {
					r.clk.Advance(10 * time.Millisecond)
				}
				r.feed(msg)
			}
			if r.m.cs.typed != "" {
				t.Fatalf("the field holds %q after %s", r.m.cs.typed, ev.name)
			}
			if !r.m.FieldOpen() {
				t.Fatal("the field closed; it should clear and stay")
			}
			if len(r.b.decisions) != 0 {
				t.Fatal("a decision was sent")
			}
			// Typing the code after that, and slowly, still works: the
			// card is not locked.
			r.m.cs.openedAt = r.clk.Now().Add(-time.Second)
			r.typ("7Q4K")
			r.key("enter")
			if len(r.b.decisions) != 1 {
				t.Fatalf("the right code was not accepted after %s", ev.name)
			}
		})
	}
}

func TestConfirmFieldIgnoresLoosePasteOfTheCode(t *testing.T) {
	r := withField(t)
	r.feed(tea.PasteMsg{Content: "7Q4K"})
	r.key("enter")
	if len(r.b.decisions) != 0 {
		t.Fatal("a pasted code was accepted")
	}
}

func TestConfirmFieldBurstUsesTheInjectedClock(t *testing.T) {
	r := withField(t)
	r.clk.Advance(time.Second)
	r.key("7")
	r.clk.Advance(29 * time.Millisecond)
	r.key("Q")
	if r.m.cs.typed != "" {
		t.Fatalf("keys 29 ms apart were taken: %q", r.m.cs.typed)
	}
	r.clk.Advance(30 * time.Millisecond)
	r.key("7")
	r.clk.Advance(30 * time.Millisecond)
	r.key("Q")
	if r.m.cs.typed != "7Q" {
		t.Fatalf("keys 30 ms apart were refused: %q", r.m.cs.typed)
	}
}

func TestConfirmFieldRefusesALongerCode(t *testing.T) {
	r := withField(t)
	r.typ("7Q4K7")
	if r.m.cs.typed != "" {
		t.Fatalf("typed %q", r.m.cs.typed)
	}
}

func TestFieldKeysAreTheFieldsExceptEnterEscAndCtrlC(t *testing.T) {
	r := withField(t)
	r.typ("qadx")
	if r.m.cs.typed != "QADX" {
		t.Fatalf("typed %q", r.m.cs.typed)
	}
	if len(r.actions) != 0 {
		t.Fatalf("keys of the field reported %+v", r.actions)
	}
	if r.m.cs.sending || len(r.b.decisions) != 0 || r.m.mode != modeCard {
		t.Fatal("a key typed into the field acted")
	}
	// A fifth character clears the field, and the keys after it start
	// the next try.
	r.typ("l")
	if r.m.cs.typed != "" {
		t.Fatalf("the field was not cleared by an overlong code: %q", r.m.cs.typed)
	}
	r.typ("jk")
	if r.m.cs.typed != "JK" {
		t.Fatalf("typed %q", r.m.cs.typed)
	}
	// Esc closes the field and drops what was typed.
	r.typ("7Q")
	r.key("esc")
	if r.m.FieldOpen() || r.m.cs.typed != "" || r.m.mode != modeCard {
		t.Fatal("Esc did not close the field and stay on the card")
	}
	// ctrl+c is reported, and drops the field.
	r.key("a")
	r.typ("7Q")
	if act := r.key("ctrl+c"); act.Kind != Quit {
		t.Fatalf("ctrl+c reported %+v", act)
	}
	if r.m.FieldOpen() || r.m.cs.typed != "" {
		t.Fatal("ctrl+c left the field open")
	}
}

func TestDenyFieldTakesEveryPrintableKey(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.key("x")
	if !r.m.FieldOpen() || r.m.cs.field != fieldDeny {
		t.Fatalf("the deny field did not open:\n%s", r.screen())
	}
	r.typ("qadxljk q")
	if r.m.cs.typed != "qadxljk q" || len(r.actions) != 0 || r.m.mode != modeCard {
		t.Fatalf("typed %q, actions %v", r.m.cs.typed, r.actions)
	}
	r.key("backspace")
	r.key("backspace")
	r.seen("reason> qadxljk_", "[enter] deny")
	r.key("enter")
	if len(r.b.decisions) != 1 {
		t.Fatalf("%d decisions", len(r.b.decisions))
	}
	d := r.b.decisions[0]
	if d.Approve || d.Reason != "qadxljk" || d.ID != "p-1" || d.BindingSHA256 != "binding-p-1" || len(d.SignedOff) != 0 {
		t.Fatalf("decision %+v", d)
	}
}

func TestDenyNeedsNoReadingAndTakesAnEmptyReason(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("x")
	r.key("enter")
	if len(b.decisions) != 1 || b.decisions[0].Approve || b.decisions[0].Reason != "" {
		t.Fatalf("decisions %+v", b.decisions)
	}
}

func TestDenyReasonIsCapped(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("x")
	for i := 0; i < 520; i++ {
		r.key("é")
		if i < 5 {
			continue
		}
	}
	if got := len(r.m.cs.typed); got != 500 {
		t.Fatalf("the reason holds %d bytes", got)
	}
	r.seen(card.ReasonTooLong)
	r.key("enter")
	if len(b.decisions) != 1 || len(b.decisions[0].Reason) != 500 {
		t.Fatalf("decisions %+v", b.decisions)
	}
}

func TestDenyFieldIgnoresPasteAndAnswersAndEscDrops(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("x")
	r.typ("no")
	r.feed(tea.PasteMsg{Content: "\x1b[31mpasted"})
	r.feed(tea.CursorPositionMsg{X: 1, Y: 1})
	r.feed(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "\x1b]0;x\x07"})
	if r.m.cs.typed != "no" {
		t.Fatalf("typed %q", r.m.cs.typed)
	}
	r.key("esc")
	if r.m.FieldOpen() || len(b.decisions) != 0 {
		t.Fatal("Esc did not drop the field")
	}
}

func TestHeldOutcomeReturnsToTheListWithTheMark(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.b.decide = func(card.Decision) (card.Outcome, error) {
		r.b.listing.Pending[0].Held = true
		return card.Outcome{State: record.StatePending, Line: "recorded, not applied", Held: true}, nil
	}
	r.key("a")
	r.typ("7Q4K")
	r.key("enter")
	if r.m.CardOpen() {
		t.Fatalf("a held decision stayed on the card:\n%s", r.screen())
	}
	r.seen("[decided past the limit]", "decided past the limit: it is back in the list (recorded, not applied)")
	// The card, opened again, can be decided again, and shows the mark.
	c := testCard("p-1")
	c.After.Held = true
	r.b.cards["p-1"] = c
	r.openFirst()
	r.seen("Outcome · after the decision", "decided past the limit")
}

func TestDecideErrorIsOneLineAndTheCardStays(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.b.decide = func(card.Decision) (card.Outcome, error) {
		return card.Outcome{}, &record.Error{Code: record.CodeBindingMismatch, Detail: "private detail"}
	}
	r.key("a")
	r.typ("7Q4K")
	r.key("enter")
	r.seen("the decision was not accepted (binding-mismatch)")
	r.notSeen("private detail")
	if !r.m.cs.live() || r.m.cs.sending {
		t.Fatal("the card is not live again")
	}
	if r.code() != "ACDE" {
		t.Fatalf("no new code after a refusal: %q", r.code())
	}
}

func TestApproveBeforeTheCardHasBeenOpen400ms(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.key("enter")
	r.signAll()
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("the field opened in the first 400 ms")
	}
	r.seen("approve is off: " + card.ReasonJustOpened)
	r.clk.Advance(400 * time.Millisecond)
	r.key("a")
	if !r.m.FieldOpen() {
		t.Fatalf("the field did not open at 400 ms:\n%s", r.screen())
	}
}

func TestApproveAfterTheExpiry(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.clk.Advance(31 * time.Minute)
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("the field opened past the expiry")
	}
	r.seen("approve is off: " + card.ReasonExpired)
	r.key("x")
	r.seen("deny is off: " + card.ReasonExpired)
}

func TestReadOnlyOffersNothingAndSaysWhy(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.deciderO, b.deciderW = false, "another console holds the decider's place"
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	rows := r.rows()
	if got := rows[20]; strings.Contains(got, "sign off") || strings.Contains(got, "approve") || strings.Contains(got, "deny") {
		t.Fatalf("hints %q offer a decision", got)
	}
	if !strings.Contains(rows[21], "read-only: another console holds the decider's place") {
		t.Fatalf("message row %q", rows[21])
	}
	if r.code() != "" {
		t.Fatal("a code is drawn in a read-only view")
	}
	for _, k := range []string{"space", "a", "x"} {
		r.key(k)
		if r.m.FieldOpen() || len(r.m.cs.reading.SignedOff) != 0 {
			t.Fatalf("%q acted in a read-only view", k)
		}
		r.seen("read-only: another console holds the decider's place")
	}
	r.key("d")
	r.key("esc")
	r.seen("APPROVAL")
}

func TestReadOnlyWithNoReasonGivenHasOurs(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.deciderO = false
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.seen("read-only: " + reasonReadOnly)
}

func TestCardEndsWhileOpenFromTheListing(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.key("a")
	r.typ("7Q")
	// The backend now says the request was withdrawn.
	r.b.listing.Pending = nil
	r.b.listing.Recent = []card.Item{{ID: "p-1", State: record.StateWithdrawn, Project: "project-a", Verb: "feature finish"}}
	r.run(r.m.Refresh())
	r.seen("this request ended: withdrawn")
	if r.m.FieldOpen() || r.m.cs.typed != "" {
		t.Fatal("what was typed was kept")
	}
	if r.code() != "" || r.m.cs.code != "" {
		t.Fatal("the code was kept")
	}
	if len(r.m.cs.reading.SignedOff) != 0 {
		t.Fatal("the sign-offs were kept")
	}
	rows := r.rows()
	if strings.TrimSpace(rows[3]) != ">this request ended: withdrawn" && !strings.Contains(rows[3], "this request ended: withdrawn") {
		t.Fatalf("the body is %q", rows[3])
	}
	for y := 4; y < 19; y++ {
		if strings.TrimSpace(rows[y]) != "" {
			t.Fatalf("row %d of the ended card is not blank: %q", y, rows[y])
		}
	}
	for _, k := range []string{"a", "x", "space", "enter"} {
		r.key(k)
		if r.m.FieldOpen() || len(r.b.decisions) != 0 {
			t.Fatalf("%q acted on an ended card", k)
		}
	}
	r.key("esc")
	if r.m.CardOpen() {
		t.Fatal("Esc did not leave an ended card")
	}
}

func TestCardEndsWhileOpenFromOpen(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	c := testCard("p-1")
	c.After.State = record.StateExpired
	r.b.cards["p-1"] = c
	r.run(r.m.Refresh())
	r.seen("this request ended: expired")
}

func TestCardEndsWhenItIsNoLongerListed(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.b.listing = card.Listing{}
	r.run(r.m.Refresh())
	r.seen("this request ended: it is no longer pending")
}

func TestAnEndedCardOpenedFromRecentIsReadOnlyButWhole(t *testing.T) {
	c := testCard("p-0")
	c.After = card.After{State: record.StateApplied, Line: "applied: develop moved"}
	b := backendWith(t, nil, []card.Item{c.Item()})
	b.cards["p-0"] = c
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.seen("Outcome · after the decision", "state  applied", "applied: develop moved", "Plan (host")
	r.notSeen("[a] approve", "[x] deny", "[space] sign off")
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("an ended card opened the confirm field")
	}
	r.seen("approve is off: this request ended: applied")
}

func TestARefreshKeepsAnOpenLiveCard(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	r.key("a")
	r.typ("7Q")
	r.run(r.m.Refresh())
	if !r.m.FieldOpen() || r.m.cs.typed != "7Q" {
		t.Fatalf("a refresh disturbed the field: %q", r.m.cs.typed)
	}
	if !r.m.cs.live() {
		t.Fatal("the card is not live")
	}
}

// OC28: a second card for the same target opens on the "differs from"
// block, and Approve stays off until the block was scrolled past.
func TestOC28DiffersBlockOpensFirstAndGatesApprove(t *testing.T) {
	c := testCard("p-2")
	c.Differs = &card.Differs{
		EarlierID: "p-1", ReadAt: t0.Add(3 * time.Minute),
		Feature:      card.Change{Old: hex(3), New: hex(5)},
		Target:       card.Change{Old: hex(1), New: hex(1)},
		Merge:        card.Change{Old: hex(2), New: hex(6)},
		PathsAdded:   lead("docs/new.md"),
		PathsRemoved: lead("lib/old.py"),
	}
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	if r.m.cs.reading.Differs {
		t.Fatal("the block counts as passed with nothing scrolled")
	}
	rows := r.rows()
	if !strings.Contains(rows[3], "Differs from p-1, which you read at 14:05") || !strings.Contains(rows[3], "from an earlier card") {
		t.Fatalf("the card does not open on the block: %q", rows[3])
	}
	r.seen("feature tip  "+short(hex(3))+" -> "+short(hex(5)), "target tip   "+short(hex(1))+" (the same)",
		"paths added (1) · unverified lead data", "docs/new.md", "paths removed (1)", "lib/old.py", "end of what differs")
	// Approve is off for that reason first, and later for the next one.
	r2 := newRig(t, b, 80, 24)
	r2.openFirst()
	r2.key("a")
	r2.seen("approve is off: " + card.ReasonDiffers)
	// Scroll the screen past the block, with every other duty unmet.
	for i := 0; i < 30; i++ {
		r2.key("down")
	}
	if !r2.m.cs.reading.Differs {
		t.Fatal("the block was not passed after scrolling")
	}
	r2.seen("approve is off: 2 flagged paths are " + card.ReasonUnsigned)
}

func TestDiffersBlockIsNotPassedByBeingOnTheFirstScreen(t *testing.T) {
	c := testCard("p-2")
	c.Differs = &card.Differs{EarlierID: "p-1", Feature: card.Change{Old: hex(3), New: hex(5)}}
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 40)
	r.openFirst()
	if r.m.cs.reading.Differs {
		t.Fatal("a block on the first screen counted as passed")
	}
	r.key("down")
	if r.m.cs.reading.Differs {
		t.Fatal("one row down passed the block")
	}
	for i := 0; i < 8; i++ {
		r.key("down")
	}
	if !r.m.cs.reading.Differs {
		t.Fatal("the cursor moved past the block and it was not passed")
	}
}

func TestDiffersRefusedLineStandsWhereTheBlockWould(t *testing.T) {
	c := testCard("p-2")
	c.DiffersRefused = card.DiffersRefusedLine + " (record-invalid)"
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 40)
	r.openFirst()
	r.seen("Differs from · from an earlier card", card.DiffersRefusedLine)
}

func TestPruneCardReadsItsRefs(t *testing.T) {
	c := testCard("p-3")
	c.Diff, c.Lists, c.Merge, c.Flagged = nil, nil, nil, nil
	c.Verb = card.VerbLine{Name: "branch prune", Class: "ref-delete"}
	c.Refs = nil
	for i := 0; i < 30; i++ {
		c.Refs = append(c.Refs, card.RefLine{
			Kind: "ref", Ref: card.NewLeadText(fmt.Sprintf("refs/heads/old-%d", i)), Old: hex(i + 10), Deletes: true,
			New:        strings.Repeat("0", 40),
			MergedInto: &card.MergedInto{Ref: card.NewLeadText("refs/heads/develop"), Tip: hex(2)},
		})
	}
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("a")
	r.seen("approve is off: " + card.ReasonRefs)
	r.readAll()
	r.seen("approve is ready", "merged into refs/heads/develop")
	r.key("a")
	if !r.m.FieldOpen() {
		t.Fatalf("the field did not open on a prune:\n%s", r.screen())
	}
	r.typ("7Q4K")
	r.key("enter")
	if len(b.decisions) != 1 || !b.decisions[0].Approve || len(b.decisions[0].SignedOff) != 0 {
		t.Fatalf("decisions %+v", b.decisions)
	}
}

// truncatedCard is a card whose record holds the first 3 of 12 paths.
func truncatedCard() (card.Card, func(id string, kind card.PageKind, n int) (card.Page, error)) {
	c := testCard("p-4")
	c.Diff.Truncated = true
	c.Diff.Files = 12
	c.Flagged = nil
	pages := func(id string, kind card.PageKind, n int) (card.Page, error) {
		p := card.Page{Kind: kind, N: n, Pages: 3, ReviewDigest: "review-a"}
		for i := 0; i < 30; i++ {
			p.Paths = append(p.Paths, record.PathChange{Path: record.Text(fmt.Sprintf("pkg%d/file%d.go", n, i)), Status: "M", OldMode: "100644", NewMode: "100644", Added: 1})
		}
		return p, nil
	}
	return c, pages
}

func TestTruncatedPathListIsReadThroughPages(t *testing.T) {
	c, pages := truncatedCard()
	b := backendWith(t, []card.Card{c}, nil)
	b.pages = func(id string, kind card.PageKind, n int) (card.Page, error) {
		if kind != card.PagePaths || id != "p-4" {
			t.Errorf("Page(%q, %v, %d)", id, kind, n)
		}
		return pages(id, kind, n)
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	if len(b.pageCalls) != 1 || b.pageCalls[0] != 0 {
		t.Fatalf("the first page was not asked for: %v", b.pageCalls)
	}
	r.seen("the path list is cut: the card holds the first 3 of 12 paths")
	r.readAll()
	r.bodyHas("Whole path list: page 1 of 3")
	r.seen("pkg0/file29.go", "[ and ] turn pages")
	r.key("a")
	r.seen("approve is off: " + card.ReasonPathPages)
	// Page 1 is read; the others are not, and approve stays off. No key
	// jumps over a page: ] goes to the next one only.
	r.key("]")
	r.readAll()
	r.bodyHas("Whole path list: page 2 of 3")
	r.seen("pkg1/file29.go")
	r.key("a")
	r.seen("approve is off: " + card.ReasonPathPages)
	r.key("[")
	r.bodyHas("Whole path list: page 1 of 3")
	r.key("]")
	r.key("]")
	r.bodyHas("Whole path list: page 3 of 3")
	r.key("a")
	r.seen("approve is off: " + card.ReasonPathPages)
	r.readAll()
	r.seen("approve is ready")
	r.key("a")
	if !r.m.FieldOpen() {
		t.Fatalf("the field did not open after every page was read:\n%s", r.screen())
	}
	// No such page.
	r.key("esc")
	r.key("]")
	r.seen("there is no page there")
	if want := []int{0, 1, 0, 1, 2}; fmt.Sprint(b.pageCalls) != fmt.Sprint(want) {
		t.Fatalf("pages asked for: %v, want %v", b.pageCalls, want)
	}
}

func TestPageOfAnotherReviewMakesTheCardStale(t *testing.T) {
	c, pages := truncatedCard()
	b := backendWith(t, []card.Card{c}, nil)
	b.pages = func(id string, kind card.PageKind, n int) (card.Page, error) {
		p, _ := pages(id, kind, n)
		if n == 1 {
			p.ReviewDigest = "review-b"
		}
		return p, nil
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	r.key("]")
	r.seen(reasonStale)
	r.readAll()
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("a stale card opened the confirm field")
	}
	r.seen("approve is off: " + reasonStale)
}

func TestBackendErrorOnAPageIsOneLine(t *testing.T) {
	c, _ := truncatedCard()
	b := backendWith(t, []card.Card{c}, nil)
	b.pages = func(string, card.PageKind, int) (card.Page, error) {
		return card.Page{}, &record.Error{Code: record.CodeReviewChanged, Detail: "private"}
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.seen(reasonStale)
	r.notSeen("private")
	b2 := backendWith(t, []card.Card{c}, nil)
	r2 := newRig(t, b2, 80, 24)
	r2.openFirst()
	r2.seen("a page of the path list could not be read")
}

func TestPagesKeyOnAWholeListIsOff(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("]")
	r.seen("pages are off: the path list on this card is whole")
}

func TestActionsLeadAndCtrlCInThePager(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.pages = func(_ string, _ card.PageKind, n int) (card.Page, error) {
		return card.Page{Kind: card.PagePatch, N: n, Pages: 1, ReviewDigest: "review-a", Patch: []string{"x"}, Cont: []bool{false}}, nil
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	if act := r.key("l"); act != (Action{Kind: Lead, ID: "p-1", Project: "project-a"}) {
		t.Fatalf("l reported %+v", act)
	}
	r.key("d")
	if act := r.key("ctrl+c"); act.Kind != Quit {
		t.Fatalf("ctrl+c in the pager reported %+v", act)
	}
}

func TestTerminalAnswersOutsideAFieldAreIgnored(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	for _, msg := range []tea.Msg{tea.CursorPositionMsg{}, tea.PasteMsg{Content: "x"}, tea.BackgroundColorMsg{}, tea.MouseWheelMsg{}} {
		if act := r.feed(msg); act != (Action{}) {
			t.Fatalf("%T reported %+v", msg, act)
		}
	}
	if len(r.m.cs.reading.SignedOff) != 2 {
		t.Fatal("a message disturbed the reading")
	}
}

func TestMessagesOfAnotherViewAreIgnored(t *testing.T) {
	r := readyRig(t, testCard("p-1"))
	other := New(Options{Backend: newBackend(t)})
	for _, msg := range []tea.Msg{
		listedMsg{owner: other}, openedMsg{owner: other}, pagedMsg{owner: other}, decidedMsg{owner: other}, tickMsg{owner: other},
	} {
		r.feed(msg)
	}
	if !r.m.cs.live() || r.m.mode != modeCard {
		t.Fatal("a message of another view changed this one")
	}
}

// A page turned to counts as read only once its last row was drawn, also
// when it is shorter than the page before it and its end lies inside the
// screen that showed the end of that one.
func TestPageTurnedToIsNotReadBeforeItsEndIsDrawn(t *testing.T) {
	c, _ := truncatedCard()
	b := backendWith(t, []card.Card{c}, nil)
	b.pages = func(id string, kind card.PageKind, n int) (card.Page, error) {
		p := card.Page{Kind: kind, N: n, Pages: 2, ReviewDigest: "review-a"}
		rows := []int{30, 25}[n]
		for i := 0; i < rows; i++ {
			p.Paths = append(p.Paths, record.PathChange{Path: record.Text(fmt.Sprintf("pkg%d/file%d.go", n, i)), Status: "M", OldMode: "100644", NewMode: "100644", Added: 1})
		}
		return p, nil
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	r.key("]")
	r.bodyHas("Whole path list: page 2 of 2")
	r.notSeen("pkg1/file24.go")
	if ok, _ := r.m.cs.c.MayApprove(r.m.cs.reading, r.clk.Now()); ok {
		t.Fatalf("approve was ready before the last row of page 2 was drawn:\n%s", r.screen())
	}
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("the confirm field opened before the last row of page 2 was drawn")
	}
	r.seen("approve is off: " + card.ReasonPathPages)
	r.readAll()
	r.seen("pkg1/file24.go", "approve is ready")
}

// noDecision checks that Enter in the open field sent nothing, that the field
// closed with the reason on the message row, and that what was typed is gone.
func noDecision(t *testing.T, r *rig, reason string) {
	t.Helper()
	r.key("enter")
	if len(r.b.decisions) != 0 {
		t.Fatalf("a decision was sent: %+v", r.b.decisions)
	}
	if r.m.FieldOpen() || r.m.cs.typed != "" || r.m.cs.sending {
		t.Fatalf("the field stayed open or what was typed was kept (typed %q)", r.m.cs.typed)
	}
	r.seen(reason)
}

func TestEnterSendsNothingAfterTheWindowShrank(t *testing.T) {
	r := withField(t)
	r.typ("7Q4")
	r.run(r.m.SetSize(70, 20))
	r.typ("K")
	noDecision(t, r, "approve is off: "+reasonSmall)

	// A deny too, with its reason typed in the window that became small.
	r = readyRig(t, testCard("p-1"))
	r.key("x")
	if !r.m.FieldOpen() {
		t.Fatal("the deny field did not open")
	}
	r.typ("no")
	r.run(r.m.SetSize(80, 23))
	noDecision(t, r, "deny is off: "+reasonSmall)
}

func TestEnterSendsNothingAfterTheBackendSaidReadOnly(t *testing.T) {
	line := "another console holds the decider's place"
	for _, key := range []string{"a", "x"} {
		r := readyRig(t, testCard("p-1"))
		r.key(key)
		if !r.m.FieldOpen() {
			t.Fatalf("%q: the field did not open", key)
		}
		if key == "a" {
			r.typ("7Q4K")
		} else {
			r.typ("no")
		}
		r.b.deciderO, r.b.deciderW = false, line
		r.run(r.m.Refresh())
		noDecision(t, r, "read-only: "+line)
	}
}

func TestEnterSendsNoApproveOfAStaleCard(t *testing.T) {
	r := withField(t)
	r.typ("7Q4K")
	// A page answer that arrives while the field is open makes the card stale.
	r.m.cs.stale = true
	noDecision(t, r, "approve is off: "+reasonStale)
}
