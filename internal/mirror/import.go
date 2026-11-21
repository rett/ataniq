package mirror

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"ataniq/internal/exchange/gitenv"
)

// ErrImport is returned by [Mirror.ImportUpstream] when the branch did not
// arrive as a commit under its upstream name.
var ErrImport = errors.New("mirror: the upstream branch was not imported")

// ImportUpstream brings one branch of a local repository into the mirror as the
// upstream branch of the same name: refs/heads/<branch>. It is the owner's
// step to give a new mirror its first upstream branch, the one a lead's tree
// is cloned from. The lead's branches never take this path: [Mirror.Fetch]
// writes the lead's namespace only, and never upstream.
//
// source is the absolute clean path of a repository (bare or not) that the
// owner chose: an owner-only config names it, and no lead or client sends it.
// Unlike the source of a fetch, it is not sandboxed, because it is the owner's
// own repository, and the git it starts runs with the explicit environment of
// [gitenv] and may use the "file" transport only. A source that a lead can
// write must not be named here: use [Mirror.Fetch] for that.
//
// The refspec has no "+": a branch that is not there is created, a branch that
// is there is moved forward and never rewound, and a branch that has diverged
// is an error. Tags are not carried. The mirror's own settings check every
// object that arrives (transfer.fsckObjects). After the fetch the ref must
// resolve to a commit, or the error wraps [ErrImport].
func (m *Mirror) ImportUpstream(ctx context.Context, source, branch string) error {
	if err := checkAbsClean("source", source); err != nil {
		return err
	}
	ref, err := UpstreamRef(branch)
	if err != nil {
		return err
	}
	_, err = m.run(ctx, gitenv.Cmd{
		GitDir:    m.path,
		Protocols: []string{"file"},
		Args: []string{
			"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance",
			"--end-of-options", filepath.Clean(source), ref + ":" + ref,
		},
	}, nil, fetchOutput)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrImport, err)
	}
	got, err := m.Resolve(ctx, ref)
	switch {
	case err != nil:
		return fmt.Errorf("%w: the branch is not in the mirror after the fetch: %w", ErrImport, err)
	case got.Type != "commit":
		return fmt.Errorf("%w: the branch is not a commit", ErrImport)
	}
	return nil
}
