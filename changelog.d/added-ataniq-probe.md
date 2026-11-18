- Add `ataniq-probe`, a static helper command of the lead sandbox proposal. It
  is unqualified, runs on Linux only (elsewhere it builds and exits with a
  message) and nothing uses it yet. It has two modes. `ataniq-probe --spec PATH
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
  pass-fds [--read FD=PATH]... [--write FD=PATH]... [--walk-dir FD ROOT REL]...
  [--walk-file FD ROOT REL]... -- COMMAND ARGV...` runs on the host between
  pasta and bubblewrap, because pasta closes the descriptors it inherits: it
  takes descriptor numbers in decimal digits without a leading zero, each at
  most once, and places at most 128 descriptors in all (a hard cap: one per
  bind source Ataniq supplies, the seccomp file and the report channel, with
  room to spare). `--read` and `--write` open files (read-only regular files,
  or write-only appending regular files and FIFOs, never created or truncated,
  links not followed; every such path must lie in folders only the holder can
  write, as the checks on a path are not atomic with the open; a FIFO that no
  process has open for reading is refused at once and does not wait).
  `--walk-dir` and `--walk-file` reach a bind source by handle: the probe opens
  the folder ROOT without following a symbolic link in any component of it
  (once for all the walks that name it), opens REL beneath that handle in one
  `openat2` lookup that follows no link and crosses no mount point, checks on
  the descriptor it got that the source is a folder or a regular file, and
  keeps that `O_PATH` descriptor, so that the object it checked is the object
  the command gets, whatever is done to the name afterwards. The walks are made
  first, and a walk that is refused (a link, a missing entry, another type, a
  mount point, no `openat2`) is exit 96 with one line that names the walk;
  nothing is opened for writing and nothing is executed. The probe then places
  each descriptor at exactly its number, makes every other descriptor above 2
  close-on-exec and executes the command with the environment as Go reads it,
  as above; any other error is exit 98 and nothing is executed. Both modes
  refuse (exit 98, one line) a number that is one of the Go runtime's own
  descriptors, which it holds when a standard stream was non-blocking at
  start: a target number in the pass-through mode, the report descriptor in the
  inside mode.
