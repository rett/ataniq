//go:build linux

package mountpoints

import (
	"ataniq/internal/leadsandbox/profile"
)

// Check is the read-only check of every start. Each mount point must exist
// and have the right type, no component of its path may be a symbolic link,
// and each tree placeholder must be listed in .git/info/exclude. It returns
// every problem it finds, not only the first, up to [MaxProblems]. It creates
// and repairs nothing. The error is set only when the inputs are refused (a
// *Problem of kind ErrInvalid).
func Check(in profile.Inputs) (Report, error) {
	pts, err := List(in)
	if err != nil {
		return Report{}, err
	}
	rs := newRootSet(in)
	defer rs.close()
	var rep Report
	for _, p := range pts {
		root, prob := rs.get(p.RootDir)
		rep.add(prob)
		if root == nil {
			continue
		}
		present, prob := inspect(root, p)
		if prob != nil {
			rep.add(prob)
			continue
		}
		if !present {
			rep.add(problem(ErrMissing, p.HostPath, "the mount point does not exist"))
		}
	}
	checkExclude(rs, in.Layout().Tree(), pts, &rep)
	return rep, nil
}

// checkExclude checks that every tree placeholder is listed.
func checkExclude(rs *rootSet, treeDir string, pts []Point, rep *Report) {
	var ph []Point
	for _, p := range pts {
		if p.Placeholder {
			ph = append(ph, p)
		}
	}
	if len(ph) == 0 {
		return
	}
	tree, _ := rs.get(treeDir)
	if tree == nil {
		return // the folder is already reported
	}
	data, exists, prob := readExclude(tree, treeDir)
	if prob != nil {
		rep.add(prob)
		return
	}
	for _, p := range ph {
		switch {
		case !exists:
			rep.add(problem(ErrNotExcluded, p.HostPath, "%s does not exist", excludeRel))
		case !listedIn(data, p.Rel):
			rep.add(problem(ErrNotExcluded, p.HostPath, "%s has no line %s", excludeRel, excludeLine(p.Rel)))
		}
	}
}
