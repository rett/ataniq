//go:build linux && ataniq_canary

package leadcanary_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
)

var errInjected = errors.New("injected failure")

// failNextWrite makes the next write put the first keep bytes of the line in
// the file and then report what report says for the count put. Later writes
// are the file's own.
func failNextWrite(ev *leadcanary.Evidence, keep int, report func(n int) (int, error)) {
	failed := false
	ev.WrapWrite(func(real func([]byte) (int, error)) func([]byte) (int, error) {
		return func(b []byte) (int, error) {
			if failed {
				return real(b)
			}
			failed = true
			n, _ := real(b[:keep])
			return report(n)
		}
	})
}

func withError(err error) func(int) (int, error) {
	return func(n int) (int, error) { return n, err }
}

func (e *evFixture) raw(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// rows splits the file into its rows (the file must end in a newline) and
// reports for each whether it is a whole JSON object.
func rows(t *testing.T, data []byte) (all []string, whole []bool) {
	t.Helper()
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("the file does not end in a newline: %q", data)
	}
	for _, r := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var m map[string]any
		all = append(all, r)
		whole = append(whole, json.Unmarshal([]byte(r), &m) == nil)
	}
	return all, whole
}

func TestEvidenceWithoutAFailureHasNoBlankLine(t *testing.T) {
	e := newEvidence(t)
	for i := 0; i < 3; i++ {
		if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
			t.Fatal(err)
		}
	}
	data := e.raw(t)
	if bytes.Contains(data, []byte("\n\n")) || bytes.HasPrefix(data, []byte("\n")) {
		t.Fatalf("blank line in %q", data)
	}
}

// A write that leaves part of a line is ended by a newline before the next
// line, so each later line is whole and on a line of its own.
func TestEvidenceRecoversAfterAPartlyWrittenLine(t *testing.T) {
	for name, report := range map[string]func(int) (int, error){
		"error with a short count": withError(errInjected),
		"short count, no error":    func(n int) (int, error) { return n, nil },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEvidence(t)
			if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
				t.Fatal(err)
			}
			failNextWrite(e.ev, 20, report)
			err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventStop})
			if err == nil {
				t.Fatal("a failed write was reported as done")
			}
			if name == "error with a short count" && !errors.Is(err, errInjected) {
				t.Fatalf("got %v, want the write error", err)
			}
			for _, ev := range []leadcanary.Event{leadcanary.EventDefine, leadcanary.EventStop} {
				if err := e.ev.Append(leadcanary.Entry{Event: ev}); err != nil {
					t.Fatal(err)
				}
			}
			all, whole := rows(t, e.raw(t))
			// first, fragment, define, stop: only the fragment is not whole,
			// and it is exactly the 20 bytes that were written.
			want := []bool{true, false, true, true}
			if len(all) != len(want) {
				t.Fatalf("%d rows, want %d: %q", len(all), len(want), all)
			}
			for i := range want {
				if whole[i] != want[i] {
					t.Fatalf("row %d whole=%v, want %v: %q", i, whole[i], want[i], all)
				}
			}
			if len(all[1]) != 20 {
				t.Fatalf("fragment %q", all[1])
			}
		})
	}
}

// A write that fails with nothing written still leaves the next line whole;
// the newline before it makes a blank row, which the reader of the file skips.
func TestEvidenceRecoversAfterAWriteThatWroteNothing(t *testing.T) {
	e := newEvidence(t)
	failNextWrite(e.ev, 0, withError(errInjected))
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventStop}); !errors.Is(err, errInjected) {
		t.Fatalf("got %v, want the write error", err)
	}
	for i := 0; i < 2; i++ {
		if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
			t.Fatal(err)
		}
	}
	data := e.raw(t)
	if !bytes.HasPrefix(data, []byte("\n{")) || bytes.Contains(data[1:], []byte("\n\n")) {
		t.Fatalf("got %q: want one newline, then the lines, with no more blank row", data)
	}
	if got := e.lines(t); len(got) != 2 {
		t.Fatalf("%d lines, want 2", len(got))
	}
}

// A failed sync is returned, and the line it follows is whole in the file and
// is not followed by a recovery newline.
func TestEvidenceReturnsAFailedSync(t *testing.T) {
	e := newEvidence(t)
	failed := false
	e.ev.WrapSync(func(real func() error) func() error {
		return func() error {
			if !failed {
				failed = true
				return errInjected
			}
			return real()
		}
	})
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventStop}); !errors.Is(err, errInjected) {
		t.Fatalf("got %v, want the sync error", err)
	}
	if err := e.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
		t.Fatal(err)
	}
	data := e.raw(t)
	if bytes.Contains(data, []byte("\n\n")) {
		t.Fatalf("blank row in %q", data)
	}
	if got := e.lines(t); len(got) != 2 {
		t.Fatalf("%d lines, want 2", len(got))
	}
}
