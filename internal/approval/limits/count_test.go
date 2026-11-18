package limits

import (
	"strings"
	"testing"
)

// clean is a change that sits exactly on every numbered term.
func clean() CountInput {
	return CountInput{Files: 20, ChangedLines: 800, NewBytes: 400000, SuiteClean: true}
}

func TestDefaultFirstTerms(t *testing.T) {
	got := DefaultFirstTerms()
	if got != (FirstTerms{MaxFiles: 20, MaxChangedLines: 800, MaxNewBytes: 400000}) {
		t.Fatalf("terms: %+v", got)
	}
}

func TestCountMet(t *testing.T) {
	for name, in := range map[string]CountInput{
		"on every edge": clean(),
		"empty change":  {SuiteClean: true},
		"small change":  {Files: 3, ChangedLines: 40, NewBytes: 1200, SuiteClean: true},
	} {
		c := Count(in)
		if !c.Met || c.Reason != "" {
			t.Errorf("%s: %+v", name, c)
		}
		if got := c.Line(); got != "would have met the First template's terms (the template's verdict runs are not counted)" {
			t.Errorf("%s: line %q", name, got)
		}
	}
}

func TestCountNotMet(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*CountInput)
		want   string
	}{
		{"one file too many", func(in *CountInput) { in.Files = 21 }, "21 files, and the terms allow 20"},
		{"one line too many", func(in *CountInput) { in.ChangedLines = 801 }, "801 changed lines, and the terms allow 800"},
		{"one byte too many", func(in *CountInput) { in.NewBytes = 400001 }, "400001 new bytes, and the terms allow 400000"},
		{"a flagged path", func(in *CountInput) { in.Flagged = 1 }, "1 flagged path"},
		{"flagged paths", func(in *CountInput) { in.Flagged = 3 }, "3 flagged paths"},
		{"a binary file", func(in *CountInput) { in.Binary = 1 }, "1 binary file"},
		{"a symbolic link", func(in *CountInput) { in.Symlinks = 1 }, "1 symbolic link"},
		{"mode changes", func(in *CountInput) { in.ModeChanges = 2 }, "2 mode changes"},
		{"a submodule", func(in *CountInput) { in.Submodules = 1 }, "1 submodule"},
		{"no clean check record", func(in *CountInput) { in.SuiteClean = false }, "the check record is not clean"},
		{"truncated", func(in *CountInput) { in.Truncated = true }, "the summary is truncated"},
		{"negative files", func(in *CountInput) { in.Files = -1 }, "a negative number in the summary"},
		{"negative changed lines", func(in *CountInput) { in.ChangedLines = -1 }, "a negative number in the summary"},
		{"negative bytes", func(in *CountInput) { in.NewBytes = -1 }, "a negative number in the summary"},
		{"negative flagged", func(in *CountInput) { in.Flagged = -1 }, "a negative number in the summary"},
		{"negative binary", func(in *CountInput) { in.Binary = -1 }, "a negative number in the summary"},
		{"negative symbolic links", func(in *CountInput) { in.Symlinks = -1 }, "a negative number in the summary"},
		{"negative mode changes", func(in *CountInput) { in.ModeChanges = -1 }, "a negative number in the summary"},
		{"negative submodules", func(in *CountInput) { in.Submodules = -1 }, "a negative number in the summary"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := clean()
			c.change(&in)
			got := Count(in)
			if got.Met {
				t.Fatal("met")
			}
			if want := c.want + " (" + VerdictRunsNote + ")"; got.Reason != want {
				t.Fatalf("reason %q, want %q", got.Reason, want)
			}
			if strings.ContainsAny(got.Reason, "\n\r") {
				t.Fatalf("reason %q is not one line", got.Reason)
			}
			if want := "would not have met the First template's terms: " + got.Reason; got.Line() != want {
				t.Fatalf("line %q, want %q", got.Line(), want)
			}
		})
	}
}

// Every missed term is named, in one line, and the note stands once at the
// end.
func TestCountNamesEveryMissedTerm(t *testing.T) {
	got := Count(CountInput{
		Files: 5000, ChangedLines: 900, NewBytes: 1 << 40,
		Flagged: 2, Binary: 1, Symlinks: 1, ModeChanges: 1, Submodules: 2,
		Truncated: true,
	})
	want := "5000 files, and the terms allow 20; 900 changed lines, and the terms allow 800; " +
		"1099511627776 new bytes, and the terms allow 400000; 2 flagged paths; 1 binary file; " +
		"1 symbolic link; 1 mode change; 2 submodules; the check record is not clean; " +
		"the summary is truncated (the template's verdict runs are not counted)"
	if got.Met || got.Reason != want {
		t.Fatalf("reason %q\n  want %q", got.Reason, want)
	}
	if strings.Count(got.Reason, VerdictRunsNote) != 1 {
		t.Fatal("the note does not stand exactly once")
	}
}

// Terms other than the defaults: the numbers come from the value.
func TestCountAgainstOtherTerms(t *testing.T) {
	terms := FirstTerms{MaxFiles: 2, MaxChangedLines: 10, MaxNewBytes: 100}
	if c := terms.Count(CountInput{Files: 2, ChangedLines: 10, NewBytes: 100, SuiteClean: true}); !c.Met {
		t.Fatalf("on the edge: %+v", c)
	}
	c := terms.Count(CountInput{Files: 3, ChangedLines: 10, NewBytes: 100, SuiteClean: true})
	if c.Met || !strings.HasPrefix(c.Reason, "3 files, and the terms allow 2") {
		t.Fatalf("one file over: %+v", c)
	}
	if !Count(CountInput{Files: 3, ChangedLines: 10, NewBytes: 100, SuiteClean: true}).Met {
		t.Fatal("the defaults were changed")
	}
}
