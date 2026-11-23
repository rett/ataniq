package release

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

func TestBuildLayout(t *testing.T) {
	src := staticSrc(t)
	o := testOptions(t, src)
	id, files, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	if id != "0007-0123456" {
		t.Fatalf("id %q", id)
	}
	dir := filepath.Join(o.Out, id)
	got := listing(t, dir)
	// Every path, its mode and its content digest: folders 0755, files 0644,
	// bin/* 0755, whatever the source's modes were.
	want := []string{
		". drwxr-xr-x",
		"MANIFEST -rw-r--r-- " + sumOf([]byte("id: 0007-0123456\ncounter: 7\ncommit: "+testCommit+"\nbuilt_on: dev-linux-amd64\ngo_version: go1.27.2\npackage_snapshot: 2026-11-21\ncomponents: static\n")),
		"SHA256SUMS -rw-r--r-- ",
		"bin drwxr-xr-x",
		"bin/ah-noaicheck -rwxr-xr-x " + sumOf([]byte("#!/bin/sh\nexit 0\n")),
		"bin/ah-pkgstep -rwxr-xr-x " + sumOf([]byte(StaticPackageStep)),
		"bin/ah-switch -rwxr-xr-x " + sumOf([]byte("#!/bin/sh\nexit 0\n")),
		"etc drwxr-xr-x",
		"etc/caddy drwxr-xr-x",
		"etc/caddy/Caddyfile -rw-r--r-- " + sumOf([]byte(testCaddyfile)),
		"share drwxr-xr-x",
		"share/ai-names.list -rw-r--r-- " + sumOf([]byte("# names\n")),
		"share/health.cmd -rw-r--r-- " + sumOf([]byte(testHealth)),
		"share/packages.allow -rw-r--r-- " + sumOf([]byte("# packages\n")),
		"share/reload.list -rw-r--r-- " + sumOf([]byte(testReload)),
		"share/stop.list -rw-r--r-- " + sumOf([]byte(testStop)),
		"share/validators.list -rw-r--r-- " + sumOf([]byte(testValidators)),
	}
	// SHA256SUMS holds the sums of the others, so its own digest is not fixed
	// by the test; compare it apart.
	for i, line := range got {
		if strings.HasPrefix(line, "SHA256SUMS ") {
			got[i] = "SHA256SUMS -rw-r--r-- "
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layout\n got: %q\nwant: %q", got, want)
	}
	if files != 12 { // 10 files, MANIFEST and SHA256SUMS
		t.Errorf("files = %d", files)
	}
	if _, err := os.Lstat(filepath.Join(o.Out, "."+id+".partial")); err == nil {
		t.Error("the staging folder is left")
	}
}

var sumsLine = regexp.MustCompile(`^[0-9a-f]{64}  [A-Za-z0-9._@+/-]+$`)

func TestBuildSumsFormat(t *testing.T) {
	o := testOptions(t, staticSrc(t))
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(o.Out, id)
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") || strings.Contains(text, "\n\n") {
		t.Fatalf("SHA256SUMS is not newline-terminated lines: %q", text)
	}
	var paths []string
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if !sumsLine.MatchString(l) {
			t.Fatalf("line %q is not 'sha256sum' format", l)
		}
		paths = append(paths, l[66:])
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("not sorted by path: %q", paths)
	}
	for _, p := range paths {
		if p == "SHA256SUMS" || p == "SHA256SUMS.sig" {
			t.Errorf("%s is listed", p)
		}
	}
	// The switch's own parser accepts it, and every digest is the file's.
	listed, err := switcher.ParseSums(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 11 {
		t.Errorf("%d files listed", len(listed))
	}
	for p, sum := range listed {
		content, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil || sumOf(content) != sum {
			t.Errorf("%s: sum does not match (%v)", p, err)
		}
	}
}

func TestBuildManifestIsTheSwitchs(t *testing.T) {
	for _, components := range []string{"", "static"} {
		o := testOptions(t, staticSrc(t))
		o.Components = components
		id, _, err := Build(o)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(o.Out, id, "MANIFEST"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := switcher.ParseManifest(data)
		if err != nil {
			t.Fatal(err)
		}
		rid, err := switcher.ParseID(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Check(rid); err != nil {
			t.Errorf("the manifest does not describe %s: %v", id, err)
		}
		if m.Components != components || m.Counter != 7 || m.Commit != testCommit || m.BuiltOn != "dev-linux-amd64" ||
			m.GoVersion != "go1.27.2" || m.PackageSnapshot != "2026-11-21" {
			t.Errorf("manifest %+v", m)
		}
		if components == "" && strings.Contains(string(data), "components") {
			t.Errorf("a components line without static: %q", data)
		}
	}
}

func TestBuildDeterministic(t *testing.T) {
	src := staticSrc(t)
	bins := testBins(t)
	var all [][]string
	for i := 0; i < 2; i++ {
		o := testOptions(t, src)
		o.Bins = bins
		id, _, err := Build(o)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, listing(t, filepath.Join(o.Out, id)))
	}
	if !reflect.DeepEqual(all[0], all[1]) {
		t.Errorf("two builds differ:\n%q\n%q", all[0], all[1])
	}
}

func TestBuildIDPadsAndCuts(t *testing.T) {
	for _, tc := range []struct {
		counter uint64
		commit  string
		id      string
	}{
		{1, "abcdef0", "0001-abcdef0"},
		{12345, testCommit, "12345-0123456"},
		{999999999999999999, "ffffffffffff", "999999999999999999-fffffff"},
	} {
		o := testOptions(t, staticSrc(t))
		o.Counter, o.Commit = tc.counter, tc.commit
		id, _, err := Build(o)
		if err != nil || id != tc.id {
			t.Errorf("counter %d: %q, %v", tc.counter, id, err)
		}
		if _, err := switcher.ParseID(id); err != nil {
			t.Errorf("the switch does not read %s: %v", id, err)
		}
	}
}

// TestBuildRefusals gives each wrong input and checks the refusal and that
// nothing is left in the output folder.
func TestBuildRefusals(t *testing.T) {
	type tc struct {
		name  string
		mod   func(t *testing.T, o *Options)
		code  string
		usage bool
	}
	for _, c := range []tc{
		{"counter below 1", func(t *testing.T, o *Options) { o.Counter = 0 }, CodeCounter, true},
		{"counter too long for an id", func(t *testing.T, o *Options) { o.Counter = 1 << 62 }, CodeCounter, true},
		{"uppercase commit", func(t *testing.T, o *Options) { o.Commit = "ABCDEF0123" }, CodeCommit, true},
		{"short commit", func(t *testing.T, o *Options) { o.Commit = "abcdef" }, CodeCommit, true},
		{"commit with a newline", func(t *testing.T, o *Options) { o.Commit = "abcdef0\nid: 9999-aaaaaaa" }, CodeCommit, true},
		{"commit with a slash", func(t *testing.T, o *Options) { o.Commit = "abcdef0/../.." }, CodeCommit, true},
		{"commit too long", func(t *testing.T, o *Options) { o.Commit = strings.Repeat("a", 65) }, CodeCommit, true},
		{"go version with a newline", func(t *testing.T, o *Options) { o.GoVersion = "go1\ncounter: 99" }, CodeGoVersion, true},
		{"empty go version", func(t *testing.T, o *Options) { o.GoVersion = "" }, CodeGoVersion, true},
		{"built on with a space", func(t *testing.T, o *Options) { o.BuiltOn = "a host" }, CodeBuiltOn, true},
		{"built on with a hash", func(t *testing.T, o *Options) { o.BuiltOn = "#x" }, CodeBuiltOn, true},
		{"snapshot with a slash", func(t *testing.T, o *Options) { o.PackageSnapshot = "a/b" }, CodeSnapshot, true},
		{"snapshot too long", func(t *testing.T, o *Options) { o.PackageSnapshot = strings.Repeat("a", 129) }, CodeSnapshot, true},
		{"components other than static", func(t *testing.T, o *Options) { o.Components = "dynamic" }, CodeComponents, true},
		{"a static release with its own ah-pkgstep", func(t *testing.T, o *Options) {
			p := filepath.Join(t.TempDir(), PackageStepName)
			writeTestFile(t, p, "#!/bin/sh\nexit 0\n", 0o755)
			o.Bins = append(o.Bins, p)
		}, CodeStaticStep, true},
		{"no ah-switch", func(t *testing.T, o *Options) { o.Bins = o.Bins[:1] }, CodeBinMissing, true},
		{"no ah-noaicheck", func(t *testing.T, o *Options) { o.Bins = o.Bins[1:] }, CodeBinMissing, true},
		{"two binaries of one name", func(t *testing.T, o *Options) { o.Bins = append(o.Bins, o.Bins[0]) }, CodeBinTwice, true},
		{"binary with a newline in its name", func(t *testing.T, o *Options) {
			p := filepath.Join(t.TempDir(), "a\nb")
			writeTestFile(t, p, "x", 0o755)
			o.Bins = append(o.Bins, p)
		}, CodeBinName, true},
		{"binary that is a symbolic link", func(t *testing.T, o *Options) {
			l := filepath.Join(t.TempDir(), "ah-extra")
			if err := os.Symlink(o.Bins[0], l); err != nil {
				t.Fatal(err)
			}
			o.Bins = append(o.Bins, l)
		}, CodeNotRegular, false},
		{"binary that is a folder", func(t *testing.T, o *Options) {
			d := filepath.Join(t.TempDir(), "ah-extra")
			os.Mkdir(d, 0o755)
			o.Bins = append(o.Bins, d)
		}, CodeNotRegular, false},
		{"binary that is missing", func(t *testing.T, o *Options) { o.Bins = append(o.Bins, "/nonexistent/ah-extra") }, CodeNotRegular, false},
		{"source without etc", func(t *testing.T, o *Options) { os.RemoveAll(filepath.Join(o.Src, "etc")) }, CodeSrc, false},
		{"source without share", func(t *testing.T, o *Options) { os.RemoveAll(filepath.Join(o.Src, "share")) }, CodeSrc, false},
		{"source that is missing", func(t *testing.T, o *Options) { o.Src = filepath.Join(o.Src, "none") }, CodeSrc, false},
		{"symbolic link in etc", func(t *testing.T, o *Options) {
			os.Symlink("/etc/passwd", filepath.Join(o.Src, "etc/caddy/link"))
		}, CodeNotRegular, false},
		{"symbolic link to a folder in share", func(t *testing.T, o *Options) {
			os.Symlink(".", filepath.Join(o.Src, "share/loop"))
		}, CodeNotRegular, false},
		{"dangling symbolic link", func(t *testing.T, o *Options) {
			os.Symlink("nowhere", filepath.Join(o.Src, "etc/dangling"))
		}, CodeNotRegular, false},
		{"fifo in share", func(t *testing.T, o *Options) {
			if err := syscall.Mkfifo(filepath.Join(o.Src, "share/pipe"), 0o644); err != nil {
				t.Skip("no fifo:", err)
			}
		}, CodeNotRegular, false},
		{"socket in etc", func(t *testing.T, o *Options) {
			// A socket file made by binding to it.
			fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			if err != nil {
				t.Skip("no socket:", err)
			}
			defer syscall.Close(fd)
			if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: filepath.Join(o.Src, "etc/sock")}); err != nil {
				t.Skip("no socket:", err)
			}
		}, CodeNotRegular, false},
		{"name with a newline", func(t *testing.T, o *Options) {
			writeTestFile(t, filepath.Join(o.Src, "etc/caddy/a\nb"), "x", 0o644)
		}, CodeBadName, false},
		{"name with a space", func(t *testing.T, o *Options) {
			writeTestFile(t, filepath.Join(o.Src, "share/a b"), "x", 0o644)
		}, CodeBadName, false},
		{"name with a backslash", func(t *testing.T, o *Options) {
			writeTestFile(t, filepath.Join(o.Src, "share/a\\b"), "x", 0o644)
		}, CodeBadName, false},
		{"folder name with a newline", func(t *testing.T, o *Options) {
			writeTestFile(t, filepath.Join(o.Src, "etc/d\ne/x"), "x", 0o644)
		}, CodeBadName, false},
		{"release folder exists", func(t *testing.T, o *Options) {
			os.MkdirAll(filepath.Join(o.Out, "0007-0123456"), 0o755)
		}, CodeExists, false},
		{"release name taken by a file", func(t *testing.T, o *Options) {
			writeTestFile(t, filepath.Join(o.Out, "0007-0123456"), "x", 0o644)
		}, CodeExists, false},
		{"release name taken by a link", func(t *testing.T, o *Options) {
			os.Symlink("nowhere", filepath.Join(o.Out, "0007-0123456"))
		}, CodeExists, false},
		{"a partial folder left", func(t *testing.T, o *Options) {
			os.MkdirAll(filepath.Join(o.Out, ".0007-0123456.partial"), 0o755)
		}, CodeExists, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := testOptions(t, staticSrc(t))
			c.mod(t, &o)
			before := listing(t, o.Out)
			_, _, err := Build(o)
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if r.Code != c.code || r.Usage != c.usage {
				t.Errorf("refusal %q usage %v, want %q usage %v", r.Finding, r.Usage, c.code, c.usage)
			}
			line := r.Finding.String()
			if strings.ContainsAny(line, "\n\x00") || !strings.HasPrefix(line, "FAIL: "+c.code+" ") {
				t.Errorf("line %q", line)
			}
			if strings.Contains(line, "passwd") || strings.Contains(line, "nonexistent") {
				t.Errorf("line shows an input: %q", line)
			}
			if after := listing(t, o.Out); !reflect.DeepEqual(before, after) {
				t.Errorf("the output folder changed:\n%q\n%q", before, after)
			}
		})
	}
}

// TestBuildKeepsAnExistingRelease: a refused build leaves the existing folder
// as it was.
func TestBuildKeepsAnExistingRelease(t *testing.T) {
	o := testOptions(t, staticSrc(t))
	if _, _, err := Build(o); err != nil {
		t.Fatal(err)
	}
	before := listing(t, o.Out)
	_, _, err := Build(o)
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeExists {
		t.Fatalf("second build: %v", err)
	}
	if after := listing(t, o.Out); !reflect.DeepEqual(before, after) {
		t.Error("the existing release changed")
	}
}

// TestBuildCopiesDeepTrees: nested folders of the source arrive with their
// content, and an empty folder stays a folder.
func TestBuildCopiesDeepTrees(t *testing.T) {
	src := staticSrc(t)
	writeTestFile(t, filepath.Join(src, "etc/systemd/system/app.service.d/10-x.conf"), "[Service]\n", 0o644)
	os.MkdirAll(filepath.Join(src, "etc/nftables/empty"), 0o755)
	o := testOptions(t, src)
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(o.Out, id)
	for _, p := range []string{"etc/systemd/system/app.service.d/10-x.conf", "etc/nftables/empty"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// TestBuildOutOfSourceModes: a source file of mode 0600 or 0777 becomes 0644.
func TestBuildOutOfSourceModes(t *testing.T) {
	src := staticSrc(t)
	writeTestFile(t, filepath.Join(src, "share/exec.sh"), "x", 0o777)
	writeTestFile(t, filepath.Join(src, "share/suid"), "x", os.ModeSetuid|0o755)
	o := testOptions(t, src)
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"share/exec.sh", "share/suid", "etc/caddy/Caddyfile"} {
		info, err := os.Lstat(filepath.Join(o.Out, id, p))
		if err != nil || info.Mode() != 0o644 {
			t.Errorf("%s: %v %v", p, info, err)
		}
	}
}

// runStep runs the built ah-pkgstep with the given arguments.
func runStep(t *testing.T, p string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{p}, args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// TestBuildWritesAStaticPackageStep: a static release given no ah-pkgstep
// carries the builder's no-op one, mode 0755, listed in SHA256SUMS, and
// check accepts the folder.
func TestBuildWritesAStaticPackageStep(t *testing.T) {
	o := testOptions(t, staticSrc(t))
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(o.Out, id)
	p := filepath.Join(dir, "bin", PackageStepName)
	info, err := os.Lstat(p)
	if err != nil || info.Mode() != 0o755 {
		t.Fatalf("bin/ah-pkgstep: %v, %v", info, err)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != StaticPackageStep {
		t.Fatalf("content %q, %v", data, err)
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil || !strings.Contains(string(sums), sumOf(data)+"  bin/ah-pkgstep\n") {
		t.Errorf("SHA256SUMS does not list it: %q, %v", sums, err)
	}
	if f := Check(dir, CheckOptions{NoNoAICheck: true}); len(f) != 0 {
		t.Errorf("check: %s", lines(f))
	}
	if strings.ContainsAny(StaticPackageStep, "\r") || !strings.HasPrefix(StaticPackageStep, "#!/bin/sh\n") {
		t.Errorf("not a plain sh script: %q", StaticPackageStep)
	}
}

// TestStaticPackageStepRuns runs the script: it checks its arguments as the
// switch gives them (the --from may be empty on a first switch), succeeds
// only on a first switch or an unchanged snapshot, refuses a changed one
// (exit 1, a line on stderr), changes nothing and gives the same answer
// twice.
func TestStaticPackageStepRuns(t *testing.T) {
	o := testOptions(t, staticSrc(t))
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(o.Out, id, "bin", PackageStepName)
	before := listing(t, o.Out)
	for i := 0; i < 2; i++ {
		for _, from := range []string{"", "2026-11-21"} {
			code, out, errOut := runStep(t, p, "--release", filepath.Join(o.Out, id), "--from", from, "--to", "2026-11-21")
			if code != 0 || out != "ah-pkgstep: static release, no package change\n" || errOut != "" {
				t.Errorf("from %q: exit %d, %q, %q", from, code, out, errOut)
			}
		}
		for _, from := range []string{"2026-11-01", "2026-11-21x"} {
			code, out, errOut := runStep(t, p, "--release", filepath.Join(o.Out, id), "--from", from, "--to", "2026-11-21")
			if code != 1 || out != "" || errOut != "ah-pkgstep: refused: a static release changes no package, and its package snapshot is not the host's (build it with the host's --package-snapshot)\n" {
				t.Errorf("from %q: exit %d, %q, %q", from, code, out, errOut)
			}
		}
	}
	if after := listing(t, o.Out); !reflect.DeepEqual(before, after) {
		t.Error("the step changed the release")
	}
	for _, args := range [][]string{
		nil,
		{"--release", "d", "--from", "a"},
		{"--release", "d", "--to", "a", "--from", "b"},
		{"--from", "a", "--release", "d", "--to", "b"},
		{"--release", "d", "--from", "a", "--to", "b", "extra"},
	} {
		code, out, errOut := runStep(t, p, args...)
		if code != 2 || out != "" || !strings.HasPrefix(errOut, "usage: ah-pkgstep --release DIR --from OLD --to NEW") {
			t.Errorf("%q: exit %d, %q, %q", args, code, out, errOut)
		}
	}
}

// TestBuildKeepsAGivenPackageStep: a binary named ah-pkgstep is shipped as
// it is by a release that is not static (a static release is the generated
// no-op and refuses one, see TestBuildRefusals), and a release that is not
// static, given none, gets none made.
func TestBuildKeepsAGivenPackageStep(t *testing.T) {
	o := testOptions(t, staticSrc(t))
	o.Components = ""
	pkg := filepath.Join(t.TempDir(), "ah-pkgstep")
	writeTestFile(t, pkg, "#!/bin/sh\n# mine\nexit 0\n", 0o755)
	o.Bins = append(o.Bins, pkg)
	id, _, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(o.Out, id, "bin/ah-pkgstep"))
	if err != nil || string(data) != "#!/bin/sh\n# mine\nexit 0\n" {
		t.Errorf("content %q, %v", data, err)
	}

	o = testOptions(t, staticSrc(t))
	o.Components = ""
	id, _, err = Build(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(o.Out, id, "bin/ah-pkgstep")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a release that is not static got a package step: %v", err)
	}
}

// TestBuildPutsTheReleaseInItsUnits: in the files of etc/systemd, units and
// drop-ins, every {release} becomes the release's own folder on the host,
// so a unit runs its own release's binary; no other file is changed (the
// lists keep theirs for the switch). The sums are of the bytes written, the
// check passes, two builds are equal, and the switch accepts the folder.
func TestBuildPutsTheReleaseInItsUnits(t *testing.T) {
	src := dynamicSrc(t)
	unit := "[Unit]\nDescription=the seat\n\n[Service]\nExecStart={release}/bin/ataniq-seat --socket /run/seat/http.sock\n# {release} twice\n"
	set(t, src, "etc/systemd/system/ataniq-seat.service", unit)
	set(t, src, "etc/systemd/system/ataniq-seat.service.d/10-env.conf", "[Service]\nEnvironment=RELEASE={release}\n")
	set(t, src, "etc/caddy/Caddyfile", "# {release} stays here\n"+testCaddyfile)
	o := dynamicOptions(t, src)
	// A package step of its own, for the first switch the dry run below
	// is of.
	o.Bins = testBins(t, "ah-noaicheck", "ah-switch", "ataniq-seat", "ah-pkgstep")
	dir := built(t, o)
	const rel = "/opt/authority-host/releases/0007-0123456"
	read := func(p string) string {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got, want := read("etc/systemd/system/ataniq-seat.service"), strings.ReplaceAll(unit, "{release}", rel); got != want {
		t.Errorf("the unit:\n%s\nwant:\n%s", got, want)
	}
	if got := read("etc/systemd/system/ataniq-seat.service.d/10-env.conf"); got != "[Service]\nEnvironment=RELEASE="+rel+"\n" {
		t.Errorf("the drop-in: %q", got)
	}
	if got := read("etc/caddy/Caddyfile"); !strings.HasPrefix(got, "# {release} stays here\n") {
		t.Errorf("the Caddyfile changed: %q", got)
	}
	if got := read("share/validators.list"); got != dynValidators {
		t.Errorf("validators.list changed: %q", got)
	}
	if f := Check(dir, noRun); len(f) != 0 {
		t.Errorf("check: %s", lines(f))
	}

	o2 := dynamicOptions(t, src)
	o2.Bins = o.Bins
	dir2 := built(t, o2)
	if !reflect.DeepEqual(listing(t, dir), listing(t, dir2)) {
		t.Error("two builds of the same input differ")
	}

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	writeTestFile(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop ataniq-seat\n", 0o644)
	var log bytes.Buffer
	s := switcher.New(switcher.Config{Root: root, Dry: true, From: dir, Out: &log})
	if code := s.Switch(t.Context(), "0007-0123456"); code != switcher.ExitOK {
		t.Fatalf("the switch refuses it: exit %d\n%s", code, log.String())
	}
}

// TestBuildRefusesAUnitTooLargeToRead: a file of etc/systemd is read whole,
// so one over 4 MiB is refused by name and nothing is made.
func TestBuildRefusesAUnitTooLargeToRead(t *testing.T) {
	src := dynamicSrc(t)
	set(t, src, "etc/systemd/system/big.conf", strings.Repeat("#\n", maxSmallFile/2+1))
	o := dynamicOptions(t, src)
	_, _, err := Build(o)
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeTooLarge || r.Name != "etc/systemd/system/big.conf" || r.Usage {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(o.Out); len(entries) != 0 {
		t.Errorf("a refused build left %v", entries)
	}
}
