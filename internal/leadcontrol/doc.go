//go:build linux && ataniq_canary

// Package leadcontrol is the durable command table of the lead control
// plane: the record that makes a mutating command idempotent. A client names
// each command with a client string and a command id. The table binds that
// key to the operation, the role and a digest of the payload, and keeps the
// receipt that answers the command.
//
// The table is one append-only JSON lines file, owned by the caller (the
// daemon puts it at <state root>/lead-commands.jsonl). The file is opened by
// handle: it must be a regular file of the caller with mode 0600 and one link,
// it is opened without following a symbolic link at its last element, and Open
// takes an exclusive, non-blocking flock on it, so one process owns it at a
// time. Every line is at most 4 KiB with its newline, and every line is
// written whole and fsynced before the call that wrote it returns.
//
// The kinds of line are admit, finish, interrupted and tombstone. [Table.Admit]
// returns only after its admit line is synced, so a caller that acts after
// Admit acts on a command that is durably admitted. A replay of the same key
// with the same operation, role and digest returns the stored receipt and never
// runs the command again, whatever its status; the same key with anything else
// is [ErrConflict].
//
// Open replays the file. A final line without its newline is an append that
// never returned: it is cut off. Any other damage (a bad line, an unknown
// field, a record that breaks the rules below, a line over the cap) makes Open
// fail with [ErrCorrupt] and nothing is repaired. Every command that was
// admitted and never finished gets an interrupted line: its receipt reads
// uncertain, or unavailable for a command that repeats harmlessly (a ticket).
// The client is never told to send it again: it reads the state of the role,
// which is the authority.
//
// The table is bounded. When more than MaxReceipts receipts are held, Open
// compacts the file (a temporary file, fsynced, renamed over the table, and the
// folder fsynced) and keeps every receipt that is not final plus the newest
// final ones. A key that is dropped leaves a tombstone, the SHA-256 of its
// client and id, and a replay of a tombstoned key is [ErrExpired]: a dropped
// command is never run again. At most MaxTombstones tombstones are kept, the
// newest; a key older than that looks new, which is the residual risk named in
// the design. A tombstone that the file holds is kept in memory for the run of
// the process, so a key is never admitted after its tombstone in the file,
// which Open would refuse as corrupt. While MaxReceipts receipts are all not final, a new key is
// refused with [ErrFull] and nothing is admitted.
//
// A command id that belongs to a receipt that is not final may not be used by
// another client: [Table.Finish] names a command by its id alone, so the id of
// an open command is unique. A command id of a final receipt may be reused by
// another client.
//
// The times in the file are for the receipt only. A clock that went back
// between two runs is not damage.
//
// A failed write or sync of the file closes the table for good ([ErrBroken]):
// what reached the disk is unknown, so no later line may follow it. The table
// still answers a replay of a key it holds with the receipt it holds (in
// progress when the finish could not be written), and refuses everything else.
// The next Open replays whatever is there.
//
// The client string is a namespace, not an identity: the client API gives the
// daemon no peer identity beyond the user's own, so nothing here authenticates
// a caller.
//
// This package never starts or signals anything, reads nothing but its own
// file, and keeps no second copy of the lead state: the registry and the
// evidence stay the authority on what a lead is and on what was sent.
package leadcontrol
