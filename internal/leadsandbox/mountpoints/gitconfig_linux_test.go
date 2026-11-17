//go:build linux

package mountpoints

import (
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func TestFindInclude(t *testing.T) {
	long := strings.Repeat("# filler line\n", 5000)
	tests := []struct {
		name     string
		data     string
		wantLine int
		wantName string
	}{
		// Sections that make git read another file.
		{"include", "[include]\n\tpath = ../x\n", 1, "include"},
		{"includeIf with a condition", "[core]\n\tbare = false\n[includeIf \"gitdir:/work/\"]\n\tpath = x\n", 3, "includeIf"},
		{"upper case", "[INCLUDE]\n", 1, "include"},
		{"mixed case includeIf", "[IncludeIf \"onbranch:main\"]\n", 1, "includeIf"},
		{"old subsection form", "[core]\n[include.path]\n", 2, "include"},
		{"old subsection form of includeIf", "[includeif.gitdir:/x/]\n", 1, "includeIf"},
		{"leading white space", "   \t[include]\n", 1, "include"},
		{"white space inside the bracket", "[ include ]\n", 1, "include"},
		{"carriage returns", "[core]\r\n\tbare = true\r\n[include]\r\n", 3, "include"},
		{"after a header on the same line", "[core] bare = false\n[user] name = a [include]\n[x][include]\n", 3, "include"},
		{"after a value with a continuation", "[core]\n\tx = a \\\n b\n[include]\n", 4, "include"},
		{"after a value with a continuation and a carriage return", "[core]\n\tx = a \\\r\n b\n[include]\n", 4, "include"},
		{"after a quoted comment character", "[core]\n\tx = \"a # b\"\n[include]\n", 3, "include"},
		{"after a long file", long + "[include]\n", 5001, "include"},
		{"last line without a newline", "[core]\n[include]", 2, "include"},
		{"after a subsection with an escaped quote", "[remote \"a\\\"b\"]\n[include]\n", 2, "include"},
		{"after an unterminated header", "[remote \"a\n[include]\n", 2, "include"},
		{"no space before a subsection", "[include\"x\"]\n", 1, "include"},
		// Text that is not such a section.
		{"empty", "", 0, ""},
		{"ordinary configuration", "[core]\n\trepositoryformatversion = 0\n[user]\n\tname = x\n", 0, ""},
		{"a section with a longer name", "[includes]\n[includeIfs]\n[inclu]\n", 0, ""},
		{"a section whose name has a prefix", "[xinclude]\n[core.include]\n", 0, ""},
		{"a key named include", "[core]\n\tinclude = x\n\tincludeIf = y\n", 0, ""},
		{"a value that holds a bracket", "[core]\n\tx = [include]\n", 0, ""},
		{"a quoted value that holds a bracket", "[core]\n\tx = \"[include]\"\n", 0, ""},
		// git refuses a file whose quote is not closed on its line; the scan
		// reads the next line as a line of its own and over-refuses.
		{"an unterminated quote ends the value", "[core]\n\tx = \"\n[include]\"\n", 3, "include"},
		{"a comment", "# [include]\n; [include]\n[core]\n\tbare = false # [include]\n\tx = y ; [include]\n", 0, ""},
		{"a value continued over a header", "[core]\n\tx = a \\\n[include]\n", 0, ""},
		{"a subsection named include", "[remote \"include\"]\n\turl = x\n", 0, ""},
		{"a header that is never closed", "[core\n", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, name := findInclude([]byte(tt.data))
			if line != tt.wantLine || name != tt.wantName {
				t.Errorf("findInclude = line %d %q, want line %d %q", line, name, tt.wantLine, tt.wantName)
			}
		})
	}
}

func TestPrepareRefusesACloneConfigWithAnIncludeSection(t *testing.T) {
	for _, cfg := range []string{
		"[core]\n\tbare = false\n[include]\n\tpath = ../../../x\n",
		"[includeIf \"gitdir:/work/\"]\n\tpath = ../x\n",
		"[INCLUDE]\n\tpath = /etc/x\n",
	} {
		f := newFixture(t, profile.Claude)
		f.write(f.tree+"/.git/config", cfg)
		before := listing(t, f.base)
		err := Prepare(f.in, PrepareOptions{NoLeadHasRun: true})
		wantKind(t, err, ErrInclude)
		for _, want := range []string{f.tree + "/.git/config", "line", "remove it"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q does not say %q", err, want)
			}
		}
		// Everything is looked at before anything is written.
		sameListing(t, before, listing(t, f.base))
		if _, err := os.Lstat(f.gitMeta); err == nil {
			t.Error("a folder was made")
		}
	}
}

func TestPrepareCopiesAConfigWithOtherSections(t *testing.T) {
	f := newFixture(t, profile.Claude)
	cfg := "[core]\n\thooksPath = /x\n\tfsmonitor = true\n[remote \"origin\"]\n\turl = /srv/mirror.git\n[includes]\n\tx = 1\n"
	f.write(f.tree+"/.git/config", cfg)
	f.prepare()
	if got, _ := os.ReadFile(f.gitMeta + "/config"); string(got) != cfg {
		t.Errorf("config = %q: no other key is interpreted, so the copy is the clone's", got)
	}
}

// The scan is linear in the text and never panics, whatever the text holds:
// the largest .git/config Prepare reads is maxGitConfig bytes.
func TestFindIncludeOnHostileTextOfTheLargestSize(t *testing.T) {
	for _, unit := range []string{"[\"\\", "\\\n[", "=\"\\", "[x \"\\\"", "\r\\\r\n=", "[", "=", "\\", "\""} {
		data := []byte(strings.Repeat(unit, maxGitConfig/len(unit)))
		if len(data) > maxGitConfig {
			t.Fatalf("test text of %d bytes is over the cap", len(data))
		}
		if _, name := findInclude(data); name != "" {
			t.Errorf("%q repeated: found %q", unit, name)
		}
	}
}
