//go:build linux

package mountpoints

import (
	"path"
	"strings"

	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
)

// SourceWalk says how the source of one bind row that Ataniq supplies is
// reached by handle (rule B1 of the design, revision 4, section 3.6): the
// folder to open by name, the path to walk beneath it, and the type the source
// must have. It is data only: it names paths, holds no descriptor and opens
// nothing. It is for the code that does a walk: [CheckBindSources] here, which
// takes the type and the place of the source from it, and the pass-through
// step of the launch plan (the probe's pass-fds mode, the last host-side code
// before bubblewrap), which opens Root without following a link in any
// component of its path, walks Rel beneath that handle with openat2
// (RESOLVE_BENEATH and RESOLVE_NO_SYMLINKS), checks the type on the descriptor
// it got, and gives bubblewrap that descriptor, so that the object checked is
// the object mounted. The plan takes the walks as data ([PlanWalks] gives them
// in its type) and writes them into the step; nothing in this package does
// that last step, and whether bubblewrap mounts the object behind the
// descriptor is a qualification item.
//
// Every row of the bind table that has a source and is not a row of the host
// (profile.RoleHost) has a walk; a row of the host (/usr, the /etc files) has
// none, because its source is the host's own path, which no lead writes and
// which may go through the host's links.
//
// Root is chosen from the inputs alone:
//
//   - for a source below the state root (the folders the lead writes, the
//     entries of the git metadata folder, the mirror, the mailbox, the
//     published folder, and any source the caller placed there), Root is the
//     state root and Rel the whole path below it, so that every component
//     between the state root and the source is walked without a link.
//     BelowState is set;
//   - for every other source (the skills, the instruction file, the git
//     identity folder, the assignment, the projection, the managed settings,
//     the probe, the spec folder, resolv.conf, the agent and Qargi installs, the
//     sources of the tree placeholders), Root is the source's own parent
//     folder and Rel its last element. The inputs name no folder above such a
//     source, so the parent is the nearest folder they give; the caller's
//     obligation that a source and everything above it are Ataniq's (see "What
//     the holder must do") is what makes it a folder no sandbox can write. The
//     package cannot check that.
//
// Root is opened by name: the walk protects the components of Rel, and Root
// and what lies above it are the caller's to keep. Both walks refuse a link
// above Root at the moment they are made, and nothing more than a link:
// [CheckBindSources] looks from the root of the file system (for a source
// outside the state root it does not open Root at all, and looks the whole
// path up from there), and the pass-through step opens Root itself in one
// lookup from the root of the file system that follows none. The two differ
// in one place: beneath Root the step crosses no mount point, for any row,
// while [CheckBindSources] crosses none below the state root only. So a source
// outside the state root that is itself a mount point passes the check here
// and is refused by the step: the holder keeps every source on the mount of
// the folder that holds it.
type SourceWalk struct {
	// Inside is the inside path of the row.
	Inside string
	// Root is the host folder to open by name: absolute and clean.
	Root string
	// Rel is the path of the source beneath Root: relative and clean, without
	// "..", and "." only when the source is Root itself.
	Rel string
	// Dir says the source must be a folder; else it must be a regular file.
	Dir bool
	// BelowState says the source lies below the state root, and Root is the
	// state root.
	BelowState bool
}

// Path is the host path of the source: Root joined with Rel. It equals the
// source the bind table names for the row.
func (w SourceWalk) Path() string {
	switch {
	case w.Rel == ".":
		return w.Root
	case w.Root == "/":
		return "/" + w.Rel
	}
	return w.Root + "/" + w.Rel
}

// SourceWalks validates the inputs and returns the walk of every bind row that
// Ataniq supplies, in the order of the bind table. It reads nothing from the
// file system: the result depends on the inputs alone, so a caller in another
// process gets the same walks from the same inputs. The error is a *Problem of
// kind ErrInvalid.
func SourceWalks(in profile.Inputs) ([]SourceWalk, error) {
	t, err := in.Binds()
	if err != nil {
		p := problem(ErrInvalid, "", "%v", err)
		p.Err = err
		return nil, p
	}
	var out []SourceWalk
	for _, b := range t {
		if w, ok := sourceWalk(in.StateRoot, b); ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// sourceWalk is the walk of one row of the bind table, and false for a row
// without a source and for a row of the host.
func sourceWalk(stateRoot string, b profile.Bind) (SourceWalk, bool) {
	if b.Source == "" || b.SourceRole == profile.RoleHost {
		return SourceWalk{}, false
	}
	w := SourceWalk{Inside: b.Inside, Dir: b.IsDir}
	switch rel, below := strings.CutPrefix(b.Source, stateRoot+"/"); {
	case below && rel != "":
		w.Root, w.Rel, w.BelowState = stateRoot, rel, true
	case b.Source == "/":
		w.Root, w.Rel = "/", "."
	default:
		w.Root, w.Rel = path.Dir(b.Source), path.Base(b.Source)
	}
	return w, true
}

// PlanWalks is [SourceWalks] in the type the plan takes
// (plan.Options.SourceWalks): the walk of every bind row that Ataniq supplies,
// in the order of the bind table, for the pass-through step to make. A caller
// builds the plan's walks with it and never by hand: the walk the step makes
// is then the walk [CheckBindSources] made on the same rows just before, from
// the same inputs. The error is a *Problem of kind ErrInvalid.
func PlanWalks(in profile.Inputs) ([]plan.SourceWalk, error) {
	walks, err := SourceWalks(in)
	if err != nil {
		return nil, err
	}
	out := make([]plan.SourceWalk, len(walks))
	for i, w := range walks {
		out[i] = plan.SourceWalk{Inside: w.Inside, Root: w.Root, Rel: w.Rel, Dir: w.Dir}
	}
	return out, nil
}
