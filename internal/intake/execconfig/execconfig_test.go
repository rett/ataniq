package execconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// example gives, for each built-in pattern, a path the rule must match.
var example = map[string]string{
	".claude/":                "settings.json",
	".codex/":                 "config.toml",
	".mcp.json":               "",
	".envrc":                  "",
	".vscode/":                "tasks.json",
	".idea/":                  "workspace.xml",
	".githooks/":              "pre-commit",
	"Makefile":                "",
	"scripts/":                "x.sh",
	".gitattributes":          "",
	".gitmodules":             "",
	"go.mod":                  "",
	".git":                    ".git/config",
	".husky/":                 "pre-commit",
	".pre-commit-config.yaml": "",
	"lefthook.yml":            "",
	".agents/":                "skills/x/SKILL.md",
	".github/":                "workflows/ci.yml",
	".gitlab-ci.yml":          "",
	".circleci/":              "config.yml",
	".woodpecker/":            "build.yml",
	".woodpecker.yml":         "",
	"Jenkinsfile":             "",
	".cursor/":                "rules/x.mdc",
	".gemini/":                "settings.json",
	".aider.conf.yml":         "",
	"CLAUDE.md":               "",
	"CLAUDE.local.md":         "",
	"AGENTS.md":               "",
	"AGENTS.override.md":      "",
	"GEMINI.md":               "",
	".cursorrules":            "",
	"GNUmakefile":             "",
	"*.mk":                    "build.mk",
	"Justfile":                "",
	"Taskfile.yml":            "",
	"Taskfile.yaml":           "",
	"magefile.go":             "",
	"lefthook.yaml":           "",
	"go.work":                 "",
	"go.sum":                  "",
	"go.work.sum":             "",
	".go-version":             "",
	".tool-versions":          "",
	".mise.toml":              "",
	"mise.toml":               "",
	".npmrc":                  "",
	"package.json":            "",
	".devcontainer/":          "devcontainer.json",
	".devcontainer.json":      "",
	"devcontainer.json":       "",
	"Dockerfile":              "",
	".env":                    "",
	".env.*":                  ".env.local",
}

// exampleFor returns the path that the rule must match at the root.
func exampleFor(t *testing.T, pattern string) string {
	t.Helper()
	rest, ok := example[pattern]
	if !ok {
		t.Fatalf("no example for built-in pattern %q", pattern)
	}
	switch {
	case strings.HasSuffix(pattern, "/"):
		return pattern + rest
	case rest != "":
		return rest
	}
	return pattern
}

func mustMatch(t *testing.T, s Set, path, pattern string) {
	t.Helper()
	m, ok := s.Match(path)
	if !ok {
		t.Errorf("Match(%q): no match, want pattern %q", path, pattern)
		return
	}
	if m.Pattern != pattern {
		t.Errorf("Match(%q): pattern %q, want %q", path, m.Pattern, pattern)
	}
	if m.Reason == "" {
		t.Errorf("Match(%q): empty reason", path)
	}
}

func mustNotMatch(t *testing.T, s Set, path string) {
	t.Helper()
	if m, ok := s.Match(path); ok {
		t.Errorf("Match(%q): matched %q, want no match", path, m.Pattern)
	}
}

func TestBuiltInRulesMatchTheirExamples(t *testing.T) {
	s := Default()
	rules := s.Rules()
	if len(rules) != len(example) {
		t.Errorf("%d rules but %d examples", len(rules), len(example))
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[strings.ToLower(r.Pattern)] {
			t.Errorf("duplicate rule %q", r.Pattern)
		}
		seen[strings.ToLower(r.Pattern)] = true
		if r.Reason == "" {
			t.Errorf("rule %q has no reason", r.Pattern)
		}
		ex := exampleFor(t, r.Pattern)
		variants := []string{
			ex,
			"a/" + ex,
			"a/b/c/" + ex,
			strings.ToUpper(ex),
			strings.ToLower(ex),
			"A/" + swapCase(ex),
			"x.y/" + ex,
		}
		for _, v := range variants {
			mustMatch(t, s, v, r.Pattern)
		}
	}
}

func swapCase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case 'a' <= c && c <= 'z':
			b[i] = c - 32
		case 'A' <= c && c <= 'Z':
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestSourcesAndLists(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rules  []Rule
		source string
	}{
		{"Design", Design(), SourceDesign},
		{"Scope", Scope(), SourceFeature},
		{"Relatives", Relatives(), SourceRelative},
	} {
		if len(tc.rules) == 0 {
			t.Errorf("%s is empty", tc.name)
		}
		for _, r := range tc.rules {
			if r.Source != tc.source {
				t.Errorf("%s: rule %q has source %q, want %q", tc.name, r.Pattern, r.Source, tc.source)
			}
		}
	}
	if got := len(Design()); got != 12 {
		t.Errorf("Design has %d rules, want the 12 of the design", got)
	}
	var all []Rule
	all = append(all, Design()...)
	all = append(all, Scope()...)
	all = append(all, Relatives()...)
	got := Default().Rules()
	if len(got) != len(all) {
		t.Fatalf("Default has %d rules, the lists have %d", len(got), len(all))
	}
	for i := range all {
		if got[i] != all[i] {
			t.Errorf("rule %d: Default %v, lists %v", i, got[i], all[i])
		}
	}
	// The lists are copies.
	d := Design()
	d[0].Pattern = "changed"
	if Design()[0].Pattern == "changed" {
		t.Error("Design returns shared storage")
	}
}

func TestDesignScenariosS19(t *testing.T) {
	s := Default()
	for _, p := range []string{
		".claude/settings.json", ".mcp.json", ".codex/config.toml", ".envrc",
		".vscode/tasks.json", "Makefile", "scripts/x.sh", "go.mod",
	} {
		if _, ok := s.Match(p); !ok {
			t.Errorf("Match(%q): no match", p)
		}
	}
}

func TestNearMisses(t *testing.T) {
	s := Default()
	for _, p := range []string{
		"Makefile.txt", "myscripts/x", "claude/settings.json",
		"go.mod.bak", "ago.mod", "x.github/workflows/ci.yml", ".githubx/ci.yml",
		"my.github", "README.md", "src/main.go", ".editorconfig", ".dockerignore",
		"docs/.gitignore", ".gitignore", "env", ".environment", "a.env",
		".envrc.bak", "Dockerfile.bak", "mk", "x.mkx", "package.json5",
		"git~2/x", "gitt~1", ".gitx/y", "my.git", "docs/CLAUDE.mdx",
		".mcp.jsonx", "xMakefile", "scripts.d/x", "a/b/c",
	} {
		mustNotMatch(t, s, p)
	}
}

// The matcher sees no file modes, and a symbolic link or a submodule stands
// for a folder. So a folder rule also matches a path whose last element is
// the folder name.
func TestFolderRuleMatchesALastElement(t *testing.T) {
	s := Default()
	for path, pattern := range map[string]string{
		".claude":          ".claude/",
		"src/.claude":      ".claude/",
		"a/.codex":         ".codex/",
		".vscode":          ".vscode/",
		"scripts":          "scripts/",
		"a/scripts":        "scripts/",
		"x/.devcontainer":  ".devcontainer/",
		".github":          ".github/",
		"a/.github":        ".github/",
		"SCRIPTS":          "scripts/",
		".claude.":         ".claude/",
		"scripts/":         "scripts/",
		"a/scripts/":       "scripts/",
		".claude/":         ".claude/",
		".claude/settings": ".claude/",
	} {
		mustMatch(t, s, path, pattern)
	}
	// A name that merely contains the rule name is not the rule name.
	for _, p := range []string{
		"myscripts", "myscripts/x", "scripts.d", "scripts.d/x", "claude",
		"claude/settings.json", "x.claude", "a/x.claude/y", ".claudex",
		"my.github", ".githubx", "a/.vscodes",
	} {
		mustNotMatch(t, s, p)
	}
}

func TestNamePatternMatchesFolderToo(t *testing.T) {
	s := Default()
	mustMatch(t, s, "go.mod/x", "go.mod")
	mustMatch(t, s, "a/Makefile/b/c", "Makefile")
	mustMatch(t, s, ".git", ".git")
	mustMatch(t, s, "sub/.git/hooks/pre-commit", ".git")
}

func TestFolding(t *testing.T) {
	s := Default()
	for path, pattern := range map[string]string{
		".claude./settings.json": ".claude/",
		".claude /settings.json": ".claude/",
		".mcp.json ":             ".mcp.json",
		"a/.mcp.json.":           ".mcp.json",
		"go.mod. . ":             "go.mod",
		"GIT~1/config":           ".git",
		"a/git~1":                ".git",
		"Git~1./x":               ".git",
	} {
		mustMatch(t, s, path, pattern)
	}
	// Short names other than git~1 are out of scope.
	for _, p := range []string{"claude~1/x", "makef~1", "progra~1/x", "git~1x", "x.git~1"} {
		mustNotMatch(t, s, p)
	}
	// An element of dots and spaces only is not an empty name.
	mustNotMatch(t, s, "... /x")
}

func TestLetterCase(t *testing.T) {
	s := Default()
	for _, p := range []string{"MAKEFILE", "makefile", "mAkEfIlE", ".CLAUDE/x", "SCRIPTS/X.SH", "Go.Mod", "dockerfile", "jenkinsfile", "a/.GitHub/workflows/x"} {
		if _, ok := s.Match(p); !ok {
			t.Errorf("Match(%q): no match", p)
		}
	}
	// Only ASCII folds.
	mustNotMatch(t, s, "KLAUDE.md") // KELVIN SIGN is not K
	mustNotMatch(t, s, "MAKEFİLE")  // dotted capital I is not I
}

// The lists have no rule per spelling: matching ignores case, so "makefile"
// is covered by "Makefile" and needs no rule of its own.
func TestNoRuleForASpelling(t *testing.T) {
	s := Default()
	for _, r := range s.Rules() {
		if r.Pattern == "makefile" {
			t.Errorf("rule %q duplicates %q", r.Pattern, "Makefile")
		}
	}
	for _, p := range []string{"makefile", "MAKEFILE", "a/makeFILE"} {
		mustMatch(t, s, p, "Makefile")
	}
}

func TestWith(t *testing.T) {
	base := Default()
	for _, tc := range []struct {
		pattern string
		yes     []string
		no      []string
	}{
		{"deploy.sh", []string{"deploy.sh", "a/deploy.sh", "x/DEPLOY.SH", "deploy.sh/y"}, []string{"deploy.sh.bak", "mydeploy.sh"}},
		{"ci/", []string{"ci/a", "x/ci/a/b", "CI/a", "ci", "x/ci"}, []string{"ci.d/a", "mci/a", "ci.d"}},
		{"/ops", []string{"ops", "ops/x", "OPS/x"}, []string{"a/ops", "a/ops/x", "opsx"}},
		{"/tools/", []string{"tools/x", "tools/a/b", "tools"}, []string{"a/tools/x", "a/tools", "tools.d/x"}},
		{"*.gen.go", []string{"a.gen.go", "x/y/b.gen.go", ".gen.go"}, []string{"a.gen.go.txt", "agen.go", "x.gen_go"}},
		{"build?", []string{"build1", "x/buildz", "buildé"}, []string{"build", "build12"}},
		{"docs/*.sh", []string{"docs/a.sh", "x/docs/b.sh", "docs/a.sh/y"}, []string{"docs/a/b.sh", "docs.sh", "a.sh"}},
		{"/a/b", []string{"a/b", "a/b/c"}, []string{"x/a/b", "a/x/b", "a"}},
		{"a/b/", []string{"a/b/c", "x/a/b/c", "x/a/b/", "a/b", "x/a/b"}, []string{"a/c/b/d", "a/bc"}},
		{"*", []string{"anything", "a/b"}, nil},
	} {
		s, err := base.With([]string{tc.pattern})
		if err != nil {
			t.Errorf("With(%q): %v", tc.pattern, err)
			continue
		}
		for _, p := range tc.yes {
			m, ok := s.Match(p)
			if !ok {
				t.Errorf("pattern %q: %q did not match", tc.pattern, p)
			} else if tc.pattern != "*" && (m.Pattern != tc.pattern || m.Reason != "project addition") {
				t.Errorf("pattern %q: %q matched %+v", tc.pattern, p, m)
			}
		}
		for _, p := range tc.no {
			if m, ok := s.Match(p); ok {
				t.Errorf("pattern %q: %q matched %+v", tc.pattern, p, m)
			}
		}
	}
}

func TestWithOrderAndImmutability(t *testing.T) {
	base := Default()
	n := len(base.Rules())
	s1, err := base.With([]string{"x.sh", "go.mod", "y.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Rules()) != n {
		t.Error("With changed the receiver")
	}
	rs := s1.Rules()
	if len(rs) != n+3 {
		t.Fatalf("%d rules, want %d", len(rs), n+3)
	}
	for i, p := range []string{"x.sh", "go.mod", "y.sh"} {
		r := rs[n+i]
		if r.Pattern != p || r.Reason != "project addition" || r.Source != SourceProject {
			t.Errorf("addition %d: %+v", i, r)
		}
	}
	// A built-in rule comes before an addition with the same pattern.
	m, _ := s1.Match("go.mod")
	if m.Reason == "project addition" {
		t.Errorf("addition won over a built-in rule: %+v", m)
	}
	// Two branches from one set do not share storage.
	a, _ := base.With([]string{"aaa"})
	b, _ := base.With([]string{"bbb"})
	mustMatch(t, a, "aaa", "aaa")
	mustNotMatch(t, a, "bbb")
	mustMatch(t, b, "bbb", "bbb")
	// Chaining keeps both.
	c, err := a.With([]string{"ccc"})
	if err != nil {
		t.Fatal(err)
	}
	mustMatch(t, c, "aaa", "aaa")
	mustMatch(t, c, "ccc", "ccc")
	mustNotMatch(t, a, "ccc")
	// An empty list changes nothing.
	e, err := base.With(nil)
	if err != nil || len(e.Rules()) != n {
		t.Errorf("With(nil): %v, %d rules", err, len(e.Rules()))
	}
}

func TestWithRefusals(t *testing.T) {
	base := Default()
	for name, p := range map[string]string{
		"empty":           "",
		"slash only":      "/",
		"NUL":             "a\x00b",
		"newline":         "a\nb",
		"carriage return": "a\rb",
		"backslash":       `a\b`,
		"dotdot":          "../x",
		"inner dotdot":    "a/../b",
		"dotdot folder":   "a/..",
		"dotdot only":     "..",
		"dot element":     "a/./b",
		"empty element":   "a//b",
		"double slash":    "//a",
		"open bracket":    "a[b",
		"close bracket":   "a]",
		"class":           "[ab]c",
		"too long":        strings.Repeat("a", 256),
		"too long dirs":   strings.Repeat("a/", 128),
		"trailing dot":    "foo.",
		"trailing space":  "foo ",
		"dots":            "foo..",
		"dot in a dir":    "foo./bar",
		"space in a dir":  "a/foo /bar",
		"dot folder":      "foo./",
		"anchored dot":    "/foo.",
		"space only":      " ",
		"star then dot":   "*.",
		"inner and last":  "a./b.",
	} {
		_, err := base.With([]string{p})
		if err == nil {
			t.Errorf("%s: With(%q) succeeded", name, p)
		} else if !errors.Is(err, ErrPattern) {
			t.Errorf("%s: error %v does not wrap ErrPattern", name, err)
		}
	}
	// A bad pattern among good ones fails the whole call.
	if _, err := base.With([]string{"ok", "bad\\"}); err == nil {
		t.Error("a bad pattern among good ones was accepted")
	}
	// A dot or a space inside an element, or a "*" at its end, is fine.
	for _, p := range []string{"foo.bar", "a b", ".env.*", ".hidden", "x/.y", "foo*", "foo?"} {
		if _, err := base.With([]string{p}); err != nil {
			t.Errorf("With(%q): %v", p, err)
		}
	}
	// The longest pattern is fine.
	if _, err := base.With([]string{strings.Repeat("a", 255)}); err != nil {
		t.Errorf("255 bytes: %v", err)
	}
}

// A caller that logs the error and keeps going must still have the
// built-in rules.
func TestFailedWithKeepsTheReceiver(t *testing.T) {
	base := Default()
	extra, err := base.With([]string{"deploy.sh"})
	if err != nil {
		t.Fatal(err)
	}
	for name, patterns := range map[string][]string{
		"bad pattern":    {"ok", "bad["},
		"too many":       make([]string, MaxAdditions+1),
		"empty pattern":  {""},
		"bad after good": {"fine", "a/../b"},
	} {
		if name == "too many" {
			for i := range patterns {
				patterns[i] = "p" + strings.Repeat("x", i%7) + string(rune('a'+i%26))
			}
		}
		for _, recv := range []Set{base, extra} {
			got, err := recv.With(patterns)
			if !errors.Is(err, ErrPattern) {
				t.Errorf("%s: error %v does not wrap ErrPattern", name, err)
			}
			if !reflect.DeepEqual(got.Rules(), recv.Rules()) {
				t.Errorf("%s: a failed With changed the rules: %d, want %d", name, len(got.Rules()), len(recv.Rules()))
			}
			mustMatch(t, got, "Makefile", "Makefile")
			mustMatch(t, got, ".claude/settings.json", ".claude/")
		}
	}
	mustMatch(t, extra, "deploy.sh", "deploy.sh")
}

func TestWithLimitOnCount(t *testing.T) {
	base := Default()
	list := make([]string, MaxAdditions)
	for i := range list {
		list[i] = "p" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	s, err := base.With(list)
	if err != nil {
		t.Fatalf("256 additions: %v", err)
	}
	if _, err := base.With(append(list, "one-more")); !errors.Is(err, ErrPattern) {
		t.Errorf("257 additions: %v", err)
	}
	if _, err := s.With([]string{"one-more"}); !errors.Is(err, ErrPattern) {
		t.Errorf("256 and one more: %v", err)
	}
	if _, err := s.With(nil); err != nil {
		t.Errorf("256 and none: %v", err)
	}
}

func TestOddPaths(t *testing.T) {
	s := Default()
	// Matched conservatively: empty elements are skipped, and so is a
	// trailing slash.
	for path, pattern := range map[string]string{
		"/Makefile":            "Makefile",
		"//Makefile":           "Makefile",
		"a//Makefile":          "Makefile",
		"/.claude/settings":    ".claude/",
		"a//.claude//x":        ".claude/",
		".claude//":            ".claude/",
		"./go.mod":             "go.mod",
		"a/../go.mod":          "go.mod",
		"../.envrc":            ".envrc",
		"a\nb/.codex/c":        ".codex/",
		".codex/\n":            ".codex/",
		"\xff\xfe/.envrc":      ".envrc",
		".envrc/\xff":          ".envrc",
		"scripts/\x00":         "scripts/",
		".env.\xff":            ".env.*",
		"x\ny.mk":              "*.mk",
		".claude\n/x":          "",
		"\xc3\x28/scripts/a.b": "scripts/",
	} {
		if pattern == "" {
			mustNotMatch(t, s, path)
			continue
		}
		mustMatch(t, s, path, pattern)
	}
	// Nothing to match.
	for _, p := range []string{"", "/", "//", "///", "\x00", "\n", "\xff"} {
		mustNotMatch(t, s, p)
	}
	// Bytes that are not UTF-8 do not fold into each other.
	r, err := s.With([]string{"a\xffb"})
	if err != nil {
		t.Fatal(err)
	}
	mustMatch(t, r, "x/a\xffb", "a\xffb")
	mustNotMatch(t, r, "x/a\xfeb")
	mustNotMatch(t, r, "x/a�b")
}

func TestOddPathsDropAnchoring(t *testing.T) {
	s, err := Default().With([]string{"/ops/", "/rel"})
	if err != nil {
		t.Fatal(err)
	}
	mustMatch(t, s, "ops/x", "/ops/")
	mustNotMatch(t, s, "a/ops/x")
	// An odd path may name the root in a form the matcher does not resolve,
	// so anchored rules see it as unanchored.
	mustMatch(t, s, "/ops/x", "/ops/")
	mustMatch(t, s, "./ops/x", "/ops/")
	mustMatch(t, s, "a/../ops/x", "/ops/")
	mustMatch(t, s, "x//rel", "/rel")
	mustMatch(t, s, "a/../a/rel", "/rel")
}

func TestZeroSet(t *testing.T) {
	var s Set
	mustNotMatch(t, s, "Makefile")
	if len(s.Rules()) != 0 {
		t.Error("zero Set has rules")
	}
	s, err := s.With([]string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	mustMatch(t, s, "a/x", "x")
	mustNotMatch(t, s, "Makefile")
}

func TestRulesReturnsACopy(t *testing.T) {
	s := Default()
	rs := s.Rules()
	first := rs[0]
	rs[0] = Rule{Pattern: "zzz", Reason: "x", Source: "x"}
	if got := s.Rules()[0]; got != first {
		t.Errorf("Rules exposes its storage: %+v", got)
	}
	mustMatch(t, s, ".claude/x", ".claude/")
	mustNotMatch(t, s, "zzz")
}

func TestFirstMatchOrder(t *testing.T) {
	s := Default()
	// A path that two rules match is reported by the earlier rule.
	mustMatch(t, s, ".githooks/.git/x", ".githooks/") // design before feature
	mustMatch(t, s, ".claude/Makefile", ".claude/")   // first rule of the list
	mustMatch(t, s, "scripts/go.mod", "scripts/")
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		pat, name string
		want      bool
	}{
		{"", "", true},
		{"", "a", false},
		{"*", "", true},
		{"*", "abc", true},
		{"a*", "a", true},
		{"a*c", "abc", true},
		{"a*c", "abcbc", true},
		{"a*c", "abcb", false},
		{"*a*b*", "xaxbx", true},
		{"*a*b*", "xbxax", false},
		{"**a", "ba", true},
		{"?", "", false},
		{"?", "é", true},
		{"?", "\xff", true},
		{"??", "é", false},
		{"a?c", "abc", true},
		{"*.mk", ".mk", true},
		{".env.*", ".env", false},
		{".env.*", ".env.", true},
	} {
		if got := glob(tc.pat, tc.name); got != tc.want {
			t.Errorf("glob(%q, %q) = %v, want %v", tc.pat, tc.name, got, tc.want)
		}
	}
}

func TestLongPathsAreFine(t *testing.T) {
	s := Default()
	long := strings.Repeat("d/", 5000) + ".envrc"
	mustMatch(t, s, long, ".envrc")
	mustNotMatch(t, s, strings.Repeat("d/", 5000)+"envrc")
	mustNotMatch(t, s, strings.Repeat("*", 100000))
}

func TestNoPanicOnEveryShortPath(t *testing.T) {
	s, err := Default().With([]string{"/a?b/", "*x*", "c"})
	if err != nil {
		t.Fatal(err)
	}
	alphabet := []byte{0, '\n', '/', '.', ' ', 'a', 'x', 'G', '~', '1', 0xff}
	var walk func(prefix []byte, depth int)
	walk = func(prefix []byte, depth int) {
		s.Match(string(prefix))
		if depth == 0 {
			return
		}
		for _, b := range alphabet {
			walk(append(prefix[:len(prefix):len(prefix)], b), depth-1)
		}
	}
	walk(nil, 4)
}
