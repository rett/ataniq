// Command ataniq-seat serves the seat web app (package seatapp) on a Unix
// socket or a loopback address. A proxy in front owns the network port and the
// TLS; this command never listens on a non-loopback address.
//
//	ataniq-seat (--origin https://HOST | --origin-file PATH) (--socket PATH | --listen 127.0.0.1:PORT)
//	            [--rpid HOST] [--socket-mode 0660] [--client-addr-header NAME]
//
// The origin is the address the reverse proxy serves the app on, https on
// port 443, and has no default: it comes from --origin or from the first line
// of an owner-local file named by --origin-file. The app itself speaks plain
// HTTP behind the proxy.
//
// Nothing secret is printed or read from the command line. The daemon port is
// not wired yet: pairing answers with the fixed code daemon_unavailable.
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"ataniq/internal/seatapp"
)

const usageLine = "Usage: ataniq-seat (--origin https://HOST | --origin-file PATH) (--socket PATH | --listen LOOPBACK:PORT) [--rpid HOST] [--socket-mode MODE] [--client-addr-header NAME]\n" +
	"--origin and --origin-file exclude each other; --socket and --listen exclude each other; --listen takes an IP literal of the loopback range."

// maxOriginFile bounds the origin file.
const maxOriginFile = 1 << 10

// shutdownGrace is how long a shutdown lets requests in flight finish.
const shutdownGrace = 10 * time.Second

// unwiredDaemon is the daemon port until the production wiring exists.
type unwiredDaemon struct{}

func (unwiredDaemon) Pair(context.Context, seatapp.PairRequest) (seatapp.PairResult, error) {
	return seatapp.PairResult{}, seatapp.ErrDaemonUnavailable
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stderr, nil)
	stop()
	os.Exit(code)
}

// options is what the flags select.
type options struct {
	origin, originFile, rpid, socket, listen, clientAddrHeader string
	socketMode                                                 os.FileMode
}

// parse reads the flags. On failure it has printed the diagnostic and the
// usage to errout and returns the exit code to use.
func parse(args []string, errout io.Writer) (o options, code int, ok bool) {
	fs := flag.NewFlagSet("ataniq-seat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("socket-mode", "0660", "permission bits of the socket file, octal")
	fs.StringVar(&o.origin, "origin", "", "the app's origin, https://host")
	fs.StringVar(&o.originFile, "origin-file", "", "owner-local file holding the origin")
	fs.StringVar(&o.rpid, "rpid", "", "relying party id; the origin's host when empty")
	fs.StringVar(&o.socket, "socket", "", "Unix socket path")
	fs.StringVar(&o.listen, "listen", "", "loopback address and port")
	fs.StringVar(&o.clientAddrHeader, "client-addr-header", "", "header the proxy sets to the client address")
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
	return o, 0, true
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
	h, err := seatapp.New(seatapp.Config{
		Origin:           o.origin,
		RPID:             o.rpid,
		Daemon:           unwiredDaemon{},
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
	fmt.Fprintf(errout, "ataniq-seat: serving on %s\n", l.Addr())
	if ready != nil {
		ready(l.Addr())
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case err := <-done:
		fmt.Fprintf(errout, "ataniq-seat: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(errout, "ataniq-seat: shutdown: %v\n", err)
		return 1
	}
	return 0
}
