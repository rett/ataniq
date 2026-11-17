//go:build linux && amd64

package harness_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"ataniq/qualification/leadsandbox/internal/harness"
)

const manualPath = "../../../../docs/qualification-lead-sandbox.md"

func manual(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(manualPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The configuration in the manual is read by the strict decoder, so the manual
// cannot drift from the keys the harness accepts. The example's paths are
// illustrative and need not exist: loading is lexical.
func TestManualExampleIsAValidConfiguration(t *testing.T) {
	m := manual(t)
	re := regexp.MustCompile("(?s)```json\n(.*?)\n```")
	found := re.FindStringSubmatch(m)
	if found == nil {
		t.Fatal("the manual has no json example")
	}
	cfg, err := harness.ParseConfig([]byte(found[1]))
	if err != nil {
		t.Fatalf("the manual's example is refused: %v", err)
	}
	if !cfg.CoordinatorReviewed || len(cfg.Cases) != len(harness.AutomatedCases) {
		t.Errorf("example: %+v", cfg)
	}
	for _, p := range []string{cfg.Campaign, cfg.State.Root, cfg.Executables.Pasta, cfg.Sources.AgentInstall} {
		if !strings.HasPrefix(p, "/owned/") {
			t.Errorf("%s is not an illustrative path", p)
		}
	}
	// The scratch command of the example is the default one.
	if got := strings.Join(cfg.Command(), " "); got != strings.Join(harness.DefaultScratchCommand(), " ") {
		t.Errorf("scratch command %q", got)
	}
}

// Every case of the design's table and every evidence file has its place in
// the manual, and the keys it describes are the ones the harness reads.
func TestManualCoversEveryCaseAndEvidenceFile(t *testing.T) {
	m := manual(t)
	for n := 1; n <= 20; n++ {
		if !strings.Contains(m, fmt.Sprintf("| S%d |", n)) {
			t.Errorf("case S%d has no row in the manual", n)
		}
	}
	for _, name := range []string{harness.FilePlan, harness.FileSpec, harness.FileReport, harness.FileOutside, harness.FileTerminal, harness.FileSummary} {
		if !strings.Contains(m, "`"+name+"`") {
			t.Errorf("evidence file %s is not described", name)
		}
	}
	for _, key := range []string{"coordinator_reviewed", "no_lead_has_run", "reviewed_bpf", "--run", "--unattended", "time_limit_seconds", "managed_settings_inside", "make qualification-leadsandbox-build", harness.StepBindSources} {
		if !strings.Contains(m, key) {
			t.Errorf("the manual does not mention %s", key)
		}
	}
	// Case G1 is not in the design's table: it has its own row, the attended
	// variant and the keys of its evidence.
	for _, s := range []string{"| G1 |", "### Case G1, attended variant", "bind_sources", "links_checked", "no_symbolic_link_component", "no_source_in_read_write_source", "checked_path", "ataniq-g1-canary-text", "Rule G1 and git inside the sandbox"} {
		if !strings.Contains(m, s) {
			t.Errorf("the manual does not mention %q", s)
		}
	}
	// The statements the harness's own text depends on.
	for _, s := range []string{"R17", "--disable-userns", "unit"} {
		if !strings.Contains(m, s) {
			t.Errorf("the manual does not mention %q", s)
		}
	}
}
