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
		o.CheckBindSources = func(srcs []string) mountpoints.Report { checked = append([]string(nil), srcs...); return base(srcs) }
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
// usually is) is checked by its real path: the plan cannot name another, and
// the host's folders are not the lead's.
func TestHostRowThatIsALinkIsCheckedByItsRealPath(t *testing.T) {
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
		o.CheckBindSources = func(srcs []string) mountpoints.Report {
			checked = append([]string(nil), srcs...)
			return f.CheckBindSources(srcs)
		}
	})
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	if src, ok := mountSource(r.out, "/etc/localtime"); !ok || src != "/etc/localtime" {
		t.Fatalf("the plan binds /etc/localtime from %q (%v)", src, ok)
	}
	if !contains(checked, zone) || contains(checked, "/etc/localtime") {
		t.Errorf("checked %v: want the real path %s and not the name", checked, zone)
	}
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

func TestGitMetaKeyNamesAnotherFolder(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	f.Mkdir(f.Base+"/ataniq-owned", 0o700)
	gm := f.Base + "/ataniq-owned/gitmeta"
	f.Config.Sources.GitMeta = gm
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	if got := strings.Join(f.Names(gm), ","); got != "config,hooks,info" {
		t.Errorf("the folder holds %s", got)
	}
	if _, err := os.Lstat(f.Layout().GitMeta()); !os.IsNotExist(err) {
		t.Errorf("the default folder was made: %v", err)
	}
	if src, _ := mountSource(r.out, profile.TreeInside(f.Project)+"/.git/config"); src != gm+"/config" {
		t.Errorf("config is bound from %s", src)
	}
}

func TestGitMetaKeyIsValidatedLikeASource(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	l := f.Layout()
	cfg, _ := l.ConfigDir(profile.Claude)
	for _, tc := range []struct{ name, value, want string }{
		{"relative", "gitmeta", "git metadata folder"},
		{"unclean", f.Base + "/a/../gitmeta", "git metadata folder"},
		{"in the tree", l.Tree() + "/gitmeta", "tree"},
		{"in the home", l.Home() + "/gitmeta", "home"},
		{"in the config folder", cfg + "/gitmeta", "config folder"},
		{"is the tree", l.Tree(), "tree"},
		{"above the tree", l.LeadDir(), "lead's"},
		{"in the campaign", f.Campaign + "/gitmeta", "campaign"},
		{"never mounted", f.Base + "/owner/work/gitmeta", "never-mounted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refused(t, f, mutate(t, f, func(m map[string]any) { sub(m, "sources")["git_meta"] = tc.value }), tc.want)
		})
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
				o.CheckBindSources = func(srcs []string) mountpoints.Report {
					tc.plant(t, f, outside)
					return base(srcs)
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
