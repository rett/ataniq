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
// never shown ready. When this daemon launched the epoch itself, it stops the
// lead (stopSkewed); a lead it adopted is left starting, and its ticket and
// message are refused holder-version-skew as before.
//
// What ready means here is the holder's word and no more: the holder is up and
// the agent was started. The agent's login is not observed, so ready is not a
// claim that the agent works. There is no way back: this file never sets
// degraded or starting, a lead that is ready stays so until a stop, and a
// failed or slow observation leaves the lead starting with nothing else
// changed.
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

	// codeHolderReady is the reason code of a state the observer sets.
	codeHolderReady = "holder-ready"
	// detailHolderReady is its detail.
	detailHolderReady = "the holder is up and the agent was started; the agent's login is not observed"
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
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	var why string
	for {
		done, w := l.readyStep(ctx, lr, epoch)
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
// did not settle it.
func (l *leadMode) readyStep(ctx context.Context, lr *leadRole, epoch uint64) (done bool, why string) {
	if !l.startingIn(lr, epoch) {
		return true, ""
	}
	ext := lr.in.ext
	resp, _, err := callHolder(ctx, ext.holderDir, wire.Request{
		Op: wire.OpStatus, Epoch: epoch, CommandID: ext.commandID("status", epoch),
	}, l.cm.dialFor, l.cm.dialFor)
	switch {
	case err != nil:
		return false, "no usable answer"
	case !resp.OK:
		return false, "refused: " + SafeText(resp.Code)
	case resp.Status == nil:
		return false, "no status in the answer"
	case resp.Status.Epoch != epoch || resp.Status.Lead != lr.role.String():
		// Another epoch or another lead: not this lead's holder. Ignored.
		return false, "the answer is for another epoch or lead"
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
		if l.stopSkewed(ctx, lr, epoch, f) {
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
// was held, which the caller tries again. A stop that fails leaves the epoch
// to the settle timer, as a failed lead.stop does.
func (l *leadMode) stopSkewed(ctx context.Context, lr *leadRole, epoch uint64, f *opFail) (done bool) {
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
	e, err := l.ctl.Stop(ctx, lr.role)
	if err != nil {
		l.armSettle(lr)
		l.registryDown(err)
		l.d.logf("scratch ready %q: the stop of the holder of another build failed: %s", lr.key, SafeText(err.Error()))
		return true
	}
	l.cancelSettle(lr)
	lr.in.ext.record(leadcanary.Entry{Event: leadcanary.EventStop, Epoch: e.Epoch, Detail: f.msg})
	return true
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
