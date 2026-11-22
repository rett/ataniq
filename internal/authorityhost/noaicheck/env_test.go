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

func TestParseEnv(t *testing.T) {
	const (
		sh   = syntaxShell
		ef   = syntaxEnvFile
		pam  = syntaxPam
		unit = syntaxSystemd
	)
	cases := []struct {
		name    string
		syntax  envSyntax
		in      string
		want    []string
		files   []string
		sourced []string
		bad     bool
	}{
		{"etc/environment", ef, "ANTHROPIC_API_KEY=" + secret + "\nLANG=C\n", []string{"ANTHROPIC_API_KEY"}, nil, nil, false},
		{"environment file quoted", ef, `OPENAI_API_KEY="` + secret + `"`, []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"environment file export", ef, "export GEMINI_API_KEY=x\n", []string{"GEMINI_API_KEY"}, nil, nil, false},
		{"environment file comments", ef, "# OPENAI_API_KEY=x\n; ANTHROPIC_API_KEY=x\n", nil, nil, nil, false},
		{"environment file value spanning lines", ef, "A=\"one\nOPENAI_API_KEY=x\"\nCODEX_HOME=/x\n", []string{"CODEX_HOME"}, nil, nil, false},
		{"environment file value like a name", ef, "A=OPENAI_API_KEY=x\n", nil, nil, nil, false},
		{"environment file malformed", ef, "LANG=C\nnot an assignment\n", nil, nil, nil, true},
		{"environment file unclosed quote", ef, "A=\"x\n", nil, nil, nil, true},
		{"pam_environment", pam, "OPENAI_API_KEY DEFAULT=x\nLANG OVERRIDE=C\n# CODEX_HOME DEFAULT=x\n", []string{"OPENAI_API_KEY"}, nil, nil, false},

		{"export", sh, "export GEMINI_API_KEY=" + secret, []string{"GEMINI_API_KEY"}, nil, nil, false},
		{"export without a value", sh, "export LANG CLAUDE_CONFIG_DIR", []string{"CLAUDE_CONFIG_DIR"}, nil, nil, false},
		{"a quoted export list", sh, `export "ANTHROPIC_API_KEY" 'OPENAI_API_KEY'=x "LANG=C OPENAI_ORG=y" -n CODEX_HOME`,
			[]string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_HOME"}, nil, nil, false},
		{"declare", sh, "declare -x CODEX_HOME=/x", []string{"CODEX_HOME"}, nil, nil, false},
		{"append", sh, "PATH+=:/x\nOPENAI_ORG+=x", []string{"OPENAI_ORG"}, nil, nil, false},
		{"two before a command", sh, "A=1 GROQ_API_KEY=2 cmd", []string{"GROQ_API_KEY"}, nil, nil, false},
		{"env", sh, "env OPENAI_API_KEY=x cmd", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"after an operator", sh, "true && OPENAI_API_KEY=x; [ -n x ] || { CODEX_HOME=y; }", []string{"OPENAI_API_KEY", "CODEX_HOME"}, nil, nil, false},
		{"csh setenv", sh, "setenv OPENAI_API_KEY " + secret, []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"csh setenv names one", sh, "setenv LANG OPENAI_API_KEY", nil, nil, nil, false},
		{"fish set in a fish file", sh, "set -gx ANTHROPIC_AUTH_TOKEN " + secret, []string{"ANTHROPIC_AUTH_TOKEN"}, nil, nil, false},
		{"a commented-out assignment", sh, "# OPENAI_API_KEY=x\n   # export CODEX_HOME=x\nLANG=C # ANTHROPIC_API_KEY=x\n", nil, nil, nil, false},
		{"a value like a name", sh, "A=\"OPENAI_API_KEY=x\"\nexport B=OPENAI_API_KEY C='x OPENAI_ORG=y'\nalias k='ANTHROPIC_API_KEY=1 cmd'\n", nil, nil, nil, false},
		{"a quote across lines", sh, "PS1='\nOPENAI_API_KEY=x\n'\n", nil, nil, nil, false},
		{"a here-document", sh, "cat <<EOF >/tmp/x\nOPENAI_API_KEY=x\nEOF\ncat <<-'END'\n\tCODEX_HOME=y\n\tEND\nGEMINI_API_KEY=z\n",
			[]string{"GEMINI_API_KEY"}, nil, nil, false},
		{"a substitution", sh, "X=$(printf '%s' \"a) OPENAI_API_KEY=1\")\nY=`echo CODEX_HOME=2`\nZ=${A:-OPENAI_ORG=3}\n", nil, nil, nil, false},
		{"a reference is not a setting", sh, `echo "$OPENAI_API_KEY"` + "\n[ \"$ANTHROPIC_API_KEY\" = x ]", nil, nil, nil, false},
		{"unset", sh, "unset OPENAI_API_KEY", nil, nil, nil, false},
		{"once each", sh, "OPENAI_API_KEY=1\nOPENAI_API_KEY=2", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"sourced", sh, ". /etc/app/env.sh\nsource \"/etc/app/more.sh\"\n. ./relative.sh\n. \"$HOME/x\"\n", nil, nil,
			[]string{"/etc/app/env.sh", "/etc/app/more.sh"}, false},
		{"a profile in the usual style", sh, profileSample, nil, nil, nil, false},
		{"an unclosed quote", sh, "A='x\n", nil, nil, nil, true},
		{"an unclosed substitution", sh, "A=$(x\n", nil, nil, nil, true},
		{"a here-document that never ends", sh, "cat <<EOF\nx\n", nil, nil, nil, true},
		{"an export whose name is an expansion", sh, "export $VARS\nexport PATH=$PATH:/x\n", nil, nil, nil, true},

		{"unit Environment", unit, "[Service]\nEnvironment=LANG=C \"ANTHROPIC_API_KEY=" + secret + "\"",
			[]string{"ANTHROPIC_API_KEY"}, nil, nil, false},
		{"unit Environment quoted whole", unit, `Environment="OPENAI_API_KEY=` + secret + `"`, []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"unit Environment value like a name", unit, `Environment="A=OPENAI_API_KEY=x" 'B=x CODEX_HOME=y'`, nil, nil, nil, false},
		{"unit continued line", unit, "Environment=LANG=C \\\n  # a comment\n  OPENAI_API_KEY=x\n", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"unit comments", unit, "#Environment=OPENAI_API_KEY=x\n;Environment=CODEX_HOME=x\n", nil, nil, nil, false},
		{"unit other keys", unit, "ExecStart=/usr/bin/env OPENAI_API_KEY=x 'unclosed\nDescription=CODEX_HOME=x\n", nil, nil, nil, false},
		{"manager DefaultEnvironment", unit, "DefaultEnvironment=CLAUDE_CODE_X=1", []string{"CLAUDE_CODE_X"}, nil, nil, false},
		{"PassEnvironment", unit, "PassEnvironment=LANG OPENAI_API_KEY", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"EnvironmentFile", unit, "EnvironmentFile=-/etc/default/app\nEnvironmentFile=\"/etc/app env\"\nEnvironmentFile=-'/etc/x/*.env'\n", nil,
			[]string{"/etc/default/app", "/etc/app env", "/etc/x/*.env"}, nil, false},
		{"EnvironmentFile relative", unit, "EnvironmentFile=relative", nil, nil, nil, true},
		{"EnvironmentFile with a specifier", unit, "EnvironmentFile=/etc/app/%i.env", nil, nil, nil, true},
		{"EnvironmentFile in the home or runtime folder", unit, "EnvironmentFile=-%h/.config/app.env\nEnvironmentFile=-%t/app/env.conf\n", nil,
			[]string{"%h/.config/app.env", "%t/app/env.conf"}, nil, false},
		{"unit unclosed quote", unit, "Environment=\"OPENAI_API_KEY=x", nil, nil, nil, true},
		{"unit word without a value", unit, "Environment=OPENAI_API_KEY", nil, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := parseEnv(tc.syntax, tc.in, strings.Contains(tc.name, "fish"))
			if !reflect.DeepEqual(r.names, tc.want) || !reflect.DeepEqual(r.files, tc.files) ||
				!reflect.DeepEqual(r.sourced, tc.sourced) || r.bad != tc.bad {
				t.Errorf("got %q %q %q bad=%v, want %q %q %q bad=%v", r.names, r.files, r.sourced, r.bad,
					tc.want, tc.files, tc.sourced, tc.bad)
			}
			for _, n := range r.names {
				if !validName(n) {
					t.Errorf("an invalid name reported: %q", n)
				}
			}
		})
	}
}

// profileSample is a system profile in the usual style: functions, case,
// tests, loops, arithmetic and parameter expansion. It sets no provider
// key and must parse.
const profileSample = `# a system profile
append_path () {
    case ":$PATH:" in
        *:"$1":*)
            ;;
        *)
            PATH="${PATH:+$PATH:}$1"
    esac
}
append_path '/usr/local/sbin'
export PATH
if test -d /etc/profile.d/; then
	for profile in /etc/profile.d/*.sh; do
		test -r "$profile" && . "$profile"
	done
	unset profile
fi
unset -f append_path
if [ -n "${BASH_VERSION-}" -a -n "${PS1-}" ] && [ "${0#-}" != sh ]; then
	[[ $- != *i* ]] && return
	PS1='[\u@\h \W]\$ '
	n=$(( ${#PATH} + 1 ))
	arr=(one "two three" $(echo four))
	printf '\033]0;%s\007' "${USER}@${HOSTNAME%%.*}"
	echo "it's \"quoted\"" > /dev/null 2>&1
fi
`

func TestValidName(t *testing.T) {
	for n, want := range map[string]bool{
		"OPENAI_API_KEY": true, "_x1": true, "": false, "1A": false, "A-B": false, "A B": false,
		"sk-planted": false, strings.Repeat("A", maxNameLen): true, strings.Repeat("A", maxNameLen+1): false,
	} {
		if validName(n) != want {
			t.Errorf("validName(%q) = %v", n, !want)
		}
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
		{"an EnvironmentFile glob", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironmentFile=-\"/etc/app/*.env\"\n")
			f.file("etc/app/a.env", "LANG=C\n")
			f.file("etc/app/b.env", "CODEX_HOME=/x\n")
			f.file("etc/app/c.txt", "OPENAI_API_KEY=x\n")
		}, []Hit{env("/etc/app/b.env", "CODEX_HOME")}},
		{"an EnvironmentFile glob in a folder name", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironmentFile=/etc/*/x.env\n")
		}, []Hit{{Kind: KindIncomplete, Path: PathEnvSyntax, Detail: "/etc/systemd/system/app.service"}}},
		{"a sourced file, in a cycle", func(f *fixture) {
			f.file("etc/profile.d/a.sh", ". /etc/app/b.sh\n")
			f.file("etc/app/b.sh", ". /etc/profile.d/a.sh\nexport X_API_KEY=1\n")
		}, []Hit{env("/etc/app/b.sh", "X_API_KEY")}},
		{"syntax that cannot be parsed", func(f *fixture) {
			f.file("etc/profile.d/odd.sh", "export OPENAI_API_KEY=x\nA='unclosed\n")
		}, []Hit{env("/etc/profile.d/odd.sh", "OPENAI_API_KEY"), {Kind: KindIncomplete, Path: PathEnvSyntax, Detail: "/etc/profile.d/odd.sh"}}},
		{"a commented-out profile line", func(f *fixture) {
			f.file("home/user/.bashrc", "# export ANTHROPIC_API_KEY="+secret+"\nexport LANG=C # OPENAI_API_KEY=x\n")
		}, nil},
		{"EnvironmentFile specifiers in a user unit", func(f *fixture) {
			f.file("usr/lib/systemd/user/app.service", "[Service]\nEnvironmentFile=-%h/.config/app.env\nEnvironmentFile=-%t/app.env\n")
			f.file("home/user/.config/app.env", "OPENAI_API_KEY=x\n")
			f.file("run/user/1000/app.env", "CODEX_HOME=x\n")
		}, []Hit{env("/home/user/.config/app.env", "OPENAI_API_KEY"), env("/run/user/1000/app.env", "CODEX_HOME")}},
		{"EnvironmentFile specifiers in a system unit", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironmentFile=-%t/app.env\n")
			f.file("run/app.env", "CODEX_HOME=x\n")
		}, []Hit{env("/run/app.env", "CODEX_HOME")}},
		{"a pam_environment", func(f *fixture) {
			f.file("home/user/.pam_environment", "GEMINI_API_KEY DEFAULT="+secret+"\n")
		}, []Hit{env("/home/user/.pam_environment", "GEMINI_API_KEY")}},
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
	r := c.RunAll(nil)
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
	want := "HIT env /etc/environment ANTHROPIC_API_KEY\nHIT env /etc/profile.d/x.sh OPENAI_API_KEY\n" +
		"NOTE packages not enforced\nNOTE nft not pinned\nRUN incomplete\nFAILED 2 hit(s)\n"
	if text.String() != want {
		t.Errorf("text\n%s\nwant\n%s", text.String(), want)
	}
}
