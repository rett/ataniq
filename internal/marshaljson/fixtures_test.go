package marshaljson

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// A pin is the fixture set of one Qargi release: its cases and its manifest.
// versions lists the schema integers (the KIND/vN directories) it covers.
type pin struct {
	version      string
	manifestHash string
	versions     map[Kind][]int
}

var pins = []pin{
	{"0.3.9", "46845896d1679464689825440aec9d242355e0aecb7a77a889ba9bea0887a8e8",
		map[Kind][]int{Status: {1}, Check: {1}, Handoff: {1}, Refusal: {1}}},
	{"0.3.10", "601194159c8ab52c1b263db51629c0a2597a5be676108f928ff05213dcc2c8fc",
		map[Kind][]int{Status: {1, 2}, Check: {1, 2}, Handoff: {1}, Refusal: {1}}},
}

// current is the pin the decoder tests read, and the integer of each kind in it.
var current = pins[1]
var currentSchema = map[Kind]int{Status: 2, Check: 2, Handoff: 1, Refusal: 1}

// kindsManifestHash is the digest the Qargi 0.3.10 lead reported for
// lib/kinds/MANIFEST.json at the tag.
const kindsManifestHash = "5cfeb8f32c80bc23d66ea7df2c9d64294399d6b09144458aa3e4cdbc100ed3fb"

var kinds = []Kind{Status, Check, Handoff, Refusal}

func (p pin) dir() string { return "testdata/qargi-" + p.version }

func (p pin) archive(t *testing.T) fs.FS {
	t.Helper()
	f, err := zip.OpenReader(path.Join(p.dir(), "cases.zip"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// covers reports whether a fixture path (KIND/vN/...) belongs to the pin.
func (p pin) covers(name string) bool {
	for kind, versions := range p.versions {
		for _, n := range versions {
			if strings.HasPrefix(name, fmt.Sprintf("%s/v%d/", kind, n)) {
				return true
			}
		}
	}
	return false
}

func casesArchive(t *testing.T) fs.FS { return current.archive(t) }

func read(t *testing.T, files fs.FS, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(files, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixture reads a file of the kind's current schema in the current pin.
func fixture(t *testing.T, files fs.FS, kind Kind, name string) []byte {
	t.Helper()
	return fixtureAt(t, files, kind, currentSchema[kind], name)
}

func fixtureAt(t *testing.T, files fs.FS, kind Kind, schema int, name string) []byte {
	t.Helper()
	return read(t, files, path.Join(string(kind), fmt.Sprintf("v%d", schema), name+".json"))
}

func TestPinnedCases(t *testing.T) {
	for _, p := range pins {
		files := p.archive(t)
		for _, kind := range kinds {
			for _, n := range p.versions[kind] {
				var cases []struct{ File, Expect string }
				if err := json.Unmarshal(fixtureAt(t, files, kind, n, "cases"), &cases); err != nil {
					t.Fatal(err)
				}
				for _, c := range cases {
					t.Run(fmt.Sprintf("%s/%s/v%d/%s", p.version, kind, n, c.File), func(t *testing.T) {
						body := read(t, files, path.Join(string(kind), fmt.Sprintf("v%d", n), c.File))
						d, err := Decode(kind, body)
						switch c.Expect {
						case "valid":
							if err != nil || d.Kind() != kind || d.Schema() != n {
								t.Fatalf("got kind %q schema %d, err %v", d.Kind(), d.Schema(), err)
							}
							if c.File == "valid-unknown-field.json" && !d.Unknown() {
								t.Fatal("unknown fields went undetected")
							}
							if (kind == Refusal || d.Unknown() || c.File == "valid-truncated.json") && d.UsableEvidence("app", "r1") {
								t.Fatal("incomplete or refused input gave usable evidence")
							}
						case "invalid":
							if err == nil || d.UsableEvidence("app", "r1") {
								t.Fatal("broken fixture accepted or gave usable evidence")
							}
						default:
							t.Fatalf("unrecognized fixture verdict %q", c.Expect)
						}
					})
				}
			}
		}
	}
}

func hashes(t *testing.T, files fs.FS, root string) []string {
	t.Helper()
	var lines []string
	err := fs.WalkDir(files, root, func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			lines = append(lines, fmt.Sprintf("%x  %s\n", sha256.Sum256(read(t, files, name)), name))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func (p pin) checkManifest(t *testing.T, lines []string) {
	t.Helper()
	want := read(t, os.DirFS(p.dir()), "MANIFEST.sha256")
	if fmt.Sprintf("%x", sha256.Sum256(want)) != p.manifestHash {
		t.Fatalf("fixture manifest differs from the Qargi %s pin", p.version)
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	if strings.Join(lines, "") != string(want) {
		t.Fatal("fixture paths or hashes differ from MANIFEST.sha256")
	}
}

// pinned keeps the lines of the files a pin covers. The embedded registries
// hold every integer, and the older pin covers only its own.
func (p pin) pinned(lines []string) []string {
	return slices.DeleteFunc(slices.Clone(lines), func(line string) bool { return !p.covers(line[66:]) })
}

func TestPinnedHashes(t *testing.T) {
	schemas, err := zip.NewReader(bytes.NewReader(registries), int64(len(registries)))
	if err != nil {
		t.Fatal(err)
	}
	registryLines := hashes(t, schemas, ".")
	for _, p := range pins {
		t.Run(p.version, func(t *testing.T) {
			p.checkManifest(t, append(hashes(t, p.archive(t), "."), p.pinned(registryLines)...))
		})
	}
}

// The Qargi lead reported the digest of lib/kinds/MANIFEST.json at the tag. The
// pin keeps that file, and the file names the registries the decoder embeds.
func TestKindsManifestDigest(t *testing.T) {
	raw := read(t, os.DirFS(current.dir()), "kinds-MANIFEST.json")
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != kindsManifestHash {
		t.Fatalf("lib/kinds/MANIFEST.json digest %s, want %s", got, kindsManifestHash)
	}
	var m struct {
		Manifest int
		Kinds    []struct {
			Kind   Kind
			Status string
			Schema int
			Sha256 string
		}
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	schemas, err := zip.NewReader(bytes.NewReader(registries), int64(len(registries)))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, k := range m.Kinds {
		n, ok := currentSchema[k.Kind]
		if !ok {
			continue // A draft kind this reader does not decode.
		}
		seen++
		registry := read(t, schemas, fmt.Sprintf("%s/v%d/registry.json", k.Kind, n))
		if k.Status != "pinned" || k.Schema != n || k.Sha256 != fmt.Sprintf("%x", sha256.Sum256(registry)) {
			t.Errorf("%s: manifest entry %+v does not describe the embedded v%d registry", k.Kind, k, n)
		}
	}
	if seen != len(kinds) {
		t.Fatalf("lib/kinds/MANIFEST.json names %d of the %d decoded kinds", seen, len(kinds))
	}
}

func TestSupportedSchemasMatchPins(t *testing.T) {
	for _, kind := range kinds {
		var have []int
		for n := range schemas[kind] {
			have = append(have, int(n))
		}
		slices.Sort(have)
		if !slices.Equal(have, current.versions[kind]) || have[len(have)-1] != currentSchema[kind] {
			t.Errorf("%s: decoder reads %v, the current pin covers %v", kind, have, current.versions[kind])
		}
	}
}

// installedPin returns the pin of the installed Qargi, or why there is none.
func installedPin(files fs.FS) (pin, string) {
	b, err := fs.ReadFile(files, "VERSION")
	if err != nil {
		return pin{}, fmt.Sprintf("cannot identify installed Qargi: %v", err)
	}
	version := strings.TrimSuffix(string(b), "\n")
	for _, p := range pins {
		if p.version == version {
			return p, ""
		}
	}
	return pin{}, fmt.Sprintf("installed Qargi version %q has no fixture pin", version)
}

func TestInstalledVersionGate(t *testing.T) {
	for _, version := range []string{"0.3.9", "0.3.9\n", "0.3.10", "0.3.10\n", "0.3.8", "0.3.11\n", "0.3.10-dev", "0.3.1", ""} {
		files := fstest.MapFS{}
		if version != "" {
			files["VERSION"] = &fstest.MapFile{Data: []byte(version)}
		}
		p, reason := installedPin(files)
		want := strings.TrimSuffix(version, "\n")
		known := want == "0.3.9" || want == "0.3.10"
		if (reason == "") != known || (known && p.version != want) {
			t.Errorf("version %q: pin %q, reason %q", version, p.version, reason)
		}
	}
}

func TestInstalledFixtureHashes(t *testing.T) {
	const installed = "/opt/qargi"
	p, reason := installedPin(os.DirFS(installed))
	if reason != "" {
		t.Skip(reason)
	}
	files := os.DirFS(installed + "/tests/fixtures/json")
	var lines []string
	for _, kind := range kinds {
		lines = append(lines, hashes(t, files, string(kind))...)
	}
	p.checkManifest(t, lines)
	if p.version == current.version {
		got := read(t, os.DirFS(installed+"/lib/kinds"), "MANIFEST.json")
		if want := read(t, os.DirFS(p.dir()), "kinds-MANIFEST.json"); !bytes.Equal(got, want) {
			t.Fatal("installed lib/kinds/MANIFEST.json differs from the pin")
		}
	}
}
