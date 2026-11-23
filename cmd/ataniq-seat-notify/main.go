// Command ataniq-seat-notify is the notifier of the authority host's seat: the
// one small unit that holds the VAPID key and the push subscriptions and is
// the only part that talks to a push service. The seat app (ataniq-seat)
// reaches nothing on the network but the gate and this unit's Unix socket; it
// tells this unit one integer, how many cards wait, and this unit sends the
// phone a Web Push notice that says so and carries nothing else. A notice
// never decides: the payload is {"n":N}, the text and the one link are the
// seat's own page's, and a decision still needs the phone's own assertion.
//
//	ataniq-seat-notify vapid-init --key PATH
//	ataniq-seat-notify run --socket PATH --seat-user NAME --vapid-key PATH
//	                       --subscriptions PATH --vapid-subject SUBJECT
//	                       [--push-hosts LIST] [--ttl DURATION]
//	ataniq-seat-notify send-test --socket PATH N
//
// vapid-init makes a new VAPID key (an ECDSA P-256 key) in a new file of mode
// 0600 at PATH, whose folder must exist and not be writable by group or
// other. It never overwrites. It prints the key's PUBLIC half, base64url
// without padding, and nothing else: that is the applicationServerKey the
// phone subscribes with.
//
// run serves the seatnotice protocol (package seatnotice) on the Unix socket
// --socket, an absolute path, made with mode 0660 (its group is the process's
// group: the unit runs as its own group, seat-notify, which the seat's unit joins as a supplementary group). Only the user --seat-user, looked up
// once at the start (the command refuses to start when the name is unknown),
// is served: the kernel's peer credentials decide, the socket's mode is not
// relied on. --vapid-key is the key file vapid-init made. --subscriptions is
// the file the subscriptions are kept in (mode 0600, in a folder this user
// owns; read strictly, written atomically; at most 4 subscriptions, each with
// an https endpoint on a host --push-hosts allows). --vapid-subject is the
// contact the push service may reach the operator by, a mailto: or https:
// value of the owner's, required; it is owner-local and in no release.
// --push-hosts is a comma-separated list of the push service hosts, exactly
// web.push.apple.com by default: no other host is reachable, no wildcard, no
// subdomain, no redirect. --ttl is how long a push service keeps a notice for
// a phone it cannot reach, 1h by default.
//
// A notify of N sends {"n":N} to each subscription in turn, with the topic
// "waiting", urgency normal and the TTL, within the connection's turn of 2
// seconds; a subscription the push service reports gone (404 or 410) is
// dropped, one that failed otherwise is kept. At most 30 pushes are made in
// any hour, all subscriptions together, whatever the seat asks. The log holds
// fixed text and counts, never an endpoint or a key. SIGTERM or SIGINT stops
// the service gracefully.
//
// send-test is for the owner's one test after setting the host up: it tells
// the notifier on --socket that N cards wait (N, 0 to 999) and prints how
// many pushes were accepted. It must run as the seat user, which is the only
// user the socket serves.
//
// Nothing secret is printed or read from the command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ataniq/internal/seatnotice"
	"ataniq/internal/webpush"
)

const usageLine = "Usage: ataniq-seat-notify run --socket PATH --seat-user NAME --vapid-key PATH --subscriptions PATH --vapid-subject SUBJECT [--push-hosts LIST] [--ttl DURATION]\n" +
	"       ataniq-seat-notify vapid-init --key PATH\n" +
	"       ataniq-seat-notify send-test --socket PATH N\n" +
	"--socket, --vapid-key, --subscriptions and --key are absolute paths; --vapid-subject is a mailto: or https: value; --push-hosts defaults to " + defaultPushHosts + "; --ttl defaults to 1h."

// defaultPushHosts is the one push service the notifier reaches unless the
// owner says otherwise.
const defaultPushHosts = "web.push.apple.com"

// defaultTTL is how long a push service keeps a notice for a phone it cannot
// reach.
const defaultTTL = time.Hour

// socketMode is the socket's permission bits: the notifier's group, which the seat joins, may connect;
// the kernel's peer credentials decide who is served.
const socketMode = 0o660

// lookupUID reads the user id of a user name. It is a variable so a test can
// stand in for the host's user database; the command never sets it.
var lookupUID = func(name string) (uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(u.Uid, 10, 32)
	return uint32(n), err
}

// allowEndpoints makes the policy of the endpoints the notifier keeps from the
// host policy. It is a variable so a test can allow the address of a local
// push service; the command never sets it.
var allowEndpoints = endpointAllowed

// senderHook, when not nil, may change the sender's configuration before the
// sender is made (a test points it at a local push service). The command
// never sets it.
var senderHook func(*webpush.Config)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := dispatch(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// dispatch runs the subcommand args name and returns the exit code.
func dispatch(ctx context.Context, args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(errout, "ataniq-seat-notify: give a subcommand\n%s\n", usageLine)
		return 2
	}
	switch args[0] {
	case "vapid-init":
		return vapidInit(args[1:], out, errout)
	case "run":
		return run(ctx, args[1:], errout, nil)
	case "send-test":
		return sendTest(ctx, args[1:], out, errout)
	}
	fmt.Fprintf(errout, "ataniq-seat-notify: unknown subcommand\n%s\n", usageLine)
	return 2
}

// newFlags makes a flag set that prints nothing by itself.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("ataniq-seat-notify "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// vapidInit makes the VAPID key file and prints the public key to out, and
// nothing else. It returns the exit code.
func vapidInit(args []string, out, errout io.Writer) int {
	fs := newFlags("vapid-init")
	path := fs.String("key", "", "path of the new key file")
	fail := func(msg string) int {
		fmt.Fprintf(errout, "ataniq-seat-notify vapid-init: %s\n%s\n", msg, usageLine)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return fail("bad flags")
	}
	if fs.NArg() != 0 {
		return fail("no arguments are taken")
	}
	if *path == "" || !filepath.IsAbs(*path) {
		return fail("--key must be an absolute path")
	}
	k, err := webpush.GenerateVAPIDKey()
	if err != nil {
		fmt.Fprintln(errout, "ataniq-seat-notify vapid-init: cannot make a key")
		return 1
	}
	// The errors of the key file helpers are fixed texts, with no path and
	// nothing of the key.
	if err := webpush.WriteVAPIDKeyFile(*path, k); err != nil {
		fmt.Fprintf(errout, "ataniq-seat-notify vapid-init: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, k.PublicKeyString())
	return 0
}

// sendTest tells the notifier on the socket that N cards wait and prints how
// many pushes it sent.
func sendTest(ctx context.Context, args []string, out, errout io.Writer) int {
	fs := newFlags("send-test")
	socket := fs.String("socket", "", "the notifier's socket")
	fail := func(msg string) int {
		fmt.Fprintf(errout, "ataniq-seat-notify send-test: %s\n%s\n", msg, usageLine)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return fail("bad flags")
	}
	n, err := strconv.Atoi(fs.Arg(0))
	if fs.NArg() != 1 || err != nil || n < 0 || n > seatnotice.MaxN {
		return fail("give one count, 0 to 999")
	}
	c, err := seatnotice.NewClient(*socket)
	if err != nil {
		return fail("--socket must be an absolute path")
	}
	sent, err := c.NotifySent(ctx, n)
	if err != nil {
		fmt.Fprintf(errout, "ataniq-seat-notify send-test: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "sent %d\n", sent)
	return 0
}

// options is what the flags of run select.
type options struct {
	socket, seatUser, vapidKey, subscriptions, subject string
	hosts                                              []string
	ttl                                                time.Duration
}

// parse reads the flags of run. On failure it has printed the diagnostic and
// the usage to errout and returns the exit code to use.
func parse(args []string, errout io.Writer) (o options, code int, ok bool) {
	fs := newFlags("run")
	hosts := fs.String("push-hosts", defaultPushHosts, "comma-separated push service hosts")
	fs.StringVar(&o.socket, "socket", "", "absolute path of the Unix socket")
	fs.StringVar(&o.seatUser, "seat-user", "", "the user whose connections are served")
	fs.StringVar(&o.vapidKey, "vapid-key", "", "the VAPID key file made by vapid-init")
	fs.StringVar(&o.subscriptions, "subscriptions", "", "the subscriptions file")
	fs.StringVar(&o.subject, "vapid-subject", "", "mailto: or https: contact for the push service")
	fs.DurationVar(&o.ttl, "ttl", defaultTTL, "how long a push service keeps a notice")
	fail := func(msg string) (options, int, bool) {
		fmt.Fprintf(errout, "ataniq-seat-notify: %s\n%s\n", msg, usageLine)
		return options{}, 2, false
	}
	if err := fs.Parse(args); err != nil {
		return fail("bad flags")
	}
	if fs.NArg() != 0 {
		return fail("no arguments are taken")
	}
	for _, p := range []struct{ name, val string }{
		{"--socket", o.socket}, {"--vapid-key", o.vapidKey}, {"--subscriptions", o.subscriptions},
	} {
		if p.val == "" || !filepath.IsAbs(p.val) {
			return fail(p.name + " must be an absolute path")
		}
	}
	if o.seatUser == "" {
		return fail("--seat-user is required")
	}
	if !strings.HasPrefix(o.subject, "mailto:") && !strings.HasPrefix(o.subject, "https:") {
		return fail("--vapid-subject is required: a mailto: or https: value")
	}
	if o.ttl < 0 || o.ttl > webpush.MaxTTL {
		return fail("--ttl is out of range (0 to 28 days)")
	}
	for _, h := range strings.Split(*hosts, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if !validHostName(h) {
			return fail("--push-hosts is not a comma-separated list of host names")
		}
		o.hosts = append(o.hosts, h)
	}
	return o, 0, true
}

// validHostName accepts a lower-case DNS name of letters, digits, hyphens and
// dots, with at least one dot and no empty label: no IP literal, no wildcard,
// no port.
func validHostName(h string) bool {
	if len(h) == 0 || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	allDigits := true
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
			if c < '0' || c > '9' {
				allDigits = false
			}
		}
	}
	return !allDigits
}

// listen opens the socket: a stale socket file (one nothing answers on) is
// replaced; any other file at the path is an error. The file is made with
// mode 0660 whatever the unit's UMask, with no moment when it is wider.
func listen(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != os.ModeSocket {
			return nil, errors.New("the --socket path exists and is not a socket")
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, errors.New("another process answers on the --socket path")
		}
		if err := os.Remove(path); err != nil {
			return nil, errors.New("the stale socket cannot be removed")
		}
	}
	old := syscall.Umask(int(0o777 &^ socketMode))
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, errors.New("cannot listen on the --socket path")
	}
	if err := os.Chmod(path, socketMode); err != nil {
		l.Close()
		return nil, errors.New("cannot set the socket's mode")
	}
	return l, nil
}

// pushClient is the HTTP client of the pushes: no proxy, whatever the
// environment says, system roots, bounded handshakes.
func pushClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: pushTimeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		DisableCompression:    true,
	}}
}

// run serves until ctx ends, then stops gracefully. ready, when not nil, is
// called with the listener's address once it accepts. It returns the exit
// code.
func run(ctx context.Context, args []string, errout io.Writer, ready func(net.Addr)) int {
	o, code, ok := parse(args, errout)
	if !ok {
		return code
	}
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(errout, format, args...)
	}
	uid, err := lookupUID(o.seatUser)
	if err != nil {
		logf("ataniq-seat-notify: --seat-user is not a user of this host\n")
		return 2
	}
	key, err := webpush.LoadVAPIDKeyFile(o.vapidKey)
	if err != nil {
		logf("ataniq-seat-notify: --vapid-key: %v\n", err)
		return 2
	}
	policy := webpush.AllowHosts(o.hosts...)
	allow := allowEndpoints(policy)
	st, err := openStore(o.subscriptions, allow)
	if err != nil {
		logf("ataniq-seat-notify: --subscriptions: %v\n", err)
		return 2
	}
	client := pushClient()
	defer client.CloseIdleConnections()
	cfg := webpush.Config{Key: key, Subject: o.subject, Client: client, Hosts: policy, Timeout: pushTimeout}
	if senderHook != nil {
		senderHook(&cfg)
	}
	sender, err := webpush.NewSender(cfg)
	if err != nil {
		logf("ataniq-seat-notify: --vapid-subject is not a valid contact\n")
		return 2
	}
	h := newNotifier(key, st, sender, allow, o.ttl, newLimiter(maxPushesPerHour, time.Hour, nil), logf)
	l, err := listen(o.socket)
	if err != nil {
		logf("ataniq-seat-notify: %v\n", err)
		return 1
	}
	logf("ataniq-seat-notify: serving the seat user on the socket, %d subscriptions held\n", st.count())
	if ready != nil {
		ready(l.Addr())
	}
	if err := seatnotice.Serve(ctx, l, h, seatnotice.ServeConfig{AllowedUID: uid}); err != nil {
		logf("ataniq-seat-notify: the socket failed\n")
		return 1
	}
	logf("ataniq-seat-notify: stopped\n")
	return 0
}
