package inject

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
)

// invariants are what must hold after every scenario, whatever the scripted
// events did. residual is true only for the one window design 4.6 leaves open
// (the agent changes after the last reading), where an approval is possible.
type invariants struct {
	// deadline is the time after which nothing may be written, zero for none.
	deadline time.Time
	// residual allows one approval recorded.
	residual bool
}

func checkInvariants(t *testing.T, f *fake, m Message, res Result, inv invariants) {
	t.Helper()
	paste := pasteBytes(m)

	// 1. The writes are a prefix of [paste, Enter], each its own write, and
	// nothing else is ever written.
	want := []string{paste, "\r"}
	if len(f.writes) > 2 {
		t.Fatalf("%d writes were attempted, at most two are allowed", len(f.writes))
	}
	for i, w := range f.writes {
		if w.data != want[i] {
			t.Errorf("write %d is %q, want %q", i, w.data, want[i])
		}
		if !w.frozen {
			t.Errorf("write %d was made outside Frozen", i)
		}
		if !inv.deadline.IsZero() && !w.at.Before(inv.deadline) {
			t.Errorf("write %d was made at %s, after the deadline %s", i, w.at, inv.deadline)
		}
	}

	// 2. The writes that reached the agent are the fence's, each once, with
	// the paste byte-exact, and the writer's held keys come after all of
	// them.
	lastFence := -1
	for i, e := range f.agentLog {
		if strings.HasPrefix(e, "paste:") || e == "enter" {
			lastFence = i
			if strings.HasPrefix(e, "paste:") && e != "paste:"+strings.ReplaceAll(m.Text, "\n", "\r") {
				t.Errorf("the paste that reached the agent is %q", e)
			}
		}
	}
	for i, e := range f.agentLog {
		if strings.HasPrefix(e, "keys:") && i < lastFence {
			t.Errorf("writer keys %q reached the agent before a write of the fence (log %q)", e, f.agentLog)
		}
	}

	// 3. Enter only after a passed second check: what the emulator showed
	// when the Enter was written is an idle composer holding the header or
	// the marker, with no dialog, no frame, no stale or blocked report.
	if len(f.writes) == 2 {
		e := f.writes[1].atWrite
		rec, ok := screen.For(f.rt, f.version)
		if !ok {
			t.Fatalf("no recogniser for %s %s", f.rt, f.version)
		}
		rd := rec.Read(e.snap)
		switch {
		case f.writes[1].ended:
			t.Errorf("Enter was written to an ended session")
		case rd.Dialog || !rd.Composer:
			t.Errorf("Enter was written with no composer on screen (dialog %t)", rd.Dialog)
		case !rd.Holds(m.Header) && !rd.PastedMarker:
			t.Errorf("Enter was written while the composer held neither the header nor a marker")
		case !e.term.ParserGround || e.term.FrameOpen || !e.term.BracketedPaste:
			t.Errorf("Enter was written with the terminal in state %+v", e.term)
		case e.status.Stale || e.status.Report.State == osc7501.StateBlocked:
			t.Errorf("Enter was written with the report %+v", e.status)
		}
		if !f.writes[0].ok {
			t.Errorf("Enter was attempted after the paste write failed")
		}
	}

	// 4. No approval, and no key ever written into a dialog.
	if f.approvals > 0 && !(inv.residual && f.approvals == 1) {
		t.Errorf("%d approvals were recorded", f.approvals)
	}
	if len(f.dialogKeys) > 0 {
		t.Errorf("keys reached an open dialog: %q", f.dialogKeys)
	}

	// 5. Written matches the write log, and the pair is one the command
	// machine accepts. Written is exact for every scenario whose writes all
	// succeeded; it is unknown only when a write or the session failed (the
	// outcome is then died).
	var attempted, okCount int
	for _, w := range f.writes {
		attempted++
		if w.ok {
			okCount++
		}
	}
	switch {
	case attempted == 0:
		if res.Written != WrittenNothing {
			t.Errorf("no write was attempted but Written is %q", res.Written)
		}
	case attempted == 1 && okCount == 1:
		if res.Written != WrittenPaste && res.Written != WrittenUnknown {
			t.Errorf("only the paste was written but Written is %q", res.Written)
		}
		if res.Outcome != OutcomeDied && res.Written != WrittenPaste {
			t.Errorf("only the paste was written, nothing failed, but Written is %q", res.Written)
		}
	case attempted == 1 && okCount == 0:
		if res.Written != WrittenUnknown {
			t.Errorf("the paste write failed but Written is %q", res.Written)
		}
	case attempted == 2 && okCount == 2:
		if res.Written != WrittenPasteAndEnter && res.Written != WrittenUnknown {
			t.Errorf("paste and Enter were written but Written is %q", res.Written)
		}
		if res.Outcome != OutcomeDied && res.Written != WrittenPasteAndEnter {
			t.Errorf("paste and Enter were written, nothing failed, but Written is %q", res.Written)
		}
	case attempted == 2:
		if res.Written != WrittenUnknown {
			t.Errorf("the Enter write failed but Written is %q", res.Written)
		}
	}
	if !validPair(res) {
		t.Errorf("Written %q with outcome %q is not a pair the fence may return", res.Written, res.Outcome)
	}
	if res.Outcome == OutcomeSubmitted && (len(f.writes) != 2 || res.Written != WrittenPasteAndEnter) {
		t.Errorf("submitted without paste and Enter written")
	}

	// 6. The lock was released, the result carries its readings, and the
	// readings are bounded.
	if f.locked {
		t.Errorf("the input lock was not released")
	}
	if line := res.Readings(); len(line) > 1024 {
		t.Errorf("Readings is %d bytes", len(line))
	}
	if f.settles > 2 || f.frozens > 2 || f.lockCalls > 1 {
		t.Errorf("settles %d frozens %d locks %d", f.settles, f.frozens, f.lockCalls)
	}
}

// validPair lists the (Written, Outcome) pairs Deliver may return. Each is
// one the command machine reads as it should: no pair here makes it record an
// inconsistent result.
func validPair(r Result) bool {
	switch r.Written {
	case WrittenNothing:
		switch r.Outcome {
		case OutcomeNotIdle, OutcomeBlocked, OutcomeUntestedVersion, OutcomeRefusedText, OutcomeDied:
			return true
		}
	case WrittenPaste:
		return r.Outcome == OutcomeNotSubmitted
	case WrittenPasteAndEnter:
		return r.Outcome == OutcomeSubmitted
	case WrittenUnknown:
		return r.Outcome == OutcomeDied
	}
	return false
}
