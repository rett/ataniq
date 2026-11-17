// Package mirror is the exchange mirror: the bare git repository through which
// work leaves a managed lead.
//
// # Why
//
// A managed lead works in its own clone inside a sandbox. Owner-side code
// never runs git in that clone and never trusts what is in it. Ataniq keeps
// one bare repository for each project, the exchange mirror, at
// <state>/mirrors/<project>.git. The lead's branches are fetched into it.
// The mirror is bound read-only into the lead's sandbox, so the lead can read
// it and cannot write it.
//
// # Two namespaces
//
// refs/heads/* and refs/tags/* are upstream. Only the owner writes them,
// by a push or by the reviewed intake. refs/leads/<role>/heads/* are a lead's
// branches. They are untrusted. Lead tags are not carried. A lead branch
// named develop and the upstream branch develop are two different refs:
// refs/leads/<role>/heads/develop and refs/heads/develop.
//
// # What it does
//
//   - Names and paths: [ValidName], [ValidBranch], [Path], [LeadTree],
//     [UpstreamRef], [LeadRef], [LeadPrefix], [LeadRefspec], [SplitLeadRef].
//     The ref name rules are written in Go, without starting git, and are
//     tested against git check-ref-format.
//   - [Create] makes the repository with fixed settings and a marker file.
//     [Open] checks that the folder is still what Create made. Both take
//     options, for the limits: [UseLimits].
//   - Reading: [Mirror.Upstream], [Mirror.LeadBranches], [Mirror.Roles],
//     [Mirror.Resolve] and [Mirror.Size]. A listing is capped; more refs than
//     the cap is an error, never a cut list. [Mirror.Resolve] takes exact full
//     ref names and does no search.
//   - Fetching: [Mirror.Fetch] brings a lead's branches from its clone into
//     the lead's namespace of the mirror.
//
// # The fetch
//
// The lead's clone is at <state>/leads/<role>/tree and the lead writes all of
// it, ".git" included. Owner-side code never runs git in it. A fetch from a
// local path starts git-upload-pack in the source, which has a large attack
// surface and reads the source's configuration. So the serving half runs in
// a sandbox, with no network and the clone read-only, named with git's
// --upload-pack option, and only the receiving half runs as the owner. The
// sandbox is reached through a [sandboxplan.Planner]; a fetch without one does
// not exist, and there is no fallback that runs unsandboxed.
//
// The fetch goes in two moves. The receiving half fetches into a staging
// repository, a fresh bare repository beside the mirror that has the mirror's
// objects as its only alternate. What arrived is checked there: sizes, the
// number of refs, the names and types of the refs, and that the repository is
// still what this package made. Only then do the branches move from the
// staging repository into the mirror, in one atomic update of refs, and the
// refs outside the lead's namespace are compared with the snapshot taken
// before. The fetch takes the
// lead's branches only (+refs/heads/*:refs/leads/<role>/heads/*) and no tag,
// so a lead's develop or tag can never replace an upstream one. Two fetches
// into one mirror cannot overlap: a lock file beside the mirror makes the
// second one return [ErrBusy].
//
// The limits of a fetch are in [FetchLimits], a type of its own: a time limit
// for the whole fetch, a size cap for the lead's object store as measured in
// the sandbox, for what one fetch adds, for the whole mirror and for the
// number of files, and a cap on the lead's branches. The [Limits] of a
// [Mirror] are another thing: the time limit of each git run and the cap of a
// listing. The size of the mirror after the move is a check and not a quota.
//
// # A failed fetch
//
// A failure before the move leaves the refs and the objects of the mirror as
// they were. The move is atomic, so a move that fails leaves no ref changed.
// If a check after the move fails, the lead's namespace is put back as it was
// before the move, in one transaction, and the error is returned; if that
// fails too, the error says so. The objects that the move brought stay, with
// no ref that reaches them, and are the business of the owner's garbage
// collection, which this package does not run.
//
// After the move, a ref outside the lead's namespace may differ from the
// snapshot taken before, because another owner process, such as an intake or a
// push, may write there while a fetch runs; the fetch takes no lock against
// them. That is a note in [FetchReport.OutsideChanged]. The proof that this
// package can give is narrower: the move names only the lead's namespace, so
// it can write only the values of the staging repository's refs, the tips. A
// ref outside the namespace that holds a tip that this fetch staged (one that
// the mirror lacked before) can only be a bug, and is [ErrUpstreamChanged].
//
// # What a lead's ref says
//
// A ref under refs/leads/<role>/ says that the role offered that commit. It
// does not say that the role authored it, or that its objects came from that
// role's clone. A lead can point a ref at any commit that is in the mirror,
// for example the commit of another lead, because the staging repository has
// the mirror's objects as its alternate and git treats them as already
// there. No check is added, on purpose. The mirror is bound read-only into
// every lead's sandbox, so a lead can read every object in it and could
// fetch and offer those objects itself; a check would protect nothing. Commit
// authorship is lead text in any case. A later step that treats a lead's
// namespace as that lead's own work must not rely on the namespace for
// authorship.
//
// # How it is safe
//
// The folder is checked with safefs: no symlink is followed, and a swap of
// the path after the check changes nothing. Every git run goes through gitenv
// with an explicit --git-dir and through boundedexec with a time limit and
// output caps. Nothing runs in a lead's tree and nothing is checked out. The
// check in [Open] is a consistency check of a repository that Ataniq made; it
// catches a mirror somebody edited by hand, and it is not a defence against
// an attacker who can write the folder.
//
// # Text from the lead
//
// Ref names come from a lead. This package returns them raw and never
// prints, logs or interprets them. A caller that shows a name must escape it
// with the leadtext package first. Error messages from this package carry no
// output of git and no ref name, with one exception: the text of
// [ErrUpstreamChanged] names the refs that it is about, at most ten, quoted
// with strconv.QuoteToASCII, so that it holds only printable ASCII.
// [FetchReport.OutsideChanged] holds names quoted the same way. The updates
// of a fetch ([RefUpdate]) hold raw ref names.
//
// # What it does not do
//
// It does not sandbox anything itself (the planner and its caller do), does
// not decide which of a lead's branches are taken in beyond the name and type
// rules, does not write upstream refs, does not run garbage collection, and
// prunes only the refs of the lead whose branches it fetches. Code for
// systems other than Linux is a stub whose calls return an "unsupported"
// error.
package mirror
