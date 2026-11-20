//go:build linux && amd64

package harness_test

import (
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// Rule B1, second half, in the harness: the plan it launches binds every
// source Ataniq supplies from a descriptor that the pass-through step walks
// to, the evidence says so per source, and a bubblewrap known to be too old
// for that is not launched. Nothing here starts a sandbox tool: the launcher
// is the fake.

// The plan of a run is given the walks of mountpoints, and binds every source
// Ataniq supplies from a descriptor of its pass-through step.
func TestThePlanOfARunBindsEverySuppliedSourceFromADescriptor(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := harnesstest.New(t, rt)
			approved(f)
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
			mustNoErr(t, r)
			if r.out.StepFailed {
				t.Fatalf("steps: %+v", r.out.Summary.Steps)
			}
			in := r.fake.Requests()[0].Plan
			walks, err := mountpoints.SourceWalks(inputsOf(t, f, r))
			if err != nil {
				t.Fatal(err)
			}
			if len(in.Walks) != len(walks) || len(walks) < 20 {
				t.Fatalf("the plan walks %d sources, mountpoints gives %d", len(in.Walks), len(walks))
			}
			for i, w := range walks {
				if got := in.Walks[i]; got.Inside != w.Inside || got.Root != w.Root || got.Rel != w.Rel || got.Dir != w.Dir {
					t.Errorf("walk %d of the plan is %+v, mountpoints gives %+v", i, got, w)
				}
			}
			// plan.json: a descriptor for every such mount, none for a row of
			// the host, and the walk of each.
			var pe harness.PlanEvidence
			readJSON(t, r.out.Dir+"/plan.json", &pe)
			if pe.Format != 3 || harness.EvidenceFormat != 3 {
				t.Errorf("the evidence format is %d (constant %d), want 3", pe.Format, harness.EvidenceFormat)
			}
			if len(pe.Walks) != len(walks) {
				t.Fatalf("plan.json lists %d walks, want %d", len(pe.Walks), len(walks))
			}
			byInside := map[string]harness.WalkEvidence{}
			for i, w := range pe.Walks {
				byInside[w.Inside] = w
				want := harness.WalkFile
				if walks[i].Dir {
					want = harness.WalkFolder
				}
				if w.Root != walks[i].Root || w.Rel != walks[i].Rel || w.Type != want || w.FD < 10 {
					t.Errorf("plan.json walk %d is %+v, want %+v", i, w, walks[i])
				}
			}
			for _, m := range pe.Mounts {
				w, supplied := byInside[m.Inside]
				switch {
				case m.Source == "":
					if m.FD != 0 {
						t.Errorf("%s has no source and descriptor %d", m.Inside, m.FD)
					}
				case supplied != (m.FD != 0) || m.FD != w.FD:
					t.Errorf("%s (source %s): descriptor %d, walk %+v", m.Inside, m.Source, m.FD, w)
				}
				// A source under the fixture is one Ataniq supplies; the host's
				// own rows are bound by path.
				if own := strings.HasPrefix(m.Source, f.Base+"/"); m.Source != "" && own != supplied {
					t.Errorf("%s from %s: a walk %v", m.Inside, m.Source, supplied)
				}
			}
			// No argument of bubblewrap is a path under the fixture: none of
			// these sources is bound by path.
			argv := pe.Argv
			bwrap := 0
			for i, a := range argv {
				if a == f.Config.Executables.Bwrap {
					bwrap = i
				}
			}
			end := len(argv)
			for i := bwrap; i < len(argv); i++ {
				if argv[i] == "--" {
					end = i
					break
				}
			}
			if bwrap == 0 {
				t.Fatal("no bubblewrap in the argument vector")
			}
			for _, a := range argv[bwrap+1 : end] {
				if strings.HasPrefix(a, f.Base+"/") {
					t.Errorf("the bubblewrap argument %q is a path under the fixture", a)
				}
			}
			raw, err := os.ReadFile(r.out.Dir + "/plan.json")
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{`"walks": [`, `"root": "` + f.State + `"`, `"type": "folder"`, `"type": "file"`, `"format": 3`} {
				if !strings.Contains(string(raw), k) {
					t.Errorf("plan.json does not hold %s", k)
				}
			}
		})
	}
}

// inputsOf are the profile inputs of the run: the fixture's configuration with
// the two paths the harness makes in its attempt folder.
func inputsOf(t *testing.T, f *harnesstest.Fixture, r *result) profile.Inputs {
	t.Helper()
	// The harness derives the inputs itself; the test reads back what the
	// plan binds for the two rows the harness supplies.
	var resolv, spec string
	for _, m := range r.out.Plan.Mounts {
		switch m.Inside {
		case profile.ResolvConfInside:
			resolv = m.Source
		case profile.SpecDirInside:
			spec = m.Source
		}
	}
	in, err := harness.InputsForTest(f.Load(), resolv, spec)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// summary.json says, per source, how the plan binds it and what the check
// found: bound from a descriptor with the walk for a source Ataniq supplies,
// by path for a row of the host; no link, and the type the row binds.
func TestEvidenceSaysHowEachSourceIsBound(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	var sum harness.Summary
	readJSON(t, r.out.Dir+"/summary.json", &sum)
	if sum.Format != 3 {
		t.Errorf("summary format %d", sum.Format)
	}
	ev := sum.BindSources
	if ev.NoAtaniqSourceBoundByPath == nil || !*ev.NoAtaniqSourceBoundByPath {
		t.Errorf("the verifier's fact about descriptors: %v", ev.NoAtaniqSourceBoundByPath)
	}
	walks := map[string]harness.WalkEvidence{}
	for _, w := range r.out.Plan.Walks {
		walks[w.Inside] = w
	}
	supplied, host := 0, 0
	for _, s := range ev.Sources {
		w, ok := walks[s.Inside]
		if s.BoundByDescriptor != ok {
			t.Errorf("%s: bound_by_descriptor is %v, the plan has a walk: %v", s.Inside, s.BoundByDescriptor, ok)
		}
		if !ok {
			host++
			if s.Descriptor != 0 || s.WalkRoot != "" || s.WalkRel != "" || s.WalkType != "" || s.HasExpectedType != nil {
				t.Errorf("%s is a row of the host and has a walk or a type fact: %+v", s.Inside, s)
			}
			if strings.HasPrefix(s.Source, f.Base+"/") {
				t.Errorf("%s from %s is bound by path", s.Inside, s.Source)
			}
			continue
		}
		supplied++
		if s.Descriptor != w.FD || s.WalkRoot != w.Root || s.WalkRel != w.Rel || s.WalkType != w.Type {
			t.Errorf("%s: %+v, the plan's walk is %+v", s.Inside, s, w)
		}
		// The walk leads to the path that is bound and checked.
		joined := s.WalkRoot + "/" + s.WalkRel
		if s.WalkRel == "." {
			joined = s.WalkRoot
		}
		if joined != s.Source || s.CheckedPath != s.Source {
			t.Errorf("%s: the walk leads to %s, the source is %s and the path checked %s", s.Inside, joined, s.Source, s.CheckedPath)
		}
		if s.HasExpectedType == nil || !*s.HasExpectedType || s.NoSymbolicLinkComponent == nil || !*s.NoSymbolicLinkComponent {
			t.Errorf("%s: the facts of a clean source are %+v", s.Inside, s)
		}
		// A source below the state root is walked from the state root.
		if below := strings.HasPrefix(s.Source, f.State+"/"); below != (s.WalkRoot == f.State) {
			t.Errorf("%s from %s is walked from %s", s.Inside, s.Source, s.WalkRoot)
		}
	}
	if supplied != len(walks) || supplied < 20 || host < 3 {
		t.Errorf("%d sources bound from a descriptor (the plan has %d walks), %d by path", supplied, len(walks), host)
	}
	raw, err := os.ReadFile(r.out.Dir + "/summary.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		`"bound_by_descriptor": true`, `"bound_by_descriptor": false`, `"descriptor": 10`, `"walk_root": "` + f.State + `"`,
		`"walk_rel": "`, `"walk_type": "folder"`, `"walk_type": "file"`, `"has_expected_type": true`,
		`"no_ataniq_source_bound_by_path": true`, `"format": 3`,
	} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("summary.json does not hold %s", k)
		}
	}
}

// A source of another type than its row binds refuses the launch, and is a
// fact of its own: it is not recorded as a link, and not as nothing.
func TestWrongTypeAtABindSourceIsItsOwnFact(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(t *testing.T, f *harnesstest.Fixture)
		says string
	}{
		{"a file where a folder is bound", func(t *testing.T, f *harnesstest.Fixture) {
			src := f.Config.Sources.AssignmentDir
			if err := os.Rename(src, src+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(src, []byte("not a folder"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "is a regular file, and the row binds a folder"},
		{"a folder where a file is bound", func(t *testing.T, f *harnesstest.Fixture) {
			src := f.Config.Sources.ManagedSettings
			if err := os.Rename(src, src+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(src, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "is a folder, and the row binds a regular file"},
		{"a file where the git hooks folder is bound", func(t *testing.T, f *harnesstest.Fixture) {
			hooks := f.Layout().GitMeta() + "/hooks"
			if err := os.RemoveAll(hooks); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hooks, []byte("#!/bin/sh\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "is a regular file, and the row binds a folder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
				base := o.CheckBindSources
				o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
					tc.swap(t, f)
					return base(rows)
				}
			})
			mustNoErr(t, r)
			wantStep(t, r.out, harness.StepBindSources, harness.StepFailed)
			wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
			if r.fake.Launches() != 0 {
				t.Errorf("%d launches", r.fake.Launches())
			}
			if e := step(t, r.out, harness.StepBindSources).Error; !strings.Contains(e, tc.says) {
				t.Errorf("the step's error %q does not say %q", e, tc.says)
			}
			var sum harness.Summary
			readJSON(t, r.out.Dir+"/summary.json", &sum)
			wrong := 0
			for _, s := range sum.BindSources.Sources {
				switch {
				case s.HasExpectedType != nil && !*s.HasExpectedType:
					wrong++
					if !strings.Contains(s.Problem, tc.says) {
						t.Errorf("%s: problem %q", s.Inside, s.Problem)
					}
					// Not a link, and the fact about links is not given: the
					// walk stopped at the type.
					if s.NoSymbolicLinkComponent != nil {
						t.Errorf("%s: a wrong type is recorded with the link fact %v", s.Inside, *s.NoSymbolicLinkComponent)
					}
					if !s.BoundByDescriptor || s.WalkRoot == "" {
						t.Errorf("%s: the fact has no walk: %+v", s.Inside, s)
					}
				case s.BoundByDescriptor && (s.HasExpectedType == nil || s.NoSymbolicLinkComponent == nil || !*s.NoSymbolicLinkComponent):
					t.Errorf("%s: another source lost its facts: %+v", s.Inside, s)
				}
			}
			if wrong != 1 {
				t.Errorf("%d sources with the wrong-type fact, want 1", wrong)
			}
			raw, err := os.ReadFile(r.out.Dir + "/summary.json")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"has_expected_type": false`) {
				t.Error("summary.json does not hold the fact")
			}
			// The link result of the case is not failed by a wrong type: no
			// link was found. It is not ok either, as one source has no link
			// fact.
			if st := g1Status(t, r.out, "bind-sources-no-symbolic-link"); st != assert.StatusNotChecked {
				t.Errorf("G1 links result is %s", st)
			}
		})
	}
}

// A link keeps its own fact, and gives no type fact: the walk never reached
// the type.
func TestALinkAtABindSourceGivesNoTypeFact(t *testing.T) {
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
	found := false
	for _, s := range r.out.Summary.BindSources.Sources {
		if s.Inside != profile.AssignmentInside {
			continue
		}
		found = true
		if s.NoSymbolicLinkComponent == nil || *s.NoSymbolicLinkComponent || s.HasExpectedType != nil {
			t.Errorf("the fact of a source that is a link: %+v", s)
		}
	}
	if !found {
		t.Error("no fact for the assignment folder")
	}
}

// A missing source has neither fact.
func TestAMissingBindSourceGivesNeitherFact(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	src := f.Config.Sources.AssignmentDir
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.CheckBindSources
		o.CheckBindSources = func(rows []mountpoints.ResolvedSource) mountpoints.Report {
			if err := os.Rename(src, src+".moved"); err != nil {
				t.Fatal(err)
			}
			return base(rows)
		}
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepBindSources, harness.StepFailed)
	for _, s := range r.out.Summary.BindSources.Sources {
		if s.Inside == profile.AssignmentInside && (s.NoSymbolicLinkComponent != nil || s.HasExpectedType != nil || !strings.Contains(s.Problem, "does not exist")) {
			t.Errorf("the fact of a missing source: %+v", s)
		}
	}
}

// A dry run builds and verifies the plan and checks nothing on the host: the
// plan's own facts are there, the facts of the check are not.
func TestDryRunHasTheWalksAndNoCheckedFact(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.NoLeadHasRun = true
	r := executeWith(t, f, newFake(nil), false, nil)
	mustNoErr(t, r)
	if len(r.out.Plan.Walks) < 20 {
		t.Errorf("the dry run's plan has %d walks", len(r.out.Plan.Walks))
	}
	ev := r.out.Summary.BindSources
	if ev.LinksChecked || len(ev.Sources) != 0 {
		t.Errorf("a dry run has checked facts: %+v", ev)
	}
	if ev.NoAtaniqSourceBoundByPath == nil || !*ev.NoAtaniqSourceBoundByPath {
		t.Errorf("the verifier's fact about descriptors in a dry run: %v", ev.NoAtaniqSourceBoundByPath)
	}
}
