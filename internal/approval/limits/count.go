package limits

import (
	"fmt"
	"strings"
)

// CountInput is one change as plain numbers, as the card's summary gives
// them. ChangedLines is added plus removed lines. NewBytes is the size of the
// content the change adds. Flagged, Binary, Symlinks, ModeChanges and
// Submodules count the paths of each kind. SuiteClean says that the check
// record of the change is clean, and Truncated that the summary was cut
// short, so that the numbers may be too low.
type CountInput struct {
	Files        int
	ChangedLines int
	NewBytes     int64
	Flagged      int
	Binary       int
	Symlinks     int
	ModeChanges  int
	Submodules   int
	SuiteClean   bool
	Truncated    bool
}

// FirstTerms holds the sizes the First template allows: the terms that have a
// number. The other terms have none and are always the same: no flagged path,
// no binary file, no symbolic link, no mode change, no submodule, a clean
// check record, and a summary that is not truncated.
//
// DefaultFirstTerms is the one place where the numbers stand.
type FirstTerms struct {
	MaxFiles        int
	MaxChangedLines int
	MaxNewBytes     int64
}

// DefaultFirstTerms returns the terms of this build: at most 20 files, at
// most 800 changed lines and at most 400,000 new bytes. The files and the
// lines are the template's own numbers. The bytes are a provisional number of
// this build.
func DefaultFirstTerms() FirstTerms {
	return FirstTerms{MaxFiles: 20, MaxChangedLines: 800, MaxNewBytes: 400000}
}

// VerdictRunsNote says what the count leaves out. The First template also
// asks for two verdict runs. None exist yet, so the count cannot look at
// them, and every answer says so.
const VerdictRunsNote = "the template's verdict runs are not counted"

// Counted is the answer of the count for one change. Met says that the change
// would have met the terms. When it would not, Reason is one line that names
// every term it missed and ends with VerdictRunsNote; when it would, Reason
// is empty. The reason is made of numbers and fixed words only.
type Counted struct {
	Met    bool
	Reason string
}

// Line returns the answer as one line for a record or a screen. It carries
// VerdictRunsNote whether the terms were met or not.
func (c Counted) Line() string {
	if c.Met {
		return "would have met the First template's terms (" + VerdictRunsNote + ")"
	}
	return "would not have met the First template's terms: " + c.Reason
}

// Count says whether in would have met the terms of this build.
func Count(in CountInput) Counted {
	return DefaultFirstTerms().Count(in)
}

// Count says whether in would have met the terms t. A number in the input
// that is negative is no count at all, and the terms are not met.
func (t FirstTerms) Count(in CountInput) Counted {
	var missed []string
	miss := func(format string, args ...any) {
		missed = append(missed, fmt.Sprintf(format, args...))
	}

	if in.Files < 0 || in.ChangedLines < 0 || in.NewBytes < 0 || in.Flagged < 0 ||
		in.Binary < 0 || in.Symlinks < 0 || in.ModeChanges < 0 || in.Submodules < 0 {
		miss("a negative number in the summary")
	}
	if in.Files > t.MaxFiles {
		miss("%d files, and the terms allow %d", in.Files, t.MaxFiles)
	}
	if in.ChangedLines > t.MaxChangedLines {
		miss("%d changed lines, and the terms allow %d", in.ChangedLines, t.MaxChangedLines)
	}
	if in.NewBytes > t.MaxNewBytes {
		miss("%d new bytes, and the terms allow %d", in.NewBytes, t.MaxNewBytes)
	}
	none := func(n int, one, many string) {
		switch {
		case n == 1:
			miss("1 %s", one)
		case n > 1:
			miss("%d %s", n, many)
		}
	}
	none(in.Flagged, "flagged path", "flagged paths")
	none(in.Binary, "binary file", "binary files")
	none(in.Symlinks, "symbolic link", "symbolic links")
	none(in.ModeChanges, "mode change", "mode changes")
	none(in.Submodules, "submodule", "submodules")
	if !in.SuiteClean {
		miss("the check record is not clean")
	}
	if in.Truncated {
		miss("the summary is truncated")
	}

	if len(missed) == 0 {
		return Counted{Met: true}
	}
	return Counted{Reason: strings.Join(missed, "; ") + " (" + VerdictRunsNote + ")"}
}
