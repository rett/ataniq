// Package durable implements the checkpoint witness on a local Linux
// filesystem supporting flock and durable file and directory fsync.
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
// increases at every transition, and signed slot revision at every mutation.
// A whole-directory restore can reproduce a valid older prefix, including its
// epoch, signatures and tombstones. A home or owner retaining a later receipt,
// revision or head can detect the discrepancy. If all independent evidence
// rolls back together, the lost interval is unknowable. After a known restore,
// keep admission inhibited while the owner reconciles surviving evidence and
// provisions a fresh identity and higher epoch.
//
// Each store serves exactly one home public key. Create binds its fingerprint
// into genesis and Open checks it against Config.HomePin. The per-store limit
// is 65,536 ordinary receipts and fences by default. ReceiptLimit may reduce
// admission. Release and retirement retain room for one entry per claim and
// enrollment. At the ordinary limit the owner has a separate allowance of
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
