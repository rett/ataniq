// Package leadregistry is the lead daemon's durable record of every lead
// role: which manager runs it, the launch profile that was requested and the
// profile that was observed, the epoch, the desired and shown states, the
// current launch intent and the unit evidence of the current epoch.
//
// The record is one append-only journal file, registry.jsonl, in a folder the
// caller names. Every mutation appends exactly one line and returns only after
// the file is fsynced. The in-memory state is rebuilt only by replaying the
// file when it is opened. Open takes an exclusive, non-blocking flock on the
// file, so one process writes at a time. A final line without its newline is
// an append that never returned: Open cuts it off and reports it. Any other
// damage makes Open fail with ErrCorrupt and nothing is repaired.
//
// The shown state changes by these rules only; a record that breaks them is
// refused when written and makes Open fail with ErrCorrupt when it is found in
// a journal. SetState takes one state of the first column to one of the
// others. Y is admitted. P is admitted with a non-empty proof text and while
// the launch intent is not pending; with a pending intent it is refused with
// ErrOutcomePending, so the caller records the outcome first (failed,
// abandoned, launched or adopted) and a stopped or ended with proof after
// launched or adopted is fine. N is refused with ErrStateTransition.
//
//	from \ to   desired ready degraded stopping stopped ended unknown
//	desired        Y      Y      Y        Y        P       P     Y
//	starting       N      Y      Y        Y        P       P     Y
//	ready          N      Y      Y        Y        P       P     Y
//	degraded       N      Y      Y        Y        P       P     Y
//	stopping       N      Y      Y        Y        P       P     Y
//	stopped        Y      Y      Y        Y        P       P     Y
//	ended          Y      Y      Y        Y        P       P     Y
//	unknown        N      N      N        N        P       P     Y
//
// starting is never set by SetState; BeginLaunch sets it. So desired is
// accepted only from desired, stopped or ended and cannot make a live or
// unknown role launchable, and from unknown the only ways out are stopped or
// ended with proof. An owner decision that resolves unknown is a separate
// operation, ResolveUnknown, journaled as its own record kind and shown with
// the evidence kind owner-decision and a non-empty decision text; it moves to
// stopped or ended only, from unknown only, with the same pending-intent rule.
// SetState with owner-decision evidence does not leave unknown.
//
// What this package does not prove. It never calls systemd and never looks at
// a process, a control group or a lock: the caller supplies the unit evidence
// and the proof texts and they are stored as given. The SHA-256 chain finds
// accidental damage and a line that was changed, deleted or inserted before
// further lines; it has no key, so it does not stop someone who can write the
// file from rewriting it. A cut anywhere is read as an unfinished append:
// committed records after it are lost and their epochs may be reused, and a
// cut exactly at a line boundary is not even reported. Recovery, available
// after Open, says when a cut was made; the daemon should then show the roles
// as unknown. After a failed write or sync the failed record may or may not be
// on disk (ErrBroken). The registry does not decide when a launch is safe
// beyond the checks documented on BeginLaunch; the caller must still prove
// that the previous unit is gone.
//
// This package is provisional and unqualified: it is part of the lead daemon
// work in progress, the file format is version 1 and may change before any
// release.
package leadregistry
