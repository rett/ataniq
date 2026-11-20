//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
)

// TestVerifyPlanRefusesBeforeLaunch shows the two checks of the step verify-plan
// that guard the launch: with either gone the start would still be refused, but
// only at outside-checks, after the agent had been launched.
func TestVerifyPlanRefusesBeforeLaunch(t *testing.T) {
	cases := map[string]func(w *world){
		"a holder path is a source": func(w *world) { w.in.HolderPaths = []string{w.in.Sources.LeadSkills} },
		"a source in the runtime folder": func(w *world) {
			w.in.Sources.LeadSkills = RuntimeFolder(w.run, sampleRole) + "/skills"
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			set(w)
			_, err := w.run_()
			var se *StepError
			if !errors.As(err, &se) || se.Step != StepVerify {
				t.Fatalf("error %v, want a refusal at %s", err, StepVerify)
			}
			if w.lch.launches() != 0 {
				t.Error("launched")
			}
		})
	}
}

// TestReadReportRefusesAnotherDigest shows a report line for another profile
// digest than the spec's is refused at read-report, before the outside checks.
func TestReadReportRefusesAnotherDigest(t *testing.T) {
	w := newWorld(t)
	other := &otherDigestSystem{fakeSystem: w.sys}
	_, err := Run(context.Background(), w.in, other, w.lch)
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepReadReport {
		t.Fatalf("error %v, want a refusal at %s", err, StepReadReport)
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("the refusal does not name the digest: %v", err)
	}
	for _, r := range se.Steps {
		if r.Name == StepOutside {
			t.Errorf("the step %s ran after the refusal", StepOutside)
		}
	}
	if k := w.lch.kids[0]; len(k.stops) != 1 || k.closes != 1 {
		t.Errorf("child stops %v closes %d", k.stops, k.closes)
	}
}

// otherDigestSystem gives a report channel whose line is for another digest.
type otherDigestSystem struct{ *fakeSystem }

func (s *otherDigestSystem) OpenReport(runtimeRoot, layoutRole string) (ReportChannel, error) {
	c, err := s.fakeSystem.OpenReport(runtimeRoot, layoutRole)
	if err != nil {
		return nil, err
	}
	return &otherDigestChannel{ReportChannel: c, inner: c.(*fakeChannel)}, nil
}

type otherDigestChannel struct {
	ReportChannel
	inner *fakeChannel
}

func (c *otherDigestChannel) ReadLine(ctx context.Context, exited <-chan struct{}) (assert.Report, error) {
	return c.inner.line(strings.Repeat("1", 64)), nil
}
