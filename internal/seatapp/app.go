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
// gate (without one it answers every pairing [ErrDaemonUnavailable]); a test of
// this package uses an in-memory fake.
//
// Pair returns [ErrCodeRefused] when the daemon does not accept the code
// (unknown, expired, used or out of tries) and [ErrDaemonUnavailable] when it
// cannot be reached. The app maps any other error to a fixed code and never
// shows its text.
type Daemon interface {
	Pair(ctx context.Context, r PairRequest) (PairResult, error)
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
	// AllowNoFetchMetadataForTests lets a POST come without a Sec-Fetch-Site
	// header. Every browser that makes a passkey sends it, so a POST without
	// it is refused; this option is for tests of this package, whose requests
	// are not a browser's, and no command sets it.
	AllowNoFetchMetadataForTests bool
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
	cfg  Config
	pair *pairing
	// host is what the Host header of a request must be: the origin without
	// its scheme.
	host string
}

// New returns the app's handler. It refuses a configuration with an origin
// that is not https on the default port (see [Config].Origin), a relying party
// id that is not the origin's host, or no daemon.
func New(cfg Config) (http.Handler, error) {
	origin, host, err := canonicalOrigin(cfg.Origin, cfg.AllowInsecureLoopbackForTests)
	if err != nil {
		return nil, err
	}
	cfg.Origin = origin
	if cfg.RPID == "" || cfg.RPID != host {
		return nil, errors.New("seatapp: Config.RPID must be the origin's host")
	}
	if cfg.Daemon == nil {
		return nil, errors.New("seatapp: Config.Daemon is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	_, hostHeader, _ := strings.Cut(origin, "://")
	a := &app{cfg: cfg, pair: newPairing(cfg), host: hostHeader}

	mux := http.NewServeMux()
	for _, rt := range a.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return a.guard(mux), nil
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
	}
}

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
var securityHeaders = [][2]string{
	{"Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; require-trusted-types-for 'script'"},
	{"Strict-Transport-Security", "max-age=63072000"},
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "no-referrer"},
	{"X-Frame-Options", "DENY"},
	{"Cache-Control", "no-store"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
	{"Cross-Origin-Resource-Policy", "same-origin"},
	{"Permissions-Policy", "publickey-credentials-create=(self), publickey-credentials-get=(), camera=(), microphone=(), geolocation=(), payment=(), usb=(), bluetooth=(), serial=(), hid=(), clipboard-read=(), clipboard-write=()"},
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
// the Origin header equals the app's origin, Sec-Fetch-Site (when the browser
// sends it) says same-origin, and the body is declared JSON.
func (a *app) post(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != a.cfg.Origin {
			writeText(w, http.StatusForbidden, "forbidden\n")
			return
		}
		if r.Host != a.host {
			writeText(w, http.StatusForbidden, "forbidden\n")
			return
		}
		site := r.Header.Get("Sec-Fetch-Site")
		if site != "same-origin" && !(site == "" && a.cfg.AllowNoFetchMetadataForTests) {
			writeText(w, http.StatusForbidden, "forbidden\n")
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
