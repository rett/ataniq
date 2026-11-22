package release

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/authorityhost/noaicheck"
)

const testNFT = "table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t\tcounter packets 12 bytes 3456\n\t\ttcp dport 22 accept\n\t}\n}\n"

// baselineInputs writes the four input files and returns baseline options
// for a static source tree and a new output folder.
func baselineInputs(t *testing.T) BaselineOptions {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "units"), "# enabled units\nsshd.service\ngetty@.service\n\nsshd.service\n  caddy.service  \n", 0o644)
	writeTestFile(t, filepath.Join(dir, "ports"), "443\n22\n# web\n80\n22\n", 0o644)
	writeTestFile(t, filepath.Join(dir, "packages"), "systemd\nbase\ncaddy\nlib32-glibc\nbase\ngtk3\n", 0o644)
	writeTestFile(t, filepath.Join(dir, "nft"), testNFT, 0o644)
	return BaselineOptions{
		Src:   staticSrc(t),
		Units: filepath.Join(dir, "units"), Ports: filepath.Join(dir, "ports"),
		Packages: filepath.Join(dir, "packages"), NFT: filepath.Join(dir, "nft"),
		Out: filepath.Join(t.TempDir(), "baseline"),
	}
}

func readOut(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// body is the lines of a generated file that are not comments.
func body(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestBaselineFillsTheAllowlists(t *testing.T) {
	o := baselineInputs(t)
	o.Stamp = "2026-11-22"
	srcBefore := listing(t, o.Src)
	out, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	if out != o.Out {
		t.Errorf("out = %q", out)
	}
	if got := body(readOut(t, out, "share/units.allow")); !reflect.DeepEqual(got, []string{"caddy.service", "getty@.service", "sshd.service"}) {
		t.Errorf("units: %q", got)
	}
	if got := body(readOut(t, out, "share/ports.allow")); !reflect.DeepEqual(got, []string{"22", "80", "443"}) {
		t.Errorf("ports (numeric order): %q", got)
	}
	if got := body(readOut(t, out, "share/packages.allow")); !reflect.DeepEqual(got, []string{"base", "caddy", "gtk3", "lib32-glibc", "systemd"}) {
		t.Errorf("packages: %q", got)
	}
	digest := noaicheck.NFTDigest([]byte(testNFT))
	if got := body(readOut(t, out, "share/nft.digest")); !reflect.DeepEqual(got, []string{digest}) {
		t.Errorf("nft.digest: %q, want %s", got, digest)
	}

	// The headers say what the lists were taken from, with the stamp, and
	// name no host.
	for _, f := range []string{"units.allow", "ports.allow", "packages.allow", "nft.digest"} {
		text := readOut(t, out, "share/"+f)
		if !strings.HasPrefix(text, "# ") || !strings.Contains(text, "ah-release baseline") || !strings.Contains(text, "(2026-11-22)") {
			t.Errorf("%s header:\n%s", f, text)
		}
	}
	// What is written is what the no-AI check reads back.
	for f, parse := range map[string]func(string) error{
		"units.allow":    func(s string) error { _, err := noaicheck.ParseList(strings.NewReader(s)); return err },
		"packages.allow": func(s string) error { _, err := noaicheck.ParseList(strings.NewReader(s)); return err },
		"ports.allow":    func(s string) error { _, err := noaicheck.ParsePorts(strings.NewReader(s)); return err },
		"nft.digest":     func(s string) error { _, err := noaicheck.ParseDigest(strings.NewReader(s)); return err },
	} {
		if err := parse(readOut(t, out, "share/"+f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	// Nothing is written into the source, the rest of the tree is copied as
	// it is, and the copy has the source's own list files besides.
	if after := listing(t, o.Src); !reflect.DeepEqual(srcBefore, after) {
		t.Errorf("the source changed:\n%q\n%q", srcBefore, after)
	}
	for _, f := range []string{"etc/caddy/Caddyfile", "share/validators.list", "share/reload.list", "share/health.cmd", "share/stop.list", "share/ai-names.list"} {
		if readOut(t, out, f) != readOut(t, o.Src, f) {
			t.Errorf("%s differs from the source's", f)
		}
	}
	if _, err := os.Lstat(filepath.Join(out, "bin")); err == nil {
		t.Error("a bin folder was made")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(out), "."+filepath.Base(out)+".partial")); err == nil {
		t.Error("the staging folder is left")
	}
	// Folders 0755, files 0644.
	for _, l := range listing(t, out) {
		if m := strings.Fields(l)[1]; m != "drwxr-xr-x" && m != "-rw-r--r--" {
			t.Errorf("mode: %s", l)
		}
	}

}

// TestBaselineIsDeterministic: no time, user or host is in a file, and the
// same inputs make the same bytes; without a stamp no stamp is written.
func TestBaselineIsDeterministic(t *testing.T) {
	o := baselineInputs(t)
	a, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	o2 := o
	o2.Out = filepath.Join(t.TempDir(), "baseline")
	b, err := Baseline(o2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listing(t, a), listing(t, b)) {
		t.Error("two runs differ")
	}
	host, _ := os.Hostname()
	for _, f := range []string{"units.allow", "ports.allow", "packages.allow", "nft.digest"} {
		text := readOut(t, a, "share/"+f)
		if strings.Contains(text, "host (") || (host != "" && strings.Contains(text, host)) {
			t.Errorf("%s holds a stamp or a host name:\n%s", f, text)
		}
	}
}

// TestBaselineDigestIgnoresCounters: the digest is the one of
// "ah-noaicheck nft-digest", which drops the values that change.
func TestBaselineDigestIgnoresCounters(t *testing.T) {
	o := baselineInputs(t)
	writeTestFile(t, o.NFT, strings.Replace(testNFT, "packets 12 bytes 3456", "packets 99 bytes 1", 1), 0o644)
	out, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(readOut(t, out, "share/nft.digest")); !reflect.DeepEqual(got, []string{noaicheck.NFTDigest([]byte(testNFT))}) {
		t.Errorf("digest %q", got)
	}
}

func TestBaselineOutputBuilds(t *testing.T) {
	o := baselineInputs(t)
	out, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	b := testOptions(t, out)
	id, _, err := Build(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(readOut(t, filepath.Join(b.Out, id), "share/ports.allow")); !reflect.DeepEqual(got, []string{"22", "80", "443"}) {
		t.Errorf("ports in the release: %q", got)
	}
}

func TestBaselineDefaultOutIsNew(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	o := baselineInputs(t)
	o.Out = ""
	a, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || filepath.Dir(a) != tmp || filepath.Dir(b) != tmp {
		t.Errorf("default folders %q, %q (temporary folder %q)", a, b, tmp)
	}
	if _, err := os.Lstat(filepath.Join(a, "share/units.allow")); err != nil {
		t.Error(err)
	}
	info, err := os.Lstat(a)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, %v", info, err)
	}
}

// bigInput is a comment padding of n lines of the given length, then entry.
func bigInput(total int, entry string) string {
	var b strings.Builder
	for b.Len() < total-len(entry)-1 {
		n := min(1000, total-len(entry)-1-b.Len())
		b.WriteString("#" + strings.Repeat("x", n-2) + "\n")
	}
	b.WriteString(entry + "\n")
	return b.String()
}

func TestBaselineRefusals(t *testing.T) {
	type tc struct {
		name  string
		mod   func(t *testing.T, o *BaselineOptions)
		code  string
		input string // the name the line is about
		usage bool
	}
	setInput := func(field func(o *BaselineOptions) *string, content string) func(t *testing.T, o *BaselineOptions) {
		return func(t *testing.T, o *BaselineOptions) { writeTestFile(t, *field(o), content, 0o644) }
	}
	units := func(o *BaselineOptions) *string { return &o.Units }
	ports := func(o *BaselineOptions) *string { return &o.Ports }
	packages := func(o *BaselineOptions) *string { return &o.Packages }
	nft := func(o *BaselineOptions) *string { return &o.NFT }
	for _, c := range []tc{
		{"no src", func(t *testing.T, o *BaselineOptions) { o.Src = "" }, CodeBaselineArgs, "", true},
		{"no units", func(t *testing.T, o *BaselineOptions) { o.Units = "" }, CodeBaselineArgs, "", true},
		{"no ports", func(t *testing.T, o *BaselineOptions) { o.Ports = "" }, CodeBaselineArgs, "", true},
		{"no packages", func(t *testing.T, o *BaselineOptions) { o.Packages = "" }, CodeBaselineArgs, "", true},
		{"no nft", func(t *testing.T, o *BaselineOptions) { o.NFT = "" }, CodeBaselineArgs, "", true},
		{"stamp with a newline", func(t *testing.T, o *BaselineOptions) { o.Stamp = "a\nb" }, CodeStamp, "", true},
		{"stamp with a hash", func(t *testing.T, o *BaselineOptions) { o.Stamp = "a #b" }, CodeStamp, "", true},
		{"stamp too long", func(t *testing.T, o *BaselineOptions) { o.Stamp = strings.Repeat("a", 129) }, CodeStamp, "", true},

		{"units is a fifo", func(t *testing.T, o *BaselineOptions) {
			os.Remove(o.Units)
			if err := syscall.Mkfifo(o.Units, 0o644); err != nil {
				t.Skip("no fifo:", err)
			}
		}, CodeInputNotFile, "units", false},
		{"ports is a link", func(t *testing.T, o *BaselineOptions) {
			real := o.Ports + ".real"
			os.Rename(o.Ports, real)
			os.Symlink(real, o.Ports)
		}, CodeInputNotFile, "ports", false},
		{"packages is a folder", func(t *testing.T, o *BaselineOptions) {
			os.Remove(o.Packages)
			os.Mkdir(o.Packages, 0o755)
		}, CodeInputNotFile, "packages", false},
		{"nft is missing", func(t *testing.T, o *BaselineOptions) { os.Remove(o.NFT) }, CodeInputNotFile, "nft", false},
		{"nft is a device", func(t *testing.T, o *BaselineOptions) { o.NFT = "/dev/null" }, CodeInputNotFile, "nft", false},

		{"units over 2 MiB", setInput(units, bigInput(MaxInputBytes+1, "sshd.service")), CodeInputTooLarge, "units", false},
		{"ports over 2 MiB", setInput(ports, bigInput(MaxInputBytes+1, "22")), CodeInputTooLarge, "ports", false},
		{"packages over 2 MiB", setInput(packages, bigInput(MaxInputBytes+1, "base")), CodeInputTooLarge, "packages", false},
		{"nft over 2 MiB", setInput(nft, strings.Repeat("a", MaxInputBytes+1)), CodeInputTooLarge, "nft", false},

		{"unit with a space inside", setInput(units, "ssh d.service\n"), CodeInputInvalid, "units", false},
		{"unit with a slash", setInput(units, "../x.service\n"), CodeInputInvalid, "units", false},
		{"unit with a backslash", setInput(units, "dev-a\\x2db.device\n"), CodeInputInvalid, "units", false},
		{"unit without a type", setInput(units, "sshd\n"), CodeInputInvalid, "units", false},
		{"unit with a tab", setInput(units, "sshd.service\t\n"), CodeInputInvalid, "units", false},
		{"unit with a carriage return", setInput(units, "sshd.service\r\n"), CodeInputInvalid, "units", false},
		{"unit that is not ASCII", setInput(units, "sshdé.service\n"), CodeInputInvalid, "units", false},
		{"unit line over 4 KiB", setInput(units, strings.Repeat("a", maxInputLine)+".service\n"), CodeInputInvalid, "units", false},
		{"enabled unit file line", setInput(units, "sshd.service enabled\n"), CodeInputInvalid, "units", false},
		{"port 0", setInput(ports, "0\n"), CodeInputInvalid, "ports", false},
		{"port 65536", setInput(ports, "65536\n"), CodeInputInvalid, "ports", false},
		{"port with a zero", setInput(ports, "080\n"), CodeInputInvalid, "ports", false},
		{"port with an address", setInput(ports, "127.0.0.1:22\n"), CodeInputInvalid, "ports", false},
		{"port negative", setInput(ports, "-1\n"), CodeInputInvalid, "ports", false},
		{"package with a space", setInput(packages, "base devel\n"), CodeInputInvalid, "packages", false},
		{"package starting with a hyphen", setInput(packages, "-base\n"), CodeInputInvalid, "packages", false},
		{"package with a version", setInput(packages, "base 3-1\n"), CodeInputInvalid, "packages", false},
		{"package with a slash", setInput(packages, "core/base\n"), CodeInputInvalid, "packages", false},

		{"empty units", setInput(units, ""), CodeInputEmpty, "units", false},
		{"units of comments", setInput(units, "# none\n\n  # none\n"), CodeInputEmpty, "units", false},
		{"empty ports", setInput(ports, "\n\n"), CodeInputEmpty, "ports", false},
		{"empty packages", setInput(packages, "# none\n"), CodeInputEmpty, "packages", false},
		{"empty nft", setInput(nft, ""), CodeInputEmpty, "nft", false},
		{"blank nft", setInput(nft, "\n  \n\t\n"), CodeInputEmpty, "nft", false},

		{"source without share", func(t *testing.T, o *BaselineOptions) { os.RemoveAll(filepath.Join(o.Src, "share")) }, CodeSrc, "", false},
		{"link in the source", func(t *testing.T, o *BaselineOptions) {
			os.Symlink("/etc/passwd", filepath.Join(o.Src, "etc/link"))
		}, CodeNotRegular, "etc/link", false},
		{"output exists", func(t *testing.T, o *BaselineOptions) { os.MkdirAll(o.Out, 0o755) }, CodeOutExists, "", false},
		{"output is a file", func(t *testing.T, o *BaselineOptions) { writeTestFile(t, o.Out, "x", 0o644) }, CodeOutExists, "", false},
		{"output is a dangling link", func(t *testing.T, o *BaselineOptions) { os.Symlink("nowhere", o.Out) }, CodeOutExists, "", false},
		{"a partial folder left", func(t *testing.T, o *BaselineOptions) {
			os.MkdirAll(filepath.Join(filepath.Dir(o.Out), ".baseline.partial"), 0o755)
		}, CodeOutExists, "", false},
		{"output is the source", func(t *testing.T, o *BaselineOptions) { o.Out = o.Src }, CodeOut, "", false},
		{"output in the source", func(t *testing.T, o *BaselineOptions) { o.Out = filepath.Join(o.Src, "share", "out") }, CodeOut, "", false},
		{"output in the source by a link", func(t *testing.T, o *BaselineOptions) {
			l := filepath.Join(t.TempDir(), "l")
			os.Symlink(o.Src, l)
			o.Out = filepath.Join(l, "new", "out")
		}, CodeOut, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := baselineInputs(t)
			c.mod(t, &o)
			var srcBefore, outBefore []string
			if _, err := os.Stat(o.Src); err == nil && o.Src != "" {
				srcBefore = listing(t, o.Src)
			}
			parent := filepath.Dir(o.Out)
			if _, err := os.Stat(parent); err == nil && !strings.HasPrefix(o.Out, o.Src+"/") && o.Out != o.Src && !strings.Contains(c.name, "by a link") {
				outBefore = listing(t, parent)
			}
			_, err := Baseline(o)
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if r.Code != c.code || r.Usage != c.usage || (c.input != "" && r.Name != c.input) {
				t.Errorf("refusal %q (usage %v), want %s %s (usage %v)", r.Finding, r.Usage, c.code, c.input, c.usage)
			}
			line := r.Finding.String()
			if strings.ContainsAny(line, "\n\x00") || !strings.HasPrefix(line, "FAIL: "+c.code+" ") {
				t.Errorf("line %q", line)
			}
			for _, leak := range []string{"sshd", "passwd", o.Src, o.Out} {
				if leak != "" && strings.Contains(line, leak) {
					t.Errorf("line shows an input: %q", line)
				}
			}
			if srcBefore != nil {
				if after := listing(t, o.Src); !reflect.DeepEqual(srcBefore, after) {
					t.Errorf("the source changed:\n%q\n%q", srcBefore, after)
				}
			}
			if outBefore != nil {
				if after := listing(t, parent); !reflect.DeepEqual(outBefore, after) {
					t.Errorf("a refused run left something:\n%q\n%q", outBefore, after)
				}
			}
		})
	}
}

// TestBaselineAcceptsInputAtTheLimit: a file of exactly 2 MiB and a line of
// exactly 4 KiB are accepted.
func TestBaselineAcceptsInputAtTheLimit(t *testing.T) {
	o := baselineInputs(t)
	writeTestFile(t, o.Packages, bigInput(MaxInputBytes, "base"), 0o644)
	writeTestFile(t, o.Units, "# "+strings.Repeat("x", maxInputLine-2)+"\nsshd.service\n", 0o644)
	if info, err := os.Stat(o.Packages); err != nil || info.Size() != MaxInputBytes {
		t.Fatalf("input size %v, %v", info, err)
	}
	out, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(readOut(t, out, "share/packages.allow")); !reflect.DeepEqual(got, []string{"base"}) {
		t.Errorf("packages %q", got)
	}
}

// TestBaselineNoNewlineAtEnd: the last line needs no newline.
func TestBaselineNoNewlineAtEnd(t *testing.T) {
	o := baselineInputs(t)
	writeTestFile(t, o.Ports, "22\n80", 0o644)
	out, err := Baseline(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(readOut(t, out, "share/ports.allow")); !reflect.DeepEqual(got, []string{"22", "80"}) {
		t.Errorf("ports %q", got)
	}
}
