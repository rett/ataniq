package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

const commit = "0123456789abcdef0123456789abcdef01234567"

func call(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func write(t *testing.T, p, content string, perm fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// source writes a small static source tree.
func source(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	write(t, filepath.Join(src, "etc/caddy/Caddyfile"), ":80 {\n\trespond \"ok\" 200\n}\n", 0o644)
	write(t, filepath.Join(src, "share/validators.list"), "caddy validate --config {release}/etc/caddy/Caddyfile\n", 0o644)
	write(t, filepath.Join(src, "share/reload.list"), "systemctl restart caddy\n", 0o644)
	write(t, filepath.Join(src, "share/health.cmd"), "curl -fsS --max-time 5 http://127.0.0.1/healthz\n", 0o644)
	write(t, filepath.Join(src, "share/stop.list"), "# none\n", 0o644)
	return src
}

func bins(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ah-noaicheck"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(dir, "ah-switch"), "#!/bin/sh\nexit 0\n", 0o755)
	return filepath.Join(dir, "ah-noaicheck"), filepath.Join(dir, "ah-switch")
}

func buildArgs(out, src, a, b string, more ...string) []string {
	args := []string{"build", "--out", out, "--counter", "3", "--commit", commit,
		"--go-version", "go1.27.2", "--built-on", "dev-linux-amd64", "--package-snapshot", "2026-11-21",
		"--bin", a, "--bin", b, "--src", src, "--components", "static"}
	return append(args, more...)
}

func TestUsage(t *testing.T) {
	out := t.TempDir()
	src := source(t)
	a, b := bins(t)
	good := buildArgs(out, src, a, b)
	swap := func(flag, value string) []string {
		args := append([]string(nil), good...)
		for i := range args {
			if args[i] == flag {
				args[i+1] = value
				return args
			}
		}
		t.Fatalf("no %s", flag)
		return nil
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"make"}},
		{"unknown flag", append(append([]string(nil), good...), "--no-such-flag")},
		{"a positional argument", append(append([]string(nil), good...), "extra")},
		{"counter 0", swap("--counter", "0")},
		{"counter negative", swap("--counter", "-1")},
		{"counter not a number", swap("--counter", "x")},
		{"counter empty", swap("--counter", "")},
		{"commit with a newline", swap("--commit", "abcdef0\nx")},
		{"uppercase commit", swap("--commit", "ABCDEF0")},
		{"go version with a space", swap("--go-version", "go 1")},
		{"built on empty", swap("--built-on", "")},
		{"snapshot with a slash", swap("--package-snapshot", "a/b")},
		{"components dynamic", swap("--components", "dynamic")},
		{"out empty", swap("--out", "")},
		{"src empty", swap("--src", "")},
		{"only one bin", []string{"build", "--out", out, "--counter", "3", "--commit", commit, "--go-version", "g", "--built-on", "d",
			"--package-snapshot", "s", "--bin", a, "--src", src}},
	} {
		code, stdout, errOut := call(tc.args...)
		if code != 2 || !strings.Contains(errOut, "ah-release: ") || stdout != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", tc.name, code, stdout, errOut)
		}
		if strings.ContainsAny(strings.TrimSuffix(errOut, "\n"), "\x00") || strings.Contains(errOut, "abcdef0\nx") {
			t.Errorf("%s: stderr echoes an input: %q", tc.name, errOut)
		}
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 0 {
		t.Errorf("a usage error wrote into the output folder: %v, %v", entries, err)
	}
	if code, stdout, _ := call("--help"); code != 0 || !strings.Contains(stdout, "usage: ah-release") {
		t.Errorf("--help: exit %d, %q", code, stdout)
	}
}

func TestBuildPrintsOnlyTheIDAndRefusesTwice(t *testing.T) {
	out := t.TempDir()
	a, b := bins(t)
	args := buildArgs(out, source(t), a, b)
	code, stdout, errOut := call(args...)
	if code != 0 || stdout != "built release 0003-0123456 (9 files)\n" || errOut != "" {
		t.Fatalf("exit %d, %q, %q", code, stdout, errOut)
	}
	code, stdout, errOut = call(args...)
	if code != 1 || stdout != "FAIL: exists that release folder exists already: 0003-0123456\n" || errOut != "" {
		t.Errorf("second build: exit %d, %q, %q", code, stdout, errOut)
	}
}

func TestBuildRefusesWhatItCannotCopy(t *testing.T) {
	out := t.TempDir()
	src := source(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "etc/link")); err != nil {
		t.Fatal(err)
	}
	a, b := bins(t)
	code, stdout, _ := call(buildArgs(out, src, a, b)...)
	if code != 1 || stdout != "FAIL: not-regular not a folder or a regular file: etc/link\n" {
		t.Errorf("exit %d, %q", code, stdout)
	}
	// A hostile name is not printed.
	os.Remove(filepath.Join(src, "etc/link"))
	write(t, filepath.Join(src, "etc/bad\nname"), "x", 0o644)
	code, stdout, _ = call(buildArgs(out, src, a, b)...)
	if code != 1 || stdout != "FAIL: bad-name a name is not letters, digits and ._@+-\n" {
		t.Errorf("exit %d, %q", code, stdout)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 0 {
		t.Errorf("a refused build left %v", entries)
	}
}

// TestBuiltFolderIsAcceptedBySwitch imports the built folder with the
// switch's own verification: a dry run of ah-switch --from, which checks
// the sums, the manifest, the lists and the programs of the folder.
func TestBuiltFolderIsAcceptedBySwitch(t *testing.T) {
	out := t.TempDir()
	a, b := bins(t)
	// A first switch moves the host to the release's package snapshot, which
	// needs the release's package step.
	pkg := filepath.Join(t.TempDir(), "ah-pkgstep")
	write(t, pkg, "#!/bin/sh\nexit 0\n", 0o755)
	if code, stdout, errOut := call(buildArgs(out, source(t), a, b, "--bin", pkg)...); code != 0 {
		t.Fatalf("exit %d, %q, %q", code, stdout, errOut)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop caddy\n", 0o644)
	var log bytes.Buffer
	s := switcher.New(switcher.Config{Root: root, Dry: true, From: filepath.Join(out, "0003-0123456"), Out: &log})
	if code := s.Switch(t.Context(), "0003-0123456"); code != switcher.ExitOK {
		t.Fatalf("the switch refuses the release: exit %d\n%s", code, log.String())
	}
	if !strings.Contains(log.String(), "would point current at releases/0003-0123456") {
		t.Errorf("the dry run did not get to the end:\n%s", log.String())
	}
}

// TestTamperedFolderIsRefusedBySwitch: the same dry run refuses a folder
// whose file changed after the build, so the test above is not vacuous.
func TestTamperedFolderIsRefusedBySwitch(t *testing.T) {
	out := t.TempDir()
	a, b := bins(t)
	pkg := filepath.Join(t.TempDir(), "ah-pkgstep")
	write(t, pkg, "#!/bin/sh\nexit 0\n", 0o755)
	if code, _, _ := call(buildArgs(out, source(t), a, b, "--bin", pkg)...); code != 0 {
		t.Fatal("build")
	}
	dir := filepath.Join(out, "0003-0123456")
	write(t, filepath.Join(dir, "share/health.cmd"), "true\n", 0o644)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	write(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop caddy\n", 0o644)
	var log bytes.Buffer
	s := switcher.New(switcher.Config{Root: root, Dry: true, From: dir, Out: &log})
	if code := s.Switch(t.Context(), "0003-0123456"); code == switcher.ExitOK || !strings.Contains(log.String(), "SHA256") {
		t.Fatalf("the switch accepts a tampered release (exit %d):\n%s", code, log.String())
	}
}

// builtRelease builds the static test release and returns its folder.
func builtRelease(t *testing.T, mod func(src string)) string {
	t.Helper()
	out := t.TempDir()
	src := source(t)
	if mod != nil {
		mod(src)
	}
	a, b := bins(t)
	if code, stdout, errOut := call(buildArgs(out, src, a, b)...); code != 0 {
		t.Fatalf("build: exit %d, %q, %q", code, stdout, errOut)
	}
	return filepath.Join(out, "0003-0123456")
}

func TestCheckUsage(t *testing.T) {
	dir := builtRelease(t, nil)
	for _, args := range [][]string{
		{"check"},
		{"check", dir, dir},
		{"check", "--no-such-flag", dir},
		{"check", "--no-noaicheck", "--root", "/x", dir},
		{"check", "--no-noaicheck", "--require-policy", dir},
		{"check", "--root", "", dir},
	} {
		code, stdout, errOut := call(args...)
		if code != 2 || stdout != "" || !strings.Contains(errOut, "ah-release: ") {
			t.Errorf("%q: exit %d, %q, %q", args, code, stdout, errOut)
		}
	}
}

func TestCheckPasses(t *testing.T) {
	dir := builtRelease(t, nil)
	for _, args := range [][]string{{"check", "--no-noaicheck", dir}, {"check", dir, "--no-noaicheck"}} {
		code, stdout, errOut := call(args...)
		if code != 0 || stdout != "ok: release 0003-0123456\n" || errOut != "" {
			t.Errorf("%q: exit %d, %q, %q", args, code, stdout, errOut)
		}
	}
	// With the no-AI check: the fake one exits 0.
	if code, stdout, errOut := call("check", dir); code != 0 {
		t.Errorf("with the no-AI check: exit %d, %q, %q", code, stdout, errOut)
	}
}

func TestCheckFailsWithFixedLines(t *testing.T) {
	// A validator missing; the file's text holds a secret that must not be
	// printed; a hostile name.
	dir := builtRelease(t, func(src string) {
		write(t, filepath.Join(src, "share/validators.list"), "# SECRET-TOKEN-1234\n", 0o644)
		write(t, filepath.Join(src, "etc/caddy/Caddyfile"), "# SECRET-TOKEN-5678\n:80\n", 0o644)
	})
	write(t, filepath.Join(dir, "share/bad\nname"), "SECRET-TOKEN-9999", 0o644)
	code, stdout, errOut := call("check", "--no-noaicheck", dir)
	if code != 1 || errOut != "" {
		t.Fatalf("exit %d, %q", code, errOut)
	}
	for _, want := range []string{
		"FAIL: bad-name a name is not letters, digits and ._@+-\n",
		"FAIL: validator-missing no validator for a shipped file: etc/caddy/Caddyfile\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if !strings.HasPrefix(line, "FAIL: ") {
			t.Errorf("line %q", line)
		}
	}
	if strings.Contains(stdout, "SECRET") {
		t.Errorf("a file's content is printed:\n%s", stdout)
	}
}

func TestCheckPrintsAtMostFiftyLines(t *testing.T) {
	dir := builtRelease(t, nil)
	for i := 0; i < 80; i++ {
		write(t, filepath.Join(dir, "share", "extra"+strconv.Itoa(i)), "x", 0o644)
	}
	code, stdout, _ := call("check", "--no-noaicheck", dir)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != 1 || len(lines) != 50 || !strings.HasPrefix(lines[49], "FAIL: more-findings ") {
		t.Errorf("exit %d, %d lines, last %q", code, len(lines), lines[len(lines)-1])
	}
}

func TestCheckHitOfTheShippedCheckFails(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "0003-0123456")
	a, b := bins(t)
	write(t, a, "#!/bin/sh\necho 'HIT binary /usr/local/bin/agent'\nexit 1\n", 0o755)
	if code, _, _ := call(buildArgs(out, source(t), a, b)...); code != 0 {
		t.Fatal("build")
	}
	code, stdout, _ := call("check", dir)
	want := "FAIL: noaicheck-exit the shipped no-AI check exited with a failure\nFAIL: noaicheck-hit the shipped no-AI check reported a hit\n"
	if code != 1 || stdout != want {
		t.Errorf("exit %d, %q", code, stdout)
	}
}

// repoSource is the real source tree of the authority host's releases.
const repoSource = "../../deploy/authority-host"

// TestRealStaticRelease builds the first (static) release from the tree this
// repository ships, with fake binaries, and checks it: coverage passes, the
// switch accepts it, and the Caddyfile and lists are the ones the design
// asks for.
func TestRealStaticRelease(t *testing.T) {
	out := t.TempDir()
	a, b := bins(t)
	pkg := filepath.Join(t.TempDir(), "ah-pkgstep")
	write(t, pkg, "#!/bin/sh\nexit 0\n", 0o755)
	code, stdout, errOut := call(buildArgs(out, repoSource, a, b, "--bin", pkg)...)
	if code != 0 {
		t.Fatalf("build: exit %d, %q, %q", code, stdout, errOut)
	}
	dir := filepath.Join(out, "0003-0123456")
	if code, stdout, errOut := call("check", "--no-noaicheck", dir); code != 0 || stdout != "ok: release 0003-0123456\n" {
		t.Fatalf("check: exit %d, %q, %q", code, stdout, errOut)
	}
	// With the shipped no-AI check too (the fake exits 0).
	if code, stdout, errOut := call("check", dir); code != 0 {
		t.Fatalf("check with the no-AI check: exit %d, %q, %q", code, stdout, errOut)
	}

	read := func(p string) string {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	const caddyfile = "{\n\tadmin off\n\tauto_https off\n\tservers {\n\t\ttimeouts {\n\t\t\tread_header 10s\n\t\t\tread_body 10s\n\t\t\twrite 30s\n\t\t\tidle 60s\n\t\t}\n\t}\n}\n\n" +
		":80 {\n\theader {\n\t\tX-Content-Type-Options nosniff\n\t\tReferrer-Policy no-referrer\n\t\tX-Frame-Options DENY\n" +
		"\t\tContent-Security-Policy \"default-src 'none'; style-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'\"\n" +
		"\t\tCache-Control no-store\n\t\t-Server\n\t}\n\n\thandle /healthz {\n\t\trespond \"ok\" 200\n\t}\n\n\thandle {\n\t\troot * /srv/authority/public\n\t\tfile_server\n\t}\n}\n"
	if got := read("etc/caddy/Caddyfile"); got != caddyfile {
		t.Errorf("Caddyfile:\n%s", got)
	}
	if got := read("share/health.cmd"); got != "curl -fsS --max-time 5 http://127.0.0.1/healthz\n" {
		t.Errorf("health.cmd: %q", got)
	}
	if got := read("share/validators.list"); !strings.Contains(got, "\ncaddy validate --config {release}/etc/caddy/Caddyfile --adapter caddyfile\n") {
		t.Errorf("validators.list:\n%s", got)
	}
	if got := read("share/reload.list"); !strings.Contains(got, "\nsystemctl restart caddy\n") || !strings.Contains(got, "admin off") {
		t.Errorf("reload.list:\n%s", got)
	}
	for _, line := range strings.Split(read("share/stop.list"), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("stop.list holds a command: %q", line)
		}
	}
	for _, p := range []string{"share/ai-names.list", "share/forbidden-packages.list", "share/packages.allow", "share/ports.allow", "share/units.allow", "MANIFEST", "SHA256SUMS"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if !strings.Contains(read("MANIFEST"), "components: static\n") {
		t.Errorf("MANIFEST:\n%s", read("MANIFEST"))
	}

	// The switch accepts the folder: sums, manifest, lists and programs.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	write(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop caddy\n", 0o644)
	var log bytes.Buffer
	s := switcher.New(switcher.Config{Root: root, Dry: true, From: dir, Out: &log})
	if code := s.Switch(t.Context(), "0003-0123456"); code != switcher.ExitOK {
		t.Fatalf("the switch refuses the real static release: exit %d\n%s", code, log.String())
	}
}

// TestRealTreeHasNoPrivateNames: nothing of the source tree names a host, a
// user, an address or a home folder.
func TestRealTreeHasNoPrivateNames(t *testing.T) {
	for _, p := range []string{"etc/caddy/Caddyfile", "share/validators.list", "share/reload.list", "share/health.cmd", "share/stop.list"} {
		data, err := os.ReadFile(filepath.Join(repoSource, p))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"/ho" + "me/", "@", "192.168.", "10.0."} {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s holds %q", p, bad)
			}
		}
	}
}
