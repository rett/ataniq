package limitview

import (
	"image/color"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/approval/limits"
)

// Colors are the colours the view draws with. The parent fills them from its
// theme. A nil colour is the terminal's default.
type Colors struct {
	Fg, Bg color.Color
	// Dim is the colour of the labels and the key help.
	Dim color.Color
	// Warn is the colour of a refusal and of a day left out.
	Warn color.Color
}

// Options configure a Model.
type Options struct {
	Colors Colors
}

// Kind says what the parent is asked to do after an Update.
type Kind int

const (
	// None asks for nothing.
	None Kind = iota
	// Close asks the parent to close the view: Esc was pressed.
	Close
	// Propose asks the parent to ask its backend for Action.Change. The view
	// has applied nothing.
	Propose
)

// Action is what Update returns. Change is set only when Kind is Propose.
type Action struct {
	Kind   Kind
	Change limits.Change
}

// maxDigits bounds the typed number. The longest number the rule allows has
// two digits; a third lets a mistaken 400 be seen and refused.
const maxDigits = 3

// MaxProjectCells bounds the project's name, in cells.
const MaxProjectCells = 40

// MaxErrorCells bounds the line SetError shows, in cells.
const MaxErrorCells = 160

// The view's own words.
const (
	title        = "DAILY CARD LIMIT"
	helpLine     = "Enter takes the suggestion if it is not higher; type digits then Enter for a number; Esc closes"
	sameAsNow    = "the suggestion is the present limit: nothing to change"
	typedSame    = "that is the present limit: nothing changes"
	sentNote     = "change sent; waiting for the new limit"
	reasonReach  = "reached the limit"
	reasonAlert  = "had an alert"
	reasonBoth   = "reached the limit, had an alert"
	confirmFmt   = "press Enter again to set "
	daysHeading  = "last 14 days (a day left out is not counted in the suggestion)"
	raisedNote   = "the limit was already raised today"
	nameFallback = "(unnamed)"
	errFallback  = "the change was refused"
)

// Model is the screen. Make one with New, give it a View with SetView and its
// size with SetSize, hand it every message the parent receives while it is
// open (Update) and draw it with Draw. A Model is used from one goroutine.
type Model struct {
	colors Colors
	view   limits.View
	has    bool
	w, h   int

	// typed is the digits typed so far.
	typed string
	// note is the line under the input: a refusal, a warning or a hint.
	note string
	// warn says that note is a question and that another Enter with the same
	// typed number confirms it.
	warn bool
	// pending says that a change was sent and the parent has not answered
	// yet, with SetView or SetError. While it is set no second change is sent.
	pending bool
}

// New returns a view with no View yet; it draws only its title until SetView.
func New(o Options) *Model { return &Model{colors: o.Colors} }

// SetView shows v, which the parent got from its backend. It clears what was
// typed and every line under the input: v is the state after whatever the
// parent did with the last Action.
func (m *Model) SetView(v limits.View) {
	m.view, m.has = v, true
	m.typed, m.note, m.warn, m.pending = "", "", false, false
}

// SetError shows line, which the parent wrote about its backend's refusal of
// the last change, and lets the owner try again. The view cleans line (it
// keeps only printable characters, one line of at most MaxErrorCells cells,
// an ellipsis where it was cut) and shows nothing of the backend's own text
// beyond that. An empty line shows a fixed one.
func (m *Model) SetError(line string) {
	m.typed, m.warn, m.pending = "", false, false
	m.note = cleanText(line, MaxErrorCells)
	if m.note == "" {
		m.note = errFallback
	}
}

// SetSize tells the view the size of its region.
func (m *Model) SetSize(width, height int) {
	m.w, m.h = max(0, width), max(0, height)
}

// Typed returns the digits typed and not yet sent.
func (m *Model) Typed() string { return m.typed }

// Update handles one message. It returns the Action the parent is asked for.
// Only a key press does anything: a paste, a mouse event or a terminal's
// answer is ignored.
func (m *Model) Update(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return Action{}
	}
	// Keys are matched by code, and only with no modifier: the text a key
	// carries (an input method can commit any string) never stands for Enter,
	// Esc or Backspace.
	if k.Mod != 0 {
		return Action{}
	}
	switch {
	case k.Code == tea.KeyEscape:
		return Action{Kind: Close}
	case m.pending:
		// A change is out: nothing is edited or sent until the parent answers.
	case k.Code == tea.KeyEnter:
		return m.enter()
	case k.Code == tea.KeyBackspace:
		if m.typed != "" {
			m.typed = m.typed[:len(m.typed)-1]
		}
		m.note, m.warn = "", false
	case k.Code >= '0' && k.Code <= '9' && k.Text == string(k.Code):
		// A digit is exactly one ASCII digit as text, with no modifier.
		if len(m.typed) < maxDigits {
			m.typed += k.Text
		}
		m.note, m.warn = "", false
	}
	return Action{}
}

func (m *Model) enter() Action {
	if !m.has {
		return Action{}
	}
	v := m.view
	if m.typed == "" {
		switch {
		case v.Suggestion > v.Limit:
			m.note, m.warn = higherSuggestionNote(v), false
			return Action{}
		case v.Suggestion == v.Limit:
			m.note, m.warn = sameAsNow, false
			return Action{}
		}
		return m.propose(limits.Change{To: v.Suggestion})
	}
	n, err := strconv.Atoi(m.typed)
	if err != nil {
		m.note, m.warn = "", false
		return Action{}
	}
	if n == v.Limit {
		m.note, m.warn = typedSame, false
		return Action{}
	}
	c := limits.Change{To: n, Typed: true}
	if err := limits.Check(v.Limit, v.RaisedToday, c); err != nil {
		m.note, m.warn = err.Error(), false
		return Action{}
	}
	if w := limits.LowerWarning(v.Limit, n); w != "" && !m.warn {
		m.note, m.warn = w+"; "+confirmFmt+strconv.Itoa(n), true
		return Action{}
	}
	return m.propose(c)
}

// higherSuggestionNote returns limits' own reason for why the suggestion,
// which is higher than the present limit, is not taken by Enter: the reason
// a typed raise is refused if one is (the limit was raised today, or it is
// too small to raise), else the reason one key does not raise.
func higherSuggestionNote(v limits.View) string {
	if err := limits.Check(v.Limit, v.RaisedToday, limits.Change{To: v.Suggestion, Typed: true}); err != nil {
		return err.Error()
	}
	if err := limits.Check(v.Limit, v.RaisedToday, limits.Change{To: v.Suggestion}); err != nil {
		return err.Error()
	}
	return ""
}

// propose checks c once more with limits, so that no change leaves the view
// that its rule refuses, and reports it.
func (m *Model) propose(c limits.Change) Action {
	if err := limits.Check(m.view.Limit, m.view.RaisedToday, c); err != nil {
		m.note, m.warn = err.Error(), false
		return Action{}
	}
	m.typed, m.note, m.warn, m.pending = "", sentNote, false, true
	return Action{Kind: Propose, Change: c}
}

// CleanName returns name as the view draws it: only printable characters
// (control, format, direction and zero-width code points and bytes that are
// not UTF-8 are dropped), at most MaxProjectCells cells, an ellipsis where it
// was cut.
func CleanName(name string) string {
	if s := cleanText(name, MaxProjectCells); s != "" {
		return s
	}
	return nameFallback
}

// cleanText returns s with only its printable characters, trimmed, at most
// maxCells cells, an ellipsis where it was cut. It is empty if nothing is
// left.
func cleanText(name string, maxCells int) string {
	var b strings.Builder
	for rest := name; rest != ""; {
		r, n := utf8.DecodeRuneInString(rest)
		rest = rest[n:]
		if r == utf8.RuneError && n <= 1 {
			continue
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if ansi.StringWidth(s) <= maxCells {
		return s
	}
	var out strings.Builder
	used := 0
	for rest := s; rest != ""; {
		c, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if c == "" || used+w > maxCells-1 {
			break
		}
		rest = rest[len(c):]
		out.WriteString(c)
		used += w
	}
	return out.String() + "…"
}

// cleanDate returns a day's date for drawing: YYYY-MM-DD as the rule's own
// values have it, and a question mark for any other character.
func cleanDate(s string) string {
	b := make([]byte, 0, 10)
	for i := 0; i < len(s) && i < 10; i++ {
		if c := s[i]; (c >= '0' && c <= '9') || c == '-' {
			b = append(b, c)
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}

type drawer struct {
	dst  Canvas
	area uv.Rectangle
	cell uv.Cell
}

// Canvas is what the view draws into: a *uv.Buffer, or any uv.Screen.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

func (d *drawer) set(x, y int, content string, width int, st uv.Style) {
	if x < 0 || y < 0 || y >= d.area.Dy() || x+width > d.area.Dx() {
		return
	}
	d.cell = uv.Cell{Content: content, Width: width, Style: st}
	d.dst.SetCell(d.area.Min.X+x, d.area.Min.Y+y, &d.cell)
}

// text draws s from (x, y), at most maxw cells, an ellipsis where it is cut.
// It returns the cells used.
func (d *drawer) text(x, y, maxw int, s string, st uv.Style) int {
	if maxw <= 0 {
		return 0
	}
	limit := maxw
	if ansi.StringWidth(s) > maxw {
		limit = maxw - 1
	}
	used := 0
	for rest := s; rest != ""; {
		c, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if c == "" || used+w > limit {
			break
		}
		rest = rest[len(c):]
		d.set(x+used, y, c, w, st)
		used += w
	}
	if limit < maxw {
		d.set(x+used, y, "…", 1, st)
		used++
	}
	return used
}

// Draw draws the view into area of dst. It writes cells, never escape
// sequences, and every cell of area.
func (m *Model) Draw(dst Canvas, area uv.Rectangle) {
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	d := &drawer{dst: dst, area: area}
	text := uv.Style{Fg: m.colors.Fg, Bg: m.colors.Bg}
	dim := uv.Style{Fg: m.colors.Dim, Bg: m.colors.Bg}
	warn := uv.Style{Fg: m.colors.Warn, Bg: m.colors.Bg}
	for y := 0; y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); x++ {
			d.set(x, y, " ", 1, text)
		}
	}
	w, h := min(m.w, area.Dx()), min(m.h, area.Dy())
	if w <= 0 || h <= 0 {
		return
	}
	y := 0
	row := func(s string, st uv.Style) {
		if y < h {
			d.text(0, y, w, s, st)
		}
		y++
	}
	if !m.has {
		row(title, dim)
		return
	}
	v := m.view
	used := d.text(0, 0, w, title+" · ", dim)
	d.text(used, 0, w-used, CleanName(v.Project), text)
	y = 1
	row("present limit  "+strconv.Itoa(v.Limit), text)
	row("suggestion     "+strconv.Itoa(v.Suggestion), text)
	if v.RaisedToday {
		row(raisedNote, warn)
	} else {
		row("", text)
	}
	row("new limit      "+m.typed+"_", text)
	st := dim
	if m.note != "" && m.note != sentNote {
		st = warn
	}
	row(m.note, st)
	row(helpLine, dim)
	row("", text)
	row(daysHeading, dim)
	for i, day := range v.Days {
		if i >= limits.WindowDays {
			break
		}
		line := cleanDate(day.Date) + "  " + padLeft(strconv.Itoa(day.Uses), 4)
		if day.LeftOut() {
			line += "  left out: " + leftOutWhy(day)
			row(line, warn)
			continue
		}
		row(line, text)
	}
}

func leftOutWhy(d limits.Day) string {
	switch {
	case d.ReachedLimit && d.Alert:
		return reasonBoth
	case d.ReachedLimit:
		return reasonReach
	}
	return reasonAlert
}

func padLeft(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-len(s)) + s
}
