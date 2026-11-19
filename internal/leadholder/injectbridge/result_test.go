//go:build linux && ataniq_canary

package injectbridge

import (
	"strings"
	"testing"

	"ataniq/internal/inject"
	"ataniq/internal/leadholder/wire"
)

type fencePair struct {
	outcome inject.Outcome
	written inject.Written
}

type wireWordsWant struct {
	outcome wire.InjectOutcome
	reason  wire.InjectReason
	written wire.InjectWritten
}

// The rows of the mapping table, written out here a second time, in the
// wire's own spellings, so that the test does not repeat the function.
var mappingRows = map[fencePair]wireWordsWant{
	{"not idle", "nothing"}:          {"not-now", "not-ready", "nothing"},
	{"blocked", "nothing"}:           {"not-now", "blocked", "nothing"},
	{"untested version", "nothing"}:  {"refused", "untested", "nothing"},
	{"refused text", "nothing"}:      {"refused", "text", "nothing"},
	{"not submitted", "paste"}:       {"uncertain", "not-submitted", "partial"},
	{"submitted", "paste and Enter"}: {"submitted", "", "whole"},
	{"died", "nothing"}:              {"uncertain", "died", "nothing"},
	{"died", "paste"}:                {"uncertain", "died", "partial"},
	{"died", "paste and Enter"}:      {"uncertain", "died", "whole"},
	{"died", "unknown"}:              {"uncertain", "died", "unknown"},
}

// Every outcome and every written of the library, and values it does not
// have: the pairs of all of them are the rows and every contradiction.
var (
	allOutcomes = []inject.Outcome{
		inject.OutcomeNotIdle, inject.OutcomeBlocked, inject.OutcomeUntestedVersion, inject.OutcomeRefusedText,
		inject.OutcomeNotSubmitted, inject.OutcomeSubmitted, inject.OutcomeDied,
		"", "delivered", "Died", "not-now",
	}
	allWritten = []inject.Written{
		inject.WrittenNothing, inject.WrittenPaste, inject.WrittenPasteAndEnter, inject.WrittenUnknown,
		"", "whole", "Nothing",
	}
)

func TestWireResultMapsEveryRowAndEveryContradiction(t *testing.T) {
	rowsSeen := 0
	for _, o := range allOutcomes {
		for _, w := range allWritten {
			r := inject.Result{Outcome: o, Written: w, Detail: "a sample detail"}
			got := WireResult(r)
			if err := got.Validate(); err != nil {
				t.Errorf("%q, %q: the result %+v is not valid: %v", o, w, got, err)
			}
			// The readings hold the detail, so they stand alone.
			wantDetail := wire.SanitizeDetail(r.Readings())
			want, row := mappingRows[fencePair{o, w}]
			if row {
				rowsSeen++
			} else {
				want = wireWordsWant{"uncertain", "died", "unknown"}
				wantDetail = wire.SanitizeDetail("contradictory result: " + r.Readings())
			}
			if got.Outcome != want.outcome || got.Reason != want.reason || got.Written != want.written {
				t.Errorf("%q, %q: mapped to %q, %q, %q, want %q, %q, %q", o, w, got.Outcome, got.Reason, got.Written, want.outcome, want.reason, want.written)
			}
			if got.Detail != wantDetail {
				t.Errorf("%q, %q: detail %q, want %q", o, w, got.Detail, wantDetail)
			}
			if strings.HasPrefix(got.Detail, "contradictory result: ") == row {
				t.Errorf("%q, %q: detail %q, in the table %t", o, w, got.Detail, row)
			}
		}
	}
	if rowsSeen != len(mappingRows) || rowsSeen != 10 {
		t.Errorf("%d rows of the table were mapped, want all %d", rowsSeen, len(mappingRows))
	}
	// The spellings above are the constants': a rename on either side fails.
	for _, pair := range []struct{ got, want string }{
		{string(inject.OutcomeNotIdle), "not idle"}, {string(inject.OutcomeBlocked), "blocked"},
		{string(inject.OutcomeUntestedVersion), "untested version"}, {string(inject.OutcomeRefusedText), "refused text"},
		{string(inject.OutcomeNotSubmitted), "not submitted"}, {string(inject.OutcomeSubmitted), "submitted"},
		{string(inject.OutcomeDied), "died"},
		{string(inject.WrittenNothing), "nothing"}, {string(inject.WrittenPaste), "paste"},
		{string(inject.WrittenPasteAndEnter), "paste and Enter"}, {string(inject.WrittenUnknown), "unknown"},
	} {
		if pair.got != pair.want {
			t.Errorf("the library spells %q as %q", pair.want, pair.got)
		}
	}
}

// The only pair that answers submitted is the one that wrote the paste and
// the Enter, and no pair with something written answers that nothing was.
func TestWireResultNeverSaysMoreThanTheFenceDid(t *testing.T) {
	for _, o := range allOutcomes {
		for _, w := range allWritten {
			got := WireResult(inject.Result{Outcome: o, Written: w})
			if got.Outcome == wire.InjectOutcomeSubmitted && (o != inject.OutcomeSubmitted || w != inject.WrittenPasteAndEnter) {
				t.Errorf("%q, %q answers submitted", o, w)
			}
			if got.Written == wire.InjectWrittenNothing && w != inject.WrittenNothing {
				t.Errorf("%q, %q answers that nothing was written", o, w)
			}
			if (got.Outcome == wire.InjectOutcomeNotNow || got.Outcome == wire.InjectOutcomeRefused) && w != inject.WrittenNothing {
				t.Errorf("%q, %q answers %q, which says nothing was written", o, w, got.Outcome)
			}
		}
	}
}

// The detail is the fence's readings alone when they hold its detail, else
// the detail, " | " and the readings, in printable ASCII and at most the
// wire's length, whatever the fence's words hold.
func TestWireResultDetail(t *testing.T) {
	plain := inject.Result{Outcome: inject.OutcomeNotIdle, Written: inject.WrittenNothing, Detail: "the screen did not settle in time"}
	got := WireResult(plain)
	if want := plain.Readings(); got.Detail != want {
		t.Errorf("detail %q, want %q", got.Detail, want)
	}
	if strings.Count(got.Detail, "the screen did not settle in time") != 1 {
		t.Errorf("detail %q spends the fence's detail twice", got.Detail)
	}
	// Readings that do not hold the detail (here a detail with a byte that
	// the readings replace) get the detail in front, then " | ".
	odd := inject.Result{Outcome: inject.OutcomeNotIdle, Written: inject.WrittenNothing, Detail: "caf\xe9"}
	if got, want := WireResult(odd).Detail, wire.SanitizeDetail("caf\xe9 | "+odd.Readings()); got != want {
		t.Errorf("detail %q, want %q", got, want)
	}
	if !strings.Contains(got.Detail, "before: not read; after: not read") {
		t.Errorf("detail %q does not hold the readings", got.Detail)
	}

	for name, detail := range map[string]string{
		"empty":            "",
		"control and wide": "line one\nline two \x1b[2m 日本 \x00 end",
		"not UTF-8":        "bad \xff\xfe bytes",
		"long":             strings.Repeat("long detail ", 200),
		"long and wide":    strings.Repeat("語", 400),
	} {
		for _, r := range []inject.Result{
			{Outcome: inject.OutcomeDied, Written: inject.WrittenUnknown, Detail: detail},
			{Outcome: inject.Outcome(detail), Written: inject.Written(detail), Detail: detail},
		} {
			got := WireResult(r)
			if err := got.Validate(); err != nil {
				t.Errorf("%s: the result is not valid: %v", name, err)
			}
			if err := wire.CheckInjectDetail(got.Detail); err != nil || len(got.Detail) > wire.MaxInjectDetail {
				t.Errorf("%s: detail of %d bytes: %v", name, len(got.Detail), err)
			}
			if got.Detail == "" {
				t.Errorf("%s: the detail is empty", name)
			}
		}
	}
	got = WireResult(inject.Result{Outcome: inject.OutcomeDied, Written: inject.WrittenPaste, Detail: "a\tb 語"})
	if !strings.HasPrefix(got.Detail, "a?b ??? | died, written paste") {
		t.Errorf("detail %q, want each byte outside printable ASCII as a question mark", got.Detail)
	}
}
