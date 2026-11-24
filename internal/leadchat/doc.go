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
// [ScanOutbox] reaches the folder from the state root's descriptor ([Root]),
// one name at a time, each opened with O_DIRECTORY and O_NOFOLLOW (by openat2
// beneath the descriptor, with no link and no mount crossed, where the kernel
// has it, and by openat otherwise), each a folder of the daemon's user that
// group and other cannot write. No path below the state root is opened by
// name. It reads at most [MaxDirEntries] names, and judges each by
// fstatat without following it:
//
//   - a symbolic link, a FIFO, a socket or a device is removed, counted
//     refused;
//   - a folder is left and counted, and one with a reply name counts as
//     waiting;
//   - a file whose name starts with a dot is a reply being written: it is
//     left, and removed when its change time is older than [StaleAge];
//   - any other file without a reply name is removed, counted name;
//   - a reply of another user or with more than one link is removed (refused),
//     and one over [MaxReplyBytes] is removed (too-long).
//
// At most [MaxWaiting] replies wait; the excess, in name order, is removed
// (full). A reply is taken only when the kernel's change time is at least
// [SettleAge] old (a lead cannot set it, and the rename sets it). It is
// opened by name below the outbox's descriptor with O_NOFOLLOW and
// O_NONBLOCK, judged again on the handle (the same inode, a regular file of
// the daemon's user, one link, the size), read, and judged a third time: a
// file that changed while it was read is left for a later scan.
//
// # Exactly once
//
// A reply is named by its src, the first 16 bytes of SHA-256 over its inode
// number, its name and its bytes. [Store.Ingest] stores the entry with its
// src in a synced append, and only then removes the file, when its name
// still holds the inode that was read: a lead that replaced the reply in
// between keeps its new file. A crash between the append and the removal is
// repaired at the next scan: a reply whose src is among the newest
// [SrcWindow] entries is removed and not stored again. After a restart the
// chat is off and the next turn-on clears the outbox, so the file is not
// taken again either.
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
// its kind. The first line is {"kind":"open","v":1,"next":N}, the
// allocation high-water: no sequence number below N is given again. A switch
// line {"kind":"switch","chat":"read|off","gen":G,"at":T,"epoch":E,"discarded":N}
// records each change of the switch with its generation; an entry line
// {"kind":"entry","seq":S,"gen":G,"epoch":E,"who":"lead|system","at":T,"text":"...","src":"..."}
// records one entry. Entries are checked in their own increasing order, so a
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
// is effective at once and every call checks it. Off to read clears the
// outbox, so nothing written before is taken, then writes the switch line
// and the entry [TextTurnedOn]. Read to off turns the switch off in memory
// and drops the lead's entries by a compaction. The switch is never restored
// from disk: [OpenStore] opens every lead off, dropping what an earlier run
// left on, and the next turn-on starts an empty segment.
package leadchat
