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
// expected come from the inputs. The result is validated.
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
		Version:          SpecVersion,
		Digest:           digest,
		Runtime:          in.Runtime,
		HolderPaths:      append([]string(nil), h.HolderPaths...),
		OwnerPaths:       dedupe(in.NeverMounted, h.OwnerPaths),
		MountSubstrings:  dedupe(h.MountSubstrings, in.NeverMounted, h.HolderPaths),
		OtherConfigDir:   profile.ConfigInside(in.Runtime.Other()),
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
