- Add `ah-switch`, the release switch of the authority host.
  `ah-switch --from INCOMING_DIR ID` imports a release folder into
  `/opt/authority-host/releases/ID` as new, read-only, root-owned files
  (folders and regular files only, within limits of entries, size and
  time; staged in a new folder of its own, the only one it ever removes,
  and refused before anything is created or removed when the incoming
  folder is, contains or lies beneath `releases/` or anything in it; the
  incoming folder is opened once, without blocking or following a link,
  so a FIFO swapped in never holds the lock),
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
  `/etc/authority-host/stop.list` first (its programs are bare names or
  protected host programs like a release's; any other absolute path
  refuses the list, loudly, without blocking a journal's recovery), when it cannot (that list runs
  before the stopping phase is saved, and, while a journal is stopping,
  first in every command, under the lock, even when `releases/`, the
  counter or the record cannot be used). Every transaction
  first writes `/var/lib/authority-host/decisions.inhibit`; a
  decision-capable service may start while it is there (and answer its
  readiness probe) but refuses every decision while it is there or cannot
  be read, and while the journal `txn.json` is there, checking both before
  each decision (a healthy end removes the inhibit before the journal, so
  a power loss between the two still refuses); the switch removes it only
  when the transaction ends healthy, after running the reload list,
  health check and post-activation no-AI check (`--phase post`, the
  firewall and listeners as they are now) of `current` again when it
  finds a healthy transaction not yet settled (a reboot may have come;
  when any of it fails, the release not verifying again included, the
  host's stop list runs, the failure is journaled so that an interrupted
  settling never qualifies it away, a failed no-AI check is
  recorded as the check's verdict, the command and every retry of the
  same request exit 1 and the outcome stays owed with the failure until
  acknowledged); a
  failure keeps it across a reboot until a healthy switch or
  `ah-switch inhibit-clear --asked WORDS` (recorded with the time). All
  of it is one transaction journaled in `txn.json`, phase by phase
  (packages, links, activating, restoring, stopping, done), with the trust
  it began with;
  the next command resumes an interrupted one from its phase, a link
  counts as moved only once its folder is synced, a failed stop keeps the
  transaction open (so does a journal that cannot be saved as restoring,
  after the stops of the failed release and of the host ran), and every settled outcome is owed in its own file,
  `/var/lib/authority-host/owed.json` (written before the journal is
  removed, never in `deploy.json`, and not taken back by a printed result
  line): the same request run again reports it instead of doing it again
  and leaves it owed (a report that cannot be written exits 1),
  a rollback bound to its request id so that a retry never resolves
  `previous` anew, any other switch or rollback reports it and is refused,
  and only the owner's `ah-switch ack --txn ID[,ID...] --asked WORDS` takes
  it back (naming the transactions of every outcome owed, as `status` and
  the switch print them, so that an ack repeated after a newer outcome
  became owed is never taken for that one: a mismatch is refused and
  changes nothing, a repeat keeps the first receipt; recorded
  with the time; when the outcomes it prints cannot be written or flushed it
  records nothing, keeps them owed and exits 1, as a retry does; an ack
  interrupted after its record is finished by the next ack, which keeps
  the recorded words and time); `--dry` only shows it.
  `ah-switch --rollback-to previous` goes back under the same policy,
  `ah-switch recover --counter N` restores a missing counter,
  `ah-switch recover --package-state SNAPSHOT` settles an unknown package
  state, a journal that does not match the host (its links, or a counter
  below those attempted since) is never resumed until
  `ah-switch recover --discard-journal TXN` sets it aside (only a journal
  proven stale: one whose evidence cannot be read is neither resumed nor
  discarded),
  `ah-switch recover --services-stopped` settles a transaction whose stop
  commands failed on the owner's word,
  `ah-switch status` shows the state under a shared lock, `--dry` prints
  every step and changes nothing, `--root DIR` works on another folder
  (running programs there only with `--allow-exec`), and
  `--noaicheck PATH` runs another no-AI check only when it is protected
  root-owned code and the switch is not run through sudo (any protected
  host program, `/usr/bin/true` included, would pass, so a sudo rule for
  the switch never chooses it), bound to the transaction by its SHA-256
  and checked again before every run, from a private copy made from the checked
  descriptor and hashed again. A program of a release's lists is a file
  of the release (`{release}/bin/...`, in any list), a protected
  root-owned host program such as `/usr/bin/systemctl`, or a bare name in
  the fixed search path; any other absolute path is refused when the
  release is verified. The outcome is written to
  `/var/lib/authority-host/deploy.json` as stable failure codes, never a
  program's output. Exit codes are 0 ok, 1 failed, 2 usage and 3
  locked. Known limits (an old journal restored by root can replay a
  settled transaction; a package step's descendant that keeps the lock
  open keeps every command out until it is stopped) are in
  `deploy/authority-host/README.md`.
