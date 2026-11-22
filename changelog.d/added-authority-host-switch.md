- Add `ah-switch`, the release switch of the authority host.
  `ah-switch --from INCOMING_DIR ID` imports a release folder into
  `/opt/authority-host/releases/ID` as new, read-only, root-owned files
  (folders and regular files only, within limits of entries, size and
  time), and `ah-switch ID` activates an imported release: it checks its
  `SHA256SUMS` over every file and, when the root-owned switch policy
  `/etc/authority-host/switch-policy.json` asks for it, its SSH signature by
  the policy's principal; refuses a counter not above the highest ever
  attempted or any counter the host shows (its links, its last recorded
  switch, its release folders), and a `highest-counter` below the counters
  used, before recording anything; runs the release's package step (before the check; a step
  that fails or is interrupted leaves the package state unknown, refusing
  further switches until the owner states the snapshot or the step runs
  again to the end; the step holds the switch lock and dies with it), its no-AI
  check (given the root and a listing of the firewall) and its validators;
  renames a new `current` link over the old one, reloads and checks every
  health command within one 60-second deadline, going back to the old
  release with that release's own reload and health lists, or stopping
  the decision services, with the host's own
  `/etc/authority-host/stop.list` first, when it cannot. All of it is one
  transaction journaled in `txn.json`, phase by phase (packages, links,
  activating, restoring, stopping, done), with the trust it began with;
  the next command resumes an interrupted one from its phase, a link
  counts as moved only once its folder is synced, a failed stop keeps the
  transaction open, and a retried request reports the outcome its
  interrupted transaction reached instead of doing it again.
  `ah-switch --rollback-to previous` goes back under the same policy,
  `ah-switch recover --counter N` restores a missing counter,
  `ah-switch recover --package-state SNAPSHOT` settles an unknown package
  state, `ah-switch recover --services-stopped` settles a transaction whose stop
  commands failed on the owner's word,
  `ah-switch status` shows the state under a shared lock, `--dry` prints
  every step and changes nothing, and `--root DIR` works on another folder
  (running programs there only with `--allow-exec`). The outcome is
  written to `/var/lib/authority-host/deploy.json` as stable failure codes,
  never a program's output. Exit codes are 0 ok, 1 failed, 2 usage and 3
  locked. See `deploy/authority-host/README.md`.
