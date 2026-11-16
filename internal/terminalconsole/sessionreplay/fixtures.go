package sessionreplay

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:generate go run ./cmd/genfixtures -out testdata

// The README keeps the directory embeddable before the generator has run.
//
//go:embed testdata
var fixtureFS embed.FS

// FixtureNames returns the names of the embedded synthetic recordings, sorted.
func FixtureNames() []string {
	entries, err := fixtureFS.ReadDir("testdata")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// FixtureBytes returns the recording text of an embedded fixture.
func FixtureBytes(name string) ([]byte, error) {
	data, err := fixtureFS.ReadFile("testdata/" + name)
	if err != nil {
		return nil, fmt.Errorf("no fixture %q", name)
	}
	return data, nil
}

// LoadFixture parses an embedded fixture.
func LoadFixture(name string) (*Recording, error) {
	data, err := FixtureBytes(name)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
