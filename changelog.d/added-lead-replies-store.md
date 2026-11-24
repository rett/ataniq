- Under the canary tag, `internal/leadchat` holds the daemon's side of a
  scratch lead's written replies to the owner, read only and not wired into
  the daemon or the seat yet: a reader of the lead's outbox
  (`/home/lead/.ataniq-replies`, one plain-text file per reply, renamed from a
  dot-name to `NAME.txt`), reached by descriptor from the state root without
  following a link, that takes only a settled regular file of the daemon's
  user with one link and at most 2000 bytes, removes links, FIFOs, sockets,
  devices, hard links and badly named files, reads at most 256 entries a scan,
  keeps at most 32 replies waiting and takes at most 6 a minute and 60 an hour;
  and a chat store per lead (`<state root>/chat/<layout role>.jsonl`, strict
  synced JSON lines, at most 8 MiB, locked and checked by handle, that
  replays after every compaction) that stores each reply
  exactly once, drops text that is not plain or too long, withholds text
  shaped like a key, a token or a full commit id, keeps 500 entries or 14
  days, and has a per-lead switch `off` or `read` that clears the outbox when
  turned on, deletes the stored replies when turned off, and is off again
  after every restart. See `docs/scratch-leads.md`, "Lead replies".
