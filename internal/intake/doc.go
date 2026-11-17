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
// ref that moves during the review cannot mix two states. The package
// writes nothing to the repository, checks nothing out, and opens no file of
// the repository except through git. The repository must be a bare
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
// # What it does not do
//
// It does not sign anything off, keep records, write refs or merge. The alias
// check resolves a symlink's target by its text, against the symlinks of the
// tip's own tree: it follows a symlink that the target passes through, and one
// that is its last element, but at most 8 symlinks in all. A longer chain or
// a loop is not followed; it is flagged on the symlink itself, when that
// symlink is changed. It does not check the content of a file, only the path
// and the kind of each entry. Matching of paths is the job of the
// execconfig package, with the limits its comment lists.
package intake
