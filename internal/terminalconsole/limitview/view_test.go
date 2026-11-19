package limitview

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/limits"
)

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// sampleView builds a View through limits, as a parent would.
func sampleView(t *testing.T, limit int, uses []int, raised bool) limits.View {
	t.Helper()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var days []limits.Day
	for i, n := range uses {
		days = append(days, limits.Day{Date: now.AddDate(0, 0, -(i + 1)).Format("2006-01-02"), Uses: n})
	}
	var last time.Time
	if raised {
		last = now
	}
	v, err := limits.NewView("project-a", limit, days, last, now)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func newModel(v limits.View) *Model {
	m := New(Options{})
	m.SetSize(100, 30)
	m.SetView(v)
	return m
}

func typeKeys(m *Model, s string) {
	for _, c := range s {
		m.Update(key(string(c)))
	}
}

func screen(m *Model) string {
	buf := uv.NewBuffer(100, 30)
	m.Draw(buf, buf.Bounds())
	var b strings.Builder
	for y := 0; y < 30; y++ {
		for x := 0; x < 100; x++ {
			c := buf.CellAt(x, y)
			if c == nil || c.Content == "" {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(c.Content)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestShowsEverything(t *testing.T) {
	v := sampleView(t, 12, []int{3, 5, 0, 7}, true)
	v.Days[13].ReachedLimit = true
	v.Days[12].Alert = true
	m := newModel(v)
	s := screen(m)
	for _, want := range []string{"project-a", "present limit  12", "suggestion     14", "already raised today",
		"2026-11-18", "left out: reached the limit", "left out: had an alert", "last 14 days"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if n := strings.Count(s, "2026-"); n != 14 {
		t.Errorf("%d days drawn, want 14", n)
	}
	v.Days[13].Alert = true
	m.SetView(v)
	if !strings.Contains(screen(m), "reached the limit, had an alert") {
		t.Error("both marks not shown")
	}
}

func TestEnterAcceptsLowerSuggestion(t *testing.T) {
	m := newModel(sampleView(t, 20, []int{3}, false)) // suggestion 6
	a := m.Update(key("enter"))
	if a.Kind != Propose || a.Change != (limits.Change{To: 6}) {
		t.Fatalf("got %+v", a)
	}
}

func TestEnterEqualSuggestion(t *testing.T) {
	m := newModel(sampleView(t, 6, []int{3}, false))
	if a := m.Update(key("enter")); a.Kind != None {
		t.Fatalf("got %+v", a)
	}
	if !strings.Contains(screen(m), sameAsNow) {
		t.Error("no note")
	}
}

func TestEnterHigherSuggestionGivesLimitsReason(t *testing.T) {
	cases := []struct {
		name   string
		limit  int
		uses   []int
		raised bool
		want   string // a part of the reason, to see that the right one was chosen
	}{
		{"can be typed", 12, []int{9}, false, "a higher number has to be typed"},
		{"raised today", 12, []int{9}, true, "already raised"},
		{"limit of one", 1, []int{9}, false, "cannot be raised"},
	}
	for _, c := range cases {
		v := sampleView(t, c.limit, c.uses, c.raised)
		if v.Suggestion <= v.Limit {
			t.Fatalf("%s: suggestion %d is not higher than %d", c.name, v.Suggestion, v.Limit)
		}
		reason := limits.Check(v.Limit, v.RaisedToday, limits.Change{To: v.Suggestion, Typed: true})
		if reason == nil {
			reason = limits.Check(v.Limit, v.RaisedToday, limits.Change{To: v.Suggestion})
		}
		m := newModel(v)
		if a := m.Update(key("enter")); a.Kind != None {
			t.Fatalf("%s: got %+v", c.name, a)
		}
		s := screen(m)
		if !strings.Contains(s, reason.Error()) || !strings.Contains(reason.Error(), c.want) {
			t.Errorf("%s: screen lacks limits' reason %q:\n%s", c.name, reason, s)
		}
		if strings.Contains(s, "has to be typed") != (c.want == "a higher number has to be typed") {
			t.Errorf("%s: the hint to type a number is wrong:\n%s", c.name, s)
		}
	}
}

func TestTypedPresentNumberSendsNothing(t *testing.T) {
	for _, limit := range []int{12, 1, 40} {
		m := newModel(sampleView(t, limit, []int{3}, false))
		typeKeys(m, strconv.Itoa(limit))
		if a := m.Update(key("enter")); a.Kind != None {
			t.Fatalf("limit %d: got %+v", limit, a)
		}
		if !strings.Contains(screen(m), typedSame) {
			t.Errorf("limit %d: no note that nothing changes", limit)
		}
		// A second Enter still sends nothing.
		if a := m.Update(key("enter")); a.Kind != None {
			t.Fatalf("limit %d: second Enter got %+v", limit, a)
		}
	}
}

func TestTypedRaise(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{9}, false))
	typeKeys(m, "18")
	if m.Typed() != "18" {
		t.Fatal(m.Typed())
	}
	a := m.Update(key("enter"))
	if a.Kind != Propose || a.Change != (limits.Change{To: 18, Typed: true}) {
		t.Fatalf("got %+v", a)
	}
	if m.Typed() != "" {
		t.Error("typed number kept")
	}
	// Nothing applied: the old view is still drawn.
	if !strings.Contains(screen(m), "present limit  12") {
		t.Error("view applied the change itself")
	}
}

func TestRefusalsShowLimitsReason(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		raise bool
		typed string
	}{
		{"more than half", 12, false, "19"},
		{"above max", 12, false, "41"},
		{"under min", 12, false, "0"},
		{"second raise", 12, true, "15"},
		{"limit of one", 1, false, "2"},
	}
	for _, c := range cases {
		m := newModel(sampleView(t, c.limit, []int{3}, c.raise))
		typeKeys(m, c.typed)
		n := 0
		for _, d := range c.typed {
			n = n*10 + int(d-'0')
		}
		want := limits.Check(c.limit, c.raise, limits.Change{To: n, Typed: true}).Error()
		if a := m.Update(key("enter")); a.Kind != None {
			t.Errorf("%s: reported %+v", c.name, a)
		}
		if !strings.Contains(screen(m), want) {
			t.Errorf("%s: screen lacks %q", c.name, want)
		}
		if m.Typed() != c.typed {
			t.Errorf("%s: typed number dropped", c.name)
		}
	}
}

func TestLoweringToOneAsksOnce(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{3}, false))
	typeKeys(m, "1")
	if a := m.Update(key("enter")); a.Kind != None {
		t.Fatalf("first Enter reported %+v", a)
	}
	if !strings.Contains(screen(m), "cannot be raised again") {
		t.Error("no warning")
	}
	a := m.Update(key("enter"))
	if a.Kind != Propose || a.Change != (limits.Change{To: 1, Typed: true}) {
		t.Fatalf("got %+v", a)
	}
	// An edit drops the question.
	typeKeys(m, "2")
	m.Update(key("enter"))
	typeKeys(m, "0")
	m.Update(key("backspace"))
	if a := m.Update(key("enter")); a.Kind != None {
		t.Fatalf("edited number confirmed without a new question: %+v", a)
	}
}

func TestBackspaceAndLength(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{3}, false))
	typeKeys(m, "12345")
	if m.Typed() != "123" {
		t.Fatal(m.Typed())
	}
	m.Update(key("backspace"))
	m.Update(key("backspace"))
	m.Update(key("backspace"))
	m.Update(key("backspace"))
	if m.Typed() != "" {
		t.Fatal(m.Typed())
	}
}

func TestEscCloses(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{3}, false))
	if a := m.Update(key("esc")); a.Kind != Close {
		t.Fatalf("got %+v", a)
	}
}

func TestSetViewShowsNewView(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{9}, false))
	typeKeys(m, "18")
	m.Update(key("enter"))
	v, err := limits.Apply(sampleView(t, 12, []int{9}, false), limits.Change{To: 18, Typed: true})
	if err != nil {
		t.Fatal(err)
	}
	m.SetView(v)
	s := screen(m)
	if !strings.Contains(s, "present limit  18") || !strings.Contains(s, "already raised today") {
		t.Errorf("new view not shown:\n%s", s)
	}
	if strings.Contains(s, sentNote) {
		t.Error("old note kept")
	}
}

func TestNoSingleKeyRaises(t *testing.T) {
	views := []limits.View{
		sampleView(t, 12, []int{20}, false), // suggestion 40
		sampleView(t, 12, []int{9}, false),
		sampleView(t, 1, []int{9}, false),
		sampleView(t, 40, []int{9}, true),
	}
	var keys []tea.KeyPressMsg
	for c := rune(0); c < 128; c++ {
		keys = append(keys, tea.KeyPressMsg{Code: c, Text: string(c)}, tea.KeyPressMsg{Code: c})
		for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt, tea.ModShift, tea.ModCtrl | tea.ModAlt} {
			keys = append(keys, tea.KeyPressMsg{Code: c, Mod: mod})
		}
	}
	for _, c := range []rune{tea.KeyEnter, tea.KeyEscape, tea.KeyBackspace, tea.KeyTab, tea.KeyUp, tea.KeyDown,
		tea.KeyLeft, tea.KeyRight, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd, tea.KeyDelete,
		tea.KeyInsert, tea.KeySpace, tea.KeyF1, tea.KeyF12, tea.KeyKpEnter, tea.KeyKpPlus} {
		keys = append(keys, tea.KeyPressMsg{Code: c}, tea.KeyPressMsg{Code: c, Mod: tea.ModShift}, tea.KeyPressMsg{Code: c, Mod: tea.ModCtrl})
	}
	for _, v := range views {
		for _, k := range keys {
			m := newModel(v)
			a := m.Update(k)
			if a.Kind == Propose && a.Change.To > v.Limit {
				t.Errorf("key %q (limit %d) raised to %d", k.String(), v.Limit, a.Change.To)
			}
			if a.Kind == Propose && limits.Check(v.Limit, v.RaisedToday, a.Change) != nil {
				t.Errorf("key %q reported a change the rule refuses", k.String())
			}
		}
	}
}

func TestKeysMatchByCodeWithNoModifier(t *testing.T) {
	// The suggestion is lower, so a real Enter would send a change.
	m := newModel(sampleView(t, 20, []int{3}, false))
	for _, k := range []tea.KeyPressMsg{
		{Code: '1', Text: "1", Mod: tea.ModAlt},
		{Code: '1', Text: "12"},
		{Code: '1', Text: "1", Mod: tea.ModCtrl},
		{Code: 'x', Text: "1"},
		{Code: 'x', Text: "12"},
		{Code: 'x', Text: "enter"},
		{Code: 'x', Text: "esc"},
		{Code: 'x', Text: "backspace"},
		{Code: tea.KeyEnter, Mod: tea.ModAlt},
		{Code: tea.KeyEscape, Mod: tea.ModShift},
		{Code: tea.KeyBackspace, Mod: tea.ModCtrl},
		{Code: '1'},
	} {
		if a := m.Update(k); a.Kind != None {
			t.Errorf("key %+v acted: %+v", k, a)
		}
		if m.Typed() != "" {
			t.Fatalf("key %+v typed %q", k, m.Typed())
		}
	}
	typeKeys(m, "1")
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt})
	if m.Typed() != "1" {
		t.Errorf("modified Backspace edited: %q", m.Typed())
	}
}

func TestOtherMessagesIgnored(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{3}, false))
	for _, msg := range []tea.Msg{tea.PasteMsg{Content: "18"}, tea.MouseClickMsg{}, tea.WindowSizeMsg{Width: 1, Height: 1}, nil, "18"} {
		if a := m.Update(msg); a.Kind != None {
			t.Errorf("%T acted: %+v", msg, a)
		}
	}
	if m.Typed() != "" {
		t.Error("a paste was typed")
	}
}

func TestHostileProject(t *testing.T) {
	for _, name := range []string{
		"a\x1b[31mred\x1b]0;title\x07b\x9b1m",
		"x\u202eevil\u200b\u2066y",
		"bad\xff\xfebytes",
		strings.Repeat("w", 1<<20),
		"[a] approve   [x] deny",
		"\x1bP1;2q data\x1b\\",
		"",
		"\n\t\x00",
	} {
		got := CleanName(name)
		for _, r := range got {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e || r == 0x200b || r == 0x2066 || r == 0xfffd {
				t.Errorf("CleanName(%q) holds %U", name, r)
			}
		}
		if len([]rune(got)) > MaxProjectCells {
			t.Errorf("CleanName(%q) is %d runes", name, len([]rune(got)))
		}
		v := sampleView(t, 12, []int{3}, false)
		v.Project = name
		s := screen(newModel(v))
		for _, r := range s {
			if r != '\n' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e || r == 0x200b) {
				t.Errorf("screen for %q holds %U", name, r)
			}
		}
	}
	if CleanName("") != nameFallback {
		t.Error("empty name not replaced")
	}
}

func TestHostileDate(t *testing.T) {
	v := sampleView(t, 12, []int{3}, false)
	v.Days[0].Date = "\x1b[31m2026-11-01\x1b[0m"
	s := screen(newModel(v))
	if strings.ContainsRune(s, 0x1b) {
		t.Error("escape drawn")
	}
}

func TestDrawFillsAreaAndSmallSizes(t *testing.T) {
	m := newModel(sampleView(t, 12, []int{3}, false))
	for _, sz := range [][2]int{{0, 0}, {1, 1}, {5, 3}, {20, 8}, {200, 60}} {
		m.SetSize(sz[0], sz[1])
		buf := uv.NewBuffer(100, 30)
		m.Draw(buf, buf.Bounds())
		m.Draw(buf, uv.Rect(-5, -5, 0, 0))
	}
	New(Options{}).Draw(uv.NewBuffer(10, 3), uv.Rect(0, 0, 10, 3))
	// No View yet: Enter does nothing.
	e := New(Options{})
	if a := e.Update(key("enter")); a.Kind != None {
		t.Error("enter without a view acted")
	}
}
