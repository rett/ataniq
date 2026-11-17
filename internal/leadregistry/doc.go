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
// What this package does not prove. It never calls systemd and never looks at
// a process, a control group or a lock: the caller supplies the unit evidence
// and the proof texts and they are stored as given. The SHA-256 chain finds
// accidental damage and truncation in the middle of the file; it has no key,
// so it does not stop someone who can write the file from rewriting it, and a
// cut at a line boundary is not detectable. The registry does not decide when
// a launch is safe beyond the checks documented on BeginLaunch; the caller
// must still prove that the previous unit is gone.
//
// This package is provisional and unqualified: it is part of the lead daemon
// work in progress, the file format is version 1 and may change before any
// release.
package leadregistry
