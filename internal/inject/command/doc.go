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
// that the view shows to the operator; it never ends the uncertain state.
//
// A hold. Late information can also arrive about a command that an operator
// already decided was not delivered, or is deciding: a late fence result or
// late delivery evidence for a command resolved as "not delivered, send again"
// or as "not delivered, drop", or for an uncertain command whose resend is
// half written. Then the queue of its target is held. A hold is a property of
// the queue, journaled as its own record (RecordHold: the target, the command
// the information is about, a reason code, a rendering of the call and the
// time) before the note, and it changes no command. While a queue is held
// BeginDispatch refuses it as queue-held, naming that command, whatever state
// any replacement is in (queued, dispatching, ended) and whatever a running
// fence later reports. A replacement
// that is already dispatching is not interrupted; if its fence wrote nothing it
// returns to queued and the hold keeps it there. The hold is set even when the
// note itself cannot be stored, because the note list is full or the note
// repeats an earlier one; late information for a command the queue is already
// held for does not hold it again, it advances the counter below. Only an
// operator ends a hold, with ReleaseHold.
// The operator decides about an uncertain command with Resolve, as before, and
// withdraws a queued command that must not be sent with Withdraw (allowed only
// in a held queue). No timer, Tick or Restore ends a hold, and Restore rebuilds
// holds from the journal and refuses a hold, release or withdrawal that the
// live machine could not have written. The view shows, per target, whether it
// is held, for which command, since when and why; a queue can be both frozen
// and held. After a release, the same evidence arriving again holds the queue
// again. A note equal to one the command already has is skipped, and at most
// eight notes are kept (late-notes-full after that); the hold is decided before
// both, and before the bounds on the evidence, so evidence refused as bad
// (bad-evidence) holds the queue too. The daemon passes each piece of evidence
// once.
//
// A release covers what the operator has seen. Each piece of late information
// for a command the hold is about (a second piece about a command the queue is
// held for as well, and a piece whose note is skipped, refused as
// late-notes-full or not written because the evidence was bad) is journaled as
// a hold record that advances a counter of the target, shown as
// TargetView.HeldSeq. The hold record also carries a bounded, safe rendering of
// the call (the evidence text, or what the fence wrote, its outcome and its
// readings, cut to the detail bound), and the view shows the last eight pieces
// of the hold as TargetView.HeldPieces: the counter value, the command, the
// time and the rendering of each. So a piece whose note was refused is still
// shown, and a release covers what the operator has seen when the daemon shows
// HeldPieces up to HeldSeq. The one call that writes nothing is a repeat, byte
// for byte, of the last piece of a queue that is held. The counter is never
// reset, not by a release either (the pieces end with the hold). ReleaseHold
// takes the value the operator saw and is refused as hold-changed when
// information arrived since; the release record carries that value. Restore
// rebuilds the counter and the pieces, and refuses a hold record that does not
// advance the counter by one, repeats the last piece, has no valid digest or
// no bounded, safe rendering, and a release that does not carry the current
// counter. Late information for an uncertain command with no resend pending
// is a note only and is not counted: that command freezes the queue, and its
// notes are shown with it.
//
// A decision covers the notes the operator has seen. Resolve takes
// Resolution.SeenLate, the number of late notes the operator was shown
// (len(CommandView.Late)), and refuses with notes-changed when the command has
// another number now; it applies to every decision about an uncertain command,
// and to the repeat that finishes a half-written resend. Nothing about it is
// journaled: the refusal changes nothing, and a resolution record is valid
// whatever the operator had read, so Restore needs no new field.
//
// What the hold does not do. Tick does not refuse the head of a held queue by
// the queue time limit: the head was not tried, so "not idle" would be a false
// reason, and a held queue never drains by time. One release ends the hold for
// every command it is about. A hold stops every sender's messages to that
// target, not only the sender of the commands it is about, and the queue can
// fill to its cap (admission refuses queue-full) until an operator acts.
//
// What is promised. Ataniq never sends by itself what an operator has not
// decided, and it stops the queue when late information arrives about a command
// an operator decided was not delivered (dropped or sent again), or is
// deciding (an uncertain command with its resend half written). It cannot
// recall a replacement whose fence is already running: that replacement may
// still be written, and the hold only keeps the next dispatch back. It does not
// promise to stop the queue for late information that contradicts a decision of
// delivered; see below.
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
//     one is found. The machine holds a queue only for evidence it is told
//     about; evidence the daemon has not read yet is not known to it.
//   - The daemon matches late evidence only by the header id of a user turn in
//     the conversation file. A resend carries the same body as its original
//     under a new header, so matching by body, or anywhere in the file, would
//     take the delivery of the resend for late evidence of the original and
//     hold the queue after every resend; and a lead can print a line that looks
//     like a header for a dropped id in its own output, which is not a user turn.
//   - The daemon passes each piece of late information once. Alternating
//     repeats (A, B, A, B) each advance the counter and so keep a release from
//     going through, because only a repeat of the last piece is suppressed. It
//     shows the operator HeldPieces and the late notes of the commands the hold
//     is about (Machine.Command, also for a command that has ended), and passes
//     back HeldSeq to ReleaseHold and the number of notes it showed to Resolve
//     (Resolution.SeenLate).
//   - Open: SeenLate counts stored notes only. For an uncertain command with
//     no resend pending, a piece refused as late-notes-full or bad-evidence is
//     stored nowhere, so a decision can cover it unseen; the daemon logs and
//     shows it itself. Storing such a piece as a hold record (as is done for a
//     decided command) would close it.
//   - The daemon repeats a late call (RecordFence or ConfirmDelivery for a
//     decided command) that returned a journal error. The hold is written
//     before the note: if the hold failed nothing changed, and if the note
//     failed the hold stands and the repeat writes the note only.
//   - Refused submissions are journaled without a bound: a sender that submits
//     refused messages in a loop writes a record for each, so the daemon must
//     rate-limit requests before Submit.
//   - Ended commands (delivered, refused, resolved) are kept in memory for the
//     life of the Machine and Restore replays the whole journal; there is no
//     compaction.
//   - A late result that contradicts an operator's decision of delivered, for
//     example a fence result "nothing written" for a command resolved as
//     operator-delivered, is refused as wrong-state and not journaled today. The
//     message may never have reached the lead, and the audit loses the result.
//     Whether it should be kept as a note and also hold the queue is an open
//     question for the owner.
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
