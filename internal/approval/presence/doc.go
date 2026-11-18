// Package presence decides whether the owner is at a surface, and notices the
// moment he comes back to it.
//
// The owner is present while the surface has seen an action of his (a key, a
// pointer action) within a window of time, ten minutes unless set otherwise.
// Otherwise he is away. The first action after an away stretch is an arrival:
// the moment to show him what happened while he was gone. A decision on an
// approval card is accepted only while he is present.
//
// # What the package is
//
// It is a pure state machine. It reads no clock: every method takes the time
// it is asked about, so a caller passes its own clock's reading and a test
// passes any time it likes. It starts no goroutine, keeps no file and imports
// nothing but the standard library. A [Tracker] is not safe for use from more
// than one goroutine at a time; a caller that needs that holds its own lock.
//
// # How a caller uses it
//
// A surface makes one session id per run with [NewSession] and one tracker
// with [New]. Then:
//
//   - for every action of the owner it calls [Tracker.Activity]. The answer
//     says whether this action is an arrival and whether a presence beat is
//     due. The tracker does not take the action: the caller still delivers
//     the key where it was aimed.
//   - from time to time it may call [Tracker.Tick], which reports the moment
//     he went away, once.
//   - before a decision is acted on it calls [Tracker.MayDecide]. The answer
//     is the [Stamp] the journal keeps with the decision, or an [*Away] when
//     there was no activity inside the window. This package knows no refusal
//     codes; its caller maps an [*Away] to the code "owner-away".
//
// The side that receives beats (a backend, a service) runs a tracker of its
// own and feeds each beat to Activity in the same way.
//
// # The limit of it
//
// A process of the owner's own user can imitate activity. Presence is a
// convenience and a record, not a fence; what bounds a forged decision is
// decided elsewhere.
//
// # Time that runs backwards
//
// A time earlier than the last activity is never inside the window, so a
// clock that is set back never makes the owner present by itself. Only a new
// action of his does, and that action is then an arrival, because by the
// clock's new reading he was not present before it.
//
// A caller should pass readings of its own running clock, as the Now
// function of package time returns them. Such a reading carries Go's
// monotonic clock, which never goes back, and the tracker compares times by
// it: a change of the wall clock then never reaches the tracker, and what is
// said here does not come up.
//
// It comes up with times that carry no such reading: times that were stored
// and read back, parsed from text, or handed on by another process. The
// tracker takes them as they are. Each action at a time earlier than the
// last activity, by however little, is reported as the owner leaving and
// arriving again, and a beat is due for it. Times that step back again and
// again while he types therefore show him as arriving each time, and send
// beats more often than once in a [BeatInterval]. A caller that holds only
// such times and cannot trust their order must settle that itself before it
// calls the tracker.
//
// # Since he last looked
//
// What the arrival summary lists is "what happened since he last looked".
// That mark is one time value. The caller stores it, wherever it keeps such
// things, and hands it back: [Advance] moves it, and [Newer] says whether
// something that happened at a given time is newer than it.
package presence
