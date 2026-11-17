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
//     submodule, or that lies in a folder an aliasing symlink stands for, in
//     the change of some commit of the branch, but that is not in the net
//     change. It was added and removed again (or changed and changed back)
//     inside the branch. It enters upstream history, so it needs a sign-off
//     too. For a merge commit only the paths that differ from every parent
//     count: a path that equals one parent was brought in by that parent.
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
// the tip ref under refs/leads/.
//
// # Text from the lead
//
// Everything a lead wrote is hostile: paths, commit subjects, author names,
// diff content, symlink targets. The structs of this package ([Commit],
// [Change], [Flag]) hold that text raw, byte for byte, so that a signed
// digest and a later comparison are exact. A caller that shows a field must
// escape it for display first with the leadtext package, and must show
// leadtext.Label near it. The reason of a flag is the package's own text; lead
// text inside it (a path, a link target) is already escaped with
// leadtext.Escape. Error messages carry no output of git and no lead text
// except escaped ref names.
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
// record. Records are read through safefs: regular files only, at most 1 MiB,
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
// newest verification record in the store for this project and tip is a clean
// pass ([ErrNotVerified] otherwise, so a later failed run withdraws an
// earlier pass; a pass whose output was cut, or that was stopped, is not
// clean). Then it moves the upstream branch to the tip with one
// compare-and-swap, git update-ref in a transaction on standard input that
// expects the target to be at the base the review saw, and writes an
// [IntakeRecord]. The move is fast-forward only: Inspect has refused a tip
// that does not contain the target. It never creates a branch (a missing
// target is [ErrNoRef]), never deletes, never forces, and writes no ref but
// the one target, which must be a branch under refs/heads/. The mirror's HEAD,
// tags and refs/leads/ are never targets. No reflog message is written; the
// intake record is the record. If the branch moved and the record cannot be
// written, the error is a [RecordError] that says so and carries the record.
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
// verify). This package cannot tell who typed the sign-off. The lead cannot
// run it, because the mirror is read-only in its sandbox and the records
// folder is not mounted there; that is a property of the sandbox plan and of
// the command that calls this package, not something this package checks.
//
// # What it does not do
//
// It does not run the gates (package verify does), build a sandbox, decide
// who signs, or touch a working checkout. The alias
// check resolves a symlink's target by its text, against the symlinks of the
// tip's own tree: it follows a symlink that the target passes through, and one
// that is its last element, but at most 8 symlinks in all. A longer chain or
// a loop is not followed; it is flagged on the symlink itself, when that
// symlink is changed. It does not check the content of a file, only the path
// and the kind of each entry. Matching of paths is the job of the
// execconfig package, with the limits its comment lists.
package intake
