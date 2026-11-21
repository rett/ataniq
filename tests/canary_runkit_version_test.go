package tests

// This file ties the run kit and its documentation to the one table of the
// versions the injection fence is tested with (internal/inject/versions). The
// run script stays shell: the tests read the script and the table and compare
// them. Nothing here runs the script or starts anything.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/inject/versions"
)

var (
	prepareRuntimeVersionLine = regexp.MustCompile(`(?m)^RUNTIME_VERSION=(\S*)$`)
	prepareScreensLine        = regexp.MustCompile(`(?m)^SCREENS=\$REPO/internal/inject/screen/testdata/(\S+)$`)
)

// prepareScript returns the text of canary-prepare.sh.
func prepareScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/canary-prepare.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestCanaryRunKitVersionIsTested: the version canary-prepare.sh declares for
// the claude the run uses is one the table lists, the recorded screens it
// copies into the replay folder are that entry's fixture folder, and the
// script names the version nowhere else.
func TestCanaryRunKitVersionIsTested(t *testing.T) {
	s := prepareScript(t)
	m := prepareRuntimeVersionLine.FindAllStringSubmatch(s, -1)
	if len(m) != 1 {
		t.Fatalf("canary-prepare.sh has %d RUNTIME_VERSION= lines, want one", len(m))
	}
	version := m[0][1]
	e, ok := versions.Lookup("claude", version)
	if !ok {
		t.Fatalf("canary-prepare.sh declares RUNTIME_VERSION=%s, which the table in internal/inject/versions does not list for claude: the fence refuses it as untested", version)
	}
	if version != e.Version {
		t.Errorf("RUNTIME_VERSION=%s is an alias, want the table's own spelling %s", version, e.Version)
	}
	sm := prepareScreensLine.FindAllStringSubmatch(s, -1)
	if len(sm) != 1 {
		t.Fatalf("canary-prepare.sh has %d SCREENS= lines under internal/inject/screen/testdata, want one", len(sm))
	}
	if sm[0][1] != e.Fixtures {
		t.Errorf("SCREENS names the folder %s, the table's fixture set for claude %s is %s", sm[0][1], e.Version, e.Fixtures)
	}
	// No other line of the script names a tested version or its folder.
	rest := prepareRuntimeVersionLine.ReplaceAllString(s, "")
	rest = prepareScreensLine.ReplaceAllString(rest, "")
	for _, te := range versions.Tested() {
		for _, spelling := range append(te.Spellings(), te.Fixtures) {
			re := regexp.MustCompile(`(^|[^0-9A-Za-z.-])` + regexp.QuoteMeta(spelling) + `([^0-9A-Za-z-]|\.?$|\.[^0-9])`)
			if re.MatchString(rest) {
				t.Errorf("canary-prepare.sh names %s outside RUNTIME_VERSION and SCREENS: it must take it from there", spelling)
			}
		}
	}
	// The tests of pass B declare the same version, which the fake claude of
	// those tests prints.
	if passBVersion != version {
		t.Errorf("passBVersion is %s, canary-prepare.sh declares %s", passBVersion, version)
	}
}

// TestCanaryRunKitDocExamplesUseTestedVersions: where a configuration example
// in the run kit's guide or in the guides of the canary slice and of the
// scratch leads names a runtime the fence knows (claude or codex) with its
// runtime_version, that version is one the table lists, so the example is not
// a configuration the fence refuses as untested. An example that does not name
// a runtime key ("<a runtime key the registry accepts for a lead>") is a
// placeholder and is not read.
func TestCanaryRunKitDocExamplesUseTestedVersions(t *testing.T) {
	re := regexp.MustCompile(`"runtime":\s*"(claude|codex)",\s*"runtime_version":\s*"([^"]*)"`)
	found := 0
	for _, f := range []string{"docs/canary-slice.md", "docs/scratch-leads.md", "qualification/canary/run/README.md"} {
		raw, err := os.ReadFile(filepath.Join(root(t), f))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			found++
			if _, ok := versions.Lookup(m[1], m[2]); !ok {
				t.Errorf("%s: an example gives runtime %s version %s, which the table in internal/inject/versions does not list", f, m[1], m[2])
			}
		}
	}
	if found == 0 {
		t.Error("no configuration example with a runtime and a runtime_version was found: the pattern of this test is out of date")
	}
}

// TestVersionDocTableMatchesTable: the table of tested versions in
// docs/lead-injection.md has one row for each entry of the table in
// internal/inject/versions, with its aliases and its app name, and no other.
func TestVersionDocTableMatchesTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root(t), "docs/lead-injection.md"))
	if err != nil {
		t.Fatal(err)
	}
	sec := string(raw)
	i := strings.Index(sec, "\n## The tested table\n")
	if i < 0 {
		t.Fatal("docs/lead-injection.md has no section \"The tested table\"")
	}
	sec = sec[i+1:]
	if j := strings.Index(sec[3:], "\n## "); j >= 0 {
		sec = sec[:j+3]
	}
	var rows []string
	for _, line := range strings.Split(sec, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| Runtime") && !strings.HasPrefix(line, "| ---") {
			rows = append(rows, line)
		}
	}
	tested := versions.Tested()
	if len(rows) != len(tested) {
		t.Fatalf("the table in docs/lead-injection.md has %d rows, the table in internal/inject/versions %d entries", len(rows), len(tested))
	}
	for i, e := range tested {
		want := "| " + e.Runtime + " | " + e.Version + " | " + strings.TrimSpace(strings.Join(e.Aliases, ", ")) + " | `" + e.App + "` |"
		want = strings.Replace(want, " |  | ", " | | ", 1) // an empty cell has one blank, as the table is written
		if rows[i] != want {
			t.Errorf("row %d of the table in docs/lead-injection.md is %q, want %q", i+1, rows[i], want)
		}
	}
}
