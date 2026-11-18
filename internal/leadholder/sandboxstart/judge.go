//go:build linux && ataniq_canary

package sandboxstart

import (
	"ataniq/internal/leadsandbox/assert"
)

// judge judges the whole report, the probe's results and the outside ones, as
// the holder judges a start (assert.Problems with every result required), and
// says why it does not allow one; the list is empty when it does.
//
// In canary mode, and only then, assertion 10 is excused: the result of the
// qualification record that is not ok (no record exists in the slice, and no
// store is asked) is read as ok, and waived is true. Nothing else is: a result
// of any other assertion that is failed, not checked or missing, a second
// result of assertion 10, a digest that is not the spec's, all stay problems.
// The waiver changes no result: the caller keeps the report as it is.
func judge(rep assert.Report, spec assert.Spec, canary bool) (waived bool, problems []string) {
	if canary {
		if i := qualificationResult(rep); i >= 0 && rep.Results[i].Status != assert.StatusOK {
			excused := rep
			excused.Results = append([]assert.Result(nil), rep.Results...)
			excused.Results[i].Status = assert.StatusOK
			return true, assert.Problems(excused, spec, assert.RequireAll)
		}
	}
	return false, assert.Problems(rep, spec, assert.RequireAll)
}

// qualificationResult is the index of the one result of assertion 10 in the
// report, or -1 when there is none or more than one.
func qualificationResult(rep assert.Report) int {
	at := -1
	for i, r := range rep.Results {
		if r.Number != qualificationNumber && r.ID != assert.IDQualificationRecord {
			continue
		}
		if at >= 0 {
			return -1
		}
		at = i
	}
	if at >= 0 && (rep.Results[at].Number != qualificationNumber || rep.Results[at].ID != assert.IDQualificationRecord) {
		return -1
	}
	return at
}

// qualificationNumber is the number of the assertion about the qualification
// record in the table of the design.
const qualificationNumber = 10
