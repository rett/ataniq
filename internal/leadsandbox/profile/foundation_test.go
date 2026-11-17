package profile

import (
	"errors"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	long63 := "a" + strings.Repeat("b", 62)
	tests := []struct {
		name string
		want bool
	}{
		{"a", true},
		{"0", true},
		{"arc-lead", true},
		{"a-", true},
		{"x1-y2-z3", true},
		{long63, true},
		{long63 + "c", false},
		{"", false},
		{"-a", false},
		{"A", false},
		{"a_b", false},
		{"a.b", false},
		{"a b", false},
		{"a/b", false},
		{"..", false},
		{"a\x00", false},
		{"a\n", false},
		{"é", false},
	}
	for _, tt := range tests {
		if got := ValidName(tt.name); got != tt.want {
			t.Errorf("ValidName(%q) = %v, want %v", tt.name, got, tt.want)
		}
		if err := CheckName("role", tt.name); (err == nil) != tt.want {
			t.Errorf("CheckName(%q) = %v, want ok %v", tt.name, err, tt.want)
		}
	}
}

func TestCheckPath(t *testing.T) {
	tests := []struct {
		path   string
		reason string // "" for accepted
	}{
		{"/", ""},
		{"/srv/state", ""},
		{"/srv/a b/c", ""},
		{"", "is empty"},
		{"srv/state", "absolute"},
		{"./srv", "absolute"},
		{"/srv/state/", "clean"},
		{"/srv//state", "clean"},
		{"/srv/./state", "clean"},
		{"/srv/../state", "clean"},
		{"/srv/state\n", "control"},
		{"/srv/st\x00ate", "control"},
		{"/" + strings.Repeat("a", maxPathLen), "longer"},
	}
	for _, tt := range tests {
		err := CheckPath("p", tt.path)
		if tt.reason == "" {
			if err != nil {
				t.Errorf("CheckPath(%q) = %v, want ok", tt.path, err)
			}
			continue
		}
		var ref *Refusal
		if !errors.As(err, &ref) || ref.Field != "p" || !strings.Contains(ref.Reason, tt.reason) {
			t.Errorf("CheckPath(%q) = %v, want a refusal mentioning %q", tt.path, err, tt.reason)
		}
	}
}

func TestCheckValue(t *testing.T) {
	for _, v := range []string{"", "xterm-256color", "C.UTF-8", "Europe/Berlin", strings.Repeat("a", maxValueLen)} {
		if err := CheckValue("v", v); err != nil {
			t.Errorf("CheckValue(%q) = %v, want ok", v, err)
		}
	}
	for _, v := range []string{"a\x00b", "a\nb", "a\rb", "a\x1bb", "a\x7fb", strings.Repeat("a", maxValueLen+1)} {
		if err := CheckValue("v", v); err == nil {
			t.Errorf("CheckValue(%q) = nil, want a refusal", v)
		}
	}
}

func TestRelated(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/a", "/a", true},
		{"/a/b", "/a", true},
		{"/a", "/a/b", true},
		{"/a", "/ab", false},
		{"/a/b", "/a/c", false},
		{"/", "/a", true},
		{"/a", "/", true},
		{"/", "/", true},
	}
	for _, tt := range tests {
		if got := related(tt.a, tt.b); got != tt.want {
			t.Errorf("related(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
	if below("/", "/") || below("/a", "/a") {
		t.Error("below must be strict")
	}
}

func TestRuntime(t *testing.T) {
	tests := []struct {
		rt                        Runtime
		other                     Runtime
		dir, env, instruction     string
		configInside, skillsInDir string
	}{
		{Claude, Codex, ".claude", "CLAUDE_CONFIG_DIR", "CLAUDE.md", "/home/lead/.claude", "/home/lead/.claude/skills"},
		{Codex, Claude, ".codex", "CODEX_HOME", "AGENTS.md", "/home/lead/.codex", "/home/lead/.codex/skills"},
	}
	for _, tt := range tests {
		if !tt.rt.Valid() || tt.rt.Other() != tt.other || tt.rt.Other().Other() != tt.rt {
			t.Errorf("%s: Valid/Other wrong", tt.rt)
		}
		if tt.rt.ConfigDirName() != tt.dir || tt.rt.ConfigEnvName() != tt.env || tt.rt.InstructionFileName() != tt.instruction {
			t.Errorf("%s: names wrong: %q %q %q", tt.rt, tt.rt.ConfigDirName(), tt.rt.ConfigEnvName(), tt.rt.InstructionFileName())
		}
		if got := ConfigInside(tt.rt); got != tt.configInside {
			t.Errorf("ConfigInside(%s) = %q", tt.rt, got)
		}
		if got := SkillsInside(tt.rt); got != tt.skillsInDir {
			t.Errorf("SkillsInside(%s) = %q", tt.rt, got)
		}
		if got := InstructionInside(tt.rt); got != tt.configInside+"/"+tt.instruction {
			t.Errorf("InstructionInside(%s) = %q", tt.rt, got)
		}
		if got, err := ParseRuntime(string(tt.rt)); err != nil || got != tt.rt {
			t.Errorf("ParseRuntime(%s) = %q, %v", tt.rt, got, err)
		}
	}
	bad := Runtime("gemini")
	if bad.Valid() || bad.Other() != "" || bad.ConfigDirName() != "" || bad.ConfigEnvName() != "" ||
		bad.InstructionFileName() != "" || ConfigInside(bad) != "" || SkillsInside(bad) != "" || InstructionInside(bad) != "" {
		t.Error("an invalid runtime must give empty names")
	}
	for _, s := range []string{"", "Claude", "gemini", "claude "} {
		if _, err := ParseRuntime(s); err == nil {
			t.Errorf("ParseRuntime(%q) = nil, want a refusal", s)
		}
	}
	if got := Runtimes(); len(got) != 2 || got[0] != Claude || got[1] != Codex {
		t.Errorf("Runtimes() = %v", got)
	}
}

func TestInsidePaths(t *testing.T) {
	// The contract's inside paths, exactly.
	tests := []struct{ name, got, want string }{
		{"home", HomeInside, "/home/lead"},
		{"tree", TreeInside("ataniq"), "/work/ataniq"},
		{"probe", ProbeInside, "/ataniq/probe"},
		{"spec folder", SpecDirInside, "/ataniq/spec"},
		{"spec file", SpecFileInside, "/ataniq/spec/spec.json"},
		{"agent", AgentInside, "/ataniq/agent"},
		{"qargi", QargiInside, "/ataniq/qargi"},
		{"mirror", MirrorInside("ataniq"), "/ataniq/mirror/ataniq.git"},
		{"assignment", AssignmentInside, "/ataniq/assignment"},
		{"projection", ProjectionInside, "/ataniq/projection"},
		{"git identity", GitIdentityInside, "/ataniq/git/config"},
		{"git identity folder", GitIdentityDirInside, "/ataniq/git"},
		{"git identity file name", GitIdentityFileName, "config"},
		{"requests", RequestsInside, "/ataniq/requests"},
		{"mailbox", MailboxInside, "/ataniq/mailbox"},
		{"mail", MailInside, "/ataniq/mail"},
		{"coordinators", CoordInside, "/coord"},
		{"tmp", TmpInside, "/tmp"},
		{"claude managed", ManagedSettingsDefaultInside(Claude), "/etc/claude-code/managed-settings.json"},
		{"codex managed", ManagedSettingsDefaultInside(Codex), ""},
		{"resolv.conf", ResolvConfInside, "/etc/resolv.conf"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if got := strings.Join(GitReadOnlyRel(), " "); got != ".git/config .git/hooks .git/info" {
		t.Errorf("GitReadOnlyRel = %q", got)
	}
}

func TestLayout(t *testing.T) {
	l := Layout{StateRoot: "/srv/state", Role: "arc-lead", Project: "ataniq"}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := l.LeadDir(); got != "/srv/state/leads/arc-lead" {
		t.Errorf("LeadDir = %q", got)
	}
	if got := l.Home(); got != "/srv/state/leads/arc-lead/home" {
		t.Errorf("Home = %q", got)
	}
	if got := l.Tree(); got != "/srv/state/leads/arc-lead/tree" {
		t.Errorf("Tree = %q", got)
	}
	if got := l.GitMeta(); got != "/srv/state/leads/arc-lead/gitmeta" {
		t.Errorf("GitMeta = %q", got)
	}
	for rel, want := range map[string]string{
		".git/config": "/srv/git/config",
		".git/hooks":  "/srv/git/hooks",
		".git/info":   "/srv/git/info",
	} {
		if got := GitMetaSource("/srv/git", rel); got != want {
			t.Errorf("GitMetaSource(%q) = %q, want %q", rel, got, want)
		}
	}
	if GitMetaConfig != "config" || GitMetaHooks != "hooks" || GitMetaInfo != "info" {
		t.Error("the entries of the git metadata folder changed")
	}
	if got := l.QargiRecords(); got != "/srv/state/leads/arc-lead/qargi" {
		t.Errorf("Records = %q", got)
	}
	if got := l.Mirror(); got != "/srv/state/mirrors/ataniq.git" {
		t.Errorf("Mirror = %q", got)
	}
	for name, p := range map[string][2]string{
		"Requests":     {l.Requests(), "/srv/state/leads/arc-lead/requests"},
		"Mail":         {l.Mail(), "/srv/state/leads/arc-lead/mail"},
		"MailboxesDir": {l.MailboxesDir(), "/srv/state/mailbox"},
		"Mailbox":      {l.Mailbox(), "/srv/state/mailbox/arc-lead"},
		"PublishedDir": {l.PublishedDir(), "/srv/state/pub"},
		"Published":    {l.Published(), "/srv/state/pub/ataniq"},
	} {
		if p[0] != p[1] {
			t.Errorf("%s = %q, want %q", name, p[0], p[1])
		}
	}
	for rt, want := range map[Runtime]string{
		Claude: "/srv/state/leads/arc-lead/config/claude",
		Codex:  "/srv/state/leads/arc-lead/config/codex",
	} {
		got, err := l.ConfigDir(rt)
		if err != nil || got != want {
			t.Errorf("ConfigDir(%s) = %q, %v; want %q", rt, got, err, want)
		}
	}
	if _, err := l.ConfigDir("gemini"); err == nil {
		t.Error("ConfigDir of an invalid runtime must be refused")
	}
	if DirMode != 0o700 {
		t.Errorf("DirMode = %o", DirMode)
	}
}

func TestLayoutValidateRefusals(t *testing.T) {
	good := Layout{StateRoot: "/srv/state", Role: "arc-lead", Project: "ataniq"}
	tests := []struct {
		name   string
		mutate func(*Layout)
		field  string
	}{
		{"relative root", func(l *Layout) { l.StateRoot = "state" }, "state root"},
		{"empty root", func(l *Layout) { l.StateRoot = "" }, "state root"},
		{"unclean root", func(l *Layout) { l.StateRoot = "/srv/state/" }, "state root"},
		{"dotdot root", func(l *Layout) { l.StateRoot = "/srv/../state" }, "state root"},
		{"file system root", func(l *Layout) { l.StateRoot = "/" }, "state root"},
		{"empty role", func(l *Layout) { l.Role = "" }, "role"},
		{"upper-case role", func(l *Layout) { l.Role = "Arc" }, "role"},
		{"role with slash", func(l *Layout) { l.Role = "a/b" }, "role"},
		{"role dotdot", func(l *Layout) { l.Role = ".." }, "role"},
		{"leading dash role", func(l *Layout) { l.Role = "-a" }, "role"},
		{"long role", func(l *Layout) { l.Role = strings.Repeat("a", 64) }, "role"},
		{"empty project", func(l *Layout) { l.Project = "" }, "project"},
		{"project with dot", func(l *Layout) { l.Project = "a.git" }, "project"},
		{"project with NUL", func(l *Layout) { l.Project = "a\x00" }, "project"},
	}
	for _, tt := range tests {
		l := good
		tt.mutate(&l)
		err := l.Validate()
		var ref *Refusal
		if !errors.As(err, &ref) || ref.Field != tt.field {
			t.Errorf("%s: Validate = %v, want a refusal of %q", tt.name, err, tt.field)
		}
	}
}
