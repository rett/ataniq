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
//   - [Host.Merge] is the Merger: it builds the host's merge and pins it
//     under refs/ataniq/pending/, the key being the service's own.
//   - [Host.Intake] is the Intake: the feature and the target are read from
//     the mirror by their exact names; the review is the intake's merge mode
//     (Inspect and Summarize), with the summary the card shows; a use reads
//     the refs, the review digest, the rule-set digest and the verification
//     records again; the ref update is [intake.Integrate], one transaction
//     with the old value, no checkout and no hook; the mirror is read for
//     what an unfinished use left.
//   - [Host.Verifier] is the Verifier: it runs a [Suite] on the merge and
//     keeps the result as a verification record of the intake's store, which
//     is also what [intake.Integrate] needs before it moves a ref.
//   - [Host.Mailbox] is the Mailbox: a reply file beside the request, the
//     only reply this build writes (a mailbox reply to a lead is out of
//     scope).
//   - [Collector] is a file-drop collector for the scratch pass: it reads
//     request files from a folder, refuses a feature name outside the
//     grammar before anything is built, and gives the rest to the service.
//     The real spool is out of scope.
//
// # What it does not do
//
// It adds no maintenance entry point: nothing here runs "git gc" or prunes.
// A pin keeps the merge and both parents alive, and a test of this package
// shows it by running git gc on a temporary mirror directly, outside gitenv.
// It does not refresh the lead's branch in the mirror (the mirror's own sync
// does), and it runs no suite by itself: the [Suite] is injected, and the
// verification sandbox of package verify takes a tip under refs/leads/ only,
// so a merge that lives under refs/ataniq/pending/ cannot be handed to it yet.
//
// # Text from the lead
//
// Paths and the patch are raw lead text. The adapter keeps them as the
// intake does: the summary holds raw paths as record.Text, and the patch
// lines are the intake's escaped lines, which the daemon turns back with
// leaddaemon.UnescapingIntake (see [Host.Service]).
package hostadapter
