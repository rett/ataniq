package pairview

import (
	"context"
	"image/color"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/qr"
)

// Phase is where the daemon's pairing is.
type Phase int

const (
	// None: no pairing is open.
	None Phase = iota
	// Waiting: the code was made and no phone has used it yet.
	Waiting
	// Pending: a phone sent its key; the words wait for the owner.
	Pending
	// Expired: the code ran out, or the pairing was ended.
	Expired
)

// Offer is the daemon's answer to a start: the one-time code and when it
// ends. The code is a secret: the view draws it only inside the QR.
type Offer struct {
	Code      string
	ExpiresAt time.Time
}

// WordCount is the number of words of a fingerprint. It is this package's own
// copy of seatpair.FingerprintWords (this package may import nothing of the
// repository but the QR encoder), and a test in tests/ holds the two equal.
const WordCount = 8

// State is the daemon's answer to a status read. Words is the fingerprint of
// the phone's key, [WordCount] short words, set on the Pending phase.
//
// PendingID names the pending credential the words are the fingerprint of:
// 16 lowercase hex characters, set on the Pending phase. Confirm and reject
// carry it back, so that the daemon acts on the credential the owner looked
// at and on no other.
type State struct {
	Phase     Phase
	PendingID string
	Words     [WordCount]string
	ExpiresAt time.Time
}

// Paired is the daemon's answer to a confirm: the id of the new deciding key
// and its kind (the kind of a syncing passkey is "unverified").
type Paired struct {
	ID   string
	Kind string
}

// Backend is where the pairing is done: the daemon. The view calls it only
// from a command, with a context that ends after Options.Timeout. Every
// string it returns is untrusted.
type Backend interface {
	// PairStart makes a one-time code. With replace it ends a pairing that is
	// still open; without, the daemon may refuse while one is.
	PairStart(ctx context.Context, replace bool) (Offer, error)
	// PairStatus reads the pairing's state.
	PairStatus(ctx context.Context) (State, error)
	// PairConfirm adds the phone's key as a deciding key. id is the
	// State.PendingID the owner compared the words of.
	PairConfirm(ctx context.Context, id string) (Paired, error)
	// PairReject ends the pairing without adding anything. id is the
	// State.PendingID that was shown.
	PairReject(ctx context.Context, id string) error
}

// Colors are the colours the view draws its text with. The parent fills them
// from its theme. The QR does not use them: it is always black on white.
type Colors struct {
	Fg, Bg color.Color
	// Dim is the colour of labels and key help.
	Dim color.Color
	// Accent is the colour of the title and of the words.
	Accent color.Color
	// Warn is the colour of a refusal.
	Warn color.Color
}

// Defaults of Options.
const (
	// DefaultPoll is how often the status is read while a code or the words
	// wait.
	DefaultPoll = 2 * time.Second
	// DefaultTimeout bounds each call of the backend.
	DefaultTimeout = 10 * time.Second
	// confirmDelay is how long after c a y is not taken: a key typed ahead
	// or repeated does not confirm.
	confirmDelay = 250 * time.Millisecond
)

// Options configure a Model.
type Options struct {
	// Backend is where the pairing is done. It must not be nil.
	Backend Backend
	// Origin is the app's fixed address, such as "https://app.example". See
	// ValidOrigin: with any other value the view draws only a fixed line.
	Origin string
	Colors Colors
	// After makes a command that sends a message once d has passed. Nil
	// means tea.Tick. A test gives its own clock.
	After func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
	// Now is the clock of the seconds left. Nil means time.Now.
	Now func() time.Time
	// Poll is how often the status is read. Zero means DefaultPoll.
	Poll time.Duration
	// Timeout bounds each call of the backend. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Kind says what the parent is asked to do after an Update.
type Kind int

const (
	// Nothing asks for nothing.
	Nothing Kind = iota
	// Close asks the parent to close the view: Esc was pressed. Nothing was
	// confirmed and the daemon's pairing is left as it is.
	Close
	// Quit asks the parent to quit: ctrl+c was pressed.
	Quit
)

// Action is what Update returns.
type Action struct{ Kind Kind }

// The view's own words. The text of a backend's error is never drawn: one of
// these stands for it.
const (
	title            = "PAIR A PHONE"
	noOrigin         = "pairing origin not configured"
	errStart         = "the daemon did not make a code; nothing was started"
	errRead          = "could not read the pairing state"
	errPoll          = "could not read the pairing state; trying again"
	errConfirm       = "the confirmation did not complete; the phone is not shown as added"
	errReject        = "the rejection did not complete; the pairing may still be open"
	errCode          = "the daemon's code cannot be drawn; nothing was started"
	errWords         = "the words cannot be shown safely; press x to reject"
	msgExpired       = "The code expired. Nothing was added."
	msgGone          = "No pairing is open on the daemon."
	msgRejected      = "Rejected. Nothing was added."
	msgOpen          = "A pairing is already open. Its code cannot be shown again."
	msgPaired        = "Paired."
	kindUnverif      = "Kind: unverified (a syncing passkey)."
	helpWords        = "Compare with your phone. Confirm only if they match."
	confirmLine1     = "Add this phone as a device that can approve cards."
	confirmLine2     = "press y to confirm, any other key cancels"
	msgTooSmallWords = "terminal too small to show the eight words"
	keysTooSmall     = "x: reject   Esc: close"
	waitingHint      = "Esc: close (the code stays valid until it expires)"
	maxWordCells     = 16
	maxIDCells       = 12
	maxCodeBytes     = 128
	startingLine     = "Asking the daemon for a one-time code..."
	checkingLine     = "Checking for a pairing that is already open..."
	confirmingTxt    = "Confirming..."
	rejectingTxt     = "Rejecting..."
)

type screen int

const (
	scIdle       screen = iota // explains, Enter starts
	scStarting                 // PairStart is out
	scWaiting                  // the QR is drawn
	scOpen                     // a pairing is open that this view has no code for
	scPending                  // the words wait for the owner
	scConfirm                  // the second question
	scConfirming               // PairConfirm is out
	scRejecting                // PairReject is out
	scPaired                   // done
	scExpired                  // the code ran out
	scGone                     // the daemon has no pairing
	scFailed                   // a start or a code that could not be used
)

// Model is the screen. Make one with New, start it with Init, tell it its
// size with SetSize, hand it every message the parent receives while it is
// open (Update) and draw it with Draw. A Model is used from one goroutine, as
// Bubble Tea uses a model.
type Model struct {
	o      Options
	after  func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
	now    func() time.Time
	poll   time.Duration
	origin string // the cleaned origin; "" when it is not valid
	w, h   int

	screen screen
	// note is one fixed line under the screen's text: a refusal or a hint.
	note string
	// rows are the drawn rows of the QR; the code and the address are not
	// kept.
	rows []string
	// rowsMin are the rows with the least quiet zone, for a screen that
	// does not hold rows.
	rowsMin []string
	expires time.Time
	// words are the words as drawn, and wordsOK says each is one short
	// printable word, so the owner may confirm.
	words   [WordCount]string
	wordsOK bool
	// pid is the pending id the words belong to, as the daemon gave it; it
	// is valid (see validPendingID) when wordsOK is.
	pid string
	// id and kind are the confirmed key's, cleaned.
	id, kind string
	// promptAt is when the second question was asked.
	promptAt time.Time

	// gen numbers the pairings this view has started; an answer or a tick of
	// another generation is dropped. seq numbers the commands and the await
	// fields hold the one answer awaited for each kind.
	gen                                                int
	seq                                                int
	awaitStart, awaitStatus, awaitConfirm, awaitReject int
	// opening is set from Init until the first status answer.
	opening bool
	// cycle is set while a tick is scheduled or a status read is out.
	cycle bool
}

// New returns a view for o. It has asked nothing yet: Init returns the
// command that reads the daemon's state.
func New(o Options) *Model {
	m := &Model{o: o, after: o.After, now: o.Now, poll: o.Poll}
	if m.after == nil {
		m.after = tea.Tick
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.poll <= 0 {
		m.poll = DefaultPoll
	}
	if m.o.Timeout <= 0 {
		m.o.Timeout = DefaultTimeout
	}
	if origin, ok := ValidOrigin(o.Origin); ok && o.Backend != nil {
		m.origin = origin
	}
	return m
}

// Init returns the command that reads whether a pairing is already open. It
// is nil when the origin is not valid: nothing is asked of the daemon then.
func (m *Model) Init() tea.Cmd {
	if m.origin == "" {
		return nil
	}
	m.opening = true
	return m.statusCmd()
}

// SetSize tells the view the size of its region.
func (m *Model) SetSize(width, height int) {
	w, h := max(0, width), max(0, height)
	if (w != m.w || h != m.h) && m.screen == scConfirm {
		// The second question was asked for a screen the owner saw at the
		// old size: it is dropped.
		m.screen = scPending
	}
	m.w, m.h = w, h
}

// Configured reports whether the view has a valid origin and a backend.
func (m *Model) Configured() bool { return m.origin != "" }

// The messages of the view's own commands. Each carries its owner, so that
// the answer to a command of another view is left alone.
type (
	startedMsg struct {
		owner *Model
		seq   int
		gen   int
		offer Offer
		err   error
	}
	statusMsg struct {
		owner *Model
		seq   int
		gen   int
		st    State
		err   error
	}
	confirmedMsg struct {
		owner *Model
		seq   int
		gen   int
		p     Paired
		err   error
	}
	rejectedMsg struct {
		owner *Model
		seq   int
		gen   int
		err   error
	}
	tickMsg struct {
		owner *Model
		gen   int
	}
)

// Update handles a message. It returns the command to run and what the
// parent is asked to do. Only a key press and the view's own messages do
// anything: a paste, a mouse event or a terminal's answer is ignored.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, Action) {
	switch v := msg.(type) {
	case startedMsg:
		if v.owner == m {
			return m.onStarted(v), Action{}
		}
	case statusMsg:
		if v.owner == m {
			return m.onStatus(v), Action{}
		}
	case confirmedMsg:
		if v.owner == m {
			m.onConfirmed(v)
			return m.poller(), Action{}
		}
	case rejectedMsg:
		if v.owner == m {
			m.onRejected(v)
			return m.poller(), Action{}
		}
	case tickMsg:
		if v.owner == m {
			return m.onTick(v), Action{}
		}
	case tea.KeyPressMsg:
		return m.onKey(v)
	}
	return nil, Action{}
}

// callCtx makes the context of one backend call. The commands take the
// timeout when they are made: they run on another goroutine and read nothing
// of the model.
func callCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func (m *Model) startCmd(replace bool) tea.Cmd {
	m.seq++
	seq, gen, b, to := m.seq, m.gen, m.o.Backend, m.o.Timeout
	m.awaitStart = seq
	return func() tea.Msg {
		ctx, cancel := callCtx(to)
		defer cancel()
		o, err := b.PairStart(ctx, replace)
		return startedMsg{owner: m, seq: seq, gen: gen, offer: o, err: err}
	}
}

func (m *Model) statusCmd() tea.Cmd {
	m.seq++
	seq, gen, b, to := m.seq, m.gen, m.o.Backend, m.o.Timeout
	m.awaitStatus = seq
	return func() tea.Msg {
		ctx, cancel := callCtx(to)
		defer cancel()
		st, err := b.PairStatus(ctx)
		return statusMsg{owner: m, seq: seq, gen: gen, st: st, err: err}
	}
}

func (m *Model) confirmCmd() tea.Cmd {
	m.seq++
	seq, gen, b, to, id := m.seq, m.gen, m.o.Backend, m.o.Timeout, m.pid
	m.awaitConfirm = seq
	return func() tea.Msg {
		ctx, cancel := callCtx(to)
		defer cancel()
		p, err := b.PairConfirm(ctx, id)
		return confirmedMsg{owner: m, seq: seq, gen: gen, p: p, err: err}
	}
}

func (m *Model) rejectCmd() tea.Cmd {
	m.seq++
	seq, gen, b, to, id := m.seq, m.gen, m.o.Backend, m.o.Timeout, m.pid
	m.awaitReject = seq
	return func() tea.Msg {
		ctx, cancel := callCtx(to)
		defer cancel()
		err := b.PairReject(ctx, id)
		return rejectedMsg{owner: m, seq: seq, gen: gen, err: err}
	}
}

// polling reports whether the screen waits on the daemon: the status is read
// every Options.Poll while it does.
func (m *Model) polling() bool {
	switch m.screen {
	case scWaiting, scOpen, scPending, scConfirm:
		return true
	}
	return false
}

// poller starts the reading cycle when the screen needs one and none runs.
func (m *Model) poller() tea.Cmd {
	if m.cycle || !m.polling() {
		return nil
	}
	m.cycle = true
	return m.tickCmd()
}

func (m *Model) tickCmd() tea.Cmd {
	gen := m.gen
	return m.after(m.poll, func(time.Time) tea.Msg { return tickMsg{owner: m, gen: gen} })
}

func (m *Model) onTick(v tickMsg) tea.Cmd {
	if v.gen != m.gen {
		return nil
	}
	if !m.polling() {
		m.cycle = false
		return nil
	}
	if m.awaitStatus != 0 {
		// A read is out; its answer schedules the next tick.
		return nil
	}
	return m.statusCmd()
}

// startOver asks the daemon for a new code. It makes a new generation, so
// that nothing of the last one is applied.
func (m *Model) startOver(replace bool) tea.Cmd {
	if m.origin == "" {
		return nil
	}
	m.gen++
	m.cycle, m.opening = false, false
	m.awaitStatus, m.awaitStart, m.awaitConfirm, m.awaitReject = 0, 0, 0, 0
	m.screen, m.note, m.rows, m.rowsMin = scStarting, "", nil, nil
	return m.startCmd(replace)
}

func (m *Model) onStarted(v startedMsg) tea.Cmd {
	if v.gen != m.gen || v.seq != m.awaitStart || m.screen != scStarting {
		return nil
	}
	m.awaitStart = 0
	if v.err != nil {
		m.screen, m.note = scFailed, errStart
		return nil
	}
	rows, rowsMin, err := m.draw(v.offer.Code)
	m.expires = v.offer.ExpiresAt
	switch {
	case err != "":
		m.screen, m.note = scFailed, err
		return nil
	case !m.expires.After(m.now()):
		m.screen, m.note = scExpired, ""
		return nil
	}
	m.rows, m.rowsMin = rows, rowsMin
	m.screen, m.note = scWaiting, ""
	return m.poller()
}

// draw builds the QR rows of the pairing address for code, with the full
// quiet zone and with the least one. The code leaves this function only
// inside the rows. The line it returns is empty on success: errCode when the
// code is not one a fragment can hold, and the origin line when the address
// is too long for the largest code (the origin is configuration, not the
// daemon's).
func (m *Model) draw(code string) (rows, rowsMin []string, errLine string) {
	if !validCode(code) {
		return nil, nil, errCode
	}
	c, err := qr.Encode(m.origin+"/pair/#"+code, qr.M)
	if err != nil {
		return nil, nil, noOrigin
	}
	return c.HalfBlocks(qr.DefaultQuiet), c.HalfBlocks(qr.MinQuiet), ""
}

// validCode reports whether code is only what a fragment of a pairing
// address holds: URL-safe letters, digits, - and _.
func validCode(code string) bool {
	if code == "" || len(code) > maxCodeBytes {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func (m *Model) onStatus(v statusMsg) tea.Cmd {
	if v.gen != m.gen || v.seq != m.awaitStatus {
		return nil
	}
	m.awaitStatus = 0
	if m.opening {
		m.opening = false
		return m.onOpening(v)
	}
	if !m.polling() {
		m.cycle = false
		return nil
	}
	if v.err != nil {
		m.note = errPoll
		// A code that ran out is not left drawn because no read answers.
		if m.screen == scWaiting && !m.expires.After(m.now()) {
			m.end(scExpired)
			return nil
		}
		return m.tickCmd()
	}
	m.note = ""
	st := v.st
	switch st.Phase {
	case Waiting:
		if !st.ExpiresAt.IsZero() {
			m.expires = st.ExpiresAt
		}
		switch m.screen {
		case scWaiting:
			if !m.expires.After(m.now()) {
				m.end(scExpired)
				return nil
			}
		case scPending, scConfirm:
			// The daemon's pairing is another one: the words are not its.
			m.screen, m.rows, m.rowsMin = scOpen, nil, nil
		}
	case Pending:
		if !st.ExpiresAt.IsZero() {
			m.expires = st.ExpiresAt
		}
		m.setWords(st.Words, st.PendingID)
	case Expired:
		m.end(scExpired)
		return nil
	case None:
		m.end(scGone)
		return nil
	default:
		m.note = errPoll
	}
	return m.tickCmd()
}

// onOpening takes the answer to the first status read.
func (m *Model) onOpening(v statusMsg) tea.Cmd {
	if v.err != nil {
		m.note = errRead
		return nil
	}
	switch v.st.Phase {
	case Pending:
		m.expires = v.st.ExpiresAt
		m.setWords(v.st.Words, v.st.PendingID)
	case Waiting:
		m.expires = v.st.ExpiresAt
		m.screen = scOpen
	default:
		return nil
	}
	return m.poller()
}

// end leaves the polling for a screen with no code or words.
func (m *Model) end(s screen) {
	m.screen, m.note, m.rows, m.rowsMin, m.cycle = s, "", nil, nil, false
	m.words, m.wordsOK, m.pid = [WordCount]string{}, false, ""
}

// validPendingID reports whether id is 16 lowercase hex characters.
func validPendingID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// setWords shows the words of a pending pairing. A second question that was
// open is dropped when the words or the pending id are not the ones it was
// asked for. An id that is not well formed cannot be confirmed (it is still
// given back on a reject, which the daemon checks).
func (m *Model) setWords(in [WordCount]string, id string) {
	var w [WordCount]string
	ok := true
	for i, s := range in {
		w[i] = cleanText(s, maxWordCells)
		if w[i] != s || w[i] == "" || strings.ContainsAny(w[i], " \t") {
			ok = false
		}
	}
	ok = ok && validPendingID(id)
	if m.screen != scConfirm || w != m.words || id != m.pid || !ok {
		m.screen = scPending
	}
	m.rows, m.rowsMin = nil, nil
	m.words, m.wordsOK, m.pid = w, ok, id
	if !ok {
		m.note = errWords
	}
}

func (m *Model) onConfirmed(v confirmedMsg) {
	if v.gen != m.gen || v.seq != m.awaitConfirm || m.screen != scConfirming {
		return
	}
	m.awaitConfirm = 0
	if v.err != nil {
		m.screen, m.note = scPending, errConfirm
		return
	}
	m.gen++
	m.end(scPaired)
	m.id = cleanText(v.p.ID, maxIDCells)
	m.kind = cleanText(v.p.Kind, 32)
}

func (m *Model) onRejected(v rejectedMsg) {
	if v.gen != m.gen || v.seq != m.awaitReject || m.screen != scRejecting {
		return
	}
	m.awaitReject = 0
	if v.err != nil {
		m.screen, m.note = scPending, errReject
		return
	}
	m.gen++
	m.end(scIdle)
	m.note = msgRejected
}

// lockMods are lock states a terminal may report with a key; they never
// change which key it is.
const lockMods = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

// isLetter reports whether k is the plain key c: no modifier but lock
// states, and no text but c itself.
func isLetter(k tea.KeyPressMsg, c rune) bool {
	return k.Mod&^lockMods == 0 && k.Code == c && (k.Text == "" || k.Text == string(c))
}

func (m *Model) onKey(k tea.KeyPressMsg) (tea.Cmd, Action) {
	plain := k.Mod&^lockMods == 0
	switch {
	case k.Mod&^lockMods == tea.ModCtrl && k.Code == 'c':
		return nil, Action{Kind: Quit}
	case plain && k.Code == tea.KeyEscape:
		return nil, Action{Kind: Close}
	}
	if m.screen == scConfirm {
		if isLetter(k, 'y') {
			if m.now().Sub(m.promptAt) < confirmDelay {
				return nil, Action{}
			}
			if !m.wordsFit() {
				m.screen = scPending
				return nil, Action{}
			}
			m.note = ""
			m.screen = scConfirming
			return m.confirmCmd(), Action{}
		}
		// Any other key cancels the question.
		m.screen = scPending
		return nil, Action{}
	}
	if !plain {
		return nil, Action{}
	}
	switch m.screen {
	case scIdle:
		if k.Code == tea.KeyEnter {
			return m.startOver(false), Action{}
		}
	case scExpired, scGone, scFailed, scOpen:
		if k.Code == tea.KeyEnter {
			return m.startOver(true), Action{}
		}
	case scPending:
		switch {
		case isLetter(k, 'c') && m.wordsOK && m.wordsFit():
			m.screen, m.note, m.promptAt = scConfirm, "", m.now()
		case isLetter(k, 'x'):
			m.note = ""
			m.screen = scRejecting
			return m.rejectCmd(), Action{}
		}
	}
	return nil, Action{}
}

// cleanText returns s with only its printable characters, trimmed, at most
// maxCells cells, an ellipsis where it was cut. It is empty if nothing is
// left. It is the view's sanitiser for every string a backend gives.
func cleanText(s string, maxCells int) string {
	var b strings.Builder
	for rest := s; rest != ""; {
		r, n := utf8.DecodeRuneInString(rest)
		rest = rest[n:]
		if r == utf8.RuneError && n <= 1 {
			continue
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimFunc(b.String(), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsMark(r) })
	if ansi.StringWidth(out) <= maxCells {
		return out
	}
	var cut strings.Builder
	used := 0
	for rest := out; rest != ""; {
		c, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if c == "" || used+w > maxCells-1 {
			break
		}
		rest = rest[len(c):]
		cut.WriteString(c)
		used += w
	}
	return cut.String() + "…"
}
