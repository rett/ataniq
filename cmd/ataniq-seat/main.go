// Command ataniq-seat serves the seat web app (package seatapp) on a Unix
// socket or a loopback address. A proxy in front owns the network port and the
// TLS; this command never listens on a non-loopback address.
//
//	ataniq-seat (--origin https://HOST | --origin-file PATH) (--socket PATH | --listen 127.0.0.1:PORT)
//	            [--rpid HOST] [--socket-mode 0660] [--client-addr-header NAME]
//	            [--gate HOST:PORT --gate-pin-file PATH --identity DIR]
//	            [--decision-guard DIR]
//	ataniq-seat gate-init --identity DIR
//
// The origin is the address the reverse proxy serves the app on, https on
// port 443, and has no default: it comes from --origin or from the first line
// of an owner-local file named by --origin-file. The app itself speaks plain
// HTTP behind the proxy.
//
// --socket needs --client-addr-header, the header the proxy sets to the
// client's address on every request: a client of a Unix socket has no address
// of its own, so the per-source limits would otherwise be one bucket for all.
//
// gate-init makes the seat's own key for the seat gate: a new identity
// (package seatgate) written to DIR as cert.pem and key.pem, the key with mode
// 0600, DIR made with mode 0700 when it is missing. It never overwrites: a
// file already there is an error and nothing is written. It prints one line,
// the key's fingerprint (64 lower-case hex digits), which the owner carries to
// the gate's pin list, and nothing else.
//
// --gate, --gate-pin-file and --identity link the app to the daemon through
// the seat gate (package seatgate, by way of package seatlink), and are given
// all three or none. --gate is the gate's address, HOST:PORT. --gate-pin-file
// names a file holding the gate key's fingerprint, which the owner carried
// here from the gate's host; it must be a regular file, not a link, of at most
// 1 KiB, that group and other cannot write, and hold exactly one fingerprint
// (64 lower-case hex digits). --identity is the folder gate-init
// wrote the seat's key to. The link is kept up in the background, with
// pinned-key TLS at both ends, and redialled when it drops; while it is down
// pairing answers daemon_unavailable, and a code the daemon refused answers
// code_refused. The login (/login/), the card pages (/cards/, behind a
// session) and the Card page's approve and deny ask the daemon through the
// same link, and the link recomputes each login challenge and each
// decision's challenge from the pin before the phone is asked. Phone
// decisions are for scratch state and the STAND-IN door only. Without the
// three flags the daemon port is not wired: pairing and login answer with
// the fixed code daemon_unavailable, and no card offers a decision.
//
// --decision-guard names the authority host's state folder, the one the
// release switch keeps decisions.inhibit in: /var/lib/authority-host when it
// is not given, and an absolute path when it is. Before the app relays a
// decision's challenge or a decision to the gate, every time, the command
// asks decisionguard.Check of that folder; while the inhibit is there, or the
// folder or the file cannot be looked up, nothing is relayed and the phone is
// told "Decisions are paused on this host. Nothing was sent." under the code
// decisions-inhibited (fail closed: on a host without the folder no decision
// is ever relayed). Pairing, login and the reading of cards decide nothing
// and are not guarded. Each refusal is logged with the guard's fixed code.
//
// Nothing secret is printed or read from the command line. The pairing code
// is never printed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

const usageLine = "Usage: ataniq-seat (--origin https://HOST | --origin-file PATH) (--socket PATH | --listen LOOPBACK:PORT) [--rpid HOST] [--socket-mode MODE] [--client-addr-header NAME] [--gate HOST:PORT --gate-pin-file PATH --identity DIR] [--decision-guard DIR]\n" +
	"--origin and --origin-file exclude each other; --socket and --listen exclude each other; --socket needs --client-addr-header; --listen takes an IP literal of the loopback range; --gate, --gate-pin-file and --identity go together; --decision-guard is an absolute folder (default " + defaultGuardDir + ")."

const gateInitUsage = "Usage: ataniq-seat gate-init --identity DIR\n" +
	"Makes the seat's key for the seat gate in DIR and prints its fingerprint; it never overwrites."

// identityName is the name in the certificate of the seat's gate identity.
const identityName = "ataniq-seat"

// maxOriginFile bounds the origin file.
const maxOriginFile = 1 << 10

// maxPinFile bounds the gate's pin file.
const maxPinFile = 1 << 10

// shutdownGrace is how long a shutdown lets requests in flight finish.
const shutdownGrace = 10 * time.Second

// gateLinkHook, when not nil, is called with the gate client once it is made.
// It is for tests, which watch the link come up; the command never sets it.
var gateLinkHook func(*seatgate.Client)

// daemonHook, when not nil, is called with the daemon port the app is given.
// It is for tests; the command never sets it.
var daemonHook func(seatapp.Daemon)

// unwiredDaemon is the daemon port without a gate link: every call is
// ErrDaemonUnavailable, and a card is offered no decision.
type unwiredDaemon struct{}

func (unwiredDaemon) Pair(context.Context, seatapp.PairRequest) (seatapp.PairResult, error) {
	return seatapp.PairResult{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) LoginChallenge(context.Context) (seatapp.LoginChallenge, error) {
	return seatapp.LoginChallenge{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) Login(context.Context, seatapp.LoginRequest) (seatapp.LoginResult, error) {
	return seatapp.LoginResult{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) List(context.Context) (seatapp.Listing, error) {
	return seatapp.Listing{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) Show(context.Context, string) (seatapp.Card, error) {
	return seatapp.Card{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) Pages(context.Context, string, int) (seatapp.PathsPage, error) {
	return seatapp.PathsPage{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) Offer(string, string, seatapp.Reading, time.Time) seatapp.Offer {
	return seatapp.Offer{}
}

func (unwiredDaemon) DecideChallenge(context.Context, seatapp.DecideChallengeRequest) (seatapp.DecideChallenge, error) {
	return seatapp.DecideChallenge{}, seatapp.ErrDaemonUnavailable
}

func (unwiredDaemon) Decide(context.Context, seatapp.DecideRequest) (seatapp.DecideResult, error) {
	return seatapp.DecideResult{}, seatapp.ErrDaemonUnavailable
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := dispatch(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// dispatch runs the subcommand args name, or the server, and returns the exit
// code.
func dispatch(ctx context.Context, args []string, out, errout io.Writer) int {
	if len(args) > 0 && args[0] == "gate-init" {
		return gateInit(args[1:], out, errout)
	}
	return run(ctx, args, errout, nil)
}

// gateInit makes the seat's gate identity in the folder --identity names and
// prints its fingerprint to out, and nothing else. It returns the exit code.
func gateInit(args []string, out, errout io.Writer) int {
	fs := flag.NewFlagSet("ataniq-seat gate-init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("identity", "", "folder for the seat's gate key")
	fail := func(msg string) int {
		fmt.Fprintf(errout, "ataniq-seat gate-init: %s\n%s\n", msg, gateInitUsage)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return fail("bad flags")
	}
	if fs.NArg() != 0 {
		return fail("no arguments are taken")
	}
	if *dir == "" {
		return fail("--identity is required")
	}
	// The errors of seatgate's file helpers are fixed texts, with no path
	// and nothing of the key.
	id, err := seatgate.WriteIdentityFiles(*dir, identityName)
	if err != nil {
		fmt.Fprintf(errout, "ataniq-seat gate-init: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, id.Fingerprint())
	return 0
}

// options is what the flags select.
type options struct {
	origin, originFile, rpid, socket, listen, clientAddrHeader string
	socketMode                                                 os.FileMode
	// gate, gatePinFile and identity are the gate link's flags; gatePin is
	// the fingerprint read from gatePinFile. All are empty without a link.
	gate, gatePinFile, identity, gatePin string
	// guardDir is the state folder decisionguard.Check is asked of before
	// each decision is relayed.
	guardDir string
}

// parse reads the flags. On failure it has printed the diagnostic and the
// usage to errout and returns the exit code to use.
func parse(args []string, errout io.Writer) (o options, code int, ok bool) {
	fs := flag.NewFlagSet("ataniq-seat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("socket-mode", "0660", "permission bits of the socket file, octal")
	fs.StringVar(&o.origin, "origin", "", "the app's origin, https://host")
	fs.StringVar(&o.originFile, "origin-file", "", "owner-local file holding the origin")
	fs.StringVar(&o.rpid, "rpid", "", "relying party id, which must be the origin's host; that host when empty")
	fs.StringVar(&o.socket, "socket", "", "Unix socket path")
	fs.StringVar(&o.listen, "listen", "", "loopback address and port")
	fs.StringVar(&o.clientAddrHeader, "client-addr-header", "", "header the proxy sets to the client address")
	fs.StringVar(&o.gate, "gate", "", "the seat gate's address, host:port")
	fs.StringVar(&o.gatePinFile, "gate-pin-file", "", "file holding the gate key's fingerprint")
	fs.StringVar(&o.identity, "identity", "", "folder of the seat's gate key, made by gate-init")
	fs.StringVar(&o.guardDir, "decision-guard", defaultGuardDir, "the authority host's state folder, which holds decisions.inhibit")
	fail := func(msg string) (options, int, bool) {
		fmt.Fprintf(errout, "ataniq-seat: %s\n%s\n", msg, usageLine)
		return options{}, 2, false
	}
	if err := fs.Parse(args); err != nil {
		return fail("bad flags")
	}
	if fs.NArg() != 0 {
		return fail("no arguments are taken")
	}
	if (o.socket == "") == (o.listen == "") {
		return fail("give exactly one of --socket and --listen")
	}
	if o.socket != "" && o.clientAddrHeader == "" {
		// A client of a Unix socket has no address: without the header the
		// proxy sets, every client would share one rate-limit bucket.
		return fail("--socket needs --client-addr-header: a Unix peer has no address, so all clients would share one limit")
	}
	if (o.origin == "") == (o.originFile == "") {
		return fail("give exactly one of --origin and --origin-file")
	}
	if o.originFile != "" {
		b, err := readSmallFile(o.originFile)
		if err != nil {
			return fail("--origin-file: " + err.Error())
		}
		o.origin = strings.TrimSpace(string(b))
	}
	u, err := url.Parse(o.origin)
	if err != nil || u.Hostname() == "" {
		return fail("--origin is not an origin")
	}
	if o.rpid == "" {
		o.rpid = u.Hostname()
	}
	m, err := strconv.ParseUint(*mode, 8, 32)
	if err != nil || m > 0o777 {
		return fail("--socket-mode is not an octal permission")
	}
	o.socketMode = os.FileMode(m)
	if o.listen != "" {
		if err := checkLoopback(o.listen); err != nil {
			return fail(err.Error())
		}
	}
	if !filepath.IsAbs(o.guardDir) {
		return fail("--decision-guard must be an absolute folder")
	}
	if n := countSet(o.gate, o.gatePinFile, o.identity); n != 0 && n != 3 {
		return fail("give --gate, --gate-pin-file and --identity together, or none of them")
	}
	if o.gate != "" {
		if err := checkGate(o.gate); err != nil {
			return fail(err.Error())
		}
		pin, err := readPinFile(o.gatePinFile)
		if err != nil {
			return fail("--gate-pin-file: " + err.Error())
		}
		o.gatePin = pin
	}
	return o, 0, true
}

// countSet counts the strings that are not empty.
func countSet(ss ...string) int {
	n := 0
	for _, s := range ss {
		if s != "" {
			n++
		}
	}
	return n
}

// checkGate refuses a gate address that is not host:port with a host and a
// port from 1 to 65535, or whose host is an unspecified address.
func checkGate(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return errors.New("--gate is not host:port")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return errors.New("--gate has no valid port")
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		return errors.New("--gate must name the gate's host, not an unspecified address")
	}
	return nil
}

// readPinFile reads the gate's pin: a regular file, not a link, of at most
// maxPinFile bytes, that group and other cannot write, holding exactly one
// fingerprint and nothing but space around it. Its errors hold nothing of the
// file.
func readPinFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("cannot be read, or is a link")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxPinFile {
		return "", errors.New("is not a small regular file")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return "", errors.New("is writable by group or other")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPinFile+1))
	if err != nil || len(b) > maxPinFile {
		return "", errors.New("is not a small regular file")
	}
	pin := strings.TrimSpace(string(b))
	if !seatgate.ValidFingerprint(pin) {
		return "", errors.New("does not hold exactly one fingerprint of 64 lower-case hex digits")
	}
	return pin, nil
}

// serverName is the name the seat sends as the TLS server name for the gate:
// the host of the address when it is a name, none for an address literal.
// The gate's key is pinned, so the name is not checked.
func serverName(addr string) string {
	host, _, _ := net.SplitHostPort(addr)
	if _, err := netip.ParseAddr(host); err == nil {
		return ""
	}
	return host
}

// readSmallFile reads a regular file of at most maxOriginFile bytes.
func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot be read")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxOriginFile {
		return nil, errors.New("is not a small regular file")
	}
	return io.ReadAll(io.LimitReader(f, maxOriginFile))
}

// checkLoopback refuses an address that is not an IP literal of the loopback
// range with a port.
func checkLoopback(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("--listen is not host:port")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return errors.New("--listen has no valid port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return errors.New("--listen must be a loopback IP address; the proxy owns the network")
	}
	return nil
}

// listen opens the listener the options select. A stale socket file (one
// nothing answers on) is replaced; any other file at the path is an error.
func listen(o options) (net.Listener, error) {
	if o.listen != "" {
		return net.Listen("tcp", o.listen)
	}
	if fi, err := os.Lstat(o.socket); err == nil {
		if fi.Mode().Type() != os.ModeSocket {
			return nil, errors.New("the --socket path exists and is not a socket")
		}
		if c, err := net.DialTimeout("unix", o.socket, time.Second); err == nil {
			c.Close()
			return nil, errors.New("another process answers on the --socket path")
		}
		if err := os.Remove(o.socket); err != nil {
			return nil, err
		}
	}
	// The socket file is made with the mode asked, not the process's umask
	// default, so there is no moment when it is wider.
	old := syscall.Umask(int(0o777 &^ o.socketMode))
	l, err := net.Listen("unix", o.socket)
	syscall.Umask(old)
	return l, err
}

// run serves until ctx ends, then shuts down gracefully. ready, when not nil,
// is called with the listener's address once it accepts. It returns the exit
// code.
func run(ctx context.Context, args []string, errout io.Writer, ready func(net.Addr)) int {
	o, code, ok := parse(args, errout)
	if !ok {
		return code
	}
	var daemon seatapp.Daemon = unwiredDaemon{}
	var client *seatgate.Client
	if o.gate != "" {
		id, err := seatgate.LoadIdentity(o.identity)
		if err != nil {
			// seatgate's errors are fixed texts, with no path and nothing
			// of the key.
			fmt.Fprintf(errout, "ataniq-seat: --identity: %v\n", err)
			return 2
		}
		client, err = seatgate.NewClient(o.gate, seatgate.ClientConfig{
			TLS:  seatgate.ClientTLS(id, o.gatePin, serverName(o.gate)),
			Caps: seatlink.Caps(),
			// The link's budget keeps room at the gate for this many pings.
			PingInterval: seatlink.PingInterval,
		})
		if err != nil {
			fmt.Fprintf(errout, "ataniq-seat: %v\n", err)
			return 2
		}
		link, err := seatlink.New(client, seatlink.WithGatePin(o.gatePin))
		if err != nil {
			fmt.Fprintf(errout, "ataniq-seat: %v\n", err)
			return 2
		}
		daemon = link
		if gateLinkHook != nil {
			gateLinkHook(client)
		}
	}
	// Every decision's challenge and every decision asks the guard first.
	// The refusals are logged from the handlers' goroutines, so from here on
	// every write to errout goes through logf, one at a time.
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(errout, format, args...)
	}
	daemon = newGuardedDaemon(daemon, o.guardDir, func(code string) {
		logf("ataniq-seat: a decision was refused, decisions are paused on this host: %s\n", code)
	})
	if daemonHook != nil {
		daemonHook(daemon)
	}
	h, err := seatapp.New(seatapp.Config{
		Origin:           o.origin,
		RPID:             o.rpid,
		Daemon:           daemon,
		ClientAddrHeader: o.clientAddrHeader,
	})
	if err != nil {
		fmt.Fprintf(errout, "ataniq-seat: %v\n", err)
		return 2
	}
	l, err := listen(o)
	if err != nil {
		fmt.Fprintf(errout, "ataniq-seat: %v\n", err)
		return 1
	}
	srv := seatapp.Server(h)
	if client != nil {
		// The link runs under the signal context, and its own cancel, so it
		// ends with the server: after the shutdown below has let requests in
		// flight finish, the deferred cancel stops it and the wait sees it
		// out.
		lctx, cancel := context.WithCancel(ctx)
		linked := make(chan struct{})
		go func() {
			defer close(linked)
			client.Run(lctx)
		}()
		defer func() {
			cancel()
			<-linked
		}()
		logf("ataniq-seat: linking to the seat gate at %s\n", o.gate)
	}
	logf("ataniq-seat: decisions follow the guard in %s\n", o.guardDir)
	logf("ataniq-seat: serving on %s\n", l.Addr())
	if ready != nil {
		ready(l.Addr())
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case err := <-done:
		logf("ataniq-seat: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logf("ataniq-seat: shutdown: %v\n", err)
		return 1
	}
	return 0
}
