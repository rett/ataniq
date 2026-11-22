package switcher

import (
	"path/filepath"
	"strings"
)

// The command lists of a release, under share/. All four are required. They
// are consistency checks of an honest release, not a privilege boundary: a
// signed release carries code that runs as root anyway.
const (
	ValidatorsList = "share/validators.list"
	ReloadList     = "share/reload.list"
	HealthCmd      = "share/health.cmd"
	StopList       = "share/stop.list"
)

// ReleasePlaceholder in an argument of a validator is replaced by the absolute
// path of the release folder, so that a validator checks the files of the
// release being switched to, not those of the current one. The reload, health
// and stop lists act on what is live (current) and may not use it.
const ReleasePlaceholder = "{release}"

// Bounds of a command list: commands per list and words per command, so
// that a list cannot hold the lock beyond its time limits or fill memory.
const (
	MaxListCommands = 64
	MaxCommandArgs  = 32
)

// shellChars are refused in a listed command: the commands run without a
// shell, so quoting, pipes, redirections and variables would be passed on
// literally, which is never what their author meant.
const shellChars = "|&;<>$`'\"\\*?~(){}"

// ParseCommandList reads one command per line: arguments separated by spaces
// or tabs, no shell. Blank lines and lines starting with # are skipped. The
// program is an absolute path or a bare name (looked up in the runner's fixed
// search path); a relative path with a slash is refused. {release} in any
// argument becomes releaseDir; with releaseDir empty it is refused. A list
// holds at most MaxListCommands commands of at most MaxCommandArgs words.
func ParseCommandList(name string, data []byte, releaseDir string) ([][]string, error) {
	var cmds [][]string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		argv := strings.Fields(line)
		if len(argv) > MaxCommandArgs {
			return nil, failf(CodeListInvalid, "%s line %d: more than %d words", name, i+1, MaxCommandArgs)
		}
		if len(cmds) == MaxListCommands {
			return nil, failf(CodeListInvalid, "%s: more than %d commands", name, MaxListCommands)
		}
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

// lists are the command lists of a release.
type lists struct {
	validators [][]string // run with {release} replaced, before the switch
	reloads    [][]string // run after current is moved
	health     [][]string // each must pass for the release to be healthy
	stops      [][]string // stop the decision services when no healthy release is left
}

// readLists reads and parses the four command lists of a verified release.
// Each must be there. Each must hold at least one command, unless the
// release says in its MANIFEST that it is static (components: static), when
// a list may hold comment lines only.
func (r *release) readLists(static bool) (lists, error) {
	var l lists
	for _, f := range []struct {
		name       string
		releaseDir string
		into       *[][]string
	}{
		{ValidatorsList, r.dir, &l.validators},
		{ReloadList, "", &l.reloads},
		{HealthCmd, "", &l.health},
		{StopList, "", &l.stops},
	} {
		ok, err := r.exists(f.name)
		if err != nil {
			return lists{}, err
		}
		if !ok {
			return lists{}, failf(CodeListMissing, "%s missing: a release names its validators, reloads, health checks and stops", f.name)
		}
		data, err := r.readSmall(f.name)
		if err != nil {
			return lists{}, err
		}
		if *f.into, err = ParseCommandList(f.name, data, f.releaseDir); err != nil {
			return lists{}, err
		}
		if len(*f.into) == 0 && !static {
			return lists{}, failf(CodeListInvalid, "%s holds no command; only a release whose MANIFEST says components: static may leave it empty", f.name)
		}
	}
	return l, nil
}
