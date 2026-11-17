package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/mirror"
)

// check validates the flags every subcommand has: the state root, the
// project name, and that a git executable is known.
func (e *env) check(c common) error {
	if err := need("state", c.state, "project", c.project); err != nil {
		return err
	}
	if err := checkState(c.state); err != nil {
		return err
	}
	if !sandboxplan.ValidName(c.project) {
		return usagef("--project: not a valid name (a lower-case letter or digit, then up to 62 lower-case letters, digits or dashes)")
	}
	if e.deps.Git.Path == "" {
		return errors.New("no git executable found")
	}
	return nil
}

// checkRole checks a role name.
func checkRole(role string) error {
	if role == "" {
		return usagef("--role is required")
	}
	if !mirror.ValidName(role) {
		return usagef("--role: not a valid name (a lower-case letter or digit, then up to 62 lower-case letters, digits or dashes)")
	}
	return nil
}

// branchArg reads a branch name that the owner typed. The name is printed
// escaped by the command, so the owner types it as printed and the command
// unescapes it, to name a branch with odd bytes exactly.
func branchArg(flagName, v string) (string, error) {
	if v == "" {
		return "", usagef("--%s is required", flagName)
	}
	b, err := leadtext.Unescape(v)
	if err != nil {
		return "", usagef("--%s: not a name as printed by this command", flagName)
	}
	if !mirror.ValidBranch(b) {
		return "", usagef("--%s: not a valid branch name", flagName)
	}
	return b, nil
}

// mirrorOf returns the mirror of the project, which init made.
func (e *env) mirrorOf(c common) (*mirror.Mirror, error) {
	path, err := mirror.Path(c.state, c.project)
	if err != nil {
		return nil, usagef("%v", err)
	}
	return mirror.Open(e.ctx, e.deps.Git, path)
}

// refs returns the full names of the lead branch and of the upstream target.
func refs(role, branch, target string) (tipRef, baseRef string, err error) {
	tipRef, err = mirror.LeadRef(role, branch)
	if err != nil {
		return "", "", usagef("--branch: not a valid branch name")
	}
	baseRef, err = mirror.UpstreamRef(target)
	if err != nil {
		return "", "", usagef("--target: not a valid branch name")
	}
	return tipRef, baseRef, nil
}

// exists reports whether a name is present, without following a symlink.
func exists(p string) (bool, error) {
	_, err := os.Lstat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// mirrorsDir returns the folder that holds the mirrors.
func mirrorsDir(state string) string { return filepath.Join(state, "mirrors") }

// sourceOf names the mirror as the repository the intake reads.
func sourceOf(e *env, m *mirror.Mirror) intake.Source {
	return intake.Source{Git: e.deps.Git, GitDir: m.Path()}
}
