//go:build linux && ataniq_canary

package sandboxstart

import (
	"ataniq/internal/leadsandbox/assert"
)

// The steps of the start sequence, in order. Each carries the name the step has
// in the qualification harness (qualification/leadsandbox/internal/harness,
// steps.go), so that the evidence of the two can be read side by side. A test
// compares this list with the harness's by reading that file as text; this
// package may not import it.
//
// The harness steps that have no counterpart here are attempt-folder (there is
// no campaign: the folders of the sequence are made by the steps resolv-conf
// and report-channel), planted-bind-paths (case S21), wait-exit (the holder
// serves the child, it does not wait for it) and write-evidence (the holder
// keeps its own event log). The judgement of the whole report, and the waiver,
// are part of the step outside-checks.
const (
	StepValidate    = "validate-config"
	StepVersions    = "tool-versions"
	StepOwnClone    = "own-clone"
	StepPrepare     = "prepare"
	StepCheck       = "check"
	StepResolv      = "resolv-conf"
	StepResolve     = "resolve-sources"
	StepChannel     = "report-channel"
	StepCanary      = "canary"
	StepBuild       = "build-plan"
	StepVerify      = "verify-plan"
	StepSpec        = "write-spec"
	StepRecheck     = "recheck"
	StepBindSources = "check-bind-sources"
	StepLaunch      = "launch"
	StepReadReport  = "read-report"
	StepOutside     = "outside-checks"
	StepStop        = "stop"
	StepTrailing    = "report-trailing"
	StepCleanup     = "cleanup"
)

// StepNames is every step of the sequence in the order it runs. Run records the
// steps it reached in this order; a step that was not needed is recorded as
// skipped, and a step after a failure is not recorded.
func StepNames() []string {
	return []string{
		StepValidate, StepVersions, StepOwnClone, StepPrepare, StepCheck, StepResolv, StepResolve,
		StepChannel, StepCanary, StepBuild, StepVerify, StepSpec, StepRecheck, StepBindSources, StepLaunch,
		StepReadReport, StepOutside, StepStop, StepTrailing, StepCleanup,
	}
}

// StepStatus is how a step ended. It says whether the sequence did what the
// step is for; it says nothing about the profile.
type StepStatus string

// The statuses of a step.
const (
	StepOK      StepStatus = "ok"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
)

// StepRecord is one step with how it ended. Error is the reason of a failed or
// skipped step, escaped and bounded.
type StepRecord struct {
	Name   string     `json:"name"`
	Status StepStatus `json:"status"`
	Error  string     `json:"error,omitempty"`
}

// StepError is the refusal of a start at one step. Step is the harness's name
// of the step; Err is the reason. Steps lists the steps reached, the failed one
// last, as Started.Steps does for a start that succeeded.
type StepError struct {
	Step  string
	Err   error
	Steps []StepRecord
}

// Error implements error.
func (e *StepError) Error() string {
	return "sandbox start refused at step " + e.Step + ": " + e.Err.Error()
}

// Unwrap returns the reason.
func (e *StepError) Unwrap() error { return e.Err }

// recorder keeps the steps in the order they were reached.
type recorder struct{ steps []StepRecord }

func (r *recorder) add(name string, st StepStatus, err error) {
	rec := StepRecord{Name: name, Status: st}
	if err != nil {
		rec.Error = assert.EscapeDetail(err.Error())
	}
	r.steps = append(r.steps, rec)
}

func (r *recorder) skip(name, reason string) {
	r.steps = append(r.steps, StepRecord{Name: name, Status: StepSkipped, Error: assert.EscapeDetail(reason)})
}

func (r *recorder) list() []StepRecord { return append([]StepRecord(nil), r.steps...) }
