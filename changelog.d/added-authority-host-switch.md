- Add `ah-switch`, the release switch of the authority host.
  `ah-switch --from INCOMING_DIR ID` imports a release folder into
  `/opt/authority-host/releases/ID` as new, read-only, root-owned files
  (folders and regular files only, within limits of entries, size and
  time; staged in a new folder of its own, the only one it ever removes,
  and refused before anything is created or removed when the incoming
  folder is, contains or lies beneath `releases/` or anything in it),
  recording the digest of the tree it made (paths, inodes, sizes,
  hashes), and `ah-switch ID` activates an imported release, refusing a
  folder without that record or that no longer matches it: it checks its
  `SHA256SUMS` over every file and, when the root-owned switch policy
  `/etc/authority-host/switch-policy.json` asks for it, its SSH signature by
  the policy's principal; refuses a counter not above the highest ever
  attempted or any counter the host shows (its links, its last recorded
  switch, its release folders, the append-only
  `/var/lib/authority-host/counters.log` of every counter attempted), and
  a `highest-counter` below the counters used, before recording anything;
  runs its no-AI check before activation (`--phase pre`: the release's
  units and the host, without ports and firewall) before and after the
  release's package step (a step that fails or is interrupted leaves the
  package state unknown, refusing further switches until the owner states
  the snapshot or the step runs again to the end; the step holds the
  owner-only switch lock and dies with it, and one that outlives its
  switch is named by its process id), then its validators;
  renames a new `current` link over the old one, reloads and checks every
  health command within one 60-second deadline, runs the no-AI check
  again (`--phase post`, with the live listeners and a listing of the
  loaded firewall) before recording success, going back to the old
  release (after stopping the failed release's services) with that
  release's own reload and health lists and its no-AI check, or stopping
  the decision services, with the host's own
  `/etc/authority-host/stop.list` first, when it cannot. Every transaction
  first writes `/var/lib/authority-host/decisions.inhibit`, which
  decision-capable units name in `ConditionPathExists=!` so they do not
  start while it is there, and removes it only when it ends healthy; a
  failure keeps it across a reboot until a healthy switch or
  `ah-switch inhibit-clear --asked WORDS` (recorded with the time). All
  of it is one transaction journaled in `txn.json`, phase by phase
  (packages, links, activating, restoring, stopping, done), with the trust
  it began with;
  the next command resumes an interrupted one from its phase, a link
  counts as moved only once its folder is synced, a failed stop keeps the
  transaction open, and a retried request reports the outcome it is still
  owed (`pending_report`, set before the journal is removed and cleared
  only once the result line is written, so a lost response is reported
  too) instead of doing it again, a rollback bound to its request id so
  that a retry never resolves `previous` anew; `--dry` only shows it.
  `ah-switch --rollback-to previous` goes back under the same policy,
  `ah-switch recover --counter N` restores a missing counter,
  `ah-switch recover --package-state SNAPSHOT` settles an unknown package
  state, a journal that does not match the host (its links, or a counter
  below those attempted since) is never resumed until
  `ah-switch recover --discard-journal TXN` sets it aside,
  `ah-switch recover --services-stopped` settles a transaction whose stop
  commands failed on the owner's word,
  `ah-switch status` shows the state under a shared lock, `--dry` prints
  every step and changes nothing, `--root DIR` works on another folder
  (running programs there only with `--allow-exec`), and
  `--noaicheck PATH` runs another no-AI check only when it is protected
  root-owned code, bound to the transaction by its SHA-256 and checked
  again before every run. The outcome is written to
  `/var/lib/authority-host/deploy.json` as stable failure codes, never a
  program's output. Exit codes are 0 ok, 1 failed, 2 usage and 3
  locked. See `deploy/authority-host/README.md`.
