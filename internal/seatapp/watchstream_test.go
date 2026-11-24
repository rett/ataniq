package seatapp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	deadline := time.Now().Add(5 * time.Second)
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
	deadline := time.Now().Add(5 * time.Second)
	for ft.pending() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d timers armed, want %d", ft.pending(), n)
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
	if b, ok := c.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("first block %q %v, want the heartbeat", b, ok)
	}
	wd.set(func(st *WatchState) {
		st.Version, st.Waiting = "bb", 7
		st.Cards = []WatchCard{{ID: testCardID, Kind: "approval", BindingSHA256: strings.Repeat("ab", 32)}}
	})
	b, ok := c.nextEvent(t, 3*time.Second)
	if !ok || b != "event: cards-changed\ndata: {\"version\":\"bb\"}" {
		t.Fatalf("event %q %v", b, ok)
	}
	// A change that leaves the version says nothing.
	wd.set(func(st *WatchState) { st.Waiting = 8 })
	wd.set(func(st *WatchState) { st.Version = "cc" })
	if b, ok := c.nextEvent(t, 3*time.Second); !ok || b != "event: cards-changed\ndata: {\"version\":\"cc\"}" {
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
	if b, ok := c.nextEvent(t, 3*time.Second); !ok || b != "event: cards-changed\ndata: {\"version\":\"bb\"}" {
		t.Fatalf("event %q %v", b, ok)
	}
	// With the current version there is nothing to say.
	c2 := openStream(t, srv, `{"version":"bb"}`, s)
	if b, ok := c2.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("first block %q %v", b, ok)
	}
	select {
	case b := <-c2.blocks:
		t.Errorf("a block with nothing changed: %q", b)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSeatStreamCoalesces: changes within the gap after an event are one
// event, with the last version, at the gap's end.
func TestSeatStreamCoalesces(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	r.app.streamGap = 300 * time.Millisecond
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	c.next(t, 3*time.Second)
	wd.set(func(st *WatchState) { st.Version = "b1" })
	first, _ := c.nextEvent(t, 3*time.Second)
	start := time.Now()
	for _, v := range []string{"b2", "b3", "b4", "b5"} {
		wd.set(func(st *WatchState) { st.Version = v })
	}
	second, _ := c.nextEvent(t, 3*time.Second)
	if !strings.Contains(first, `"b1"`) || !strings.Contains(second, `"b5"`) {
		t.Errorf("events %q then %q, want b1 and then the last version", first, second)
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Errorf("the second event came %v after the changes, inside the gap", el)
	}
	select {
	case b := <-c.blocks:
		if b != ":" {
			t.Errorf("a third block: %q", b)
		}
	case <-time.After(400 * time.Millisecond):
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
	if streamLife > 25*time.Second || streamLife+streamWrite > wt || streamBeat >= streamLife {
		t.Errorf("life %v, write %v, beat %v, write timeout %v", streamLife, streamWrite, streamBeat, wt)
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
	c.next(t, 3*time.Second)
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	// Two heartbeats, then the life's end.
	ft.advance(streamBeat)
	if b, ok := c.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("heartbeat %q %v", b, ok)
	}
	waitTimers(t, ft, 3)
	ft.advance(streamBeat)
	if b, ok := c.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("heartbeat %q %v", b, ok)
	}
	waitTimers(t, ft, 3)
	ft.advance(streamLife - 2*streamBeat)
	c.ended(t, 3*time.Second)
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
	w := within(t, got, 3*time.Second)
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
	within(t, got, 3*time.Second)
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
	c.next(t, 3*time.Second)
	for i := 0; i < 3; i++ {
		waitTimers(t, ft, 3)
		ft.advance(streamBeat)
		c.next(t, 3*time.Second)
		wd.set(func(st *WatchState) { st.Version = fmt.Sprintf("%02x", i) })
		c.nextEvent(t, 3*time.Second)
	}
	if !used().Equal(start) {
		t.Errorf("the stream marked the session used: %v, was %v", used(), start)
	}
	// The session ends 30 minutes after the login, with the stream open.
	waitTimers(t, ft, 3)
	ft.advance(SessionIdle - 5*time.Minute - 3*streamBeat)
	c.ended(t, 3*time.Second)
	if _, ok := r.sessionOf(s.Value); ok {
		t.Error("the session outlived its idle time with a stream open")
	}
}

// TestSeatStreamEndsAtLogout: a logout ends the stream at once, in silence.
func TestSeatStreamEndsAtLogout(t *testing.T) {
	r, _ := newWatchRig(t, "aa", time.Minute)
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	c.next(t, 3*time.Second)
	if w := r.postWith("/api/logout", "{}", s); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	c.ended(t, 3*time.Second)
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
		c.next(t, 3*time.Second)
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
		c.ended(t, 3*time.Second)
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
	// blockedAt is when the first blocked write began, zero before.
	blockedAt time.Time
	blocked   atomic.Int32
}

func (w *slowWriter) Header() http.Header { return w.h }
func (w *slowWriter) WriteHeader(int)     {}
func (w *slowWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	d := w.deadline
	first := w.writes == 1
	if !first && w.blockedAt.IsZero() {
		w.blockedAt = time.Now()
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
	r.app.streamWrite = 150 * time.Millisecond
	r.app.streamGap = time.Millisecond
	s := r.login(t)
	srv := streamServer(t, r)
	fast := openStream(t, srv, streamOK, s)
	fast.next(t, 3*time.Second)

	slow := &slowWriter{h: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.h.ServeHTTP(slow, streamRequest(context.Background(), streamPath, streamOK, s))
	}()
	r.waitStreams(t, 2)
	// The first change is the slow stream's event, whose write blocks.
	wd.set(func(st *WatchState) { st.Version = "ee" })
	for slow.blocked.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if b, ok := fast.nextEvent(t, time.Second); !ok || !hintRe.MatchString(b) {
		t.Errorf("the other stream: %q %v", b, ok)
	}
	// The watcher publishes without waiting for anyone, and the other
	// stream hears of it while the slow one is stuck.
	for i := 0; i < 20; i++ {
		setAt := time.Now()
		wd.set(func(st *WatchState) { st.Version = fmt.Sprintf("%02x", i) })
		if time.Since(setAt) > 50*time.Millisecond {
			t.Fatal("publishing waited for a stream")
		}
	}
	if b, ok := fast.nextEvent(t, time.Second); !ok || !hintRe.MatchString(b) {
		t.Errorf("the other stream: %q %v", b, ok)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the slow stream was not ended by its write deadline")
	}
	slow.mu.Lock()
	el := time.Since(slow.blockedAt)
	slow.mu.Unlock()
	if el < r.app.streamWrite-10*time.Millisecond {
		t.Errorf("the slow stream ended %v after its write blocked, before its deadline", el)
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
		within(t, o, 3*time.Second)
	}
	r.waitStreams(t, 0)
	got := r.streamAsync(context.Background(), streamOK, other)
	r.waitStreams(t, 1)
	r.postWith("/api/logout", "{}", other)
	within(t, got, 3*time.Second)
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
func TestSeatStreamEndsAtShutdown(t *testing.T) {
	for _, viaOpen := range []bool{false, true} {
		t.Run(fmt.Sprintf("open=%v", viaOpen), func(t *testing.T) {
			r, _ := newWatchRig(t, "aa", time.Minute)
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
			c.next(t, 3*time.Second)
			c2 := openStream(t, ts, streamOK, s)
			c2.next(t, 3*time.Second)
			r.waitStreams(t, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			if el := time.Since(start); el > 3*time.Second {
				t.Errorf("shutdown took %v", el)
			}
			c.ended(t, 3*time.Second)
			c2.ended(t, 3*time.Second)
			r.waitStreams(t, 0)
			if err := <-served; !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve: %v", err)
			}
			if w := r.streamDo(context.Background(), streamOK, nil, s); w.Code != 503 || w.Body.String() != streamTextDown {
				t.Errorf("a stream after the shutdown: %d %q", w.Code, w.Body)
			}
			deadline := time.Now().Add(3 * time.Second)
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
	s := r.login(t)
	x := &App{Handler: r.h, stop: func() {}}
	got := r.streamAsync(context.Background(), streamOK, s)
	r.waitStreams(t, 1)
	x.Close()
	within(t, got, 3*time.Second)
}

// TestSeatStreamDoesNotSuppressNotices: an open stream, its heartbeats and
// its events are not a poll: they neither hold the notices back nor count
// the cards as seen.
func TestSeatStreamDoesNotSuppressNotices(t *testing.T) {
	r, wd := newWatchRig(t, "aa", time.Minute)
	ft := &fakeTimers{clk: r.clock}
	r.app.streamTimer = ft.timer
	s := r.login(t)
	srv := streamServer(t, r)
	c := openStream(t, srv, streamOK, s)
	c.next(t, 3*time.Second)
	waitTimers(t, ft, 3)
	ft.advance(streamBeat)
	c.next(t, 3*time.Second)
	wd.set(func(st *WatchState) { st.Version, st.Waiting = "bb", 3 })
	c.nextEvent(t, 3*time.Second)
	if !r.app.lastPollTime().IsZero() {
		t.Error("the stream counted as a poll for the notices")
	}
	if seq, _ := r.app.pollSeen(); seq != 0 {
		t.Error("the stream counted the cards as seen")
	}
	// The notice core, given the time of the last poll (none), sends.
	var core noticeCore
	if d := core.decide(r.clock.Now(), wd.Watch(), r.app.lastPollTime()); !d.Send || d.N != 3 {
		t.Errorf("with a stream open the notice is %+v", d)
	}
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
