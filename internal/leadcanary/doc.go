//go:build linux && ataniq_canary

// Package leadcanary holds what the daemon of canary mode needs that is not
// the daemon's own: the canary config, the mapping between the role and the
// names the layout and the unit use, the preflight and the holder command that
// the lifecycle controller asks for, the admission of the one message, and the
// evidence file. It exists only in a build with the ataniq_canary tag: every
// file of the package, the tests included, starts with
// "//go:build linux && ataniq_canary", and nothing outside canary mode imports
// it.
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
//     intake role "<project>-lead" and the unit name, and back. [NamesFor] and
//     [RoleOfFor] do the same for the other reserved kind, the scratch lead
//     (leadrole.ScratchPrefix), which [Config.Reserved] selects; the zero value
//     is the throwaway kind, so the canary is unchanged. Everything of the
//     package that takes a role follows the kind: the config's checks, its
//     layout, the holder's runtime folder, the preflight ([NewPreflightFor]),
//     the admission ([NewAdmissionFor]) and the evidence path, which is
//     <state root>/evidence/<layout role>.jsonl for a scratch lead so that
//     leads that share a state root never share a file.
//   - [Preflight] satisfies leadlifecycle.Preflight and passes for the config's
//     role only.
//   - [HolderCommand] satisfies leadlifecycle.HolderCommand. It builds the
//     holder's argument vector, its working folder and its whole environment.
//   - [Admission] builds the text of the one message. Its file, admission.go,
//     is the only file of the package that imports from the injection tree
//     (internal/inject/command), and a test holds that.
//   - [Evidence] appends the events of a run to <state root>/canary-evidence.jsonl.
//     [ResultEntry] and [Judge] read the holder's result for it and for the
//     daemon.
//
// # What the evidence says
//
// Two lines of the evidence say what canary mode waives and does not check: on
// the define and start lines, "qualification" is [QualificationText] and "login"
// is [LoginText], and the start line also carries the waiver
// (canaryroot.WaiverText). They show what the daemon declares. They are not a
// finding of the holder.
//
// The message body is never written to the evidence, nor is anything that
// could hold it: [Entry] has no field for it.
package leadcanary
