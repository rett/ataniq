- The daemon serves the host's side of the guided new-project interview over
  the seat gate: the capability `genesis` and its one operation,
  `seat.genesis`, with the verbs start, round, answer, change, summary, finish
  and discard. The questions are the host's own, from the versioned catalog,
  and the answers fill a typed project brief and nothing else: an answer is a
  message from the owner and never an approval. The threads are kept by a new
  store (`internal/genesis/thread`), one file each (mode 0600, written whole
  and synced) in `owner/genesis/` of the state folder, a folder the daemon
  makes with mode 0700 when the gate starts and refuses, and so does not start
  the gate, when it is a link, not the daemon user's or writable by group or
  others. At most two threads are open (a third start is `genesis-busy`), eight
  files are kept, a thread untouched for 14 days is discarded, text that looks
  like a key or a password is refused `genesis-secret-shaped`, and a thread
  that is finished is frozen with the digest of its brief. A file that is not as
  the daemon wrote it is not served and is logged once, in fixed text, and
  never repaired; no log line holds an answer. A restart serves the same
  threads. The gate's start line now lists every capability it serves in words,
  `genesis` last (for example `pair, read, login, decide, watch and genesis`).
  The refusal bound counts `genesis-bad-answer`, `genesis-secret-shaped`,
  `genesis-over-cap` and `genesis-unknown-thread`, and not `genesis-busy` or
  `genesis-done`, which answer the state of the store. The seat has no page for
  the interview yet.
