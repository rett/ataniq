package seatapp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Notifier is the app's port to the notifier, the separate unit that sends
// the phone a push. The app tells it one thing: how many cards wait, an
// integer 0 to 999. It never gives it a card, an id, a text or a time, and
// there is no method that could. Package seatnotice's client implements it
// over a Unix socket (this package imports no other package of the module, so
// the port is defined here). The seat reaches nothing but the gate and this
// socket: the push itself is the notifier's.
//
// A notice never decides: the phone's text is fixed and its only link is the
// Cards list. See the notices section of the live refresh design.
//
// Notify's error tells the app whether the notifier answered: a refusal with
// a code is a [*PushError] (what package seatnotice's client returns as its
// coded error, wrapped by the command), and counts toward the day's cap of
// noticeDailyMax like a success; any other error (a timeout, a connection
// refused or lost) is a call that got no answer, which is backed off and not
// counted.
type Notifier interface {
	Notify(ctx context.Context, n int) error
}

// The limits of the notices. They are the app's and fixed here; nothing
// about them is kept on disk, so a restart of the app starts them anew (a
// restart can at worst send one more notice for the cards then waiting).
const (
	// noticeQuiet is how long after a browser's poll no notice is sent: a
	// page that polls is open, and the owner is looking at it.
	noticeQuiet = 60 * time.Second
	// noticeSpacing is how long a count that comes back to what was
	// announced does not announce again: a card decided and another asked
	// within it is not a new notice. A count above what was announced is
	// one at once.
	noticeSpacing = 2 * time.Minute
	// noticeDailyMax is the most calls the notifier answered in noticeDay: a
	// call that did not get an answer (a timeout, a refused or lost
	// connection) is not counted, since nothing was sent to the phone for
	// it; noticeTryMax bounds those in the same day.
	noticeDailyMax = 30
	noticeDay      = 24 * time.Hour
	// noticeTryMax is the most calls of any end in noticeDay, a safety valve
	// for a notifier that is unreachable for a long time: with the back-off
	// below it is a few dozen calls a day, so only a fault in this package
	// could reach it.
	noticeTryMax = 200
	// noticeBackoff is the wait after the first call that failed; each
	// failure after it doubles the wait, up to noticeBackoffMax, and a call
	// the notifier took ends it. noticeLogGap is the least time between two
	// lines in the log about a failure.
	noticeBackoff    = time.Minute
	noticeBackoffMax = 30 * time.Minute
	noticeLogGap     = time.Minute
	// noticeCall bounds one call to the notifier. A notify may take 15
	// seconds (package seatnotice's DefaultNotifyDeadline, which the client
	// enforces; the notifier itself stops at 12), so this is that and a
	// margin of 5: the client's deadline is what ends a slow call, and this
	// only a call that is stuck for another reason.
	noticeCall = 20 * time.Second
	// noticeMaxN is the largest count told to the notifier.
	noticeMaxN = 999
)

// noticeCore decides when the owner is told that cards wait. It is pure: it
// holds no clock, no goroutine and no port, and each call is given the time
// and the state it is to judge. Not safe for concurrent use.
//
// What it holds is the count last announced (floor), the least count seen
// since (low), when it announced (lastSent), and the day's tally. A notice is
// due when the cards waiting are more than the floor, with the floor as it
// stands: the announced count at once after an announcement, and, once
// noticeSpacing has passed, the least the count was in the meantime (so a
// card decided and another asked is a new notice, but only after the
// spacing). Then it is held back while a browser polled within noticeQuiet,
// while a failed call is backed off, past noticeDailyMax answered calls in a
// day, and past noticeTryMax calls of any end in a day.
type noticeCore struct {
	floor, low   int
	lastSent     time.Time
	backoffUntil time.Time
	backoff      time.Duration
	lastLog      time.Time
	dayStart     time.Time
	dayCalls     int
	tryStart     time.Time
	tries        int
}

// noticeDecision is what the core says about one state. Send says to tell
// the notifier N (with Waiting the count it stands for, which sent takes);
// otherwise Wake, when not zero, is the time to look again if nothing
// changes before it. A change of the state is a reason to look again too.
type noticeDecision struct {
	Send    bool
	N       int
	Waiting int
	Wake    time.Time
}

// decide judges the state st at now, given the time of the last browser poll
// (zero when there was none). It changes only what it has seen of the count.
func (c *noticeCore) decide(now time.Time, st WatchState, lastPoll time.Time) noticeDecision {
	// A state that is not live says nothing: the count may be old.
	if !st.Watching || st.Offline {
		return noticeDecision{}
	}
	w := st.Waiting
	if w < 0 {
		w = 0
	}
	if w < c.low {
		c.low = w
	}
	within := !c.lastSent.IsZero() && now.Before(c.lastSent.Add(noticeSpacing))
	floor := c.floor
	if !within && c.low < floor {
		floor = c.low
		c.floor = floor
	}
	if w <= floor {
		if within && min(floor, c.low) < w {
			// The count is back above what it dipped to: due once the
			// spacing has passed, with no change to wake for.
			return noticeDecision{Wake: c.lastSent.Add(noticeSpacing)}
		}
		return noticeDecision{}
	}
	// Due. Each reason to hold back names the time it ends; the notice is
	// not due before the last of them.
	var wake time.Time
	hold := func(until time.Time) {
		if until.After(now) && until.After(wake) {
			wake = until
		}
	}
	if !lastPoll.IsZero() {
		hold(lastPoll.Add(noticeQuiet))
	}
	hold(c.backoffUntil)
	if !c.dayStart.IsZero() && now.Before(c.dayStart.Add(noticeDay)) && c.dayCalls >= noticeDailyMax {
		hold(c.dayStart.Add(noticeDay))
	}
	if !c.tryStart.IsZero() && now.Before(c.tryStart.Add(noticeDay)) && c.tries >= noticeTryMax {
		hold(c.tryStart.Add(noticeDay))
	}
	if !wake.IsZero() {
		return noticeDecision{Wake: wake}
	}
	return noticeDecision{Send: true, N: min(w, noticeMaxN), Waiting: w}
}

// tried records a call to the notifier at now, whatever its end, for the
// safety valve of noticeTryMax in a day.
func (c *noticeCore) tried(now time.Time) {
	if c.tryStart.IsZero() || !now.Before(c.tryStart.Add(noticeDay)) {
		c.tryStart, c.tries = now, 0
	}
	c.tries++
}

// attempt records a call the notifier answered at now (a count taken, or a
// refusal with a code, rate_limited too), for the day's tally of
// noticeDailyMax. A call that got no answer is not recorded here: the cap is
// on the notices the phone could get, and a notifier that is down must not
// spend it.
func (c *noticeCore) attempt(now time.Time) {
	if c.dayStart.IsZero() || !now.Before(c.dayStart.Add(noticeDay)) {
		c.dayStart, c.dayCalls = now, 0
	}
	c.dayCalls++
}

// sent records that the notifier took the count waiting, and ends the
// back-off.
func (c *noticeCore) sent(now time.Time, waiting int) {
	c.floor, c.low, c.lastSent = waiting, waiting, now
	c.backoffUntil, c.backoff = time.Time{}, 0
}

// failed records a call that did not go through, and reports whether to log
// it: at most one line in noticeLogGap. The wait before the next call is
// noticeBackoff after the first failure and doubles with each one after it,
// to noticeBackoffMax; only sent ends the run of failures.
func (c *noticeCore) failed(now time.Time) (logIt bool) {
	if c.backoff == 0 {
		c.backoff = noticeBackoff
	} else {
		c.backoff = min(c.backoff*2, noticeBackoffMax)
	}
	c.backoffUntil = now.Add(c.backoff)
	if c.lastLog.IsZero() || !now.Before(c.lastLog.Add(noticeLogGap)) {
		c.lastLog = now
		return true
	}
	return false
}

// App is the handler of [New], with what runs in the background for it: the
// notices' goroutine, when [Config].Notifier is set. Close stops it.
type App struct {
	http.Handler
	stop func()
	once sync.Once
}

// Open is New for a configuration with a [Notifier]: it also starts the
// goroutine that tells the notifier when cards wait, which Close stops. It
// needs a Daemon that is a [Watcher], since the goroutine watches the same
// state the polls do. Without a Notifier it starts nothing and Close does
// nothing.
func Open(cfg Config) (*App, error) {
	a, h, err := newApp(cfg)
	if err != nil {
		return nil, err
	}
	x := &App{Handler: h, stop: func() {}}
	if cfg.Notifier != nil {
		stop, err := a.startNotices()
		if err != nil {
			return nil, err
		}
		x.stop = stop
	}
	return x, nil
}

// Close stops the notices' goroutine and waits for it. A call to the
// notifier in flight is cancelled. It is safe to call more than once.
func (x *App) Close() error {
	x.once.Do(x.stop)
	return nil
}

// polled records that a browser's poll was accepted, now. Only the time is
// kept, with no part of the poll or its session.
func (a *app) polled() { a.lastPoll.Store(a.cfg.Now().UnixNano()) }

// lastPollTime is when the last accepted poll was, zero when none was.
func (a *app) lastPollTime() time.Time {
	n := a.lastPoll.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// noticeTimer starts a timer of d and returns its channel and a stop for it.
type noticeTimer func(d time.Duration) (<-chan time.Time, func())

func realNoticeTimer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// startNotices starts the goroutine and returns the function that stops it
// and waits. It is the only place the app calls the notifier from.
func (a *app) startNotices() (stop func(), err error) {
	w, ok := a.watcher()
	if !ok {
		return nil, errors.New("seatapp: Config.Notifier needs a Daemon that watches")
	}
	timer := a.noticeTimer
	if timer == nil {
		timer = realNoticeTimer
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.noticeLoop(ctx, w, a.cfg.Notifier, timer)
	}()
	return func() {
		cancel()
		<-done
	}, nil
}

// noticeLoop watches the state and tells the notifier what the core says,
// until ctx ends. It takes the watcher's channel before its state, so that
// no change between the two is missed.
func (a *app) noticeLoop(ctx context.Context, w Watcher, n Notifier, timer noticeTimer) {
	var core noticeCore
	for ctx.Err() == nil {
		ch := w.WatchChanged()
		st := w.Watch()
		now := a.cfg.Now()
		d := core.decide(now, st, a.lastPollTime())
		if d.Send {
			core.tried(now)
			cctx, cancel := context.WithTimeout(ctx, noticeCall)
			err := n.Notify(cctx, d.N)
			cancel()
			if ctx.Err() != nil {
				return
			}
			now = a.cfg.Now()
			// A call counts toward the day's cap when the notifier
			// answered it: it took the count, or refused it with a code.
			var pe *PushError
			if err == nil || errors.As(err, &pe) {
				core.attempt(now)
			}
			if err != nil {
				if core.failed(now) && a.cfg.Logf != nil {
					// Fixed text: nothing of the error, which a port
					// of another kind could fill with anything.
					a.cfg.Logf("seatapp: the notifier did not take the count; trying again later\n")
				}
			} else {
				core.sent(now, d.Waiting)
			}
			// Judge the state again at once: it may have risen during
			// the call, and a failure now has its back-off to wait out.
			continue
		}
		var tc <-chan time.Time
		stopTimer := func() {}
		if !d.Wake.IsZero() {
			wait := d.Wake.Sub(now)
			if wait < time.Millisecond {
				wait = time.Millisecond
			}
			tc, stopTimer = timer(wait)
		}
		if a.noticeParked != nil {
			a.noticeParked()
		}
		select {
		case <-ctx.Done():
		case <-ch:
		case <-tc:
		}
		stopTimer()
	}
}
