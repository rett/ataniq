// Package terminalconsole presents bounded, read-only cockpit projections.
package terminalconsole

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Source supplies a snapshot, not an authority for commands or state changes.
type Source interface{ Snapshot() Projection }

// Projection describes display data and why live control is unavailable.
type Projection struct {
	Projects           []Project
	ControlUnavailable string
}
type Project struct {
	Name  string
	Leads []Lead
	Tasks []string
}
type Lead struct {
	Name         string
	Subagents    []string
	Conversation []string
}

// Model holds its own sanitized, bounded copy of a source snapshot.
type Model struct {
	projects                                         []Project
	leadSelections                                   []int
	controlUnavailable                               string
	project, lead, projectCursor, leadCursor, scroll int
	page, theme, notice                              string
	width, height                                    int
}

func clean(s string) string {
	var b strings.Builder
	// Bound both inspection and output; do not emit partial UTF-8 sequences.
	for inspected := 0; len(s) > 0 && inspected < 4096; {
		r, n := utf8.DecodeRuneInString(s)
		if inspected+n > 4096 {
			break
		}
		s = s[n:]
		inspected += n
		if r == utf8.RuneError && n == 1 {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if b.Len()+n > 512 {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
func bounded(ss []string) []string {
	if len(ss) > 64 {
		ss = ss[:64]
	}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, clean(s))
	}
	return out
}
func New(source Source, theme string) *Model {
	m := &Model{theme: theme, page: "projects"}
	if source == nil {
		return m
	}
	snapshot := source.Snapshot()
	m.controlUnavailable = clean(snapshot.ControlUnavailable)
	if m.controlUnavailable == "" {
		m.controlUnavailable = "No live control capability is available."
	}
	ps := snapshot.Projects
	if len(ps) > 32 {
		ps = ps[:32]
	}
	for _, p := range ps {
		cp := Project{Name: clean(p.Name), Tasks: bounded(p.Tasks)}
		ls := p.Leads
		if len(ls) > 32 {
			ls = ls[:32]
		}
		for _, l := range ls {
			cp.Leads = append(cp.Leads, Lead{Name: clean(l.Name), Subagents: bounded(l.Subagents), Conversation: bounded(l.Conversation)})
		}
		m.projects = append(m.projects, cp)
		m.leadSelections = append(m.leadSelections, 0)
	}
	return m
}
func (m *Model) Init() tea.Cmd { return nil }
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = min(4096, max(0, v.Width))
		m.height = min(4096, max(0, v.Height))
	case tea.KeyPressMsg:
		key := v.String()
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.page = "help"
			m.scroll = 0
		case "esc", "backspace":
			switch m.page {
			case "conversation", "tasks":
				m.page = "leads"
			case "leads", "help":
				m.page = "projects"
			}
			m.scroll = 0
		case "up", "k":
			switch m.page {
			case "projects":
				m.projectCursor = max(0, m.projectCursor-1)
			case "leads":
				m.leadCursor = max(0, m.leadCursor-1)
			default:
				m.scroll = max(0, m.scroll-1)
			}
		case "down", "j":
			switch m.page {
			case "projects":
				m.projectCursor = min(max(0, len(m.projects)-1), m.projectCursor+1)
			case "leads":
				if len(m.projects) > 0 {
					m.leadCursor = min(max(0, len(m.projects[m.project].Leads)-1), m.leadCursor+1)
				}
			default:
				m.scroll = min(max(0, len(m.lines())-1), m.scroll+1)
			}
		case "enter":
			if m.page == "projects" && len(m.projects) > 0 {
				m.leadSelections[m.project] = m.leadCursor
				m.project = m.projectCursor
				m.leadCursor = m.leadSelections[m.project]
				m.lead = m.leadCursor
				m.page = "leads"
				m.scroll = 0
			} else if m.page == "leads" && len(m.projects) > 0 && len(m.projects[m.project].Leads) > 0 {
				m.lead = m.leadCursor
				m.page = "conversation"
				m.scroll = 0
			}
		case "t":
			if m.page == "leads" {
				m.page = "tasks"
				m.scroll = 0
			}
		case "s", "a", "i":
			m.notice = "Unavailable: " + m.controlUnavailable
		}
	}
	return m, nil
}
func (m *Model) lines() []string {
	lines := []string{"ATANIQ / DEMO FIXTURE", "Read-only fixture; no live backend", ""}
	if len(m.projects) == 0 {
		return append(lines, "No projects")
	}
	p := m.projects[m.project]
	switch m.page {
	case "projects":
		lines = append(lines, "Projects")
		for i, p := range m.projects {
			mark := "  "
			if i == m.projectCursor {
				mark = "> "
			}
			lines = append(lines, mark+p.Name)
		}
	case "leads":
		lines = append(lines, "Project: "+p.Name, "Persistent leads (fixture)")
		if len(p.Leads) == 0 {
			lines = append(lines, "  No leads")
		}
		for i, l := range p.Leads {
			mark := "  "
			if i == m.leadCursor {
				mark = "> "
			}
			lines = append(lines, mark+l.Name)
			for _, s := range l.Subagents {
				lines = append(lines, "    subagent: "+s)
			}
		}
	case "tasks":
		lines = append(lines, "Project: "+p.Name, "Tasks (fixture)")
		lines = append(lines, p.Tasks...)
	case "conversation":
		if len(p.Leads) > 0 {
			l := p.Leads[m.lead]
			lines = append(lines, "Project: "+p.Name+" / Lead: "+l.Name, "Conversation (fixture)")
			lines = append(lines, l.Conversation...)
		}
	case "help":
		lines = append(lines, "Help", "Up/down or j/k: navigate or scroll", "Enter: open project or conversation", "t: project tasks   Esc: back", "s/a/i: unavailable live controls", "q or Ctrl-C: quit")
	}
	return lines
}
func (m *Model) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		return tea.NewView("")
	}
	all := m.lines()
	lines := []string{all[0]}
	if m.height > 1 {
		body := all[1:]
		start := 0
		room := m.height - 1
		// Identity and selected row take precedence at height two. Otherwise
		// reserve a line for an unavailable-control notice.
		showNotice := m.notice != "" && (room > 1 || (m.page != "projects" && m.page != "leads"))
		if showNotice {
			room--
		}
		if m.page == "projects" {
			start = max(0, 3+m.projectCursor-(room-1))
		} else if m.page == "leads" {
			selected := 4
			if len(m.projects) > 0 {
				for i := 0; i < m.leadCursor; i++ {
					selected += 1 + len(m.projects[m.project].Leads[i].Subagents)
				}
			}
			start = max(0, selected-(room-1))
		} else {
			start = min(m.scroll, max(0, len(body)-1))
		}
		start = min(start, len(body))
		end := start + min(room, len(body)-start)
		lines = append(lines, body[start:end]...)
		if showNotice {
			lines = append(lines, m.notice)
		}
	}
	for i, s := range lines {
		lines[i] = ansi.Truncate(s, m.width, "")
	}
	content := strings.Join(lines, "\n")
	// Styling is generated locally, after untrusted text has been sanitized.
	if content != "" {
		fg, bg := "97", "40"
		if m.theme == "light" {
			fg, bg = "30", "107"
		}
		content = fmt.Sprintf("\x1b[%s;%sm%s\x1b[0m", fg, bg, content)
	}
	return tea.NewView(content)
}
