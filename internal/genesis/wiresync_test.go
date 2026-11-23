package genesis

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
)

// The wire (package ownerwire) cannot import the brief or the catalog, so it
// holds their grammars and caps as forms of its own. These tests hold the two
// in step, and run the whole of catalog v1 through the wire's forms.

// The caps of the wire are the caps of the catalog and the brief.
func TestTheWireCapsAreTheCatalogsAndTheBriefs(t *testing.T) {
	for _, c := range []struct {
		name       string
		wire, host int
	}{
		{"rounds", ownerwire.MaxGenesisRounds, catalog.MaxRounds},
		{"questions to a round", ownerwire.MaxGenesisNodes, catalog.MaxPerRound},
		{"answers to a round", ownerwire.MaxGenesisAnswers, catalog.MaxPerRound},
		{"options", ownerwire.MaxGenesisOptions, catalog.MaxOptions},
		{"choices", ownerwire.MaxGenesisOptions, brief.MaxChoices},
		{"prompt", ownerwire.MaxGenesisPrompt, catalog.MaxPrompt},
		{"label", ownerwire.MaxGenesisLabel, catalog.MaxLabel},
		{"example", ownerwire.MaxGenesisExample, catalog.MaxExample},
		{"other text", ownerwire.MaxGenesisOther, brief.MaxFreeText},
		{"summary rows", ownerwire.MaxGenesisRows, brief.MaxFields},
		{"choice id", ownerwire.MaxGenesisChoiceBytes, brief.MaxChoiceLen},
	} {
		if c.wire != c.host {
			t.Errorf("%s: the wire holds %d, the host %d", c.name, c.wire, c.host)
		}
	}
}

// answerOf is the wire's params for one answer, as the daemon would read them.
func answerParams(node, kind string, ids []string, text string) ownerwire.SeatGenesisParams {
	return ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbAnswer, Thread: "0123456789abcdef", Round: 1,
		Answers: []ownerwire.GenesisAnswer{{Node: node, Kind: kind, IDs: ids, Text: text}}}
}

// The wire accepts a node id, a choice id and a field name exactly when the
// brief does, over a corpus of the shapes that differ.
func TestTheWireGrammarsAreTheBriefs(t *testing.T) {
	corpus := []string{"", "a", "goal", "Goal", "go-al", "go_al", "go.al", "1goal", "_goal", "-goal", "a b", "aé", "ab.", ".ab", "a..b",
		"media.format.podcast", "software.stack", "software.", "software.1a", "software.Stack", "music.key", "kind", "review_points",
		strings.Repeat("a", 31), strings.Repeat("a", 32), strings.Repeat("a", 33), strings.Repeat("a", 40), strings.Repeat("a", 41),
		strings.Repeat("a", 42), strings.Repeat("a", 47), strings.Repeat("a", 48), strings.Repeat("a", 49),
		"software." + strings.Repeat("a", 32), "software." + strings.Repeat("a", 33), "1", "5", "yes", "no", "9a", "a9", "a-", "a_"}
	for _, s := range corpus {
		// A node id.
		_, err := ownerwire.EncodeSeatGenesisParams(answerParams(s, ownerwire.GenesisKindSkip, nil, ""))
		if (err == nil) != brief.ValidNodeID(s) {
			t.Errorf("node id %q: the wire accepts %v, the brief %v", s, err == nil, brief.ValidNodeID(s))
		}
		// A choice id.
		_, err = ownerwire.EncodeSeatGenesisParams(answerParams("goal", ownerwire.GenesisKindChoice, []string{s}, ""))
		if (err == nil) != brief.ValidChoice(s) {
			t.Errorf("choice id %q: the wire accepts %v, the brief %v", s, err == nil, brief.ValidChoice(s))
		}
		// A field name, in a row of a summary.
		r := ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateSummary, Thread: "0123456789abcdef", Of: 6, Nodes: []ownerwire.GenesisNode{},
			CatalogDigest: strings.Repeat("ab", 32), Groups: &ownerwire.GenesisGroups{
				YouChose: []ownerwire.GenesisRow{{Node: "goal", Field: s, Label: "l", Value: "v"}}, DecidedForYou: []ownerwire.GenesisRow{}, Defaults: []ownerwire.GenesisRow{}}}
		_, err = ownerwire.EncodeSeatGenesisResult(r)
		if (err == nil) != brief.ValidFieldName(s) {
			t.Errorf("field name %q: the wire accepts %v, the brief %v", s, err == nil, brief.ValidFieldName(s))
		}
	}
}

// wireNode is a question as the host would put it in a result.
func wireNode(n catalog.Node) ownerwire.GenesisNode {
	out := ownerwire.GenesisNode{ID: n.ID, Type: string(n.Type), Prompt: n.Prompt, Topic: n.Topic, Options: []ownerwire.GenesisOption{},
		AllowOther: n.AllowOther, AllowYouDecide: n.AllowYouDecide, CanSkip: n.Default != ""}
	for _, o := range n.Options {
		out.Options = append(out.Options, ownerwire.GenesisOption{ID: o.ID, Label: o.Label, Example: o.Example})
	}
	return out
}

// wireRow is a summary item as the host would put it in a result: the labels
// of the chosen options, or the owner's own text, marked untrusted.
func wireRow(it catalog.Item) ownerwire.GenesisRow {
	row := ownerwire.GenesisRow{Node: it.Node, Field: it.Field, Label: it.Prompt}
	if text, ok := it.Value.Other(); ok {
		row.Value, row.Untrusted = string(text), true
	} else {
		row.Value = strings.Join(it.Labels, ", ")
	}
	return row
}

func wireRows(items []catalog.Item) []ownerwire.GenesisRow {
	out := []ownerwire.GenesisRow{}
	for _, it := range items {
		out = append(out, wireRow(it))
	}
	return out
}

// Every question the catalog asks, in every walk, is a result the wire's form
// accepts, and so is the summary of every walk. A walk answers each question
// one way: with the first option, with all options, with the longest text the
// question takes, or by leaving it to the host.
func TestEveryWalkOfCatalogV1IsAResultTheWireAccepts(t *testing.T) {
	c, err := catalog.V1()
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("word ", ownerwire.MaxGenesisOther/5)
	if len(long) != ownerwire.MaxGenesisOther {
		t.Fatalf("the long text is %d bytes", len(long))
	}
	first := func(n catalog.Node) catalog.Answer {
		switch n.Type {
		case catalog.Scale:
			return catalog.Answer{Choice: "3"}
		case catalog.Multi:
			return catalog.Answer{Choices: []string{n.Options[0].ID}}
		}
		return catalog.Answer{Choice: n.Options[0].ID}
	}
	ways := map[string]func(n catalog.Node) catalog.Answer{
		"first option": first,
		"all options": func(n catalog.Node) catalog.Answer {
			if n.Type == catalog.Multi {
				var ids []string
				for _, o := range n.Options {
					ids = append(ids, o.ID)
				}
				return catalog.Answer{Choices: ids}
			}
			if n.Type == catalog.Scale {
				return catalog.Answer{Choice: "5"}
			}
			return catalog.Answer{Choice: n.Options[len(n.Options)-1].ID}
		},
		"long text": func(n catalog.Node) catalog.Answer {
			if n.AllowOther {
				return catalog.Answer{Other: brief.Untrusted(long)}
			}
			return first(n)
		},
		"you decide, then skip": func(n catalog.Node) catalog.Answer {
			switch {
			case n.AllowYouDecide:
				return catalog.Answer{YouDecide: true}
			case n.Default != "":
				return catalog.Answer{Skip: true}
			}
			return first(n)
		},
	}
	// The kind node starts each walk: each of its options, and other text.
	kindNode, _ := c.Node(brief.FieldKind)
	var starts []catalog.Answer
	for _, o := range kindNode.Options {
		starts = append(starts, catalog.Answer{Choice: o.ID})
	}
	starts = append(starts, catalog.Answer{Other: "a quilt club newsletter"})
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	largest := 0
	walks := 0
	for name, way := range ways {
		for i, start := range starts {
			b := brief.New(c.Digest())
			b, _, err := catalog.Apply(c, b, kindNode.ID, withRound(start, 1), at)
			if err != nil {
				t.Fatalf("%s %d: %v", name, i, err)
			}
			for {
				r, done, err := catalog.Next(c, b, catalog.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				filled, total := catalog.Readiness(c, b)
				res := ownerwire.SeatGenesisResult{Thread: "0123456789abcdef", Of: catalog.MaxRounds, Nodes: []ownerwire.GenesisNode{},
					Progress: ownerwire.GenesisProgress{Filled: filled, Total: total}, CatalogDigest: c.Digest(), Round: b.Rounds}
				if done {
					g, err := catalog.Summary(c, b)
					if err != nil {
						t.Fatal(err)
					}
					res.State = ownerwire.GenesisStateSummary
					res.Groups = &ownerwire.GenesisGroups{YouChose: wireRows(g.YouChose), DecidedForYou: wireRows(g.DecidedForYou), Defaults: wireRows(g.Defaults)}
				} else {
					res.State, res.Round = ownerwire.GenesisStateQuestion, r.Number
					for _, n := range r.Nodes {
						res.Nodes = append(res.Nodes, wireNode(n))
					}
				}
				out, err := ownerwire.EncodeSeatGenesisResult(res)
				if err != nil {
					t.Fatalf("%s, start %d, round %d (%s): %v", name, i, b.Rounds+1, res.State, err)
				}
				if _, err := ownerwire.DecodeSeatGenesisResult(out); err != nil {
					t.Fatalf("%s, start %d: the result does not decode: %v", name, i, err)
				}
				if len(out) > largest {
					largest = len(out)
				}
				walks++
				if done {
					break
				}
				for _, n := range r.Nodes {
					a := way(n)
					a.Round = r.Number
					if b, _, err = catalog.Apply(c, b, n.ID, a, at); err != nil {
						t.Fatalf("%s, start %d: the answer to %s: %v", name, i, n.ID, err)
					}
				}
			}
		}
	}
	t.Logf("%d results of catalog v1 passed the wire, the largest %d bytes of %d", walks, largest, ownerwire.MaxSeatGenesisResultBytes)
}

func withRound(a catalog.Answer, n int) catalog.Answer { a.Round = n; return a }

// Every answer the wire's form can hold for a question is the walker's: the
// wire's kinds are the walker's five, and an answer built from them is read
// by the walker as it was meant.
func TestTheWireAnswersAreTheWalkers(t *testing.T) {
	c, err := catalog.V1()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	goal, _ := c.Node("goal")
	multi, _ := c.Node("formats")
	kind, _ := c.Node("kind")
	if len(goal.Options) < 2 || len(multi.Options) < 2 || !kind.AllowOther {
		t.Fatalf("the nodes of the test are not what it expects")
	}
	base := brief.New(c.Digest())
	base, _, err = catalog.Apply(c, base, "kind", catalog.Answer{Round: 1, Choice: kind.Options[0].ID}, at)
	if err != nil {
		t.Fatal(err)
	}
	walkerAnswer := func(a ownerwire.GenesisAnswer, round int) catalog.Answer {
		out := catalog.Answer{Round: round}
		switch a.Kind {
		case ownerwire.GenesisKindChoice:
			out.Choice = a.IDs[0]
		case ownerwire.GenesisKindChoices:
			out.Choices = a.IDs
		case ownerwire.GenesisKindOther:
			out.Other = brief.Untrusted(a.Text)
		case ownerwire.GenesisKindYouDecide:
			out.YouDecide = true
		case ownerwire.GenesisKindSkip:
			out.Skip = true
		}
		return out
	}
	for name, a := range map[string]ownerwire.GenesisAnswer{
		"a choice":   {Node: "goal", Kind: ownerwire.GenesisKindChoice, IDs: []string{goal.Options[0].ID}},
		"choices":    {Node: "formats", Kind: ownerwire.GenesisKindChoices, IDs: []string{multi.Options[1].ID, multi.Options[0].ID}},
		"other":      {Node: "goal", Kind: ownerwire.GenesisKindOther, Text: "a recipe box for the street"},
		"you decide": {Node: "hosts", Kind: ownerwire.GenesisKindYouDecide},
		"skip":       {Node: "schedule", Kind: ownerwire.GenesisKindSkip},
	} {
		// The wire holds it...
		p := ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbAnswer, Thread: "0123456789abcdef", Round: 2, Answers: []ownerwire.GenesisAnswer{a}}
		out, err := ownerwire.EncodeSeatGenesisParams(p)
		if err != nil {
			t.Errorf("%s: the wire refuses it: %v", name, err)
			continue
		}
		back, err := ownerwire.DecodeSeatGenesisParams(out)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		// ...and the walker takes it, or refuses it for a reason of the question.
		_, _, err = catalog.Apply(c, base, back.Answers[0].Node, walkerAnswer(back.Answers[0], back.Round), at)
		if code, ok := brief.CodeOf(err); err != nil && (!ok || gateWord[code] == "") {
			t.Errorf("%s: the walker refuses it with %v, which is not a word of the gate", name, err)
		}
	}
	// The wire's text is held to the walker's: what the walker calls too long
	// the wire does not carry at all, and what looks like a key is the walker's
	// to refuse, with the word the gate has for it.
	secret := brief.Untrusted("my key " + "AKI" + "A" + "IOSFODNN7" + "EXAMPLE")
	if _, err := ownerwire.EncodeSeatGenesisParams(answerParams("goal", ownerwire.GenesisKindOther, nil, string(secret))); err != nil {
		t.Errorf("the wire refuses key-shaped text, which is the host's to refuse: %v", err)
	}
	_, _, err = catalog.Apply(c, base, "kind", catalog.Answer{Round: 2, Other: secret}, at)
	if code, ok := brief.CodeOf(err); !ok || gateWord[code] != ownerwire.CodeGenesisSecretShaped {
		t.Errorf("the walker refuses key-shaped text with %v, want the word %s", err, ownerwire.CodeGenesisSecretShaped)
	}
}
