package terminalconsole

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func key(m *Model, s rune) tea.Cmd {
	_, c := m.Update(tea.KeyPressMsg{Code: s, Text: string(s)})
	return c
}
func enter(m *Model)          { m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) }
func size(m *Model, w, h int) { m.Update(tea.WindowSizeMsg{Width: w, Height: h}) }
func content(m *Model) string { return ansi.Strip(m.View().Content) }
func TestNavigationAndScope(t *testing.T) {
	m := New(Fixture{}, "dark")
	size(m, 80, 24)
	enter(m)
	if !strings.Contains(content(m), "Planning lead") {
		t.Fatal(content(m))
	}
	enter(m)
	if !strings.Contains(content(m), "Project: Northstar (fixture) / Lead: Planning lead") {
		t.Fatal(content(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	key(m, 't')
	if !strings.Contains(content(m), "Review launch outline") {
		t.Fatal(content(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	key(m, 'j')
	enter(m)
	enter(m)
	if !strings.Contains(content(m), "Harbor lead") || strings.Contains(content(m), "Planning lead") {
		t.Fatal(content(m))
	}
}
func TestControlsAndQuit(t *testing.T) {
	m := New(Fixture{}, "dark")
	size(m, 80, 24)
	enter(m)
	enter(m)
	before := m.projects[0].Leads[0].Conversation[0]
	for _, k := range "sai" {
		if c := key(m, k); c != nil {
			t.Fatal("control produced command")
		}
	}
	if m.projects[0].Leads[0].Conversation[0] != before || !strings.Contains(content(m), "Unavailable") {
		t.Fatal(content(m))
	}
	if c := key(m, 'q'); c == nil {
		t.Fatal("quit missing")
	} else if _, ok := c().(tea.QuitMsg); !ok {
		t.Fatal("wrong quit message")
	}
}

type malicious struct{}

func (malicious) Snapshot() Projection {
	return Projection{Projects: []Project{{Name: "x\x1b]52;c;bad\x07\u009b\n\u202ey", Leads: []Lead{{Name: strings.Repeat("z", 900)}}}}}
}
func TestSanitizeAndBounds(t *testing.T) {
	m := New(malicious{}, "dark")
	size(m, 200, 20)
	s := content(m)
	if strings.ContainsAny(s, "\x1b\x07\u009b\u202e") || strings.Contains(s, "\nx") {
		t.Fatal(s)
	}
	if len(m.projects[0].Leads[0].Name) > 512 {
		t.Fatal("unbounded")
	}
}
func TestViewportAndTheme(t *testing.T) {
	m := New(Fixture{}, "light")
	size(m, 0, 0)
	if m.View().Content != "" {
		t.Fatal("zero viewport")
	}
	size(m, 4, 1)
	if !strings.Contains(m.View().Content, "30;107") {
		t.Fatal("light theme")
	}
	if ansi.StringWidth(content(m)) > 4 {
		t.Fatal(content(m))
	}
	size(m, 80, 10)
	key(m, '?')
	if !strings.Contains(content(m), "Help") {
		t.Fatal(content(m))
	}
}
