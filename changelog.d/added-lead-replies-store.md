- Under the canary tag, `internal/leadchat` holds the daemon's side of a
  scratch lead's written replies to the owner, read only and not wired into
  the daemon or the seat yet: a reader of the lead's outbox
  (`/home/lead/.ataniq-replies`, one plain-text file per reply, closed and
  renamed from a dot-name to `NAME.txt`), reached by descriptor from the state
  root with `openat2` and no fallback, without following a link or crossing a
  mount point, that moves each reply it takes into a claim folder of the
  daemon's by one rename that replaces nothing and reads only that claim,
  takes only a settled regular file of the daemon's user with one link,
  readable by its owner, no write bit for group or other and at most 2000
  bytes, drops a claim it cannot read whole within three tries or 30 seconds,
  never claims or removes drafts and other names, stops the lead's chat with
  one line when the outbox holds more than 32 entries, holds a folder, a link
  or a special file, or is replaced, removed or no longer private, and takes
  at most 6 a minute and 60 an hour; and a chat store per lead
  (`<state root>/chat/<layout role>.jsonl`, strict synced JSON lines, at most
  8 MiB, locked and checked by handle, that replays after every compaction)
  that stores each claim at most once in a conversation, drops text that is
  not plain or too long, withholds text shaped like a key, a token or a full
  commit id (any bare word of 40 or 64 hexadecimal digits), keeps 500 entries or 14 days, and has a per-lead switch `off` or
  `read` that, when turned on, makes a fresh outbox (mode 0700) in a private
  staging folder (`<state root>/leads/<layout role>/staging`), holds it open
  and proves with a probe there that the file system can claim, sets the
  earlier outbox aside unread, an empty one included, in the lead's quarantine
  (`<state root>/leads/<layout role>/quarantine`, at most four, which the
  owner purges), and publishes the held folder at the outbox's name by a
  rename that replaces nothing, refusing the turn-on when the name was taken
  or replaced meanwhile; it deletes the stored replies when turned off, and
  is off again after every restart. See `docs/scratch-leads.md`, "Lead
  replies".
