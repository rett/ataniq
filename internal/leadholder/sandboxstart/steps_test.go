//go:build linux && ataniq_canary

package sandboxstart

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// harnessSteps reads the step names of the qualification harness from its
// source, as text: this package may not import it. The names are the string
// constants of its const block, in the order the file declares them, which is
// the order the harness runs them.
func harnessSteps(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../../qualification/leadsandbox/internal/harness/steps.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s+Step[A-Za-z]+\s+=\s+"([a-z][a-z-]*)"\s*$`)
	var names []string
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		names = append(names, m[1])
	}
	if len(names) < 20 {
		t.Fatalf("read only %d step names from the harness source: %v", len(names), names)
	}
	return names
}

// TestStepNamesAreTheHarnessNames compares the steps of the sequence with the
// harness's: every name is a harness name, in the harness's order, and the
// harness steps this package does not have are the four the package comment
// names.
func TestStepNamesAreTheHarnessNames(t *testing.T) {
	harness := harnessSteps(t)
	at := map[string]int{}
	for i, n := range harness {
		at[n] = i
	}
	mine := StepNames()
	last := -1
	for _, n := range mine {
		i, ok := at[n]
		if !ok {
			t.Errorf("step %q is not a step of the harness", n)
			continue
		}
		if i <= last {
			t.Errorf("step %q is out of the harness's order (position %d after %d)", n, i, last)
		}
		last = i
	}
	have := map[string]bool{}
	for _, n := range mine {
		have[n] = true
	}
	var missing []string
	for _, n := range harness {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	want := "attempt-folder,planted-bind-paths,wait-exit,write-evidence"
	if strings.Join(missing, ",") != want {
		t.Errorf("the harness steps with no counterpart are %v, want %s", missing, want)
	}
}

// TestStepConstantsAreInOrder checks the exported constants are the list.
func TestStepConstantsAreInOrder(t *testing.T) {
	want := []string{
		StepValidate, StepVersions, StepOwnClone, StepPrepare, StepCheck, StepResolv, StepResolve,
		StepChannel, StepCanary, StepBuild, StepVerify, StepSpec, StepRecheck, StepBindSources, StepLaunch,
		StepReadReport, StepOutside, StepStop, StepTrailing, StepCleanup,
	}
	got := StepNames()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("StepNames = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("step %q is listed twice", n)
		}
		seen[n] = true
	}
}
