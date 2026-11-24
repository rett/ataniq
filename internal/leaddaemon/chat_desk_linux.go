//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
	"ataniq/internal/leadchat"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadwire"
)

// This file is the desk's side of the scratch leads' replies: the chat desk,
// which holds one chat store per lead (package leadchat), the desk's switch of
// each lead, the scanner that takes a lead's replies while its switch is read,
// and the three operations of the desk, chat.list, chat.set and chat.purge,
// which only the daemon's own user reaches on the Unix socket. The seat reads through the
// port of chat_port_linux.go, which this desk is the source of.
//
// The switch is the desk's and lives in memory: every lead is off when the
// daemon starts (the stores open off too, see leadchat.OpenStore), a set is
// effective at once, and every list, read and scan checks it. A lead whose
// store cannot be opened or fails has no chat until the daemon is restarted,
// and no other lead is touched.

// DefaultChatScanEvery is how often the scanner of a lead whose chat is on
// reads its reply folder when [Scratch].ChatScanEvery is not set.
const DefaultChatScanEvery = 2 * time.Second

// The fixed texts of the chat operations' errors. None holds anything a peer
// sent.
const (
	msgChatNotALead     = "the key is not a lead of this daemon"
	msgChatUnrepresent  = "the lead's key is longer than a seat's lead identifier holds (32 bytes); its chat stays off"
	msgChatSendNotBuilt = "sending to a lead is not built; the switch is off or read"
	msgChatBadSwitch    = "the switch is off or read"
	msgChatStore        = "the lead's chat store cannot be used; see the daemon's log; the lead has no chat until the daemon is restarted"
	msgChatFolder       = "the lead's reply folder cannot be used: it must be a folder of the daemon's user with mode 0700 in the home of a provisioned lead; see the daemon's log"
	msgChatEpoch        = "the lead's epoch cannot be carried to the seat"
	msgChatStopping     = "the daemon is stopping"
	msgChatQuarantine   = "the lead's quarantine is full: its four names all hold a reply folder set aside, and a turn-on needs one free; stop the lead, purge it (ataniqd chat purge) or empty it by hand, then turn chat on again"
	msgChatPurge        = "the lead's quarantine cannot be purged; see the daemon's log"
	msgChatNotStopped   = "the lead is not proven stopped (it runs, starts or stops, or another command of it is in progress); its quarantine is purged only while it is stopped: stop it, then purge again"
)

// chatState is what the registry shows of a lead, for its chat: the state as
// one word of the seat and the launch epoch.
type chatState struct {
	word  string
	epoch uint64
}

// chatStateOf maps the registry's entry of a role to the seat's words:
// ready and degraded are running; desired and starting, starting; stopping,
// stopped, ended and a role the registry does not know, stopped; stalled,
// unknown and a registry that cannot be read, unknown.
func chatStateOf(e leadregistry.Entry, err error) chatState {
	switch {
	case errors.Is(err, leadregistry.ErrNoRole):
		return chatState{word: ownerwire.ChatLeadStopped}
	case err != nil:
		return chatState{word: ownerwire.ChatLeadUnknown}
	}
	st := chatState{epoch: e.Epoch}
	switch e.Shown.State {
	case leadregistry.StateReady, leadregistry.StateDegraded:
		st.word = ownerwire.ChatLeadRunning
	case leadregistry.StateDesired, leadregistry.StateStarting:
		st.word = ownerwire.ChatLeadStarting
	case leadregistry.StateStopping, leadregistry.StateStopped, leadregistry.StateEnded:
		st.word = ownerwire.ChatLeadStopped
	default:
		st.word = ownerwire.ChatLeadUnknown
	}
	return st
}

// chatEpoch is the epoch as the wire and the store take it, and false for one
// they cannot hold.
func chatEpoch(e uint64) (int64, bool) {
	if e > ownerwire.MaxChatSeq {
		return 0, false
	}
	return int64(e), true
}

// chatLead is one lead's chat.
type chatLead struct {
	key    string
	role   leadrole.Role
	layout string // the layout role, the name of the lead's chat store
	// seatID is the lead's identifier on the seat, "daemon:<key>", or ""
	// for a key the identifier cannot hold: the seat never lists such a
	// lead and chat.set refuses it.
	seatID string

	// setMu orders the sets of the lead's switch.
	setMu sync.Mutex

	// mu guards what follows; a seat read holds it for the whole read, so a
	// set to off that has returned is seen by every read after it.
	mu       sync.Mutex
	store    *leadchat.Store // nil when it could not be opened
	broken   bool            // the store failed; the lead has no chat until a restart
	on       bool            // the desk's switch: read when true
	lastHead int64
	// worker is the scanner of the lead while its switch is read.
	worker *chatWorker
}

// chatWorker is one scanner, of one generation of the lead's switch: the
// segment the store's turn-on of that generation started.
type chatWorker struct {
	gen    int64
	cancel context.CancelFunc
	done   chan struct{}
}

// chatDesk is the chat of every lead of scratch mode.
type chatDesk struct {
	logf   func(format string, a ...any)
	root   *leadchat.Root
	leads  []*chatLead
	byKey  map[string]*chatLead
	bySeat map[string]*chatLead
	every  time.Duration
	// stateOf reads the registry's entry of a role; nil until the registry
	// is open, which leaves every lead's state unknown.
	stateOf func(leadrole.Role) chatState
	// holdStopped takes the role's operation lock and proves the lead
	// stopped by the registry, and answers the release of the lock; false
	// (with the lock not held) when another command of the role holds the
	// lock or the lead is not proven stopped. nil until the registry is
	// open, which proves no lead stopped.
	holdStopped func(leadrole.Role) (release func(), ok bool)
	// beforeSeatRead, a test's hook, runs in a seat read after the desk's
	// own checks and before the store's.
	beforeSeatRead func(cl *chatLead)
	// beforeSwitch, a test's hook, runs in chat.set read before the
	// store's switch is set.
	beforeSwitch func(cl *chatLead)
	// afterScan, a test's hook, runs in a scanner after each scan, with
	// whether it goes on, before an ending scanner lets go of the lead.
	afterScan func(ctx context.Context, cl *chatLead, goOn bool)

	// changed is the function the seat's port gave (onChange), called when
	// what the chat cursor covers may have changed; nil when none.
	changed atomic.Pointer[func()]

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	wg     sync.WaitGroup
}

// onChange gives the desk the function it calls when what seatCursor answers
// may have changed; nil takes it back.
func (d *chatDesk) onChange(f func()) {
	if f == nil {
		d.changed.Store(nil)
		return
	}
	d.changed.Store(&f)
}

// cursorMoved says that what the chat cursor covers may have changed. It is
// called only for a lead that was or becomes readable by the seat: never for
// the activity of a lead whose chat is off, which the cursor does not cover.
// It never blocks, and may be called under the lead's lock.
func (d *chatDesk) cursorMoved() {
	if f := d.changed.Load(); f != nil {
		(*f)()
	}
}

// newChatDesk builds the desk of the leads, in the order of the config. It
// opens nothing.
func newChatDesk(logf func(string, ...any), every time.Duration) *chatDesk {
	if every <= 0 {
		every = DefaultChatScanEvery
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &chatDesk{logf: logf, byKey: map[string]*chatLead{}, bySeat: map[string]*chatLead{}, every: every, ctx: ctx, cancel: cancel}
}

// add adds a lead. A key the seat's identifier cannot hold gets no seat
// identifier.
func (d *chatDesk) add(key string, role leadrole.Role, layout string) {
	cl := &chatLead{key: key, role: role, layout: layout}
	if id := ownerwire.ChatKindDaemon + ":" + key; ownerwire.ValidChatLead(id) && len(d.bySeat) < ownerwire.MaxChatLeads {
		cl.seatID = id
		d.bySeat[id] = cl
	}
	d.leads = append(d.leads, cl)
	d.byKey[key] = cl
}

// unseated is how many leads the seat cannot list.
func (d *chatDesk) unseated() int {
	n := 0
	for _, cl := range d.leads {
		if cl.seatID == "" {
			n++
		}
	}
	return n
}

// open opens the chat store of every lead below the state root. A lead whose
// store cannot be opened has no chat (fail closed); the others are untouched.
// Every store opens off.
func (d *chatDesk) open(stateDir string) {
	if n := d.unseated(); n > 0 {
		d.logf("chat: %d lead(s) have a key longer than a seat's lead identifier holds (32 bytes): the seat does not list them and chat.set refuses them", n)
	}
	root, err := leadchat.OpenRoot(stateDir)
	if err != nil {
		d.logf("chat: the state root cannot be opened for the chat stores (%s); no lead has chat until the daemon is restarted", chatErrWord(err))
		return
	}
	d.root = root
	for _, cl := range d.leads {
		s, err := leadchat.OpenStore(root, cl.layout, leadchat.Options{})
		if err != nil {
			d.logf("chat: the chat store of the lead %q cannot be opened (%s); the lead has no chat until the daemon is restarted", cl.key, chatErrWord(err))
			continue
		}
		cl.store = s
		if st, err := s.Status(); err == nil {
			cl.lastHead = st.Head
		}
	}
}

// chatErrWord is a fixed word for an error of the chat store: the log never
// holds an error's text, which may name a path.
func chatErrWord(err error) string {
	switch {
	case errors.Is(err, leadchat.ErrCorrupt):
		return "corrupt"
	case errors.Is(err, leadchat.ErrLocked):
		return "locked"
	case errors.Is(err, leadchat.ErrInsecure):
		return "not private"
	case errors.Is(err, leadchat.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, leadchat.ErrBroken):
		return "broken"
	case errors.Is(err, leadchat.ErrQuarantined):
		return "quarantine full"
	case errors.Is(err, leadchat.ErrFull):
		return "full"
	case errors.Is(err, leadchat.ErrClosed):
		return "closed"
	}
	return "failed"
}

// storeDown reports an error that leaves the store unusable for good.
func storeDown(err error) bool {
	return errors.Is(err, leadchat.ErrBroken) || errors.Is(err, leadchat.ErrClosed) || errors.Is(err, leadchat.ErrCorrupt)
}

// markBroken records that the lead's store failed. The caller holds cl.mu.
func (d *chatDesk) markBroken(cl *chatLead, err error) {
	if !cl.broken {
		cl.broken = true
		if cl.on {
			d.cursorMoved()
		}
		d.logf("chat: the chat store of the lead %q failed (%s); the lead has no chat until the daemon is restarted", cl.key, chatErrWord(err))
	}
}

// state is what the registry shows of the lead now.
func (d *chatDesk) state(cl *chatLead) chatState {
	if d.stateOf == nil {
		return chatState{word: ownerwire.ChatLeadUnknown}
	}
	return d.stateOf(cl.role)
}

// shutdown tells every scanner to end; the caller waits for d.wg. No scanner
// starts after it, and chat.set read is answered unavailable.
func (d *chatDesk) shutdown() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.closed = true
	d.cancel()
	d.mu.Unlock()
}

// closeStores closes every store and the root. The scanners have ended.
func (d *chatDesk) closeStores() error {
	if d == nil {
		return nil
	}
	var errs []error
	for _, cl := range d.leads {
		cl.mu.Lock()
		if cl.store != nil {
			if err := cl.store.Close(); err != nil && !errors.Is(err, leadchat.ErrClosed) {
				errs = append(errs, err)
			}
			cl.store = nil
		}
		cl.on = false
		cl.mu.Unlock()
	}
	if d.root != nil {
		if err := d.root.Close(); err != nil && !errors.Is(err, leadchat.ErrClosed) {
			errs = append(errs, err)
		}
		d.root = nil
	}
	return errors.Join(errs...)
}

// ---- the seat's reads ---------------------------------------------------

// seatLeads answers the rows of seat.chat's leads: the leads the seat can
// list, in the order of the config. A lead whose switch is off is a bare row
// (its identifier and off); one whose switch is read carries the registry's
// state, its epoch and its chat's head, with the state unknown for a chat
// the scan stopped or a store that failed. A lead whose epoch the wire cannot
// hold is shown off.
func (d *chatDesk) seatLeads() ([]ownerwire.ChatLeadRow, error) {
	rows := make([]ownerwire.ChatLeadRow, 0, len(d.bySeat))
	for _, cl := range d.leads {
		if cl.seatID == "" {
			continue
		}
		rows = append(rows, d.seatRow(cl))
	}
	return rows, nil
}

func (d *chatDesk) seatRow(cl *chatLead) ownerwire.ChatLeadRow {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	off := ownerwire.ChatLeadRow{Lead: cl.seatID, Chat: ownerwire.ChatOff}
	if !cl.on {
		return off
	}
	st := d.state(cl)
	epoch, ok := chatEpoch(st.epoch)
	if !ok {
		return off
	}
	word, head := st.word, cl.lastHead
	switch {
	case cl.broken || cl.store == nil:
		word = ownerwire.ChatLeadUnknown
	default:
		s, err := cl.store.Status()
		switch {
		case err != nil:
			d.markBroken(cl, err)
			word = ownerwire.ChatLeadUnknown
		case s.Chat != ownerwire.ChatRead:
			return off
		default:
			head = s.Head
			cl.lastHead = head
			if s.Suspended {
				word = ownerwire.ChatLeadUnknown
			}
		}
	}
	return ownerwire.ChatLeadRow{Lead: cl.seatID, Chat: ownerwire.ChatRead, State: word, Epoch: &epoch, Head: &head}
}

// seatCursor answers what the chat cursor of the extended seat.wait covers:
// each lead the seat can list whose chat is read, not stopped by a scan, with
// a store that works and an epoch the wire holds (the leads whose
// conversation a seat read is served), with the generation of its switch and
// its head. A lead whose chat is off is left out, and nothing of it is read.
// It reads the stores' status only.
func (d *chatDesk) seatCursor() ([]ownerwire.ChatCursorLead, error) {
	var out []ownerwire.ChatCursorLead
	for _, cl := range d.leads {
		if cl.seatID == "" {
			continue
		}
		if l, ok := d.cursorLead(cl); ok {
			out = append(out, l)
		}
	}
	return out, nil
}

func (d *chatDesk) cursorLead(cl *chatLead) (ownerwire.ChatCursorLead, bool) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if !cl.on || cl.broken || cl.store == nil {
		return ownerwire.ChatCursorLead{}, false
	}
	if _, ok := chatEpoch(d.state(cl).epoch); !ok {
		return ownerwire.ChatCursorLead{}, false
	}
	s, err := cl.store.Status()
	switch {
	case err != nil:
		d.markBroken(cl, err)
		return ownerwire.ChatCursorLead{}, false
	case s.Chat != ownerwire.ChatRead || s.Suspended:
		return ownerwire.ChatCursorLead{}, false
	}
	return ownerwire.ChatCursorLead{Lead: cl.seatID, Gen: s.Gen, Head: s.Head}, true
}

// setText sets an entry's text, whose type is the wire's record text, from a
// string, without naming the record package.
func setText[T ~string](dst *T, s string) { *dst = T(s) }

// seatTime is the wire's form of a time: UTC, to the second, ending in Z.
const seatTime = "2006-01-02T15:04:05Z"

// seatRead answers one page of a lead's conversation. An unknown lead is
// chat-unknown-lead, a lead whose switch is off chat-off, and a lead whose
// chat the scan stopped, whose store failed, or whose epoch the wire cannot
// hold, chat-busy. It holds the lead's lock for the whole read, and the
// store's ReadSeat checks the lead's chat and copies the page under one
// lock of the store's.
func (d *chatDesk) seatRead(lead string, after int64, limit int) (ownerwire.SeatChatResult, error) {
	cl, ok := d.bySeat[lead]
	if !ok {
		return ownerwire.SeatChatResult{}, refuseUnknown
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	switch {
	case !cl.on:
		return ownerwire.SeatChatResult{}, refuseOff
	case cl.broken || cl.store == nil:
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	epoch, ok := chatEpoch(d.state(cl).epoch)
	if !ok {
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	if d.beforeSeatRead != nil {
		d.beforeSeatRead(cl)
	}
	// One call of the store checks that the lead may be shown (on, not
	// stopped by a scan) and copies the page under one lock.
	entries, reset, head, err := cl.store.ReadSeat(after)
	switch {
	case errors.Is(err, leadchat.ErrOff):
		return ownerwire.SeatChatResult{}, refuseOff
	case errors.Is(err, leadchat.ErrSuspended):
		return ownerwire.SeatChatResult{}, refuseBusy
	case errors.Is(err, leadchat.ErrInvalid):
		return ownerwire.SeatChatResult{}, refuseParams
	case err != nil:
		if storeDown(err) {
			d.markBroken(cl, err)
		}
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	cl.lastHead = head
	from := make([]ownerwire.ChatEntry, len(entries))
	for i, e := range entries {
		from[i] = ownerwire.ChatEntry{Seq: e.Seq, Who: e.Who, At: e.At.UTC().Format(seatTime)}
		setText(&from[i].Text, e.Text)
	}
	page := ownerwire.SeatChatResult{State: ownerwire.ChatStateRead, Lead: cl.seatID, Epoch: epoch, Head: head, Reset: reset}
	r, err := ownerwire.FillSeatChatRead(page, from, limit)
	if err != nil {
		d.logf("chat: a page of the lead %q does not encode; the read is answered busy", cl.key)
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	return r, nil
}

// ---- the scanner ----------------------------------------------------------

// switched records the store's effective switch st, which SetSwitch
// answered, in the desk, under the lead's lock: the desk's switch is read
// exactly when the store's is, and a lead that is read and not stopped by a
// scan has one scanner, of the store's generation. A scanner of the same
// generation is kept; one of another generation, or of a lead that is off or
// stopped, is let go of, and returned for the caller to end and wait for;
// started says a new scanner was started. No scanner starts once the desk
// closed. The caller holds cl.setMu.
func (d *chatDesk) switched(cl *chatLead, st leadchat.Status) (old *chatWorker, started bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cl.mu.Lock()
	defer cl.mu.Unlock()
	was := cl.on
	cl.on = st.Chat == ownerwire.ChatRead
	if cl.on {
		cl.lastHead = st.Head
	}
	if was || cl.on {
		// A turn-on, a new segment, or a failed turn-on that left the lead
		// off: the leads the seat may read may differ.
		d.cursorMoved()
	}
	want := cl.on && !st.Suspended && !cl.broken && !d.closed
	if w := cl.worker; w != nil && (!want || w.gen != st.Gen) {
		old, cl.worker = w, nil
	}
	if !want || cl.worker != nil {
		return old, false
	}
	ctx, cancel := context.WithCancel(d.ctx)
	w := &chatWorker{gen: st.Gen, cancel: cancel, done: make(chan struct{})}
	cl.worker = w
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer close(w.done)
		d.scan(ctx, cl)
		// A scanner that ended by itself (the chat stopped, or its store
		// failed) is no longer the lead's.
		cl.mu.Lock()
		if cl.worker == w {
			cl.worker = nil
		}
		cl.mu.Unlock()
	}()
	return old, true
}

// endWorker ends a scanner the desk let go of and waits for it. The caller
// holds no lock of the lead's but cl.setMu.
func endWorker(w *chatWorker) {
	if w != nil {
		w.cancel()
		<-w.done
	}
}

// stopWorker ends the lead's scanner and waits for it. The caller holds
// cl.setMu and not cl.mu.
func (d *chatDesk) stopWorker(cl *chatLead) {
	cl.mu.Lock()
	w := cl.worker
	cl.worker = nil
	cl.mu.Unlock()
	endWorker(w)
}

// scan is one lead's scanner: a scan of its reply folder every interval
// while its switch is read, until the chat is off or stopped, its store
// fails, or ctx ends. A panic ends this scanner alone, and the lead's chat
// with it.
func (d *chatDesk) scan(ctx context.Context, cl *chatLead) {
	defer func() {
		if r := recover(); r != nil {
			cl.mu.Lock()
			cl.broken = true
			if cl.on {
				d.cursorMoved()
			}
			cl.mu.Unlock()
			d.logf("chat: the scanner of the lead %q panicked (%T); the lead has no chat until the daemon is restarted", cl.key, r)
		}
	}()
	t := time.NewTicker(d.every)
	defer t.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if ctx.Err() != nil {
			return
		}
		goOn := d.scanOnce(cl, &failing)
		if d.afterScan != nil {
			d.afterScan(ctx, cl, goOn)
		}
		if !goOn {
			return
		}
	}
}

// scanOnce scans the lead's reply folder once, and reports whether the
// scanner goes on. Its log lines hold counts and fixed words only: never a
// reply's text, a file name or a path.
func (d *chatDesk) scanOnce(cl *chatLead, failing *bool) bool {
	cl.mu.Lock()
	if !cl.on || cl.broken || cl.store == nil {
		cl.mu.Unlock()
		return false
	}
	store := cl.store
	cl.mu.Unlock()
	epoch, ok := chatEpoch(d.state(cl).epoch)
	if !ok {
		return true
	}
	rep, err := store.Ingest(epoch)
	switch {
	case errors.Is(err, leadchat.ErrOff), errors.Is(err, leadchat.ErrSuspended):
		// The scanner runs only while the desk's switch is read: the lead
		// was readable, and the store says it is not now.
		d.cursorMoved()
		return false
	case err != nil && storeDown(err):
		cl.mu.Lock()
		d.markBroken(cl, err)
		cl.mu.Unlock()
		return false
	case err != nil:
		if !*failing {
			*failing = true
			d.logf("chat: a scan of the lead %q failed (%s); it is tried again every %s", cl.key, chatErrWord(err), d.every)
		}
		return true
	}
	if *failing {
		*failing = false
		d.logf("chat: the scans of the lead %q work again", cl.key)
	}
	dropped := 0
	for _, n := range rep.Dropped {
		dropped += n
	}
	if rep.Taken+rep.Withheld > 0 || rep.Suspended {
		// The lead is readable (its scanner runs only then): its head moved,
		// or a scan stopped its chat.
		d.cursorMoved()
	}
	if rep.Taken+rep.Withheld+rep.Repaired+dropped > 0 {
		d.logf("chat: the lead %q: %d repl(ies) taken, %d withheld, %d repaired, %d file(s) dropped, %d waiting", cl.key, rep.Taken, rep.Withheld, rep.Repaired, dropped, rep.Waiting)
	}
	if rep.Suspended {
		d.logf("chat: the chat of the lead %q was stopped: its reply folder was replaced, opened to others, closed to the daemon or held too much; chat.set read turns it on again", cl.key)
		return false
	}
	return true
}

// ---- the desk's operations ---------------------------------------------------

// row is the desk's row of the lead.
func (d *chatDesk) row(cl *chatLead) leadwire.ChatLead {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	r := leadwire.ChatLead{Key: cl.key, Chat: leadwire.ChatOff, Seat: cl.seatID != "", Head: cl.lastHead, Dropped: map[string]int{}}
	if cl.on {
		r.Chat = leadwire.ChatRead
	}
	if cl.broken || cl.store == nil {
		r.Unavailable = true
		return r
	}
	s, err := cl.store.Status()
	if err != nil {
		d.markBroken(cl, err)
		r.Unavailable = true
		return r
	}
	cl.lastHead = s.Head
	r.Head, r.Entries, r.Suspended = s.Head, s.Entries, s.Suspended && cl.on
	for k, v := range s.Dropped {
		if v > 0 {
			r.Dropped[string(k)] = v
		}
	}
	return r
}

// list answers chat.list.
func (d *chatDesk) list(params json.RawMessage) (any, *clientapi.ErrorBody) {
	if len(params) > 0 {
		var none struct{}
		if f := decodeStrict(params, &none); f != nil {
			return nil, f
		}
	}
	res := leadwire.ChatListResult{Leads: make([]leadwire.ChatLead, 0, len(d.leads))}
	for _, cl := range d.leads {
		res.Leads = append(res.Leads, d.row(cl))
	}
	return res, nil
}

// set answers chat.set: it sets the lead's switch at once and answers the
// lead's row. Off always succeeds: the desk's switch is off before the call
// returns, the scanner has ended, and the store drops the lead's entries.
// Read turns the store's switch on (which clears the reply folder) and starts
// the scanner; on a lead that is on already it changes nothing, and on a lead
// whose chat the scan stopped it turns it on afresh.
func (d *chatDesk) set(params json.RawMessage) (any, *clientapi.ErrorBody) {
	var p leadwire.ChatSetParams
	if f := decodeLead(params, &p, "key", "chat"); f != nil {
		return nil, f
	}
	cl, ok := d.byKey[p.Key]
	switch {
	case !ok:
		return nil, refused(msgChatNotALead)
	case p.Chat == leadwire.ChatSend:
		return nil, &clientapi.ErrorBody{Code: leadwire.CodeChatSendNotBuilt, Message: msgChatSendNotBuilt}
	case p.Chat != leadwire.ChatOff && p.Chat != leadwire.ChatRead:
		return nil, &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgChatBadSwitch}
	case cl.seatID == "":
		return nil, &clientapi.ErrorBody{Code: leadwire.CodeChatKeyUnrepresentable, Message: msgChatUnrepresent}
	}
	cl.setMu.Lock()
	defer cl.setMu.Unlock()
	if p.Chat == leadwire.ChatOff {
		d.setOff(cl)
		return d.row(cl), nil
	}
	if f := d.setRead(cl); f != nil {
		return nil, f
	}
	return d.row(cl), nil
}

// setOff turns the lead's chat off: the desk's switch first, so that no read
// after this call sees the lead on, then the scanner, then the store.
func (d *chatDesk) setOff(cl *chatLead) {
	cl.mu.Lock()
	was := cl.on
	cl.on = false
	store, broken := cl.store, cl.broken
	if was {
		d.cursorMoved()
	}
	cl.mu.Unlock()
	d.stopWorker(cl)
	if store == nil || broken {
		return
	}
	epoch, _ := chatEpoch(d.state(cl).epoch)
	if _, err := store.SetSwitch(ownerwire.ChatOff, epoch); err != nil {
		cl.mu.Lock()
		d.markBroken(cl, err)
		cl.mu.Unlock()
		return
	}
	if was {
		d.logf("chat: the chat of the lead %q is off", cl.key)
	}
}

// setRead turns the lead's chat on. The store's switch and the desk's are
// changed together: whatever SetSwitch answers, the effective switch it
// reports is what the desk records, with the scanner of its generation, so a
// failed turn-on (a suspended lead is turned off first) leaves the desk off
// as the seat sees it, and a scan that stopped the chat meanwhile cannot
// leave a new segment without a scanner.
func (d *chatDesk) setRead(cl *chatLead) *clientapi.ErrorBody {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return unavailable(msgChatStopping)
	}
	cl.mu.Lock()
	store, broken, was := cl.store, cl.broken, cl.on
	cl.mu.Unlock()
	if store == nil || broken {
		return unavailable(msgChatStore)
	}
	epoch, ok := chatEpoch(d.state(cl).epoch)
	if !ok {
		return unavailable(msgChatEpoch)
	}
	if d.beforeSwitch != nil {
		d.beforeSwitch(cl)
	}
	st, err := store.SetSwitch(ownerwire.ChatRead, epoch)
	if err != nil && storeDown(err) {
		cl.mu.Lock()
		d.markBroken(cl, err)
		cl.mu.Unlock()
	}
	old, started := d.switched(cl, st)
	endWorker(old)
	switch {
	case err != nil && storeDown(err):
		return unavailable(msgChatStore)
	case err != nil:
		d.logf("chat: the chat of the lead %q cannot be turned on (%s)", cl.key, chatErrWord(err))
		if was && st.Chat != ownerwire.ChatRead {
			d.logf("chat: the chat of the lead %q is off", cl.key)
		}
		if errors.Is(err, leadchat.ErrQuarantined) {
			return &clientapi.ErrorBody{Code: leadwire.CodeChatQuarantineFull, Message: msgChatQuarantine}
		}
		return unavailable(msgChatFolder)
	}
	if !was || started {
		d.logf("chat: the chat of the lead %q is on (read)", cl.key)
	}
	return nil
}

// purge answers chat.purge: it removes what it can of the lead's quarantine
// (leadchat.Store.PurgeQuarantine) and answers the counts. It runs only while
// the lead is proven stopped, holding the role's operation lock so that no
// define, start or stop runs beside it, and is refused chat-lead-not-stopped
// otherwise: with the lead's processes gone, nothing but the daemon changes
// what the quarantine holds. It changes no switch, and runs whether the
// lead's chat is off or read.
func (d *chatDesk) purge(params json.RawMessage) (any, *clientapi.ErrorBody) {
	var p leadwire.ChatPurgeParams
	if f := decodeLead(params, &p, "key"); f != nil {
		return nil, f
	}
	cl, ok := d.byKey[p.Key]
	if !ok {
		return nil, refused(msgChatNotALead)
	}
	notStopped := &clientapi.ErrorBody{Code: leadwire.CodeChatLeadNotStopped, Message: msgChatNotStopped}
	if d.holdStopped == nil {
		return nil, notStopped
	}
	release, ok := d.holdStopped(cl.role)
	if !ok {
		return nil, notStopped
	}
	defer release()
	cl.setMu.Lock()
	defer cl.setMu.Unlock()
	cl.mu.Lock()
	store, broken := cl.store, cl.broken
	cl.mu.Unlock()
	if store == nil || broken {
		return nil, unavailable(msgChatStore)
	}
	rep, err := store.PurgeQuarantine(leadchat.PurgeOptions{Stopped: true})
	if err != nil {
		if storeDown(err) {
			cl.mu.Lock()
			d.markBroken(cl, err)
			cl.mu.Unlock()
			return nil, unavailable(msgChatStore)
		}
		d.logf("chat: the quarantine of the lead %q cannot be purged (%s)", cl.key, chatErrWord(err))
		return nil, unavailable(msgChatPurge)
	}
	d.logf("chat: the quarantine of the lead %q was purged: %d removed, %d left, %d of %d names free", cl.key, rep.Removed, rep.Left, rep.Free, leadchat.MaxQuarantine)
	return leadwire.ChatPurgeResult{Key: cl.key, Removed: rep.Removed, Left: rep.Left, Free: rep.Free}, nil
}

// chatExt is the extension of the three chat operations.
type chatExt struct{ d *chatDesk }

var _ clientapi.Extension = (*chatExt)(nil)

// Operations lists chat.list, chat.set and chat.purge, in leadwire's order.
func (x *chatExt) Operations() []string { return leadwire.ChatOperations() }

// Mutating is true: chat.set changes the switch, and chat.purge removes.
func (x *chatExt) Mutating() bool { return true }

// Handle serves chat.list, chat.set and chat.purge.
func (x *chatExt) Handle(_ context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	switch op {
	case leadwire.OpChatList:
		return x.d.list(params)
	case leadwire.OpChatSet:
		return x.d.set(params)
	case leadwire.OpChatPurge:
		return x.d.purge(params)
	}
	return nil, &clientapi.ErrorBody{Code: clientapi.CodeUnsupportedOperation, Message: msgBadParams}
}
