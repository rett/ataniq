package main

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/noaicheck"
)

const (
	secret  = "sk-planted-SECRET-9876543210"
	content = "CONTENT-OF-A-PLANTED-FILE"
)

func write(t *testing.T, dir, rel, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, dir, rel, target string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// release copies the shipped lists into a release folder and fills the
// allowlists the lead fills at deploy time; extra share/ files are added.
func release(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	shipped := filepath.Join("..", "..", "deploy", "authority-host", "share")
	entries, err := os.ReadDir(shipped)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(shipped, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, "share/"+e.Name(), string(data))
	}
	write(t, dir, "share/units.allow", "sshd.service\n")
	write(t, dir, "share/ports.allow", "22\n")
	for name, data := range extra {
		write(t, dir, "share/"+name, data)
	}
	return dir
}

// host is a small clean tree standing for the host's root folder.
func host(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "etc/passwd", "root:x:0:0::/root:/bin/bash\nuser:x:1000:1000::/home/user:/bin/bash\n")
	write(t, root, "usr/bin/bash", "x")
	symlink(t, root, "bin", "usr/bin")
	write(t, root, "home/user/.bashrc", "export LANG=C.UTF-8\n")
	write(t, root, "root/.profile", "umask 077\n")
	write(t, root, "etc/environment", "LANG=C.UTF-8\n")
	symlink(t, root, "etc/systemd/system/multi-user.target.wants/sshd.service", "/usr/lib/systemd/system/sshd.service")
	write(t, root, "usr/lib/systemd/system/sshd.service", "[Service]\nExecStart=/usr/bin/sshd -D\n")
	write(t, root, "proc/1/comm", "systemd\n")
	write(t, root, "var/lib/pacman/local/bash-5-1/desc", "%NAME%\nbash\n")
	write(t, root, "proc/net/tcp", "  sl  local_address rem_address   st\n"+
		"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 1\n")
	return root
}

func runCmd(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestClean(t *testing.T) {
	code, out, errOut := runCmd(t, "", "--root", host(t), "--release", release(t, nil))
	if code != exitClean || out != "NOTE packages not enforced\nNOTE nft not pinned\nRUN incomplete\nok\n" || errOut != "" {
		t.Errorf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

func TestEachCheckFails(t *testing.T) {
	cases := []struct {
		name  string
		plant func(root string)
		want  string
	}{
		{"binary", func(r string) { write(t, r, "usr/local/bin/claude", content) }, "HIT binary /usr/local/bin/claude"},
		{"tool", func(r string) { write(t, r, "home/user/.local/bin/npm", content) }, "HIT tool /home/user/.local/bin/npm"},
		{"credential folder", func(r string) { write(t, r, "home/user/.codex/auth.json", content) },
			"HIT credential /home/user/.codex"},
		{"credential in a config folder", func(r string) { write(t, r, "root/.config/opencode/x", content) },
			"HIT credential /root/.config/opencode"},
		{"env name", func(r string) { write(t, r, "etc/profile.d/k.sh", "export ANTHROPIC_API_KEY="+secret+"\n") },
			"HIT env /etc/profile.d/k.sh ANTHROPIC_API_KEY"},
		{"env in a unit", func(r string) {
			write(t, r, "usr/lib/systemd/system/sshd.service", "[Service]\nEnvironment=OPENAI_API_KEY="+secret+"\n")
		}, "HIT env /usr/lib/systemd/system/sshd.service OPENAI_API_KEY"},
		{"process", func(r string) { write(t, r, "proc/4242/comm", "gemini\n") }, "HIT process /proc/4242 gemini"},
		{"unit", func(r string) {
			symlink(t, r, "etc/systemd/system/timers.target.wants/agent.timer", "/etc/systemd/system/agent.timer")
		}, "HIT unit /etc/systemd/system/timers.target.wants/agent.timer"},
		{"package", func(r string) { write(t, r, "var/lib/pacman/local/mise-1-1/desc", "%NAME%\nmise\n") },
			"HIT package mise forbidden"},
		{"listening port", func(r string) { write(t, r, "proc/net/tcp6", tcp6Listen8080) }, "HIT port tcp6:[::]:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := host(t)
			tc.plant(root)
			code, out, errOut := runCmd(t, "", "--root", root, "--release", release(t, nil))
			if code != exitHit || !strings.Contains(out, tc.want+"\n") || !strings.HasSuffix(out, "FAILED 1 hit(s)\n") {
				t.Errorf("exit %d\nstdout %s\nwant %s", code, out, tc.want)
			}
			for _, s := range []string{out, errOut} {
				if strings.Contains(s, secret) || strings.Contains(s, content) {
					t.Errorf("output holds planted content:\n%s", s)
				}
			}
		})
	}
}

func TestPackagesEnforced(t *testing.T) {
	root := host(t)
	write(t, root, "var/lib/pacman/local/zsh-5-1/desc", "%NAME%\nzsh\n")
	code, out, _ := runCmd(t, "", "--root", root, "--release", release(t, map[string]string{"packages.allow": "bash\n"}))
	if code != exitHit || out != "HIT package zsh not-allowed\nNOTE nft not pinned\nRUN incomplete\nFAILED 1 hit(s)\n" {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestNFT(t *testing.T) {
	listing := "table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t}\n}\n"
	digest := noaicheck.NFTDigest([]byte(listing))
	dir := t.TempDir()
	file := filepath.Join(dir, "ruleset")
	write(t, dir, "ruleset", listing)
	rel := release(t, map[string]string{"nft.digest": digest + "\n"})

	code, out, _ := runCmd(t, "", "nft-digest", file)
	if code != exitClean || out != digest+"\n" {
		t.Errorf("nft-digest: exit %d, %q", code, out)
	}
	code, out, _ = runCmd(t, listing, "nft-digest", "-")
	if code != exitClean || out != digest+"\n" {
		t.Errorf("nft-digest from stdin: exit %d, %q", code, out)
	}
	if code, out, _ = runCmd(t, "", "--root", host(t), "--release", rel, "--nft-listing", file); code != exitClean {
		t.Errorf("same rules: exit %d\n%s", code, out)
	}
	if code, out, _ = runCmd(t, listing, "--root", host(t), "--release", rel, "--nft-listing", "-"); code != exitClean {
		t.Errorf("same rules from stdin: exit %d\n%s", code, out)
	}
	code, out, _ = runCmd(t, "table inet filter {\n}\n", "--root", host(t), "--release", rel, "--nft-listing", "-")
	if code != exitHit || !strings.Contains(out, "HIT nft ruleset digest-differs\n") {
		t.Errorf("changed rules: exit %d\n%s", code, out)
	}
	code, out, _ = runCmd(t, "", "--root", host(t), "--release", rel)
	if code != exitHit || !strings.Contains(out, "HIT nft ruleset listing-missing\n") {
		t.Errorf("no listing: exit %d\n%s", code, out)
	}
}

func TestPorts(t *testing.T) {
	root := host(t)
	rel := release(t, nil)
	if code, out, _ := runCmd(t, "", "ports", "--root", root, "--release", rel); code != exitClean || out != "RUN complete\nok\n" {
		t.Errorf("clean: exit %d\n%s", code, out)
	}
	write(t, root, "proc/net/tcp6", tcp6Listen8080)
	code, out, _ := runCmd(t, "", "ports", "--root", root, "--release", rel)
	if code != exitHit || out != "HIT port tcp6:[::]:8080\nRUN complete\nFAILED 1 hit(s)\n" {
		t.Errorf("planted: exit %d\n%s", code, out)
	}
	if code, _, _ := runCmd(t, "", "ports", "--root", root, "--release", rel, "--nft-listing", "x"); code != exitError {
		t.Errorf("ports with --nft-listing: exit %d", code)
	}
}

const tcp6Listen8080 = "  sl  local_address rem_address   st\n" +
	"   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 0 0 0\n"

// The switch's call: --require-policy with every policy of the release set
// passes on a clean host and says the run was complete; each policy left
// unset is a hit of its own, and so is a pinned digest without a listing.
func TestRequirePolicy(t *testing.T) {
	listing := "table inet filter {\n}\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "ruleset")
	write(t, dir, "ruleset", listing)
	full := map[string]string{"packages.allow": "bash\n", "nft.digest": noaicheck.NFTDigest([]byte(listing)) + "\n"}
	with := func(name, data string) map[string]string {
		m := map[string]string{}
		for k, v := range full {
			m[k] = v
		}
		m[name] = data
		return m
	}
	code, out, errOut := runCmd(t, "", "--root", host(t), "--release", release(t, full), "--require-policy", "--nft-listing", file)
	if code != exitClean || out != "RUN complete\nok\n" || errOut != "" {
		t.Errorf("all set: exit %d\n%s%s", code, out, errOut)
	}
	cases := []struct {
		name  string
		files map[string]string
		args  []string
		want  string
	}{
		{"empty units.allow", with("units.allow", "# none\n"), []string{"--nft-listing", file}, "HIT policy units.allow empty\n"},
		{"empty packages.allow", with("packages.allow", ""), []string{"--nft-listing", file}, "HIT policy packages.allow empty\n"},
		{"empty ports.allow", with("ports.allow", ""), []string{"--nft-listing", file}, "HIT policy ports.allow empty\n"},
		{"no nft.digest", map[string]string{"packages.allow": "bash\n"}, []string{"--nft-listing", file}, "HIT policy nft.digest missing\n"},
		{"pinned, no listing", full, nil, "HIT nft ruleset listing-missing\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--root", host(t), "--release", release(t, tc.files), "--require-policy"}, tc.args...)
			code, out, _ := runCmd(t, "", args...)
			if code != exitHit || !strings.Contains(out, tc.want) || strings.Contains(out, "NOTE") {
				t.Errorf("exit %d\n%s\nwant %s", code, out, tc.want)
			}
		})
	}
	// Without the flag the same release prints notes, and the run says it
	// was not complete.
	code, out, _ = runCmd(t, "", "--root", host(t), "--release", release(t, map[string]string{"packages.allow": "bash\n"}))
	if code != exitClean || out != "NOTE nft not pinned\nRUN incomplete\nok\n" {
		t.Errorf("without the flag: exit %d\n%s", code, out)
	}
}

// --phase: pre reads the candidate release's own units and leaves out the
// listening ports and the digest; post adds them back; full (the default)
// does not read the release's files.
func TestPhase(t *testing.T) {
	listing := "table inet filter {\n}\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "ruleset")
	write(t, dir, "ruleset", listing)
	rel := release(t, map[string]string{"packages.allow": "bash\n", "nft.digest": noaicheck.NFTDigest([]byte(listing)) + "\n"})
	write(t, rel, "etc/systemd/seat.service", "[Service]\nEnvironment=OPENAI_API_KEY="+secret+"\n")
	root := host(t)
	write(t, root, "proc/net/tcp6", tcp6Listen8080)
	unitHit := "HIT env " + filepath.ToSlash(rel) + "/etc/systemd/seat.service OPENAI_API_KEY\n"
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--phase", "pre"}, unitHit + "RUN complete\nFAILED 1 hit(s)\n"},
		{[]string{"--phase", "post", "--nft-listing", file}, unitHit + "HIT port tcp6:[::]:8080\nRUN complete\nFAILED 2 hit(s)\n"},
		{[]string{"--phase", "full", "--nft-listing", file}, "HIT port tcp6:[::]:8080\nRUN complete\nFAILED 1 hit(s)\n"},
		{[]string{"--nft-listing", file}, "HIT port tcp6:[::]:8080\nRUN complete\nFAILED 1 hit(s)\n"},
	}
	for _, tc := range cases {
		args := append([]string{"--root", root, "--release", rel, "--require-policy"}, tc.args...)
		code, out, errOut := runCmd(t, "", args...)
		if code != exitHit || out != tc.want || errOut != "" {
			t.Errorf("%v: exit %d\n%s%s\nwant\n%s", tc.args, code, out, errOut, tc.want)
		}
		if strings.Contains(out, secret) {
			t.Error("the output holds the value")
		}
	}
	code, out, _ := runCmd(t, "", "--root", root, "--release", rel, "--phase", "pre", "--json")
	if code != exitHit || !strings.Contains(out, `"phase":"pre"`) {
		t.Errorf("json: exit %d %s", code, out)
	}
	for _, args := range [][]string{
		{"--root", root, "--release", rel, "--phase", "later"},
		{"--root", root, "--release", rel, "--phase", "pre", "--nft-listing", file},
		{"ports", "--root", root, "--release", rel, "--phase", "pre"},
	} {
		if code, out, errOut := runCmd(t, "", args...); code != exitError || out != "" || !strings.Contains(errOut, "phase") {
			t.Errorf("%v: exit %d %q", args, code, errOut)
		}
	}
	if code, _, errOut := runCmd(t, "", "--root", root, "--release", filepath.Join(rel, "share", "ai-names.list"), "--phase", "pre"); code != exitError || errOut == "" {
		t.Errorf("a release that is not a folder: exit %d", code)
	}
}

func TestJSON(t *testing.T) {
	root := host(t)
	write(t, root, "etc/environment", "GEMINI_API_KEY="+secret+"\n")
	code, out, _ := runCmd(t, "", "--root", root, "--release", release(t, nil), "--json")
	var r struct {
		Result string
		Count  int
		Hits   []noaicheck.Hit
		Notes  []string
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code != exitHit || r.Result != "failed" || r.Count != 1 || r.Hits[0] != (noaicheck.Hit{Kind: "env", Path: "/etc/environment", Detail: "GEMINI_API_KEY"}) {
		t.Errorf("exit %d %+v", code, r)
	}
	if strings.Contains(out, secret) {
		t.Error("the JSON holds the value")
	}
}

func TestUsageAndReadErrors(t *testing.T) {
	root := host(t)
	rel := release(t, nil)
	badList := release(t, map[string]string{"units.allow": "a/b\n"})
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--root", root, "--release", rel, "--fast"}, "flag provided but not defined"},
		{"extra argument", []string{"--root", root, "--release", rel, "now"}, "unexpected argument"},
		{"empty root", []string{"--root", "", "--release", rel}, "--root is empty"},
		{"missing root", []string{"--root", filepath.Join(root, "missing"), "--release", rel}, "no such file"},
		{"missing release", []string{"--root", root, "--release", filepath.Join(rel, "missing")}, "the release's lists"},
		{"bad list", []string{"--root", root, "--release", badList}, "units.allow"},
		{"missing listing", []string{"--root", root, "--release", rel, "--nft-listing", filepath.Join(rel, "none")}, "no such file"},
		{"nft-digest without a file", []string{"nft-digest"}, "takes one FILE"},
		{"nft-digest with two", []string{"nft-digest", "a", "b"}, "takes one FILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runCmd(t, "", tc.args...)
			if code != exitError || out != "" || !strings.Contains(errOut, tc.want) {
				t.Errorf("exit %d\nstdout %q\nstderr %q\nwant %q", code, out, errOut, tc.want)
			}
		})
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"ports", "--help"}, {"nft-digest", "--help"}} {
		if code, out, _ := runCmd(t, "", args...); code != exitClean || !strings.HasPrefix(out, "usage: ah-noaicheck") {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// A path or flag the caller passed reaches stderr only escaped and bounded:
// a control byte, a quote or an invalid byte cannot forge a line or reach
// the terminal, and a long one is cut.
func TestDiagnosticsAreEscaped(t *testing.T) {
	root := host(t)
	rel := release(t, nil)
	planted := "x\nok\x1b[2J\u202e\xff"
	long := strings.Repeat("a", 4*noaicheck.MaxField)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"a flag", []string{"--root", root, "--release", rel, "--" + planted}, `-x\nok\x1b[2J\u202e\xff`},
		{"an argument", []string{"--root", root, "--release", rel, planted}, `"x\nok\x1b[2J\u202e\xff"`},
		{"a root", []string{"--root", filepath.Join(root, "missing", planted), "--release", rel}, `missing/x\nok\x1b[2J\u202e\xff"`},
		{"a release", []string{"--root", root, "--release", filepath.Join(rel, planted)}, `x\nok\x1b[2J\u202e\xff/share/ai-names.list"`},
		{"a listing", []string{"--root", root, "--release", rel, "--nft-listing", filepath.Join(rel, planted)}, `x\nok\x1b[2J\u202e\xff"`},
		{"nft-digest", []string{"nft-digest", filepath.Join(rel, planted)}, `x\nok\x1b[2J\u202e\xff"`},
		{"a long path", []string{"--root", root, "--release", rel, "--nft-listing", "/" + long}, `aaa...: `},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runCmd(t, "", tc.args...)
			if code != exitError || out != "" {
				t.Fatalf("exit %d stdout %q", code, out)
			}
			first, rest, _ := strings.Cut(errOut, "\n")
			for i := 0; i < len(first); i++ {
				if b := first[i]; b < 0x20 || b >= 0x7f {
					t.Errorf("a raw byte %#x in %q", b, first)
					break
				}
			}
			if !strings.Contains(first, tc.want) || !(rest == "" || strings.HasPrefix(rest, "usage: ")) {
				t.Errorf("stderr %q, want its first line to hold %q", errOut, tc.want)
			}
			if len(first) > 3*noaicheck.MaxField {
				t.Errorf("a diagnostic of %d bytes", len(first))
			}
		})
	}
}

// The program writes nothing: the tree is the same before and after a run
// with hits.
func TestReadsOnly(t *testing.T) {
	root := host(t)
	write(t, root, "usr/bin/codex", content)
	before := snapshot(t, root)
	if code, _, _ := runCmd(t, "", "--root", root, "--release", release(t, nil)); code != exitHit {
		t.Fatalf("exit %d", code)
	}
	if after := snapshot(t, root); after != before {
		t.Errorf("the tree changed:\n%s\n%s", before, after)
	}
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(p + " " + fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().String() + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestImportGuard: the command imports the standard library and the check's
// package only, and no process or network package.
func TestImportGuard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			switch {
			case p == "ataniq/internal/authorityhost/noaicheck":
			case p == "C" || p == "unsafe" || p == "os/exec" || p == "plugin" || p == "net" || strings.HasPrefix(p, "net/"):
				t.Errorf("%s imports %s", name, p)
			case strings.Contains(first, ".") || strings.HasPrefix(p, "ataniq/"):
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
}
