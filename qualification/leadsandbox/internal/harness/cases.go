//go:build linux && amd64

package harness

import (
	"fmt"

	"ataniq/internal/leadsandbox/assert"
)

// caseNotes say what else in the evidence bears on an automated case.
var caseNotes = map[string][]string{
	"S1":  {"results holder-hidden and owner-paths-absent: the probe's look from inside, and the harness's look at the bind sources"},
	"S2":  {"the canary counts in outside.json and summary.json are the evidence that does not depend on the probe's own judgement"},
	"S4":  {"the resolv.conf derivation of this run is in summary.json (resolv_conf); a derivation that was refused ends the run before a launch, with the reason in the step"},
	"S7":  {"the sysctl or filter fact only: the harness never issues TIOCSTI"},
	"S11": {"the harness has no qualification record store: the result is not-checked, and it says so"},
}

// casesFor lists, for each requested case, the results the evidence holds that
// speak to it, by the table of assertions (each assertion names the cases it
// maps to). A case whose results are missing (the probe never reported) lists
// what is there, which may be nothing.
func casesFor(requested []string, results []assert.Result) []CaseEvidence {
	var out []CaseEvidence
	for _, c := range requested {
		ev := CaseEvidence{Case: c, Results: []CaseResult{}, Notes: caseNotes[c]}
		for _, r := range results {
			a, ok := assert.ByNumber(r.Number)
			if !ok {
				continue
			}
			for _, ac := range a.Cases {
				if ac == c {
					ev.Results = append(ev.Results, CaseResult{ID: r.ID, Status: r.Status})
				}
			}
		}
		if len(ev.Results) == 0 {
			ev.Notes = append(append([]string{}, ev.Notes...), fmt.Sprintf("no result of case %s was recorded in this run", c))
		}
		out = append(out, ev)
	}
	return out
}
