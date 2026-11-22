package noaicheck

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseList(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"comments and blanks", "# a comment\n\n   \n  # indented comment\n", nil, false},
		{"names trimmed and kept in order", "b\n  a  \nb\n", []string{"b", "a"}, false},
		{"unit names", "getty@.service\nsshd.service\n", []string{"getty@.service", "sshd.service"}, false},
		{"no final newline", "x", []string{"x"}, false},
		{"path separator", "ok\nbin/claude\n", nil, true},
		{"leading slash", "/claude\n", nil, true},
		{"tab", "a\tb\n", nil, true},
		{"carriage return", "claude\r\n", nil, true},
		{"nul", "cla\x00ude\n", nil, true},
		{"escape", "\x1b[2Jclaude\n", nil, true},
		{"delete", "claude\x7f\n", nil, true},
		{"control byte in a comment", "# \x1b]0;title\x07\n", nil, true},
		{"inner space", "two words\n", nil, true},
		{"inline comment", "claude # note\n", nil, true},
		{"backslash", "a\\b\n", nil, true},
		{"dot", ".\n", nil, true},
		{"dot dot", "..\n", nil, true},
		{"invalid utf-8", "\xff\n", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseList(strings.NewReader(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrList) {
					t.Errorf("error %v does not wrap ErrList", err)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An error names the line, never its text: a list line could hold anything.
func TestParseListErrorOmitsText(t *testing.T) {
	_, err := ParseList(strings.NewReader("ok\nsecret-value/x\n"))
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Errorf("error %q holds the line's text", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %q does not name line 2", err)
	}
}

func TestParseAINames(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantNames []string
		wantPaths []string
		wantErr   bool
	}{
		{"split", "# c\nclaude\n.claude\n.config/opencode\ncodex\n",
			[]string{"claude", "codex"}, []string{".claude", ".config/opencode"}, false},
		{"separator in an executable name", "bin/claude\n", nil, nil, true},
		{"empty component", ".config//opencode\n", nil, nil, true},
		{"trailing separator", ".claude/\n", nil, nil, true},
		{"dot dot component", ".config/../../etc\n", nil, nil, true},
		{"control byte in a path", ".config/\x01x\n", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, paths, err := ParseAINames(strings.NewReader(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(names, tc.wantNames) || !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Errorf("got %q %q, want %q %q", names, paths, tc.wantNames, tc.wantPaths)
			}
		})
	}
}

func TestParsePorts(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"# none\n", []int{}, false},
		{"443\n22\n", []int{443, 22}, false},
		{"0\n", nil, true},
		{"65536\n", nil, true},
		{"080\n", nil, true},
		{"+80\n", nil, true},
		{"https\n", nil, true},
		{"127.0.0.1:80\n", nil, true},
	}
	for _, tc := range cases {
		got, err := ParsePorts(strings.NewReader(tc.in))
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v, want error %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseDigest(t *testing.T) {
	const d = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{d + "\n", d, false},
		{"# pinned\n" + strings.ToUpper(d) + "  -\n", d, false},
		{"", "", true},
		{d[:10] + "\n", "", true},
		{d + "\n" + d + "\n", "", true},
		{"zz" + d[2:] + "\n", "", true},
	}
	for _, tc := range cases {
		got, err := ParseDigest(strings.NewReader(tc.in))
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// writeRelease writes a release folder with the given share/ files; a nil
// value leaves the file out.
func writeRelease(t *testing.T, files map[string]*string) string {
	t.Helper()
	dir := t.TempDir()
	share := filepath.Join(dir, "share")
	if err := os.MkdirAll(share, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if content == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(share, name), []byte(*content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func str(s string) *string { return &s }

func minimalRelease() map[string]*string {
	return map[string]*string{
		FileAINames:           str("claude\n.claude\n"),
		FileForbiddenPackages: str("nodejs\n"),
		FilePackagesAllow:     str(""),
		FileUnitsAllow:        str("sshd.service\n"),
		FilePortsAllow:        str("443\n"),
	}
}

func TestLoadLists(t *testing.T) {
	l, err := LoadLists(writeRelease(t, minimalRelease()))
	if err != nil {
		t.Fatal(err)
	}
	if l.NFTDigest != "" || len(l.PackagesAllow) != 0 || l.PortsAllow[0] != 443 {
		t.Errorf("unexpected lists %+v", l)
	}

	for _, name := range []string{FileAINames, FileForbiddenPackages, FilePackagesAllow, FileUnitsAllow, FilePortsAllow} {
		files := minimalRelease()
		files[name] = nil
		if _, err := LoadLists(writeRelease(t, files)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("without %s: err = %v, want not exist", name, err)
		}
		files = minimalRelease()
		files[name] = str("a/b\n")
		if _, err := LoadLists(writeRelease(t, files)); !errors.Is(err, ErrList) || !strings.Contains(err.Error(), name) {
			t.Errorf("bad %s: err = %v", name, err)
		}
	}

	files := minimalRelease()
	files[FileAINames] = str("claude\n")
	if _, err := LoadLists(writeRelease(t, files)); !errors.Is(err, ErrList) {
		t.Errorf("no home path: err = %v", err)
	}

	files = minimalRelease()
	files[FileNFTDigest] = str("not a digest\n")
	if _, err := LoadLists(writeRelease(t, files)); !errors.Is(err, ErrList) {
		t.Errorf("bad digest: err = %v", err)
	}
}

// The lists shipped in deploy/authority-host/share parse and name what the
// check is for.
func TestShippedLists(t *testing.T) {
	l, err := LoadLists(filepath.Join("..", "..", "..", "deploy", "authority-host"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex", "opencode", "gemini", "amp", "copilot", "crush",
		"cursor-agent", "grok", "kilo", "kimi", "qwen", "aider", "goose", "continue", "cline", "devin",
		"kiro-cli", "antigravity", "agy", "droid"} {
		if !slices.Contains(l.AINames, name) {
			t.Errorf("ai-names.list lacks %s", name)
		}
	}
	for _, p := range []string{".claude", ".codex", ".config/opencode", ".gemini", ".cursor", ".kiro"} {
		if !slices.Contains(l.AIHomePaths, p) {
			t.Errorf("ai-names.list lacks %s", p)
		}
	}
	for _, p := range []string{"nodejs", "npm", "mise", "mise-bin", "uv", "pipx", "cargo", "rust", "go", "python-pip"} {
		if !slices.Contains(l.ForbiddenPackages, p) {
			t.Errorf("forbidden-packages.list lacks %s", p)
		}
	}
	if len(l.PackagesAllow) != 0 || len(l.UnitsAllow) != 0 || len(l.PortsAllow) != 0 || l.NFTDigest != "" {
		t.Errorf("the shipped allowlists are filled at deploy time, not in the repository: %+v", l)
	}
}
