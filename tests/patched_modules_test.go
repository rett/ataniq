package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goModLines returns the non-blank lines of a go.mod file.
func goModLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// subtract returns the lines of a that are not in b, counting repeats.
func subtract(a, b []string) []string {
	rest := slices.Clone(b)
	var out []string
	for _, line := range a {
		if i := slices.Index(rest, line); i >= 0 {
			rest = slices.Delete(rest, i, i+1)
			continue
		}
		out = append(out, line)
	}
	return out
}

// The patched x/vt and x/ansi are adopted by one direct requirement (x/vt), the
// indirect requirement for x/exp/ordered that any importer of x/vt needs, and
// two directory replacements; nothing else in go.mod changes, in particular no
// `go mod tidy` rewriting. testdata/go.mod.base is the go.mod of the commit the
// adoption was made on. A later change that deliberately changes go.mod updates
// both the base and the expected lines here, in its own commit.
func TestGoModDiffFromBase(t *testing.T) {
	base := goModLines(t, "testdata/go.mod.base")
	current := goModLines(t, filepath.Join(root(t), "go.mod"))
	const vtRequire = "\tgithub.com/charmbracelet/x/vt v0.1.0"
	wantAdded := []string{
		vtRequire,
		"\tgithub.com/charmbracelet/x/exp/ordered v0.1.0 // indirect",
		"replace github.com/charmbracelet/x/vt => ./third_party/patched/x-vt",
		"replace github.com/charmbracelet/x/ansi => ./third_party/patched/x-ansi",
	}
	added := subtract(current, base)
	removed := subtract(base, current)
	if len(removed) != 0 {
		t.Errorf("go.mod lost or changed lines: %q", removed)
	}
	slices.Sort(added)
	slices.Sort(wantAdded)
	if !slices.Equal(added, wantAdded) {
		t.Errorf("go.mod added lines:\n got  %q\n want %q", added, wantAdded)
	}
	// The new requirement is in the direct block, and the replacements follow
	// every requirement.
	text := strings.Join(current, "\n")
	if strings.Index(text, vtRequire) > strings.Index(text, "// indirect") {
		t.Error("the x/vt requirement is not in the direct requirement block")
	}
}

// go.sum has the lines the x/vt dependency on x/exp/ordered needs, as they
// appear in the module's own go.sum and in the module cache.
func TestGoSumHasOrderedLines(t *testing.T) {
	sum, err := os.ReadFile(filepath.Join(root(t), "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"github.com/charmbracelet/x/exp/ordered v0.1.0 h1:55/qLwjIh0gL0Vni+QAWk7T/qRVP6sBf+2agPBgnOFE=",
		"github.com/charmbracelet/x/exp/ordered v0.1.0/go.mod h1:5UHwmG+is5THxMyCJHNPCn2/ecI07aKNrW+LcResjJ8=",
	} {
		if !slices.Contains(strings.Split(string(sum), "\n"), want) {
			t.Errorf("go.sum lacks %q", want)
		}
	}
}

// Each patched module keeps its upstream license and has a NOTICE.md that
// names the module, the version, the patches and the condition for removal.
func TestPatchedModuleNotices(t *testing.T) {
	for _, tc := range []struct {
		dir, module, version string
		patches              []string
	}{
		{"x-vt", "github.com/charmbracelet/x/vt", "v0.1.0", []string{"keep a grapheme cluster open across writes", "add OpenCluster"}},
		{"x-ansi", "github.com/charmbracelet/x/ansi", "v0.11.9", []string{"do not end string data on a 0x9C byte inside a UTF-8 character", "do not end decoded string data on a 0x9C byte inside a UTF-8 character"}},
	} {
		dir := filepath.Join(root(t), "third_party", "patched", tc.dir)
		for _, name := range []string{"LICENSE", "go.mod", "NOTICE.md"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("%s: %v", tc.dir, err)
			}
		}
		b, err := os.ReadFile(filepath.Join(dir, "NOTICE.md"))
		if err != nil {
			continue
		}
		notice := strings.Join(strings.Fields(string(b)), " ")
		for _, want := range append([]string{tc.module, tc.version, "upstream release", "Removal"}, tc.patches...) {
			if !strings.Contains(notice, want) {
				t.Errorf("%s/NOTICE.md lacks %q", tc.dir, want)
			}
		}
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && !strings.HasPrefix(string(mod), "module "+tc.module+"\n") {
			t.Errorf("%s/go.mod is not module %s", tc.dir, tc.module)
		}
	}
	// No version-control metadata or build output is carried with the copies.
	err := filepath.WalkDir(filepath.Join(root(t), "third_party", "patched"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".git") || strings.HasSuffix(d.Name(), ".orig") || strings.HasSuffix(d.Name(), ".rej") {
			t.Errorf("unexpected file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The notice of x/exp/ordered, which x/vt imports, follows the console's
	// convention: the upstream license, and a line in the README.
	console := filepath.Join(root(t), "third_party", "terminal-console")
	lic, err := os.ReadFile(filepath.Join(console, "x-exp-ordered", "LICENSE"))
	if err != nil || !strings.Contains(string(lic), "MIT License") {
		t.Errorf("x-exp-ordered/LICENSE: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(console, "README.md"))
	if err != nil || !strings.Contains(string(readme), "`x-exp-ordered`: github.com/charmbracelet/x/exp/ordered v0.1.0") {
		t.Errorf("terminal-console README does not list x-exp-ordered: %v", err)
	}
}

// make test runs the patched modules' own tests, by package name, from the
// main module; the sixel package, which needs an uncached module, is left out.
func TestMakeTestRunsPatchedModuleTests(t *testing.T) {
	dir := fakePath(t, "exit 99\n")
	for _, target := range []string{"test", "test-patched"} {
		cmd := exec.Command(filepath.Join(dir, "make"), "-n", target)
		cmd.Dir = root(t)
		cmd.Env = append(os.Environ(), "PATH="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %s %v", target, out, err)
		}
		want := "sh scripts/go-tool.sh test github.com/charmbracelet/x/vt github.com/charmbracelet/x/ansi github.com/charmbracelet/x/ansi/parser\n"
		if !strings.Contains(string(out), want) || strings.Contains(string(out), "sixel") {
			t.Errorf("make -n %s lacks %q:\n%s", target, want, out)
		}
	}
}
