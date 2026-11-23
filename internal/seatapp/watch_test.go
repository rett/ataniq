package seatapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWatcher is a Watcher whose state a test sets. before, when set, runs
// once inside Watch after the state was read and before it is returned, as a
// change that comes between a caller's WatchChanged and Watch would.
type fakeWatcher struct {
	mu     sync.Mutex
	st     WatchState
	ch     chan struct{}
	asked  int
	before func(w *fakeWatcher)
}

func newFakeWatcher(version string) *fakeWatcher {
	return &fakeWatcher{st: WatchState{Watching: true, Version: version}, ch: make(chan struct{})}
}

func (w *fakeWatcher) Watch() WatchState {
	w.mu.Lock()
	w.asked++
	s := w.st
	s.Cards = append([]WatchCard(nil), s.Cards...)
	hook := w.before
	w.before = nil
	w.mu.Unlock()
	if hook != nil {
		hook(w)
	}
	return s
}

func (w *fakeWatcher) WatchChanged() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

// set changes the state and wakes whoever holds the channel.
func (w *fakeWatcher) set(f func(*WatchState)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.st)
	close(w.ch)
	w.ch = make(chan struct{})
}

// watchCalls is how many times Watch was called.
func (w *fakeWatcher) watchCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.asked
}

// watchDaemon is the fake daemon with a Watcher. onRead runs at the start of
// each List and Show, as a change between a page's version and its read.
type watchDaemon struct {
	*fakeDaemon
	*fakeWatcher
	onRead func()
}

var _ Watcher = (*watchDaemon)(nil)

func (d *watchDaemon) List(ctx context.Context) (Listing, error) {
	if d.onRead != nil {
		d.onRead()
	}
	return d.fakeDaemon.List(ctx)
}

func (d *watchDaemon) Show(ctx context.Context, id string) (Card, error) {
	if d.onRead != nil {
		d.onRead()
	}
	return d.fakeDaemon.Show(ctx, id)
}

// newWatchRig is a rig whose daemon port is a Watcher in the state of
// version, with the hold given.
func newWatchRig(t *testing.T, version string, hold time.Duration) (*rig, *watchDaemon) {
	t.Helper()
	wd := &watchDaemon{fakeWatcher: newFakeWatcher(version)}
	r := newRig(t, func(c *Config) {
		wd.fakeDaemon = c.Daemon.(*fakeDaemon)
		c.Daemon = wd
	})
	r.app.watchHold = hold
	return r, wd
}

// watchReq is one poll's request.
func watchReq(ctx context.Context, body string, cookies ...*http.Cookie) *http.Request {
	req := newRequest("POST", "/api/watch", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// poll sends a poll and waits for its answer.
func (r *rig) poll(body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, watchReq(context.Background(), body, cookies...))
	return w
}

// pollAsync sends a poll in a goroutine; the answer arrives on the channel.
func (r *rig) pollAsync(ctx context.Context, body string, cookies ...*http.Cookie) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, watchReq(ctx, body, cookies...))
		out <- w
	}()
	return out
}

// pollAnswer decodes a poll's answer, which must be 200 and JSON.
func pollAnswer(t *testing.T, w *httptest.ResponseRecorder) watchAnswer {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("poll: %d %s", w.Code, w.Body)
	}
	var a watchAnswer
	dec := json.NewDecoder(w.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		t.Fatalf("poll answer: %v: %s", err, w.Body)
	}
	return a
}

// held is how many polls are held, of all sessions.
func (r *rig) held() int {
	r.app.sess.mu.Lock()
	defer r.app.sess.mu.Unlock()
	n := 0
	for _, s := range r.app.sess.m {
		n += len(s.kicks)
	}
	return n
}

// waitHeld waits until n polls are held.
func (r *rig) waitHeld(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.held() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d held polls, want %d", r.held(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// within reads one answer from ch, or fails after d.
func within(t *testing.T, ch <-chan *httptest.ResponseRecorder, d time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-ch:
		return w
	case <-time.After(d):
		t.Fatal("no answer in time")
		return nil
	}
}

const pollOK = `{"version":"aa","card":"","binding":""}`

// TestSeatWatchHoldsAndWakes: a poll with the current version is held
// without asking the gate, and a change of the state answers it at once with
// "changed" and the new version.
func TestSeatWatchHoldsAndWakes(t *testing.T) {
	r, wd := newWatchRig(t, "aa", 20*time.Second)
	s := r.login(t)
	asked := r.daemon.count()
	got := r.pollAsync(context.Background(), pollOK, s)
	r.waitHeld(t, 1)
	select {
	case <-got:
		t.Fatal("answered before anything changed")
	case <-time.After(30 * time.Millisecond):
	}
	wd.set(func(st *WatchState) { st.Version, st.Waiting = "bb", 3 })
	a := pollAnswer(t, within(t, got, 3*time.Second))
	if a.State != "changed" || a.Version != "bb" || a.Waiting != 3 {
		t.Errorf("answer %+v", a)
	}
	if n := r.daemon.count(); n != asked {
		t.Errorf("a poll made %d calls of the daemon", n-asked)
	}
	if r.held() != 0 {
		t.Error("the poll is still held")
	}
}

// TestSeatWatchAnswersAtOnceWhenVersionDiffers: a page that is older than the
// state, or has none, is told "changed" without a hold.
func TestSeatWatchAnswersAtOnceWhenVersionDiffers(t *testing.T) {
	r, _ := newWatchRig(t, "bb", time.Minute)
	s := r.login(t)
	for _, v := range []string{"aa", ""} {
		start := time.Now()
		a := pollAnswer(t, r.poll(`{"version":"`+v+`","card":"","binding":""}`, s))
		if a.State != "changed" || a.Version != "bb" {
			t.Errorf("version %q: %+v", v, a)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("version %q: held", v)
		}
	}
	checkHeaders(t, "watch", r.poll(pollOK, s))
}

// TestSeatWatchSameAfterTheHold: with nothing changed the poll ends at the
// hold with "same", and the same version.
func TestSeatWatchSameAfterTheHold(t *testing.T) {
	r, wd := newWatchRig(t, "aa", 60*time.Millisecond)
	s := r.login(t)
	start := time.Now()
	a := pollAnswer(t, r.poll(pollOK, s))
	if a.State != "same" || a.Version != "aa" {
		t.Errorf("answer %+v", a)
	}
	if d := time.Since(start); d < 55*time.Millisecond {
		t.Errorf("answered after %v, before the hold", d)
	}
	if r.held() != 0 || wd.watchCalls() == 0 {
		t.Errorf("held %d", r.held())
	}
}

// TestSeatWatchHoldIsUnderTheWriteTimeout: the app's hold is shorter than
// the server's write timeout and the proxy's 30 seconds.
func TestSeatWatchHoldIsUnderTheWriteTimeout(t *testing.T) {
	if watchHold != 25*time.Second || watchHold >= Server(nil).WriteTimeout {
		t.Errorf("hold %v, write timeout %v", watchHold, Server(nil).WriteTimeout)
	}
	r := newRig(t)
	if r.app.watchHold != watchHold {
		t.Errorf("the app's default hold is %v", r.app.watchHold)
	}
}

// TestSeatWatchEndedWithoutASession: no session, a planted cookie, a logged
// out one and an idle one all get "ended", and the watcher is not asked.
func TestSeatWatchEndedWithoutASession(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	planted := &http.Cookie{Name: SessionCookie, Value: b64.EncodeToString(make([]byte, 32))}
	loggedOut := r.login(t)
	r.postWith("/api/logout", "{}", loggedOut)
	idle := r.login(t)
	r.clock.Advance(SessionIdle)
	for name, cookies := range map[string][]*http.Cookie{
		"none": nil, "planted": {planted}, "logged out": {loggedOut}, "idle": {idle},
		"two": {planted, planted},
	} {
		a := pollAnswer(t, r.poll(pollOK, cookies...))
		if a.State != "ended" || a.Card != "unknown" {
			t.Errorf("%s: %+v", name, a)
		}
	}
	if wd.watchCalls() != 0 {
		t.Errorf("the watcher was asked %d times without a session", wd.watchCalls())
	}
}

// TestSeatWatchPollsDoNotExtendASession: a poll only looks at the session.
// With polls all along, the session ends at its idle time, counted from its
// last real use.
func TestSeatWatchPollsDoNotExtendASession(t *testing.T) {
	r, _ := newWatchRig(t, "bb", time.Minute)
	s := r.login(t)
	// Polls at 5, 10, ... 25 minutes: the session is live at each.
	for i := 0; i < 5; i++ {
		r.clock.Advance(5 * time.Minute)
		if a := pollAnswer(t, r.poll(pollOK, s)); a.State != "changed" {
			t.Fatalf("after %d min: %+v", 5*(i+1), a)
		}
	}
	r.clock.Advance(5*time.Minute + time.Second)
	if a := pollAnswer(t, r.poll(pollOK, s)); a.State != "ended" {
		t.Errorf("an idle session kept alive by polls: %+v", a)
	}
	if _, ok := r.sessionOf(s.Value); ok {
		t.Error("the session is live")
	}
	// A real use in between does extend it, so the polls are not what ended
	// the first.
	s2 := r.login(t)
	for i := 0; i < 5; i++ {
		r.clock.Advance(10 * time.Minute)
		if w := r.getWith("/cards/", s2); w.Code != 200 {
			t.Fatalf("read %d: %d", i, w.Code)
		}
		if a := pollAnswer(t, r.poll(pollOK, s2)); a.State != "changed" {
			t.Fatalf("poll %d: %+v", i, a)
		}
	}
}

// TestSeatSessionsLookDoesNotMarkUsed: look returns the session and leaves
// its time of use; get moves it.
func TestSeatSessionsLookDoesNotMarkUsed(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	ss := newSessions(clk.Now)
	v, k, err := newToken(&counterReader{})
	if err != nil {
		t.Fatal(err)
	}
	ss.add(k, Session{Credential: "c"})
	used := func() time.Time {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		return ss.m[k].used
	}
	start := used()
	clk.Advance(10 * time.Minute)
	s, gone, ok := ss.look(v)
	if !ok || s.Credential != "c" || s.key != k || gone == nil {
		t.Fatalf("look: %+v %v", s, ok)
	}
	if !used().Equal(start) {
		t.Error("look marked the session used")
	}
	if _, ok := ss.get(v); !ok || used().Equal(start) {
		t.Error("get did not mark the session used")
	}
	clk.Advance(SessionIdle)
	if _, _, ok := ss.look(v); ok || ss.count() != 0 {
		t.Error("look returned an idle session")
	}
	if _, _, ok := ss.look("short"); ok {
		t.Error("look returned a session for a value that is not a token")
	}
	select {
	case <-gone:
	default:
		t.Error("the session's gone channel is not closed after its removal")
	}
}

// TestSeatWatchEndsWithTheSession: a logout, or the session's idle time
// passing in the hold, ends a held poll with "ended".
func TestSeatWatchEndsWithTheSession(t *testing.T) {
	r, _ := newWatchRig(t, "aa", 30*time.Second)
	s := r.login(t)
	got := r.pollAsync(context.Background(), pollOK, s)
	r.waitHeld(t, 1)
	r.postWith("/api/logout", "{}", s)
	if a := pollAnswer(t, within(t, got, 3*time.Second)); a.State != "ended" {
		t.Errorf("after a logout: %+v", a)
	}

	r2, _ := newWatchRig(t, "aa", 80*time.Millisecond)
	s2 := r2.login(t)
	got = r2.pollAsync(context.Background(), pollOK, s2)
	r2.waitHeld(t, 1)
	r2.clock.Advance(SessionIdle)
	if a := pollAnswer(t, within(t, got, 3*time.Second)); a.State != "ended" {
		t.Errorf("after the idle time: %+v", a)
	}
}

// TestSeatWatchOffline: a port that does not watch, one that says it is
// offline, and an app whose daemon is no Watcher answer "offline" at once,
// and never hold.
func TestSeatWatchOffline(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	for name, f := range map[string]func(*WatchState){
		"not watching": func(st *WatchState) { *st = WatchState{} },
		"offline":      func(st *WatchState) { *st = WatchState{Watching: true, Version: "aa", Waiting: 2, Offline: true} },
	} {
		wd.set(f)
		start := time.Now()
		a := pollAnswer(t, r.poll(pollOK, s))
		if a.State != "offline" || a.Card != "unknown" {
			t.Errorf("%s: %+v", name, a)
		}
		if time.Since(start) > 2*time.Second || r.held() != 0 {
			t.Errorf("%s: held", name)
		}
	}
	plain := newRig(t)
	if _, ok := plain.app.watcher(); ok {
		t.Fatal("the plain fake is a Watcher")
	}
	ps := plain.login(t)
	if a := pollAnswer(t, plain.poll(pollOK, ps)); a.State != "offline" || a.Card != "unknown" {
		t.Errorf("no watcher: %+v", a)
	}
	// Going offline while held wakes the poll, which says so.
	wd.set(func(st *WatchState) { *st = WatchState{Watching: true, Version: "aa"} })
	got := r.pollAsync(context.Background(), pollOK, s)
	r.waitHeld(t, 1)
	wd.set(func(st *WatchState) { st.Offline = true })
	if a := pollAnswer(t, within(t, got, 3*time.Second)); a.State != "offline" {
		t.Errorf("went offline while held: %+v", a)
	}
}

// TestSeatWatchHeldPollsPerSession: a session holds a poll for each of three
// tabs; the fourth takes the place of the oldest, which is answered
// "replaced" at once and is no longer held; another session holds its own.
func TestSeatWatchHeldPollsPerSession(t *testing.T) {
	r, wd := newWatchRig(t, "aa", 30*time.Second)
	s := r.login(t)
	other := r.login(t)
	var tabs []<-chan *httptest.ResponseRecorder
	for i := 1; i <= maxHeldPolls; i++ {
		tabs = append(tabs, r.pollAsync(context.Background(), pollOK, s))
		r.waitHeld(t, i)
	}
	// Up to the most, no poll takes another's place.
	select {
	case <-tabs[0]:
		t.Fatal("a poll was answered with the most of held polls not passed")
	case <-time.After(30 * time.Millisecond):
	}
	fourth := r.pollAsync(context.Background(), pollOK, s)
	a := pollAnswer(t, within(t, tabs[0], 3*time.Second))
	if a.State != "replaced" || a.Version != "aa" || a.Card != "unknown" {
		t.Errorf("the oldest poll: %+v", a)
	}
	r.waitHeld(t, maxHeldPolls)
	for i, ch := range append(tabs[1:], fourth) {
		select {
		case <-ch:
			t.Fatalf("held poll %d was answered", i)
		case <-time.After(10 * time.Millisecond):
		}
	}
	fifth := r.pollAsync(context.Background(), pollOK, s)
	if a := pollAnswer(t, within(t, tabs[1], 3*time.Second)); a.State != "replaced" {
		t.Errorf("the next oldest poll: %+v", a)
	}
	r.waitHeld(t, maxHeldPolls)
	third := r.pollAsync(context.Background(), pollOK, other)
	r.waitHeld(t, maxHeldPolls+1)
	wd.set(func(st *WatchState) { st.Version = "bb" })
	held := map[string]<-chan *httptest.ResponseRecorder{"tab 3": tabs[2], "fourth": fourth, "fifth": fifth, "other": third}
	for name, ch := range held {
		if a := pollAnswer(t, within(t, ch, 3*time.Second)); a.State != "changed" || a.Version != "bb" {
			t.Errorf("%s: %+v", name, a)
		}
	}
	r.waitHeld(t, 0)
}

// TestSeatWatchTwoTabsDoNotKickEachOther: two tabs of one session that poll as
// watch.js does keep both polls held, and a replaced poll is not asked again
// before a hold's length has passed. Traced over a simulated ten minutes with
// the seat's cap of held polls and the script's own waits, for two to five
// tabs: up to four tabs (the three held and one that replaces) make three
// polls each in a minute at most, within the credential's limit; five tabs
// are the case "close the extra tabs" names and are only logged.
func TestSeatWatchTwoTabsDoNotKickEachOther(t *testing.T) {
	js := watchScript(t)
	m := regexp.MustCompile(`REPLACED_WAIT = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no REPLACED_WAIT")
	}
	replacedWait, _ := strconv.Atoi(m[1])
	const minGap = 2000
	hold := int(watchHold / time.Millisecond)
	if replacedWait < hold {
		t.Fatalf("a replaced poll waits %d ms, under a hold of %d", replacedWait, hold)
	}
	// Each tab is {when it polls next}; a held poll is {tab, began}. Time
	// advances to the next event: a tab's poll, or the end of a hold.
	for tabs := 2; tabs <= 5; tabs++ {
		type held struct {
			tab, began int
		}
		next := make([]int, tabs)
		for i := range next {
			next[i] = i * 100 // the tabs do not start together
		}
		var holds []held
		var starts []int
		const horizon = 10 * 60 * 1000
		for now := 0; now < horizon; {
			// The next event.
			at := horizon
			for _, n := range next {
				if n >= 0 && n < at {
					at = n
				}
			}
			for _, h := range holds {
				if h.began+hold < at {
					at = h.began + hold
				}
			}
			now = at
			// Holds that end now answer "same": the tab asks again after
			// the gap the script leaves, none when the poll was long.
			var keep []held
			for _, h := range holds {
				if h.began+hold <= now {
					next[h.tab] = h.began + hold + max(0, minGap-hold)
				} else {
					keep = append(keep, h)
				}
			}
			holds = keep
			for tab := range next {
				if next[tab] < 0 || next[tab] > now {
					continue
				}
				starts = append(starts, now)
				if len(holds) >= maxHeldPolls {
					old := holds[0]
					holds = holds[1:]
					next[old.tab] = now + replacedWait
				}
				holds = append(holds, held{tab, now})
				next[tab] = -1 // held
			}
		}
		perMinute := 0
		for i := range starts {
			n := 0
			for _, s := range starts[i:] {
				if s < starts[i]+60000 {
					n++
				}
			}
			if n > perMinute {
				perMinute = n
			}
		}
		t.Logf("%d tabs: at most %d polls in a minute", tabs, perMinute)
		// A tab polls once a hold, three times in a minute at most, whether
		// its poll was held or replaced.
		if tabs <= maxHeldPolls+1 && perMinute > 3*tabs {
			t.Errorf("%d tabs make %d polls in a minute, more than three each", tabs, perMinute)
		}
		if tabs <= maxHeldPolls+1 && perMinute > watchRequests {
			t.Errorf("%d tabs make %d polls in a minute: the limit of %d would be used up", tabs, perMinute, watchRequests)
		}
	}
}

// TestSeatWatchTwoTabsKeepBothHeld: through the handler, two tabs of one
// session are both held, answer nobody "same" or "replaced", and the limiter
// has counted exactly the two polls that started.
func TestSeatWatchTwoTabsKeepBothHeld(t *testing.T) {
	r, wd := newWatchRig(t, "aa", 30*time.Second)
	s := r.login(t)
	a := r.pollAsync(context.Background(), pollOK, s)
	r.waitHeld(t, 1)
	b := r.pollAsync(context.Background(), pollOK, s)
	r.waitHeld(t, 2)
	for _, ch := range []<-chan *httptest.ResponseRecorder{a, b} {
		select {
		case w := <-ch:
			t.Fatalf("a tab's poll was answered: %s", w.Body)
		case <-time.After(30 * time.Millisecond):
		}
	}
	wd.set(func(st *WatchState) { st.Version = "bb" })
	for _, ch := range []<-chan *httptest.ResponseRecorder{a, b} {
		if got := pollAnswer(t, within(t, ch, 3*time.Second)); got.State != "changed" {
			t.Errorf("%+v", got)
		}
	}
	// Two polls started, two of the credential's twelve are used.
	// (The version is now "bb", so a poll with "aa" is answered at once.)
	used := 0
	for r.poll(pollOK, s).Code == 200 {
		used++
	}
	if used != watchRequests-2 {
		t.Errorf("%d polls were left of %d after two", used, watchRequests)
	}
}

// TestSeatWatchLimiter: a credential's polls are bounded at 12 a minute,
// whichever session sent them, apart from the reads' bound.
func TestSeatWatchLimiter(t *testing.T) {
	r, wd := newWatchRig(t, "bb", time.Minute)
	s := r.login(t)
	s2 := r.login(t)
	for i := 0; i < watchRequests; i++ {
		c := s
		if i%2 == 1 {
			c = s2
		}
		if a := pollAnswer(t, r.poll(pollOK, c)); a.State != "changed" {
			t.Fatalf("poll %d: %+v", i, a)
		}
	}
	calls := wd.watchCalls()
	w := r.poll(pollOK, s)
	if w.Code != http.StatusTooManyRequests || strings.TrimSpace(w.Body.String()) != `{"error":"rate_limited"}` {
		t.Errorf("past the limit: %d %s", w.Code, w.Body)
	}
	if wd.watchCalls() != calls {
		t.Error("the watcher was asked for a poll past the limit")
	}
	// The reads are not used up by polls.
	if w := r.getWith("/cards/", s); w.Code != 200 {
		t.Errorf("a read after the polls: %d", w.Code)
	}
	r.clock.Advance(watchWindow)
	if a := pollAnswer(t, r.poll(pollOK, s)); a.State != "changed" {
		t.Errorf("a minute later: %+v", a)
	}
	// And the polls are not used up by the reads.
	r2, _ := newWatchRig(t, "bb", time.Minute)
	s3 := r2.login(t)
	for i := 0; i < readRequests; i++ {
		if w := r2.getWith("/cards/", s3); w.Code != 200 {
			t.Fatalf("read %d: %d", i, w.Code)
		}
	}
	if w := r2.getWith("/cards/", s3); w.Code != http.StatusTooManyRequests {
		t.Fatalf("read past the limit: %d", w.Code)
	}
	if a := pollAnswer(t, r2.poll(pollOK, s3)); a.State != "changed" {
		t.Errorf("a poll after the reads were used up: %+v", a)
	}
}

// TestSeatWatchBodyIsStrict: a body that is not exactly the three fields of
// the forms they take is refused 400, 413 for one too long, and reaches
// nothing.
func TestSeatWatchBodyIsStrict(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	hex64 := strings.Repeat("ab", 32)
	for name, body := range map[string]string{
		"empty":              ``,
		"not json":           `version`,
		"an array":           `[]`,
		"null":               `null`,
		"unknown field":      `{"version":"aa","card":"","binding":"","extra":1}`,
		"data after":         pollOK + `{}`,
		"trailing garbage":   pollOK + ` x`,
		"wrong type":         `{"version":1,"card":"","binding":""}`,
		"upper-case hex":     `{"version":"AA","card":"","binding":""}`,
		"not hex":            `{"version":"xyz","card":"","binding":""}`,
		"version too long":   `{"version":"` + strings.Repeat("a", maxWatchVersion+1) + `","card":"","binding":""}`,
		"card not a card":    `{"version":"aa","card":"../x","binding":""}`,
		"card too long":      `{"version":"aa","card":"ap-` + strings.Repeat("a", 60) + `","binding":""}`,
		"card upper":         `{"version":"aa","card":"AP-1","binding":""}`,
		"binding no card":    `{"version":"aa","card":"","binding":"` + hex64 + `"}`,
		"binding not hex":    `{"version":"aa","card":"` + testCardID + `","binding":"zz"}`,
		"binding too long":   `{"version":"aa","card":"` + testCardID + `","binding":"` + hex64 + `0"}`,
		"a nested object":    `{"version":{"a":1},"card":"","binding":""}`,
		"a number as a card": `{"version":"aa","card":7,"binding":""}`,
	} {
		w := r.poll(body, s)
		if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != `{"error":"bad_request"}` {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	big := `{"version":"aa","card":"","binding":"` + strings.Repeat("a", MaxBody) + `"}`
	if w := r.poll(big, s); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body past the cap: %d %s", w.Code, w.Body)
	}
	if wd.watchCalls() != 0 || r.held() != 0 {
		t.Errorf("a refused body reached the watcher: %d", wd.watchCalls())
	}
	// Whole and valid, with a card and its binding.
	ok := `{"version":"aa","card":"` + testCardID + `","binding":"` + hex64 + `"}`
	wd.set(func(st *WatchState) { st.Version = "bb" })
	if a := pollAnswer(t, r.poll(ok, s)); a.State != "changed" {
		t.Errorf("a valid body: %+v", a)
	}
}

// TestSeatWatchCardStates: the card of the answer, from the cards the state
// lists.
func TestSeatWatchCardStates(t *testing.T) {
	bind := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	listed := []WatchCard{{ID: testCardID, Kind: "approval", BindingSHA256: bind}, {ID: "ap-20261122-0002", Kind: "approval", BindingSHA256: other}}
	for _, c := range []struct {
		name  string
		st    WatchState
		card  string
		bind  string
		state string
		want  string
	}{
		{"listed, same binding", WatchState{Watching: true, Version: "bb", Cards: listed}, testCardID, bind, "changed", "live"},
		{"listed, another binding", WatchState{Watching: true, Version: "bb", Cards: listed}, testCardID, other, "changed", "changed"},
		{"listed, no binding sent", WatchState{Watching: true, Version: "bb", Cards: listed}, testCardID, "", "changed", "live"},
		{"absent, whole list", WatchState{Watching: true, Version: "bb", Cards: listed[1:]}, testCardID, bind, "changed", "gone"},
		{"absent, empty list", WatchState{Watching: true, Version: "bb"}, testCardID, bind, "changed", "gone"},
		{"absent, more", WatchState{Watching: true, Version: "bb", Cards: listed[1:], More: true, Waiting: 200}, testCardID, bind, "changed", "unknown"},
		{"listed, more", WatchState{Watching: true, Version: "bb", Cards: listed, More: true, Waiting: 200}, testCardID, bind, "changed", "live"},
		{"listed, more, another binding", WatchState{Watching: true, Version: "bb", Cards: listed, More: true}, testCardID, other, "changed", "changed"},
		{"offline", WatchState{Watching: true, Version: "bb", Cards: listed, Offline: true}, testCardID, bind, "offline", "unknown"},
		{"not watching", WatchState{Cards: listed}, testCardID, bind, "offline", "unknown"},
		{"no card named", WatchState{Watching: true, Version: "bb", Cards: listed}, "", "", "changed", "unknown"},
	} {
		r, wd := newWatchRig(t, "bb", time.Minute)
		wd.set(func(st *WatchState) { *st = c.st })
		s := r.login(t)
		body, _ := json.Marshal(map[string]string{"version": "aa", "card": c.card, "binding": c.bind})
		a := pollAnswer(t, r.poll(string(body), s))
		if a.State != c.state || a.Card != c.want {
			t.Errorf("%s: state %q card %q, want %q %q", c.name, a.State, a.Card, c.state, c.want)
		}
	}
	// The answer holds no card text: fixed words and numbers.
	r, wd := newWatchRig(t, "bb", time.Minute)
	wd.set(func(st *WatchState) { st.Cards = listed; st.Waiting = 2 })
	w := r.poll(`{"version":"aa","card":"","binding":""}`, r.login(t))
	if strings.Contains(w.Body.String(), "ap-") || strings.Contains(w.Body.String(), bind) {
		t.Errorf("the answer names a card: %s", w.Body)
	}
	// A card that goes while the poll is held: the wake says so.
	wd.set(func(st *WatchState) { st.Version, st.Cards = "aa", listed })
	s := r.login(t)
	got := r.pollAsync(context.Background(), `{"version":"aa","card":"`+testCardID+`","binding":"`+bind+`"}`, s)
	r.waitHeld(t, 1)
	wd.set(func(st *WatchState) { st.Version, st.Cards = "cc", listed[1:] })
	if a := pollAnswer(t, within(t, got, 3*time.Second)); a.State != "changed" || a.Card != "gone" || a.Version != "cc" {
		t.Errorf("a card decided while held: %+v", a)
	}
}

// TestSeatWatchTakesTheChannelBeforeTheState: a change that comes between the
// poll's WatchChanged and its Watch is not missed: the hold wakes at once,
// and the answer is of the new state.
func TestSeatWatchTakesTheChannelBeforeTheState(t *testing.T) {
	r, wd := newWatchRig(t, "aa", 20*time.Second)
	s := r.login(t)
	wd.before = func(w *fakeWatcher) {
		// The state the poll reads is "aa"; the change comes right after.
		w.set(func(st *WatchState) { st.Version = "bb" })
	}
	start := time.Now()
	a := pollAnswer(t, r.poll(pollOK, s))
	if a.State != "changed" || a.Version != "bb" {
		t.Errorf("answer %+v", a)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the change between the channel and the state was missed until the hold's end")
	}
}

// TestSeatWatchLeavesNoGoroutine: held polls end with their request, a newer
// poll, a change or the hold, and the app has no goroutine left of any.
func TestSeatWatchLeavesNoGoroutine(t *testing.T) {
	r, wd := newWatchRig(t, "aa", 40*time.Millisecond)
	s := r.login(t)
	s2 := r.login(t)
	runtime.GC()
	before := runtime.NumGoroutine()
	for round := 0; round < 3; round++ {
		// Ended by the request.
		r.app.watchHold = time.Minute
		ctx, cancel := context.WithCancel(context.Background())
		var outs []<-chan *httptest.ResponseRecorder
		for i := 0; i < 7; i++ { // four of s, of which one is replaced, and three of s2
			c := s
			if i%2 == 1 {
				c = s2
			}
			outs = append(outs, r.pollAsync(ctx, pollOK, c))
		}
		r.waitHeld(t, 2*maxHeldPolls)
		cancel()
		for _, o := range outs {
			within(t, o, 3*time.Second)
		}
		// Ended by a newer poll (past the most held) and by a change.
		a := r.pollAsync(context.Background(), pollOK, s)
		r.waitHeld(t, 1)
		var rest []<-chan *httptest.ResponseRecorder
		for i := 1; i < maxHeldPolls; i++ {
			rest = append(rest, r.pollAsync(context.Background(), pollOK, s))
			r.waitHeld(t, i+1)
		}
		rest = append(rest, r.pollAsync(context.Background(), pollOK, s))
		within(t, a, 3*time.Second)
		wd.set(func(st *WatchState) { st.Version += "1" })
		for _, b := range rest {
			within(t, b, 3*time.Second)
		}
		wd.set(func(st *WatchState) { st.Version = "aa" })
		// Ended by the hold.
		r.app.watchHold = 20 * time.Millisecond
		within(t, r.pollAsync(context.Background(), pollOK, s), 3*time.Second)
		r.clock.Advance(watchWindow)
	}
	if r.held() != 0 {
		t.Errorf("%d polls still held", r.held())
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("%d goroutines, %d before:\n%s", runtime.NumGoroutine(), before, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSeatWatchRequestEndLeavesNoAnswer: a poll whose request ended writes
// nothing.
func TestSeatWatchRequestEndLeavesNoAnswer(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.h.ServeHTTP(w, watchReq(ctx, pollOK, s))
		close(done)
	}()
	r.waitHeld(t, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the poll outlived its request")
	}
	if w.Body.Len() != 0 {
		t.Errorf("an answer for an ended request: %s", w.Body)
	}
	r.waitHeld(t, 0)
}

// TestSeatWatchPostChecks: a poll passes the checks of every other POST.
func TestSeatWatchPostChecks(t *testing.T) {
	r, wd := newWatchRig(t, "bb", time.Minute)
	s := r.login(t)
	send := func(header map[string]string) *httptest.ResponseRecorder {
		req := watchReq(context.Background(), pollOK, s)
		for k, v := range header {
			switch {
			case k == "Host":
				req.Host = v
			case v == "":
				req.Header.Del(k)
			default:
				req.Header.Set(k, v)
			}
		}
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		return w
	}
	for name, c := range map[string]struct {
		header map[string]string
		status int
	}{
		"no origin":          {map[string]string{"Origin": ""}, 403},
		"another origin":     {map[string]string{"Origin": "https://evil.example"}, 403},
		"origin with a path": {map[string]string{"Origin": testOrigin + "/"}, 403},
		"another host":       {map[string]string{"Host": "evil.example"}, 403},
		"cross-site":         {map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		"same-site":          {map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		"none":               {map[string]string{"Sec-Fetch-Site": "none"}, 403},
		"no fetch metadata":  {map[string]string{"Sec-Fetch-Site": ""}, 403},
		"form encoded":       {map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		"plain text":         {map[string]string{"Content-Type": "text/plain"}, 415},
		"no content type":    {map[string]string{"Content-Type": ""}, 415},
	} {
		if w := send(c.header); w.Code != c.status {
			t.Errorf("%s: %d, want %d", name, w.Code, c.status)
		}
	}
	if wd.watchCalls() != 0 {
		t.Errorf("a refused request reached the watcher %d times", wd.watchCalls())
	}
	if w := send(nil); w.Code != 200 {
		t.Errorf("a well-formed poll: %d", w.Code)
	}
	if w := r.getWith("/api/watch", s); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/watch: %d", w.Code)
	}
}

// TestSeatWatchVersionIsTakenBeforeThePageRead: the card list and the Card
// page carry the version the watcher had when the page's read began, even
// when the state changed during the read, and the Card page carries the card
// and the binding it shows.
func TestSeatWatchVersionIsTakenBeforeThePageRead(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	wd.onRead = func() { wd.set(func(st *WatchState) { st.Version += "b" }) }
	list := r.getWith("/cards/", s).Body.String()
	for _, want := range []string{`id="watch"`, `data-live="yes"`, `data-version="aa"`, `<script src="/static/watch.js"></script>`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %s", want)
		}
	}
	if strings.Contains(list, "data-card") {
		t.Error("the list names a card")
	}
	// The state moved on during the list's read: the page's version is the
	// older, so its first poll is answered "changed".
	if st := wd.Watch(); st.Version != "aab" {
		t.Fatalf("the hook did not run: %q", st.Version)
	}
	wd.set(func(st *WatchState) { st.Version = "cc" })
	wd.onRead = nil
	card := r.getWith("/cards/"+testCardID, s).Body.String()
	for _, want := range []string{`data-live="yes"`, `data-version="cc"`, `data-card="` + testCardID + `"`, `data-binding="` + strings.Repeat("ab", 32) + `"`,
		`<script src="/static/watch.js"></script>`, `<script src="/static/decide.js"></script>`} {
		if !strings.Contains(card, want) {
			t.Errorf("the card page lacks %s", want)
		}
	}
	if n := strings.Count(card, "/static/watch.js"); n != 1 {
		t.Errorf("the card page loads watch.js %d times", n)
	}
	wd.onRead = func() { wd.set(func(st *WatchState) { st.Version = "dd" }) }
	card = r.getWith("/cards/"+testCardID, s).Body.String()
	if !strings.Contains(card, `data-version="cc"`) {
		t.Errorf("the card page's version is not the one before its read: %s", card)
	}
	if a := pollAnswer(t, r.poll(`{"version":"cc","card":"","binding":""}`, s)); a.State != "changed" || a.Version != "dd" {
		t.Errorf("a page whose version is behind: %+v", a)
	}
	// The other pages do not poll.
	for _, path := range []string{"/", "/pair/", "/login/", "/cards/" + testCardID + "/paths/1"} {
		if body := r.getWith(path, s).Body.String(); strings.Contains(body, "watch") {
			t.Errorf("%s mentions watch", path)
		}
	}
}

// TestSeatWatchCardPageOfACardThatIsNotPending: only a pending card is named
// on its page. A decided, ended, expired or refused card is not in the
// pending set by design, so naming it would make the first answer past a hold
// "gone", and a banner "decided elsewhere" on a card that moved long ago, or
// by this session's own decision (the page it reads afterwards is of the
// decided card). Such a page carries data-still, which keeps it from
// reloading, and its poll can say nothing of a card.
func TestSeatWatchCardPageOfACardThatIsNotPending(t *testing.T) {
	r, _ := newWatchRig(t, "aa", 40*time.Millisecond)
	s := r.login(t)
	pending := func() string { return r.getWith("/cards/"+testCardID, s).Body.String() }
	body := pending()
	for _, want := range []string{`data-card="` + testCardID + `"`, `data-binding="`} {
		if !strings.Contains(body, want) {
			t.Errorf("a pending card's page lacks %s", want)
		}
	}
	if strings.Contains(body, "data-still") {
		t.Error("a pending card's page is marked still")
	}
	for name, mod := range map[string]func(c *Card){
		"decided":      func(c *Card) { c.State = "approved"; c.Ended = true },
		"decided open": func(c *Card) { c.State = "approved" },
		"ended":        func(c *Card) { c.Ended = true },
		"expired":      func(c *Card) { c.State = "expired"; c.Ended = true },
		"refused":      func(c *Card) { c.State = "refused"; c.Refused = true; c.Ended = true },
		"denied":       func(c *Card) { c.State = "denied"; c.Ended = true },
	} {
		c := sampleCard()
		mod(&c)
		r.daemon.card = c
		body := pending()
		for _, bad := range []string{"data-card", "data-binding"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: the page of a card that is not pending carries %s", name, bad)
			}
		}
		if !strings.Contains(body, `data-still="yes"`) || !strings.Contains(body, `data-live="yes"`) {
			t.Errorf("%s: the page is not live and still", name)
		}
		// Its poll names no card, and so is never told "gone".
		a := pollAnswer(t, r.poll(`{"version":"aa","card":"","binding":""}`, s))
		if a.Card != "unknown" {
			t.Errorf("%s: a poll without a card is told %q", name, a.Card)
		}
	}
	// The list is no still page, and no page but a Card page of a card that
	// is not pending is marked still.
	if strings.Contains(r.getWith("/cards/", s).Body.String(), "data-still") {
		t.Error("the list is marked still")
	}
	// The script does not reload a still page, and does not say "elsewhere"
	// of a card this page's own decision moved.
	js := watchScript(t)
	for _, want := range []string{`root.dataset.still === "yes"`, `data.state === "changed" && !still`,
		`document.documentElement.dataset.sent !== "yes"`} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	dec := r.do("GET", "/static/decide.js", nil, nil).Body.String()
	if !strings.Contains(dec, `document.documentElement.dataset.sent = "yes"`) ||
		!strings.Contains(dec, "delete document.documentElement.dataset.sent") {
		t.Error("decide.js does not set the sent mark when it sends, and clear it when the card is as it was")
	}
	// The mark is set where the decision is sent (the confirm tap), before
	// any network call, and cleared only in back().
	if strings.Index(dec, `dataset.sent = "yes"`) > strings.Index(dec, "confirm(p).catch") {
		t.Error("decide.js sets the sent mark after it sends")
	}
}

// TestSeatWatchPagesWithoutALiveWatcher: a port without a watcher, or one
// that does not watch, gives pages that say so and carry no version.
func TestSeatWatchPagesWithoutALiveWatcher(t *testing.T) {
	plain := newRig(t)
	ps := plain.login(t)
	r, wd := newWatchRig(t, "aa", time.Minute)
	wd.set(func(st *WatchState) { *st = WatchState{} })
	s := r.login(t)
	for name, get := range map[string]func(string) string{
		"no watcher":   func(p string) string { return plain.getWith(p, ps).Body.String() },
		"not watching": func(p string) string { return r.getWith(p, s).Body.String() },
	} {
		for _, path := range []string{"/cards/", "/cards/" + testCardID} {
			body := get(path)
			if !strings.Contains(body, `data-live="no" data-version=""`) || !strings.Contains(body, `/static/watch.js`) {
				t.Errorf("%s %s: no live mark", name, path)
			}
		}
	}
	// A version that is not of the form a poll takes is not embedded.
	wd.set(func(st *WatchState) { *st = WatchState{Watching: true, Version: `"><x`} })
	if body := r.getWith("/cards/", s).Body.String(); strings.Contains(body, "<x") || !strings.Contains(body, `data-live="yes" data-version=""`) {
		t.Errorf("a bad version in the page: %s", body)
	}
}

// TestSeatWatchAssetIsServed: watch.js is served like the other scripts, from
// the app's own origin, under the same CSP.
func TestSeatWatchAssetIsServed(t *testing.T) {
	r := newRig(t)
	w := r.do("GET", "/static/watch.js", nil, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Body.Len() == 0 {
		t.Errorf("watch.js: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	checkHeaders(t, "watch.js", w)
	found := false
	for _, e := range inventory {
		found = found || e.pattern == "GET /static/watch.js"
	}
	if !found {
		t.Error("watch.js is not in the page inventory")
	}
}
