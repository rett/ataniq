// Package leadlock holds the two kinds of advisory lock the lead daemon
// design relies on: the daemon singleton lock (<state>/daemon.lock) and the
// per-role lock (for example <state>/roles/<project>/lead.lock). Both are the
// same mechanism, an flock(LOCK_EX|LOCK_NB) on a regular file that only its
// owner can touch. This package takes paths; the caller computes role lock
// paths, and [DaemonPath] gives the daemon's.
//
// # What the lock is
//
// The lock is the flock and nothing else. It belongs to an open file
// description, so it is held exactly as long as some descriptor of that
// description is open anywhere. The kernel releases it when the last such
// descriptor closes, including when the holder is killed with SIGKILL, so a
// crashed holder never leaves a stale lock behind. The file is opened with
// O_CLOEXEC, so an exec'd child does not keep the lock. A forked child that
// has not yet exec'd, or a child given [Lock.File] through exec.Cmd.ExtraFiles
// or a socket, shares the description and keeps the lock alive after the
// parent calls [Lock.Release]. That is the way to pass a held lock on.
//
// # What the pid text is not
//
// After the lock is taken, the holder writes its own pid as decimal text and
// a newline into the file. That text is a hint for display. It is written
// after the lock is taken, so a reader can see a stale or empty value in the
// window around an acquire, and a pid says nothing about a process that has
// since exited and been reused. [ReadHolder] and the pid carried by [ErrHeld]
// are for operator messages only. A peer is authenticated by other means (a
// pidfd from the socket credentials), never by this text.
//
// # Never probe
//
// The package offers no way to ask whether a lock is held by taking and
// releasing it, and callers must not build one. A probe takes the lock for
// an instant, so it can make a real acquire elsewhere fail, and a probe that
// succeeds says nothing by the time it returns. The daemon in particular never
// probes a role lock. The only questions this package answers are "did my
// Acquire succeed" (the returned [Lock]) and, for display, "what pid text is
// in the file".
//
// # Files and folders
//
// [Acquire] creates missing parent folders with mode 0700 and the file with
// mode 0600. It refuses a final symbolic link, a file that is not regular,
// one not owned by the effective user, and one whose permission bits are
// wider than 0600. It does not tighten or check folders that already exist;
// the caller owns the state folder. The lock file is never unlinked, because
// unlinking it lets a second locker create and lock a new file while the
// first holder still holds the old one.
//
// The package is Linux only, provisional and unqualified: no crash, hostile
// filesystem or race scenario has been qualified against it, and it proves
// nothing about who holds a lock.
package leadlock
