// Package ownerchannel is the host's side of an approval card: it takes a
// lead's request through building, verification and planning, takes the
// owner's decision, hands it to the door and carries it out, and answers the
// reads of the owner connection.
//
// It is a pure service. It opens no socket, runs no git and reads and writes
// no file but the journal, which the caller opens and hands over. Everything
// else that happens outside it is an interface this package declares, and
// the daemon's adapters satisfy them in Part 2: the door (package door's
// interface), the merge builder ([Merger]), the intake ([Intake]: the review
// and the summary, the fresh values at a use, the ref update, and what the
// mirror shows), the verifier ([Verifier]), the mailbox ([Mailbox]) and the
// clock. The tests of this package use fakes for them, the stand-in door of
// package doortest, and the real journal in a temporary folder.
//
// Nothing here is a command. No program imports this package in Part 1.
// Everything is provisional until the joint contract with the planner's
// project is agreed.
//
// # The flow of a request
//
// [Service.Submit] is owner-channel 4.2, steps 2 to 8. The door admits the
// request; the request waits in the state collected until it has the one
// verification slot of the host; in the state verifying the intake resolves
// the feature and the target, the merge builder writes and pins the merge,
// the verifier runs the suite on it and the intake reviews it; then the door
// plans it into a pending record. Anything but a clean verification refuses
// the request verification-failed and makes no card. A refusal of the intake
// (resolving or reviewing) reaches the lead with its own code when the code is
// one of feature-name, feature-moved, record-too-large, binding-mismatch and
// request-invalid; any other failure of the intake is request-invalid with a
// fixed line. Every refusal is
// answered in the lead's mailbox, undoes the pin, ends the request at the
// door through [RequestEnder] when there is one, and is counted. The count
// from the first card, whether the request would have met the First
// template's terms, is journaled for every request.
//
// # The flow of a decision
//
// [Service.Decide] is steps 9 to 11. A frame is checked in this order: the
// decider (a value the caller passes in [Source]; this package inspects no
// peer), the frame's form, presence, the pending record's state, the binding
// digest, and for an approve that the sign-offs equal the flagged list. A
// refusal the journal has a kind for is journaled as refused with its code.
// An approve past the project's limit for the day is journaled as held and
// comes back as a pending card with the held mark; it is never handed to the
// door. A deny is never held and does not count as a use.
//
// Otherwise the service fills the fields of the decision record that the
// daemon owns, journals the record, hands the same bytes to the door, and
// marks the decision delivered. An approve goes on: the intake reads the
// repository again, the door's Use consumes the approval, the intake applies
// the ref update, the door settles, and the journal keeps the outcome. An
// outcome that is unknown stays and waits for the owner: only [Service.Dispose],
// on his word, moves it on.
//
// # Append first, and a restart
//
// The journal is written before anything acts on its entry: the decision
// before the door sees it. Steps that cannot be journaled first (the door's
// use, the ref update) are followed by the entry, and a restart reads what
// the door and the mirror say. [New] runs the restart, and [Service.Tick] may
// run it again; every step is safe to repeat:
//
//   - a journaled decision that was not delivered is delivered once;
//   - an approval that was delivered and has no outcome continues from the
//     door's state, and one the door already used is settled from what the
//     intake says the mirror shows: applied, not-applied, else unknown. It
//     is never applied again;
//   - a held decision delivers nothing;
//   - a decision the door holds and the journal lacks is refused: an alert
//     is journaled and the approval is never used, while others go on;
//   - a held card whose pending record ran out is closed, and one that
//     ended at the door another way is not listed as pending.
//
// When the journal answers [ownerjournal.ErrUncertain] to a call, the
// service closes it, opens it again through [Config.Reopen] and does the
// restart, and tells the caller [ErrJournalReopened]: it never reports such
// an append as a refusal.
//
// A test stops the service between two steps with [Config.Fault], then
// starts a new service on the same journal and door.
//
// # Time
//
// The service reads the clock through [Config.Now]. A clock that moves back
// by more than a minute refuses uses until a new service starts (owner-channel
// 7.1). A day, for the limit, is a calendar day in the location of the
// clock's times.
//
// # The operations of the owner connection
//
// [Service.List], [Service.Show], [Service.Paths], [Service.Patch],
// [Service.Limit], [Service.LimitSet], [Service.Beat] and [Service.Looked]
// (and the pairing operations below) return the result types of package ownerwire, so that the daemon only has
// to register them. [Service.Decide] takes the frame type of the same
// package.
//
// # Pairing a phone and the list of deciding keys
//
// [Service.PairStart], [Service.PairStatus], [Service.PairPresent],
// [Service.PairConfirm], [Service.PairReject], [Service.Keys],
// [Service.KeyRevoke] and [Service.KeysOff] are the pairing operations of the
// owner connection (see package seatpair for the pairing's own steps, which
// the service drives). The one pairing the service holds is in memory: a
// restart abandons it, with its code and its pending credential, and nothing
// of it is journaled but how it ended. The pairing code is in the result of
// [Service.PairStart] and nowhere else: not in the journal, a notice, an error
// or any other answer.
//
// The writes that start, confirm or reject a pairing ask what a decision asks
// of its source: the decider's place and the owner's presence. A revocation
// asks the decider's place only: it only lowers. [Service.PairPresent] is the
// seat app's call and takes a source whose surface is [SurfaceSeat]; the
// daemon decides which connection that is.
//
// A credential that becomes pending has a pending id, 8 random bytes in 16
// lower-case hexadecimal digits that [Service.PairStatus] shows with the eight
// words. [Service.PairConfirm] and [Service.PairReject] take it back
// (ownerwire.PairPendingParams, required) and compare it, under the pairing's
// lock and in constant time, to the credential pending at that moment. The
// owner's word is therefore about the credential whose words he compared: if
// the pairing was replaced, or ran out and another began, the call is refused
// with [ErrPairingChanged] and nothing changes (the daemon's code is
// pairing-changed). A replacement or an expired pairing has a new id.
//
// The ten minutes of a code and of a pending credential are measured with the
// clock's reading as [Config].Now gives it, monotonic part included (a reading
// from time.Now has one), so a wall clock set back does not lengthen them; what
// the service shows and journals is the wall reading. A code or a pending
// credential that has been seen expired stays expired, whatever the clock reads
// afterwards (package seatpair states the exact rule, for clocks without a
// monotonic reading too).
//
// A confirmed credential goes on the list of deciding keys, [KeyStore], in
// [Config].Keys, with the kind "unverified". The service owns the store and
// closes it. The list only grows: a revoked credential stays on it, marked,
// and is never made good again. Without a store, or with one that cannot be
// used, every operation on it is refused with [ErrKeysUnavailable], and a
// list that cannot be read is never taken for an empty one. A confirm and a
// reject write only after they found the list usable, so a credential stays
// pending when the list is down. A reject then drops the credential before it
// writes the journal, because a drop only lowers what anyone may do: if the
// journal write fails, or the file has as many rejected pairings as it keeps
// room for, the call answers that error although the pairing ended, and the
// status says none. A credential that is on the list already, revoked or not,
// is refused when it is presented ([ErrCredentialInvalid], after the code was
// checked) and ends the pairing. The errors of these operations are a
// refusal of the connection ([*record.Error]), one of the package's sentinels
// with a fixed text, or, for the few steps that fail for a reason of their own
// (the random source, an error of seatpair that has no sentinel here), an
// error that wraps the cause: [Service.PairStart], [Service.PairPresent] and
// [Service.PairConfirm] wrap what seatpair returned in such a case, and the
// daemon answers it "failed" with a fixed text and writes the cause to its
// log. A pairing that expired, was burnt by wrong tries, or was replaced leaves
// no record anywhere (the journal and the list hold nothing of it; only a
// rejection is journaled), and it reads as the phase "expired" until the next
// start, with the time it ended: the code's or the credential's deadline, or,
// for a burnt code, the time of the try that burnt it, which is in the past. The
// desk is told of a burnt code by a notice ([Config].Notice) that holds nothing
// of the code or of the tries.
//
// # Concurrency
//
// A [Service] is safe for use by several goroutines and starts none of its
// own. Decisions, uses and restarts run one at a time; one verification runs
// at a time. [Service.Close] cancels the calls under way, waits for them and
// closes the journal.
package ownerchannel
