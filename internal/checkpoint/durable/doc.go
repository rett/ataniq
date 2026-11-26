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
// Open and Create start inhibited and without home grants. The service supplies
// current grants and explicitly enables mutations after checking its guard.
package durable
