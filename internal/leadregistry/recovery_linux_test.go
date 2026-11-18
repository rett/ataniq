package leadregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadrole"
)

// appendRaw appends bytes to the journal of dir, as a write that never
// finished.
func appendRaw(t *testing.T, dir string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryIsJournaledAndSurvivesReopen(t *testing.T) {
	ok := okf(t)
	dir := regDir(t)
	r := openReg(t, dir)
	alpha := lead(t, "alpha")
	ok(r.Define(alpha, ManagerAtaniq, goodProfile()))
	if r.Recovery().Cut() {
		t.Fatal("a fresh journal reports a cut")
	}
	rev, _ := r.Revision()
	size := journalSize(t, dir)
	r.Close()

	tail := []byte(`{"seq":9,"time":"2026`)
	appendRaw(t, dir, tail)
	r = openReg(t, dir)
	want := Recovery{Lines: 1, Bytes: int64(len(tail)), Offset: size}
	if got := r.Recovery(); got != want || !got.Cut() {
		t.Fatalf("Recovery = %+v, want %+v", got, want)
	}
	// The cut is a record, the next in sequence, and the roles are as they were.
	if got, _ := r.Revision(); got != rev+1 {
		t.Fatalf("revision %d, want %d", got, rev+1)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	lines := journalLines(b)
	last := string(lines[len(lines)-1])
	if len(lines) != int(rev)+2 || !strings.Contains(last, `"kind":"recovered"`) || strings.Contains(last, `"role"`) {
		t.Fatalf("last line %q of %d", last, len(lines))
	}
	if e, _ := r.Get(alpha); e.Epoch != 0 {
		t.Fatalf("entry %+v", e)
	}

	// A reopen with nothing unfinished still reports it: it was not
	// acknowledged. It does not change by itself, nor by other writes.
	for i := 0; i < 3; i++ {
		ok(r.SetDesired(alpha, DesiredRunning))
		if got := r.Recovery(); got != want {
			t.Fatalf("Recovery after a write = %+v", got)
		}
		r.Close()
		if got := r.Recovery(); got != want {
			t.Fatalf("Recovery after Close = %+v", got)
		}
		r = openReg(t, dir)
		if got := r.Recovery(); got != want {
			t.Fatalf("round %d: Recovery after reopen = %+v, want %+v", i, got, want)
		}
	}
	revBefore, _ := r.Revision()

	// The acknowledgement clears it, once, and also across reopen.
	got, err := r.AcknowledgeRecovery("every role was looked at by hand")
	if err != nil || got != want {
		t.Fatalf("AcknowledgeRecovery = %+v, %v", got, err)
	}
	if r.Recovery().Cut() || r.Recovery() != (Recovery{}) {
		t.Fatalf("Recovery after the acknowledgement = %+v", r.Recovery())
	}
	if rv, _ := r.Revision(); rv != revBefore+1 {
		t.Fatalf("the acknowledgement is not one record: %d, %d", rv, revBefore)
	}
	b, _ = os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), `"kind":"recovery-acknowledged","payload":{"note":"every role was looked at by hand"}`) {
		t.Fatalf("acknowledgement not journaled:\n%s", b)
	}
	r.Close()
	r = openReg(t, dir)
	if r.Recovery().Cut() {
		t.Fatalf("Recovery after reopen = %+v", r.Recovery())
	}
	want2 := snapshot(t, r)
	// A second cut is a new report, of that cut only.
	r.Close()
	size = journalSize(t, dir)
	appendRaw(t, dir, []byte("{"))
	r = openReg(t, dir)
	if got := r.Recovery(); got != (Recovery{Lines: 1, Bytes: 1, Offset: size}) {
		t.Fatalf("second cut: %+v", got)
	}
	if !sameEntries(snapshot(t, r), want2) {
		t.Fatal("the roles changed with a cut")
	}
}

// TestUnacknowledgedCutsAccumulate: two cuts with no acknowledgement between
// them are both reported, and one acknowledgement clears both.
func TestUnacknowledgedCutsAccumulate(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	okf(t)(r.Define(lead(t, "alpha"), ManagerAtaniq, goodProfile()))
	r.Close()
	appendRaw(t, dir, []byte("abc"))
	openReg(t, dir).Close()
	size := journalSize(t, dir)
	appendRaw(t, dir, []byte("defgh"))
	r = openReg(t, dir)
	if got := r.Recovery(); got != (Recovery{Lines: 2, Bytes: 8, Offset: size}) {
		t.Fatalf("Recovery = %+v", got)
	}
	if got, err := r.AcknowledgeRecovery("both"); err != nil || got.Lines != 2 {
		t.Fatalf("acknowledge: %+v %v", got, err)
	}
	r.Close()
	if openReg(t, dir).Recovery().Cut() {
		t.Fatal("still reported")
	}
}

func TestAcknowledgeRecoveryRefusals(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	okf(t)(r.Define(lead(t, "alpha"), ManagerAtaniq, goodProfile()))
	size := journalSize(t, dir)
	rev, _ := r.Revision()
	// Nothing to acknowledge.
	if _, err := r.AcknowledgeRecovery("note"); !errors.Is(err, ErrNoRecovery) {
		t.Fatalf("no cut: %v", err)
	}
	if _, err := r.AcknowledgeRecovery(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no cut, no note: %v", err)
	}
	r.Close()
	appendRaw(t, dir, []byte("{"))
	r = openReg(t, dir)
	size = journalSize(t, dir)
	rev, _ = r.Revision()
	for name, note := range map[string]string{
		"empty":      "",
		"too long":   strings.Repeat("n", MaxProofBytes+1),
		"newline":    "a\nb",
		"NUL":        "a\x00",
		"not UTF-8":  "\xff",
		"escape":     "\x1b[0m",
		"separators": "a b",
	} {
		if _, err := r.AcknowledgeRecovery(note); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if journalSize(t, dir) != size {
		t.Fatal("a refused acknowledgement wrote")
	}
	if rv, _ := r.Revision(); rv != rev || !r.Recovery().Cut() {
		t.Fatal("a refused acknowledgement changed the registry")
	}
	// The largest note is accepted, and a second acknowledgement is refused.
	if _, err := r.AcknowledgeRecovery(strings.Repeat("n", MaxProofBytes)); err != nil {
		t.Fatalf("longest note: %v", err)
	}
	size = journalSize(t, dir)
	if _, err := r.AcknowledgeRecovery("again"); !errors.Is(err, ErrNoRecovery) {
		t.Fatalf("second acknowledgement: %v", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("a refused acknowledgement wrote")
	}
	r.Close()
	if _, err := r.AcknowledgeRecovery("closed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

// TestRecoveredRecordIsCutToo: a crash while the recovered record is being
// written cuts the new tail again, and the file keeps opening.
func TestRecoveredRecordIsCutToo(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	okf(t)(r.Define(lead(t, "alpha"), ManagerAtaniq, goodProfile()))
	r.Close()
	appendRaw(t, dir, []byte("{"))
	openReg(t, dir).Close()
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	lines := journalLines(data)
	lastStart := len(data) - len(lines[len(lines)-1])
	for cut := lastStart; cut < len(data); cut++ {
		d := writeJournal(t, data[:cut])
		r, err := Open(d, testOpts(1))
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		rec := r.Recovery()
		if cut == lastStart {
			if rec.Cut() {
				t.Fatalf("cut %d on a line boundary reported %+v", cut, rec)
			}
		} else if rec != (Recovery{Lines: 1, Bytes: int64(cut - lastStart), Offset: int64(lastStart)}) {
			t.Fatalf("cut %d: %+v", cut, rec)
		}
		r.Close()
	}
}

func TestRecoveryRecordCorruption(t *testing.T) {
	// A journal with one define, one cut and its record.
	dir := regDir(t)
	r := openReg(t, dir)
	okf(t)(r.Define(lead(t, "alpha"), ManagerAtaniq, goodProfile()))
	r.Close()
	appendRaw(t, dir, []byte("{"))
	openReg(t, dir).Close()
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	lines := journalLines(data)
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	prefix := func(n int) []byte { return bytes.Join(lines[:n], nil) }
	alpha := lead(t, "alpha")
	rec := func(l, b, o int64) *recoveredPayload { return &recoveredPayload{Lines: int(l), Bytes: b, Offset: o} }
	ack := func(note string) *ackPayload { return &ackPayload{Note: note} }
	cases := []struct {
		name string
		data []byte
		line int
		why  string
	}{
		{"cut of no lines", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, rec(0, 5, 10)), 3, "cuts 0 lines"},
		{"cut of two lines", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, rec(2, 5, 10)), 3, "cuts 2 lines"},
		{"cut of no bytes", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, rec(1, 0, 10)), 3, "cuts 0 bytes"},
		{"cut of a negative size", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, rec(1, -3, 10)), 3, "cuts -3 bytes"},
		{"cut over the line bound", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, rec(1, MaxLineBytes+2, 10)), 3, "bytes"},
		{"cut at a negative offset", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, rec(1, 5, -1)), 3, "offset -1"},
		{"recovered record with a role", appendRecord(t, prefix(2), kindRecovered, alpha, rec(1, 5, 10)), 3, "names a role"},
		{"acknowledgement with a role", appendRecord(t, prefix(3), kindRecoveryAck, alpha, ack("n")), 4, "names a role"},
		{"acknowledgement with no cut", appendRecord(t, prefix(2), kindRecoveryAck, leadrole.Role{}, ack("n")), 3, "no recovery to acknowledge"},
		{"second acknowledgement", appendRecord(t, appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, ack("n")), kindRecoveryAck, leadrole.Role{}, ack("n")), 5, "no recovery to acknowledge"},
		{"acknowledgement without a note", appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, ack("")), 4, "recovery note is empty"},
		{"acknowledgement with a long note", appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, ack(strings.Repeat("n", MaxProofBytes+1))), 4, "recovery note is longer"},
		{"acknowledgement with a control character", appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, ack("a\nb")), 4, "control character"},
		{"unknown field in a recovered record", appendRecord(t, prefix(2), kindRecovered, leadrole.Role{}, &struct {
			Lines, Bytes, Offset, Extra int
		}{1, 5, 10, 1}), 3, "bad recovered payload"},
		{"unknown field in an acknowledgement", appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, &struct {
			Note  string `json:"note"`
			Extra int    `json:"extra"`
		}{"n", 1}), 4, "bad recovery-acknowledged payload"},
	}
	for _, tc := range cases {
		err := openErr(t, tc.data)
		var ce *CorruptError
		if !errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) {
			t.Errorf("%s: Open = %v, want ErrCorrupt", tc.name, err)
			continue
		}
		if ce.Line != tc.line || !strings.Contains(ce.Reason, tc.why) {
			t.Errorf("%s: line %d reason %q, want line %d holding %q", tc.name, ce.Line, ce.Reason, tc.line, tc.why)
		}
		for _, other := range append(otherErrors, ErrNoRecovery) {
			if errors.Is(err, other) {
				t.Errorf("%s: Open error also matches %v", tc.name, other)
			}
		}
	}
	// The valid sequences around them open, and report what they say.
	for name, c := range map[string]struct {
		data []byte
		want Recovery
	}{
		"cut, not acknowledged":      {prefix(3), Recovery{Lines: 1, Bytes: 1, Offset: int64(len(prefix(2)))}},
		"cut, acknowledged":          {appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, ack("n")), Recovery{}},
		"cut, acknowledged, cut":     {appendRecord(t, appendRecord(t, prefix(3), kindRecoveryAck, leadrole.Role{}, ack("n")), kindRecovered, leadrole.Role{}, rec(1, 7, 99)), Recovery{Lines: 1, Bytes: 7, Offset: 99}},
		"two cuts":                   {appendRecord(t, prefix(3), kindRecovered, leadrole.Role{}, rec(1, 7, 99)), Recovery{Lines: 2, Bytes: 8, Offset: 99}},
		"two cuts, one for the both": {appendRecord(t, appendRecord(t, prefix(3), kindRecovered, leadrole.Role{}, rec(1, 7, 99)), kindRecoveryAck, leadrole.Role{}, ack("n")), Recovery{}},
	} {
		d := writeJournal(t, c.data)
		r, err := Open(d, testOpts(1))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := r.Recovery(); got != c.want {
			t.Errorf("%s: Recovery = %+v, want %+v", name, got, c.want)
		}
		r.Close()
	}
}

// TestRecoveryOfAJournalCutInTheHeader: a file that holds only half the
// header opens fresh, and says so.
func TestRecoveryOfAJournalCutInTheHeader(t *testing.T) {
	d := writeJournal(t, []byte(`{"seq":0,"time":"2026-01-02`))
	r, err := Open(d, testOpts(1))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Recovery(); got != (Recovery{Lines: 1, Bytes: 27, Offset: 0}) {
		t.Fatalf("Recovery = %+v", got)
	}
	if rev, _ := r.Revision(); rev != 1 {
		t.Fatalf("revision %d", rev)
	}
	r.Close()
	r2 := openReg(t, d)
	if !r2.Recovery().Cut() {
		t.Fatal("not reported after reopen")
	}
	if _, err := r2.AcknowledgeRecovery("fresh"); err != nil {
		t.Fatal(err)
	}
}
