package terminalconsole

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	pairTestCode   = "SYNTH-TEST-CODE-0123"
	pairTestOrigin = "https://app.example"
)

// pairFake is a PairingBackend for tests.
type pairFake struct {
	mu       sync.Mutex
	calls    []string
	replaces []bool
	expires  time.Time
	state    PairState
}

func (f *pairFake) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *pairFake) count(s string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == s {
			n++
		}
	}
	return n
}

func (f *pairFake) PairStart(_ context.Context, replace bool) (PairOffer, error) {
	f.log("start")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaces = append(f.replaces, replace)
	return PairOffer{Code: pairTestCode, ExpiresAt: f.expires}, nil
}

func (f *pairFake) PairStatus(context.Context) (PairState, error) {
	f.log("status")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *pairFake) PairConfirm(context.Context, string) (PairPaired, error) {
	f.log("confirm")
	return PairPaired{ID: "cred-1", Kind: "unverified"}, errors.New("no")
}

func (f *pairFake) PairReject(context.Context, string) error { f.log("reject"); return nil }

// pairRig drives a console with a pairing backend, a clock of its own and
// timers it fires by hand.
type pairRig struct {
	t      *testing.T
	m      *Model
	b      *pairFake
	now    time.Time
	timers []func(time.Time) tea.Msg
	quit   bool
}

func newPairRig(t *testing.T, b *pairFake, origin string, w, h int) *pairRig {
	t.Helper()
	r := &pairRig{t: t, b: b, now: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	o := Options{Theme: "dark", Now: func() time.Time { return r.now }}
	if b != nil {
		b.expires = r.now.Add(10 * time.Minute)
		o = o.WithPairing(b, origin)
	}
	m := NewWithOptions(Fixture{}, o)
	t.Cleanup(m.Close)
	// Only the pair view's poll, every two seconds, is held for the test;
	// the other timers of the console are never fired.
	m.after = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg {
			if d == 2*time.Second {
				r.timers = append(r.timers, fn)
			}
			return nil
		}
	}
	r.m = m
	r.send(tea.WindowSizeMsg{Width: w, Height: h})
	return r
}

func (r *pairRig) send(msg tea.Msg) {
	r.t.Helper()
	_, cmd := r.m.Update(msg)
	r.pump(cmd)
}

func (r *pairRig) pump(cmd tea.Cmd) {
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

func (r *pairRig) press(s string) {
	r.t.Helper()
	switch s {
	case "esc":
		r.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	case "enter":
		r.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "ctrl+c":
		r.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	default:
		for _, c := range s {
			r.send(tea.KeyPressMsg{Code: c, Text: string(c)})
		}
	}
}

func (r *pairRig) fire() {
	r.t.Helper()
	if len(r.timers) == 0 {
		r.t.Fatal("no timer is waiting")
	}
	fn := r.timers[0]
	r.timers = r.timers[1:]
	r.now = r.now.Add(2 * time.Second)
	r.send(fn(r.now))
}

func (r *pairRig) see(texts ...string) {
	r.t.Helper()
	c := content(r.m)
	for _, x := range texts {
		if !strings.Contains(c, x) {
			r.t.Fatalf("%q is not on the screen:\n%s", x, c)
		}
	}
}

func TestPOpensThePairView(t *testing.T) {
	for _, sz := range [][2]int{{140, 45}, {80, 24}, {60, 20}} {
		b := &pairFake{}
		r := newPairRig(t, b, pairTestOrigin, sz[0], sz[1])
		r.press("P")
		if !r.m.pairOpen() {
			t.Fatalf("size %v: the view did not open", sz)
		}
		if b.count("status") != 1 {
			t.Fatalf("size %v: calls %v", sz, b.calls)
		}
		r.see("PAIR A PHONE", "Enter: start")
		r.press("esc")
		if r.m.pairOpen() || strings.Contains(content(r.m), "PAIR A PHONE") {
			t.Fatalf("size %v: Esc did not close", sz)
		}
	}
}

func TestPWithNoPairingSaysSoAndDoesNothing(t *testing.T) {
	for _, sz := range [][2]int{{140, 45}, {80, 24}} {
		r := newPairRig(t, nil, "", sz[0], sz[1])
		r.press("P")
		if r.m.pairOpen() {
			t.Fatal("the view opened without a backend")
		}
		r.see(NoPairing)
		// The next key clears the notice and the console works.
		r.press("g")
		if !r.m.apprOpen() {
			t.Fatal("the console is stuck")
		}
	}
}

func TestAPairViewWithAnInvalidOriginSaysSoAndStartsNothing(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, "http://app.example", 140, 45)
	r.press("P")
	r.see("pairing origin not configured")
	r.press("enter")
	if len(b.calls) != 0 {
		t.Fatalf("calls %v", b.calls)
	}
}

func TestThePairViewDrawsTheQRPollsAndConfirmsThroughTheConsole(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, pairTestOrigin, 80, 24)
	r.press("P")
	r.press("enter")
	if b.count("start") != 1 || len(b.replaces) != 1 || b.replaces[0] {
		t.Fatalf("calls %v replaces %v", b.calls, b.replaces)
	}
	// The QR is on the screen as black on white blocks and no text of the code.
	v := r.m.View()
	if !strings.Contains(ansi.Strip(v.Content), "▀") {
		t.Fatal("no QR modules on the screen")
	}
	if !strings.Contains(v.Content, "38;2;0;0;0;48;2;255;255;255m") {
		t.Fatal("the QR is not drawn black on white")
	}
	for _, bad := range []string{pairTestCode, "SYNTH", "/pair/#"} {
		if strings.Contains(v.Content, bad) || strings.Contains(ansi.Strip(v.Content), bad) {
			t.Fatalf("%q is on the screen as text", bad)
		}
	}
	r.see("Scan with your phone's camera. Expires in 600 s.")
	// The poll goes through the console's own clock.
	b.state = PairState{Phase: PairPending, PendingID: "0123456789abcdef", Words: [6]string{"lemon", "river", "tiger", "ocean", "maple", "quartz"}, ExpiresAt: r.now.Add(5 * time.Minute)}
	r.fire()
	r.see("1 lemon", "Compare with your phone. Confirm only if they match.")
	// Confirm asks once more, and a confirm that the daemon refuses is a fixed line.
	r.press("c")
	r.see("press y to confirm, any other key cancels")
	r.now = r.now.Add(time.Second)
	r.press("y")
	if b.count("confirm") != 1 {
		t.Fatalf("calls %v", b.calls)
	}
	r.see("the confirmation did not complete")
	if strings.Contains(content(r.m), "no\n") {
		t.Fatal("the backend's error text is drawn")
	}
}

func TestTheQRIsBlackOnWhiteInTheLightThemeToo(t *testing.T) {
	b := &pairFake{}
	m := NewWithOptions(Fixture{}, Options{Theme: "light"}.WithPairing(b, pairTestOrigin))
	t.Cleanup(m.Close)
	m.after = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	r := &pairRig{t: t, m: m, b: b, now: time.Now()}
	b.expires = r.now.Add(time.Minute)
	r.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	r.press("P")
	r.press("enter")
	if !strings.Contains(m.View().Content, "38;2;0;0;0;48;2;255;255;255m") {
		t.Fatal("the QR is not drawn black on white")
	}
}

func TestThePairViewHasEveryKeyBeforeTheCockpitDoes(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, pairTestOrigin, 140, 45)
	r.press("P")
	pane, overlay := r.m.pane, r.m.overlay
	for _, k := range []string{"g", "a", "?", "tab", "q", "L", "r", "P"} {
		r.press(k)
	}
	if r.quit || r.m.apprOpen() || r.m.leadsOpen() || r.m.pane != pane || r.m.overlay != overlay || !r.m.pairOpen() {
		t.Fatal("a key reached the cockpit")
	}
	// A paste is dropped.
	r.send(tea.PasteMsg{Content: "hello"})
	if !r.m.pairOpen() {
		t.Fatal("a paste closed the view")
	}
	r.press("esc")
	r.press("g")
	if !r.m.apprOpen() {
		t.Fatal("g did not open approvals after the view closed")
	}
	// ctrl+c quits from the view.
	r2 := newPairRig(t, b, pairTestOrigin, 140, 45)
	r2.press("P")
	r2.press("ctrl+c")
	if !r2.quit {
		t.Fatal("ctrl+c did not quit")
	}
}

func TestPIsAKeyOfConsoleFocusOnly(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, pairTestOrigin, 140, 45)
	r.press("?")
	r.press("P")
	if r.m.pairOpen() {
		t.Fatal("P opened over an overlay")
	}
	r.press("esc")
	r.m.zoom = true
	r.press("P")
	if r.m.pairOpen() {
		t.Fatal("P opened in zoom")
	}
	r.m.zoom = false
	r.m.focus = sessionFocus
	r.press("P")
	if r.m.pairOpen() || len(b.calls) != 0 {
		t.Fatal("P opened while the session had the focus")
	}
	r.m.focus = consoleFocus
	r.press("g")
	r.press("P")
	if r.m.pairOpen() {
		t.Fatal("P opened over the approvals screen")
	}
	r.press("esc")
	for _, k := range []tea.KeyPressMsg{
		{Code: 'p', Text: "p"}, {Code: 'P', Mod: tea.ModCtrl}, {Code: 'P', Mod: tea.ModAlt, Text: "P"},
	} {
		r.send(k)
		if r.m.pairOpen() {
			t.Fatalf("%+v opened the view", k)
		}
	}
	r.send(tea.KeyPressMsg{Code: 'p', ShiftedCode: 'P', Mod: tea.ModShift | tea.ModCapsLock})
	if !r.m.pairOpen() {
		t.Fatal("shift+p did not open the view")
	}
	// The leads key does not open the leads view over it.
	r.press("L")
	if r.m.leadsOpen() {
		t.Fatal("L opened the leads view over the pair view")
	}
}

func TestPWorksInTheSingleColumnPages(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, pairTestOrigin, 60, 20)
	if r.m.layout().cockpit {
		t.Fatal("60x20 is a cockpit")
	}
	r.press("P")
	if !r.m.pairOpen() {
		t.Fatal("P did not open the view below the cockpit's size")
	}
	r.press("esc")
	// With no backend the single-column page shows the notice.
	r = newPairRig(t, nil, "", 60, 20)
	r.press("P")
	r.see(NoPairing)
}

func TestThePairViewResizesWithTheWindowAndDrawsInsideIt(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, pairTestOrigin, 140, 45)
	r.press("P")
	r.press("enter")
	for _, sz := range [][2]int{{1, 1}, {30, 8}, {41, 23}, {80, 24}, {500, 300}, {140, 45}} {
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

func TestTheAnswerToACommandOfAClosedPairViewIsLeftAlone(t *testing.T) {
	b := &pairFake{}
	r := newPairRig(t, b, pairTestOrigin, 140, 45)
	r.press("P")
	_, cmd := r.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no command")
	}
	r.press("esc")
	r.pump(cmd)
	if r.m.pairOpen() || r.quit || len(r.timers) != 0 {
		t.Fatal("a late answer opened the view, ended the console or started a poll")
	}
	// A new view does not take the old one's answer either.
	r.press("P")
	r.pump(cmd)
	if strings.Contains(ansi.Strip(r.m.View().Content), "▀") {
		t.Fatal("a QR from the closed view's start")
	}
	if !r.m.pairOpen() {
		t.Fatal("the view closed")
	}
}

func TestTheBackendIsCalledOnlyFromCommandsOfThePairView(t *testing.T) {
	b := &pairFake{}
	m := NewWithOptions(Fixture{}, Options{Theme: "dark"}.WithPairing(b, pairTestOrigin))
	t.Cleanup(m.Close)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	for _, k := range "cxyc" {
		m.Update(tea.KeyPressMsg{Code: k, Text: string(k)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.View()
	if len(b.calls) != 0 {
		t.Fatalf("the backend was called outside a command: %v", b.calls)
	}
}

func TestHelpMentionsPOnlyWithPairing(t *testing.T) {
	with := NewWithOptions(Fixture{}, Options{Theme: "dark"}.WithPairing(&pairFake{}, pairTestOrigin))
	without := NewWithOptions(Fixture{}, Options{Theme: "dark"})
	t.Cleanup(with.Close)
	t.Cleanup(without.Close)
	with.page, without.page = "help", "help"
	if !strings.Contains(strings.Join(with.lines(), "\n"), "P: pair a phone") {
		t.Fatal("the help does not list P with pairing")
	}
	if strings.Contains(strings.Join(without.lines(), "\n"), "P: pair") {
		t.Fatal("the help lists P without pairing")
	}
}
