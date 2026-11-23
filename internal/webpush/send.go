package webpush

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one Send, from the start of the request to the
	// end of the bounded body read, when [Config.Timeout] is zero.
	DefaultTimeout = 15 * time.Second
	// DefaultTokenLife is the life of each VAPID token when
	// [Config.TokenLife] is zero.
	DefaultTokenLife = time.Hour
	// MaxTTL is the longest TTL [Options] may ask for: 28 days.
	MaxTTL = 28 * 24 * time.Hour
	// MaxRetryAfter caps the Retry-After a [StatusError] carries.
	MaxRetryAfter = 24 * time.Hour

	// maxBody is the most of a response body the sender reads.
	maxBody = 4 << 10
	// maxEndpoint bounds the length of an endpoint URL.
	maxEndpoint = 2048
	// maxTopic is the longest Topic of RFC 8030.
	maxTopic = 32
)

// Subscription is what a browser's PushSubscription gives: the endpoint URL
// of the push service, and the decoded p256dh public key (65 bytes) and auth
// secret (16 bytes). [DecodeKey] decodes the base64url text of its keys.
type Subscription struct {
	Endpoint string
	P256dh   []byte
	Auth     []byte
}

// DecodeKey decodes a key of a subscription as the browser gives it:
// base64url, with or without padding.
func DecodeKey(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// Urgency is the Urgency header of RFC 8030 section 5.3.
type Urgency string

// The four urgencies. The empty Urgency sends no header, which a push
// service takes as normal.
const (
	UrgencyVeryLow Urgency = "very-low"
	UrgencyLow     Urgency = "low"
	UrgencyNormal  Urgency = "normal"
	UrgencyHigh    Urgency = "high"
)

// Options are the per-message headers of RFC 8030.
type Options struct {
	// TTL is how long the push service may keep the message for a device it
	// cannot reach, in whole seconds (a fraction is dropped); zero means
	// deliver now or never. It is at most [MaxTTL].
	TTL time.Duration
	// Urgency, when set, is sent as the Urgency header.
	Urgency Urgency
	// Topic, when set, is sent as the Topic header: a later message with the
	// same topic replaces one not yet delivered. It is 1 to 32 characters of
	// the base64url alphabet.
	Topic string
}

// HostPolicy says whether the sender may post to an endpoint whose host is
// host: a lower-case name (or, with [Config.TestEndpoints], an IP literal
// without brackets), with no port. The sender asks it only after its own
// refusals.
type HostPolicy func(host string) bool

// AllowHosts returns a policy that accepts exactly the hosts given, compared
// without regard to case: no wildcard, no subdomain. An empty name is left
// out. The list is copied.
func AllowHosts(hosts ...string) HostPolicy {
	set := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		if h != "" {
			set[strings.ToLower(h)] = true
		}
	}
	return func(host string) bool { return set[strings.ToLower(host)] }
}

// Config is what a [Sender] is made from.
type Config struct {
	// Key signs the VAPID token. Required.
	Key *VAPIDKey
	// Subject is the sub claim: a mailto: or https: URI by which the push
	// service can reach the operator. Required.
	Subject string
	// Client sends the requests. Required. The sender uses a copy of it that
	// follows no redirect and has no cookie jar; its transport is used as
	// it is.
	Client *http.Client
	// Hosts decides which endpoint hosts the sender may post to. Required.
	Hosts HostPolicy
	// TestEndpoints lifts the refusal of an endpoint whose host is an IP
	// literal or whose port is not 443, so a test can post to a local
	// server. Hosts still decides. Never set it outside tests.
	TestEndpoints bool
	// Timeout bounds each Send. Zero means [DefaultTimeout].
	Timeout time.Duration
	// TokenLife is how long each VAPID token is valid, at most
	// [MaxTokenLife]. Zero means [DefaultTokenLife].
	TokenLife time.Duration
	// Now is the clock of the token's exp and of a Retry-After date. Nil
	// means time.Now.
	Now func() time.Time
	// Random supplies the salt and ephemeral key of each message. Nil means
	// crypto/rand.Reader.
	Random io.Reader
}

// Sender sends Web Push messages. It is safe for concurrent use when its
// Random is (crypto/rand.Reader is).
type Sender struct {
	key       *VAPIDKey
	subject   string
	client    *http.Client
	hosts     HostPolicy
	test      bool
	timeout   time.Duration
	tokenLife time.Duration
	now       func() time.Time
	random    io.Reader
}

// ErrConfig is returned by [NewSender] for a [Config] that lacks a required
// field or has one out of range.
var ErrConfig = errors.New("webpush: the sender's configuration is not valid")

// NewSender checks c and returns a sender.
func NewSender(c Config) (*Sender, error) {
	if c.Key == nil || c.Client == nil || c.Hosts == nil || !validSubject(c.Subject) {
		return nil, ErrConfig
	}
	if c.Timeout < 0 || c.TokenLife < 0 || c.TokenLife > MaxTokenLife {
		return nil, ErrConfig
	}
	s := &Sender{
		key: c.Key, subject: c.Subject, hosts: c.Hosts, test: c.TestEndpoints,
		timeout: c.Timeout, tokenLife: c.TokenLife, now: c.Now, random: c.Random,
	}
	if s.timeout == 0 {
		s.timeout = DefaultTimeout
	}
	if s.tokenLife == 0 {
		s.tokenLife = DefaultTokenLife
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.random == nil {
		s.random = rand.Reader
	}
	client := *c.Client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s.client = &client
	return s, nil
}

// ErrEndpoint is returned, wrapped with the reason, for an endpoint the
// sender refuses. Nothing is sent.
var ErrEndpoint = errors.New("webpush: the endpoint is refused")

// ErrOptions is returned for [Options] out of range. Nothing is sent.
var ErrOptions = errors.New("webpush: the message options are not valid")

// Send encrypts payload to sub and posts it to sub's endpoint. It returns nil
// when the push service answers 201 Created, and otherwise an error: one
// wrapping [ErrEndpoint], [ErrOptions], [ErrPayloadTooLarge] or
// [ErrSubscriptionKeys] when nothing was sent, a [*StatusError] for any
// other answer, or the transport's error (without the endpoint in its text)
// when there was no answer, which is context.DeadlineExceeded when the
// timeout ran out.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opt Options) error {
	audience, err := s.checkEndpoint(sub.Endpoint)
	if err != nil {
		return err
	}
	headers, err := optionHeaders(opt)
	if err != nil {
		return err
	}
	body, err := Encrypt(s.random, sub.P256dh, sub.Auth, payload)
	if err != nil {
		return err
	}
	now := s.now()
	auth, err := s.key.AuthorizationHeader(audience, s.subject, now, s.tokenLife)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: it cannot be requested", ErrEndpoint)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// A *url.Error names the URL, and the endpoint is a capability: keep
		// only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("webpush: no answer from the push service: %w", err)
	}
	// Read at most 4 KiB, so a short body can be drained and the connection
	// kept, and a long one cannot hold the sender; then close.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		return nil
	}
	e := &StatusError{Status: resp.StatusCode}
	if e.Status == http.StatusTooManyRequests || (e.Status >= 500 && e.Status <= 599) {
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), s.now())
	}
	return e
}

// checkEndpoint refuses an endpoint the sender must not post to, and returns
// the origin that is the token's audience.
func (s *Sender) checkEndpoint(endpoint string) (string, error) {
	refuse := func(why string) (string, error) { return "", fmt.Errorf("%w: %s", ErrEndpoint, why) }
	if endpoint == "" || len(endpoint) > maxEndpoint {
		return refuse("it is empty or too long")
	}
	if strings.Contains(endpoint, "#") {
		return refuse("it has a fragment")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return refuse("it is not a URL")
	}
	if u.Scheme != "https" || u.Opaque != "" {
		return refuse("it is not https")
	}
	if u.User != nil {
		return refuse("it has credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return refuse("it has no host")
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return refuse("its port is empty")
	}
	isIP := strings.HasPrefix(u.Host, "[") || strings.ContainsAny(host, ":%")
	if _, err := netip.ParseAddr(host); err == nil {
		isIP = true
	}
	if isIP && !s.test {
		return refuse("its host is an IP literal")
	}
	if port != "" && port != "443" && !s.test {
		return refuse("its port is not 443")
	}
	if !s.hosts(host) {
		return refuse("its host is not allowed by the policy")
	}
	origin := host
	if strings.Contains(origin, ":") {
		origin = "[" + origin + "]"
	}
	if port != "" && port != "443" {
		origin += ":" + port
	}
	return "https://" + origin, nil
}

// optionHeaders checks opt and returns the headers it asks for.
func optionHeaders(opt Options) (map[string]string, error) {
	if opt.TTL < 0 || opt.TTL > MaxTTL {
		return nil, fmt.Errorf("%w: the TTL is out of range", ErrOptions)
	}
	h := map[string]string{"TTL": strconv.FormatInt(int64(opt.TTL/time.Second), 10)}
	switch opt.Urgency {
	case "":
	case UrgencyVeryLow, UrgencyLow, UrgencyNormal, UrgencyHigh:
		h["Urgency"] = string(opt.Urgency)
	default:
		return nil, fmt.Errorf("%w: the urgency is not one of the four", ErrOptions)
	}
	if opt.Topic != "" {
		if len(opt.Topic) > maxTopic || !base64urlText(opt.Topic) {
			return nil, fmt.Errorf("%w: the topic is not 1 to 32 base64url characters", ErrOptions)
		}
		h["Topic"] = opt.Topic
	}
	return h, nil
}

func base64urlText(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// retryAfter reads a Retry-After header, delta-seconds or an HTTP date, as a
// duration from now, capped at [MaxRetryAfter]. A missing or unreadable
// header, or a date not after now, is zero.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.ParseUint(v, 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
		d = MaxRetryAfter
		if n < uint64(MaxRetryAfter/time.Second) {
			d = time.Duration(n) * time.Second
		}
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), MaxRetryAfter)
}

// Kinds of [StatusError], for errors.Is.
var (
	// ErrGone: the push service answered 404 or 410; the subscription has
	// expired or been withdrawn and should be dropped.
	ErrGone = errors.New("webpush: the subscription is gone")
	// ErrEntityTooLarge: the push service answered 413.
	ErrEntityTooLarge = errors.New("webpush: the push service refused the message as too large")
	// ErrRateLimited: the push service answered 429.
	ErrRateLimited = errors.New("webpush: the push service asked to send less")
	// ErrServer: the push service answered 500 to 599.
	ErrServer = errors.New("webpush: the push service failed")
	// ErrUnexpectedStatus: the push service gave any other answer than 201,
	// a redirect included.
	ErrUnexpectedStatus = errors.New("webpush: the push service gave an unexpected answer")
)

// StatusError is the push service's answer when it is not 201 Created. It
// is one of the kinds above for errors.Is.
type StatusError struct {
	// Status is the HTTP status code.
	Status int
	// RetryAfter is the Retry-After of a 429 or 5xx answer, capped at
	// [MaxRetryAfter]; zero when there was none.
	RetryAfter time.Duration
}

func (e *StatusError) kind() error {
	switch {
	case e.Status == http.StatusNotFound || e.Status == http.StatusGone:
		return ErrGone
	case e.Status == http.StatusRequestEntityTooLarge:
		return ErrEntityTooLarge
	case e.Status == http.StatusTooManyRequests:
		return ErrRateLimited
	case e.Status >= 500 && e.Status <= 599:
		return ErrServer
	}
	return ErrUnexpectedStatus
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("%s (status %d)", e.kind().Error(), e.Status)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(", retry after %s", e.RetryAfter)
	}
	return s
}

// Is reports whether target is the kind of e.
func (e *StatusError) Is(target error) bool { return target == e.kind() }
