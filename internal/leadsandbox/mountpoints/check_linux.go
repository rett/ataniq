//go:build linux

package mountpoints

import "ataniq/internal/leadsandbox/profile"

// Check is the read-only check of every start. Each mount point must exist
// and have the right type, and no component of its path may be a symbolic
// link. The git metadata folder (profile.Inputs.GitMeta), the source of the
// three read-only .git binds, must exist with its three entries (config a
// regular file, hooks and info folders), no link on its path or among them, and
// the modes Prepare gave them (0700 for folders, 0600 for the file), and its
// info/exclude must list each tree placeholder as one anchored line. It
// returns every problem it finds, not only the first, up to [MaxProblems]. It
// creates and repairs nothing. The error is set only when the inputs are
// refused (a *Problem of kind ErrInvalid).
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
	checkGitMeta(rs, in, placeholdersOf(pts), &rep)
	return rep, nil
}
