//go:build linux && amd64

package harness_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

const probeDocPath = "../../../../cmd/ataniq-probe/doc.go"

// optionWords are the words of a documented command line that name an option:
// those that start with two hyphens, outside the "--" that ends the options,
// with the brackets of an optional part taken off, up to that "--".
func optionWords(line string) []string {
	var out []string
	for _, field := range strings.Fields(line) {
		if field == "--" {
			break
		}
		if w := strings.TrimPrefix(field, "["); strings.HasPrefix(w, "--") {
			out = append(out, w)
		}
	}
	return out
}

// The plan's command line is read by the probe, which is a program of its own
// built elsewhere: the words the plan writes must be the ones its documented
// command lines name. The probe's package main is not imported; its doc
// comment is the contract.
func TestPlanUsesTheOptionWordsTheProbeParses(t *testing.T) {
	data, err := os.ReadFile(probeDocPath)
	if err != nil {
		t.Fatal(err)
	}
	var inside, passFDs string
	for _, m := range regexp.MustCompile(`(?m)^//\t(ataniq-probe .*)$`).FindAllStringSubmatch(string(data), -1) {
		switch {
		case strings.HasPrefix(m[1], "ataniq-probe "+plan.PassFDsMode+" "):
			passFDs = m[1]
		case strings.Contains(m[1], " --spec "):
			inside = m[1]
		}
	}
	if inside == "" || passFDs == "" {
		t.Fatalf("the probe's documentation has no command line for each mode: inside %q, pass-through %q", inside, passFDs)
	}

	// A seccomp file makes the plan use both options of the pass-through mode.
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	bpf := f.Owned + "/filter.bpf"
	f.Write(bpf, "reviewed-filter-bytes", 0o644)
	f.Config.Sources.Seccomp = &harness.Seccomp{File: bpf, SHA256: sha(t, "reviewed-filter-bytes"), ReviewedBPF: true}
	r := executeWith(t, f, newFake(nil), false, nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	argv := r.out.Plan.Argv
	index := func(from int, word string) int {
		for i := from; i < len(argv); i++ {
			if argv[i] == word {
				return i
			}
		}
		t.Fatalf("the plan has no %q after element %d: %q", word, from, argv)
		return -1
	}
	// optionsAt returns the option words the plan writes from the element
	// after at up to the next "--".
	optionsAt := func(at int) (words, values []string) {
		for i := at + 1; i < len(argv) && argv[i] != "--"; i++ {
			if strings.HasPrefix(argv[i], "--") {
				words = append(words, argv[i])
				if i+1 < len(argv) {
					values = append(values, argv[i+1])
				}
			}
		}
		return words, values
	}

	// The pass-through mode: the probe on the host, then its mode word, then
	// the options the documentation lists, each with FD=PATH.
	mode := index(0, plan.PassFDsMode)
	if mode == 0 || argv[mode-1] != f.Config.Executables.Probe {
		t.Errorf("the mode word %q does not follow the probe's path: %q", plan.PassFDsMode, argv[:mode+1])
	}
	if fields := strings.Fields(passFDs); len(fields) < 2 || fields[1] != plan.PassFDsMode {
		t.Errorf("the documented mode word is not %q: %q", plan.PassFDsMode, passFDs)
	}
	// The probe may document more options than the plan uses; every option
	// the plan names and writes must be a documented one.
	documented := optionWords(passFDs)
	for _, w := range []string{plan.PassFDsRead, plan.PassFDsWrite} {
		if !contains(documented, w) {
			t.Errorf("the plan names the option %q, and the probe documents %q for its pass-through mode", w, documented)
		}
	}
	words, values := optionsAt(mode)
	seen := map[string]bool{}
	for i, w := range words {
		seen[w] = true
		if !contains(documented, w) {
			t.Errorf("the plan passes %q to the pass-through mode, which the probe does not document", w)
		}
		if !regexp.MustCompile(`^[0-9]+=/`).MatchString(values[i]) {
			t.Errorf("the value of %s is %q, not FD=PATH", w, values[i])
		}
	}
	if !seen[plan.PassFDsRead] || !seen[plan.PassFDsWrite] {
		t.Errorf("the plan does not use both options (the seccomp file and the report channel): %q", words)
	}

	// The inside mode: the probe's path inside, then its options, in the
	// order the documentation gives, each used once.
	// (The path also appears as a bind target; the command is the last.)
	probe := -1
	for i, w := range argv {
		if w == profile.ProbeInside {
			probe = i
		}
	}
	if probe < 0 {
		t.Fatalf("the plan has no %q: %q", profile.ProbeInside, argv)
	}
	words, values = optionsAt(probe)
	documented = optionWords(inside)
	if strings.Join(words, " ") != strings.Join(documented, " ") {
		t.Errorf("the plan passes the options %q to the probe inside, the documented command line has %q", words, documented)
	}
	if !contains(words, plan.ProbeReportFD) || !contains(documented, plan.ProbeReportFD) {
		t.Errorf("%q is not on both sides: plan %q, documentation %q", plan.ProbeReportFD, words, documented)
	}
	for i, w := range words {
		switch w {
		case "--spec":
			if values[i] != profile.SpecFileInside {
				t.Errorf("--spec is %q, not %q", values[i], profile.SpecFileInside)
			}
		case plan.ProbeReportFD:
			if values[i] != strconv.Itoa(harness.ReportFD) {
				t.Errorf("%s is %q, the harness's descriptor is %d", w, values[i], harness.ReportFD)
			}
		}
	}
	// The agent's command follows the probe's "--".
	end := index(probe, "--")
	if got := strings.Join(argv[end+1:], " "); got != strings.Join(harness.DefaultScratchCommand(), " ") {
		t.Errorf("after the probe's options: %q", got)
	}
}
