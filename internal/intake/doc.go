// Package intake reviews a lead's branch against its upstream branch before
// anything of it enters the owner's history.
//
// # Why
//
// A lead's branch in the exchange mirror (refs/leads/<role>/heads/<branch>)
// is untrusted. To check it out or to build it in an owner folder would run
// the lead's code as the owner. So the first step of the owner's intake is a
// review that reads the change between the upstream branch (refs/heads/<target>)
// and the lead's tip with git plumbing in the bare mirror. Nothing is checked
// out. The review reads the diff, not a checkout.
//
// # What a review holds
//
// [Inspect] resolves the two refs once to full commit ids, checks that the
// upstream commit is an ancestor of the tip, lists the commits between them,
// and lists the net change of the tip's tree against the base's tree. It marks
// every changed path that needs its own sign-off with a [Flag]:
//
//   - exec-config: the path is in the execution-config set (package
//     execconfig), for example agent settings, a Makefile or a hook.
//   - symlink: the entry is, or was, a symbolic link. A link can make a
//     checkout reach an execution-config path through another name.
//   - submodule: the entry is, or was, a submodule (a gitlink).
//   - aliased: a symlink whose own path is execution config stands for a
//     folder, and this path lies in that folder. A symlink that points out of
//     the repository, at an absolute path, or through too long a chain of
//     symlinks, is flagged on its own path.
//   - history-only: a path that is execution config, a symlink or a
//     submodule, or that lies in a folder an aliasing symlink (of the base or
//     of the tip) stands for, in the change of some commit of the branch,
//     but that is not in the net change. It was added and removed again (or
//     changed and changed back) inside the branch. It enters upstream
//     history, so it needs a sign-off too. For a merge commit only the paths
//     that differ from every parent count: a path that equals one parent was
//     brought in by that parent.
//
// The flags are collected in [Review.Flagged], one for each kind and path.
// [Review.Digest] is a hash over what the owner is asked to sign off, so a
// later step can check that the owner signed this review and not another.
// [Diff] writes the patch of the net change for the owner to read.
//
// # How it is safe
//
// Every git run goes through gitenv with an explicit --git-dir and through
// boundedexec with a time limit that covers the whole call and with output
// caps taken from [Limits]. Every ref is resolved once, by its exact full
// name, and every later command gets object ids and never a ref name, so a
// ref that moves during the review cannot mix two states. [Inspect] and
// [Diff] write nothing to the repository. [Integrate] writes one ref, the
// target branch, and nothing else. The package checks nothing out, and opens
// no file of the repository except through git. The repository must be a bare
// repository that Ataniq made, never a lead's clone.
//
// This package does not import the mirror package. It takes a repository and
// full ref names, and checks only that the base ref is under refs/heads/ and
// the tip ref under refs/leads/ (under [PendingPrefix] in merge mode).
//
// # Text from the lead
//
// Everything a lead wrote is hostile: paths, commit subjects, author names,
// diff content, symlink targets. The structs of this package ([Commit],
// [Change], [Flag]) hold that text raw, byte for byte, so that a signed
// digest and a later comparison are exact. The commits are read as the raw
// objects the lead wrote, with no conversion of text: git would convert a
// message from the charset the commit's own encoding header names, so no git
// command that prints a message runs, a commit that declares an encoding other
// than UTF-8 is refused with [ErrCommitEncoding], and a commit object that is
// not in the form git writes is refused with [ErrCommitFormat]. A caller that shows a field must
// escape it for display first with the leadtext package, and must show
// leadtext.Label near it. The reason of a flag is the package's own text; lead
// text inside it (a path, a link target) is already escaped with
// leadtext.Escape. Error messages carry no output of git and no lead text
// except escaped ref names and the escaped value of an encoding header.
//
// [Diff] writes one header line of its own, which names the two commits,
// and then the patch through leadtext.NewWriter. A lead can write any line
// into a patch, including one that looks like the header. The header is
// therefore trusted only as the first line. Everything after the first line is
// leadtext.Label: unverified lead data.
//
// # Sign-off
//
// The owner signs off the change, and separately each flagged path. A
// [SignOff] is bound to the tip's full commit id and to the [Review.Digest] of
// the review the owner saw. [Review.CheckSignOff] accepts it only when both
// match, the change as a whole is accepted, every flagged path has an entry,
// and no entry names a path that is not flagged (a stale list is noticed). A
// refusal is a [SignOffError] that lists everything wrong at once.
//
// # Records
//
// A [Store] is the owner's records folder, with one JSON file for each
// record: a [VerificationRecord] for each verification run, written by
// [Store.PutVerification], and an [IntakeRecord] for each integration. The
// folder must be a real folder owned by the caller with mode 0700. It is the
// owner's: it must not be inside the exchange mirror or inside a state folder
// that a lead can read or write, and a lead's sandbox must not have it
// mounted. This package cannot know the layout, so the command that opens a
// Store checks it.
//
// A file is written to a temporary name, synced, and given its name without
// replacing anything that is there; this package never overwrites or deletes a
// record. The order of the names is the order of the records, and it does not
// depend on a clock: a write holds an exclusive lock on the folder (shared by
// every Store on it, in any process), reads the newest record name in the
// folder, and names the new record later than it when the writer's clock is
// not later. Names can therefore run ahead of the clock after a step of it.
// The folder is synced after the rename. If only that sync fails, the record
// is there under its name; the write counts as done, and the call returns the
// record with an error that matches [ErrNotSynced], to be shown as a warning.
// The folder holds at most 100000 names of any kind (a verification with
// output uses two); with more, every read and write fails with [ErrStore] and
// [Integrate] stops before it moves a branch. Nothing here deletes or rotates
// records, so the owner moves old ones away before that.
//
// Records are read through safefs: regular files only, at most 1 MiB,
// no symlink followed, no block on a FIFO. A file whose name is not a record's
// name is ignored. A file with a record's name that cannot be read, or that
// does not decode strictly (an unknown field, a wrong version, a missing
// value), is an error that names it, and nothing goes on without it, because a
// record that cannot be read could be the failure that withdraws a pass. Raw
// bytes of a path or a ref name that are not UTF-8 are kept exactly, as
// base64.
//
// The output of a verification run is lead-influenced text. The record holds
// only its SHA-256 and length. The raw output is written next to the record
// with the suffix ".out", never read back and never parsed by this package; a
// person who looks at it shows it through leadtext.
//
// # Integration
//
// [Integrate] is the last step. It inspects again (a fresh [Inspect], which
// reads the refs as they are now), and goes on only when the fresh review's
// tip and digest are the ones the owner signed ([ErrMoved] otherwise, naming
// the old and the new ids), the sign-off passes [Review.CheckSignOff], and the
// newest verification record in the store that counts for this review is a
// clean pass ([ErrNotVerified] otherwise, so a later failed run withdraws an
// earlier pass; a pass whose output was cut, or that was stopped, is not
// clean). Only the records of the review's mode count: for a lead branch the
// records of the project and the tip that are not merge records, and for a
// merge the merge-mode records whose merge is the tip and whose base is the
// review's base (see Merge mode). Then it moves the upstream branch to the tip with one
// compare-and-swap, git update-ref in a transaction on standard input that
// expects the target to be at the base the review saw, and writes an
// [IntakeRecord]. The move is fast-forward only: Inspect has refused a tip
// that does not contain the target. It never creates a branch (a missing
// target is [ErrNoRef]), never deletes, never forces, and writes no ref but
// the one target, which must be a branch under refs/heads/. The mirror's HEAD,
// tags and refs/leads/ are never targets. No reflog message is written; the
// intake record is the record. If the branch moved and the record cannot be
// written, the error is a [RecordError] that says so and carries the record.
// A refused update (git exits with a status before it has prepared the
// transaction) moved nothing and writes no record, even when the branch is
// found at the tip, put there by another writer: the error is [ErrMoved]. Only
// an update that ended unclearly (a time-out, a cancel, a kill, or an error
// after the transaction was prepared) can be found at the tip and recorded;
// the record then comes back with an error that matches [ErrRecovered], and
// when the branch cannot be read afterwards the error matches
// [ErrOutcomeUnknown] and the owner looks at the branch.
//
// The integration moves a branch in the bare mirror only. This package runs
// no checkout, merge, pull, reset, worktree or push, and the mirror has no
// index and no work tree. The owner's working checkout is not touched: it gets
// the new commits later, by the owner's own fetch from the mirror.
//
// # What a sign-off is, and is not
//
// After the sign-off the code runs as the owner, as any merged code does. The
// review at intake is the boundary: what the owner signs is what enters
// the history. A verification pass is evidence, not proof (see package
// verify). Any passing record of the review's mode for the project and the tip
// (for a merge, on the review's base) counts, whatever gates it ran: this
// package does not compare the record's plan (gates) with the project's
// current gates or the record's lead ref with the review's. The
// gates that were run are in the [IntakeRecord] that [Integrate] returns, in
// its Verification field. The command that calls [Integrate] must show them
// to the owner before the sign-off (with the review output) and after it (with
// the result). This package cannot tell who typed the sign-off. The lead cannot
// run it, because the mirror is read-only in its sandbox and the records
// folder is not mounted there; that is a property of the sandbox plan and of
// the command that calls this package, not something this package checks.
//
// # Merge mode
//
// A lead never supplies a merge. When a feature is to be finished, the host
// builds the merge of the feature into the target itself (package mergebuild,
// which this package does not import) and pins it under [PendingPrefix]. A
// [Request] with Merge set reviews that merge: BaseRef is the target branch,
// whose tip is the base, and TipRef is the pin. Everything above holds, with
// "tip" meaning the merge. In addition:
//
//   - The tip must be a commit of exactly two parents whose first is the base
//     ([ErrNotMerge] otherwise; a first parent that is not the target's tip is
//     a *[MergeBaseError], and [Integrate] reports it as [ErrMoved], since the
//     target moved after the merge was built).
//   - The review is of base..merge, so it holds the feature's commits and the
//     merge, and the net change is what the merge brings in, never the
//     target's own earlier changes.
//   - The repository's "info/attributes" is checked ([gitenv.CheckInfoAttributes],
//     [ErrAttributes]) before each pinned diff: by Inspect, [Diff] of a merge
//     review and [Summarize]. Lead mode does not run it, as before.
//   - A path that is odd gets the flag [FlagOddPath].
//   - Only a merge-mode verification record counts for the merge, one whose
//     tip is the merge and whose base is the review's base. A lead-mode record
//     of the same commit id, and a merge record made against another base, are
//     no evidence and do not withdraw a pass. The host adapter of the owner
//     channel (package hostadapter, tagged build) makes these records, one for
//     each run of the suite on the merge, and counts them by the same rule.
//   - [Integrate] moves the target from the base to the merge, with the same
//     compare-and-swap, and nothing else: the pin stays (the host removes it),
//     and the lead's branches, tags and HEAD are not touched.
//
// The lead mode is unchanged: a [Request] without Merge takes a tip under
// refs/leads/ only, and a pin is refused there as a ref name.
//
// # The summary
//
// [Summarize] gives the fuller account that the owner's card shows, from the
// ids of a review: the number of changed paths, the lines added and removed,
// the bytes of new content, a row for each path, and the lists of binary
// files, symbolic links, mode changes, submodules and odd paths. A path is
// binary by content, not by git's heuristic or an attribute: a NUL byte
// anywhere in either side, or a side over [TextCap] (1 MiB). A binary path has
// its sizes and no line counts. The lines of every other path come from git
// diff --numstat with renames off, so a rename is a deletion and an addition,
// both counted. [Summary] is raw lead text in its paths.
//
// # Odd paths
//
// [OddPath] says why the execution-config matcher cannot be trusted with a
// path: bytes that are not UTF-8, a control or invisible character, a colon, a
// "~" and a digit, or a form that is not NFC. The module carries no
// normalization tables, so the NFC test is a superset (it also flags a path
// with a combining mark that is in form C). Merge mode flags such a path
// [FlagOddPath], so it needs its own sign-off whatever the matcher says of it.
//
// # What it does not do
//
// It does not run the gates (package verify does), build a sandbox, decide
// who signs, or touch a working checkout. The alias check resolves a
// symlink's target by its text, against the symlinks of the same tree: the
// tip's tree for the net change, and the base's tree as well as the tip's for
// the history pass. It follows a symlink that the target passes through, and
// one that is its last element, but at most 8 symlinks in all. A longer
// chain or a loop is not followed; it is flagged on the symlink itself, when that
// symlink is changed. It does not check the content of a file, only the path
// and the kind of each entry. Matching of paths is the job of the
// execconfig package, with the limits its comment lists.
package intake
