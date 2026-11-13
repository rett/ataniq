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

// This deliberately covers inline links and explicit reference links, not all Markdown.
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

func checkTarget(source, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	if u.IsAbs() || u.Host != "" {
		return nil
	}
	path := source
	if u.Path != "" {
		path = filepath.Join(filepath.Dir(source), filepath.FromSlash(u.Path))
	}
	data, err := os.ReadFile(path)
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

func checkDoc(path string) error {
	data, err := os.ReadFile(path)
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
			if err := checkTarget(path, m[1]); err != nil {
				return err
			}
		}
		for _, m := range referenceLink.FindAllStringSubmatch(line, -1) {
			target, ok := refs[strings.ToLower(m[1])]
			if !ok {
				return fmt.Errorf("%s: missing reference %s", path, m[1])
			}
			if err := checkTarget(path, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestRepositoryDocumentationLinks(t *testing.T) {
	for _, name := range []string{"README.md", "AGENTS.md", "CONTRIBUTING.md", "CHANGELOG.md"} {
		if err := checkDoc(filepath.Join(root(t), name)); err != nil {
			t.Error(err)
		}
	}
	err := filepath.WalkDir(filepath.Join(root(t), "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			return checkDoc(path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDocumentationLinkFixtures(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("target.md", "# A heading\n# A heading\n")
	for _, tc := range []struct {
		name, content string
		pass          bool
	}{
		{"valid", "[x](target.md#a-heading-1)\n[y][ref]\n[ref]: target.md#a-heading\n", true},
		{"missing path", "[x](missing.md)\n", false},
		{"missing anchor", "[x](target.md#missing)\n", false},
		{"missing reference", "[x][unknown]\n", false},
		{"bad reference target", "[x][ref]\n[ref]: missing.md\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write("source.md", tc.content)
			err := checkDoc(filepath.Join(dir, "source.md"))
			if (err == nil) != tc.pass {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
