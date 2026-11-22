package release

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func writeTestFile(t *testing.T, p, content string, perm fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		t.Fatal(err)
	}
}

// The content of a static source tree: a Caddyfile served as it is, and the
// four lists the switch needs, with the validators and reload a static
// release must have.
const (
	testCaddyfile  = ":80 {\n\trespond \"ok\" 200\n}\n"
	testValidators = "# validators\ncaddy validate --config {release}/etc/caddy/Caddyfile --adapter caddyfile\n"
	testReload     = "# admin is off\nsystemctl restart caddy\n"
	testHealth     = "curl -fsS --max-time 5 http://127.0.0.1/healthz\n"
	testStop       = "# a static release runs no decision service\n"
)

// staticSrc writes a source tree of a static release and returns its folder.
func staticSrc(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "etc/caddy/Caddyfile"), testCaddyfile, 0o600)
	writeTestFile(t, filepath.Join(src, "share/validators.list"), testValidators, 0o644)
	writeTestFile(t, filepath.Join(src, "share/reload.list"), testReload, 0o644)
	writeTestFile(t, filepath.Join(src, "share/health.cmd"), testHealth, 0o644)
	writeTestFile(t, filepath.Join(src, "share/stop.list"), testStop, 0o644)
	writeTestFile(t, filepath.Join(src, "share/ai-names.list"), "# names\n", 0o644)
	writeTestFile(t, filepath.Join(src, "share/packages.allow"), "# packages\n", 0o644)
	return src
}

// testBins writes fake binaries and returns their paths: the two every
// release needs.
func testBins(t *testing.T, names ...string) []string {
	t.Helper()
	if len(names) == 0 {
		names = []string{"ah-noaicheck", "ah-switch"}
	}
	dir := t.TempDir()
	var out []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		writeTestFile(t, p, "#!/bin/sh\nexit 0\n", 0o700)
		out = append(out, p)
	}
	return out
}

// testOptions are valid options for a static release built from src.
func testOptions(t *testing.T, src string) Options {
	t.Helper()
	return Options{
		Out: t.TempDir(), Counter: 7, Commit: testCommit,
		GoVersion: "go1.27.2", BuiltOn: "dev-linux-amd64", PackageSnapshot: "2026-11-21",
		Bins: testBins(t), Src: src, Components: "static",
	}
}

// listing is every path of a tree with its mode, sorted, folders with a
// trailing slash, for a comparison of two trees.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		line := rel + " " + info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += " " + sumOf(data)
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func lines(findings []Finding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	return b.String()
}
