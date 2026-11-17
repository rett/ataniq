package assert

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// Bounds on the report channel.
const (
	// maxEmptyReads is how many reads that return no byte and no error
	// ReadProbeReport tolerates before it gives up (io.ErrNoProgress).
	maxEmptyReads = 100
	// maxTrailingBytes is the most CheckNoMore reads from the channel.
	maxTrailingBytes = 4096
)

// ReadProbeReport reads exactly one report line from the channel the probe
// writes to, and parses it with [ParseProbeLine]. It reads one byte at a time
// and stops at the first newline, so no byte that follows the line is taken
// from r: [CheckNoMore] looks at what comes after. The line, terminator
// included, is at most limit bytes (and never more than [MaxReportLineBytes]);
// a longer line, a read that ends before a newline, and a line that
// ParseProbeLine refuses are errors, with the reason.
//
// It does not wait for end-of-file, which does not come while the sandbox is
// alive: bubblewrap's own processes keep the descriptor open (a qualification
// item). The one accepted line is the positive signal. A read that blocks is
// the caller's to bound, with a deadline or a non-blocking descriptor: this
// function sets none.
func ReadProbeReport(r io.Reader, limit int) (Report, error) {
	if r == nil {
		return Report{}, refuse("report channel", "no reader")
	}
	if limit <= 0 {
		return Report{}, refuse("report channel", "the limit must be positive")
	}
	if limit > MaxReportLineBytes {
		limit = MaxReportLineBytes
	}
	line := make([]byte, 0, 512)
	var one [1]byte
	empty := 0
	for {
		n, err := r.Read(one[:])
		if n > 0 {
			empty = 0
			line = append(line, one[0])
			if one[0] == '\n' {
				return ParseProbeLine(line)
			}
			if len(line) >= limit {
				return Report{}, refuse("report channel", "the report line is longer than %d bytes", limit)
			}
			continue
		}
		switch {
		case err == io.EOF:
			if len(line) == 0 {
				return Report{}, refuse("report channel", "ended before a report line")
			}
			return Report{}, refuse("report channel", "ended after %d bytes, before the end of the report line", len(line))
		case err != nil:
			return Report{}, fmt.Errorf("assert: read the report channel: %w", err)
		}
		if empty++; empty >= maxEmptyReads {
			return Report{}, fmt.Errorf("assert: read the report channel: %w", io.ErrNoProgress)
		}
	}
}

// CheckNoMore says nothing follows the report line: the caller offers the
// channel again after [ReadProbeReport] and CheckNoMore reads what is there,
// once, at most 4096 bytes. A byte (a second line, or anything else) is an
// error that says how many came and shows the first of them escaped: a start
// that the holder takes by one line does not tolerate more, because the lead
// may hold the descriptor too, or something else may write. It stops at the
// first byte, so it does not wait for the rest. End-of-file, and a read that
// has nothing to give (a deadline that passed, or EAGAIN on a non-blocking
// descriptor) is nil. Any other read error is returned: the question was not
// asked, and the holder must not take the start.
//
// The caller bounds the wait, as for ReadProbeReport: a blocking read of a
// channel that stays quiet would not return.
func CheckNoMore(r io.Reader) error {
	if r == nil {
		return refuse("report channel", "no reader")
	}
	var buf [maxTrailingBytes]byte
	for empty := 0; ; empty++ {
		n, err := r.Read(buf[:])
		switch {
		case n > 0:
			return refuse("report channel", "holds %d bytes or more after the report line, from %s", n, clipQuote(string(buf[:min(n, 40)])))
		case err == io.EOF, errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, syscall.EAGAIN):
			return nil
		case err != nil:
			return fmt.Errorf("assert: read the report channel: %w", err)
		case empty+1 >= maxEmptyReads:
			return fmt.Errorf("assert: read the report channel: %w", io.ErrNoProgress)
		}
	}
}
