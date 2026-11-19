package terminalconsole

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/demo"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

// The approvals flow tests drive the whole console model in process, as the
// integration test does, against the sample backend of internal/approval/demo,
// with an injected clock and no terminal. Each test names, in its name or its
// first comment, the case of the owner channel design (section 13) that the
// console can prove alone: OC10, OC11, OC12, the console's part of OC16 and
// OC28. The other tests are the console's own rules for the card, the limit
// and the arrival strip, run end to end.
//
// What these tests prove is the console in front of sample cards. They say
// nothing about the real door, which only the owner channel's own tests and
// the host's can prove.

// flowBackend gives the console the sample backend, and notes what the console
// asked of it. It fails a test when a method is called outside a command that
// the rig runs: the console may not call its backend from Update or View. A
// test can also hide a card, withdraw one, hand the backend no presence beat
// or change a card on its way out; the sample backend has no way to do these.
type flowBackend struct {
	t  *testing.T
	in bool
	d  *demo.Backend

	calls   []string
	decs    []card.Decision
	outs    []card.Outcome
	errs    []error
	changes []limits.Change
	acts    []time.Time

	// dropActs makes the backend hear no presence beat.
	dropActs bool
	// hidden cards are in no listing; withdrawn cards are in the recent
	// ones, in the state withdrawn.
	hidden    map[string]bool
	withdrawn map[string]bool
	// change edits the card Open returns for an id.
	change map[string]func(*card.Card)
}

func (f *flowBackend) guard(what string) {
	f.t.Helper()
	if !f.in {
		f.t.Errorf("%s was called outside a command", what)
	}
	f.calls = append(f.calls, what)
}

// called counts the calls of a method.
func (f *flowBackend) called(what string) int {
	n := 0
	for _, c := range f.calls {
		if c == what {
			n++
		}
	}
	return n
}

// listing is the sample backend's listing with the hidden cards left out and
// the withdrawn ones moved to the recent decisions. It is not a call of the
// console.
func (f *flowBackend) listing() card.Listing {
	l, err := f.d.List()
	if err != nil {
		f.t.Fatal(err)
	}
	var pending []card.Item
	for _, it := range l.Pending {
		switch {
		case f.hidden[it.ID]:
		case f.withdrawn[it.ID]:
			it.State, it.Line = record.StateWithdrawn, "withdrawn"
			l.Recent = append([]card.Item{it}, l.Recent...)
		default:
			pending = append(pending, it)
		}
	}
	l.Pending = pending
	return l
}

func (f *flowBackend) List() (card.Listing, error) {
	f.guard("List")
	return f.listing(), nil
}

func (f *flowBackend) Open(id string) (card.Card, error) {
	f.guard("Open")
	c, err := f.d.Open(id)
	if edit := f.change[id]; edit != nil && err == nil {
		edit(&c)
	}
	return c, err
}

func (f *flowBackend) Page(id string, k card.PageKind, n int) (card.Page, error) {
	f.guard("Page")
	return f.d.Page(id, k, n)
}

func (f *flowBackend) Decide(d card.Decision) (card.Outcome, error) {
	f.guard("Decide")
	f.decs = append(f.decs, d)
	out, err := f.d.Decide(d)
	f.outs, f.errs = append(f.outs, out), append(f.errs, err)
	return out, err
}

func (f *flowBackend) Decider() (bool, string) {
	f.guard("Decider")
	return f.d.Decider()
}

func (f *flowBackend) Limit(project string) (limits.View, error) {
	f.guard("Limit")
	return f.d.Limit(project)
}

func (f *flowBackend) SetLimit(project string, c limits.Change) (limits.View, error) {
	f.guard("SetLimit")
	f.changes = append(f.changes, c)
	return f.d.SetLimit(project, c)
}

func (f *flowBackend) Activity(at time.Time) {
	f.guard("Activity")
	f.acts = append(f.acts, at)
	if !f.dropActs {
		f.d.Activity(at)
	}
}

func (f *flowBackend) Looked(at time.Time) {
	f.guard("Looked")
	f.d.Looked(at)
}

// flowRig is the console with the flow backend and a clock of its own. The
// commands the model returns run on the spot; every key moves the clock on by
// 100 ms, which is slow enough to be typing.
type flowRig struct {
	*rig
	b   *flowBackend
	clk *testClock
}

// flowCode is the confirm code the rig's source of randomness gives first.
const flowCode = "7Q4K"

// flowCodes returns n confirm codes, one for each card the rig is to open or
// each wrong code it is to make.
func flowCodes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = flowCode
	}
	return out
}

// newFlowRig makes the console with the flow backend in a window of w by h.
// The source of randomness gives the codes, one for each code the console
// makes; without any it gives flowCode every time.
func newFlowRig(t *testing.T, w, h int, codes ...string) *flowRig {
	t.Helper()
	if len(codes) == 0 {
		codes = flowCodes(100)
	}
	clk := &testClock{t: apprT0}
	b := &flowBackend{t: t, d: demo.New(clk.Now), hidden: map[string]bool{}, withdrawn: map[string]bool{},
		change: map[string]func(*card.Card){}}
	f := &flowRig{b: b, clk: clk}
	f.rig = newRig(t, 0, 0, Options{Backend: b, Now: clk.Now, Random: &codeReader{codes: codes}})
	if w > 0 {
		f.pump(f.size(w, h))
	}
	return f
}

// pump runs cmd and every command that follows, handing each answer to the
// model. The timers the model schedules are left in the rig's list.
func (f *flowRig) pump(cmd tea.Cmd) {
	f.t.Helper()
	if cmd == nil {
		return
	}
	f.b.in = true
	msg := cmd()
	f.b.in = false
	switch v := msg.(type) {
	case nil, tea.RawMsg, tea.QuitMsg:
	case tea.BatchMsg:
		for _, c := range v {
			f.pump(c)
		}
	default:
		_, next := f.m.Update(msg)
		f.pump(next)
	}
}

// send hands msg to the model, at the clock's present time.
func (f *flowRig) send(msg tea.Msg) {
	f.t.Helper()
	_, cmd := f.m.Update(msg)
	f.pump(cmd)
}

// press presses a key, 100 ms after the last one.
func (f *flowRig) press(k tea.KeyPressMsg) {
	f.t.Helper()
	f.clk.Advance(100 * time.Millisecond)
	f.send(k)
}

// typeKeys presses each character of s at the pace of typing.
func (f *flowRig) typeKeys(s string) {
	f.t.Helper()
	for _, c := range s {
		f.press(ch(c))
	}
}

// poll lets the console read the backend's listing, as its timer does.
func (f *flowRig) poll() {
	f.t.Helper()
	f.send(apprTickMsg{at: f.clk.Now()})
}

// focusPane moves the pane focus of the cockpit to pane with Tab.
func (f *flowRig) focusPane(pane int) {
	f.t.Helper()
	for i := 0; f.m.pane != pane; i++ {
		if i > paneCount {
			f.t.Fatal("no such pane")
		}
		f.press(tabKey)
	}
}

// evidence returns the text of the right pane, its wrapped lines joined.
func (f *flowRig) evidence() string {
	var parts []string
	for _, l := range f.m.evidenceLines(f.m.layout().evidence.Dx() - 2) {
		parts = append(parts, l.s)
	}
	return strings.Join(parts, " ")
}

// idsOf returns the ids of the rows of the approvals list, in the order the
// cursor moves over them: the pending cards, then the recent ones.
func (f *flowRig) idsOf() []string {
	l := f.b.listing()
	var ids []string
	for _, it := range l.Pending {
		ids = append(ids, it.ID)
	}
	for _, it := range l.Recent {
		ids = append(ids, it.ID)
	}
	return ids
}

// leave goes back to the cockpit, whatever is open.
func (f *flowRig) leave() {
	f.t.Helper()
	for i := 0; f.m.apprOpen(); i++ {
		if i > 6 {
			f.t.Fatalf("Esc does not leave approvals:\n%s", f.view())
		}
		f.press(escKey)
	}
}

// openList opens approvals at its list, from the cockpit.
func (f *flowRig) openList() {
	f.t.Helper()
	f.leave()
	f.press(ch('g'))
	if !f.m.apprOpen() || f.m.ap.view == nil {
		f.t.Fatalf("g did not open approvals:\n%s", f.view())
	}
}

// openCardID opens the card of an id from the list, and lets the 400 ms of a
// new card pass.
func (f *flowRig) openCardID(id string) {
	f.t.Helper()
	f.openList()
	at := -1
	for i, have := range f.idsOf() {
		if have == id {
			at = i
		}
	}
	if at < 0 {
		f.t.Fatalf("the list has no %s: %v", id, f.idsOf())
	}
	for i := 0; i < at; i++ {
		f.press(ch('j'))
	}
	f.press(enterKey)
	if !f.m.ap.view.CardOpen() {
		f.t.Fatalf("%s did not open:\n%s", id, f.view())
	}
	f.clk.Advance(time.Second)
}

// readAll moves the cursor to the end of the card, a row at a time, as a
// reader does.
func (f *flowRig) readAll() {
	f.t.Helper()
	for i := 0; i < 160; i++ {
		f.press(ch('j'))
	}
}

// cursorRow is the row of the screen that the cursor is on.
func (f *flowRig) cursorRow() string {
	for _, row := range strings.Split(f.view(), "\n") {
		if strings.HasPrefix(row, ">") {
			return row
		}
	}
	return ""
}

// cursorTo moves the cursor to the first row that holds text, from the top.
func (f *flowRig) cursorTo(text string) {
	f.t.Helper()
	for i := 0; i < 160; i++ {
		f.press(ch('k'))
	}
	for i := 0; i < 160; i++ {
		if strings.Contains(f.cursorRow(), text) {
			return
		}
		f.press(ch('j'))
	}
	f.t.Fatalf("no row with %q:\n%s", text, f.view())
}

var flowTitleCode = regexp.MustCompile(`^ APPROVAL ([A-Z0-9]{4}) `)

// titleCode returns the confirm code in the title of the card, or "" when
// the title shows none.
func (f *flowRig) titleCode() string {
	m := flowTitleCode.FindStringSubmatch(strings.SplitN(f.view(), "\n", 2)[0])
	if m == nil {
		return ""
	}
	return m[1]
}

func (f *flowRig) seenAll(texts ...string) {
	f.t.Helper()
	v := strings.Join(strings.Fields(f.view()), " ")
	for _, want := range texts {
		if !strings.Contains(v, strings.Join(strings.Fields(want), " ")) {
			f.t.Fatalf("the screen does not hold %q:\n%s", want, f.view())
		}
	}
}

func (f *flowRig) notSeenAny(texts ...string) {
	f.t.Helper()
	v := f.view()
	for _, bad := range texts {
		if strings.Contains(v, bad) {
			f.t.Fatalf("the screen holds %q:\n%s", bad, v)
		}
	}
}

// noDecision fails the test when the backend was asked to decide.
func (f *flowRig) noDecision(when string) {
	f.t.Helper()
	if len(f.b.decs) != 0 || f.b.called("Decide") != 0 {
		f.t.Fatalf("%s: a decision reached the backend: %+v", when, f.b.decs)
	}
}

// flowSGR matches the colour sequences the frame is drawn with.
var flowSGR = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// checkFrame fails the test when the frame holds anything a terminal would
// act on or hide: the only sequences in it are the colours the console drew
// itself, and no character that is a control, a direction override, a
// zero-width or other invisible one, a line or paragraph separator, or bytes
// that are not UTF-8.
func checkFrame(t *testing.T, where, raw string) {
	t.Helper()
	if !utf8.ValidString(raw) {
		t.Fatalf("%s: the frame holds bytes that are not UTF-8", where)
	}
	for _, r := range flowSGR.ReplaceAllString(raw, "") {
		switch {
		case r == '\n':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Fatalf("%s: the frame holds the control %U", where, r)
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Bidi_Control, r), unicode.Is(unicode.Variation_Selector, r),
			unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r), unicode.Is(unicode.Zl, r),
			unicode.Is(unicode.Zp, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r),
			r == 0x2800, r == utf8.RuneError:
			t.Fatalf("%s: the frame holds the invisible or direction code point %U", where, r)
		}
	}
}

// leadMarkers are texts that stand in the lead-written parts of the hostile
// cards (a path, the note, the patch) and in no word of the console's own
// chrome: text shaped like a decision, a code, a card's id, or a sequence.
var leadMarkers = []string{"APPROVED", "K7P3", "ap-sample-hostile", "osc-bel", "osc-st", "dcs", "evil", "zero", "caf",
	"owned", "diff --git", "not utf-8", "workflows", "exec-config"}

// visibleEscape is how a lead's escape byte is drawn: the text \x1b, once. A
// frame that held the byte itself, or dropped it, would not hold it.
// visibleEscapeTwice is the same text with its backslash escaped again.
const (
	visibleEscape      = `\x1b[2J`
	visibleEscapeTwice = `\\x1b`
)

// chromeRows returns the rows of a card's screen that are the console's own:
// the title, the verb line and the rule at the top, and the rule, the key
// hints, the status and the footer at the bottom.
func chromeRows(frame string) []string {
	rows := strings.Split(frame, "\n")
	h := len(rows)
	return append(append([]string(nil), rows[:3]...), rows[h-5:]...)
}

// ---------------------------------------------------------------- OC10

// TestApprovalsFlowHostileTextOnTheCardAndInThePagerOC10 is case OC10: the
// hostile sample cards (escape sequences, controls, direction overrides, text
// shaped like the console's own words, and feature names shaped to mislead)
// are read on the card and in the pager, at two sizes. At every step the frame
// holds no raw byte of a lead, and the console's own rows hold no lead text
// but the feature name on the verb line.
func TestApprovalsFlowHostileTextOnTheCardAndInThePagerOC10(t *testing.T) {
	cards := []struct{ id, feature string }{
		{"ap-sample-hostile", "approve"},
		{"ap-sample-hostile-develop", "develop"},
		{"ap-sample-hostile-code", "a-then-code"},
		{"ap-sample-hostile-same", "small-fix"},
	}
	for _, size := range [][2]int{{140, 45}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			f := newFlowRig(t, size[0], size[1])
			f.poll()
			f.openList()
			list := f.m.View().Content
			checkFrame(t, "list", list)
			for _, c := range cards {
				f.seenAll("name=" + c.feature)
			}
			// The cards' bodies are not in the list: none of their text is.
			f.notSeenAny(leadMarkers...)
			for _, c := range cards {
				f.openCardID(c.id)
				if got := f.titleCode(); got != flowCode {
					t.Fatalf("%s: the title code is %q:\n%s", c.id, got, f.view())
				}
				var shown strings.Builder
				for step := 0; step < 90; step++ {
					f.press(ch('j'))
					raw := f.m.View().Content
					where := fmt.Sprintf("%s step %d", c.id, step)
					checkFrame(t, where, raw)
					frame := ansi.Strip(raw)
					shown.WriteString(frame)
					if strings.Contains(frame, visibleEscapeTwice) {
						t.Fatalf("%s: a lead's escape is escaped twice:\n%s", where, frame)
					}
					rows := strings.Split(frame, "\n")
					if len(rows) > size[1] {
						t.Fatalf("%s: %d rows", where, len(rows))
					}
					for _, row := range rows {
						if w := ansi.StringWidth(row); w > size[0] {
							t.Fatalf("%s: a row is %d cells wide", where, w)
						}
					}
					chrome := chromeRows(frame)
					// The verb line names the feature, and says nothing else
					// a lead wrote.
					if want := "feature finish name=" + c.feature + "  ->  develop    class: ref-move"; strings.TrimSpace(chrome[1]) != want {
						t.Fatalf("%s: the verb line is %q, want %q", where, chrome[1], want)
					}
					for i, row := range chrome {
						if i == 1 {
							continue
						}
						for _, bad := range leadMarkers {
							if strings.Contains(row, bad) {
								t.Fatalf("%s: the chrome row %q holds the lead text %q", where, row, bad)
							}
						}
					}
				}
				f.seenAll("end of the card")
				// The lead's escape in a path is drawn as a visible escape.
				if !strings.Contains(shown.String(), visibleEscape) {
					t.Fatalf("%s: no frame draws the escape as %q", c.id, visibleEscape)
				}
			}
			// The pager on the hostile patch.
			f.openCardID(cards[0].id)
			f.press(ch('d'))
			if f.m.ap.view.CardOpen() && !strings.Contains(f.view(), "PATCH") {
				t.Fatalf("d did not open the pager:\n%s", f.view())
			}
			// The first screen of the pager holds the hostile hunk's escape.
			pagerShown := strings.Builder{}
			pagerShown.WriteString(ansi.Strip(f.m.View().Content))
			for step := 0; step < 70; step++ {
				if step%2 == 0 {
					f.press(tea.KeyPressMsg{Code: tea.KeyPgDown})
				} else {
					f.press(ch('j'))
				}
				raw := f.m.View().Content
				where := fmt.Sprintf("pager step %d", step)
				checkFrame(t, where, raw)
				pagerShown.WriteString(ansi.Strip(raw))
				if strings.Contains(ansi.Strip(raw), visibleEscapeTwice) {
					t.Fatalf("%s: a lead's escape is escaped twice in the pager", where)
				}
				rows := strings.Split(ansi.Strip(raw), "\n")
				last := rows[len(rows)-1]
				for _, bad := range leadMarkers {
					if strings.Contains(last, bad) {
						t.Fatalf("%s: the pager's own row holds %q: %q", where, bad, last)
					}
				}
			}
			f.seenAll("PATCH")
			// The escape of the hostile hunk is drawn as a visible escape.
			if !strings.Contains(pagerShown.String(), visibleEscape) {
				t.Fatalf("no frame of the pager draws the escape as %q", visibleEscape)
			}
			f.press(escKey)
			if f.m.ap.view == nil || !f.m.ap.view.CardOpen() {
				t.Fatal("Esc left the card with the pager")
			}
			f.noDecision("hostile text")
		})
	}
}

// ---------------------------------------------------------------- OC11

// TestApprovalsFlowTerminalAnswersBurstAndPasteAtTheConfirmFieldOC11 is case
// OC11: whatever the terminal sends by itself (status reports, colour replies,
// capability answers, the end of a paste), a burst of keys faster than typing
// and a bracketed paste of the very code, all aimed at the confirm field,
// reach the backend as no decision. Typing the code as a person does still
// decides, so the field was not merely broken.
func TestApprovalsFlowTerminalAnswersBurstAndPasteAtTheConfirmFieldOC11(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.openCardID("ap-sample-clean")
	f.readAll()
	f.press(ch('a'))
	if !f.m.ap.view.FieldOpen() {
		t.Fatalf("a did not open the confirm field:\n%s", f.view())
	}
	code := f.titleCode()
	if code == "" {
		t.Fatal("no code in the title")
	}
	f.seenAll("code> ____")
	refused := "input that was not typed was refused, and the field was cleared"

	answers := []struct {
		name string
		msgs []tea.Msg
	}{
		{"cursor position report", []tea.Msg{tea.CursorPositionMsg{X: 3, Y: 4}}},
		{"foreground colour reply", []tea.Msg{tea.ForegroundColorMsg{Color: color.RGBA{R: 1, G: 2, B: 3, A: 255}}}},
		{"background colour reply", []tea.Msg{tea.BackgroundColorMsg{Color: color.RGBA{R: 1, G: 2, B: 3, A: 255}}}},
		{"mode report", []tea.Msg{tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset}}},
		{"capability answer", []tea.Msg{tea.CapabilityMsg{Content: "RGB"}}},
		{"terminal version", []tea.Msg{tea.TerminalVersionMsg{Name: "terminal-a"}}},
		{"keyboard enhancements", []tea.Msg{tea.KeyboardEnhancementsMsg{Flags: 1}}},
		{"start of a paste", []tea.Msg{tea.PasteStartMsg{}}},
		{"end of a paste", []tea.Msg{tea.PasteEndMsg{}}},
		{"raw output", []tea.Msg{tea.RawMsg{Msg: "\x1b[0n"}}},
		{"the code pasted", []tea.Msg{tea.PasteStartMsg{}, tea.PasteMsg{Content: code}, tea.PasteEndMsg{}}},
		{"the code pasted with Enter", []tea.Msg{tea.PasteMsg{Content: code + "\r"}}},
	}
	for _, a := range answers {
		// One character typed by a person is in the field, and then the
		// terminal answers.
		f.clk.Advance(time.Second)
		f.press(ch(rune(code[0])))
		f.seenAll("code> *___")
		for _, msg := range a.msgs {
			f.send(msg)
		}
		f.seenAll(refused)
		if !f.m.ap.view.FieldOpen() {
			t.Fatalf("%s: the field closed", a.name)
		}
		f.noDecision(a.name)
	}

	// One key press whose text is the whole code is not typing.
	f.clk.Advance(time.Second)
	f.press(tea.KeyPressMsg{Code: rune(code[0]), Text: code})
	f.seenAll(refused)
	f.noDecision("a key with the whole code as its text")

	// A burst: the code's four keys, all at one instant. The first is taken,
	// each after it is too close to the one before and clears the field.
	f.clk.Advance(time.Second)
	for _, c := range code {
		f.send(ch(c))
	}
	f.seenAll("keys came faster than typing and were refused, and the field was cleared")
	f.noDecision("a burst")

	// Enter on what is left is a wrong code, which makes a new code and
	// sends nothing.
	f.press(enterKey)
	f.seenAll("that is not the code: a new code is in the title")
	f.noDecision("Enter on an empty field")

	// A burst that ends as a paste would: the keys one after the other at one
	// instant, and then Enter in the same instant.
	f.clk.Advance(time.Second)
	for _, c := range f.titleCode() {
		f.send(ch(c))
	}
	f.send(enterKey)
	f.noDecision("a burst of the code and Enter")

	// The control: the code typed at the pace of a person is a decision.
	if !f.m.ap.view.FieldOpen() {
		f.press(ch('a'))
	}
	f.clk.Advance(time.Second)
	f.press(ch('x'))
	f.press(escKey)
	f.press(ch('a'))
	code = f.titleCode()
	f.typeKeys(strings.ToLower(code))
	f.press(enterKey)
	if len(f.b.decs) != 1 || !f.b.decs[0].Approve || f.b.decs[0].ID != "ap-sample-clean" {
		t.Fatalf("the typed code did not decide:\n%s\n%+v", f.view(), f.b.decs)
	}
}

// ---------------------------------------------------------------- OC12

// TestApprovalsFlowKeysTypedWithTheSessionFocusedDecideNothingOC12 is case
// OC12: with the session focused, every key (an a, a code, a deny key, Enter,
// a paste of the code) goes to the session and none reaches the backend as a
// decision, or opens a card, or changes a limit. The pending cards are still
// shown, in the status line, and the arrival strip is not drawn over the
// session.
func TestApprovalsFlowKeysTypedWithTheSessionFocusedDecideNothingOC12(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.focusSession()
	if f.m.focus != sessionFocus {
		t.Fatal("the session did not take the focus")
	}
	before := len(f.rig.sink.keys)
	// A card drawn by the session, as a lead could draw one: the owner types
	// what it says.
	typed := []tea.KeyPressMsg{ch('a'), ch('7'), ch('Q'), ch('4'), ch('K'), enterKey, ch('x'), ch('y'), ch('g'),
		ch('L'), ch(' '), ch('1'), escKey, ch('a'), ch('A'), enterKey}
	for _, k := range typed {
		f.press(k)
		if f.m.apprOpen() {
			t.Fatalf("%v opened approvals", k)
		}
	}
	f.send(tea.PasteMsg{Content: flowCode + "\r"})
	f.send(tea.PasteStartMsg{})
	f.send(tea.PasteEndMsg{})
	if got := len(f.rig.sink.keys) - before; got != len(typed) {
		t.Fatalf("the session got %d of %d keys: %v", got, len(typed), f.rig.sink.keys)
	}
	if len(f.rig.sink.pastes) != 1 || f.rig.sink.pastes[0] != flowCode+"\r" {
		t.Fatalf("the paste went to %v", f.rig.sink.pastes)
	}
	f.noDecision("session focus")
	for _, call := range []string{"Open", "Page", "Decide", "Limit", "SetLimit"} {
		if f.b.called(call) != 0 {
			t.Fatalf("the backend was asked %s in session focus", call)
		}
	}
	if f.m.ap.view != nil || f.m.ap.limit != nil {
		t.Fatal("an approvals screen is open")
	}
	// The count of waiting cards is still in the status line, in session
	// focus too; the strip is not drawn over the session.
	f.seenAll("approvals 12 (oldest 2 h)")
	if !f.m.ap.strip.Visible() {
		t.Fatal("the strip should be showing, undrawn, to be seen in console focus")
	}
	f.notSeenAny("while you were away")
	f.noDecision("session focus, after drawing")
}

// ---------------------------------------------------------------- OC16

// TestApprovalsFlowApproveIsNotOfferedWithASignOffMissingOC16 is the
// console's part of case OC16: on a card with a flagged path, Approve is not
// offered until that path has its own sign-off, a sign-off on a path that is
// not flagged is refused, and the decision that leaves carries exactly the
// flagged list.
func TestApprovalsFlowApproveIsNotOfferedWithASignOffMissingOC16(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.openCardID("ap-sample-flagged")
	f.readAll()
	flagged := "[ ] exec-config  Makefile"
	f.seenAll("Flagged paths (each needs its own sign-off)", flagged)

	// Everything is read and the sign-off is missing.
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatalf("Approve was offered with a sign-off missing:\n%s", f.view())
	}
	f.seenAll("approve is off: 1 flagged path is not signed off")
	f.notSeenAny("code>")

	// A sign-off off the flagged row is refused, and Approve stays off.
	f.cursorTo("Changed paths")
	f.press(ch(' '))
	f.seenAll("sign-off is off: the cursor is not on a flagged path")
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatal("Approve was offered after a sign-off off the flagged row")
	}

	// The sign-off on the flagged row, taken back, is a sign-off missing.
	f.cursorTo("exec-config")
	f.press(ch(' '))
	f.seenAll("[x] exec-config  Makefile")
	f.press(ch(' '))
	f.seenAll(flagged)
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatal("Approve was offered with the sign-off taken back")
	}
	f.noDecision("a sign-off missing")

	// With the sign-off made, Approve is offered, and what leaves is the
	// flagged list and no other.
	f.press(ch(' '))
	f.press(ch('a'))
	if !f.m.ap.view.FieldOpen() {
		t.Fatalf("Approve was not offered with the sign-off made:\n%s", f.view())
	}
	f.typeKeys(strings.ToLower(f.titleCode()))
	f.press(enterKey)
	if len(f.b.decs) != 1 {
		t.Fatalf("%d decisions:\n%s", len(f.b.decs), f.view())
	}
	c, err := f.b.d.Open("ap-sample-flagged")
	if err != nil || len(c.Flagged) != 1 {
		t.Fatal(err, c.Flagged)
	}
	d := f.b.decs[0]
	if !d.Approve || len(d.SignedOff) != 1 || d.SignedOff[0] != c.Flagged[0].SignOff() {
		t.Fatalf("the decision does not carry the flagged list: %+v", d)
	}
	if err := f.b.errs[0]; err != nil {
		t.Fatalf("the backend refused the sign-offs: %v", err)
	}
}

// ---------------------------------------------------------------- OC28

// TestApprovalsFlowAskReadWithdrawAskAgainOC28 is case OC28: a card is asked
// and read, its request is withdrawn while it is open, and the lead asks again.
// The new card has a new code, opens on the block that says how it differs
// from the card that was read, and may not be approved until that block was
// passed. The code of the first card is no code of the second.
func TestApprovalsFlowAskReadWithdrawAskAgainOC28(t *testing.T) {
	// The codes the console makes, in order: one for each card it opens and
	// one for each wrong code.
	f := newFlowRig(t, 140, 45, "AC34", "DE67", "FG9Y")
	first, second := "ap-sample-pair-1", "ap-sample-pair-2"
	f.b.hidden[second] = true
	f.poll()
	f.openCardID(first)
	f.readAll()
	if got := f.titleCode(); got != "AC34" {
		t.Fatalf("the first card's code is %q", got)
	}
	f.press(ch('a'))
	if !f.m.ap.view.FieldOpen() {
		t.Fatalf("the first card, read, may not be approved:\n%s", f.view())
	}
	f.press(escKey)
	f.notSeenAny("Differs from")

	// The request is withdrawn, and the card that is open learns of it.
	f.b.withdrawn[first] = true
	f.poll()
	f.seenAll("this request ended: withdrawn")
	if f.titleCode() != "" {
		t.Fatal("the code of an ended request is still drawn")
	}
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatal("a withdrawn request may be approved")
	}
	f.noDecision("a withdrawn request")

	// The lead asks again: a new card, with a new code.
	delete(f.b.hidden, second)
	f.poll()
	f.openCardID(second)
	if got := f.titleCode(); got != "DE67" {
		t.Fatalf("the new card's code is %q, not a new one:\n%s", got, f.view())
	}
	rows := strings.Split(f.view(), "\n")
	if !strings.Contains(rows[3], "Differs from "+first) {
		t.Fatalf("the new card does not open on the differs-from block:\n%s", f.view())
	}
	f.seenAll("Differs from "+first+", which you read at", "src/pair_c.go")
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatalf("Approve was offered before the differs-from block was passed:\n%s", f.view())
	}
	f.seenAll("approve is off: " + card.ReasonDiffers)

	// Reading past the block, and the rest of the card, offers it. The code
	// of the first card is no code of this one.
	f.readAll()
	f.press(ch('a'))
	if !f.m.ap.view.FieldOpen() {
		t.Fatalf("Approve was not offered after the card was read:\n%s", f.view())
	}
	f.typeKeys("ac34")
	f.press(enterKey)
	f.noDecision("the code of the withdrawn card")
	f.seenAll("that is not the code: a new code is in the title")
	if got := f.titleCode(); got != "FG9Y" {
		t.Fatalf("the code after a wrong one is %q", got)
	}
	f.typeKeys("fg9y")
	f.press(enterKey)
	if len(f.b.decs) != 1 || f.b.decs[0].ID != second || !f.b.decs[0].Approve || f.b.errs[0] != nil {
		t.Fatalf("the new card was not decided: %+v %v\n%s", f.b.decs, f.b.errs, f.view())
	}
}

// ---------------------------------------------------------------- pages

// TestApprovalsFlowATruncatedListMustBeReadPageByPage: the sample card whose
// record holds 100 of 5,000 paths may not be approved until each page of the
// whole list was read to its end, from the first to the last.
func TestApprovalsFlowATruncatedListMustBeReadPageByPage(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.openCardID("ap-sample-many-paths")
	f.readAll()
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatalf("a cut list was approved on the card alone:\n%s", f.view())
	}
	f.seenAll("approve is off: " + card.ReasonPathPages)
	const pages = 50
	for n := 0; n < pages; n++ {
		if n > 0 {
			f.press(ch(']'))
		}
		f.readAll()
		f.press(ch('a'))
		open := f.m.ap.view.FieldOpen()
		if n < pages-1 && open {
			t.Fatalf("Approve was offered after %d of %d pages:\n%s", n+1, pages, f.view())
		}
		if n == pages-1 && !open {
			t.Fatalf("Approve was not offered after every page was read:\n%s", f.view())
		}
		if n < pages-1 {
			f.seenAll("approve is off: " + card.ReasonPathPages)
		}
	}
	f.noDecision("pages")
}

// TestApprovalsFlowASkippedPageKeepsApproveOff: a page that was turned past and
// not read keeps Approve off, though the pages around it were read.
func TestApprovalsFlowASkippedPageKeepsApproveOff(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.openCardID("ap-sample-many-paths")
	f.readAll()
	f.press(ch(']'))
	f.press(ch(']')) // page 2 is shown, page 1 was not read
	for n := 2; n < 50; n++ {
		f.readAll()
		if n < 49 {
			f.press(ch(']'))
		}
	}
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatalf("Approve was offered with a page not read:\n%s", f.view())
	}
}

// ---------------------------------------------------------------- the limit

// TestApprovalsFlowACardDecidedPastTheLimitComesBackWithItsMark: the sample
// card of the project that has used its limit is approved, and comes back in
// the list with its mark, still waiting. A deny of it is not held: it is
// decided, and the mark goes.
func TestApprovalsFlowACardDecidedPastTheLimitComesBackWithItsMark(t *testing.T) {
	const id = "ap-sample-held"
	mark := "[" + card.HeldLabel + "] feature finish name=past-the-limit"
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.openList()
	f.seenAll(mark)

	f.openCardID(id)
	f.readAll()
	f.press(ch('a'))
	f.typeKeys(strings.ToLower(f.titleCode()))
	f.press(enterKey)
	if len(f.b.outs) != 1 || !f.b.outs[0].Held || f.b.outs[0].State != record.StatePending {
		t.Fatalf("the approve past the limit was not held: %+v\n%s", f.b.outs, f.view())
	}
	if f.m.ap.view.CardOpen() {
		t.Fatalf("the held card is still open:\n%s", f.view())
	}
	f.seenAll(card.HeldLabel+": it is back in the list", mark, "Pending (12)")
	if l := f.b.listing(); len(l.Recent) != 2 {
		t.Fatalf("the held card was decided: %d recent", len(l.Recent))
	}

	// The card, opened again, is a new card with its own code, and a deny of
	// it is decided and not held.
	f.openCardID(id)
	f.press(ch('x'))
	f.typeKeys("not now")
	f.press(enterKey)
	if len(f.b.outs) != 2 || f.b.outs[1].Held || f.b.outs[1].State != record.StateDenied {
		t.Fatalf("the deny past the limit was held: %+v\n%s", f.b.outs, f.view())
	}
	f.seenAll("state denied", "sample: denied; nothing was changed")
	f.press(escKey)
	f.seenAll("Pending (11)", "denied  feature finish name=past-the-limit")
	f.notSeenAny("[" + card.HeldLabel + "]")
}

// TestApprovalsFlowTheLimitScreen walks the limit's screen: one key lowers, a
// raise has to be typed, and each refusal shows the reason the limit rules give
// for it. Nothing is sent to the backend that the screen's rule refuses.
func TestApprovalsFlowTheLimitScreen(t *testing.T) {
	reason := func(present int, raised bool, to int, typed bool) string {
		t.Helper()
		err := limits.Check(present, raised, limits.Change{To: to, Typed: typed})
		if err == nil {
			t.Fatalf("the rule takes %d from %d", to, present)
		}
		return err.Error()
	}
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.openList()
	f.press(ch('L'))
	if f.m.ap.limit == nil {
		t.Fatalf("L did not open the limit's screen:\n%s", f.view())
	}
	f.seenAll("DAILY CARD LIMIT · project-a", "present limit 12", "suggestion 10")

	// A raise is refused for its size, and a number outside the rule for the
	// rule; none of them is sent.
	for _, c := range []struct {
		typed string
		why   string
	}{
		{"19", reason(12, false, 19, true)},
		{"41", reason(12, false, 41, true)},
		{"0", reason(12, false, 0, true)},
	} {
		f.typeKeys(c.typed)
		f.press(enterKey)
		f.seenAll(c.why)
		for range c.typed {
			f.press(tea.KeyPressMsg{Code: tea.KeyBackspace})
		}
	}
	if len(f.b.changes) != 0 {
		t.Fatalf("a refused number was sent: %+v", f.b.changes)
	}

	// One key lowers: Enter takes the lower suggestion.
	f.press(enterKey)
	if len(f.b.changes) != 1 || f.b.changes[0] != (limits.Change{To: 10}) {
		t.Fatalf("the suggestion was not taken: %+v\n%s", f.b.changes, f.view())
	}
	f.seenAll("present limit 10")

	// A raise is typed, and is taken once a day.
	f.typeKeys("15")
	f.press(enterKey)
	if len(f.b.changes) != 2 || f.b.changes[1] != (limits.Change{To: 15, Typed: true}) {
		t.Fatalf("the typed raise was not sent: %+v\n%s", f.b.changes, f.view())
	}
	f.seenAll("present limit 15")
	f.typeKeys("20")
	f.press(enterKey)
	f.seenAll(reason(15, true, 20, true))
	if len(f.b.changes) != 2 {
		t.Fatalf("a second raise in a day was sent: %+v", f.b.changes)
	}
	// Esc closes the screen to the list, and Esc again goes to the cockpit.
	f.press(escKey)
	if f.m.ap.limit != nil || f.m.ap.view == nil {
		t.Fatal("Esc did not go back to the list")
	}
	f.press(escKey)
	if f.m.apprOpen() {
		t.Fatal("Esc did not go back to the cockpit")
	}

	// A project whose suggestion is higher than its limit: Enter does not
	// raise, and says why.
	f.openList()
	for i := 0; i < 11; i++ {
		f.press(ch('j'))
	}
	f.press(ch('L'))
	f.seenAll("DAILY CARD LIMIT · project-c", "present limit 2", "suggestion 6")
	f.press(enterKey)
	f.seenAll(reason(2, false, 6, true))
	if len(f.b.changes) != 2 {
		t.Fatalf("a raise was taken with one key: %+v", f.b.changes)
	}
}

// ---------------------------------------------------------------- the strip

// TestApprovalsFlowTheArrivalStripShowsOncePerArrival: the strip shows at the
// first action after the owner was away, once, and shows again only at the next
// arrival. The key that woke the console goes where it was aimed. A digit takes
// a line only in console focus with nothing else open, never the digit that
// woke the console, and the strip is not drawn in session focus.
func TestApprovalsFlowTheArrivalStripShowsOncePerArrival(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	if f.m.ap.strip.Visible() {
		t.Fatal("the strip showed before any action")
	}
	start := f.m.treeCursor
	f.press(ch('j')) // the arrival
	if !f.m.ap.strip.Visible() {
		t.Fatal("the arrival did not show the strip")
	}
	if f.m.treeCursor != start+1 {
		t.Fatalf("the key that woke the console did not go where it was aimed: cursor %d", f.m.treeCursor)
	}
	f.seenAll("while you were away", "approvals waiting 12 · oldest 2 h", "held notices 1 · "+card.HeldLabel+": project-c")
	// Another key is no arrival. Esc closes the strip, and no later key brings
	// it back.
	f.press(escKey)
	f.notSeenAny("while you were away")
	f.press(ch('k'))
	f.press(ch('j'))
	if f.m.ap.strip.Visible() {
		t.Fatal("the strip came back without an arrival")
	}
	// Eleven minutes with no key, and the next one is an arrival again. In
	// session focus the strip is not drawn, and takes no key; the key goes to
	// the session.
	f.focusSession()
	f.clk.Advance(11 * time.Minute)
	keys := len(f.rig.sink.keys)
	f.press(ch('1'))
	if !f.m.ap.strip.Visible() {
		t.Fatal("the next arrival did not show the strip")
	}
	f.notSeenAny("while you were away")
	f.press(ch('1'))
	if f.m.apprOpen() || len(f.rig.sink.keys) != keys+2 {
		t.Fatalf("a digit in session focus reached the strip: %v", f.rig.sink.keys)
	}
	// Back in console focus the strip is drawn, with the waking digit not
	// taken: the first digit after the return opens the line.
	f.press(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	if f.m.focus != consoleFocus {
		t.Fatal("the return key did not return")
	}
	f.seenAll("while you were away")
	f.press(ch('?'))
	f.press(ch('1'))
	if f.m.apprOpen() {
		t.Fatal("a digit reached the strip with an overlay open")
	}
	f.press(escKey)
	f.press(ch('1'))
	if !f.m.apprOpen() || f.m.ap.strip.Visible() {
		t.Fatalf("the digit did not open the line of the waiting cards:\n%s", f.view())
	}
	// The line opens the oldest waiting card, to read: the earlier of the
	// pair, which has no differs-from block.
	if f.m.ap.view == nil || !f.m.ap.view.CardOpen() {
		t.Fatalf("the digit did not open a card:\n%s", f.view())
	}
	f.seenAll("name=pair-feature")
	f.notSeenAny("Differs from")
	f.noDecision("the strip")
}

// TestApprovalsFlowADigitThatWakesTheConsoleIsNotTheStrips: the first digit after
// the owner was away only wakes the console; the next one opens the line, and
// the card of it is the oldest waiting.
func TestApprovalsFlowADigitThatWakesTheConsoleIsNotTheStrips(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.press(ch('1'))
	if !f.m.ap.strip.Visible() || f.m.apprOpen() {
		t.Fatal("the waking digit was taken by the strip")
	}
	f.view() // the strip is drawn, so its lines can be opened
	f.press(ch('1'))
	if !f.m.apprOpen() || f.m.ap.strip.Visible() {
		t.Fatalf("the next digit did not open the line:\n%s", f.view())
	}
	f.seenAll("APPROVAL "+flowCode, "name=pair-feature")
}

// TestApprovalsFlowAnEscThatWakesTheConsoleDoesNotCloseTheStrip: the first key
// after the owner was away, when it is Esc, only wakes the console: the strip
// that was waiting for him stays, and the next Esc closes it.
func TestApprovalsFlowAnEscThatWakesTheConsoleDoesNotCloseTheStrip(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	f.press(escKey)
	if !f.m.ap.strip.Visible() || f.m.apprOpen() {
		t.Fatalf("the waking Esc was taken by the strip:\n%s", f.view())
	}
	f.view() // the strip is drawn
	f.press(escKey)
	if f.m.ap.strip.Visible() {
		t.Fatal("the next Esc did not close the strip")
	}
}

// TestApprovalsFlowTheGateInThePaneOpensTheCardItNames: Enter on the pending
// gate in the right pane opens the oldest waiting card, the one the gate
// names, and reading it asks nothing of the backend but its card.
func TestApprovalsFlowTheGateInThePaneOpensTheCardItNames(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.poll()
	// The pane wraps its lines, so the text is compared without spaces.
	gate := strings.ReplaceAll(f.evidence(), " ", "")
	if !strings.Contains(gate, "Action:featurefinishname=pair-feature") ||
		!strings.Contains(gate, "State:pending,12waiting(oldest2h)") {
		t.Fatalf("the gate does not name the oldest card: %s", gate)
	}
	f.focusPane(paneEvidence)
	f.press(enterKey)
	if f.m.ap.view == nil || !f.m.ap.view.CardOpen() {
		t.Fatalf("Enter on the gate did not open a card:\n%s", f.view())
	}
	// The card is the one the gate named: the earlier of the pair.
	f.seenAll("name=pair-feature", "project-b")
	f.noDecision("the gate")
}

// ---------------------------------------------------------------- presence

// TestApprovalsFlowADecisionWithNoRecentActivityIsRefused: a backend that has
// heard no presence beat refuses a decision as owner-away. The console says so,
// the card is still pending, and once the beat comes the same card may be
// decided, with a new code.
func TestApprovalsFlowADecisionWithNoRecentActivityIsRefused(t *testing.T) {
	f := newFlowRig(t, 140, 45)
	f.b.dropActs = true
	f.poll()
	f.openCardID("ap-sample-clean")
	f.readAll()
	f.press(ch('a'))
	first := f.titleCode()
	f.typeKeys(strings.ToLower(first))
	f.press(enterKey)
	if len(f.b.decs) != 1 || f.b.errs[0] == nil {
		t.Fatalf("the decision was not refused: %+v %v", f.b.decs, f.b.errs)
	}
	if code, ok := record.CodeOf(f.b.errs[0]); !ok || code != record.CodeOwnerAway {
		t.Fatalf("the refusal is %v", f.b.errs[0])
	}
	f.seenAll("the decision was not accepted (owner-away)")
	f.notSeenAny("state approved")
	if l := f.b.listing(); len(l.Pending) != 12 {
		t.Fatalf("the refused card left the pending list: %d", len(l.Pending))
	}
	if f.titleCode() == "" {
		t.Fatalf("the card cannot be tried again:\n%s", f.view())
	}

	// The beat reaches the backend a minute on, and the card is decided.
	f.b.dropActs = false
	f.clk.Advance(2 * time.Minute)
	f.press(ch('a'))
	f.typeKeys(strings.ToLower(f.titleCode()))
	f.press(enterKey)
	if len(f.b.decs) != 2 || f.b.errs[1] != nil || !f.b.decs[1].Approve {
		t.Fatalf("the second try did not decide: %+v %v\n%s", f.b.decs, f.b.errs, f.view())
	}
	f.seenAll("state approved")
}

// ---------------------------------------------------------------- the whole

// TestApprovalsFlowAWholeApproveAt80By24 is a whole approve, from g to the
// outcome line, in a window of the smallest size that may decide: open the
// list, open the card, read it, press a, type the code the title shows, press
// Enter.
func TestApprovalsFlowAWholeApproveAt80By24(t *testing.T) {
	f := newFlowRig(t, 80, 24)
	f.poll()
	f.press(ch('g'))
	f.seenAll("APPROVALS", "Pending (12)", "sample cards: a decision changes nothing")
	f.press(enterKey)
	f.clk.Advance(time.Second)
	f.seenAll("APPROVAL "+flowCode, "name=small-fix")
	// Not read yet: Approve is off, and says why.
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatal("Approve was offered before the card was read")
	}
	f.readAll()
	f.seenAll("approve is ready: press [a], then type the code from the title")
	f.press(ch('a'))
	f.seenAll("code> ____")
	f.typeKeys(strings.ToLower(f.titleCode()))
	f.seenAll("code> ****")
	f.noDecision("before Enter")
	f.press(enterKey)
	if len(f.b.decs) != 1 {
		t.Fatalf("no decision:\n%s", f.view())
	}
	d := f.b.decs[0]
	c, _ := f.b.d.Open("ap-sample-clean")
	if !d.Approve || d.ID != "ap-sample-clean" || d.BindingSHA256 != c.BindingSHA256 || len(d.SignedOff) != 0 || d.Reason != "" {
		t.Fatalf("the decision is %+v", d)
	}
	f.seenAll("Outcome · after the decision", "state approved", "sample: approved; nothing was changed")
	if f.titleCode() != "" {
		t.Fatal("the code is still drawn after the decision")
	}
	f.press(ch('a'))
	if f.m.ap.view.FieldOpen() {
		t.Fatal("a decided card may be approved again")
	}
	f.seenAll("this request was decided")
	rows := strings.Split(f.view(), "\n")
	if len(rows) != 24 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > 80 {
			t.Fatalf("a row is wider than 80 cells: %q", row)
		}
	}
	f.press(escKey)
	f.seenAll("11 pending", "approved  feature finish name=small-fix")
}

// TestApprovalsFlowApproveAndDenyAreNotOfferedBelow80By24: in a window under
// 80 columns or 24 rows the card can be read, and neither Approve nor Deny is
// offered, each with the reason; the code is not drawn.
func TestApprovalsFlowApproveAndDenyAreNotOfferedBelow80By24(t *testing.T) {
	for _, size := range [][2]int{{79, 24}, {80, 23}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			f := newFlowRig(t, 80, 24)
			f.poll()
			f.openCardID("ap-sample-clean")
			f.readAll()
			f.press(ch('a'))
			if !f.m.ap.view.FieldOpen() {
				t.Fatal("Approve is not offered at 80 by 24, the control of this test")
			}
			f.press(escKey)
			f.pump(f.size(size[0], size[1]))
			f.readAll()
			if got := f.titleCode(); got != "" {
				t.Fatalf("the code is drawn in a window of %v: %q", size, got)
			}
			f.press(ch('a'))
			if f.m.ap.view.FieldOpen() {
				t.Fatal("Approve was offered")
			}
			f.seenAll("approve is off: needs a window of 80 columns by 24 rows")
			f.press(ch('x'))
			if f.m.ap.view.FieldOpen() {
				t.Fatal("Deny was offered")
			}
			f.seenAll("deny is off: needs a window of 80 columns by 24 rows")
			f.noDecision("a small window")
		})
	}
}

// ---------------------------------------------------------------- verdicts

// TestApprovalsFlowTheVerdictAdviceComesAfterTheDecision: a card shows the
// advice of the verdict runs only once it was decided, and before it nothing
// but the one header line of a card that exists because a run refused. This
// is the display check from end to end. The backend wrapper below sets the
// card's fields as card.New does, so the rule that gates them is proved in
// the card and approvals packages, not here.
func TestApprovalsFlowTheVerdictAdviceComesAfterTheDecision(t *testing.T) {
	const id = "ap-sample-clean"
	f := newFlowRig(t, 140, 45)
	// The backend of the real service gives a card that is not decided only
	// the mark that a run refused, and a decided card its runs.
	f.b.change[id] = func(c *card.Card) {
		if !c.Decided() {
			c.Refused = true
			return
		}
		c.Verdicts = &card.VerdictBlock{Refused: true, Runs: [2]card.Run{
			{ID: "run-1", Provider: "provider-a", Model: "model-a", Approve: false},
			{ID: "run-2", Provider: "provider-b", Model: "model-b", Approve: true},
		}}
	}
	f.poll()
	f.openCardID(id)
	f.seenAll("this card is here because a verdict run refused")
	f.notSeenAny("verdicts (", "provider-a", "model-a", "run 1", "advice of this program")
	f.press(ch('x'))
	f.typeKeys("no")
	f.press(enterKey)
	if len(f.b.decs) != 1 || f.b.decs[0].Approve {
		t.Fatalf("the deny was not sent:\n%s", f.view())
	}
	// The card that was open is read again after the decision: it shows both
	// runs, labelled as advice. The refused line is now the one inside the
	// verdicts, and not also the one of the header.
	f.seenAll("verdicts (advice of this program, not in any record)", "run 1", "provider-a / model-a", "run 2")
	if n := strings.Count(f.view(), "this card is here because a verdict run refused"); n != 1 {
		t.Fatalf("the refused line is drawn %d times, not once:\n%s", n, f.view())
	}
	f.press(escKey)
	// Opened again, the decided card shows the same.
	f.openCardID(id)
	f.seenAll("verdicts (advice of this program, not in any record)", "run 1", "provider-a / model-a", "run 2")

	// A card that was decided before shows its two runs, labelled as advice,
	// and the list says how often they agreed with the owner.
	f.openCardID("ap-sample-verdict-agreed")
	f.seenAll("verdicts (advice of this program, not in any record)", "run 1", "provider-a / model-a")
	f.press(escKey)
	f.seenAll("verdict runs agreed with you on 1 of 2 (advice of this program, not in any record)")
}
