// Package thread is the thread store of the guided new-project interview: the
// daemon's own record of the interviews the owner has begun on the phone, and
// the one writer of them. The threads are the daemon's, not a seat's: every
// pinned seat of the owner reaches the same ones, and the cap on open threads
// is for all of them together.
//
// A thread is a project brief (package brief) being filled under one version
// of the question catalog (package catalog), with an id, a state, and the
// times it was made and last touched; a finished thread may hold the plan of
// its brief (package plan). The store implements the nine verbs of the wire's
// union (package ownerwire, SeatGenesisParams): [Store.Start], [Store.List],
// [Store.Round], [Store.Answer], [Store.Change], [Store.Summary],
// [Store.Finish], [Store.Discard] and [Store.Plan], and [Store.Genesis],
// which takes the params as the gate decoded them and hands them to the verb.
// Every verb answers the wire's result. An answer is a message from the
// owner: it fills a brief field and nothing else, and a plan is a proposal:
// nothing here reaches a decision, a contract or an approval.
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
// whitespace), with a digest of its own content. A finished thread's file may
// hold its plan, the member plan (the plan record in its canonical form) with
// plan_digest; a file without them is as valid as one with them. It is
// written whole, to a
// temporary file in the same folder that is synced, renamed over the thread's
// file and followed by a sync of the folder, so a crash leaves the old thread
// or the new one and never half of one. A restart serves the same state.
//
// A file is read strictly. One that is not a regular file of the daemon's
// user and mode 0600, that does not decode, is not in its canonical form,
// whose id is not its name, whose brief does not decode or was started under
// another catalog than the thread's, whose digest does not match, or whose
// plan is on a thread that is not finished, does not decode and pass the plan
// record's own checks, does not match its digest, or was not drawn from the
// thread's frozen brief and catalog, is not served: the log says so once, in a fixed text that names the file by its
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
//     round. An answer to a round the thread has already taken, to a node
//     already answered in the round named (a round asked again after a
//     change keeps the last round's number), or to any round once the walk
//     is done, is refused genesis-stale-round (the
//     thread's state, which a second tab or a post sent twice meets); one to
//     a round it has not reached, genesis-bad-answer. A change must name a
//     node that has an answer. One answer failing fails the whole call and
//     changes nothing. Text that looks like a secret is refused
//     genesis-secret-shaped.
//   - List answers the open threads, oldest first, each with its state
//     (question or summary), round and progress, and nothing of its answers;
//     a finished or discarded thread is not listed. It is a read, as Round
//     and Summary are: it touches no thread. It lists at most [MaxOpen], the
//     oldest, and never fails for more: a folder may hold more open threads
//     (files restored by hand), the newer ones are served by id, and Start is
//     refused genesis-busy while [MaxOpen] or more are open.
//   - A finished thread is frozen: an answer, a change or a discard is
//     refused genesis-done, and its brief digest is the one finish returned. Finish
//     may be called at any time on an open thread (the owner's "enough"), and
//     again on a finished one, which answers the same digest.
//   - A brief is complete for planning when every required field of its
//     kind is set: an answer decided for the owner (a "you decide" answer the
//     host inferred) counts whether or not he has confirmed it, and the plan
//     drawn from it carries the warning inferred-answers. The progress of a
//     result (filled of total) and plan_ready follow that one notion.
//   - Finish of a brief that is complete, of a kind a template serves,
//     compiles its plan, revision 1, and stores it with the frozen brief in
//     the one write; a brief that is not, or of a kind no template serves,
//     is frozen without a plan. Plan answers a preview on an open thread
//     whose brief is complete (compiled for the read, stored nowhere), and
//     the stored plan on a finished one; a finished thread with none whose
//     brief is complete (finished before plans were stored) is given its
//     plan on the first ask, stored once, and only when it is the plan of the
//     thread's frozen brief (else [ErrPlanBrief], nothing kept). A brief that
//     is not complete is refused genesis-not-ready, a kind no template serves
//     genesis-no-template. A stored plan is never compiled again: a template
//     that changes later never alters it, and a plan of a template no longer
//     embedded is served as it was stored. Its plan goes with a thread's
//     file when the file is pruned.
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
// content: a thread was started, finished, discarded or expired, a plan was
// drawn (stored) or could not be drawn at finish, an answer was refused as
// secret-shaped (with how many were since the store opened), and a file is not
// served. No line holds anything of a plan.
package thread
