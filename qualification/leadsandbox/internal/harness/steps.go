//go:build linux && amd64

package harness

import (
	"time"

	"ataniq/internal/leadsandbox/assert"
)

// StepStatus is how a step ended.
type StepStatus string

// The statuses of a step. They say whether the harness did what the step is
// for; they say nothing about the profile.
const (
	StepOK          StepStatus = "ok"
	StepFailed      StepStatus = "failed"
	StepTimeout     StepStatus = "timeout"
	StepInterrupted StepStatus = "interrupted"
	StepSkipped     StepStatus = "skipped"
)

// The steps, in order.
const (
	StepValidate      = "validate-config"
	StepAttempt       = "attempt-folder"
	StepVersions      = "tool-versions"
	StepOwnClone      = "own-clone"
	StepPrepare       = "prepare"
	StepCheck         = "check"
	StepResolv        = "resolv-conf"
	StepResolve       = "resolve-sources"
	StepChannel       = "report-channel"
	StepCanary        = "canary"
	StepBuild         = "build-plan"
	StepVerify        = "verify-plan"
	StepSpec          = "write-spec"
	StepRecheck       = "recheck"
	StepBindSources   = "check-bind-sources"
	StepLaunch        = "launch"
	StepReadReport    = "read-report"
	StepWaitExit      = "wait-exit"
	StepOutside       = "outside-checks"
	StepStop          = "stop"
	StepTrailing      = "report-trailing"
	StepCleanup       = "cleanup"
	StepWriteEvidence = "write-evidence"
)

// stepOrder is every step in the order it runs; a step that did not run is
// recorded as skipped, in its place.
var stepOrder = []string{
	StepValidate, StepAttempt, StepVersions, StepOwnClone, StepPrepare, StepCheck, StepResolv, StepResolve,
	StepChannel, StepCanary, StepBuild, StepVerify, StepSpec, StepRecheck, StepBindSources, StepLaunch,
	StepReadReport, StepWaitExit, StepOutside, StepStop, StepTrailing, StepCleanup, StepWriteEvidence,
}

// StepRecord is one step with its start, end, status and error text.
type StepRecord struct {
	Name   string     `json:"name"`
	Start  string     `json:"start"`
	End    string     `json:"end"`
	Status StepStatus `json:"status"`
	// Error is the reason of a failed, timed-out or skipped step, bounded
	// and escaped.
	Error string `json:"error,omitempty"`
}

// recorder keeps the steps that ran.
type recorder struct {
	now   func() time.Time
	steps map[string]StepRecord
}

func newRecorder(now func() time.Time) *recorder {
	return &recorder{now: now, steps: map[string]StepRecord{}}
}

const timeFormat = time.RFC3339Nano

// run records a step that runs fn: ok when it returns no error, failed with
// the error text otherwise. fn may return another status with its error (a
// timeout, an interruption).
func (r *recorder) run(name string, fn func() (StepStatus, error)) StepStatus {
	start := r.now()
	st, err := fn()
	if err != nil && st == StepOK {
		st = StepFailed
	}
	rec := StepRecord{Name: name, Start: start.UTC().Format(timeFormat), End: r.now().UTC().Format(timeFormat), Status: st}
	if err != nil {
		rec.Error = assert.EscapeDetail(err.Error())
	}
	r.steps[name] = rec
	return st
}

// skip records a step that did not run, with the reason.
func (r *recorder) skip(name, reason string) {
	t := r.now().UTC().Format(timeFormat)
	r.steps[name] = StepRecord{Name: name, Start: t, End: t, Status: StepSkipped, Error: reason}
}

// ran says the step has a record.
func (r *recorder) ran(name string) bool {
	_, ok := r.steps[name]
	return ok
}

// list is every step in order; a step with no record is skipped, with the
// reason given.
func (r *recorder) list(reason string) []StepRecord {
	out := make([]StepRecord, 0, len(stepOrder))
	for _, n := range stepOrder {
		if rec, ok := r.steps[n]; ok {
			out = append(out, rec)
			continue
		}
		t := r.now().UTC().Format(timeFormat)
		out = append(out, StepRecord{Name: n, Start: t, End: t, Status: StepSkipped, Error: reason})
	}
	return out
}

// anyFailed says some step failed, timed out or was interrupted.
func (r *recorder) anyFailed() bool {
	for _, rec := range r.steps {
		switch rec.Status {
		case StepFailed, StepTimeout, StepInterrupted:
			return true
		}
	}
	return false
}
