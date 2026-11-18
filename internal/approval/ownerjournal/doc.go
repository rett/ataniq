// Package ownerjournal is the store of what the owner channel must remember
// across a restart of the daemon.
//
// It is a journal: a list of entries that only grows. No entry is ever
// changed or removed; where something ends, a later entry says so and names
// the entry it closes. The store is a file of its own, which the caller
// names. It is not the dispatch journal (internal/journal) and shares
// nothing with it but the SQLite module.
//
// # Entries
//
// Every entry has a sequence number (1, 2, 3, ... in the order written), a
// kind from a fixed list, a time, the pending id and the project it applies
// to (either may be empty where the kind has none), the sequence number of
// the earlier entry it belongs to or closes (0 when there is none), and a
// body: one JSON object in canonical form (keys sorted, no whitespace, the
// escapes \" and \\ only). The kinds are fixed: decision, held, refused,
// delivered, outcome, expired, limit, use, alert, count, activity, looked.
//
// # Append first
//
// An append is durable when it returns: the entry is on the disk, not in a
// cache, before the call comes back. So a caller journals first and acts
// afterwards, and after a crash the journal knows at least as much as
// whatever the caller went on to tell.
//
// This costs one synchronous write to the disk for each append: some
// milliseconds on a solid-state disk and tens of milliseconds on a turning
// one. Appends are made one after the other, so a second caller waits for
// the first. The journal is meant for the few things an owner does in a day
// and for what follows from them, not for anything that happens every
// second.
//
// An append writes all of its entries or none of them. When an append
// returns an error, nothing was written, with one exception: an error from
// the final write itself (the disk failed at that moment) leaves it open
// whether the entries are there, and the queries of this package answer
// that.
//
// # The file
//
// [Open] takes the file's path and has no default. The folder must exist,
// must belong to the user the process runs as, and must not be reachable by
// the group or by others; the same holds for the file, which is created
// with mode 0600 when it is not there. A new file appears under its name
// only once it is a whole, empty store, so a crash while a store is created
// leaves no half-made store behind. A file of zero bytes, a file cut short
// and a file of another kind are refused with [ErrDamaged]; the package
// never repairs a store and never starts a new one over a file that is
// there.
//
// One process holds a store at a time. A second Open of the same file,
// from this process or another, is refused at once with [ErrLocked]; it
// does not wait. The hold ends with [Store.Close], or with the process.
//
// A store written by a newer version of this package is refused with
// [ErrNewerSchema].
//
// # What Open checks
//
// Each entry carries a digest over its own fields and the digest of the
// entry before it. Open reads every entry, checks that the sequence numbers
// run from 1 without a gap and that every digest holds, and asks SQLite to
// check the file's structure. The time this takes grows with the number of
// entries. The digests find damage: a changed, missing or half-written
// entry. They are not a signature: whoever can write the file can write
// digests that hold, and entries lost from the end of the file leave a
// shorter journal that still holds. What protects the journal from being
// written is the folder's mode and, in the design, the sandbox that keeps a
// lead away from it.
//
// # Since he last looked
//
// The looked entries hold one mark for every surface: the time up to which
// the owner has seen what happened. [Store.AppendLooked] moves it forward
// and [Store.LookedAt] returns it. The mark never moves back.
//
// The package is built for Linux only, like the dispatch journal: both rely
// on the same file locks, and one process may hold both stores.
package ownerjournal
