- Add `ataniqd`, a provisional and unqualified daemon skeleton. It is
  read-only: it starts, stops and manages no unit and defines no lead, and no
  flag changes that. `ataniqd run [--state-dir DIR] [--runtime-dir DIR]` (state
  defaults to `$XDG_STATE_HOME/ataniq`, else `$HOME/.local/state/ataniq`;
  runtime to `$XDG_RUNTIME_DIR/ataniq`, with no default and no fallback to a
  shared folder when `XDG_RUNTIME_DIR` is unset) refuses to run as root,
  creates the state folder with mode 0700 and refuses one that is not the
  caller's or is open to group or others, takes `daemon.lock` there (a second
  daemon exits with its own code and names the lock as held, with the pid in
  the file shown as a hint, not as proof), takes a second lock,
  `daemon.sock.lock` in the runtime folder, opens the registry journal in
  `registry/` (a corrupt one stops the daemon and nothing is repaired),
  reconciles once, removes a stale socket only once it holds both locks, and
  serves `hello` and `status` on `daemon.sock` until SIGTERM or SIGINT. The
  runtime folder lock keeps two daemons with different state folders and one
  runtime folder from taking each other's socket: the second exits with code 17
  and says that another daemon serves that folder, with the pid in the lock
  shown as a hint. It is refused before it opens its registry, so it has
  reconciled nothing and written nothing to its journal. A socket path that is
  too long for a Unix socket is refused before the runtime folder or the lock
  file is made. A socket or lock file that a crashed daemon left does not block
  the next start. At shutdown it stops accepting, waits for the connections for
  a bounded time, closes the listener (which removes the socket) under the
  runtime folder lock, closes the registry, releases the runtime folder lock
  and releases the daemon lock last;
  when the server does not end in that time it exits with code 19, says what is
  left and closes the listener; the registry and both locks are left to the end
  of the process. It never stops a lead or any unit. The reconcile reads the
  units and may write the registry's journal, the states stopped, ended or
  unknown with their proof or reason and the outcomes adopted or abandoned of
  launch intents, and, once it has acted on every role of a registry whose
  journal was cut when it was opened, the acknowledgement of that cut, which
  is a record of its own kind in the journal; it starts, stops and signals
  nothing. A cut that was acknowledged shows no more in `status`; a cut that
  stays unacknowledged (the manager did not answer for a role, or the
  reconcile stopped first) shows as the reconcile not completed, and the next
  start acts on it again. When systemd cannot be
  asked (also when the registry has no role to ask about), when any call to it
  failed during the reconcile, or when the reconcile ran out of time (also in
  its last step), the daemon still serves the roles as the registry has them,
  with the reconcile shown as not completed, which is also how an
  unacknowledged recovery of the registry shows, as the protocol has no field
  for the reason. An answer such as "not loaded" is not a failure. Reconcile
  runs at start only, not on a timer. Each start-up step that fails has its own
  exit code (10 to 18) and one line on standard error. The socket is mode 0600
  in a 0700 folder, and a peer is accepted only when its user id equals the
  daemon's, which does not tell one process of that user from another. `ataniqd
  status [--socket PATH] [--json]` prints one line per role (role, kind, state,
  reason, epoch, desired, manager, and for a coordinator its parent and
  instance) and one for the daemon, or the status result as JSON; every string
  from the socket is escaped, so a hostile one cannot act on the terminal. Only
  `run` takes SIGTERM and SIGINT as the request to stop; they end `status` at
  once. `status` exits 0 when the daemon answered, 20 when no daemon answers,
  21 when it refuses the user and 22 for an answer that is not valid or an
  error the daemon answered. The requested and the observed profile stay apart,
  and a field that was not observed is shown as not observed. The protocol has
  no place for the proof of a stopped or ended state or for the unit evidence,
  so `status` shows neither. `ataniqd version` prints the version, `0.0.0-dev`
  unless set with a linker flag, because the `VERSION` file cannot be embedded
  from the command's folder. Leads, coordinators, the holder, the sandbox and
  the console are not part of it, and nothing in it has run against systemd.
  The same change adds `leadunit-qualify`, an opt-in harness for the systemd
  unit manager that `ataniqd` uses, and the one new command that can start a
  unit. Without `--run` it prints its plan and touches nothing. With `--run
  --state-dir DIR` it starts one transient user unit,
  `ataniq-qualify-<random>.service`, and one bound to it,
  `ataniq-qualify-<random>-child.service`, never under the name of a role,
  checks the manager against them, prints one JSON report and cleans its units
  up. It is run by hand: no test and no make target runs it, and no run of it
  is recorded. See [the lead daemon guide](docs/lead-daemon.md).
