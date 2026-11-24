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
// new bind is needed. It writes under a name that starts with a dot, closes
// the file, and then renames it to [a-z0-9][a-z0-9._-]{0,59}.txt
// ([ValidReplyName]). The daemon assigns the sender, the sequence number and
// the time.
//
// The store reaches the lead's folders from the state root's descriptor
// ([Root]), one name at a time, each opened by openat2 beneath the
// descriptor with O_DIRECTORY and O_NOFOLLOW, no symbolic or magic link and
// no mount point crossed. There is no fallback: a kernel without openat2, or
// a filter that denies it, leaves the outbox unavailable ([ErrUnavailable])
// and chat cannot be turned on. The folders on the way are of the daemon's
// user and group and other cannot write them. No path below the state root
// is opened by name.
//
// # A fresh outbox for every turn-on
//
// The outbox belongs to one segment: the time from a turn-on to the next
// turn-off, suspension or restart. [Store.SetSwitch] to read makes it
// afresh, so nothing published before the turn-on can be taken, whatever its
// times say:
//
//  1. the lead's quarantine, <state root>/leads/<layout role>/quarantine, is
//     made (mode 0700) or verified: a folder of the daemon's, bound into no
//     sandbox, on the file system of the lead's home;
//  2. every claim an earlier segment or an earlier run left is discarded:
//     a file is unlinked unopened, and a folder, which unlink refuses, is set
//     aside in the quarantine;
//  3. the earlier outbox is removed when it is an empty folder, and set
//     aside otherwise (whatever it is: a folder that holds anything, a
//     symbolic link, a file): renamed, by a rename that replaces nothing,
//     into the quarantine under 1, 2, 3 or 4, whichever is free, and never
//     opened, read or walked. A quarantine that holds [MaxQuarantine]
//     already refuses the turn-on ([ErrQuarantined]) until the owner purges
//     it ([Store.PurgeQuarantine]);
//  4. a new outbox is made with mode 0700, opened by openat2, set to 0700
//     exactly and verified on its descriptor. A name that is there again
//     already (the lead made it in the moment between) refuses the turn-on
//     ([ErrUnavailable]). The descriptor is held until the segment ends;
//  5. a probe file made in the new outbox is claimed and removed as a reply
//     is: a file system or a filter that cannot rename without replacing
//     (EINVAL, ENOSYS, EOPNOTSUPP, EPERM, EACCES, or two file systems)
//     refuses the turn-on ([ErrUnavailable]) rather than the first reply.
//
// Anything that fails leaves the switch off. The lead must use the folder
// the daemon made and never make it itself: while chat is off, what it
// writes there is set aside unread at the next turn-on.
//
// # A scan
//
// [Store.Ingest] first checks that the outbox's name still leads, without a
// link or a mount point, to the folder the turn-on holds: of the daemon's
// user, mode 0700 exactly, and of the held descriptor's device and inode,
// which no other folder can have while the descriptor is held. An outbox
// that is gone, replaced, not private or closed to the daemon, and a kernel
// that no longer lets the daemon reach or claim from it, stops the lead's
// chat (suspended) with the one entry [TextStoppedFolder]. Then the scan
// works from the held descriptor, never by name, in this order:
//
//  1. the inventory, before anything changes. At most [MaxWaiting] and one
//     names are read: an outbox whose entries, whatever they are, and the
//     replies claimed and not yet read are more than [MaxWaiting] stops the
//     chat with [TextStoppedFull]. Each name is judged by fstatat without
//     following it: a folder, a symbolic link, a FIFO, a socket or a device
//     stops the chat with [TextStoppedOdd]. Nothing in the outbox is ever
//     opened, walked or removed by the scan;
//  2. the claims an earlier scan of the segment left, in id order;
//  3. the new replies, in name order: a regular file with a reply name whose
//     change time is at least [SettleAge] old (a lead cannot set it, and the
//     rename sets it), within the rate. A name that starts with a dot (a
//     draft) and any other name are never claimed and never removed: they
//     wait, counted toward [MaxWaiting].
//
// A stopped chat can still be read; only a fresh turn-on, which sets the
// whole outbox aside, takes replies again.
//
// # Claims
//
// A reply is claimed by renameat2 with RENAME_NOREPLACE from the held
// outbox to the lead's claim folder <state root>/chat/<layout role>.claims
// (mode 0700, on the same file system, which no sandbox binds), as r<id>,
// the id from the store's claim counter. From then on only the claim is
// judged, never the lead's name, and what the lead does to its name (a new
// reply under it included) is a new entry for a later scan.
//
// The claim is judged by fstatat in the claim folder, where only the daemon
// renames. One that is not a regular file (a folder or a special file took
// the reply's name after the inventory) is set aside in the quarantine
// unopened and stops the chat with [TextStoppedOdd]. A regular file is
// opened with O_NOFOLLOW and O_NONBLOCK and judged on its handle: one the
// daemon may not open, of another user, with more than one link, that group
// or other may write, or that its owner may not read is dropped (refused);
// one over [MaxReplyBytes] is too long. A claim that is not the entry the
// inventory saw settle (another inode, size or modification time) is read
// only once its own change time is [SettleAge] old. It is read, judged
// again, and one that changed while it was read, whose read failed or that
// a lease holds is left for a later scan; while it is left, nothing newer is
// claimed. Every claim reaches an end: one whose read failed
// [UnsettledTries] times, or that is still unread [UnsettledAge] after a
// scan first left it, is dropped (unsettled). A lead must close its draft
// before it renames it: what it writes through a handle it kept is not part
// of the reply, and a reply that keeps changing is dropped.
//
// # Recovery
//
// [Store.Ingest] stores a reply's entry with its claim id in a synced append,
// and only then removes the claim. Within one segment a claim is committed
// at most once: the store remembers each claim of the segment, so a retry
// after an error between the claim and the append reads it and stores it
// once, and one after an error between the append and the removal only
// removes it (repaired). The same name with the same bytes written twice is
// two replies.
//
// A restart ends the segment: [OpenStore] opens every lead off and drops
// the segment's entries, and the next turn-on discards the claims and the
// outbox the segment left, unread. So a reply claimed and not stored before
// a restart is never stored, one stored and not removed is never shown
// again, and nothing is stored twice. Because of this no folder is synced
// after a claim, a removal or a setting aside: their order on the disk
// matters only within a segment, and a power loss is a restart. Claim ids
// are unique within a segment; the floor of the ids is persisted (the open
// line), so an id an entry holds is never given again, and an id that a
// restart left with no line may be given again in a later segment, after
// the turn-on discarded its claim.
//
// # The text
//
// [ReplyText] drops a reply over [MaxReplyBytes] (never cut), trims its
// trailing line feeds, and drops what is not plain text by
// [ownerwire.ValidChatText]. A text that [brief.SecretShaped], weighed on the
// whole text, finds shaped like a key, a token or a full commit id is not
// stored: the entry is the fixed system text [TextWithheld]. A drop writes a
// fixed system entry, at most one per reason a minute. No entry holds a file
// name, a path or a count.
//
// # The store
//
// A lead's chat is <state root>/chat/<layout role>.jsonl, mode 0600, in a
// folder of mode 0700 that no sandbox binds. It is strict JSON lines: each
// line valid UTF-8 and at most [MaxLineBytes] with its newline, one object
// whose members are each named once and none null, of exactly the members of
// its kind. The first line is {"kind":"open","v":1,"next":N,"claim":C}, the
// allocation high-waters: no sequence number below N and no claim id up to
// C is given again. A switch line
// {"kind":"switch","chat":"read|off","gen":G,"at":T,"epoch":E,"discarded":N}
// records each change of the switch with its generation, and for a turn-on
// how many things it set aside; an entry line
// {"kind":"entry","seq":S,"gen":G,"epoch":E,"who":"lead|system","at":T,"text":"...","claim":C}
// records one entry, with the claim id of the reply it was stored from (a
// lead's entry and [TextWithheld] have one, from 1; no other entry has the
// member at all, not even "claim":0). A generation is at most 2^53-1 and a
// read switch's at most one less, so the turn-off after it always has one; a
// turn-on that would leave none is refused ([ErrFull]). Entries are
// checked in their own increasing order, so a compacted file (the open
// line's floor above the entries it kept) and the appends after it replay
// alike; every segment starts with the entry [TextTurnedOn], and a system
// entry holds one of the fixed texts. The file is opened by handle (a
// regular file of the caller, mode 0600, one link, no link followed) and
// held with an exclusive, non-blocking flock. Every append is synced before
// the call returns. A last line without its newline is cut off; any other
// damage makes [OpenStore] fail with [ErrCorrupt], whose text is a fixed
// reason and the line's number and never a byte of the file, and nothing is
// repaired, so that lead has no chat (fail closed) while the others are
// untouched. A write or sync that fails breaks the store ([ErrBroken]).
// Sequence numbers only grow: a new one is one above the highest given, or
// above the open line's floor. A compaction writes a temporary file, syncs
// it, renames it over the file and syncs the folder; a temporary file left
// by a crash is removed.
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
// is effective at once and every call checks it. Off to read starts a
// segment as above, then writes the switch line and the entry [TextTurnedOn]
// in one synced append. Read to off ends the segment: the switch is off in
// memory, the outbox is let go of, and the lead's entries are dropped by a
// compaction. A suspended lead turned on again is turned off so first. The
// switch is never restored from disk: [OpenStore] opens every lead off,
// dropping what an earlier run left on, and the next turn-on starts an
// empty segment.
//
// # What the lead is told
//
// The lead's instructions must say: write each reply under a dot-name in
// the folder the daemon made, close it, then rename it to a reply name; use
// that folder as it is and never make it, change its mode or put a folder,
// a link or a special file in it; when the folder is not there, or chat is
// stopped, stop publishing until the owner turns chat on again (a reply
// written then is set aside unread, and a full folder stops chat rather than
// waiting).
package leadchat
