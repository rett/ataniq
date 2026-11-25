//go:build linux

package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// module is this repository's module path.
const module = "ataniq"

// scratchRepoDeps are the packages of this repository the command depends on
// in a build without the ataniq_canary tag, directly or not: exactly these
// seven. A build with the tag adds the written-out list in
// imports_canary_test.go.
var scratchRepoDeps = []string{
	"ataniq/internal/leadholder/buildid",
	"ataniq/internal/leadholder/eventlog",
	"ataniq/internal/leadholder/ptyhost",
	"ataniq/internal/leadholder/serve",
	"ataniq/internal/leadholder/session",
	"ataniq/internal/leadholder/wire",
	"ataniq/internal/terminalconsole/sessionpane",
}

// forbiddenRepoDep says why a package of this repository may never be a
// dependency of the command, with or without the tag, or "" when no rule
// forbids it by name. (A package that is merely not on the list is refused by
// the list.)
func forbiddenRepoDep(pkg string) string {
	under := func(base string) bool { return pkg == base || strings.HasPrefix(pkg, base+"/") }
	switch {
	case under(module + "/qualification"):
		return "nothing under qualification/ may be in the holder"
	case under(module+"/internal/terminalconsole") && pkg != module+"/internal/terminalconsole/sessionpane":
		return "of the console only sessionpane may be in the holder"
	case under(module + "/internal/leadcanary"):
		return "internal/leadcanary is the daemon's side of the canary slice"
	case under(module + "/internal/leaddaemon"):
		return "the holder does not depend on the daemon"
	case under(module + "/internal/leadholder/fakeagent"):
		return "the fake agent runs a script and may start processes: only tests import it"
	}
	return ""
}

// checkRepoDeps compares the dependency list of the command (the output lines
// of go list -deps) with the allowed packages of this repository: every
// package of the repository in the list, but the command itself, must be
// allowed, none may be forbidden by name, and every allowed package must be in
// the list, so that the allowed list is exactly the real one.
func checkRepoDeps(deps, allowed []string) []string {
	self := module + "/cmd/ataniq-holder"
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	seen := map[string]bool{}
	var problems []string
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" || dep == self || !(dep == module || strings.HasPrefix(dep, module+"/")) {
			continue
		}
		seen[dep] = true
		if why := forbiddenRepoDep(dep); why != "" {
			problems = append(problems, "the command depends on "+dep+": "+why)
		} else if !ok[dep] {
			problems = append(problems, "the command depends on "+dep+", which is not on the written-out list")
		}
	}
	for _, a := range allowed {
		if !seen[a] {
			problems = append(problems, "the written-out list names "+a+", which the command does not depend on")
		}
	}
	sort.Strings(problems)
	return problems
}

// guardGoTool finds the Go executable the way scripts/go-tool.sh does: the one
// on PATH when it is the pinned version, else the one the bootstrap recorded.
func guardGoTool(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	pin, err := os.ReadFile(filepath.Join(root, ".go-version"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(pin))
	exact := func(tool string) bool {
		cmd := exec.Command(tool, "version")
		cmd.Env = guardGoEnv()
		out, err := cmd.Output()
		return err == nil && strings.HasPrefix(string(out), "go version go"+version+" ")
	}
	if tool, err := exec.LookPath("go"); err == nil && exact(tool) {
		return tool
	}
	if record, err := os.ReadFile(filepath.Join(root, "build", "go-toolchain")); err == nil {
		lines := strings.Split(strings.TrimRight(string(record), "\n"), "\n")
		if len(lines) == 2 && lines[0] == version && filepath.IsAbs(lines[1]) && strings.HasSuffix(lines[1], "/bin/go") && exact(lines[1]) {
			return lines[1]
		}
	}
	t.Fatalf("no Go %s: run make bootstrap", version)
	return ""
}

// guardGoEnv is the environment of the helper script: offline, and no flag of
// the caller's, so that no tag comes in from outside.
func guardGoEnv() []string {
	return append(os.Environ(), "GOENV=off", "GOWORK=off", "GOFLAGS=", "GO111MODULE=on", "GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=none", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
}

// TestImportGuard keeps the command small. It walks the whole dependency list
// of the command (go list -deps, offline, without the tests' own imports), not
// the direct imports: the packages of this repository in it are exactly the
// list of this build (guardRepoDeps: seven without the ataniq_canary tag, a
// written-out longer list with it), and nothing under qualification/, no
// console package beyond sessionpane, internal/leadcanary, internal/leaddaemon
// and the fake agent are ever among them.
func TestImportGuard(t *testing.T) {
	args := append([]string{"list", "-deps"}, guardListFlags...)
	cmd := exec.Command(guardGoTool(t), append(args, ".")...)
	cmd.Env = guardGoEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	deps := strings.Split(string(out), "\n")
	if len(deps) < 10 {
		t.Fatalf("the dependency list is too short to be one: %q", out)
	}
	for _, p := range checkRepoDeps(deps, guardRepoDeps()) {
		t.Error(p)
	}
}

// TestImportGuardDirectImports is the rule for what is outside this
// repository: the command's own files, in either build, import only the
// standard library and golang.org/x/sys/unix, and neither C nor unsafe.
func TestImportGuardDirectImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(p, "/")
			switch {
			case p == "C", p == "unsafe":
				t.Errorf("%s imports %s", name, p)
			case p == module || strings.HasPrefix(p, module+"/"), p == "golang.org/x/sys/unix":
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s: only the standard library and golang.org/x/sys/unix are allowed", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}

// TestImportGuardCheck shows what the check refuses, on made-up lists.
func TestImportGuardCheck(t *testing.T) {
	base := append([]string{"fmt", "golang.org/x/sys/unix", module + "/cmd/ataniq-holder"}, scratchRepoDeps...)
	if p := checkRepoDeps(base, scratchRepoDeps); len(p) != 0 {
		t.Fatalf("the seven are refused: %v", p)
	}
	for _, c := range []struct{ extra, want string }{
		{module + "/qualification/leadsandbox/internal/harness", "qualification/"},
		{module + "/qualification/host", "qualification/"},
		{module + "/internal/terminalconsole", "only sessionpane"},
		{module + "/internal/terminalconsole/toolkit", "only sessionpane"},
		{module + "/internal/leadcanary", "leadcanary"},
		{module + "/internal/leadcanary/provision", "leadcanary"},
		{module + "/internal/leaddaemon", "daemon"},
		{module + "/internal/leadholder/fakeagent", "fake agent"},
		{module + "/internal/leadholder/sandboxstart", "not on the written-out list"},
		{module + "/internal/inject", "not on the written-out list"},
		{module + "/internal/leadrole", "not on the written-out list"},
	} {
		p := checkRepoDeps(append(append([]string(nil), base...), c.extra), scratchRepoDeps)
		if len(p) != 1 || !strings.Contains(p[0], c.extra) || !strings.Contains(p[0], c.want) {
			t.Errorf("with %s: %v, want one problem saying %q", c.extra, p, c.want)
		}
	}
	// A forbidden package is refused even when a list names it.
	bad := module + "/internal/leaddaemon"
	if p := checkRepoDeps(append(append([]string(nil), base...), bad), append(append([]string(nil), scratchRepoDeps...), bad)); len(p) != 1 {
		t.Errorf("a listed forbidden package: %v", p)
	}
	// A package on the list that is not a dependency is a problem too: the
	// list is exactly the real one.
	if p := checkRepoDeps(base[:len(base)-1], scratchRepoDeps); len(p) != 1 || !strings.Contains(p[0], "does not depend on") {
		t.Errorf("a missing package: %v", p)
	}
}
