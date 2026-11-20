//go:build linux && amd64

package harness

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

func newChannel(t *testing.T) *reportChannel {
	t.Helper()
	dir, err := os.MkdirTemp("", "chan")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c, err := openReportChannel(dir + "/report.fifo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.close() })
	return c
}

// A writer opened without waiting for a reader proves the reader is there:
// the open of a FIFO for writing fails with ENXIO when nobody reads it.
func TestReaderIsOpenedFirst(t *testing.T) {
	c := newChannel(t)
	fd, err := unix.Open(c.path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("the reader is not open: %v", err)
	}
	unix.Close(fd)
	st, err := os.Lstat(c.path)
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 || st.Mode().Perm() != 0o600 {
		t.Errorf("FIFO %v %v", st, err)
	}
}

// reportLine is a valid probe line with the digest given.
func reportLine(t *testing.T, digest string) []byte {
	t.Helper()
	var rs []assert.Result
	for _, a := range assert.Assertions() {
		if a.HasInside() {
			rs = append(rs, assert.Result{Number: a.Number, ID: a.ID, Status: assert.StatusOK, Detail: "fake"})
		}
	}
	return assert.EncodeLine(assert.Report{Version: assert.ReportVersion, Digest: digest, Results: rs})
}

func TestChannelDeliversOneLineAndLeavesWhatFollows(t *testing.T) {
	c := newChannel(t)
	w, err := os.OpenFile(c.path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	line := reportLine(t, digest)
	// The writer stays open: no end-of-file comes, and the line is accepted
	// without waiting for one.
	if _, err := w.Write(append(append([]byte{}, line...), "and more after the newline\n"...)); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	got := <-c.out
	if got.Err != nil || got.Report.Digest != digest || len(got.Report.Results) != 9 || string(got.Data) != string(line) || got.EOF {
		t.Fatalf("got %+v", got)
	}
	// The bytes after the newline were not taken by the read of the line.
	err = c.trailing()
	var refusal *assert.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("the bytes after the line were not found: %v", err)
	}
}

func TestChannelQuietAfterTheLineIsNotAFailure(t *testing.T) {
	c := newChannel(t)
	w, _ := os.OpenFile(c.path, os.O_WRONLY, 0)
	defer w.Close()
	w.Write(reportLine(t, strings.Repeat("b", 64)))
	if got := <-c.out; got.Err != nil {
		t.Fatalf("got %+v", got)
	}
	// Nothing is there, and the writers are still open: the question
	// returns at once with no answer to wait for.
	if err := c.trailing(); err != nil {
		t.Errorf("a quiet channel: %v", err)
	}
	// After the last writer has gone the channel is at end-of-file, which is
	// quiet too.
	w.Close()
	c.closeWriter()
	if err := c.trailing(); err != nil {
		t.Errorf("a closed channel: %v", err)
	}
}

func TestChannelTrailingByteAfterAQuietCheckIsFound(t *testing.T) {
	c := newChannel(t)
	w, _ := os.OpenFile(c.path, os.O_WRONLY, 0)
	defer w.Close()
	w.Write(reportLine(t, strings.Repeat("c", 64)))
	<-c.out
	if err := c.trailing(); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("x"))
	if err := c.trailing(); err == nil {
		t.Error("a byte after the line was not found")
	}
}

func TestChannelDoesNotEndBeforeTheWriterCloses(t *testing.T) {
	c := newChannel(t)
	select {
	case got := <-c.out:
		t.Fatalf("the channel delivered %+v with nobody having written", got)
	default:
	}
	c.closeWriter()
	got := <-c.out
	if got.Err == nil || !got.EOF || len(got.Data) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestChannelPartialLineEndsAtEOF(t *testing.T) {
	c := newChannel(t)
	w, _ := os.OpenFile(c.path, os.O_WRONLY, 0)
	io.WriteString(w, "no newline")
	w.Close()
	c.closeWriter()
	got := <-c.out
	if got.Err == nil || !got.EOF || string(got.Data) != "no newline" {
		t.Errorf("got %+v", got)
	}
}

func TestChannelRefusesAGarbageLineAndKeepsItForTheEvidence(t *testing.T) {
	c := newChannel(t)
	w, _ := os.OpenFile(c.path, os.O_WRONLY, 0)
	defer w.Close()
	io.WriteString(w, "not a report\n")
	got := <-c.out
	if got.Err == nil || got.EOF || string(got.Data) != "not a report\n" {
		t.Errorf("got %+v", got)
	}
}

func TestChannelRefusesAnOverlongLine(t *testing.T) {
	c := newChannel(t)
	w, _ := os.OpenFile(c.path, os.O_WRONLY, 0)
	go func() {
		defer w.Close()
		buf := bytes.Repeat([]byte("a"), 4096)
		for i := 0; i < assert.MaxReportLineBytes/4096+3; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}()
	got := <-c.out
	if got.Err == nil || got.EOF || len(got.Data) > assert.MaxReportLineBytes {
		t.Errorf("got %d bytes, %v", len(got.Data), got.Err)
	}
}

func TestChannelCloseRemovesTheFIFOAndIsRepeatable(t *testing.T) {
	c := newChannel(t)
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(c.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the FIFO is still there: %v", err)
	}
	if err := c.close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestOpenReportChannelNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/report.fifo", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openReportChannel(dir + "/report.fifo"); err == nil {
		t.Fatal("an existing file was replaced")
	}
	if _, err := os.Stat(dir + "/report.fifo"); err != nil {
		t.Errorf("the existing file is gone: %v", err)
	}
}

func TestBoundedBuffer(t *testing.T) {
	b := newBoundedBuffer(10)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Write([]byte("abcd"))
		}()
	}
	wg.Wait()
	s := b.snapshot()
	if len(s.Data) != 10 || s.Total != 20 || s.Dropped != 10 {
		t.Errorf("%d kept, %d total, %d dropped", len(s.Data), s.Total, s.Dropped)
	}
	// The snapshot is a copy.
	s.Data[0] = 'z'
	if b.snapshot().Data[0] == 'z' {
		t.Error("the snapshot shares memory with the buffer")
	}
	n, err := b.Write(nil)
	if n != 0 || err != nil {
		t.Errorf("empty write: %d %v", n, err)
	}
}

func TestOpenPTYMakesAPairOfTheGivenSize(t *testing.T) {
	m, s, err := openPTY(31, 97)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	defer m.Close()
	defer s.Close()
	ws, err := unix.IoctlGetWinsize(int(s.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Row != 31 || ws.Col != 97 {
		t.Errorf("size %+v, %v", ws, err)
	}
	if _, err := io.WriteString(s, "hello\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	var got []byte
	for !bytes.Contains(got, []byte("hello")) {
		n, err := m.Read(buf)
		if err != nil {
			t.Fatalf("read the master: %v (got %q)", err, got)
		}
		got = append(got, buf[:n]...)
	}
}
