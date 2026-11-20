package assert

import (
	"fmt"

	"ataniq/internal/leadsandbox/profile"
)

// HostFacts are the parts of a spec that the profile inputs do not hold: what
// the holder knows about its own host.
type HostFacts struct {
	// HolderPaths are the holder's sockets and state, as they would appear
	// inside if they were mounted.
	HolderPaths []string
	// OwnerPaths are further paths of the owner that must not exist inside.
	// The inputs' never-mounted paths (the owner's checkout, the real
	// provider folders, .ssh) are added to them.
	OwnerPaths []string
	// MountSubstrings must not appear in the mount table. The never-mounted
	// paths and the holder paths are added to them, so a bind that leaks one
	// shows in the table of the sandbox, where a bind source is its root.
	MountSubstrings []string
	// Identities are the identities of the owner and holder paths that exist
	// and of their ancestors, as the holder saw them outside: the output of
	// CollectIdentities on the spec's owner and holder paths. Without them
	// assertion 1 is not-checked and no start is accepted.
	Identities []FileIdentity
	// CoordinatorPaths are further folders of the coordinators that must not
	// be visible inside. The two roots of the layout, <state>/coords and
	// <state>/coord-login, are always in the spec and come first.
	CoordinatorPaths []string
	// CoordinatorIdentities are the identities of the coordinators' folders
	// that exist and of their ancestors, as the holder saw them outside: the
	// output of CollectIdentities on CoordinatorIdentityPaths and on the
	// folders CoordinatorFolders finds, at most MaxCoordinatorIdentities.
	// Without them assertion 11 is not-checked and no start is accepted.
	CoordinatorIdentities []FileIdentity
	// LoopbackTCP, LoopbackTCP2 and AbstractSocket are the canary's addresses
	// (Canary.TCP, Canary.TCP2, which is empty when the host has no IPv6
	// loopback, and Canary.Abstract).
	LoopbackTCP    string
	LoopbackTCP2   string
	AbstractSocket string
	// ProviderHost is the provider host name that must resolve.
	ProviderHost string
	// ManagedMarker is the text the managed-settings file must contain:
	// required for claude.
	ManagedMarker string
	// Optional, ConnectTimeoutMS and ResolveTimeoutMS go to the spec as they
	// are.
	Optional         []int
	ConnectTimeoutMS int
	ResolveTimeoutMS int
}

// NewSpec builds the spec for a profile's inputs, the environment the process
// inside is expected to have and the host's facts: the digest, the runtime,
// the other runtime's config folder inside, the tree and its three read-only
// git paths, the managed-settings path and whether a seccomp filter is
// expected come from the inputs. The result is validated. The owner and holder
// paths whose identities the caller collected are the spec's OwnerPaths and
// HolderPaths: collect them from the same lists (see CollectIdentities). The
// coordinators' paths are the two roots of the inputs' layout
// (profile.Layout.CoordsDir and CoordLoginDir) and the facts' own.
//
// The expected environment is the caller's, not derived from the inputs
// alone: bubblewrap keeps and sets PWD, from --chdir, even with --clearenv, so
// the environment inside is the profile's allowlist plus PWD, and a spec made
// from the allowlist alone would fail assertion 2 on a start that is as the
// plan says. The caller has the plan and passes its inside environment (the
// allowlist and PWD); the order is kept and a variable the plan sets must be
// listed with its value, as assertion 2 compares the names and values
// exactly.
func NewSpec(in profile.Inputs, env []profile.EnvVar, h HostFacts) (Spec, error) {
	digest, err := in.Digest()
	if err != nil {
		return Spec{}, err
	}
	s := Spec{
		Version:         SpecVersion,
		Digest:          digest,
		Runtime:         in.Runtime,
		HolderPaths:     append([]string(nil), h.HolderPaths...),
		OwnerPaths:      dedupe(in.NeverMounted, h.OwnerPaths),
		MountSubstrings: dedupe(h.MountSubstrings, in.NeverMounted, h.HolderPaths),

		ForbiddenIdentities: dedupeIdentities(h.Identities),
		OtherConfigDir:      profile.ConfigInside(in.Runtime.Other()),

		CoordinatorPaths:      coordinatorPaths(in, h),
		CoordinatorIdentities: dedupeIdentities(h.CoordinatorIdentities),

		LoopbackTCP:      h.LoopbackTCP,
		LoopbackTCP2:     h.LoopbackTCP2,
		AbstractSocket:   h.AbstractSocket,
		ProviderHost:     h.ProviderHost,
		SeccompExpected:  in.SeccompEnabled(),
		Tree:             profile.TreeInside(in.Project),
		ManagedSettings:  in.ManagedInside(),
		ManagedMarker:    h.ManagedMarker,
		Optional:         append([]int(nil), h.Optional...),
		ConnectTimeoutMS: h.ConnectTimeoutMS,
		ResolveTimeoutMS: h.ResolveTimeoutMS,
	}
	for _, e := range env {
		s.Env = append(s.Env, EnvEntry{Name: e.Name, Value: e.Value})
	}
	for _, rel := range profile.GitReadOnlyRel() {
		s.GitReadOnly = append(s.GitReadOnly, s.Tree+"/"+rel)
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// dedupe joins the lists, first occurrence first, without repeats.
func dedupe(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, v := range l {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// dedupeIdentities returns the list without repeats, in order.
func dedupeIdentities(list []FileIdentity) []FileIdentity {
	var out []FileIdentity
	seen := map[FileIdentity]bool{}
	for _, id := range list {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// IdentityPaths are the paths whose identities and whose ancestors' the holder
// collects with CollectIdentities for the spec NewSpec makes from the same
// inputs and facts: the owner's paths (the inputs' never-mounted paths and the
// facts') and the holder's.
func IdentityPaths(in profile.Inputs, h HostFacts) []string {
	return dedupe(in.NeverMounted, h.OwnerPaths, h.HolderPaths)
}

// coordinatorPaths are the coordinators' paths of the spec: the two roots of
// the layout, then the facts' own.
func coordinatorPaths(in profile.Inputs, h HostFacts) []string {
	l := in.Layout()
	return dedupe([]string{l.CoordsDir(), l.CoordLoginDir()}, h.CoordinatorPaths)
}

// CoordinatorIdentityPaths are the coordinators' paths of the spec NewSpec
// makes from the same inputs and facts: the paths whose identities and whose
// ancestors' the holder collects with CollectIdentities for
// HostFacts.CoordinatorIdentities. The roots alone do not name a single
// coordinator's tree or home: add the folders CoordinatorFolders finds.
func CoordinatorIdentityPaths(in profile.Inputs, h HostFacts) []string {
	return coordinatorPaths(in, h)
}

// maxCoordinatorDirEntries is the most entries CoordinatorFolders reads from
// one folder.
const maxCoordinatorDirEntries = MaxCoordinatorIdentities

// CoordinatorFolders is the holder's helper for the coordinators' identities:
// it lists, outside the sandbox, the folders that exist below the two roots of
// the inputs' layout, so that the identity of each can be collected:
//
//	<state>/coords/<project>/<feature>, and its tree and home
//	<state>/coord-login/<project>/<runtime>
//
// A bind of one coordinator's tree or home that is taken from a file system
// not mounted at "/" has a mount root no path matches by text, and the roots'
// identities do not name it; the identity of the folder itself does. names
// reads the names of the entries of a folder, at most limit, as
// [System.DirNames] does ([HostSystem] has it). A root or a folder that does
// not exist, or is not a folder (a link is not followed, so it is not one), is
// passed over. Any other failure is an
// error, and so are a folder with more entries than the limit and more than
// MaxCoordinatorIdentities paths (a spec lists no more identities than that): a
// list that is cut short would make the check look better than it is. The tree and the home of a feature are listed whether or
// not they exist; CollectIdentities passes over one that does not.
func CoordinatorFolders(in profile.Inputs, names func(dir string, limit int) ([]string, error)) ([]string, error) {
	if names == nil {
		return nil, refuse("coordinator folders", "no way to read the entries of a folder")
	}
	var out []string
	add := func(p string) error {
		if len(out) >= MaxCoordinatorIdentities {
			return refuse("coordinator folders", "more than %d folders", MaxCoordinatorIdentities)
		}
		out = append(out, p)
		return nil
	}
	list := func(dir string) ([]string, error) {
		entries, err := names(dir, maxCoordinatorDirEntries)
		switch {
		case err == nil:
			return entries, nil
		case absent(err):
			return nil, nil
		}
		return nil, fmt.Errorf("assert: entries of %s: %w", clipQuote(dir), err)
	}
	l := in.Layout()
	projects, err := list(l.CoordsDir())
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		features, err := list(l.CoordsDir() + "/" + project)
		if err != nil {
			return nil, err
		}
		for _, feature := range features {
			dir := l.CoordsDir() + "/" + project + "/" + feature
			paths := []string{dir}
			for _, private := range coordinatorPrivate {
				paths = append(paths, dir+"/"+private)
			}
			for _, p := range paths {
				if err := add(p); err != nil {
					return nil, err
				}
			}
		}
	}
	projects, err = list(l.CoordLoginDir())
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		logins, err := list(l.CoordLoginDir() + "/" + project)
		if err != nil {
			return nil, err
		}
		for _, login := range logins {
			if err := add(l.CoordLoginDir() + "/" + project + "/" + login); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
