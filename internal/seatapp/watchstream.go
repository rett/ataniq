package seatapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The hint stream of the card list: POST /api/watch/stream, a response of
// server-sent events that says, while the page is open, that the pending
// cards moved. The long poll (watchpoll.go) stays, as the way of every page
// that cannot stream and of every failure of the stream. The stream is fed
// by the same watcher as the poll (one seat.wait at the gate for the whole
// seat), so a stream, its heartbeats and its reconnects ask the gate
// nothing.
//
// What crosses it is a closed set: one kind of event, streamKindCards, whose
// data is the version a poll answers ({"version":"<hex>"}), and a heartbeat
// comment. No card text, id, count, title or time is ever written to it. An
// event only prompts the page's own read: the list reloads by the rules of
// watch.js, as after a poll's "changed".

// streamPath is the stream's one address.
const streamPath = "/api/watch/stream"

// streamKindCards is the one kind of event the stream writes.
const streamKindCards = "cards-changed"

// The limits of the stream.
const (
	// streamLife is the longest a stream stays open: the seat ends it and
	// the page opens a new one. It is under the seat's 30-second write
	// timeout and the proxy's, so that no write has to outlast them.
	streamLife = 25 * time.Second
	// streamBeat is the gap between two heartbeats, a comment line that
	// shows the connection is alive to the page and to the proxies.
	streamBeat = 10 * time.Second
	// streamGap is the least time between two events of a stream; changes
	// that come within it are one event, with the version of the last.
	streamGap = 2 * time.Second
	// streamWrite bounds each write: a client that does not read within it
	// loses its stream, and the page opens another or falls back to the
	// poll.
	streamWrite = 5 * time.Second
	// streamPerCredential is the most streams open at once for one
	// credential, across its sessions: one for each of three tabs.
	streamPerCredential = 3
	// streamOpens and streamOpenWindow bound how often one credential's
	// sessions open a stream: a rotated stream is opened again about
	// every 25 seconds, so three tabs open about 7 a minute.
	streamOpens      = 12
	streamOpenWindow = time.Minute
	// streamMax is the most streams open at once, of all credentials.
	streamMax = 32
	// streamEventMax is the most bytes of one event, its lines included.
	streamEventMax = 256
)

// The Retry-After of the stream's refusals, in seconds. Each is a fixed
// number: the page falls back to the poll whatever it says.
const (
	streamRetryLimited = 60 // the credential opened too many streams in a minute
	streamRetryFull    = 25 // a stream's life: one of the open streams ends by then
	streamRetryNotLive = 30 // the poll's wait while the desk is out of reach
	streamRetryDown    = 5  // the seat is stopping
)

// The fixed texts of the stream's refusals.
const (
	streamTextEnded   = "session ended\n"
	streamTextLimited = "too many streams\n"
	streamTextFull    = "busy\n"
	streamTextNotLive = "not live\n"
	streamTextDown    = "shutting down\n"
)

// streamHeartbeat is the heartbeat: a comment, which a reader of the stream
// skips.
var streamHeartbeat = []byte(":\n\n")

// streamBody is the body of POST /api/watch/stream: the version the page
// has. A newer version at the seat is said at once, as the first event.
type streamBody struct {
	Version string `json:"version"`
}

// streamHint is the data of a cards-changed event.
type streamHint struct {
	Version string `json:"version"`
}

// streamEvent returns the bytes of a cards-changed event for version, and
// false when they would not fit in streamEventMax (a version a poll takes
// always fits).
func streamEvent(version string) ([]byte, bool) {
	data, err := json.Marshal(streamHint{Version: version})
	if err != nil {
		return nil, false
	}
	b := make([]byte, 0, streamEventMax)
	b = append(b, "event: "+streamKindCards+"\ndata: "...)
	b = append(b, data...)
	b = append(b, "\n\n"...)
	if len(b) > streamEventMax {
		return nil, false
	}
	return b, true
}

// streamLive reports whether a state can be streamed: the watcher watches,
// reaches the gate, and has a version a poll takes. Any other state ends
// the stream, and the page's poll then says what it is.
func streamLive(s WatchState) bool {
	return s.Watching && !s.Offline && validWatchVersion(s.Version)
}

// streamSlots counts the open streams, by credential and in all, and ends
// them all when the server shuts down.
type streamSlots struct {
	mu    sync.Mutex
	total int
	per   map[string]int
	down  chan struct{}
	ended bool
}

func newStreamSlots() *streamSlots {
	return &streamSlots{per: map[string]int{}, down: make(chan struct{})}
}

// The refusals of acquire.
const (
	slotOK = iota
	slotCredential
	slotFull
	slotDown
)

// acquire takes a place for a stream of credential, and returns its release,
// which the stream runs when it ends. It refuses past streamPerCredential
// for the credential, past streamMax in all, and once the slots are ended.
func (s *streamSlots) acquire(credential string) (release func(), refusal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.ended:
		return nil, slotDown
	case s.per[credential] >= streamPerCredential:
		return nil, slotCredential
	case s.total >= streamMax:
		return nil, slotFull
	}
	s.per[credential]++
	s.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.total--
			if s.per[credential]--; s.per[credential] <= 0 {
				delete(s.per, credential)
			}
		})
	}, slotOK
}

// end ends every open stream and refuses every new one. It is safe to call
// more than once.
func (s *streamSlots) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.ended = true
		close(s.down)
	}
}

// open returns how many streams are open, in all and of credential.
func (s *streamSlots) open(credential string) (total, mine int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.per[credential]
}

// streamEnder is what [Server] looks for in a handler to end its streams
// when the server shuts down: http.Server.Shutdown waits for every request
// in flight and cancels none, so without it a stream would hold the
// shutdown until its life ended.
type streamEnder interface {
	endStreams()
}

// appHandler is the handler of [New]: the app's routes, and the end of its
// streams for [Server].
type appHandler struct {
	http.Handler
	a *app
}

func (h *appHandler) endStreams() { h.a.streams.end() }

// endStreams ends the streams of the App's handler.
func (x *App) endStreams() {
	if e, ok := x.Handler.(streamEnder); ok {
		e.endStreams()
	}
}

// refuseStream answers a stream that is not opened: a fixed text, and a
// Retry-After when the page may try again later.
func refuseStream(w http.ResponseWriter, status int, text string, retry int) {
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	writeText(w, status, text)
}

// watchStream answers POST /api/watch/stream. Every check is made before a
// header is written: the checks of every POST (a.post: Origin, Host,
// Sec-Fetch-Site, a JSON body), exactly one session cookie of a live session,
// looked at and never marked used (no stream, heartbeat or reconnect extends
// the session's idle time), a body of the stream's form, the credential's
// opens and open streams, all streams together, and a watcher that is live.
// A refusal is a fixed text with no redirect, which the page takes as the
// sign to fall back to the poll.
//
// Once open, the stream ends when its life is over, when the session ends
// (logout, eviction, its idle time or its age, even in silence: a timer is
// armed at the earlier of the two), when the watcher is no longer live, when
// a write fails or outlasts streamWrite, when the request ends, and when the
// server shuts down. The session is looked at again before every write.
func (a *app) watchStream(w http.ResponseWriter, r *http.Request) {
	s, gone, until, ok := a.sess.lookUntil(cookieValue(r, SessionCookie))
	if !ok {
		refuseStream(w, http.StatusForbidden, streamTextEnded, 0)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, bodyError(err))
		return
	}
	var in streamBody
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		writeError(w, errBadRequest)
		return
	}
	if err := decodeBody(bytes.NewReader(raw), &in); err != nil {
		writeError(w, bodyError(err))
		return
	}
	if !validWatchVersion(in.Version) {
		writeError(w, errBadRequest)
		return
	}
	select {
	case <-a.streams.down:
		refuseStream(w, http.StatusServiceUnavailable, streamTextDown, streamRetryDown)
		return
	default:
	}
	if !a.streamOpens.allow(s.Credential, a.cfg.Now()) {
		refuseStream(w, http.StatusTooManyRequests, streamTextLimited, streamRetryLimited)
		return
	}
	wt, ok := a.watcher()
	if !ok {
		refuseStream(w, http.StatusServiceUnavailable, streamTextNotLive, streamRetryNotLive)
		return
	}
	// The channel before the state: a change between the two is not
	// missed.
	ch := wt.WatchChanged()
	st := wt.Watch()
	if !streamLive(st) {
		refuseStream(w, http.StatusServiceUnavailable, streamTextNotLive, streamRetryNotLive)
		return
	}
	release, refusal := a.streams.acquire(s.Credential)
	switch refusal {
	case slotCredential:
		refuseStream(w, http.StatusTooManyRequests, streamTextLimited, streamRetryFull)
		return
	case slotFull:
		refuseStream(w, http.StatusServiceUnavailable, streamTextFull, streamRetryFull)
		return
	case slotDown:
		refuseStream(w, http.StatusServiceUnavailable, streamTextDown, streamRetryDown)
		return
	}
	defer release()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// Cache-Control: no-store is on every response (guard). This one asks
	// a proxy in front not to buffer the stream; a proxy that does not know
	// it ignores it.
	h.Set("X-Accel-Buffering", "no")
	sw := &streamWriter{w: w, rc: http.NewResponseController(w), wait: a.streamWrite}
	// The end of the response (the chunked body's last chunk) is written
	// after the handler returns: it gets a deadline of its own, whatever
	// the last write's was.
	defer func() { sw.rc.SetWriteDeadline(time.Now().Add(a.streamWrite)) }()
	w.WriteHeader(http.StatusOK)
	if sw.send(streamHeartbeat) != nil {
		return
	}
	a.runStream(r, sw, wt, ch, st, in.Version, s.key, gone, until)
}

// runStream is the open stream's loop. sent is the version the page has,
// st the state as the stream opened, and ch the channel taken before it.
func (a *app) runStream(r *http.Request, sw *streamWriter, wt Watcher, ch <-chan struct{}, st WatchState,
	sent string, k tokenKey, gone <-chan struct{}, until time.Time) {
	timer := a.streamTimer
	if timer == nil {
		timer = realNoticeTimer
	}
	life, stopLife := timer(a.streamLife)
	defer stopLife()
	beat, stopBeat := timer(a.streamBeat)
	defer func() { stopBeat() }()
	sessionLeft := func(until time.Time) time.Duration {
		return max(until.Sub(a.cfg.Now()), time.Millisecond)
	}
	sessEnd, stopSess := timer(sessionLeft(until))
	defer func() { stopSess() }()
	// cooling is set for streamGap after an event; a change in it is
	// pending, and is said when the gap ends, with the state as it is
	// then.
	var gap <-chan time.Time
	stopGap := func() {}
	defer func() { stopGap() }()
	pending := false

	// alive looks at the session again, without marking it used, before
	// a write; the server's end is looked at too.
	alive := func() bool {
		select {
		case <-a.streams.down:
			return false
		case <-gone:
			return false
		default:
		}
		_, ok := a.sess.liveUntil(k)
		return ok
	}
	// say writes the event of the version now, when it is not the one the
	// page has.
	say := func(s WatchState) bool {
		if s.Version == sent {
			return true
		}
		b, ok := streamEvent(s.Version)
		if !ok || !alive() || sw.send(b) != nil {
			return false
		}
		sent = s.Version
		gap, stopGap = timer(a.streamGap)
		return true
	}
	if st.Version != sent && !say(st) {
		return
	}
	for {
		select {
		case <-ch:
			ch = wt.WatchChanged()
			st = wt.Watch()
			if !streamLive(st) {
				return
			}
			if st.Version == sent {
				continue
			}
			if gap != nil {
				pending = true
				continue
			}
			if !say(st) {
				return
			}
		case <-gap:
			stopGap()
			gap, stopGap = nil, func() {}
			if pending {
				pending = false
				st = wt.Watch()
				if !streamLive(st) || !say(st) {
					return
				}
			}
		case <-beat:
			stopBeat()
			if !alive() || sw.send(streamHeartbeat) != nil {
				return
			}
			beat, stopBeat = timer(a.streamBeat)
		case <-sessEnd:
			stopSess()
			u, ok := a.sess.liveUntil(k)
			if !ok {
				return
			}
			sessEnd, stopSess = timer(sessionLeft(u))
		case <-life:
			return
		case <-gone:
			return
		case <-a.streams.down:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// streamWriter writes the stream, each write with its own deadline, and
// flushes it at once.
type streamWriter struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	wait time.Duration
}

// send writes b and flushes it. A writer that takes no deadline (a test's
// recorder) is written without one; the server's own write timeout still
// bounds the response.
func (sw *streamWriter) send(b []byte) error {
	if err := sw.rc.SetWriteDeadline(time.Now().Add(sw.wait)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := sw.w.Write(b); err != nil {
		return err
	}
	return sw.rc.Flush()
}
