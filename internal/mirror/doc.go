// Package mirror is the exchange mirror: the bare git repository through which
// work leaves a managed lead.
//
// # Why
//
// A managed lead works in its own clone inside a sandbox. Owner-side code
// never runs git in that clone and never trusts what is in it. Ataniq keeps
// one bare repository for each project, the exchange mirror, at
// <state>/mirrors/<project>.git. The lead's branches are fetched into it
// (a later part of this package). The mirror is bound read-only into the
// lead's sandbox, so the lead can read it and cannot write it.
//
// # Two namespaces
//
// refs/heads/* and refs/tags/* are upstream. Only the owner writes them,
// by a push or by the reviewed intake. refs/leads/<role>/heads/* are a lead's
// branches. They are untrusted. Lead tags are not carried. A lead branch
// named develop and the upstream branch develop are two different refs:
// refs/leads/<role>/heads/develop and refs/heads/develop.
//
// # What this part does
//
//   - Names and paths: [ValidName], [ValidBranch], [Path], [LeadTree],
//     [UpstreamRef], [LeadRef], [LeadPrefix], [LeadRefspec], [SplitLeadRef].
//     The ref name rules are written in Go, without starting git, and are
//     tested against git check-ref-format.
//   - [Create] makes the repository with fixed settings and a marker file.
//     [Open] checks that the folder is still what Create made.
//   - Reading: [Mirror.Upstream], [Mirror.LeadBranches], [Mirror.Roles],
//     [Mirror.Resolve] and [Mirror.Size]. A listing is capped; more refs than
//     the cap is an error, never a cut list. [Mirror.Resolve] takes exact full
//     ref names and does no search.
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
// output of git and no ref name.
//
// # What it does not do
//
// It does not fetch from a lead's clone, does not decide which branches are
// taken in, does not write upstream refs, does not delete or prune, and does
// not run garbage collection. Code for systems other than Linux is a stub
// whose calls return an "unsupported" error.
package mirror
