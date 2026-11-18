//go:build linux && amd64

package harness

import (
	"fmt"

	"ataniq/internal/leadsandbox/assert"
)

// caseNotes say what else in the evidence bears on an automated case.
var caseNotes = map[string][]string{
	"S1": {
		"results holder-hidden and owner-paths-absent: the probe's look from inside, and the harness's look at the bind sources",
		"result parent-folders (assertion 11, a result of this case by amendment H2 of the coordinator note): the four parent folders inside, and the probe's look for a coordinator's tree or home; the identities it compares are of the coordinators' folders that existed when the spec was written (coordinator_identities in spec.json)",
	},
	"S2":  {"the canary counts in outside.json and summary.json are the evidence that does not depend on the probe's own judgement"},
	"S4":  {"the resolv.conf derivation of this run is in summary.json (resolv_conf); a derivation that was refused ends the run before a launch, with the reason in the step"},
	"S7":  {"the sysctl or filter fact only: the harness never issues TIOCSTI"},
	"S11": {"the harness has no qualification record store: the result is not-checked, and it says so"},
	"S21": {
		"the facts are planted_paths in summary.json: for every path the start walk looks at (mountpoints.List, and the tree's .git folder, which the walk looks at too) and each kind of plant (a symbolic link to a path outside, and an entry of the wrong type: a regular file where a folder belongs, a folder where a file belongs), whether the start was refused, which checks refused, the problem the start walk reported and the path it names, that nothing was launched, and whether the original was put back",
		"the results are planted-paths-refused (the configured runtime: the checks of a start, the walk of the mount points deciding), planted-paths-refused-other-runtime (the other runtime, by the walk alone, at the paths in folders the configuration names: the .git paths and the config folder's place in the home) and planted-paths-restored; each is ok, failed or not-checked, and a plant that could not be made or a path that could not be restored is never ok",
		"not covered: the places of the skills and the instruction file in the other runtime's config folder, where this run neither prepares nor writes, because it plants only in the folders the configured runtime's walk names (a second run with the other runtime's configuration; not_covered says which); a lead that plants from inside (the harness plants from outside, as a stand-in, and the attended variant, a lead plants in one run and the next run is refused, is a manual procedure); the coordinator repeat and the mirror fetch's gitdir leg, which belong to other features",
		"the case plants in the scratch folders and puts them back before the steps that follow; it needs --run, coordinator_reviewed and scratch_folders_disposable, does nothing in a dry run, and when it is the only case requested the run launches nothing",
	},
	"G1": {
		"the per-source facts are bind_sources in summary.json: for every bind source of the plan, whether any component of its path is a symbolic link (checked right before the launch without following a link: a source below the state root beneath the state root's handle, any other from the root of the file system; the fact is absent for a source on which the check found another problem), and whether any source lies in or below a read-write source (plan.Verify)",
		"bind_sources also says, per source, whether the plan binds it from a descriptor that its pass-through step walks to by handle (every source Ataniq supplies) or by path (a row of the host), with the walk, and whether the check found it to be of the type its row binds; these are facts about the plan and about the check before the launch: whether bubblewrap mounts the object behind a descriptor is not among them",
	},
}

// The ids of the results of case G1. They are the harness's own facts, not the
// probe's: they have no number in the table of assertions.
const (
	resultG1NoLink     = "bind-sources-no-symbolic-link"
	resultG1NoWritable = "no-bind-source-in-read-write-source"
)

// g1Results turns the facts about the bind sources into the results of case G1.
// A fact that was not asked is not-checked, never ok.
func g1Results(ev BindSourcesEvidence) []CaseResult {
	links := assert.StatusOK
	switch {
	case !ev.LinksChecked || len(ev.Sources) == 0:
		links = assert.StatusNotChecked
	default:
		for _, s := range ev.Sources {
			if s.NoSymbolicLinkComponent == nil {
				if links == assert.StatusOK {
					links = assert.StatusNotChecked
				}
			} else if !*s.NoSymbolicLinkComponent {
				links = assert.StatusFailed
				break
			}
		}
	}
	rw := assert.StatusNotChecked
	if ev.NoSourceInReadWriteSource != nil {
		rw = assert.StatusFailed
		if *ev.NoSourceInReadWriteSource {
			rw = assert.StatusOK
		}
	}
	return []CaseResult{{ID: resultG1NoLink, Status: links}, {ID: resultG1NoWritable, Status: rw}}
}

// casesFor lists, for each requested case, the results the evidence holds that
// speak to it, by the table of assertions (each assertion names the cases it
// maps to), then the harness's own results for it (own). A case whose results are missing (the probe never reported) lists
// what is there, which may be nothing.
func casesFor(requested []string, results []assert.Result, own map[string][]CaseResult) []CaseEvidence {
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
		ev.Results = append(ev.Results, own[c]...)
		if len(ev.Results) == 0 {
			ev.Notes = append(append([]string{}, ev.Notes...), fmt.Sprintf("no result of case %s was recorded in this run", c))
		}
		out = append(out, ev)
	}
	return out
}
