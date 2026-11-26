// Package durable implements the checkpoint witness on a local Linux
// filesystem supporting flock, keep-size fallocate and durable file and directory fsync.
//
// Create provisions a journal and a lock file in an existing service-owned
// 0700 directory. Open requires both files. The lock file also records the
// digest of each committed frame and its exact acknowledgement signature.
// Losing a complete journal suffix alone is therefore detectable. Replay
// verifies every frame, receipt and predecessor before serving. A complete
// final transition left by a crash can finish its missing acknowledgement.
// A partial frame or partial signature entry requires owner intervention.
// Preserve the directory for investigation before re-enrolling.
//
// The independently configured activation epoch is signed into acknowledgements
// and dispositions. The link pins it for an activation. Journal sequence
// orders transitions within this directory, including Settle fences. A matched
// truncation of both files at a commit boundary or a whole-directory restore
// can hide later mutations, releases, retirements and fences under the same
// epoch. A forgotten fence can let its formerly fenced ID commit. A fence
// changes neither slot revision nor ledger head and issues no mutation receipt.
// Each mutation acknowledgement signs its directory sequence. A challenged
// read also signs the current directory sequence and the digest of the last
// frame signature (the predecessor for the next frame). A home or owner keeping
// these outside the directory can detect a lower sequence, or a different
// digest at the same observed position. A larger sequence alone does not prove
// continuity. Without independent evidence, replay cannot distinguish a valid
// older prefix from the latest one. After a known restore,
// keep admission inhibited while the owner reconciles surviving evidence and
// provisions a fresh identity and higher epoch.
//
// The index flock excludes concurrent writers to one directory instance.
// A second flock covers the signing key file while the witness is open.
// A copy of both the directory and key file has independent locks and can
// serve the same identity. Retained signed sequences and head digests permit
// detection when the copies present conflicting evidence.
//
// Each store serves exactly one home public key. Create binds its fingerprint
// into genesis and Open checks it against Config.HomePin. The per-store limit
// is 65,536 ordinary receipts and fences by default. ReceiptLimit may reduce
// admission. Release and retirement retain room for one entry per claim and
// enrollment. Before ordinary appends, both files reserve disk space for
// these closing operations. A failed append is truncated to its original EOF
// and synced by its writer before service stops. At the ordinary limit the owner has a separate allowance of
// ReceiptLimit new settlement fences (65,536 by default). An exact settlement
// lookup spends no credit. The absolute maximum is 196,608 entries plus genesis.
// There is no compaction, on purpose: prior receipts and fences are permanent.
//
// Open and Create start inhibited and without home grants. The service supplies
// current grants and explicitly enables mutations after checking its guard.
// Before journal I/O the witness durably creates recovery.pending. A clean close
// removes it. Process death leaves this marker. After recovery every new
// transition stays inhibited until Administration().AcknowledgeStorageError.
// A reported write or sync error stops service and creates and syncs a second
// marker, poisoned, on a best-effort basis. Open refuses while it exists.
// An I/O error on this directory requires the owner's check and repair before
// removing poisoned and reopening. On every Open, replay rewrites the last
// validated frame and index entry in place and syncs both files and the directory
// before signing or serving. Torn or corrupt evidence still prevents Open.
package durable
