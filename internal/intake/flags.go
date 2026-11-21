package intake

import (
	"slices"
	"strings"

	"ataniq/internal/intake/execconfig"
)

// kindOrder is the order of the kinds when flags of one path are sorted.
var kindOrder = []FlagKind{FlagExecConfig, FlagSymlink, FlagSubmodule, FlagAliased, FlagHistory, FlagOddPath}

func kindRank(k FlagKind) int {
	if i := slices.Index(kindOrder, k); i >= 0 {
		return i
	}
	return len(kindOrder)
}

// addFlag adds f to flags. A flag of the same kind and path is not added
// twice: its reason gets the new reason after a semicolon.
func addFlag(flags []Flag, f Flag) []Flag {
	for i := range flags {
		if flags[i].Kind == f.Kind && flags[i].Path == f.Path {
			if f.Reason != "" && !strings.Contains(flags[i].Reason, f.Reason) {
				flags[i].Reason += "; " + f.Reason
			}
			return flags
		}
	}
	return append(flags, f)
}

// sortFlags sorts by path bytes and then by kind.
func sortFlags(flags []Flag) {
	slices.SortStableFunc(flags, func(a, b Flag) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return kindRank(a.Kind) - kindRank(b.Kind)
	})
}

// entryFlags returns the flags a change earns by itself: exec-config by the
// path, symlink and submodule by the old or new mode.
func entryFlags(rules execconfig.Set, ch Change) []Flag {
	var flags []Flag
	if m, ok := rules.Match(ch.Path); ok {
		flags = addFlag(flags, Flag{Kind: FlagExecConfig, Path: ch.Path, Pattern: m.Pattern, Reason: m.Reason})
	}
	switch {
	case ch.NewMode == modeSymlink:
		flags = addFlag(flags, Flag{Kind: FlagSymlink, Path: ch.Path,
			Reason: "a symlink entry can make a checkout alias an execution-config path"})
	case ch.OldMode == modeSymlink:
		flags = addFlag(flags, Flag{Kind: FlagSymlink, Path: ch.Path,
			Reason: "a symlink entry is changed or removed"})
	}
	if ch.OldMode == modeSubmodule || ch.NewMode == modeSubmodule {
		flags = addFlag(flags, Flag{Kind: FlagSubmodule, Path: ch.Path,
			Reason: "a submodule entry fetches and runs other code"})
	}
	return flags
}

// collect returns the flags of all changes, with the extra flags added, as
// the sorted Flagged list, one flag for each kind and path.
func collect(changes []Change, extra []Flag) []Flag {
	var all []Flag
	for _, ch := range changes {
		for _, f := range ch.Flags {
			all = addFlag(all, f)
		}
	}
	for _, f := range extra {
		all = addFlag(all, f)
	}
	sortFlags(all)
	return all
}
