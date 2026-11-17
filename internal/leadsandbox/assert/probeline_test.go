package assert

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
)

func probeLine(t *testing.T) []byte {
	t.Helper()
	return EncodeLine(insideReport(testSpec("claude")))
}

func TestReadProbeReportTakesOneLineAndNothingMore(t *testing.T) {
	line := probeLine(t)
	rest := "trailing"
	r := bytes.NewReader(append(append([]byte(nil), line...), rest...))
	rep, err := ReadProbeReport(r, MaxReportLineBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 8 {
		t.Errorf("results: %+v", rep.Results)
	}
	// Not a byte past the newline was taken.
	left, _ := io.ReadAll(r)
	if string(left) != rest {
		t.Errorf("left %q, want %q", left, rest)
	}
	// The same reader, offered again, shows the trailing bytes.
	r2 := bytes.NewReader(append(append([]byte(nil), line...), "x\n"...))
	if _, err := ReadProbeReport(r2, MaxReportLineBytes); err != nil {
		t.Fatal(err)
	}
	err = CheckNoMore(r2)
	if _, ok := err.(*Refusal); !ok || !strings.Contains(err.Error(), "2 bytes or more") {
		t.Errorf("CheckNoMore: %v", err)
	}
	// A line that is all there is leaves nothing.
	r3 := bytes.NewReader(line)
	if _, err := ReadProbeReport(r3, len(line)); err != nil {
		t.Fatalf("a limit of exactly the line: %v", err)
	}
	if err := CheckNoMore(r3); err != nil {
		t.Errorf("CheckNoMore after the only line: %v", err)
	}
}

// The writer keeps the descriptor open, as bubblewrap's own processes do: the
// line is enough, and end-of-file is never awaited. A second line that comes
// later is seen.
func TestReadProbeReportDoesNotWaitForEOF(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	if _, err := pw.Write(probeLine(t)); err != nil {
		t.Fatal(err)
	}
	if err := pr.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProbeReport(pr, MaxReportLineBytes); err != nil {
		t.Fatal(err)
	}
	// Nothing follows yet, and the write end is open: a read with a deadline
	// that has passed has nothing to give, which is not trailing data.
	if err := pr.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoMore(pr); err != nil {
		t.Errorf("nothing followed: %v", err)
	}
	// A second line is a failure of the start.
	if _, err := pw.Write(probeLine(t)); err != nil {
		t.Fatal(err)
	}
	if err := pr.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoMore(pr); err == nil || !strings.Contains(err.Error(), "after the report line") {
		t.Errorf("a second line: %v", err)
	}
}

func TestReadProbeReportRefusals(t *testing.T) {
	line := probeLine(t)
	full := EncodeLine(insideReport(testSpec("claude")).With(Result{9, IDRoleLockLingering, StatusOK, ""}))
	cases := []struct {
		name  string
		r     io.Reader
		limit int
		want  string
	}{
		{"nothing", strings.NewReader(""), 1000, "ended before a report line"},
		{"no newline", bytes.NewReader(bytes.TrimSuffix(line, []byte("\n"))), MaxReportLineBytes, "before the end of the report line"},
		{"over the limit", bytes.NewReader(line), len(line) - 1, "longer than"},
		{"over the limit and no end", strings.NewReader(strings.Repeat("a", 100)), 10, "longer than 10"},
		{"not a report", strings.NewReader("hello\n"), 100, "does not start with"},
		{"a holder result", bytes.NewReader(full), MaxReportLineBytes, "not a result the probe makes"},
		{"no limit", bytes.NewReader(line), 0, "limit must be positive"},
		{"no reader", nil, 10, "no reader"},
	}
	for _, c := range cases {
		_, err := ReadProbeReport(c.r, c.limit)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", c.name, err, c.want)
		}
	}
	boom := errors.New("boom")
	if _, err := ReadProbeReport(iotest.ErrReader(boom), 100); !errors.Is(err, boom) {
		t.Errorf("a read error: %v", err)
	}
	if _, err := ReadProbeReport(emptyReader{}, 100); !errors.Is(err, io.ErrNoProgress) {
		t.Errorf("a reader that gives nothing: %v", err)
	}
	// A limit above the largest line is the largest line.
	huge := strings.Repeat("a", MaxReportLineBytes+10)
	if _, err := ReadProbeReport(strings.NewReader(huge), 1<<30); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("an unbounded line: %v", err)
	}
	// A reader that gives a byte at a time, with empty reads between, works.
	if _, err := ReadProbeReport(iotest.OneByteReader(bytes.NewReader(line)), MaxReportLineBytes); err != nil {
		t.Errorf("one byte at a time: %v", err)
	}
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

func TestCheckNoMore(t *testing.T) {
	if err := CheckNoMore(strings.NewReader("")); err != nil {
		t.Errorf("end of file: %v", err)
	}
	for _, e := range []error{os.ErrDeadlineExceeded, syscall.EAGAIN} {
		if err := CheckNoMore(iotest.ErrReader(e)); err != nil {
			t.Errorf("%v: %v", e, err)
		}
	}
	boom := errors.New("boom")
	if err := CheckNoMore(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Errorf("a read error is not a quiet channel: %v", err)
	}
	if err := CheckNoMore(emptyReader{}); !errors.Is(err, io.ErrNoProgress) {
		t.Errorf("a reader that gives nothing: %v", err)
	}
	if err := CheckNoMore(nil); err == nil {
		t.Error("no reader")
	}
	// Trailing bytes are shown escaped and bounded.
	err := CheckNoMore(strings.NewReader("\x1b[31mx\n"))
	if err == nil || !strings.Contains(err.Error(), "7 bytes or more") || strings.Contains(err.Error(), "\x1b") {
		t.Errorf("trailing bytes: %v", err)
	}
	big := CheckNoMore(strings.NewReader(strings.Repeat("a", 3*maxTrailingBytes)))
	if big == nil || !strings.Contains(big.Error(), "4096 bytes or more") || len(big.Error()) > 300 {
		t.Errorf("a flood: %v", big)
	}
}
