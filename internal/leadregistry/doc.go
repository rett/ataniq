// Package leadregistry is the lead daemon's durable record of every lead
// role (a project lead, the supervisor, or the coordinator of one feature of a
// project): which manager runs it, the launch profile that was requested and the
// profile that was observed, the epoch, the desired and shown states, the
// current launch intent and the unit evidence of the current epoch.
//
// The record is one append-only journal file, registry.jsonl, in a folder the
// caller names. Every mutation appends exactly one line and returns only after
// the file is fsynced. The in-memory state is rebuilt only by replaying the
// file when it is opened. Open takes an exclusive, non-blocking flock on the
// file, so one process writes at a time. A final line without its newline is
// an append that never returned: Open cuts it off and journals the cut in a
// recovered record, which Recovery reports until AcknowledgeRecovery journals
// a recovery-acknowledged record after it, also across a reopen. Any other
// damage makes Open fail with ErrCorrupt and nothing is repaired.
//
// The shown state changes by these rules only; a record that breaks them is
// refused when written and makes Open fail with ErrCorrupt when it is found in
// a journal. SetState takes one state of the first column to one of the
// others. Y is admitted. P is admitted with a non-empty proof text, evidence
// other than owner-decision, and while the launch intent is not pending; with
// a pending intent it is refused with ErrOutcomePending, so the caller records
// the outcome first (failed, abandoned, launched or adopted) and a stopped or
// ended with proof after launched or adopted is fine. N, and P with
// owner-decision evidence, are refused with ErrStateTransition.
//
//	from \ to   desired ready degraded stalled stopping stopped ended unknown
//	desired        Y      Y      Y        N       Y        P       P     Y
//	starting       N      Y      Y        C       Y        P       P     Y
//	ready          N      Y      Y        C       Y        P       P     Y
//	degraded       N      Y      Y        C       Y        P       P     Y
//	stalled        N      Y      Y        N       Y        P       P     Y
//	stopping       N      Y      Y        N       Y        P       P     Y
//	stopped        Y      Y      Y        N       Y        P       P     Y
//	ended          Y      Y      Y        N       Y        P       P     Y
//	unknown        N      N      N        N       N        P       P     Y
//
// C is Y for a coordinator and N for every other role. The stalled row exists
// for coordinators only: stalled is the state of a coordinator that was
// starting, ready or degraded and has stopped making progress. It is entered
// only from those three states, and left to ready, degraded, stopping,
// unknown, or stopped or ended with proof; no launch is admitted from it, so a
// stalled coordinator is proven stopped before its next instance starts.
//
// Coordinators. A coordinator is a role of its own, "<project>/coord/<feature>",
// with the epoch of a role counting its instances: instance n is epoch n, it
// grows by one for every launch and is never reused, whether the launch failed
// or not, and not after a reopen. Define takes its runtime and model (claude
// and fable when empty); its other profile fields are optional. BeginLaunch
// admits a coordinator only when, in addition to the gate of every role, the
// parent lead (the lead of its project) is defined, run by ataniq and shown
// starting, ready or degraded, and its own launch did not fail and was not
// abandoned; the intent then records the parent's epoch and the identity
// "<project>-lead@e<parent epoch>:coord:<feature>:<n>". The other direction:
// BeginLaunch of a lead is refused with ErrLaunchCoordinator while a
// coordinator of it, recorded under an earlier epoch of the lead, is not
// stopped, ended or desired (never launched). CoordinatorStopped says whether
// an instance is proven stopped, and Coordinators lists those of a lead. The
// registry gives a coordinator no authority and does not stop one when its
// parent's epoch ends; it only keeps the record and refuses the launches
// above.
//
// A launch intent may also carry the boot identity the caller passed to
// BeginLaunchWith (bounded text, stored as given and compared with nothing);
// BeginLaunch records none.
//
// starting is never set by SetState; BeginLaunch sets it. So desired is
// accepted only from desired, stopped or ended and cannot make a live or
// unknown role launchable, and from unknown the only ways out are stopped or
// ended with proof. An owner decision that resolves unknown is a separate
// operation, ResolveUnknown, journaled as its own record kind and shown with
// the evidence kind owner-decision and a non-empty decision text; it moves to
// stopped or ended only, from unknown only, with the same pending-intent rule.
// SetState refuses owner-decision evidence for stopped and ended from every
// state with ErrStateTransition, so a decision of the owner enters the journal
// only as an owner-decision record, and only as the resolution of unknown.
//
// What this package does not prove. It never calls systemd and never looks at
// a process, a control group or a lock: the caller supplies the unit evidence
// and the proof texts and they are stored as given. The SHA-256 chain finds
// accidental damage and a line that was changed, deleted or inserted before
// further lines; it has no key, so it does not stop someone who can write the
// file from rewriting it. A cut anywhere is read as an unfinished append:
// committed records after it are lost and their epochs may be reused, and a
// cut exactly at a line boundary is not even reported. Recovery, available
// after Open, says when a cut was made and has not been acknowledged; the
// daemon should then show the roles as unknown. The cut and its recovered
// record are written before a single sync, so a crash between them can lose
// the record and then the cut is not reported. After a failed write or sync
// the failed record may or may not be on disk (ErrBroken). The registry does
// not decide when a launch is safe beyond the checks documented on
// BeginLaunch; the caller must still prove that the previous unit is gone.
//
// Builds of other versions. A journal outlives the build that wrote it, so
// replay reads a record by what it says and tolerates what it does not know:
// a member of a line or of a payload that this build has no field for is
// ignored (the hash chain still covers the line's bytes as they are), and a
// member this build added is written only when it is set (omitempty or
// omitzero), so a record that does not use it is encoded as an older build
// encoded it. The identity of a requested profile is its stored bytes: the
// profile digest a launch intent carries, and the one replay checks it
// against, is the digest of the define record's requested member as stored,
// compacted and escaped as json.Marshal escapes (see Requested.Digest), which
// for a profile this build wrote is Requested.Digest. So a launch-begin line
// keeps matching its profile whatever members later builds add, and a line of
// a newer build whose profile has a member this build lacks still replays.
// What this build cannot read whole it does not act on: BeginLaunch refuses a
// role whose stored profile is not what this build would write for it, with
// ErrLaunchProfile, until the role is defined again. A define record that
// repeats a member anywhere in the record, its payload and the envelope that
// holds it (two payload members of which only the last would be read)
// included, exactly or as a name encoding/json
// folds to the same field, is such a profile too: no build writes one, and the
// typed decoding merges the occurrences while the stored bytes keep the last,
// so it replays and is not launched (see repeatsMember). testdata/v1-base holds a
// journal an older build wrote, with the digests it recorded; the tests read
// it back byte for byte.
//
// The tolerance is of members, not of values. A value this build does not
// know in a member it knows is damage, as before: a provider, a manager, a
// desired or shown state, an evidence kind or an outcome outside this build's
// sets, a text over its bound, a required member left empty, or a member of
// another JSON type fails the record's check, and the journal does not open
// (ErrCorrupt; the daemon exits 14). So are an unknown record kind and a
// header of another format version. A journal a newer build wrote opens only
// when that build added members and nothing else.
//
// The rule for a later build follows from that. Replay ignores an unknown
// member in every record kind, and only the requested profile of a define
// record is checked for one (ErrLaunchProfile): a member added to any other
// kind is dropped by an older build without a word, which replays the record
// as the record it knows and may act on it (resolve an intent, launch, stop).
// A member may therefore be added to an existing record kind only when an
// older build that ignores it does nothing the newer build would not: a note,
// a piece of evidence, provenance. A member that restricts what a reader may
// do (a hold, a fence, a guard, a precondition) must not be added to an
// existing kind. It needs a new record kind or a new format version, both of
// which an older build refuses as damage, or a value of a known member that
// an older build refuses, which has the same effect for that record.
//
// This package is provisional and unqualified: it is part of the lead daemon
// work in progress, the file format is version 1 and may change before any
// release. Version 1 has no compatibility promise until the first deployment:
// a rule may be tightened so that a journal written by an earlier build is no
// longer accepted, and there is no migration.
package leadregistry
