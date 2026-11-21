package leadctl

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
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

	// seq numbers the commands; busy holds, for each role, the number of the
	// command whose answer is awaited.
	seq  int
	busy map[string]int
	// status is the receipt shown on the last line, nil when there is none.
	// note is one line of the view's own about the last key or answer, and
	// takes the place of status while it is not empty.
	status *Receipt
	note   string
	// statusRole is the role the status receipt answers, and statusListed
	// whether the list was read again since the answer: only then does a
	// row say whether the daemon holds that message spent (see heldFor).
	statusRole   string
	statusListed bool
	// unconfirmed holds the "role epoch" pairs for which a message was sent
	// and has no final answer: no second message is made for them.
	unconfirmed map[string]bool
	// delivered holds, for each "role epoch" pair, the holder's answer to a
	// message this view sent or read the receipt of.
	delivered map[string]Delivery

	// confirm is the open confirmation of a stop, edit the open message
	// editor.
	confirm *confirmState
	edit    *editState

	ticking bool
}

type confirmState struct {
	role, key string
	epoch     uint64
}

type editState struct {
	role, key string
	epoch     uint64
	text      string
}

// New returns a view that reads from o.Backend. It has read nothing yet:
// Init returns the command that reads the list.
func New(o Options) *Model {
	m := &Model{b: o.Backend, o: o, random: o.Random, after: o.After, poll: o.Poll,
		busy: map[string]int{}, unconfirmed: map[string]bool{}, delivered: map[string]Delivery{}}
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
		writer bool
		r      Receipt
		err    error
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

// needsPoll says whether a receipt in progress is shown.
func (m *Model) needsPoll() bool {
	if m.status != nil && m.status.Status == InProgress {
		return true
	}
	for _, l := range m.leads {
		if l.Last != nil && l.Last.Status == InProgress {
			return true
		}
	}
	return false
}

// onListed takes in the answer to a list request. An answer to a request that
// a later one replaced is left alone.
func (m *Model) onListed(v listedMsg) tea.Cmd {
	if v.seq != m.listSeq {
		return nil
	}
	if v.err != nil {
		m.listErr = true
		return nil
	}
	var keep string
	if m.cursor >= 0 && m.cursor < len(m.leads) {
		keep = m.leads[m.cursor].Role
	}
	m.listErr, m.have = false, true
	m.statusListed = m.statusRole != ""
	m.running, m.capacity = max(0, v.l.Running), max(0, v.l.MaxRunning)
	m.leads = m.leads[:0]
	m.leftOut = max(0, v.l.LeftOut)
	for _, l := range v.l.Leads {
		if len(m.leads) == maxLeads || l.Role == "" || len(l.Role) > maxRole {
			m.leftOut++
			continue
		}
		c := Lead{
			Key: safeID(l.Key, 64), Role: l.Role, Defined: l.Defined,
			Provision: safeID(l.Provision, 16), State: safeID(l.State, 16), StateReason: safeID(l.StateReason, 64),
			Epoch: l.Epoch, Unit: safeID(l.Unit, 96), Inject: l.Inject, MessageSpent: l.MessageSpent, MessageResult: l.MessageResult,
		}
		if l.Last != nil {
			last := *l.Last
			c.Last = &last
		}
		m.leads = append(m.leads, c)
	}
	for i, l := range m.leads {
		if l.Role == keep && keep != "" {
			m.cursor = i
		}
	}
	// A pair whose epoch is no longer the lead's current one is over.
	for k := range m.delivered {
		held := false
		for _, l := range m.leads {
			if pairKey(l.Role, l.Epoch) == k {
				held = true
			}
		}
		if !held {
			delete(m.delivered, k)
		}
	}
	for k := range m.unconfirmed {
		held := false
		for _, l := range m.leads {
			if pairKey(l.Role, l.Epoch) == k {
				held = true
			}
		}
		if !held {
			delete(m.unconfirmed, k)
		}
	}
	// The status line follows the receipt it shows: the list holds the
	// newer state of the same command.
	if m.status != nil && m.status.CommandID != "" {
		for _, l := range m.leads {
			if l.Last != nil && l.Last.CommandID == m.status.CommandID {
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
// own, and words are taken only from a receipt that is done as well.
func keepWords(list *Receipt, press Receipt) {
	if list.Status != Done || press.Status != Done || list.Op != press.Op {
		return
	}
	if list.Outcome == "" && list.Reason == "" && list.Written == "" {
		list.Outcome, list.Reason, list.Written = press.Outcome, press.Reason, press.Written
	}
	if list.Folder == "" && list.TicketFile == "" {
		list.Folder, list.TicketFile = press.Folder, press.TicketFile
	}
}

func pairKey(role string, epoch uint64) string { return role + "\x00" + itoa(epoch) }

// clamp keeps the cursor on a lead and the lead on the screen.
func (m *Model) clamp() {
	m.cursor = max(0, min(m.cursor, len(m.leads)-1))
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
		m.cursor++
		m.clamp()
	case "k", "up":
		m.cursor--
		m.clamp()
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

// admit checks what every command needs: a lead under the cursor and no
// command of that lead still waiting for its answer.
func (m *Model) admit() (Lead, bool) {
	l, ok := m.current()
	if !ok {
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
	m.busy[role] = seq
	m.status, m.statusRole, m.statusListed, m.note = nil, "", false, "sending: "+opWords(op)+" for "+key
	return func() tea.Msg {
		r, err := call(b)
		return doneMsg{owner: m, seq: seq, role: role, key: key, op: op, epoch: epoch, writer: writer, r: r, err: err}
	}
}

// startStop opens the confirmation of a stop.
func (m *Model) startStop() {
	l, ok := m.admit()
	if !ok {
		return
	}
	m.confirm = &confirmState{role: l.Role, key: l.Key, epoch: l.Epoch}
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
	if _, busy := m.busy[c.role]; busy {
		m.note = busyLine
		return nil, Action{}
	}
	return m.command(Lead{Role: c.role, Key: c.key}, OpStop, c.epoch, false,
		func(b Backend) (Receipt, error) { return b.Stop(c.role, c.epoch) }), Action{}
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
	case m.unconfirmed[pairKey(l.Role, l.Epoch)]:
		m.note = unconfirmed
	default:
		m.edit = &editState{role: l.Role, key: l.Key, epoch: l.Epoch}
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
	if _, busy := m.busy[e.role]; busy {
		m.note = busyLine
		return nil
	}
	id, ok := m.messageID()
	if !ok {
		m.note = noRandomLine
		return nil
	}
	m.unconfirmed[pairKey(e.role, e.epoch)] = true
	// What the view kept of an earlier message of the epoch is not this one's:
	// a message whose answer is lost must not read as the earlier one.
	delete(m.delivered, pairKey(e.role, e.epoch))
	role, epoch, body := e.role, e.epoch, e.text
	return m.command(Lead{Role: role, Key: e.key}, OpInject, epoch, false,
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
	if m.busy[v.role] != v.seq {
		return nil, Action{}
	}
	delete(m.busy, v.role)
	m.note, m.status, m.statusRole, m.statusListed = "", nil, "", false
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
	m.status, m.statusRole = &r, v.role
	if r.Earlier {
		// The answer to an earlier command is shown as that and settles
		// nothing for this press: no attach with a ticket that may be
		// spent, and the epoch's message stays unconfirmed until the list
		// shows otherwise.
		return tea.Batch(cmds...), Action{}
	}
	if v.op == OpInject {
		m.noteDelivery(v, r)
	}
	if v.op == OpInject && clearOfMessage(r) {
		delete(m.unconfirmed, pairKey(v.role, v.epoch))
	}
	if v.op == OpTicket && r.Status == Done {
		if act, ok := m.attach(v, r); ok {
			return tea.Batch(cmds...), act
		}
		m.status, m.statusRole, m.note = nil, "", unusableAns
	}
	return tea.Batch(cmds...), Action{}
}

// noteDelivery keeps the holder's answer of a done message, for the epoch the
// receipt names (the one the press was for when it names none), so that the
// MSG column says what became of it.
func (m *Model) noteDelivery(v doneMsg, r Receipt) {
	if r.Status != Done {
		return
	}
	epoch := r.Epoch
	if epoch == 0 {
		epoch = v.epoch
	}
	if epoch == 0 {
		return
	}
	m.delivered[pairKey(v.role, epoch)] = Delivery{Outcome: r.Outcome, Reason: r.Reason, Written: r.Written}
}

// deliveryOf returns what the view knows of the holder's answer to the
// lead's current epoch's message: the adapter's, else the one it kept.
func (m *Model) deliveryOf(l Lead) Delivery {
	if l.MessageResult != (Delivery{}) {
		return l.MessageResult
	}
	return m.delivered[pairKey(l.Role, l.Epoch)]
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
	if !usablePath(r.Folder) || !usablePath(r.TicketFile) {
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
