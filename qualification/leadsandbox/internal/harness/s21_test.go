//go:build linux && amd64

package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// s21World is a fixture whose mount points exist (made the way a first run
// makes them) and whose configuration requests case S21 alone, with every
// statement the case needs.
func s21World(t *testing.T, rt profile.Runtime) *harnesstest.Fixture {
	t.Helper()
	f := harnesstest.New(t, rt)
	f.Config.CoordinatorReviewed = true
	f.Config.ScratchFoldersDisposable = true
	f.Config.Cases = []string{harness.CaseS21}
	in, err := harness.InputsForTest(f.Config, f.Base+"/resolv.conf", f.Base+"/spec")
	if err != nil {
		t.Fatal(err)
	}
	if err := mountpoints.Prepare(in, mountpoints.PrepareOptions{NoLeadHasRun: true}); err != nil {
		t.Fatal(err)
	}
	return f
}

// listing describes every entry below root with its type, mode, inode, link
// target and, for a regular file, a digest of its content: what "as before"
// means for the scratch folders. A rename keeps the inode, so an entry that
// was set aside and put back is the same entry.
func listing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s %v ino=%d", strings.TrimPrefix(p, root), info.Mode(), info.Sys().(*syscall.Stat_t).Ino)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			dst, err := os.Readlink(p)
			if err != nil {
				return err
			}
			line += " -> " + dst
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			line += " sha256=" + hex.EncodeToString(sum[:8])
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameListing(t *testing.T, what string, before, after []string) {
	t.Helper()
	if strings.Join(before, "\n") == strings.Join(after, "\n") {
		return
	}
	seen := map[string]bool{}
	for _, l := range before {
		seen[l] = true
	}
	var diff []string
	for _, l := range after {
		if !seen[l] {
			diff = append(diff, "+ "+l)
		}
		delete(seen, l)
	}
	for l := range seen {
		diff = append(diff, "- "+l)
	}
	t.Errorf("%s: the folders are not as they were:\n%s", what, strings.Join(diff, "\n"))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type want struct {
	host   string
	folder bool
}

func hosts(ws []want) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.host)
	}
	return out
}

func plantFacts(o *harness.Outcome, other bool) []harness.PlantFact {
	var out []harness.PlantFact
	if o.Summary.PlantedPaths == nil {
		return nil
	}
	for _, p := range o.Summary.PlantedPaths.Plants {
		if p.OtherRuntime == other {
			out = append(out, p)
		}
	}
	return out
}

func s21Result(t *testing.T, o *harness.Outcome, id string) assert.Status {
	t.Helper()
	for _, c := range o.Summary.Cases {
		if c.Case != harness.CaseS21 {
			continue
		}
		for _, r := range c.Results {
			if r.ID == id {
				return r.Status
			}
		}
	}
	t.Fatalf("no result %s of case S21 in %+v", id, o.Summary.Cases)
	return ""
}

// The paths of the walk are the mount points of mountpoints.List and the
// tree's .git: the case plants each of them, once as a link and once as an
// entry of the wrong type, and every plant is refused with its path and put
// back. The launcher is never called, and the folders are as they were.
func TestS21PlantsEveryWalkedPathAndKindRefusesAndRestores(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := s21World(t, rt)
			before := listing(t, f.State)
			in, err := harness.InputsForTest(f.Config, f.Base+"/resolv.conf", f.Base+"/spec")
			if err != nil {
				t.Fatal(err)
			}
			pts, err := mountpoints.List(in)
			if err != nil {
				t.Fatal(err)
			}
			// The tree's .git comes before the first of its rows, as the walk has it.
			var wants []want
			for _, p := range pts {
				if p.Git && !contains(hosts(wants), f.Layout().Tree()+"/.git") {
					wants = append(wants, want{f.Layout().Tree() + "/.git", true})
				}
				wants = append(wants, want{p.HostPath, p.Dir})
			}
			for _, rel := range []string{".git", ".git/config", ".git/hooks", ".git/info"} {
				if !contains(hosts(wants), f.Layout().Tree()+"/"+rel) {
					t.Fatalf("the walked paths lack %s", rel)
				}
			}

			r := execute(t, f, nil, nil)
			mustNoErr(t, r)
			if r.fake.Launches() != 0 {
				t.Fatalf("the case launched %d times", r.fake.Launches())
			}
			wantStep(t, r.out, harness.StepPlanted, harness.StepOK)
			wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
			if r.out.StepFailed {
				t.Fatalf("a step failed: %+v", r.out.Summary.Steps)
			}
			pe := r.out.Summary.PlantedPaths
			if pe == nil || !pe.Ran || pe.NotRun != "" || pe.Launched || pe.FoldersAsFound == nil || !*pe.FoldersAsFound {
				t.Fatalf("planted paths: %+v", pe)
			}
			facts := plantFacts(r.out, false)
			if len(facts) != 2*len(wants) {
				t.Fatalf("%d plants of the configured runtime, want %d", len(facts), 2*len(wants))
			}
			for i, w := range wants {
				for j, kind := range []string{harness.PlantSymlink, harness.PlantWrongType} {
					p := facts[2*i+j]
					wantKind, expected := "symbolic-link", harness.WalkFolder
					if kind == harness.PlantWrongType {
						wantKind = "wrong-type"
					}
					if !w.folder {
						expected = harness.WalkFile
					}
					if p.Path != w.host || p.Plant != kind || p.Expected != expected || p.Runtime != string(rt) {
						t.Errorf("plant %d is %+v, want %s %s", 2*i+j, p, kind, w.host)
						continue
					}
					if p.Status != assert.StatusOK || !p.Planted || p.StartRefused == nil || !*p.StartRefused ||
						p.ProblemKind != wantKind || p.ProblemPath != w.host || p.Launched || p.Restored == nil || !*p.Restored {
						t.Errorf("%s %s: %+v", kind, w.host, p)
					}
					for _, c := range []string{harness.StepOwnClone, harness.StepCheck, harness.StepResolve, harness.StepVerify, harness.StepBindSources} {
						if !contains(p.ChecksRun, c) {
							t.Errorf("%s %s: the check %s was not run (%v)", kind, w.host, c, p.ChecksRun)
						}
					}
					if !contains(p.RefusedBy, harness.StepCheck) {
						t.Errorf("%s %s: the start walk did not refuse (refused by %v)", kind, w.host, p.RefusedBy)
					}
				}
			}
			if got := s21Result(t, r.out, "planted-paths-refused"); got != assert.StatusOK {
				t.Errorf("planted-paths-refused is %s", got)
			}
			if got := s21Result(t, r.out, "planted-paths-restored"); got != assert.StatusOK {
				t.Errorf("planted-paths-restored is %s", got)
			}
			sameListing(t, "after case S21", before, listing(t, f.State))
			// The evidence on disk holds the facts.
			var sum harness.Summary
			readJSON(t, r.out.Dir+"/summary.json", &sum)
			if sum.Format != harness.EvidenceFormat || sum.PlantedPaths == nil || len(sum.PlantedPaths.Plants) != len(pe.Plants) {
				t.Errorf("summary.json: format %d, planted paths %+v", sum.Format, sum.PlantedPaths)
			}
			raw, _ := os.ReadFile(r.out.Dir + "/summary.json")
			for _, k := range []string{`"planted_paths"`, `"start_refused": true`, `"problem_kind": "symbolic-link"`, `"problem_kind": "wrong-type"`, `"folders_as_found": true`, `"launched": false`} {
				if !strings.Contains(string(raw), k) {
					t.Errorf("summary.json does not hold %s", k)
				}
			}
		})
	}
}

// A start of the other runtime is checked too (the design's S21 holds across a
// runtime switch): the .git paths and the place of the config folder in the
// home are planted and refused for it. The places in its own config folder
// are not covered, and the evidence says so.
func TestS21ChecksTheStartOfTheOtherRuntime(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := s21World(t, rt)
			before := listing(t, f.State)
			r := execute(t, f, nil, nil)
			mustNoErr(t, r)
			other := rt.Other()
			pe := r.out.Summary.PlantedPaths
			if pe == nil || pe.OtherRuntime != string(other) {
				t.Fatalf("planted paths: %+v", pe)
			}
			facts := plantFacts(r.out, true)
			byPath := map[string][]harness.PlantFact{}
			for _, p := range facts {
				byPath[p.Path] = append(byPath[p.Path], p)
				if p.Runtime != string(other) || p.Status != assert.StatusOK || !p.Planted || p.Restored == nil || !*p.Restored || p.Launched {
					t.Errorf("%+v", p)
				}
				if len(p.ChecksRun) != 1 || p.ChecksRun[0] != harness.StepCheck {
					t.Errorf("the other runtime's start is checked by its walk alone, not by %v", p.ChecksRun)
				}
			}
			tree := f.Layout().Tree()
			for _, rel := range []string{".git", ".git/config", ".git/hooks", ".git/info"} {
				if len(byPath[tree+"/"+rel]) != 2 {
					t.Errorf("the other runtime's walk plants %s %d times, want 2", rel, len(byPath[tree+"/"+rel]))
				}
			}
			if len(byPath[f.Layout().Home()+"/."+string(other)]) != 2 {
				t.Errorf("the config folder's place in the home (%s) is not planted for the other runtime: %v", other, byPath)
			}
			// The other runtime's skills and instruction places are in its own
			// config folder, where this run neither prepares nor writes.
			skills, instr := 0, 0
			cfgDir, _ := f.Layout().ConfigDir(other)
			for _, nc := range pe.NotCovered {
				if nc.Runtime != string(other) || !strings.Contains(nc.Reason, "second run with the other runtime's configuration") || !strings.Contains(nc.Reason, "neither prepares nor writes") || strings.Contains(nc.Reason, "does not name") {
					t.Errorf("not covered: %+v", nc)
				}
				switch nc.Path {
				case cfgDir + "/skills":
					skills++
				case cfgDir + "/" + other.InstructionFileName():
					instr++
				}
			}
			if skills != 1 || instr != 1 || len(pe.NotCovered) != 2 {
				t.Errorf("not covered: %+v", pe.NotCovered)
			}
			if got := s21Result(t, r.out, "planted-paths-refused-other-runtime"); got != assert.StatusOK {
				t.Errorf("planted-paths-refused-other-runtime is %s", got)
			}
			sameListing(t, "after case S21", before, listing(t, f.State))
		})
	}
}

// A walk that accepts everything stands in for a start that does not refuse a
// planted path: each plant is then failed, never ok, and the paths are still
// put back.
func TestS21APlantThatIsNotRefusedIsFailed(t *testing.T) {
	f := s21World(t, profile.Claude)
	before := listing(t, f.State)
	r := execute(t, f, nil, func(o *harness.Options) {
		o.CheckMountPoints = func(profile.Inputs) (mountpoints.Report, error) { return mountpoints.Report{}, nil }
	})
	mustNoErr(t, r)
	if r.fake.Launches() != 0 {
		t.Fatalf("launched %d times", r.fake.Launches())
	}
	wantStep(t, r.out, harness.StepPlanted, harness.StepOK)
	n := 0
	for _, other := range []bool{false, true} {
		for _, p := range plantFacts(r.out, other) {
			n++
			if p.Status != assert.StatusFailed || !p.Planted || p.Restored == nil || !*p.Restored {
				t.Errorf("%+v", p)
			}
			// Another check may refuse (the own-clone check looks at .git), but the
			// walk of the design did not, and that is what the case is about.
			if contains(p.RefusedBy, harness.StepCheck) {
				t.Errorf("the walk refused: %+v", p)
			}
		}
	}
	if n == 0 {
		t.Fatal("no plant was made")
	}
	var sawNotRefused bool
	for _, p := range plantFacts(r.out, false) {
		if p.StartRefused != nil && !*p.StartRefused && strings.Contains(p.Detail, "not refused") {
			sawNotRefused = true
		}
	}
	if !sawNotRefused {
		t.Error("no plant was recorded as not refused at all")
	}
	for _, id := range []string{"planted-paths-refused", "planted-paths-refused-other-runtime"} {
		if got := s21Result(t, r.out, id); got != assert.StatusFailed {
			t.Errorf("%s is %s, want failed", id, got)
		}
	}
	if got := s21Result(t, r.out, "planted-paths-restored"); got != assert.StatusOK {
		t.Errorf("planted-paths-restored is %s", got)
	}
	sameListing(t, "after case S21", before, listing(t, f.State))
}

// A path that is left planted ends the case with a failure that names it: the
// walk stands in for a lead that replaces the harness's plant, so the harness
// does not remove what it did not plant. Nothing is launched after it, and the
// plants that were not made are not-checked.
func TestS21ARestoreThatFailsNamesThePathLeftPlanted(t *testing.T) {
	f := s21World(t, profile.Claude)
	target := f.Layout().Tree() + "/.git/config"
	tampered := false
	f.Config.Cases = []string{"S1", harness.CaseS21}
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		o.CheckMountPoints = func(in profile.Inputs) (mountpoints.Report, error) {
			if fi, err := os.Lstat(target); err == nil && fi.Mode()&fs.ModeSymlink != 0 && !tampered {
				tampered = true
				if err := os.Remove(target); err != nil {
					t.Error(err)
				}
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Error(err)
				}
			}
			return mountpoints.Check(in)
		}
	})
	mustNoErr(t, r)
	if !tampered {
		t.Fatal("the stand-in never saw the plant")
	}
	if r.fake.Launches() != 0 {
		t.Fatalf("a run with a planted path launched %d times", r.fake.Launches())
	}
	st := step(t, r.out, harness.StepPlanted)
	if st.Status != harness.StepFailed || !strings.Contains(st.Error, target+" is left planted") {
		t.Fatalf("step %s: %+v", harness.StepPlanted, st)
	}
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	if !r.out.StepFailed {
		t.Error("StepFailed is not set")
	}
	pe := r.out.Summary.PlantedPaths
	if pe == nil || pe.Ran || pe.Launched {
		t.Fatalf("planted paths: %+v", pe)
	}
	var left *harness.PlantFact
	afterwards := 0
	for i, p := range pe.Plants {
		switch {
		case left == nil && !p.OtherRuntime && p.Path == target && p.Plant == harness.PlantSymlink:
			left = &pe.Plants[i]
		case left != nil:
			afterwards++
			if p.Planted || p.Status != assert.StatusNotChecked || !strings.Contains(p.Detail, "not planted") {
				t.Errorf("a plant after the failure: %+v", p)
			}
		}
	}
	if left == nil || left.Restored == nil || *left.Restored || left.Status != assert.StatusNotChecked || !strings.Contains(left.Detail, target+" is left planted") {
		t.Fatalf("the plant that could not be restored: %+v", left)
	}
	if afterwards == 0 {
		t.Error("no plant came after the failure")
	}
	if got := s21Result(t, r.out, "planted-paths-restored"); got != assert.StatusFailed {
		t.Errorf("planted-paths-restored is %s", got)
	}
	if got := s21Result(t, r.out, "planted-paths-refused"); got == assert.StatusOK {
		t.Errorf("planted-paths-refused is ok although the case did not finish")
	}
}

// The guards: the case needs --run, coordinator_reviewed and the statement that
// the folders are disposable. A dry run plants nothing and says so.
func TestS21GuardsRefuse(t *testing.T) {
	t.Run("without the statement that the folders are disposable", func(t *testing.T) {
		f := s21World(t, profile.Claude)
		f.Config.ScratchFoldersDisposable = false
		before := listing(t, f.State)
		r := execute(t, f, nil, nil)
		var gate *harness.GateError
		if !errors.As(r.err, &gate) || !strings.Contains(gate.Reason, "scratch_folders_disposable") || !strings.Contains(gate.Reason, "S21") {
			t.Fatalf("error %v, want a gate error naming scratch_folders_disposable", r.err)
		}
		if r.out != nil || r.fake.Calls() != 0 || r.canaries != 0 || len(f.Names(f.Campaign)) != 0 {
			t.Fatalf("the refused run did something: %v, %d calls", r.out, r.fake.Calls())
		}
		sameListing(t, "after a refused run", before, listing(t, f.State))
	})
	t.Run("without the coordinator's statement", func(t *testing.T) {
		f := s21World(t, profile.Claude)
		f.Config.CoordinatorReviewed = false
		r := execute(t, f, nil, nil)
		var gate *harness.GateError
		if !errors.As(r.err, &gate) || !strings.Contains(gate.Reason, "coordinator_reviewed") {
			t.Fatalf("error %v, want a gate error naming coordinator_reviewed", r.err)
		}
		if r.out != nil || r.fake.Calls() != 0 {
			t.Fatal("the refused run did something")
		}
	})
	t.Run("a dry run plants nothing and says so", func(t *testing.T) {
		f := s21World(t, profile.Claude)
		// Neither statement is needed to be told that a dry run does nothing.
		f.Config.ScratchFoldersDisposable = false
		f.Config.CoordinatorReviewed = false
		before := listing(t, f.State)
		r := executeWith(t, f, newFake(nil), false, nil)
		mustNoErr(t, r)
		if r.fake.Calls() != 0 {
			t.Fatalf("the launcher was called %d times", r.fake.Calls())
		}
		st := step(t, r.out, harness.StepPlanted)
		if st.Status != harness.StepSkipped || !strings.Contains(st.Error, "dry run") || !strings.Contains(st.Error, "S21") {
			t.Errorf("step %s: %+v", harness.StepPlanted, st)
		}
		pe := r.out.Summary.PlantedPaths
		if pe == nil || pe.Ran || !strings.Contains(pe.NotRun, "dry run") || len(pe.Plants) != 0 {
			t.Errorf("planted paths: %+v", pe)
		}
		for _, id := range []string{"planted-paths-refused", "planted-paths-refused-other-runtime", "planted-paths-restored"} {
			if got := s21Result(t, r.out, id); got != assert.StatusNotChecked {
				t.Errorf("%s is %s, want not-checked", id, got)
			}
		}
		sameListing(t, "after a dry run", before, listing(t, f.State))
	})
	t.Run("an earlier step that failed", func(t *testing.T) {
		f := s21World(t, profile.Claude)
		cfgDir, _ := f.Layout().ConfigDir(profile.Claude)
		if err := os.Remove(cfgDir + "/CLAUDE.md"); err != nil {
			t.Fatal(err)
		}
		r := execute(t, f, nil, nil)
		mustNoErr(t, r)
		wantStep(t, r.out, harness.StepCheck, harness.StepFailed)
		wantStep(t, r.out, harness.StepPlanted, harness.StepSkipped)
		pe := r.out.Summary.PlantedPaths
		if pe == nil || pe.Ran || pe.NotRun != "an earlier step failed" || len(pe.Plants) != 0 {
			t.Errorf("planted paths: %+v", pe)
		}
		if got := s21Result(t, r.out, "planted-paths-refused"); got != assert.StatusNotChecked {
			t.Errorf("planted-paths-refused is %s", got)
		}
	})
}

// A path whose set-aside name is taken (a case that did not finish left it) is
// not planted: it is not-checked with the reason, never ok, and nothing is
// touched there.
func TestS21APathThatCannotBePlantedIsNotChecked(t *testing.T) {
	f := s21World(t, profile.Claude)
	leftover := f.Layout().Tree() + "/.git" + harness.PlantAsideSuffix
	f.Write(leftover, "left by an earlier case", 0o600)
	before := listing(t, f.State)
	r := execute(t, f, nil, nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepPlanted, harness.StepOK)
	notChecked := 0
	for _, p := range plantFacts(r.out, false) {
		if p.Path == f.Layout().Tree()+"/.git" {
			notChecked++
			if p.Status != assert.StatusNotChecked || p.Planted || p.StartRefused != nil || !strings.Contains(p.Detail, "not planted") || !strings.Contains(p.Detail, "already exists") {
				t.Errorf("%+v", p)
			}
			// The entry at the set-aside name is the original, not a leftover to
			// delete: the detail says so, and how to put it back.
			for _, w := range []string{"it is the original entry", "if what is at " + p.Path + " is a plant, remove it", "rename .git" + harness.PlantAsideSuffix + " back to .git"} {
				if !strings.Contains(p.Detail, w) {
					t.Errorf("the detail %q does not say %q", p.Detail, w)
				}
			}
			if strings.Contains(p.Detail, "remove it first") {
				t.Errorf("the detail tells to remove the original: %q", p.Detail)
			}
		} else if p.Status != assert.StatusOK {
			t.Errorf("another path is not ok: %+v", p)
		}
	}
	if notChecked != 2 {
		t.Errorf("%d plants at .git are not-checked, want 2", notChecked)
	}
	if got := s21Result(t, r.out, "planted-paths-refused"); got != assert.StatusNotChecked {
		t.Errorf("planted-paths-refused is %s, want not-checked", got)
	}
	sameListing(t, "after case S21", before, listing(t, f.State))
}

// With other cases requested, case S21 is its own phase before the launch: the
// folders are as found when the launch phase uses them, and the launch happens
// once.
func TestS21RunsBeforeTheLaunchAndLeavesTheFoldersAsFound(t *testing.T) {
	f := s21World(t, profile.Claude)
	f.Config.Cases = []string{"S1", "S2", harness.CaseS21}
	before := listing(t, f.State)
	var seenAtLaunch []string
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		seenAtLaunch = listing(t, f.State)
		_ = harness.WriteReportLine(req, harnesstest.ProbeLine(req.Plan.Digest, assert.StatusOK, 0))
		return harness.Exit{}
	}, nil)
	mustNoErr(t, r)
	if r.fake.Launches() != 1 {
		t.Fatalf("launched %d times, want 1", r.fake.Launches())
	}
	if seenAtLaunch == nil {
		t.Fatal("the launch phase did not run")
	}
	sameListing(t, "when the launch phase uses the folders", before, seenAtLaunch)
	// The case's step is before the spec and the second check.
	order := map[string]int{}
	for i, s := range r.out.Summary.Steps {
		order[s.Name] = i
	}
	if !(order[harness.StepVerify] < order[harness.StepPlanted] && order[harness.StepPlanted] < order[harness.StepSpec] && order[harness.StepSpec] < order[harness.StepRecheck]) {
		t.Errorf("the steps are not in order: %v", order)
	}
	wantStep(t, r.out, harness.StepPlanted, harness.StepOK)
	wantStep(t, r.out, harness.StepLaunch, harness.StepOK)
	var cases []string
	for _, c := range r.out.Summary.Cases {
		cases = append(cases, c.Case)
	}
	if strings.Join(cases, ",") != "S1,S2,S21" {
		t.Errorf("cases %v", cases)
	}
	if got := s21Result(t, r.out, "planted-paths-refused"); got != assert.StatusOK {
		t.Errorf("planted-paths-refused is %s", got)
	}
}

// The statement is a key of the configuration, read strictly like the others,
// and S21 is an automated case that may be requested.
func TestS21ConfigurationKeyAndCase(t *testing.T) {
	if !contains(harness.AutomatedCases, harness.CaseS21) {
		t.Fatal("S21 is not an automated case")
	}
	f := harnesstest.New(t, profile.Claude)
	f.Config.Cases = []string{"S1", harness.CaseS21}
	f.Config.ScratchFoldersDisposable = true
	cfg := f.Load()
	if !cfg.ScratchFoldersDisposable || !contains(cfg.Cases, harness.CaseS21) {
		t.Errorf("configuration: %+v", cfg)
	}
	f.Config.Cases = []string{harness.CaseS21, harness.CaseS21}
	if _, err := harness.ParseConfig(mustJSON(t, f.Config)); err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Errorf("S21 listed twice: %v", err)
	}
	f.Config.Cases = []string{"S1"}
	data := strings.Replace(string(mustJSON(t, f.Config)), `"scratch_folders_disposable"`, `"scratch_folders_disposable": true, "scratch_folders_disposable"`, 1)
	if _, err := harness.ParseConfig([]byte(data)); err == nil {
		t.Error("a configuration with the statement written twice is accepted")
	}
}
