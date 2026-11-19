// Package mergebuild builds the merge commit of a feature on the host, and
// pins it.
//
// A lead never supplies a merge. When a lead asks for a feature to be
// finished, the host merges the feature's tip into the target branch's tip
// itself, in a bare repository only the host can write, and the owner is
// shown that merge and no other. This package is that step, and nothing
// around it: it moves no branch, reads no record and knows no request beyond
// the id it writes into the message.
//
// # What Build does
//
// [Build] is given the two tips as full object ids, the names that go into
// the message, an identity, the time the request was collected, and a key
// the host made. It runs git only through [gitenv] and the bounded runner
// ([boundedexec]), so every run has the pinned environment and a deadline:
//
//   - "merge-tree --write-tree", in the one form gitenv allows, computes the
//     merged tree with no working tree. No attribute is read from any tree
//     (in a SHA-1 repository, see below), so a ".gitattributes" a lead
//     committed cannot make a conflicting merge end cleanly or change what
//     a clean one holds. The repository's own "info/attributes" cannot be
//     switched off, so the package refuses a repository in which it is not
//     absent or empty, before git runs.
//   - A merge with a conflict is refused with the code "merge-conflict", and
//     no commit and no ref is written. The lead resolves the conflict in its
//     feature, where a review sees it as ordinary commits.
//   - "hash-object -tcommit -w --stdin" writes the commit from a text the
//     package wrote in full.
//   - "update-ref" creates the pin, the ref [PinPrefix] and the key, at the
//     commit. The commit has both tips as parents, so the pin keeps all three
//     even when the lead rewrites its branch. [Unpin] removes it.
//
// # The commit text
//
// The text is the same for the same values, byte for byte:
//
//	tree <the merged tree>
//	parent <the target's tip>
//	parent <the feature's tip>
//	author <name> <<address>> <seconds> +0000
//	committer <name> <<address>> <seconds> +0000
//
//	Merge feature/<feature> into <target>
//
//	Request-Id: <request id>
//
// The author and the committer are the one identity the host supplies, and
// both dates are the collected time in seconds since 1970, in UTC; the clock
// is not read. Nothing in the text comes from a commit, a tree or a branch a
// lead wrote. The only bytes a lead chose are the feature's name and the
// request id, and both have passed the grammar of the record package
// (lower-case letters, digits and dashes) before they are written. So a
// feature's commits may carry any author, date, header or message: none of
// it reaches the merge commit's own headers and message.
//
// Every value is checked before any text is written, and a value that fails
// is never repeated in the error. Object ids must be full ids of the
// repository's own object format; the package asks the repository for its
// format once per call and refuses an id of the other length, which git
// would read as the name of a ref.
//
// # Object format
//
// [Build] and [Check] serve SHA-1 repositories only and refuse any other
// ([ErrObjectFormat]), before git merges anything. The reason is the
// attribute pin: [gitenv] pins the attribute source to the SHA-1 empty
// tree, an id that a SHA-256 repository reads as a name, so a ref of that
// name (a tag or a branch from upstream) would become the attribute source
// and could bend the merge. The refusal ends when the pinned environment
// pins the empty tree of the repository's own format. [Unpin] merges
// nothing and works in either format.
//
// # What it does not do
//
// It does not decide whether the merge may happen, does not review it, and
// does not move the target. It does not stop the repository's maintenance
// from pruning: the pin is what keeps the commit, and whoever runs
// maintenance must not remove pins. The check of "info/attributes" is a
// check and not a fence, as [gitenv.CheckInfoAttributes] says.
package mergebuild
