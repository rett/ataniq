//go:build linux

package mountpoints

import (
	"path"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// PrepareOptions carries what the caller must state to [Prepare].
type PrepareOptions struct {
	// NoLeadHasRun is the caller's statement that no lead has run in these
	// folders yet. Prepare refuses without it (ErrNoStatement): nothing is
	// written into a folder a lead has run in (the lead-hosting design,
	// section 3.6). The package cannot tell; the caller must know.
	NoLeadHasRun bool
}

// Prepare makes the mount points and fills the git metadata folder before the
// lead's first run, outside any sandbox.
//
// Mount points. For each one, through a [safefile.Root] of the folder it lies
// in, it makes the folder (MkdirAll) or the empty file (created exclusively,
// its parent made first) when it is absent; an existing entry of the right
// type is kept; a symbolic link, at the mount point or in any component before
// it, or a wrong type is refused. .git/config, .git/hooks and .git/info of the
// tree must already exist, as a regular file and two folders, as a clone has
// them: a missing one is refused and never invented, except .git/hooks, which
// git may omit and which is made empty. The folders the mount points lie in
// (home, config folder, tree, records) must exist too. These mount points are
// only the targets of the .git binds: what is bound over them comes from the
// git metadata folder.
//
// The git metadata folder (profile.Inputs.GitMeta) is Ataniq's copy of the
// tree's protected git files, and the source of the three read-only .git
// binds (the lead-hosting design, section 3.3; rule G1: no bind source lies in
// a folder the lead can write). When it is absent, Prepare builds it in a
// temporary sibling in the lead's state folder (the name of the folder and
// ".tmp"), exclusively, mode 0700, filled with files of mode 0600; syncs each
// file and each folder; and moves it into its place with renameat2 and
// RENAME_NOREPLACE, then syncs the folder that holds it. The place never holds
// a folder that is only partly written, even after an error part-way or a
// crash. The folder holds:
//
//   - config: a copy of the clone's .git/config, read through the tree's
//     handle, a regular file of at most 1 MiB that is not hard-linked. A config
//     with an include or includeIf section (any case; a bounded scan of the
//     section headers, no other key is interpreted) is refused with ErrInclude,
//     because git would read the file it names as configuration;
//   - hooks/: empty. The clone's own hooks are NOT copied: a managed lead's
//     tree has no hooks;
//   - info/: a copy of each regular file directly in the clone's .git/info
//     (at most 64 entries, 1 MiB each and 4 MiB in all; a link, a folder or
//     another type is skipped, a hard-linked file is refused). Each tree
//     placeholder outside .git is listed in info/exclude as one anchored line,
//     in this copy, which is what git inside the sandbox reads; the file is
//     made when the clone has none. The clone's own .git/info/exclude is not
//     changed.
//
// When the folder already exists (an earlier Prepare made it), it is kept and
// only checked, as [Check] checks it: nothing in it is written again, so a
// placeholder added to the inputs after the first Prepare is reported as not
// excluded. A temporary sibling that an earlier Prepare left (it failed or was
// interrupted before the move) is never reused: Prepare refuses it with
// ErrIncomplete and a reason that names it, whether the folder is in place or
// not. Remove the leftover (nothing in it is the lead's) and call Prepare again
// before the first run.
//
// Everything is looked at before anything is written, so a refusal found while
// looking leaves the folders as they were. An error while writing (permission,
// space) can leave the earlier mount points made; a repeated Prepare, still
// before the first run, completes the mount points. Prepare is idempotent: a
// call after a complete one changes nothing. The error is a *Problem
// (errors.Is with the Err kinds); the first problem found is returned.
func Prepare(in profile.Inputs, opts PrepareOptions) error {
	if !opts.NoLeadHasRun {
		return problem(ErrNoStatement, "", "nothing is written into a folder a lead has run in")
	}
	pts, err := List(in)
	if err != nil {
		return err
	}
	rs := newRootSet(in)
	defer rs.close()
	treeDir := in.Layout().Tree()

	// Look first.
	present := make([]bool, len(pts))
	roots := make([]*safefile.Root, len(pts))
	for i, p := range pts {
		root, prob := rs.get(p.RootDir)
		if prob != nil {
			return prob
		}
		roots[i] = root
		ok, prob := inspect(root, p)
		if prob != nil {
			return prob
		}
		if !ok && p.Git && p.Rel != ".git/hooks" {
			return problem(ErrMissing, p.HostPath, "a clone has it: it is never invented")
		}
		present[i] = ok
	}
	parent, copyOf, prob := lookGitMeta(rs, in, treeDir, placeholdersOf(pts))
	if prob != nil {
		return prob
	}

	// Then write.
	for i, p := range pts {
		if present[i] {
			continue
		}
		if err := create(roots[i], p); err != nil {
			return fromErr(p.HostPath, err)
		}
	}
	if copyOf == nil {
		return nil
	}
	if prob := writeGitMeta(parent, in, copyOf); prob != nil {
		return prob
	}
	return nil
}

// lookGitMeta looks at the git metadata folder before Prepare writes. When
// the folder is absent it reads what it is made from and returns the folder
// that will hold it and the copy; when it exists it checks it and returns a
// nil copy; a problem is returned as the first one found.
func lookGitMeta(rs *rootSet, in profile.Inputs, treeDir string, placeholders []Point) (*safefile.Root, *metaCopy, *Problem) {
	parent, prob := rs.get(path.Dir(in.GitMeta))
	if prob != nil {
		return nil, nil, prob
	}
	if parent == nil {
		return nil, nil, problem(ErrUnreadable, path.Dir(in.GitMeta), "the folder that holds the git metadata folder is not usable")
	}
	// A temporary sibling that is still there is what a Prepare that failed
	// or was interrupted left. It is never reused or removed here: what
	// is in it is not known to be complete, and nothing in it is the lead's.
	tmp := path.Join(path.Dir(in.GitMeta), gitMetaTmp(in.GitMeta))
	switch _, err := parent.Lstat(gitMetaTmp(in.GitMeta)); {
	case err == nil:
		return nil, nil, problem(ErrIncomplete, tmp, "an earlier Prepare left this folder, built and not moved into place (it failed or was interrupted): nothing in it is the lead's and it is not reused; remove it and call Prepare again before the first run")
	case !isNotExist(err):
		return nil, nil, fromErr(tmp, err)
	}
	_, err := parent.Lstat(path.Base(in.GitMeta))
	switch {
	case err == nil:
		var rep Report
		checkGitMeta(rs, in, placeholders, &rep)
		if len(rep.Problems) > 0 {
			return nil, nil, rep.Problems[0]
		}
		return nil, nil, nil
	case !isNotExist(err):
		return nil, nil, fromErr(in.GitMeta, err)
	}
	tree, prob := rs.get(treeDir)
	if prob != nil {
		return nil, nil, prob
	}
	if tree == nil {
		return nil, nil, problem(ErrMissing, treeDir, "the tree folder is not usable")
	}
	m, prob := readCloneMeta(tree, treeDir, placeholders)
	if prob != nil {
		return nil, nil, prob
	}
	return parent, m, nil
}

// create makes one absent mount point.
func create(root *safefile.Root, p Point) error {
	if p.Dir {
		return root.MkdirAll(p.Rel, dirMode)
	}
	if parent := path.Dir(p.Rel); parent != "." {
		if err := root.MkdirAll(parent, dirMode); err != nil {
			return err
		}
	}
	return root.WriteFileExclusive(p.Rel, nil, fileMode)
}
