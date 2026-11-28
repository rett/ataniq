// Package cgroupsig signals the processes of a control group without trusting
// a name or a bare process number. It holds what the holder's process host
// (internal/leadholder/ptyhost) and the systemd unit manager
// (internal/leadunit/systemdmgr) share: the pidfd signalling of a list of
// processes with a membership check after each pidfd is open, and a [Group], a
// control group directory pinned by an open descriptor and checked against an
// identity that was recorded earlier.
//
// What a Group gives. [Open] resolves the group's path below the hierarchy
// root one component at a time, never following a link, and compares the
// device, inode and owner of the directory it opened with the identity the
// caller recorded. Every later operation goes through that one descriptor
// with openat, so a group that was removed and replaced by another of the
// same name is never written to: the descriptor still names the old,
// removed directory, and the new one is refused at [Open].
//
//   - [Group.Kill] writes "1" to cgroup.kill: the kernel kills every process
//     of the group and of the groups below it, atomically with respect to new
//     forks. It never falls back to a signal by name or by process number. A
//     removed group is [ErrGone].
//   - [Group.Signal] lists cgroup.procs from the descriptor, opens a pidfd for
//     each pid, lists the file again and signals only the pids still listed
//     (a pid that was reused by a process outside the group is not). The main
//     process, when it is listed, is signalled first. It covers the processes
//     the group lists itself, not those of child groups.
//
// What it does not prove. The identity is that of the directory object: it
// says nothing about which unit owns the group. A group that is not removed
// but whose processes left it is signalled as empty. The package is
// provisional: nothing here was qualified on a live host, in particular the
// reuse of an inode number after a group is removed and cgroup.kill in a
// delegated user subtree (the opt-in lead unit qualification checks them).
// It builds on Linux only.
package cgroupsig
