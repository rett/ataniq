package switcher

import (
	"path/filepath"
	"strings"
)

// The command lists of a release, under share/.
const (
	ValidatorsList = "share/validators.list"
	ReloadList     = "share/reload.list"
	HealthCmd      = "share/health.cmd"
)

// ReleasePlaceholder in an argument of a validator is replaced by the absolute
// path of the release folder, so that a validator checks the files of the
// release being switched to, not those of the current one. The reload list and
// the health command act on what is live (current) and may not use it.
const ReleasePlaceholder = "{release}"

// shellChars are refused in a listed command: the commands run without a
// shell, so quoting, pipes, redirections and variables would be passed on
// literally, which is never what their author meant.
const shellChars = "|&;<>$`'\"\\*?~(){}"

// ParseCommandList reads one command per line: arguments separated by spaces
// or tabs, no shell. Blank lines and lines starting with # are skipped. The
// program is an absolute path or a bare name (looked up in the runner's fixed
// search path); a relative path with a slash is refused. {release} in any
// argument becomes releaseDir; with releaseDir empty it is refused.
func ParseCommandList(name string, data []byte, releaseDir string) ([][]string, error) {
	var cmds [][]string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		argv := strings.Fields(line)
		for j, arg := range argv {
			if releaseDir == "" && strings.Contains(arg, ReleasePlaceholder) {
				return nil, failf(CodeListInvalid, "%s line %d: %s is only for validators; this list acts on what current points at", name, i+1, ReleasePlaceholder)
			}
			arg = strings.ReplaceAll(arg, ReleasePlaceholder, releaseDir)
			if strings.ContainsAny(strings.ReplaceAll(argv[j], ReleasePlaceholder, ""), shellChars) {
				return nil, failf(CodeListInvalid, "%s line %d: %s: no shell runs these commands, so quoting, pipes, redirections, globs and variables are refused", name, i+1, q(argv[j]))
			}
			argv[j] = arg
		}
		prog := argv[0]
		if !filepath.IsAbs(prog) && strings.Contains(prog, "/") {
			return nil, failf(CodeListInvalid, "%s line %d: program %s is a relative path; give an absolute path, {release}/..., or a bare name", name, i+1, q(prog))
		}
		cmds = append(cmds, argv)
	}
	return cmds, nil
}

// ParseHealthCmd reads share/health.cmd, which holds exactly one command.
func ParseHealthCmd(data []byte) ([]string, error) {
	cmds, err := ParseCommandList(HealthCmd, data, "")
	if err != nil {
		return nil, err
	}
	if len(cmds) != 1 {
		return nil, failf(CodeListInvalid, "%s holds %d commands; it must hold exactly one", HealthCmd, len(cmds))
	}
	return cmds[0], nil
}

// readLists reads and parses the command lists of a verified release. The
// validators and reload lists may be absent; the health command may not.
func (r *release) readLists() (validators, reloads [][]string, health []string, err error) {
	read := func(name string, required bool) ([]byte, bool, error) {
		ok, err := r.exists(name)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			if required {
				return nil, false, failf(CodeListMissing, "%s missing: a release must name its health check", name)
			}
			return nil, false, nil
		}
		data, err := r.readSmall(name)
		return data, err == nil, err
	}
	data, ok, err := read(ValidatorsList, false)
	if err != nil {
		return nil, nil, nil, err
	}
	if ok {
		if validators, err = ParseCommandList(ValidatorsList, data, r.dir); err != nil {
			return nil, nil, nil, err
		}
	}
	data, ok, err = read(ReloadList, false)
	if err != nil {
		return nil, nil, nil, err
	}
	if ok {
		if reloads, err = ParseCommandList(ReloadList, data, ""); err != nil {
			return nil, nil, nil, err
		}
	}
	data, _, err = read(HealthCmd, true)
	if err != nil {
		return nil, nil, nil, err
	}
	if health, err = ParseHealthCmd(data); err != nil {
		return nil, nil, nil, err
	}
	return validators, reloads, health, nil
}
