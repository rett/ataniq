package assert

import (
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
	// LoopbackTCP and AbstractSocket are the canary's addresses
	// (Canary.TCP and Canary.Abstract).
	LoopbackTCP    string
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

// NewSpec builds the spec for a profile's inputs and the host's facts: the
// digest, the runtime, the expected environment (the profile's allowlist with
// its values), the other runtime's config folder inside, the tree and its
// three read-only git paths, the managed-settings path and whether a seccomp
// filter is expected come from the inputs. The result is validated.
func NewSpec(in profile.Inputs, h HostFacts) (Spec, error) {
	digest, err := in.Digest()
	if err != nil {
		return Spec{}, err
	}
	env, err := in.Environment()
	if err != nil {
		return Spec{}, err
	}
	s := Spec{
		Version:          SpecVersion,
		Digest:           digest,
		Runtime:          in.Runtime,
		HolderPaths:      append([]string(nil), h.HolderPaths...),
		OwnerPaths:       dedupe(in.NeverMounted, h.OwnerPaths),
		MountSubstrings:  dedupe(h.MountSubstrings, in.NeverMounted, h.HolderPaths),
		OtherConfigDir:   profile.ConfigInside(in.Runtime.Other()),
		LoopbackTCP:      h.LoopbackTCP,
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
