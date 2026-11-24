package thread

import (
	"reflect"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/catalog"
)

// firstAnswer answers a question with its first option.
func firstAnswer(n ownerwire.GenesisNode) ownerwire.GenesisAnswer {
	if n.Type == ownerwire.GenesisTypeMulti {
		return choices(n.ID, n.Options[0].ID)
	}
	return choice(n.ID, n.Options[0].ID)
}

// answerRound answers every question of a question result with its first
// option, in the result's round, and returns the result after.
func (w *world) answerRound(id string, res ownerwire.SeatGenesisResult) ownerwire.SeatGenesisResult {
	w.t.Helper()
	if res.State != ownerwire.GenesisStateQuestion {
		w.t.Fatalf("not a round: %+v", res)
	}
	var as []ownerwire.GenesisAnswer
	for _, n := range res.Nodes {
		as = append(as, firstAnswer(n))
	}
	return w.ok(w.s.Answer(ctx, id, res.Round, as))
}

// A change after the last round that clears answers asks their questions
// again, in rounds that keep the last round's number (the wire's six rounds
// stay its six), until the brief is complete; finishing then draws the plan.
// A round asked again and answered is stale when its post comes again, and a
// seventh round stays refused.
func TestAChangeAfterTheLastRoundIsAskedAgain(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	sum := w.interview(id, 1)
	if sum.State != ownerwire.GenesisStateSummary || sum.Round != catalog.MaxRounds || !sum.Progress.PlanReady {
		t.Fatalf("the summary: %+v", sum)
	}

	// The sub-kind changes: the four answers of the build are cleared and
	// asked again in round 6.
	res := w.ok(w.s.Change(ctx, id, "software.sub_kind", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"web_tool"}}))
	if res.Reset != 4 || res.State != ownerwire.GenesisStateQuestion || res.Round != 6 || res.Of != 6 ||
		res.Progress.Filled != 10 || res.Progress.Total != 14 || res.Progress.PlanReady ||
		!reflect.DeepEqual(nodesOf(res), []string{"software.stack", "software.data", "software.auth"}) {
		t.Fatalf("the change: %+v", res)
	}
	round := []ownerwire.GenesisAnswer{choice("software.stack", "go"), choice("software.data", "none"), choice("software.auth", "none")}
	next := w.ok(w.s.Answer(ctx, id, 6, round))
	if next.State != ownerwire.GenesisStateQuestion || next.Round != 6 || !reflect.DeepEqual(nodesOf(next), []string{"software.deploy_kind"}) {
		t.Fatalf("the round after: %+v", next)
	}
	// The same post again is the round already answered, not a misuse; the
	// question of the original sixth round too.
	frozen := w.bytesOf(id)
	_, err := w.s.Answer(ctx, id, 6, round)
	refused(t, err, ownerwire.CodeGenesisStaleRound)
	_, err = w.s.Answer(ctx, id, 6, webApp[5])
	refused(t, err, ownerwire.CodeGenesisStaleRound)
	// There is no seventh round, and a question of no round is still a bad
	// answer.
	_, err = w.s.Answer(ctx, id, 7, []ownerwire.GenesisAnswer{choice("software.deploy_kind", "local")})
	refused(t, err, ownerwire.CodeGenesisBadAnswer)
	_, err = w.s.Answer(ctx, id, 6, []ownerwire.GenesisAnswer{choice("goal", "share")})
	refused(t, err, ownerwire.CodeGenesisBadAnswer)
	if string(w.bytesOf(id)) != string(frozen) {
		t.Fatal("a refused answer changed the thread")
	}
	done := w.ok(w.s.Answer(ctx, id, 6, []ownerwire.GenesisAnswer{choice("software.deploy_kind", "local")}))
	if done.State != ownerwire.GenesisStateSummary || done.Round != 6 || done.Progress.Filled != 14 || !done.Progress.PlanReady {
		t.Fatalf("the summary after the round asked again: %+v", done)
	}
	fin := w.ok(w.s.Finish(ctx, id))
	if fin.State != ownerwire.GenesisStateFinished || !fin.Progress.PlanReady {
		t.Fatalf("finish: %+v", fin)
	}
	if p := w.ok(w.s.Plan(ctx, id)); !p.Stored || p.Plan == nil {
		t.Fatalf("the plan at finish: %+v", p)
	}
}

// A change of the kind after the last round moves the brief to the other
// kind; its questions are asked in rounds that keep the last round's number,
// a question that depends on another in a later one, until it is complete.
func TestAChangeOfKindAfterTheLastRoundAsksTheNewKind(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	w.interview(id, 1)
	res := w.ok(w.s.Change(ctx, id, "kind", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"publishing"}}))
	want := [][]string{
		{"publishing.sub_kind"},
		{"publishing.medium", "publishing.editorial_steps", "publishing.style_guide"},
		{"publishing.channel"},
	}
	for i, nodes := range want {
		if res.State != ownerwire.GenesisStateQuestion || res.Round != 6 || !reflect.DeepEqual(nodesOf(res), nodes) {
			t.Fatalf("round asked again %d: %+v, want %v", i+1, res, nodes)
		}
		res = w.answerRound(id, res)
	}
	if res.State != ownerwire.GenesisStateSummary || res.Progress.Filled != res.Progress.Total || res.Progress.Total != 14 || !res.Progress.PlanReady {
		t.Fatalf("the summary: %+v", res)
	}
}

// Without a change, the walk ends after the sixth round whatever is left:
// three questions left unanswered on the way are one more than the rounds
// have room for, and the summary says how many answers are filled in.
func TestWithoutAChangeTheWalkEndsAfterTheLastRound(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	res := w.ok(w.s.Round(ctx, id))
	blank := map[string]bool{"goal": true, "software.auth": true, "budget_band": true}
	for res.State == ownerwire.GenesisStateQuestion {
		var as []ownerwire.GenesisAnswer
		for _, n := range res.Nodes {
			if blank[n.ID] {
				delete(blank, n.ID)
				continue
			}
			as = append(as, firstAnswer(n))
		}
		res = w.ok(w.s.Answer(ctx, id, res.Round, as))
	}
	if res.State != ownerwire.GenesisStateSummary || res.Round != 6 || res.Progress.Filled != 13 || res.Progress.PlanReady {
		t.Fatalf("the summary: %+v", res)
	}
}
