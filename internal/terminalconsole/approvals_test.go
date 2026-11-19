package terminalconsole

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

// The tests of the approvals pieces of the cockpit. They use a fake backend
// of their own: the console imports no sample backend, and a fake can say
// what the cockpit asked of it and when.

var apprT0 = time.Date(2026, 11, 18, 14, 2, 0, 0, time.UTC)

// testClock is the clock a test injects.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeApprovals is a backend for tests. It fails the test when a method is
// called anywhere but inside a command that the rig runs.
type fakeApprovals struct {
	t     *testing.T
	mu    sync.Mutex
	inCmd bool

	listing  card.Listing
	listErr  error
	cards    map[string]card.Card
	view     limits.View
	setErr   error
	decider  bool
	activity []time.Time
	looked   []time.Time

	opens     []string
	decisions []card.Decision
	limitFor  []string
	changes   []limits.Change
}

func newFakeApprovals(t *testing.T) *fakeApprovals {
	return &fakeApprovals{t: t, cards: map[string]card.Card{}, decider: true,
		view: limits.View{Project: "project-a", Limit: 12, Suggestion: 12}}
}

func (f *fakeApprovals) guard(what string) {
	if !f.inCmd {
		f.t.Errorf("%s was called outside a command", what)
	}
}

func (f *fakeApprovals) List() (card.Listing, error) {
	f.guard("List")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listing, f.listErr
}

func (f *fakeApprovals) Open(id string) (card.Card, error) {
	f.guard("Open")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, id)
	c, ok := f.cards[id]
	if !ok {
		return card.Card{}, &record.Error{Code: record.CodeRecordInvalid}
	}
	return c, nil
}

func (f *fakeApprovals) Page(string, card.PageKind, int) (card.Page, error) {
	f.guard("Page")
	return card.Page{}, errors.New("no pages")
}

func (f *fakeApprovals) Decide(d card.Decision) (card.Outcome, error) {
	f.guard("Decide")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decisions = append(f.decisions, d)
	return card.Outcome{State: record.StateApplied, Line: "applied: sample"}, nil
}

func (f *fakeApprovals) Decider() (bool, string) {
	f.guard("Decider")
	return f.decider, ""
}

func (f *fakeApprovals) Limit(project string) (limits.View, error) {
	f.guard("Limit")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limitFor = append(f.limitFor, project)
	v := f.view
	v.Project = project
	return v, nil
}

func (f *fakeApprovals) SetLimit(project string, c limits.Change) (limits.View, error) {
	f.guard("SetLimit")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, c)
	if f.setErr != nil {
		return limits.View{}, f.setErr
	}
	f.view.Limit = c.To
	v := f.view
	v.Project = project
	return v, nil
}

func (f *fakeApprovals) Activity(at time.Time) {
	f.guard("Activity")
	f.mu.Lock()
	f.activity = append(f.activity, at)
	f.mu.Unlock()
}

func (f *fakeApprovals) Looked(at time.Time) {
	f.guard("Looked")
	f.mu.Lock()
	f.looked = append(f.looked, at)
	f.mu.Unlock()
}

// add makes a card pending, asked the given time before apprT0.
func (f *fakeApprovals) add(id, project string, askedAgo time.Duration) card.Card {
	c := apprCard(id, project)
	c.Title.Asked = apprT0.Add(-askedAgo)
	c.Title.Expires = apprT0.Add(30*time.Minute - askedAgo)
	f.cards[id] = c
	f.listing.Pending = append(f.listing.Pending, c.Item())
	return c
}

// apprCard returns a card with no diff, so that its ref lines are what has to
// be read, and no flagged path.
func apprCard(id, project string) card.Card {
	return card.Card{
		Title: card.Title{
			ID: id, Project: project,
			Subject: card.Subject{Kind: "seat", Role: "lead-a", DisplayID: "lead-1", Runtime: "runtime-a"},
			Asked:   apprT0, Expires: apprT0.Add(30 * time.Minute),
		},
		Verb: card.VerbLine{Name: "feature finish", Feature: "feature-x", Target: "develop", Class: "ref-move"},
		Plan: card.Plan{Token: "h-3fa9c2e1", Lines: []card.LeadText{card.NewLeadText("merge feature/feature-x into develop")}},
		Refs: []card.RefLine{{Kind: "ref", Ref: card.NewLeadText("refs/heads/develop"),
			Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40)}},
		BindingSHA256: "binding-" + id,
		Note:          card.NewLeadText("a note"),
	}
}

// codeReader gives the bytes of the session id and then of the confirm codes
// a test wants. The session id reads eight bytes; the card's code reads 16 at
// a time, and the model keeps only the bytes below the largest multiple of
// the alphabet's size, so each code character is the index of the character
// in the alphabet and the rest of the 16 bytes is 0xff.
type codeReader struct {
	codes []string
}

const codeAlphabetForTest = "ACDEFGHJKLMNPQRTUVWXY34679"

func (r *codeReader) Read(p []byte) (int, error) {
	if len(p) == 8 {
		for i := range p {
			p[i] = byte(i + 1)
		}
		return len(p), nil
	}
	if len(r.codes) == 0 {
		return 0, errors.New("no more codes")
	}
	code := r.codes[0]
	r.codes = r.codes[1:]
	for i := range p {
		p[i] = 0xff
	}
	for i := 0; i < len(code) && i < len(p); i++ {
		p[i] = byte(strings.IndexByte(codeAlphabetForTest, code[i]))
	}
	return len(p), nil
}

// apprRig is a rig with an approvals backend, a clock of its own and the
// commands the model returns run on the spot.
type apprRig struct {
	*rig
	b   *fakeApprovals
	clk *testClock
	// out collects the messages of commands that no Update took: what the
	// terminal would be sent.
	raw  []string
	quit bool
}

func newApprRig(t *testing.T, w, h int, b *fakeApprovals, o Options, codes ...string) *apprRig {
	t.Helper()
	clk := &testClock{t: apprT0}
	o.Backend, o.Now = b, clk.Now
	o.Random = &codeReader{codes: codes}
	r := &apprRig{b: b, clk: clk}
	r.rig = newRig(t, 0, 0, o)
	if w > 0 {
		r.pump(r.size(w, h))
	}
	return r
}

// pump runs cmd and every command that follows, handing each answer to the
// model. A timer the model schedules is left in the rig's list.
func (r *apprRig) pump(cmd tea.Cmd) {
	r.t.Helper()
	if cmd == nil {
		return
	}
	r.b.inCmd = true
	msg := cmd()
	r.b.inCmd = false
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			r.pump(c)
		}
	case tea.RawMsg:
		r.raw = append(r.raw, v.Msg.(string))
	case tea.QuitMsg:
		r.quit = true
	default:
		_, next := r.m.Update(msg)
		r.pump(next)
	}
}

// send hands msg to the model and runs what it returns.
func (r *apprRig) send(msg tea.Msg) {
	r.t.Helper()
	_, cmd := r.m.Update(msg)
	r.pump(cmd)
}

// press presses a key, 100 ms after the last one.
func (r *apprRig) press(k tea.KeyPressMsg) {
	r.t.Helper()
	r.clk.Advance(100 * time.Millisecond)
	r.send(k)
}

// typeText presses each character of s.
func (r *apprRig) typeText(s string) {
	r.t.Helper()
	for _, c := range s {
		r.press(tea.KeyPressMsg{Code: c, Text: string(c)})
	}
}

// poll reads the backend's listing, as the cockpit's timer does.
func (r *apprRig) poll() { r.send(apprTickMsg{at: r.clk.Now()}) }

func ch(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c, Text: string(c)} }

// open opens approvals with g from console focus.
func (r *apprRig) open() {
	r.t.Helper()
	r.press(ch('g'))
	if !r.m.apprOpen() {
		r.t.Fatalf("g did not open approvals:\n%s", r.view())
	}
}

func (r *apprRig) seen(texts ...string) {
	r.t.Helper()
	v := r.view()
	flat := strings.Join(strings.Fields(v), " ")
	for _, want := range texts {
		if !strings.Contains(v, want) && !strings.Contains(flat, strings.Join(strings.Fields(want), " ")) {
			r.t.Fatalf("the screen does not hold %q:\n%s", want, v)
		}
	}
}

// evidence returns the text of the right pane, wrapped lines joined.
func (r *apprRig) evidence() string {
	var parts []string
	for _, l := range r.m.evidenceLines(r.m.layout().evidence.Dx() - 2) {
		parts = append(parts, l.s)
	}
	return strings.Join(parts, " ")
}

func (r *apprRig) notSeen(texts ...string) {
	r.t.Helper()
	v := r.view()
	for _, bad := range texts {
		if strings.Contains(v, bad) {
			r.t.Fatalf("the screen holds %q:\n%s", bad, v)
		}
	}
}

func (r *apprRig) focusPane(pane int) {
	r.t.Helper()
	for i := 0; r.m.pane != pane; i++ {
		if i > paneCount {
			r.t.Fatal("no such pane")
		}
		r.press(tabKey)
	}
}

// openFirstCard opens the first pending card and lets the 400 ms pass.
func (r *apprRig) openFirstCard() {
	r.t.Helper()
	r.open()
	r.press(enterKey)
	if !r.m.ap.view.CardOpen() {
		r.t.Fatalf("no card opened:\n%s", r.view())
	}
	r.clk.Advance(time.Second)
}

// readCard scrolls to the end of the open card, so that its ref lines count
// as read.
func (r *apprRig) readCard() {
	r.t.Helper()
	for i := 0; i < 40; i++ {
		r.press(ch('j'))
	}
}

func oneCardBackend(t *testing.T) *fakeApprovals {
	b := newFakeApprovals(t)
	b.add("ap-card-1", "project-a", 5*time.Minute)
	return b
}

func TestWithNoBackendApprovalsSaysThereIsNoOwnerChannel(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.key(ch('g'))
	if !r.m.apprOpen() || !strings.Contains(r.view(), NoOwnerChannel) {
		t.Fatal(r.view())
	}
	// Keys are not the cockpit's here: q does not quit, and nothing decides.
	for _, k := range []tea.KeyPressMsg{ch('q'), ch('a'), ch('j'), enterKey} {
		if cmd := r.key(k); cmd != nil {
			t.Fatalf("%v returned a command", k)
		}
	}
	if !strings.Contains(r.view(), NoOwnerChannel) {
		t.Fatal("the screen went away")
	}
	r.key(escKey)
	if r.m.apprOpen() || !strings.Contains(r.view(), "Projects & leads") {
		t.Fatal(r.view())
	}
	// ctrl+c quits from the screen.
	r.key(ch('g'))
	if cmd := r.key(ctrlC); cmd == nil {
		t.Fatal("ctrl+c did not quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit")
	}
}

func TestWithNoBackendTheActionListKeepsItsReasons(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.key(ctrlK)
	v := wordsOf(r.view())
	for _, want := range []string{"Approve pending gate [Ctrl+K]", "Unavailable: No grant is honoured until the owner channel is qualified.", "Deny pending gate"} {
		if !strings.Contains(v, want) {
			t.Fatalf("%q missing:\n%s", want, r.view())
		}
	}
	// g in the list opens nothing: with no backend the controls are not live.
	r.key(ch('g'))
	if r.m.apprOpen() || r.m.overlay != "actions" {
		t.Fatal("g acted in the action list")
	}
}

func TestWithABackendTheActionListsApproveAndDenyAreLiveAndOpenTheView(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{})
	r.press(ctrlK)
	r.seen("ACTIONS (approve and deny open the approvals view)", "Approve pending gate [g]", "Available: available: g opens")
	r.notSeen("No grant is honoured")
	r.seen("Full attach [a]", "Unavailable: Full attach needs")
	r.press(ch('g'))
	if !r.m.apprOpen() || r.m.overlay != "" {
		t.Fatalf("g in the action list did not open approvals:\n%s", r.view())
	}
	r.seen("APPROVALS")
}

func TestGOpensApprovalsFromConsoleFocusOnly(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{})
	r.focusSession()
	r.press(ch('g'))
	if r.m.apprOpen() {
		t.Fatal("g in session focus opened approvals")
	}
	if got := r.sink.keys; len(got) != 1 || got[0] != "g" {
		t.Fatalf("the session got %v, want the g", got)
	}
	// Nothing was asked of the backend for approvals by that key.
	if len(r.b.opens) != 0 || len(r.b.decisions) != 0 {
		t.Fatal("the backend was used")
	}
	r.press(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	if r.m.focus != consoleFocus {
		t.Fatal("the return key did not return")
	}
	r.open()
	r.seen("APPROVALS", "1 pending")
}

func TestGDoesNothingInZoom(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{})
	r.press(zoomPress)
	r.press(ch('g'))
	if r.m.apprOpen() {
		t.Fatal("g opened an invisible approvals screen in zoom")
	}
}

func TestEscWalksBackFromFieldToCardToListToCockpit(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{}, "7Q4K")
	r.openFirstCard()
	r.press(ch('x'))
	if !r.m.ap.view.FieldOpen() {
		t.Fatalf("x did not open the deny reason:\n%s", r.view())
	}
	r.press(escKey)
	if r.m.ap.view.FieldOpen() || !r.m.ap.view.CardOpen() {
		t.Fatal("Esc did not close the field only")
	}
	r.press(escKey)
	if r.m.ap.view == nil || r.m.ap.view.CardOpen() {
		t.Fatal("Esc did not go from the card to the list")
	}
	r.seen("APPROVALS", "Pending (1)")
	r.press(escKey)
	if r.m.apprOpen() {
		t.Fatal("Esc did not leave the list")
	}
	r.seen("Projects & leads")
	if len(b.looked) != 1 {
		t.Fatalf("leaving the list moved the mark %d times", len(b.looked))
	}
}

func TestKeysGoToTheViewBeforeTheCockpitsOwn(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{})
	r.open()
	before := r.m.treeCursor
	for _, c := range "qaTk?r" {
		r.press(ch(c))
		if r.quit || !r.m.apprOpen() {
			t.Fatalf("%q reached a cockpit command", c)
		}
	}
	if r.m.notice != "" || r.m.overlay != "" || r.m.treeCursor != before || len(r.sink.keys) != 0 {
		t.Fatal("a key of the list reached the cockpit")
	}
	// ctrl+c quits from the list.
	if _, cmd := r.m.Update(ctrlC); cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
}

func TestLettersOfTheCodeAlphabetTypedIntoTheFieldsNeverReachTheCockpit(t *testing.T) {
	letters := "qajk"
	for _, c := range letters {
		if !strings.ContainsRune(strings.ToLower(codeAlphabetForTest), c) {
			t.Fatalf("%q is not a letter of the code alphabet", c)
		}
	}
	t.Run("deny reason", func(t *testing.T) {
		r := newApprRig(t, 140, 45, oneCardBackend(t), Options{}, "7Q4K")
		r.openFirstCard()
		r.press(ch('x'))
		before := r.m.treeCursor
		r.typeText(letters)
		if r.quit || !r.m.apprOpen() || !r.m.ap.view.FieldOpen() {
			t.Fatal("a letter left the field")
		}
		r.seen("reason> qajk_")
		if r.m.notice != "" || r.m.overlay != "" || r.m.treeCursor != before || len(r.sink.keys) != 0 {
			t.Fatal("a letter reached the cockpit")
		}
	})
	t.Run("confirm code", func(t *testing.T) {
		// The code is QAJK: typed in either case, every letter is the field's.
		b := oneCardBackend(t)
		r := newApprRig(t, 140, 45, b, Options{}, "QAJK")
		r.openFirstCard()
		r.readCard()
		r.press(ch('a'))
		if !r.m.ap.view.FieldOpen() {
			t.Fatalf("a did not open the confirm field:\n%s", r.view())
		}
		r.seen("APPROVAL QAJK")
		before := r.m.treeCursor
		r.typeText("qaj")
		if r.quit || !r.m.apprOpen() || !r.m.ap.view.FieldOpen() {
			t.Fatal("a letter left the field")
		}
		r.seen("code> ***_")
		r.typeText("k")
		r.seen("code> ****")
		if r.m.notice != "" || r.m.overlay != "" || r.m.treeCursor != before || len(r.sink.keys) != 0 {
			t.Fatal("a letter reached the cockpit")
		}
		r.press(enterKey)
		if len(b.decisions) != 1 || !b.decisions[0].Approve || b.decisions[0].ID != "ap-card-1" {
			t.Fatalf("decisions: %+v", b.decisions)
		}
		r.seen("applied: sample")
	})
	t.Run("digits and Esc reach the field, not the strip", func(t *testing.T) {
		b := oneCardBackend(t)
		r := newApprRig(t, 140, 45, b, Options{}, "34CD")
		r.poll()
		r.press(ch('j')) // an arrival: the strip shows
		r.view()
		if !r.m.ap.strip.Visible() {
			t.Fatal("no strip")
		}
		r.openFirstCard()
		r.readCard()
		r.press(ch('a'))
		r.seen("APPROVAL 34CD")
		r.typeText("34")
		r.seen("code> **__")
		r.press(escKey)
		if r.m.ap.view.FieldOpen() || !r.m.ap.view.CardOpen() {
			t.Fatal("Esc did not close the field only")
		}
		if r.m.ap.strip.Visible() {
			t.Fatal("the strip was left showing while approvals opened")
		}
	})
}

func TestEnterOnThePendingGateOpensTheOldestWaitingCard(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-newer", "project-a", 2*time.Minute)
	b.add("ap-oldest", "project-b", 40*time.Minute)
	b.add("ap-middle", "project-a", 9*time.Minute)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.focusPane(paneEvidence)
	r.press(enterKey)
	if !r.m.apprOpen() {
		t.Fatal("Enter on the gate did not open approvals")
	}
	if len(b.opens) != 1 || b.opens[0] != "ap-oldest" {
		t.Fatalf("opened %v, want the oldest card", b.opens)
	}
	if !r.m.ap.view.CardOpen() {
		t.Fatalf("no card is open:\n%s", r.view())
	}
	// Enter anywhere else does not open approvals.
	r2 := newApprRig(t, 140, 45, b, Options{})
	r2.press(enterKey)
	if r2.m.apprOpen() {
		t.Fatal("Enter in the tree opened approvals")
	}
}

func TestEnterOnTheGateOpensTheCardTheGateNamedWhenTheOrderChanged(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-newer", "project-a", 2*time.Minute)
	b.add("ap-oldest", "project-b", 40*time.Minute)
	b.add("ap-middle", "project-a", 9*time.Minute)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.focusPane(paneEvidence)
	// The backend orders the cards differently by the time the view reads
	// them: the gate's card is first now, not second.
	p := b.listing.Pending
	b.listing.Pending = []card.Item{p[1], p[2], p[0]}
	r.press(enterKey)
	if len(b.opens) != 1 || b.opens[0] != "ap-oldest" {
		t.Fatalf("the gate named ap-oldest and the view opened %v", b.opens)
	}
}

func TestEnterOnTheGateOpensNothingWhenTheViewsFirstListHasNoSuchCardAndNothingFiresLater(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-1", "project-a", 5*time.Minute)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.focusPane(paneEvidence)
	// The card was decided between the cockpit's reading and the view's.
	gone := b.listing.Pending
	b.listing.Pending = nil
	r.press(enterKey)
	if !r.m.apprOpen() || r.m.ap.view.CardOpen() || len(b.opens) != 0 {
		t.Fatalf("a card opened from an empty list: %v", b.opens)
	}
	if r.m.ap.wantOldest {
		t.Fatal("the drive stayed armed")
	}
	// The card comes back, and the view is refreshed on its timer: it is shown
	// in the list and nothing is opened.
	b.listing.Pending = gone
	r.poll()
	r.poll()
	if r.m.ap.view.Pending() != 1 || r.m.ap.view.CardOpen() || len(b.opens) != 0 {
		t.Fatalf("a later listing opened a card: %v\n%s", b.opens, r.view())
	}
}

func TestThePendingGateIsTheOldestWaitingCard(t *testing.T) {
	b := newFakeApprovals(t)
	r := newApprRig(t, 140, 45, b, Options{})
	if e := r.evidence(); !strings.Contains(e, "PENDING GATE State: reading the list") {
		t.Fatal(e)
	}
	r.poll()
	if e := r.evidence(); !strings.Contains(e, "No card waits.") {
		t.Fatal(e)
	}
	b.add("ap-newer", "project-a", 2*time.Minute)
	b.add("ap-oldest", "project-b", 40*time.Minute)
	r.poll()
	e := r.evidence()
	for _, want := range []string{"Action: feature finish name=feature-x", "Scope: project-b", "2 waiting (oldest 40 min)", "Enter opens the oldest waiting card"} {
		if !strings.Contains(e, want) {
			t.Fatalf("%q missing:\n%s", want, e)
		}
	}
	for _, bad := range []string{"Fixture project, one branch", "Merge feature branch into develop"} {
		if strings.Contains(e, bad) {
			t.Fatalf("%q still shown", bad)
		}
	}
}

func TestTheStatusLineCarriesTheApprovalsElement(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-1", "project-a", 3*time.Minute)
	b.add("ap-2", "project-a", 1*time.Minute)
	r := newApprRig(t, 100, 30, b, Options{})
	r.poll()
	rows := r.rows()
	last := rows[len(rows)-1]
	if !strings.Contains(last, "approvals 2 (oldest 3 min)") || !strings.Contains(last, "g approvals") {
		t.Fatalf("status line %q", last)
	}
	// No waiting card: no element.
	b.listing.Pending = nil
	r.poll()
	rows = r.rows()
	if strings.Contains(rows[len(rows)-1], "approvals 0") || strings.Contains(rows[len(rows)-1], "(oldest") {
		t.Fatalf("status line %q", rows[len(rows)-1])
	}
}

func TestTheStatusLineShowsAnArrivingCardInSessionFocus(t *testing.T) {
	b := newFakeApprovals(t)
	r := newApprRig(t, 100, 30, b, Options{})
	r.poll()
	r.focusSession()
	b.add("ap-new", "project-a", 4*time.Minute)
	r.poll()
	rows := r.rows()
	last := rows[len(rows)-1]
	if !strings.Contains(last, "approvals 1 (oldest 4 min)") || !strings.Contains(last, "SESSION FOCUS") {
		t.Fatalf("status line %q", last)
	}
	// The card gave no key to the session and rang nothing: he is present.
	if len(r.sink.keys) != 0 || len(r.raw) != 0 {
		t.Fatalf("keys %v, raw %q", r.sink.keys, r.raw)
	}
}

func TestTheTimelineShowsAskedDecidedAndApplied(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-wait", "project-a", 2*time.Minute)
	done := apprCard("ap-done", "project-b")
	done.Title.Asked = apprT0.Add(-50 * time.Minute)
	done.After = card.After{State: record.StateApplied, Line: "applied: develop moved"}
	denied := apprCard("ap-denied", "project-a")
	denied.Title.Asked = apprT0.Add(-30 * time.Minute)
	denied.After = card.After{State: record.StateDenied, Line: "denied by the owner"}
	b.listing.Recent = []card.Item{denied.Item(), done.Item()}
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	entries := r.m.approvalsTimeline()
	var got []string
	for _, e := range entries {
		got = append(got, e.Text)
	}
	want := []string{
		"asked: feature finish name=feature-x (project-b)", "decided: approved", "applied: applied · applied: develop moved",
		"asked: feature finish name=feature-x (project-a)", "decided: denied · denied by the owner",
		"asked: feature finish name=feature-x (project-a)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("timeline:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The newest entry is last and the pane shows them.
	r.seen("asked: feature finish name=feature-x (project-a)", "decided: denied · denied by the owner")
	// The source's own entries stay first.
	if len(r.m.allTimeline()) != len(r.m.timeline)+len(entries) || r.m.allTimeline()[0] != r.m.timeline[0] {
		t.Fatal("the source's timeline is not first")
	}
}

func TestTheHeaderSaysSamplesChangeNothing(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 100, 30, b, Options{})
	if strings.Contains(r.rows()[0], "sample cards") {
		t.Fatal("the header says sample before the listing is read")
	}
	b.listing.Sample = true
	r.poll()
	if !strings.Contains(r.rows()[0], "sample cards: a decision changes nothing") {
		t.Fatalf("header %q", r.rows()[0])
	}
}

func TestBackendIsCalledOnlyFromCommands(t *testing.T) {
	// The fake fails the test when a method runs outside the rig's commands:
	// run the whole path of opening, deciding and the limit once.
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{}, "7Q4K")
	r.poll()
	r.open()
	r.press(ch('L'))
	r.press(escKey)
	r.press(enterKey)
	r.clk.Advance(time.Second)
	r.press(ch('x'))
	r.typeText("no")
	r.press(enterKey)
	if len(b.decisions) != 1 || b.decisions[0].Approve || b.decisions[0].Reason != "no" {
		t.Fatalf("decisions %+v", b.decisions)
	}
}

func TestLTheLimitScreenIsOpenedForTheProjectUnderTheCursor(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-1", "project-a", 5*time.Minute)
	b.add("ap-2", "project-b", 4*time.Minute)
	r := newApprRig(t, 140, 45, b, Options{})
	r.open()
	r.press(ch('j'))
	r.press(ch('L'))
	if r.m.ap.limit == nil || len(b.limitFor) != 1 || b.limitFor[0] != "project-b" {
		t.Fatalf("limit asked for %v:\n%s", b.limitFor, r.view())
	}
	r.seen("DAILY CARD LIMIT", "project-b", "present limit  12")
	// Digits, Backspace and Enter are the limit screen's: 1 to 4 reach the
	// number, nothing reaches the strip or the cockpit.
	r.typeText("1234")
	if got := r.m.ap.limit.Typed(); got != "123" {
		t.Fatalf("typed %q, want the three digits the screen holds", got)
	}
	r.press(tea.KeyPressMsg{Code: tea.KeyBackspace})
	r.press(tea.KeyPressMsg{Code: tea.KeyBackspace})
	r.press(tea.KeyPressMsg{Code: tea.KeyBackspace})
	// A lower number is one key: type 9 and Enter.
	r.typeText("9")
	r.press(enterKey)
	if len(b.changes) != 1 || b.changes[0].To != 9 {
		t.Fatalf("changes %+v", b.changes)
	}
	r.seen("present limit  9")
	// Esc goes back to the list, then to the cockpit.
	r.press(escKey)
	if r.m.ap.limit != nil || r.m.ap.view == nil {
		t.Fatal("Esc did not go back to the list")
	}
	r.press(escKey)
	if r.m.apprOpen() {
		t.Fatal("Esc did not leave the list")
	}
}

func TestARefusedLimitChangeShowsItsReasonOnTheScreen(t *testing.T) {
	b := oneCardBackend(t)
	b.setErr = &record.Error{Code: record.CodeLimitRefused, Detail: "the limit was already raised today"}
	r := newApprRig(t, 140, 45, b, Options{})
	r.open()
	r.press(ch('L'))
	r.typeText("9")
	r.press(enterKey)
	r.seen("the change was refused (limit-refused): the limit was already raised today")
	if r.m.ap.limit == nil {
		t.Fatal("the screen closed")
	}
}

func TestTheArrivalStripShowsOncePerArrivalAndNeverTakesTheWakingKey(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-1", "project-a", 5*time.Minute)
	b.add("ap-2", "project-a", 2*time.Minute)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	if r.m.ap.strip.Visible() {
		t.Fatal("the strip showed before any action")
	}
	// The waking key goes where it was aimed: j moves the tree cursor.
	r.press(ch('j'))
	if !r.m.ap.strip.Visible() || r.m.treeCursor != 1 {
		t.Fatalf("strip %v, cursor %d", r.m.ap.strip.Visible(), r.m.treeCursor)
	}
	r.seen("while you were away", "approvals waiting 2")
	// The waking key was a digit this time? A digit that wakes the console is
	// refused by the strip: see below.
	// Esc closes the strip, and it does not come back with the next key.
	r.press(escKey)
	if r.m.ap.strip.Visible() {
		t.Fatal("Esc did not close the strip")
	}
	r.press(ch('j'))
	if r.m.ap.strip.Visible() {
		t.Fatal("the strip came back without an arrival")
	}
	// After a quiet stretch longer than the presence window the next action
	// is an arrival and shows it again.
	r.clk.Advance(11 * time.Minute)
	r.press(ch('k'))
	if !r.m.ap.strip.Visible() {
		t.Fatal("the strip did not show at the next arrival")
	}
}

func TestADigitThatWakesTheConsoleIsNotTheStrips(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.press(ch('1'))
	if !r.m.ap.strip.Visible() || r.m.apprOpen() {
		t.Fatal("the waking digit was taken by the strip")
	}
	r.view()
	// The next digit opens the line that was drawn: the waiting cards.
	r.press(ch('1'))
	if !r.m.apprOpen() || r.m.ap.strip.Visible() {
		t.Fatalf("the digit did not open the line:\n%s", r.view())
	}
}

func TestNoDigitReachesTheStripFromSessionFocus(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.press(ch('j')) // arrival
	r.view()
	if !r.m.ap.strip.Visible() {
		t.Fatal("no strip")
	}
	r.focusSession()
	r.view()
	r.press(ch('1'))
	r.press(escKey)
	if !r.m.ap.strip.Visible() || r.m.apprOpen() {
		t.Fatal("a key typed in session focus reached the strip")
	}
	got := r.sink.keys
	if len(got) < 2 || got[len(got)-2] != "1" || got[len(got)-1] != "esc" {
		t.Fatalf("the session got %v", got)
	}
}

func TestTheStripTakesNothingInAnOverlay(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.press(ch('j'))
	r.view()
	r.press(ch('?'))
	r.view()
	r.press(ch('1'))
	if r.m.apprOpen() || !r.m.ap.strip.Visible() || r.m.overlay != "help" {
		t.Fatal("the strip took a key while an overlay was open")
	}
}

func TestTheBellRingsForANewCardWhileAwayAndNeverForTheCardsAtStart(t *testing.T) {
	b := newFakeApprovals(t)
	b.add("ap-start", "project-a", 5*time.Minute)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	if len(r.raw) != 0 {
		t.Fatalf("the bell rang for a card that was waiting at start: %q", r.raw)
	}
	// A card that arrives while the owner has done nothing rings once.
	b.add("ap-new", "project-a", 0)
	r.poll()
	if len(r.raw) != 1 || r.raw[0] != "\a" {
		t.Fatalf("the bell: %q", r.raw)
	}
	r.poll()
	r.poll()
	if len(r.raw) != 1 {
		t.Fatalf("the bell rang again for the same card: %q", r.raw)
	}
	// While he is present, a new card does not ring.
	r.press(ch('j'))
	b.add("ap-present", "project-a", 0)
	r.poll()
	if len(r.raw) != 1 {
		t.Fatalf("the bell rang while he was present: %q", r.raw)
	}
	// And not for a card that arrived while it was present, once he is away.
	r.clk.Advance(time.Hour)
	r.poll()
	if len(r.raw) != 1 {
		t.Fatalf("the bell rang later for a card that came while he was present: %q", r.raw)
	}
	b.add("ap-away", "project-a", 0)
	r.poll()
	if len(r.raw) != 2 {
		t.Fatalf("the bell did not ring for a card that came while he was away: %q", r.raw)
	}
}

func TestTheBellCanBeTurnedOff(t *testing.T) {
	b := newFakeApprovals(t)
	r := newApprRig(t, 140, 45, b, Options{BellOff: true})
	r.poll()
	b.add("ap-new", "project-a", 0)
	r.poll()
	if len(r.raw) != 0 {
		t.Fatalf("the bell rang with the setting off: %q", r.raw)
	}
}

func TestPresenceTellsTheBackendAtMostOnceAMinute(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	for i := 0; i < 5; i++ {
		r.press(ch('j'))
	}
	if len(b.activity) != 1 {
		t.Fatalf("%d beats in the first minute", len(b.activity))
	}
	r.clk.Advance(61 * time.Second)
	r.press(ch('k'))
	r.press(ch('j'))
	if len(b.activity) != 2 {
		t.Fatalf("%d beats after a minute", len(b.activity))
	}
	// A pointer action is activity too.
	r.clk.Advance(61 * time.Second)
	r.send(tea.MouseClickMsg{X: 3, Y: 3, Button: tea.MouseLeft})
	if len(b.activity) != 3 {
		t.Fatalf("%d beats after a mouse click", len(b.activity))
	}
}

func TestAnActionByPointerIsAnArrival(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.send(tea.MouseClickMsg{X: 3, Y: 3, Button: tea.MouseLeft})
	if !r.m.ap.strip.Visible() {
		t.Fatal("a click did not show the strip")
	}
}

func TestAPasteWhileApprovalsIsOpenClearsTheConfirmFieldAndIsNotSentToTheSession(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{}, "QAJK")
	r.openFirstCard()
	r.readCard()
	r.press(ch('a'))
	r.typeText("QA")
	r.seen("code> **__")
	r.send(tea.PasteMsg{Content: "QAJK"})
	if len(r.sink.pastes) != 0 {
		t.Fatal("the paste reached the session")
	}
	r.seen("input that was not typed was refused, and the field was cleared")
}

func TestTheStripsHintIsDrawnOnlyWhereItTakesKeys(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{})
	r.poll()
	r.press(ch('j'))
	r.seen("a digit opens its line")
	r.focusSession()
	r.notSeen("a digit opens its line")
	r.seen("while you were away")
}

func TestApprovalsBelowTheSmallestWindowSaysWhyApproveIsNotOffered(t *testing.T) {
	for _, size := range [][2]int{{79, 23}, {70, 20}, {60, 30}} {
		b := oneCardBackend(t)
		r := newApprRig(t, size[0], size[1], b, Options{}, "7Q4K")
		// Below the cockpit's size the single-column pages show; g opens
		// approvals there too.
		r.press(ch('g'))
		if !r.m.apprOpen() {
			t.Fatalf("%v: g did not open approvals", size)
		}
		r.press(enterKey)
		r.clk.Advance(time.Second)
		r.readCard()
		r.seen("needs a window of 80 columns by 24 rows")
		r.press(ch('a'))
		if r.m.ap.view.FieldOpen() {
			t.Fatalf("%v: the confirm field opened in a window too small to decide", size)
		}
		r.notSeen("APPROVAL 7Q4K")
	}
}

func TestApprovalsTakesTheWindowWhateverItsSize(t *testing.T) {
	r := newApprRig(t, 3, 2, oneCardBackend(t), Options{})
	r.press(ch('g'))
	_ = r.view()
	r.size(5000, 5000)
	_ = r.view()
	if lines := strings.Count(r.m.View().Content, "\n") + 1; lines > maxCanvasRows {
		t.Fatalf("%d rows", lines)
	}
}

func TestTheListIsReadAgainOnATimerAndANewCardShowsInTheListOfTheView(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	// The cockpit schedules its next reading with its own timer.
	found := false
	for _, s := range r.sched {
		if _, ok := s.fn(apprT0).(apprTickMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("no timer for the next reading")
	}
	r.open()
	b.add("ap-2", "project-a", 0)
	r.poll() // the view refreshes with the tick
	r.seen("2 pending")
}

func TestAReadingThatFailsKeepsTheLastListing(t *testing.T) {
	b := oneCardBackend(t)
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	b.listErr = errors.New("down")
	r.poll()
	if e := r.evidence(); !strings.Contains(e, "Action: feature finish") || !strings.Contains(e, "The list could not be read the last time") {
		t.Fatal(e)
	}
	b.listErr = nil
	r.poll()
	if e := r.evidence(); strings.Contains(e, "could not be read") {
		t.Fatal(e)
	}
}

func TestTheLeadActionSelectsTheProjectAndClosesTheView(t *testing.T) {
	b := oneCardBackend(t)
	b.listing.Pending[0].Project = "Harbor (fixture)"
	c := b.cards["ap-card-1"]
	c.Title.Project = "Harbor (fixture)"
	b.cards["ap-card-1"] = c
	r := newApprRig(t, 140, 45, b, Options{})
	r.openFirstCard()
	r.press(ch('l'))
	if r.m.apprOpen() || r.m.projects[r.m.project].Name != "Harbor (fixture)" {
		t.Fatalf("view open %v, project %q", r.m.apprOpen(), r.m.projects[r.m.project].Name)
	}
}

func TestEveryKeyAndPointerActionFeedsPresence(t *testing.T) {
	r := newApprRig(t, 140, 45, oneCardBackend(t), Options{})
	if _, ok := r.m.ap.tracker.LastActivity(); ok {
		t.Fatal("activity before any action")
	}
	r.press(ch('j'))
	first, _ := r.m.ap.tracker.LastActivity()
	r.clk.Advance(time.Minute)
	r.send(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	second, _ := r.m.ap.tracker.LastActivity()
	if !second.After(first) {
		t.Fatal("a mouse action was not activity")
	}
	// A message that is not an action of the owner is not activity.
	r.clk.Advance(time.Minute)
	r.send(tea.FocusMsg{})
	r.send(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
	if third, _ := r.m.ap.tracker.LastActivity(); !third.Equal(second) {
		t.Fatal("a terminal's own message was taken as activity")
	}
}

func TestHostileTextFromTheListingNeverReachesTheTerminalRaw(t *testing.T) {
	b := newFakeApprovals(t)
	hostile := "\x1b]0;title\a\x1b[31m\u202e\u200b\u009b"
	c := b.add("ap-hostile", "project"+hostile, 5*time.Minute)
	b.listing.Pending[0].Project = "project" + hostile
	b.listing.Pending[0].Verb = "feature" + hostile
	b.listing.Pending[0].Held = true
	b.listing.Recent = []card.Item{{ID: "ap-r", Project: "p" + hostile, Verb: "v" + hostile, Feature: "name-1",
		Asked: apprT0.Add(-time.Hour), State: record.StateDenied, Line: "line" + hostile}}
	_ = c
	r := newApprRig(t, 140, 45, b, Options{})
	r.poll()
	r.press(ch('j')) // an arrival: the strip shows, with the held line
	for _, view := range []string{r.m.View().Content} {
		for _, bad := range []string{"\x1b]", "\a", "\u202e", "\u200b", "\u009b", "\x1b[31m\u202e"} {
			if strings.Contains(view, bad) {
				t.Fatalf("the screen holds %q", bad)
			}
		}
	}
	r.focusPane(paneTimeline)
	r.seen("decided: denied")
}
