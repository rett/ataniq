// Command ataniq-probe is the static helper of the lead sandbox proposal. It
// has two modes, and it is a proposal that awaits qualification and an owner
// decision: nothing here says the sandbox is qualified, safe or supported, and
// nothing uses the command yet. It states what it does and what it checks.
// Only Linux is supported; on another system the command builds, says so on
// standard error and exits 98. It is built without cgo (CGO_ENABLED=0) as a
// static program, and imports only the standard library, golang.org/x/sys/unix
// and, of this module, the lead sandbox packages assert and profile (it uses
// assert). The design is the
// lead-hosting design, sections 1.1 and 3.8.
//
// # Inside mode
//
//	ataniq-probe --spec PATH --report-fd N -- AGENT ARGV...
//
// The probe runs inside the sandbox, as the program the launch plan starts
// last. PATH and AGENT are absolute paths and N is a descriptor from 3 to
// 1023, in decimal digits with no leading zero; the options come in either
// order and each once. In order, the probe:
//
//  1. Marks descriptor N close-on-exec, before anything else, so that no later
//     step and no exec can leak it. A descriptor that is not open, or that is
//     one of the Go runtime's own (see below), is exit 98.
//  2. Checks that AGENT is an executable regular file (stat, following a
//     link, and faccessat with X_OK for the effective ids), before the checks
//     run. Otherwise it is exit 98 and no report is written: the holder sees
//     the exit status alone.
//  3. Reads the spec: opened without following a link at the last component,
//     it must be a regular file, and assert.DecodeSpec reads at most
//     assert.MaxSpecBytes and refuses anything else. A spec that is refused is
//     exit 98 and no report is written: the holder sees the exit status.
//  4. Runs assert.RunInside against the real system (assert.HostSystem) and
//     writes the one report line (assert.EncodeLine) to descriptor N, in full,
//     on success and on failure: exactly one line, once, and no other byte,
//     here or later (see "The report channel"). A write that fails, or that
//     makes no progress, is exit 98; a partial write is continued until the
//     line is whole.
//  5. Judges the report with assert.Failed. When it says the report does not
//     allow a start the probe exits 97 and executes nothing, after listing the
//     problems on standard error (display only: the holder reads the
//     descriptor and the exit status).
//  6. When descriptor 0 is a terminal, gives the agent a new session with that
//     terminal as its controlling terminal (variant B of the design): setsid,
//     then TIOCSCTTY. When setsid is refused (the probe leads a process
//     group) the agent is started as a child in a new session that has the
//     terminal as its controlling terminal, and the probe waits, forwards
//     SIGTERM, SIGINT, SIGHUP and SIGQUIT to the child and exits with the
//     child's status, or 128 plus the signal number when a signal ended it; it
//     closes its copy of the report descriptor after the child has started
//     (the child has none). When descriptor 0 is not a terminal it says so on
//     standard error and skips this step. A failure to get the terminal when
//     there is one is exit 98.
//  7. Makes every descriptor above 2 close-on-exec, so that the execve closes
//     it (the agent keeps none), and executes AGENT with the process's
//     environment as Go reads it (see below). The agent starts as the probe's
//     own process, and the exec closes the probe's copy of the report
//     descriptor. A failure to execute is exit 98. The check of step 2 finds the usual
//     causes (a missing, non-executable or non-regular file), but not all: a
//     file that the kernel does not accept as a program, or one replaced since
//     the check, fails here, after a good report. A holder cannot tell that
//     exit 98 from an agent that started and exited 98.
//
// The probe never issues TIOCSTI, never creates or writes a file, never adds,
// removes or changes an environment variable, and starts no program but the
// agent. The environment it passes on is what Go reads from the process
// (os.Environ), and that is not always the block it was given: a name that
// appears twice is passed on once, with its first value, and an entry without
// "=" is passed on as it is. Both modes do the same.
//
// # The report channel
//
// The contract is the one of the package assert ("The probe and the holder"):
// the holder takes one accepted line as the signal that the probe has reported,
// does not wait for end-of-file, and fails the start on any further byte on the
// channel. So the probe writes exactly one line, the report, to descriptor N in
// every path that writes a report, and nothing else, ever: it writes no second
// line, no marker and no status to it, whether the checks pass or fail, and
// after the write it only closes its copy, or has it closed by the exec. The
// descriptor is close-on-exec from the first step, in the process that is
// executed and in the child of the waiting parent, so the agent has no copy;
// copies that other processes of the sandbox hold (bubblewrap's) are not the
// probe's. Standard error is another channel: it holds display-only lines and
// is never part of the report. A refusal before the report (a usage error, a
// report descriptor that is not open or that is one of the Go runtime's own, an
// agent that cannot be executed, a spec that is refused) writes nothing to the
// channel, and the holder sees the exit status alone. Tests read the channel
// with the holder's own functions, assert.ReadProbeReport and
// assert.CheckNoMore.
//
// # Pass-through mode
//
//	ataniq-probe pass-fds [--read FD=PATH]... [--write FD=PATH]... -- COMMAND ARGV...
//
// The launch plan puts this step on the host between pasta and bubblewrap,
// because pasta closes every descriptor it inherits before it starts its
// command. FD is 3 to 1023 (decimal digits, no leading zero), each at most
// once, PATH and COMMAND are absolute paths, and at most 8 files are given. A
// --read file is opened read-only and must be a regular file. A --write file is
// opened write-only, appending, never created and never truncated, and must be
// a regular file or a FIFO. All are opened with O_NOFOLLOW (a link at the last
// component is refused), O_CLOEXEC and O_NOCTTY, and non-blocking while the
// open and the type check last; the flag is cleared afterwards. A --write FIFO
// that no process has open for reading is therefore refused at once (ENXIO,
// exit 98, one line) and does not hold the launch: the holder opens its reading
// end first. Each file is then placed at exactly its number without
// close-on-exec, every other descriptor above 2 is made close-on-exec, and
// COMMAND is executed, with ARGV (whose first element is COMMAND's own) and the
// environment as Go reads it (see above). Any error is exit 98 with one line,
// and nothing is executed; so is a number that the Go runtime holds (see
// below).
//
// The checks on a path are not atomic with the open, and the probe does not
// try to make them so. A --write path is looked at (lstat) before it is opened
// and checked again on the descriptor it opened; a --read path is checked only
// on the descriptor, after the open, so a device node at a --read path is
// opened (without becoming a controlling terminal, and not blocking) before it
// is refused. O_NOFOLLOW covers the last component only. Whoever can rename or
// replace an entry in a folder on the path can change what the probe opens
// between the look and the open. So every path given to the pass-through mode
// must lie in a folder that only the holder can write, and nobody else may be
// able to change a folder above it; the launch plan requires the report path
// to lie directly in a folder the holder owns. The probe does not reopen the
// descriptor through /proc to compare it with the first look.
//
// # Descriptors are closed by the exec
//
// Both modes close descriptors by close_range(2) with CLOSE_RANGE_CLOEXEC (and
// by listing /proc/self/fd where the kernel does not have it): the descriptor
// stays open in the probe until the execve, which closes it. Closing it at once
// could pull a descriptor out from under the Go runtime, which may hold one of
// its own, and the program that is executed sees the same descriptors either
// way.
//
// The runtime holds descriptors of its own (its poller's eventpoll and
// eventfd, at the lowest free numbers, 3 and 4 when everything else is closed)
// when any of descriptors 0 to 2 is non-blocking as the program starts: the os
// package registers a non-blocking standard stream with the poller before main
// runs. A file placed over one of them would be used by the runtime before the
// execve (an epoll wait on a regular file makes the runtime throw, and its
// wake-up writes eight bytes to whatever file it finds), and a report written
// to one would not reach the holder. So the pass-through mode refuses, before
// it opens anything, a number that is an eventpoll or an eventfd descriptor
// (exit 98, one line), and the inside mode refuses such a number as the report
// descriptor in the same way. The probe cannot tell the runtime's eventpoll or
// eventfd from one that the caller passed, so it refuses both; any other
// descriptor inherited at a target number is still replaced, as before.
//
// The runtime's cgroup-aware default for GOMAXPROCS would add one more: in a
// process whose cgroup has the cpu controller (a service of the system
// manager has it, a login session often does not) it opens the cgroup's cpu.max
// and keeps it open, close-on-exec, at the lowest free number, which is 3 when
// the caller closed everything else. The probe could not tell that file from
// one the caller passed, and with a file placed over it the runtime would
// read that file as its CPU limit. So the probe turns the feature off in the
// program itself, with a go:debug directive (containermaxprocs=0), and holds no
// such descriptor: its descriptors above 2 are the caller's, and the poller's
// described above. A GODEBUG setting in the probe's environment overrides the
// directive, so a caller must not start it with containermaxprocs=1.
//
// # Exit statuses and standard error
//
//   - 0 is never returned by the command itself: a successful execve replaces
//     it.
//   - 97: the inside checks did not pass. Standard error holds a first line
//     "ataniq-probe: the inside checks did not pass: N problems; the agent is
//     not started" and then one line per problem, "ataniq-probe:   assertion
//     K (ID): failed: DETAIL" (or "not checked", or "no result"), at most 40
//     of them. Every line is escaped (assert.EscapeDetail) and at most 512
//     bytes.
//   - 98: a usage error, "ataniq-probe: usage: REASON" (inside mode) or
//     "ataniq-probe: usage: pass-fds: REASON", or any other failure to do what
//     the command is for, "ataniq-probe: MESSAGE", always one line.
//
// A parent that waited for the agent returns the agent's own status, which can
// be 97 or 98 too.
//
//go:debug containermaxprocs=0
package main
