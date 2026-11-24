package seatapp

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxBody is the most bytes of a request body the app reads. A request with a
// longer body is refused with status 413.
const MaxBody = 8 << 10

// maxHeaderBytes bounds the request header the server reads.
const maxHeaderBytes = 16 << 10

// PairRequest is what the app hands the daemon to pair a phone: the
// one-time code from the desk, the new credential's id and its public key as
// DER SPKI, and the registration they came in. The app has checked the form
// of the code and that the registration is one for its relying party, with
// user presence and user verification, whose credential and key are these;
// whether the code is the code is the daemon's to say.
type PairRequest struct {
	Code         string
	CredentialID []byte
	SPKI         []byte
	// AuthenticatorData is the registration's authenticator data, as the
	// browser gave it and the app checked it, and RPID the relying party id
	// it was checked against ([Config].RPID). The daemon registers the
	// credential from them with its own parser and reads the claims below
	// again, so a port relays these two and not the fields after them.
	AuthenticatorData []byte
	RPID              string
	// AAGUID, BackupEligible, BackupState, UserVerified and SignCount are
	// what the app read in AuthenticatorData: the authenticator's claimed
	// model, its BE, BS and UV flags and its sign counter. With attestation
	// "none" each is a claim of the authenticator, not a fact, and is shown
	// as one wherever it is shown.
	AAGUID         [16]byte
	BackupEligible bool
	BackupState    bool
	UserVerified   bool
	SignCount      uint32
}

// WordCount is the number of words of a key's fingerprint. It is this
// package's own copy of seatpair.FingerprintWords (this package may import
// nothing of the repository), and the words script's COUNT is the same
// number; a test in tests/ holds the three equal.
const WordCount = 8

// PairResult is the daemon's answer to a pairing: the [WordCount] words of the
// key's fingerprint, which the phone shows to be compared with the desk
// console. The words are text the app did not write: it renders them with
// [Text].
type PairResult struct {
	Words [WordCount]string
}

// Daemon is the app's port to the daemon. Package seatlink implements it over
// the seat gate, and the command ataniq-seat wires that in when it is given a
// gate (without one it answers every call [ErrDaemonUnavailable]); a test of
// this package uses an in-memory fake.
//
// Pair returns [ErrCodeRefused] when the daemon does not accept the code
// (unknown, expired, used or out of tries) and [ErrDaemonUnavailable] when it
// cannot be reached.
//
// LoginChallenge asks the daemon for a login challenge (seat.login-challenge)
// and returns it only after the port recomputed the challenge from the gate's
// key and the daemon's nonce. Login hands the daemon a browser's assertion
// over it (seat.login). They return [ErrNotOffered] when the gate does not
// offer logins, [ErrChallengeStale] for a challenge the daemon no longer
// holds, [ErrLoginRefused] for a login it did not accept, and
// [ErrDaemonUnavailable] when it cannot be reached.
//
// List, Show and Pages are the reads of the cards (owner.list, owner.show and
// owner.paths): the list, one card built from its record's bytes, and page n
// of a card's whole path list. They return [ErrNoCard] for a card the daemon
// does not have, [ErrCardChanged] for a page of a review that is no longer
// the card's, [ErrTooLarge] for an answer that does not fit the gate's frame,
// [ErrNotOffered] when the gate does not offer reads, [ErrBusy] when it does
// not take the call for its load, and [ErrDaemonUnavailable] when it cannot
// be reached. No read decides anything.
//
// Offer, DecideChallenge and Decide are a decision from the phone. Offer
// asks the gate nothing: it says what the Card page may offer for a card the
// port showed, as a session read it, by the card model's rules for approve
// and deny; a port that takes no decision now sets Offer.Inhibited, and the
// page shows a notice and no form. DecideChallenge freezes the decision the owner tapped from the
// card the port showed (never from anything a browser sent but the action,
// the indexes of the sign-offs and the reason), asks the daemon for its
// challenge (seat.challenge), and returns it only after the port recomputed
// it from the gate's key, the frozen frame and the daemon's nonce
// ([ErrChallengeDiffers] otherwise, and nothing is asked of the phone).
// Decide hands the daemon the phone's assertion over it (seat.decide). They
// return [*NotAllowedError] for a decision the card model does not allow,
// [ErrCardChanged] for a card the port did not show at that binding,
// [ErrNotOffered] when the gate does not offer decisions, [ErrBusy] for its
// load, the errors of the daemon's refusals ([ErrChallengeStale],
// [ErrCardExpired], [ErrCannotDecide], [ErrAssertionRefused], [ErrKeyRevoked],
// [ErrKindRefused]), [ErrDecisionsInhibited] when the host the seat runs on
// takes no decision now (nothing is sent), and [ErrDaemonUnavailable] when
// nothing was sent. Once
// Decide has sent the decision, a lost answer is [ErrOutcomeUnknown], never
// ErrDaemonUnavailable.
//
// The app maps any other error to a fixed code and never shows its text.
type Daemon interface {
	Pair(ctx context.Context, r PairRequest) (PairResult, error)
	LoginChallenge(ctx context.Context) (LoginChallenge, error)
	Login(ctx context.Context, r LoginRequest) (LoginResult, error)
	List(ctx context.Context) (Listing, error)
	Show(ctx context.Context, id string) (Card, error)
	Pages(ctx context.Context, id string, n int) (PathsPage, error)
	Offer(id, bindingSHA256 string, r Reading, now time.Time) Offer
	DecideChallenge(ctx context.Context, r DecideChallengeRequest) (DecideChallenge, error)
	Decide(ctx context.Context, r DecideRequest) (DecideResult, error)
}

// ChallengeDropper is the part of a [Daemon] that keeps room for the answer
// to each decision's challenge it drew (package seatlink keeps the room of
// its seat.decide at the gate). The app tells it, by the challenge's id,
// when it drops a challenge whose decision it will then never send: a new
// tap of the session took its place, or the tap was burnt or refused before
// its decision was sent. The room is let go then rather than at the end of
// the challenge's life. A session's end (logout, idle time, eviction) does
// not tell it: the room of a decision pending then is left to the
// challenge's life. A port without it is not told.
type ChallengeDropper interface {
	DropChallenge(id string)
}

// The errors a Daemon returns that the app tells apart.
var (
	ErrCodeRefused       = errors.New("seatapp: the pairing code was not accepted")
	ErrDaemonUnavailable = errors.New("seatapp: the daemon is not reachable")
)

// Config is what New needs.
type Config struct {
	// Origin is the app's one origin, "https://" and a host name: no path,
	// no user information, and no port (or :443, which is dropped, since a
	// browser's Origin header leaves the default port out). Links to the app
	// always go to the reverse proxy over HTTPS on port 443; the app itself
	// listens on plain HTTP behind it. The Origin header of every POST must
	// equal it, the Host header must be its host (the proxy passes the Host
	// it was asked for), and the passkey's client data must name it.
	Origin string
	// RPID is the relying party id of the passkeys: exactly the origin's host
	// name, "seat.example" for "https://seat.example". The seat has a name of
	// its own, so a parent domain, which would let a passkey be used by every
	// name below it (and which a browser refuses for a public suffix), is not
	// accepted.
	RPID string
	// Daemon is the port to the daemon. It is required.
	Daemon Daemon
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Random is the source of challenges; crypto/rand when nil.
	Random io.Reader
	// ClientAddrHeader names the header the proxy in front sets to the
	// client's address, for the per-source limits. Empty means the app trusts
	// no header and uses the peer's address. Set it only when the proxy
	// replaces the header on every request.
	ClientAddrHeader string
	// AllowInsecureLoopbackForTests lets Origin be an http origin, or have a
	// port, when its host is a loopback IP address or "localhost". It is for
	// tests of this package and no command sets it.
	AllowInsecureLoopbackForTests bool
	// Notifier, when set, is told how many cards wait, and nothing else,
	// when the count rises and no browser is looking (see [Notifier] and
	// [Open]); [New] refuses a configuration that sets it, since only Open
	// can stop what it starts. Nil starts nothing. When it is also a
	// [PushPort] the app serves the phone's side of the notices (the card
	// list's controls, the service worker, the manifest and the push API);
	// otherwise none of that exists and its routes answer 404.
	Notifier Notifier
	// Logf, when set, receives the app's own log lines (each ends in a
	// newline): so far the notices' failures, at most one a minute. They
	// hold nothing but fixed text.
	Logf func(format string, args ...any)
}

// canonicalOrigin checks an origin and returns it in the form a browser's
// Origin header has, with its host name.
func canonicalOrigin(o string, insecureLoopback bool) (origin, host string, err error) {
	bad := errors.New("seatapp: Config.Origin must be https://HOST, with no port, path or user information")
	u, perr := url.Parse(o)
	if perr != nil || u.Opaque != "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || u.Host == "" || strings.HasSuffix(u.Host, ":") ||
		u.Hostname() == "" || u.Hostname() != strings.ToLower(u.Hostname()) ||
		o != u.Scheme+"://"+u.Host {
		return "", "", bad
	}
	host = u.Hostname()
	ip, ipErr := netip.ParseAddr(host)
	if insecureLoopback {
		if (u.Scheme != "http" && u.Scheme != "https") || !(host == "localhost" || (ipErr == nil && ip.IsLoopback())) {
			return "", "", errors.New("seatapp: the insecure test option takes only a loopback origin")
		}
		return o, host, nil
	}
	// A passkey is bound to a domain name, so an address literal is no origin.
	if u.Scheme != "https" || ipErr == nil || (u.Port() != "" && u.Port() != "443") {
		return "", "", bad
	}
	return "https://" + host, host, nil
}

// app is the handler's state.
type app struct {
	cfg   Config
	pair  *pairing
	login *logins
	sess  *sessions
	// reads bounds the reads of each credential's sessions, and decides
	// their decision challenges.
	reads   *limiter
	decides *limiter
	// geneses bounds the project pages and posts of each credential's
	// sessions (see genesis.go).
	geneses *limiter
	// glog is the rate limit of the journal line for that limit (see
	// genesisAllow).
	glog struct {
		mu   sync.Mutex
		last time.Time
		held int
	}
	// watches bounds the watch polls of each credential's sessions, apart
	// from the reads; watchHold is how long a poll is held.
	watches   *limiter
	watchHold time.Duration
	// lastPoll is the time, in Unix nanoseconds, of the last accepted poll,
	// 0 before the first; the notices stay quiet while it is recent.
	lastPoll atomic.Int64
	// pollSaw is what the owner has seen on a page: the count of cards
	// waiting when the last live poll was accepted, with the number of such
	// polls, which the notices' goroutine takes once each (see sawWaiting).
	pollMu      sync.Mutex
	pollSeq     uint64
	pollWaiting int
	// push is the notifier's PushPort, nil when Config.Notifier is not one:
	// the pages then show nothing of notices and the routes of push.go
	// answer 404. pushes bounds the push API per credential.
	push   PushPort
	pushes *limiter
	// noticeTimer is the notices' timer; the real one when nil. For tests.
	noticeTimer noticeTimer
	// noticeParked, when set, is called each time the notices' goroutine is
	// about to wait for a change or a timer. For tests.
	noticeParked func()
	// host is what the Host header of a request must be: the origin without
	// its scheme.
	host string
}

// New returns the app's handler. It refuses a configuration with an origin
// that is not https on the default port (see [Config].Origin), a relying party
// id that is not the origin's host, or no daemon.
func New(cfg Config) (http.Handler, error) {
	if cfg.Notifier != nil {
		return nil, errors.New("seatapp: Config.Notifier starts a goroutine: use Open, and Close it")
	}
	_, h, err := newApp(cfg)
	return h, err
}

// newApp is New, and also returns the app's state, for the tests.
func newApp(cfg Config) (*app, http.Handler, error) {
	origin, host, err := canonicalOrigin(cfg.Origin, cfg.AllowInsecureLoopbackForTests)
	if err != nil {
		return nil, nil, err
	}
	cfg.Origin = origin
	if cfg.RPID == "" || cfg.RPID != host {
		return nil, nil, errors.New("seatapp: Config.RPID must be the origin's host")
	}
	if cfg.Daemon == nil {
		return nil, nil, errors.New("seatapp: Config.Daemon is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	_, hostHeader, _ := strings.Cut(origin, "://")
	a := &app{cfg: cfg, pair: newPairing(cfg), login: newLogins(cfg), sess: newSessions(cfg.Now),
		reads: newLimiter(readRequests, readWindow), decides: newLimiter(decideRequests, decideWindow),
		watches: newLimiter(watchRequests, watchWindow), watchHold: watchHold, host: hostHeader,
		pushes: newLimiter(pushRequests, pushWindow), geneses: newLimiter(genesisRequests, genesisWindow)}
	a.push, _ = cfg.Notifier.(PushPort)

	mux := http.NewServeMux()
	for _, rt := range a.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return a, a.guard(mux), nil
}

// route is one entry of the app's route table.
type route struct {
	pattern string
	handler http.HandlerFunc
}

// routes is the app's whole route table. The page inventory test lists every
// pattern in it, so a route cannot be added without being inventoried.
func (a *app) routes() []route {
	return []route{
		{"GET /healthz", a.healthz},
		{"GET /{$}", a.page("home")},
		// The desk's QR opens /pair/ with the code in the fragment, so that is
		// the canonical path; /pair goes there with a 308 (a browser keeps the
		// fragment across a redirect that has none of its own).
		{"GET /pair/{$}", a.page("pair")},
		{"GET /pair", a.redirect(pairPath)},
		{"GET /static/words.js", a.asset("assets/words.js", "text/javascript; charset=utf-8")},
		{"GET /static/pair.js", a.asset("assets/pair.js", "text/javascript; charset=utf-8")},
		{"GET /static/app.css", a.asset("assets/app.css", "text/css; charset=utf-8")},
		{"POST /api/pair-challenge", a.post(a.pair.challenge)},
		{"POST /api/pair", a.post(a.pair.pair)},
		{"GET /login/{$}", a.passkeyPage("login")},
		{"GET /login", a.redirect(loginPath)},
		{"GET /static/login.js", a.asset("assets/login.js", "text/javascript; charset=utf-8")},
		{"POST /api/login-challenge", a.post(a.loginChallenge)},
		{"POST /api/login", a.post(a.loginPost)},
		{"POST /api/logout", a.post(a.logout)},
		{"GET /cards/{$}", a.authed(a.cardList)},
		{"GET /cards", a.redirect(cardsPath)},
		{"GET /cards/{id}", a.authed(a.cardPage)},
		{"GET /cards/{id}/paths/{n}", a.authed(a.pathsPage)},
		{"GET /static/session.js", a.asset("assets/session.js", "text/javascript; charset=utf-8")},
		{"GET /static/decide.js", a.asset("assets/decide.js", "text/javascript; charset=utf-8")},
		{"POST /api/decide-challenge", a.post(a.decideChallenge)},
		{"POST /api/decide", a.post(a.decidePost)},
		{"GET /static/watch.js", a.asset("assets/watch.js", "text/javascript; charset=utf-8")},
		{"POST /api/watch", a.post(a.watchPost)},
		// The phone's side of the notices: all of them answer 404 unless
		// Config.Notifier is also a PushPort (see push.go).
		{"GET /manifest.webmanifest", a.pushOnly(a.asset("assets/manifest.webmanifest", "application/manifest+json"))},
		{"GET /sw.js", a.pushOnly(a.serviceWorker())},
		{"GET /static/push.js", a.pushOnly(a.asset("assets/push.js", "text/javascript; charset=utf-8"))},
		{"GET /static/icon-192.png", a.pushOnly(a.asset("assets/icon-192.png", "image/png"))},
		{"GET /static/icon-512.png", a.pushOnly(a.asset("assets/icon-512.png", "image/png"))},
		{"GET /static/apple-touch-icon.png", a.pushOnly(a.asset("assets/apple-touch-icon.png", "image/png"))},
		{"POST /api/push/info", a.pushOnly(a.post(a.pushInfo))},
		{"POST /api/push/subscribe", a.pushOnly(a.post(a.pushSubscribe))},
		{"POST /api/push/unsubscribe", a.pushOnly(a.post(a.pushUnsubscribe))},
		// The guided new-project interview (see genesis.go): pages made on
		// the server and plain forms, each post answered with a redirect.
		// Without a Genesis port, or with one that is not offered, the pages
		// answer "not offered" and the posts go to the project list.
		{"GET /projects/{$}", a.authed(a.projectList)},
		{"GET /projects", a.redirect(projectsPath)},
		{"POST /projects/new", a.form(a.projectStart)},
		{"GET /projects/{thread}", a.authed(a.projectPage)},
		{"GET /projects/{thread}/change/{node}", a.authed(a.changePage)},
		{"GET /projects/{thread}/discard", a.authed(a.discardPage)},
		{"GET /projects/{thread}/plan", a.authed(a.planPage)},
		{"POST /projects/{thread}/answer", a.form(a.projectAnswer)},
		{"POST /projects/{thread}/change", a.form(a.projectChange)},
		{"POST /projects/{thread}/finish", a.form(a.projectFinish)},
		{"POST /projects/{thread}/discard", a.form(a.projectDiscard)},
	}
}

// afterLogin is where the login page goes once a session is made.
const afterLogin = cardsPath

// pairPath is the canonical path of the pairing page, the one the desk's QR
// holds and the only one served as a page.
const pairPath = "/pair/"

// redirect answers with a permanent redirect that keeps the method (308) to a
// path of the app. The Location holds no fragment: the browser carries over
// the one it has.
func (a *app) redirect(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", to)
		writeText(w, http.StatusPermanentRedirect, "moved\n")
	}
}

// Server returns an http.Server for h with the header, read, write and idle
// limits of the app. The caller sets Addr or serves a listener.
func Server(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// securityHeaders are on every response, an error included.
//
// The content security policy is closed (default-src 'none') and opens only
// to the app's own origin: scripts, styles, connections and images, nothing
// inline, and Trusted Types are required for every script sink. manifest-src
// and worker-src are the two the notices need and nothing else: the web app
// manifest, and the service worker (/sw.js) that shows a push. The page
// registers the worker through a Trusted Types policy of its own, since the
// register call is a script sink (see push.js); no other policy exists, and
// the trusted-types directive names it, seat-sw, and nothing else (no
// allow-duplicates, no default), so a script that runs on the origin cannot
// make a policy of its own to reach a sink.
//
// The referrer policy is same-origin: a request to another origin carries no
// Referer header, so no address of the app leaves it, and a request to the
// app's own origin keeps its Origin header. That second half is what the
// project pages' plain HTML forms need. A browser sends a form's post with
// the policy of the page that holds the form, and under no-referrer it sends
// "Origin: null" (Fetch Standard, "serializing a request origin"), which the
// app refuses (formOrigin): every form would answer 403. The scripts set
// their own policy on each fetch (strict-origin) and do not depend on this
// one. A test of the page inventory holds every page with a form to it.
var securityHeaders = [][2]string{
	{"Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; require-trusted-types-for 'script'; trusted-types seat-sw"},
	{"Strict-Transport-Security", "max-age=63072000"},
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "same-origin"},
	{"X-Frame-Options", "DENY"},
	{"Cache-Control", "no-store"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
	{"Cross-Origin-Resource-Policy", "same-origin"},
	{"Permissions-Policy", permissionsPolicy("()")},
}

// permissionsPolicy is the Permissions-Policy header, with get the allow-list
// of publickey-credentials-get: "()" on every response but a page that asks
// the phone for an assertion, which has "(self)", the app's own origin and no
// other.
func permissionsPolicy(get string) string {
	return "publickey-credentials-create=(self), publickey-credentials-get=" + get +
		", camera=(), microphone=(), geolocation=(), payment=(), usb=(), bluetooth=(), serial=(), hid=(), clipboard-read=(), clipboard-write=()"
}

// guard sets the headers and cuts every request body at MaxBody.
func (a *app) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for _, kv := range securityHeaders {
			h.Set(kv[0], kv[1])
		}
		if r.ContentLength > MaxBody {
			writeText(w, http.StatusRequestEntityTooLarge, "request too large\n")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		}
		next.ServeHTTP(w, r)
	})
}

// post wraps a POST handler with the checks every changing request passes:
// the Origin header equals the app's origin, the Host header its host,
// Sec-Fetch-Site says same-origin (sameOrigin), and the body is declared
// JSON. A plain HTML form's post passes the same checks, but that a browser
// may leave its Origin header out when its Sec-Fetch-Site says same-origin
// (formOrigin), with the form's content type (form, in genesis.go).
func (a *app) post(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.sameOrigin(w, r) {
			return
		}
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			writeText(w, http.StatusUnsupportedMediaType, "unsupported media type\n")
			return
		}
		h(w, r)
	}
}

// writeText writes a fixed plain-text body.
func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func (a *app) healthz(w http.ResponseWriter, r *http.Request) {
	writeText(w, http.StatusOK, "ok\n")
}
