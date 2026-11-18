//go:build linux && amd64

package harness_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// The checks of the host that the binding by descriptor brought: what the git
// identity folder may hold, and which bubblewrap is launched. Nothing here
// starts a sandbox tool: the launcher is the fake, and no bubblewrap is asked.

// The git identity folder is bound whole: it holds the file "config" and
// nothing else, or nothing is started.
func TestGitIdentityFolderHoldsTheFileAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, dir string)
		want  string
	}{
		{"another file", func(t *testing.T, dir string) {
			if err := os.WriteFile(dir+"/credentials", []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, `holds "credentials" besides config`},
		{"a hidden file", func(t *testing.T, dir string) {
			if err := os.WriteFile(dir+"/.git-credentials", []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, `holds ".git-credentials" besides config`},
		{"a folder", func(t *testing.T, dir string) {
			if err := os.Mkdir(dir+"/keys", 0o700); err != nil {
				t.Fatal(err)
			}
		}, `holds "keys" besides config`},
		{"a link to a file elsewhere", func(t *testing.T, dir string) {
			if err := os.Symlink("/etc/hostname", dir+"/extra"); err != nil {
				t.Fatal(err)
			}
		}, `holds "extra" besides config`},
		{"a backup of the file", func(t *testing.T, dir string) {
			if err := os.WriteFile(dir+"/config~", []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, `holds "config~" besides config`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			tc.plant(t, f.Config.Sources.GitIdentityDir)
			for _, run := range []bool{false, true} {
				fake := newFake(okProbe(assert.StatusOK, 0, 0))
				r := executeWith(t, f, fake, run, nil)
				var se *harness.StartError
				if !errors.As(r.err, &se) || se.Step != harness.StepValidate || r.out != nil {
					t.Fatalf("run %v: got %v (outcome %v), want a refusal at %s", run, r.err, r.out, harness.StepValidate)
				}
				for _, w := range []string{"sources.git_identity_dir", tc.want, "bound whole", "nothing else"} {
					if !strings.Contains(r.err.Error(), w) {
						t.Errorf("run %v: the refusal %q does not say %q", run, r.err, w)
					}
				}
				if fake.Calls() != 0 || len(f.Attempts()) != 0 {
					t.Errorf("run %v: %d launcher calls, attempts %v", run, fake.Calls(), f.Attempts())
				}
			}
		})
	}
	// The folder with the file alone is accepted (every other test), and a
	// folder without the file is refused as before.
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	if err := os.Remove(f.Config.Sources.GitIdentityDir + "/config"); err != nil {
		t.Fatal(err)
	}
	r := executeWith(t, f, newFake(nil), true, nil)
	var se *harness.StartError
	if !errors.As(r.err, &se) || !strings.Contains(r.err.Error(), "sources.git_identity_dir") {
		t.Errorf("a folder without the file: %v", r.err)
	}
}

// A bubblewrap whose release is known to lack --bind-fd is refused: the plan
// has no form that binds a source Ataniq supplies by path. The release is read
// from the version the launcher reports; no bubblewrap is asked here.
func TestABubblewrapTooOldForDescriptorBindsIsNotLaunched(t *testing.T) {
	for _, tc := range []struct {
		version string
		refused bool
		note    bool
	}{
		{"bubblewrap 0.9.0", true, false},
		{"bubblewrap 0.9.99", true, false},
		{"bubblewrap 0.4.1", true, false},
		{"bubblewrap 0.0-fake", true, false},
		{"bubblewrap " + harness.MinBwrapBindFD, false, false},
		{"bubblewrap 0.10.1", false, false},
		{"bubblewrap 0.11.0", false, false},
		{"bubblewrap 0.13.1", false, false},
		{"bubblewrap 1.0.0", false, false},
		{"bubblewrap 1.2", false, false},
		// A line that gives no release decides nothing: the launch goes on
		// and the summary says that it is not known.
		{"", false, true},
		{"bwrap 0.11.0", false, true},
		{"bubblewrap", false, true},
		{"bubblewrap unknown", false, true},
		{"bubblewrap 11", false, true},
		{"0.11.0", false, true},
	} {
		t.Run(strconv.Quote(tc.version), func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			fake := newFake(okProbe(assert.StatusOK, 0, 0))
			fake.Versions.Bwrap = tc.version
			r := executeWith(t, f, fake, true, nil)
			mustNoErr(t, r)
			if r.out.Summary.Versions.Bwrap != tc.version {
				t.Errorf("the recorded version is %q", r.out.Summary.Versions.Bwrap)
			}
			noted := false
			for _, n := range r.out.Summary.Notes {
				if strings.Contains(n, "--bind-fd") && strings.Contains(n, "not known") {
					noted = true
				}
			}
			if noted != tc.note {
				t.Errorf("a note that the release is not known: %v, want %v (%q)", noted, tc.note, r.out.Summary.Notes)
			}
			if !tc.refused {
				wantStep(t, r.out, harness.StepVersions, harness.StepOK)
				wantStep(t, r.out, harness.StepLaunch, harness.StepOK)
				if fake.Launches() != 1 {
					t.Errorf("%d launches", fake.Launches())
				}
				return
			}
			wantStep(t, r.out, harness.StepVersions, harness.StepFailed)
			e := step(t, r.out, harness.StepVersions).Error
			for _, w := range []string{"older than " + harness.MinBwrapBindFD, "--bind-fd", "--ro-bind-fd", "nothing is launched"} {
				if !strings.Contains(e, w) {
					t.Errorf("the step's error %q does not say %q", e, w)
				}
			}
			// Nothing after it runs: no folder of the lead is prepared, no
			// channel, no canary, no launch.
			for _, n := range []string{harness.StepOwnClone, harness.StepPrepare, harness.StepChannel, harness.StepCanary, harness.StepBuild, harness.StepBindSources, harness.StepLaunch} {
				wantStep(t, r.out, n, harness.StepSkipped)
			}
			if fake.Launches() != 0 || r.canaries != 0 || !r.out.StepFailed {
				t.Errorf("launches %d, canaries %d, step failed %v", fake.Launches(), r.canaries, r.out.StepFailed)
			}
		})
	}
	if harness.MinBwrapBindFD != "0.10.0" {
		t.Errorf("the minimum release is %q: the tests and the documentation say 0.10.0", harness.MinBwrapBindFD)
	}
}

// A version that could not be read at all is, as before, a fact and not a
// reason to stop: the step fails softly and the run goes on.
func TestAnUnreadBubblewrapVersionDoesNotStopTheRun(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	fake := newFake(okProbe(assert.StatusOK, 0, 0))
	fake.Versions.Bwrap = ""
	fake.Versions.Errors = []string{"bwrap --version: it did not answer"}
	r := executeWith(t, f, fake, true, nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepVersions, harness.StepFailed)
	wantStep(t, r.out, harness.StepLaunch, harness.StepOK)
	if fake.Launches() != 1 {
		t.Errorf("%d launches", fake.Launches())
	}
}

// A dry run asks no tool for its version, so it decides nothing about
// bubblewrap.
func TestDryRunDoesNotAskBubblewrap(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.NoLeadHasRun = true
	fake := newFake(nil)
	fake.Versions.Bwrap = "bubblewrap 0.1.0"
	r := executeWith(t, f, fake, false, nil)
	mustNoErr(t, r)
	if fake.VersionCalls() != 0 || r.out.StepFailed {
		t.Errorf("%d version calls, step failed %v", fake.VersionCalls(), r.out.StepFailed)
	}
	wantStep(t, r.out, harness.StepVersions, harness.StepSkipped)
}
