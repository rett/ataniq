//go:build linux && amd64

package harness

import (
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
)

func TestHostRowNotesNameTheRealPathOfARowOfTheHostOnly(t *testing.T) {
	notes := hostRowNotes([]mountpoints.ResolvedSource{
		{Inside: "/ataniq/agent", Source: "/o/agent", Real: "/o/agent", Role: profile.RoleAgent},
		{Inside: "/etc/hosts", Source: "/etc/hosts", Real: "/etc/hosts", Role: profile.RoleHost},
		{Inside: "/etc/localtime", Source: "/etc/localtime", Real: "/usr/share/zoneinfo/UTC", Role: profile.RoleHost},
		{Inside: "/etc/ssl", Source: "/etc/ssl", Role: profile.RoleHost, Skipped: true},
		// A row Ataniq supplies whose real path differs is refused by the
		// resolution, never noted.
		{Inside: "/ataniq/probe", Source: "/o/probe", Real: "/real/probe", Role: profile.RoleProbe},
	})
	if len(notes) != 1 || !strings.Contains(notes[0], "/etc/localtime") || !strings.Contains(notes[0], "/usr/share/zoneinfo/UTC") {
		t.Errorf("notes: %q", notes)
	}
}

func TestCasesForListsTheResultsAnAssertionMapsTo(t *testing.T) {
	results := []assert.Result{
		{Number: 3, ID: "host-loopback-unreachable", Status: assert.StatusOK},
		{Number: 3, ID: "host-loopback-unreachable.outside", Status: assert.StatusFailed},
		{Number: 10, ID: "qualification-record", Status: assert.StatusNotChecked},
		{Number: 7, ID: "tree-git-readonly", Status: assert.StatusOK},
	}
	got := casesFor([]string{"S2", "S11", "S4"}, results, nil)
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

// An evidence file is made exclusively: a file that is already there, a link
// in its place included, is never written over or followed.
func TestWriteFileNeverReplacesOrFollows(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "plan.json", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(dir, "plan.json", []byte("second")); err == nil {
		t.Error("an existing evidence file was replaced")
	}
	if got, _ := os.ReadFile(dir + "/plan.json"); string(got) != "first" {
		t.Errorf("the file holds %q", got)
	}
	victim := dir + "/victim"
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dir+"/spec.json"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(dir, "spec.json", []byte("through the link")); err == nil {
		t.Error("a link in place of an evidence file was followed")
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("the target of the link holds %q", got)
	}
	st, err := os.Lstat(dir + "/plan.json")
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", st, err)
	}
}

// The harness refuses to check a plan whose bound path is not the path the
// check would open: for every row the path bound must be the path checked.
func TestPlanBindRowsRefusesABoundPathThatIsNotTheCheckedOne(t *testing.T) {
	agent := mountpoints.ResolvedSource{Inside: "/ataniq/agent", Source: "/o/agent", Real: "/o/agent", Role: profile.RoleAgent}
	local := mountpoints.ResolvedSource{Inside: "/etc/localtime", Source: "/etc/localtime", Real: "/zone/UTC", Role: profile.RoleHost}
	skipped := mountpoints.ResolvedSource{Inside: "/etc/ssl", Source: "/etc/ssl", Role: profile.RoleHost, Skipped: true}
	for _, tc := range []struct {
		name   string
		mounts []plan.Mount
		want   string // "" for accepted
	}{
		{"every path is the checked one", []plan.Mount{
			{Inside: "/", Kind: profile.KindTmpfs},
			{Inside: "/ataniq/agent", Source: "/o/agent"},
			{Inside: "/etc/localtime", Source: "/zone/UTC"},
		}, ""},
		{"a host row bound by its name", []plan.Mount{{Inside: "/etc/localtime", Source: "/etc/localtime"}}, "must be the path that is checked"},
		{"a row bound from another path", []plan.Mount{{Inside: "/ataniq/agent", Source: "/o/other"}}, "must be the path that is checked"},
		{"a row with no resolved source", []plan.Mount{{Inside: "/ataniq/probe", Source: "/o/probe"}}, "no source for that row"},
		{"a skipped row that is bound", []plan.Mount{{Inside: "/etc/ssl", Source: "/etc/ssl"}}, "no source for that row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &attempt{res2: mountpoints.Resolution{Sources: []mountpoints.ResolvedSource{agent, local, skipped}}, pl: &plan.Plan{Mounts: tc.mounts}}
			rows, err := a.planBindRows()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want == "" && len(rows) != 2:
				t.Errorf("rows: %+v", rows)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("got %v, want an error that says %q", err, tc.want)
			}
		})
	}
}
