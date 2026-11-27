package marshaljson

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
)

const fixtureDir = "testdata/qargi-0.3.9"
const manifestHash = "46845896d1679464689825440aec9d242355e0aecb7a77a889ba9bea0887a8e8"

var kinds = []Kind{Status, Check, Handoff, Refusal}

func archive(t *testing.T, name string) fs.FS {
	t.Helper()
	f, err := zip.OpenReader(path.Join(fixtureDir, name))
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
	files := archive(t, "cases.zip")
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
					if (kind == Refusal || d.Unknown() || c.File == "valid-truncated.json") && d.ActionsAllowed() {
						t.Fatal("incomplete or refused input enabled actions")
					}
				case "invalid":
					if err == nil || d.ActionsAllowed() {
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
	lines := hashes(t, archive(t, "cases.zip"), ".")
	checkManifest(t, append(lines, hashes(t, archive(t, "registries.zip"), ".")...))
}

func TestInstalledFixtureHashes(t *testing.T) {
	const installed = "/opt/qargi"
	if _, err := os.Stat(installed); errors.Is(err, fs.ErrNotExist) {
		t.Skip("Qargi is not installed")
	} else if err != nil {
		t.Fatal(err)
	}
	files := os.DirFS(installed + "/tests/fixtures/json")
	var lines []string
	for _, kind := range kinds {
		lines = append(lines, hashes(t, files, string(kind)+"/v1")...)
	}
	checkManifest(t, lines)
}
