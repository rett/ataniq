package versions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	for _, g := range [][3]string{{"claude", "2.1.325", "claude-code"}, {"codex", "0.169", "codex"}, {"codex", "0.169.0", "codex"}} {
		e, ok := Lookup(g[0], g[1])
		if !ok || e.Runtime != g[0] || e.App != g[2] {
			t.Errorf("Lookup(%q, %q) = %+v, %v; want the %s entry with app %s", g[0], g[1], e, ok, g[0], g[2])
		}
	}
}

func TestLookupRefusesUntested(t *testing.T) {
	for _, b := range [][2]string{
		{"", ""}, {"claude", ""}, {"", "2.1.325"}, {"claude", "2.1.326"}, {"claude", "2.1.330"}, {"claude", "2.1"},
		{"claude", "2.1.325 "}, {"claude", "0.169"}, {"codex", "2.1.325"}, {"codex", "0.169.1"},
		{"codex", "0.170"}, {"codex", "0.16"}, {"codex", "0.169.00"}, {"gemini", "1"},
		{"Claude", "2.1.325"}, {"claude-code", "2.1.325"}, {"codex", "v0.169"}, {"codex", "0.169."},
	} {
		if e, ok := Lookup(b[0], b[1]); ok {
			t.Errorf("Lookup(%q, %q) found %+v, want it refused", b[0], b[1], e)
		}
	}
}

func TestTested(t *testing.T) {
	got := Tested()
	want := []Entry{
		{Runtime: "claude", Version: "2.1.325", App: "claude-code", Fixtures: "claude-2.1.325", Recordings: recordings},
		{Runtime: "codex", Version: "0.169", Aliases: []string{"0.169.0"}, App: "codex", Fixtures: "codex-0.169", Recordings: recordings},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tested() = %+v, want %+v", got, want)
	}
	// Tested and Lookup describe one table: every spelling is found, and
	// finds its own entry.
	for _, e := range got {
		for _, v := range e.Spellings() {
			f, ok := Lookup(e.Runtime, v)
			if !ok || !reflect.DeepEqual(f, e) {
				t.Errorf("Lookup(%q, %q) = %+v, %v; want %+v", e.Runtime, v, f, ok, e)
			}
		}
	}
}

func TestResultsAreCopies(t *testing.T) {
	got := Tested()
	got[1].Aliases[0] = "changed"
	got[0].Version = "changed"
	got[0].Recordings[0] = "changed"
	if again := Tested(); !reflect.DeepEqual(again[1].Aliases, []string{"0.169.0"}) || again[0].Version != "2.1.325" || again[0].Recordings[0] == "changed" {
		t.Fatalf("Tested() shares memory with the table: %+v", again)
	}
	e, _ := Lookup("codex", "0.169")
	e.Aliases[0] = "changed"
	if _, ok := Lookup("codex", "0.169.0"); !ok {
		t.Fatal("Lookup shares memory with the table")
	}
}

// TestTableIsWellFormed checks what the lookups rely on: a runtime and a
// version are never empty, and no spelling belongs to two entries of a runtime.
func TestTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Tested() {
		if e.Runtime == "" || e.Version == "" || e.App == "" || e.Fixtures == "" {
			t.Errorf("entry %+v has an empty field", e)
		}
		for _, v := range e.Spellings() {
			if v == "" || strings.TrimSpace(v) != v {
				t.Errorf("%s: spelling %q is empty or padded", e.Runtime, v)
			}
			k := e.Runtime + " " + v
			if seen[k] {
				t.Errorf("%s %s is listed twice", e.Runtime, v)
			}
			seen[k] = true
		}
	}
}

// TestFixturesExist checks that every entry's fixture folder and recordings
// are in the repository: a version is listed only with the fixtures it was
// recorded as.
func TestFixturesExist(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, e := range Tested() {
		dir := filepath.Join(root, "internal", "inject", "screen", "testdata", e.Fixtures)
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("%s %s: fixture folder: %v", e.Runtime, e.Version, err)
			continue
		}
		if len(ents) == 0 {
			t.Errorf("%s %s: fixture folder %s is empty", e.Runtime, e.Version, dir)
		}
		if want := e.Runtime + "-" + e.Version; e.Fixtures != want {
			t.Errorf("%s %s: fixture set %q, want the folder name %q the replay tests expect", e.Runtime, e.Version, e.Fixtures, want)
		}
		if len(e.Recordings) == 0 {
			t.Errorf("%s %s: no recordings listed", e.Runtime, e.Version)
		}
		for _, r := range e.Recordings {
			if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(r))); err != nil || fi.IsDir() || fi.Size() == 0 {
				t.Errorf("%s %s: recording %s is not a non-empty file (%v)", e.Runtime, e.Version, r, err)
			}
		}
	}
}

// TestEveryFixtureFolderIsListed checks the other way: a folder of recorded
// screens that no entry names is a recording nothing reads.
func TestEveryFixtureFolderIsListed(t *testing.T) {
	root := filepath.Join("..", "screen", "testdata")
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, e := range Tested() {
		listed[e.Fixtures] = true
	}
	for _, d := range ents {
		if d.IsDir() && !listed[d.Name()] {
			t.Errorf("screen/testdata/%s is a fixture folder no table entry names", d.Name())
		}
	}
}
