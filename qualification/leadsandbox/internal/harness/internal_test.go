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

// The fact of the start walk is what the last check of the mount points found.
func TestRecordWalkTurnsTheCheckIntoTheFactOfAssertion7(t *testing.T) {
	link := &mountpoints.Problem{Kind: mountpoints.ErrSymlink, Path: "/srv/state/leads/qual-lead/home/.claude/skills", Detail: "the mount point is a symbolic link"}
	other := &mountpoints.Problem{Kind: mountpoints.ErrWrongType, Path: "/srv/state/leads/qual-lead/tree/.git/hooks"}
	for _, tc := range []struct {
		name      string
		statement bool
		rep       mountpoints.Report
		err       error
		status    assert.Status
		detail    []string
	}{
		{"nothing found, no lead has run", true, mountpoints.Report{}, nil, assert.StatusOK,
			[]string{"with the previous instance proven stopped", "step recheck", "no_lead_has_run"}},
		{"nothing found, no statement", false, mountpoints.Report{}, nil, assert.StatusNotChecked,
			[]string{"not made with the previous instance proven stopped", "does not prove that no process of an earlier lead remains"}},
		{"a link at a mount point", true, mountpoints.Report{Problems: []*mountpoints.Problem{link, other}}, nil, assert.StatusFailed,
			[]string{"found 2 problems", `the first is at "/srv/state/leads/qual-lead/home/.claude/skills"`, "a path component is a symbolic link"}},
		{"a problem is failed without the statement too", false, mountpoints.Report{Problems: []*mountpoints.Problem{other}}, nil, assert.StatusFailed,
			[]string{"found 1 problems", "/tree/.git/hooks", "wrong type"}},
		{"more problems than are listed", true, mountpoints.Report{Problems: []*mountpoints.Problem{link}, Truncated: true}, nil, assert.StatusFailed,
			[]string{"more problems than the check lists"}},
		{"the check refused the inputs", true, mountpoints.Report{}, &mountpoints.Problem{Kind: mountpoints.ErrInvalid, Detail: "no role"}, assert.StatusNotChecked,
			[]string{"was not made", "refused the inputs", "no role"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &attempt{cfg: &Config{NoLeadHasRun: tc.statement}}
			a.recordWalk(StepRecheck, tc.rep, tc.err)
			r := assert.CheckStartWalk(a.walk)
			if r.Status != tc.status || r.Number != 7 || r.ID != assert.IDTreeGitReadOnly+assert.OutsideSuffix {
				t.Fatalf("%+v, want %s", r, tc.status)
			}
			for _, want := range tc.detail {
				if !strings.Contains(r.Detail, want) {
					t.Errorf("detail %q lacks %q", r.Detail, want)
				}
			}
		})
	}
	// A later check replaces an earlier one: the walk right before the launch
	// is the one that counts.
	a := &attempt{cfg: &Config{NoLeadHasRun: true}}
	a.recordWalk(StepCheck, mountpoints.Report{}, nil)
	a.recordWalk(StepRecheck, mountpoints.Report{Problems: []*mountpoints.Problem{link}}, nil)
	if r := assert.CheckStartWalk(a.walk); r.Status != assert.StatusFailed || !strings.Contains(r.Detail, "step recheck") {
		t.Errorf("the later check did not replace the earlier: %+v", r)
	}
	// An attempt that made no check has no walk to speak of.
	if r := assert.CheckStartWalk((&attempt{cfg: &Config{}}).walk); r.Status != assert.StatusNotChecked {
		t.Errorf("no check at all: %+v", r)
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
	// S16 (assertion 7) is not requested, so its result is nowhere, and
	// neither S16 nor S21 is an automated case: the results of assertion 7,
	// inside and outside, are in report.json and outside.json.
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
// check would open: for every row the path bound must be the path checked. It
// refuses as well a plan that binds a row another way than the row is checked:
// a source Ataniq supplies from anything but the descriptor of the walk the
// check makes, a row of the host from a descriptor.
func TestPlanBindRowsRefusesABoundPathThatIsNotTheCheckedOne(t *testing.T) {
	walk := mountpoints.SourceWalk{Inside: "/ataniq/agent", Root: "/o", Rel: "agent", Dir: true}
	agent := mountpoints.ResolvedSource{Inside: "/ataniq/agent", Source: "/o/agent", Real: "/o/agent", Role: profile.RoleAgent, Walk: walk}
	local := mountpoints.ResolvedSource{Inside: "/etc/localtime", Source: "/etc/localtime", Real: "/zone/UTC", Role: profile.RoleHost}
	skipped := mountpoints.ResolvedSource{Inside: "/etc/ssl", Source: "/etc/ssl", Role: profile.RoleHost, Skipped: true}
	agentWalk := plan.Walk{FD: 10, Inside: "/ataniq/agent", Root: "/o", Rel: "agent", Dir: true}
	agentMount := plan.Mount{Inside: "/ataniq/agent", Source: "/o/agent", FD: 10}
	for _, tc := range []struct {
		name   string
		mounts []plan.Mount
		walks  []plan.Walk
		want   string // "" for accepted
	}{
		{"every path is the checked one", []plan.Mount{
			{Inside: "/", Kind: profile.KindTmpfs},
			agentMount,
			{Inside: "/etc/localtime", Source: "/zone/UTC"},
		}, []plan.Walk{agentWalk}, ""},
		{"a host row bound by its name", []plan.Mount{{Inside: "/etc/localtime", Source: "/etc/localtime"}}, nil, "must be the path that is checked"},
		{"a row bound from another path", []plan.Mount{{Inside: "/ataniq/agent", Source: "/o/other", FD: 10}}, []plan.Walk{agentWalk}, "must be the path that is checked"},
		{"a row with no resolved source", []plan.Mount{{Inside: "/ataniq/probe", Source: "/o/probe"}}, nil, "no source for that row"},
		{"a skipped row that is bound", []plan.Mount{{Inside: "/etc/ssl", Source: "/etc/ssl"}}, nil, "no source for that row"},
		// How a row is bound.
		{"a source Ataniq supplies bound by path", []plan.Mount{{Inside: "/ataniq/agent", Source: "/o/agent"}}, nil, "never bound by path"},
		{"a source Ataniq supplies bound by path, its walk left", []plan.Mount{{Inside: "/ataniq/agent", Source: "/o/agent"}}, []plan.Walk{agentWalk}, "never bound by path"},
		{"a mount with a descriptor and no walk", []plan.Mount{agentMount}, nil, "never bound by path"},
		{"a mount bound from another descriptor than its walk", []plan.Mount{{Inside: "/ataniq/agent", Source: "/o/agent", FD: 11}}, []plan.Walk{agentWalk}, "never bound by path"},
		{"a row of the host bound from a descriptor", []plan.Mount{{Inside: "/etc/localtime", Source: "/zone/UTC", FD: 12}}, nil, "a row of the host is bound by its real path"},
		{"a row of the host with a walk", []plan.Mount{{Inside: "/etc/localtime", Source: "/zone/UTC"}},
			[]plan.Walk{{FD: 12, Inside: "/etc/localtime", Root: "/zone", Rel: "UTC"}}, "a row of the host is bound by its real path"},
		{"a walk from another folder to the same source", []plan.Mount{agentMount},
			[]plan.Walk{{FD: 10, Inside: "/ataniq/agent", Root: "/", Rel: "o/agent", Dir: true}}, "must be the walk that is checked"},
		{"a walk by another path", []plan.Mount{agentMount},
			[]plan.Walk{{FD: 10, Inside: "/ataniq/agent", Root: "/o", Rel: "other", Dir: true}}, "must be the walk that is checked"},
		{"a walk of the other type", []plan.Mount{agentMount},
			[]plan.Walk{{FD: 10, Inside: "/ataniq/agent", Root: "/o", Rel: "agent"}}, "must be the walk that is checked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &attempt{res2: mountpoints.Resolution{Sources: []mountpoints.ResolvedSource{agent, local, skipped}}, pl: &plan.Plan{Mounts: tc.mounts, Walks: tc.walks}}
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
