- Add `ataniq-holder`, the per-agent holder process: one process for one
  hosted agent. It owns the agent's terminal (the PTY master and the emulator
  that mirrors its screen), the attach and control sockets, the event log and
  the final record, takes the role lock, and writes a state file (`holder.json`)
  before it starts the agent. The agent's environment is exactly the `--env`
  list and nothing of the holder's own. It refuses to run without `--scratch`
  (exit 2), because the sandbox is not built, so it hosts only a scratch agent;
  it also refuses to start when `dev.tty.legacy_tiocsti` is not 0, when a
  signal the agent needs is ignored, or when the state or runtime folder is not
  a private directory of the user. SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and
  SIGUSR2 stop it with a final record, and the record of an earlier epoch is
  kept as `final.json.previous`. A second holder for a held role exits 3 and
  changes nothing of the first. It is provisional and unqualified, and hosts no
  real lead; `ataniq-scratch` starts it.
