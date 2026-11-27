package leadctl

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// Limits of the view.
const (
	// maxLeads is the most leads of a listing the view takes in.
	maxLeads = 32
	// maxRole is the longest role the view passes back to the backend.
	maxRole = 128
	// maxBody is the longest message, in bytes: the daemon's admission refuses
	// a longer body with body-too-long (leadcanary.MaxBodyBytes, which a
	// test reads from its source, as this package imports nothing of the
	// repository). It is less than leadwire.MaxBody, which is only the cap
	// of the wire.
	maxBody = 2048
	// maxPath is the longest folder or ticket file the view hands to the
	// parent.
	maxPath = 4096
)

// Model is the leads view. Make one with New, start it with Init, tell it
// its size with SetSize, hand it every message the parent receives while it
// is open (Update) and draw it with Draw. A Model is used from one goroutine,
// as Bubble Tea uses a model.
type Model struct {
	b      Backend
	o      Options
	random io.Reader
	after  func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
	poll   time.Duration
	w, h   int

	// The list, as the view took it in: names are cut to a safe subset.
	leads    []Lead
	have     bool
	listErr  bool
	listSeq  int
	running  int
	capacity int
	leftOut  int
	cursor   int
	top      int

	// seq numbers the commands. A busy role awaits a receipt even if a
	// listing has invalidated the original request.
	seq  int
	busy map[string]*pendingCommand
	// status is the receipt shown on the last line, nil when there is none.
	// note is one line of the view's own about the last key or answer, and
	// takes the place of status while it is not empty.
	status *Receipt
	note   string
	// statusTarget is the identity the status receipt answers, and statusListed
	// whether the list was read again since the answer: only then does a
	// row say whether the daemon holds that message spent (see heldFor).
	statusTarget Identity
	statusListed bool
	// unconfirmed holds the "role epoch" pairs for which a message was sent
	// and has no final answer: no second message is made for them.
	unconfirmed map[Identity]bool
	// delivered holds, for each "role epoch" pair, the holder's answer to a
	// message this view sent or read the receipt of.
	delivered map[Identity]Delivery

	// confirm is the open confirmation of a stop, edit the open message
	// editor.
	confirm *Lead
	edit    *editState

	ticking bool
}

// A command can still be queued when a refreshed listing loses its target.
// Only the command goroutine reads invalid outside the model's Update.
type pendingCommand struct {
	seq     int
	target  Identity
	op      Op
	invalid atomic.Bool
}

type editState struct {
	Lead
	text string
}

// New returns a view that reads from o.Backend. It has read nothing yet:
// Init returns the command that reads the list.
func New(o Options) *Model {
	m := &Model{b: o.Backend, o: o, random: o.Random, after: o.After, poll: o.Poll,
		busy: map[string]*pendingCommand{}, unconfirmed: map[Identity]bool{}, delivered: map[Identity]Delivery{}}
	if m.after == nil {
		m.after = tea.Tick
	}
	if m.poll <= 0 {
		m.poll = DefaultPoll
	}
	return m
}

// Init returns the command that reads the list.
func (m *Model) Init() tea.Cmd { return m.listCmd() }

// SetSize tells the view the size of its region.
func (m *Model) SetSize(width, height int) {
	m.w, m.h = max(0, width), max(0, height)
	m.clamp()
}

// FieldOpen reports whether the message editor or the stop confirmation is
// open. While one is, every key is the field's.
func (m *Model) FieldOpen() bool { return m.edit != nil || m.confirm != nil }

// The messages of the view's own commands. Each carries its owner, so that
// the answer to a command of another view is left alone.
type (
	listedMsg struct {
		owner *Model
		seq   int
		l     Listing
		err   error
	}
	doneMsg struct {
		owner *Model
		seq   int
		role  string
		key   string
		op    Op
		epoch uint64
		// writer is the attach role of a ticket.
		writer  bool
		skipped bool
		r       Receipt
		err     error
	}
	tickMsg struct{ owner *Model }
)

// Update handles a message. It returns the command to run and what the
// parent is asked to do.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, Action) {
	switch v := msg.(type) {
	case listedMsg:
		if v.owner == m {
			return m.onListed(v), Action{}
		}
	case doneMsg:
		if v.owner == m {
			return m.onDone(v)
		}
	case tickMsg:
		if v.owner == m {
			return m.onTick(), Action{}
		}
	case tea.KeyPressMsg:
		return m.onKey(v)
	case tea.PasteMsg:
		m.onPaste(v.Content)
	}
	return nil, Action{}
}

// listCmd reads the list from a command.
func (m *Model) listCmd() tea.Cmd {
	m.listSeq++
	seq, b := m.listSeq, m.b
	return func() tea.Msg {
		l, err := b.List()
		return listedMsg{owner: m, seq: seq, l: l, err: err}
	}
}

// Refresh returns the command that reads the list again.
func (m *Model) Refresh() tea.Cmd { return m.listCmd() }

// tickCmd schedules the next reading of the list while a receipt is in
// progress. At most one is scheduled at a time.
func (m *Model) tickCmd() tea.Cmd {
	if m.ticking || !m.needsPoll() {
		return nil
	}
	m.ticking = true
	return m.after(m.poll, func(time.Time) tea.Msg { return tickMsg{owner: m} })
}

func (m *Model) onTick() tea.Cmd {
	m.ticking = false
	if !m.needsPoll() {
		return nil
	}
	return m.listCmd()
}

// needsPoll says whether the list is to be read again by itself: a receipt in
// progress is shown, or a listed lead is in a state that changes without a
// key (see changesByItself).
func (m *Model) needsPoll() bool {
	if m.status != nil && m.status.Status == InProgress {
		return true
	}
	for _, l := range m.leads {
		if l.Offline {
			continue
		}
		if l.Last != nil && l.Last.Status == InProgress {
			return true
		}
		if changesByItself(l.State) {
			return true
		}
	}
	return false
}

// changesByItself says whether a lead in this registry state is moving on
// without a command: starting turns ready (or stopped or ended) when its holder
// is observed, and stopping turns stopped. unknown is not one of them: the view
// does not read the list for it by a timer, so a lead whose launch the daemon
// settles after the window shows it at the next reading the owner asks for.
func changesByItself(state string) bool { return state == "starting" || state == "stopping" }

// MarkOffline retains the last rows while revoking the selection, open
// fields and pending commands. Discard any list read begun before the outage.
func (m *Model) MarkOffline() {
	m.listSeq++
	m.listErr = true
	for i := range m.leads {
		m.leads[i].Offline = true
	}
	for _, pending := range m.busy {
		pending.invalid.Store(true)
	}
	m.cursor, m.edit, m.confirm, m.note = -1, nil, nil, ""
}

// onListed takes in the answer to a list request. An answer to a request that
// a later one replaced is left alone.
func (m *Model) onListed(v listedMsg) tea.Cmd {
	if v.seq != m.listSeq {
		return nil
	}
	if v.err != nil {
		m.MarkOffline()
		return nil
	}
	keep, selected := m.under()
	firstList := !m.have
	m.listErr, m.have = false, true
	m.statusListed = m.status != nil
	m.running, m.capacity = max(0, v.l.Running), max(0, v.l.MaxRunning)
	stale := m.leads[:0]
	for _, l := range m.leads {
		if slices.Contains(v.l.OfflineHomes, l.HomeID) {
			l.Offline = true
			stale = append(stale, l)
		}
	}
	m.leads = nil
	m.leftOut = max(0, v.l.LeftOut)
	// Scratch operations address a role without its epoch. Qualified rows
	// instead have distinct addresses even when their roles are identical.
	roles := make(map[Identity]int)
	for _, l := range v.l.Leads {
		if slices.Contains(v.l.OfflineHomes, l.HomeID) {
			continue
		}
		if !l.validAddress() {
			m.leftOut++
			continue
		}
		if roles[l.address()] > 0 {
			roles[l.address()]++
			m.leftOut++
			continue
		}
		if len(m.leads) == maxLeads-len(stale) {
			m.leftOut++
			continue
		}
		c := Lead{
			HomeID: l.HomeID, ProjectID: l.ProjectID, ID: l.ID, ConversationID: l.ConversationID, Offline: l.Offline,
			Key: safeID(l.Key, 64), Role: l.Role, Defined: l.Defined,
			Provision: safeID(l.Provision, 16), State: safeID(l.State, 16), StateReason: safeID(l.StateReason, 64),
			Epoch: l.Epoch, Unit: safeID(l.Unit, 96), Inject: l.Inject, MessageSpent: l.MessageSpent, MessageResult: l.MessageResult,
		}
		if l.Last != nil {
			last := *l.Last
			c.Last = &last
		}
		m.leads = append(m.leads, c)
		roles[l.address()] = 1
	}
	listed := len(m.leads)
	m.leads = slices.DeleteFunc(m.leads, func(l Lead) bool { return roles[l.address()] > 1 })
	m.leftOut += listed - len(m.leads)
	m.leads = append(m.leads, stale...)
	m.cursor = -1
	if firstList {
		m.move(1)
	}
	if selected {
		m.cursor = m.bindingIndex(keep.identity())
		if m.cursor < 0 {
			m.edit, m.confirm = nil, nil
			m.note = "Selection cleared. Choose a lead with j or k."
		} else {
			row := m.leads[m.cursor]
			if m.edit != nil {
				m.edit.Lead = row
			}
			if m.confirm != nil {
				*m.confirm = row
			}
		}
	}
	for role, pending := range m.busy {
		if i := m.bindingIndex(pending.target); i < 0 || m.leads[i].Role != role {
			pending.invalid.Store(true)
		}
	}
	// Keep message evidence (including an unknown outcome) over an outage,
	// until a successful read removes or replaces the target.
	present := make(map[Identity]bool, len(m.leads))
	for _, l := range m.leads {
		present[l.identity()] = true
	}
	for k := range m.delivered {
		if !present[k] {
			delete(m.delivered, k)
		}
	}
	for k := range m.unconfirmed {
		if !present[k] {
			delete(m.unconfirmed, k)
		}
	}
	// The status line follows the receipt it shows: the list holds the
	// newer state of the same command.
	if m.status != nil && m.status.CommandID != "" {
		for _, l := range m.leads {
			if l.identity() == m.statusTarget && l.Last != nil && l.Last.CommandID == m.status.CommandID {
				r := *l.Last
				// The list's summary is never "earlier"; the line keeps
				// what it said of the press.
				r.Earlier = m.status.Earlier
				keepWords(&r, *m.status)
				m.status = &r
			}
		}
	}
	m.clamp()
	return m.tickCmd()
}

// keepWords gives the list's receipt the words of the press's receipt that the
// list's lacks. The daemon's list carries a summary of each lead's last command
// without its result, so a done message would otherwise lose what the holder
// answered (outcome, reason, written) and a done ticket where to attach
// (folder, ticket file). A list receipt that has any of the words keeps its
// own, and words are taken only from a receipt that is done as well. The
// summary the daemon lists carries no epoch either (it is 0), and the words
// belong to the epoch the press was answered for: the list's receipt takes it,
// so that the line is not read against the epoch the lead has moved to since.
func keepWords(list *Receipt, press Receipt) {
	if list.Status != Done || press.Status != Done || list.Op != press.Op {
		return
	}
	if list.Epoch == 0 {
		list.Epoch = press.Epoch
	}
	if list.Outcome == "" && list.Reason == "" && list.Written == "" {
		list.Outcome, list.Reason, list.Written = press.Outcome, press.Reason, press.Written
	}
	if list.Folder == "" && list.TicketFile == "" {
		list.Folder, list.TicketFile = press.Folder, press.TicketFile
	}
}

// address is the routing identity within a listing. The scratch backend
// routes only by role, so duplicate scratch roles must still be refused.
func (l Lead) address() Identity {
	if l.HomeID == "" {
		return Identity{Lead: l.Role}
	}
	id := l.identity()
	id.Epoch = 0
	return id
}

func (l Lead) validAddress() bool {
	if len(l.ProjectID) > maxRole || len(l.ID) > maxRole || len(l.ConversationID) > maxRole || len(l.Role) > maxRole {
		return false
	}
	if l.HomeID == "" {
		return l.Role != ""
	}
	return len(l.HomeID) <= maxRole && l.ProjectID != "" && l.ID != ""
}

func (l Lead) identity() Identity {
	id := Identity{Home: l.HomeID, Project: l.ProjectID, Lead: l.ID, Conversation: l.ConversationID, Epoch: l.Epoch}
	if id.Home == "" && id.Lead == "" {
		id.Lead = l.Role
	}
	return id
}

func (m *Model) bindingIndex(id Identity) int {
	found := -1
	for i, l := range m.leads {
		if !l.Offline && l.identity() == id {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}

// clamp keeps the cursor on a lead and the lead on the screen.
func (m *Model) clamp() {
	m.cursor = min(m.cursor, len(m.leads)-1)
	vh := m.tableRows()
	if vh > 0 {
		if m.cursor >= m.top+vh {
			m.top = m.cursor - vh + 1
		}
		if m.cursor < m.top {
			m.top = m.cursor
		}
	}
	m.top = max(0, min(m.top, len(m.leads)-max(vh, 1)))
}

// current returns the lead under the cursor.
func (m *Model) current() (Lead, bool) {
	if m.cursor < 0 || m.cursor >= len(m.leads) {
		m.note = noRowLine
		return Lead{}, false
	}
	return m.leads[m.cursor], true
}

// keyName returns the name of a key press for the view's own keys: "a",
// "space", "enter", "esc", "ctrl+c" and so on. A key that carries text has
// the name of that text only when it is one printable character with no
// modifier but shift, and no name otherwise, so that a text that spells a
// key's name is not that key.
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

const lockMods = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

// singlePrintable returns the text of a key press that is exactly one
// printable character typed with no modifier but shift.
func singlePrintable(k tea.KeyPressMsg) (string, bool) {
	if k.Mod&^(tea.ModShift|lockMods) != 0 || k.Text == "" {
		return "", false
	}
	r, n := utf8.DecodeRuneInString(k.Text)
	if n != len(k.Text) || !okRune(r) {
		return "", false
	}
	return k.Text, true
}

// okRune says whether r may be typed into the editor: a printable character
// that takes one or two cells.
func okRune(r rune) bool {
	if r == utf8.RuneError || !unicode.IsPrint(r) || unicode.Is(unicode.M, r) || unicode.Is(unicode.Bidi_Control, r) ||
		unicode.Is(unicode.Join_Control, r) || unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) {
		return false
	}
	return r != 0x2800 && cellWidth(r) > 0
}

func (m *Model) onKey(k tea.KeyPressMsg) (tea.Cmd, Action) {
	name := keyName(k)
	if k.String() == "ctrl+c" {
		m.edit, m.confirm = nil, nil
		return nil, Action{Kind: Quit}
	}
	if m.edit != nil {
		return m.editKey(k, name)
	}
	if m.confirm != nil {
		return m.confirmKey(name)
	}
	m.note = ""
	switch name {
	case "esc":
		return nil, Action{Kind: Close}
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "r":
		return m.listCmd(), Action{}
	case "d":
		return m.simple(OpDefine, func(b Backend, role string, _ uint64) (Receipt, error) { return b.Define(role) }), Action{}
	case "s":
		return m.simple(OpStart, func(b Backend, role string, _ uint64) (Receipt, error) { return b.Start(role) }), Action{}
	case "x":
		m.startStop()
	case "m":
		m.startMessage()
	case "w":
		return m.ticket(true), Action{}
	case "v":
		return m.ticket(false), Action{}
	}
	return nil, Action{}
}

func (m *Model) move(delta int) {
	previous, ok := m.under()
	before := m.cursor
	start := m.cursor + delta
	if m.cursor < 0 {
		start = 0
		if delta < 0 {
			start = len(m.leads) - 1
		}
	}
	for i := start; i >= 0 && i < len(m.leads); i += delta {
		if !m.leads[i].Offline {
			m.cursor = i
			break
		}
	}
	m.clamp()
	if ok && m.cursor != before {
		if pending := m.busy[previous.Role]; pending != nil && pending.op == OpTicket {
			pending.invalid.Store(true)
		}
	}
}

// admit checks what every command needs: a lead under the cursor and no
// command of that lead still waiting for its answer.
func (m *Model) admit() (Lead, bool) {
	l, ok := m.current()
	if !ok {
		return Lead{}, false
	}
	if l.Offline || l.HomeID != "" {
		m.note = "No scratch commands for this directory row."
		return Lead{}, false
	}
	if _, busy := m.busy[l.Role]; busy {
		m.note = busyLine
		return Lead{}, false
	}
	return l, true
}

// simple sends define or start for the lead under the cursor.
func (m *Model) simple(op Op, call func(Backend, string, uint64) (Receipt, error)) tea.Cmd {
	l, ok := m.admit()
	if !ok {
		return nil
	}
	return m.command(l, op, 0, false, func(b Backend) (Receipt, error) { return call(b, l.Role, 0) })
}

// command makes the command that calls the backend once, and notes that the
// lead has a command whose answer is awaited.
func (m *Model) command(l Lead, op Op, epoch uint64, writer bool, call func(Backend) (Receipt, error)) tea.Cmd {
	m.seq++
	seq, b, role, key := m.seq, m.b, l.Role, l.Key
	pending := &pendingCommand{seq: seq, target: l.identity(), op: op}
	m.busy[role] = pending
	m.status, m.statusListed, m.note = nil, false, "sending: "+opWords(op)+" for "+key
	return func() tea.Msg {
		msg := doneMsg{owner: m, seq: seq, role: role, key: key, op: op, epoch: epoch, writer: writer}
		if pending.invalid.Load() {
			msg.skipped = true
			return msg
		}
		msg.r, msg.err = call(b)
		return msg
	}
}

// startStop opens the confirmation of a stop.
func (m *Model) startStop() {
	l, ok := m.admit()
	if !ok {
		return
	}
	m.confirm = &l
}

// confirmKey handles a key while the confirmation is open: y stops, any
// other key keeps the lead running.
func (m *Model) confirmKey(name string) (tea.Cmd, Action) {
	c := m.confirm
	m.confirm = nil
	if name != "y" {
		m.note = "stop cancelled"
		return nil, Action{}
	}
	if _, busy := m.busy[c.Role]; busy {
		m.note = busyLine
		return nil, Action{}
	}
	return m.command(*c, OpStop, c.Epoch, false,
		func(b Backend) (Receipt, error) { return b.Stop(c.Role, c.Epoch) }), Action{}
}

// startMessage opens the message editor for the lead under the cursor, for
// the epoch the row shows.
func (m *Model) startMessage() {
	l, ok := m.admit()
	if !ok {
		return
	}
	switch {
	case l.Epoch == 0:
		m.note = notRunning
	case !l.Inject:
		m.note = injectOffLine
	case l.MessageSpent:
		m.note = spentLine
	case m.unconfirmed[l.identity()]:
		m.note = unconfirmed
	default:
		m.edit = &editState{Lead: l}
	}
}

// editKey handles a key while the editor is open. Printable keys are typed;
// Enter sends the message once, Esc drops it, ctrl+u clears it.
func (m *Model) editKey(k tea.KeyPressMsg, name string) (tea.Cmd, Action) {
	e := m.edit
	m.note = ""
	switch name {
	case "esc":
		m.edit = nil
		return nil, Action{}
	case "enter":
		return m.submit(), Action{}
	case "backspace":
		if e.text != "" {
			_, n := utf8.DecodeLastRuneInString(e.text)
			e.text = e.text[:len(e.text)-n]
		}
		return nil, Action{}
	case "ctrl+u":
		e.text = ""
		return nil, Action{}
	}
	text, ok := singlePrintable(k)
	if !ok {
		return nil, Action{}
	}
	if len(e.text)+len(text) > maxBody {
		m.note = tooLongLine
		return nil, Action{}
	}
	e.text += text
	return nil, Action{}
}

// onPaste takes a paste into the editor. A line break becomes a space. A
// paste with anything that cannot be sent is taken whole or not at all.
func (m *Model) onPaste(s string) {
	e := m.edit
	if e == nil {
		return
	}
	m.note = ""
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	for _, r := range s {
		if !okRune(r) {
			m.note = pasteLine
			return
		}
	}
	if len(e.text)+len(s) > maxBody {
		m.note = tooLongLine
		return
	}
	e.text += s
}

// submit sends the message once. It makes the message id, notes the pair
// before the call so that nothing else is made for the epoch until the answer
// is final, and calls the backend from a command.
func (m *Model) submit() tea.Cmd {
	e := m.edit
	m.edit = nil
	if strings.TrimSpace(e.text) == "" {
		m.note = blankLine
		return nil
	}
	if _, busy := m.busy[e.Role]; busy {
		m.note = busyLine
		return nil
	}
	id, ok := m.messageID()
	if !ok {
		m.note = noRandomLine
		return nil
	}
	m.unconfirmed[e.identity()] = true
	// What the view kept of an earlier message of the epoch is not this one's:
	// a message whose answer is lost must not read as the earlier one.
	delete(m.delivered, e.identity())
	role, epoch, body := e.Role, e.Epoch, e.text
	return m.command(e.Lead, OpInject, epoch, false,
		func(b Backend) (Receipt, error) { return b.Send(role, epoch, id, body) })
}

// messageID makes a message id from the random source: 24 characters of
// lowercase hex, which the holder's grammar takes.
func (m *Model) messageID() (string, bool) {
	r := m.random
	if r == nil {
		r = rand.Reader
	}
	var b [12]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(b[:]), true
}

// ticket asks for an attach ticket for the lead under the cursor, in the
// epoch the row shows.
func (m *Model) ticket(writer bool) tea.Cmd {
	l, ok := m.admit()
	if !ok {
		return nil
	}
	if l.Epoch == 0 {
		m.note = notRunning
		return nil
	}
	epoch := l.Epoch
	return m.command(l, OpTicket, epoch, writer, func(b Backend) (Receipt, error) { return b.Ticket(l.Role, epoch, writer) })
}

// onDone takes in the answer to a command.
func (m *Model) onDone(v doneMsg) (tea.Cmd, Action) {
	pending := m.busy[v.role]
	if pending == nil || pending.seq != v.seq {
		return nil, Action{}
	}
	delete(m.busy, v.role)
	if v.skipped {
		if v.op == OpInject {
			delete(m.unconfirmed, pending.target)
		}
		m.note = "Selection changed before the command was sent."
		return m.listCmd(), Action{}
	}
	m.note, m.status, m.statusListed = "", nil, false
	cmds := []tea.Cmd{m.listCmd()}
	if v.err != nil {
		m.note = lostAnswer
		if v.op == OpInject {
			m.note = lostInject
		}
		return tea.Batch(cmds...), Action{}
	}
	r := v.r
	// The request says what the command was; the answer only what became of
	// it. A receipt for another operation is not the answer to this one.
	if r.Op != v.op {
		r.Op = v.op
	}
	m.status, m.statusTarget = &r, pending.target
	if pending.invalid.Load() {
		// Keep the receipt of the dispatched command. Its ticket or message
		// result cannot be used for a newly selected binding.
		return tea.Batch(cmds...), Action{}
	}
	if r.Earlier {
		// The answer to an earlier command is shown as that and settles
		// nothing for this press: no attach with a ticket that may be
		// spent, and the epoch's message stays unconfirmed until the list
		// shows otherwise.
		return tea.Batch(cmds...), Action{}
	}
	if v.op == OpInject {
		m.noteDelivery(pending.target, r)
	}
	if v.op == OpInject && clearOfMessage(r) {
		delete(m.unconfirmed, pending.target)
	}
	if v.op == OpTicket && r.Status == Done {
		if act, ok := m.attach(v, r); ok {
			return tea.Batch(cmds...), act
		}
		m.status, m.note = nil, unusableAns
	}
	return tea.Batch(cmds...), Action{}
}

// noteDelivery keeps the holder's answer of a done message, for the epoch the
// receipt names (the one the press was for when it names none), so that the
// MSG column says what became of it.
func (m *Model) noteDelivery(target Identity, r Receipt) {
	if r.Status != Done {
		return
	}
	epoch := r.Epoch
	if epoch == 0 {
		epoch = target.Epoch
	}
	if epoch == 0 || epoch != target.Epoch {
		return
	}
	m.delivered[target] = Delivery{Outcome: r.Outcome, Reason: r.Reason, Written: r.Written}
}

// deliveryOf returns what the view knows of the holder's answer to the
// lead's current epoch's message: the adapter's, else the one it kept.
func (m *Model) deliveryOf(l Lead) Delivery {
	if l.HomeID != "" || l.MessageResult != (Delivery{}) {
		return l.MessageResult
	}
	return m.delivered[l.identity()]
}

// under returns the lead under the cursor without a note when there is none.
func (m *Model) under() (Lead, bool) {
	if m.cursor < 0 || m.cursor >= len(m.leads) {
		return Lead{}, false
	}
	return m.leads[m.cursor], true
}

// clearOfMessage says whether the receipt of a message says that nothing was
// sent, so that the epoch's message may be tried again.
func clearOfMessage(r Receipt) bool {
	switch r.Status {
	case Refused:
		// The daemon no longer holds the command's id: what became of an
		// earlier send is not said, so the epoch is not clear.
		return r.Code != "conflict"
	case Unavailable:
		return true
	case Done:
		return r.Outcome == OutcomeNotNow || r.Outcome == OutcomeRefused
	}
	return false
}

// attach makes the parent's request from a done ticket, when its folder and
// ticket file are usable.
func (m *Model) attach(v doneMsg, r Receipt) (Action, bool) {
	if r.Epoch != 0 && r.Epoch != v.epoch || !usablePath(r.Folder) || !usablePath(r.TicketFile) {
		return Action{}, false
	}
	return Action{Kind: Attach, Attach: AttachRequest{
		Role: v.role, Epoch: v.epoch, Writer: v.writer, Folder: r.Folder, TicketFile: r.TicketFile,
	}}, true
}

// usablePath says whether p can be handed to the parent: not empty, bounded,
// valid UTF-8 and with no control character.
func usablePath(p string) bool {
	if p == "" || len(p) > maxPath || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
