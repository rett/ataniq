//go:build linux && amd64

package harness

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// openPTY opens /dev/ptmx and its slave, of the given size. The master is
// non-blocking, so that closing it ends a read in progress; both are
// close-on-exec, and neither becomes the controlling terminal of the caller.
// The caller closes both.
func openPTY(rows, cols uint16) (master, slave *os.File, err error) {
	mfd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	closeM := func() { unix.Close(mfd) }
	if err := unix.IoctlSetPointerInt(mfd, unix.TIOCSPTLCK, 0); err != nil {
		closeM()
		return nil, nil, fmt.Errorf("unlock the pseudo-terminal: %w", err)
	}
	n, err := unix.IoctlGetInt(mfd, unix.TIOCGPTN)
	if err != nil {
		closeM()
		return nil, nil, fmt.Errorf("name the pseudo-terminal: %w", err)
	}
	if err := unix.IoctlSetWinsize(mfd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
		closeM()
		return nil, nil, fmt.Errorf("size the pseudo-terminal: %w", err)
	}
	name := fmt.Sprintf("/dev/pts/%d", n)
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		closeM()
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return os.NewFile(uintptr(mfd), "/dev/ptmx"), os.NewFile(uintptr(sfd), name), nil
}

// boundedBuffer keeps the first limit bytes written to it and counts the
// rest. It is safe for concurrent use.
type boundedBuffer struct {
	mu      sync.Mutex
	limit   int
	data    []byte
	total   int64
	dropped int64
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{limit: limit} }

// Write implements io.Writer. It never fails and never keeps more than the
// limit.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	room := b.limit - len(b.data)
	if room < 0 {
		room = 0
	}
	keep := len(p)
	if keep > room {
		keep = room
	}
	b.data = append(b.data, p[:keep]...)
	b.dropped += int64(len(p) - keep)
	return len(p), nil
}

// snapshot is the content so far.
func (b *boundedBuffer) snapshot() TerminalOutput {
	b.mu.Lock()
	defer b.mu.Unlock()
	return TerminalOutput{Data: append([]byte(nil), b.data...), Total: b.total, Dropped: b.dropped}
}
