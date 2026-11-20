//go:build linux && amd64

package harness_test

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// The steps check and recheck make their walk of the mount points through
// Options.CheckMountPoints, so a test can stand in for a walk that decides
// otherwise; nil means mountpoints.Check.
func TestTheStepsWalkThroughTheCheckMountPointsSeam(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	var calls atomic.Int32
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		o.CheckMountPoints = func(in profile.Inputs) (mountpoints.Report, error) {
			calls.Add(1)
			return mountpoints.Check(in)
		}
	})
	mustNoErr(t, r)
	if got := calls.Load(); got != 2 {
		t.Errorf("the seam was called %d times, want 2 (the steps %s and %s)", got, harness.StepCheck, harness.StepRecheck)
	}
	wantStep(t, r.out, harness.StepCheck, harness.StepOK)
	wantStep(t, r.out, harness.StepRecheck, harness.StepOK)
}

func TestAWalkThatRefusesEndsTheStartThroughTheSeam(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	planted := f.Layout().Tree() + "/.git/config"
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		o.CheckMountPoints = func(profile.Inputs) (mountpoints.Report, error) {
			// The walk is made by the seam alone: the folders hold nothing wrong.
			return mountpoints.Report{Problems: []*mountpoints.Problem{{Kind: mountpoints.ErrSymlink, Path: planted, Detail: "stand-in"}}}, nil
		}
	})
	mustNoErr(t, r)
	if r.fake.Launches() != 0 {
		t.Fatalf("a refused walk launched %d times", r.fake.Launches())
	}
	st := step(t, r.out, harness.StepCheck)
	if st.Status != harness.StepFailed || !strings.Contains(st.Error, planted) {
		t.Errorf("step %s: %+v", harness.StepCheck, st)
	}
	if _, err := os.Lstat(planted); err != nil {
		t.Errorf("the walk's stand-in changed the folders: %v", err)
	}
}
