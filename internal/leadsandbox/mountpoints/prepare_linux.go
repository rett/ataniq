//go:build linux

package mountpoints

import (
	"path"
	"strings"

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

// Prepare makes the mount points before the lead's first run, outside any
// sandbox. For each mount point, through a [safefile.Root] of the folder it
// lies in, it makes the folder (MkdirAll) or the empty file (created
// exclusively, its parent made first) when it is absent; an existing entry of
// the right type is kept; a symbolic link, at the mount point or in any
// component before it, or a wrong type is refused. Each tree placeholder
// outside .git is added to .git/info/exclude as one anchored line, when not
// already listed (the file is created when absent).
//
// .git/config, .git/hooks and .git/info must already exist, as a regular file
// and two folders, as a clone has them: a missing one is refused and never
// invented, except .git/hooks, which git may omit and which is made empty.
// The folders the mount points lie in (home, config folder, tree, records)
// must exist too.
//
// Everything is looked at before anything is written, so a refusal found while
// looking leaves the folders as they were. An error while writing (permission,
// space) can leave the earlier mount points made; a repeated Prepare, still
// before the first run, completes them. Prepare is idempotent: a call after a
// complete one changes nothing.
// The error is a *Problem (errors.Is with the Err kinds); the first problem
// found is returned.
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
	var ph []Point
	for _, p := range pts {
		if p.Placeholder {
			ph = append(ph, p)
		}
	}
	var (
		tree         *safefile.Root
		exData       []byte
		exExists     bool
		needed       []string
		hasPlacehold = len(ph) > 0
	)
	if hasPlacehold {
		tree, _ = rs.get(treeDir)
		if tree == nil {
			return problem(ErrMissing, treeDir, "the tree folder is not usable")
		}
		var prob *Problem
		exData, exExists, prob = readExclude(tree, treeDir)
		if prob != nil {
			return prob
		}
		for _, p := range ph {
			if !listedIn(exData, p.Rel) {
				needed = append(needed, excludeLine(p.Rel))
			}
		}
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
	if len(needed) == 0 {
		return nil
	}
	add := strings.Join(needed, "\n") + "\n"
	exPath := treeDir + "/" + excludeRel
	if !exExists {
		if err := tree.WriteFileExclusive(excludeRel, []byte(add), fileMode); err != nil {
			return fromErr(exPath, err)
		}
		return nil
	}
	if len(exData) > 0 && exData[len(exData)-1] != '\n' {
		add = "\n" + add
	}
	if err := tree.AppendFile(excludeRel, []byte(add)); err != nil {
		return fromErr(exPath, err)
	}
	return nil
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
