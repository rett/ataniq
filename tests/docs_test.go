package tests

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// This covers simple inline and explicit reference links and ATX heading anchors
// outside triple-backtick fences; it does not implement all Markdown or GFM.
var inlineLink = regexp.MustCompile(`\[[^\]]+\]\(([^ )]+)\)`)
var referenceLink = regexp.MustCompile(`\[[^\]]+\]\[([^\]]+)\]`)
var referenceDef = regexp.MustCompile(`^\[([^\]]+)\]:\s*(\S+)`)
var heading = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)

func linesOutsideFences(data string) []string {
	var lines []string
	fenced := false
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			lines = append(lines, line)
		}
	}
	return lines
}

func anchor(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// readContained opens every document through the same root, including sources.
func readContained(root, path string) ([]byte, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	fs, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer fs.Close()
	return fs.ReadFile(rel)
}

func checkTarget(root, source, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	if u.IsAbs() || u.Host != "" {
		return nil
	}
	if filepath.IsAbs(filepath.FromSlash(u.Path)) {
		return fmt.Errorf("%s: absolute local path %s", source, target)
	}
	path := source
	if u.Path != "" {
		path = filepath.Join(filepath.Dir(source), filepath.FromSlash(u.Path))
	}
	data, err := readContained(root, path)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", source, target, err)
	}
	if u.Fragment == "" {
		return nil
	}
	seen := map[string]int{}
	for _, line := range linesOutsideFences(string(data)) {
		if m := heading.FindStringSubmatch(line); m != nil {
			id := anchor(m[1])
			count := seen[id]
			seen[id]++
			if count > 0 {
				id = fmt.Sprintf("%s-%d", id, count)
			}
			if id == u.Fragment {
				return nil
			}
		}
	}
	return fmt.Errorf("%s: missing anchor %s", source, target)
}

func checkDoc(root, path string) error {
	data, err := readContained(root, path)
	if err != nil {
		return err
	}
	lines := linesOutsideFences(string(data))
	refs := map[string]string{}
	for _, line := range lines {
		if m := referenceDef.FindStringSubmatch(line); m != nil {
			refs[strings.ToLower(m[1])] = m[2]
		}
	}
	for _, line := range lines {
		for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
			if err := checkTarget(root, path, m[1]); err != nil {
				return err
			}
		}
		for _, m := range referenceLink.FindAllStringSubmatch(line, -1) {
			target, ok := refs[strings.ToLower(m[1])]
			if !ok {
				return fmt.Errorf("%s: missing reference %s", path, m[1])
			}
			if err := checkTarget(root, path, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestRepositoryDocumentationLinks(t *testing.T) {
	for _, name := range []string{"README.md", "AGENTS.md", "CONTRIBUTING.md", "CHANGELOG.md"} {
		if err := checkDoc(root(t), filepath.Join(root(t), name)); err != nil {
			t.Error(err)
		}
	}
	err := filepath.WalkDir(filepath.Join(root(t), "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			return checkDoc(root(t), path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDocumentationLinkFixtures(t *testing.T) {
	outer := t.TempDir()
	dir := filepath.Join(outer, "root")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("target.md", "# A heading\n# A heading\n")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, "outside.md"), []byte("# Outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outer, "outside.md"), filepath.Join(dir, "escape.md")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		pass          bool
	}{
		{"valid", "[x](target.md#a-heading-1)\n[y][ref]\n[ref]: target.md#a-heading\n", true},
		{"missing path", "[x](missing.md)\n", false},
		{"missing anchor", "[x](target.md#missing)\n", false},
		{"missing reference", "[x][unknown]\n", false},
		{"bad reference target", "[x][ref]\n[ref]: missing.md\n", false},
		{"parent within root", "[x](../target.md#a-heading)\n", true},
		{"traversal outside root", "[x](../../outside.md)\n", false},
		{"encoded escape", "[x](%2e%2e/%2e%2e/outside.md)\n", false},
		{"absolute path", "[x](" + filepath.ToSlash(filepath.Join(outer, "outside.md")) + ")\n", false},
		{"symlink escape", "[x](../escape.md)\n", false},
		{"external offline", "[x](https://example.invalid/path)\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write("nested/source.md", tc.content)
			// Existing fixtures use root-relative targets from a root-level source.
			source := filepath.Join(dir, "nested/source.md")
			if tc.name == "valid" || tc.name == "missing path" || tc.name == "missing anchor" || tc.name == "missing reference" || tc.name == "bad reference target" {
				write("source.md", tc.content)
				source = filepath.Join(dir, "source.md")
			}
			err := checkDoc(dir, source)
			if (err == nil) != tc.pass {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestDocumentationSourceContainment(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outer, "outside.md")
	if err := os.WriteFile(outside, []byte("No links here.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{link, filepath.Join(root, "..", "outside.md")} {
		if err := checkDoc(root, source); err == nil {
			t.Errorf("accepted source outside root: %s", source)
		}
	}
}

func TestProjectHomeOperatorSteps(t *testing.T) {
	for _, example := range []struct{ guide, state, project string }{
		{"scratch-leads.md", "STATE", "scratch-KEY"},
		{"canary-slice.md", "STATE", "PROJECT"},
		{"runbooks.md", "<state root>", "scratch-<key>"},
		{"lead-daemon.md", "DIR", "PROJECT"},
		{"federation.md", "DIR", "PROJECT"},
	} {
		data, err := os.ReadFile(filepath.Join(root(t), "docs", example.guide))
		if err != nil {
			t.Fatal(err)
		}
		init := "ataniqd init --state-dir " + example.state
		enroll := "ataniqd project-home --state-dir " + example.state + " --home self " + example.project
		_, after, ok := strings.Cut(string(data), init)
		if !ok || !strings.Contains(after, enroll) {
			t.Errorf("%s must show %q followed by %q", example.guide, init, enroll)
		}
	}
}

func TestFederationStateRootFileTable(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(root(t), "docs", "federation.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"instance.key", "store.incarnation", "registry/registry.jsonl", "registry/registry.jsonl.v1", "daemon.lock"} {
		if !strings.Contains(string(doc), "\n| `"+name+"` |") {
			t.Errorf("state root file table is missing %s", name)
		}
	}
}
