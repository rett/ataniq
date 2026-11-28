//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// This file is what scratch mode does to take a lead from starting to ready:
// a background observer per lead that asks the holder's control socket for its
// status and, when the holder answers for the lead's own epoch, sets the shown
// state ready. The canary has none of it.
//
// Before it sets ready the observer checks the build the holder recorded in
// its holder.json, which the holder reads from its own running executable:
// the daemon checked the holder executable by its path before the start, and
// the unit manager looks the path up again, so a holder rebuilt in between
// runs as another build. A lead whose holder is not the expected build is
// never shown ready. The file is read after an answer for the epoch, and also
// after a question that got no usable answer: a holder of another build can
// write its file of the epoch and never answer. Only a file of the epoch that
// confirms another build (buildid.Confirmed) is acted on: the lead is shown
// degraded, with the reason holder-version-skew and the refusal, which names
// both builds, in the detail (markSkewed). That launch's lead.start has
// already returned by then, done, with the state starting. The daemon never
// stops the holder for its build, whether it launched the epoch or adopted
// it: its ticket and its message are refused holder-version-skew before the
// holder is asked anything, and the owner stops the lead and starts it again,
// which is the way out. A file that is missing, cannot be read or is of
// another epoch confirms nothing, after an answer as without one: it is
// logged and read again at the next question, and the lead stays starting
// when the observation ends.
//
// A lead that an earlier daemon showed ready was ready for that daemon's
// build. Before this daemon serves anything, every lead it adopted shown
// ready has its holder.json of the epoch read again (revalidateAdopted): one
// that confirms another build than this daemon's is shown degraded in the same
// way, so that the list never shows ready a holder whose ticket this daemon
// refuses. Its unit is left running as well. A file that is missing or cannot
// be read confirms nothing, and the lead stays ready (its ticket says why it
// is refused); the daemon logs it.
//
// What ready means here is the holder's word and no more: the holder is up and
// the agent was started. The agent's login is not observed, so ready is not a
// claim that the agent works. After readiness the observer polls for loss of
// contact or exit. Only degradation due to those polls can be cleared by a
// fresh healthy status for the epoch.
//
// The observer never blocks lead.start or a client call, and takes no lock
// across the holder call: it takes the role's operation lock, as a try-lock,
// for the one compare-and-set of the epoch and the shown state that makes the
// change, which the registry does not make itself (it checks the epoch only).
// A role whose operation lock is held is tried again, never waited for, so a
// stop is never held up by an observer. A confirmed other build is a finding
// the observation's deadline does not end: its compare-and-set is tried again
// while the lock is held until it is made, the lead is no longer starting in
// the epoch, or the daemon stops.

// Defaults of the observer, and the reason of the state it sets.
const (
	// DefaultReadyWithin is how long one observation goes on asking the
	// holder before it gives up, counted from when it starts. A lead that is
	// still starting then stays so; the daemon observes it again when it is
	// started next.
	DefaultReadyWithin = 60 * time.Second
	// DefaultReadyBackoff is the wait before the second question to the holder;
	// it doubles after each question that did not settle the matter, up to
	// eight times this.
	DefaultReadyBackoff = 200 * time.Millisecond
	// DefaultHealthEvery is the interval between status checks after readiness.
	DefaultHealthEvery = 30 * time.Second

	codeHolderUnreachable = "holder-unreachable"
	codeLeadExited        = "lead-exited"

	// codeHolderReady is the reason code of a state the observer sets.
	codeHolderReady = "holder-ready"
	// detailHolderReady is its detail.
	detailHolderReady = "the holder is up and the agent was started; the agent's login is not observed"
)

// leadReadyHook is called by an observer when the holder has answered for the
// lead's epoch ("answered"), before the state is looked at and set. Only a test
// sets it, to make a stop or a start happen at that point.
var leadReadyHook func(stage string)

// showSkewedHook, when set, is called by showSkewed before the registry is
// written, and an error it returns is returned in place of the write's, with
// nothing written. Only a test sets it, to make the append fail.
var showSkewedHook func() error

// readyObservers is the observers of one daemon: at most one per lead. The
// mutex guards the fields and is never held across a call that waits.
type readyObservers struct {
	mu     sync.Mutex
	ctx    context.Context // the observers' parent; nil until the server runs
	cancel context.CancelFunc
	closed bool
	gen    uint64
	byRole map[leadrole.Role]*readyObserver
	wg     sync.WaitGroup
}

// readyObserver is the observer of one lead's epoch.
type readyObserver struct {
	gen    uint64
	epoch  uint64
	cancel context.CancelFunc
	// Only this observer's polling goroutine reads and writes missedStatus.
	missedStatus bool
}

// startObservers lets observers run, under ctx (the server's: it ends when the
// daemon is stopping), and starts one for every lead that the registry shows
// starting or eligible for health polling, with a recorded launch and a
// holder command. This includes leads adopted after a daemon restart.
func (l *leadMode) startObservers(ctx context.Context) {
	o := &l.ready
	o.mu.Lock()
	if o.closed || o.ctx != nil {
		o.mu.Unlock()
		return
	}
	o.ctx, o.cancel = context.WithCancel(ctx)
	o.byRole = map[leadrole.Role]*readyObserver{}
	o.mu.Unlock()
	for _, role := range l.cm.router.order {
		lr := l.roles[role]
		if lr.in.holderCommand() == nil {
			continue
		}
		if e, ok := lr.in.ext.runningEntry(); ok && (e.Shown.State == leadregistry.StateStarting || watchesHealth(e)) {
			l.observeReady(lr, e.Epoch)
		}
	}
	// The timers that settle a launch the daemon was killed in the middle of.
	l.startSettlers(ctx)
}

// joinObservers ends every observer and every settle timer and waits for them,
// at most grace, and reports whether they all ended. Once it is called none
// starts. It is called before the registry is closed.
func (l *leadMode) joinObservers(grace time.Duration) bool {
	if l == nil {
		return true
	}
	o := &l.ready
	o.mu.Lock()
	o.closed = true
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Unlock()
	l.cancelSettlers()
	// The seat's chat port answers busy from here on, and the chat's
	// scanners end with the observers.
	l.port.bind(nil)
	l.chat.shutdown()
	ended := make(chan struct{})
	go func() {
		o.wg.Wait()
		l.settle.wg.Wait()
		if l.chat != nil {
			l.chat.wg.Wait()
		}
		close(ended)
	}()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-ended:
		return true
	case <-t.C:
		return false
	}
}

// observeReady starts the observer of the lead's epoch, in place of any that
// the lead has for the same or an earlier epoch, and returns at once. It does
// nothing before the observers are let run or after they are joined, and
// nothing when the lead has an observer of a later epoch: the check and the
// replacement are one step under the observers' mutex, so a registration that
// was decided on an older read of the registry (the scan of startObservers,
// which runs beside the first commands) never cancels the observer of an
// epoch started since.
func (l *leadMode) observeReady(lr *leadRole, epoch uint64) {
	o := &l.ready
	o.mu.Lock()
	if o.closed || o.ctx == nil {
		o.mu.Unlock()
		return
	}
	if prev := o.byRole[lr.role]; prev != nil {
		if prev.epoch > epoch {
			o.mu.Unlock()
			l.d.logf("scratch ready %q: the observer of epoch %d is not started: the lead has one of epoch %d", lr.key, epoch, prev.epoch)
			return
		}
		prev.cancel()
	}
	o.gen++
	me := &readyObserver{gen: o.gen, epoch: epoch}
	var ctx context.Context
	ctx, me.cancel = context.WithCancel(o.ctx)
	o.byRole[lr.role] = me
	o.wg.Add(1)
	o.mu.Unlock()
	go func() {
		defer o.wg.Done()
		defer func() {
			o.mu.Lock()
			if o.byRole[lr.role] == me {
				delete(o.byRole, lr.role)
			}
			o.mu.Unlock()
			me.cancel()
		}()
		defer func() {
			if r := recover(); r != nil {
				l.d.logf("scratch ready %q: the observer panicked (%T); the lead stays as it is shown", lr.key, r)
			}
		}()
		l.watchReady(ctx, lr, epoch)
		l.watchHealth(ctx, lr, me)
	}()
}

// cancelReady ends the lead's observer without waiting for it. It is called
// under the role's operation lock, so it must not wait.
func (l *leadMode) cancelReady(lr *leadRole) {
	o := &l.ready
	o.mu.Lock()
	if cur := o.byRole[lr.role]; cur != nil {
		cur.cancel()
		delete(o.byRole, lr.role)
	}
	o.mu.Unlock()
}

// watchReady asks the holder for its status, with a growing wait between the
// questions, until the lead is ready, the lead is no longer starting in the
// epoch, the time is up or ctx ends.
func (l *leadMode) watchReady(ctx context.Context, lr *leadRole, epoch uint64) {
	within, delay := l.readyWithin, l.readyBackoff
	if within <= 0 {
		within = DefaultReadyWithin
	}
	if delay <= 0 {
		delay = DefaultReadyBackoff
	}
	longest := 8 * delay
	// base is the observer's own context, without the observation's deadline:
	// the state of a holder of another build is set whenever the role's lock
	// is free, even after the observation has ended.
	base := ctx
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	var why string
	for {
		done, w := l.readyStep(ctx, base, lr, epoch)
		if done {
			return
		}
		if w != "" {
			why = w
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			if ctx.Err() == context.DeadlineExceeded {
				l.d.logf("scratch ready %q: the holder gave no answer for epoch %d within %s (%s); the lead stays starting", lr.key, epoch, within, why)
			}
			return
		case <-t.C:
		}
		if delay *= 2; delay > longest {
			delay = longest
		}
	}
}

// readyStep makes one question. It reports done when there is nothing more to
// do (the state was set, or the lead is no longer starting in the epoch, or
// the registry cannot be used), and otherwise a short word for why the holder
// did not settle it. ctx bounds the observation; base is the observer's
// context without that bound, under which a confirmed other build is shown.
func (l *leadMode) readyStep(ctx, base context.Context, lr *leadRole, epoch uint64) (done bool, why string) {
	if !l.startingIn(lr, epoch) {
		return true, ""
	}
	ext := lr.in.ext
	resp, err := l.holderStatus(ctx, lr, epoch)
	switch {
	case err != nil:
		why = "no usable answer"
	case !resp.OK:
		why = "refused: " + SafeText(resp.Code)
	case resp.Status == nil:
		why = "no status in the answer"
	case resp.Status.Epoch != epoch || resp.Status.Lead != lr.role.String():
		// Another epoch or another lead: not this lead's holder. Ignored.
		why = "the answer is for another epoch or lead"
	}
	if why != "" {
		// No answer of the epoch's holder, but its state file may be there:
		// a holder of another build that never answers is known by the file
		// it wrote for the epoch from its own executable. Only a confirmed
		// mismatch is acted on; a file that is missing or cannot be read
		// says nothing without an answer.
		if berr := ext.holderBuild(epoch); buildid.Confirmed(berr) {
			l.markSkewed(base, lr, epoch, ext.skewRefusal("scratch ready "+strconv.Quote(lr.key), berr))
			return true, ""
		}
		return false, why
	}
	if leadReadyHook != nil {
		leadReadyHook("answered")
	}
	// The holder that answered is checked by the build it recorded for the
	// epoch, before it counts as started.
	e, ok := ext.runningEntry()
	if !ok || e.Epoch != epoch || e.Shown.State != leadregistry.StateStarting {
		return true, ""
	}
	if err := ext.holderBuild(epoch); err != nil {
		if !buildid.Confirmed(err) {
			// No state file of the epoch yet, or one that cannot be read:
			// nothing is known of its build. It is read again at the next
			// question, and the lead is not shown ready meanwhile.
			return false, "the holder's build cannot be confirmed: " + SafeText(err.Error())
		}
		l.markSkewed(base, lr, epoch, ext.skewRefusal("scratch ready "+strconv.Quote(lr.key), err))
		return true, ""
	}
	if l.promote(lr, epoch) {
		return true, ""
	}
	return false, "the operation lock was held"
}

func (l *leadMode) holderStatus(ctx context.Context, lr *leadRole, epoch uint64) (wire.Response, error) {
	ext := lr.in.ext
	resp, _, err := callHolder(ctx, ext.holderDir, wire.Request{
		Op: wire.OpStatus, Epoch: epoch, CommandID: ext.commandID("status", epoch),
	}, l.cm.dialFor, l.cm.dialFor)
	return resp, err
}

// healthDegraded distinguishes poll observations from a build-skew refusal.
func healthDegraded(e leadregistry.Entry) bool {
	return e.Shown.State == leadregistry.StateDegraded &&
		(e.Shown.Reason.Code == codeHolderUnreachable || e.Shown.Reason.Code == codeLeadExited)
}

func watchesHealth(e leadregistry.Entry) bool {
	return e.Shown.State == leadregistry.StateReady || healthDegraded(e)
}

func (l *leadMode) watchHealth(ctx context.Context, lr *leadRole, observer *readyObserver) {
	epoch := observer.epoch
	every := l.healthEvery
	if every <= 0 {
		every = DefaultHealthEvery
	}
	t := time.NewTimer(every)
	defer t.Stop()
	for {
		e, ok := lr.in.ext.runningEntry()
		if !ok || e.Epoch != epoch || !watchesHealth(e) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		resp, err := l.holderStatus(ctx, lr, epoch)
		var status *wire.Status
		if err == nil && resp.OK {
			status = resp.Status
		}
		l.recordHealth(ctx, lr, observer, status)
		t.Reset(every)
	}
}

// recordHealth applies one poll under the same operation lock as stop and
// start. A busy role discards the sample and gets a fresh one next time.
func (l *leadMode) recordHealth(ctx context.Context, lr *leadRole, observer *readyObserver, status *wire.Status) {
	epoch := observer.epoch
	e, ok := lr.in.ext.runningEntry()
	if ctx.Err() != nil || !ok || e.Epoch != epoch || !watchesHealth(e) {
		return
	}
	// Any status breaks the streak of missed replies. A status for another
	// role or epoch still supplies no evidence about this lead.
	missedBefore := observer.missedStatus
	observer.missedStatus = status == nil
	state := leadregistry.StateDegraded
	var reason leadregistry.Reason
	switch {
	case status == nil:
		if !missedBefore {
			return
		}
		reason = leadregistry.Reason{Code: codeHolderUnreachable, Detail: "the holder did not return a usable status"}
	case status.Epoch != epoch || status.Lead != lr.role.String():
		return
	case status.Facts.TUIExited == wire.FactYes || status.Facts.TreeGone == wire.FactYes:
		reason = leadregistry.Reason{Code: codeLeadExited, Detail: "the holder reports that the agent exited"}
	case status.Facts.TUIExited != wire.FactNo:
		return
	default:
		state = leadregistry.StateReady
		reason = leadregistry.Reason{Code: codeHolderReady, Detail: detailHolderReady}
	}
	if state == e.Shown.State && (state == leadregistry.StateReady || reason.Code == e.Shown.Reason.Code) {
		return
	}
	// A previous daemon may have recorded the degradation. Require this
	// daemon's expected build before publishing ready again.
	if state == leadregistry.StateReady && lr.in.ext.holderBuild(epoch) != nil {
		return
	}
	if !lr.tryLock() {
		return
	}
	defer lr.unlock()
	if ctx.Err() != nil {
		return
	}
	// The decision was made from e. The registry compares its whole shown
	// value (state, reason, time, evidence, proof) with it under its write
	// lock, and its epoch, so a stop, a new epoch or another writer's newer
	// decision between the read and the write refuses this one.
	_, err := l.reg.SetState(lr.role, leadregistry.StateChange{
		Epoch: epoch, Expected: leadregistry.ExpectShown(e.Shown), State: state,
		Evidence: leadregistry.EvidenceObservation, Reason: reason,
	})
	if err != nil {
		if supersededDecision(err) {
			return
		}
		l.registryDown(err)
		l.d.logf("scratch health %q: cannot record %s for epoch %d: %s", lr.key, state, epoch, SafeText(err.Error()))
	}
}

// supersededDecision reports whether the registry refused a write because the
// role's epoch or shown value is no longer the one the writer decided from.
// That is no failure of the registry: the newer decision stands, and the
// writer's next observation judges again.
func supersededDecision(err error) bool {
	return errors.Is(err, leadregistry.ErrStaleEpoch) || errors.Is(err, leadregistry.ErrStaleState)
}

// markSkewed shows the lead of epoch, whose holder.json confirms another
// build (f is the refusal of the check), degraded with the reason
// holder-version-skew (showSkewed). It is a compare-and-set under the role's
// operation lock, taken as a try-lock, made only while the lead is shown
// starting in the epoch, so it never changes a later epoch or a lead that was
// stopped meanwhile. While the lock is held it is tried again, with the
// observer's growing wait, until it is made or ctx ends (the daemon stops):
// the observation's deadline does not end it. Nothing is stopped and nothing
// is asked of the holder.
func (l *leadMode) markSkewed(ctx context.Context, lr *leadRole, epoch uint64, f *opFail) {
	delay := l.readyBackoff
	if delay <= 0 {
		delay = DefaultReadyBackoff
	}
	longest := 8 * delay
	for !lr.tryLock() {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			l.d.logf("scratch ready %q: the holder of epoch %d is not the expected build, and the daemon stopped before the lead could be shown degraded; its ticket and message are refused", lr.key, epoch)
			return
		case <-t.C:
		}
		if delay *= 2; delay > longest {
			delay = longest
		}
	}
	defer lr.unlock()
	e, ok := l.startingEntry(lr, epoch)
	if !ok {
		return
	}
	if err := l.showSkewed(lr, e, f); err != nil {
		if supersededDecision(err) {
			return
		}
		l.registryDown(err)
		l.d.logf("scratch ready %q: the state degraded cannot be set for epoch %d: %s", lr.key, epoch, SafeText(err.Error()))
		return
	}
	l.d.logf("scratch ready %q: the holder that started for epoch %d is not the build checked before the start; the lead is shown degraded and its unit is left running: stop the lead and start it again", lr.key, epoch)
}

// showSkewed sets the lead of e, the entry the caller read, shown degraded,
// with the reason holder-version-skew, the evidence kind ataniq-observation
// and the refusal f as the detail. The write is refused (an error that
// wraps leadregistry.ErrStaleState or ErrStaleEpoch) when the registry no
// longer shows e's value in e's epoch. The caller holds the role's operation
// lock.
func (l *leadMode) showSkewed(lr *leadRole, e leadregistry.Entry, f *opFail) error {
	detail := f.msg
	if len(detail) > leadregistry.MaxReasonDetailBytes {
		detail = detail[:leadregistry.MaxReasonDetailBytes]
	}
	if showSkewedHook != nil {
		if err := showSkewedHook(); err != nil {
			return err
		}
	}
	_, err := l.reg.SetState(lr.role, leadregistry.StateChange{
		Epoch: e.Epoch, Expected: leadregistry.ExpectShown(e.Shown), State: leadregistry.StateDegraded, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: codeHolderVersionSkew, Detail: detail},
	})
	return err
}

// revalidateAdopted reads again, before the daemon serves, the holder.json of
// every lead it found ready or degraded by a health poll (with a launch and
// a holder command): an earlier daemon set ready for its own build. A file of the epoch
// that confirms another build than this daemon's (buildid.Confirmed) makes the
// lead shown degraded (showSkewed): its unit is not stopped and nothing is
// asked of its holder. Any other answer of the check leaves the lead as it is
// shown. A degraded state that cannot be recorded is returned, and the daemon
// does not start: it would otherwise serve the earlier daemon's ready for a
// holder whose ticket it refuses. It runs before the socket is made, so no
// command of the role runs beside it; the role's lock is taken all the same.
func (l *leadMode) revalidateAdopted() error {
	for _, role := range l.cm.router.order {
		lr := l.roles[role]
		if lr.in.holderCommand() == nil {
			continue
		}
		e, ok := lr.in.ext.runningEntry()
		if !ok || !watchesHealth(e) {
			continue
		}
		err := lr.in.ext.holderBuild(e.Epoch)
		if err == nil {
			continue
		}
		op := "scratch ready " + strconv.Quote(lr.key)
		if !buildid.Confirmed(err) {
			l.d.logf("%s: the adopted %s lead's build cannot be confirmed for epoch %d; it stays %s: %s", op, e.Shown.State, e.Epoch, e.Shown.State, SafeText(err.Error()))
			continue
		}
		f := lr.in.ext.skewRefusal(op, err)
		if !lr.tryLock() {
			return fmt.Errorf("scratch mode: the lead %q of epoch %d is another build than this daemon's, and its lock is held, so it cannot be shown degraded", lr.key, e.Epoch)
		}
		serr := l.showSkewed(lr, e, f)
		lr.unlock()
		if serr != nil {
			l.registryDown(serr)
			return fmt.Errorf("scratch mode: the lead %q of epoch %d is another build than this daemon's, and its state degraded cannot be recorded: %w; the daemon does not serve a state it could not record", lr.key, e.Epoch, serr)
		}
		l.d.logf("%s: the holder of epoch %d, which this daemon adopted shown %s, is not its build; the lead is shown degraded and its unit is left running: stop the lead and start it again", op, e.Epoch, e.Shown.State)
	}
	return nil
}

// startingEntry returns the lead's entry and true when it is shown starting in
// the epoch, with its launch recorded. A write decided from it passes its
// shown value as the registry's expectation.
func (l *leadMode) startingEntry(lr *leadRole, epoch uint64) (leadregistry.Entry, bool) {
	e, ok := lr.in.ext.runningEntry()
	return e, ok && e.Epoch == epoch && e.Shown.State == leadregistry.StateStarting
}

// startingIn reports whether the lead is shown starting in the epoch, with its
// launch recorded.
func (l *leadMode) startingIn(lr *leadRole, epoch uint64) bool {
	_, ok := l.startingEntry(lr, epoch)
	return ok
}

// promote sets the state ready if the lead is still shown starting in the
// epoch, as one compare-and-set under the role's operation lock, taken as a
// try-lock and held for this alone, never across a call to the holder. It
// reports done: false only when the lock was held (a define, a start or a stop
// of the role runs), which the caller tries again.
func (l *leadMode) promote(lr *leadRole, epoch uint64) (done bool) {
	if !lr.tryLock() {
		return false
	}
	defer lr.unlock()
	e, ok := l.startingEntry(lr, epoch)
	if !ok {
		return true
	}
	_, err := l.reg.SetState(lr.role, leadregistry.StateChange{
		Epoch: epoch, Expected: leadregistry.ExpectShown(e.Shown), State: leadregistry.StateReady, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: codeHolderReady, Detail: detailHolderReady},
	})
	if err != nil {
		if supersededDecision(err) {
			return true
		}
		l.registryDown(err)
		l.d.logf("scratch ready %q: the state ready cannot be set for epoch %d: %s", lr.key, epoch, SafeText(err.Error()))
		return true
	}
	l.d.logf("scratch ready %q: the holder is up for epoch %d; the lead is shown ready (the agent's login is not observed)", lr.key, epoch)
	return true
}
