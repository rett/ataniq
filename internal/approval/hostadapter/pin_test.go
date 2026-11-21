//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
	"ataniq/internal/exchange/gitenv"
)

// OC25: after a card is shown the lead rewrites its feature branch, so that
// the feature's tip is reachable from no ref but the pin, and the mirror's
// maintenance runs. The test runs "git gc --prune=now" on the temporary
// mirror directly, with plain git and outside gitenv, and shows that the pin
// keeps the merge and both its parents and that the approval then applies.
func TestAPinKeepsTheMergeAndBothParentsAliveThroughGc(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	pin := r.pinOfMerge(merge)
	if pin == "" {
		t.Fatal("the merge has no pin")
	}
	// The lead rewrites its branch: the old tip goes, and a new history
	// that shares nothing with the old tip takes the name.
	r.lab.load(r.scene.root)
	r.lab.put("rewritten.txt", "new history\n")
	rewritten := r.lab.commit("rewritten\n", r.scene.root)
	r.lab.setRef(featureRef, rewritten)
	for _, line := range strings.Split(r.lab.refs(), "\n") {
		name, id, _ := strings.Cut(line, " ")
		if id == r.scene.feature && name != pin && !strings.HasPrefix(name, "refs/ataniq/") {
			t.Fatalf("the feature's tip is still reachable from %s", name)
		}
	}
	// Maintenance, run outside gitenv.
	r.lab.git("", "gc", "--prune=now", "--quiet")
	for what, id := range map[string]string{"merge": merge, "target's tip": r.scene.target, "feature's tip": r.scene.feature} {
		if !r.lab.has(id) {
			t.Errorf("gc removed the %s %s although the pin holds it", what, id)
		}
	}
	// The approval applies on what is left.
	res, err := r.decide(p, true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("Decide after gc: %+v, %v", res, err)
	}
	if got := r.lab.ref(targetBranch); got != merge {
		t.Errorf("the target is at %s, want the merge %s", got, merge)
	}
	if !r.lab.has(r.scene.feature) {
		t.Errorf("the feature's tip is gone after the apply")
	}
}

// The control of the test above: without the pin the same maintenance removes
// the merge and the feature's tip, so the test above cannot pass by chance.
func TestWithoutThePinGcRemovesTheMergeAndTheFeature(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	r.lab.git("", "update-ref", "-d", r.pinOfMerge(merge))
	r.lab.git("", "update-ref", "-d", featureRef)
	r.lab.git("", "gc", "--prune=now", "--quiet")
	if r.lab.has(merge) || r.lab.has(r.scene.feature) {
		t.Errorf("gc kept the merge (%v) or the feature's tip (%v) with no ref to either", r.lab.has(merge), r.lab.has(r.scene.feature))
	}
	if !r.lab.has(r.scene.target) {
		t.Errorf("gc removed the target's tip, which the target branch holds")
	}
}

// No maintenance entry point exists in the repository, and the adapter adds
// none: the pinned git environment refuses each maintenance subcommand, and
// no non-test Go file of the repository names one as a string.
func TestNoMaintenanceEntryPointExists(t *testing.T) {
	g := needGit(t)
	for _, sub := range maintenanceWords {
		if _, err := g.Argv(gitenv.Cmd{GitDir: "/mirror.git", Args: []string{sub, "--end-of-options"}}); err == nil {
			t.Errorf("gitenv accepts %q", sub)
		}
	}
	root := "../../.."
	found := 0
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			found++
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, w := range maintenanceLiterals(t, path, src) {
				t.Errorf("%s names the git subcommand %q as a string", path, w)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if found < 100 {
		t.Fatalf("the walk read %d files", found)
	}
}

var maintenanceWords = []string{"gc", "prune", "prune-packed", "repack", "maintenance", "pack-refs", "reflog", "fsck", "pack-objects"}

// maintenanceLiterals returns the string literals of a Go file that are one
// of the maintenance subcommands.
func maintenanceLiterals(t *testing.T, name string, src []byte) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, w := range maintenanceWords {
			if s == w {
				out = append(out, w)
			}
		}
		return true
	})
	return out
}

// The scan finds a literal where there is one, so that the test above would
// fail on a planted one.
func TestTheMaintenanceScanFindsAPlantedWord(t *testing.T) {
	if got := maintenanceLiterals(t, "x.go", []byte("package x\nvar a = []string{\"gc\", \"--prune=now\"}\nvar b = `repack`\n")); strings.Join(got, ",") != "gc,repack" {
		t.Errorf("found %v", got)
	}
	if got := maintenanceLiterals(t, "x.go", []byte("package x\n// gc\nvar a = \"garbage\"\n")); len(got) != 0 {
		t.Errorf("found %v in a comment and a longer word", got)
	}
}
