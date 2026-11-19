//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
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
// (bounded, for the refusal's message), whether the read ended at end-of-file,
// and the reason when no line was accepted.
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

// fifoChannel is the report FIFO. It opens the reader first, then a write end
// of its own, so that the reader does not see end-of-file before the probe's
// descriptor has been opened; the plan's pass-through step opens the FIFO
// write-only without creating it. The one read takes one line with
// assert.ReadProbeReport and does not wait for end-of-file: bubblewrap's own
// processes may keep the other end open for the life of the sandbox. Once the
// line is in, Trailing asks whether anything followed it. Closing the write end
// of the sequence (when the first process has exited) lets the reader see
// end-of-file once every other writer has gone.
type fifoChannel struct {
	base, dir int // the runtime folder and the report folder
	reader    *os.File
	out       chan lineRead

	mu       sync.Mutex
	writer   *os.File
	closed   bool
	accepted bool
}

// OpenReport implements System.
func (*realSystem) OpenReport(runtimeRoot, layoutRole string) (ReportChannel, error) {
	base, err := openRuntimeFolder(RuntimeFolder(runtimeRoot, layoutRole))
	if err != nil {
		return nil, err
	}
	dir, err := freshFolder(base, reportDirName, map[string]node{reportFIFOName: {typ: unix.S_IFIFO}})
	if err != nil {
		unix.Close(base)
		return nil, err
	}
	p := ReportFIFO(runtimeRoot, layoutRole)
	c := &fifoChannel{base: base, dir: dir, out: make(chan lineRead, 1)}
	// Whatever fails from here on leaves nothing behind: close removes the FIFO
	// and the folder.
	fail := func(err error) (ReportChannel, error) {
		c.close()
		return nil, err
	}
	if err := unix.Mkfifoat(dir, reportFIFOName, 0o600); err != nil {
		return fail(fmt.Errorf("make the report FIFO: %w", err))
	}
	rfd, err := unix.Openat(dir, reportFIFOName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NOCTTY, 0)
	if err != nil {
		return fail(fmt.Errorf("open the report FIFO for reading: %w", err))
	}
	c.reader = os.NewFile(uintptr(rfd), p)
	if err := unix.Fchmod(rfd, 0o600); err != nil {
		return fail(fmt.Errorf("set the mode of the report FIFO: %w", err))
	}
	wfd, err := unix.Openat(dir, reportFIFOName, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NOCTTY, 0)
	if err != nil {
		return fail(fmt.Errorf("open the report FIFO for writing: %w", err))
	}
	c.writer = os.NewFile(uintptr(wfd), p)
	go c.readLine()
	return c, nil
}

// readLine reads one report line, delivers what it got and ends.
func (c *fifoChannel) readLine() {
	rec := &recordingReader{r: c.reader}
	rep, err := assert.ReadProbeReport(rec, assert.MaxReportLineBytes)
	c.out <- lineRead{Report: rep, Data: rec.data, EOF: rec.eof, Err: err}
}

// ReadLine implements ReportChannel.
func (c *fifoChannel) ReadLine(ctx context.Context, exited <-chan struct{}) (assert.Report, error) {
	var lr lineRead
	got := false
	select {
	case lr = <-c.out:
		got = true
	case <-exited:
		// The process is gone: let the reader see the end of the channel. A line
		// written just before the exit is still in it.
		c.closeWriter()
		select {
		case lr = <-c.out:
			got = true
		case <-ctx.Done():
		}
	case <-ctx.Done():
	}
	if !got {
		return assert.Report{}, fmt.Errorf("no report line came: %w", ctx.Err())
	}
	if len(lr.Data) == 0 && lr.EOF {
		return assert.Report{}, errors.New("the first process exited and the channel closed with no report line")
	}
	if lr.Err != nil {
		ex := lr.Data
		if len(ex) > 200 {
			ex = ex[:200]
		}
		return assert.Report{}, fmt.Errorf("the report line was refused: %s (the line begins %q)", assert.EscapeDetail(lr.Err.Error()), assert.EscapeDetail(string(ex)))
	}
	c.mu.Lock()
	c.accepted = true
	c.mu.Unlock()
	return lr.Report, nil
}

// Trailing implements ReportChannel: assert.CheckNoMore on a non-blocking read.
// It is only valid after the read of the line has delivered (the reading
// goroutine has ended).
func (c *fifoChannel) Trailing() error {
	c.mu.Lock()
	accepted, closed := c.accepted, c.closed
	c.mu.Unlock()
	if !accepted || closed {
		return errors.New("no report line was accepted, so there is nothing to ask about what followed it")
	}
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

// closeWriter closes the sequence's own write end.
func (c *fifoChannel) closeWriter() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer != nil {
		c.writer.Close()
		c.writer = nil
	}
}

// Close implements ReportChannel.
func (c *fifoChannel) Close() error { return c.close() }

// close ends the reader, closes the write end, removes the FIFO and its folder
// and closes the descriptors of the folders. It is safe to call twice.
func (c *fifoChannel) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.closeWriter()
	var errs []error
	if c.reader != nil {
		errs = append(errs, c.reader.Close())
	}
	errs = append(errs, unlink(c.dir, reportFIFOName, false), unlink(c.base, reportDirName, true))
	for _, fd := range []int{c.dir, c.base} {
		if err := unix.Close(fd); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
