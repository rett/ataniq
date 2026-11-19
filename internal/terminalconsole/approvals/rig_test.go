package approvals

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

var t0 = time.Date(2026, 11, 18, 14, 2, 0, 0, time.UTC)

// fakeClock is the clock a test injects.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeBackend is a Backend for tests. It fails the test when a method is
// called anywhere but inside a command that the rig runs.
type fakeBackend struct {
	t     *testing.T
	mu    sync.Mutex
	inCmd bool

	listing  card.Listing
	listErr  error
	cards    map[string]card.Card
	openErr  error
	pages    func(id string, kind card.PageKind, n int) (card.Page, error)
	decide   func(d card.Decision) (card.Outcome, error)
	deciderO bool
	deciderW string

	decisions []card.Decision
	opens     []string
	looked    []time.Time
	pageCalls []int
}

func newBackend(t *testing.T) *fakeBackend {
	return &fakeBackend{t: t, cards: map[string]card.Card{}, deciderO: true}
}

func (f *fakeBackend) guard(what string) {
	if !f.inCmd {
		f.t.Errorf("%s was called outside a command", what)
	}
}

func (f *fakeBackend) List() (card.Listing, error) {
	f.guard("List")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listing, f.listErr
}

func (f *fakeBackend) Open(id string) (card.Card, error) {
	f.guard("Open")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, id)
	if f.openErr != nil {
		return card.Card{}, f.openErr
	}
	c, ok := f.cards[id]
	if !ok {
		return card.Card{}, &record.Error{Code: record.CodeRecordInvalid}
	}
	return c, nil
}

func (f *fakeBackend) Page(id string, kind card.PageKind, n int) (card.Page, error) {
	f.guard("Page")
	f.mu.Lock()
	f.pageCalls = append(f.pageCalls, n)
	pf := f.pages
	f.mu.Unlock()
	if pf == nil {
		return card.Page{}, errors.New("no pages")
	}
	return pf(id, kind, n)
}

func (f *fakeBackend) Decide(d card.Decision) (card.Outcome, error) {
	f.guard("Decide")
	f.mu.Lock()
	f.decisions = append(f.decisions, d)
	df := f.decide
	f.mu.Unlock()
	if df != nil {
		return df(d)
	}
	return card.Outcome{State: record.StateApplied, Line: "applied: develop moved"}, nil
}

func (f *fakeBackend) Decider() (bool, string) {
	f.guard("Decider")
	return f.deciderO, f.deciderW
}

func (f *fakeBackend) Limit(string) (limits.View, error) {
	f.guard("Limit")
	return limits.View{}, nil
}

func (f *fakeBackend) SetLimit(string, limits.Change) (limits.View, error) {
	f.guard("SetLimit")
	return limits.View{}, nil
}

func (f *fakeBackend) Activity(time.Time) { f.guard("Activity") }

func (f *fakeBackend) Looked(at time.Time) {
	f.guard("Looked")
	f.mu.Lock()
	f.looked = append(f.looked, at)
	f.mu.Unlock()
}

// rig drives a Model: it runs the commands the model returns, synchronously,
// and feeds their answers back.
type rig struct {
	t    *testing.T
	m    *Model
	b    *fakeBackend
	clk  *fakeClock
	w, h int
	// last is the Action of the last message fed.
	last Action
	// actions collects every Action that was not None.
	actions []Action
	// code is the confirm code the injected reader makes next.
	codes []string
}

// seqReader gives the bytes of the codes that a test wants, in order: each
// code of four characters needs four bytes, the index of each character in
// the alphabet; the reader pads each code to 16 bytes as the model reads.
type seqReader struct {
	codes []string
	pos   int
}

func (r *seqReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.codes) {
		return 0, errors.New("no more codes")
	}
	code := r.codes[r.pos]
	r.pos++
	for i := range p {
		p[i] = 0xff // refused by the model, which keeps only bytes below 234
	}
	for i := 0; i < len(code) && i < len(p); i++ {
		p[i] = byte(strings.IndexByte(codeAlphabet, code[i]))
	}
	return len(p), nil
}

func newRig(t *testing.T, b *fakeBackend, w, h int, codes ...string) *rig {
	clk := &fakeClock{t: t0}
	if len(codes) == 0 {
		codes = []string{"7Q4K", "ACDE", "FGHJ", "KLMN"}
	}
	m := New(Options{
		Backend: b,
		Now:     clk.Now,
		Random:  &seqReader{codes: codes},
		After: func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return fn(clk.Now()) }
		},
	})
	r := &rig{t: t, m: m, b: b, clk: clk, w: w, h: h}
	r.run(m.SetSize(w, h))
	r.run(m.Init())
	return r
}

// run executes cmd and every command that follows, feeding each answer to
// the model.
func (r *rig) run(cmd tea.Cmd) {
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
			r.run(c)
		}
	default:
		r.feed(msg)
	}
}

// feed hands msg to the model and runs what it returns.
func (r *rig) feed(msg tea.Msg) Action {
	r.t.Helper()
	cmd, act := r.m.Update(msg)
	r.last = act
	if act.Kind != None {
		r.actions = append(r.actions, act)
	}
	r.run(cmd)
	return act
}

// key presses a key by name.
func (r *rig) key(name string) Action {
	r.t.Helper()
	return r.feed(keyMsg(name))
}

// typ presses each character of s, 100 ms apart.
func (r *rig) typ(s string) {
	r.t.Helper()
	for _, c := range s {
		r.clk.Advance(100 * time.Millisecond)
		r.key(string(c))
	}
}

func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	rs := []rune(name)
	if len(rs) != 1 {
		panic("keyMsg: " + name)
	}
	return tea.KeyPressMsg{Code: rs[0], Text: name}
}

// draw draws the view into a buffer of the size of the rig.
func (r *rig) draw() *uv.Buffer {
	buf := uv.NewBuffer(r.w, r.h)
	r.m.Draw(buf, buf.Bounds())
	return buf
}

// rowText returns the text of row y, without the blanks at its end.
func rowText(buf *uv.Buffer, y int) string {
	var b strings.Builder
	for x := 0; x < buf.Bounds().Dx(); x++ {
		if c := buf.CellAt(x, y); c != nil {
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// rows returns every row of the drawing.
func (r *rig) rows() []string {
	buf := r.draw()
	out := make([]string, buf.Bounds().Dy())
	for y := range out {
		out[y] = rowText(buf, y)
	}
	return out
}

// screen returns the drawing as one text.
func (r *rig) screen() string { return strings.Join(r.rows(), "\n") }

// seen fails the test unless the screen holds every text.
func (r *rig) seen(texts ...string) {
	r.t.Helper()
	s := r.screen()
	flat := strings.Join(strings.Fields(strings.ReplaceAll(s, marginMark, " ")), " ")
	for _, want := range texts {
		// A line that wrapped onto the next row is found whole.
		if !strings.Contains(s, want) && !strings.Contains(flat, strings.Join(strings.Fields(want), " ")) {
			r.t.Fatalf("the screen does not hold %q:\n%s", want, s)
		}
	}
}

// notSeen fails the test when the screen holds any text.
func (r *rig) notSeen(texts ...string) {
	r.t.Helper()
	s := r.screen()
	for _, bad := range texts {
		if strings.Contains(s, bad) {
			r.t.Fatalf("the screen holds %q:\n%s", bad, s)
		}
	}
}

// code reads the confirm code from the title row, or "".
func (r *rig) code() string {
	title := r.rows()[0]
	rest, ok := strings.CutPrefix(strings.TrimSpace(title), "APPROVAL ")
	if !ok {
		return ""
	}
	f := strings.Fields(rest)
	if len(f) > 0 && len(f[0]) == codeLen && strings.Trim(f[0], codeAlphabet) == "" {
		return f[0]
	}
	return ""
}

// scrollAll moves the cursor down through the whole card, a row at a time.
func (r *rig) scrollAll() {
	for i := 0; i < len(r.m.cs.body)+5; i++ {
		r.key("down")
	}
}

func hex(n int) string { return fmt.Sprintf("%040x", n) }

func lead(ss ...string) []card.LeadText {
	out := make([]card.LeadText, len(ss))
	for i, s := range ss {
		out[i] = card.NewLeadText(s)
	}
	return out
}

// testCard returns a card of a feature finish with two flagged paths and
// empty lists.
func testCard(id string) card.Card {
	return card.Card{
		Title: card.Title{
			ID: id, Project: "project-a",
			Subject: card.Subject{Kind: "seat", Role: "lead-a", DisplayID: "lead-1", Runtime: "runtime-a"},
			Asked:   t0, Expires: t0.Add(30 * time.Minute),
		},
		Verb: card.VerbLine{Name: "feature finish", Feature: "feature-x", Target: "develop", Class: "ref-move"},
		Plan: card.Plan{Token: "h-3fa9c2e1", Lines: lead(
			"merge feature/feature-x (F "+hex(3)[:7]+") into develop (D "+hex(1)[:7]+") --no-ff",
			"result M "+hex(2)[:7]+" built by the host")},
		Refs:  []card.RefLine{{Kind: "ref", Ref: card.NewLeadText("refs/heads/develop"), Old: hex(1), New: hex(2)}},
		Merge: &card.Merge{Commit: hex(2), Parents: []string{hex(1), hex(3)}, BuiltBy: "the host"},
		Diff: &card.Diff{
			ReviewDigest: "review-a", Base: hex(1), Tip: hex(2), Files: 3, Added: 612, Removed: 88, NewBytes: 48 << 10,
			Rows: []card.Row{
				card.RowOf(record.PathChange{Path: "lib/seat_read.py", Status: "A", OldMode: "000000", NewMode: "100644", Added: 301}),
				card.RowOf(record.PathChange{Path: "Makefile", Status: "M", OldMode: "100644", NewMode: "100644", Added: 3, Removed: 1}),
				card.RowOf(record.PathChange{Path: "scripts/go-tool.sh", Status: "M", OldMode: "100644", NewMode: "100755", Added: 1}),
			},
			ExecConfig: card.ExecConfig{Matcher: "matcher-1", SHA256: hex(9), Additions: 0},
		},
		Lists: []card.List{
			{Kind: card.ListBinary}, {Kind: card.ListSymlinks}, {Kind: card.ListModeChanges}, {Kind: card.ListSubmodules},
		},
		Flagged: []card.FlaggedPath{
			{Kind: record.FlagExecConfig, Path: card.NewLeadText("Makefile"), Reason: "matches an execution-config rule", Pattern: card.NewLeadText("Makefile")},
			{Kind: record.FlagExecConfig, Path: card.NewLeadText("scripts/go-tool.sh"), Reason: "matches an execution-config rule"},
		},
		Evidence:      []card.Evidence{{Kind: "suite", RecordID: "v-0412", RecordSHA256: hex(7)}},
		BindingSHA256: "binding-" + id,
		Note:          card.NewLeadText("Wave 1 task SR merged, review r-91c2."),
	}
}

// listWith returns a backend with the cards pending and the recent ones.
func backendWith(t *testing.T, pending []card.Card, recent []card.Item) *fakeBackend {
	b := newBackend(t)
	for _, c := range pending {
		b.cards[c.Title.ID] = c
		b.listing.Pending = append(b.listing.Pending, c.Item())
	}
	b.listing.Recent = recent
	b.listing.Count = card.Count{Requests: 7, Met: 2}
	return b
}

// openFirst opens the first pending card and lets the 400 ms pass.
func (r *rig) openFirst() {
	r.t.Helper()
	r.key("enter")
	if r.m.cs == nil {
		r.t.Fatalf("no card opened:\n%s", r.screen())
	}
	r.clk.Advance(time.Second)
}

// bodyHas fails the test unless a row of the open card's body, scrolled off
// the screen or not, holds text.
func (r *rig) bodyHas(text string) {
	r.t.Helper()
	for _, row := range r.m.cs.body {
		var b strings.Builder
		for _, s := range row.segs {
			b.WriteString(s.s)
		}
		if strings.Contains(b.String(), text) {
			return
		}
	}
	r.t.Fatalf("no row of the card holds %q", text)
}

// tea2 is a key press of a character that has no name, with its text.
func tea2(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyExtended, Text: string(c)} }
