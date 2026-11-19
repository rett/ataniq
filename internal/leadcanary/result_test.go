//go:build linux && ataniq_canary

package leadcanary_test

import (
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
)

// validResults are the rows of the wire's table of valid results.
var validResults = []wire.InjectResult{
	{Outcome: "not-now", Reason: "not-ready", Written: "nothing", Detail: "the agent is busy"},
	{Outcome: "not-now", Reason: "blocked", Written: "nothing"},
	{Outcome: "refused", Reason: "untested", Written: "nothing"},
	{Outcome: "refused", Reason: "text", Written: "nothing", Detail: "text refused"},
	{Outcome: "uncertain", Reason: "not-submitted", Written: "partial"},
	{Outcome: "submitted", Reason: "", Written: "whole", Detail: "reading one | reading two"},
	{Outcome: "uncertain", Reason: "died", Written: "nothing"},
	{Outcome: "uncertain", Reason: "died", Written: "partial"},
	{Outcome: "uncertain", Reason: "died", Written: "whole"},
	{Outcome: "uncertain", Reason: "died", Written: "unknown", Detail: "no answer from the holder"},
}

func TestResultEntryCarriesTheWireFields(t *testing.T) {
	for _, r := range validResults {
		if err := r.Validate(); err != nil {
			t.Fatalf("test row %+v is not a valid result: %v", r, err)
		}
		en := leadcanary.ResultEntry("msg-1", 3, r)
		want := leadcanary.Entry{
			Event: leadcanary.EventInjectResult, Epoch: 3, MessageID: "msg-1",
			Outcome: string(r.Outcome), Reason: string(r.Reason), Written: string(r.Written), Detail: r.Detail,
		}
		if en != want {
			t.Errorf("entry %+v, want %+v", en, want)
		}
	}
}

func TestJudgeEveryResultIsSpentAndOnlyUncertainOnesAreUncertain(t *testing.T) {
	for _, r := range validResults {
		spent, uncertain := leadcanary.Judge(r)
		if !spent {
			t.Errorf("%+v: not spent", r)
		}
		if want := r.Outcome == wire.InjectOutcomeUncertain; uncertain != want {
			t.Errorf("%+v: uncertain %v, want %v", r, uncertain, want)
		}
	}
}

func TestJudgeDoesNotBelieveAResultTheWireWouldRefuse(t *testing.T) {
	invalid := []wire.InjectResult{
		{},
		{Outcome: "delivered", Written: "whole"},
		{Outcome: "submitted", Reason: "died", Written: "whole"},
		{Outcome: "submitted", Written: "nothing"},
		{Outcome: "not-now", Reason: "not-ready", Written: "whole"},
		{Outcome: "uncertain", Reason: "died", Written: "some"},
		{Outcome: "refused", Reason: "text", Written: "nothing", Detail: "bad \x1b detail"},
		{Outcome: "refused", Reason: "text", Written: "nothing", Detail: strings.Repeat("a", 513)},
	}
	for _, r := range invalid {
		if r.Validate() == nil {
			t.Fatalf("test row %+v is valid", r)
		}
		spent, uncertain := leadcanary.Judge(r)
		if !spent || !uncertain {
			t.Errorf("%+v: spent %v uncertain %v, want both", r, spent, uncertain)
		}
		en := leadcanary.ResultEntry("msg-1", 1, r)
		if en.Outcome != "uncertain" || en.Reason != "died" || en.Written != "unknown" {
			t.Errorf("%+v: entered as %+v", r, en)
		}
		if strings.Contains(en.Detail, "bad") || strings.Contains(en.Detail, "aaaa") {
			t.Errorf("%+v: the detail of an invalid result was kept: %q", r, en.Detail)
		}
		if en.Detail == "" {
			t.Errorf("%+v: no detail says why", r)
		}
	}
}

func TestNoAnswerResult(t *testing.T) {
	r := leadcanary.NoAnswerResult()
	want := wire.InjectResult{Outcome: "uncertain", Reason: "died", Written: "unknown", Detail: "no answer from the holder"}
	if r != want {
		t.Fatalf("got %+v, want %+v", r, want)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("the stand-in result is not valid: %v", err)
	}
	spent, uncertain := leadcanary.Judge(r)
	if !spent || !uncertain {
		t.Fatalf("spent %v uncertain %v", spent, uncertain)
	}
	en := leadcanary.ResultEntry("msg-1", 2, r)
	if en.Event != leadcanary.EventInjectResult || en.Detail != "no answer from the holder" {
		t.Fatalf("entry %+v", en)
	}
}
