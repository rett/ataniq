//go:build linux && ataniq_canary

package sandboxstart

import (
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadholder/canaryroot"
)

// TestCanaryRefusesAForeignLayoutRole shows the layout role of canary mode is
// the project's lead: another one is refused before any step.
func TestCanaryRefusesAForeignLayoutRole(t *testing.T) {
	for _, role := range []string{"other-lead", sampleProject + "-lead2", sampleProject, sampleProject + "-member"} {
		t.Run(role, func(t *testing.T) {
			w := newWorld(t)
			w.in.LayoutRole = role
			_, err := w.run_()
			if err == nil {
				t.Fatal("accepted")
			}
			var se *StepError
			if errors.As(err, &se) {
				t.Errorf("refused at the step %s, not before any step: %v", se.Step, err)
			}
			if !strings.Contains(err.Error(), "before any step") || !strings.Contains(err.Error(), "layout role") {
				t.Errorf("the refusal does not say it: %v", err)
			}
			if calls := w.sys.callList(); len(calls) != 0 {
				t.Errorf("the system was called: %v", calls)
			}
			var re *canaryroot.RefusedError
			if errors.As(err, &re) {
				t.Errorf("the root check refused, not the role check: %v", err)
			}
		})
	}
}
