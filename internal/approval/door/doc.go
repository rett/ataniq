// Package door names the seven verbs through which the host talks to the
// planner's store of approvals, as one interface.
//
// The host does not keep the authority over an approval. Another program,
// the planner's broker, keeps it in a store that no sandbox can reach: it
// admits a lead's request, plans what an approval would do, takes the
// owner's decision, and lets that decision be used once. The host reaches
// that store only through a small set of calls, and this package calls that
// set the door.
//
// The real door belongs to another project and is not built here. This
// package holds the interface ([Door]) and the small types its verbs take
// and return, so that the host's side can be built and tested against a
// stand-in. The stand-in is the package doortest, which is for tests only.
// Everything here is provisional until the joint contract with the
// planner's project is agreed.
//
// # The verbs
//
// The first five are the steps of one approval, in order:
//
//   - Admit takes a lead's request. The door checks its form, that it names
//     the seat's present epoch, that the feature name fits the grammar, and
//     the caps: so many requests a minute, so many not yet final, a
//     throttle on asking for the same thing again and again, and one live
//     request for one verb and target. An admitted request is not yet an
//     approval; the host goes on to build and verify what was asked.
//   - Plan takes the request again with what the host built and reviewed.
//     The door checks the seat and the epoch again, checks that the merge
//     the host built has the two parents it should have and that the target
//     has not moved, computes its plan, and writes the pending record: the
//     record the owner is shown, with the digest of the part he approves
//     (the binding).
//   - Decide takes the owner's decision, approve or deny. The door checks
//     its form, that it names the binding's digest, that the seat has not
//     changed, that nothing has expired, and that the sign-offs are exactly
//     the flagged paths.
//   - Use consumes an approval, just before the host applies it. The host
//     reads the repository again and hands in what it found; under one lock
//     the door compares that with the binding, checks the state and the
//     time, marks the approval consumed and returns the effects. An
//     approval is used once: a second use is refused.
//   - Settle says how applying went: applied, not applied, or unknown.
//
// The last two read:
//
//   - List returns one line for every pending record the door knows.
//   - Show returns one pending record, and its decision when it has one.
//
// # Records and digests
//
// A record goes in and comes out as its bytes with the digest of those
// bytes (a [Record]), and is never written again on the way: the request as
// the lead wrote it, the pending record as the door wrote it, the decision
// as the daemon journaled it. The forms are those of the package record.
//
// Decide and Settle are idempotent: for the same id and the same binding
// digest they answer what they answered the first time. So a decision that
// was journaled and perhaps not delivered (a crash) is delivered again
// without harm. That holds for the same decision bytes only: a different
// decision for a record that already has one is refused decision-invalid,
// and Show returns the decision the door kept.
//
// # Refusals and failures
//
// A door that says no returns a [*record.Error] with one of the design's
// refusal codes; a refusal is an answer, and the request or approval is
// then in a state the door can name. Any other error is a failure of the
// call (the door could not be reached, the call was cancelled), and says
// nothing about what the door did: the caller asks again, or reads the
// state with Show.
//
// A pending record has one of the states of the closed list in the package
// record ([record.State]). No state returns to pending or approved, and
// there is no state for a decision that the host holds back: a held
// decision never reaches the door, and its record stays pending.
package door
