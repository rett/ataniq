package terminalconsole

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/leadctl"
)

type sample struct{ p Projection }

func (s sample) Snapshot() Projection { return s.p }

func TestSelectionAndNoticeBoundaries(t *testing.T) {
	p := Projection{ControlUnavailable: "fixture controls disabled"}
	for i := 0; i < 8; i++ {
		var leads []Lead
		for j := 0; j < 3; j++ {
			leads = append(leads, Lead{Name: fmt.Sprintf("lead-%d", j), Subagents: []string{"one", "two"}, Conversation: []string{"hello"}})
		}
		p.Projects = append(p.Projects, Project{Name: fmt.Sprintf("project-%d", i), Leads: leads})
	}
	for _, h := range []int{2, 3, 8} {
		m := New(sample{p}, "dark")
		size(m, 80, h)
		for i := 0; i < 8; i++ {
			if !strings.Contains(content(m), fmt.Sprintf("> project-%d", i)) {
				t.Fatalf("height %d: %s", h, content(m))
			}
			key(m, 'j')
		}
		enter(m)
		for i := 0; i < 3; i++ {
			if !strings.Contains(content(m), fmt.Sprintf("> lead-%d", i)) {
				t.Fatalf("height %d: %s", h, content(m))
			}
			key(m, 'j')
		}
		if cmd := key(m, 'a'); cmd != nil {
			t.Fatal("control returned command")
		}
		if h >= 3 && !strings.Contains(content(m), "fixture controls disabled") {
			t.Fatal(content(m))
		}
		if !strings.Contains(content(m), "> lead-2") {
			t.Fatal(content(m))
		}
		size(m, 80, 1)
		if !strings.Contains(content(m), "DEMO") {
			t.Fatal(content(m))
		}
		size(m, 80, 3)
		if !strings.Contains(content(m), "fixture controls disabled") {
			t.Fatal(content(m))
		}
		enter(m)
		key(m, 'j')
		if !strings.Contains(content(m), "fixture controls disabled") {
			t.Fatal(content(m))
		}
	}
}

func TestCleanBoundedUTF8(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 511) + "😀", strings.Repeat("\x1b", 5000) + "visible", "a\xff\x1b]0;x\a\u009b\u202e\u2028z"} {
		got := clean(input)
		if !utf8.ValidString(got) || len(got) > 512 || strings.ContainsAny(got, "\x1b\a\u009b\u202e\u2028") {
			t.Fatalf("unsafe: %q", got)
		}
	}
	if got := clean(strings.Repeat("\x1b", 5000) + "visible"); got != "" {
		t.Fatal(got)
	}
	if got := clean(strings.Repeat("x", 511) + "😀"); len(got) != 511 {
		t.Fatal(len(got))
	}
}

func TestExtremeResizeAfterScroll(t *testing.T) {
	m := New(Fixture{}, "dark")
	t.Cleanup(m.Close)
	for _, page := range []string{"projects", "leads", "tasks", "conversation", "help", "actions"} {
		// The pages show below the cockpit's size.
		m.page, m.scroll = page, 3
		size(m, 99, math.MaxInt)
		if len(m.View().Content) > 100000 {
			t.Fatal("unbounded view")
		}
		size(m, 99, 99)
		size(m, math.MaxInt, math.MaxInt)
		size(m, 99, math.MaxInt)
		if len(m.View().Content) > 100000 {
			t.Fatal("unbounded view")
		}
	}
	// The cockpit draws at most its canvas, whatever the window.
	size(m, math.MaxInt, math.MaxInt)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) > maxCanvasRows {
		t.Fatalf("%d rows", len(lines))
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > maxCanvasCols {
			t.Fatalf("%d columns", ansi.StringWidth(l))
		}
	}
}

// leadsFake is a leadctl.Backend for the console's tests of the L key.
type leadsFake struct {
	mu      sync.Mutex
	lists   int
	starts  []string
	tickets []string
	sends   []string
	ticket  leadctl.Receipt
}

func (f *leadsFake) List() (leadctl.Listing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return leadctl.Listing{MaxRunning: 2, Running: 1, Leads: []leadctl.Lead{
		{Key: "alpha", Role: "scratch-alpha/lead", State: "ready", Epoch: 3, Unit: "ataniq-lead-scratch-alpha.service", Inject: true, Actions: leadctl.OfferAll()},
	}}, nil
}

func (f *leadsFake) Define(string) (leadctl.Receipt, error) { return leadctl.Receipt{}, nil }

func (f *leadsFake) Start(role string) (leadctl.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, role)
	return leadctl.Receipt{Status: leadctl.Done}, nil
}

func (f *leadsFake) Stop(string, uint64) (leadctl.Receipt, error) { return leadctl.Receipt{}, nil }

func (f *leadsFake) Ticket(role string, epoch uint64, writer bool) (leadctl.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tickets = append(f.tickets, fmt.Sprintf("%s %d %v", role, epoch, writer))
	return f.ticket, nil
}

func (f *leadsFake) Send(role string, epoch uint64, id, body string) (leadctl.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, fmt.Sprintf("%s %d %s", role, epoch, body))
	return leadctl.Receipt{Status: leadctl.Done, Outcome: leadctl.OutcomeSubmitted}, nil
}

// leadsRig drives a console with a leads backend: it runs the commands the
// model returns and hands each answer back, and notes whether one was a quit.
type leadsRig struct {
	t    *testing.T
	m    *Model
	quit bool
}

func newLeadsRig(t *testing.T, b leadctl.Backend, w, h int) *leadsRig {
	t.Helper()
	m := NewWithOptions(Fixture{}, Options{Theme: "dark", Leads: b})
	t.Cleanup(m.Close)
	m.after = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	r := &leadsRig{t: t, m: m}
	r.send(tea.WindowSizeMsg{Width: w, Height: h})
	return r
}

func (r *leadsRig) send(msg tea.Msg) {
	r.t.Helper()
	_, cmd := r.m.Update(msg)
	r.pump(cmd)
}

func (r *leadsRig) pump(cmd tea.Cmd) {
	r.t.Helper()
	if cmd == nil {
		return
	}
	switch v := cmd().(type) {
	case nil, tea.RawMsg:
	case tea.QuitMsg:
		r.quit = true
	case tea.BatchMsg:
		for _, c := range v {
			r.pump(c)
		}
	default:
		r.send(v)
	}
}

func (r *leadsRig) press(s string) {
	r.t.Helper()
	switch s {
	case "esc":
		r.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	case "enter":
		r.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "ctrl+c":
		r.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	case "tab":
		r.send(tea.KeyPressMsg{Code: tea.KeyTab})
	default:
		for _, c := range s {
			r.send(tea.KeyPressMsg{Code: c, Text: string(c)})
		}
	}
}

func (r *leadsRig) see(texts ...string) {
	r.t.Helper()
	c := content(r.m)
	for _, x := range texts {
		if !strings.Contains(c, x) {
			r.t.Fatalf("%q is not on the screen:\n%s", x, c)
		}
	}
}

func TestLOpensTheLeadsViewAndReadsTheList(t *testing.T) {
	for _, sz := range [][2]int{{140, 45}, {60, 20}} {
		b := &leadsFake{}
		r := newLeadsRig(t, b, sz[0], sz[1])
		r.press("L")
		if r.m.lc.view == nil || !r.m.leadsOpen() {
			t.Fatalf("size %v: the view did not open", sz)
		}
		if b.lists != 1 {
			t.Fatalf("size %v: lists %d", sz, b.lists)
		}
		r.see("LEAD CONTROL", "1 running of 2", "alpha")
	}
}

func TestLWithNoBackendSaysThereIsNoLeadControl(t *testing.T) {
	r := newLeadsRig(t, nil, 140, 45)
	r.press("L")
	r.see("LEAD CONTROL", NoLeadControl)
	// The screen has every key: q does not quit, and Esc closes it.
	r.press("q")
	if r.quit || !r.m.leadsOpen() {
		t.Fatal("q left the screen")
	}
	r.press("esc")
	if r.m.leadsOpen() || strings.Contains(content(r.m), NoLeadControl) {
		t.Fatal("Esc did not close the screen")
	}
	r.press("L")
	r.press("ctrl+c")
	if !r.quit {
		t.Fatal("ctrl+c did not quit from the screen")
	}
}

func TestTheLeadsViewHasEveryKeyBeforeTheCockpitDoes(t *testing.T) {
	b := &leadsFake{}
	r := newLeadsRig(t, b, 140, 45)
	r.press("L")
	// q, g, a, r, tab and ? are the cockpit's in the cockpit, and not here.
	pane, overlay := r.m.pane, r.m.overlay
	for _, k := range []string{"g", "a", "?", "tab", "q"} {
		r.press(k)
	}
	if r.quit || r.m.apprOpen() || r.m.pane != pane || r.m.overlay != overlay || !r.m.leadsOpen() {
		t.Fatal("a key reached the cockpit")
	}
	// s is the view's: it starts the lead under the cursor, once.
	r.press("s")
	if len(b.starts) != 1 || b.starts[0] != "scratch-alpha/lead" {
		t.Fatalf("starts %v", b.starts)
	}
	r.see("start: accepted, done")
	// Esc closes the view and the cockpit has its keys again.
	r.press("esc")
	if r.m.leadsOpen() {
		t.Fatal("Esc did not close")
	}
	r.press("g")
	if !r.m.apprOpen() {
		t.Fatal("g did not open approvals after the view closed")
	}
}

func TestLIsAKeyOfConsoleFocusOnly(t *testing.T) {
	b := &leadsFake{}
	r := newLeadsRig(t, b, 140, 45)
	// Zoom and the overlays take it as nothing.
	r.press("?")
	r.press("L")
	if r.m.leadsOpen() {
		t.Fatal("L opened over an overlay")
	}
	r.press("esc")
	r.m.zoom = true
	r.press("L")
	if r.m.leadsOpen() {
		t.Fatal("L opened in zoom")
	}
	r.m.zoom = false
	// While the session has the focus the key is the session's.
	r.m.focus = sessionFocus
	r.press("L")
	if r.m.leadsOpen() || b.lists != 0 {
		t.Fatal("L opened while the session had the focus")
	}
	r.m.focus = consoleFocus
	// An approvals screen has its own keys, L among them.
	r.press("g")
	r.press("L")
	if r.m.leadsOpen() {
		t.Fatal("L opened over the approvals screen")
	}
	// Other forms of the key are not the key.
	r.press("esc")
	for _, k := range []tea.KeyPressMsg{
		{Code: 'l', Text: "l"}, {Code: 'L', Mod: tea.ModCtrl}, {Code: 'L', Mod: tea.ModAlt, Text: "L"},
	} {
		r.send(k)
		if r.m.leadsOpen() {
			t.Fatalf("%+v opened the view", k)
		}
	}
	// The kitty form, shift and l with no text, is the key.
	r.send(tea.KeyPressMsg{Code: 'l', ShiftedCode: 'L', Mod: tea.ModShift | tea.ModCapsLock})
	if !r.m.leadsOpen() {
		t.Fatal("shift+l did not open the view")
	}
}

func TestTheLeadsViewResizesWithTheWindowAndDrawsInsideIt(t *testing.T) {
	r := newLeadsRig(t, &leadsFake{}, 140, 45)
	r.press("L")
	for _, sz := range [][2]int{{1, 1}, {30, 8}, {80, 24}, {500, 200}, {140, 45}} {
		r.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		lines := strings.Split(ansi.Strip(r.m.View().Content), "\n")
		if len(lines) > min(sz[1], maxCanvasRows) {
			t.Fatalf("size %v: %d rows", sz, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > min(sz[0], maxCanvasCols) {
				t.Fatalf("size %v: %d columns", sz, ansi.StringWidth(l))
			}
		}
	}
}

func TestADoneTicketEndsTheConsoleWithAnAttachRequest(t *testing.T) {
	b := &leadsFake{ticket: leadctl.Receipt{Op: leadctl.OpTicket, Status: leadctl.Done, Folder: "/srv/lead/alpha", TicketFile: "/srv/lead/alpha/ticket"}}
	r := newLeadsRig(t, b, 140, 45)
	if _, ok := r.m.Attach(); ok {
		t.Fatal("an attach request before any ticket")
	}
	r.press("L")
	r.press("v")
	if len(b.tickets) != 1 || b.tickets[0] != "scratch-alpha/lead 3 false" {
		t.Fatalf("tickets %v", b.tickets)
	}
	if !r.quit {
		t.Fatal("the console did not end")
	}
	req, ok := r.m.Attach()
	want := AttachRequest{Role: "scratch-alpha/lead", Epoch: 3, Writer: false, Folder: "/srv/lead/alpha", TicketFile: "/srv/lead/alpha/ticket"}
	if !ok || req != want {
		t.Fatalf("attach %+v %v, want %+v", req, ok, want)
	}
}

func TestATicketThatIsRefusedDoesNotEndTheConsole(t *testing.T) {
	b := &leadsFake{ticket: leadctl.Receipt{Status: leadctl.Refused, Code: "not-running"}}
	r := newLeadsRig(t, b, 140, 45)
	r.press("L")
	r.press("w")
	if r.quit || !r.m.leadsOpen() {
		t.Fatal("the console ended on a refused ticket")
	}
	if _, ok := r.m.Attach(); ok {
		t.Fatal("an attach request from a refused ticket")
	}
	r.see("attach ticket: refused: the lead is not running")
}

func TestAMessageTypedInTheViewReachesTheBackendOnceAndAPasteIsTheEditors(t *testing.T) {
	b := &leadsFake{}
	r := newLeadsRig(t, b, 140, 45)
	r.press("L")
	r.press("m")
	r.press("hello")
	r.send(tea.PasteMsg{Content: " there"})
	r.press("enter")
	if len(b.sends) != 1 || b.sends[0] != "scratch-alpha/lead 3 hello there" {
		t.Fatalf("sends %v", b.sends)
	}
	r.see("message: accepted. the message was submitted")
}

func TestTheAnswerToACommandOfAClosedViewIsLeftAlone(t *testing.T) {
	b := &leadsFake{}
	r := newLeadsRig(t, b, 140, 45)
	r.press("L")
	_, cmd := r.m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	if cmd == nil {
		t.Fatal("no command")
	}
	r.press("esc")
	r.pump(cmd)
	if r.m.leadsOpen() || r.quit {
		t.Fatal("a late answer opened the view or ended the console")
	}
	// The cockpit still works.
	r.press("g")
	if !r.m.apprOpen() {
		t.Fatal("the cockpit is stuck")
	}
}

func TestTheBackendIsCalledOnlyFromCommandsOfTheConsole(t *testing.T) {
	b := &leadsFake{}
	m := NewWithOptions(Fixture{}, Options{Theme: "dark", Leads: b})
	t.Cleanup(m.Close)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	// Opening the view and pressing its keys calls nothing: the calls are in
	// the commands, which this test does not run.
	m.Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	for _, k := range "sdwvrm" {
		m.Update(tea.KeyPressMsg{Code: k, Text: string(k)})
	}
	m.View()
	if b.lists != 0 || len(b.starts) != 0 || len(b.tickets) != 0 {
		t.Fatalf("the backend was called outside a command: lists %d starts %v tickets %v", b.lists, b.starts, b.tickets)
	}
}
