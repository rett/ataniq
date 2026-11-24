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

// The hint stream: POST /api/watch/stream, a response of server-sent events
// that says, while a page is open, that what it shows moved. It has two
// views, which the opening body names by its one member, and which share
// every admission, limit and lifetime rule below:
//
//   - The card list's ({"version":"<hex>"}): the pending cards moved. The
//     long poll (watchpoll.go) stays, as the way of every page that cannot
//     stream and of every failure of the stream. It is fed by the same
//     watcher as the poll (one seat.wait at the gate for the whole seat).
//   - The Leads pages' ({"leads":"<cursor>"}): the leads' chat the seat may
//     read moved (watchleads.go). It is fed by the chat hint the same
//     watcher learns on the same wait; it has no poll to fall back to.
//
// So a stream, its heartbeats and its reconnects ask the gate nothing.
//
// What crosses it is a closed set: for the card list one kind of event,
// streamKindCards, whose data is the version a poll answers
// ({"version":"<hex>"}); for the Leads pages one kind, streamKindChat, whose
// data is the chat cursor alone, a decimal integer; and a heartbeat comment.
// No card text, id, count, title or time, and no lead, head, state, count or
// text of the chat, is ever written to it. An event only prompts the page's
// own read, by the rules of watch.js.
//
// For the notices, a card list's stream's opening is the page's check-in: the page
// opens one only while it is shown. It takes a foreground lease (see
// foreground), and the version the page says it shows, when it is the
// watcher's, counts the cards waiting as seen. A hint the seat writes is
// not seen until the page shows it, reloads, and checks in with it. A Leads
// stream is no check-in: it takes no lease and credits nothing as seen.

// streamPath is the stream's one address.
const streamPath = "/api/watch/stream"

// The kinds of event the stream writes: one for each view.
const (
	streamKindCards = "cards-changed"
	streamKindChat  = "chat-changed"
)

// The views of the stream. The opening body names one by its one member:
// "version" the card list's, "leads" the Leads pages'.
const (
	streamViewCards = "cards"
	streamViewLeads = "leads"
)

// The limits of the stream.
const (
	// streamLife is the longest a stream's response lasts, counted from
	// the request's arrival at the handler, its admission and its end
	// included: the seat ends it and the page opens a new one. It is under
	// the seat's 30-second write timeout and the proxy's, so that no write
	// has to outlast them.
	streamLife = 25 * time.Second
	// streamBeat is the gap between two heartbeats, a comment line that
	// shows the connection is alive to the page and to the proxies.
	streamBeat = 10 * time.Second
	// streamGap is the least time between two events of a stream; changes
	// that come within it are one event, with the version of the last.
	streamGap = 2 * time.Second
	// streamWrite bounds each write: a client that does not read within it
	// loses its stream, and the page opens another or falls back to the
	// poll. A write that begins less than streamWrite before the cutoff has
	// only what is left.
	streamWrite = 5 * time.Second
	// streamFinish is the part of the life kept for the end of the
	// response: the stream stops writing this long before its cutoff.
	streamFinish = time.Second
	// streamHandshakes and streamHandshakesPer bound the requests that are
	// in their admission at once (their bodies being read), in all and for
	// one credential: a page has one at a time.
	streamHandshakes    = 8
	streamHandshakesPer = 3
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
	// streamLease is how long a stream's opening counts the owner as
	// looking at the card list, for the notices: the stream's life and the
	// page's reconnect after it.
	streamLease = 30 * time.Second
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

// streamOpening reads the body of POST /api/watch/stream: an object with
// exactly one member, a string, whose name is the view. "version" is the
// card list's: a version the poll takes (the version the page shows; empty
// for a page made while the watcher had none). "leads" is the Leads pages':
// the chat cursor the page was rendered with, a decimal integer of at most
// 2^53-1 with no sign and no leading zero, "0" for a page made while there
// was none (validChatToken). A newer version or cursor at the seat is said
// at once, as the first event. The body is read token by token, so that a
// member that is absent, null, of another type, of another name (in any
// case) or given twice, two members of either view, and anything after the
// object are refused.
func streamOpening(raw []byte) (view, token string, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	want := func(d json.Delim) bool {
		t, err := dec.Token()
		return err == nil && t == d
	}
	str := func() (string, bool) {
		t, err := dec.Token()
		v, ok := t.(string)
		return v, err == nil && ok
	}
	if !want('{') {
		return "", "", false
	}
	k, ok := str()
	if !ok {
		return "", "", false
	}
	v, ok := str()
	if !ok || !want('}') {
		return "", "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", false
	}
	switch k {
	case "version":
		return streamViewCards, v, validWatchVersion(v)
	case "leads":
		return streamViewLeads, v, validChatToken(v)
	}
	return "", "", false
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
	return sseEvent(streamKindCards, data)
}

// sseEvent returns the bytes of an event of kind with data, one line of it,
// and false when they would not fit in streamEventMax.
func sseEvent(kind string, data []byte) ([]byte, bool) {
	b := make([]byte, 0, streamEventMax)
	b = append(b, "event: "+kind+"\ndata: "...)
	b = append(b, data...)
	b = append(b, "\n\n"...)
	if len(b) > streamEventMax || bytes.ContainsAny(data, "\r\n") {
		return nil, false
	}
	return b, true
}

// streamLive reports whether a state can be streamed: the watcher watches,
// reaches the gate, and has a version a poll takes that is not empty (an
// empty one is a watcher that has no answer of the gate yet). Any other
// state ends the stream, and the page's poll then says what it is.
func streamLive(s WatchState) bool {
	return s.Watching && !s.Offline && s.Version != "" && validWatchVersion(s.Version)
}

// streamSource is what a view of the stream reads: the broadcast it waits
// on, its token now (the Cards version, or the chat cursor) with whether
// the view is live, and the event that says a token. checkIn is the card
// list's alone: the page's check-in for the notices, which takes the
// foreground lease and credits the count the page shows as seen, and
// returns the lease's end. A Leads stream has none.
type streamSource struct {
	changed func() <-chan struct{}
	current func() (token string, live bool)
	event   func(token string) ([]byte, bool)
	checkIn func(k tokenKey, now, until time.Time, shows string) (end func())
}

// streamSource returns the source of view, and false when the port has
// none (it does not watch, or has no chat hint).
func (a *app) streamSource(view string) (streamSource, bool) {
	if view == streamViewLeads {
		return a.leadsSource()
	}
	wt, ok := a.watcher()
	if !ok {
		return streamSource{}, false
	}
	// st is the state current last read; checkIn runs right after the
	// admission's read, on the same goroutine, so it is that state.
	var st WatchState
	return streamSource{
		changed: wt.WatchChanged,
		current: func() (string, bool) {
			st = wt.Watch()
			return st.Version, streamLive(st)
		},
		event: streamEvent,
		checkIn: func(k tokenKey, now, until time.Time, shows string) func() {
			end := a.fg.grant(k, now, until)
			if shows == st.Version {
				a.sawWaiting(st.Waiting)
			}
			return end
		},
	}, true
}

// foreground holds the leases of the card lists' streams: while one lasts,
// the owner counts as looking at the list, and the notices are held back,
// as for a poll within noticeQuiet. A lease is taken when a stream opens,
// the page's check-in, and lasts streamLease, at most to the session's own
// end; only a new check-in (the page's next stream, which the rotation
// brings) makes a new one. Nothing the seat writes renews it, since a
// suspended phone can leave a proxy taking the seat's writes. It ends early
// when its stream ends for any reason but its life, and with its session.
type foreground struct {
	mu     sync.Mutex
	leases map[uint64]lease
	next   uint64
}

// lease is one stream's: the session it is of, and when it runs out.
type lease struct {
	key   tokenKey
	until time.Time
}

func newForeground() *foreground { return &foreground{leases: map[uint64]lease{}} }

// grant takes a lease for the session of k at now, to the earlier of
// streamLease and sessionEnd, and returns its end, which is safe to run
// more than once.
func (f *foreground) grant(k tokenKey, now, sessionEnd time.Time) (end func()) {
	until := now.Add(streamLease)
	if sessionEnd.Before(until) {
		until = sessionEnd
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next
	f.next++
	f.leases[id] = lease{key: k, until: until}
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.leases, id)
	}
}

// live returns the leases that have not run out at now, and drops the
// others.
func (f *foreground) live(now time.Time) []lease {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []lease
	for id, l := range f.leases {
		if !now.Before(l.until) {
			delete(f.leases, id)
			continue
		}
		out = append(out, l)
	}
	return out
}

// foregroundUntil is when the last live lease of a live session runs out,
// zero when there is none.
func (a *app) foregroundUntil(now time.Time) time.Time {
	var out time.Time
	for _, l := range a.fg.live(now) {
		if _, ok := a.sess.liveUntil(l.key); ok && l.until.After(out) {
			out = l.until
		}
	}
	return out
}

// streamSlots counts the open streams, by credential and in all, and ends
// them all when the server shuts down.
type streamSlots struct {
	mu    sync.Mutex
	total int
	per   map[string]int
	down  chan struct{}
	ended bool
	// shaking and shakingPer count the handshakes in progress, in all and
	// by credential, at most shakeMax and shakePerMax (streamHandshakes and
	// streamHandshakesPer; tests lower them).
	shaking               int
	shakingPer            map[string]int
	shakeMax, shakePerMax int
}

func newStreamSlots() *streamSlots {
	return &streamSlots{per: map[string]int{}, down: make(chan struct{}), shakingPer: map[string]int{},
		shakeMax: streamHandshakes, shakePerMax: streamHandshakesPer}
}

// handshake takes a place for a request of credential in its admission,
// before its body is read, and returns its release, which is safe to run
// more than once: the stream runs it once its headers are written, and
// every exit before that runs it too. It refuses past shakePerMax for the
// credential, past shakeMax in all, and once the slots are ended.
func (s *streamSlots) handshake(credential string) (release func(), refusal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.ended:
		return nil, slotDown
	case s.shakingPer[credential] >= s.shakePerMax:
		return nil, slotCredential
	case s.shaking >= s.shakeMax:
		return nil, slotFull
	}
	s.shakingPer[credential]++
	s.shaking++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.shaking--
			if s.shakingPer[credential]--; s.shakingPer[credential] <= 0 {
				delete(s.shakingPer, credential)
			}
		})
	}, slotOK
}

// handshakes returns how many handshakes are in progress, in all and of
// credential.
func (s *streamSlots) handshakes(credential string) (total, mine int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shaking, s.shakingPer[credential]
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
// the session's idle time), a place among the handshakes in progress, which
// is taken before the body is read, a body of the stream's form, the
// credential's opens and open streams, all streams together, a watcher that
// is live (for the Leads view: a chat hint that is live while the gate
// offers chat), and, last, the session again (it may have ended while the
// body came), the server's end and the request's. A refusal is a fixed text
// with no redirect, which the card list takes as the sign to fall back to
// the poll, and a Leads page as the sign to keep to its Refresh link.
//
// The response has one cutoff, streamLife after the request reached the
// handler: its admission, its writes and the end of the response are all
// done by then. Each write has the earlier of streamWrite and the time left
// to the cutoff; between writes the cutoff alone bounds the connection.
//
// Once open, the stream ends when its life is over, when the session ends
// (logout, eviction, its idle time or its age, even in silence: a timer is
// armed at the earlier of the two), when its view is no longer live (the
// watcher, or the chat hint: a lost link, a gate that stops offering it, a
// downgrade), when a write fails or outlasts its deadline, when the request
// ends, and when the server shuts down. The session, and the view, are
// looked at again before every write.
func (a *app) watchStream(w http.ResponseWriter, r *http.Request) {
	timer := a.streamTimer
	if timer == nil {
		timer = realNoticeTimer
	}
	now := a.streamNow
	if now == nil {
		now = time.Now
	}
	// The life is armed first: the admission is part of it.
	cutoff := now().Add(a.streamLife)
	life, stopLife := timer(max(a.streamLife-streamFinish, a.streamLife/2))
	defer stopLife()

	s, gone, _, ok := a.sess.lookUntil(cookieValue(r, SessionCookie))
	if !ok {
		refuseStream(w, http.StatusForbidden, streamTextEnded, 0)
		return
	}
	// A place among the handshakes, before a byte of the body is read or
	// the watcher is looked at: a session cannot hold many bodies open.
	shook, refusal := a.streams.handshake(s.Credential)
	if !refuseSlot(w, refusal) {
		return
	}
	defer shook()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, bodyError(err))
		return
	}
	view, shows, ok := streamOpening(raw)
	if !ok {
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
	release, refusal := a.streams.acquire(s.Credential)
	if !refuseSlot(w, refusal) {
		return
	}
	defer release()
	src, ok := a.streamSource(view)
	if !ok {
		refuseStream(w, http.StatusServiceUnavailable, streamTextNotLive, streamRetryNotLive)
		return
	}
	// The channel before the state: a change between the two is not
	// missed.
	ch := src.changed()
	now0, live := src.current()
	if !live {
		refuseStream(w, http.StatusServiceUnavailable, streamTextNotLive, streamRetryNotLive)
		return
	}
	// The last look before the stream's headers: the session may have
	// ended while the body came (a logout, an eviction, its idle time), the
	// server may be stopping, the client may have gone, and the life may
	// have run out in the admission.
	until, ok := a.sess.liveUntil(s.key)
	select {
	case <-gone:
		ok = false
	default:
	}
	if !ok {
		refuseStream(w, http.StatusForbidden, streamTextEnded, 0)
		return
	}
	select {
	case <-a.streams.down:
		refuseStream(w, http.StatusServiceUnavailable, streamTextDown, streamRetryDown)
		return
	case <-life:
		refuseStream(w, http.StatusServiceUnavailable, streamTextFull, streamRetryFull)
		return
	default:
	}
	if r.Context().Err() != nil {
		return
	}

	// The stream is open. A card list checked in, visible (a hidden page
	// opens none): it holds the notices back while its lease lasts, and the
	// version it says it shows, when it is the watcher's, is the count the
	// owner has seen. A Leads page checks in for nothing.
	endLease := func() {}
	if src.checkIn != nil {
		endLease = src.checkIn(s.key, a.cfg.Now(), until, shows)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// Cache-Control: no-store is on every response (guard). This one asks
	// a proxy in front not to buffer the stream; a proxy that does not know
	// it ignores it.
	h.Set("X-Accel-Buffering", "no")
	sw := &streamWriter{w: w, rc: http.NewResponseController(w), wait: a.streamWrite, cutoff: cutoff, now: now}
	// The end of the response (the chunked body's last chunk) is written
	// after the handler returns: it too is bounded by the cutoff.
	defer sw.finish()
	w.WriteHeader(http.StatusOK)
	if sw.send(streamHeartbeat) != nil {
		endLease()
		return
	}
	shook()
	// A stream the seat rotated leaves its lease to run out, which covers
	// the page's reconnect; any other end (the page hidden, left or gone,
	// a failed write, the session's end, the server's) ends it now.
	if !a.runStream(r, sw, timer, life, src, ch, now0, shows, s.key, gone, until) {
		endLease()
	}
}

// refuseSlot answers the refusal of a place for a stream, and reports
// whether there was none (slotOK).
func refuseSlot(w http.ResponseWriter, refusal int) bool {
	switch refusal {
	case slotCredential:
		refuseStream(w, http.StatusTooManyRequests, streamTextLimited, streamRetryFull)
	case slotFull:
		refuseStream(w, http.StatusServiceUnavailable, streamTextFull, streamRetryFull)
	case slotDown:
		refuseStream(w, http.StatusServiceUnavailable, streamTextDown, streamRetryDown)
	default:
		return true
	}
	return false
}

// runStream is the open stream's loop. sent is the token the page has (the
// version, or the chat cursor), cur the view's token as the stream opened,
// and ch the channel taken before it. It reports whether the stream ended
// at its life, the seat's own rotation, rather than for any other reason.
func (a *app) runStream(r *http.Request, sw *streamWriter, timer noticeTimer, life <-chan time.Time, src streamSource,
	ch <-chan struct{}, cur string, sent string, k tokenKey, gone <-chan struct{}, until time.Time) (rotated bool) {
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
	// say writes the event of the token now, when it is not the one the
	// page has.
	say := func(tok string) bool {
		if tok == sent {
			return true
		}
		b, ok := src.event(tok)
		if !ok || !alive() || sw.send(b) != nil {
			return false
		}
		sent = tok
		gap, stopGap = timer(a.streamGap)
		return true
	}
	if cur != sent && !say(cur) {
		return false
	}
	for {
		select {
		case <-ch:
			ch = src.changed()
			tok, live := src.current()
			if !live {
				return false
			}
			if tok == sent {
				continue
			}
			if gap != nil {
				pending = true
				continue
			}
			if !say(tok) {
				return false
			}
		case <-gap:
			stopGap()
			gap, stopGap = nil, func() {}
			if pending {
				pending = false
				tok, live := src.current()
				if !live || !say(tok) {
					return false
				}
			}
		case <-beat:
			stopBeat()
			// The view is looked at again before a heartbeat too: a chat
			// hint can stop being live (its connection gone) before its
			// watcher says so.
			if _, live := src.current(); !live || !alive() || sw.send(streamHeartbeat) != nil {
				return false
			}
			beat, stopBeat = timer(a.streamBeat)
		case <-sessEnd:
			stopSess()
			u, ok := a.sess.liveUntil(k)
			if !ok {
				return false
			}
			sessEnd, stopSess = timer(sessionLeft(u))
		case <-life:
			return true
		case <-gone:
			return false
		case <-a.streams.down:
			return false
		case <-r.Context().Done():
			return false
		}
	}
}

// streamWriter writes the stream, flushes each write at once, and keeps the
// response inside its cutoff.
type streamWriter struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	wait time.Duration
	// cutoff is when the response is over, its end included; now is the
	// clock the deadlines are taken by.
	cutoff time.Time
	now    func() time.Time
}

// errStreamCutoff is a write asked at or past the cutoff.
var errStreamCutoff = errors.New("seatapp: the stream's cutoff has passed")

// deadline sets the connection's write deadline. A writer that takes none
// (a test's recorder) is written without one; the server's own write
// timeout still bounds the response.
func (sw *streamWriter) deadline(t time.Time) error {
	if err := sw.rc.SetWriteDeadline(t); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// bound is the deadline of a write that begins now: streamWrite, or the
// time left to the cutoff when that is less.
func (sw *streamWriter) bound() time.Time {
	if t := sw.now().Add(sw.wait); t.Before(sw.cutoff) {
		return t
	}
	return sw.cutoff
}

// send writes b and flushes it, within bound, and then sets the deadline
// back to the cutoff: a wait for the next write (up to a heartbeat's gap,
// which is longer than streamWrite) is bounded by the cutoff alone, never
// by the last write's deadline.
func (sw *streamWriter) send(b []byte) error {
	if !sw.now().Before(sw.cutoff) {
		return errStreamCutoff
	}
	if err := sw.deadline(sw.bound()); err != nil {
		return err
	}
	if _, err := sw.w.Write(b); err != nil {
		return err
	}
	if err := sw.rc.Flush(); err != nil {
		return err
	}
	return sw.deadline(sw.cutoff)
}

// finish sets the deadline of the end of the response, which the server
// writes after the handler returns: within bound, so never past the
// cutoff.
func (sw *streamWriter) finish() { sw.deadline(sw.bound()) }
