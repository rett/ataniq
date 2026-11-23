package catalog

import (
	"sort"

	"ataniq/internal/genesis/brief"
)

// Item is one field on the summary.
type Item struct {
	// Field is the field's name.
	Field string
	// Node is the id of the node that set it.
	Node string
	// Prompt is that node's question, or "" when the catalog does not hold
	// the node.
	Prompt string
	// Value is the field's value. Free text in it is [brief.Untrusted].
	Value brief.Value
	// Labels are the labels of the value's choices, in the value's order;
	// none for free text.
	Labels []string
	// Confirmed is set on an inferred field the owner confirmed.
	Confirmed bool
}

// Groups is the summary of a brief, grouped by provenance.
type Groups struct {
	// YouChose are the owner-chosen fields.
	YouChose []Item
	// DecidedForYou are the supervisor-inferred fields.
	DecidedForYou []Item
	// Defaults are the fields left to their default.
	Defaults []Item
	// Missing are the required fields of the brief's kind not set yet.
	Missing []string
}

// Summary groups the brief's fields by provenance (you chose, decided for
// you, defaults), each group in the catalog's order of weight, and lists
// the required fields still missing.
func Summary(c Catalog, b brief.Brief) (Groups, error) {
	if err := checkCatalog(c, b); err != nil {
		return Groups{}, err
	}
	names := make([]string, 0, len(b.Fields))
	for name := range b.Fields {
		names = append(names, name)
	}
	weight := func(name string) int {
		if i, ok := c.index[b.Fields[name].Node]; ok {
			return c.Nodes[i].Weight
		}
		return 0
	}
	sort.Slice(names, func(i, j int) bool {
		wi, wj := weight(names[i]), weight(names[j])
		if wi != wj {
			return wi > wj
		}
		return names[i] < names[j]
	})
	var g Groups
	for _, name := range names {
		f := b.Fields[name]
		it := Item{Field: name, Node: f.Node, Value: f.Value, Confirmed: f.Confirmed}
		if n, ok := c.Node(f.Node); ok {
			it.Prompt = n.Prompt
			it.Labels = n.labels(f.Value)
		}
		switch f.Provenance {
		case brief.OwnerChosen:
			g.YouChose = append(g.YouChose, it)
		case brief.SupervisorInferred:
			g.DecidedForYou = append(g.DecidedForYou, it)
		default:
			g.Defaults = append(g.Defaults, it)
		}
	}
	for _, f := range Required(c, b) {
		if _, ok := b.Get(f); !ok {
			g.Missing = append(g.Missing, f)
		}
	}
	return g, nil
}

// labels returns the labels of the value's choices; a choice that is not
// an option (a step of a scale) stands for itself.
func (n Node) labels(v brief.Value) []string {
	var ids []string
	switch v.Kind() {
	case brief.KindOne:
		ids = []string{v.One()}
	case brief.KindMany:
		ids = v.Many()
	default:
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id
		for _, o := range n.Options {
			if o.ID == id {
				out[i] = o.Label
			}
		}
	}
	return out
}
