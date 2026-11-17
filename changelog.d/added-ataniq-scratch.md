- Add `ataniq-scratch`, a development launcher that hosts a scratch agent (no
  project, no lead role, a disposable home) in a holder, in a transient systemd
  user unit or as a direct child with `--direct`, and stands in for the daemon
  to issue attach tickets over the holder's control socket. `start`, `ticket`,
  `status`, `release-writer` and `stop` are its subcommands. `start` takes the
  agent as an absolute path, because the holder does no `PATH` search, and
  refuses any other with exit status 2. It is provisional and unqualified, and
  not for a real lead.
