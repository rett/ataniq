//go:build linux && amd64

package harness

import (
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
)

func TestApplyRealPutsRealPathsIntoTheInputs(t *testing.T) {
	in := profile.Inputs{
		StateRoot: "/s", Role: "r", Project: "p", Runtime: profile.Claude,
		AgentInstall: "/o/agent", ProbeBinary: "/o/probe", ResolvConf: "/o/resolv",
		ExtraPlaceholders: []profile.Placeholder{{Rel: ".envrc", Source: "/o/envrc"}},
	}
	notes, err := applyReal(&in, []mountpoints.ResolvedSource{
		{Inside: "/ataniq/agent", Source: "/o/agent", Real: "/real/agent"},
		{Inside: "/ataniq/probe", Source: "/o/probe", Real: "/o/probe"},
		{Inside: "/etc/resolv.conf", Source: "/o/resolv", Real: "/real/resolv"},
		{Inside: "/work/p/.envrc", Source: "/o/envrc", Real: "/real/envrc"},
		{Inside: "/etc/localtime", Source: "/etc/localtime", Real: "/usr/share/zoneinfo/UTC"},
		{Inside: "/etc/ssl", Source: "/etc/ssl", Skipped: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.AgentInstall != "/real/agent" || in.ResolvConf != "/real/resolv" || in.ExtraPlaceholders[0].Source != "/real/envrc" || in.ProbeBinary != "/o/probe" {
		t.Errorf("inputs: %+v", in)
	}
	if len(notes) != 4 || !strings.Contains(notes[3], "/etc/localtime") {
		t.Errorf("notes: %q", notes)
	}
	for _, inside := range []string{"/home/lead", "/home/lead/.claude", "/work/p", "/work/p/.git/config", "/ataniq/mirror/p.git"} {
		_, err := applyReal(&in, []mountpoints.ResolvedSource{{Inside: inside, Source: "/s/x", Real: "/elsewhere/x"}})
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s: %v", inside, err)
		}
	}
}

func TestCasesForListsTheResultsAnAssertionMapsTo(t *testing.T) {
	results := []assert.Result{
		{Number: 3, ID: "host-loopback-unreachable", Status: assert.StatusOK},
		{Number: 3, ID: "host-loopback-unreachable.outside", Status: assert.StatusFailed},
		{Number: 10, ID: "qualification-record", Status: assert.StatusNotChecked},
		{Number: 7, ID: "tree-git-readonly", Status: assert.StatusOK},
	}
	got := casesFor([]string{"S2", "S11", "S4"}, results)
	if len(got) != 3 || len(got[0].Results) != 2 || got[0].Results[1].Status != assert.StatusFailed {
		t.Fatalf("S2: %+v", got[0])
	}
	if len(got[1].Results) != 1 || got[1].Results[0].ID != "qualification-record" {
		t.Errorf("S11: %+v", got[1])
	}
	// S16 (assertion 7) is not requested, so its result is nowhere.
	// A requested case with no result says so.
	if len(got[2].Results) != 0 || !strings.Contains(strings.Join(got[2].Notes, " "), "no result of case S4") {
		t.Errorf("S4: %+v", got[2])
	}
}

func TestCheckKeysIsExact(t *testing.T) {
	ty := reflectType[Config]()
	for _, bad := range []string{
		`{"campaign":"/a","campaign":"/b"}`, `{"Campaign":"/a"}`, `{"nope":1}`,
		`{"terminal":{"rows":1,"rows":2}}`, `{"sources":{"extra_placeholders":[{"rel":"a","rel":"b"}]}}`,
		`{"executables":[1]}`, `{"cases":{"a":1}}`,
	} {
		if err := checkKeys([]byte(bad), ty); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	for _, good := range []string{`{}`, `{"campaign":"/a","cases":["S1"]}`, `{"sources":{"extra_placeholders":[{"rel":"a"},{"rel":"b"}],"seccomp":{"file":"/f"}}}`} {
		if err := checkKeys([]byte(good), ty); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}
