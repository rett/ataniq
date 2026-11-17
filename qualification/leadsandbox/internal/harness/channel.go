//go:build linux && amd64

package harness

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// errOverlong says the channel held more than a report line may be, without a
// newline.
var errOverlong = fmt.Errorf("more than %d bytes without a newline", assert.MaxReportLineBytes)

// lineRead is what the channel delivered: bytes up to the first newline (kept
// in Data), and the reason it stopped when no newline came.
type lineRead struct {
	Data []byte
	// Err is nil when a whole line (ending in a newline) was read; io.EOF
	// when every writer closed first; errOverlong; or another read error.
	Err error
}

// reportChannel is the FIFO the probe's report line comes through. The
// harness opens the reader first, then a write end of its own, so the reader
// does not see end-of-file before the probe's descriptor has been opened; the
// plan's pass-through step opens the FIFO write-only without creating it.
// Closing the write end of the harness (CloseWriter) lets the reader see
// end-of-file once every other writer has gone.
type reportChannel struct {
	path   string
	reader *os.File
	out    chan lineRead

	mu     sync.Mutex
	writer *os.File
	closed bool
}

// openReportChannel makes the FIFO (mode 0600, never replacing anything),
// opens its reader and starts reading.
func openReportChannel(path string) (*reportChannel, error) {
	if err := unix.Mkfifo(path, 0o600); err != nil {
		return nil, fmt.Errorf("make the report FIFO: %w", err)
	}
	rfd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NOCTTY, 0)
	if err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("open the report FIFO for reading: %w", err)
	}
	reader := os.NewFile(uintptr(rfd), path)
	wfd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NOCTTY, 0)
	if err != nil {
		reader.Close()
		os.Remove(path)
		return nil, fmt.Errorf("open the report FIFO for writing: %w", err)
	}
	c := &reportChannel{path: path, reader: reader, writer: os.NewFile(uintptr(wfd), path), out: make(chan lineRead, 1)}
	go c.readLine()
	return c, nil
}

// readLine reads up to the first newline, delivers what it got and ends.
func (c *reportChannel) readLine() {
	var line []byte
	buf := make([]byte, 4096)
	for {
		n, err := c.reader.Read(buf)
		if n > 0 {
			line = append(line, buf[:n]...)
			if i := bytes.IndexByte(line, '\n'); i >= 0 {
				c.out <- lineRead{Data: line[:i+1]}
				return
			}
			if len(line) > assert.MaxReportLineBytes {
				c.out <- lineRead{Data: line[:assert.MaxReportLineBytes+1], Err: errOverlong}
				return
			}
		}
		if err != nil {
			c.out <- lineRead{Data: line, Err: err}
			return
		}
	}
}

// closeWriter closes the harness's own write end.
func (c *reportChannel) closeWriter() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer != nil {
		c.writer.Close()
		c.writer = nil
	}
}

// close ends the reader, closes the write end and removes the FIFO. It is
// safe to call twice; the error is the first that matters.
func (c *reportChannel) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.closeWriter()
	err := c.reader.Close()
	if rerr := os.Remove(c.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}
