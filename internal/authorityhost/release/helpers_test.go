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

// A source tree of a release with a decision service, its front end and its
// firewall, each with the validator and the reload it needs.
const (
	testUnit = "[Unit]\nDescription=the seat\n\n[Service]\nExecStart=/opt/authority-host/current/bin/ataniq-seat --socket /run/seat.sock\nRestart=on-failure\n"
	testNft  = "table inet filter {\n}\n"

	dynValidators = "caddy validate --config {release}/etc/caddy/Caddyfile\n" +
		"nft -c -f {release}/etc/nftables/rules.nft\n" +
		"systemd-analyze verify {release}/etc/systemd/system/ataniq-seat.service\n"
	dynReload = "systemctl daemon-reload\n" +
		"systemctl restart ataniq-seat\n" +
		"systemctl restart caddy\n" +
		"nft -f /opt/authority-host/current/etc/nftables/rules.nft\n"
	dynHealth = "curl -fsS --max-time 5 http://127.0.0.1/healthz\n" +
		"curl -fsS --max-time 5 --unix-socket /run/seat.sock http://localhost/readyz\n"
	dynStop = "systemctl stop ataniq-seat\n"
)

// dynamicSrc is staticSrc plus a service, and the lists that cover it.
func dynamicSrc(t *testing.T) string {
	t.Helper()
	src := staticSrc(t)
	writeTestFile(t, filepath.Join(src, "etc/systemd/system/ataniq-seat.service"), testUnit, 0o644)
	writeTestFile(t, filepath.Join(src, "etc/nftables/rules.nft"), testNft, 0o644)
	writeTestFile(t, filepath.Join(src, "share/validators.list"), dynValidators, 0o644)
	writeTestFile(t, filepath.Join(src, "share/reload.list"), dynReload, 0o644)
	writeTestFile(t, filepath.Join(src, "share/health.cmd"), dynHealth, 0o644)
	writeTestFile(t, filepath.Join(src, "share/stop.list"), dynStop, 0o644)
	return src
}

// dynamicOptions are options for the release dynamicSrc describes.
func dynamicOptions(t *testing.T, src string) Options {
	t.Helper()
	o := testOptions(t, src)
	o.Components = ""
	o.Bins = testBins(t, "ah-noaicheck", "ah-switch", "ataniq-seat")
	return o
}

// built builds o and returns the folder.
func built(t *testing.T, o Options) string {
	t.Helper()
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(o.Out, id)
}

// resum writes SHA256SUMS of dir again, as the builder would, after a test
// changed a file.
func resum(t *testing.T, dir string) {
	t.Helper()
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "SHA256SUMS" || rel == "SHA256SUMS.sig" {
			return nil
		}
		data, err := os.ReadFile(p)
		sums[rel] = sumOf(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), sumsText(sums), 0o644); err != nil {
		t.Fatal(err)
	}
}

// codes is the findings as a sorted list of "code" or "code: name".
func codes(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		s := f.Code
		if f.Name != "" {
			s += ": " + f.Name
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
