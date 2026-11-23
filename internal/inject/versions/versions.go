// Package versions holds the one table of the runtime versions the injection
// fence is tested with. A version is tested when the repository holds its
// recorded fixtures and the fence's rules were written from them and
// reviewed; the table lists exactly those versions, and every other runtime
// and version is untested and refused.
//
// The packages that read an agent (screen, title, idle) look a version up
// here instead of naming it, so a new version of a runtime is one entry in
// this table, the fixtures recorded for it and a review, and not an edit of
// each package. An entry is added only together with its fixtures, and the
// rules of a package are never edited for a version that has none (see
// "Adding a version" in docs/lead-injection.md).
//
// The package is a leaf: it imports nothing of the repository, so any package
// of the fence may import it. It is pure: it does no input or output, starts
// nothing and keeps no state.
package versions

// Entry is one tested runtime and version.
type Entry struct {
	// Runtime is the runtime's key: "claude" or "codex".
	Runtime string
	// Version is the version the fixtures were recorded from, as the table
	// spells it.
	Version string
	// Aliases are other spellings of the version that Lookup accepts, for
	// example the build number a runtime reports itself with.
	Aliases []string
	// App is the app name the runtime's OSC 7501 reports carry.
	App string
	// Fixtures names the entry's fixture set: the folder of the recorded
	// screens under internal/inject/screen/testdata.
	Fixtures string
	// Recordings lists the other files that hold recordings of this
	// runtime, as paths relative to the repository root. Each also holds
	// the recordings of the other versions of the runtime, one row per
	// recording.
	Recordings []string
}

// recordings are the files that hold the recorded window titles and OSC 7501
// reports; each names its runtime in a column of every row.
var recordings = []string{
	"internal/inject/title/testdata/titles.txt",
	"internal/inject/osc7501/testdata/reports.txt",
}

// table is the one table of tested runtimes and versions.
var table = []Entry{
	{Runtime: "claude", Version: "2.1.325", App: "claude-code", Fixtures: "claude-2.1.325", Recordings: recordings},
	{Runtime: "codex", Version: "0.169", Aliases: []string{"0.169.0"}, App: "codex", Fixtures: "codex-0.169", Recordings: recordings},
}

// Tested returns the table: every runtime and version that is tested, in a
// fixed order. The result is a copy.
func Tested() []Entry {
	out := make([]Entry, len(table))
	for i, e := range table {
		out[i] = e.clone()
	}
	return out
}

// Lookup returns the entry for a runtime and version, matching the version
// exactly against the entry's version and its aliases. Anything that is not
// in the table, and any empty value, is not found.
func Lookup(runtime, version string) (Entry, bool) {
	if runtime == "" || version == "" {
		return Entry{}, false
	}
	for _, e := range table {
		if e.Runtime == runtime && e.has(version) {
			return e.clone(), true
		}
	}
	return Entry{}, false
}

// Spellings returns the version and the aliases of the entry: every version
// string Lookup finds it by.
func (e Entry) Spellings() []string {
	return append([]string{e.Version}, e.Aliases...)
}

func (e Entry) has(version string) bool {
	if version == e.Version {
		return true
	}
	for _, a := range e.Aliases {
		if version == a {
			return true
		}
	}
	return false
}

func (e Entry) clone() Entry {
	e.Aliases = append([]string(nil), e.Aliases...)
	e.Recordings = append([]string(nil), e.Recordings...)
	return e
}
