package leadattach

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/wire"
)

// ErrNoTicket is what a ticket source returns when it has no ticket yet. It
// is not a failure: the client waits and asks again.
var ErrNoTicket = errors.New("leadattach: no ticket yet")

// maxTicketFile is the most bytes read from a ticket file. A ticket is
// wire.TicketLen characters; anything longer is not one.
const maxTicketFile = 256

// FileTickets returns a ticket source that reads the single-use ticket from
// the file at path, which the daemon writes for this console. Each call:
//
//   - reports ErrNoTicket when the file is absent or empty;
//   - refuses the file (an error that is not ErrNoTicket, naming why) when it
//     is a symbolic link, is not a regular file, is not owned by the user,
//     has any mode but 0600, or is locked by another reader;
//   - reads at most maxTicketFile bytes, and empties the file: a ticket is
//     single use, and a ticket file that was read once and holds junk is
//     emptied too, so it does not wedge the client;
//   - returns the ticket when what was read is one (wire.CheckTicket), and an
//     error when it is not.
//
// A ticket file is replaced, not appended to: the writer creates a new 0600
// file and renames it over path, so that a reader that holds the old file
// empties only that. The check of the owner and mode is made on the open
// file, after it was opened without following a link.
func FileTickets(path string) func() (string, error) {
	uid := os.Geteuid()
	return func() (string, error) { return readTicketFile(path, uid) }
}

// FileTicketSource returns the two functions of a ticket file, for
// Config.Tickets and Config.TicketReady: the same source as FileTickets, and
// a presence check that does not take the ticket. The check is true when the
// file at path exists, is a regular file (a symbolic link is not followed and
// is not one) of the user with mode 0600, and is not empty. It does not open
// the file, reads nothing of its content and empties nothing, so that
// asking it as often as the client polls costs no ticket; a file that the
// source would refuse, or that holds no ticket at the time of the read, is a
// ticket that is not there as far as the check goes, and the reason for a
// refusal is not shown while the check says no.
func FileTicketSource(path string) (tickets func() (string, error), ready func() bool) {
	uid := os.Geteuid()
	return FileTickets(path), func() bool { return ticketFilePresent(path, uid) }
}

// ticketFilePresent is the check of FileTicketSource: one lstat.
func ticketFilePresent(path string, uid int) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid &&
		fi.Mode().Perm() == 0o600 && fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) == 0
}

// ticketOpenFlags open the ticket file without side effects, whatever is at
// the path: no controlling terminal is acquired if it is a terminal device
// (O_NOCTTY), no wait for a peer if it is a named pipe (O_NONBLOCK), no
// symbolic link is followed (O_NOFOLLOW), and the descriptor does not reach a
// child process (O_CLOEXEC).
const ticketOpenFlags = os.O_RDWR | unix.O_NOCTTY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC

func readTicketFile(path string, uid int) (string, error) {
	f, err := os.OpenFile(path, ticketOpenFlags, 0)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", ErrNoTicket
		case errors.Is(err, syscall.ELOOP):
			return "", errors.New("ticket file refused: it is a symbolic link")
		case errors.Is(err, fs.ErrPermission):
			// Most likely a mode that gives the owner no write access.
			if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode().IsRegular() && fi.Mode().Perm() != 0o600 {
				return "", errors.New("ticket file refused: its mode is not 0600")
			}
		}
		return "", fmt.Errorf("ticket file refused: %s", fsReason(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("ticket file refused: %s", fsReason(err))
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return "", errors.New("ticket file refused: it is not a regular file")
	case !ok || int(st.Uid) != uid:
		return "", errors.New("ticket file refused: it is not owned by the user")
	case fi.Mode().Perm() != 0o600 || fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
		return "", errors.New("ticket file refused: its mode is not 0600")
	}
	// One reader at a time: a second console process that reads at the same
	// moment finds the file locked and sees no ticket.
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return "", ErrNoTicket
		}
		return "", fmt.Errorf("ticket file refused: %s", fsReason(err))
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxTicketFile+1))
	if err != nil {
		return "", fmt.Errorf("ticket file refused: %s", fsReason(err))
	}
	if len(buf) == 0 {
		return "", ErrNoTicket
	}
	// The ticket is spent by being read, whatever it turns out to be.
	if err := f.Truncate(0); err != nil {
		return "", fmt.Errorf("ticket file refused: it cannot be emptied (%s)", fsReason(err))
	}
	if len(buf) > maxTicketFile {
		return "", errors.New("ticket file held more than a ticket; emptied")
	}
	ticket := strings.TrimSpace(string(buf))
	if err := wire.CheckTicket(ticket); err != nil {
		return "", errors.New("ticket file did not hold a ticket; emptied")
	}
	return ticket, nil
}

// fsReason is the operating system's reason of an error without the path,
// which the client would otherwise put in its status.
func fsReason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return err.Error()
}
