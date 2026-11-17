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
//     step and no exec can leak it. A descriptor that is not open is exit 98.
//  2. Reads the spec: opened without following a link at the last component,
//     it must be a regular file, and assert.DecodeSpec reads at most
//     assert.MaxSpecBytes and refuses anything else. A spec that is refused is
//     exit 98 and no report is written: the holder sees the exit status.
//  3. Runs assert.RunInside against the real system (assert.HostSystem) and
//     writes the one report line (assert.EncodeLine) to descriptor N, in full,
//     on success and on failure. A write that fails, or that makes no
//     progress, is exit 98; a partial write is continued until the line is
//     whole.
//  4. Judges the report with assert.Failed. When it says the report does not
//     allow a start the probe exits 97 and executes nothing, after listing the
//     problems on standard error (display only: the holder reads the
//     descriptor and the exit status).
//  5. When descriptor 0 is a terminal, gives the agent a new session with that
//     terminal as its controlling terminal (variant B of the design): setsid,
//     then TIOCSCTTY. When setsid is refused (the probe leads a process
//     group) the agent is started as a child in a new session that has the
//     terminal as its controlling terminal, and the probe waits, forwards
//     SIGTERM, SIGINT, SIGHUP and SIGQUIT to the child and exits with the
//     child's status, or 128 plus the signal number when a signal ended it; it
//     closes the report descriptor after the child has started. When
//     descriptor 0 is not a terminal it says so on standard error and skips
//     this step. A failure to get the terminal when there is one is exit 98.
//  6. Makes every descriptor above 2 close-on-exec, so that the execve closes
//     it (the agent keeps none), and executes AGENT with the process's
//     environment unchanged. The agent starts as the probe's own process, so
//     the report descriptor reaches end-of-file at the exec. A failure to
//     execute is exit 98.
//
// The probe never issues TIOCSTI, never creates or writes a file, never changes
// the environment, and starts no program but the agent. Where it passes on the
// environment, that is what Go reads from the process (os.Environ): an entry
// without "=" or a name that appears twice is not passed on.
//
// # Pass-through mode
//
//	ataniq-probe pass-fds [--read FD=PATH]... [--write FD=PATH]... -- COMMAND ARGV...
//
// The launch plan puts this step on the host between pasta and bubblewrap,
// because pasta closes every descriptor it inherits before it starts its
// command. FD is 3 to 1023 (decimal digits, no leading zero), each at most
// once, PATH and COMMAND are absolute
// paths, and at most 8 files are given. A --read file is opened read-only and
// must be a regular file. A --write file is opened write-only, appending,
// never created and never truncated, and must be a regular file or a FIFO. All
// are opened with O_NOFOLLOW (a link at the last component is refused),
// O_CLOEXEC and O_NOCTTY; a --read file is opened non-blocking while its type
// is checked, and the flag is cleared afterwards. A FIFO opened write-only
// blocks until a process has its reading end open: the holder opens its end
// first. Each file is then placed at exactly its number without close-on-exec,
// every other descriptor above 2 is made close-on-exec, and COMMAND is
// executed, with ARGV (whose first element is COMMAND's own) and the
// environment unchanged. Any error is exit 98 with one line, and nothing is
// executed.
//
// # Descriptors are closed by the exec
//
// Both modes close descriptors by close_range(2) with CLOSE_RANGE_CLOEXEC (and
// by listing /proc/self/fd where the kernel does not have it): the descriptor
// stays open in the probe until the execve, which closes it. Closing it at once
// could pull a descriptor out from under the Go runtime, which may hold one of
// its own, and the program that is executed sees the same descriptors either
// way. Where the pass-through mode places a file at a number the runtime holds
// itself, the runtime would lose it; the runtime holds none unless descriptor 0
// is non-blocking when the program starts.
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
package main
