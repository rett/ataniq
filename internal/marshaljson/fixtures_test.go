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

const pinnedVersion = "0.3.9"
const fixtureDir = "testdata/qargi-" + pinnedVersion
const manifestHash = "46845896d1679464689825440aec9d242355e0aecb7a77a889ba9bea0887a8e8"

var kinds = []Kind{Status, Check, Handoff, Refusal}

func casesArchive(t *testing.T) fs.FS {
	t.Helper()
	f, err := zip.OpenReader(path.Join(fixtureDir, "cases.zip"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func read(t *testing.T, files fs.FS, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(files, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixture(t *testing.T, files fs.FS, kind Kind, name string) []byte {
	t.Helper()
	return read(t, files, path.Join(string(kind), "v1", name+".json"))
}

func TestPinnedCases(t *testing.T) {
	files := casesArchive(t)
	for _, kind := range kinds {
		var cases []struct{ File, Expect string }
		if err := json.Unmarshal(fixture(t, files, kind, "cases"), &cases); err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			t.Run(string(kind)+"/"+c.File, func(t *testing.T) {
				body := read(t, files, path.Join(string(kind), "v1", c.File))
				d, err := Decode(kind, body)
				switch c.Expect {
				case "valid":
					if err != nil || d.Kind() != kind {
						t.Fatalf("got kind %q, err %v", d.Kind(), err)
					}
					if c.File == "valid-unknown-field.json" && !d.Unknown() {
						t.Fatal("unknown fields went undetected")
					}
					if (kind == Refusal || d.Unknown() || c.File == "valid-truncated.json") && d.ActionsAllowed("app", "r1") {
						t.Fatal("incomplete or refused input enabled actions")
					}
				case "invalid":
					if err == nil || d.ActionsAllowed("app", "r1") {
						t.Fatal("broken fixture accepted or enabled actions")
					}
				default:
					t.Fatalf("unrecognized fixture verdict %q", c.Expect)
				}
			})
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

func checkManifest(t *testing.T, lines []string) {
	t.Helper()
	want := read(t, os.DirFS("testdata"), "MANIFEST.sha256")
	if fmt.Sprintf("%x", sha256.Sum256(want)) != manifestHash {
		t.Fatal("fixture manifest differs from the Qargi 0.3.9 pin")
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	if strings.Join(lines, "") != string(want) {
		t.Fatal("fixture paths or hashes differ from MANIFEST.sha256")
	}
}

func TestPinnedHashes(t *testing.T) {
	lines := hashes(t, casesArchive(t), ".")
	schemas, err := zip.NewReader(bytes.NewReader(registries), int64(len(registries)))
	if err != nil {
		t.Fatal(err)
	}
	checkManifest(t, append(lines, hashes(t, schemas, ".")...))
}

func installedVersionSkip(files fs.FS) string {
	b, err := fs.ReadFile(files, "VERSION")
	if err != nil {
		return fmt.Sprintf("cannot identify installed Qargi: %v", err)
	}
	if version := strings.TrimSuffix(string(b), "\n"); version != pinnedVersion {
		return fmt.Sprintf("installed Qargi version %q differs from fixture pin %s", version, pinnedVersion)
	}
	return ""
}

func TestInstalledVersionGate(t *testing.T) {
	for _, version := range []string{"0.3.9", "0.3.9\n", "0.3.10\n", "0.3.9-dev", ""} {
		files := fstest.MapFS{}
		if version != "" {
			files["VERSION"] = &fstest.MapFile{Data: []byte(version)}
		}
		want := version == "0.3.9" || version == "0.3.9\n"
		if compare := installedVersionSkip(files) == ""; compare != want {
			t.Errorf("version %q: compare = %v", version, compare)
		}
	}
}

func TestInstalledFixtureHashes(t *testing.T) {
	const installed = "/opt/qargi"
	if reason := installedVersionSkip(os.DirFS(installed)); reason != "" {
		t.Skip(reason)
	}
	files := os.DirFS(installed + "/tests/fixtures/json")
	var lines []string
	for _, kind := range kinds {
		lines = append(lines, hashes(t, files, string(kind)+"/v1")...)
	}
	checkManifest(t, lines)
}
