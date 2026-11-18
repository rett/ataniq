// Package pidfd holds the Linux process-identity primitives that a supervisor
// needs to tell one process from another: a pidfd opened for a pid, a
// non-blocking exit poll of that descriptor, the boot identity, the process
// start time read from /proc/<pid>/stat, and the same-boot check that an
// original process has ended.
//
// What it does not prove. A start time alone is not an exit handle: only an
// open pidfd refers to one process for its whole life, and a pid that has been
// recycled can only be told apart by the start time on the same boot. A start
// time does not identify an executable; an exec keeps the pid and the start
// time. Permission failures when reading /proc never mean absence. The poll
// reports exit of the process the descriptor was opened for, not the state of
// its children or its control group. Nothing here proves that a process is
// the one an earlier record named unless the caller also compares the boot
// identity, pid and start time it recorded.
//
// The package is provisional and unqualified: it was moved out of the
// qualification host so that other code can use it, and it has no
// qualification of its own. It builds on Linux only and has no architecture
// constraint.
package pidfd
