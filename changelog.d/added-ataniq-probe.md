- Add `ataniq-probe`, a static helper command of the lead sandbox proposal. It
  is unqualified, runs on Linux only (elsewhere it builds and exits with a
  message), and only the opt-in lead sandbox qualification harness launches it:
  no holder and no daemon uses it. It has two modes. `ataniq-probe --spec PATH
  --report-fd N -- AGENT ARGV...` runs the inside checks of the proposal, writes
  exactly one report line to descriptor N (and nothing else to it: the holder
  takes that line as its signal and fails the start on any further byte), and
  exits 97 without starting the agent when the report does not allow a start.
  Otherwise it gives the agent a new session with descriptor 0 as its
  controlling terminal when that is a terminal, makes every descriptor above 2
  close-on-exec so the exec closes it, and executes the agent with the
  environment as Go reads it (os.Environ: a name that appears twice is passed on
  once, with its first value, and an entry without `=` as it is). A usage error
  or any failure to read the spec, write the report, get the terminal or execute
  the agent is exit 98. The agent path is checked first, before the report is
  written, to be an executable regular file, otherwise it is exit 98 with no
  report; a file that the kernel then refuses to execute is exit 98 after a good
  report, which a holder cannot tell from an agent that exited 98. `ataniq-probe
  pass-fds [--read FD=PATH]... [--write FD=PATH]... -- COMMAND ARGV...` runs on
  the host between pasta and bubblewrap, because pasta closes the descriptors it
  inherits: it takes descriptor numbers in decimal digits without a leading
  zero, opens up to 8 files (read-only regular files, or write-only appending
  regular files and FIFOs, never created or truncated, links not followed; every
  path must lie in folders only the holder can write, as the checks on a path
  are not atomic with the open), places each at exactly its number (a FIFO that
  no process has open for reading is refused at once and does not wait), makes
  every other descriptor above 2 close-on-exec and executes the command with the
  environment as Go reads it, as above; any error is exit 98 and nothing is
  executed. Both modes refuse (exit 98, one line) a number that is one of the Go
  runtime's own descriptors, which it holds when a standard stream was
  non-blocking at start: a target number in the pass-through mode, the report
  descriptor in the inside mode.
