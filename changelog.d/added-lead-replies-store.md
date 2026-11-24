- Under the canary tag, `internal/leadchat` holds the daemon's side of a
  scratch lead's written replies to the owner, read only and not wired into
  the daemon or the seat yet: a reader of the lead's outbox
  (`/home/lead/.ataniq-replies`, one plain-text file per reply, renamed from a
  dot-name to `NAME.txt`), reached by descriptor from the state root with
  `openat2` and no fallback, without following a link or crossing a mount
  point, that moves each file it takes or removes into a claim folder of the
  daemon's by one rename that replaces nothing and reads only that claim,
  takes only a settled regular file of the daemon's user with one link, no
  write bit for group or other and at most 2000 bytes, removes links, FIFOs,
  sockets, devices, hard links and badly named files, stops the lead's chat
  with one line when the outbox holds more than 256 entries or more than 32
  replies and folders, or is replaced, removed or no longer private, and takes
  at most 6 a minute and 60 an hour; and a chat store per lead
  (`<state root>/chat/<layout role>.jsonl`, strict synced JSON lines, at most
  8 MiB, locked and checked by handle, that replays after every compaction)
  that stores each reply exactly once by its claim id, drops text that is not
  plain or too long, withholds text shaped like a key, a token or a full
  commit id, keeps 500 entries or 14 days, and has a per-lead switch `off` or
  `read` that, when turned on, makes or verifies the outbox (mode 0700),
  clears it until it stays empty and removes unread whatever was written
  before, deletes the stored replies when turned off, and is off again after
  every restart. See `docs/scratch-leads.md`, "Lead replies".
