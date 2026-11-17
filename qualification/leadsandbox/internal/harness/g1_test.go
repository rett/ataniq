//go:build linux && amd64

package harness_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// The tests of rule G1 in the harness: the git metadata folder, the step that
// checks the bind sources, and what a link planted by a lead does and does not
// change. Nothing here starts a process: the launcher is the fake one, and the
// links are made with the file system calls of the test.

func gitRows() []string {
	var out []string
	for _, rel := range profile.GitReadOnlyRel() {
		out = append(out, profile.TreeInside("qualproj")+"/"+rel)
	}
	return out
}

func mountSource(o *harness.Outcome, inside string) (string, bool) {
	for _, m := range o.Plan.Mounts {
		if m.Inside == inside {
			return m.Source, true
		}
	}
	return "", false
}

// plantOutside makes a canary file and a folder outside everything the lead
// owns, as the target of a link.
func plantOutside(t *testing.T, f *harnesstest.Fixture) (file, dir string) {
	t.Helper()
	dir = f.Base + "/outside"
	f.Mkdir(dir, 0o755)
	file = dir + "/canary"
	f.Write(file, "canary\n", 0o644)
	return file, dir
}

func TestBindSourcesStepIsRecordedAfterTheRecheck(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	var names []string
	for _, s := range r.out.Summary.Steps {
		names = append(names, s.Name)
	}
	got := strings.Join(names, ",")
	if !strings.Contains(got, harness.StepRecheck+","+harness.StepBindSources+","+harness.StepLaunch) {
		t.Errorf("the steps are %s: the check of the bind sources is not between the recheck and the launch", got)
	}
	wantStep(t, r.out, harness.StepBindSources, harness.StepOK)
	if r.fake.Launches() != 1 {
		t.Errorf("launches %d", r.fake.Launches())
	}

	// A dry run starts nothing and does not make the folder, so it skips it.
	f2 := harnesstest.New(t, profile.Claude)
	f2.Config.NoLeadHasRun = true
	d := executeWith(t, f2, newFake(nil), false, nil)
	mustNoErr(t, d)
	wantStep(t, d.out, harness.StepBindSources, harness.StepSkipped)
}

func TestTheCheckIsMadeOnTheSourcesOfThePlan(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	var checked []string
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.CheckBindSources
		o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
			checked = nil
			for _, row := range rows {
				checked = append(checked, row.Bound())
			}
			return base(rows)
		}
	})
	mustNoErr(t, r)
	want := map[string]bool{}
	for _, m := range r.out.Plan.Mounts {
		if m.Source != "" {
			want[m.Source] = true
		}
	}
	if len(checked) != len(want) {
		t.Errorf("%d sources checked, the plan has %d: %v", len(checked), len(want), checked)
	}
	for _, s := range checked {
		if !want[s] {
			t.Errorf("%s is checked and not a source of the plan", s)
		}
	}
	for _, rel := range []string{"config", "hooks", "info"} {
		if !contains(checked, f.Layout().GitMeta()+"/"+rel) {
			t.Errorf("the git metadata entry %s is not checked", rel)
		}
	}
}

// A row of the host whose name is a link of the host itself (as /etc/localtime
// usually is) is bound from its real path, and checked by it: the path that is
// bound is the path that is checked, and the host's folders are not the lead's.
func TestHostRowThatIsALinkIsBoundAndCheckedByItsRealPath(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	zone := f.Owned + "/zoneinfo-real"
	f.Write(zone, "zone", 0o644)
	var checked []string
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.Resolver
		o.Resolver = func(p string) (string, error) {
			if p == "/etc/localtime" {
				return zone, nil
			}
			return base(p)
		}
		o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
			checked = nil
			for _, row := range rows {
				checked = append(checked, row.Bound())
			}
			return f.CheckBindSources(rows)
		}
	})
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	if src, ok := mountSource(r.out, "/etc/localtime"); !ok || src != zone {
		t.Fatalf("the plan binds /etc/localtime from %q (%v), want the real path %s", src, ok, zone)
	}
	if !contains(checked, zone) || contains(checked, "/etc/localtime") {
		t.Errorf("checked %v: want the real path %s and not the name", checked, zone)
	}
	// For every row the path bound is the path checked.
	for _, m := range r.out.Plan.Mounts {
		if m.Source != "" && !contains(checked, m.Source) {
			t.Errorf("%s is bound from %s, which is not among the checked paths", m.Inside, m.Source)
		}
	}
	if len(checked) != len(sourcesOf(r.out)) {
		t.Errorf("%d paths checked, the plan has %d sources", len(checked), len(sourcesOf(r.out)))
	}
}

func sourcesOf(o *harness.Outcome) []string {
	var out []string
	for _, m := range o.Plan.Mounts {
		if m.Source != "" {
			out = append(out, m.Source)
		}
	}
	return out
}

func TestGitMetaFolderIsMadeOnceAndBoundOverTheTree(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := harnesstest.New(t, rt)
			approved(f)
			gm := f.Layout().GitMeta()
			if _, err := os.Lstat(gm); !os.IsNotExist(err) {
				t.Fatalf("the folder exists before the run: %v", err)
			}
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
			mustNoErr(t, r)
			if r.out.StepFailed {
				t.Fatalf("steps: %+v", r.out.Summary.Steps)
			}
			if got := strings.Join(f.Names(gm), ","); got != "config,hooks,info" {
				t.Fatalf("the folder holds %s", got)
			}
			if st, _ := os.Lstat(gm); st.Mode().Perm() != 0o700 {
				t.Errorf("mode %v", st.Mode())
			}
			if names := f.Names(gm + "/hooks"); len(names) != 0 {
				t.Errorf("hooks is not empty: %v", names)
			}
			for _, rel := range profile.GitReadOnlyRel() {
				inside := profile.TreeInside(f.Project) + "/" + rel
				src, ok := mountSource(r.out, inside)
				if !ok || src != profile.GitMetaSource(gm, rel) {
					t.Errorf("%s is bound from %q (%v), want %s", inside, src, ok, profile.GitMetaSource(gm, rel))
				}
			}
			for _, m := range r.out.Plan.Mounts {
				if m.Source != "" && strings.HasPrefix(m.Source, f.Layout().Tree()+"/") {
					t.Errorf("%s is bound from %s in the lead's tree", m.Inside, m.Source)
				}
			}
		})
	}
}

// The git metadata folder is the layout's own, <state>/leads/<role>/gitmeta:
// the configuration has no key that names another one (rule G1: a folder
// elsewhere could lie beside or above a folder the lead writes, and only its
// last component would be looked at).
func TestGitMetaHasNoKeyInTheConfiguration(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Mkdir(f.Base+"/ataniq-owned", 0o700)
	for _, value := range []string{f.Base + "/ataniq-owned/gitmeta", f.Layout().GitMeta(), ""} {
		refused(t, f, mutate(t, f, func(m map[string]any) { sub(m, "sources")["git_meta"] = value }), `sources has the unknown key "git_meta"`)
	}
}

func TestDryRunBeforeThefirstRunSaysTheFolderIsNotThereYet(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.NoLeadHasRun = true
	r := executeWith(t, f, newFake(nil), false, nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	if _, err := os.Lstat(f.Layout().GitMeta()); !os.IsNotExist(err) {
		t.Errorf("the dry run made the folder: %v", err)
	}
	if !strings.Contains(strings.Join(r.out.Summary.Notes, "\n"), "3 of the 3 entries of the git metadata folder") {
		t.Errorf("notes: %q", r.out.Summary.Notes)
	}
	// The plan names the three sources all the same.
	for _, rel := range profile.GitReadOnlyRel() {
		if _, ok := mountSource(r.out, profile.TreeInside(f.Project)+"/"+rel); !ok {
			t.Errorf("no row for %s", rel)
		}
	}
}

// Without the statement, a run that finds no folder is refused: it neither
// makes it nor goes on.
func TestRunWithoutTheStatementFindsNoGitMetaFolder(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.CoordinatorReviewed = true
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if _, err := os.Lstat(f.Layout().GitMeta()); !os.IsNotExist(err) {
		t.Errorf("the folder was made without the statement: %v", err)
	}
	if r.fake.Launches() != 0 || !r.out.StepFailed {
		t.Errorf("launches %d, step failed %v", r.fake.Launches(), r.out.StepFailed)
	}
}

// A link planted at a bind source after the sources were resolved (the plant is
// made when the check is about to run, which is after both resolutions) is
// seen by the new step, which refuses the launch with no launch made.
func TestLinkPlantedAtABindSourceRefusesTheLaunchAtTheNewStep(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, f *harnesstest.Fixture, outside string)
		named string
	}{
		{"the source itself", func(t *testing.T, f *harnesstest.Fixture, outside string) {
			src := f.Config.Sources.AssignmentDir
			if err := os.Rename(src, src+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, src); err != nil {
				t.Fatal(err)
			}
		}, "assignment"},
		{"a folder above the source", func(t *testing.T, f *harnesstest.Fixture, outside string) {
			dir := filepath.Dir(f.Config.Sources.ManagedSettings)
			if err := os.Rename(dir, dir+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir+".moved", dir); err != nil {
				t.Fatal(err)
			}
		}, "managed"},
		{"a git metadata entry", func(t *testing.T, f *harnesstest.Fixture, outside string) {
			config := f.Layout().GitMeta() + "/config"
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside+"/canary", config); err != nil {
				t.Fatal(err)
			}
		}, "gitmeta/config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			_, outside := plantOutside(t, f)
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
				base := o.CheckBindSources
				o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
					tc.plant(t, f, outside)
					return base(rows)
				}
			})
			mustNoErr(t, r)
			wantStep(t, r.out, harness.StepRecheck, harness.StepOK)
			wantStep(t, r.out, harness.StepBindSources, harness.StepFailed)
			if e := step(t, r.out, harness.StepBindSources).Error; !strings.Contains(e, "symbolic link") || !strings.Contains(e, tc.named) {
				t.Errorf("error %q does not name a symbolic link at %s", e, tc.named)
			}
			wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
			wantStep(t, r.out, harness.StepCleanup, harness.StepOK)
			if r.fake.Launches() != 0 || !r.canary.Stopped() || !r.out.StepFailed {
				t.Errorf("launches %d, canary stopped %v, step failed %v", r.fake.Launches(), r.canary.Stopped(), r.out.StepFailed)
			}
		})
	}
}

// plantsInTheTreeGit replace .git/config, .git/hooks, .git/info or .git itself
// of the lead's tree with a link to something outside.
var plantsInTheTreeGit = []struct {
	name  string
	plant func(t *testing.T, f *harnesstest.Fixture, file, dir string)
}{
	{".git/config", func(t *testing.T, f *harnesstest.Fixture, file, dir string) {
		g := f.Layout().Tree() + "/.git/config"
		mustRemove(t, g)
		mustLink(t, file, g)
	}},
	{".git/hooks", func(t *testing.T, f *harnesstest.Fixture, file, dir string) {
		g := f.Layout().Tree() + "/.git/hooks"
		mustRemove(t, g)
		mustLink(t, dir, g)
	}},
	{".git/info", func(t *testing.T, f *harnesstest.Fixture, file, dir string) {
		g := f.Layout().Tree() + "/.git/info"
		mustRemove(t, g)
		mustLink(t, dir, g)
	}},
	{".git", func(t *testing.T, f *harnesstest.Fixture, file, dir string) {
		g := f.Layout().Tree() + "/.git"
		mustRemove(t, g)
		mustLink(t, dir, g)
	}},
}

func mustRemove(t *testing.T, p string) {
	t.Helper()
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
}

func mustLink(t *testing.T, target, p string) {
	t.Helper()
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// No bind source of the plan lies in the lead's tree but the tree itself, and
// the three .git rows come from the git metadata folder.
func checkNoSourceInTheTreeGit(t *testing.T, f *harnesstest.Fixture, o *harness.Outcome) {
	t.Helper()
	tree := f.Layout().Tree()
	for _, m := range o.Plan.Mounts {
		if m.Source == "" {
			continue
		}
		if strings.HasPrefix(m.Source, tree+"/") || strings.Contains(m.Source, "/.git") {
			t.Errorf("%s is bound from %s, in the lead's tree", m.Inside, m.Source)
		}
	}
	for _, rel := range profile.GitReadOnlyRel() {
		inside := profile.TreeInside(f.Project) + "/" + rel
		if src, ok := mountSource(o, inside); !ok || src != profile.GitMetaSource(f.Layout().GitMeta(), rel) {
			t.Errorf("%s is bound from %q (%v)", inside, src, ok)
		}
	}
}

// A link planted in the tree's .git while the lead runs (here, as the launched
// process does it) changes no bind source of the plan, and the run goes on.
func TestLinkPlantedInTheTreeGitDuringTheRunChangesNoBindSource(t *testing.T) {
	for _, tc := range plantsInTheTreeGit {
		t.Run(tc.name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			file, dir := plantOutside(t, f)
			plain := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
			mustNoErr(t, plain)
			if plain.out.StepFailed {
				t.Fatalf("the control run failed: %+v", plain.out.Summary.Steps)
			}
			r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
				tc.plant(t, f, file, dir)
				return okProbe(assert.StatusOK, 0, 0)(req, p)
			}, nil)
			mustNoErr(t, r)
			if r.out.StepFailed || r.fake.Launches() != 1 {
				t.Fatalf("step failed %v, launches %d: %+v", r.out.StepFailed, r.fake.Launches(), r.out.Summary.Steps)
			}
			checkNoSourceInTheTreeGit(t, f, r.out)
			// The same sources as a run in which nothing was planted, apart
			// from the folders of the attempt (which differ by name).
			if a, b := stripAttempt(plain.out), stripAttempt(r.out); strings.Join(a, "\n") != strings.Join(b, "\n") {
				t.Errorf("the sources changed:\n%s\n--\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
			}
		})
	}
}

// A link planted after the first Prepare and seen by the mount-point check of
// the recheck refuses the start there. The bind sources are the same, and
// nothing was launched.
func TestLinkPlantedInTheTreeGitBeforeTheRecheckRefusesTheStart(t *testing.T) {
	for _, tc := range plantsInTheTreeGit {
		t.Run(tc.name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			file, dir := plantOutside(t, f)
			plain := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
			mustNoErr(t, plain)
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
				made := o.NewCanary
				o.NewCanary = func() (*assert.Canary, error) { tc.plant(t, f, file, dir); return made() }
			})
			mustNoErr(t, r)
			wantStep(t, r.out, harness.StepPrepare, harness.StepOK)
			wantStep(t, r.out, harness.StepRecheck, harness.StepFailed)
			wantStep(t, r.out, harness.StepBindSources, harness.StepSkipped)
			if r.fake.Launches() != 0 {
				t.Errorf("launched %d times", r.fake.Launches())
			}
			checkNoSourceInTheTreeGit(t, f, r.out)
			if a, b := stripAttempt(plain.out), stripAttempt(r.out); strings.Join(a, "\n") != strings.Join(b, "\n") {
				t.Errorf("the sources changed:\n%s\n--\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
			}
		})
	}
}

// stripAttempt lists the sources of the plan with the folder of the attempt
// replaced, so that two attempts can be compared.
func stripAttempt(o *harness.Outcome) []string {
	var out []string
	for _, m := range o.Plan.Mounts {
		out = append(out, m.Inside+" <- "+strings.ReplaceAll(m.Source, o.Dir, "<attempt>"))
	}
	return out
}

func g1Case(o *harness.Outcome) (harness.CaseEvidence, bool) {
	for _, c := range o.Summary.Cases {
		if c.Case == "G1" {
			return c, true
		}
	}
	return harness.CaseEvidence{}, false
}

func g1Status(t *testing.T, o *harness.Outcome, id string) assert.Status {
	t.Helper()
	c, ok := g1Case(o)
	if !ok {
		t.Fatal("the summary has no case G1")
	}
	for _, r := range c.Results {
		if r.ID == id {
			return r.Status
		}
	}
	t.Fatalf("case G1 has no result %s: %+v", id, c)
	return ""
}

// The evidence of an attempt names every bind source of the plan, with the fact
// from the check and the fact from the verifier, and the case G1 reads them.
func TestEvidenceHasTheFactOfEveryBindSource(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	var sum harness.Summary
	readJSON(t, r.out.Dir+"/summary.json", &sum)
	ev := sum.BindSources
	if !ev.LinksChecked || ev.NoSourceInReadWriteSource == nil || !*ev.NoSourceInReadWriteSource {
		t.Fatalf("bind_sources: %+v", ev)
	}
	want := map[string]string{}
	for _, m := range r.out.Plan.Mounts {
		if m.Source != "" {
			want[m.Inside] = m.Source
		}
	}
	if len(ev.Sources) != len(want) {
		t.Fatalf("%d sources in the evidence, %d in the plan", len(ev.Sources), len(want))
	}
	for _, s := range ev.Sources {
		if want[s.Inside] != s.Source {
			t.Errorf("%s: source %q, the plan has %q", s.Inside, s.Source, want[s.Inside])
		}
		if s.NoSymbolicLinkComponent == nil || !*s.NoSymbolicLinkComponent || s.Problem != "" {
			t.Errorf("%s: %+v", s.Inside, s)
		}
	}
	for _, id := range []string{"bind-sources-no-symbolic-link", "no-bind-source-in-read-write-source"} {
		if st := g1Status(t, r.out, id); st != assert.StatusOK {
			t.Errorf("G1 %s is %s", id, st)
		}
	}
	// The raw file carries the key.
	data, err := os.ReadFile(r.out.Dir + "/summary.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"no_symbolic_link_component": true`, `"no_source_in_read_write_source": true`, `"links_checked": true`} {
		if !strings.Contains(string(data), k) {
			t.Errorf("summary.json does not hold %s", k)
		}
	}
}

func TestEvidenceNamesTheSamePathBoundAndCheckedForEveryRow(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	zone := f.Owned + "/zoneinfo-real"
	f.Write(zone, "zone", 0o644)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.Resolver
		o.Resolver = func(p string) (string, error) {
			if p == "/etc/localtime" {
				return zone, nil
			}
			return base(p)
		}
	})
	mustNoErr(t, r)
	found := false
	for _, s := range r.out.Summary.BindSources.Sources {
		if s.Inside == "/etc/localtime" {
			found = true
			if s.Source != zone || s.CheckedPath != zone {
				t.Errorf("fact: %+v", s)
			}
		}
		if s.CheckedPath == "" || s.CheckedPath != s.Source {
			t.Errorf("%s: the path bound is %q and the path checked %q", s.Inside, s.Source, s.CheckedPath)
		}
	}
	if !found {
		t.Error("no fact for /etc/localtime")
	}
}

// A link found by the check is the fact for its source: false, with the reason,
// and the case says failed. The other sources keep their fact.
func TestEvidenceOfARefusedLaunchHasTheFactFalse(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	_, outside := plantOutside(t, f)
	src := f.Config.Sources.AssignmentDir
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.CheckBindSources
		o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
			if err := os.Rename(src, src+".moved"); err != nil {
				t.Fatal(err)
			}
			mustLink(t, outside, src)
			return base(rows)
		}
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepBindSources, harness.StepFailed)
	var sum harness.Summary
	readJSON(t, r.out.Dir+"/summary.json", &sum)
	bad, good := 0, 0
	for _, s := range sum.BindSources.Sources {
		switch {
		case s.NoSymbolicLinkComponent == nil:
			t.Errorf("%s has no fact", s.Inside)
		case *s.NoSymbolicLinkComponent:
			good++
		default:
			bad++
			if s.Inside != "/ataniq/assignment" || !strings.Contains(s.Problem, "symbolic link") {
				t.Errorf("fact: %+v", s)
			}
		}
	}
	if bad != 1 || good != len(sum.BindSources.Sources)-1 {
		t.Errorf("%d sources with a link, %d without", bad, good)
	}
	if st := g1Status(t, r.out, "bind-sources-no-symbolic-link"); st != assert.StatusFailed {
		t.Errorf("G1 links result is %s", st)
	}
}

// A problem that names no row (the root of the file system could not be opened)
// means no row was checked: no source has a fact, the case says not-checked, and
// the launch is refused.
func TestEvidenceOfAReportThatNamesNoRowClaimsNothing(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
			return mountpoints.Report{Problems: []*mountpoints.Problem{{
				Kind:   mountpoints.ErrUnreadable,
				Path:   "/",
				Detail: "the root of the file system cannot be opened without following links",
			}}}
		}
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepBindSources, harness.StepFailed)
	var sum harness.Summary
	readJSON(t, r.out.Dir+"/summary.json", &sum)
	if len(sum.BindSources.Sources) == 0 {
		t.Fatal("the evidence lists no source")
	}
	for _, s := range sum.BindSources.Sources {
		if s.NoSymbolicLinkComponent != nil {
			t.Errorf("%s has the fact %v, though no row was checked", s.Inside, *s.NoSymbolicLinkComponent)
		}
	}
	if st := g1Status(t, r.out, "bind-sources-no-symbolic-link"); st != assert.StatusNotChecked {
		t.Errorf("G1 links result is %s", st)
	}
}

// Nothing is claimed for a run that never reached the check: the list is empty,
// the fact is absent and the case says not-checked, with the verifier's fact
// when the plan was verified.
func TestEvidenceOfARunThatNeverCheckedClaimsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T) *result
	}{
		{"dry run", func(t *testing.T) *result {
			f := harnesstest.New(t, profile.Claude)
			f.Config.NoLeadHasRun = true
			return executeWith(t, f, newFake(nil), false, nil)
		}},
		{"recheck failed", func(t *testing.T) *result {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			assignment := f.Config.Sources.AssignmentDir
			other := f.Owned + "/assignment-elsewhere"
			f.Mkdir(other, 0o755)
			moved := false
			return execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
				base := o.Resolver
				o.Resolver = func(p string) (string, error) {
					if moved && p == assignment {
						return other, nil
					}
					return base(p)
				}
				made := o.NewCanary
				o.NewCanary = func() (*assert.Canary, error) { moved = true; return made() }
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.run(t)
			mustNoErr(t, r)
			ev := r.out.Summary.BindSources
			if ev.LinksChecked || len(ev.Sources) != 0 || ev.Sources == nil {
				t.Errorf("bind_sources: %+v", ev)
			}
			if st := g1Status(t, r.out, "bind-sources-no-symbolic-link"); st != assert.StatusNotChecked {
				t.Errorf("G1 links result is %s", st)
			}
			if ev.NoSourceInReadWriteSource == nil || !*ev.NoSourceInReadWriteSource {
				t.Errorf("the verifier's fact: %v", ev.NoSourceInReadWriteSource)
			}
			if st := g1Status(t, r.out, "no-bind-source-in-read-write-source"); st != assert.StatusOK {
				t.Errorf("G1 read-write result is %s", st)
			}
		})
	}
}

// A run that fails before the plan is built has no verifier's fact either.
func TestEvidenceOfARunWithoutAPlanSaysNotChecked(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.CoordinatorReviewed = true // no statement: the check of the mount points fails
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.Summary.BindSources.NoSourceInReadWriteSource != nil {
		t.Error("a fact without a plan")
	}
	for _, id := range []string{"bind-sources-no-symbolic-link", "no-bind-source-in-read-write-source"} {
		if st := g1Status(t, r.out, id); st != assert.StatusNotChecked {
			t.Errorf("G1 %s is %s", id, st)
		}
	}
}

func TestCaseG1IsAnAutomatedCaseOfTheConfiguration(t *testing.T) {
	if !contains(harness.AutomatedCases, "G1") {
		t.Fatal("G1 is not automated")
	}
	f := harnesstest.New(t, profile.Claude)
	if _, err := harness.ParseConfig(mutate(t, f, func(m map[string]any) { m["cases"] = []string{"G1", "S1"} })); err != nil {
		t.Errorf("G1 with S1: %v", err)
	}
	refused(t, f, mutate(t, f, func(m map[string]any) { m["cases"] = []string{"G1", "G1"} }), "listed twice")
	refused(t, f, mutate(t, f, func(m map[string]any) { m["cases"] = []string{"G2"} }), "not an automated case")
}

// The lead's working Qargi records, when the configuration names an inside
// path for them, are the layout's own folder (rule G1 across leads): the
// configuration has no key for the host folder.
func TestRecordsFolderIsTheLayoutsOwnAndHasNoKey(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Mkdir(f.Base+"/elsewhere", 0o700)
	for _, value := range []string{f.Base + "/elsewhere", f.Layout().QargiRecords(), ""} {
		refused(t, f, mutate(t, f, func(m map[string]any) { sub(m, "sources")["qargi_records"] = value }), `sources has the unknown key "qargi_records"`)
	}

	f.Mkdir(f.Layout().QargiRecords(), 0o700)
	f.Config.Sources.QargiRecordsInside = "/ataniq/records"
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	var found bool
	for _, m := range r.out.Plan.Mounts {
		if m.Inside == "/ataniq/records" {
			found = true
			if m.Kind != string(profile.KindRwBind) || m.Source != f.Layout().QargiRecords() {
				t.Errorf("the records are bound as %+v, want a read-write bind from %s", m, f.Layout().QargiRecords())
			}
		}
	}
	if !found {
		t.Error("the records are not bound")
	}
}

// The requests, mailbox, mail and published folders are the layout's own and
// are bound for every lead: the configuration has no key for them, the plan
// binds them, and a run without one of them ends before a launch.
func TestTheFourLayoutFoldersAreBoundAndRequired(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	for _, key := range []string{"requests", "mailbox", "mail", "coord", "published"} {
		refused(t, f, mutate(t, f, func(m map[string]any) { sub(m, "sources")[key] = f.Base + "/elsewhere" }), `sources has the unknown key "`+key+`"`)
	}
	l := f.Layout()
	want := map[string]struct{ kind, source string }{
		"/ataniq/requests": {string(profile.KindRwBind), l.Requests()},
		"/ataniq/mailbox":  {string(profile.KindRoBind), l.Mailbox()},
		"/ataniq/mail":     {string(profile.KindRwBind), l.Mail()},
		"/coord":           {string(profile.KindRoBind), l.Published()},
	}
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	found := 0
	for _, m := range r.out.Plan.Mounts {
		if w, ok := want[m.Inside]; ok {
			found++
			if m.Kind != w.kind || m.Source != w.source {
				t.Errorf("%s is bound as %+v, want a %s from %s", m.Inside, m, w.kind, w.source)
			}
		}
	}
	if found != len(want) {
		t.Errorf("the plan binds %d of the %d folders", found, len(want))
	}

	// A folder the lead writes is looked at with the lead's other folders (the
	// own-clone step); a folder Ataniq owns is a source, looked at by the
	// resolution.
	for _, c := range []struct {
		name string
		dir  func(profile.Layout) string
		step string
	}{
		{"requests", profile.Layout.Requests, harness.StepOwnClone},
		{"mail", profile.Layout.Mail, harness.StepOwnClone},
		{"mailbox", profile.Layout.Mailbox, harness.StepResolve},
		{"published", profile.Layout.Published, harness.StepResolve},
	} {
		t.Run(c.name+" missing", func(t *testing.T) {
			f := harnesstest.New(t, profile.Codex)
			approved(f)
			dir := c.dir(f.Layout())
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
			mustNoErr(t, r)
			wantStep(t, r.out, c.step, harness.StepFailed)
			if e := step(t, r.out, c.step).Error; !strings.Contains(e, dir) {
				t.Errorf("the step %s does not name %s: %s", c.step, dir, e)
			}
			if r.fake.Launches() != 0 {
				t.Error("launched")
			}
		})
	}
	// A requests or mail folder that others can enter is refused like the home.
	for _, dir := range []func(profile.Layout) string{profile.Layout.Requests, profile.Layout.Mail} {
		f := harnesstest.New(t, profile.Claude)
		approved(f)
		if err := os.Chmod(dir(f.Layout()), 0o755); err != nil {
			t.Fatal(err)
		}
		r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
		mustNoErr(t, r)
		wantStep(t, r.out, harness.StepOwnClone, harness.StepFailed)
		if e := step(t, r.out, harness.StepOwnClone).Error; !strings.Contains(e, dir(f.Layout())) || !strings.Contains(e, "0700") {
			t.Errorf("own-clone: %s", e)
		}
	}
}

// A clone whose .git/config has an include or includeIf section is not copied:
// the step prepare fails with the reason, nothing is made and nothing starts.
func TestACloneConfigWithAnIncludeSectionEndsTheRunAtPrepare(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Write(f.Layout().Tree()+"/.git/config", "[core]\n\tbare = false\n[includeIf \"gitdir:/work/\"]\n\tpath = ../../x\n", 0o644)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepPrepare, harness.StepFailed)
	if e := step(t, r.out, harness.StepPrepare).Error; !strings.Contains(e, "includeIf") || !strings.Contains(e, "line 3") {
		t.Errorf("the step's error does not name the section and its line: %q", e)
	}
	if _, err := os.Lstat(f.Layout().GitMeta()); !os.IsNotExist(err) {
		t.Errorf("the folder was made: %v", err)
	}
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

// A leftover of a Prepare that did not finish (the temporary folder the git
// metadata folder is built in) ends the run at the step prepare, with a reason
// that names it; nothing is reused and nothing starts.
func TestALeftoverGitMetaFolderEndsTheRunAtPrepare(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	left := f.Layout().GitMeta() + ".tmp"
	f.Mkdir(left, 0o700)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepPrepare, harness.StepFailed)
	if e := step(t, r.out, harness.StepPrepare).Error; !strings.Contains(e, left) {
		t.Errorf("the step's error does not name the leftover %s: %q", left, e)
	}
	if _, err := os.Lstat(f.Layout().GitMeta()); !os.IsNotExist(err) {
		t.Errorf("the folder was made: %v", err)
	}
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}
