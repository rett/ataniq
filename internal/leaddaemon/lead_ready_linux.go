//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"ataniq/internal/leadcanary"
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
// write its file of the epoch and never answer, and a file of the epoch that
// confirms another build (buildid.Confirmed) is acted on as an answer would
// be. When this daemon launched the epoch itself, it stops the lead
// (stopSkewed). That launch's lead.start has already returned by then, done,
// with the state starting: the stop is the daemon's own later act, under the
// role's lock and with a deadline of its own (skewStopWithin), not what is
// left of the observation's. A stop that is not proven is shown to the owner:
// the lifecycle leaves the lead unknown (stop-unproven), and a lead it left in
// another state is shown degraded with the reason skew-stop-unproven; the
// owner's lead.stop is the way out of both. A lead the daemon adopted is left
// starting, and its ticket and message are refused holder-version-skew as
// before.
//
// What ready means here is the holder's word and no more: the holder is up and
// the agent was started. The agent's login is not observed, so ready is not a
// claim that the agent works. There is no way back: this file never sets
// starting, sets degraded only for a holder of another build whose stop is not
// proven, a lead that is ready stays so until a stop, and a failed or slow
// observation leaves the lead starting with nothing else changed.
//
// The observer never blocks lead.start or a client call, and takes no lock
// across the holder call: it takes the role's operation lock, as a try-lock,
// for the one compare-and-set of the epoch and the shown state that makes the
// change, which the registry does not make itself (it checks the epoch only).
// A role whose operation lock is held is tried again, never waited for, so a
// stop is never held up by an observer.

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
	// DefaultSkewStopWithin bounds the stop of a holder of another build that
	// this daemon launched, counted from when the stop begins: the stop's
	// signals, its waits for the control group (2 s and the proof bound of
	// 5 s) and the unit manager's calls, with room to spare.
	DefaultSkewStopWithin = 30 * time.Second

	// codeHolderReady is the reason code of a state the observer sets.
	codeHolderReady = "holder-ready"
	// detailHolderReady is its detail.
	detailHolderReady = "the holder is up and the agent was started; the agent's login is not observed"
	// codeSkewStopUnproven is the reason of the degraded state of a lead whose
	// holder is another build and whose stop by the daemon is not proven.
	codeSkewStopUnproven = "skew-stop-unproven"
	// detailSkewStopUnproven heads its detail, before the refusal of the
	// build check.
	detailSkewStopUnproven = "the holder is another build and the daemon's stop of it is not proven; stop the lead: "
)

// leadReadyHook is called by an observer when the holder has answered for the
// lead's epoch ("answered"), before the state is looked at and set. Only a test
// sets it, to make a stop or a start happen at that point.
var leadReadyHook func(stage string)

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
	cancel context.CancelFunc
}

// startObservers lets observers run, under ctx (the server's: it ends when the
// daemon is stopping), and starts one for every lead that the registry shows
// starting with its launch recorded and a holder command, as a daemon that
// adopts a running lead after a restart has. Nothing runs before this: a
// start that is served has the server running.
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
		if e, ok := lr.in.ext.runningEntry(); ok && e.Shown.State == leadregistry.StateStarting {
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
// the lead has, and returns at once. It does nothing before the observers are
// let run or after they are joined.
func (l *leadMode) observeReady(lr *leadRole, epoch uint64) {
	o := &l.ready
	o.mu.Lock()
	if o.closed || o.ctx == nil {
		o.mu.Unlock()
		return
	}
	if prev := o.byRole[lr.role]; prev != nil {
		prev.cancel()
	}
	o.gen++
	me := &readyObserver{gen: o.gen}
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
	// the stop of a holder of another build is bounded on its own.
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
// context without that bound, from which the stop of a holder of another
// build takes its own.
func (l *leadMode) readyStep(ctx, base context.Context, lr *leadRole, epoch uint64) (done bool, why string) {
	if !l.startingIn(lr, epoch) {
		return true, ""
	}
	ext := lr.in.ext
	resp, _, err := callHolder(ctx, ext.holderDir, wire.Request{
		Op: wire.OpStatus, Epoch: epoch, CommandID: ext.commandID("status", epoch),
	}, l.cm.dialFor, l.cm.dialFor)
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
			f := ext.skewRefusal("scratch ready "+strconv.Quote(lr.key), berr)
			if l.stopSkewed(base, lr, epoch, f) {
				return true, ""
			}
			return false, "the operation lock was held"
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
		var se *buildid.SkewError
		if !errors.Is(err, buildid.ErrSkew) || errors.As(err, &se) && se.Why == buildid.WhyNoStateFile {
			// No state file of the epoch yet: nothing is known of its build.
			return false, "the holder's state file is not the epoch's yet"
		}
		f := ext.skewRefusal("scratch ready "+strconv.Quote(lr.key), err)
		if l.stopSkewed(base, lr, epoch, f) {
			return true, ""
		}
		return false, "the operation lock was held"
	}
	if l.promote(lr, epoch) {
		return true, ""
	}
	return false, "the operation lock was held"
}

// stopSkewed acts on a holder of the epoch that answered and is not the
// expected build (f is the refusal of the check): it is never shown ready.
// When the epoch is the one this daemon's own lead.start launched last, the
// lead is stopped through the lifecycle, as lead.stop stops it, and the stop
// is recorded in the evidence with the refusal as its detail; a lead this
// daemon adopted is left as it is shown, starting. It takes the role's
// operation lock as a try-lock, and reports done: false only when the lock
// was held, which the caller tries again.
//
// The stop is fenced to the launch: it is made under the role's lock, which
// every start, stop and define of the role takes, and only while the lead is
// shown starting in the epoch this daemon launched, so it never stops a later
// epoch. Its context is base, which ends when the daemon stops, bounded by
// skewStopWithin of its own: a holder that answered near the end of the
// observation still gets a whole stop. A stop that fails is not tried again
// (the settle timer is for a pending intent, and this launch has its
// outcome): it is shown to the owner by showStopUnproven.
func (l *leadMode) stopSkewed(base context.Context, lr *leadRole, epoch uint64, f *opFail) (done bool) {
	if !lr.tryLock() {
		return false
	}
	defer lr.unlock()
	if !l.startingIn(lr, epoch) {
		return true
	}
	if lr.launched != epoch {
		l.d.logf("scratch ready %q: the holder of epoch %d, which this daemon adopted, is not its build; the lead stays starting: %s", lr.key, epoch, SafeText(f.msg))
		return true
	}
	l.d.logf("scratch ready %q: the holder that started for epoch %d is not the build checked before the start; the lead is stopped: %s", lr.key, epoch, SafeText(f.msg))
	within := l.skewStopWithin
	if within <= 0 {
		within = DefaultSkewStopWithin
	}
	ctx, cancel := context.WithTimeout(base, within)
	e, err := l.ctl.Stop(ctx, lr.role)
	cancel()
	if err != nil {
		l.armSettle(lr)
		l.registryDown(err)
		l.d.logf("scratch ready %q: the stop of the holder of another build failed: %s", lr.key, SafeText(err.Error()))
		l.showStopUnproven(lr, epoch, f)
		return true
	}
	l.cancelSettle(lr)
	lr.in.ext.record(leadcanary.Entry{Event: leadcanary.EventStop, Epoch: e.Epoch, Detail: f.msg})
	return true
}

// showStopUnproven shows the owner a lead of epoch whose holder is another
// build (f is the refusal of the check) and whose stop by stopSkewed failed.
// The caller holds the role's operation lock. A lead the stop left unknown
// (stop-unproven, or a unit that is not its own) says so already, and one it
// left stopped or ended is gone; a lead it left in a live state (starting,
// stopping, or a state the registry was not changed from) is shown degraded
// with the reason skew-stop-unproven and the refusal in the detail, which no
// observer changes: lead.stop is the way out.
func (l *leadMode) showStopUnproven(lr *leadRole, epoch uint64, f *opFail) {
	e, err := l.reg.Get(lr.role)
	if err != nil {
		l.registryDown(err)
		return
	}
	if e.Epoch != epoch {
		return
	}
	switch e.Shown.State {
	case leadregistry.StateUnknown:
		l.d.logf("scratch ready %q: the lead of epoch %d is shown unknown (%s): stop it", lr.key, epoch, SafeText(e.Shown.Reason.Code))
		return
	case leadregistry.StateStopped, leadregistry.StateEnded:
		return
	}
	detail := detailSkewStopUnproven + f.msg
	if len(detail) > leadregistry.MaxReasonDetailBytes {
		detail = detail[:leadregistry.MaxReasonDetailBytes]
	}
	if _, err := l.reg.SetState(lr.role, leadregistry.StateChange{
		Epoch: epoch, State: leadregistry.StateDegraded, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: codeSkewStopUnproven, Detail: detail},
	}); err != nil {
		l.registryDown(err)
		l.d.logf("scratch ready %q: the state degraded cannot be set for epoch %d: %s", lr.key, epoch, SafeText(err.Error()))
		return
	}
	l.d.logf("scratch ready %q: the lead of epoch %d is shown degraded (%s): stop it", lr.key, epoch, codeSkewStopUnproven)
}

// startingIn reports whether the lead is shown starting in the epoch, with its
// launch recorded.
func (l *leadMode) startingIn(lr *leadRole, epoch uint64) bool {
	e, ok := lr.in.ext.runningEntry()
	return ok && e.Epoch == epoch && e.Shown.State == leadregistry.StateStarting
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
	if !l.startingIn(lr, epoch) {
		return true
	}
	_, err := l.reg.SetState(lr.role, leadregistry.StateChange{
		Epoch: epoch, State: leadregistry.StateReady, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: codeHolderReady, Detail: detailHolderReady},
	})
	if err != nil {
		l.registryDown(err)
		l.d.logf("scratch ready %q: the state ready cannot be set for epoch %d: %s", lr.key, epoch, SafeText(err.Error()))
		return true
	}
	l.d.logf("scratch ready %q: the holder is up for epoch %d; the lead is shown ready (the agent's login is not observed)", lr.key, epoch)
	return true
}
