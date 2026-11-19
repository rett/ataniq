package arrival

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/presence"
)

var (
	arrived = presence.Event{Arrival: true}
	typing  = presence.Event{}

	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
)

// key returns the press of a printable key.
func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// full is a summary with all four lines.
func full() Summary {
	return Summary{
		Alerts:     []string{"lead-2 stopped: the suite failed", "the journal is behind"},
		Waiting:    2,
		OldestAge:  3 * time.Minute,
		ByGrant:    3,
		ByStanding: 2,
		Held:       []string{"feature-x was merged into develop", "b", "c", "d"},
	}
}

// shown returns a strip that shows sum and was drawn once at w by h.
func shown(t *testing.T, sum Summary, w, h int) (*Strip, *uv.Buffer) {
	t.Helper()
	s := NewStrip(Colors{})
	if !s.Arrive(arrived, sum) {
		t.Fatal("Arrive did not show the strip")
	}
	return s, draw(s, w, h, true)
}

func draw(s *Strip, w, h int, focused bool) *uv.Buffer {
	buf := uv.NewBuffer(w, h)
	s.Draw(buf, buf.Bounds(), focused)
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

func TestTheLinesAndTheirOrder(t *testing.T) {
	s, buf := shown(t, full(), 100, 5)
	if s.Rows() != 5 {
		t.Fatalf("Rows() = %d, want 5", s.Rows())
	}
	want := []string{
		"while you were away · a digit opens its line · Esc closes",
		"[1] alerts 2 · lead-2 stopped: the suite failed",
		"[2] approvals waiting 2 · oldest 3 min",
		"[3] approved without you 5 · 3 by grant, 2 by standing approval",
		"[4] held notices 4 · feature-x was merged into develop",
	}
	for y, w := range want {
		if got := rowText(buf, y); got != w {
			t.Errorf("row %d = %q, want %q", y, got, w)
		}
	}
	if got := rowText(draw(s, 100, 5, false), 0); got != "while you were away" {
		t.Errorf("the title row without the focus = %q", got)
	}
}

func TestADayThatReachedTheLimitIsAnAlert(t *testing.T) {
	_, buf := shown(t, Summary{LimitReached: 1}, 100, 2)
	if got, want := rowText(buf, 1), "[1] alerts 1 · the daily card limit is reached for 1 project"; got != want {
		t.Errorf("row = %q, want %q", got, want)
	}
	_, buf = shown(t, Summary{LimitReached: 3, Alerts: []string{"lead-1 stopped"}, Waiting: 1}, 100, 3)
	if got, want := rowText(buf, 1), "[1] alerts 2 · the daily card limit is reached for 3 projects · lead-1 stopped"; got != want {
		t.Errorf("row = %q, want %q", got, want)
	}
	// The alerts stand before the cards that wait.
	if got := rowText(buf, 2); !strings.HasPrefix(got, "[2] approvals waiting 1") {
		t.Errorf("row 2 = %q", got)
	}
}

func TestAPartWithNothingToSayGivesNoLine(t *testing.T) {
	for _, tc := range []struct {
		sum  Summary
		want []string
	}{
		{Summary{Waiting: 1, OldestAge: 2 * time.Hour}, []string{"[1] approvals waiting 1 · oldest 2 h"}},
		{Summary{ByGrant: 4}, []string{"[1] approved without you 4 · 4 by grant"}},
		{Summary{ByStanding: 1, ByGrant: -5}, []string{"[1] approved without you 1 · 1 by standing approval"}},
		{Summary{Held: []string{"a notice"}, Waiting: -1}, []string{"[1] held notices 1 · a notice"}},
		{Summary{Alerts: []string{""}, Held: []string{"n"}}, []string{"[1] alerts 1 ·", "[2] held notices 1 · n"}},
		{Summary{ByGrant: int(^uint(0) >> 1), ByStanding: 1, Waiting: 1},
			[]string{"[1] approvals waiting 1 · oldest under 1 min", "[2] approved without you 9223372036854775807 · 9223372036854775807 by grant, 1 by standing approval"}},
	} {
		s, buf := shown(t, tc.sum, 120, 5)
		if s.Rows() != 1+len(tc.want) {
			t.Errorf("%+v: Rows() = %d, want %d", tc.sum, s.Rows(), 1+len(tc.want))
		}
		for i, w := range tc.want {
			if got := rowText(buf, 1+i); got != w {
				t.Errorf("%+v: row %d = %q, want %q", tc.sum, 1+i, got, w)
			}
		}
		if got := rowText(buf, 1+len(tc.want)); got != "" {
			t.Errorf("%+v: a row below the lines holds %q", tc.sum, got)
		}
	}
}

func TestAnEmptySummaryShowsNoStrip(t *testing.T) {
	for _, sum := range []Summary{{}, {Waiting: -1, ByGrant: -1, ByStanding: -1, LimitReached: -1}, {Alerts: []string{}, Held: []string{}}} {
		if !sum.Empty() {
			t.Errorf("%+v is not empty", sum)
		}
		s := NewStrip(Colors{})
		if s.Arrive(arrived, sum) || s.Visible() || s.Rows() != 0 {
			t.Errorf("%+v: a strip is shown", sum)
		}
	}
	if full().Empty() {
		t.Error("the full summary is empty")
	}
}

// TestOncePerArrival drives the strip with a tracker of package presence, as
// the cockpit does: it appears on the arrival, stays closed while he works
// on, and appears again only when he comes back from an away stretch.
func TestOncePerArrival(t *testing.T) {
	tr, err := presence.New(presence.DefaultWindow, presence.SurfaceConsole, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	s := NewStrip(Colors{})
	now := t0
	act := func() bool { return s.Arrive(tr.Activity(now), full()) }

	if !act() || !s.Visible() {
		t.Fatal("the first activity of the run showed no strip")
	}
	draw(s, 80, 5, true)
	if a, _ := s.Key(keyEsc, typing, true); a != Close || s.Visible() {
		t.Fatalf("Esc: action %v, visible %v", a, s.Visible())
	}
	for i := 0; i < 50; i++ {
		now = now.Add(5 * time.Second)
		if act() || s.Visible() {
			t.Fatalf("activity %d inside the window showed the strip again", i)
		}
		s.Refresh(full())
		if s.Visible() {
			t.Fatal("Refresh showed a closed strip")
		}
	}
	// Away for longer than the window, with the leaving reported or not.
	now = now.Add(presence.DefaultWindow + time.Minute)
	tr.Tick(now)
	if s.Visible() {
		t.Fatal("the strip is shown while he is away")
	}
	if !act() || !s.Visible() {
		t.Fatal("the arrival after an away stretch showed no strip")
	}
	// It stays while he works on, until he closes it.
	now = now.Add(time.Second)
	if act() {
		t.Fatal("Arrive returned true for an activity that is no arrival")
	}
	if !s.Visible() {
		t.Fatal("an activity that is no arrival took the strip away")
	}
	s.Dismiss()
	if s.Visible() || s.Rows() != 0 {
		t.Fatal("Dismiss left the strip")
	}
	// An arrival with nothing to say takes away a strip that was left.
	s.Arrive(arrived, full())
	if s.Arrive(arrived, Summary{}) || s.Visible() {
		t.Fatal("an arrival with an empty summary left a strip")
	}
}

func TestRefresh(t *testing.T) {
	s, _ := shown(t, full(), 100, 5)
	sum := full()
	sum.Waiting, sum.Alerts = 7, nil
	s.Refresh(sum)
	buf := draw(s, 100, 5, true)
	if got, want := rowText(buf, 1), "[1] approvals waiting 7 · oldest 3 min"; got != want {
		t.Errorf("row 1 after Refresh = %q, want %q", got, want)
	}
	if s.Rows() != 4 {
		t.Errorf("Rows() = %d, want 4", s.Rows())
	}
	s.Refresh(Summary{})
	if s.Visible() {
		t.Error("a summary that became empty left the strip")
	}
}

// TestRefreshKeepsDigitsTrueToTheLastDrawing: after Refresh a digit opens
// only the line the last drawing showed beside it.
func TestRefreshKeepsDigitsTrueToTheLastDrawing(t *testing.T) {
	// The alerts clear: every line moves up one place, and 1 would open
	// "waiting" while the screen showed "[1] alerts".
	s, _ := shown(t, full(), 100, 5)
	sum := full()
	sum.Alerts, sum.LimitReached = nil, 0
	s.Refresh(sum)
	for _, d := range "1234" {
		if a, l := s.Key(key(d), typing, true); a != None || l != NoLine {
			t.Fatalf("digit %c after Refresh without a redraw: %v %v", d, a, l)
		}
	}
	// Once drawn again, the digits name the lines on the screen.
	draw(s, 100, 5, true)
	if a, l := s.Key(key('1'), typing, true); a != Open || l != LineWaiting {
		t.Errorf("digit 1 after a redraw: %v %v, want Open waiting", a, l)
	}

	// A part at the end drops out: a line that was drawn is gone, so no
	// digit opens a line until the next drawing.
	s, _ = shown(t, full(), 100, 5)
	sum = full()
	sum.Held = nil
	s.Refresh(sum)
	for _, d := range "1234" {
		if a, l := s.Key(key(d), typing, true); a != None || l != NoLine {
			t.Fatalf("digit %c after a line went: %v %v", d, a, l)
		}
	}

	// Lines that stay where they were keep their digits.
	s, _ = shown(t, full(), 100, 5)
	sum = full()
	sum.Waiting++
	s.Refresh(sum)
	if a, l := s.Key(key('2'), typing, true); a != Open || l != LineWaiting {
		t.Errorf("digit 2 after a number changed: %v %v, want Open waiting", a, l)
	}
}

// TestTheKeyThatWokeTheConsoleIsNotTaken: whatever the key of the arrival
// is, and whether or not the strip has the focus, the strip refuses it and
// stays as it is.
func TestTheKeyThatWokeTheConsoleIsNotTaken(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{keyEsc, key('1'), key('2'), key('4'), keyEnter, key('a'), key('q')} {
		for _, focused := range []bool{false, true} {
			// The strip was left showing from an earlier arrival and was drawn.
			s, _ := shown(t, full(), 100, 5)
			s.Arrive(arrived, full())
			draw(s, 100, 5, focused)
			if a, l := s.Key(k, arrived, focused); a != None || l != NoLine {
				t.Errorf("key %q of the arrival, focused %v: %v %v", k.String(), focused, a, l)
			}
			if !s.Visible() {
				t.Errorf("key %q of the arrival closed the strip", k.String())
			}
		}
	}
}

func TestWithoutTheFocusNoKeyIsTheStrips(t *testing.T) {
	s, _ := shown(t, full(), 100, 5)
	for _, k := range allKeys() {
		if a, l := s.Key(k, typing, false); a != None || l != NoLine {
			t.Errorf("key %q without the focus: %v %v", k.String(), a, l)
		}
		if !s.Visible() {
			t.Fatalf("key %q without the focus closed the strip", k.String())
		}
	}
}

// allKeys is a broad set of key presses: every printable ASCII key, the
// named keys, and digits and Esc with modifiers.
func allKeys() []tea.KeyPressMsg {
	var ks []tea.KeyPressMsg
	for r := rune(0x20); r < 0x7f; r++ {
		ks = append(ks, key(r))
	}
	for _, c := range []rune{tea.KeyEscape, tea.KeyEnter, tea.KeyTab, tea.KeyBackspace, tea.KeyUp, tea.KeyDown,
		tea.KeyLeft, tea.KeyRight, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd, tea.KeyDelete, tea.KeyF1} {
		ks = append(ks, tea.KeyPressMsg{Code: c})
	}
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt, tea.ModShift, tea.ModCtrl | tea.ModAlt} {
		ks = append(ks, tea.KeyPressMsg{Code: '1', Mod: mod}, tea.KeyPressMsg{Code: '1', Text: "1", Mod: mod},
			tea.KeyPressMsg{Code: tea.KeyEscape, Mod: mod},
			tea.KeyPressMsg{Code: 'c', Mod: mod})
	}
	return ks
}

// TestWithTheFocusOnlyEscAndTheDigitsOfItsLines: every other key is
// refused and changes nothing.
func TestWithTheFocusOnlyEscAndTheDigitsOfItsLines(t *testing.T) {
	own := map[string]Line{"esc": NoLine, "1": LineAlerts, "2": LineWaiting, "3": LineApproved, "4": LineHeld}
	for _, k := range allKeys() {
		s, _ := shown(t, full(), 100, 5)
		a, l := s.Key(k, typing, true)
		line, mine := own[k.String()]
		if k.Mod != 0 {
			// A key with a modifier is no one's but the cockpit's, even
			// when it carries text.
			mine = false
		}
		switch {
		case !mine:
			if a != None || l != NoLine || !s.Visible() {
				t.Errorf("key %q: %v %v, visible %v; it is not the strip's", k.String(), a, l, s.Visible())
			}
		case k.String() == "esc":
			if a != Close || l != NoLine || s.Visible() {
				t.Errorf("Esc: %v %v, visible %v", a, l, s.Visible())
			}
		default:
			if a != Open || l != line || s.Visible() {
				t.Errorf("key %q: %v %v, visible %v; want Open %v and closed", k.String(), a, l, s.Visible(), line)
			}
		}
		// Once closed, no key is the strip's.
		if !s.Visible() {
			if a, l := s.Key(k, typing, true); a != None || l != NoLine {
				t.Errorf("key %q on a closed strip: %v %v", k.String(), a, l)
			}
		}
	}
}

func TestADigitOpensTheLineItStandsBeside(t *testing.T) {
	// Without alerts and held notices the digits name other lines.
	sum := Summary{Waiting: 1, ByGrant: 1}
	for d, want := range map[rune]Line{'1': LineWaiting, '2': LineApproved} {
		s, _ := shown(t, sum, 80, 3)
		if a, l := s.Key(key(d), typing, true); a != Open || l != want {
			t.Errorf("digit %c: %v %v, want Open %v", d, a, l, want)
		}
	}
	for _, d := range "03456789" {
		s, _ := shown(t, sum, 80, 3)
		if a, l := s.Key(key(d), typing, true); a != None || l != NoLine || !s.Visible() {
			t.Errorf("digit %c names no line: %v %v", d, a, l)
		}
	}
}

// TestALineThatWasNotShownCannotBeOpened: before the first drawing, in an
// area too low for it, or too narrow for its digit.
func TestALineThatWasNotShownCannotBeOpened(t *testing.T) {
	s := NewStrip(Colors{})
	s.Arrive(arrived, full())
	if a, _ := s.Key(key('1'), typing, true); a != None {
		t.Error("a line was opened before the strip was drawn")
	}
	draw(s, 80, 3, true) // the title and two lines
	for _, d := range "34" {
		if a, _ := s.Key(key(d), typing, true); a != None || !s.Visible() {
			t.Errorf("digit %c opened a line below the area", d)
		}
	}
	draw(s, 3, 5, true)
	if a, _ := s.Key(key('1'), typing, true); a != None {
		t.Error("a line was opened though its digit did not fit")
	}
	draw(s, 80, 0, true)
	if a, _ := s.Key(key('1'), typing, true); a != None {
		t.Error("a line was opened after a drawing into no area")
	}
	draw(s, 80, 3, true)
	if a, l := s.Key(key('2'), typing, true); a != Open || l != LineWaiting {
		t.Errorf("digit 2 after a drawing of two lines: %v %v", a, l)
	}
	// A new arrival must be drawn before a line of it opens.
	s.Arrive(arrived, full())
	if a, _ := s.Key(key('1'), typing, true); a != None {
		t.Error("a line of a new arrival was opened before it was drawn")
	}
	// Esc closes also what was not drawn yet.
	if a, _ := s.Key(keyEsc, typing, true); a != Close {
		t.Error("Esc did not close a strip that was not drawn yet")
	}
}

func TestDrawWritesItsAreaAndNothingElse(t *testing.T) {
	s, _ := shown(t, full(), 100, 5)
	buf := uv.NewBuffer(40, 12)
	mark := uv.Cell{Content: "#", Width: 1}
	fillAll := func() {
		for y := 0; y < 12; y++ {
			for x := 0; x < 40; x++ {
				buf.SetCell(x, y, &mark)
			}
		}
	}
	fillAll()
	area := uv.Rect(5, 3, 30, 4)
	s.Draw(buf, area, true)
	for y := 0; y < 12; y++ {
		for x := 0; x < 40; x++ {
			in := x >= 5 && x < 35 && y >= 3 && y < 7
			if c := buf.CellAt(x, y); (c.Content == "#") == in {
				t.Fatalf("cell %d,%d holds %q (inside the area: %v)", x, y, c.Content, in)
			}
		}
	}
	// A line too long for the width ends in an ellipsis.
	var row strings.Builder
	for x := 5; x < 35; x++ {
		row.WriteString(buf.CellAt(x, 4).Content)
	}
	if got, want := row.String(), "[1] alerts 2 · lead-2 stopped…"; got != want {
		t.Errorf("row = %q, want %q", got, want)
	}
	// A strip that is not showing writes nothing.
	s.Dismiss()
	fillAll()
	s.Draw(buf, area, true)
	for y := 0; y < 12; y++ {
		for x := 0; x < 40; x++ {
			if buf.CellAt(x, y).Content != "#" {
				t.Fatalf("a closed strip wrote cell %d,%d", x, y)
			}
		}
	}
}

func TestColors(t *testing.T) {
	c := Colors{Fg: red, Bg: green, Dim: blue, Warn: yellow}
	s := NewStrip(Colors{})
	s.SetColors(c)
	s.Arrive(arrived, full())
	buf := draw(s, 100, 5, true)
	for _, tc := range []struct {
		x, y int
		fg   any
	}{
		{0, 0, red},    // the title
		{22, 0, blue},  // the hint
		{0, 1, blue},   // a key mark
		{4, 1, yellow}, // the line of alerts
		{4, 2, red},    // another line
		{99, 4, red},   // a blank
	} {
		cell := buf.CellAt(tc.x, tc.y)
		if cell.Style.Fg != tc.fg || cell.Style.Bg != green {
			t.Errorf("cell %d,%d (%q): fg %v bg %v", tc.x, tc.y, cell.Content, cell.Style.Fg, cell.Style.Bg)
		}
	}
	if buf.CellAt(0, 0).Style.Attrs&uv.AttrBold == 0 {
		t.Error("the title is not bold")
	}
}

func TestLineString(t *testing.T) {
	for l, want := range map[Line]string{NoLine: "none", LineAlerts: "alerts", LineWaiting: "waiting",
		LineApproved: "approved", LineHeld: "held", Line(9): "Line(9)"} {
		if got := l.String(); got != want {
			t.Errorf("Line(%d).String() = %q, want %q", int(l), got, want)
		}
	}
}
