//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/wire"
)

// Names in the holder's runtime folder. The holder serves control.sock there
// (internal/leadholder/serve names it too; this package does not import that
// one), and the console reads the ticket file named in its --ticket-file.
const (
	controlSocketName = "control.sock"
	// TicketFileName is the name of the ticket file the daemon writes in the
	// role's runtime folder for the console.
	TicketFileName = "ticket"
)

// callHolder makes one control request of the holder: it dials the control
// socket in dir, writes the request frame and reads one response frame, waiting
// at most wait for it, counted from the end of the write. One deadline, dial
// from the start of the call, covers the dial and the write together, so the
// whole exchange ends within dial and wait.
//
// wrote tells how far it got. With wrote false nothing reached the holder: the
// socket could not be dialled or the frame could not be written (err says
// which). With wrote true the frame was written, and err is not nil when there
// was no usable answer (no answer in time, a closed connection, a frame that
// does not decode); the caller must treat the request as delivered.
func callHolder(ctx context.Context, dir string, req wire.Request, dial, wait time.Duration) (resp wire.Response, wrote bool, err error) {
	payload, err := wire.EncodeRequest(req)
	if err != nil {
		return wire.Response{}, false, err
	}
	dctx, cancel := context.WithTimeout(ctx, dial)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dctx, "unix", path.Join(dir, controlSocketName))
	if err != nil {
		return wire.Response{}, false, err
	}
	defer conn.Close()
	// A context that ends (the daemon is stopping) ends the exchange.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	// The write shares the deadline of the dial: a context made by WithTimeout
	// always has one, and it is the earlier of dial and the caller's own.
	until, _ := dctx.Deadline()
	if err := conn.SetWriteDeadline(until); err != nil {
		return wire.Response{}, false, err
	}
	if err := wire.NewWriter(conn, wire.ControlRequests).WriteFrame(wire.TypeRequest, payload); err != nil {
		return wire.Response{}, false, err
	}
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return wire.Response{}, true, err
	}
	fr, err := wire.NewReader(conn, wire.ControlResponses).Next()
	if err != nil {
		return wire.Response{}, true, err
	}
	resp, err = wire.DecodeResponse(fr.Payload)
	if err != nil {
		return wire.Response{}, true, err
	}
	return resp, true, nil
}

// openPrivateDir opens a folder by its handle, without following a link at its
// last element, and checks the handle: a folder, owned by the caller, with no
// access for group or others. The caller closes the descriptor.
func openPrivateDir(dir string) (int, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		unix.Close(fd)
		return -1, errors.New("not a folder")
	case int(st.Uid) != unix.Geteuid():
		unix.Close(fd)
		return -1, errors.New("not owned by the caller")
	case st.Mode&0o077 != 0 || st.Mode&0o700 != 0o700:
		unix.Close(fd)
		return -1, fmt.Errorf("mode %04o, want 0700", st.Mode&0o7777)
	}
	return fd, nil
}

// writeTicketFile writes the ticket, and no newline, to the ticket file in dir:
// a new file with mode 0600 made by handle in the folder's handle and renamed
// over the old one, so that a console that holds the old file empties only
// that. It returns the path of the file.
func writeTicketFile(dir, ticket string) (string, error) {
	dfd, err := openPrivateDir(dir)
	if err != nil {
		return "", err
	}
	defer unix.Close(dfd)
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	tmp := ".ticket.tmp-" + hex.EncodeToString(rnd[:])
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), tmp)
	cleanup := func() { unix.Unlinkat(dfd, tmp, 0) }
	// The umask can only narrow 0600; this sets it exactly.
	if err := unix.Fchmod(fd, 0o600); err != nil {
		f.Close()
		cleanup()
		return "", err
	}
	_, err = f.WriteString(ticket)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", err
	}
	if err := unix.Renameat(dfd, tmp, dfd, TicketFileName); err != nil {
		cleanup()
		return "", err
	}
	_ = unix.Fsync(dfd)
	return path.Join(dir, TicketFileName), nil
}
