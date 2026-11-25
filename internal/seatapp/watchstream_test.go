package seatapp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// streamOK is a stream's body with the version the watch rigs start with.
const streamOK = `{"version":"aa"}`

// streamRequest is a well-formed request for the hint stream: a same-origin
// JSON POST, as watch.js sends it.
func streamRequest(ctx context.Context, target, body string, cookies ...*http.Cookie) *http.Request {
	req := newRequest("POST", target, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// streamDo sends a stream request to the handler and returns when the
// handler does: for a refusal, or a stream whose context is done.
func (r *rig) streamDo(ctx context.Context, body string, header map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := streamRequest(ctx, streamPath, body, cookies...)
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

// streamAsync runs a stream on a recorder in a goroutine; the recorder
// arrives on the channel when the handler returns, and only then is it read.
func (r *rig) streamAsync(ctx context.Context, body string, cookies ...*http.Cookie) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, streamRequest(ctx, streamPath, body, cookies...))
		out <- w
	}()
	return out
}

// streamConn is a stream read over a real connection, block by block: a
// block is the lines up to a blank line, joined by newlines.
type streamConn struct {
	resp   *http.Response
	blocks chan string
	cancel context.CancelFunc
	// readErr is the error the reading ended with, nil for a clean end;
	// set before blocks is closed.
	readErr error
	mu      sync.Mutex
	raw     strings.Builder
}

// streamServer serves the rig's handler over loopback until the test ends.
// Its close is registered before any stream's, so it runs after them: the
// server waits for its requests in flight, and a stream left open would
// hold it for the stream's life.
func streamServer(t *testing.T, r *rig) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(r.h)
	t.Cleanup(srv.Close)
	return srv
}

// openStream opens a stream on srv, with a connection of its own.
func openStream(t *testing.T, srv *httptest.Server, body string, cookies ...*http.Cookie) *streamConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := streamRequest(ctx, srv.URL+streamPath, body, cookies...)
	req.RequestURI = ""
	tr := &http.Transport{DisableKeepAlives: true, DisableCompression: true}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &streamConn{resp: resp, blocks: make(chan string, 64), cancel: cancel}
	t.Cleanup(func() {
		cancel()
		resp.Body.Close()
		tr.CloseIdleConnections()
	})
	if resp.StatusCode != 200 {
		close(c.blocks)
		return c
	}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var lines []string
		for sc.Scan() {
			line := sc.Text()
			c.mu.Lock()
			c.raw.WriteString(line + "\n")
			c.mu.Unlock()
			if line == "" {
				c.blocks <- strings.Join(lines, "\n")
				lines = nil
				continue
			}
			lines = append(lines, line)
		}
		c.readErr = sc.Err()
		close(c.blocks)
	}()
	return c
}

// next returns the next block, and false when the stream ended first. It
// fails the test when nothing comes within d.
func (c *streamConn) next(t *testing.T, d time.Duration) (string, bool) {
	t.Helper()
	select {
	case b, ok := <-c.blocks:
		return b, ok
	case <-time.After(d):
		t.Fatal("nothing on the stream in time")
		return "", false
	}
}

// nextEvent returns the next block that is not a heartbeat.
func (c *streamConn) nextEvent(t *testing.T, d time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		b, ok := c.next(t, time.Until(deadline))
		if !ok || b != ":" {
			return b, ok
		}
	}
}

// ended waits for the stream to end, and fails when it ended with an error
// rather than the server's clean end of the response.
func (c *streamConn) ended(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if _, ok := c.next(t, time.Until(deadline)); !ok {
			break
		}
	}
	if c.readErr != nil {
		t.Errorf("the stream ended with %v, not a clean end", c.readErr)
	}
}

// text returns what was read so far.
func (c *streamConn) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw.String()
}

// streams returns how many streams are open.
func (r *rig) streams() int {
	n, _ := r.app.streams.open("")
	return n
}

// waitStreams waits until n streams are open.
func (r *rig) waitStreams(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(waitBound)
	for r.streams() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d open streams, want %d", r.streams(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitTimers waits until at least n fake timers are armed.
func waitTimers(t *testing.T, ft *fakeTimers, n int) {
	t.Helper()
	deadline := time.Now().Add(waitBound)
	for ft.pending() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d timers armed, want %d", ft.pending(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// stillTimers puts the rig's streams on fake timers that the test never
// advances: no heartbeat, rotation or session timer of a stream fires. A
// test that waits for a stream to end, with a bound generous for a loaded
// host, then sees the end that the event it tests makes and no other: on
// real timers the heartbeat (streamBeat), which looks at the session and the
// server's end, or the rotation (streamLife) would end the stream inside
// that bound too, and the test would pass with the event ignored.
func stillTimers(r *rig) *fakeTimers {
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	return ft
}

// dueIn reports whether a timer is armed to fire d after the clock's now.
// The stream arms its timers on this clock, so the arming of one is told by
// its due time: the heartbeat's is the clock plus streamBeat, an event's gap
// the clock plus streamGap, and the life's and the session's are neither.
func (f *fakeTimers) dueIn(d time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := f.clk.Now().Add(d)
	for _, tm := range f.timers {
		if tm.at.Equal(want) {
			return true
		}
	}
	return false
}

// waitDue waits until a timer is armed to fire d after the clock's now. A
// count of armed timers (waitTimers) cannot say which they are: the gap
// armed after an event counts as the heartbeat's timer that is not yet
// re-armed after a heartbeat, and a test that advances the clock then moves
// it past a timer that is armed a moment later and never fires.
func waitDue(t *testing.T, ft *fakeTimers, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(waitBound)
	for !ft.dueIn(d) {
		if time.Now().After(deadline) {
			t.Fatalf("no timer armed to fire %v from now", d)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitHandshakes waits until n stream handshakes are in progress.
func (r *rig) waitHandshakes(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(waitBound)
	for r.handshakes() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d handshakes in progress, want %d", r.handshakes(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// hintRe is the one form an event of the stream has.
var hintRe = regexp.MustCompile(`^event: cards-changed\ndata: \{"version":"[0-9a-f]{0,64}"\}$`)

// TestSeatStreamAdmission: every refusal is made before a header of the
// stream, with a fixed text and no redirect, and none reaches the watcher.
func TestSeatStreamAdmission(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	s2 := r.login(t)
	for name, c := range map[string]struct {
		header  map[string]string
		cookies []*http.Cookie
		body    string
		status  int
		text    string
	}{
		"no origin":          {map[string]string{"Origin": ""}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"another origin":     {map[string]string{"Origin": "https://evil.example"}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"null origin":        {map[string]string{"Origin": "null"}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"a sibling origin":   {map[string]string{"Origin": "https://other.seat.example"}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"another host":       {map[string]string{"Host": "evil.example"}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"cross-site":         {map[string]string{"Sec-Fetch-Site": "cross-site"}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"same-site":          {map[string]string{"Sec-Fetch-Site": "same-site"}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"no fetch metadata":  {map[string]string{"Sec-Fetch-Site": ""}, []*http.Cookie{s}, streamOK, 403, "forbidden\n"},
		"form encoded":       {map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []*http.Cookie{s}, streamOK, 415, "unsupported media type\n"},
		"event stream type":  {map[string]string{"Content-Type": "text/event-stream"}, []*http.Cookie{s}, streamOK, 415, "unsupported media type\n"},
		"no content type":    {map[string]string{"Content-Type": ""}, []*http.Cookie{s}, streamOK, 415, "unsupported media type\n"},
		"no cookie":          {nil, nil, streamOK, 403, streamTextEnded},
		"two cookies":        {nil, []*http.Cookie{s, s2}, streamOK, 403, streamTextEnded},
		"the same twice":     {nil, []*http.Cookie{s, s}, streamOK, 403, streamTextEnded},
		"an unknown session": {nil, []*http.Cookie{{Name: SessionCookie, Value: strings.Repeat("A", tokenLen)}}, streamOK, 403, streamTextEnded},
		"null body":          {nil, []*http.Cookie{s}, "null", 400, `{"error":"bad_request"}` + "\n"},
		"unknown field":      {nil, []*http.Cookie{s}, `{"version":"aa","card":"x"}`, 400, `{"error":"bad_request"}` + "\n"},
		"bad version":        {nil, []*http.Cookie{s}, `{"version":"AA"}`, 400, `{"error":"bad_request"}` + "\n"},
		"long version":       {nil, []*http.Cookie{s}, `{"version":"` + strings.Repeat("a", 65) + `"}`, 400, `{"error":"bad_request"}` + "\n"},
	} {
		w := r.streamDo(context.Background(), c.body, c.header, c.cookies...)
		if w.Code != c.status || w.Body.String() != c.text {
			t.Errorf("%s: %d %q, want %d %q", name, w.Code, w.Body, c.status, c.text)
		}
		if loc := w.Header().Get("Location"); loc != "" {
			t.Errorf("%s: redirected to %q", name, loc)
		}
		if ct := w.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("%s: a refusal with the stream's type", name)
		}
		checkHeaders(t, name, w)
	}
	if n := wd.watchCalls(); n != 0 {
		t.Errorf("a refused stream reached the watcher %d times", n)
	}
	// A GET is no stream.
	req := newRequest("GET", streamPath, nil)
	req.AddCookie(s)
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("GET: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if r.streams() != 0 {
		t.Errorf("%d streams open after refusals", r.streams())
	}
}

// TestSeatStreamNotLive: a port that does not watch, one that is offline,
// and an app whose daemon is no Watcher refuse the stream 503 with a
// Retry-After, so that the page falls back to the poll, which says why.
func TestSeatStreamNotLive(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	for name, f := range map[string]func(*WatchState){
		"not watching": func(st *WatchState) { *st = WatchState{} },
		"offline":      func(st *WatchState) { *st = WatchState{Watching: true, Version: "aa", Offline: true} },
		"bad version":  func(st *WatchState) { *st = WatchState{Watching: true, Version: "not hex"} },
		"no version":   func(st *WatchState) { *st = WatchState{Watching: true} },
	} {
		wd.set(f)
		w := r.streamDo(context.Background(), streamOK, nil, s)
		if w.Code != 503 || w.Body.String() != streamTextNotLive || w.Header().Get("Retry-After") != "30" {
			t.Errorf("%s: %d %q Retry-After %q", name, w.Code, w.Body, w.Header().Get("Retry-After"))
		}
	}
	plain := newRig(t)
	ps := plain.login(t)
	w := plain.streamDo(context.Background(), streamOK, nil, ps)
	if w.Code != 503 || w.Body.String() != streamTextNotLive {
		t.Errorf("no watcher: %d %q", w.Code, w.Body)
	}
}

// TestSeatStreamHints: an open stream starts with a heartbeat, says each
// change of the version as one cards-changed event that holds the version
// and nothing else, and carries the app's headers and the stream's type.
func TestSeatStreamHints(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	r.app.streamGap = 10 * time.Millisecond
	s := r.login(t)
	srv := streamServer(t, r)
	asked := r.daemon.count()
	c := openStream(t, srv, streamOK, s)
	if c.resp.StatusCode != 200 {
		t.Fatalf("stream: %d", c.resp.StatusCode)
	}
	h := c.resp.Header
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-store" ||
		h.Get("Content-Security-Policy") != wantHeaders["Content-Security-Policy"] || len(h.Values("Set-Cookie")) != 0 ||
		h.Get("Content-Encoding") != "" || h.Get("Location") != "" {
		t.Errorf("headers: %v", h)
	}
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("first block %q %v, want the heartbeat", b, ok)
	}
	wd.set(func(st *WatchState) {
		st.Version, st.Waiting = "bb", 7
		st.Cards = []WatchCard{{ID: testCardID, Kind: "approval", BindingSHA256: strings.Repeat("ab", 32)}}
	})
	b, ok := c.nextEvent(t, waitBound)
	if !ok || b != "event: cards-changed\ndata: {\"version\":\"bb\"}" {
		t.Fatalf("event %q %v", b, ok)
	}
	// A change that leaves the version says nothing.
	wd.set(func(st *WatchState) { st.Waiting = 8 })
	wd.set(func(st *WatchState) { st.Version = "cc" })
	if b, ok := c.nextEvent(t, waitBound); !ok || b != "event: cards-changed\ndata: {\"version\":\"cc\"}" {
		t.Fatalf("event %q %v", b, ok)
	}
	if n := r.daemon.count(); n != asked {
		t.Errorf("the stream asked the daemon %d times", n-asked)
	}
	// Everything read is a heartbeat or the one kind of event.
	c.cancel()
	for _, block := range strings.Split(strings.TrimSuffix(c.text(), "\n\n"), "\n\n") {
		if block != ":" && !hintRe.MatchString(block) {
			t.Errorf("a block of another form: %q", block)
		}
		if len(block)+2 > streamEventMax {
			t.Errorf("a block of %d bytes", len(block)+2)
		}
	}
	for _, bad := range []string{testCardID, "waiting", "approval", "ab" + "ab", "7", "8"} {
		if strings.Contains(c.text(), bad) {
			t.Errorf("the stream holds %q: %q", bad, c.text())
		}
	}
}

// TestSeatStreamSaysAMissedChangeAtOnce: a stream opened with an older
// version says the current one as its first event: the page's reconnect
// reconciles what it shows.
func TestSeatStreamSaysAMissedChangeAtOnce(t *testing.T) {
	r, _ := newWatchRig(t, "bb", time.Minute)
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	if b, ok := c.nextEvent(t, waitBound); !ok || b != "event: cards-changed\ndata: {\"version\":\"bb\"}" {
		t.Fatalf("event %q %v", b, ok)
	}
	// With the current version there is nothing to say.
	c2 := openStream(t, srv, `{"version":"bb"}`, s)
	if b, ok := c2.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("first block %q %v", b, ok)
	}
	select {
	case b := <-c2.blocks:
		t.Errorf("a block with nothing changed: %q", b)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSeatStreamCoalesces: changes within the gap after an event are one
// event, with the last version, at the gap's end. The gap is on the test's
// clock: it ends when the test says, not when the host has run for a while,
// so the changes are inside it however slowly the test runs.
func TestSeatStreamCoalesces(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	c.next(t, waitBound)
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	wd.set(func(st *WatchState) { st.Version = "b1" })
	first, _ := c.nextEvent(t, waitBound)
	// The gap of the first event is armed after the event is written: the
	// changes come inside it only once it is.
	waitDue(t, ft, streamGap)
	for _, v := range []string{"b2", "b3", "b4", "b5"} {
		wd.set(func(st *WatchState) { st.Version = v })
	}
	ft.advance(streamGap)
	second, _ := c.nextEvent(t, waitBound)
	if !strings.Contains(first, `"b1"`) || !strings.Contains(second, `"b5"`) {
		t.Errorf("events %q then %q, want b1 and then the last version", first, second)
	}
	// The second event was said at the gap's end: its own gap runs from
	// there, not from the changes, which an event said inside the first gap
	// would have armed earlier.
	waitDue(t, ft, streamGap)
	// Nothing else is said: the next block is the heartbeat.
	ft.advance(streamBeat)
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
		t.Errorf("a third block: %q %v, want the heartbeat", b, ok)
	}
	if streamGap != 2*time.Second {
		t.Errorf("the gap is %v", streamGap)
	}
}

// TestSeatStreamLimitsAreTheDesigns: the stream's limits, and that its life
// and every write fit inside the server's write timeout, so that no
// heartbeat has to outlast it.
func TestSeatStreamLimitsAreTheDesigns(t *testing.T) {
	wt := Server(nil).WriteTimeout
	// The life is the whole response, its admission and its end included,
	// so it alone must fit the write timeout; the stream stops writing
	// streamFinish before it, after more than one heartbeat.
	if streamLife > 25*time.Second || streamLife >= wt || streamFinish <= 0 || streamFinish >= streamWrite ||
		2*streamBeat >= streamLife-streamFinish {
		t.Errorf("life %v, write %v, finish %v, beat %v, write timeout %v", streamLife, streamWrite, streamFinish, streamBeat, wt)
	}
	if streamHandshakes != 8 || streamHandshakesPer != 3 {
		t.Error("the handshake limits moved: the design and the guide need to follow")
	}
	if streamPerCredential != 3 || streamOpens != 12 || streamOpenWindow != time.Minute || streamMax != 32 || streamEventMax != 256 {
		t.Error("the stream's limits moved: the design and the guide need to follow")
	}
	if b, ok := streamEvent(strings.Repeat("f", maxWatchVersion)); !ok || len(b) > streamEventMax {
		t.Errorf("the largest event is %d bytes", len(b))
	}
}

// TestSeatStreamRotates: the stream ends at its life, a clean end of the
// response, and the life is the one armed.
func TestSeatStreamRotates(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	c.next(t, waitBound)
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	// Two heartbeats, then the life's end.
	ft.advance(streamBeat)
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("heartbeat %q %v", b, ok)
	}
	waitTimers(t, ft, 3)
	ft.advance(streamBeat)
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("heartbeat %q %v", b, ok)
	}
	waitTimers(t, ft, 3)
	ft.advance(streamLife - 2*streamBeat)
	c.ended(t, waitBound)
	r.waitStreams(t, 0)
}

// TestSeatStreamEndsInSilence: a session that reaches its idle time, or its
// age, while the stream says nothing ends the stream at that time.
func TestSeatStreamEndsInSilence(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	r.app.streamLife = time.Hour // no rotation in the way
	r.app.streamBeat = time.Hour
	s := r.login(t)
	r.clock.Advance(20 * time.Minute)
	got := r.streamAsync(context.Background(), streamOK, s)
	r.waitStreams(t, 1)
	waitTimers(t, ft, 3)
	ft.advance(10*time.Minute - time.Second)
	select {
	case <-got:
		t.Fatal("the stream ended before the session")
	case <-time.After(30 * time.Millisecond):
	}
	ft.advance(time.Second)
	w := within(t, got, waitBound)
	if strings.Contains(w.Body.String(), "event:") {
		t.Errorf("an event in silence: %q", w.Body)
	}
	if _, ok := r.sessionOf(s.Value); ok {
		t.Error("the session outlived its idle time")
	}

	// The age: a session kept in use to 11 hours 50 minutes has 10 minutes
	// left, whatever its idle time says.
	r2, _ := newWatchRig(t, "aa", time.Minute)
	ft2 := &fakeTimers{clk: r2.clock}
	r2.app.streamTimer = ft2.timer
	r2.app.streamLife, r2.app.streamBeat = 24*time.Hour, 24*time.Hour
	s2 := r2.login(t)
	for i := 0; i < 71; i++ {
		r2.clock.Advance(10 * time.Minute)
		if w := r2.getWith("/cards/", s2); w.Code != 200 {
			t.Fatalf("read %d: %d", i, w.Code)
		}
	}
	got = r2.streamAsync(context.Background(), streamOK, s2)
	r2.waitStreams(t, 1)
	waitTimers(t, ft2, 3)
	ft2.advance(10 * time.Minute)
	within(t, got, waitBound)
	if _, ok := r2.sessionOf(s2.Value); ok {
		t.Error("the session outlived its age")
	}
}

// TestSeatStreamDoesNotExtendTheSession: neither the stream's opening nor
// its heartbeats nor its events mark the session used.
func TestSeatStreamDoesNotExtendTheSession(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	r.app.streamLife = time.Hour
	s := r.login(t)
	k, _ := keyOf(s.Value)
	used := func() time.Time {
		r.app.sess.mu.Lock()
		defer r.app.sess.mu.Unlock()
		return r.app.sess.m[k].used
	}
	start := used()
	srv := streamServer(t, r)
	r.clock.Advance(5 * time.Minute)
	c := openStream(t, srv, streamOK, s)
	c.next(t, waitBound)
	for i := 0; i < 3; i++ {
		// The heartbeat's timer, which the last heartbeat re-arms, and the
		// gap of the last event, which is armed after it is written: the
		// clock passes both. Counting three timers is not enough: the gap
		// stands in for the heartbeat's before it is re-armed, and a gap
		// armed after the clock moved never fires, so the next change
		// waits for it for good.
		waitDue(t, ft, streamBeat)
		if i > 0 {
			waitDue(t, ft, streamGap)
		}
		ft.advance(streamBeat)
		c.next(t, waitBound)
		wd.set(func(st *WatchState) { st.Version = fmt.Sprintf("%02x", i) })
		c.nextEvent(t, waitBound)
	}
	if !used().Equal(start) {
		t.Errorf("the stream marked the session used: %v, was %v", used(), start)
	}
	// The session ends 30 minutes after the login, with the stream open.
	waitTimers(t, ft, 3)
	ft.advance(SessionIdle - 5*time.Minute - 3*streamBeat)
	c.ended(t, waitBound)
	if _, ok := r.sessionOf(s.Value); ok {
		t.Error("the session outlived its idle time with a stream open")
	}
}

// TestSeatStreamEndsAtLogout: a logout ends the stream at once, in silence.
// The stream's timers stand still: its heartbeat, which looks at the session,
// cannot end it, so the logout's wake alone can.
func TestSeatStreamEndsAtLogout(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	ft := stillTimers(r)
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	c.next(t, waitBound)
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	if w := r.postWith("/api/logout", "{}", s); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	c.ended(t, waitBound)
	r.waitStreams(t, 0)
	if w := r.streamDo(context.Background(), streamOK, nil, s); w.Code != 403 {
		t.Errorf("a stream after the logout: %d", w.Code)
	}
}

// TestSeatStreamLogoutRacesWrites: logouts and changes at once, many times,
// leave every stream ended and no data race (run with -race).
func TestSeatStreamLogoutRacesWrites(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	r.app.streamGap = time.Millisecond
	srv := streamServer(t, r)
	for round := 0; round < 8; round++ {
		r.clock.Advance(streamOpenWindow)
		s := r.login(t)
		c := openStream(t, srv, streamOK, s)
		c.next(t, waitBound)
		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				wd.set(func(st *WatchState) { st.Version = fmt.Sprintf("%04x", i) })
				runtime.Gosched()
			}
		}()
		time.Sleep(time.Duration(round) * time.Millisecond)
		r.postWith("/api/logout", "{}", s)
		c.ended(t, waitBound)
		close(stop)
		wg.Wait()
		r.waitStreams(t, 0)
	}
}

// slowWriter is a client that stops reading after its first write: each
// later write blocks until its deadline, and then fails, as a full socket
// does.
type slowWriter struct {
	h        http.Header
	mu       sync.Mutex
	deadline time.Time
	writes   int
	// blockedUntil is the deadline the first blocked write waits for, zero
	// before: the write fails then and not before.
	blockedUntil time.Time
	blocked      atomic.Int32
}

func (w *slowWriter) Header() http.Header { return w.h }
func (w *slowWriter) WriteHeader(int)     {}
func (w *slowWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	d := w.deadline
	first := w.writes == 1
	if !first && w.blockedUntil.IsZero() {
		w.blockedUntil = d
	}
	w.mu.Unlock()
	if d.IsZero() {
		return 0, errors.New("a write with no deadline")
	}
	if first {
		return len(b), nil
	}
	w.blocked.Add(1)
	time.Sleep(time.Until(d))
	return 0, errors.New("i/o timeout")
}
func (w *slowWriter) FlushError() error { return nil }
func (w *slowWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

// TestSeatStreamSlowClient: a client that does not read loses its stream
// after the write's deadline, while the watcher and another client's stream
// go on unhindered.
func TestSeatStreamSlowClient(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	// The write deadline is real time, the one thing the slow writer waits
	// for; it is long enough that the publishing below, which a stuck write
	// must not delay, is told from the wait for it by a margin that a
	// loaded host's scheduling does not close.
	r.app.streamWrite = 400 * time.Millisecond
	r.app.streamGap = time.Millisecond
	s := r.login(t)
	srv := streamServer(t, r)
	fast := openStream(t, srv, streamOK, s)
	fast.next(t, waitBound)

	slow := &slowWriter{h: http.Header{}}
	done := make(chan struct{})
	var endedAt time.Time
	go func() {
		defer close(done)
		r.h.ServeHTTP(slow, streamRequest(context.Background(), streamPath, streamOK, s))
		endedAt = time.Now()
	}()
	r.waitStreams(t, 2)
	// The first change is the slow stream's event, whose write blocks.
	wd.set(func(st *WatchState) { st.Version = "ee" })
	for until := time.Now().Add(waitBound); slow.blocked.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(until) {
			t.Fatal("the slow stream's write did not block")
		}
	}
	if b, ok := fast.nextEvent(t, waitBound); !ok || !hintRe.MatchString(b) {
		t.Errorf("the other stream: %q %v", b, ok)
	}
	// The watcher publishes without waiting for anyone, and the other
	// stream hears of it while the slow one is stuck.
	for i := 0; i < 20; i++ {
		setAt := time.Now()
		wd.set(func(st *WatchState) { st.Version = fmt.Sprintf("%02x", i) })
		if time.Since(setAt) > r.app.streamWrite/2 {
			t.Fatal("publishing waited for a stream")
		}
	}
	if b, ok := fast.nextEvent(t, waitBound); !ok || !hintRe.MatchString(b) {
		t.Errorf("the other stream: %q %v", b, ok)
	}
	select {
	case <-done:
	case <-time.After(waitBound):
		t.Fatal("the slow stream was not ended by its write deadline")
	}
	// Ended by the write's deadline, and not before it. The stream sets the
	// deadline an instant before the write begins, so the time the write
	// blocked is not the measure: the deadline itself is.
	slow.mu.Lock()
	until := slow.blockedUntil
	slow.mu.Unlock()
	if endedAt.Before(until) {
		t.Errorf("the slow stream ended %v before its write's deadline", until.Sub(endedAt))
	}
	r.waitStreams(t, 1)
}

// TestSeatStreamPerCredentialAndGlobalLimits: three streams of a credential
// at once, across its sessions; 32 in all; a refusal says when to try
// again; a stream that ends frees its place.
func TestSeatStreamPerCredentialAndGlobalLimits(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s1, s2 := r.login(t), r.login(t)
	var outs []<-chan *httptest.ResponseRecorder
	for i, c := range []*http.Cookie{s1, s2, s1} {
		outs = append(outs, r.streamAsync(ctx, streamOK, c))
		r.waitStreams(t, i+1)
	}
	w := r.streamDo(context.Background(), streamOK, nil, s2)
	if w.Code != 429 || w.Body.String() != streamTextLimited || w.Header().Get("Retry-After") != "25" {
		t.Errorf("a fourth stream of the credential: %d %q %q", w.Code, w.Body, w.Header().Get("Retry-After"))
	}
	// Other credentials fill the rest of the 32.
	n := 3
	for cred := 1; n < streamMax; cred++ {
		r.daemon.mu.Lock()
		r.daemon.loginRes.Credential = fmt.Sprintf("%016x", cred)
		r.daemon.mu.Unlock()
		// The login challenges of all sources are 8 a minute.
		r.clock.Advance(loginAllWindow)
		c := r.login(t)
		for j := 0; j < streamPerCredential && n < streamMax; j++ {
			outs = append(outs, r.streamAsync(ctx, streamOK, c))
			n++
			r.waitStreams(t, n)
		}
	}
	r.daemon.mu.Lock()
	r.daemon.loginRes.Credential = "ffffffffffffffff"
	r.daemon.mu.Unlock()
	r.clock.Advance(loginAllWindow)
	other := r.login(t)
	w = r.streamDo(context.Background(), streamOK, nil, other)
	if w.Code != 503 || w.Body.String() != streamTextFull || w.Header().Get("Retry-After") != "25" {
		t.Errorf("a stream past %d: %d %q %q", streamMax, w.Code, w.Body, w.Header().Get("Retry-After"))
	}
	cancel()
	for _, o := range outs {
		within(t, o, waitBound)
	}
	r.waitStreams(t, 0)
	// A logout frees the place too. The stream's timers stand still from
	// here, so that the logout, not a heartbeat, ends it.
	ft := stillTimers(r)
	got := r.streamAsync(context.Background(), streamOK, other)
	r.waitStreams(t, 1)
	waitTimers(t, ft, 3)
	r.postWith("/api/logout", "{}", other)
	within(t, got, waitBound)
	r.waitStreams(t, 0)
}

// TestSeatStreamOpensPerMinute: a credential opens at most 12 streams a
// minute, the 13th is answered 429 with a Retry-After of a minute, and the
// polls keep their own limit.
func TestSeatStreamOpensPerMinute(t *testing.T) {
	r, _ := newWatchRig(t, "bb", time.Minute)
	s := r.login(t)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < streamOpens; i++ {
		if w := r.streamDo(done, streamOK, nil, s); w.Code != 200 {
			t.Fatalf("open %d: %d %q", i, w.Code, w.Body)
		}
	}
	w := r.streamDo(done, streamOK, nil, s)
	if w.Code != 429 || w.Body.String() != streamTextLimited || w.Header().Get("Retry-After") != "60" {
		t.Errorf("open past the limit: %d %q %q", w.Code, w.Body, w.Header().Get("Retry-After"))
	}
	if a := pollAnswer(t, r.poll(pollOK, s)); a.State != "changed" {
		t.Errorf("the poll after the stream's limit: %+v", a)
	}
	r.clock.Advance(streamOpenWindow)
	if w := r.streamDo(done, streamOK, nil, s); w.Code != 200 {
		t.Errorf("open a minute later: %d", w.Code)
	}
}

// TestSeatStreamEndsAtShutdown: the server's Shutdown ends the open streams
// at once, cleanly, rather than waiting for their life; a stream asked after
// it is refused; and no goroutine of a stream is left.
//
// The streams' timers stand still, so neither their heartbeat (which looks at
// the server's end) nor their life can end them: the shutdown's own context,
// the bound of the wait, runs out while a stream that ignores the shutdown is
// open, and Shutdown fails.
func TestSeatStreamEndsAtShutdown(t *testing.T) {
	for _, viaOpen := range []bool{false, true} {
		t.Run(fmt.Sprintf("open=%v", viaOpen), func(t *testing.T) {
			r, _ := newWatchRig(t, "aa", time.Minute)
			ft := stillTimers(r)
			s := r.login(t)
			var h http.Handler = r.h
			if viaOpen {
				h = &App{Handler: r.h, stop: func() {}}
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := Server(h)
			served := make(chan error, 1)
			go func() { served <- srv.Serve(ln) }()
			ts := &httptest.Server{URL: "http://" + ln.Addr().String()}
			c := openStream(t, ts, streamOK, s)
			c.next(t, waitBound)
			c2 := openStream(t, ts, streamOK, s)
			c2.next(t, waitBound)
			r.waitStreams(t, 2)
			waitTimers(t, ft, 6) // each stream's life, heartbeat and session's end
			ctx, cancel := context.WithTimeout(context.Background(), waitBound)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatalf("shutdown: %v (the streams did not end at the shutdown)", err)
			}
			c.ended(t, waitBound)
			c2.ended(t, waitBound)
			r.waitStreams(t, 0)
			if err := <-served; !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve: %v", err)
			}
			if w := r.streamDo(context.Background(), streamOK, nil, s); w.Code != 503 || w.Body.String() != streamTextDown {
				t.Errorf("a stream after the shutdown: %d %q", w.Code, w.Body)
			}
			deadline := time.Now().Add(waitBound)
			for goroutinesNamed("runStream") != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("%d stream goroutines left", goroutinesNamed("runStream"))
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// goroutinesNamed counts the goroutines whose stack holds name.
func goroutinesNamed(name string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), name)
		}
		buf = make([]byte, 2*len(buf))
	}
}

// TestSeatStreamAppCloseEndsStreams: Close of an App made by Open ends its
// streams too.
func TestSeatStreamAppCloseEndsStreams(t *testing.T) {
	wd := &watchDaemon{fakeWatcher: newFakeWatcher("aa")}
	r := newRig(t, func(c *Config) {
		wd.fakeDaemon = c.Daemon.(*fakeDaemon)
		c.Daemon = wd
	})
	// The stream's timers stand still: its rotation, which would end it
	// inside the wait's bound, cannot, so Close alone can.
	ft := stillTimers(r)
	s := r.login(t)
	x := &App{Handler: r.h, stop: func() {}}
	got := r.streamAsync(context.Background(), streamOK, s)
	r.waitStreams(t, 1)
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	x.Close()
	within(t, got, waitBound)
	r.waitStreams(t, 0)
}

// TestSeatStreamCheckInIsTheForeground: a stream's opening takes a lease
// of streamLease, at most to the session's end, which a poll's quiet does
// not need; it counts as seen only the waiting count of the version the
// page says it shows, when it is the watcher's; a stream that ends for any
// reason but its life ends the lease, and the session's end ends it too.
func TestSeatStreamCheckInIsTheForeground(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	wd.set(func(st *WatchState) { st.Version, st.Waiting = "bb", 3 })
	s := r.login(t)
	srv := streamServer(t, r)
	// A page that shows an older version: a lease, and nothing seen.
	c := openStream(t, srv, streamOK, s)
	c.next(t, waitBound)
	now := r.clock.Now()
	if got := r.app.foregroundUntil(now); !got.Equal(now.Add(streamLease)) {
		t.Errorf("the lease runs to %v, want streamLease", got.Sub(now))
	}
	if seq, _ := r.app.pollSeen(); seq != 0 {
		t.Error("a stream opened with an older version counted the cards as seen")
	}
	if !r.app.lastPollTime().IsZero() {
		t.Error("the stream counted as a poll")
	}
	// The page shows the watcher's version: its count is seen.
	c2 := openStream(t, srv, `{"version":"bb"}`, s)
	c2.next(t, waitBound)
	if seq, waiting := r.app.pollSeen(); seq != 1 || waiting != 3 {
		t.Errorf("seen %d times, %d waiting; want once, 3", seq, waiting)
	}
	// A page that closes its stream ends its lease; the other stays.
	c.cancel()
	r.waitStreams(t, 1)
	if r.app.foregroundUntil(r.clock.Now()).IsZero() {
		t.Error("one closed stream ended the other's lease")
	}
	c2.cancel()
	r.waitStreams(t, 0)
	if got := r.app.foregroundUntil(r.clock.Now()); !got.IsZero() {
		t.Errorf("closed streams left a lease to %v", got.Sub(now))
	}
	// The lease ends with the session: at its idle end, and at a logout.
	// The stream's timers stand still from here, so that the logout, not a
	// heartbeat, ends the stream.
	ft := stillTimers(r)
	r.clock.Advance(SessionIdle - 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := r.streamAsync(ctx, `{"version":"bb"}`, s)
	r.waitStreams(t, 1)
	waitTimers(t, ft, 3)
	k, _ := keyOf(s.Value)
	until, _ := r.app.sess.liveUntil(k)
	if fu := r.app.foregroundUntil(r.clock.Now()); !fu.Equal(until) {
		t.Errorf("the lease runs %v past the session", fu.Sub(until))
	}
	r.postWith("/api/logout", "{}", s)
	within(t, got, waitBound)
	if !r.app.foregroundUntil(r.clock.Now()).IsZero() {
		t.Error("a lease outlived its session")
	}
}

// streamNoticeRig is a notice rig whose card list streams, on the same
// fake clock: the stream's timers are apart from the notices', and fire
// with fire.
type streamNoticeRig struct {
	*noticeRig
	ft  *fakeTimers
	srv *httptest.Server
	s   *http.Cookie
	n   int
}

func newStreamNoticeRig(t *testing.T) *streamNoticeRig {
	t.Helper()
	return newStreamNoticeRigWith(t, nil)
}

// newStreamNoticeRigWith is newStreamNoticeRig, whose daemon port is wrap's
// of the watching port when wrap is not nil (see newNoticeRigWith).
func newStreamNoticeRigWith(t *testing.T, wrap func(*watchDaemon) Daemon) *streamNoticeRig {
	t.Helper()
	nr := newNoticeRigWith(t, wrap)
	sr := &streamNoticeRig{noticeRig: nr, ft: &fakeTimers{clk: nr.clock}}
	nr.app.streamTimer = sr.ft.timer
	nr.app.streamLife = time.Hour // no rotation in the way
	sr.s = nr.login(t)
	sr.srv = streamServer(t, nr.rig)
	return sr
}

// change sets the count waiting with a new version of hex digits, which
// a stream takes, and waits for the notices' goroutine to judge it.
func (sr *streamNoticeRig) change(t *testing.T, waiting int) {
	t.Helper()
	sr.n++
	before := sr.parks.Load()
	sr.wd.set(func(st *WatchState) {
		st.Watching, st.Offline, st.Waiting = true, false, waiting
		st.Version = fmt.Sprintf("%02x", sr.n)
	})
	sr.parked(t, before+1)
}

// version is the watcher's version now.
func (sr *streamNoticeRig) version() string { return sr.wd.Watch().Version }

// open opens the list's stream with the version the page shows.
func (sr *streamNoticeRig) open(t *testing.T, shows string) *streamConn {
	t.Helper()
	c := openStream(t, sr.srv, `{"version":"`+shows+`"}`, sr.s)
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("stream: %q %v", b, ok)
	}
	return c
}

// TestSeatStreamNoticeNotDoubledForASeenCount: a card that comes while the
// list is shown is held back by the stream's lease; the list reloads and
// checks in with the version it shows, which counts the card as seen; the
// list closes, and no notice is sent for the card the owner saw.
func TestSeatStreamNoticeNotDoubledForASeenCount(t *testing.T) {
	sr := newStreamNoticeRig(t)
	c := sr.open(t, sr.version())
	sr.change(t, 1)
	sr.expect(t)
	if b, ok := c.nextEvent(t, waitBound); !ok || !hintRe.MatchString(b) {
		t.Fatalf("hint %q %v", b, ok)
	}
	// The page reloads (its stream is left) and shows the new version.
	c.cancel()
	sr.waitStreams(t, 0)
	c2 := sr.open(t, sr.version())
	sr.expect(t)
	// The list is closed.
	c2.cancel()
	sr.waitStreams(t, 0)
	sr.pass(t, streamLease)
	sr.pass(t, noticeSpacing)
	sr.pass(t, time.Hour)
	sr.expect(t)
	// A card after the one seen is told, once the owner is gone.
	sr.change(t, 2)
	sr.expect(t, 2)
}

// TestSeatStreamNoticeDueWhenTheHintWasNotShown: a card that comes while
// the list is shown is held back only while the lease lasts. The phone is
// suspended before the list reloads: the seat's heartbeats still go out (a
// proxy takes them), and do not renew the lease; when it runs out the
// notice is sent, with the stream still open.
func TestSeatStreamNoticeDueWhenTheHintWasNotShown(t *testing.T) {
	sr := newStreamNoticeRig(t)
	c := sr.open(t, sr.version())
	sr.change(t, 1)
	if b, ok := c.nextEvent(t, waitBound); !ok || !hintRe.MatchString(b) {
		t.Fatalf("hint %q %v", b, ok)
	}
	elapsed := time.Duration(0)
	for ; elapsed+streamBeat < streamLease; elapsed += streamBeat {
		waitDue(t, sr.ft, streamBeat) // the heartbeat's timer, armed again after the last one
		sr.pass(t, streamBeat)
		sr.ft.advance(0)
		if b, ok := c.next(t, waitBound); !ok || b != ":" {
			t.Fatalf("heartbeat %q %v", b, ok)
		}
		sr.expect(t)
	}
	sr.pass(t, streamLease-elapsed)
	if sr.streams() != 1 {
		t.Fatalf("%d streams open, want the suspended page's", sr.streams())
	}
	sr.expect(t, 1)
}

// TestSeatStreamStrictInput: the body is one object with exactly one string
// member "version"; a header the checks rest on, and the session cookie,
// each come exactly once, counted before they are parsed. None of the
// refusals reaches the watcher or opens a stream.
func TestSeatStreamStrictInput(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	for name, body := range map[string]string{
		"no member":         `{}`,
		"null version":      `{"version":null}`,
		"a number":          `{"version":12}`,
		"the key twice":     `{"version":"aa","version":"aa"}`,
		"the key twice, ab": `{"version":"aa","version":"bb"}`,
		"another case":      `{"Version":"aa"}`,
		"an array":          `["aa"]`,
		"after the object":  `{"version":"aa"} {}`,
		"two members":       `{"version":"aa","x":1}`,
	} {
		w := r.streamDo(context.Background(), body, nil, s)
		if w.Code != 400 || w.Body.String() != `{"error":"bad_request"}`+"\n" {
			t.Errorf("%s: %d %q", name, w.Code, w.Body)
		}
	}
	// An empty version is a page made before the watcher had one: taken.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if w := r.streamDo(done, `{"version":""}`, nil, s); w.Code != 200 {
		t.Errorf("an empty version: %d %q", w.Code, w.Body)
	}
	twice := func(name string, mod func(*http.Request)) {
		req := streamRequest(context.Background(), streamPath, streamOK, s)
		mod(req)
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		if w.Code == 200 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
			t.Errorf("%s: %d %q", name, w.Code, w.Body)
		}
	}
	twice("two fetch metadata", func(req *http.Request) { req.Header.Add("Sec-Fetch-Site", "same-origin") })
	twice("fetch metadata after another", func(req *http.Request) {
		req.Header.Del("Sec-Fetch-Site")
		req.Header.Add("Sec-Fetch-Site", "same-origin")
		req.Header.Add("Sec-Fetch-Site", "cross-site")
	})
	twice("two content types", func(req *http.Request) { req.Header.Add("Content-Type", "application/json") })
	twice("a malformed second cookie", func(req *http.Request) { req.Header.Add("Cookie", SessionCookie+`="x y"`) })
	twice("a second cookie in one header", func(req *http.Request) {
		req.Header.Set("Cookie", SessionCookie+"="+s.Value+"; "+SessionCookie+"=\x01")
	})
	if n := wd.watchCalls(); n != 1 {
		t.Errorf("the refusals reached the watcher: %d calls, want the one stream's", n)
	}
	if r.streams() != 0 {
		t.Errorf("%d streams open", r.streams())
	}
}

// pausedBody is a request body whose first read waits until it is let go:
// a client that sends its body slowly. reads counts the reads begun.
type pausedBody struct {
	body    string
	reading chan struct{} // closed at the first read
	letGo   chan struct{}
	once    sync.Once
	reads   atomic.Int32
	off     int
}

func newPausedBody(body string) *pausedBody {
	return &pausedBody{body: body, reading: make(chan struct{}), letGo: make(chan struct{})}
}

func (b *pausedBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	b.once.Do(func() { close(b.reading) })
	<-b.letGo
	if b.off >= len(b.body) {
		return 0, io.EOF
	}
	n := copy(p, b.body[b.off:])
	b.off += n
	return n, nil
}

// pausedStream sends a stream request whose body is paused, in a
// goroutine; the recorder arrives on the channel when the handler returns.
func (r *rig) pausedStream(ctx context.Context, body *pausedBody, c *http.Cookie) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	req := streamRequest(ctx, streamPath, "", c)
	req.Body = io.NopCloser(body)
	req.ContentLength = -1
	go func() {
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		out <- w
	}()
	return out
}

// handshakes returns how many stream handshakes are in progress.
func (r *rig) handshakes() int {
	n, _ := r.app.streams.handshakes("")
	return n
}

// noStream fails when w is a stream's response, or holds a byte of one.
func noStream(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || strings.Contains(w.Body.String(), ":\n\n") {
		t.Errorf("%s: a stream's bytes: %d %q %q", name, w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}

// TestSeatStreamAdmissionLooksAgainAfterTheBody: a session that ends while
// the body is still coming, a server that stops meanwhile, and a client that
// goes are each refused before a byte of the stream, with their places
// given back.
func TestSeatStreamAdmissionLooksAgainAfterTheBody(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	b := newPausedBody(streamOK)
	got := r.pausedStream(context.Background(), b, s)
	<-b.reading
	if r.handshakes() != 1 {
		t.Errorf("%d handshakes in progress, want 1", r.handshakes())
	}
	if w := r.postWith("/api/logout", "{}", s); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	close(b.letGo)
	w := within(t, got, waitBound)
	if w.Code != 403 || w.Body.String() != streamTextEnded {
		t.Errorf("a logout during the body: %d %q, want 403 %q", w.Code, w.Body, streamTextEnded)
	}
	noStream(t, "logout", w)
	if r.streams() != 0 || r.handshakes() != 0 {
		t.Errorf("%d streams and %d handshakes left", r.streams(), r.handshakes())
	}

	// The session reaches its idle time while the body is coming.
	s = r.login(t)
	b = newPausedBody(streamOK)
	got = r.pausedStream(context.Background(), b, s)
	<-b.reading
	r.clock.Advance(SessionIdle)
	close(b.letGo)
	w = within(t, got, waitBound)
	if w.Code != 403 || w.Body.String() != streamTextEnded {
		t.Errorf("the idle time during the body: %d %q", w.Code, w.Body)
	}
	noStream(t, "idle", w)

	// The client goes while its body is coming: no stream, nothing held.
	s = r.login(t)
	ctx, cancel := context.WithCancel(context.Background())
	b = newPausedBody(streamOK)
	got = r.pausedStream(ctx, b, s)
	<-b.reading
	cancel()
	close(b.letGo)
	noStream(t, "a client gone", within(t, got, waitBound))

	// The server stops while the body is coming.
	b = newPausedBody(streamOK)
	got = r.pausedStream(context.Background(), b, s)
	<-b.reading
	r.app.streams.end()
	close(b.letGo)
	w = within(t, got, waitBound)
	if w.Code != 503 || w.Body.String() != streamTextDown {
		t.Errorf("a shutdown during the body: %d %q", w.Code, w.Body)
	}
	noStream(t, "shutdown", w)
	if r.streams() != 0 || r.handshakes() != 0 {
		t.Errorf("%d streams and %d handshakes left", r.streams(), r.handshakes())
	}
}

// TestSeatStreamBoundsHandshakes: the requests in their admission are
// bounded before a byte of their body is read, three for a credential and
// eight in all, apart from the open streams; a refusal is fixed; every
// end of a handshake gives its place back.
func TestSeatStreamBoundsHandshakes(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	creds := []string{"0123456789abcdef", "1123456789abcdef", "2123456789abcdef"}
	var cookies []*http.Cookie
	for _, c := range creds {
		r.daemon.mu.Lock()
		r.daemon.loginRes.Credential = c
		r.daemon.mu.Unlock()
		r.clock.Advance(loginAllWindow)
		cookies = append(cookies, r.login(t))
	}
	var bodies []*pausedBody
	var outs []<-chan *httptest.ResponseRecorder
	pause := func(c *http.Cookie) {
		b := newPausedBody(streamOK)
		bodies = append(bodies, b)
		outs = append(outs, r.pausedStream(context.Background(), b, c))
		<-b.reading
	}
	for i := 0; i < streamHandshakesPer; i++ {
		pause(cookies[0])
	}
	// A fourth of the credential is refused, its body unread.
	extra := newPausedBody(streamOK)
	close(extra.letGo)
	w := within(t, r.pausedStream(context.Background(), extra, cookies[0]), waitBound)
	if w.Code != 429 || w.Body.String() != streamTextLimited || w.Header().Get("Retry-After") != "25" || extra.reads.Load() != 0 {
		t.Errorf("a fourth handshake of the credential: %d %q %q, %d reads", w.Code, w.Body, w.Header().Get("Retry-After"), extra.reads.Load())
	}
	for len(bodies) < streamHandshakes {
		pause(cookies[1+len(bodies)%2])
	}
	if r.handshakes() != streamHandshakes || r.streams() != 0 {
		t.Fatalf("%d handshakes and %d streams, want %d and none", r.handshakes(), r.streams(), streamHandshakes)
	}
	extra = newPausedBody(streamOK)
	close(extra.letGo)
	// cookies[1] has two handshakes in progress: the refusal is the total's.
	w = within(t, r.pausedStream(context.Background(), extra, cookies[1]), waitBound)
	if w.Code != 503 || w.Body.String() != streamTextFull || extra.reads.Load() != 0 {
		t.Errorf("a handshake past %d: %d %q, %d reads", streamHandshakes, w.Code, w.Body, extra.reads.Load())
	}
	if n := wd.watchCalls(); n != 0 {
		t.Errorf("a handshake in progress reached the watcher %d times", n)
	}
	// Every end gives its place back: a bad body, a stream that opens and
	// ends.
	bodies[0].body = "{"
	close(bodies[0].letGo)
	if w := within(t, outs[0], waitBound); w.Code != 400 {
		t.Errorf("a bad body: %d", w.Code)
	}
	// The stream's timers stand still, so that it cannot end on its own
	// (at its rotation, say), which gives its handshake back too.
	stillTimers(r)
	ctx, cancel := context.WithCancel(context.Background())
	opened := r.streamAsync(ctx, streamOK, cookies[0])
	r.waitStreams(t, 1)
	// The stream counts as open when its place is taken, and gives its
	// handshake back once its first heartbeat is written, a moment after,
	// while it is still open.
	r.waitHandshakes(t, streamHandshakes-1)
	if n := r.streams(); n != 1 {
		t.Errorf("the handshake was given back with %d streams open, want the one still open", n)
	}
	cancel()
	within(t, opened, waitBound)
	for i, b := range bodies[1:] {
		b.body = "null"
		close(b.letGo)
		within(t, outs[i+1], waitBound)
	}
	if r.handshakes() != 0 || r.streams() != 0 {
		t.Errorf("%d handshakes and %d streams left", r.handshakes(), r.streams())
	}
}

// deadlineWriter is a client that reads everything at once and records the
// write deadlines the stream sets, by the stream's clock.
type deadlineWriter struct {
	h         http.Header
	mu        sync.Mutex
	deadlines []time.Time
	writes    []time.Time // the deadline in force at each write
	wrote     chan struct{}
}

func newDeadlineWriter() *deadlineWriter {
	return &deadlineWriter{h: http.Header{}, wrote: make(chan struct{}, 16)}
}

func (w *deadlineWriter) Header() http.Header { return w.h }
func (w *deadlineWriter) WriteHeader(int)     {}
func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	var d time.Time
	if n := len(w.deadlines); n > 0 {
		d = w.deadlines[n-1]
	}
	w.writes = append(w.writes, d)
	w.mu.Unlock()
	w.wrote <- struct{}{}
	return len(b), nil
}
func (w *deadlineWriter) FlushError() error { return nil }
func (w *deadlineWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, t)
	return nil
}

// last returns the deadline set last, and the one in force at the last
// write.
func (w *deadlineWriter) last() (set, wrote time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deadlines[len(w.deadlines)-1], w.writes[len(w.writes)-1]
}

// TestSeatStreamDeadlines: the response has one cutoff, streamLife after
// the request arrived, its admission included; a write has the earlier of
// streamWrite and the time left; between writes the cutoff alone is in
// force, so a heartbeat's gap of more than streamWrite does not end the
// stream; and the end of the response is bounded by the cutoff too.
func TestSeatStreamDeadlines(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	r.app.streamNow = r.clock.Now
	r.app.streamGap = time.Millisecond
	s := r.login(t)
	entry := r.clock.Now()
	cutoff := entry.Add(streamLife)

	dw := newDeadlineWriter()
	b := newPausedBody(streamOK)
	req := streamRequest(context.Background(), streamPath, "", s)
	req.Body = io.NopCloser(b)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.h.ServeHTTP(dw, req)
	}()
	<-b.reading
	// The admission takes 3 seconds of the life.
	r.clock.Advance(3 * time.Second)
	close(b.letGo)
	<-dw.wrote
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	set, wrote := dw.last()
	if !wrote.Equal(entry.Add(3*time.Second + streamWrite)) {
		t.Errorf("the first write had the deadline %v, want streamWrite after it began", wrote.Sub(entry))
	}
	if !set.Equal(cutoff) {
		t.Errorf("after the first write the deadline is %v, want the cutoff %v", set.Sub(entry), streamLife)
	}
	// A quiet heartbeat's gap, longer than streamWrite: nothing is armed
	// for it but the cutoff, and the heartbeat goes out.
	ft.advance(streamBeat)
	<-dw.wrote
	set, wrote = dw.last()
	if !wrote.Equal(entry.Add(3*time.Second+streamBeat+streamWrite)) || !set.Equal(cutoff) {
		t.Errorf("a heartbeat after a quiet gap: write %v, then %v", wrote.Sub(entry), set.Sub(entry))
	}
	// A write that begins 3 seconds before the cutoff has those 3 seconds.
	waitTimers(t, ft, 3)
	r.clock.Advance(cutoff.Add(-3 * time.Second).Sub(r.clock.Now()))
	wd.set(func(st *WatchState) { st.Version = "bb" })
	<-dw.wrote
	if _, wrote = dw.last(); !wrote.Equal(cutoff) {
		t.Errorf("a write near the end had the deadline %v, want the cutoff %v", wrote.Sub(entry), streamLife)
	}
	// The life ends streamFinish before the cutoff; the end of the
	// response is bounded by it.
	ft.advance(cutoff.Add(-streamFinish).Sub(r.clock.Now()))
	select {
	case <-done:
	case <-time.After(waitBound):
		t.Fatal("the stream outlived its life")
	}
	set, _ = dw.last()
	if set.After(cutoff) {
		t.Errorf("the end of the response may go to %v, past the cutoff %v", set.Sub(entry), streamLife)
	}
	dw.mu.Lock()
	for _, d := range dw.deadlines {
		if d.After(cutoff) {
			t.Errorf("a deadline at %v, past the cutoff", d.Sub(entry))
		}
	}
	dw.mu.Unlock()
}

// TestSeatStreamLifeCountsTheAdmission: an admission that outlasts the
// stream's life is refused before a byte of the stream.
func TestSeatStreamLifeCountsTheAdmission(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	s := r.login(t)
	b := newPausedBody(streamOK)
	got := r.pausedStream(context.Background(), b, s)
	<-b.reading
	ft.advance(streamLife)
	close(b.letGo)
	w := within(t, got, waitBound)
	if w.Code != 503 || w.Body.String() != streamTextFull {
		t.Errorf("an admission past the life: %d %q", w.Code, w.Body)
	}
	noStream(t, "past the life", w)
	if r.streams() != 0 || r.handshakes() != 0 {
		t.Errorf("%d streams and %d handshakes left", r.streams(), r.handshakes())
	}
}
