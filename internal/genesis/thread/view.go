package thread

import (
	"strings"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/plan"
)

// base is the result of a thread with what every result holds: the thread,
// its progress and its catalog; the caller gives the state. The progress
// counts the required answers filled for planning ([catalog.PlanReadiness]:
// an answer decided for the owner counts, confirmed or not), the notion
// plan_ready follows, so a page never says answers are missing while a plan
// can be drawn, or the reverse.
func base(c catalog.Catalog, r record, reset int) ownerwire.SeatGenesisResult {
	filled, total := catalog.PlanReadiness(c, r.brief)
	return ownerwire.SeatGenesisResult{
		Thread: r.id, Of: catalog.MaxRounds, Round: r.brief.Rounds, Nodes: []ownerwire.GenesisNode{},
		Progress: ownerwire.GenesisProgress{Filled: filled, Total: total, PlanReady: planReady(r, filled, total)},
		Reset:    reset, CatalogDigest: r.catalog,
	}
}

// planReady reports whether the verb plan answers a plan for the thread: it
// has one stored, or its brief is complete for planning (filled and total
// are its [catalog.PlanReadiness]) and a template serves its kind.
func planReady(r record, filled, total int) bool {
	if r.plan != nil {
		return true
	}
	if r.state == StateDiscarded || filled < total || r.brief.Kind == "" {
		return false
	}
	ok, err := hasTemplate(r.brief.Kind)
	return err == nil && ok
}

// planView is the wire's result for a thread's plan: the plan, its digest, and
// whether it is the plan stored for the thread or a preview.
func planView(c catalog.Catalog, r record, p plan.Plan, digest string, stored bool) (ownerwire.SeatGenesisResult, error) {
	res := base(c, r, 0)
	w := wirePlan(p)
	res.State, res.Plan, res.PlanDigest, res.Stored = ownerwire.GenesisStatePlan, &w, digest, stored
	return res, nil
}

// wirePlan is the plan as the wire carries it: the record, member by member.
// The wire writes it to the record's own canonical bytes, and refuses it when
// they are not those of the digest the result names.
func wirePlan(p plan.Plan) ownerwire.GenesisPlan {
	out := ownerwire.GenesisPlan{
		Schema: p.Schema, Revision: p.Revision, PrevDigest: p.PrevDigest, BriefDigest: p.BriefDigest, CatalogDigest: p.CatalogDigest,
		Template:       ownerwire.GenesisPlanTemplate{ID: p.Template.ID, Version: p.Template.Version},
		TemplateDigest: p.TemplateDigest, Title: p.Title, Milestones: []ownerwire.GenesisPlanMilestone{},
		Gates: append([]string{}, p.Gates...), FirstDemo: p.FirstDemo, ReviewPoints: append([]string{}, p.ReviewPoints...),
		Hosts: p.Hosts, ExternalServices: append([]string{}, p.ExternalServices...),
		BudgetBand: ownerwire.GenesisPlanBudget{ID: p.BudgetBand, Enforcement: plan.Enforcement},
		Estimate:   ownerwire.GenesisPlanEstimate{Band: p.Estimate.Band, Label: p.Estimate.Label, Estimate: true, Caveat: plan.EstimateCaveat},
		Warnings:   append([]string{}, p.Warnings...), Notes: []ownerwire.GenesisPlanNote{}, NotesDropped: p.NotesDropped,
	}
	for _, m := range p.Milestones {
		wm := ownerwire.GenesisPlanMilestone{ID: m.ID, Title: m.Title, Goal: m.Goal, Tasks: []ownerwire.GenesisPlanTask{}}
		for _, t := range m.Tasks {
			wm.Tasks = append(wm.Tasks, ownerwire.GenesisPlanTask{ID: t.ID, Title: t.Title, Kind: string(t.Kind), Size: string(t.Size),
				DependsOn: append([]string{}, t.DependsOn...), Scope: append([]string{}, t.Scope...), Checks: append([]string{}, t.Checks...)})
		}
		out.Milestones = append(out.Milestones, wm)
	}
	for _, n := range p.Notes {
		out.Notes = append(out.Notes, ownerwire.GenesisPlanNote{Field: n.Field, Text: string(n.Text), Untrusted: true})
	}
	return out
}

// view is the wire's result for a thread as it stands: the next round of an
// open thread, or its summary when the walk is done; the summary and the digest
// of a finished one. reset is how many later answers the call that led here
// cleared.
func view(c catalog.Catalog, r record, reset int) (ownerwire.SeatGenesisResult, error) {
	res := base(c, r, reset)
	switch r.state {
	case StateDiscarded:
		res.State, res.Round, res.Reset, res.Progress = ownerwire.GenesisStateDiscarded, 0, 0, ownerwire.GenesisProgress{}
		return res, nil
	case StateOpen:
		round, done, err := catalog.Next(c, r.brief, catalog.Limits{})
		if err != nil {
			return ownerwire.SeatGenesisResult{}, err
		}
		if !done {
			res.State, res.Round = ownerwire.GenesisStateQuestion, round.Number
			for _, n := range round.Nodes {
				res.Nodes = append(res.Nodes, wireNode(n))
			}
			return res, nil
		}
	}
	return summaryView(c, r, res)
}

// summaryView completes res with the summary of the thread's brief, and, for a
// finished thread, its digest.
func summaryView(c catalog.Catalog, r record, res ownerwire.SeatGenesisResult) (ownerwire.SeatGenesisResult, error) {
	g, err := catalog.Summary(c, r.brief)
	if err != nil {
		return ownerwire.SeatGenesisResult{}, err
	}
	res.State = ownerwire.GenesisStateSummary
	if r.state == StateFinished {
		res.State, res.Digest, res.Reset = ownerwire.GenesisStateFinished, r.frozen, 0
	}
	res.Round = r.brief.Rounds
	res.Groups = &ownerwire.GenesisGroups{YouChose: wireRows(g.YouChose), DecidedForYou: wireRows(g.DecidedForYou), Defaults: wireRows(g.Defaults)}
	return res, nil
}

// wireNode is a question as the result words it: the catalog's texts.
func wireNode(n catalog.Node) ownerwire.GenesisNode {
	out := ownerwire.GenesisNode{
		ID: n.ID, Type: string(n.Type), Prompt: n.Prompt, Topic: n.Topic, Options: []ownerwire.GenesisOption{},
		AllowOther: n.AllowOther, AllowYouDecide: n.AllowYouDecide, CanSkip: n.Default != "",
	}
	for _, o := range n.Options {
		out.Options = append(out.Options, ownerwire.GenesisOption{ID: o.ID, Label: o.Label, Example: o.Example})
	}
	return out
}

// wireRows are the rows of a group of the summary: the labels of the chosen
// options, joined, or the owner's own text, marked untrusted.
func wireRows(items []catalog.Item) []ownerwire.GenesisRow {
	out := []ownerwire.GenesisRow{}
	for _, it := range items {
		row := ownerwire.GenesisRow{Node: it.Node, Field: it.Field, Label: it.Prompt}
		if text, ok := it.Value.Other(); ok {
			row.Value, row.Untrusted = string(text), true
		} else {
			row.Value = strings.Join(it.Labels, ", ")
		}
		out = append(out, row)
	}
	return out
}

// walkerAnswer turns the wire's kind of answer and its members into the
// walker's, and reports false for a form the wire does not hold (the gate's
// decoder has refused it before: this is the store's own check).
func walkerAnswer(kind string, ids []string, text string, round int) (catalog.Answer, bool) {
	a := catalog.Answer{Round: round}
	switch kind {
	case ownerwire.GenesisKindChoice:
		if len(ids) != 1 || text != "" {
			return a, false
		}
		a.Choice = ids[0]
	case ownerwire.GenesisKindChoices:
		if len(ids) == 0 || text != "" {
			return a, false
		}
		a.Choices = append([]string{}, ids...)
	case ownerwire.GenesisKindOther:
		if ids != nil || text == "" {
			return a, false
		}
		a.Other = brief.Untrusted(text)
	case ownerwire.GenesisKindYouDecide:
		a.YouDecide = ids == nil && text == ""
		return a, a.YouDecide
	case ownerwire.GenesisKindSkip:
		a.Skip = ids == nil && text == ""
		return a, a.Skip
	default:
		return a, false
	}
	return a, true
}
