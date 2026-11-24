package plan

import (
	"errors"
	"testing"
	"time"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/internal/canon"
)

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantCode(t testing.TB, err error, code brief.Code) {
	t.Helper()
	got, ok := brief.CodeOf(err)
	if !ok || got != code {
		t.Fatalf("got %v, want the code %s", err, code)
	}
	var e *brief.Error
	if !errors.As(err, &e) || e.Detail == "" || !canon.Plain(e.Detail) {
		t.Fatalf("the refusal has no plain detail: %v", err)
	}
}

func v1(t testing.TB) catalog.Catalog {
	t.Helper()
	c, err := catalog.V1()
	must(t, err)
	return c
}

// Style is how the walk answers a question it has no answer for.
type style int

const (
	// defaults skips a question that has a default, and picks the first
	// option of one that has none.
	defaults style = iota
	// youDecide leaves the choice to the host where the question allows it,
	// and otherwise answers as defaults does. The walk then confirms what
	// was inferred, as the owner does on the summary.
	youDecide
	// youDecideUnconfirmed is youDecide without the confirmation.
	youDecideUnconfirmed
)

func pickOne(id string) catalog.Answer      { return catalog.Answer{Choice: id} }
func pickMany(ids ...string) catalog.Answer { return catalog.Answer{Choices: ids} }
func pickOther(s string) catalog.Answer     { return catalog.Answer{Other: brief.Untrusted(s)} }

func fallback(n catalog.Node, s style) catalog.Answer {
	if s != defaults && n.AllowYouDecide {
		return catalog.Answer{YouDecide: true}
	}
	if n.Default != "" {
		return catalog.Answer{Skip: true}
	}
	if n.Type == catalog.Multi {
		return pickMany(n.Options[0].ID)
	}
	return pickOne(n.Options[0].ID)
}

// walk runs a whole interview through the catalog's walker: each round the
// walker picks, each question answered from picks by node id, or as the
// style says. The brief is built only by catalog.Apply, as the thread
// store builds it.
func walk(t testing.TB, c catalog.Catalog, picks map[string]catalog.Answer, s style) brief.Brief {
	t.Helper()
	b := brief.New(c.Digest())
	for i := 0; ; i++ {
		if i > catalog.MaxRounds {
			t.Fatal("the walk does not end")
		}
		r, done, err := catalog.Next(c, b, catalog.Limits{})
		must(t, err)
		if done {
			break
		}
		for _, n := range r.Nodes {
			a, ok := picks[n.ID]
			if !ok {
				a = fallback(n, s)
			}
			a.Round = r.Number
			b, _, err = catalog.Apply(c, b, n.ID, a, t0.Add(time.Duration(i)*time.Minute))
			if err != nil {
				t.Fatalf("%s: %v", n.ID, err)
			}
		}
	}
	if s == youDecide {
		for name, f := range b.Fields {
			if f.Provenance == brief.SupervisorInferred {
				must(t, b.Confirm(name))
			}
		}
	}
	return b
}

func compile(t testing.TB, c catalog.Catalog, b brief.Brief) Plan {
	t.Helper()
	p, err := Compile(b, c, t0)
	must(t, err)
	return p
}

func encode(t testing.TB, p Plan) []byte {
	t.Helper()
	out, err := Encode(p)
	must(t, err)
	return out
}

func titles(p Plan) map[string]Task {
	out := map[string]Task{}
	for _, t := range p.Tasks() {
		out[t.Title] = t
	}
	return out
}
