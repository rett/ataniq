//go:build linux && ataniq_canary

package main

// This is the one file of the canary end-to-end tests that names a fixture
// path, and so a runtime folder (the plan's rule M8 and risk R6). The
// injection library keeps one folder of recorded screens for each tested
// pair, named <runtime>-<version>. The accepting-path test replays two of them,
// the idle screen and the screen after a collapsed paste, through the stand-in.
// Nothing else of the feature names a runtime, a version, a footer mode or an
// application: the pair and what goes with it come from here.
//
// When the injection library moves its fixtures, or a pair is added to its
// table, this file is the one line of change.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/inject/idle"
)

// fixtureRoot is the injection library's own screen folder.
const fixtureRoot = "../../internal/inject/screen/testdata"

// The recorded screens the replay uses: the idle screen before a paste, and
// the screen after a collapsed paste. Of the three recordings of a collapsed
// paste the third is the one: the first two carry a window title that the
// capture damaged (the injection library's own tests say so, and give them a
// repaired title in their replay), and that title reads as a busy agent, so the
// fence answers "not submitted, state working" on them. The third has its real
// title. A fixture is never edited to make it pass.
const (
	idleScreenName   = "idle-before-paste"
	pastedScreenName = "collapsed-paste-footer-c"
)

// testedPair is a runtime and version of the injection library's tested table
// that has both recorded screens, with what the replay of those screens needs.
type testedPair struct {
	// Runtime and Version are the pair, as the holder is started with them.
	Runtime, Version string
	// Mode is the footer mode the recorded idle screen shows, which is what the
	// holder is told to expect.
	Mode string
	// Cols and Rows are the size the screens were recorded at.
	Cols, Rows int
	// Report is the payload of the idle report an agent of the pair sends.
	Report string

	dir string
}

// pickTestedPair returns the first pair of the tested table whose folder holds
// both recorded screens and whose idle screen says its footer mode.
func pickTestedPair(t testing.TB) testedPair {
	t.Helper()
	for _, e := range idle.Tested() {
		dir := filepath.Join(fixtureRoot, e.Runtime+"-"+e.Version)
		var one struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
			Want struct {
				Mode string `json:"mode"`
			} `json:"want"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, idleScreenName+".json"))
		if err != nil || json.Unmarshal(raw, &one) != nil || one.Want.Mode == "" {
			continue
		}
		ok := true
		for _, f := range []string{idleScreenName + ".txt", pastedScreenName + ".json", pastedScreenName + ".txt"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				ok = false
			}
		}
		if !ok {
			continue
		}
		return testedPair{
			Runtime: e.Runtime, Version: e.Version, Mode: one.Want.Mode, Cols: one.Cols, Rows: one.Rows,
			Report: "state=idle:app=" + e.App, dir: dir,
		}
	}
	t.Fatalf("no pair of the tested table has the recorded screens %s and %s under %s", idleScreenName, pastedScreenName, fixtureRoot)
	return testedPair{}
}

// copyScreens copies the idle screen as 1.json and 1.txt, and the screen after
// the paste as 2.json and 2.txt, into the replay folder, and writes the idle
// report payload as report.txt there.
func (p testedPair) copyScreens(t testing.TB, replayDir string) {
	t.Helper()
	for to, from := range map[string]string{"1": idleScreenName, "2": pastedScreenName} {
		for _, ext := range []string{".json", ".txt"} {
			b, err := os.ReadFile(filepath.Join(p.dir, from+ext))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(replayDir, to+ext), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(replayDir, "report.txt"), []byte(p.Report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pastedText returns the text of the screen after the paste, for a test that
// looks at what the holder's pane shows at the end.
func (p testedPair) pastedText(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.dir, pastedScreenName+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(string(b), "\n")
}
