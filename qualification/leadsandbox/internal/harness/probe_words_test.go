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
	// the options the documentation lists: a file option with FD=PATH, a walk
	// option with a descriptor, a folder and the path beneath it.
	mode := index(0, plan.PassFDsMode)
	if mode == 0 || argv[mode-1] != f.Config.Executables.Probe {
		t.Errorf("the mode word %q does not follow the probe's path: %q", plan.PassFDsMode, argv[:mode+1])
	}
	if fields := strings.Fields(passFDs); len(fields) < 2 || fields[1] != plan.PassFDsMode {
		t.Errorf("the documented mode word is not %q: %q", plan.PassFDsMode, passFDs)
	}
	// The probe documents exactly the options the plan names, each with the
	// operands the plan writes.
	documented := optionWords(passFDs)
	named := []string{plan.PassFDsRead, plan.PassFDsWrite, plan.PassFDsWalkDir, plan.PassFDsWalkFile}
	if strings.Join(documented, " ") != strings.Join(named, " ") {
		t.Errorf("the probe documents the options %q for its pass-through mode, the plan names %q", documented, named)
	}
	for _, form := range []string{
		"[" + plan.PassFDsRead + " FD=PATH]...", "[" + plan.PassFDsWrite + " FD=PATH]...",
		"[" + plan.PassFDsWalkDir + " FD ROOT REL]...", "[" + plan.PassFDsWalkFile + " FD ROOT REL]...",
	} {
		if !strings.Contains(passFDs, form) {
			t.Errorf("the documented command line %q does not have %q", passFDs, form)
		}
	}
	words, values := optionsAt(mode)
	seen := map[string]bool{}
	number := regexp.MustCompile(`^[1-9][0-9]*$`)
	end := index(mode, "--")
	for i := mode + 1; i < end; {
		w := argv[i]
		seen[w] = true
		switch w {
		case plan.PassFDsRead, plan.PassFDsWrite:
			if i+1 >= end || !regexp.MustCompile(`^[0-9]+=/`).MatchString(argv[i+1]) {
				t.Fatalf("the value of %s at element %d is not FD=PATH: %q", w, i, argv[i:end])
			}
			i += 2
		case plan.PassFDsWalkDir, plan.PassFDsWalkFile:
			if i+3 >= end || !number.MatchString(argv[i+1]) || !strings.HasPrefix(argv[i+2], "/") || argv[i+3] == "" || strings.HasPrefix(argv[i+3], "/") || strings.HasPrefix(argv[i+3], "-") {
				t.Fatalf("the values of %s at element %d are not a descriptor, a folder and a path beneath it: %q", w, i, argv[i:end])
			}
			i += 4
		default:
			t.Fatalf("the plan passes %q to the pass-through mode at element %d, which the probe does not document", w, i)
		}
	}
	for _, w := range named {
		if !seen[w] {
			t.Errorf("the plan does not use the option %s (the seccomp file, the report channel, a source that is a folder and one that is a file): %q", w, words)
		}
	}
	_ = values

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
	last := index(probe, "--")
	if got := strings.Join(argv[last+1:], " "); got != strings.Join(harness.DefaultScratchCommand(), " ") {
		t.Errorf("after the probe's options: %q", got)
	}
}
