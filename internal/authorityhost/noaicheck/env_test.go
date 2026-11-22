package noaicheck

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// secret stands for a key's value; no report may hold it.
const secret = "sk-planted-SECRET-0123456789"

func TestProviderKeyName(t *testing.T) {
	for name, want := range map[string]bool{
		"ANTHROPIC_API_KEY": true, "OPENAI_BASE_URL": true, "GEMINI_API_KEY": true,
		"CLAUDE_CODE_USE_BEDROCK": true, "CODEX_HOME": true, "GOOGLE_API_KEY": true,
		"GH_AUTH_TOKEN": true, "anthropic_api_key": true, "MY_API_KEY": true,
		"PATH": false, "LANG": false, "API_KEY": false, "ANTHROPIC": false, "AUTH_TOKEN": false,
		"OPENAI": false, "CLAUDE": false, "API_KEYS": false,
	} {
		if got := ProviderKeyName(name); got != want {
			t.Errorf("ProviderKeyName(%s) = %v", name, got)
		}
	}
}

func TestEnvNames(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  []string
		files []string
	}{
		{"etc/environment", "ANTHROPIC_API_KEY=" + secret + "\nLANG=C\n", []string{"ANTHROPIC_API_KEY"}, nil},
		{"quoted", `OPENAI_API_KEY="` + secret + `"`, []string{"OPENAI_API_KEY"}, nil},
		{"export", "export GEMINI_API_KEY=" + secret, []string{"GEMINI_API_KEY"}, nil},
		{"export without a value", "export LANG CLAUDE_CONFIG_DIR", []string{"CLAUDE_CONFIG_DIR"}, nil},
		{"declare", "declare -x CODEX_HOME=/x", []string{"CODEX_HOME"}, nil},
		{"append", "PATH+=:/x\nOPENAI_ORG+=x", []string{"OPENAI_ORG"}, nil},
		{"two on a line", "A=1 GROQ_API_KEY=2 cmd", []string{"GROQ_API_KEY"}, nil},
		{"csh setenv", "setenv OPENAI_API_KEY " + secret, []string{"OPENAI_API_KEY"}, nil},
		{"csh setenv names one", "setenv LANG OPENAI_API_KEY", nil, nil},
		{"fish set", "set -gx ANTHROPIC_AUTH_TOKEN " + secret, []string{"ANTHROPIC_AUTH_TOKEN"}, nil},
		{"unit Environment", "[Service]\nEnvironment=LANG=C \"ANTHROPIC_API_KEY=" + secret + "\"",
			[]string{"ANTHROPIC_API_KEY"}, nil},
		{"unit Environment quoted whole", `Environment="OPENAI_API_KEY=` + secret + `"`, []string{"OPENAI_API_KEY"}, nil},
		{"manager DefaultEnvironment", "DefaultEnvironment=CLAUDE_CODE_X=1", []string{"CLAUDE_CODE_X"}, nil},
		{"PassEnvironment", "PassEnvironment=LANG OPENAI_API_KEY", []string{"OPENAI_API_KEY"}, nil},
		{"PassEnvironment first", "PassEnvironment=GEMINI_API_KEY", []string{"GEMINI_API_KEY"}, nil},
		{"EnvironmentFile", "EnvironmentFile=-/etc/default/app\nEnvironmentFile=relative", nil, []string{"/etc/default/app"}},
		{"comments", "# OPENAI_API_KEY=x\n; ANTHROPIC_API_KEY=x\n   # export CODEX_HOME=x", nil, nil},
		{"a reference is not a setting", `echo "$OPENAI_API_KEY"` + "\n[ \"$ANTHROPIC_API_KEY\" = x ]", nil, nil},
		{"unset", "unset OPENAI_API_KEY", nil, nil},
		{"once each", "OPENAI_API_KEY=1\nOPENAI_API_KEY=2", []string{"OPENAI_API_KEY"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, files := envNames(tc.in)
			if !reflect.DeepEqual(names, tc.want) || !reflect.DeepEqual(files, tc.files) {
				t.Errorf("got %q %q, want %q %q", names, files, tc.want, tc.files)
			}
		})
	}
}

func TestCheckEnv(t *testing.T) {
	env := func(p, n string) Hit { return Hit{Kind: KindEnv, Path: p, Detail: n} }
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"etc/environment", func(f *fixture) { f.file("etc/environment", "ANTHROPIC_API_KEY="+secret+"\n") },
			[]Hit{env("/etc/environment", "ANTHROPIC_API_KEY")}},
		{"profile.d", func(f *fixture) { f.file("etc/profile.d/ai.sh", "export OPENAI_API_KEY="+secret+"\n") },
			[]Hit{env("/etc/profile.d/ai.sh", "OPENAI_API_KEY")}},
		{"manager drop-in", func(f *fixture) {
			f.file("etc/systemd/system.conf.d/env.conf", "[Manager]\nDefaultEnvironment=GEMINI_API_KEY="+secret+"\n")
		}, []Hit{env("/etc/systemd/system.conf.d/env.conf", "GEMINI_API_KEY")}},
		{"unit drop-in", func(f *fixture) {
			f.file("etc/systemd/system/sshd.service.d/x.conf", "[Service]\nEnvironment=CLAUDE_CODE_OAUTH_TOKEN="+secret+"\n")
		}, []Hit{env("/etc/systemd/system/sshd.service.d/x.conf", "CLAUDE_CODE_OAUTH_TOKEN")}},
		{"vendor unit", func(f *fixture) {
			f.file("usr/lib/systemd/system/x.service", "[Service]\nEnvironment=CODEX_API_KEY="+secret+"\n")
		}, []Hit{env("/usr/lib/systemd/system/x.service", "CODEX_API_KEY")}},
		{"an EnvironmentFile", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironmentFile=-/etc/app/env\n")
			f.file("etc/app/env", "OPENAI_API_KEY="+secret+"\n")
		}, []Hit{env("/etc/app/env", "OPENAI_API_KEY")}},
		{"EnvironmentFile cycle", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "EnvironmentFile=/etc/app/a\n")
			f.file("etc/app/a", "EnvironmentFile=/etc/app/b\n")
			f.file("etc/app/b", "EnvironmentFile=/etc/app/a\nX_API_KEY=1\n")
		}, []Hit{env("/etc/app/b", "X_API_KEY")}},
		{"a home profile", func(f *fixture) { f.file("home/user/.bashrc", "export ANTHROPIC_API_KEY="+secret+"\n") },
			[]Hit{env("/home/user/.bashrc", "ANTHROPIC_API_KEY")}},
		{"root's profile", func(f *fixture) { f.file("root/.profile", "OPENAI_API_KEY="+secret+"\n") },
			[]Hit{env("/root/.profile", "OPENAI_API_KEY")}},
		{"a user unit", func(f *fixture) {
			f.file("home/user/.config/systemd/user/a.service", "Environment=GEMINI_API_KEY=x\n")
		}, []Hit{env("/home/user/.config/systemd/user/a.service", "GEMINI_API_KEY")}},
		{"a masked unit", func(f *fixture) { f.link("etc/systemd/system/x.service", "/dev/null") }, nil},
		{"a FIFO is not opened", func(f *fixture) { mkfifo(f, "etc/environment") },
			[]Hit{{Kind: KindUnreadable, Path: "/etc/environment", Detail: errNotRegular.Error()}}},
		{"too large", func(f *fixture) { f.file("etc/profile", strings.Repeat("#\n", maxEnvFile/2+1)) },
			[]Hit{{Kind: KindUnreadable, Path: "/etc/profile", Detail: errTooLarge.Error()}}},
	}, (*Checker).CheckEnv)
}

// No report holds a planted value: in text or JSON.
func TestEnvReportOmitsValues(t *testing.T) {
	f := cleanFixture(t)
	f.file("etc/environment", "ANTHROPIC_API_KEY="+secret+"\n")
	f.file("etc/profile.d/x.sh", "export OPENAI_API_KEY='"+secret+"'\n")
	c := f.checker(testLists())
	r := c.RunAll()
	if len(r.Hits) != 2 {
		t.Fatalf("hits %v", r.Hits)
	}
	var text, js bytes.Buffer
	if err := r.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), js.String()} {
		if strings.Contains(out, secret) || strings.Contains(out, "sk-") {
			t.Errorf("the report holds the value:\n%s", out)
		}
	}
	want := "HIT env /etc/environment ANTHROPIC_API_KEY\nHIT env /etc/profile.d/x.sh OPENAI_API_KEY\nFAILED 2 hit(s)\n"
	if text.String() != want {
		t.Errorf("text\n%s\nwant\n%s", text.String(), want)
	}
}
