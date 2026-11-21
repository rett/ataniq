package screen

import (
	"testing"

	"ataniq/internal/inject/versions"
)

// TestForFollowsTable: For finds a recogniser for exactly the versions of the
// table in package versions, under each of their spellings.
func TestForFollowsTable(t *testing.T) {
	for _, e := range versions.Tested() {
		for _, v := range e.Spellings() {
			if rec, ok := For(e.Runtime, v); !ok || rec == nil {
				t.Errorf("For(%q, %q) = %v, %v; the table lists it", e.Runtime, v, rec, ok)
			}
		}
	}
}

// TestForRefusesUntested: a version that is not in the table has no
// recogniser, whatever its neighbours have.
func TestForRefusesUntested(t *testing.T) {
	for _, c := range [][2]string{
		{"claude", "2.1.324"}, {"claude", "2.1.326"}, {"claude", "2.1.330"}, {"claude", "2.2.0"},
		{"codex", "0.168"}, {"codex", "0.170"}, {"codex", "0.169.1"}, {"gemini", "2.1.325"}, {"", ""},
	} {
		if _, listed := versions.Lookup(c[0], c[1]); listed {
			t.Fatalf("%s %s is in the table: this test must name versions that are not", c[0], c[1])
		}
		if rec, ok := For(c[0], c[1]); ok || rec != nil {
			t.Errorf("For(%q, %q) found a recogniser for an untested version", c[0], c[1])
		}
	}
}
