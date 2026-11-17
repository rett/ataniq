package leadmirror

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hookNames are the names of the git hooks that a lead's .git/hooks folder
// may hold. The first fourteen are the names of git's sample hooks; the rest
// are the other hooks that git documents. The harness writes one script for
// each name, and each script writes a canary file.
var hookNames = []string{
	"applypatch-msg", "commit-msg", "fsmonitor-watchman", "post-update",
	"pre-applypatch", "pre-commit", "pre-merge-commit", "pre-push",
	"pre-rebase", "pre-receive", "prepare-commit-msg", "push-to-checkout",
	"sendemail-validate", "update",
	"post-applypatch", "post-checkout", "post-commit", "post-index-change",
	"post-merge", "post-receive", "post-rewrite", "pre-auto-gc",
	"proc-receive", "reference-transaction",
}

// configCanaries are the canary files of the settings planted in .git/config.
var configCanaries = []string{
	"config-fsmonitor", "config-packobjectshook", "config-alternaterefscommand",
}

// leadHooksFolder is the folder, below .git, that core.hooksPath names.
const leadHooksFolder = "lead-hooks"

// canaryNames returns every canary file name that plantHostile makes a
// command write, sorted. The names are the harness's own.
func canaryNames() []string {
	var names []string
	for _, h := range hookNames {
		names = append(names, "hook-"+h, "hookspath-"+h)
	}
	names = append(names, configCanaries...)
	sort.Strings(names)
	return names
}

// plantHostile makes the lead's clone at tree hostile. It writes, as plain
// files and without running git:
//   - a hook of every name in hookNames in .git/hooks, and one in
//     .git/lead-hooks;
//   - in .git/config, core.fsmonitor, core.hooksPath,
//     uploadpack.packObjectsHook and core.alternateRefsCommand.
//
// Every hook and command writes a file in the canary folder, a folder on the
// host that the sandbox is not meant to reach. inside is the path of the clone
// as the serving half sees it; core.hooksPath names it, so that the setting
// points at the planted folder from the inside too. The canary folder and the
// tree path hold only plain characters, so no quoting is needed beyond the
// single quotes used here.
func plantHostile(tree, canary, inside string) error {
	if !plainPath(canary) || !plainPath(inside) {
		return fmt.Errorf("leadmirror: the canary or inside path holds a character that is not plain")
	}
	gitDir := filepath.Join(tree, ".git")
	for _, dir := range []string{filepath.Join(gitDir, "hooks"), filepath.Join(gitDir, leadHooksFolder)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for _, h := range hookNames {
		for _, f := range []struct{ dir, prefix string }{
			{filepath.Join(gitDir, "hooks"), "hook-"},
			{filepath.Join(gitDir, leadHooksFolder), "hookspath-"},
		} {
			script := "#!/bin/sh\n: > '" + filepath.Join(canary, f.prefix+h) + "'\nexit 0\n"
			if err := os.WriteFile(filepath.Join(f.dir, h), []byte(script), 0o700); err != nil {
				return err
			}
			// The mode of WriteFile is changed by the umask; make the hook
			// executable on purpose.
			if err := os.Chmod(filepath.Join(f.dir, h), 0o700); err != nil {
				return err
			}
		}
	}
	cfg := strings.Join([]string{
		"[core]",
		"\tfsmonitor = \": > '" + filepath.Join(canary, "config-fsmonitor") + "'; exit 1\"",
		"\thooksPath = " + inside + "/.git/" + leadHooksFolder,
		"\talternateRefsCommand = \": > '" + filepath.Join(canary, "config-alternaterefscommand") + "'\"",
		"[uploadpack]",
		"\tpackObjectsHook = \": > '" + filepath.Join(canary, "config-packobjectshook") + "'; exec\"",
		"",
	}, "\n")
	f, err := os.OpenFile(filepath.Join(gitDir, "config"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(cfg)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
