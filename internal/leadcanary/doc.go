//go:build linux && ataniq_canary

// Package leadcanary holds what the daemon of canary mode needs that is not
// the daemon's own: the canary config and the names of the role. It exists only
// in a build with the ataniq_canary tag: every file of the package, the tests
// included, starts with "//go:build linux && ataniq_canary", and nothing outside
// canary mode imports it.
//
// Canary mode starts one throwaway lead, whose project carries
// leadrole.ThrowawayPrefix, in a real sandbox with no qualification record, and
// sends it one message per epoch. This package keeps that mode to one role that
// the config names and to roots that lie nowhere near a real lead's.
//
// # The pieces
//
//   - [ReadConfig] reads the config file strictly and returns a [Config]; see
//     there for the rules. [Config.CheckRoots] runs the shared root check of
//     internal/leadholder/canaryroot, marker included, and the daemon calls it
//     before it listens.
//   - [Names] and [RoleOf] map the role "<project>/lead" to the layout and
//     intake role "<project>-lead" and the unit name, and back.
package leadcanary
