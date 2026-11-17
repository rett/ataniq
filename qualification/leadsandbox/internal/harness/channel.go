//go:build linux && amd64

package harness

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// lineRead is what the channel delivered to the one read of the report: the
// report when a line was accepted, the bytes the read took from the channel
// (bounded, for the evidence of a refused line), whether the read ended at
// end-of-file, and the reason when no line was accepted.
type lineRead struct {
	Report assert.Report
	// Data is what the read took from the channel, at most
	// assert.MaxReportLineBytes bytes.
	Data []byte
	// EOF is set when the read met end-of-file.
	EOF bool
	// Err is nil when assert.ReadProbeReport accepted one line.
	Err error
}

// recordingReader remembers what passes through it, up to the bound, and
// whether the source ended.
type recordingReader struct {
	r    io.Reader
	data []byte
	eof  bool
}

func (c *recordingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if room := assert.MaxReportLineBytes - len(c.data); n > 0 && room > 0 {
		c.data = append(c.data, p[:min(n, room)]...)
	}
	if errors.Is(err, io.EOF) {
		c.eof = true
	}
	return n, err
}

// reportChannel is the FIFO the probe's report line comes through. The
// harness opens the reader first, then a write end of its own, so the reader
// does not see end-of-file before the probe's descriptor has been opened; the
// plan's pass-through step opens the FIFO write-only without creating it.
// The one read takes one line with assert.ReadProbeReport and does not wait
// for end-of-file: bubblewrap's own processes may keep the other end open for
// the life of the sandbox. Once the line is in, [reportChannel.trailing] asks
// whether anything followed it. Closing the write end of the harness
// (closeWriter) lets the reader see end-of-file once every other writer has
// gone, which the harness does only after the first process has exited.
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

// readLine reads one report line, delivers what it got and ends.
func (c *reportChannel) readLine() {
	rec := &recordingReader{r: c.reader}
	rep, err := assert.ReadProbeReport(rec, assert.MaxReportLineBytes)
	c.out <- lineRead{Report: rep, Data: rec.data, EOF: rec.eof, Err: err}
}

// trailing asks the channel, once and without waiting, whether bytes follow
// the line that was read: assert.CheckNoMore on a non-blocking read. It is
// only valid after the read of the line has delivered (the reading goroutine
// has ended), and a nil result means nothing was there.
func (c *reportChannel) trailing() error {
	return assert.CheckNoMore(nonBlockingReader{c.reader})
}

// nonBlockingReader reads the channel's descriptor with the system call and
// never waits: a channel with nothing in it gives EAGAIN, and end-of-file is
// io.EOF.
type nonBlockingReader struct{ f *os.File }

func (r nonBlockingReader) Read(p []byte) (int, error) {
	sc, err := r.f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var n int
	var rerr error
	if err := sc.Read(func(fd uintptr) bool {
		n, rerr = unix.Read(int(fd), p)
		return true // never wait for readiness
	}); err != nil {
		return 0, err
	}
	if rerr == nil && n == 0 && len(p) > 0 {
		return 0, io.EOF
	}
	if n < 0 {
		n = 0
	}
	return n, rerr
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
