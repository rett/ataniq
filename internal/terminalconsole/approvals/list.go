package approvals

import (
	"strconv"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/approval/card"
)

// The view's own words for the list.
const (
	listTitle    = "APPROVALS"
	sampleLabel  = "sample cards: a decision changes nothing"
	onlyHereLine = "Approvals are given only here."
	newMark      = "new"
)

// onListed takes in the answer to a list request. An answer to a request
// that a later one replaced is left alone.
func (m *Model) onListed(v listedMsg) tea.Cmd {
	if v.seq != m.listSeq {
		return nil
	}
	m.deciderKnown, m.decider, m.deciderWhy = true, v.ok, v.why
	if v.err != nil {
		m.listErr = errLine("the list could not be read", v.err)
		return nil
	}
	var keep string
	if m.cursor >= 0 && m.cursor < len(m.items) {
		keep = m.items[m.cursor].ID
	}
	m.listErr = ""
	m.listing, m.haveList = v.l, true
	m.rebuildList()
	for i, it := range m.items {
		if it.ID == keep && keep != "" {
			m.cursor = i
		}
	}
	m.clampList()
	if cs := m.cs; cs != nil && m.mode == modeCard {
		m.checkEnded(cs)
	}
	return nil
}

// checkEnded closes the open card to one line when the listing says its
// request is no longer pending.
func (m *Model) checkEnded(cs *cardState) {
	if !cs.live() || cs.sending {
		return
	}
	for _, it := range m.listing.Pending {
		if it.ID == cs.id {
			return
		}
	}
	reason := "it is no longer pending"
	for _, it := range m.listing.Recent {
		if it.ID == cs.id && it.State != "" {
			reason = string(it.State)
			if it.Line != "" {
				reason += ": " + it.Line
			}
		}
	}
	m.endCard(cs, reason)
}

// rebuildList lays out the rows of the list for the present width.
func (m *Model) rebuildList() {
	l := m.listing
	m.items = append(append([]card.Item(nil), l.Pending...), l.Recent...)
	b := &builder{w: max(12, m.w-1)}
	now := m.now()
	i := 0
	section := func(name string, n int) {
		b.head(name + " (" + itoa(n) + ")")
		if n == 0 {
			b.add(seg{"  " + wordNone, sDim})
		}
	}
	itemRows := func(it card.Item, recent bool) {
		var prefix []seg
		if !l.LookedAt.IsZero() && it.Asked.After(l.LookedAt) {
			prefix = append(prefix, seg{newMark + " ", sAccent})
		}
		if it.Held {
			prefix = append(prefix, seg{"[" + card.HeldLabel + "] ", sWarn})
		}
		if recent {
			state := clip(string(it.State))
			if state == "" {
				state = "pending"
			}
			prefix = append(prefix, seg{state + "  ", sText})
		}
		text := clip(it.Verb)
		if it.Feature != "" {
			text += " name=" + clip(it.Feature)
		}
		text += "  " + clip(it.Project) + "  asked " + clock(it.Asked, now)
		if !recent {
			text += "  expires " + clock(it.Expires, it.Asked)
		}
		if recent && it.Line != "" {
			text += "  " + clip(it.Line)
		}
		first, last := b.flow(prefix, 4, text, sText, nil)
		b.mark(first, last, i+1, 0)
		i++
	}
	section("Pending", len(l.Pending))
	for _, it := range l.Pending {
		itemRows(it, false)
	}
	section("Recent decisions, uses and refusals", len(l.Recent))
	for _, it := range l.Recent {
		itemRows(it, true)
	}
	b.head("Standing rules, read-only (" + itoa(len(l.Rules)) + ")")
	if len(l.Rules) == 0 {
		b.add(seg{"  " + wordNone, sDim})
	}
	for _, r := range l.Rules {
		on := "off"
		if r.On {
			on = "on"
		}
		b.flow([]seg{{"  ", sText}}, 4, clip(r.Project)+"  "+clip(r.Name)+"  "+on+"  used "+itoa(r.Uses)+" times: "+clip(r.Text), sText, nil)
	}
	m.lrows = b.rows
}

// listRowsHeight is the number of rows of the list that scroll.
func (m *Model) listRowsHeight() int {
	h := m.h - fixedRows
	if agreementLine(m.listing) != "" {
		h--
	}
	return max(0, h)
}

// clampList keeps the cursor on an item and the item on the screen.
func (m *Model) clampList() {
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	vh := m.listRowsHeight()
	first, last := -1, -1
	for i, r := range m.lrows {
		if r.item == m.cursor+1 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first >= 0 && vh > 0 {
		if last >= m.top+vh {
			m.top = last - vh + 1
		}
		if first < m.top {
			m.top = first
		}
	}
	m.top = max(0, min(m.top, len(m.lrows)-max(vh, 1)))
}

// listKey handles a key in the list.
func (m *Model) listKey(name string) (tea.Cmd, Action) {
	m.note = ""
	switch name {
	case "esc":
		return m.lookedCmd(), Action{Kind: Close}
	case "j", "down":
		m.cursor++
		m.clampList()
	case "k", "up":
		m.cursor--
		m.clampList()
	case "enter":
		if m.cursor < 0 || m.cursor >= len(m.items) {
			m.note = "there is no card to open"
			return nil, Action{}
		}
		return m.openCard(m.items[m.cursor].ID), Action{}
	case "L", "shift+l":
		if m.cursor < 0 || m.cursor >= len(m.items) {
			m.note = reasonNoLimitRow
			return nil, Action{}
		}
		return nil, Action{Kind: Limit, Project: m.items[m.cursor].Project}
	}
	return nil, Action{}
}

// countLine is the header's line with the count from the first card.
func countLine(c card.Count) string {
	if c.Requests == 0 {
		return "From the first card: no request counted yet."
	}
	return "From the first card: " + strconv.Itoa(c.Met) + " of " + strconv.Itoa(c.Requests) +
		" requests would have met the First template."
}

// drawList draws the list into the region.
func (m *Model) drawList(d *drawer) {
	w, h := d.area.Dx(), d.area.Dy()
	title := listTitle + "  " + itoa(len(m.listing.Pending)) + " pending"
	if !m.haveList && m.listErr == "" {
		title += "  (reading the list)"
	}
	x := 1 + d.put(1, 0, w-1, title, d.style(sHead))
	if m.listing.Sample {
		d.put(x+2, 0, w-x-2, sampleLabel, d.style(sWarn))
	}
	if m.listErr != "" {
		d.put(1, 1, w-1, m.listErr, d.style(sWarn))
	} else {
		d.put(1, 1, w-1, countLine(m.listing.Count), d.style(sDim))
	}
	top := 2
	if al := agreementLine(m.listing); al != "" {
		d.put(1, top, w-1, al, d.style(sDim))
		top++
	}
	d.rule(top)
	body := top + 1
	vh := m.listRowsHeight()
	for y := 0; y < vh; y++ {
		i := m.top + y
		if i >= len(m.lrows) {
			break
		}
		r := m.lrows[i]
		d.bodyRow(body+y, r, r.item != 0 && r.item == m.cursor+1 && !r.cont && m.firstRow(i))
	}
	foot := h - 5
	if foot > body {
		d.rule(foot)
		hints := "[enter] open  [j/k] move  [L] limit of this project  [esc] back"
		d.put(1, foot+1, w-1, hints, d.style(sDim))
		d.putWrapped(1, foot+2, w-1, 2, m.listMessage(), d.style(sWarn))
		d.put(1, foot+4, w-1, onlyHereLine, d.style(sDim))
	}
}

// firstRow reports whether row i is the first row of its item.
func (m *Model) firstRow(i int) bool {
	return i == 0 || m.lrows[i-1].item != m.lrows[i].item
}

// listMessage is the list's one line under the hints: a note, the reason
// the view is read-only, or the reading of a sample.
func (m *Model) listMessage() string {
	switch {
	case m.note != "":
		return m.note
	case m.deciderKnown && !m.decider:
		return "read-only: " + m.deciderReasonText()
	case !m.deciderKnown:
		return reasonChecking
	}
	return ""
}

// deciderReasonText is the reason the backend gave for read-only, or ours.
func (m *Model) deciderReasonText() string {
	if m.deciderWhy != "" {
		return m.deciderWhy
	}
	return reasonReadOnly
}
