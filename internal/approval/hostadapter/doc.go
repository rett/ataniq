//go:build linux && ataniq_ownerchannel

// Package hostadapter joins the interfaces of package ownerchannel to the
// host's real parts: the merge builder (package mergebuild), the intake
// (package intake), a verifier over a suite runner, the journal (package
// ownerjournal) and a file reply.
//
// It is built only with the tag ataniq_ownerchannel, as is every file of the
// package, and only the command of the daemon imports it. A release build has
// no owner channel and so no adapter. Everything here is provisional and
// unqualified: the stand-in door of the tests stands for the planner's broker,
// and nothing here has run against a real one.
//
// # What it adapts
//
//   - [Host.Build], [Host.Pinned] and [Host.Unpin] are the Merger: they build
//     the host's merge and pin it under refs/ataniq/pending/, the key being
//     the service's own.
//   - [Host.Resolve], [Host.Review], [Host.TargetTip], [Host.Paths],
//     [Host.Patch], [Host.Fresh], [Host.Apply] and [Host.Mirror] are the
//     Intake: the feature and the target are read from
//     the mirror by their exact names; the review is the intake's merge mode
//     (Inspect and Summarize), with the summary the card shows; a use reads
//     the refs, the review digest, the rule-set digest and the verification
//     records again; the ref update is [intake.Integrate], one transaction
//     with the old value, no checkout and no hook; the mirror is read for
//     what an unfinished use left.
//   - [Host.Verify] is the Verifier: it runs a [Suite] on the merge, through
//     the pin that holds it, and keeps the result as a merge-mode
//     verification record of the intake's store (the merge as the tip, the
//     target's tip it was built on as the base, the pin as the ref), which is
//     what [intake.Integrate] needs before it moves a ref. A lead-mode record
//     of the same commit, or a merge record on another base, never counts,
//     here or there.
//   - [FileMailbox] is the Mailbox: a reply file beside the request, the
//     only reply this build writes (a mailbox reply to a lead is out of
//     scope).
//   - [Collector] is a file-drop collector for the scratch pass: it reads
//     request files from a folder, refuses a feature name outside the
//     grammar before anything is built, and gives the rest to the service.
//     A request a stop left taken but never handled (in the collected
//     folder, with no reply, unknown to the service and to the door) is
//     answered request-invalid once by the first scan after the start, never
//     over a reply that exists and never resubmitted. The real spool is out
//     of scope.
//
// # What it does not do
//
// It adds no maintenance entry point: nothing here runs "git gc" or prunes.
// A pin keeps the merge and both parents alive, and a test of this package
// shows it by running git gc on a temporary mirror directly, outside gitenv.
// It does not refresh the lead's branch in the mirror (the mirror's own sync
// does). The [Suite] is injected: [VerifySuite] is the real one, which runs
// package verify in its merge mode through the sandbox template, and a test
// uses a fake.
//
// # Text from the lead
//
// Paths and the patch are raw lead text. The adapter keeps them as the
// intake does: the summary holds raw paths as record.Text, and the patch
// lines are the intake's escaped lines, which the daemon turns back with
// leaddaemon.UnescapingIntake (see [Host.Patch]).
package hostadapter
