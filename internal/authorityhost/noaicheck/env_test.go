package noaicheck

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
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
		{"a brace inside a parameter expansion", sh, "o=${o%%[<{().[]*}\nx=\"${x:-{a}}\"\ny=${y:-${z}}\nOPENAI_API_KEY=1\n", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"a substitution", sh, "X=$(printf '%s' \"a) OPENAI_API_KEY=1\")\nY=`echo CODEX_HOME=2`\nZ=${A:-OPENAI_ORG=3}\n", nil, nil, nil, false},
		{"a reference is not a setting", sh, `echo "$OPENAI_API_KEY"` + "\n[ \"$ANTHROPIC_API_KEY\" = x ]", nil, nil, nil, false},
		{"unset", sh, "unset OPENAI_API_KEY", nil, nil, nil, false},
		{"once each", sh, "OPENAI_API_KEY=1\nOPENAI_API_KEY=2", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"sourced", sh, ". /etc/app/env.sh\nsource \"/etc/app/more.sh\"\n. ./relative.sh\n. \"$HOME/x\"\n", nil, nil,
			[]string{"/etc/app/env.sh", "/etc/app/more.sh", "./relative.sh", "$HOME/x"}, false},
		{"sourced by a loop variable", sh, "for p in /etc/profile.d/*.sh /etc/x.sh; do\n\ttest -r \"$p\" && . \"$p\"\ndone\n", nil, nil,
			[]string{"/etc/profile.d/*.sh", "/etc/x.sh"}, false},
		{"sourced by a loop variable without a list", sh, "for p; do . \"${p}\"; done\n", nil, nil,
			[]string{"${p}"}, false},
		{"function-local declarations", sh, completionSample, nil, nil, nil, false},
		{"a top-level local or declare of a computed name", sh, "local \"$1\"\ndeclare \"$2\"=x\ntypeset -a \"${n}\"\nreadonly \"$v\"\n", nil, nil, nil, false},
		{"a function's declarations are its own", sh, "f() {\n  local OPENAI_API_KEY=x\n  declare -x CODEX_HOME=y\n  readonly ANTHROPIC_API_KEY\n}\nfunction g { typeset GEMINI_API_KEY=z; }\n", nil, nil, nil, false},
		{"a function's export still counts", sh, "f() {\n  export OPENAI_API_KEY=x\n  CODEX_HOME=y\n}\n", []string{"OPENAI_API_KEY", "CODEX_HOME"}, nil, nil, false},
		{"a function's export of a computed name", sh, "f() { export \"$1\"; }\n", nil, nil, nil, true},
		{"after a function a declaration counts again", sh, "f() { local A; }\ndeclare -x OPENAI_API_KEY=x\n", []string{"OPENAI_API_KEY"}, nil, nil, false},
		{"a profile in the usual style", sh, profileSample, nil, nil, []string{"/etc/profile.d/*.sh"}, false},
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
			[]string{"-/etc/default/app", "/etc/app env", "-/etc/x/*.env"}, nil, false},
		{"EnvironmentFile optional inside quotes", unit, "EnvironmentFile=\"-/etc/a\"\nEnvironmentFile=/etc/b\n", nil,
			[]string{"-/etc/a", "/etc/b"}, nil, false},
		{"EnvironmentFile relative", unit, "EnvironmentFile=relative", nil, nil, nil, true},
		{"EnvironmentFile with a specifier", unit, "EnvironmentFile=/etc/app/%i.env", nil, nil, nil, true},
		{"EnvironmentFile in the home or runtime folder", unit, "EnvironmentFile=-%h/.config/app.env\nEnvironmentFile=-%t/app/env.conf\n", nil,
			[]string{"-%h/.config/app.env", "-%t/app/env.conf"}, nil, false},
		{"unit unclosed quote", unit, "Environment=\"OPENAI_API_KEY=x", nil, nil, nil, true},
		{"unit word without a value", unit, "Environment=OPENAI_API_KEY", nil, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := parseEnv(tc.syntax, tc.in, strings.Contains(tc.name, "fish"))
			var sourced, files []string
			for _, w := range r.sourced {
				sourced = append(sourced, w.lit)
			}
			for _, f := range r.files {
				if f.optional {
					f.path = "-" + f.path
				}
				files = append(files, f.path)
			}
			if !reflect.DeepEqual(r.names, tc.want) || !reflect.DeepEqual(files, tc.files) ||
				!reflect.DeepEqual(sourced, tc.sourced) || r.bad != tc.bad {
				t.Errorf("got %q %q %q bad=%v, want %q %q %q bad=%v", r.names, files, sourced, r.bad,
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

// completionSample is in the style of the packaged bash-completion
// library: functions whose local declarations name their variables by
// positional parameters. Loading it sets no login variable, and it must
// parse.
const completionSample = `# a completion library
_comp_get_words()
{
    local exclude="" flag i OPTIND=1
    local cur cword words=()
    local upargs=() upvars=() vcur vcword vprev vwords
    while getopts "c:i:n:p:w:" flag "$@"; do
        case $flag in
            [cipw]) [[ $OPTARG != *[^_a-zA-Z0-9]* ]] || return 2 ;;
        esac
    done
    local IFS=$' \t\n'
    local "$2" "$3" "$4" && _comp_upvars -a"${#words[@]}" "$2" ${words[@]+"${words[@]}"} \
        -v "$3" "$cword" -v "$4" "${cur:0:index}"
}

function _comp_upvars {
    if ! (($#)); then
        return 2
    fi
    while (($#)); do
        case $1 in
            -a*)
                [[ ${1#-a} ]] || return 1
                unset -v "$2" && eval $2=\(\"\${@:3:${1#-a}}\"\) && shift $((${1#-a} + 2)) || return 1
                ;;
            -v)
                unset -v "$2" && eval $2=\"\$3\" && shift 3 || return 1
                ;;
        esac
    done
}

_comp_have() { { declare -F "$1" || typeset -f "$1"; } >/dev/null 2>&1; }
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
		{"a function library", func(f *fixture) {
			f.file("etc/bash.bashrc", "[[ -r /usr/share/bash-completion/bash_completion ]] && . /usr/share/bash-completion/bash_completion\n")
			f.file("usr/share/bash-completion/bash_completion", completionSample)
		}, nil},
		{"a profile sources a home-relative file", func(f *fixture) {
			f.file("etc/profile.d/locale.sh", localeSample)
			f.file("home/user/.config/locale.conf", "LANG=C.UTF-8\nOPENAI_API_KEY="+secret+"\n")
		}, []Hit{env("/home/user/.config/locale.conf", "OPENAI_API_KEY")}},
		{"a home's profile sources its own file", func(f *fixture) {
			f.file("home/user/.bashrc", "[ -f ~/.bash_aliases ] && . ~/.bash_aliases\nsource \"${HOME}/.env\"\n")
			f.file("home/user/.bash_aliases", "alias ll='ls -l'\n")
			f.file("home/user/.env", "export CODEX_HOME=/x\n")
			f.file("srv/app/.env", "export GEMINI_API_KEY=x\n")
		}, []Hit{env("/home/user/.env", "CODEX_HOME")}},
		{"a profile.d loop", func(f *fixture) {
			f.file("etc/profile", "for profile in /etc/profile.d/*.sh; do\n\ttest -r \"$profile\" && . \"$profile\"\ndone\nunset profile\n")
			f.file("etc/profile.d/z.sh", "export ANTHROPIC_API_KEY=x\n")
		}, []Hit{env("/etc/profile.d/z.sh", "ANTHROPIC_API_KEY")}},
		{"a source the check cannot resolve", func(f *fixture) {
			f.file("etc/profile.d/dyn.sh", "for f in \"${files[@]}\"; do . \"$f\"; done\n")
			f.file("home/user/.bashrc", ". ./relative.sh\n")
			f.file("root/.profile", "load() { . \"$1\"; }\n")
		}, []Hit{
			{Kind: KindIncomplete, Path: PathEnvSource, Detail: "/etc/profile.d/dyn.sh"},
			{Kind: KindIncomplete, Path: PathEnvSource, Detail: "/root/.profile"},
			{Kind: KindIncomplete, Path: PathEnvSource, Detail: "/home/user/.bashrc"},
		}},
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

// localeSample is in the style of a packaged locale profile: it sources a
// configuration file from the user's XDG or home folder.
const localeSample = `#!/bin/sh
if [ -z "$LANG" ]; then
  if [ -n "$XDG_CONFIG_HOME" ] && [ -r "$XDG_CONFIG_HOME/locale.conf" ]; then
    . "$XDG_CONFIG_HOME/locale.conf"
  elif [ -n "$HOME" ] && [ -r "$HOME/.config/locale.conf" ]; then
    . "$HOME/.config/locale.conf"
  elif [ -r /etc/locale.conf ]; then
    . /etc/locale.conf
  fi
fi
LANG=${LANG:-C.UTF-8}
export LANG LANGUAGE LC_CTYPE
`

func TestSourcePaths(t *testing.T) {
	c := cleanFixture(t).checker(testLists())
	cases := []struct {
		lit  string
		exp  bool
		home string
		want []string
		ok   bool
	}{
		{"/etc/x", false, "", []string{"etc/x"}, true},
		{"/etc/../etc/x", false, "", []string{"etc/x"}, true},
		{"relative", false, "", nil, false},
		{"$HOME/x", false, "", nil, false}, // a literal "$HOME" folder name is relative
		{"~/x", false, "home/user", []string{"home/user/x"}, true},
		{"~", false, "home/user", []string{"home/user"}, true},
		{"~other/x", false, "home/user", nil, false},
		{"$HOME/x", true, "home/user", []string{"home/user/x"}, true},
		{"${HOME}/.x", true, "srv/app", []string{"srv/app/.x"}, true},
		{"$HOME/x", true, "", []string{"root/x", "home/user/x", "srv/app/x"}, true},
		{"$XDG_CONFIG_HOME/locale.conf", true, "home/user", []string{"home/user/.config/locale.conf"}, true},
		{"${XDG_CONFIG_HOME:-$HOME/.config}/a", true, "home/user", []string{"home/user/.config/a"}, true},
		{"$HOMEDIR/x", true, "home/user", nil, false},
		{"$HOME/$X", true, "home/user", nil, false},
		{"$HOME", true, "home/user", []string{"home/user"}, true},
		{"$HOMEx", true, "home/user", nil, false},
		{"$f", true, "", nil, false},
		{"$(pwd)/x", true, "", nil, false},
		{"~/$X", true, "home/user", nil, false},
	}
	for _, tc := range cases {
		got, ok := c.sourcePaths(srcWord{lit: tc.lit, exp: tc.exp}, tc.home)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sourcePaths(%q, %v, %q) = %q %v, want %q %v", tc.lit, tc.exp, tc.home, got, ok, tc.want, tc.ok)
		}
	}
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

// The specifiers of an EnvironmentFile= are replaced once per manager, never
// multiplied out over the homes, and the expansion and the queue of files
// count against the run's budget: a unit made to exhaust it ends the run
// with "incomplete budget", not a hang or a crash.
func TestEnvExpansionIsBudgeted(t *testing.T) {
	f := cleanFixture(t)
	f.file("etc/systemd/user/many.service", "[Service]\nEnvironmentFile=-"+strings.Repeat("%h/", 64)+"x\n")
	c := f.checker(testLists())
	within(t, c.CheckEnv)
	if r := c.Report(); len(r.Hits) != 0 {
		t.Errorf("hits %v", r.Hits)
	}

	f = cleanFixture(t)
	f.file("etc/systemd/system/big.service", "[Service]\nEnvironmentFile=-/x"+strings.Repeat("%h", 200000)+"\n")
	c = f.checker(testLists())
	c.t.b = newBudget(limits{entries: 1 << 30, bytes: 1 << 30, time: time.Hour, memory: 1 << 20})
	within(t, c.CheckEnv)
	want := Hit{Kind: KindIncomplete, Path: PathBudget, Detail: budgetMemory}
	if r := c.Report(); len(r.Hits) == 0 || r.Hits[len(r.Hits)-1] != want || r.Complete {
		t.Errorf("hits %v", r.Hits)
	}

	f = cleanFixture(t)
	var src strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&src, ". /etc/app/a-file-with-a-long-name-%03d.sh\n", i)
	}
	f.file("etc/profile.d/many.sh", src.String())
	c = f.checker(testLists())
	c.t.b = newBudget(limits{entries: 300, bytes: 1 << 30, time: time.Hour, memory: 1 << 30})
	within(t, c.CheckEnv)
	want = Hit{Kind: KindIncomplete, Path: PathBudget, Detail: budgetEntries}
	if r := c.Report(); len(r.Hits) == 0 || r.Hits[len(r.Hits)-1] != want {
		t.Errorf("hits %v", r.Hits)
	}
	if c.t.b.memory < 0 {
		t.Errorf("memory released twice: %d", c.t.b.memory)
	}
}
