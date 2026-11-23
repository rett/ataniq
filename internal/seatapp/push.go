package seatapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PushSubscription is a browser's Web Push subscription as the subscribe
// page sends it: the endpoint of the browser's push service and the two keys
// a message to it is encrypted with. The app checks its form
// ([ValidPushSubscription]) and relays it; the notifier is the only part
// that keeps it, and no page, answer or log of the app ever shows it back.
type PushSubscription struct {
	Endpoint string
	P256DH   string
	Auth     string
}

// PushPort is the optional part of a [Notifier] that lets the phone turn
// notices on and off: the notifier holds the VAPID key and the subscriptions,
// and the app only relays. A notifier that is not a PushPort leaves the
// notices to whatever sends them without a phone to set them up: the pages
// show nothing about notices and every route of this file answers 404.
//
// Info returns the VAPID public key (the unpadded base64url of an
// uncompressed P-256 point) and how many subscriptions the notifier holds.
// Unsubscribe names a subscription by the SHA-256 of its endpoint, 64
// lower-case hex digits. A refusal by the notifier is a [*PushError]; any
// other error is "not reachable".
type PushPort interface {
	Info(ctx context.Context) (vapidPublic string, subscriptions int, err error)
	Subscribe(ctx context.Context, s PushSubscription) error
	Unsubscribe(ctx context.Context, endpointSHA256 string) error
}

// PushError is a refusal of the notifier, with the code it gave
// ([a-z0-9_]{1,40}). The app maps the codes it knows to fixed answers and
// every other to one more; it never shows a code it does not know.
type PushError struct{ Code string }

func (e *PushError) Error() string { return "seatapp: the notifier refused the call" }

// The codes of the notifier the app tells apart (package seatnotice's, which
// this package may not import).
const (
	pushCodeBadSubscription = "bad_subscription"
	pushCodeTooMany         = "too_many"
	pushCodeEndpointRefused = "endpoint_refused"
	pushCodeUnavailable     = "unavailable"
)

// The bounds of a subscription: this package's own copy of package
// seatnotice's, which the notifier checks again (a test of the command holds
// the two to the same answers).
const (
	maxPushEndpoint = 2048
	pushP256DHLen   = 65
	pushAuthLen     = 16
	// maxPushSubscriptions bounds the count the notifier may report.
	maxPushSubscriptions = 999
)

// The limits of the push API: one credential's sessions may call it this
// often, the three routes together. A call that reaches the notifier is a
// call on a socket of its own, apart from the gate, so this bound is for a
// script that taps without pause.
const (
	pushRequests = 6
	pushWindow   = time.Minute
	// pushCall bounds one call to the notifier.
	pushCall = 5 * time.Second
)

// ValidPushSubscription reports whether s has the form the notifier takes: an
// https endpoint of at most 2048 bytes with a host, no user information, no
// fragment and only visible ASCII; a p256dh that is the unpadded base64url of
// 65 bytes, the first 0x04; and an auth that is the unpadded base64url of 16.
func ValidPushSubscription(s PushSubscription) bool {
	if !validPushEndpoint(s.Endpoint) {
		return false
	}
	p, ok := decodePushB64(s.P256DH, pushP256DHLen)
	if !ok || p[0] != 0x04 {
		return false
	}
	_, ok = decodePushB64(s.Auth, pushAuthLen)
	return ok
}

func validPushEndpoint(e string) bool {
	if len(e) == 0 || len(e) > maxPushEndpoint || !strings.HasPrefix(e, "https://") {
		return false
	}
	for i := 0; i < len(e); i++ {
		if e[i] <= 0x20 || e[i] >= 0x7f {
			return false
		}
	}
	u, err := url.Parse(e)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return !strings.Contains(e, "#")
}

// decodePushB64 decodes s, the unpadded base64url of exactly n bytes, with
// its last digit canonical.
func decodePushB64(s string, n int) ([]byte, bool) {
	if len(s) != base64.RawURLEncoding.EncodedLen(n) {
		return nil, false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil && len(b) == n
}

// validPushKey reports whether s is a VAPID public key: the unpadded
// base64url of an uncompressed P-256 point's 65 bytes.
func validPushKey(s string) bool {
	b, ok := decodePushB64(s, pushP256DHLen)
	return ok && b[0] == 0x04
}

// The failures of the push API. Each answer is {"ok":false,"code":CODE} with
// a fixed code: nothing the browser sent, and nothing of the notifier's text,
// is in it.
var (
	errPushNoSession   = apiError{http.StatusUnauthorized, "no_session"}
	errPushBadSub      = apiError{http.StatusBadRequest, pushCodeBadSubscription}
	errPushTooMany     = apiError{http.StatusConflict, pushCodeTooMany}
	errPushEndpoint    = apiError{http.StatusUnprocessableEntity, pushCodeEndpointRefused}
	errPushUnavailable = apiError{http.StatusServiceUnavailable, pushCodeUnavailable}
	errPushNotifier    = apiError{http.StatusBadGateway, "notifier_error"}
)

// pushFailure maps a failed call to the notifier to an answer: a refusal by
// its known code, a refusal by any other code to notifier_error, and a call
// that did not get an answer (not reachable, too slow) to unavailable.
func pushFailure(err error) apiError {
	var pe *PushError
	if !errors.As(err, &pe) {
		return errPushUnavailable
	}
	switch pe.Code {
	case pushCodeBadSubscription:
		return errPushBadSub
	case pushCodeTooMany:
		return errPushTooMany
	case pushCodeEndpointRefused:
		return errPushEndpoint
	case pushCodeUnavailable:
		return errPushUnavailable
	}
	return errPushNotifier
}

// writePushError answers a failure of the push API.
func writePushError(w http.ResponseWriter, e apiError) {
	writeJSON(w, e.status, map[string]any{"ok": false, "code": e.code})
}

// pushOnly makes a route answer 404 when the app has no [PushPort], before
// anything else is checked: without one there is nothing to see.
func (a *app) pushOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.push == nil {
			writeText(w, http.StatusNotFound, "not found\n")
			return
		}
		h(w, r)
	}
}

// serviceWorker serves the service worker's script. It is the one response
// that is not "no-store": a browser checks the script for a change with each
// start, and "no-cache" lets it do so with a conditional request rather than
// never. Service-Worker-Allowed fixes its scope at the whole origin, which
// the script's own path (/sw.js) already gives; the header says it plainly.
func (a *app) serviceWorker() http.HandlerFunc {
	h := a.asset("assets/sw.js", "application/javascript")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/")
		h(w, r)
	}
}

// pushPrelude is what the three calls share: the session (this is a tap of
// the owner's, so it counts as a use of the session), a body that is one JSON
// object of exactly the call's fields, the call's own check of them (valid;
// nil for none), and the per-credential limit. It decodes the body into in,
// and reports false once it has answered.
func (a *app) pushPrelude(w http.ResponseWriter, r *http.Request, in any, valid func() bool) (context.Context, context.CancelFunc, bool) {
	s, ok := a.session(r)
	if !ok {
		writePushError(w, errPushNoSession)
		return nil, nil, false
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writePushError(w, bodyError(err))
		return nil, nil, false
	}
	// JSON null would decode to the empty object: only an object is a call.
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		writePushError(w, errBadRequest)
		return nil, nil, false
	}
	if err := decodeBody(bytes.NewReader(raw), in); err != nil {
		writePushError(w, bodyError(err))
		return nil, nil, false
	}
	if valid != nil && !valid() {
		writePushError(w, errBadRequest)
		return nil, nil, false
	}
	if !a.pushes.allow(s.Credential, a.cfg.Now()) {
		writePushError(w, errRateLimited)
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), pushCall)
	return ctx, cancel, true
}

// pushInfo answers POST /api/push/info: the VAPID public key the browser
// subscribes with, and how many subscriptions the notifier holds. It says
// nothing else of them, and never an endpoint.
func (a *app) pushInfo(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	ctx, cancel, ok := a.pushPrelude(w, r, &in, nil)
	if !ok {
		return
	}
	defer cancel()
	key, n, err := a.push.Info(ctx)
	if err != nil {
		writePushError(w, pushFailure(err))
		return
	}
	// The key goes to the browser's subscribe call: only one of the right
	// form does.
	if !validPushKey(key) || n < 0 || n > maxPushSubscriptions {
		writePushError(w, errPushNotifier)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vapid_public": key, "subscriptions": n})
}

// pushSubscribeBody is the body of POST /api/push/subscribe.
type pushSubscribeBody struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// pushSubscribe answers POST /api/push/subscribe: it checks the form of the
// subscription and relays it to the notifier.
func (a *app) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	var in pushSubscribeBody
	sub := func() PushSubscription {
		return PushSubscription{Endpoint: in.Endpoint, P256DH: in.P256DH, Auth: in.Auth}
	}
	ctx, cancel, ok := a.pushPrelude(w, r, &in, func() bool { return ValidPushSubscription(sub()) })
	if !ok {
		return
	}
	defer cancel()
	if err := a.push.Subscribe(ctx, sub()); err != nil {
		writePushError(w, pushFailure(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pushUnsubscribeBody is the body of POST /api/push/unsubscribe.
type pushUnsubscribeBody struct {
	EndpointSHA256 string `json:"endpoint_sha256"`
}

// pushUnsubscribe answers POST /api/push/unsubscribe: it relays the hash of
// the endpoint to forget. The browser names a subscription by the hash, so
// that the endpoint travels once, in the subscribe call.
func (a *app) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in pushUnsubscribeBody
	ctx, cancel, ok := a.pushPrelude(w, r, &in, func() bool { return len(in.EndpointSHA256) == 64 && validHex(in.EndpointSHA256, 64) })
	if !ok {
		return
	}
	defer cancel()
	if err := a.push.Unsubscribe(ctx, in.EndpointSHA256); err != nil {
		writePushError(w, pushFailure(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
