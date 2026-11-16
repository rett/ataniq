package terminalconsole

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/terminalconsole/sessionpane"
	"ataniq/internal/terminalconsole/sessionreplay"
)

// Replay pacing. The console drives the replay player with tea.Tick: each tick
// message carries the time that the player and the pane's frame time limit
// both use, so no clock is read anywhere else.
const (
	tickInterval = 33 * time.Millisecond
	// chunkBytes bounds one write to the pane, which holds its lock for the
	// whole write, so that drawing stays responsive.
	chunkBytes = 4 << 10
	// tickBudget bounds the bytes written in one tick. The rest waits for the
	// next one.
	tickBudget = 64 << 10

	paneMaxCols = sessionpane.MaxCols
	paneMaxRows = sessionpane.MaxRows
)

type tickMsg struct {
	at  time.Time
	gen int
}

// holdMsg redraws the cockpit when the pane stops holding a frame for
// synchronized output.
type holdMsg struct{ at time.Time }

// replay is the session shown in the pane: a recording played into it. A
// recording's own resize records are ignored, because the layout sets the
// pane's size.
type replay struct {
	pane   *sessionpane.Pane
	player *sessionreplay.Player
	gen    int
	base   time.Time
	queue  []sessionreplay.Event
	holdAt time.Time
}

// themeColours are the colours the pane reports to the session's colour
// queries: the theme's, which are what the cockpit paints. The replies are
// discarded in the fixture console.
func (m *Model) themeColours() sessionpane.Colours {
	pal := paletteFor(m.theme)
	return sessionpane.Colours{Foreground: pal.fg, Background: pal.bg, Cursor: pal.fg}
}

// startSession makes a pane and a player for the chosen recording. When there
// is no such recording the pane stays nil and the cockpit says so.
func (m *Model) startSession() {
	m.rep.gen++
	m.rep.base, m.rep.queue, m.rep.holdAt = time.Time{}, nil, time.Time{}
	rec, err := sessionreplay.LoadFixture(m.opts.Recording)
	if err != nil {
		return
	}
	player, err := sessionreplay.NewPlayer(rec)
	if err != nil {
		return
	}
	cols, rows := player.Size()
	if l := m.layout(); l.cockpit {
		cols, rows = l.inner.Dx(), l.inner.Dy()
	}
	m.rep.player = player
	m.rep.pane = sessionpane.New(cols, rows, sessionpane.Options{
		GraphemeWidths: m.graphemes,
		Colours:        m.themeColours(),
		Input:          m.opts.Input,
		Clock:          func() time.Time { return m.now },
	})
}

// Close stops the session pane's goroutine. It may be called more than once.
func (m *Model) Close() {
	if m.rep.pane != nil {
		m.rep.pane.Close()
	}
}

// restart plays the recording again from its start, in a new pane.
func (m *Model) restart() tea.Cmd {
	m.Close()
	m.rep = replay{gen: m.rep.gen}
	m.focus = consoleFocus
	m.startSession()
	if m.started {
		return m.tickCmd()
	}
	return nil
}

func (m *Model) tickCmd() tea.Cmd {
	gen := m.rep.gen
	return m.after(tickInterval, func(t time.Time) tea.Msg { return tickMsg{at: t, gen: gen} })
}

// onTick advances the replay to the tick's time and feeds the pane what has
// become due.
func (m *Model) onTick(v tickMsg) tea.Cmd {
	if v.gen != m.rep.gen || m.rep.pane == nil || m.rep.player == nil {
		return nil
	}
	m.now = v.at
	if m.rep.base.IsZero() {
		m.rep.base = v.at
	}
	elapsed := v.at.Sub(m.rep.base)
	m.rep.queue = append(m.rep.queue, m.rep.player.Advance(elapsed)...)
	m.feed(elapsed)
	if m.sessionEnded() {
		return nil
	}
	return tea.Batch(m.tickCmd(), m.holdCmd())
}

// feed writes the queued events to the pane in bounded chunks, at most
// tickBudget bytes. The recording's end waits for the linger time.
func (m *Model) feed(elapsed time.Duration) {
	budget := tickBudget
	for len(m.rep.queue) > 0 {
		ev := &m.rep.queue[0]
		switch ev.Kind {
		case sessionreplay.KindOutput:
			for len(ev.Data) > 0 && budget > 0 {
				n := min(len(ev.Data), chunkBytes, budget)
				m.rep.pane.Write(ev.Data[:n])
				ev.Data, budget = ev.Data[n:], budget-n
			}
			if len(ev.Data) > 0 {
				return
			}
		case sessionreplay.KindEnd:
			if elapsed < ev.At+m.opts.Linger {
				return
			}
			m.rep.pane.End(ev.Status, ev.StatusKnown)
		}
		m.rep.queue = m.rep.queue[1:]
	}
}

// holdCmd schedules a redraw for when the frame the pane is holding for
// synchronized output stops being shown.
func (m *Model) holdCmd() tea.Cmd {
	deadline, ok := m.rep.pane.HoldDeadline()
	if !ok || deadline.Equal(m.rep.holdAt) {
		return nil
	}
	m.rep.holdAt = deadline
	return m.after(max(0, deadline.Sub(m.now)), func(t time.Time) tea.Msg { return holdMsg{at: t} })
}

func (m *Model) onHold(v holdMsg) {
	if v.at.After(m.now) {
		m.now = v.at
	}
}
