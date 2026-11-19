package card

import (
	"math"
	"reflect"
	"testing"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

func TestCountInput(t *testing.T) {
	s := record.Summary{
		Files: 14, Added: 612, Removed: 88, NewBytes: 48211,
		Flagged:     []record.Flag{{Kind: record.FlagExecConfig, Path: "Makefile"}},
		Binary:      []record.Text{"a", "b"},
		Symlinks:    []record.Text{"a", "b", "c"},
		ModeChanges: []record.Text{"a", "b", "c", "d"},
		Submodules:  []record.Text{"a", "b", "c", "d", "e"},
	}
	want := limits.CountInput{
		Files: 14, ChangedLines: 700, NewBytes: 48211,
		Flagged: 1, Binary: 2, Symlinks: 3, ModeChanges: 4, Submodules: 5,
		SuiteClean: true,
	}
	if got := CountInput(s, true); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	want.SuiteClean = false
	if got := CountInput(s, false); got != want {
		t.Errorf("without a clean suite: got %+v, want %+v", got, want)
	}
	s.PathsTruncated = true
	want.Truncated = true
	if got := CountInput(s, false); got != want {
		t.Errorf("truncated: got %+v, want %+v", got, want)
	}

	// Every field of the input is filled: a field added to the input
	// later stays zero here, and fails the test until it is.
	filled := reflect.ValueOf(CountInput(s, true))
	for i := 0; i < filled.NumField(); i++ {
		if filled.Field(i).IsZero() {
			t.Errorf("CountInput leaves %s empty", filled.Type().Field(i).Name)
		}
	}
	if got := CountInput(record.Summary{}, false); got != (limits.CountInput{}) {
		t.Errorf("an empty summary: got %+v", got)
	}
}

// The count's answer for cards: a small clean change meets the terms, and
// a flag, a cut list or a large change does not.
func TestCountInputThroughLimits(t *testing.T) {
	clean := rawPending()
	clean.Binding.Summary.Flagged = []record.Flag{}
	in, ok := BindingCountInput(clean.Binding)
	if !ok || !limits.Count(in).Met {
		t.Errorf("a small clean change: %+v, %v, %s", in, ok, limits.Count(in).Line())
	}

	for name, change := range map[string]func(b *record.Binding){
		"a flagged path":      func(b *record.Binding) {},
		"a cut path list":     func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.PathsTruncated = true },
		"21 files":            func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Files = 21 },
		"801 changed lines":   func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Added, b.Summary.Removed = 400, 401 },
		"a binary file":       func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Binary = []record.Text{"x"} },
		"a symbolic link":     func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Symlinks = []record.Text{"x"} },
		"a mode change":       func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.ModeChanges = []record.Text{"x"} },
		"a submodule":         func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Submodules = []record.Text{"x"} },
		"no suite record":     func(b *record.Binding) { b.Summary.Flagged = nil; b.Evidence = nil },
		"too many new bytes":  func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.NewBytes = 400001 },
		"a negative count":    func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Removed = -1 },
		"a negative addition": func(b *record.Binding) { b.Summary.Flagged = nil; b.Summary.Added = -5; b.Summary.Removed = 9 },
	} {
		p := rawPending()
		change(&p.Binding)
		in, ok := BindingCountInput(p.Binding)
		if !ok || limits.Count(in).Met {
			t.Errorf("%s: the terms are met (%+v, %v)", name, in, ok)
		}
	}

	// A summary of 5,000 paths that the record holds a part of.
	cut, _ := truncatedPending(t)
	if in, ok := BindingCountInput(cut.Binding); !ok || !in.Truncated || in.Files != 5000 || limits.Count(in).Met {
		t.Errorf("a truncated summary: got %+v, %v", in, ok)
	}

	// A prune has no summary, and the count does not cover it.
	if in, ok := BindingCountInput(prunePending(t).Binding); ok || in != (limits.CountInput{}) {
		t.Errorf("a prune: got %+v, %v", in, ok)
	}
}

// Numbers too large for their field do not wrap around into small ones.
func TestCountInputLargeNumbers(t *testing.T) {
	s := record.Summary{Files: math.MaxInt64, Added: math.MaxInt64, Removed: math.MaxInt64, NewBytes: math.MaxInt64}
	got := CountInput(s, true)
	if got.Files != math.MaxInt || got.ChangedLines != math.MaxInt || got.NewBytes != math.MaxInt64 {
		t.Errorf("got %+v", got)
	}
	if limits.Count(got).Met {
		t.Error("the largest numbers meet the terms")
	}
	if got := CountInput(record.Summary{Added: math.MaxInt64 - 1, Removed: 1}, true); got.ChangedLines != math.MaxInt {
		t.Errorf("a sum at the edge: got %d", got.ChangedLines)
	}
	if got := CountInput(record.Summary{Files: math.MinInt64, Added: math.MinInt64, Removed: math.MinInt64}, true); got.Files >= 0 || got.ChangedLines >= 0 {
		t.Errorf("negative numbers turned positive: %+v", got)
	}
}

func TestCountAdd(t *testing.T) {
	var c Count
	c.Add(limits.Counted{Met: true})
	c.Add(limits.Counted{Reason: "1 flagged path"})
	c.Add(limits.Counted{Met: true})
	if c != (Count{Requests: 3, Met: 2}) {
		t.Errorf("got %+v", c)
	}
}
