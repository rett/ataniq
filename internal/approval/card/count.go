package card

import (
	"math"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

// CountInput turns a summary into the plain numbers the limits package
// counts with. suiteClean says whether the change has a clean suite record.
// A truncated summary is marked as such, so it never meets the terms. A
// number too large for its field is set to the largest value of the field,
// which no terms allow.
func CountInput(s record.Summary, suiteClean bool) limits.CountInput {
	return limits.CountInput{
		Files:        toInt(s.Files),
		ChangedLines: toInt(sum(s.Added, s.Removed)),
		NewBytes:     s.NewBytes,
		Flagged:      len(s.Flagged),
		Binary:       len(s.Binary),
		Symlinks:     len(s.Symlinks),
		ModeChanges:  len(s.ModeChanges),
		Submodules:   len(s.Submodules),
		SuiteClean:   suiteClean,
		Truncated:    s.PathsTruncated,
	}
}

// BindingCountInput is CountInput for the summary of a binding. A binding
// has a clean suite record when its evidence names one: the host writes no
// pending record for a merge whose suite did not pass. ok is false for a
// binding without a summary (a prune), which the count does not cover.
func BindingCountInput(b record.Binding) (in limits.CountInput, ok bool) {
	if b.Summary == nil {
		return limits.CountInput{}, false
	}
	suite := false
	for _, e := range b.Evidence {
		if e.Kind == record.EvidenceSuite {
			suite = true
		}
	}
	return CountInput(*b.Summary, suite), true
}

// sum adds two counts. A negative count stays negative, so that the limits
// package sees it; a sum too large for an int64 is the largest int64.
func sum(a, b int64) int64 {
	if a < 0 {
		return a
	}
	if b < 0 {
		return b
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// toInt narrows a count to an int without wrapping around.
func toInt(n int64) int {
	if n > math.MaxInt {
		return math.MaxInt
	}
	if n < math.MinInt {
		return math.MinInt
	}
	return int(n)
}
