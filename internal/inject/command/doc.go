// Package command is the message admission rules and the durable command
// state machine of the lead injection protocol (design sections 4.1, 4.2 and
// 4.5).
//
// A message from the daemon, the supervisor or a project lead to a lead or the
// supervisor becomes a command. This package decides whether the message may be
// admitted (who may send to whom, the shape of the header, the characters of
// the text, the size and rate caps), keeps the commands of each target in a
// first-in first-out queue, and moves each command through the states
// accepted, queued, dispatching, delivered, refused, uncertain and resolved.
// Every move is first appended to a Journal and only then applied, so the
// journal is the truth: Restore rebuilds the machine from its records.
//
// The package is pure. It does no I/O of its own, starts no goroutine and
// reads no clock: each method that needs the time takes it as a parameter. A
// Machine is not safe for concurrent use; the daemon owns one instance and
// serialises calls to it.
//
// The package is not the fence and not the holder. It never writes to a
// terminal, never reads a screen and never imports the fence: the daemon asks
// BeginDispatch for the text to send, runs the fence, and reports the fence
// result back as plain values with RecordFence. Delivery evidence (for example
// a position in the conversation file) is likewise a value the daemon passes
// to ConfirmDelivery. Nothing here retries: a command that is uncertain stays
// uncertain, and freezes its queue, until an operator records a decision with
// Resolve. A fence result or delivery evidence that arrives after a command
// became uncertain (for example after FenceTimeout) is kept as an annotation
// that the view shows to the operator; it never ends the uncertain state. The
// same evidence for a command an operator already resolved as "not delivered,
// send again" is kept as an annotation on it too and is not refused. When the
// newest replacement along the resend chain is still queued, it becomes
// uncertain (original-evidence-arrived) and holds its queue until an operator
// decides about it, so that nothing is sent twice while evidence that the
// first send arrived is in hand; when the replacement is already dispatching
// or has ended, only the annotation is written. A note equal to one the command
// already has is skipped, and at most eight notes are kept (late-notes-full
// after that). The daemon passes each piece of evidence once.
//
// Ended commands (delivered, refused, resolved) stay in memory, without their
// text, so that a command id is never used twice; trimming that history is the
// daemon's concern. Their late notes stay with them for the audit, up to the
// cap of eight notes, so an ended command can keep a few kilobytes. Refusals
// at admission are journaled too and keep no command.
//
// This package is provisional and unqualified: it is built from recorded
// sessions of one version per runtime and has no live qualification yet.
//
// Left to the daemon. These are known and not handled here:
//
//   - Before dispatching a resend, the daemon looks in the conversation file
//     for the ids of the whole resend chain (the command that was sent again,
//     and the commands that replaced it in turn) and does not dispatch when
//     one is found. The machine freezes a queued replacement only for evidence
//     it is told about; evidence the daemon has not read yet is not known to
//     it.
//   - Refused submissions are journaled without a bound: a sender that submits
//     refused messages in a loop writes a record for each, so the daemon must
//     rate-limit requests before Submit.
//   - Ended commands (delivered, refused, resolved) are kept in memory for the
//     life of the Machine and Restore replays the whole journal; there is no
//     compaction.
//   - Whether the queue time limit should also end a command whose lead stays
//     blocked is an open question for the owner. Today it does: a head that
//     waits longer than QueueTimeLimit is refused as not idle, with the last
//     reason "blocked" in its detail.
//
// A message is data. Its text is a header line, a newline and the body; the
// header names the sender, which is never the owner (the owner speaks as the
// writer). The rules that keep a body from forging a header are written on
// CheckBody.
//
// Known limit. The check that keeps a body from carrying the header marker
// folds the body to ASCII letters and digits. It folds invisible fillers,
// fullwidth forms, and the mathematical alphanumerics of U+1D400 to U+1D6A3 and
// the mathematical digits (see CheckBody). It does not fold any other rune that
// merely looks like a Latin letter: letters of other scripts (Cyrillic, Greek),
// Latin letters that are not ASCII (the dotless i, IPA letters such as U+0251
// and U+0261, precomposed letters with a mark, so that the same visible letter
// written composed is admitted and written with a combining mark is refused),
// modifier letters, small capitals, the letters of the script, fraktur and
// double-struck alphabets that Unicode places in Letterlike Symbols (U+210A,
// U+2110 to U+2133, U+2139) and not in the mathematical block, and enclosed
// letters (circled, parenthesized), which are not letters and are dropped. A
// body that spells the marker with such runes is admitted, so a body can still
// show a line that looks like a header to a human or a model. Closing that
// needs a design decision, for example a fixed prefix on every body line so
// that only the header line has none. That is an open question for the owner.
package command
