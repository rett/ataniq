package approvals

import (
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
	"ataniq/internal/terminalconsole/pager"
)

// MinCols and MinRows are the smallest window in which a card may be
// decided. Below it the card can still be read, in pages, and says why
// Approve is not offered.
const (
	MinCols = 80
	MinRows = 24
)

// fixedRows is the number of rows of the view that do not scroll: three at
// the top and five at the bottom (the rule, the key hints, two rows for the
// one line that says why a control is off, and the footer), for the list and
// for the card alike.
const fixedRows = 8

// The view's own one-line words for what it cannot do.
const (
	reasonSmall      = "needs a window of 80 columns by 24 rows"
	reasonNoCode     = "no confirm code: the console has no source of randomness"
	reasonSending    = "a decision is being sent"
	reasonStale      = "the review changed while it was read: close the card and open it again"
	reasonDecided    = "this request was decided"
	reasonChecking   = "the console is still finding out whether it may decide"
	reasonReadOnly   = "this console may not decide"
	reasonNoLimitRow = "there is no row under the cursor"
)

type mode int

const (
	modeList mode = iota
	modeOpening
	modeCard
)

// Model is the approvals view. Make one with New, start it with Init, tell
// it its size with SetSize, hand it every message the parent receives while
// it is open (Update) and draw it with Draw. A Model is used from one
// goroutine, as Bubble Tea uses a model.
type Model struct {
	b   Backend
	o   Options
	now func() time.Time
	w   int
	h   int

	mode mode

	// The list.
	listing  card.Listing
	haveList bool
	listErr  string
	listSeq  int
	// items are the pending cards and then the recent ones: the rows the
	// cursor moves over.
	items  []card.Item
	lrows  []row
	cursor int
	top    int
	// note is one line about the list: a held card, an error.
	note string

	// What the backend said about who may decide.
	deciderKnown bool
	decider      bool
	deciderWhy   string

	// The card being opened or open.
	openSeq int
	openID  string
	cs      *cardState
}

// New returns a view that reads from o.Backend. It has read nothing yet:
// Init returns the command that reads the list.
func New(o Options) *Model {
	m := &Model{b: o.Backend, o: o, now: o.Now}
	if m.now == nil {
		m.now = time.Now
	}
	return m
}

// SetGraphemeWidths tells the view whether the outer terminal measures whole
// grapheme clusters (mode 2027); the pager follows it. The returned command
// is to be run, as Update's.
func (m *Model) SetGraphemeWidths(on bool) tea.Cmd {
	m.o.GraphemeWidths = on
	if m.cs != nil && m.cs.pager != nil {
		return m.cs.pager.SetGraphemeWidths(on)
	}
	return nil
}

// Init returns the command that reads the list and asks the backend who may
// decide.
func (m *Model) Init() tea.Cmd { return m.listCmd() }

// Refresh returns the command that reads the list again, and the open card
// again when there is one that can still end. The parent calls it on its
// timer. A card that ended meanwhile closes to one line.
func (m *Model) Refresh() tea.Cmd {
	cmds := []tea.Cmd{m.listCmd()}
	if cs := m.cs; cs != nil && cs.live() && !cs.sending {
		cmds = append(cmds, m.openCmd(cs.id, true))
	}
	return tea.Batch(cmds...)
}

// SetSize tells the view the size of its region. The returned command is to
// be run, as Update's: the pager may need another page.
func (m *Model) SetSize(width, height int) tea.Cmd {
	width, height = max(0, width), max(0, height)
	if width == m.w && height == m.h {
		return nil
	}
	m.w, m.h = width, height
	m.rebuildList()
	var cmd tea.Cmd
	if m.cs != nil {
		m.buildBody()
		m.markRead()
		if m.cs.pager != nil {
			cmd = m.cs.pager.SetSize(m.w, m.h)
		}
	}
	return cmd
}

// Pending returns the number of pending cards the list last showed.
func (m *Model) Pending() int { return len(m.listing.Pending) }

// FieldOpen reports whether the confirm field or the deny reason is open.
// While it is, every printable key goes to the field.
func (m *Model) FieldOpen() bool { return m.cs != nil && m.cs.field != fieldNone }

// CardOpen reports whether a card is open or being opened.
func (m *Model) CardOpen() bool { return m.mode != modeList }

// The messages of the view's own commands. Each carries its owner, so that
// the answer to a command of another view is left alone.
type (
	listedMsg struct {
		owner *Model
		seq   int
		l     card.Listing
		err   error
		ok    bool
		why   string
	}
	openedMsg struct {
		owner   *Model
		seq     int
		id      string
		refresh bool
		// reread says that the card is read again after the owner's
		// decision on it, to take in what comes after the decision.
		reread bool
		c      card.Card
		err    error
	}
	pagedMsg struct {
		owner *Model
		id    string
		seq   int
		n     int
		p     card.Page
		err   error
	}
	decidedMsg struct {
		owner *Model
		id    string
		seq   int
		out   card.Outcome
		err   error
	}
	tickMsg struct{ owner *Model }
)

// listCmd reads the list and asks who may decide, in one command.
func (m *Model) listCmd() tea.Cmd {
	m.listSeq++
	seq, b := m.listSeq, m.b
	return func() tea.Msg {
		l, err := b.List()
		ok, why := b.Decider()
		return listedMsg{owner: m, seq: seq, l: l, err: err, ok: ok, why: why}
	}
}

func (m *Model) openCmd(id string, refresh bool) tea.Cmd {
	seq, b := m.openSeq, m.b
	return func() tea.Msg {
		c, err := b.Open(id)
		return openedMsg{owner: m, seq: seq, id: id, refresh: refresh, c: c, err: err}
	}
}

// rereadCmd reads the card again after a decision that is not held, so that
// the card on the screen takes in the verdict lines that follow a decision.
func (m *Model) rereadCmd(id string) tea.Cmd {
	b := m.b
	return func() tea.Msg {
		c, err := b.Open(id)
		return openedMsg{owner: m, id: id, reread: true, c: c, err: err}
	}
}

func (m *Model) pageCmd(id string, n int) tea.Cmd {
	cs := m.cs
	cs.pageSeq++
	seq, b := cs.pageSeq, m.b
	return func() tea.Msg {
		p, err := b.Page(id, card.PagePaths, n)
		return pagedMsg{owner: m, id: id, seq: seq, n: n, p: p, err: err}
	}
}

func (m *Model) decideCmd(d card.Decision) tea.Cmd {
	cs := m.cs
	cs.decideSeq++
	seq, b := cs.decideSeq, m.b
	return func() tea.Msg {
		out, err := b.Decide(d)
		return decidedMsg{owner: m, id: d.ID, seq: seq, out: out, err: err}
	}
}

func (m *Model) lookedCmd() tea.Cmd {
	at, b := m.now(), m.b
	return func() tea.Msg {
		b.Looked(at)
		return nil
	}
}

func (m *Model) afterCmd(d time.Duration) tea.Cmd {
	fn := func(time.Time) tea.Msg { return tickMsg{owner: m} }
	if m.o.After != nil {
		return m.o.After(d, fn)
	}
	return tea.Tick(d, fn)
}

// errLine is one line in the view's own words for an error of the backend.
// A *record.Error is shown by its code; the text of an error is never drawn.
func errLine(what string, err error) string {
	if code, ok := record.CodeOf(err); ok {
		return what + " (" + string(code) + ")"
	}
	return what
}

// Update handles one message and returns the command to run, which never
// ends the program, and what the parent is asked to do.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, Action) {
	switch v := msg.(type) {
	case listedMsg:
		if v.owner == m {
			return m.onListed(v), Action{}
		}
		return nil, Action{}
	case openedMsg:
		if v.owner == m {
			return m.onOpened(v), Action{}
		}
		return nil, Action{}
	case pagedMsg:
		if v.owner == m {
			m.onPaged(v)
		}
		return nil, Action{}
	case decidedMsg:
		if v.owner == m {
			return m.onDecided(v), Action{}
		}
		return nil, Action{}
	case tickMsg:
		return nil, Action{}
	}
	if m.cs != nil && m.cs.pager != nil {
		return m.updatePager(msg)
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return m.onKey(k)
	}
	if m.FieldOpen() && m.cs.field == fieldConfirm && isTerminalAnswer(msg) {
		m.refuse("input that was not typed was refused, and the field was cleared")
	}
	return nil, Action{}
}

// updatePager hands a message to the pager. ctrl+c is the parent's.
func (m *Model) updatePager(msg tea.Msg) (tea.Cmd, Action) {
	if k, ok := msg.(tea.KeyPressMsg); ok && keyName(k) == "ctrl+c" {
		return nil, Action{Kind: Quit}
	}
	cmd, act := m.cs.pager.Update(msg)
	if act == pager.Close {
		m.cs.pager = nil
	}
	return cmd, Action{}
}

// lockMods are lock states a terminal may report with a key. They never
// change which key it is.
const lockMods = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

// keyName returns the name of a key press for the view's own keys: "a",
// "space", "enter", "esc", "ctrl+c", "pgdown" and so on. A key that carries
// text has the name of that text only when it is one printable character
// with no modifier but shift, and no name otherwise, so that a text that
// spells a key's name is not that key.
func keyName(k tea.KeyPressMsg) string {
	if k.Text == "" {
		return k.String()
	}
	if t, ok := singlePrintable(k); ok {
		if t == " " {
			return "space"
		}
		return t
	}
	return ""
}

// singlePrintable returns the text of a key press that is exactly one
// printable character typed with no modifier but shift: no ctrl, alt, meta,
// hyper or super chord, no text of several characters.
func singlePrintable(k tea.KeyPressMsg) (string, bool) {
	if k.Mod&^(tea.ModShift|lockMods) != 0 || k.Text == "" {
		return "", false
	}
	r, n := utf8.DecodeRuneInString(k.Text)
	if n != len(k.Text) || r == utf8.RuneError || !unicode.IsPrint(r) {
		return "", false
	}
	return k.Text, true
}

// isTerminalAnswer reports whether msg is something a terminal sent by
// itself: a paste, a status or colour report, a capability answer. Such input
// is never the owner typing.
func isTerminalAnswer(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg,
		tea.CursorPositionMsg, tea.ForegroundColorMsg, tea.BackgroundColorMsg, tea.CursorColorMsg,
		tea.ModeReportMsg, tea.CapabilityMsg, tea.TerminalVersionMsg, tea.KeyboardEnhancementsMsg,
		tea.ClipboardMsg, tea.EnvMsg, tea.RawMsg,
		uv.UnknownEvent, uv.UnknownCsiEvent, uv.UnknownOscEvent, uv.UnknownDcsEvent,
		uv.UnknownSosEvent, uv.UnknownPmEvent, uv.UnknownApcEvent,
		uv.TertiaryDeviceAttributesEvent, uv.ModifyOtherKeysEvent, uv.KittyGraphicsEvent,
		uv.WindowOpEvent, uv.DarkColorSchemeEvent, uv.LightColorSchemeEvent:
		return true
	}
	return false
}

// sizeOK says whether the region is large enough for deciding.
func (m *Model) sizeOK() bool { return m.w >= MinCols && m.h >= MinRows }
