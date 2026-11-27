package noaicheck

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRunAllClean(t *testing.T) {
	f := cleanFixture(t)
	c := f.checker(testLists())
	var r Report
	within(t, func() { r = c.RunAll(nil) })
	if len(r.Hits) != 0 {
		t.Errorf("hits on a clean tree: %v", r.Hits)
	}
	var out bytes.Buffer
	if err := r.WriteText(&out); err != nil {
		t.Fatal(err)
	}
	if want := "NOTE packages not enforced\nNOTE nft not pinned\nRUN incomplete\nok\n"; out.String() != want {
		t.Errorf("text %q, want %q", out.String(), want)
	}
}

// With RequirePolicy every policy the release leaves unset is a hit, and a
// run with every policy set and nothing planted is complete.
func TestRunAllRequirePolicy(t *testing.T) {
	listing := []byte("table inet filter {\n}\n")
	policy := func(list, detail string) Hit { return Hit{Kind: KindPolicy, Path: list, Detail: detail} }
	cases := []struct {
		name     string
		edit     func(l *Lists)
		want     []Hit
		complete bool
	}{
		{"every policy set", nil, nil, true},
		{"empty units.allow", func(l *Lists) { l.UnitsAllow = nil }, []Hit{
			policy(FileUnitsAllow, DetailEmpty),
			{Kind: KindUnit, Path: "/etc/systemd/system/getty.target.wants/getty@tty1.service"},
			{Kind: KindUnit, Path: "/etc/systemd/system/multi-user.target.wants/sshd.service"},
		}, false},
		{"empty ports.allow", func(l *Lists) { l.PortsAllow = nil }, []Hit{
			policy(FilePortsAllow, DetailEmpty),
			{Kind: KindPort, Path: "tcp:0.0.0.0:22"},
			{Kind: KindPort, Path: "tcp6:[::]:443"},
		}, false},
		{"empty packages.allow", func(l *Lists) { l.PackagesAllow = nil }, []Hit{policy(FilePackagesAllow, DetailEmpty)}, false},
		{"no nft.digest", func(l *Lists) { l.NFTDigest = "" }, []Hit{policy(FileNFTDigest, DetailMissing)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := testLists()
			l.PackagesAllow = []string{"bash"}
			l.NFTDigest = NFTDigest(listing)
			if tc.edit != nil {
				tc.edit(l)
			}
			c := cleanFixture(t).checker(l)
			c.RequirePolicy = true
			var r Report
			within(t, func() { r = c.RunAll(listing) })
			if !(len(r.Hits) == 0 && len(tc.want) == 0) && !reflect.DeepEqual(r.Hits, tc.want) {
				t.Errorf("hits\n got %v\nwant %v", r.Hits, tc.want)
			}
			if r.Complete != tc.complete || len(r.Notes) != 0 {
				t.Errorf("complete %v notes %v, want %v and no notes", r.Complete, r.Notes, tc.complete)
			}
			var out bytes.Buffer
			if err := r.WriteText(&out); err != nil {
				t.Fatal(err)
			}
			if tc.complete && out.String() != "RUN complete\nok\n" {
				t.Errorf("text %q", out.String())
			}
		})
	}
}

// A listening port not in ports.allow fails the main run, not only the
// ports subcommand.
func TestRunAllChecksPorts(t *testing.T) {
	f := cleanFixture(t)
	f.file("proc/net/tcp", tcpHeader+
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0\n")
	r := f.checker(testLists()).RunAll(nil)
	if want := []Hit{{Kind: KindPort, Path: "tcp:0.0.0.0:8080"}}; !reflect.DeepEqual(r.Hits, want) {
		t.Errorf("hits %v, want %v", r.Hits, want)
	}
}

// A tree with one plant per check fails every check, and neither report
// holds a planted file's content.
func TestRunAllPlanted(t *testing.T) {
	const content = "CONTENT-OF-A-PLANTED-FILE"
	f := cleanFixture(t)
	f.file("usr/local/bin/claude", content)
	f.file("usr/bin/node", content)
	f.file("home/user/.claude/credentials.json", content)
	f.file("etc/environment", "OPENAI_API_KEY="+secret+"\n")
	f.file("proc/99/comm", "codex\n")
	f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/etc/systemd/system/agent.service")
	f.file("etc/systemd/system/agent.service", "[Service]\nExecStart=/usr/local/bin/claude "+content+"\n")
	f.file("var/lib/pacman/local/nodejs-22-1/desc", "%NAME%\nnodejs\n")
	f.file("home/user/.local/bin/x\nok", content) // a name that tries to forge a line
	f.file("proc/net/tcp6", tcp6Header+
		"   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0 100 0 0 10 0\n")
	l := testLists()
	l.AINames = append(l.AINames, "x\nok")
	l.NFTDigest = NFTDigest([]byte("table inet filter {\n}\n"))
	c := f.checker(l)
	r := c.RunAll([]byte("table inet filter {\n\tchain input {\n\t}\n}\n"))

	kinds := map[string]bool{}
	for _, h := range r.Hits {
		kinds[h.Kind] = true
	}
	for _, k := range []string{KindBinary, KindTool, KindCredential, KindEnv, KindProcess, KindUnit, KindPort, KindPackage, KindNFT} {
		if !kinds[k] {
			t.Errorf("no %s hit in %v", k, r.Hits)
		}
	}
	var text, js bytes.Buffer
	if err := r.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), js.String()} {
		if strings.Contains(out, content) || strings.Contains(out, secret) {
			t.Errorf("a report holds planted content:\n%s", out)
		}
	}
	lines := strings.Split(strings.TrimSuffix(text.String(), "\n"), "\n")
	if lines[len(lines)-1] != "FAILED "+strconv.Itoa(len(r.Hits))+" hit(s)" {
		t.Errorf("last line %q", lines[len(lines)-1])
	}
	for _, line := range lines {
		if line == "ok" {
			t.Errorf("a planted name forged an ok line:\n%s", text.String())
		}
	}
	if !strings.Contains(text.String(), `HIT binary "/home/user/.local/bin/x\nok"`) {
		t.Errorf("the planted name is not quoted:\n%s", text.String())
	}
	var decoded struct {
		Result string
		Count  int
		Hits   []Hit
	}
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Result != "failed" || decoded.Count != len(r.Hits) || len(decoded.Hits) != len(r.Hits) {
		t.Errorf("json %+v", decoded)
	}
}

func TestWriteJSONClean(t *testing.T) {
	var b bytes.Buffer
	if err := (Report{}).WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if want := `{"result":"ok","complete":false,"count":0,"hits":[],"notes":[]}` + "\n"; b.String() != want {
		t.Errorf("got %s want %s", b.String(), want)
	}
}

// TestImportGuard keeps the check small and inert: its non-test files import
// only the standard library, with os/exec confined to the fixed live systemctl
// query. They import no network package except
// net/netip (address values, no I/O), no cgo and no unsafe, and calls none
// of the syscall package's ways to start a process or open a socket.
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
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"os.StartProcess", "syscall.Exec", "syscall.ForkExec", "syscall.StartProcess",
			"syscall.Socket", "syscall.Connect", "syscall.Open(", "os.WriteFile", "os.Create", "os.Remove", "os.Rename"} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s calls %s", name, call)
			}
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			switch {
			case p == "net/netip", p == "os/exec" && name == "witness_systemd.go":
			case p == "C" || p == "unsafe" || p == "os/exec" || p == "plugin" || p == "os/signal":
				t.Errorf("%s imports %s", name, p)
			case p == "net" || strings.HasPrefix(p, "net/") || strings.HasPrefix(p, "crypto/tls"):
				t.Errorf("%s imports the network package %s", name, p)
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s from outside the standard library", name, p)
			case strings.HasPrefix(p, "ataniq/"):
				t.Errorf("%s imports %s; the check stands alone", name, p)
			}
		}
	}
}
