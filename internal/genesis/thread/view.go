package thread

import (
	"strings"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
)

// base is the result of a thread with what every result holds: the thread,
// its progress and its catalog; the caller gives the state.
func base(c catalog.Catalog, r record, reset int) ownerwire.SeatGenesisResult {
	filled, total := catalog.Readiness(c, r.brief)
	return ownerwire.SeatGenesisResult{
		Thread: r.id, Of: catalog.MaxRounds, Round: r.brief.Rounds, Nodes: []ownerwire.GenesisNode{},
		Progress: ownerwire.GenesisProgress{Filled: filled, Total: total}, Reset: reset, CatalogDigest: r.catalog,
	}
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
