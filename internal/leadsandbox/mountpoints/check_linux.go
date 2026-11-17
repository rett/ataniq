//go:build linux

package mountpoints

import "ataniq/internal/leadsandbox/profile"

// Check is the read-only check of every start: the walk of rule B1 over every
// bind target that lies in a folder the lead writes. It walks the runtime's
// config folder in the home (.claude or .codex), the places of the skills and
// of the instruction file in the config folder, the tree's .git folder itself
// and its config, hooks and info, the tree placeholders, and the place of the
// records when it lies in the home. Each folder they lie in is opened from the
// state root's handle, and each path is looked up beneath that handle without
// following a link, with its type decided on the descriptor the lookup
// returned. A symbolic link, at the path or in a component before it, and a
// wrong type (a file where a folder belongs, a folder where a file belongs, a
// FIFO or another type) are problems that name the path. Each mount point must
// also exist, with the right type, as [Prepare] made it: an absent one is a
// problem too. The tree's .git is no mount point: it may be absent, and its
// three mount points are then the ones reported as missing; when it is refused,
// it is reported once and what lies below it is not looked at.
//
// The git metadata folder (profile.Inputs.GitMeta), the source of the
// three read-only .git binds, must exist with its three entries (config a
// regular file, hooks and info folders), no link on its path or among them, and
// the modes Prepare gave them (0700 for folders, 0600 for the file), and its
// info/exclude must list each tree placeholder as one anchored line. It
// returns every problem it finds, not only the first, up to [MaxProblems]. It
// creates and repairs nothing, and nothing an earlier call found is reused: the
// holder calls it before each start, with the previous instance stopped, across
// epochs and across a change of runtime. The error is set only when the inputs
// are refused (a *Problem of kind ErrInvalid).
func Check(in profile.Inputs) (Report, error) {
	pts, err := List(in)
	if err != nil {
		return Report{}, err
	}
	rs := newRootSet(in)
	defer rs.close()
	var rep Report
	var refused []walked
	for _, w := range walkList(pts) {
		root, prob := rs.get(w.RootDir)
		rep.add(prob)
		if root == nil || w.below(refused) {
			continue
		}
		present, prob := inspect(root, w)
		if prob != nil {
			rep.add(prob)
			if w.holder {
				refused = append(refused, w)
			}
			continue
		}
		if !present && !w.holder {
			rep.add(problem(ErrMissing, w.HostPath, "the mount point does not exist"))
		}
	}
	checkGitMeta(rs, in, placeholdersOf(pts), &rep)
	return rep, nil
}
