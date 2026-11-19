package approvals

import (
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
	"ataniq/internal/terminalconsole/pager"
)

// codeAlphabet holds the characters of the confirm code: those that are hard
// to mistake for one another.
const codeAlphabet = "ACDEFGHJKLMNPQRTUVWXY34679"

// codeLen is the length of the confirm code.
const codeLen = 4

// burstGap is the least time that may lie between two key presses typed into
// the confirm field. Two presses closer than that are not a person typing.
const burstGap = 30 * time.Millisecond

type fieldKind int

const (
	fieldNone fieldKind = iota
	fieldConfirm
	fieldDeny
)

// cardState is everything the view holds about the card that is open: the
// card, what the reader has done with it, the confirm code and the field.
// The code is in this value and nowhere else; no value handed to the parent
// or to the backend holds it.
type cardState struct {
	id       string
	c        card.Card
	reading  card.Reading
	openedAt time.Time
	code     string

	// body is the card laid out for the width bodyW; cursor is the row
	// under the cursor and top the first row on the screen.
	body   []row
	bodyW  int
	cursor int
	top    int

	// The field that is open, what was typed into it and when the last key
	// of the confirm field arrived.
	field   fieldKind
	typed   string
	lastKey time.Time

	// status is one line about the last key; it is cleared by the next.
	// outcome is the backend's line after a decision, which stays.
	status  string
	outcome string
	// sending says that a decision has been handed to the backend and its
	// answer has not come. decided says that an answer came.
	sending   bool
	decided   bool
	decideSeq int
	// endedLine, when set, replaces the card: the request ended while the
	// card was open.
	endedLine string
	// stale says that a page showed that the review changed.
	stale bool

	// page is the page of the whole path list that was read last.
	page        *card.Page
	pageLoading bool
	pageSeq     int
	// turned says that the reader asked for the page that is being read,
	// so the view goes to its top when it comes.
	turned bool

	// pager is the open pager on the patch.
	pager *pager.Model
}

// live says that the card may still be decided: it has not ended and no
// decision of this view is under way or done.
func (cs *cardState) live() bool {
	return cs.endedLine == "" && !cs.decided && !cs.c.Ended()
}

// shownCode returns the code to draw in the title, or "" when none is
// drawn: the card cannot be approved from here, or the code is not made.
func (m *Model) shownCode() string {
	cs := m.cs
	if cs == nil || cs.code == "" || !cs.live() || !m.deciderKnown || !m.decider || !m.sizeOK() {
		return ""
	}
	return cs.code
}

// newCode makes a confirm code from the reader, or "" when it cannot. A byte
// is used only when it is below the largest multiple of the alphabet's size,
// so that every character is equally likely.
func (m *Model) newCode() string {
	if m.o.Random == nil {
		return ""
	}
	n := len(codeAlphabet)
	limit := 256 - 256%n
	code := make([]byte, 0, codeLen)
	buf := make([]byte, 16)
	for tries := 0; len(code) < codeLen && tries < 8; tries++ {
		if _, err := io.ReadFull(m.o.Random, buf); err != nil {
			return ""
		}
		for _, b := range buf {
			if int(b) < limit && len(code) < codeLen {
				code = append(code, codeAlphabet[int(b)%n])
			}
		}
	}
	if len(code) < codeLen {
		return ""
	}
	return string(code)
}

// openCard asks the backend for a card. The list stays under the opening
// line until it comes.
func (m *Model) openCard(id string) tea.Cmd {
	m.openSeq++
	m.mode, m.openID = modeOpening, id
	return m.openCmd(id, false)
}

// onOpened takes in the answer to Open.
func (m *Model) onOpened(v openedMsg) tea.Cmd {
	if v.reread {
		m.onReread(v)
		return nil
	}
	if v.refresh {
		// The card is read again to see whether its request ended.
		cs := m.cs
		if cs == nil || cs.id != v.id || !cs.live() || cs.sending {
			return nil
		}
		if v.err == nil && v.c.Ended() {
			m.endCard(cs, string(v.c.After.State))
		}
		return nil
	}
	if m.mode != modeOpening || v.seq != m.openSeq || v.id != m.openID {
		return nil
	}
	if v.err != nil {
		m.mode = modeList
		m.note = errLine("the card could not be opened", v.err)
		return nil
	}
	if v.c.BindingSHA256 == "" {
		m.mode = modeList
		m.note = card.ReasonNoCard
		return nil
	}
	now := m.now()
	cs := &cardState{id: v.id, c: v.c, openedAt: now, reading: card.Reading{Opened: now}}
	cs.code = m.newCode()
	m.cs, m.mode = cs, modeCard
	cmds := []tea.Cmd{m.afterCmd(card.OpenDelay)}
	if d := cs.c.Diff; d != nil && d.Truncated {
		cs.pageLoading = true
		cmds = append(cmds, m.pageCmd(cs.id, 0))
	}
	m.buildBody()
	m.markRead()
	return tea.Batch(cmds...)
}

// onReread takes in the card read again after the owner's decision on it.
// Only the two things that come after a decision are taken: the verdicts and
// the mark that a run refused, which a decided card does not carry (it says
// it in its verdicts). The outcome, the cursor and everything the owner
// read stay. A card that is not decided there, or whose binding is not the
// one decided on, is left as it is.
func (m *Model) onReread(v openedMsg) {
	cs := m.cs
	if cs == nil || cs.id != v.id || !cs.decided || cs.endedLine != "" || v.err != nil ||
		!v.c.Decided() || v.c.BindingSHA256 != cs.c.BindingSHA256 {
		return
	}
	cs.c.Verdicts, cs.c.Refused = v.c.Verdicts, v.c.Refused
	m.buildBody()
}

// endCard closes the card to one line and drops what was typed.
func (m *Model) endCard(cs *cardState, reason string) {
	cs.endedLine = "this request ended: " + reason
	cs.field, cs.typed, cs.status = fieldNone, "", ""
	cs.code = ""
	cs.reading = card.Reading{}
	cs.pager = nil
	cs.cursor, cs.top = 0, 0
	m.buildBody()
}

// onPaged takes in a page of the whole path list.
func (m *Model) onPaged(v pagedMsg) {
	cs := m.cs
	if cs == nil || cs.id != v.id || v.seq != cs.pageSeq || cs.endedLine != "" {
		return
	}
	cs.pageLoading = false
	turn := cs.turned
	cs.turned = false
	jump := false
	d := cs.c.Diff
	switch {
	case v.err != nil:
		if code, ok := record.CodeOf(v.err); ok && code == record.CodeReviewChanged {
			cs.stale = true
			cs.status = reasonStale
		} else {
			cs.status = errLine("a page of the path list could not be read", v.err)
		}
	case d == nil || v.p.Kind != card.PagePaths || v.p.N != v.n || v.p.Pages < 1 || v.n >= v.p.Pages:
		cs.status = "the backend answered with a page that is not the one asked for"
	case v.p.ReviewDigest != d.ReviewDigest:
		cs.stale = true
		cs.status = reasonStale
	default:
		p := v.p
		cs.page = &p
		jump = turn
	}
	m.buildBody()
	// The reader starts at the top of a page turned to, before anything is
	// counted as shown, so that the rows of it are shown in order and its
	// end is not counted before it was drawn.
	if jump {
		for i, r := range cs.body {
			if r.ends&startPage != 0 {
				cs.cursor, cs.top = i, i
				m.clampCard()
				break
			}
		}
	}
	m.markRead()
}

// onDecided takes in the answer to Decide.
func (m *Model) onDecided(v decidedMsg) tea.Cmd {
	cs := m.cs
	if cs == nil || cs.id != v.id || v.seq != cs.decideSeq || !cs.sending {
		return nil
	}
	cs.sending, cs.status = false, ""
	if v.err != nil {
		cs.status = errLine("the decision was not accepted", v.err)
		cs.code = m.newCode()
		m.buildBody()
		return m.listCmd()
	}
	cs.decided = true
	cs.c.After = card.After{State: v.out.State, Line: v.out.Line, Held: v.out.Held}
	if v.out.Held {
		m.mode, m.cs = modeList, nil
		m.note = card.HeldLabel + ": it is back in the list"
		if v.out.Line != "" {
			m.note += " (" + v.out.Line + ")"
		}
		return m.listCmd()
	}
	cs.outcome = v.out.Line
	if cs.outcome == "" {
		cs.outcome = "the decision was recorded"
	}
	cs.code = ""
	// The outcome stands at the top of the card.
	cs.cursor, cs.top = 0, 0
	m.buildBody()
	// The card is read again for the verdict lines that follow a decision.
	return tea.Batch(m.listCmd(), m.rereadCmd(cs.id))
}

// approveReason says whether Approve is offered, and if not why, in one
// line.
func (m *Model) approveReason(now time.Time) (bool, string) {
	cs := m.cs
	switch {
	case cs.endedLine != "":
		return false, cs.endedLine
	case cs.sending:
		return false, reasonSending
	case cs.decided:
		return false, reasonDecided
	case !m.deciderKnown:
		return false, reasonChecking
	case !m.decider:
		return false, m.deciderReasonText()
	case cs.stale:
		return false, reasonStale
	case !m.sizeOK():
		return false, reasonSmall
	case cs.code == "":
		return false, reasonNoCode
	}
	return cs.c.MayApprove(cs.reading, now)
}

// clampCard keeps the cursor and the top of the card in range.
func (m *Model) clampCard() {
	cs := m.cs
	vh := m.cardRowsHeight()
	n := len(cs.body)
	cs.cursor = max(0, min(cs.cursor, n-1))
	if vh > 0 {
		if cs.cursor >= cs.top+vh {
			cs.top = cs.cursor - vh + 1
		}
		if cs.cursor < cs.top {
			cs.top = cs.cursor
		}
	}
	cs.top = max(0, min(cs.top, n-max(vh, 1)))
	if cs.cursor < cs.top {
		cs.cursor = cs.top
	}
}

// cardRowsHeight is the number of rows of the card that scroll.
func (m *Model) cardRowsHeight() int { return max(0, m.h-fixedRows) }

// markRead notes in the card's Reading what has been on the screen: a list
// or the path list whose last row was shown, a page of the whole path list
// whose last row was shown, the refs of a prune, and the "differs from"
// block once the screen or the cursor has moved past its last row: the block
// is passed by scrolling, and not by being on the first screen. The reader
// moves over every row, so a row shown now was shown on the way.
func (m *Model) markRead() {
	cs := m.cs
	if cs == nil || cs.endedLine != "" || !cs.live() {
		return
	}
	vh := m.cardRowsHeight()
	if vh == 0 {
		return
	}
	bottom := min(len(cs.body), cs.top+vh) - 1
	for i := 0; i < len(cs.body) && i < cs.cursor; i++ {
		// The cursor is never above the top of the screen, so a row above
		// the cursor was passed.
		if cs.body[i].ends&endDiffers != 0 {
			cs.reading.Differs = true
		}
	}
	for i := cs.top; i <= bottom; i++ {
		e := cs.body[i].ends
		if e == 0 {
			continue
		}
		if e&endPaths != 0 {
			cs.reading.Paths = true
		}
		for k := 0; k < card.NumLists; k++ {
			if e&(endList0<<uint(k)) != 0 {
				cs.reading.Lists[k] = true
			}
		}
		if e&endRefs != 0 {
			cs.reading.Refs = true
		}

		if e&endPage != 0 && cs.page != nil {
			cs.reading.ReadPage(*cs.page)
		}
	}
}

// moveCard moves the cursor of the card by n rows and the screen with it.
func (m *Model) moveCard(n int) {
	cs := m.cs
	cs.cursor += n
	m.clampCard()
	m.markRead()
}

// onKey handles a key press.
func (m *Model) onKey(k tea.KeyPressMsg) (tea.Cmd, Action) {
	name := keyName(k)
	if name == "ctrl+c" {
		if m.cs != nil {
			m.cs.field, m.cs.typed = fieldNone, ""
		}
		return nil, Action{Kind: Quit}
	}
	switch m.mode {
	case modeOpening:
		if name == "esc" {
			m.mode = modeList
			m.openSeq++
		}
		return nil, Action{}
	case modeCard:
		if m.cs.field != fieldNone {
			return m.fieldKey(k, name)
		}
		return m.cardKey(name)
	}
	return m.listKey(name)
}

// cardKey handles a key on the card when no field is open.
func (m *Model) cardKey(name string) (tea.Cmd, Action) {
	cs := m.cs
	cs.status = ""
	if cs.sending {
		return nil, Action{}
	}
	vh := max(1, m.cardRowsHeight()-1)
	switch name {
	case "esc":
		m.mode, m.cs = modeList, nil
		return m.listCmd(), Action{}
	case "j", "down":
		m.moveCard(1)
	case "k", "up":
		m.moveCard(-1)
	case "pgdown":
		m.moveCard(vh)
	case "pgup":
		m.moveCard(-vh)
	}
	if cs.endedLine != "" {
		return nil, Action{}
	}
	switch name {
	case "space":
		m.signOff()
	case "d":
		return m.openPager(), Action{}
	case "a":
		m.startConfirm()
	case "x":
		m.startDeny()
	case "l":
		return nil, Action{Kind: Lead, ID: cs.id, Project: cs.c.Title.Project}
	case "]":
		return m.turnPage(1), Action{}
	case "[":
		return m.turnPage(-1), Action{}
	}
	return nil, Action{}
}

// readOnlyReason returns why signing off, approving and denying are not
// offered because of who may decide, or "" when they are.
func (m *Model) readOnlyReason() string {
	switch {
	case !m.deciderKnown:
		return reasonChecking
	case !m.decider:
		return "read-only: " + m.deciderReasonText()
	}
	return ""
}

// signOff signs off the flagged path under the cursor, or takes the
// sign-off back.
func (m *Model) signOff() {
	cs := m.cs
	if why := m.readOnlyReason(); why != "" {
		cs.status = why
		return
	}
	if !cs.live() {
		cs.status = "sign-off is off: this request ended or was decided"
		return
	}
	if cs.cursor < 0 || cs.cursor >= len(cs.body) || cs.body[cs.cursor].item == 0 || len(cs.c.Flagged) == 0 {
		cs.status = "sign-off is off: the cursor is not on a flagged path"
		return
	}
	i := cs.body[cs.cursor].item - 1
	if i >= len(cs.c.Flagged) {
		return
	}
	cs.reading.Toggle(cs.c.Flagged[i].SignOff())
	m.buildBody()
	m.markRead()
}

// openPager opens the pager on the full patch.
func (m *Model) openPager() tea.Cmd {
	cs := m.cs
	d := cs.c.Diff
	if d == nil {
		cs.status = "diff is off: this card has no diff"
		return nil
	}
	pg := pager.New(pageSource{b: m.b, id: cs.id, digest: d.ReviewDigest}, pager.Options{
		Colors: pager.Colors{
			Fg: m.o.Colors.Fg, Bg: m.o.Colors.Bg, Dim: m.o.Colors.Dim, Warn: m.o.Colors.Warn,
		},
		GraphemeWidths: m.o.GraphemeWidths,
	})
	cs.pager = pg
	return tea.Batch(pg.SetSize(m.w, m.h), pg.Init())
}

// turnPage reads the next or the previous page of the whole path list.
func (m *Model) turnPage(by int) tea.Cmd {
	cs := m.cs
	d := cs.c.Diff
	if d == nil || !d.Truncated {
		cs.status = "pages are off: the path list on this card is whole"
		return nil
	}
	if cs.pageLoading {
		cs.status = "a page is being read"
		return nil
	}
	n := 0
	if cs.page != nil {
		n = cs.page.N + by
		if n < 0 || n >= cs.page.Pages {
			cs.status = "there is no page there"
			return nil
		}
	}
	cs.pageLoading, cs.turned = true, true
	cmd := m.pageCmd(cs.id, n)
	m.buildBody()
	return cmd
}

// mayDecide is the one rule that decides whether a decision of the kind
// (fieldConfirm for an approve, fieldDeny for a deny) may leave the view. It
// is asked when a field opens and again when Enter is pressed. It returns the
// line that says why not.
func (m *Model) mayDecide(kind fieldKind, now time.Time) (bool, string) {
	cs := m.cs
	if why := m.readOnlyReason(); why != "" {
		return false, why
	}
	if kind == fieldConfirm {
		if ok, why := m.approveReason(now); !ok {
			return false, "approve is off: " + why
		}
		return true, ""
	}
	switch {
	case !m.sizeOK():
		return false, "deny is off: " + reasonSmall
	case cs.sending, cs.decided, cs.endedLine != "":
		return false, "deny is off: this request ended, was decided or is being sent"
	}
	if _, ok, why := cs.c.Deny("", now); !ok {
		return false, "deny is off: " + why
	}
	return true, ""
}

// startConfirm opens the confirm field when the card may be approved, and
// says why not in one line when it may not.
func (m *Model) startConfirm() {
	cs := m.cs
	now := m.now()
	if ok, why := m.mayDecide(fieldConfirm, now); !ok {
		cs.status = why
		return
	}
	cs.field, cs.typed, cs.lastKey = fieldConfirm, "", now
}

// startDeny opens the deny reason.
func (m *Model) startDeny() {
	cs := m.cs
	if ok, why := m.mayDecide(fieldDeny, m.now()); !ok {
		cs.status = why
		return
	}
	cs.field, cs.typed = fieldDeny, ""
}

// refuse clears the confirm field and says why.
func (m *Model) refuse(why string) {
	cs := m.cs
	cs.typed = ""
	cs.status = why
}

// fieldKey handles a key while a field is open. Every printable key goes to
// the field. Only Enter, Esc and ctrl+c act.
func (m *Model) fieldKey(k tea.KeyPressMsg, name string) (tea.Cmd, Action) {
	cs := m.cs
	cs.status = ""
	switch name {
	case "esc":
		cs.field, cs.typed = fieldNone, ""
		return nil, Action{}
	case "enter":
		return m.submit()
	}
	if cs.field == fieldDeny {
		m.denyKey(k, name)
		return nil, Action{}
	}
	// The confirm field takes a single printable key press, typed by a
	// person, after the card has been open long enough; it refuses and
	// clears on anything else.
	now := m.now()
	text, ok := singlePrintable(k)
	switch {
	case !ok:
		m.refuse("input that was not typed was refused, and the field was cleared")
	case now.Sub(cs.openedAt) < card.OpenDelay:
		cs.lastKey = now
		m.refuse("input in the first 400 ms of a card was refused, and the field was cleared")
	case !cs.lastKey.IsZero() && now.Sub(cs.lastKey) < burstGap:
		cs.lastKey = now
		m.refuse("keys came faster than typing and were refused, and the field was cleared")
	case len(cs.typed)+len(text) > codeLen:
		cs.lastKey = now
		m.refuse("that is longer than the code, and the field was cleared")
	default:
		cs.lastKey = now
		cs.typed += strings.ToUpper(text)
	}
	return nil, Action{}
}

// denyKey edits the reason of a deny.
func (m *Model) denyKey(k tea.KeyPressMsg, name string) {
	cs := m.cs
	if name == "backspace" {
		if cs.typed != "" {
			r := []rune(cs.typed)
			cs.typed = string(r[:len(r)-1])
		}
		return
	}
	text, ok := singlePrintable(k)
	if !ok {
		return
	}
	if len(cs.typed)+len(text) > record.MaxReasonBytes {
		cs.status = card.ReasonTooLong
		return
	}
	cs.typed += text
}

// submit handles Enter in a field.
func (m *Model) submit() (tea.Cmd, Action) {
	cs := m.cs
	now := m.now()
	kind, typed := cs.field, cs.typed
	cs.field, cs.typed = fieldNone, ""
	var d card.Decision
	// The rules for who may decide, and in which window, are asked again
	// now: the window, the backend's answer or the card may have changed
	// since the field opened. What was typed is dropped.
	if ok, why := m.mayDecide(kind, now); !ok {
		cs.status = why
		return nil, Action{}
	}
	switch kind {
	case fieldConfirm:
		if typed != cs.code {
			// A wrong code makes a new one.
			cs.code = m.newCode()
			cs.status = "that is not the code: a new code is in the title"
			if cs.code == "" {
				cs.status = reasonNoCode
			} else {
				cs.field, cs.lastKey = fieldConfirm, now
			}
			return nil, Action{}
		}
		dec, ok, why := cs.c.Approve(cs.reading, now)
		if !ok {
			cs.status = "approve is off: " + why
			return nil, Action{}
		}
		d = dec
	case fieldDeny:
		dec, ok, why := cs.c.Deny(typed, now)
		if !ok {
			cs.status = "deny is off: " + why
			return nil, Action{}
		}
		d = dec
	}
	// The decision is handed over once; the code is gone.
	cs.sending = true
	cs.code = ""
	cs.status = "sending"
	return m.decideCmd(d), Action{}
}
