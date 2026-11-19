// Package doortest is a stand-in for the planner's door, for tests.
//
// The real door ([door.Door]) is another project's broker, with a store on
// disk that no sandbox can reach. This package answers the same seven verbs
// from memory, with a clock the test hands in, so that the host's side of
// an approval can be tested without that broker. It is imported by test
// files only: no program is built with it, and it is not the sample backend
// of the console, which holds cards of its own and knows no door.
//
// # What it follows
//
// The stand-in follows the design of the owner channel:
//
//   - A pending record has one of the states of [record.State], a closed
//     list, and moves between them only as the table in this package
//     allows ([Moves]). No state returns to pending or approved. There is
//     no state for a decision the host holds back at the daily limit: such
//     a decision never reaches a door, and its record stays pending.
//   - Admit checks the request's form, the seat's assignment, the epoch
//     against the seat's, the feature name's grammar, a request id used
//     before, a second live request for one verb and target, the repeat
//     throttle, and the two caps.
//   - Plan writes a pending record through the package record, after it
//     has checked the seat and the epoch again, the feature's tip against
//     the one the request named, and the merge's parents and the target's
//     tip as the values it is given.
//   - Decide checks the decision's form, the binding digest, the fields
//     repeated beside it, the seat's revision and epoch, expiry, and that
//     an approve signs off exactly the flagged paths. It answers the same
//     for the same id and binding digest.
//   - Use compares the fresh values with the binding under the one lock of
//     the stand-in, and consumes the approval. A second use, a mismatch, an
//     expired approval and changed evidence are each refused with their
//     code; a mismatch and changed evidence also void the record.
//   - Settle takes applied, not-applied or unknown, and answers the same
//     when it is told the same again. An unknown outcome may be settled
//     once more, as applied or not-applied: that is the owner disposing of
//     it.
//
// # Where it is kinder than the real door
//
// A test that passes against the stand-in shows that the host's side
// follows the sequence and reads the codes. It does not show that the real
// door accepts the same calls. The stand-in is kinder in these ways, and
// may be in others:
//
//   - It has one seat and one project, and takes the seat's facts from its
//     configuration. It does not check that the seat is held by a lead, or
//     that no transfer is under way.
//   - It reads no repository. What the real door looks up for itself (the
//     parents of the merge, the tip of the target) it takes as given.
//   - It writes no file, no audit line and no projection, so a use is never
//     refused for a line that could not be written, and nothing survives
//     the test.
//   - Its plan text is a few lines made from the binding, with no salted
//     token behind it.
//   - It has no rule and no grant: no verb here leads to approved-by-rule or
//     approved-by-grant, though the table of moves names them.
//   - It cannot tell who calls Settle, so it lets any caller move a record
//     out of unknown with applied or not-applied. Only the owner may do
//     that; the host never does it by itself, and the service that holds
//     the owner's disposal must keep to it.
//   - Only admitted requests count against the caps and the throttle.
//   - It does not hold the note to the digest the request names: the
//     vectors' request and pending record do not agree on it.
//
// # Ending a request
//
// A request ends in the real system when the lead withdraws it, when its
// time runs out, or when something it is bound to changes. None of that
// comes through the seven verbs, so the stand-in has methods of its own for
// a test to say that it happened: [Door.Withdraw], [Door.Void] and
// [Door.Expire], with [Clock] to move the time, and [Door.SetEpoch],
// [Door.SetSeatRev] and [Door.SetAssigned] to change the seat.
package doortest
