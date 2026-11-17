package idle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
)

// The fixtures are the screen package's recorded screens. This package reads
// them where they are and keeps its own table of what each one must classify
// as; it carries no copy of a screen.
const fixtureRoot = "../screen/testdata"

type spanJSON struct {
	Row  int `json:"row"`
	From int `json:"from"`
	To   int `json:"to"`
}

// fixtureJSON holds the snapshot values of a fixture; the want of the screen
// package is not read.
type fixtureJSON struct {
	Description  string     `json:"description"`
	Cols         int        `json:"cols"`
	Rows         int        `json:"rows"`
	CursorX      int        `json:"cursor_x"`
	CursorY      int        `json:"cursor_y"`
	CursorHidden bool       `json:"cursor_hidden"`
	AltScreen    bool       `json:"alt_screen"`
	Title        string     `json:"title"`
	Faint        []spanJSON `json:"faint"`
}

// recorded is one fixture: a runtime, its version folder, a name and the
// snapshot.
type recorded struct {
	runtime, version, name string
	snap                   screen.Snapshot
}

func loadFixture(t testing.TB, runtime, version, name string) recorded {
	t.Helper()
	base := filepath.Join(fixtureRoot, runtime+"-"+version, name)
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureJSON
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s.json: %v", base, err)
	}
	text, err := os.ReadFile(base + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	s := screen.Snapshot{
		Text: string(text), Cols: f.Cols, Rows: f.Rows, CursorX: f.CursorX, CursorY: f.CursorY,
		CursorHidden: f.CursorHidden, AltScreen: f.AltScreen, Title: f.Title,
	}
	for _, sp := range f.Faint {
		s.Faint = append(s.Faint, screen.Span{Row: sp.Row, From: sp.From, To: sp.To})
	}
	return recorded{runtime: runtime, version: version, name: name, snap: s}
}

// loadAll reads every fixture of every runtime folder, sorted.
func loadAll(t testing.TB) []recorded {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(fixtureRoot, "*-*"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	var all []recorded
	for _, d := range dirs {
		rt, ver, ok := strings.Cut(filepath.Base(d), "-")
		if !ok {
			t.Fatalf("fixture folder %q is not <runtime>-<version>", d)
		}
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		sort.Strings(files)
		for _, jp := range files {
			name := strings.TrimSuffix(filepath.Base(jp), ".json")
			all = append(all, loadFixture(t, rt, ver, name))
		}
	}
	return all
}

// status builds the status of a fresh, readable report.
func status(state osc7501.State, app string) osc7501.Status {
	return osc7501.Status{Report: osc7501.Report{State: state, App: app}, Has: true}
}

// appOf is the app name a runtime's reports carry.
func appOf(runtime string) string {
	if runtime == "claude" {
		return "claude-code"
	}
	return runtime
}

func lookup(t testing.TB, runtime, version string) Classifier {
	t.Helper()
	c, ok := Lookup(runtime, version)
	if !ok {
		t.Fatalf("no classifier for %s %s", runtime, version)
	}
	return c
}

// signal returns the signal of a source, and whether there is one.
func signal(cl Classification, source string) (Signal, bool) {
	for _, s := range cl.Signals {
		if s.Source == source {
			return s, true
		}
	}
	return Signal{}, false
}

func bareScreen(text string, cols, rows int) screen.Snapshot {
	return screen.Snapshot{Text: text, Cols: cols, Rows: rows, AltScreen: true}
}
