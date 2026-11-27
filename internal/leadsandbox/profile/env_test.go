package profile

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func envMap(t *testing.T, in Inputs) map[string]string {
	t.Helper()
	env, err := in.Environment()
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}
	m := map[string]string{}
	for _, e := range env {
		if _, dup := m[e.Name]; dup {
			t.Errorf("%s appears twice", e.Name)
		}
		m[e.Name] = e.Value
	}
	if !sort.SliceIsSorted(env, func(i, j int) bool { return env[i].Name < env[j].Name }) {
		t.Error("the environment is not sorted by name")
	}
	return m
}

func TestEnvironmentValues(t *testing.T) {
	tests := []struct {
		rt   Runtime
		want map[string]string
	}{
		{Claude, map[string]string{"CLAUDE_CONFIG_DIR": "/home/lead/.claude"}},
		{Codex, map[string]string{"CODEX_HOME": "/home/lead/.codex"}},
	}
	for _, tt := range tests {
		got := envMap(t, fixture(tt.rt))
		want := map[string]string{
			"PATH":                    DefaultPath,
			"HOME":                    "/home/lead",
			"USER":                    "root",
			"LOGNAME":                 "root",
			"SHELL":                   "/bin/bash",
			"TERM":                    "xterm-256color",
			"COLORTERM":               "truecolor",
			"LANG":                    "C.UTF-8",
			"TZ":                      "UTC",
			"TMPDIR":                  "/tmp",
			"XDG_CONFIG_HOME":         "/home/lead/.config",
			"XDG_DATA_HOME":           "/home/lead/.local/share",
			"XDG_STATE_HOME":          "/home/lead/.local/state",
			"XDG_CACHE_HOME":          "/home/lead/.cache",
			"GIT_CEILING_DIRECTORIES": "/work",
			"GIT_CONFIG_GLOBAL":       "/ataniq/git/config",
			"GOCACHE":                 "/home/lead/.cache/go-build",
			"GOMODCACHE":              "/home/lead/go/pkg/mod",
			"QARGI_LEAD":              "ataniq-lead-0123456789abcdef",
		}
		for k, v := range tt.want {
			want[k] = v
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: environment\n got: %v\nwant: %v", tt.rt, got, want)
		}
	}
}

func TestEnvironmentInputs(t *testing.T) {
	in := fixture(Claude)
	in.ColorTerm, in.Lang, in.TZ = "", "", ""
	got := envMap(t, in)
	for _, n := range []string{"COLORTERM", "LANG", "TZ"} {
		if _, ok := got[n]; ok {
			t.Errorf("%s is set though the host has none", n)
		}
	}
	// The names are the same either way.
	if !reflect.DeepEqual(in.EnvNames(), fixture(Claude).EnvNames()) {
		t.Error("an unset optional variable changes the names")
	}

	in = fixture(Codex)
	in.Identity, in.OwnerUID, in.OwnerGID, in.OwnerUser = IdentityOwner, 1000, 1000, "owner"
	in.Path, in.Shell, in.LeadIdentity = "/usr/bin:/bin", "/bin/sh", "ataniq-lead"
	in.TZ = "Europe/Berlin"
	got = envMap(t, in)
	for k, v := range map[string]string{
		"USER": "owner", "LOGNAME": "owner", "PATH": "/usr/bin:/bin", "SHELL": "/bin/sh",
		"QARGI_LEAD": "ataniq-lead", "TZ": "Europe/Berlin",
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestEnvironmentIsReadFromTheInputsOnly(t *testing.T) {
	t.Setenv("HOME", "/root")
	t.Setenv("TERM", "dumb")
	t.Setenv("QARGI_LEAD", "someone")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	got := envMap(t, fixture(Claude))
	if got["HOME"] != "/home/lead" || got["TERM"] != "xterm-256color" || got["QARGI_LEAD"] != "ataniq-lead-0123456789abcdef" {
		t.Errorf("the process environment leaked in: %v", got)
	}
	if _, ok := got["SSH_AUTH_SOCK"]; ok {
		t.Error("SSH_AUTH_SOCK is in the allowlist")
	}
}

func TestEnvNames(t *testing.T) {
	in := fixture(Claude)
	in.ExtraEnv = []EnvVar{{"PROJECT_FLAG", "1"}, {"ANOTHER", "x"}}
	names := in.EnvNames()
	if !sort.StringsAreSorted(names) {
		t.Errorf("names are not sorted: %v", names)
	}
	for _, want := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "COLORTERM", "LANG", "TZ", "TMPDIR",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CLAUDE_CONFIG_DIR",
		"GIT_CEILING_DIRECTORIES", "GIT_CONFIG_GLOBAL", "GOCACHE", "GOMODCACHE", "QARGI_LEAD", "PROJECT_FLAG", "ANOTHER"} {
		if !contains(names, want) {
			t.Errorf("name %s is missing", want)
		}
	}
	if contains(names, "CODEX_HOME") || len(names) != 22 {
		t.Errorf("names = %v", names)
	}
	// Every defined value has a name.
	for _, e := range func() []EnvVar { v, _ := in.Environment(); return v }() {
		if !contains(names, e.Name) {
			t.Errorf("%s has no name", e.Name)
		}
	}
}

func TestExtraEnvironment(t *testing.T) {
	in := fixture(Claude)
	in.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}, {"project_flag", "1"}, {"EMPTY", ""}}
	got := envMap(t, in)
	if got["GOFLAGS"] != "-mod=mod" || got["project_flag"] != "1" {
		t.Errorf("extra names missing: %v", got)
	}
	if v, ok := got["EMPTY"]; !ok || v != "" {
		t.Error("an extra variable with an empty value is lost")
	}
}

func TestExtraEnvironmentRefusals(t *testing.T) {
	tests := []struct {
		name   string
		extra  []EnvVar
		reason string
	}{
		{"agent socket", []EnvVar{{"SSH_AUTH_SOCK", "/tmp/s"}}, "may not add it"},
		{"session bus", []EnvVar{{"DBUS_SESSION_BUS_ADDRESS", "unix:path=/x"}}, "may not add it"},
		{"runtime dir", []EnvVar{{"XDG_RUNTIME_DIR", "/run/user/1000"}}, "may not add it"},
		{"anthropic key", []EnvVar{{"ANTHROPIC_API_KEY", "k"}}, "may not add it"},
		{"openai key", []EnvVar{{"OPENAI_API_KEY", "k"}}, "may not add it"},
		{"lower-case socket", []EnvVar{{"ssh_auth_sock", "x"}}, "may not add it"},
		{"other key", []EnvVar{{"GEMINI_API_KEY", "k"}}, "_API_KEY"},
		{"lower-case key", []EnvVar{{"my_api_key", "k"}}, "_API_KEY"},
		{"secret", []EnvVar{{"DB_SECRET", "s"}}, "_SECRET"},
		{"lower-case secret", []EnvVar{{"client_secret", "s"}}, "_SECRET"},
		{"password", []EnvVar{{"DB_PASSWORD", "p"}}, "_PASSWORD"},
		{"mixed-case password", []EnvVar{{"Db_Password", "p"}}, "_PASSWORD"},
		{"key", []EnvVar{{"SIGNING_KEY", "k"}}, "_KEY"},
		{"short key", []EnvVar{{"X_KEY", "k"}}, "_KEY"},
		{"key id of a cloud", []EnvVar{{"AWS_ACCESS_KEY_ID", "k"}}, "AWS_"},
		{"loader preload", []EnvVar{{"LD_PRELOAD", "/x.so"}}, "LD_"},
		{"loader library path", []EnvVar{{"ld_library_path", "/x"}}, "LD_"},
		{"git configuration parameters", []EnvVar{{"GIT_CONFIG_PARAMETERS", "'a.b=c'"}}, "GIT_CONFIG_"},
		{"git configuration count", []EnvVar{{"GIT_CONFIG_COUNT", "1"}}, "GIT_CONFIG_"},
		{"git configuration key", []EnvVar{{"GIT_CONFIG_KEY_0", "a.b"}}, "GIT_CONFIG_"},
		{"git system configuration", []EnvVar{{"git_config_system", "/x"}}, "GIT_CONFIG_"},
		{"git global configuration", []EnvVar{{"GIT_CONFIG_GLOBAL", "/x"}}, "defines it already"},
		{"aws region", []EnvVar{{"AWS_REGION", "x"}}, "AWS_"},
		{"aws secret", []EnvVar{{"AWS_SECRET_ACCESS_KEY", "x"}}, "_KEY"},
		{"aws profile", []EnvVar{{"aws_profile", "x"}}, "AWS_"},
		{"token", []EnvVar{{"GITHUB_TOKEN", "t"}}, "_TOKEN"},
		{"mixed-case token", []EnvVar{{"Npm_Token", "t"}}, "_TOKEN"},
		{"auth token", []EnvVar{{"ANTHROPIC_AUTH_TOKEN", "t"}}, "_TOKEN"},
		{"base name", []EnvVar{{"HOME", "/root"}}, "defines it already"},
		{"optional base name", []EnvVar{{"LANG", "C"}}, "defines it already"},
		{"the runtime's own config variable", []EnvVar{{"CLAUDE_CONFIG_DIR", "/x"}}, "defines it already"},
		{"the other runtime's config variable", []EnvVar{{"CODEX_HOME", "/x"}}, "config folder"},
		{"duplicate", []EnvVar{{"A", "1"}, {"A", "2"}}, "twice"},
		{"empty name", []EnvVar{{"", "1"}}, "characters"},
		{"name with a space", []EnvVar{{"A B", "1"}}, "[A-Za-z_]"},
		{"name with =", []EnvVar{{"A=B", "1"}}, "[A-Za-z_]"},
		{"name starting with a digit", []EnvVar{{"1A", "1"}}, "[A-Za-z_]"},
		{"long name", []EnvVar{{strings.Repeat("A", 65), "1"}}, "characters"},
		{"value with NUL", []EnvVar{{"A", "x\x00y"}}, "control"},
		{"value with a newline", []EnvVar{{"A", "x\ny"}}, "control"},
		{"long value", []EnvVar{{"A", strings.Repeat("x", 257)}}, "longer"},
	}
	for _, tt := range tests {
		in := fixture(Claude)
		in.ExtraEnv = tt.extra
		err := in.Validate()
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
			continue
		}
		if ref := refusalOf(t, err); !strings.Contains(ref.Reason, tt.reason) {
			t.Errorf("%s: reason %q does not mention %q", tt.name, ref.Reason, tt.reason)
		}
	}
	// Too many names.
	in := fixture(Claude)
	for i := 0; i <= maxExtraEnv; i++ {
		in.ExtraEnv = append(in.ExtraEnv, EnvVar{Name: "V" + strings.Repeat("A", i%30) + string(rune('A'+i/30)), Value: "1"})
	}
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too many names: %v", err)
	}
	// A refusal never prints the value it refused.
	in = fixture(Claude)
	in.ExtraEnv = []EnvVar{{"GITHUB_TOKEN", "super-secret-value"}}
	if err := in.Validate(); err == nil || strings.Contains(err.Error(), "super-secret-value") {
		t.Errorf("the refusal holds the value: %v", err)
	}
}

func TestEnvironmentInputRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Inputs)
		field  string
	}{
		{"no TERM", func(in *Inputs) { in.Term = "" }, "TERM"},
		{"TERM with NUL", func(in *Inputs) { in.Term = "xterm\x00" }, "TERM"},
		{"TERM with a newline", func(in *Inputs) { in.Term = "xterm\n" }, "TERM"},
		{"long TERM", func(in *Inputs) { in.Term = strings.Repeat("x", 257) }, "TERM"},
		{"COLORTERM with a newline", func(in *Inputs) { in.ColorTerm = "a\nb" }, "COLORTERM"},
		{"LANG with NUL", func(in *Inputs) { in.Lang = "C\x00" }, "LANG"},
		{"long TZ", func(in *Inputs) { in.TZ = strings.Repeat("x", 300) }, "TZ"},
		{"TZ with a carriage return", func(in *Inputs) { in.TZ = "UTC\r" }, "TZ"},
		{"lead identity with a newline", func(in *Inputs) { in.LeadIdentity = "a\nb" }, "lead identity"},
		{"lead identity with a slash", func(in *Inputs) { in.LeadIdentity = "ataniq/lead" }, "lead identity"},
		{"lead identity with dots", func(in *Inputs) { in.LeadIdentity = "../../x" }, "lead identity"},
		{"lead identity with a shell expansion", func(in *Inputs) { in.LeadIdentity = "x$(id)" }, "lead identity"},
		{"lead identity with a space and a pair", func(in *Inputs) { in.LeadIdentity = "x y=z" }, "lead identity"},
		{"lead identity with a semicolon", func(in *Inputs) { in.LeadIdentity = "x;y" }, "lead identity"},
		{"lead identity with capitals", func(in *Inputs) { in.LeadIdentity = "Lead" }, "lead identity"},
		{"lead identity with an underscore", func(in *Inputs) { in.LeadIdentity = "arc_lead" }, "lead identity"},
		{"lead identity starting with a dash", func(in *Inputs) { in.LeadIdentity = "-lead" }, "lead identity"},
		{"lead identity longer than 64 bytes", func(in *Inputs) { in.LeadIdentity = strings.Repeat("a", 65) }, "lead identity"},
		{"relative PATH element", func(in *Inputs) { in.Path = "/usr/bin:bin" }, "PATH element"},
		{"empty PATH element", func(in *Inputs) { in.Path = "/usr/bin::/bin" }, "PATH element"},
		{"unclean PATH element", func(in *Inputs) { in.Path = "/usr/bin/../x" }, "PATH element"},
		{"long PATH", func(in *Inputs) { in.Path = "/" + strings.Repeat("a/", 2100) + "a" }, "PATH"},
		{"relative SHELL", func(in *Inputs) { in.Shell = "bash" }, "SHELL"},
	}
	for _, tt := range tests {
		in := fixture(Claude)
		tt.mutate(&in)
		err := in.Validate()
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
			continue
		}
		if ref := refusalOf(t, err); ref.Field != tt.field {
			t.Errorf("%s: refused field %q, want %q", tt.name, ref.Field, tt.field)
		}
	}
}

func TestLeadIdentityShape(t *testing.T) {
	for _, id := range []string{"a", "arc-lead", "0", "a-", strings.Repeat("a", 64), "a--b"} {
		in := fixture(Claude)
		in.LeadIdentity = id
		if err := in.Validate(); err != nil {
			t.Errorf("%q: %v", id, err)
		} else if got := envMap(t, in)[EnvQargiLead]; got != id {
			t.Errorf("QARGI_LEAD = %q, want %q", got, id)
		}
	}
	in := fixture(Claude)
	in.LeadIdentity = ""
	if got, ok := envMap(t, in)[EnvQargiLead]; ok {
		t.Errorf("profile without an identity exported QARGI_LEAD=%q", got)
	}
}

func TestCheckEnvName(t *testing.T) {
	for _, n := range []string{"PATH", "A", "_A", "a1", "GOFLAGS", "MY_TOKENS", "API_KEYS", "TOKEN", "_TOKEN_X"} {
		if err := CheckEnvName(n); err != nil {
			t.Errorf("CheckEnvName(%q) = %v", n, err)
		}
	}
	// Names that merely look close are fine.
	for _, n := range []string{"KEY", "SECRET", "PASSWORDS", "KEY_X", "LD", "LDFLAGS", "GIT_CONFIG", "GIT_CONFIGX", "AWSX", "GIT_CONFIG_GLOBAL"} {
		if err := CheckEnvName(n); err != nil {
			t.Errorf("CheckEnvName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"X_TOKEN", "_TOKEN", "_API_KEY", "x_api_key", "X_SECRET", "_SECRET", "x_password", "_KEY", "X_KEY", "LD_X", "ld_", "GIT_CONFIG_X", "git_config_", "AWS_X", "aws_"} {
		if err := CheckEnvName(n); err == nil {
			t.Errorf("CheckEnvName(%q) = nil", n)
		}
	}
}

func TestDiffEnvironment(t *testing.T) {
	want := []EnvVar{{"A", "1"}, {"B", "2"}, {"C", "x=y"}, {"D", ""}}
	tests := []struct {
		name     string
		observed []string
		want     []string // the String of each difference
	}{
		{"equal", []string{"A=1", "B=2", "C=x=y", "D="}, nil},
		{"equal in another order", []string{"D=", "C=x=y", "B=2", "A=1"}, nil},
		{"missing", []string{"A=1", "B=2", "C=x=y"}, []string{`missing "D"`}},
		{"unexpected", []string{"A=1", "B=2", "C=x=y", "D=", "SSH_AUTH_SOCK=/tmp/s"}, []string{`unexpected "SSH_AUTH_SOCK"`}},
		{"changed", []string{"A=1", "B=3", "C=x=y", "D="}, []string{`changed "B": want "2", got "3"`}},
		{"changed to empty", []string{"A=1", "B=", "C=x=y", "D="}, []string{`changed "B": want "2", got ""`}},
		{"changed from empty", []string{"A=1", "B=2", "C=x=y", "D=x"}, []string{`changed "D": want "", got "x"`}},
		{"value with an equals sign", []string{"A=1", "B=2", "C=x", "D="}, []string{`changed "C": want "x=y", got "x"`}},
		{"duplicate", []string{"A=1", "A=1", "B=2", "C=x=y", "D="}, []string{`duplicate "A"`}},
		{"duplicate that differs", []string{"A=1", "A=2", "B=2", "C=x=y", "D="}, []string{`duplicate "A"`}},
		{"malformed", []string{"A=1", "junk", "B=2", "C=x=y", "D="}, []string{`malformed "(entry 1)"`}},
		{"empty name", []string{"=v", "A=1", "B=2", "C=x=y", "D="}, []string{`malformed "(entry 0)"`}},
		{"nothing observed", nil, []string{`missing "A"`, `missing "B"`, `missing "C"`, `missing "D"`}},
		{"sorted", []string{"Z=1", "A=9", "M=1", "C=x=y", "D="}, []string{`changed "A": want "1", got "9"`, `missing "B"`, `unexpected "M"`, `unexpected "Z"`}},
		{"case matters", []string{"a=1", "B=2", "C=x=y", "D="}, []string{`missing "A"`, `unexpected "a"`}},
	}
	for _, tt := range tests {
		diffs, err := DiffEnvironment(want, tt.observed)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		var got []string
		for _, d := range diffs {
			got = append(got, d.String())
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got: %q\nwant: %q", tt.name, got, tt.want)
		}
	}
}

func mustDiff(t *testing.T, want []EnvVar, observed []string) []EnvDiff {
	t.Helper()
	d, err := DiffEnvironment(want, observed)
	if err != nil {
		t.Fatalf("DiffEnvironment: %v", err)
	}
	return d
}

func TestDiffEnvironmentQuotesObservedNames(t *testing.T) {
	// A name with a newline or another control byte is legal in environ. It
	// must not turn one difference into two report lines or forge one.
	for _, name := range []string{"EVIL\nok: forged", "A\rB", "A\x00B", "\x1b[2JX", "caf\xc3", "A B"} {
		d := mustDiff(t, nil, []string{name + "=v"})
		if len(d) != 1 {
			t.Fatalf("%q: %d differences", name, len(d))
		}
		line := d[0].String()
		if strings.ContainsAny(line, "\n\r\x00\x1b") {
			t.Errorf("%q: the line %q holds a raw control byte", name, line)
		}
		if want := fmt.Sprintf("unexpected %q", name); line != want {
			t.Errorf("line = %q, want %q", line, want)
		}
	}
	// The same for a changed value's name and a missing name.
	d := mustDiff(t, []EnvVar{{"A\nB", "1"}}, []string{"A\nB=2"})
	if len(d) != 1 || strings.Contains(d[0].String(), "\n") || !strings.HasPrefix(d[0].String(), `changed "A\nB":`) {
		t.Errorf("changed: %q", d)
	}
	d = mustDiff(t, []EnvVar{{"A\nB", "1"}}, nil)
	if len(d) != 1 || strings.Contains(d[0].String(), "\n") {
		t.Errorf("missing: %q", d)
	}
}

func TestDiffEnvironmentRefusesDuplicateWantedNames(t *testing.T) {
	for _, want := range [][]EnvVar{
		{{"A", "1"}, {"A", "2"}},
		{{"A", "1"}, {"B", "1"}, {"A", "1"}},
	} {
		d, err := DiffEnvironment(want, []string{"A=1"})
		if err == nil || d != nil {
			t.Errorf("%v: got %v, %v", want, d, err)
			continue
		}
		if ref := refusalOf(t, err); !strings.Contains(ref.Reason, "twice") {
			t.Errorf("reason = %q", ref.Reason)
		}
	}
}

func TestDiffEnvironmentIsBounded(t *testing.T) {
	var observed []string
	for i := 0; i < 1000; i++ {
		observed = append(observed, fmt.Sprintf("X%04d=v", i))
	}
	d := mustDiff(t, nil, observed)
	if len(d) != MaxEnvDiffs+1 {
		t.Fatalf("%d differences, want %d", len(d), MaxEnvDiffs+1)
	}
	last := d[len(d)-1]
	if last.Kind != EnvTruncated || last.Name != fmt.Sprintf("(%d more)", 1000-MaxEnvDiffs) {
		t.Errorf("last = %+v", last)
	}
	// Exactly the limit is not truncated.
	if d := mustDiff(t, nil, observed[:MaxEnvDiffs]); len(d) != MaxEnvDiffs || d[len(d)-1].Kind == EnvTruncated {
		t.Errorf("%d differences at the limit", len(d))
	}
}

func TestDiffEnvironmentDoesNotKeepUnexpectedValues(t *testing.T) {
	diffs := mustDiff(t, nil, []string{"ANTHROPIC_API_KEY=sk-secret"})
	if len(diffs) != 1 || diffs[0].Kind != EnvUnexpected {
		t.Fatalf("diffs = %v", diffs)
	}
	if diffs[0].Got != "" || diffs[0].Want != "" || strings.Contains(diffs[0].String(), "sk-secret") {
		t.Errorf("the value of an unexpected variable is kept: %+v", diffs[0])
	}
	long := strings.Repeat("n", 500)
	d := mustDiff(t, nil, []string{long + "=1"})
	if len(d) != 1 || len(d[0].Name) > 70 {
		t.Errorf("a long name is not clipped: %d", len(d[0].Name))
	}
	d = mustDiff(t, []EnvVar{{"A", "1"}}, []string{"A=" + strings.Repeat("v", 500)})
	if len(d) != 1 || len(d[0].Got) > 300 {
		t.Errorf("a long value is not clipped: %d", len(d[0].Got))
	}
}

func TestDiffAgainstTheProfileEnvironment(t *testing.T) {
	in := fixture(Claude)
	env, err := in.Environment()
	if err != nil {
		t.Fatal(err)
	}
	var observed []string
	for _, e := range env {
		observed = append(observed, e.Name+"="+e.Value)
	}
	if d := mustDiff(t, env, observed); len(d) != 0 {
		t.Errorf("the environment differs from itself: %v", d)
	}
	observed = append(observed, "XDG_RUNTIME_DIR=/run/user/1000")
	if d := mustDiff(t, env, observed); len(d) != 1 || d[0].String() != `unexpected "XDG_RUNTIME_DIR"` {
		t.Errorf("diff = %v", d)
	}
}
