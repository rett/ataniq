package noaicheck

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
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
	if want := "NOTE packages not enforced\nNOTE nft not pinned\nok\n"; out.String() != want {
		t.Errorf("text %q, want %q", out.String(), want)
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
	l := testLists()
	l.AINames = append(l.AINames, "x\nok")
	l.NFTDigest = NFTDigest([]byte("table inet filter {\n}\n"))
	c := f.checker(l)
	r := c.RunAll([]byte("table inet filter {\n\tchain input {\n\t}\n}\n"))

	kinds := map[string]bool{}
	for _, h := range r.Hits {
		kinds[h.Kind] = true
	}
	for _, k := range []string{KindBinary, KindTool, KindCredential, KindEnv, KindProcess, KindUnit, KindPackage, KindNFT} {
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
	if want := `{"result":"ok","count":0,"hits":[],"notes":[]}` + "\n"; b.String() != want {
		t.Errorf("got %s want %s", b.String(), want)
	}
}

// TestImportGuard keeps the check small and inert: its non-test files import
// only the standard library, no process package, no network package except
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
			case p == "net/netip":
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
