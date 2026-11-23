package seatapp

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"
)

// The limits of a decision from the phone.
const (
	// maxReadings is the most cards a session keeps its readings of; the
	// one read least recently goes first.
	maxReadings = 16
	// maxSignOffs is the most sign-off indexes a decide-challenge body may
	// carry, each below it: a decision record of 8 KiB holds fewer.
	maxSignOffs = 300
	// decideRequests and decideWindow bound the decision challenges one
	// credential's sessions may ask for. The port keeps the decisions to a
	// room of their own at the gate (package seatlink: 16 calls in 80
	// seconds, 8 decisions), which logins, pairings and reads cannot take,
	// and asks a challenge only with room for its decision as well, kept for
	// that challenge until the app drops it (ChallengeDropper): a tap past
	// it is answered desk_busy before any challenge, and a decision the
	// phone signed is never refused for the gate's rate.
	decideRequests = 10
	decideWindow   = time.Minute
	// decideTTL is the longest a decision's challenge is kept; the daemon's
	// own expiry is used when it is sooner.
	decideTTL = 5 * time.Minute
	// maxForms is the most decide forms a session's reading of a card keeps:
	// the forms of the Card pages served last, one per page. formBytes is
	// the size of a form's id, drawn at random for each page.
	maxForms  = 8
	formBytes = 16
)

// formLen is the length of a form's id: formBytes in unpadded base64url.
var formLen = b64.EncodedLen(formBytes)

// pageForm is the decide form of one Card page: its id, which the page's
// script sends with a tap, and the binding that page showed.
type pageForm struct {
	id      string
	binding string
}

// cardReading is one session's reading of one card, kept server-side with
// the session.
type cardReading struct {
	// binding, expires, flagged and whole are of the card the Card page
	// last showed: its binding digest, its expiry, how many flagged paths
	// it has, and the parts of it the page showed whole. binding is empty
	// while only pages of its path list were read.
	binding string
	expires time.Time
	flagged int
	whole   Whole
	// opened is when the Card page last showed it.
	opened time.Time
	// pages is the page count of the whole path list, review the review
	// digest those pages were of, and read the pages of it the session
	// read. A page of another review or count starts the count again.
	pages  int
	review string
	read   map[int]bool
	// live says the card was pending when last shown: not ended and no
	// decision on its way.
	live bool
	// sent says a decision of the card was sent and no answer settled it.
	// Only a new read of the card (the Card page, "Look again") clears it,
	// so a lost answer is never followed by a second decision unseen; a read
	// that began before the decision was marked sent (sentAt) does not.
	sent   bool
	sentAt uint64
	// forms are the decide forms of the Card pages of the card served to
	// the session, oldest first, each with the binding its page showed. A
	// tap names its page's form, and is taken only when that page showed
	// the binding the session read last: a page left open in another tab,
	// on a card that was replaced since, is not taken for the new one.
	forms []pageForm
	// touched orders the readings by use.
	touched uint64
}

// public is the reading as the port takes it.
func (r *cardReading) public() Reading {
	out := Reading{Opened: r.opened, Whole: r.whole, Pages: r.pages, Review: r.review}
	for n := range r.read {
		out.PagesRead = append(out.PagesRead, n)
	}
	sort.Ints(out.PagesRead)
	return out
}

// pendingDecision is the one decision a session has a challenge for: the
// frame the port froze, kept server-side until the phone's answer comes.
type pendingDecision struct {
	card        string
	challengeID string
	frame       DecisionFrame
	expires     time.Time
}

// with runs f on the session of k under the table's lock, and reports
// whether there was one.
func (ss *sessions) with(k tokenKey, f func(*session)) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[k]
	if !ok {
		return false
	}
	f(s)
	return true
}

// reading returns the session's reading of a card, made when it has none
// and create is true. With the table full, the one used least recently goes.
func (s *session) reading(id string, create bool) *cardReading {
	s.tick++
	if r, ok := s.cards[id]; ok {
		r.touched = s.tick
		return r
	}
	if !create {
		return nil
	}
	if s.cards == nil {
		s.cards = map[string]*cardReading{}
	}
	for len(s.cards) >= maxReadings {
		var oldest string
		var at uint64
		first := true
		for k, r := range s.cards {
			if first || r.touched < at {
				oldest, at, first = k, r.touched, false
			}
		}
		delete(s.cards, oldest)
	}
	r := &cardReading{touched: s.tick}
	s.cards[id] = r
	return r
}

// begin returns the mark of a read that begins now: a decision marked sent
// later has a higher one.
func (ss *sessions) begin() uint64 {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.seq
}

// opened notes that the Card page showed c to the session at now, from a
// read that began at the mark since, and returns the session's reading of
// it, and fresh. A read of the card settles a lost answer: what the page
// shows is the card's state now. A read that began before a decision of the
// card that is still unsettled was marked sent may show the card as it was
// before it, so it changes nothing and is not fresh.
func (ss *sessions) opened(k tokenKey, c Card, now time.Time, since uint64) (Reading, bool) {
	var out Reading
	fresh := true
	ss.with(k, func(s *session) {
		r := s.reading(c.ID, true)
		if r.sent && r.sentAt > since {
			fresh = false
			out = r.public()
			return
		}
		if r.binding != "" && r.binding != c.BindingSHA256 {
			r.pages, r.review, r.read = 0, "", nil
		}
		r.binding, r.expires, r.flagged, r.opened, r.whole = c.BindingSHA256, c.Expires, len(c.Flagged), now, wholeOf(c)
		r.live = !c.Ended && !c.OnItsWay && (c.State == "" || c.State == "pending")
		r.sent = false
		out = r.public()
	})
	return out, fresh
}

// readPage notes that the session read page n of pages of a card's whole
// path list, of the review given. A page count or a review other than the
// one kept starts the count again.
func (ss *sessions) readPage(k tokenKey, id string, n, pages int, review string) {
	ss.with(k, func(s *session) {
		r := s.reading(id, true)
		if r.pages != pages || r.review != review || r.read == nil {
			r.pages, r.review, r.read = pages, review, map[int]bool{}
		}
		r.read[n] = true
	})
}

// addForm notes the decide form of a Card page that showed a card at the
// binding given to the session. The oldest form goes when there are
// maxForms.
func (ss *sessions) addForm(k tokenKey, id string, f pageForm) {
	ss.with(k, func(s *session) {
		r := s.reading(id, true)
		if len(r.forms) >= maxForms {
			r.forms = append(r.forms[:0:0], r.forms[len(r.forms)-maxForms+1:]...)
		}
		r.forms = append(r.forms, f)
	})
}

// form returns the binding the page of a form showed, and whether the
// reading has that form.
func (r *cardReading) form(id string) (string, bool) {
	for _, f := range r.forms {
		if subtle.ConstantTimeCompare([]byte(f.id), []byte(id)) == 1 {
			return f.binding, true
		}
	}
	return "", false
}

// newFormID draws a form's id from r.
func newFormID(r io.Reader) (string, error) {
	raw := make([]byte, formBytes)
	if _, err := io.ReadFull(r, raw); err != nil {
		return "", err
	}
	return b64.EncodeToString(raw), nil
}

// validFormID reports whether s is of the form of a form's id.
func validFormID(s string) bool {
	if len(s) != formLen {
		return false
	}
	b, ok := decodeB64(s)
	return ok && len(b) == formBytes
}

// readingOf returns a copy of the session's reading of a card.
func (ss *sessions) readingOf(k tokenKey, id string) (cardReading, bool) {
	var out cardReading
	found := false
	ss.with(k, func(s *session) {
		if r := s.reading(id, false); r != nil {
			out, found = *r, true
			out.read = make(map[int]bool, len(r.read))
			for n := range r.read {
				out.read[n] = true
			}
			out.forms = append([]pageForm(nil), r.forms...)
		}
	})
	return out, found
}

// keepDecision keeps the session's one decision; a new one replaces the old,
// which is returned, and whether there was one.
func (ss *sessions) keepDecision(k tokenKey, d pendingDecision) (pendingDecision, bool) {
	var old pendingDecision
	replaced := false
	ss.with(k, func(s *session) {
		if s.decision != nil {
			old, replaced = *s.decision, true
		}
		s.decision = &d
	})
	return old, replaced
}

// dropChallenge tells the port, when it keeps room for a decision's
// challenge, that the decision of the challenge given will never be sent.
func (a *app) dropChallenge(id string) {
	if dr, ok := a.cfg.Daemon.(ChallengeDropper); ok && id != "" {
		dr.DropChallenge(id)
	}
}

// takeDecision burns the session's decision: it is removed whatever comes
// next, and returned when there was one.
func (ss *sessions) takeDecision(k tokenKey) (pendingDecision, bool) {
	var out pendingDecision
	found := false
	ss.with(k, func(s *session) {
		if s.decision != nil {
			out, found = *s.decision, true
			s.decision = nil
		}
	})
	return out, found
}

// markSent marks a card's decision as sent, before it is sent. It refuses
// when the session has no live reading of the card or one already sent.
func (ss *sessions) markSent(k tokenKey, id string) bool {
	ok := false
	ss.with(k, func(s *session) {
		if r := s.reading(id, false); r != nil && r.live && !r.sent {
			ss.seq++
			r.sent, r.sentAt, ok = true, ss.seq, true
		}
	})
	return ok
}

// settle notes the certain answer to a decision that was sent: nothing was
// decided (refused), or the daemon's outcome (stillLive for a held
// approve, which leaves the card pending).
func (ss *sessions) settle(k tokenKey, id string, stillLive bool) {
	ss.with(k, func(s *session) {
		if r := s.reading(id, false); r != nil {
			r.sent, r.live = false, stillLive
		}
	})
}

// The words the phone shows for a decision, the app's own; no text of the
// gate's or of the daemon's but its line, through Text, is ever shown.
const (
	sayStale       = "This took too long, or the card changed. Look at the card and tap again."
	sayChanged     = "The card changed. Look at it again."
	sayExpired     = "The card's time ran out."
	sayCannot      = "The card cannot be decided now. Look at it again."
	sayAssertion   = "The phone's answer was not accepted. If this repeats, check the seat at the desk."
	sayRevoked     = "This phone's passkey is no longer paired. Pair it again at the desk."
	sayKind        = "This project does not take decisions from this kind of passkey."
	sayBusy        = "The desk is busy. Try again in a few minutes."
	sayFailed      = "Something went wrong. Look at the card again."
	sayUnknown     = "Decision sent. Outcome unknown."
	sayUnreachable = "The desk cannot be reached. Nothing was sent."
	sayTooLong     = "This took too long. Tap again."
	sayNotOffered  = "The desk does not take decisions from the phone now."
	sayDiffers     = "The desk's challenge is not this card's, so your passkey was not asked. Nothing was sent. Check the seat at the desk."
	sayLookFirst   = "Look at the card again first."
	saySentUnknown = "A decision of this card was sent and its outcome is not known. Look at the card again first."
	sayHeld        = "Held: the project's limit for today is reached. Nothing was applied. Raise the limit at the desk, then approve again here or at the desk."
	sayDone        = "Sent. The desk answered:"
	sayRate        = "Too many taps. Wait a minute, then try again."
	sayNoSession   = "The session ended. Log in again."
	sayTooLarge    = "This is too large to send."
	sayNotAllowed  = "This cannot be sent: "
)

// decideError is a failure of a decision: a status, a fixed code and the
// fixed words the phone shows.
type decideError struct {
	status int
	code   string
	say    string
}

// The failures of the decision API.
var (
	errDecideStale      = decideError{http.StatusConflict, "challenge_stale", sayStale}
	errDecideChanged    = decideError{http.StatusConflict, "card_changed", sayChanged}
	errDecideExpired    = decideError{http.StatusConflict, "card_expired", sayExpired}
	errDecideCannot     = decideError{http.StatusConflict, "cannot_decide", sayCannot}
	errDecideAssertion  = decideError{http.StatusForbidden, "assertion_refused", sayAssertion}
	errDecideRevoked    = decideError{http.StatusForbidden, "key_revoked", sayRevoked}
	errDecideKind       = decideError{http.StatusForbidden, "kind_refused", sayKind}
	errDecideBusy       = decideError{http.StatusServiceUnavailable, "desk_busy", sayBusy}
	errDecideFailed     = decideError{http.StatusBadGateway, "failed", sayFailed}
	errDecideUnknown    = decideError{http.StatusBadGateway, "outcome_unknown", sayUnknown}
	errDecideUnreached  = decideError{http.StatusServiceUnavailable, "daemon_unavailable", sayUnreachable}
	errDecideTooLong    = decideError{http.StatusConflict, "challenge_expired", sayTooLong}
	errDecideNotOffered = decideError{http.StatusServiceUnavailable, "decide_unavailable", sayNotOffered}
	errDecideDiffers    = decideError{http.StatusBadGateway, "challenge_differs", sayDiffers}
	errDecideLookFirst  = decideError{http.StatusConflict, "look_again", sayLookFirst}
	errDecideSent       = decideError{http.StatusConflict, "sent_unknown", saySentUnknown}
	errDecideRate       = decideError{http.StatusTooManyRequests, "rate_limited", sayRate}
	errDecideNoSession  = decideError{http.StatusUnauthorized, "session_ended", sayNoSession}
	errDecideBad        = decideError{http.StatusBadRequest, "bad_request", sayFailed}
	errDecideTooLarge   = decideError{http.StatusRequestEntityTooLarge, "too_large", sayTooLarge}
)

func writeDecideError(w http.ResponseWriter, e decideError) {
	writeJSON(w, e.status, map[string]string{"error": e.code, "say": e.say})
}

// decideBodyError maps an error of reading a body.
func decideBodyError(err error) decideError {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return errDecideTooLarge
	}
	return errDecideBad
}

// decideFailure maps an error of the port's decision calls to its failure,
// and reports whether it says for certain that nothing was decided. Only
// ErrOutcomeUnknown and errors the app does not tell apart leave it open.
func decideFailure(err error) (decideError, bool) {
	var na *NotAllowedError
	switch {
	case errors.As(err, &na):
		return decideError{http.StatusConflict, "not_allowed", sayNotAllowed + Text(na.Reason, maxLine) + "."}, true
	case errors.Is(err, ErrChallengeStale):
		return errDecideStale, true
	case errors.Is(err, ErrCardChanged):
		return errDecideChanged, true
	case errors.Is(err, ErrCardExpired):
		return errDecideExpired, true
	case errors.Is(err, ErrCannotDecide):
		return errDecideCannot, true
	case errors.Is(err, ErrAssertionRefused):
		return errDecideAssertion, true
	case errors.Is(err, ErrKeyRevoked):
		return errDecideRevoked, true
	case errors.Is(err, ErrKindRefused):
		return errDecideKind, true
	case errors.Is(err, ErrBusy):
		return errDecideBusy, true
	case errors.Is(err, ErrNotOffered):
		return errDecideNotOffered, true
	case errors.Is(err, ErrChallengeDiffers):
		return errDecideDiffers, true
	case errors.Is(err, ErrDaemonUnavailable):
		return errDecideUnreached, true
	case errors.Is(err, ErrTooLarge):
		return errDecideTooLarge, true
	case errors.Is(err, ErrOutcomeUnknown):
		return errDecideUnknown, false
	}
	return errDecideFailed, false
}

// decideSession returns the session of a decision's request; without one it
// answers session_ended.
func (a *app) decideSession(w http.ResponseWriter, r *http.Request) (Session, bool) {
	s, ok := a.session(r)
	if !ok {
		writeDecideError(w, errDecideNoSession)
	}
	return s, ok
}

// failDecide answers a failure of the port. A passkey that is no longer
// paired ends the session.
func (a *app) failDecide(w http.ResponseWriter, r *http.Request, e decideError) {
	if e == errDecideRevoked {
		a.sess.end(cookieValue(r, SessionCookie))
		clearCookie(w, SessionCookie)
	}
	writeDecideError(w, e)
}

// decideChallengeBody is the body of POST /api/decide-challenge: the card,
// the form of the Card page tapped, the action, the flagged paths signed off
// by their index on the card, and the reason of a deny. Nothing else of the
// decision comes from the browser.
type decideChallengeBody struct {
	Card      string `json:"card"`
	Form      string `json:"form"`
	Action    string `json:"action"`
	SignedOff []int  `json:"signed_off"`
	Reason    string `json:"reason"`
}

// decideChallenge answers POST /api/decide-challenge: it checks the
// session's reading of the card and that the tap came from a page that
// showed the binding the session read last, has the port freeze the
// decision from the card it showed and ask the daemon for its challenge,
// keeps the frozen frame with the session, and answers the challenge with
// the session's one credential as the allow-list.
func (a *app) decideChallenge(w http.ResponseWriter, r *http.Request) {
	s, ok := a.decideSession(w, r)
	if !ok {
		return
	}
	var in decideChallengeBody
	if err := decodeBody(r.Body, &in); err != nil {
		writeDecideError(w, decideBodyError(err))
		return
	}
	approve := in.Action == "approve"
	if !validCardID(in.Card) || (!approve && in.Action != "deny") || len(in.SignedOff) > maxSignOffs ||
		(approve && in.Reason != "") || (!approve && len(in.SignedOff) != 0) ||
		!validFormID(in.Form) {
		writeDecideError(w, errDecideBad)
		return
	}
	seen := make(map[int]bool, len(in.SignedOff))
	for _, i := range in.SignedOff {
		if i < 0 || i >= maxSignOffs || seen[i] {
			writeDecideError(w, errDecideBad)
			return
		}
		seen[i] = true
	}
	now := a.cfg.Now()
	rd, ok := a.sess.readingOf(s.key, in.Card)
	if !ok || rd.binding == "" {
		writeDecideError(w, errDecideLookFirst)
		return
	}
	// The tap is of one Card page: the binding that page showed must be the
	// one the session read last, or the page is of a card that was replaced
	// since (in another tab, say), and the tap is not taken for the new one.
	shown, known := rd.form(in.Form)
	switch {
	case !known:
		writeDecideError(w, errDecideLookFirst)
		return
	case shown != rd.binding:
		writeDecideError(w, errDecideChanged)
		return
	}
	switch {
	case rd.sent:
		writeDecideError(w, errDecideSent)
		return
	case !rd.live:
		writeDecideError(w, errDecideCannot)
		return
	case !now.Before(rd.expires):
		// An expired card is not renewed by a new challenge: nothing is
		// asked.
		writeDecideError(w, errDecideExpired)
		return
	}
	for _, i := range in.SignedOff {
		if i >= rd.flagged {
			writeDecideError(w, errDecideBad)
			return
		}
	}
	if !a.decides.allow(s.Credential, now) {
		writeDecideError(w, errDecideRate)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	ch, err := a.cfg.Daemon.DecideChallenge(ctx, DecideChallengeRequest{
		ID: in.Card, BindingSHA256: rd.binding, Approve: approve, SignedOff: in.SignedOff, Reason: in.Reason,
		Credential: s.Credential, Reading: rd.public(), Now: now,
	})
	if err != nil {
		e, _ := decideFailure(err)
		a.failDecide(w, r, e)
		return
	}
	f := ch.Frame
	if len(ch.Challenge) != challengeSize || !validChallengeID(ch.ID) || f.ID != in.Card || f.BindingSHA256 != rd.binding ||
		f.Approve != approve || (approve && len(f.SignedOff) != len(in.SignedOff)) || (!approve && (len(f.SignedOff) != 0 || f.Reason != in.Reason)) {
		a.dropChallenge(ch.ID)
		writeDecideError(w, errDecideFailed)
		return
	}
	expires := now.Add(decideTTL)
	if !ch.Expires.IsZero() && ch.Expires.Before(expires) {
		expires = ch.Expires
	}
	if !now.Before(expires) {
		a.dropChallenge(ch.ID)
		writeDecideError(w, errDecideTooLong)
		return
	}
	// The session keeps one decision: the one this tap replaces is never
	// sent.
	if old, ok := a.sess.keepDecision(s.key, pendingDecision{card: in.Card, challengeID: ch.ID, frame: f, expires: expires}); ok {
		a.dropChallenge(old.challengeID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge_id": ch.ID,
		"challenge":    b64.EncodeToString(ch.Challenge),
		"allow":        []string{b64.EncodeToString(s.CredentialID)},
		"action":       in.Action,
		"card":         Text(f.ID, maxID),
		"binding":      short(f.BindingSHA256),
		"signed_off":   len(f.SignedOff),
		"expires_in":   int(expires.Sub(now) / time.Second),
	})
}

// decideBody is the body of POST /api/decide: the challenge's id and the
// WebAuthn assertion over it.
type decideBody struct {
	ChallengeID       string `json:"challenge_id"`
	CredentialID      string `json:"credential_id"`
	ClientDataJSON    string `json:"client_data_json"`
	AuthenticatorData string `json:"authenticator_data"`
	Signature         string `json:"signature"`
}

// decidePost answers POST /api/decide: it burns the session's decision
// first, whatever comes next, takes the assertion only for that decision's
// challenge and the session's credential, marks the card's decision sent,
// and hands the port the frame it kept, never one the browser sent. A lost
// answer leaves the mark: the card is decided again only after a new read
// of it.
func (a *app) decidePost(w http.ResponseWriter, r *http.Request) {
	s, ok := a.decideSession(w, r)
	if !ok {
		return
	}
	pd, ok := a.sess.takeDecision(s.key)
	// A decision burnt here and not sent is never sent: its challenge is
	// dropped.
	sent := false
	if ok {
		defer func() {
			if !sent {
				a.dropChallenge(pd.challengeID)
			}
		}()
	}
	var in decideBody
	if err := decodeBody(r.Body, &in); err != nil {
		writeDecideError(w, decideBodyError(err))
		return
	}
	now := a.cfg.Now()
	if !ok || !now.Before(pd.expires) || subtle.ConstantTimeCompare([]byte(in.ChallengeID), []byte(pd.challengeID)) != 1 {
		writeDecideError(w, errDecideTooLong)
		return
	}
	credID, ok1 := decodeB64(in.CredentialID)
	clientData, ok2 := decodeB64(in.ClientDataJSON)
	authData, ok3 := decodeB64(in.AuthenticatorData)
	sig, ok4 := decodeB64(in.Signature)
	if !ok1 || !ok2 || !ok3 || !ok4 ||
		len(credID) < minCredentialID || len(credID) > maxCredentialID ||
		len(authData) < minAuthData || len(authData) > maxAuthData ||
		len(clientData) > maxClientData || len(sig) > maxSignature {
		writeDecideError(w, errDecideBad)
		return
	}
	// The session's decisions use its one credential: an assertion of any
	// other is not sent.
	if subtle.ConstantTimeCompare(credID, s.CredentialID) != 1 {
		writeDecideError(w, errDecideAssertion)
		return
	}
	if !a.sess.markSent(s.key, pd.card) {
		writeDecideError(w, errDecideSent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	sent = true
	res, err := a.cfg.Daemon.Decide(ctx, DecideRequest{
		ChallengeID: pd.challengeID, Frame: pd.frame,
		CredentialID: credID, AuthenticatorData: authData, ClientDataJSON: clientData, Signature: sig,
	})
	if err != nil {
		e, certain := decideFailure(err)
		if certain {
			a.sess.settle(s.key, pd.card, true)
		}
		a.failDecide(w, r, e)
		return
	}
	a.sess.settle(s.key, pd.card, res.Held)
	say := sayDone
	if res.Held {
		say = sayHeld
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": Text(res.State, maxID),
		"line":  Text(res.Line, maxLine),
		"held":  res.Held,
		"say":   say,
	})
}

// validChallengeID reports whether s is a challenge's id: 16 lower-case hex
// digits.
func validChallengeID(s string) bool { return validDisplayID(s) }
