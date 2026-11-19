package approvals

import (
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/card"
)

// Draw draws the view into area of dst, its top left cell at area.Min. It
// draws at the size given to SetSize, which should be area's: where area is
// smaller the drawing is cut, and where it is larger the rest is blank. Only
// area is written, and all of it.
//
// Draw writes cells, never escape sequences, and it calls no backend: what
// has not come yet is drawn once its answer has.
func (m *Model) Draw(dst Canvas, area uv.Rectangle) {
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	(&drawer{dst: dst, area: area, col: m.o.Colors}).fill()
	if m.w <= 0 || m.h <= 0 {
		return
	}
	area.Max.X = min(area.Max.X, area.Min.X+m.w)
	area.Max.Y = min(area.Max.Y, area.Min.Y+m.h)
	if m.cs != nil && m.cs.pager != nil {
		m.cs.pager.Draw(dst, area)
		return
	}
	d := &drawer{dst: dst, area: area, col: m.o.Colors}
	switch m.mode {
	case modeOpening:
		d.put(1, 0, area.Dx()-1, listTitle, d.style(sHead))
		d.put(1, 2, area.Dx()-1, "opening the card  [esc] cancel", d.style(sDim))
		d.put(1, area.Dy()-1, area.Dx()-1, onlyHereLine, d.style(sDim))
	case modeCard:
		m.drawCard(d)
	default:
		m.drawList(d)
	}
}

// cardHint is the word for each key of a card in the key hints. The card's
// own labels are longer than the line has room for at 80 columns.
func cardHint(a card.Action) string {
	switch a {
	case card.ActionSignOff:
		return "sign off"
	case card.ActionApprove:
		return "approve"
	case card.ActionDeny:
		return "deny"
	case card.ActionDiff:
		return "diff"
	case card.ActionLead:
		return "lead"
	case card.ActionBack:
		return "back"
	}
	return ""
}

// hints returns the key hints of the open card: the keys that it offers now.
func (m *Model) hints() string {
	cs := m.cs
	if cs.endedLine != "" {
		return "[esc] back"
	}
	keys := cs.c.Keys()
	if cs.decided {
		keys = []card.Key{{Key: "esc", Action: card.ActionBack}}
	}
	canAct := m.deciderKnown && m.decider
	var parts []string
	for _, k := range keys {
		switch k.Action {
		case card.ActionSignOff, card.ActionDeny:
			if !canAct {
				continue
			}
		case card.ActionApprove:
			if !canAct || !m.sizeOK() {
				continue
			}
		}
		parts = append(parts, "["+k.Key+"] "+cardHint(k.Action))
	}
	return strings.Join(parts, "  ")
}

// cardMessage is the row under the hints: what the open field asks for, the
// state of a decision, the last refusal, or why Approve is off.
func (m *Model) cardMessage() (string, style) {
	cs := m.cs
	switch {
	case cs.status != "":
		return cs.status, sWarn
	case cs.field == fieldConfirm:
		n := utf8.RuneCountInString(cs.typed)
		return "code> " + strings.Repeat("*", n) + strings.Repeat("_", max(0, codeLen-n)) +
			"   the code is in the title   [enter] send   [esc] cancel", sText
	case cs.field == fieldDeny:
		return "reason> " + tailCells(cs.typed, max(8, m.w-40)) + "_   (" + itoa(len(cs.typed)) +
			" of 500 bytes)   [enter] deny   [esc] cancel", sText
	case cs.sending:
		return "sending", sText
	case cs.outcome != "":
		return cs.outcome, sText
	case cs.endedLine != "":
		return "", sText
	}
	if why := m.readOnlyReason(); why != "" {
		return why, sWarn
	}
	ok, why := m.approveReason(m.now())
	if !ok {
		return "approve is off: " + why, sWarn
	}
	return "approve is ready: press [a], then type the code from the title", sText
}

// tailCells returns the end of s, at most n characters, so that what was
// typed last stays on the screen.
func tailCells(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}

// scrollLine says how much of the card lies below the screen.
func (m *Model) scrollLine() string {
	cs := m.cs
	vh := m.cardRowsHeight()
	below := len(cs.body) - (cs.top + vh)
	s := "end of the card"
	if below > 0 {
		s = "▼ " + itoa(below) + " more rows below"
	}
	if d := cs.c.Diff; d != nil && d.Truncated && cs.endedLine == "" {
		s += " · [ and ] turn pages"
	}
	return s
}

// drawCard draws the card into the region: the title, the verb line, the
// scrolling body and the footer.
func (m *Model) drawCard(d *drawer) {
	cs := m.cs
	w, h := d.area.Dx(), d.area.Dy()
	left, right := cs.titleParts(m.shownCode())
	used := d.put(1, 0, w-1, left, d.style(sHead))
	if !cs.whenInBody(max(12, m.w-1)) && cs.endedLine == "" {
		if x := w - width(right) - 1; x >= 1+used+2 {
			d.put(x, 0, w-x, right, d.style(sDim))
		}
	}
	if h < fixedRows+1 {
		// Too small for the layout: the reason, and the footer.
		msg, st := m.cardMessage()
		if h > 1 {
			d.put(1, 1, w-1, msg, d.style(st))
		}
		if h > 2 {
			d.put(1, h-1, w-1, onlyHereLine, d.style(sDim))
		}
		return
	}
	d.put(1, 1, w-1, cs.verbLine(), d.style(sText))
	d.rule(2)
	vh := m.cardRowsHeight()
	for y := 0; y < vh; y++ {
		i := cs.top + y
		if i >= len(cs.body) {
			break
		}
		d.bodyRow(3+y, cs.body[i], i == cs.cursor)
	}
	foot := h - 5
	d.rule(foot)
	d.put(1, foot+1, w-1, m.hints(), d.style(sDim))
	msg, st := m.cardMessage()
	d.putWrapped(1, foot+2, w-1, 2, msg, d.style(st))
	scroll := m.scrollLine()
	used = d.put(1, foot+4, w-1, scroll, d.style(sDim))
	if x := w - width(onlyHereLine) - 1; x >= 1+used+2 {
		d.put(x, foot+4, w-x, onlyHereLine, d.style(sDim))
	}
}
