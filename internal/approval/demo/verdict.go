package demo

import (
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

// verdictSamples returns the samples that carry verdicts: two decided cards,
// one where both runs agreed with the owner and one where they did not. The
// provider and model names are neutral strings.
func verdictSamples(now time.Time) []sample {
	agreed := build(now, verdictSpec("verdict-agreed", "agreed-change", 3*time.Hour))
	agreed.state, agreed.line = record.StateApproved, lineApproved
	agreed.verdicts = verdictBlock(true, true)

	differed := build(now, verdictSpec("verdict-differed", "differed-change", 4*time.Hour))
	differed.state, differed.line = record.StateApproved, lineApproved
	differed.verdicts = verdictBlock(false, true)
	return []sample{agreed, differed}
}

// verdictSpec is a small decided change for a verdict sample.
func verdictSpec(slug, feature string, ago time.Duration) spec {
	return spec{
		slug: slug, project: projectB, role: "role-b", feature: feature,
		askedAgo: ago,
		paths:    []record.PathChange{change("lib/verdict_demo.go", "M", 5, 2)},
		note:     "A decided sample that carries the advice of two verdict runs.",
	}
}

// verdictBlock makes the advice of two runs: the first run approves or
// rejects as first says, the second as second says.
func verdictBlock(first, second bool) *card.VerdictBlock {
	checks := func(ok bool) []card.CheckResult {
		return []card.CheckResult{
			{Kind: card.CheckPlan, Pass: ok}, {Kind: card.CheckScope, Pass: ok},
			{Kind: card.CheckFlagged, Pass: true}, {Kind: card.CheckSuite, Pass: true},
		}
	}
	return &card.VerdictBlock{
		Runs: [2]card.Run{
			{ID: "run-sample-1", Provider: "provider-a", Model: "model-a", Approve: first, Checks: checks(first)},
			{ID: "run-sample-2", Provider: "provider-b", Model: "model-b", Approve: second, Checks: checks(second)},
		},
	}
}
