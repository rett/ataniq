//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
)

// limitedEvidence is an evidence that takes the first lines lines of the inner
// one and refuses every one after them.
type limitedEvidence struct {
	inner evidenceAppender
	lines int
	mu    sync.Mutex
	n     int
}

func (l *limitedEvidence) Append(en leadcanary.Entry) error {
	l.mu.Lock()
	l.n++
	ok := l.n <= l.lines
	l.mu.Unlock()
	if !ok {
		return errors.New("the evidence takes no more lines")
	}
	return l.inner.Append(en)
}

// readFixture writes the lines (each ended by a newline) to a file in a private
// folder and reads the epochs they spend back, with the canary's reader and
// with scratch's.
func readFixture(t *testing.T, lines ...string) (canary, scratch map[uint64]bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	if canary, err = spentEpochs(path, canaryRoleText); err != nil {
		t.Fatalf("the canary's reader: %v", err)
	}
	if scratch, err = scratchSpentEpochs(path, canaryRoleText); err != nil {
		t.Fatalf("scratch's reader: %v", err)
	}
	return canary, scratch
}

func sameEpochs(a, b map[uint64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for e := range a {
		if !b[e] {
			return false
		}
	}
	return true
}

// TestRecordBeforeWrite: the inject-sent line is in the evidence before the
// frame is written, a frame is never written when the line is not, the dial's
// failure is a close that frees a lead of scratch mode's message and not the
// canary's, and the effect ids of the evidence are never made again.
func TestRecordBeforeWrite(t *testing.T) {
	sent := func(id, effect string) string {
		more := `,"epoch":1,"message_id":"` + id + `"`
		if effect != "" {
			more += `,"effect_id":"` + effect + `"`
		}
		return evidenceLineOf("inject-sent", canaryRoleText, more)
	}
	unsent := func(id, effect string) string {
		more := `,"epoch":1,"message_id":"` + id + `","code":"unsent"`
		if effect != "" {
			more += `,"effect_id":"` + effect + `"`
		}
		return evidenceLineOf("inject-refused", canaryRoleText, more)
	}
	result := func(id string) string {
		return evidenceLineOf("inject-result", canaryRoleText, `,"epoch":1,"message_id":"`+id+`","outcome":"submitted","written":"whole"`)
	}
	want := func(t *testing.T, got map[uint64]bool, spent bool, what string) {
		t.Helper()
		if got[1] != spent || len(got) > 1 {
			t.Fatalf("%s: spent %v, want epoch 1 spent %v", what, got, spent)
		}
	}

	// A daemon killed after the line, whether or not a frame was written, left
	// no other line: the epoch is spent for both readers.
	t.Run("SentThenNothingIsSpent", func(t *testing.T) {
		c, s := readFixture(t, sent("m-1", "lead-inject-1-1"))
		want(t, c, true, "canary")
		want(t, s, true, "scratch")
		// And after a message that was freed, the open one still spends.
		_, s = readFixture(t, sent("m-1", "lead-inject-1-1"), unsent("m-1", "lead-inject-1-1"), sent("m-2", "lead-inject-1-2"))
		want(t, s, true, "scratch after a freed message and a killed one")
	})

	t.Run("SentThenUnsentScratchFree", func(t *testing.T) {
		_, s := readFixture(t, sent("m-1", "lead-inject-1-1"), unsent("m-1", "lead-inject-1-1"))
		want(t, s, false, "scratch")
		// Several messages in one epoch, each with its own effect id, each
		// freed by its own line.
		_, s = readFixture(t,
			sent("m-1", "lead-inject-1-1"), unsent("m-1", "lead-inject-1-1"),
			sent("m-2", "lead-inject-1-2"), unsent("m-2", "lead-inject-1-2"))
		want(t, s, false, "scratch, two messages")
		// The unsent line closes only the message of its id, and only once.
		_, s = readFixture(t, sent("m-1", "lead-inject-1-1"), unsent("m-2", "lead-inject-1-1"))
		want(t, s, true, "scratch, another message's line")
		_, s = readFixture(t, sent("m-1", "lead-inject-1-1"), unsent("m-1", "lead-inject-1-1"), unsent("m-1", "lead-inject-1-1"))
		want(t, s, true, "scratch, a line twice")
		_, s = readFixture(t, sent("m-1", "lead-inject-1-1"), unsent("m-1", "lead-inject-1-1"), result("m-1"))
		want(t, s, true, "scratch, an unsent line and a result")
	})

	t.Run("SentThenUnsentCanarySpent", func(t *testing.T) {
		c, _ := readFixture(t, sent("m-1", "canary-inject-1-1"), unsent("m-1", "canary-inject-1-1"))
		want(t, c, true, "canary")
	})

	// The ids of a daemon's start continue from the largest counter in the
	// evidence, so that no effect id of the evidence is made again.
	t.Run("DuplicateEffectIDAcrossIncarnations", func(t *testing.T) {
		c := newCanaryEnv(t)
		path := c.cfg.EvidencePath()
		seen := map[string]int{}
		var last uint64
		for incarnation := 1; incarnation <= 4; incarnation++ {
			state, err := readEvidence(path, canaryRoleText, true)
			if err != nil {
				t.Fatalf("incarnation %d: %v", incarnation, err)
			}
			if state.effects != last {
				t.Fatalf("incarnation %d: the evidence's counter is %d, want %d", incarnation, state.effects, last)
			}
			ev, err := leadcanary.OpenEvidence(c.cfg, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			x := &canaryExt{ev: ev, idPrefix: "lead-", logf: func(string, ...any) {}, commands: state.effects}
			// A ticket takes ids that the evidence never shows.
			x.commandID("ticket", 1)
			for i := 0; i < 3; i++ {
				id := x.commandID("inject", 1)
				if prev, dup := seen[id]; dup {
					t.Fatalf("incarnation %d made the id %q that incarnation %d made", incarnation, id, prev)
				}
				seen[id] = incarnation
				if err := x.appendSent(fmt.Sprintf("m-%d-%d", incarnation, i), 1, id); err != nil {
					t.Fatal(err)
				}
				if !x.refuseUnsent(fmt.Sprintf("m-%d-%d", incarnation, i), 1, id) {
					t.Fatal("the unsent line is not written")
				}
			}
			last = x.commands
			if err := ev.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if len(seen) != 12 {
			t.Fatalf("%d distinct ids, want 12", len(seen))
		}
		// Ids of the file are read whatever their prefix and epoch; a member
		// that is no id is no counter.
		state, err := readEvidence(writeLines(t,
			sent("m-1", "canary-inject-7-41"),
			sent("m-2", "x"),
			evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":1,"message_id":"m-3","effect_id":7`),
			evidenceLineOf("inject-sent", "other-role", `,"epoch":1,"message_id":"m-4","effect_id":"lead-inject-1-99"`),
		), canaryRoleText, false)
		if err != nil || state.effects != 41 {
			t.Fatalf("effects %d, err %v", state.effects, err)
		}
	})

	// A file of an older daemon has no effect id and reads as it always did,
	// with or without the ids on the same lines.
	t.Run("OldFormatReadsTheSame", func(t *testing.T) {
		for name, tc := range map[string]struct {
			old, with []string
			canary    bool
			scratch   bool
		}{
			"nothing":              {nil, nil, false, false},
			"sent":                 {[]string{sent("m-1", "")}, []string{sent("m-1", "lead-inject-1-1")}, true, true},
			"sent and result":      {[]string{sent("m-1", ""), result("m-1")}, []string{sent("m-1", "lead-inject-1-1"), result("m-1")}, true, true},
			"sent and holder-busy": {[]string{sent("m-1", ""), refusedLine("m-1", "holder-busy")}, []string{sent("m-1", "lead-inject-1-1"), refusedLine("m-1", "holder-busy")}, true, true},
			"sent and holder-disabled": {
				[]string{sent("m-1", ""), refusedLine("m-1", "holder-disabled")}, []string{sent("m-1", "lead-inject-1-1"), refusedLine("m-1", "holder-disabled")}, true, false,
			},
			// What an older daemon wrote for a dial that failed, which it did
			// before it wrote a sent line, and what a daemon that is older
			// still read of the new unsent line: no close.
			"holder-unreachable alone": {[]string{refusedLine("m-1", "holder-unreachable")}, []string{refusedLine("m-1", "holder-unreachable")}, false, false},
			"sent and holder-unreachable": {
				[]string{sent("m-1", ""), refusedLine("m-1", "holder-unreachable")}, []string{sent("m-1", "lead-inject-1-1"), refusedLine("m-1", "holder-unreachable")}, true, true,
			},
		} {
			t.Run(name, func(t *testing.T) {
				oc, osc := readFixtureOrEmpty(t, tc.old)
				wc, wsc := readFixtureOrEmpty(t, tc.with)
				if !sameEpochs(oc, wc) || !sameEpochs(osc, wsc) {
					t.Fatalf("old: canary %v scratch %v; with ids: canary %v scratch %v", oc, osc, wc, wsc)
				}
				if oc[1] != tc.canary || osc[1] != tc.scratch {
					t.Fatalf("canary spent %v, want %v; scratch spent %v, want %v", oc[1], tc.canary, osc[1], tc.scratch)
				}
				state, err := readEvidence(writeLines(t, tc.old...), canaryRoleText, true)
				if err != nil || state.effects != 0 {
					t.Fatalf("an old file has effects %d (err %v)", state.effects, err)
				}
			})
		}
	})

	// The order is record, connect, frame, wait, and nothing else runs after a
	// record that fails: no frame without the line.
	t.Run("NoFrameAfterAppendFailure", func(t *testing.T) {
		boom := errors.New("boom")
		for name, tc := range map[string]struct {
			fail  string // the step that fails
			stage sendStage
			calls string
		}{
			"nothing fails":      {"", sendWritten, "record connect frame await"},
			"the record fails":   {"record", sendNotRecorded, "record"},
			"the connect fails":  {"connect", sendUnsent, "record connect"},
			"the frame fails":    {"frame", sendFrameFailed, "record connect frame"},
			"the answer is lost": {"await", sendWritten, "record connect frame await"},
		} {
			t.Run(name, func(t *testing.T) {
				var calls []string
				step := func(name string) func() error {
					return func() error {
						calls = append(calls, name)
						if name == tc.fail {
							return boom
						}
						return nil
					}
				}
				st, err := runSend(sendSteps{record: step("record"), connect: step("connect"), frame: step("frame"), await: step("await")})
				if got := strings.Join(calls, " "); got != tc.calls || st != tc.stage {
					t.Fatalf("calls %q stage %v, want %q stage %v", got, st, tc.calls, tc.stage)
				}
				if (tc.fail != "") != errors.Is(err, boom) {
					t.Fatalf("err %v", err)
				}
				if tc.fail == "record" && st.recorded() {
					t.Fatal("a record that failed counts as recorded")
				}
				if tc.fail != "record" && !st.recorded() {
					t.Fatal("a record that worked does not count as recorded")
				}
			})
		}
		// A record that is not there is a send of a request that spends nothing.
		var calls []string
		step := func(name string) func() error { return func() error { calls = append(calls, name); return nil } }
		if st, err := runSend(sendSteps{connect: step("connect"), frame: step("frame"), await: step("await")}); err != nil || st != sendWritten || len(calls) != 3 {
			t.Fatalf("no record: stage %v, err %v, calls %v", st, err, calls)
		}

		// The real call, with a holder folder that is not there: a record that
		// fails is the stage before the dial, which would end as unsent, and a
		// record that works meets the dial's failure.
		dir := filepath.Join(t.TempDir(), "no-such-folder")
		req := wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "msg-one", Text: "body text\n", CommandID: "sample-1"}
		_, st, err := callHolderThen(context.Background(), dir, req, time.Second, time.Second, func() error { return boom })
		if st != sendNotRecorded || !errors.Is(err, boom) {
			t.Fatalf("a record that fails: stage %v, err %v", st, err)
		}
		_, st, err = callHolderThen(context.Background(), dir, req, time.Second, time.Second, func() error { return nil })
		if st != sendUnsent || err == nil {
			t.Fatalf("a record that works: stage %v, err %v", st, err)
		}
		_, st, err = callHolderThen(context.Background(), dir, wire.Request{Op: wire.OpInject}, time.Second, time.Second, func() error {
			t.Error("the record ran for a request that does not encode")
			return nil
		})
		if st != sendNotStarted || err == nil {
			t.Fatalf("a request that does not encode: stage %v, err %v", st, err)
		}

		// And through the extension: an evidence that takes no line gives the
		// answer failed, with a record that was refused and no
		// state spent. The holder's folder is not there, so a frame that was
		// tried would show as holder-unreachable.
		c := newCanaryEnv(t)
		path := c.cfg.EvidencePath()
		inner, err := leadcanary.OpenEvidence(c.cfg, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		defer inner.Close()
		var log strings.Builder
		x := &canaryExt{
			ev: &limitedEvidence{inner: inner, lines: 0}, holderDir: dir, dial: time.Second, wait: time.Second,
			logf: func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) },
		}
		x.spent = map[uint64]bool{}
		_, st, err = callHolderThen(context.Background(), x.holderDir, req, x.dial, x.wait, func() error { return x.appendSent("msg-one", 1, "lead-inject-1-1") })
		if st != sendNotRecorded || err == nil || !strings.Contains(log.String(), "canary evidence: inject-sent: ") {
			t.Fatalf("stage %v, err %v, log %q", st, err, log.String())
		}
		if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
			t.Fatalf("the evidence holds %q (%v)", data, err)
		}
	})
}

func refusedLine(id, code string) string {
	return evidenceLineOf("inject-refused", canaryRoleText, `,"epoch":1,"message_id":"`+id+`","code":"`+code+`"`)
}

func readFixtureOrEmpty(t *testing.T, lines []string) (canary, scratch map[uint64]bool) {
	t.Helper()
	path := writeLines(t, lines...)
	var err error
	if canary, err = spentEpochs(path, canaryRoleText); err != nil {
		t.Fatal(err)
	}
	if scratch, err = scratchSpentEpochs(path, canaryRoleText); err != nil {
		t.Fatal(err)
	}
	return canary, scratch
}

// writeLines writes the lines to a new evidence file in a private folder and
// returns its path.
func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	var data string
	if len(lines) > 0 {
		data = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
