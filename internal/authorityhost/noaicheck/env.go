package noaicheck

import (
	"errors"
	"path"
	"strings"
)

// maxEnvFile bounds an environment file, profile or unit file; a larger one
// is a hit of kind "unreadable".
const maxEnvFile = 4 << 20

// envFiles are the system files that set variables for every login or
// service.
var envFiles = []string{
	"etc/environment", "etc/profile", "etc/bash.bashrc", "etc/zsh/zshenv", "etc/zsh/zprofile",
	"etc/zsh/zshrc", "etc/zsh/zlogin", "etc/systemd/system.conf", "etc/systemd/user.conf",
}

// envDirs are folders whose regular files set variables, with how deep each
// is looked into.
var envDirs = []struct {
	rel   string
	depth int
}{
	{"etc/profile.d", 1},
	{"etc/environment.d", 1},
	{"etc/systemd/system.conf.d", 1},
	{"etc/systemd/user.conf.d", 1},
	{"etc/systemd/system", 3},
	{"etc/systemd/user", 3},
	{"usr/lib/systemd/system", 3},
	{"usr/lib/systemd/user", 3},
	{"usr/lib/environment.d", 1},
}

// homeEnvFiles are a home's shell profiles and environment files, and
// homeEnvDirs its folders of them.
var (
	homeEnvFiles = []string{
		".profile", ".bash_profile", ".bash_login", ".bashrc", ".zshenv", ".zprofile", ".zshrc",
		".zlogin", ".pam_environment", ".config/fish/config.fish",
	}
	homeEnvDirs = []struct {
		rel   string
		depth int
	}{
		{".config/environment.d", 1},
		{".config/systemd/user", 3},
		{".config/fish/conf.d", 1},
	}
)

// ProviderKeyName reports whether name is named like a provider key:
// *_API_KEY, *_AUTH_TOKEN, ANTHROPIC_*, OPENAI_*, GEMINI_*, CLAUDE_* or
// CODEX_*, in any case.
func ProviderKeyName(name string) bool {
	n := strings.ToUpper(name)
	if strings.HasSuffix(n, "_API_KEY") || strings.HasSuffix(n, "_AUTH_TOKEN") {
		return true
	}
	for _, p := range []string{"ANTHROPIC_", "OPENAI_", "GEMINI_", "CLAUDE_", "CODEX_"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// CheckEnv is check 4: an environment file, shell profile, systemd manager
// configuration or unit file that sets or passes a variable named like a
// provider key (see ProviderKeyName) is a hit of kind "env", with the
// variable's name as its detail; the value is never read into the report.
// The files are those of /etc/environment, /etc/profile and the shells'
// system profiles, /etc/profile.d, the environment.d folders, the systemd
// manager configuration, every unit file and drop-in under /etc/systemd and
// /usr/lib/systemd, every home's profiles and user units, and each file an
// EnvironmentFile= line of those names.
func (c *Checker) CheckEnv() {
	done := map[string]bool{}
	var queue []string
	scan := func(rel string) {
		p, data, err := c.t.readFile(rel, maxEnvFile)
		if errors.Is(err, errNotRegular) && p == "dev/null" {
			return // a masked file
		}
		if err != nil {
			c.unreadable(rel, err)
			return
		}
		if done[p] {
			return
		}
		done[p] = true
		names, files := envNames(string(data))
		for _, n := range names {
			c.hit(KindEnv, show(p), n)
		}
		for _, f := range files {
			queue = append(queue, clean(f))
		}
	}
	scanDir := func(rel string, depth int) {
		c.walk(rel, depth, func(e *wentry) bool {
			if e.isRegular() {
				scan(e.rel)
			}
			return true
		})
	}
	for _, f := range envFiles {
		scan(f)
	}
	for _, d := range envDirs {
		scanDir(d.rel, d.depth)
	}
	for _, home := range c.homeFolders() {
		for _, f := range homeEnvFiles {
			scan(path.Join(home, f))
		}
		for _, d := range homeEnvDirs {
			scanDir(path.Join(home, d.rel), d.depth)
		}
	}
	// An EnvironmentFile= may name another; done ends a cycle.
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		scan(f)
	}
}

// envNames returns the provider-key names a file sets or passes, once each,
// and the absolute paths its EnvironmentFile= lines name. It reads:
// assignments (NAME=, NAME+=) anywhere on a line, as in /etc/environment,
// a shell profile or a unit's Environment= and DefaultEnvironment=; the
// names after export, declare, typeset or readonly; the first name after
// setenv (csh) or after set's options (fish); and the names of
// PassEnvironment=. Lines starting with '#' or ';' are comments.
func envNames(data string) (names, envFiles []string) {
	seen := map[string]bool{}
	add := func(n string) {
		if ProviderKeyName(n) && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		for _, n := range assignedNames(line) {
			add(n)
		}
		fields := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '"' || r == '\'' || r == ';'
		})
		if len(fields) == 0 {
			continue
		}
		key, value, isKey := strings.Cut(fields[0], "=")
		switch {
		case fields[0] == "export" || fields[0] == "declare" || fields[0] == "typeset" || fields[0] == "readonly":
			for _, f := range fields[1:] {
				if !strings.HasPrefix(f, "-") {
					n, _, _ := strings.Cut(f, "=")
					add(n)
				}
			}
		case fields[0] == "setenv" || fields[0] == "set":
			for _, f := range fields[1:] {
				if !strings.HasPrefix(f, "-") {
					add(f)
					break
				}
			}
		case isKey && strings.TrimSpace(key) == "PassEnvironment":
			add(value)
			for _, f := range fields[1:] {
				add(f)
			}
		case isKey && strings.TrimSpace(key) == "EnvironmentFile":
			f := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "EnvironmentFile=")), "-")
			if strings.HasPrefix(f, "/") {
				envFiles = append(envFiles, f)
			}
		}
	}
	return names, envFiles
}

// assignedNames returns every NAME of a NAME= or NAME+= on the line, where
// NAME is a shell identifier not preceded by an identifier byte or '$'.
func assignedNames(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		if !identStart(line[i]) || (i > 0 && (ident(line[i-1]) || line[i-1] == '$')) {
			i++
			continue
		}
		j := i + 1
		for j < len(line) && ident(line[j]) {
			j++
		}
		if j < len(line) && (line[j] == '=' || (line[j] == '+' && j+1 < len(line) && line[j+1] == '=')) {
			out = append(out, line[i:j])
		}
		i = j
	}
	return out
}

func identStart(b byte) bool { return b == '_' || (b|0x20 >= 'a' && b|0x20 <= 'z') }

func ident(b byte) bool { return identStart(b) || (b >= '0' && b <= '9') }
