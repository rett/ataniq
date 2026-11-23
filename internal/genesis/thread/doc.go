// Package thread is the thread store of the guided new-project interview: the
// daemon's own record of the interviews the owner has begun on the phone, and
// the one writer of them.
//
// A thread is a project brief (package brief) being filled under one version
// of the question catalog (package catalog), with an id, a state, and the
// times it was made and last touched. The store implements the seven verbs of
// the wire's union (package ownerwire, SeatGenesisParams): [Store.Start],
// [Store.Round], [Store.Answer], [Store.Change], [Store.Summary],
// [Store.Finish] and [Store.Discard], and [Store.Genesis], which takes the
// params as the gate decoded them and hands them to the verb. Every verb
// answers the wire's result. An answer is a message from the owner: it fills a
// brief field and nothing else, and nothing here reaches a decision, a
// contract or a plan.
//
// # The folder
//
// [Open] takes the folder the threads live in, <owner state folder>/genesis.
// It makes it, mode 0700, when it is not there, and refuses a folder that is a
// link, is not the daemon user's, or is writable by group or other. The store
// reaches nothing outside that folder: every file is opened through an
// [os.Root] on it, which cannot leave it by a link or by "..". It never
// executes anything.
//
// A thread is one file, <16 lower-case hexadecimal digits>.json, mode 0600, in
// the canonical form of package brief's strict JSON (keys sorted, no
// whitespace), with a digest of its own content. It is written whole, to a
// temporary file in the same folder that is synced, renamed over the thread's
// file and followed by a sync of the folder, so a crash leaves the old thread
// or the new one and never half of one. A restart serves the same state.
//
// A file is read strictly. One that is not a regular file of the daemon's
// user and mode 0600, that does not decode, is not in its canonical form,
// whose id is not its name, whose brief does not decode or was started under
// another catalog than the thread's, or whose digest does not match, is not
// served: the log says so once, in a fixed text that names the file by its
// name (16 hexadecimal digits at most), and the file is left as it is. The
// store never repairs a file, and never overwrites one it did not read: its
// id is spent.
//
// # The rules
//
//   - At most [MaxOpen] threads are open: a third start is refused
//     genesis-busy. At most [MaxFiles] files are kept; when a start would
//     pass it, the oldest finished or discarded threads are removed.
//   - A thread untouched for [Expiry] (14 days) is discarded, on the next
//     access of any thread or when the store is opened. A discarded thread
//     keeps no answer: its brief is emptied. Every call to it is
//     genesis-unknown-thread, as one never started.
//   - An answer must name the round the thread is at, and a node of that
//     round. A change must name a node that has an answer. One answer failing
//     fails the whole call and changes nothing. Text that looks like a secret
//     is refused genesis-secret-shaped.
//   - A finished thread is frozen: an answer or a change is refused
//     genesis-done, and its brief digest is the one finish returned. Finish
//     may be called at any time on an open thread (the owner's "enough"), and
//     again on a finished one, which answers the same digest.
//
// # Concurrency and the clock
//
// One mutex serialises every call of a store. A call holds it for its own
// computation and the one file write of its change (a start that removes old
// files does so after it let go); it reads no folder and runs nothing. The
// clock and the source of ids are fields of [Options], so a test fixes both.
//
// # The log
//
// The store's log lines are fixed texts that hold no answer and no brief
// content: a thread was started, finished, discarded or expired, an answer
// was refused as secret-shaped (with how many were since the store opened),
// and a file is not served.
package thread
