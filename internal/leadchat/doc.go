//go:build linux && ataniq_canary

// Package leadchat is the daemon's side of a scratch lead's replies to the
// owner, read only: the outbox reader, which takes the replies a lead writes
// into a folder of its own home, and the chat store, which keeps them for
// the seat. Nothing here sends to a lead, and nothing here is wired into the
// daemon or the seat yet.
//
// # The outbox
//
// A lead replies by writing one plain-text file per reply into
// /home/lead/.ataniq-replies, which is <state root>/leads/<layout
// role>/home/.ataniq-replies on the host: the lead's own writable home, so no
// new bind is needed. It writes under a name that starts with a dot and then
// renames the file to [a-z0-9][a-z0-9._-]{0,59}.txt ([ValidReplyName]). The
// daemon assigns the sender, the sequence number and the time.
//
// The store reaches the folder from the state root's descriptor ([Root]),
// one name at a time, each opened by openat2 beneath the descriptor with
// O_DIRECTORY and O_NOFOLLOW, no symbolic or magic link and no mount point
// crossed. There is no fallback: a kernel without openat2, or a filter that
// denies it, leaves the outbox unavailable ([ErrUnavailable]) and chat cannot
// be turned on. The folders on the way are of the daemon's user and group and
// other cannot write them; the outbox itself is of the daemon's user with
// mode 0700 exactly. No path below the state root is opened by name.
//
// The outbox is bound to the turn-on: [Store.SetSwitch] makes it (mode 0700)
// when it is not there, verifies it, and records its device and inode, and
// every scan checks that the outbox is that folder still. An outbox that is
// gone, replaced, not private or closed to the daemon stops the lead's chat
// (suspended) with the one entry [TextStoppedFolder]; what the chat holds
// can still be read, and only a fresh turn-on takes replies again.
//
// A scan reads the outbox's names and judges each by fstatat without
// following it, before it changes anything. An outbox of more than
// [MaxDirEntries] entries, or of more than [MaxWaiting] replies and folders
// (folders are never removed, so they count for good), is not touched: the
// chat is suspended with the one entry [TextStoppedFull], and no part of it
// is processed. Otherwise:
//
//   - a folder is left and counted (folder);
//   - an entry whose change time is before the moment the turn-on found the
//     outbox empty was written before chat was on: it is removed (before);
//   - a file whose name starts with a dot is a reply being written: it is
//     left, and removed when its change time is older than [StaleAge]
//     (stale);
//   - any other entry without a reply name is removed (name);
//   - an entry with a reply name is a reply, claimed when the kernel's change
//     time is at least [SettleAge] old (a lead cannot set it, and the rename
//     sets it), within the rate.
//
// # Claims
//
// The scan never acts on a lead's name after one rename: every entry it
// takes or removes is first claimed, renamed by renameat2 with
// RENAME_NOREPLACE, from the outbox's descriptor to the lead's claim folder
// <state root>/chat/<layout role>.claims (mode 0700, on the same file
// system, which no sandbox binds), under a name the daemon chooses: r<id>
// for a reply and x<id> for an entry to remove, the id from a per-lead claim
// counter that only grows. What the lead does to the name afterwards (a new
// reply under the same name included) is a new entry for a later scan. A
// kernel or file system that cannot rename so leaves the outbox unavailable.
//
// A claimed reply is judged in the claim folder, where only the daemon
// renames: one that is not a regular file is removed and never opened (a
// folder with what it holds, at most 16 deep and 8192 entries); a regular
// file is opened with O_NOFOLLOW and O_NONBLOCK and judged on its handle (a
// file of the daemon's user, one link, no write bit for group or other, at
// most [MaxReplyBytes]; a reply of another user, with more links or that
// others may write is removed, refused, and one over the size, too-long),
// read, and judged again. A claim that is not the entry the scan saw settle
// (another inode, size or modification time) is read only once its own
// change time is [SettleAge] old, and one that changed while it was read, or
// that a lease holds, is left for a later scan; until it is read, nothing
// newer is claimed.
//
// # Exactly once
//
// [Store.Ingest] stores a reply's entry with its claim id in a synced append,
// and only then removes the claim. The claim folder is read first by every
// scan: a reply claim whose id is at most the highest id stored was stored
// and is only removed (repaired); a reply claim above it was claimed and
// never stored, and is read and stored once; a claim to remove is removed.
// So a crash between the claim and the append, between the append and the
// removal, or a removal that fails, stores each reply once, and the same
// name with the same bytes written twice is two replies. After a restart the
// chat is off, and the next turn-on removes every claim and clears the
// outbox, so nothing of before is taken.
//
// # The text
//
// [ReplyText] drops a reply over [MaxReplyBytes] (never cut), trims its
// trailing line feeds, and drops what is not plain text by
// [ownerwire.ValidChatText]. A text that [brief.SecretShaped], weighed on the
// whole text, finds shaped like a key, a token or a full commit id is not
// stored: the entry is the fixed system text [TextWithheld]. A drop the owner
// is told about writes a fixed system entry, at most one per reason a
// minute. No entry holds a file name, a path or a count.
//
// # The store
//
// A lead's chat is <state root>/chat/<layout role>.jsonl, mode 0600, in a
// folder of mode 0700 that no sandbox binds. It is strict JSON lines: each
// line valid UTF-8 and at most [MaxLineBytes] with its newline, one object
// whose members are each named once and none null, of exactly the members of
// its kind. The first line is {"kind":"open","v":1,"next":N,"claim":C}, the
// allocation high-waters: no sequence number below N and no claim id up to
// C is given again. A switch
// line {"kind":"switch","chat":"read|off","gen":G,"at":T,"epoch":E,"discarded":N}
// records each change of the switch with its generation, and a turn-on
// also "since" (the moment the outbox was found empty) and "outbox" (its
// "device:inode"); an entry line
// {"kind":"entry","seq":S,"gen":G,"epoch":E,"who":"lead|system","at":T,"text":"...","claim":C}
// records one entry, with the claim id of the reply it was stored from (a
// lead's entry and [TextWithheld] have one, no other entry has). Entries are checked in their own increasing order, so a
// compacted file (the open line's floor above the entries it kept) and the
// appends after it replay alike; every segment starts with the entry
// [TextTurnedOn], and a system entry holds one of the fixed texts. The file
// is opened by handle (a regular file of the caller, mode 0600, one link, no
// link followed) and held with an exclusive, non-blocking flock. Every
// append is synced before the call returns. A last line without its newline
// is cut off; any other damage makes [OpenStore] fail with [ErrCorrupt],
// whose text is a fixed reason and the line's number and never a byte of
// the file, and nothing is repaired, so that lead has no chat (fail closed)
// while the others are untouched. A write or sync that fails breaks the
// store ([ErrBroken]). Sequence numbers only grow: a new one is one above the
// highest given, or above the open line's floor. A compaction writes a
// temporary file, syncs it, renames it over the file and syncs the folder;
// a temporary file left by a crash is removed.
//
// The file is at most [MaxJournalBytes]: a replay refuses a larger one
// before it reads a line, and keeps no entry while it reads (every lead
// opens off), and an append that would pass the bound compacts first and is
// refused ([ErrFull]) if it still would.
//
// The store keeps the newest [RetainEntries] entries, none older than
// [RetainAge]; the latest entry that says chat was turned on is never
// expired. Everything it keeps is in memory, so [Store.Read] touches no
// file. A reader's cursor is the sequence number of the last entry it was
// given: a cursor that is not an entry held now (one of an earlier segment,
// past the head, or in a gap that expired) is answered with everything held
// and reset set.
//
// # The switch
//
// A lead's switch is off or read ([Store.SetSwitch]); send is not built. It
// is effective at once and every call checks it. Off to read verifies the
// outbox as above, removes the claims an earlier segment left, and clears
// the outbox: every entry that is not a folder is claimed and removed, and
// the outbox is listed again until a listing finds none (at most 8192
// entries a listing, at most 8 listings, and at most [MaxWaiting] folders;
// otherwise [ErrFull]). Then it writes the switch line, with how many files
// the clear removed, the outbox's identity and the moment the outbox was
// found empty by the kernel's coarse clock (the clock of change times, to
// the nanosecond), and the entry [TextTurnedOn]. Anything that fails leaves
// the switch off. So no reply written before the turn-on is shown: what the
// clear did not see has a change time before that moment and is removed
// unread. Read to off turns the switch off in memory and drops the lead's
// entries by a compaction. The switch is never restored from disk:
// [OpenStore] opens every lead off, dropping what an earlier run left on,
// and the next turn-on starts an empty segment.
package leadchat
