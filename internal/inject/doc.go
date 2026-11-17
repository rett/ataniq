// Package inject is the fence of the lead injection protocol (design sections
// 4.4 and 4.5, with the change of Q13) and the narrow interface the holder
// implements for it. Deliver pastes one message into the composer of a hosted
// agent and, only when the screen proves the paste landed in an idle composer,
// presses Enter.
//
// The package is provisional and unqualified: it is built from recorded
// sessions of one version per runtime, with no live qualification yet.
//
// # What it is
//
// A holder owns the terminal of an agent. It implements Session, Locked and
// View (session.go): the input lock, the two calls that read and write
// (Settle, Frozen) and the state of the emulator at one instant. Deliver takes
// a Session, one Message and Options, and makes one attempt. It never waits for
// the agent to become idle, never retries, never writes anything to clean up,
// and never sleeps: every wait is a Settle call, and the only clock it has is
// the deadline it was given. It returns a Result that says what was written and
// how the attempt ended, with the readings of both checks as plain data.
//
// The package imports osc7501, screen and idle of this feature, and nothing
// else of the repository. Package command (the queue) is imported by tests
// only: the queue lives in the daemon and the fence in the holder, and only the
// daemon joins them.
//
// # The rules of design 4.1 that the fence enforces
//
//   - Never Enter unless the composer is known to be idle and known to hold the
//     text. The Enter is written only after a second check, made after the
//     paste has settled, found the state idle, the composer on screen with the
//     cursor in it, and the header (or the runtime's pasted-text marker, where
//     there was none before) in it. Both checks refuse an idle that rests on the
//     OSC 7501 report alone (idle.Classification.ReportOnly); the second accepts
//     it only with the new marker.
//   - When the guard cannot tell, the answer is "not idle". A screen the
//     recogniser does not know, a stale or foreign report, a parser in the
//     middle of a sequence, an open synchronized-output frame, bracketed paste
//     off, an agent that is working or has a background task, a composer that
//     holds a draft, a screen or an owner that is not quiet: nothing is written.
//   - No key into an approval prompt. A blocked report, or a dialog on the
//     screen, keeps the paste from being written at all (the Q13 veto), and a
//     dialog that shows up after the paste keeps the Enter from being written.
//     The only bytes the fence ever writes are one bracketed paste and, at most
//     once and as its own write, a carriage return after a passed second check.
//   - Tested versions only. A runtime and version that is not in the table of
//     package idle is refused before the session is touched: no lock is taken.
//   - The text is data. It is checked first, before anything else: valid UTF-8,
//     printable characters and newlines, 1 to 4096 bytes, a header that equals
//     the first line.
//
// # What the fence leaves to the holder
//
// The holder implements the interface and keeps its promises (the comments on
// View, Locked and Session are written for the author of a holder): the PTY,
// the emulator, the input lock with the writer's keys held and not dropped,
// the faint spans of every snapshot (Snapshot.Faint), the OSC 7501 tracker
// and its stale flag, the quiet times, and the reply rule. The terminal's
// replies (the OSC 7501 query reply, DA1) travel the same serialised path as
// every other write: Settle writes the replies that the output it applied
// caused, before it returns and in order, so replies land between the paste
// and the Enter and never inside the paste; Frozen applies no output and so
// makes none; and no goroutine of the holder that carries replies may wait for
// the lock while Settle waits for it. The holder also keeps reading the PTY
// into a buffer it does not apply while a Frozen function runs, bounds the
// write by the deadline Frozen is given (an error once the bound passes), and
// answers View.Pending: true while output is queued to the holder and not
// applied (bytes queued on the PTY master, or in that buffer from the moment
// the holder's read returns). Pending is asked fresh at each call and is never
// cached. The fence reads it last, directly before each write, and writes
// nothing while it is true. A write that times out in the middle of a paste
// can leave an open bracketed paste in the agent's input: the holder must not
// complete or retry it, and the result is died with Written unknown, for the
// operator to decide.
//
// # What the fence leaves to the daemon
//
// The queue and the command states, the journal, the intent record before the
// first byte, the notice to the owner when a lead is blocked, the time limit
// for a lead that stays not idle, the confirmation of delivery from the
// agent's conversation file, the operator's decisions, and the deadline and
// the fence timeout that must agree (the deadline shorter than the command
// machine's fence timeout). The daemon must always set Options.Deadline: a
// zero Deadline is refused, with nothing written (not idle, "no deadline
// given"). A Result of outcome submitted is not delivery.
//
// The daemon maps a Result to the command machine's FenceResult in a few
// lines. The spellings of Written and Outcome are those of command.Written and
// command.FenceOutcome, so the mapping is a conversion, and a test in this
// package fails if they drift:
//
//	res := inject.Deliver(ctx, session, inject.Message{Text: d.Text, Header: d.Header}, opts)
//	update, err := machine.RecordFence(d.ID, command.FenceResult{
//		Written:  command.Written(res.Written),
//		Outcome:  command.FenceOutcome(res.Outcome),
//		Readings: res.Readings(),
//	}, now)
//
// where d is the command.Dispatch that BeginDispatch returned before the fence
// ran. Readings is one bounded line of fixed words and short tokens: it never
// holds the message, the owner's draft or a line the agent drew.
//
// # Outcomes
//
// Nothing written: not idle (the guard did not hold, the screen did not settle,
// the context ended or the deadline passed), blocked (the lead waits for an
// approval), untested version, refused text. Paste written, no Enter: not
// submitted. Paste and Enter written: submitted. An error of the session or of
// a write: died, with what was written as nothing when no write was attempted
// and unknown after one was.
//
// # Limits (design 4.6)
//
// The fence narrows a window and cannot close it. The agent can open a dialog
// on its own after the last reading and before the Enter lands, for example
// when a background task finishes; then the Enter answers that dialog with its
// default. Output already queued to the holder when Pending is read is seen
// (Pending refuses the write); what remains is output the agent has written
// that the kernel has not yet handed to the line discipline, or that the
// holder's reader has taken and not yet counted. Treating a background task as not idle narrows it. Input that
// reaches the agent without passing the holder (Remote Control) cannot be seen
// or held. That a paste into an open dialog does nothing rests on one
// observation per agent, so it is a live case per version, not an assumption.
// The recognisers read the screen as it was recorded for one version of each
// runtime; a different layout is unknown, and unknown is not idle.
//
// # Not shown by the recordings
//
// What the tests assume and no recording shows:
//
//   - No recording shows a multi-line paste displayed in either composer, in
//     the production form (a header, a carriage return and a body inside one
//     bracketed paste). The fake's collapse threshold for claude (over 1000
//     bytes or over 10 lines become a marker) and its inline display of a short
//     multi-line paste are assumptions. If they are wrong the fence fails safe
//     with not submitted, but case I-1 (an idle composer gets the paste and
//     then Enter) does not then hold live: it needs a live check with a real
//     multi-line message for each runtime.
//   - "Enter at a dialog approves its default" comes from the earlier
//     injection runs, not from the fenced runs. The fake models it so that an
//     approval is detected.
//   - Holds proves that the header is in the composer, not that the body
//     arrived whole: a truncated or partly echoed paste with the header intact
//     passes.
//   - On the marker path nothing ties the marker to this message: the
//     marker's count of lines or characters is not compared with the text.
//
// The tests drive the fence with a deterministic fake session in test files:
// no sleeps, a fake clock, a model of each agent's screen laid out as the
// recorded fixtures of package screen are, and scripted events at every step
// of the fence. They check on every scenario that the writes are a prefix of
// the paste and the Enter, that Enter follows a passed second check, that no
// approval is ever recorded outside the window above, and that Written is
// exact for every scenario whose writes all succeeded (unknown only when a
// write or the session failed).
package inject
