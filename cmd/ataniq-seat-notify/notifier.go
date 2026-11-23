package main

import (
	"context"
	"crypto/ecdh"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ataniq/internal/seatnotice"
	"ataniq/internal/webpush"
)

const (
	// topic is the Topic of every push: a notice not yet delivered is
	// replaced by the next one, so a phone that was off is told once.
	topic = "waiting"
	// maxPushesPerHour is the notifier's own bound on pushes, all
	// subscriptions together, whatever the seat asks: a seat that went wrong
	// cannot make the phone buzz without end. Each push attempt counts.
	maxPushesPerHour = 30
	// pushTimeout bounds one push; the connection's turn (2 seconds, see
	// seatnotice.DefaultDeadline) bounds all the pushes of one notify.
	pushTimeout = 5 * time.Second
	// codeRateLimited is answered when the hour's pushes are spent and none
	// could be sent; the seat tries again after its own backoff.
	codeRateLimited = "rate_limited"
)

// pusher is the sender of webpush as the notifier uses it.
type pusher interface {
	Send(ctx context.Context, sub webpush.Subscription, payload []byte, opt webpush.Options) error
}

// limiter allows at most max takes in any window, counted from the clock it
// is given. It is safe for concurrent use.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	stamps []time.Time
}

func newLimiter(max int, window time.Duration, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{max: max, window: window, now: now}
}

// take spends one place of the window and reports whether there was one.
func (l *limiter) take() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	keep := l.stamps[:0]
	for _, t := range l.stamps {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	l.stamps = keep
	if len(l.stamps) >= l.max {
		return false
	}
	l.stamps = append(l.stamps, now)
	return true
}

// notifier is the seatnotice.Handler of the unit: it holds the VAPID key's
// public half, the subscriptions and the sender, and is the only part that
// reaches a push service. It logs fixed text and counts, never an endpoint
// or a key.
type notifier struct {
	pub   string
	store *store
	push  pusher
	allow func(endpoint string) bool
	ttl   time.Duration
	limit *limiter
	// turn lets one notify send at a time; a call that cannot get it before
	// its context ends is answered unavailable.
	turn chan struct{}
	logf func(format string, args ...any)
}

var _ seatnotice.Handler = (*notifier)(nil)

func newNotifier(key *webpush.VAPIDKey, st *store, push pusher, allow func(string) bool, ttl time.Duration, limit *limiter, logf func(string, ...any)) *notifier {
	return &notifier{
		pub: key.PublicKeyString(), store: st, push: push, allow: allow, ttl: ttl,
		limit: limit, turn: make(chan struct{}, 1), logf: logf,
	}
}

// endpointAllowed is the policy of the endpoints the notifier keeps and sends
// to: an https URL whose host is one the host policy accepts, with no port
// but 443. (The sender holds to the same, and refuses an IP literal as well:
// this is the same refusal made at subscribe, where the owner can see it.)
func endpointAllowed(hosts webpush.HostPolicy) func(string) bool {
	return func(endpoint string) bool {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.User != nil {
			return false
		}
		if p := u.Port(); p != "" && p != "443" {
			return false
		}
		host := strings.ToLower(u.Hostname())
		return host != "" && hosts(host)
	}
}

func (n *notifier) Info(ctx context.Context) (seatnotice.Info, error) {
	return seatnotice.Info{VAPIDPublic: n.pub, Subscriptions: n.store.count()}, nil
}

// Subscribe keeps s: refused as bad_subscription when it is not a valid
// subscription (a p256dh that is not a point of the curve included), as
// endpoint_refused when its host is not allowed, as too_many when four others
// are kept. A subscription with the endpoint of one kept replaces it.
func (n *notifier) Subscribe(ctx context.Context, s seatnotice.Subscription) error {
	if seatnotice.ValidateSubscription(s) != nil {
		return seatnotice.NewCodedError(seatnotice.CodeBadSubscription)
	}
	if p, err := webpush.DecodeKey(s.P256DH); err != nil || !onCurve(p) {
		return seatnotice.NewCodedError(seatnotice.CodeBadSubscription)
	}
	if !n.allow(s.Endpoint) {
		return seatnotice.NewCodedError(seatnotice.CodeEndpointRefused)
	}
	switch err := n.store.put(s); {
	case err == nil:
		n.logf("ataniq-seat-notify: a subscription was kept (%d held)\n", n.store.count())
		return nil
	case errors.Is(err, errTooMany):
		return seatnotice.NewCodedError(seatnotice.CodeTooMany)
	default:
		n.logf("ataniq-seat-notify: a subscription could not be kept: %v\n", err)
		return errors.New("store")
	}
}

func onCurve(p []byte) bool {
	_, err := ecdh.P256().NewPublicKey(p)
	return err == nil
}

// Unsubscribe drops the subscription whose endpoint hashes to the given
// hash; one that is not there is not an error.
func (n *notifier) Unsubscribe(ctx context.Context, endpointSHA256 string) error {
	if !seatnotice.ValidEndpointHash(endpointSHA256) {
		return seatnotice.NewCodedError(seatnotice.CodeBadSubscription)
	}
	dropped, err := n.store.remove(endpointSHA256)
	if err != nil {
		n.logf("ataniq-seat-notify: a subscription could not be dropped: %v\n", err)
		return errors.New("store")
	}
	n.logf("ataniq-seat-notify: %d subscriptions dropped on request\n", dropped)
	return nil
}

// Notify sends {"n":N} to every subscription, one after the other, with the
// topic "waiting", urgency normal and the configured TTL, as far as the
// call's context allows (its turn is 2 seconds) and the hour's bound of
// pushes. A subscription the push service says is gone is dropped; any other
// failure keeps it. It returns how many pushes the service accepted. When
// none was and something other than a gone subscription stopped them, it
// answers rate_limited (the hour's pushes were spent) or unavailable, so the
// seat tries again later; a notify with nothing to send, or only gone
// subscriptions, is a success with none sent.
func (n *notifier) Notify(ctx context.Context, count int) (int, error) {
	if count < 0 || count > seatnotice.MaxN {
		return 0, seatnotice.NewCodedError(seatnotice.CodeUnavailable)
	}
	subs := n.store.list()
	if len(subs) == 0 {
		return 0, nil
	}
	select {
	case n.turn <- struct{}{}:
		defer func() { <-n.turn }()
	case <-ctx.Done():
		return 0, seatnotice.NewCodedError(seatnotice.CodeUnavailable)
	}
	payload := []byte(`{"n":` + strconv.Itoa(count) + `}`)
	opt := webpush.Options{TTL: n.ttl, Urgency: webpush.UrgencyNormal, Topic: topic}
	var sent, failed, limited int
	var gone []string
	for i, s := range subs {
		if ctx.Err() != nil {
			failed += len(subs) - i
			break
		}
		if !n.limit.take() {
			limited = len(subs) - i
			break
		}
		ws := webpush.Subscription{Endpoint: s.Endpoint}
		var kerr, aerr error
		ws.P256dh, kerr = webpush.DecodeKey(s.P256DH)
		ws.Auth, aerr = webpush.DecodeKey(s.Auth)
		if kerr != nil || aerr != nil {
			failed++
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, pushTimeout)
		err := n.push.Send(pctx, ws, payload, opt)
		cancel()
		switch {
		case err == nil:
			sent++
		case errors.Is(err, webpush.ErrGone):
			gone = append(gone, seatnotice.EndpointSHA256(s.Endpoint))
		default:
			failed++
		}
	}
	dropped := 0
	if len(gone) > 0 {
		var err error
		if dropped, err = n.store.remove(gone...); err != nil {
			n.logf("ataniq-seat-notify: gone subscriptions could not be dropped: %v\n", err)
		}
	}
	n.logf("ataniq-seat-notify: notify: %d of %d pushes accepted, %d failed, %d dropped as gone, %d held back by the hourly bound\n",
		sent, len(subs), failed, dropped, limited)
	if sent == 0 {
		switch {
		case limited > 0:
			return 0, seatnotice.NewCodedError(codeRateLimited)
		case failed > 0:
			return 0, seatnotice.NewCodedError(seatnotice.CodeUnavailable)
		}
	}
	return sent, nil
}
