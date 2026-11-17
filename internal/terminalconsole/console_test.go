package terminalconsole

import (
	"strings"
	"testing"
	"unicode/utf8"

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
	for _, k := range "a" {
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

type hostile struct{}

func (hostile) Snapshot() Projection {
	bad := "x\x1b]52;c;bad\x07\x1b[2J\u009b‮ y" + strings.Repeat("z", 900)
	p := Projection{Operator: bad, ControlUnavailable: bad, Gate: Gate{Action: bad, Scope: bad, Target: bad, Approver: bad, State: bad}}
	for i := 0; i < 500; i++ {
		p.Contract = append(p.Contract, Check{Name: bad, State: bad})
		p.Timeline = append(p.Timeline, TimelineEntry{Time: bad, Text: bad})
		p.Controls = append(p.Controls, Control{Name: bad, Key: bad, Reason: bad})
	}
	lead := Lead{Name: bad, Subagents: []string{bad}, Session: Session{Provider: bad, Runtime: bad, Authority: bad, Host: bad, ID: bad, Attachment: bad, Binding: bad, Execution: bad}}
	p.Projects = []Project{{Name: bad, Leads: []Lead{lead}}}
	return p
}

func TestExtendedProjectionIsSanitizedAndBounded(t *testing.T) {
	m := New(hostile{}, "dark")
	t.Cleanup(m.Close)
	if len(m.contract) > maxContract || len(m.timeline) > maxList || len(m.controls) > maxControls {
		t.Fatal(len(m.contract), len(m.timeline), len(m.controls))
	}
	check := func(s string) {
		if len(s) > 512 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x1b\x07\u009b‮ ") {
			t.Fatalf("unsafe %q", s)
		}
	}
	for _, s := range []string{m.operator, m.gate.Action, m.gate.Scope, m.gate.Target, m.gate.Approver, m.gate.State, m.controlUnavailable,
		m.contract[0].Name, m.contract[0].State, m.timeline[0].Time, m.timeline[0].Text, m.controls[0].Name, m.controls[0].Key, m.controls[0].Reason} {
		check(s)
	}
	ss := m.projects[0].Leads[0].Session
	for _, s := range []string{ss.Provider, ss.Runtime, ss.Authority, ss.Host, ss.ID, ss.Attachment, ss.Binding, ss.Execution} {
		check(s)
	}
	// Every state of the cockpit draws no sequence of the snapshot's.
	for _, sz := range [][2]int{{140, 45}, {100, 30}, {80, 24}} {
		size(m, sz[0], sz[1])
		for _, keys := range []string{"", "?", "\x1b", "K"} {
			switch keys {
			case "?":
				key(m, '?')
			case "\x1b":
				m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			case "K":
				m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
			}
			if bad := foreignSequence(m.View().Content); bad != "" {
				t.Fatalf("%v: %q", sz, bad)
			}
		}
	}
}

// foreignSequence returns the first escape sequence in content that is not a
// style (CSI with digits, semicolons and colons, ended by m), or a control
// character; it returns "" when there is none.
func foreignSequence(content string) string {
	for i := 0; i < len(content); i++ {
		switch c := content[i]; {
		case c == 0x1b:
			j := i + 1
			if j >= len(content) || content[j] != '[' {
				return content[i:min(len(content), i+12)]
			}
			for j++; j < len(content) && (content[j] >= '0' && content[j] <= '9' || content[j] == ';' || content[j] == ':'); j++ {
			}
			if j >= len(content) || content[j] != 'm' {
				return content[i:min(len(content), j+1)]
			}
			i = j
		case c < 0x20 && c != '\n', c == 0x7f:
			return string(c)
		}
	}
	return ""
}

func TestNilSourceDrawsAnEmptyCockpit(t *testing.T) {
	m := New(nil, "light")
	t.Cleanup(m.Close)
	for _, sz := range [][2]int{{140, 45}, {100, 30}, {80, 24}} {
		size(m, sz[0], sz[1])
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		_ = m.View()
	}
}
