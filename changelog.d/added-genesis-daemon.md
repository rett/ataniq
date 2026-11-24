- The daemon serves the host's side of the guided new-project interview over
  the seat gate: the capability `genesis` and its one operation,
  `seat.genesis`, with the verbs start, round, answer, change, summary, finish
  and discard. The questions are the host's own, from the versioned catalog,
  and the answers fill a typed project brief and nothing else: an answer is a
  message from the owner and never an approval. The threads are kept by a new
  store (`internal/genesis/thread`), one file each (mode 0600, written whole
  and synced) in `owner/genesis/` of the state folder, a folder the daemon
  makes with mode 0700 when the gate starts and refuses when it is a link, not
  the daemon user's or writable by group or others. A refused folder does not
  stop the daemon: it logs one line with a fixed reason word (such as
  `folder-link`) and starts the gate without `genesis`, so the phone offers no
  "New project" until the folder is fixed and the daemon restarted. At most two threads are open (a third start is `genesis-busy`), eight
  files are kept, a thread untouched for 14 days is discarded, text that looks
  like a key or a password is refused `genesis-secret-shaped` (a pasted key
  or an obvious assignment such as `password=...`; names, web addresses and
  paths pass, and a bare git commit id is refused), and a thread
  that is finished is frozen with the digest of its brief (an answer, a
  change or a discard of it is `genesis-done`). A file that is not as
  the daemon wrote it is not served and is logged once, in fixed text, and
  never repaired; no log line holds an answer. A restart serves the same
  threads. The gate's start line now lists every capability it serves in words,
  `genesis` last (for example `pair, read, login, decide, watch and genesis`).
  An answer to a round the thread has already taken (a double tap, a second
  tab) is `genesis-stale-round`, the thread's state and not a bad answer. The
  refusal bound counts only `genesis-bad-answer` and `bad-params` of the
  interview, and not `genesis-stale-round`, `genesis-busy`, `genesis-done`,
  `genesis-unknown-thread`, `genesis-secret-shaped` or `genesis-over-cap`, so
  that a few taps never shut the seat out of approvals.
