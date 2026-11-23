package ownerchannel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// The errors of the service that are not refusals, found with errors.Is. A
// refusal is a [*record.Error] with one of the design's codes.
var (
	// ErrClosed says the service was closed.
	ErrClosed = errors.New("ownerchannel: the service is closed")
	// ErrStopped says the service stopped at an injected fault, or after
	// the journal could not be reopened. Every later call is refused; a
	// new service on the same journal and door takes over.
	ErrStopped = errors.New("ownerchannel: the service stopped")
	// ErrJournalReopened says a write to the journal may or may not have
	// been made. The service closed the journal, opened it again and
	// finished what the journal says was under way, so nothing is lost or
	// done twice, but it cannot say whether this call's own entry was
	// written. The caller asks the owner to look at the card again.
	ErrJournalReopened = errors.New("ownerchannel: the journal was reopened after a write that may not have been made; look at the card again")
	// ErrDoorUnavailable says the door could not be reached. The decision
	// is journaled and is handed over again at the next restart or tick.
	ErrDoorUnavailable = errors.New("ownerchannel: the door could not be reached")
)

// The largest number of presence trackers kept, one for each surface and
// session that has sent a beat or a decision.
const maxTrackers = 64

// clockBackLimit is how far the clock may move back before uses are
// refused until a restart: design 7.1 and OC22.
const clockBackLimit = time.Minute

// Service is the host's side of a card: it takes a lead's request through
// building, verification and planning, takes the owner's decision, hands
// it to the door and carries it out, and answers the owner connection's
// reads. It is safe for use by several goroutines.
//
// It starts no goroutine of its own. A call runs in the caller's
// goroutine and is cancelled by the caller's context or by [Service.Close],
// which waits for every call that is under way.
type Service struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	// life is how long an approval lasts from its decision.
	life time.Duration

	// dmu serialises everything that decides, uses, applies or restarts,
	// so that a limit is judged and journaled as one step and nothing is
	// applied twice.
	dmu sync.Mutex
	// tmu guards the presence trackers, which are not safe for
	// concurrent use.
	tmu sync.Mutex
	// pmu serialises the pairing operations and everything that changes the
	// list of keys; pr is the one pairing, in memory, which pmu guards.
	pmu sync.Mutex
	pr  pairing
	// seat holds the outstanding challenges of the seat gate, in memory: a
	// restart drops them.
	seat *challengeStore
	// slot holds the one verification that may run at a time.
	slot chan struct{}
	// cmu makes the compare and the raise of a sign counter one step, so
	// that verifications at once are compared in turn (see
	// [Service.verified]). It guards counters and roomTold: by display id,
	// the highest counter a verified assertion reported while the service
	// ran, and the keys whose counter the list had no room left to keep,
	// which the desk was told of. Both are in memory only. No code that
	// holds cmu takes another lock of the service.
	cmu      sync.Mutex
	counters map[string]uint32
	roomTold map[string]bool

	wg   sync.WaitGroup
	dead atomic.Bool

	// recovering is set while the journal is being reopened and the service
	// takes up what it says; dmu guards it. An uncertain answer while it is
	// set stops the service: a store that fails again right after it was
	// reopened is not tried again.
	recovering bool

	// mu guards the fields below. It is never held across a call out.
	mu     sync.Mutex
	closed bool
	store  Journal
	// gen counts how often the store was replaced.
	gen     int
	high    time.Time
	back    bool
	pending map[string]pendingCache
	// trackers hold the presence of each surface and session.
	trackers map[string]*presence.Tracker
	order    []string
	// status holds the state of requests before they have a pending
	// record, newest last.
	status      map[string]RequestStatus
	statusOrder []string
	// opened holds, for each verb and target, the cards the owner opened
	// lately, for the "differs from" block.
	opened map[string][]openedCard
	// ended holds when a pending record was first seen in a final state
	// without the journal knowing when.
	ended map[string]time.Time
	// blocked holds the pending ids whose decision the door holds and the
	// journal lacks.
	blocked map[string]bool
	// closedOut holds the pending ids whose card ended and was answered
	// and unpinned by this service.
	closedOut map[string]bool
	idx       index
	// pages holds the last pages that were split.
	pages []cachedPages
}

// pendingCache is a decoded pending record with the digest of its bytes.
type pendingCache struct {
	sha     string
	bytes   []byte
	pending record.Pending
}

// New makes a service from cfg and takes up what the journal says was under
// way when the last service stopped (see [Service.Tick]). It takes the
// journal over: [Service.Close] closes it.
func New(ctx context.Context, cfg Config) (*Service, error) {
	switch {
	case cfg.Journal == nil, cfg.Door == nil, cfg.Merger == nil, cfg.Intake == nil,
		cfg.Verifier == nil, cfg.Mailbox == nil, cfg.Now == nil:
		return nil, errors.New("ownerchannel: the journal, the door, the merger, the intake, the verifier, the mailbox and the clock are all required")
	}
	if cfg.PresenceWindow == 0 {
		cfg.PresenceWindow = presence.DefaultWindow
	}
	if err := presence.CheckWindow(cfg.PresenceWindow); err != nil {
		return nil, err
	}
	if cfg.ApprovalLife == 0 {
		cfg.ApprovalLife = defaultApprovalLife
	}
	if cfg.ApprovalLife < time.Minute || cfg.ApprovalLife > 24*time.Hour {
		return nil, errors.New("ownerchannel: the approval's life is under a minute or over a day")
	}
	s := &Service{
		cfg:       cfg,
		life:      cfg.ApprovalLife,
		slot:      make(chan struct{}, 1),
		store:     cfg.Journal,
		pending:   make(map[string]pendingCache),
		trackers:  make(map[string]*presence.Tracker),
		status:    make(map[string]RequestStatus),
		opened:    make(map[string][]openedCard),
		ended:     make(map[string]time.Time),
		blocked:   make(map[string]bool),
		closedOut: make(map[string]bool),
		idx:       newIndex(),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.initPairing()
	s.seat = newChallengeStore(cfg.ChallengeRand, cfg.SeatLog)
	// A door that cannot be reached now does not stop the service: the next
	// tick takes up what it could not.
	if err := s.Tick(ctx); err != nil && !errors.Is(err, ErrDoorUnavailable) {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// defaultApprovalLife is the design's: an approval lasts 30 minutes from
// the decision.
const defaultApprovalLife = 30 * time.Minute

// Close stops the service: it cancels the calls under way, waits for them,
// and closes the journal. Later calls are refused with [ErrClosed]. It
// leaves no goroutine behind.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	s.mu.Lock()
	st := s.store
	s.mu.Unlock()
	err := st.Close()
	if s.cfg.Keys != nil {
		err = errors.Join(err, s.cfg.Keys.Close())
	}
	return err
}

// begin admits a call: it refuses one on a closed or stopped service, and
// returns the context the call runs under, which ends with the caller's or
// with the service's, and the function that ends the call.
func (s *Service) begin(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if s.dead.Load() {
		s.mu.Unlock()
		return nil, nil, ErrStopped
	}
	s.wg.Add(1)
	gen := s.gen
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	// The call remembers the number of the store it read last, which is
	// the store it began with until it reads one: when that store fails
	// and another call has replaced it already, the call does not reopen
	// the new one (see journalIn and jfix).
	cell := &callStore{}
	cell.gen.Store(int64(gen))
	c, cancel := context.WithCancel(context.WithValue(ctx, genKey{}, cell))
	stop := context.AfterFunc(s.ctx, cancel)
	return c, func() {
		stop()
		cancel()
		s.wg.Done()
	}, nil
}

// now reads the clock and watches it: a reading more than a minute before
// the latest one marks the clock as moved back, which refuses uses until a
// restart. It is the one place the service reads the clock. The reading's
// monotonic part is dropped (Round(0)): the guard is about the wall clock,
// and a reading from time.Now carries a monotonic one with which After and
// Sub would ignore a wall clock that was set back. What the service shows and
// journals is this wall reading. The pairing's lifetimes are the exception,
// see [Service.nowBoth].
func (s *Service) now() time.Time {
	_, wall := s.nowBoth()
	return wall
}

// nowBoth is now with the reading as the clock gave it as well: the wall
// reading, which is what now returns and what the service shows and journals,
// and the raw one, whose monotonic part (when the clock has one, as time.Now
// does) the pairing needs to measure the ten minutes of a code and of a
// pending credential, so that a wall clock set back does not lengthen them.
// The clock is read once and watched as in now.
func (s *Service) nowBoth() (raw, wall time.Time) {
	raw = s.cfg.Now()
	wall = raw.Round(0)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.high.IsZero() || wall.After(s.high):
		s.high = wall
	case s.high.Sub(wall) > clockBackLimit:
		s.back = true
	}
	return raw, wall
}

// clockMovedBack reports whether the clock moved back by more than a minute
// since this service started.
func (s *Service) clockMovedBack() bool {
	s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.back
}

// fault calls the test hook for the point between two steps. A non-nil
// answer stops the service, as a crash would.
func (s *Service) fault(point string) error {
	if s.cfg.Fault == nil {
		return nil
	}
	if err := s.cfg.Fault(point); err != nil {
		s.dead.Store(true)
		return fmt.Errorf("%w at %s: %w", ErrStopped, point, err)
	}
	return nil
}

// journal returns the store in use.
func (s *Service) journal() Journal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store
}

// journalIn returns the store in use, like journal, and notes its number
// for the call that holds ctx, so that a failure of this read is judged
// against the store that was read, not the one the call began with.
func (s *Service) journalIn(ctx context.Context) Journal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := ctx.Value(genKey{}).(*callStore); ok {
		c.gen.Store(int64(s.gen))
	}
	return s.store
}

// notice tells the operator one line.
func (s *Service) notice(line string) {
	if s.cfg.Notice != nil {
		s.cfg.Notice(line)
	}
}

// genKey is the context key of the callStore of a call.
type genKey struct{}

// callStore holds the number of the store a call read last. It is set in
// [Service.begin] and by [Service.journalIn].
type callStore struct{ gen atomic.Int64 }

// handles returns what the writes through the handles go to.
func (s *Service) handles() HandleWrites {
	if s.cfg.Handles != nil {
		return s.cfg.Handles
	}
	return directHandles{}
}

// recoverJournal runs after the store answered ErrUncertain: it closes the
// store, opens it again, and takes up what the file says. The caller holds
// dmu. It returns [ErrJournalReopened], or [ErrStopped] when the store
// cannot be reopened or is uncertain again while the service takes up what
// the file says: one reopen for a failure, and no more.
func (s *Service) recoverJournal(ctx context.Context) error {
	if s.recovering {
		s.dead.Store(true)
		return fmt.Errorf("%w: the journal was uncertain again right after it was reopened", ErrStopped)
	}
	if s.cfg.Reopen == nil {
		s.dead.Store(true)
		return fmt.Errorf("%w: the journal needs reopening and the service has no way to do it", ErrStopped)
	}
	old := s.journal()
	_ = old.Close()
	fresh, err := s.cfg.Reopen()
	if err != nil {
		s.dead.Store(true)
		return fmt.Errorf("%w: the journal could not be reopened: %w", ErrStopped, err)
	}
	s.mu.Lock()
	s.store = fresh
	s.gen++
	s.mu.Unlock()
	s.recovering = true
	defer func() { s.recovering = false }()
	if err := s.reconcile(ctx); err != nil {
		if errors.Is(err, ErrStopped) {
			return err
		}
		if !errors.Is(err, ErrJournalReopened) {
			s.notice("after reopening the journal: " + err.Error())
		}
	}
	return ErrJournalReopened
}

// jerr turns an error of the journal into what a caller returns: an
// uncertain store is recovered and reported as [ErrJournalReopened], and
// anything else is passed on. The caller holds dmu.
func (s *Service) jerr(ctx context.Context, err error) error {
	if errors.Is(err, ownerjournal.ErrUncertain) {
		return s.recoverJournal(ctx)
	}
	return err
}

// jfix is jerr for a caller that does not hold dmu: an uncertain store is
// recovered under it. The caller's store may have been replaced by another
// call while it waited for dmu: the call holds the number of the store it
// read last (see journalIn), and when that is not the present one the
// failure was of a store that is closed already, so the present one is not
// reopened.
func (s *Service) jfix(ctx context.Context, err error) error {
	if !errors.Is(err, ownerjournal.ErrUncertain) {
		return err
	}
	s.dmu.Lock()
	defer s.dmu.Unlock()
	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	if c, ok := ctx.Value(genKey{}).(*callStore); ok && int(c.gen.Load()) != gen {
		return ErrJournalReopened
	}
	return s.recoverJournal(ctx)
}

// code returns the refusal code of err, if it is a refusal.
func code(err error) (record.Code, bool) { return record.CodeOf(err) }

// refuse makes a refusal with a host-written detail.
func refuse(c record.Code, detail string) *record.Error {
	return &record.Error{Code: c, Detail: detail}
}

// doorOutcome reports whether err from the door is a refusal (the door
// answered) or a failure of the call.
func doorRefusal(err error) (*record.Error, bool) {
	var e *record.Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
