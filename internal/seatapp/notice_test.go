package seatapp

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)

func live(waiting int) WatchState {
	return WatchState{Watching: true, Version: "aa", Waiting: waiting}
}

// ---- the core, with no goroutine: times and states are given ----

func TestNoticeCoreFirstCardNotifiesOnce(t *testing.T) {
	var c noticeCore
	d := c.decide(t0, live(1), time.Time{})
	if !d.Send || d.N != 1 {
		t.Fatalf("first card: %+v", d)
	}
	c.attempt(t0)
	c.sent(t0, d.Waiting)
	// The same count never notifies again, however long it stays.
	for _, dt := range []time.Duration{0, time.Second, noticeSpacing, 10 * time.Minute, 20 * time.Hour} {
		if d := c.decide(t0.Add(dt), live(1), time.Time{}); d.Send {
			t.Errorf("the same count notified again after %v: %+v", dt, d)
		}
	}
}

func TestNoticeCoreNothingWaitingNothingSent(t *testing.T) {
	var c noticeCore
	if d := c.decide(t0, live(0), time.Time{}); d.Send || !d.Wake.IsZero() {
		t.Errorf("no card: %+v", d)
	}
	if d := c.decide(t0, live(-3), time.Time{}); d.Send {
		t.Errorf("negative count: %+v", d)
	}
}

func TestNoticeCoreARiseNotifiesAtOnce(t *testing.T) {
	var c noticeCore
	d := c.decide(t0, live(1), time.Time{})
	c.attempt(t0)
	c.sent(t0, d.Waiting)
	// A rise within the spacing is a notice at once.
	at := t0.Add(5 * time.Second)
	d = c.decide(at, live(2), time.Time{})
	if !d.Send || d.N != 2 {
		t.Fatalf("a rise: %+v", d)
	}
	c.attempt(at)
	c.sent(at, d.Waiting)
	// A fall tells nothing; the floor follows it after the spacing.
	if d := c.decide(at.Add(time.Second), live(1), time.Time{}); d.Send {
		t.Errorf("a fall notified: %+v", d)
	}
}

func TestNoticeCoreCountBackAtTheSameValueWaitsOutTheSpacing(t *testing.T) {
	var c noticeCore
	d := c.decide(t0, live(1), time.Time{})
	c.attempt(t0)
	c.sent(t0, d.Waiting)
	// The card is decided, and another asked, within the spacing: no
	// notice yet, and the core says when to look again.
	if d := c.decide(t0.Add(10*time.Second), live(0), time.Time{}); d.Send {
		t.Fatalf("a fall notified: %+v", d)
	}
	d = c.decide(t0.Add(20*time.Second), live(1), time.Time{})
	if d.Send {
		t.Fatalf("the same count again within the spacing notified: %+v", d)
	}
	if want := t0.Add(noticeSpacing); !d.Wake.Equal(want) {
		t.Fatalf("wake %v, want %v", d.Wake, want)
	}
	// Once the spacing has passed, the card that came is new to the owner.
	d = c.decide(t0.Add(noticeSpacing), live(1), time.Time{})
	if !d.Send || d.N != 1 {
		t.Errorf("after the spacing: %+v", d)
	}
	// A count that never dipped does not notify at the spacing's end.
	var c2 noticeCore
	d = c2.decide(t0, live(2), time.Time{})
	c2.attempt(t0)
	c2.sent(t0, d.Waiting)
	if d := c2.decide(t0.Add(10*time.Second), live(1), time.Time{}); d.Send || !d.Wake.IsZero() {
		t.Errorf("a fall to 1: %+v", d)
	}
	if d := c2.decide(t0.Add(noticeSpacing), live(1), time.Time{}); d.Send {
		t.Errorf("1 after 2, no card new: %+v", d)
	}
	if d := c2.decide(t0.Add(noticeSpacing+time.Second), live(2), time.Time{}); !d.Send || d.N != 2 {
		t.Errorf("back to 2 after the spacing: %+v", d)
	}
}

func TestNoticeCorePollSuppresses(t *testing.T) {
	var c noticeCore
	poll := t0.Add(-30 * time.Second)
	d := c.decide(t0, live(1), poll)
	if d.Send {
		t.Fatalf("a poll 30 s ago: %+v", d)
	}
	if want := poll.Add(noticeQuiet); !d.Wake.Equal(want) {
		t.Errorf("wake %v, want %v", d.Wake, want)
	}
	// 59 s after the poll still quiet; at 60 s the notice goes.
	if d := c.decide(poll.Add(59*time.Second), live(1), poll); d.Send {
		t.Errorf("59 s after the poll: %+v", d)
	}
	if d := c.decide(poll.Add(noticeQuiet), live(1), poll); !d.Send {
		t.Errorf("60 s after the poll: %+v", d)
	}
	// A suppressed notice is not spent: a poll later, the count waits.
	if c.lastSent != (time.Time{}) || c.dayCalls != 0 {
		t.Errorf("a suppressed notice was recorded: %+v", c)
	}
}

func TestNoticeCoreNotLiveSuppresses(t *testing.T) {
	var c noticeCore
	for name, st := range map[string]WatchState{
		"offline":      {Watching: true, Offline: true, Waiting: 5},
		"not watching": {Watching: false, Waiting: 5},
		"zero":         {},
	} {
		if d := c.decide(t0, st, time.Time{}); d.Send || !d.Wake.IsZero() {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// The state that was offline is judged by what it says when it is live.
	if d := c.decide(t0, live(5), time.Time{}); !d.Send || d.N != 5 {
		t.Errorf("live again: %+v", d)
	}
}

func TestNoticeCoreDailyCap(t *testing.T) {
	var c noticeCore
	now := t0
	for i := 1; i <= noticeDailyMax; i++ {
		d := c.decide(now, live(i), time.Time{})
		if !d.Send || d.N != i {
			t.Fatalf("notice %d: %+v", i, d)
		}
		c.attempt(now)
		c.sent(now, d.Waiting)
		now = now.Add(time.Minute)
	}
	// A rise, and no more that day.
	d := c.decide(now, live(noticeDailyMax+1), time.Time{})
	if d.Send {
		t.Fatalf("a %dth notice in a day: %+v", noticeDailyMax+1, d)
	}
	if want := t0.Add(noticeDay); !d.Wake.Equal(want) {
		t.Errorf("wake %v, want %v", d.Wake, want)
	}
	if d := c.decide(t0.Add(noticeDay-time.Second), live(noticeDailyMax+1), time.Time{}); d.Send {
		t.Errorf("a notice a second before the day is out: %+v", d)
	}
	if d := c.decide(t0.Add(noticeDay), live(noticeDailyMax+1), time.Time{}); !d.Send {
		t.Errorf("no notice when the day is out: %+v", d)
	}
}

func TestNoticeCoreFailureBacksOffAndRetriesTheSameCount(t *testing.T) {
	var c noticeCore
	d := c.decide(t0, live(1), time.Time{})
	c.attempt(t0)
	if !c.failed(t0) {
		t.Error("the first failure is not logged")
	}
	d = c.decide(t0.Add(time.Second), live(3), time.Time{})
	if d.Send {
		t.Fatalf("a retry a second after a failure: %+v", d)
	}
	if want := t0.Add(noticeBackoff); !d.Wake.Equal(want) {
		t.Errorf("wake %v, want %v", d.Wake, want)
	}
	// After the minute the count as it is then goes, the one now waiting.
	d = c.decide(t0.Add(noticeBackoff), live(3), time.Time{})
	if !d.Send || d.N != 3 {
		t.Errorf("after the back-off: %+v", d)
	}
	// A failure counts for the day's tally, and a success ends the back-off.
	if c.dayCalls != 1 {
		t.Errorf("calls %d", c.dayCalls)
	}
}

func TestNoticeCoreLogsAFailureOnceAMinute(t *testing.T) {
	var c noticeCore
	logged := 0
	for _, dt := range []time.Duration{0, time.Second, 30 * time.Second, 59 * time.Second, 60 * time.Second, 61 * time.Second, 119 * time.Second, 121 * time.Second} {
		if c.failed(t0.Add(dt)) {
			logged++
		}
	}
	if logged != 3 {
		t.Errorf("%d lines for 8 failures in 2 minutes, want 3 (at 0, 60 and 121 s)", logged)
	}
}

func TestNoticeCoreCapsTheCountAt999(t *testing.T) {
	var c noticeCore
	d := c.decide(t0, live(5000), time.Time{})
	if !d.Send || d.N != 999 || d.Waiting != 5000 {
		t.Errorf("%+v", d)
	}
	var c2 noticeCore
	if d := c2.decide(t0, live(999), time.Time{}); d.N != 999 {
		t.Errorf("%+v", d)
	}
}

// ---- the goroutine, with a fake clock, fake timers and a fake notifier ----

type fakeNotifier struct {
	mu    sync.Mutex
	calls []int
	err   error
	block chan struct{}
}

func (f *fakeNotifier) Notify(ctx context.Context, n int) error {
	f.mu.Lock()
	f.calls = append(f.calls, n)
	err, block := f.err, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeNotifier) got() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.calls...)
}

func (f *fakeNotifier) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// fakeTimers are timers on the test's clock: they fire when it passes them.
type fakeTimers struct {
	mu     sync.Mutex
	clk    *clock
	timers map[int]*fakeTimer
	next   int
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func (f *fakeTimers) timer(d time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timers == nil {
		f.timers = map[int]*fakeTimer{}
	}
	id := f.next
	f.next++
	tm := &fakeTimer{at: f.clk.Now().Add(d), ch: make(chan time.Time, 1)}
	f.timers[id] = tm
	return tm.ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.timers, id)
	}
}

// advance moves the clock and fires the timers it passed; it reports
// whether one fired.
func (f *fakeTimers) advance(d time.Duration) bool {
	f.clk.Advance(d)
	now := f.clk.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	fired := false
	for id, tm := range f.timers {
		if !tm.at.After(now) {
			tm.ch <- now
			delete(f.timers, id)
			fired = true
		}
	}
	return fired
}

func (f *fakeTimers) pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// noticeRig is an app whose notices run on a fake clock.
type noticeRig struct {
	*rig
	wd     *watchDaemon
	n      *fakeNotifier
	timers *fakeTimers
	stop   func()
	parks  atomic.Int64
	logs   []string
	logMu  sync.Mutex
}

func newNoticeRig(t *testing.T) *noticeRig {
	t.Helper()
	nr := &noticeRig{n: &fakeNotifier{}}
	wd := &watchDaemon{fakeWatcher: newFakeWatcher("aa")}
	nr.wd = wd
	nr.rig = newRig(t, func(c *Config) {
		wd.fakeDaemon = c.Daemon.(*fakeDaemon)
		c.Daemon = wd
		c.Notifier = nr.n
		c.Logf = func(format string, args ...any) {
			nr.logMu.Lock()
			defer nr.logMu.Unlock()
			nr.logs = append(nr.logs, format)
		}
	})
	nr.timers = &fakeTimers{clk: nr.clock}
	nr.app.noticeTimer = nr.timers.timer
	nr.app.noticeParked = func() { nr.parks.Add(1) }
	stop, err := nr.app.startNotices()
	if err != nil {
		t.Fatal(err)
	}
	nr.stop = stop
	t.Cleanup(stop)
	nr.parked(t, 1)
	return nr
}

// parked waits until the goroutine has parked n times in all.
func (nr *noticeRig) parked(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for nr.parks.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("the notices' goroutine parked %d times, want %d", nr.parks.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// change sets the watcher's state and waits for the goroutine to have
// judged it and parked again.
func (nr *noticeRig) change(t *testing.T, waiting int) {
	t.Helper()
	before := nr.parks.Load()
	nr.wd.set(func(s *WatchState) {
		s.Watching, s.Offline, s.Waiting = true, false, waiting
		s.Version += "x"
	})
	nr.parked(t, before+1)
}

// offline sets the watcher offline.
func (nr *noticeRig) setOffline(t *testing.T, off bool) {
	t.Helper()
	before := nr.parks.Load()
	nr.wd.set(func(s *WatchState) { s.Offline = off })
	nr.parked(t, before+1)
}

// pass moves the clock by d, and when that fires a timer waits for the
// goroutine to have judged again.
func (nr *noticeRig) pass(t *testing.T, d time.Duration) {
	t.Helper()
	before := nr.parks.Load()
	if nr.timers.advance(d) {
		nr.parked(t, before+1)
	}
}

func equalInts(a, b []int) bool { return reflect.DeepEqual(a, b) || (len(a) == 0 && len(b) == 0) }

func (nr *noticeRig) expect(t *testing.T, want ...int) {
	t.Helper()
	if got := nr.n.got(); !equalInts(got, want) {
		t.Fatalf("notifier told %v, want %v", got, want)
	}
}

func TestNoticeFirstCardNotifiesOnceAndARiseNotifies(t *testing.T) {
	nr := newNoticeRig(t)
	nr.expect(t)
	nr.change(t, 1)
	nr.expect(t, 1)
	// The state changes (a new version) with the same count: nothing.
	nr.change(t, 1)
	nr.change(t, 1)
	nr.expect(t, 1)
	// A rise: told at once, though inside the spacing.
	nr.change(t, 2)
	nr.expect(t, 1, 2)
	nr.change(t, 4)
	nr.expect(t, 1, 2, 4)
}

func TestNoticeSameCountDoesNotNotifyAgainWithinTwoMinutes(t *testing.T) {
	nr := newNoticeRig(t)
	nr.change(t, 1)
	nr.expect(t, 1)
	nr.change(t, 0)
	nr.change(t, 1)
	nr.expect(t, 1)
	nr.pass(t, time.Minute+59*time.Second)
	nr.expect(t, 1)
	// The spacing ends: the card that came since is announced.
	nr.pass(t, time.Second)
	nr.expect(t, 1, 1)
}

func TestNoticeAnAcceptedPollSuppresses(t *testing.T) {
	nr := newNoticeRig(t)
	nr.app.watchHold = 10 * time.Millisecond
	if !nr.app.lastPollTime().IsZero() {
		t.Fatal("a poll is recorded before any")
	}
	// A refused poll (no session) records nothing.
	if got := nr.poll(pollOK); got.Code != 200 || !nr.app.lastPollTime().IsZero() {
		t.Fatalf("a poll without a session: %d, recorded %v", got.Code, nr.app.lastPollTime())
	}
	// A real accepted poll from a logged-in session is what is recorded.
	s := nr.login(t)
	if got := nr.poll(pollOK, s); got.Code != 200 {
		t.Fatalf("poll: %d", got.Code)
	}
	if got := nr.app.lastPollTime(); !got.Equal(nr.clock.Now()) {
		t.Fatalf("the poll was recorded at %v, want %v", got, nr.clock.Now())
	}
	nr.pass(t, 20*time.Second)
	nr.change(t, 1)
	nr.expect(t)
	nr.pass(t, 39*time.Second)
	nr.expect(t)
	nr.pass(t, time.Second)
	nr.expect(t, 1)
}

func TestNoticeSuppressedByAPollThenSentWhenItIsQuiet(t *testing.T) {
	nr := newNoticeRig(t)
	nr.app.polled()
	nr.pass(t, 30*time.Second) // a poll 30 s old
	nr.change(t, 1)
	nr.expect(t)
	if nr.timers.pending() != 1 {
		t.Fatalf("%d timers, want the one for the poll's end", nr.timers.pending())
	}
	nr.pass(t, 29*time.Second)
	nr.expect(t)
	nr.pass(t, time.Second)
	nr.expect(t, 1)
}

func TestNoticeANewPollKeepsItQuiet(t *testing.T) {
	nr := newNoticeRig(t)
	nr.app.polled()
	nr.change(t, 1)
	nr.pass(t, 50*time.Second)
	nr.app.polled() // the page polled again
	nr.pass(t, 10*time.Second)
	nr.expect(t) // the first poll's end: not quiet yet, the next poll's
	nr.pass(t, 50*time.Second)
	nr.expect(t, 1)
}

func TestNoticeOfflineSuppressesAndRecovers(t *testing.T) {
	nr := newNoticeRig(t)
	nr.setOffline(t, true)
	before := nr.parks.Load()
	nr.wd.set(func(s *WatchState) { s.Waiting, s.Version = 3, "bb" })
	nr.parked(t, before+1)
	nr.expect(t)
	// Back live with cards waiting: the notice goes.
	nr.setOffline(t, false)
	nr.expect(t, 3)
}

func TestNoticeNotWatchingSuppresses(t *testing.T) {
	nr := newNoticeRig(t)
	before := nr.parks.Load()
	nr.wd.set(func(s *WatchState) { s.Watching, s.Waiting = false, 4 })
	nr.parked(t, before+1)
	nr.expect(t)
}

func TestNoticeDailyCap(t *testing.T) {
	nr := newNoticeRig(t)
	for i := 1; i <= noticeDailyMax; i++ {
		nr.change(t, i)
		nr.pass(t, time.Second)
	}
	if n := len(nr.n.got()); n != noticeDailyMax {
		t.Fatalf("%d calls, want %d", n, noticeDailyMax)
	}
	nr.change(t, noticeDailyMax+1)
	nr.change(t, noticeDailyMax+2)
	if n := len(nr.n.got()); n != noticeDailyMax {
		t.Fatalf("%d calls past the cap", n)
	}
	// The day is out: the card waiting then goes, as one notice.
	nr.pass(t, 24*time.Hour)
	if got := nr.n.got(); len(got) != noticeDailyMax+1 || got[len(got)-1] != noticeDailyMax+2 {
		t.Errorf("after the day: %v", got)
	}
}

func TestNoticeErrorBacksOffWithoutAStorm(t *testing.T) {
	nr := newNoticeRig(t)
	nr.n.setErr(errors.New("the notifier is down"))
	nr.change(t, 1)
	nr.expect(t, 1)
	// A state change inside the back-off does not call again.
	for i := 0; i < 5; i++ {
		nr.change(t, 1)
	}
	nr.pass(t, 59*time.Second)
	nr.expect(t, 1)
	// A minute after the failure it tries once more, and fails again.
	nr.pass(t, time.Second)
	nr.expect(t, 1, 1)
	nr.pass(t, 59*time.Second)
	nr.expect(t, 1, 1)
	// The notifier is back: the next try, a minute later, goes through, and
	// then it is quiet.
	nr.n.setErr(nil)
	nr.pass(t, time.Second)
	nr.expect(t, 1, 1, 1)
	nr.pass(t, 10*time.Minute)
	nr.expect(t, 1, 1, 1)
	// The log has fixed text and no more than a line a minute.
	nr.logMu.Lock()
	defer nr.logMu.Unlock()
	if len(nr.logs) != 2 {
		t.Errorf("%d log lines for two failures a minute apart: %q", len(nr.logs), nr.logs)
	}
	for _, l := range nr.logs {
		if strings.Contains(l, "down") || strings.Contains(l, "%") {
			t.Errorf("the log line holds something that is not fixed: %q", l)
		}
	}
}

func TestNoticeBackoffTriesTheCountThatWaitsThen(t *testing.T) {
	nr := newNoticeRig(t)
	nr.n.setErr(errors.New("x"))
	nr.change(t, 1)
	nr.change(t, 3)
	nr.n.setErr(nil)
	nr.pass(t, time.Minute)
	nr.expect(t, 1, 3)
}

func goroutinesIn(name string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), name)
		}
		buf = make([]byte, 2*len(buf))
	}
}

func waitGoroutines(t *testing.T, name string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for goroutinesIn(name) != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines in %s, want %d", goroutinesIn(name), name, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNoticeOpenStartsAndCloseStops(t *testing.T) {
	waitGoroutines(t, "noticeLoop", 0)
	r := newRig(t)
	wd := &watchDaemon{fakeWatcher: newFakeWatcher("aa"), fakeDaemon: r.daemon}
	n := &fakeNotifier{}
	x, err := Open(Config{Origin: testOrigin, RPID: testRPID, Daemon: wd, Notifier: n, ClientAddrHeader: testClientHeader})
	if err != nil {
		t.Fatal(err)
	}
	waitGoroutines(t, "noticeLoop", 1)
	wd.set(func(s *WatchState) { s.Waiting, s.Version = 2, "bb" })
	deadline := time.Now().Add(3 * time.Second)
	for len(n.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := n.got(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("notifier told %v", got)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	waitGoroutines(t, "noticeLoop", 0)
	// Closing again is nothing, and a change now tells no one.
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	wd.set(func(s *WatchState) { s.Waiting, s.Version = 9, "cc" })
	time.Sleep(20 * time.Millisecond)
	if got := n.got(); len(got) != 1 {
		t.Errorf("a closed app told the notifier: %v", got)
	}
	// The handler still answers.
	w := httptest.NewRecorder()
	x.ServeHTTP(w, newRequest("GET", "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Errorf("healthz %d", w.Code)
	}
}

func TestNoticeCloseCancelsACallInFlight(t *testing.T) {
	r := newRig(t)
	wd := &watchDaemon{fakeWatcher: newFakeWatcher("aa"), fakeDaemon: r.daemon}
	n := &fakeNotifier{block: make(chan struct{})}
	x, err := Open(Config{Origin: testOrigin, RPID: testRPID, Daemon: wd, Notifier: n, ClientAddrHeader: testClientHeader})
	if err != nil {
		t.Fatal(err)
	}
	wd.set(func(s *WatchState) { s.Waiting, s.Version = 1, "bb" })
	deadline := time.Now().Add(3 * time.Second)
	for len(n.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { x.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on a call in flight")
	}
	waitGoroutines(t, "noticeLoop", 0)
}

func TestNoticeNoNotifierStartsNothing(t *testing.T) {
	waitGoroutines(t, "noticeLoop", 0)
	r := newRig(t)
	wd := &watchDaemon{fakeWatcher: newFakeWatcher("aa"), fakeDaemon: r.daemon}
	x, err := Open(Config{Origin: testOrigin, RPID: testRPID, Daemon: wd, ClientAddrHeader: testClientHeader})
	if err != nil {
		t.Fatal(err)
	}
	wd.set(func(s *WatchState) { s.Waiting, s.Version = 2, "bb" })
	time.Sleep(20 * time.Millisecond)
	if n := goroutinesIn("noticeLoop"); n != 0 {
		t.Errorf("%d notice goroutines without a notifier", n)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	// New starts none either, and refuses a notifier it could not stop.
	if _, err := New(Config{Origin: testOrigin, RPID: testRPID, Daemon: wd, ClientAddrHeader: testClientHeader}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Origin: testOrigin, RPID: testRPID, Daemon: wd, Notifier: &fakeNotifier{}}); err == nil {
		t.Error("New took a notifier")
	}
	// A notifier with a daemon that does not watch is a mistake, said so.
	if _, err := Open(Config{Origin: testOrigin, RPID: testRPID, Daemon: r.daemon, Notifier: &fakeNotifier{}}); err == nil {
		t.Error("a notifier without a watcher was accepted")
	}
}

// ---- the payload is an integer, and nothing else can reach the notifier ----

func TestNoticePayloadIsAnIntegerOnly(t *testing.T) {
	// The port has one method, and its arguments are a context and an int.
	typ := reflect.TypeOf((*Notifier)(nil)).Elem()
	if typ.NumMethod() != 1 {
		t.Fatalf("the port has %d methods", typ.NumMethod())
	}
	m := typ.Method(0)
	if m.Name != "Notify" || m.Type.NumIn() != 2 || m.Type.NumOut() != 1 ||
		m.Type.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() || m.Type.In(1).Kind() != reflect.Int ||
		m.Type.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Errorf("Notify has the type %v", m.Type)
	}

	// The code that makes the call reads the count of the state and nothing
	// else of it: no card, id, binding, version or text appears in notice.go
	// as a selector, and the call is made with the core's integer.
	f, err := parser.ParseFile(token.NewFileSet(), "notice.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"Cards": true, "ID": true, "BindingSHA256": true, "Version": true, "Held": true,
		"Pending": true, "Recent": true, "Project": true, "Verb": true, "Feature": true, "Lead": true,
		"Credential": true, "Session": true, "Show": true, "List": true}
	calls := 0
	ast.Inspect(f, func(nd ast.Node) bool {
		switch x := nd.(type) {
		case *ast.SelectorExpr:
			if banned[x.Sel.Name] {
				t.Errorf("notice.go reads .%s", x.Sel.Name)
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Notify" {
				calls++
				if len(x.Args) != 2 {
					t.Errorf("a Notify call with %d arguments", len(x.Args))
					break
				}
				if arg, ok := x.Args[1].(*ast.SelectorExpr); !ok || arg.Sel.Name != "N" {
					t.Errorf("Notify is not given the decision's N")
				}
			}
		}
		return true
	})
	if calls != 1 {
		t.Errorf("%d calls of Notify in notice.go, want the one", calls)
	}
}

// TestNoticeToldOnlyIntegers drives states that hold cards and checks what
// reached the notifier is the integer.
func TestNoticeToldOnlyIntegers(t *testing.T) {
	nr := newNoticeRig(t)
	before := nr.parks.Load()
	nr.wd.set(func(s *WatchState) {
		s.Waiting, s.Version = 2, "bb"
		s.Cards = []WatchCard{{ID: "ap-20261121-0003", Kind: "decision", BindingSHA256: strings.Repeat("ab", 32)}, {ID: "ap-20261121-0004"}}
	})
	nr.parked(t, before+1)
	nr.expect(t, 2)
}
