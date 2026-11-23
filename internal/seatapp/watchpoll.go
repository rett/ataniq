package seatapp

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

// The limits of the watch poll.
const (
	// watchRequests and watchWindow bound the polls of one credential's
	// sessions, apart from the reads: a poll asks the gate nothing, so it
	// takes no room of the gate's, but a script that polls without pause
	// must end.
	watchRequests = 12
	watchWindow   = time.Minute
	// watchHold is the longest a poll is held. It is under the seat's
	// 30-second write timeout and the proxy's.
	watchHold = 25 * time.Second
	// maxHeldPolls is the most polls of one session held at once, one for
	// each tab the owner has open; a poll past it takes the place of the
	// oldest, which is answered "replaced".
	maxHeldPolls = 3
	// maxWatchVersion is the most characters of the version a poll carries.
	maxWatchVersion = 64
)

// The card states of an answer to a poll.
const (
	cardLive    = "live"
	cardChanged = "changed"
	cardGone    = "gone"
	cardUnknown = "unknown"
)

// The states of an answer to a poll.
const (
	watchSame    = "same"
	watchChanged = "changed"
	watchOffline = "offline"
	watchEnded   = "ended"
	// watchReplaced answers a held poll that a newer one of its session
	// took the place of; the script waits out a hold before it asks again.
	watchReplaced = "replaced"
)

// watcher returns the daemon's watcher, if the port has one.
func (a *app) watcher() (Watcher, bool) {
	w, ok := a.cfg.Daemon.(Watcher)
	return w, ok
}

// watchPage is what a page that polls carries: the watcher's version as the
// page's read begins, and, for the Card page, the card and the binding it
// shows. Every field is the app's own, checked, or empty.
type watchPage struct {
	// Live says the port watches, so the page's script may poll.
	Live    bool
	Version string
	Card    string
	Binding string
	// Still marks a Card page that names no card, because the card is not
	// pending: the page polls for the status line, and never reloads.
	Still bool
}

// watchVersion takes the watcher's state for a page. It must run before the
// page's own read of the daemon, so that a change between the two makes the
// page's version older than what it shows, and the page's first poll says
// "changed" at once, never the other way round.
func (a *app) watchVersion() watchPage {
	w, ok := a.watcher()
	if !ok {
		return watchPage{}
	}
	s := w.Watch()
	if !s.Watching || !validWatchVersion(s.Version) {
		return watchPage{Live: s.Watching}
	}
	return watchPage{Live: true, Version: s.Version}
}

// withCard adds the card and the binding a Card page shows, each only when
// it is of the form a poll takes back.
func (p watchPage) withCard(id, binding string) watchPage {
	if validCardID(id) && validHex(binding, 64) {
		p.Card, p.Binding = id, binding
	}
	return p
}

// still marks the page a Card page of a card that is not pending: it carries
// no card, so a poll never says the card is gone, and the script does not
// treat it as a list that reloads.
func (p watchPage) still() watchPage {
	p.Still = true
	return p
}

// validWatchVersion reports whether s is a version a poll takes: lower-case
// hex, at most maxWatchVersion characters, possibly none.
func validWatchVersion(s string) bool { return validHex(s, maxWatchVersion) }

// validHex reports whether s is at most max lower-case hex digits.
func validHex(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// watchBody is the body of POST /api/watch: the version the page has, and
// for a Card page the card and binding it shows.
type watchBody struct {
	Version string `json:"version"`
	Card    string `json:"card"`
	Binding string `json:"binding"`
}

func (b watchBody) valid() bool {
	if !validWatchVersion(b.Version) || !validHex(b.Binding, 64) {
		return false
	}
	if b.Card == "" {
		return b.Binding == ""
	}
	return validCardID(b.Card)
}

// watchAnswer is the answer to a poll. It holds no card text.
type watchAnswer struct {
	State   string `json:"state"`
	Version string `json:"version"`
	Waiting int    `json:"waiting"`
	Card    string `json:"card"`
}

// cardState says where the card a page shows stands in s: gone when it is
// not in the list and the list is the whole set, changed when it is listed
// with another binding, unknown when the list cannot say (more cards than it
// lists, or no live state), and live otherwise. With no card named there is
// nothing to say: unknown.
func cardState(s WatchState, id, binding string) string {
	if id == "" || !s.Watching || s.Offline {
		return cardUnknown
	}
	for _, c := range s.Cards {
		if c.ID != id {
			continue
		}
		if binding != "" && c.BindingSHA256 != binding {
			return cardChanged
		}
		return cardLive
	}
	if s.More {
		return cardUnknown
	}
	return cardGone
}

// answerOf builds the answer of a poll from the state now.
func answerOf(s WatchState, in watchBody) watchAnswer {
	out := watchAnswer{State: watchSame, Version: s.Version, Waiting: s.Waiting, Card: cardState(s, in.Card, in.Binding)}
	switch {
	case !s.Watching || s.Offline:
		out.State = watchOffline
	case s.Version != in.Version:
		out.State = watchChanged
	}
	return out
}

// watchPost answers POST /api/watch: a long poll that asks the daemon port's
// watcher and never the gate. The session is only looked at, never marked
// used (a poll does not extend its idle time). The answer is immediate when
// the port does not watch, is offline, or has a version other than the
// poll's; otherwise the poll is held until the state changes, the hold ends
// (same), a newer poll of the session takes its place (replaced), the
// request ends, or the session does (ended). At most maxHeldPolls polls of a
// session are held, one for each tab, and the oldest is replaced past that.
func (a *app) watchPost(w http.ResponseWriter, r *http.Request) {
	s, gone, ok := a.sess.look(cookieValue(r, SessionCookie))
	if !ok {
		writeJSON(w, http.StatusOK, watchAnswer{State: watchEnded, Card: cardUnknown})
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, bodyError(err))
		return
	}
	// JSON null would decode to the empty poll: only an object is a poll.
	var in watchBody
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		writeError(w, errBadRequest)
		return
	}
	if err := decodeBody(bytes.NewReader(raw), &in); err != nil {
		writeError(w, bodyError(err))
		return
	}
	if !in.valid() {
		writeError(w, errBadRequest)
		return
	}
	if !a.watches.allow(s.Credential, a.cfg.Now()) {
		writeError(w, errRateLimited)
		return
	}
	a.polled()
	wt, ok := a.watcher()
	if !ok {
		writeJSON(w, http.StatusOK, watchAnswer{State: watchOffline, Card: cardUnknown})
		return
	}
	// The channel before the state: a change between the two wakes the
	// hold below, and is never missed.
	ch := wt.WatchChanged()
	st := wt.Watch()
	if st.Watching && !st.Offline {
		a.sawWaiting(st.Waiting)
	}
	if !st.Watching || st.Offline || st.Version != in.Version {
		writeJSON(w, http.StatusOK, answerOf(st, in))
		return
	}
	kick, release, ok := a.sess.holdWatch(s.key)
	if !ok {
		writeJSON(w, http.StatusOK, watchAnswer{State: watchEnded, Card: cardUnknown})
		return
	}
	defer release()
	hold := time.NewTimer(a.watchHold)
	defer hold.Stop()
	select {
	case <-ch:
	case <-hold.C:
	case <-kick:
		// A fourth tab took this poll's place: say so, so that this page
		// waits out a hold's length rather than taking the place back at
		// once, which would end a poll of another tab without end.
		writeJSON(w, http.StatusOK, watchAnswer{State: watchReplaced, Version: in.Version, Card: cardUnknown})
		return
	case <-gone:
		writeJSON(w, http.StatusOK, watchAnswer{State: watchEnded, Card: cardUnknown})
		return
	case <-r.Context().Done():
		return
	}
	// The session may have ended by its idle time or its age meanwhile.
	if _, _, ok := a.sess.look(cookieValue(r, SessionCookie)); !ok {
		writeJSON(w, http.StatusOK, watchAnswer{State: watchEnded, Card: cardUnknown})
		return
	}
	writeJSON(w, http.StatusOK, answerOf(wt.Watch(), in))
}
