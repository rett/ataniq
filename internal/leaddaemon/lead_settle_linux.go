//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// This file is what scratch mode does for a lead that a start left unknown
// with its launch intent pending: a timer per lead that settles the intent
// itself when the late-start window and its margin end, so that the lead need
// not wait for a restart of the daemon.
//
// The timer decides nothing. When the deadline has passed it asks the
// lifecycle to settle the one role ([leadlifecycle.Controller.SettleLaunch]),
// which runs the code Reconcile runs for it, under the lifecycle's own lock for
// the role: no unit loaded, its control group proven empty, and the window and
// margin passed on the registry's clock (or a changed boot identity), and then
// the intent is abandoned and the role is stopped, with the proof. Otherwise
// nothing is settled: a unit that is loaded at the deadline (the launch's own,
// which is adopted, or another, which is reported and its intent abandoned)
// leaves the role unknown and is never signalled; a manager that does not
// answer, or a control group not proven empty, is tried again later. A stop
// that the owner asks for, and a start, are refused or done by the lifecycle's
// rules as before, and the lifecycle's lock keeps them from overlapping the
// timer's settle: a start can only follow a settle that recorded the role
// stopped, and it takes the next epoch.
//
// A timer is armed for a role the registry shows unknown with the reason
// launch-unsettled, launch-reply-lost or (after a try) control-group-not-proven-empty
// and its intent pending: when the daemon
// starts serving (a start that was cut short by the death of an earlier daemon)
// and when a start of this daemon ended with its reply lost. A role shown
// unknown for a cut of the registry (registry-recovered) is the owner's and is
// never armed. A timer ends when the intent is no longer pending, when the
// role no longer shows the epoch and intent it was armed for, on a proven stop,
// on a new start, when the daemon stops, and when the lifecycle answers that
// the reconcile at start did not act on the registry's cut journal (the daemon
// logs it once and names the restart: nothing in a running daemon reconciles
// again, so no later try would be answered otherwise).
//
// The timer takes the role's operation lock as a try-lock, for one read of the
// registry that checks the role still shows what it was armed for, and never
// across a call to the manager or the lifecycle: a role whose lock is held is
// tried again later, never waited for, so a start or a stop is never held up by
// it.

// The reason codes that arm a timer: the lifecycle's words for a launch whose
// intent is still pending.
const (
	codeLaunchUnsettled = "launch-unsettled"
	codeLaunchReplyLost = "launch-reply-lost"

	// codeGroupNotEmpty is what the lifecycle shows for a pending intent whose
	// control group was not proven empty at a try: the intent is still the
	// timer's, which tries again. For an intent with an outcome the same code
	// arms nothing, since the intent is the gate.
	codeGroupNotEmpty = "control-group-not-proven-empty"
)

// The waits of a timer that could not settle the role.
const (
	// settleRetryFirst is the first wait after a try that left the intent
	// pending (the manager did not answer, the control group was not proven
	// empty, the role's lock was held); it doubles after each, up to
	// settleRetryLongest.
	settleRetryFirst   = 2 * time.Second
	settleRetryLongest = time.Minute
)

// settleHook is called by a timer after the role's operation lock was released
// and before the lifecycle is asked to settle ("settling"). Only a test sets
// it, to make a stop or a start happen at that point.
var settleHook func(stage string)

// settlers is the timers of one daemon: at most one per lead. The mutex guards
// the fields and is never held across a call that waits, except for the read of
// one registry entry (the registry takes no callback, so no lock of the
// registry can wait for this one).
type settlers struct {
	mu     sync.Mutex
	ctx    context.Context // the timers' parent; nil until the server runs
	cancel context.CancelFunc
	closed bool
	byRole map[leadrole.Role]*settler
	wg     sync.WaitGroup
}

// settler is the timer of one lead's launch intent.
type settler struct {
	epoch  uint64
	intent string
	cancel context.CancelFunc
}

// startSettlers lets timers run, under ctx (the server's: it ends when the
// daemon is stopping), and arms one for every lead that the registry shows
// unknown with a launch intent pending for the reasons above.
func (l *leadMode) startSettlers(ctx context.Context) {
	s := &l.settle
	s.mu.Lock()
	if s.closed || s.ctx != nil {
		s.mu.Unlock()
		return
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.byRole = map[leadrole.Role]*settler{}
	s.mu.Unlock()
	for _, role := range l.cm.router.order {
		l.armSettle(l.roles[role])
	}
}

// cancelSettlers ends every timer and lets none start. It does not wait.
func (l *leadMode) cancelSettlers() {
	s := &l.settle
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// settleWanted reports whether the entry is one a timer is for: unknown with
// the reason of a launch that was not settled, run by ataniq, and its launch
// intent pending.
func settleWanted(e leadregistry.Entry) bool {
	if e.Manager != leadregistry.ManagerAtaniq || e.Intent == nil || e.Intent.Outcome != leadregistry.OutcomePending {
		return false
	}
	if e.Shown.State != leadregistry.StateUnknown {
		return false
	}
	switch e.Shown.Reason.Code {
	case codeLaunchUnsettled, codeLaunchReplyLost, codeGroupNotEmpty:
		return true
	}
	return false
}

// armSettle starts the timer of the lead's pending launch intent when the
// registry shows the lead as settleWanted says, in place of a timer for another
// epoch or intent; a timer that is armed for the same intent is left as it is.
// It returns at once, and does nothing before the timers are let run or after
// they are ended. It takes no lock but the timers' own and, for one read, the
// registry's, so it may be called under the role's operation lock. The entry is
// read under the timers' lock, so that it is the entry the timer replaces the
// older one for: a read made before the lock could be of an epoch that a stop
// and a new start of the role have left behind while the call waited, and its
// timer would then replace the newer epoch's.
func (l *leadMode) armSettle(lr *leadRole) {
	s := &l.settle
	s.mu.Lock()
	if s.closed || s.ctx == nil {
		s.mu.Unlock()
		return
	}
	e, err := l.reg.Get(lr.role)
	if err != nil || !settleWanted(e) {
		s.mu.Unlock()
		return
	}
	if cur := s.byRole[lr.role]; cur != nil {
		if cur.epoch == e.Epoch && cur.intent == e.Intent.ID {
			s.mu.Unlock()
			return
		}
		cur.cancel()
	}
	me := &settler{epoch: e.Epoch, intent: e.Intent.ID}
	var ctx context.Context
	ctx, me.cancel = context.WithCancel(s.ctx)
	s.byRole[lr.role] = me
	s.wg.Add(1)
	s.mu.Unlock()
	l.d.logf("scratch settle %q: the launch of epoch %d is not settled; the daemon settles it when the late-start window ends, at %s", lr.key, e.Epoch,
		l.ctl.LaunchDeadline(*e.Intent).UTC().Format(time.RFC3339))
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			if s.byRole[lr.role] == me {
				delete(s.byRole, lr.role)
			}
			s.mu.Unlock()
			me.cancel()
		}()
		defer func() {
			if r := recover(); r != nil {
				l.d.logf("scratch settle %q: the timer panicked (%T); the lead stays as it is shown", lr.key, r)
			}
		}()
		l.watchSettle(ctx, lr, me)
	}()
}

// cancelSettle ends the lead's timer without waiting for it. It is called under
// the role's operation lock, so it must not wait.
func (l *leadMode) cancelSettle(lr *leadRole) {
	s := &l.settle
	s.mu.Lock()
	if cur := s.byRole[lr.role]; cur != nil {
		cur.cancel()
		delete(s.byRole, lr.role)
	}
	s.mu.Unlock()
}

// settleOf returns the lead's entry when it is still the one the timer is for.
func (l *leadMode) settleOf(lr *leadRole, me *settler) (leadregistry.Entry, bool) {
	e, err := l.reg.Get(lr.role)
	if err != nil || !settleWanted(e) || e.Epoch != me.epoch || e.Intent.ID != me.intent {
		return leadregistry.Entry{}, false
	}
	return e, true
}

// watchSettle waits for the deadline of the intent on the daemon's clock (the
// registry's, which the lifecycle measures the window on), then asks the
// lifecycle to settle the role, and goes on at growing intervals while the
// intent stays pending, until it is settled, the role no longer shows it, or
// ctx ends.
func (l *leadMode) watchSettle(ctx context.Context, lr *leadRole, me *settler) {
	retry := settleRetryFirst
	for {
		e, ok := l.settleOf(lr, me)
		if !ok {
			return
		}
		if wait := l.ctl.LaunchDeadline(*e.Intent).Sub(l.d.Clock.Now()); wait > 0 {
			// Not before the deadline; the clock is read again after the wait, in
			// case it stepped.
			if l.d.Clock.Sleep(ctx, wait) != nil {
				return
			}
			continue
		}
		if l.broken.Load() {
			return
		}
		if !l.settleStep(ctx, lr, me) {
			return
		}
		if l.d.Clock.Sleep(ctx, retry) != nil {
			return
		}
		if retry *= 2; retry > settleRetryLongest {
			retry = settleRetryLongest
		}
	}
}

// settleStep makes one try and reports whether the intent is still pending, so
// that it is tried again. The role's operation lock is held for the check that
// the role still shows what the timer is for, and released before the
// lifecycle is asked: the lifecycle's own lock for the role is what keeps the
// try from overlapping a start or a stop, and a try that finds the role changed
// settles nothing of the new epoch before its window.
func (l *leadMode) settleStep(ctx context.Context, lr *leadRole, me *settler) (again bool) {
	if !lr.tryLock() {
		return true
	}
	_, ok := l.settleOf(lr, me)
	lr.unlock()
	if !ok {
		return false
	}
	if settleHook != nil {
		settleHook("settling")
	}
	bound := l.d.ReconcileTimeout
	if bound <= 0 {
		bound = DefaultReconcileTimeout
	}
	sctx, cancel := context.WithTimeout(ctx, bound)
	rr, err := l.ctl.SettleLaunch(sctx, lr.role)
	cancel()
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		if l.registryDown(err) {
			return false
		}
		if errors.Is(err, leadlifecycle.ErrNotReconciled) {
			// The reconcile at start did not act on the registry's cut journal,
			// and nothing in this daemon runs it again: every later try would
			// give the same answer. The role is left as it is shown.
			l.d.logf("scratch settle %q: the launch of epoch %d cannot be settled by this daemon, which did not finish acting on the registry's cut journal at its start; the timer ends, and a restart of the daemon settles it", lr.key, me.epoch)
			return false
		}
		l.d.logf("scratch settle %q: the launch of epoch %d could not be settled: %s; the daemon tries again", lr.key, me.epoch, SafeText(err.Error()))
		return true
	}
	l.d.logf("scratch settle %q: epoch %d: %s (%s); the lead is shown %s: %s", lr.key, me.epoch,
		SafeText(string(rr.Action)), SafeText(rr.Reason), SafeText(string(rr.After)), SafeText(rr.Detail))
	_, pending := l.settleOf(lr, me)
	return pending
}
