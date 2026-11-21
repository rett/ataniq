package idle

import (
	"testing"

	"ataniq/internal/inject/screen"
	"ataniq/internal/inject/title"
	"ataniq/internal/inject/versions"
)

// TestPackagesAgreeOnTestedVersions: the recogniser, the title rules and the
// classifier know the same runtimes and versions, and they are the ones of the
// table in package versions. A version that one of them reads and another does
// not would let a half-tested version through.
func TestPackagesAgreeOnTestedVersions(t *testing.T) {
	runtimes := []string{"claude", "codex", "gemini", ""}
	candidates := []string{
		"", "2.1.324", "2.1.325", "2.1.326", "2.1.330", "2.2.0", "0.168", "0.169", "0.169.0", "0.169.1", "0.170",
	}
	for _, e := range versions.Tested() {
		candidates = append(candidates, e.Spellings()...)
	}
	for _, rt := range runtimes {
		for _, v := range candidates {
			_, listed := versions.Lookup(rt, v)
			_, screenOK := screen.For(rt, v)
			_, titleOK := title.For(rt, v)
			_, idleOK := Lookup(rt, v)
			if screenOK != listed || titleOK != listed || idleOK != listed {
				t.Errorf("%q %q: table %v, screen %v, title %v, idle %v; want all the same", rt, v, listed, screenOK, titleOK, idleOK)
			}
		}
	}
}

// TestTestedIsTheTable: Tested describes the table in package versions.
func TestTestedIsTheTable(t *testing.T) {
	got, want := Tested(), versions.Tested()
	if len(got) != len(want) {
		t.Fatalf("Tested() lists %d entries, the table %d", len(got), len(want))
	}
	for i, e := range want {
		g := got[i]
		if g.Runtime != e.Runtime || g.Version != e.Version || g.App != e.App || len(g.Aliases) != len(e.Aliases) {
			t.Fatalf("Tested()[%d] = %+v, the table has %+v", i, g, e)
		}
		for j := range e.Aliases {
			if g.Aliases[j] != e.Aliases[j] {
				t.Fatalf("Tested()[%d] = %+v, the table has %+v", i, g, e)
			}
		}
	}
}

// TestLookupRefusesUntested: a version that is not in the table has no
// classifier, whatever its neighbours have.
func TestLookupRefusesUntested(t *testing.T) {
	for _, c := range [][2]string{
		{"claude", "2.1.324"}, {"claude", "2.1.326"}, {"claude", "2.1.330"}, {"claude", "2.2.0"},
		{"codex", "0.168"}, {"codex", "0.170"}, {"codex", "0.169.1"}, {"gemini", "2.1.325"}, {"", ""},
	} {
		if _, listed := versions.Lookup(c[0], c[1]); listed {
			t.Fatalf("%s %s is in the table: this test must name versions that are not", c[0], c[1])
		}
		if cl, ok := Lookup(c[0], c[1]); ok || cl.Runtime() != "" {
			t.Errorf("Lookup(%q, %q) found a classifier for an untested version", c[0], c[1])
		}
	}
}
