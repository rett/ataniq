# Authority host deployment

The authority host is a separate host that holds the owner's deciding seat and must never
carry an AI agent tool or credential. This folder describes how a release of it is laid
out and activated and holds what a release ships besides its binaries: `share/` becomes
the release's `share/` (`/opt/authority-host/releases/<id>/share/`). No owner-local value
(a host name, an address, a user name, a key, a secret) belongs here or in a release: those
live only in `/etc/authority-host/` on the host.

## The switch: ah-switch

`ah-switch` (command `cmd/ah-switch`, package `internal/authorityhost/switcher`)
is the one way a release becomes live. It is run by hand, as root, on the
host. It needs only the Go standard library and opens no network
connection.

What this tree ships is the switch and the no-AI check program with its
lists. It ships no timer and no service unit: the recurring run of the
no-AI check (with what happens when it fails) and a deploy driver that
fills the incoming folder and calls the switch are separate planned items,
not part of this feature. Until they exist, the check runs only when a
switch runs it. The folder the switch imports is made by the release
builder, `ah-release` (see "The release builder" below).

### Layout

```
/opt/authority-host/
  releases/<id>/          id = <counter>-<short commit>, e.g. 0007-ab12cd3;
                          imported by ah-switch --from, read-only
    MANIFEST              key: value lines
    SHA256SUMS            every other file of the folder
    SHA256SUMS.sig        checked in signed mode (see the switch policy)
    bin/  etc/  share/
  current  -> releases/<id>
  previous -> releases/<id>
/etc/authority-host/
  switch-policy.json      the switch policy (optional; see below)
  stop.list               the host's own emergency stop list (required)
/var/lib/authority-host/
  deploy.json             the outcome of the last switch
  owed.json               the outcomes owed until acknowledged (see "The journal and recover")
  highest-counter         the highest counter a switch ever attempted
  counters.log            every counter a switch attempted, appended
  decisions.inhibit       while it is there, every decision service refuses every decision
  txn.json                the journal of the transaction under way
  switch.lock             the lock of a running switch
  imported/<id>           the record of the import that made releases/<id>
```

- The switch trusts `/var/lib/authority-host`, `/opt/authority-host` and
  `/opt/authority-host/releases` only when each of them, and every folder
  above it up to the root, is a real folder (not a symbolic link), owned by
  root and not writable by its group or by others; otherwise it stops
  before it changes or records anything. (When `ah-switch` does not run as
  root, as in tests, the folders of the user running it are accepted too.)
  It creates the state folder, mode 0755, when it is missing, holds each
  folder open while it works, and opens the lock, `highest-counter`,
  `counters.log`, `deploy.json`, `owed.json` and `txn.json` relative to that folder
  without following a link. Each of them must be a regular file with one link, owned by root (or
  that user) and not writable by its group or by others, and of a bounded
  size: a folder made safe after the fact can still hold a file another
  account can write or hold the lock of, so the files are checked as the
  folder is. The lock must moreover be owner-only (mode 0600) and empty, as
  the switch creates it: `flock` needs only a descriptor open for reading,
  so a lock file others can read (a migrated 0644 one) is a lock another
  account can hold. A file that fails this stops the switch before it
  changes anything, and an untrusted `deploy.json` is not even recorded
  over; for the lock the switch says what it must be: while no `ah-switch`
  runs, remove it, and the next command creates it again.
- The id is the counter, zero-padded to at least four digits, a hyphen and the
  short commit in lowercase hex (7 to 40 characters).
- `current` and `previous` hold exactly `releases/<id>`, as the switch writes
  them. Any other target (an absolute path, `./`, `..`, a doubled slash),
  even one that would clean to a release, is refused as `state-invalid`
  before anything is done; a target is only ever shown escaped.
- `MANIFEST` holds the keys `id`, `counter`, `commit`, `built_on` (the class of
  the build host, never its name), `go_version` and `package_snapshot` (1 to
  128 bytes of letters, digits and `._:-`, so that the journal and
  `deploy.json` that carry it always fit their readers), one
  `key: value` per line, and optionally `components: static` (no other
  value is accepted). `id` must be the folder's name, `counter` its counter
  and `commit` must start with its short commit. Blank lines and `#` lines are
  skipped; other keys are ignored.
- `SHA256SUMS` is in the format of `sha256sum`: lowercase hex, two spaces (or a
  space and `*`) and the path relative to the release folder, without `./`,
  `.` or `..`. It lists every file but the two top-level metadata files,
  `SHA256SUMS` itself and `SHA256SUMS.sig` (a file of that name anywhere
  else is listed like any other). Build it from inside the folder, for
  example
  `find . -type f ! -path ./SHA256SUMS ! -path ./SHA256SUMS.sig -printf '%P\0' | LC_ALL=C sort -z | xargs -0 sha256sum -- > SHA256SUMS`
  (`--` so that no file name is taken as an option).
- A release holds only folders and regular files with one link each. A
  symbolic link, device, fifo, socket or hard-linked file anywhere in it is
  refused, so hashing never follows one out of the folder.
- A release in `releases/` is made by the switch's import (see below), never
  by copying into `releases/` directly. Without `--from` the switch accepts
  a release folder (to switch to, to roll back to, or as the release to go
  back to) only when the import's record of it,
  `/var/lib/authority-host/imported/<id>`, is there (root-owned, checked as
  the state files are) and names the digest the tree has now: every path,
  its kind, its inode number and size, and every file's SHA-256. A folder
  copied in by hand has no record (`provenance-missing`), and one whose
  files changed or were replaced, even by the same bytes in a new file, no
  longer matches it (`provenance-mismatch`): a file that existed before the
  import could still be held open for writing by whoever made it, which no
  owner or mode check can see. Move such a folder out of `releases/` and
  import it again with `--from`, which makes a fresh copy (restoring
  `releases/` from a backup changes the inodes too). The folder must also
  have nothing writable by its group or by others, no set-user-id,
  set-group-id or sticky bit, and everything owned by root (or by the user
  running the switch when that is not root), as the import leaves it.
- In signed mode `SHA256SUMS.sig` is an SSH signature in the namespace
  `authority-host-release`:
  `ssh-keygen -Y sign -f KEY -n authority-host-release SHA256SUMS`.
- `share/` holds the command lists the switch runs. All four are required,
  and each must hold at least one command, unless the release's `MANIFEST`
  says `components: static` (a release with no decision services), when a
  list may hold comment lines only:
  - `validators.list`: one validator per line, such as
    `systemd-analyze verify {release}/etc/systemd/app.service`,
    `caddy validate --config {release}/etc/caddy/Caddyfile` or
    `nft -c -f {release}/etc/nftables/rules.nft`. `{release}` becomes the
    absolute path of the release folder, so a validator checks the new
    release, not the live one.
  - `reload.list`: one reload command per line, such as
    `systemctl daemon-reload` or `systemctl reload caddy`. These act on what
    `current` points at, and their arguments may not use `{release}` (a
    program of the release itself may: `{release}/bin/...`, see below).
    They must (re)start
    every decision service the release runs (`systemctl restart`, not only
    `reload`): going back to a release first stops the failed release's
    services, then runs the reload list of the release gone back to.
  - `health.cmd`: one or more health commands, one per line, each of which
    exits 0 when its part is healthy: usually the front end (`/healthz` on
    Caddy) and the application's readiness (`/readyz`). Every line must
    pass. Its arguments may not use `{release}` either.
  - `stop.list`: the commands that stop the decision services, such as
    `systemctl stop ataniq-seat`, run when no healthy release is left, and
    for the release a switch goes back from, before the earlier release is
    reloaded (see the failure cases); the static status page stays. No
    `{release}` in its arguments.

  In each list, blank lines and `#` lines are skipped. A list holds at most 64
  commands and a command at most 32 words. A command is its words
  separated by spaces; no shell runs it, so quotes, pipes, redirections, globs
  and variables are refused. The program is one of:

  - `{release}/PATH`, in any of the four lists: a file of the release
    itself, with an execute bit (`PATH` clean, inside the release), which
    was verified with it and is read-only since its import. It runs as
    `/opt/authority-host/releases/<id>/PATH`, that release's own folder.
  - An absolute path of a protected host program, such as
    `/usr/bin/systemctl`: a regular file with one link and an execute bit,
    owned by root and not writable by its group or by others, in folders
    that are all real folders (not symbolic links: `/usr/bin/systemctl`,
    not `/bin/systemctl` where `/bin` is a link), owned by root and not
    writable by group or others, from the root down; it is checked below
    `--root` and runs as that path below `--root` (the same path on the
    host).
  - A bare name looked up in `/usr/sbin:/usr/bin:/sbin:/bin` (never in the
    caller's `PATH`).

  Any other absolute path (a deployment workspace, a home folder, `/tmp`,
  a symbolic link, a file another account owns or can write) is refused
  when the release is verified (`list-invalid`), before anything runs: its
  bytes are not what was verified, and whoever can write them would choose
  what runs as root. Every program the switch runs gets the
  environment `PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C` and nothing else,
  the working folder `/`, no standard input, and its own process group, killed
  whole at its time limit.

  A signed release contains code that runs as root (its no-AI check, its
  package step, its units), so these lists are consistency checks of an
  honest release, not a privilege boundary: the signature is what decides
  what may run. For the same reason the switch does not check that the lists
  cover what the release ships (that `health.cmd` checks the application's
  readiness and not only the front end, that a release declared static has
  no decision service, that the validators check every changed rule file):
  that coverage is the release builder's job, and a health command that
  passes against a service still running from the old release is a
  packaging error the switch cannot see.
- `bin/ah-noaicheck` is the release's no-AI check, required. `bin/ah-pkgstep`
  is its package step, required when the release's package snapshot differs
  from the host's, and idempotent (see step 3).

### Putting a release on the host

The release folder is first copied, as the deploying user, into an incoming
folder that user owns, outside `/opt/authority-host`; then root imports it
and switches to it:

```
rsync -a --delete RELEASE/ HOST:INCOMING/<id>/     # as the deploying user
ah-switch --from INCOMING/<id> <id>                 # as root, on the host
```

The incoming folder must lie outside `releases/`: one that is, contains
or lies beneath `releases/` or anything in it (a release, a staging folder
of an import), compared by the real path with every symbolic link resolved
and by device and inode from the folder up to the root (which a bind mount
keeps), is refused (`incoming-overlaps`) before anything is created or
removed, so an import can never read what it writes or remove its own
input.

The incoming folder belongs to the deploying user, who can swap it for
something else while the switch holds its lock, so it is opened once, with
`O_DIRECTORY`, `O_NOFOLLOW` and `O_NONBLOCK`: a FIFO (even one nobody
writes to), a device or a link put in its place is refused at once
(`release-not-folder`), never waited on, and a different folder is refused
(`file-changed`). The opened descriptor must be a folder and the one the
switch first looked at, and the copy reads through that descriptor, never
through the name again. This needs `/proc`, as the switch's check of its
own and the package step's processes does; without it the incoming folder
is refused (`read-failed`).

`--from` copies the incoming folder into a fresh folder of `releases/`, the
import's staging folder, as new files. The staging folder is
`releases/.import-<id>-<16 random hex digits>`, made new for this import
(`mkdir`, which fails on a name that is there), and it is the only folder
the import ever removes: a folder another import left (a switch that died
while it imported) is neither used nor removed, and every `--from` names
it (`warning: releases/.import-... was left by an earlier import`) for the
owner to look at and remove by hand. The copy holds folders and
regular files only (a symbolic link, device, fifo, socket or hard-linked
file is refused), each file opened without following a link and without
blocking, checked to be the regular file with one link that was listed (the
same inode, of the size the walk saw), and read to that size (one that
changes while it is copied is refused). Modes are
set, not copied: folders 0755 and files 0644, or 0755 when the incoming file
had any execute bit; no set-user-id, set-group-id or sticky bit, extended
attribute or ACL is carried over, and when the switch runs as root the copy
is owned by root:root. Only that copy is then verified, signature-checked,
run and linked; the incoming folder can change afterwards without touching
it. Once the copy is verified it is made read-only (every write bit
removed), its record is written to
`/var/lib/authority-host/imported/<id>`, and it is renamed to
`releases/<id>`. A copy that fails to import or to
verify is removed; `releases/<id>` must not exist already, or `--from` is
refused. A release that was imported stays when a later step fails: switch
to it again without `--from`.

The work of reading a release is bounded, for the import and for the
verification of any release: at most 20000 entries (folders and files),
2 GiB in all and 512 MiB per file, and 10 minutes for the import and the
hashing together. The import charges the bytes it actually writes to the
2 GiB, as it writes them, so an incoming folder that changes during the copy
cannot make it write more: the copy stops at the limit. Going over a limit
fails with its own code (`too-many-entries`, `too-large`, `file-too-large`,
`time-budget`), removes the import's copy and releases the lock. Folders
are read in batches with one folder open at a time, every file is opened without blocking, and a
file that changes size or time, or stops being a regular file, while it is
read is a failure (`file-changed`).

### The switch policy

`/etc/authority-host/switch-policy.json` (below `--root`) says how strict
every switch must be: a forward switch, `--rollback-to previous`, the
automatic return to the old release after a failed health check, and the
resume of an interrupted transaction (which also keeps the trust the
transaction began with; see "The journal and recover").

```
{"min_mode": "signed",
 "allowed_signers": "/etc/authority-host/allowed_signers",
 "principal": "release-signer"}
```

- `min_mode` is `manual` or `signed`, the weakest mode a switch may use.
  `--mode` may only be as strict or stricter.
- `allowed_signers` is the ssh allowed signers file and `principal` the
  identity a release must be signed by; both are required for signed mode.
  The signature namespace is fixed: `authority-host-release`.
- No other key is accepted. The file must be exactly one JSON object with
  each key given once and spelled exactly as above: a second object or any
  other text after it, a key given twice (which a plain JSON reader would
  settle by keeping one of them), or a key in other letter case
  (`MIN_MODE`, `Min_Mode`), alone or beside the key itself (which a plain
  JSON reader would take for that key), is refused as `policy-invalid`.
  The state files (`deploy.json`, `txn.json`, the import records) are read
  with the same checks. The policy file, the signers file
  and every folder above them must be owned by root and not writable by
  group or others (the same check as the state folder); otherwise, or when
  the policy does not parse, the switch does nothing but the stops a
  transaction still owes: under the lock, a journal in `stopping` (see
  "The journal and recover") is resumed first, with the trust it began
  with, since stopping needs no signers file (the host's stop list runs,
  and each release's list when the release still verifies); then the
  error is reported, and nothing else is resumed or started. `recover`
  does the same, and `recover --services-stopped`, which runs nothing,
  needs no policy.
  Each file is read from the descriptor that was checked, never opened
  again by its name, and `ssh-keygen` is given a private copy of the
  signers file made from that descriptor (mode 0600, in a new folder of
  mode 0700, removed afterwards), so what is verified against is what was
  checked.
- Without the file the policy is manual: unsigned releases are accepted.
  `deploy.json` then records `trust: manual`, and `status` says
  `trust: manual: no switch policy, unsigned releases are accepted`.

### Commands

```
ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] [--from INCOMING_DIR] ID
ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] --rollback-to previous
ah-switch [--root DIR] status
ah-switch [--root DIR [--allow-exec]] [--dry] recover --counter N
ah-switch [--root DIR [--allow-exec]] [--dry] recover --package-state SNAPSHOT
ah-switch [--root DIR [--allow-exec]] [--dry] recover --services-stopped
ah-switch [--root DIR [--allow-exec]] [--dry] recover --discard-journal TXN
ah-switch [--root DIR] [--dry] inhibit-clear --asked WORDS
ah-switch [--root DIR] [--dry] ack --asked WORDS
```

- `--root DIR` is the folder that holds `opt/authority-host`,
  `var/lib/authority-host` and `etc/authority-host`; it is `/` on the host
  and a scratch folder in tests. It is also passed to the no-AI check, which
  then scans that folder. The programs a switch runs (validators, the
  package step, reloads, the stop list and health commands) act on the host
  itself whatever the root, so with a root other than `/` a switch or
  rollback is refused (exit 2) unless `--allow-exec` is given too, which is
  meant only for an isolated test runner whose commands touch nothing live.
  Without it only `--dry` and `status` work there; they run no program.
- `--mode manual` checks `SHA256SUMS` only; `--mode signed` also checks
  `SHA256SUMS.sig`. Without `--mode` the switch policy's minimum applies; a
  mode weaker than the policy is refused (exit 2), and `--mode signed`
  needs the policy's `allowed_signers` and `principal`.
- `--from INCOMING_DIR` imports the release from that folder first (see
  above). With `--dry` nothing is imported: the incoming folder is checked
  in place, apart from the owner and the modes, which the import would set.
- `--noaicheck PATH` runs another no-AI check program instead of the
  release's `bin/ah-noaicheck`. `PATH` is an absolute path on the host
  (below `--root`), and the program must be protected code: a regular
  file with one link, owned by root and not writable by its group or by
  others, in folders that are all real folders, owned by root and not
  writable by group or others, from the root down; otherwise the switch
  is refused at noaicheck before anything runs. Its SHA-256 is taken
  through the descriptor that was checked and journaled with the
  transaction, and before every run of it (each phase, a resumed one
  included) it is checked again and copied from the descriptor that was
  checked into a private copy (mode 0700, in a new folder of mode 0700 in
  the state folder, removed after the run), hashed as it is copied and
  hashed again from the copy; that copy is what runs, so an override
  replaced meanwhile (while the switch lists the firewall, for example)
  never runs. A program or copy that no longer matches is not run
  (`noaicheck-changed`), which fails the check. The default, the imported
  release's own checker, is a file of the verified, read-only release and
  runs where it is.
- `--rollback-to previous` goes back to the release `previous` points at. It
  accepts that release's id too, and refuses any other.
- `--dry` prints every step and what it would do, runs no program and changes
  nothing: no link, no state file, no lock file. It still reads and checks
  the release (sums, manifest, lists, the record of its import, the
  counter, the package state, a `--noaicheck` program) and the host's
  stop list, and it is refused while a switch holds the lock or a
  transaction is still to resume (a journal that does not match the host
  included). A dry run of a request whose outcome is owed (see "The
  journal and recover") shows that outcome and changes nothing either:
  the outcome stays owed to the real request; a dry run of another
  request while one is owed is refused as the request would be.
- `status` prints `current`, `previous`, the highest counter, the trust of
  the switch policy, a transaction still to resume and its phase (with the
  stop commands that failed, when it is stopping), the host's stop list,
  whether decisions are inhibited, every outcome owed
  (`outcome owed (request ..., transaction ...)`), the owner's last
  acknowledgement, and the last outcome from `deploy.json`. It takes the
  lock shared, so what it prints is
  one state between two switches: while a switch runs it exits 3, and
  while it reads, a switch is refused with exit 3. It prints all of it or
  nothing, and it changes nothing.
- `recover --counter N` writes a missing or broken `highest-counter`,
  `recover --package-state SNAPSHOT` settles an unknown package state,
  `recover --services-stopped` settles a transaction whose stop commands
  failed, and `recover --discard-journal TXN` sets aside a journal that
  does not match the host; see "The journal and recover".
- `inhibit-clear --asked WORDS` removes the decision inhibit on the
  owner's word; see "The decision inhibit".
- `ack --asked WORDS` acknowledges the outcomes owed on the owner's word;
  see "The journal and recover".

Flags may come before or after the id.

### Steps

`ah-switch ID` prints each step and stops at the first failure. Up to the
renames in step 6, a failure leaves `current` as it was (a package step
that already ran stays applied, and `deploy.json` says so, or says that
the package state is unknown; after a package step the decision services
are stopped and decisions stay inhibited, see step 4).

0. **import** (only with `--from`): the incoming folder is copied into a
   staging folder of its own, `releases/.import-ID-RANDOM`, as described
   above.
1. **verify**: `releases/ID` (or the import's copy) must be a folder, not a
   symbolic link, and pass the check of an imported release above (without
   `--from`, the record of its import included). Every
   file in it must be listed in `SHA256SUMS` and match it; a file not
   listed, a listed file missing or changed, a listed path that escapes the
   folder or is not clean, or a link or special file in the folder is a
   failure. In signed mode the signature must verify:
   `ssh-keygen -Y verify -f ALLOWED_SIGNERS -I PRINCIPAL -n authority-host-release`
   checks the sums, with the signers file and the principal of the switch
   policy (the principal is never discovered from the signature, so another
   identity of a shared signers file cannot authorize a release). Then
   `MANIFEST` must match the id and the command lists must parse.
2. **counter**: the counter must be above the highest ever attempted
   (`highest-counter`) and above every counter the host shows besides it:
   the releases `current` and `previous` point at, the counter of the last
   forward switch `deploy.json` records as having used its counter
   (`counter_used`), the highest counter of `counters.log`, and every
   other release folder in `releases/` (the counter of its name, and of
   its `MANIFEST` when that can be read), so a `highest-counter` restored
   from an older state cannot admit an old release again. A rollback skips
   this rule but accepts only the release `previous` points at, and only
   when that release folder is there.

   `counters.log` holds one line `COUNTER ID TIME` per forward switch that
   used its counter, appended and synced (with its folder) before
   `highest-counter` is written, and checked like the other state files.
   It is only ever appended to, so it keeps the evidence of an attempted
   counter that nothing else shows any more: `deploy.json` recording a
   later rollback or failure, the release folder removed, or
   `highest-counter` restored from an older copy. A line cut short by a
   crash (the end of the file after its last newline) is not an attempt
   and is cut off before the next line is appended; any other line that
   does not parse stops the switch, as unreadable evidence does.

   Before any of the steps, and before anything is recorded, a switch or
   rollback checks `highest-counter` itself against the counters the host
   used (the links, `deploy.json` and `counters.log`): one that cannot be read
   (`counter-invalid`), one missing on a host that used counters
   (`counter-missing`; none means 0 only on a host that has no `current`
   yet), or one below a counter used (`counter-invalid`) stops it, with
   nothing recorded, so that `deploy.json` stays as evidence for `recover`.
   What the evidence needs and cannot read (a link, `deploy.json`,
   `counters.log`, the folder of releases) stops it the same way; it is
   never taken as no evidence.
3. **packages**: when the manifest's `package_snapshot` differs from the one
   recorded in `deploy.json`, the release's
   `bin/ah-pkgstep --release DIR --from OLD --to NEW` runs, within 30 minutes
   (taking a filesystem snapshot first is that program's job). The no-AI
   check of step 4 runs once before it, on the release's own inputs, while
   nothing has changed or begun, and once more after it, with the
   validators, on the host as the step left it. The transaction begins
   (`decisions.inhibit`, then the journal) just before the step runs. A
   changed snapshot and no `bin/ah-pkgstep` is a failure
   (`package-step-missing`). The step's progress is journaled in
   `txn.json` (`started`, with its process id and start time once it runs,
   then `done`), and on success the new snapshot is what `deploy.json`
   records from then on, even when a later step fails.

   A step that exits with a failure, or is interrupted, may have changed
   some packages: the host's package state is then **unknown**.
   `deploy.json` says `package_state: unknown` with the two ends of the step
   (`package_from`, `package_to`), and records neither as the host's
   snapshot. Every later switch and rollback is refused
   (`package-state-unknown`, nothing run) until either the owner states
   which end the host is at, with
   `ah-switch recover --package-state SNAPSHOT` (recorded with the time in
   `package_stated_at`), or a switch to a release of the snapshot the step
   went to runs the step again, from the same `--from`, to the end. So
   `bin/ah-pkgstep` must be idempotent: run twice with the same `--from`
   and `--to` it must end in the same state.

   The step never overlaps another: it runs in its own process group, is
   killed when the switch dies (where the system has a parent-death signal),
   and is given the switch's lock file as its descriptor 3, so the lock
   stays held as long as the step or anything it started keeps that
   descriptor open. The journal records the step's process id and start
   time, and the process id and start time of the switch driving the
   transaction; a command refused by the lock says which holds it: `an
   active ah-switch (process N) holds the lock`, or `surviving helper pid
   N (start time matches): stop it or reboot` when the switch is gone and
   its package step still runs (exit 3 either way, nothing killed or
   changed). Stop that step by hand, or reboot, then run the command
   again, which settles the transaction. Before an interrupted step is settled, the switch makes
   sure it has ended: a process of the journaled id and start time still
   running has its process group killed; a process of that id with another
   start time is another process; processes left in its group with no
   such process are refused (`package-state-unknown`), and are the owner's
   to stop.
4. **noaicheck**: the new release's no-AI check runs in its first phase,
   `bin/ah-noaicheck --phase pre --root ROOT --release DIR --require-policy`,
   and must exit 0 within 5 minutes: before the package step when there is
   one (a failure then changes nothing), and before the validators on the
   host as it now is. A failure of the second, or of a validator, after a
   package step ran is a failure of a host that changed and that nothing
   qualified since: the transaction stops the decision services (the
   stopping phase, below), decisions stay inhibited, and it exits 1. This
   phase scans the release's own files (the units under its `etc/systemd`
   and the environment names they set) and the host's AI binaries and
   credentials, but not the listening ports and the firewall digest: those
   are still the old release's until the reload, and are checked in step
   8. Any other exit status is a
   failure, including the check's own report that it could not finish
   (`HIT incomplete`): it is never a warning.
5. **validate**: each command of `share/validators.list` must exit 0, within
   30 seconds each.
6. **switch**: first the journal `txn.json` says `links` (and, on a forward
   switch, that the counter is used) and is synced with its folder; on a
   forward switch the counter is then appended to `counters.log` and
   `highest-counter` is set to the new counter: it
   holds the highest counter ever attempted, so a release whose switch got
   this far cannot be switched to again, even after it failed or was put
   back; build the next counter. Then `previous` is pointed at the old
   target of `current`, and a new link `current.new` is made and renamed
   over `current`, so there is never a moment without `current`; each
   rename is followed by a sync of the folder. Then the journal says
   `activating`, and each command of `share/reload.list` runs, within 30
   seconds each.
7. **health**: the commands of `share/health.cmd` run in order until one
   round in which every one of them exits 0, a new round every 2 seconds.
   The 60 seconds are one deadline for the whole check: each command gets at
   most 10 seconds and never more than what is left of the 60, no command
   starts once they are over, and a round that ends after them does not
   count. A static release with no health command is healthy at once.
8. **postcheck**: the no-AI check runs again, in its last phase, on the host
   as the reload left it: the switch runs `nft list ruleset` (within 30
   seconds; its output at most 4 MiB), writes what it printed to a file of
   mode 0600 in a new temporary folder of mode 0700, and runs
   `bin/ah-noaicheck --phase post --root ROOT --release DIR --require-policy --nft-listing FILE`,
   which checks everything, the live listeners and the loaded firewall
   against the release's pinned digest included, and must exit 0 within 5
   minutes; the folder is removed afterwards. A failure, or a failing `nft`
   (`nft-listing-failed`, and the check does not run), goes back as a
   failed health check does, so a release whose units open a new listener
   or set a provider variable is never recorded as `ok`. The check program
   is journaled, so a resumed activation runs the same one. Going back to
   the old release runs the check again, its own (or the journaled
   override), on the host as it then is: the package step may have changed
   it since that release passed.
9. **record**: the journal says `done` with the outcome; the link folder is
   synced once more, `deploy.json` is written (a temporary file renamed over
   the old one), the outcome is added to `owed.json` (see "The journal and
   recover"), the decision inhibit is removed when the transaction
   ended healthy (or kept, with the outcome as its reason), and the
   journal is removed. Only then does the switch print `result: ok` and
   exit 0. When any of that fails it prints `result:
   unresolved`, exits 1 and leaves the journal, and the next command
   records the outcome.

A link is taken as moved only once its folder is synced. A rename that is
visible but whose folder could not be synced is not trusted to survive a
power loss: the switch then puts both links back and goes back (as the
resume of the `links` phase does, below); if that cannot be synced
either, it stops with
`result: unresolved` and the journal still in `links`, and the next
command syncs and settles it. No outcome is recorded, and the journal is
never removed, before the link folder is synced.

`--noaicheck PATH` replaces the release's check in both phases, checked
again against the SHA-256 its transaction journaled before each run and
run from a private copy made before the run (in phase post, before the
firewall listing).

Before the switch, in step 1, the release `current` points at (the
fallback) is opened and verified as the switch policy says, and its own
reload and health lists are read. A fallback that is missing or no longer
verifies is not used, and the switch says so (`fallback: none: ...`).

If the reload, the health check or the post-activation check fails, the
journal says `restoring` before anything is put back. First the failed
release's services are stopped, so that nothing only it runs survives
going back: its own `stop.list` when it still verifies, the host's own
stop list when it does not. Then `current` is renamed back to the
fallback, `previous` is put back as it was, both synced, and the
fallback's own `reload.list` and `health.cmd` run (never the failed
release's), then the no-AI check of its phase post (the fallback's own
`bin/ah-noaicheck`, or the journaled `--noaicheck` program, with a new
firewall listing), on the host as it now is. The result is `rolled-back`
with `restored_health: ok` only when all of that succeeded, putting
`previous` back included. Otherwise (or with no
fallback: the very first switch, or one that cannot be used, when
`current` stays at the new release) the decision services are stopped
(the static status page stays): first the host's own
`/etc/authority-host/stop.list`, which runs before the journal is saved
as `stopping`, so that a journal that cannot be written does not keep the
services running (the command then prints `result: unresolved`, and the
next command runs the stops again); then, once the journal says
`stopping`, the stop list of the new release and of the fallback, each
one that verifies (a release that does not verify runs no code; the
host's list does not depend on any release).
The result is `failed` on a first switch and `failed-recovery` otherwise,
with `restored_health: failed` when going back was tried, and
`services_stopped: true`; the console is needed, and decisions stay
inhibited. A stop command that fails
does not stop the others, but the switch then never says the services are
stopped: the journal stays in `stopping` with the commands that failed,
the command prints `result: unresolved` and exits 1, and every later
command runs the stops again first (see below).

The host's stop list is the owner's, never part of a release: one command
per line, in the format of the release lists, at least one command, and
owned and checked like the switch policy. It must stop every decision
service of any release (for example `systemctl stop ataniq-seat.service`).
A switch or rollback is refused while it is missing, empty, not
trusted or refused (see below), and `status` says so.

The programs of the host's list are checked as those of the release lists
are, except that a file of a release is not possible there: a bare name
(the fixed search path) or an absolute path of a protected host program
(root-owned, not writable by group or others, one link, an execute bit,
in folders the switch trusts), which then runs as that path below the root.
Any other absolute path (a deployment workspace, a home folder, `/tmp`,
a link) is refused when the list is loaded, with the code
`stop-list-refused` and a fixed reason, and the whole list is refused: the
list runs as root whenever the decision services must be stopped, so
whoever can write a helper it names would choose what runs. An older list
that names such a helper must be changed to name a protected program.

A refused list would otherwise block the recovery it exists for, so it is
reported loudly and does not stop the rest: the command prints
`reconcile: FAILED: the host's stop list cannot be used`, a journal in any
phase (not only `stopping`) is resumed, and the steps that need the host's
list (the stopping phase, going back when the new release's own stop list
does not verify) fail with `stop-list-refused` in their detail, so that
a transaction that needed it stays in `stopping` with the failure, while
the stop lists of the releases that verify run. Only new work is refused,
with exit 1, once that resume has run.

While a journal says `stopping`, every switch, rollback and `recover`
(but `recover --services-stopped`, which runs nothing) runs the host's
stop list first, under the lock, right after taking it and before
anything that could fail on the way to the resume: `releases/` missing or
not trusted, `highest-counter`, `counters.log` or `deploy.json` that
cannot be read, a journal that cannot be checked against the host, a
switch policy that cannot be used. The command then goes on as it would
(the resume runs the release lists that verify, and records the outcome
when every stop passed), or stops where it fails, with the journal still
in `stopping`; one command never runs the host's list twice.

### The decision inhibit

`/var/lib/authority-host/decisions.inhibit` is the switch's own file
(root-owned, mode 0644, written whole and synced with its folder): while
it is there, every decision-capable service refuses every decision. The
contract for those services is:

- A decision-capable service **may start** while the file is there: its
  unit carries no start condition on it. It must, since the switch's
  health check probes its readiness while decisions are still inhibited.
  An earlier version of this guide told the release builder to add
  `ConditionPathExists=!/var/lib/authority-host/decisions.inhibit` to every
  such unit; that condition kept the service from starting, so its
  readiness probe could never pass. Do not add it, and remove it where it
  was added.
- The application **must refuse every decision** while the file is there:
  it checks the file before each decision (not once at start), and
  refuses when the file is there or when whether it is there cannot be
  told (any error other than "no such file"). Readiness and the static
  status page do not depend on it.

The release builder makes every decision-capable service follow this
contract (the switch does not check the services for it). The file holds
the id of the transaction that set it and why (`txn`, `reason`, `time`);
only its existence matters to the services.

- The switch writes it at the start of every transaction: before the
  package step, or, without one, before the journal says `links`. A
  resumed transaction whose file is gone gets it again before anything
  of it runs.
- It is removed only when the transaction ends healthy: a switch or
  rollback whose release passed its health check and its post-activation
  no-AI check (`ok`), or a going back whose release passed its reload, its
  health check and its no-AI check of phase post (`rolled-back` with
  `restored_health: ok`). The removal is synced before the journal is
  removed.
- A healthy transaction found in phase `done` by a later command (a
  reboot, or a crash, came between its health check and its settling)
  is not taken as healthy on its word: the release `current` points at,
  verified again, runs its `reload.list` and its `health.cmd` again,
  decisions still inhibited, before the inhibit is removed. When either
  fails, the outcome is recorded as it was decided, with
  `after an interruption in phase done, ... did not pass its reload and
  health check again` in `detail`, decisions stay inhibited, and the
  command exits 1 whatever it was asked: it never reports the decided
  `ok`. Because the reload list was started, a service may be running, so
  the host's stop list runs (a failing stop command is added to `detail`).
  The failure is kept apart from the decided outcome: the outcome in
  `owed.json` carries `requalify_failed: true`, and while it is owed the
  same request run again fails again (exit 1, `decisions stay inhibited`)
  and leaves it owed, instead of reporting the old `ok` once and taking it
  back; `ah-switch ack --asked WORDS` takes it back, and the inhibit stays
  until a healthy switch or `inhibit-clear`. `status` says so while the
  journal is there (`requalify: ...`) and afterwards on the owed outcome.
- Any other end keeps it, with the outcome as its reason: a failure after
  the package step changed the host, a failed recovery, a first switch
  that failed (with its release still `current`), an interrupted package
  step. It survives a reboot (it is a file), so a failed release's
  decision services refuse decisions after a reboot too. `recover
  --services-stopped` keeps it too: that the services are stopped is not
  that the host is qualified.
- Besides a later transaction that ends healthy, only the owner removes
  it: `ah-switch inhibit-clear --asked WORDS`, after looking at the host,
  with in `WORDS` what was asked or seen (1 to 512 bytes). The time and
  the words are recorded in `deploy.json` (`inhibit_cleared_at`,
  `inhibit_cleared_asked`) and kept by later records. It runs no program,
  is refused while a transaction is open (run any command to settle it
  first) or when there is nothing to clear, and with `--dry` changes
  nothing. Removing the file does not start anything: the services take
  decisions again only where they run; start them by hand, or switch.
- A failure before any transaction begins (verify, counter, the no-AI
  check before the package step, or any step when there is no package
  step) changes nothing on the host, and neither sets nor removes it.
- `status` says `decisions: inhibited (...)` with the transaction and the
  reason, or `decisions: not inhibited`.

### The journal and recover

Everything a switch or rollback changes on the host is one transaction,
journaled in `txn.json`: written whole and synced with its folder before
every change of its phase. It holds the transaction's id, its direction
(`forward` or `rollback`), what was asked (`previous` or an id), the
release, its counter, the old targets of `current` and `previous`, the
trust the transaction began with (the mode and, when signed, the signers
file and principal: the stricter of the policy and `--mode`), the package
step's snapshots and progress, and once known the outcome. Its phases, in
order:

| Phase | What it means | How the next command resumes it |
| --- | --- | --- |
| `packages` | The package step runs or ran; no link was touched | Makes sure the step has ended, then stops the decision services (the host changed, and nothing checked it) and records the switch as `failed`: `interrupted` when the step was done, or `package-step-interrupted` with the package state unknown when it was not |
| `links` | The counter is used and the links are being moved; nothing was reloaded | Puts `current` and `previous` back exactly as they were, then goes back as a failed health check does (restoring), so that the old release is qualified again: `rolled-back` with the code `interrupted`; on a first switch the decision services are stopped and it records `failed` |
| `activating` | The links point at the new release; its reload, health and post-activation check run | Verifies the release again, makes and syncs both links again, runs its reload list, health check and post-activation check again: `ok` (`completed after an interruption`), or goes back as a failed health check does |
| `restoring` | Going back to the old release | Does all of going back again, from stopping the failed release's services |
| `stopping` | The decision services are being stopped | Runs every stop list again |
| `done` | The outcome is decided | Syncs the link folder, appends to `counters.log` (when it lacks the counter) and writes `highest-counter` when the counter was used; when the transaction ended healthy, runs the reload list and health check of the release `current` points at again (a reboot may have come; see "The decision inhibit"); records the outcome, removes the inhibit only when that passed, and removes the journal |

A transaction that dies (a crash, a power loss, a kill) leaves `txn.json`.
Every switch, rollback and `recover` takes the lock and resumes it from its
phase before anything else: each phase is redone from its start, which is
safe to do twice, and where the links point never decides what happens.
`status` shows it (`interrupted: ... in phase ... (transaction TXN)`) and
changes nothing, and `--dry` refuses to go on while it is there. A resume
verifies releases with the trust the transaction began with as well as the
policy of the time, so a signed transaction is never finished, or gone
back from, as a manual one. A journal that does not parse, names an
unknown phase or is not trusted is never guessed at: the command fails and
names it.

Before anything of a journal is resumed, it must match the host: in its
`packages` phase `current` and `previous` must be the journal's old
targets, in any later phase each must be its old target or its new one,
and a forward journal's counter must not be below the counters the host
attempted (`highest-counter`, `counters.log`, the last forward switch of
`deploy.json`, the releases the links name). A journal that does not
match (one restored from a backup, or written by hand) is stale: resuming
it would move the links to its releases. It stays unresolved: the command
prints `reconcile: unresolved: ...`, runs and moves nothing, and exits 1,
and so does every later command until the owner, having looked at the
host, runs `ah-switch recover --discard-journal TXN` with the id `status`
shows. That renames the journal to `txn-TXN.discarded` in the state
folder (kept as evidence), resumes nothing of it and prints the time; it
refuses a journal that matches the host (any command resumes that) or
another id.

Only a journal proven stale is discarded: one whose links or counter the
host's evidence contradicts. When that evidence cannot be read (a link,
`highest-counter`, `counters.log`, `deploy.json` or `releases/` that
cannot be read), nothing is proven: the journal is unresolved, neither
resumed (`reconcile: unresolved: ...; the journal cannot be checked
against the host`) nor discarded (`recover --discard-journal` refuses
it), until the evidence is readable again.

When the resume ends with the decision services stopped, the command that
resumed it does nothing else: a new switch never starts on its own after a
failed recovery. Run it again once the host is looked at.

Every settled transaction owes its outcome to whoever asked for it, and
that outcome is kept in its own file, `/var/lib/authority-host/owed.json`
(written whole and synced, mode 0644, read with the checks of the other
state files), never in `deploy.json`: a later record, a refusal's
included, cannot lose it. Each outcome there holds the transaction's id
(`txn`), its request id (`request_id`: its direction and the release it
went to, `forward:ID` or `rollback:ID`), its `direction`, what was asked
(`requested`), the releases (`id`, `from_id`, `current_id`), its `result`,
`failed_step`, `restored_health`, `services_stopped`, `detail` and
`time`. It is written before the journal is removed, by whichever command
settles the transaction, and it stays owed after the switch printed its
result line: a line written to the switch's output is no proof that it
reached the one who asked (a remote session can drop it). Only two
things take it back:

- **The same request, run again**, reports the outcome and does nothing
  else (`settled: this ... has been settled already; it is not done
  again`), and the outcome is no longer owed. A rollback is bound to its
  request id: retried as `--rollback-to previous` (or by the id it went
  to), it reports the rollback whose outcome is owed and never resolves
  `previous` anew, which would go back to the release the first rollback
  left. A forward switch is retried by the same id. Run the request once
  more to do it again. When the report cannot be written or flushed (a
  full filesystem, a dropped session) the outcome has not been delivered:
  it stays owed, the command exits 1 and says so on its error output, and
  the next retry reports it.
  An outcome whose requalification failed (see "The decision inhibit")
  is the exception: it is reported with exit 1, stays owed, and only
  `ack` takes it back.
- **`ah-switch ack --asked WORDS`**, the owner's acknowledgement that the
  outcomes were seen, with in `WORDS` what was asked or seen (1 to 512
  bytes). It prints every outcome owed, records the time, the words and
  the transactions in `deploy.json` (`outcome_acked_at`,
  `outcome_acked_asked`, `outcome_acked_txn`, kept by later records), and
  removes `owed.json`. It runs no program, is refused while a transaction
  is open (run any command to settle it first) or when nothing is owed,
  and with `--dry` changes nothing.

Any other switch or rollback while an outcome is owed (another release,
or one that is not there) first reports the outcomes owed (`owed: the ...
ended ...`), leaves them owed and is refused, exit 1, with nothing
recorded or run. A dry run only shows the outcome (see `--dry`);
`recover`, `status` and `inhibit-clear` are not refused, and
`recover --package-state` keeps it owed. An `owed.json` that does not
parse, is not trusted or does not describe a transaction refuses every
switch, rollback and `ack` (`state: FAILED`): it is never taken as
nothing owed.

`ah-switch recover --counter N` writes `highest-counter` as `N` when it is
missing, does not hold a number, or holds less than the counters the host
shows (see step 2). The switch never guesses it: on a host with a
`current` link a missing counter could let an old release be replayed.
`N` must be at least every counter the host shows, the release folders
included; take it from the newest release ever deployed. It refuses to
overwrite a counter that is at least all of that.

`ah-switch recover --package-state SNAPSHOT` settles an unknown package
state: `SNAPSHOT` must be one of the two ends of the step that did not
finish, the one the host is at. It is recorded as the host's snapshot with
the time in `package_stated_at`, and switches go on from there.

`ah-switch recover --services-stopped` settles a transaction that stays in
`stopping` because a stop command keeps failing: it is the owner's
statement that the decision services are stopped (by hand, or the failing
command was not needed). It runs nothing, and records that statement with
the time in `detail`, with `services_stopped: true`. Use it only once every
decision service is really stopped. It leaves the decision inhibit (see
"The decision inhibit"); `inhibit-clear` removes that.

### deploy.json

`deploy.json` (mode 0644) holds `id` (the release asked for), `previous_id`
(what `current` pointed at before), `current_id` (what it points at after),
`counter`, `mode` (how the release was verified), `trust` (the policy's
minimum, `manual` without a policy), `rollback`, `time` (UTC), `result`
(`ok`, `failed`, `rolled-back` or `failed-recovery`), `check` (the no-AI
check: `passed`, `failed` or `not run`), `failed_step` (`import`,
`verify`, `counter`, `packages`, `noaicheck`, `validate`, `switch`,
`reload`, `health`, `postcheck`, `record` or `interrupted`; empty when ok),
`error`, `detail`, `package_snapshot` (the snapshot the host's packages are
known to be at), `package_state` (`unknown` after a package step that did
not finish, with `package_from` and `package_to`; empty when known),
`package_stated_at` (when the owner last stated it), `counter_used` (a
forward switch that got as far as using its counter: evidence of that
counter for step 2), `restored_health` (`ok`, `failed` or empty),
`services_stopped` (the decision services were stopped), and `txn`,
`requested` and `request_id` (the transaction this record ends, what was
asked, and its direction and release as `forward:ID` or `rollback:ID`;
whether its outcome is still owed is in `owed.json`, not here),
`inhibit_cleared_at` and `inhibit_cleared_asked` (when the owner last
removed the decision inhibit by hand, and the words given), and
`outcome_acked_at`, `outcome_acked_asked` and `outcome_acked_txn` (when
the owner last acknowledged the outcomes owed, the words given and the
transactions). An earlier `deploy.json` with `pending_report` still
parses; the key is no longer written or used. A dry run writes nothing.

`error` is a stable failure code, such as `release-exists`, `bad-entry`,
`file-changed`, `too-many-entries`, `time-budget`, `untrusted`,
`sums-mismatch`, `file-not-listed`, `manifest-mismatch`,
`signature-missing`, `signature-bad`, `list-missing`, `counter-not-above`,
`counter-missing`, `counter-invalid`, `package-step-missing`,
`package-step-failed`, `package-step-interrupted`, `package-state-unknown`,
`nft-listing-failed`, `noaicheck-failed`, `validator-failed`, `link-failed`,
`reload-failed`, `health-failed`, `record-failed`, `stop-list-missing`,
`stop-list-refused`, `stop-failed`, `provenance-missing`, `provenance-mismatch` or `interrupted`
(the full list is in `internal/authorityhost/switcher/failure.go`). `detail`
adds fixed words and identifiers: a name that holds anything but printable
ASCII, or a space, is quoted with Go's escapes and every identifier is cut
at 160 bytes, so a planted file name cannot forge a line. The diagnostics of
the command line (an unknown flag, a bad value, a path that cannot be
resolved) escape what was typed in the same way. What a program the switch
runs writes (the no-AI check, a validator, a reload or health command) is
never printed or recorded, since a diagnostic can hold a secret: only how it
ended (`exit status N`, killed, time limit) is. Run the program by hand to
see its messages. The text of a broken state file is not repeated either.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Switched and healthy (recorded, journal removed), a dry run whose checks passed, `status`, `recover`, `inhibit-clear` or `ack` done, or a retried request whose settled outcome was `ok` |
| 1 | Failed; outside a dry run the outcome is in `deploy.json`, except when the state folder, the switch policy or the host's stop list is not trusted, or `releases/`, `current` or `previous` cannot be read (`state: FAILED`), which records nothing, or when the result is `unresolved`, which leaves the journal for the next command |
| 2 | Bad arguments, `--mode` weaker than the switch policy, or a root other than `/` without `--allow-exec`; nothing was done |
| 3 | Another switch holds the lock, or `status` holds it while a switch is asked for (`flock`, not waited for), or a package step that outlived its switch holds it (said as `surviving helper pid N`); nothing was done |

### Failure cases

| Case | What happens |
| --- | --- |
| Incoming folder holds a link, special or hard-linked file, or `releases/ID` exists | Refused at import; nothing is left in `releases/` |
| Incoming folder swapped for a FIFO, a link or another folder while the switch opens it | Refused at import (`release-not-folder`, `file-changed`) at once; nothing waits, nothing is created, the lock is released |
| Incoming folder is, contains or lies beneath `releases/` or anything in it | Refused at import (`incoming-overlaps`) before anything is created or removed; the incoming folder stays as it is |
| A staging folder an earlier import left in `releases/` | Left as it is and named by every `--from`; remove it by hand |
| Wrong sum, file missing or not listed, path escape, link in the release, release folder a link | Refused at verify; `current` unchanged; an import's copy is removed |
| A list's program is an absolute path outside the release that is not a protected host program, or `{release}/PATH` that is not an executable file of the release | Refused at verify (`list-invalid`); nothing runs |
| A release folder or file writable by group or others, set-id, or not root's | Refused at verify |
| A release folder without the record of its import, or not the tree its import made | Refused at verify (`provenance-missing`, `provenance-mismatch`); as the old release, it is no fallback |
| Signature missing or bad in signed mode, on a switch or a rollback | Refused at verify |
| Switch policy or signers file not root's, writable by others, missing, or not parsing | Refused; nothing recorded; only a journal in `stopping` is resumed first, under the lock, so its stops still run |
| `releases/` missing or not trusted, `current` or `previous` not a link to `releases/<id>` | `state: FAILED`, exit 1; nothing recorded: `deploy.json`, with the package state, the counter evidence and an outcome still to report, stays as it is (`recover` too) |
| The host's `stop.list` missing, empty or not trusted | Refused; nothing recorded; a journal in `stopping` still runs the release stop lists that verify first, and stays |
| The host's `stop.list` names an absolute program that is not a protected host program | `stop-list-refused`, reported loudly; new work is refused; a journal in any phase is still resumed, with the release stop lists that verify, and one that needs the host's list stays in `stopping` |
| `--mode` weaker than the switch policy | Exit 2; nothing done |
| Counter not above the highest ever attempted | Refused at counter |
| `--rollback-to` anything but previous | Refused at counter |
| The snapshot changes and there is no package step | Refused at packages; `current` unchanged |
| Package step fails | Failed at packages; `current` unchanged; the decision services are stopped and decisions stay inhibited; the package state is unknown, and every switch is refused until `recover --package-state` or a switch to the same new snapshot runs the step to the end |
| No-AI check fails before the package step (phase pre) | Refused at noaicheck; nothing ran, nothing began |
| No-AI check (phase pre) or a validator fails after the package step | Failed at noaicheck or validate; `current` unchanged; the decision services are stopped and decisions stay inhibited |
| No-AI check fails before activation, with no package step | Refused at noaicheck; no validator runs |
| No-AI check fails after the reload and health (phase post), or `nft` fails | Back to the old release, as for a failed health check; `failed_step: postcheck` |
| A validator fails | Refused at validate; nothing reloaded |
| Crash during the package step | The next command makes sure the step has ended, stops the decision services and records `package-step-interrupted` with the package state unknown; decisions stay inhibited |
| Reload or health fails after the switch | The new release's stop list (the host's, when it no longer verifies), then back to the old release, with its own reload and health lists and its no-AI check; `rolled-back`, and decisions are no longer inhibited |
| Going back fails (putting `previous` back included), or the old release is not healthy or fails its no-AI check | The host's `stop.list` and the stop lists of both releases run; `failed-recovery` with `restored_health: failed`; decisions stay inhibited; the console is needed |
| First switch, or the old release missing or no longer verifying, and the new one fails | `current` stays; the host's and the release's stop lists run; `failed` or `failed-recovery`; decisions stay inhibited, across a reboot |
| `decisions.inhibit` there after a failure | Decision-capable services may run but refuse every decision; a later healthy switch or `inhibit-clear --asked WORDS` removes it |
| Reboot or crash after a healthy release was decided (`done`) and before it was settled | The next command runs the reload list and health check of `current` again before removing the inhibit; when they fail, the host's stop list runs, decisions stay inhibited, the outcome stays owed with `requalify_failed`, and that command and every retry of the same request exit 1 until `ack --asked WORDS` |
| A stop command fails | `result: unresolved`, exit 1; the journal stays in `stopping`; every later command runs the stops again and does nothing else until they pass, or `recover --services-stopped` |
| A link folder cannot be synced | `result: unresolved`, exit 1; the journal stays; nothing is recorded as done until the next command syncs it |
| `--rollback-to` when `previous` points at a missing release | Refused at counter (`no-previous`) |
| Crash or power loss during the switch | `current` points at a whole release, the old or the new one, and the journal is there; the next command resumes its phase (see "The journal and recover") |
| The same request run again while its outcome is owed (settled by its own command or by another one, its result line printed or not) | Reports that outcome once and does nothing else; a rollback retried as `previous` goes to no other release; `--dry` only shows it |
| The same request run again, and its report cannot be written or flushed | The outcome stays owed; exit 1; the next retry reports it |
| Another switch or rollback while an outcome is owed | Reports the outcomes owed, leaves them owed and is refused, exit 1, nothing recorded or run, until `ack --asked WORDS` or the same request again |
| `highest-counter` missing or broken while `current` exists, or below a counter the links, `deploy.json` or `counters.log` show was used | Refused before anything, nothing recorded (`counter-missing`, `counter-invalid`); run `recover --counter N` |
| A journal whose links or counter do not match the host (restored from a backup) | `reconcile: unresolved`, exit 1; nothing resumed, run or moved until `recover --discard-journal TXN` |
| A journal that cannot be checked against the host (its evidence cannot be read) | `reconcile: unresolved`, exit 1; neither resumed nor discarded until the evidence is readable; a journal in `stopping` still gets the host's stop list first |
| A journal in `stopping` while `releases/`, the counter or `deploy.json` cannot be used | The host's stop list runs first, under the lock; the journal stays in `stopping` until the rest can run |
| The journal cannot be saved as `restoring` after the new release failed its reload, health check or no-AI check | The host's stop list and the new release's (when it verifies) run first, under the lock, so that its services do not keep running; then `result: unresolved`, the journal stays in `activating`, and the next command resumes it and goes back |
| The journal cannot be saved as `stopping` | The host's stop list has run already; `result: unresolved`, and the next command runs the stops again |
| A release folder of a higher counter is in `releases/` | Refused at counter (`counter-not-above`); build a higher counter |
| A second switch while one runs | Exit 3; nothing done |

The switch never deletes a release (it removes only the staging folder
of an import that it made itself and that failed). Removing old releases is not part of this feature: do it by
hand, never the targets of `current` or `previous`, and keep in mind that a
release folder is evidence of its counter (step 2).

### Limits

What the switch does not do, so that nobody relies on it:

- A signed release is code that runs as root (its no-AI check, its
  package step, its units): the signature decides what may run, and the
  switch's checks of an honest release are not a privilege boundary.
- It does not check that a release's lists cover what the release ships
  (see the lists above), nor that its decision-capable services refuse
  decisions while the inhibit is there: that is the release builder's job.
- The decision inhibit keeps decisions from being taken by a service
  that follows the contract; it neither stops a service nor keeps one
  from starting. Stopping is the stop lists' job, and a service that does
  not check the file is not held back by it.
- The journal makes a switch safe against crashes, power loss and kills,
  and the trust checks keep other accounts out; neither protects against
  someone who is root on the host, who can rewrite any of it.
- An old journal put back can replay a transaction that was settled
  already. The condition: someone who can write the root-owned state
  folder (root) restores an earlier `txn.json` from a backup, while the
  links and the counters still allow it. For example, a switch to release
  3 that reached `activating`, failed its health check and went back to
  release 2 leaves both links and the counter floor (3) where that
  journal permits them; restoring that journal alone makes the next
  command run release 3 again, without a new request. A rollback journal
  restored after later rollbacks put the links back to the same pair is
  accepted the same way. The switch compares the journal with the links
  and the counters (see "The journal and recover"), but keeps no record of
  which transactions were settled to compare it with. This is accepted:
  restoring state files is a root action, and a backup of the state folder
  must never be restored over a host that switched since (restore the
  whole host, or none of it).
- The package step is killed when the switch dies only on Linux (the
  parent-death signal), and only the step itself: a process it starts in
  another process group, or one that closes its descriptor 3, is outside
  what the switch tracks. A step that does that must end its own children.
- A descendant of the package step that keeps its descriptor 3 (the
  switch lock) open can keep every command out indefinitely. The
  condition: the switch dies during the package step, Linux signals only
  the step itself, and a process the step started (in its process group
  or not) survives and still holds the inherited descriptor. Every later
  switch, rollback, `recover`, `ack` and `inhibit-clear` then exits 3 with
  the generic lock message once the journaled step and switch are gone
  (the descendant is not named), and the journal and the decision inhibit
  stay. Find the process that holds the lock (for example
  `fuser -v /var/lib/authority-host/switch.lock`), stop it, or reboot; the
  next command then settles the transaction. This is accepted: a package
  step must end its own children before it exits.
- The staging folder of an import that was interrupted (a crash while it
  copied) is not removed by any later command: no journal names it, and a
  folder the switch cannot prove it made is never removed. Every `--from`
  names it; remove it by hand.
- A release whose stop list cannot be verified when the services must be
  stopped does not run it: the host's own stop list is what must cover
  every decision service.
- A release folder restored from a backup has new inodes, and needs to be
  imported again with `--from`.

## The no-AI check

`ah-noaicheck` (source in `cmd/ah-noaicheck` and
`internal/authorityhost/noaicheck`) is a small static program, standard
library only, shipped in every release. The switch runs the new release's
copy (see "The switch"). This tree ships no timer and no service unit for
it: a recurring run is a separate planned item, so today the check runs
when the switch runs it or when someone runs it by hand. It reads only:
it writes nothing, opens no network connection and runs no program; the
processes and listening sockets are read from `/proc`. It is meant to run
as root, which can read every folder it looks into.

```
ah-noaicheck [--phase pre|post|full] [--root DIR] [--release DIR] [--require-policy] [--nft-listing FILE] [--json]
ah-noaicheck ports [--root DIR] [--release DIR] [--require-policy] [--json]
ah-noaicheck nft-digest FILE
```

It prints one line per hit, `HIT <kind> <path-or-name> [detail]`, a
`NOTE` line for what is not a hit (a policy that is not enforced, a
masked unit, a deferred source), `RUN complete` or
`RUN incomplete`, and a final `ok` or `FAILED n hit(s)`; `--json` writes
the same as one JSON object (with `"complete"` and `"phase"`). A field
that holds anything but printable ASCII is quoted with Go's escapes, so a
planted file name cannot forge a line. An error message writes every
path or word that came from the caller (a `--root`, `--release` or
`--nft-listing` path, a flag as typed) the same way, cut to 512 bytes. It
never prints a file's content or a variable's value. Exit status: 0
clean, 1 any hit, 2 a usage error or a read error it cannot continue past
(a missing or invalid list, a root, listing or release folder it cannot
open).

Every file it opens by name (the lists in `share/`, the `--nft-listing`
FILE and the `nft-digest` FILE) must be a regular file. Its type is
looked at without following a final link, it is opened without blocking
and without following a final link, and the opened descriptor must be a
regular file and the one looked at before a byte is read. A FIFO (one
without a writer included), a device, a socket, a folder or a link there
is never read and never waited on: the run prints
`HIT incomplete input <name>` (the name written like every field) and
exits 1 at once. `-` (stdin) stays the streaming interface of a listing.
The root and release folders are opened as folders only, so a FIFO in
their place is a read error, not a wait.

### Phases

`--phase` says which moment of a deployment the run checks:

- `pre`, before a switch: the candidate release's own files and the
  host's binaries, tools, credentials, environment files, processes,
  enabled units and forbidden packages. It does not check the listening
  ports or the firewall digest, which describe services the switch has
  not started yet, and `--nft-listing` is a usage error in it. Nor does
  it check the installed packages against the candidate's
  `packages.allow`: it runs on the old host, before the switch's package
  step moves the host to the candidate's package set, so a package the
  old host has and the candidate drops is not a hit there. A package of
  `forbidden-packages.list` is, and a package database the check cannot
  use is still incomplete; `post` and `full` check the allowlist, and
  with `--require-policy` an empty `packages.allow` is a hit in every
  phase. The release's own files are those of the folder `--release`
  names:
  - every file beneath its `etc/systemd` is read as a unit or drop-in,
    with the rules of an installed unit (below): its `Environment=`,
    `PassEnvironment=` and `EnvironmentFile=` names, a link to
    `/dev/null` is a mask and is not read, and every file an
    `EnvironmentFile=` names is read on the host (a required one that is
    missing is `HIT incomplete env-file`). A unit file and the drop-ins
    of its drop-in folder beside it (`app.service` and
    `app.service.d/*`) are merged as systemd merges them, the unit file
    and then the drop-ins by file name, before any file their
    `EnvironmentFile=` lines name is opened: an empty `EnvironmentFile=`
    drops every one before it, so a required file a later drop-in drops
    is not reported missing. The units are merged one at a time; what a
    unit file keeps for the merge is charged to the run's memory budget
    before it is kept and released once that unit is checked, so a
    release with very many unit files ends `HIT incomplete budget
    memory` rather than holding them all. A file in a folder named `user` is a user
    unit, its `EnvironmentFile=` resolved for every user; any other is a
    system unit;
  - every file beneath its `etc/caddy` is read, by its format, for the
    variables it refers to through a placeholder Caddy itself replaces,
    and only those. A file whose name ends in `.json` is Caddy's native
    JSON, read as JSON: only `{env.NAME}` in a string (a key or a value,
    as decoded) counts, a brace escaped for Caddy (`\{`) is literal, and
    every other text, a literal `$` and `{$NAME}` included, is a private
    value, never matched. Any other file is a Caddyfile: `{env.NAME}` and
    `{$NAME}` (`{$NAME:default}` too) count; a comment (a `#` that starts
    a word outside quotes) and every literal operand are passed over. In
    either, shell-style `$NAME` and `${NAME}` are literal text, since
    Caddy does not read them (a systemd unit's are read by the unit rules
    above). Each reference named like a provider key is a `HIT env`; a
    word that only looks like a variable's name is not reported, so no
    configuration text reaches the report. A `{env.` (or, in a
    Caddyfile, `{$`) the check cannot read (no valid name and closing
    brace after it; for `{$NAME:default}`, no closing brace after the
    default, which then names nothing and publishes no part of its text),
    and JSON that does not parse or nests more than 256
    levels, is `HIT incomplete release-syntax <file>`, which names the
    file and a fixed reason, nothing of its content. The files beneath
    `etc/nftables` refer to no environment variable and are not read for
    names.

  A hit in the release names the file by its path within `--root` when
  the release is beneath it (`/opt/authority-host/releases/<id>/...`),
  by its own path otherwise.
- `post`, after a switch: everything `pre` checks, and the listening
  ports and the firewall digest against `--nft-listing`.
- `full`, the default: the host as it is, every check, without a separate
  read of the release's files (its units are read where the host loads
  them).

### Policies and completeness

`--require-policy` turns each policy the release leaves unset into a hit,
`HIT policy <list> empty` or `HIT policy nft.digest missing`: an empty
`units.allow`, `packages.allow` or `ports.allow`, and a release without
`share/nft.digest`. It applies in every phase, `pre` included, though
`pre` does not check the ports or the digest: the release must carry
them. Without it (a check by hand) an empty `packages.allow` and a
missing `nft.digest` are `NOTE` lines instead, and the run is
`RUN incomplete`: it exits 0 when nothing else is found, but it did not
enforce those policies.

What is complete and what is not, plainly:

- Complete: `RUN complete` means the run covered every place its phase
  covers, listed below, to the end, with every policy of the release
  enforced, and no hit says otherwise.
- Incomplete, and a failure: anything the check had to cover but could
  not is a hit, so the run exits 1 and says `RUN incomplete`. That is
  every hit that is not a finding: `HIT incomplete ...`,
  `HIT unreadable ...`, `HIT unit-link ...` and `HIT policy ...`. A run
  never prints one of them together with `RUN complete`. Nothing is
  skipped silently.
- Incomplete, not a failure: only a check by hand without
  `--require-policy`, where an unenforced policy is a `NOTE` and the run
  is `RUN incomplete` but exits 0.

The `HIT incomplete` lines:

| Line | Meaning |
|---|---|
| `HIT incomplete <folder> depth` | a walk stopped at its depth limit (40 levels) above a folder it had to enter |
| `HIT incomplete <path> type` | a folder entry whose type the listing did not give and a stat could not find: the walk cannot tell whether to enter it |
| `HIT incomplete budget <limit>` | the run's budget is spent (4 million folder entries and queued files, 1 GiB read, 4 minutes, 256 MiB held, or shell expansions or brace groups nested more than 64 deep); memory is charged before the work is done: specifier expansions, queued files, the unit scan's keys of each manager, each source reference and loop word a profile's parse keeps (a loop's words once for every source of its variable), and, for a candidate release's units, what each unit file keeps (its `EnvironmentFile=` lines) and the merged references of one unit at a time, both released once that unit's files are queued. Each path a source, a PATH element or an `XDG_CONFIG_HOME` value stands for (in each home, and below each configured `XDG_CONFIG_HOME` folder) is charged as an entry before it is built, one at a time, never as a list of every combination. Every later read and parse stops, and so does every loop of the startup scan |
| `HIT incomplete env-syntax <file>` | a file whose syntax the check cannot parse, or an `EnvironmentFile=` it cannot resolve |
| `HIT incomplete env-source <file>` | a profile sources a file by a path the check cannot resolve |
| `HIT incomplete release-syntax <file>` | a front-end configuration file of the candidate release (`pre`, `post`) holds a placeholder the check cannot read, or is Caddy JSON that does not parse |
| `HIT incomplete env-file <path>` | a required `EnvironmentFile=` (no leading `-`) that is not there, or a required glob that matches nothing |
| `HIT incomplete path <file>` | a PATH setting whose folder the check cannot resolve |
| `HIT incomplete proc <why>` | no usable `/proc`: missing, unreadable, without processes, or listing processes of which not one name or executable could be read (`no-identity`) |
| `HIT incomplete proc-access <n>` | run as another user than root, `n` processes refused their name or executable |
| `HIT incomplete proc-identity <n>` | `n` processes whose `/proc` folder stays but which give no name: `comm` missing or empty and no executable (a process whose folder is gone has ended and is passed over) |
| `HIT incomplete packages missing` | there is no pacman database (`/var/lib/pacman/local`), so not even the forbidden packages can be checked |
| `HIT incomplete packages db` | the pacman database lists no package at all |
| `HIT incomplete <entry> no-name` | a pacman database entry whose `desc` names no package |
| `HIT incomplete input <name>` | a file the program opens by name (a list, the `--nft-listing` or `nft-digest` FILE) that is not a regular file: a FIFO, a device, a socket, a folder or a link; it is not read and the run stops there |
| `HIT incomplete nft listing-missing` | the release pins `nft.digest` and no `--nft-listing` was given (`post` and `full`) |
| `HIT incomplete /proc/net/tcp header` | a socket table that is empty or does not start with its own protocol's header line: `sl local_address rem_address st` for `/proc/net/tcp`, `sl local_address remote_address st` for `/proc/net/tcp6` (checked too when it is there) |

`--root` (default `/`) checks the tree beneath another folder, for tests;
links inside it are resolved within it. `--release` names the release
folder whose `share/` holds the lists (and, in `pre` and `post`, whose
`etc/` is read); by default it is the folder above the program's own
`bin/`, so the new release's copy checks against the new release's lists.

### What it detects

- `binary`: a file, link or folder named exactly like an executable name
  of `ai-names.list`:
  - in a folder of the PATH inventory: `/usr/bin`, `/usr/sbin`, `/bin`,
    `/sbin`, `/usr/local/bin`, `/usr/local/sbin` (a fixed list, not the
    PATH of whoever runs it), and every folder a PATH setting adds in a
    startup file, read by the same scan as the environment (see `env`):
    the system's and every home's profiles and environment files
    (`.profile`, `.bash_profile`, `.bash_login`, `.bashrc`, `.zshenv`,
    `.zprofile`, `.zshrc`, `.zlogin` ...) and every file they source,
    transitively, so a folder a sourced file adds, wherever it is, is in
    it (`PATH=`, `PATH+=`, `export PATH=`, csh's `setenv PATH`,
    and the argument of a function that adds its argument to PATH, such
    as `/etc/profile`'s `append_path`; `~/`, `$HOME` and
    `$XDG_CONFIG_HOME` are resolved in the file's home, or in every home
    for a system file; an element that only refers to PATH itself adds
    nothing; anything else is `HIT incomplete path <file>`);
  - in every immediate subfolder of `/usr/bin`, `/usr/local/bin` and
    `/usr/local/sbin` (such as `/usr/bin/core_perl`);
  - anywhere in `/usr/local` and `/opt`, at every depth;
  - in any `node_modules/.bin` there, in `/usr/lib/node_modules` or in a
    home;
  - in every home (the homes of `/etc/passwd`, `/root` and every folder in
    `/home`; a home in `/proc`, `/sys` or `/dev` is not walked): in any
    folder named `bin` at most three levels below it (`bin`, `.local/bin`,
    `.cargo/bin`, `go/bin`, `src/tool/bin` ...), also when `bin`,
    `.local/bin`, `.cargo/bin` or `go/bin` is a link to elsewhere, anywhere
    in the mise, npm, pnpm, volta, nvm, uv and pipx install folders, and,
    as a regular executable file, anywhere in it.

  Every home, `/usr/local` and `/opt` are walked completely, down to 40
  levels; a deeper folder is not entered and the run is
  `HIT incomplete <folder> depth`.
- `tool`: `mise`, `npm`, `npx`, `node`, `nodejs`, `uv`, `uvx`, `pipx`,
  `pnpm`, `yarn`, `bun` or `deno` in the same places, except as a plain
  executable file elsewhere in a home.
- `credential`: a credential or config path of `ai-names.list` (the
  entries starting with `.`, such as `.claude` or `.config/opencode`) in
  any home, in `/root` or in `/`.
- `env`: a variable named like a provider key (`*_API_KEY`,
  `*_AUTH_TOKEN`, `ANTHROPIC_*`, `OPENAI_*`, `GEMINI_*`, `CLAUDE_*`,
  `CODEX_*`) set or passed in `/etc/environment`, `/etc/profile` and the
  shells' system profiles, `/etc/profile.d`, the `environment.d` folders,
  the systemd manager configuration and its drop-ins, a unit file or
  drop-in a systemd manager loads, a home's profiles, a file a profile
  sources, or a file an `EnvironmentFile=` line names. The detail is the
  variable's name, and only ever a valid identifier
  (`[A-Za-z_][A-Za-z0-9_]*`, at most 128 bytes): no part of a value is
  printed.

  The units are read through each manager's effective unit view: the
  system manager's, and one user manager's for every home with its user
  id (from `/etc/passwd`, 0 for `/root`, the folder's owner for a folder
  of `/home` that `/etc/passwd` does not name). A view holds the
  manager's unit folders in systemd's order of precedence: for the
  system manager `/etc/systemd/system.control`,
  `/run/systemd/system.control`, `/run/systemd/transient`, the early
  generator folder, `/etc/systemd/system`, `/etc/systemd/system.attached`,
  `/run/systemd/system`, `/run/systemd/system.attached`, the generator
  folder, `/usr/local/lib/systemd/system`, `/usr/lib/systemd/system` and
  the late generator folder; for a user manager the same order of the
  user folders (the home's `.config/systemd/user.control`,
  `.config/systemd/user` and `.config/systemd/user.attached`,
  `/run/user/UID/systemd/...` with its `user` and `user.attached`,
  `/etc/xdg/systemd/user`, `/etc/systemd/user`, `/run/systemd/user`, the
  home's `.local/share/systemd/user`, `/usr/local/share/systemd/user`,
  `/usr/share/systemd/user`, `/usr/local/lib/systemd/user` and
  `/usr/lib/systemd/user`). The unit read for a name is the first of it
  in that order, not every one (an instance without its own falls back to
  its template); a mask (a link to `/dev/null`, or an empty unit file, as
  `systemd.unit(5)` defines it) hides the unit and its drop-ins in every
  lower folder, while a mask in a lower folder hides nothing. A drop-in
  file name in a higher folder hides the same name in a lower one. An
  entry of a dependency folder (`*.wants`, `*.requires`, `*.upholds`)
  stands for the unit systemd loads by its name: the unit of that name in
  the view (an `/etc` override, say), not the file the link leads to;
  only a name the view does not hold is read where its link leads. A
  unit, dependency folder or drop-in folder that is a link (into
  `current/etc/systemd`, say) is read where it leads within the root.
  Each file is read once; a unit keeps the manager that loads it wherever
  its file is, so its `EnvironmentFile=` specifiers are that manager's:
  `%h` the user's home (`/root` for the system manager), `%t`
  `/run/user/UID` (`/run`), `%%` a percent sign, each replaced once. The
  `EnvironmentFile=` lines of a unit and its drop-ins (in file-name
  order) are evaluated in systemd's order before any file they name is
  opened: an empty `EnvironmentFile=` drops every one before it.

  Each file is read in its own syntax. A shell profile is read as shell
  words: an assignment at the start of a command, the names `export`,
  `declare`, `typeset`, `readonly` and `env` set, `setenv` (csh) and, in
  a `.fish` file, `set`; comments, quoted values, substitutions and
  here-documents are never read for names, and a `[[ ... ]]` conditional
  is one expression up to its `]]` (a parenthesis of a regular
  expression in it starts no command). A function body
  (`NAME() { ... }`, `function NAME { ... }`) is followed by its braces:
  a plain `local`, `declare` or `typeset` in it is the function's own
  variable and is passed over, but one with a global or export flag
  (`declare -g`, `declare -x`, `declare -gx`, `typeset -x`, `local -x`)
  sets the login's variable or one every program the function starts
  sees, so each name it assigns counts, and so does each name a
  `readonly` in a function assigns (it is global). An `export` keeps
  counting everywhere, and one whose name is an expansion cannot be
  parsed. A `local` outside a function and a `declare`, `typeset` or
  `readonly` whose name is an expansion are passed over.

  The startup files are read as one scan: the system's
  (`/etc/environment`, `/etc/profile`, `/etc/bash.bashrc`, the zsh system
  files, `/etc/profile.d`, the `environment.d` folders), each home's
  (`.profile`, `.bash_profile`, `.bash_login`, `.bashrc`, `.zshenv`,
  `.zprofile`, `.zshrc`, `.zlogin`, `.pam_environment`, its fish and
  `environment.d` files) and every file one of them sources, transitively.
  A file a startup file sources (`.` or `source`) is read too when its
  path is a literal absolute path, starts with `~/`, `$HOME`, `${HOME}` or
  `$XDG_CONFIG_HOME` (in its usual forms), resolved in the home of the
  sourcing file or in every home for a system file, or is the variable of
  an earlier `for NAME in WORDS` loop (`/etc/profile`'s loop over
  `/etc/profile.d/*.sh`); a glob in its last component is matched.
  `$XDG_CONFIG_HOME` stands for the home's `.config` and for every folder
  a startup file gives `XDG_CONFIG_HOME` (`export XDG_CONFIG_HOME=...`,
  `XDG_CONFIG_HOME=...` in an environment file), resolved like a source's
  path; the home's fish and `environment.d` files are read below that
  folder too. A value the check cannot resolve, or one a sourced file
  gives (too late: the sources are resolved first), is
  `HIT incomplete env-source <file>` for the file that gives it. Any other
  source at the top level or in a top-level loop (a relative path,
  another variable, a command substitution, an array) is
  `HIT incomplete env-source <file>`. A source in a function body runs
  only when the function is called, not at login: one the check can
  resolve is read, and one it cannot is the line
  `NOTE deferred-source <file>`, which is not a hit and leaves the run
  complete.

  A unit or manager file is read by its `Environment=`,
  `DefaultEnvironment=`, `ManagerEnvironment=` and `PassEnvironment=`
  keys (systemd's quoting) and `EnvironmentFile=` (quoted or not, a
  leading `-` for an optional file, the specifiers above, a glob in the
  last component matched within the root). A required file that is not
  there, or a required glob that matches nothing, is
  `HIT incomplete env-file <path>`: systemd would not start the unit,
  and the check cannot know what the file would set once it is there.
  `/etc/environment`, `environment.d` and the files `EnvironmentFile=`
  names are `KEY=VALUE` lines; `~/.pam_environment` is read by its first
  word. What the check cannot parse (an unclosed quote or substitution, a
  here-document that never ends, an `export` whose name is an expansion,
  another specifier, a line that is not `KEY=VALUE`) is
  `HIT incomplete env-syntax <file>`.
- `process`: a running process whose name or executable is named like an
  executable of `ai-names.list`. A process that ends while it is read (its
  `/proc` folder is gone) and a kernel thread are passed over. Only a
  name that is not empty counts. A missing or empty `/proc`, or one in
  which not one process's name or executable could be read, is
  `HIT incomplete proc <why>`; a process whose folder stays but which
  gives no name (its `comm` missing or empty, and no executable) is
  counted into one `HIT incomplete proc-identity <n>`, so one readable
  process cannot hide others the check cannot see. A process whose name or executable is
  refused for lack of permission is a `HIT unreadable` of its own when the
  check runs as root, and otherwise counted into one
  `HIT incomplete proc-access <n>`, `n` the number of such processes: run
  by hand as another user, the check cannot see other users' processes
  and says so.
- `unit`: an enabled unit not in `units.allow`: an entry of a `*.wants`,
  `*.requires` or `*.upholds` folder (one that is a link within the root
  included) of a folder of enablement: `/etc/systemd/system`,
  `/etc/systemd/system.attached`, `/run/systemd/system` and
  `/run/systemd/system.attached` for the system manager; a home's
  `.config/systemd/user` and `.config/systemd/user.attached`,
  `/etc/xdg/systemd/user`, `/etc/systemd/user`,
  `/run/user/UID/systemd/user`, `/run/user/UID/systemd/user.attached` and
  `/run/systemd/user` for a user's. An instance is allowed by its own name
  or by its template's. An enabled unit that is masked (its entry links to
  `/dev/null` or to an empty file, or the unit the manager's view loads
  for its name, or its template's, is a mask, in a folder of higher
  precedence, `/etc` over `/run` over `/usr/lib`) is not enabled: it is
  the line `NOTE unit masked <path>`, which is not a hit.
- `unit-link`: a unit link, or a linked dependency or drop-in folder,
  that loops (`loop`) or whose target climbs above the root (`escape`).
- `package`: an installed package (the pacman database) named in
  `forbidden-packages.list`, or, once `packages.allow` is filled, any
  package not in it (in `post` and `full`; `pre` checks only the
  forbidden list, see "Phases"). While it is empty the check prints
  `NOTE packages not enforced` instead. The database is the evidence for
  both, and forbidden packages are checked either way, so a database the
  check cannot use is incomplete, never a pass and never a finding: no
  database is `HIT incomplete packages missing`, one that lists no
  package `HIT incomplete packages db`, and an entry without a name
  `HIT incomplete <entry> no-name`.
- `nft` (`post` and `full`): when the release has `share/nft.digest`, the
  SHA-256 of the normalised listing passed with `--nft-listing` (the
  caller runs `nft list ruleset`; the check never does) differs. No
  listing at all is not a finding but missing evidence,
  `HIT incomplete nft listing-missing`. `ah-noaicheck nft-digest FILE` prints the digest to
  pin. Normalising drops blank lines and trailing spaces and, by their
  place among the line's unquoted tokens only, the values that change
  while the rules stay the same: `packets N bytes N` of a counter,
  `expires T` of a set element, `used N bytes` of a quota and a final
  `# handle N`. A quoted operand (an interface name, a comment) is never
  changed, so rules that differ only there have different digests. The
  listing is read token by token, never a whole line of tokens at once;
  past a million tokens the rest of a line is kept as it is.
- `port` (`post` and `full`): a listening TCP socket, in `/proc/net/tcp`
  or `/proc/net/tcp6`, on a port not in `ports.allow`. Each table must
  start with its own protocol's header line, as the kernel writes it (the
  IPv4 table's third column is `rem_address`, the IPv6 table's
  `remote_address`); a table with a header and no listening socket is
  fine, and a row whose address is not of the table's family is a
  `HIT unreadable` malformed line. The `ports` subcommand checks only
  this.
- `policy` (with `--require-policy`): a policy list of the release is
  empty, or `nft.digest` is missing.
- `unreadable`: a place the check must look into but cannot read, a link
  loop, a file too large to read, or a file replaced between its check and
  its opening. The check fails closed.
- `incomplete`: a check could not cover all it must, so the run proves
  nothing there (see the table above). Never a pass.

The lists in `share/`, one entry per line, `#` comments and blank lines
ignored; a line with a path separator (except between the components of a
home path in `ai-names.list`), a control byte or an inner space is refused:

- `ai-names.list`: executable names and home credential paths of known
  agent tools.
- `forbidden-packages.list`: packages the host never needs.
- `packages.allow`, `units.allow`, `ports.allow`: the allowlists, empty in
  the repository; the lead fills them from the host's baseline at deploy
  time. An empty `units.allow` or `ports.allow` allows nothing; with
  `--require-policy` any of the three empty is a hit.
- `nft.digest`: the pinned firewall digest; optional for a check by hand,
  required with `--require-policy`.

### Limits

- It is detection, not prevention. The prevention is that nobody logs in,
  the egress filter and the package allowlist.
- A renamed binary is not caught by name; the package allowlist and the
  egress filter cover that. Names are matched exactly, and the lists need
  upkeep as tools appear.
- A tool outside the places above (say in `/srv`, outside every PATH
  folder and every home) is not caught by name; it is left to the package
  allowlist and the egress filter.
- The PATH inventory is read from the startup files and the files they
  source, by their PATH settings: a folder a program puts on PATH at run
  time is not in it.
- A process that renames itself is not caught by name.
- Ports are those of the network namespace the check runs in; UDP is not
  checked.
- Enabled units are those linked in the folders of enablement above; the
  dependencies vendor units in `/usr/lib/systemd` declare, and those a
  generator writes, are left to the package allowlist (their unit files
  are still read for variables).
- A specifier in `EnvironmentFile=` other than `%h`, `%t` and `%%` is
  `HIT incomplete env-syntax`, and so is one in a user unit for a home
  without a known user id.
- A profile that sources files by a run-time variable at its top level
  is `HIT incomplete env-source` on every run. The packaged
  bash-completion library is one: its loader's source in a function is
  only `NOTE deferred-source`, but its array-driven startup loop stays
  incomplete, so the host does not install it. This is accepted: the
  library is outside the host's package baseline, and a host that has
  it fails the check, never passes it.
- The enabled-unit check (`unit`) takes an entry of a dependency folder
  as masked when the file its link leads to is a mask (a link to
  `/dev/null` or an empty file), even when a folder of higher
  precedence holds a unit of the same name that is not one; systemd
  would load that unit, so such an entry is a `NOTE unit masked` line
  where it could be a `HIT unit`. This is accepted: the host's
  enablement links lead to the unit systemd loads for their name, and
  the environment check reads the unit of the view, not the link's
  target.
- Run by hand as an ordinary user it cannot read other users' processes
  or folders; it says so (`HIT incomplete proc-access`, `HIT unreadable`)
  rather than pass.

## The release builder

`ah-release` (command `cmd/ah-release`, package
`internal/authorityhost/release`) makes the release folder `ah-switch`
imports (see "Layout" and "The switch" above) from the binaries and this
folder's `etc/` and `share/`. It needs only the Go standard library and
opens no network connection. It is run on the build host, as the building
user; it needs no root.

```
ah-release build --out DIR --counter N --commit SHA --go-version V --built-on CLASS --package-snapshot S --bin FILE... --src deploy/authority-host [--components static]
```

- `--out DIR` is the folder the release folder `DIR/<id>` is made in (it is
  created when it is missing). The id is the counter zero-padded to four
  digits, a hyphen and the first 7 digits of the commit, in lowercase.
  A release that exists already (any entry of that name) is refused and
  left as it is, and so is a counter below 1.
- `--commit` is the full commit, 7 to 64 lowercase hex digits (as the
  switch's `MANIFEST` check reads it). `--go-version` and `--built-on`
  (the class of the build host, never its name) are 1 to 128 bytes of
  letters, digits and `._:+-`, stricter than the switch, which takes any
  text, so that a value can never hold a line break, a comment mark or
  padding. `--package-snapshot` is as the switch reads it: 1 to 128 bytes
  of letters, digits and `._:-`. `--components static` writes the
  `components: static` line.
- `--bin FILE` (given once per file) is copied to `bin/<name of the file>`,
  mode 0755; the name is letters, digits and `._@+-`. `ah-noaicheck` and
  `ah-switch` are required; add `ah-pkgstep` when the release's package
  snapshot may differ from the host's (a first switch always moves the host
  to the snapshot, so it needs it).
- `--src` is the deployment source folder: its `etc/` is copied to
  `etc/` and its `share/` to `share/`, which must hold the four command
  lists and the lists of the no-AI check. Nothing else of it is copied (not
  `bootstrap/`, a deploy script or this file). In those two folders every
  entry must be a folder or a regular file with a plain name (letters,
  digits and `._@+-`): a symbolic link, device, fifo or socket, or a name
  with a space, newline or backslash, refuses the build before anything is
  written.
- The folder holds `bin/`, `etc/`, `share/`, `MANIFEST` (the keys `id`,
  `counter` (no padding), `commit`, `built_on`, `go_version`,
  `package_snapshot` and, with `--components static`, `components: static`,
  in that order) and `SHA256SUMS`: lowercase hex, two spaces and the path,
  sorted by path as `LC_ALL=C sort` sorts (by byte), listing every file but
  `SHA256SUMS` itself and `SHA256SUMS.sig`, as "Layout" describes it.
  Folders are 0755 and files 0644, 0755 for `bin/*`, whatever the source's
  modes (no set-user-id, set-group-id or sticky bit carries over); there is
  no link, and no time, user or host name in any content, so two builds of
  the same input are byte-identical.
- It is made beside its place, as `DIR/.<id>.partial`, and renamed to
  `DIR/<id>` when whole; a failed build removes the partial folder, and a
  leftover one (a build killed half way) is refused by name until it is
  removed by hand. At most 20000 entries, 512 MiB a file and 2 GiB in all,
  the switch's own limits.

Exit status: 0 ok, 1 refused, 2 usage (an argument that is wrong, with a
message that does not repeat it). A refusal prints
`FAIL: <code> <fixed reason>`, with, after a colon, the name of the file it
is about when that name is plain; it prints nothing from any file. The
signature (`SHA256SUMS.sig`) is made afterwards, by the owner, with
`ssh-keygen -Y sign` as "Layout" says; the builder never signs.

### Checking a built release

```
ah-release check [--no-noaicheck] [--root DIR] [--require-policy] DIR/<id>
```

`check` reads a built folder back, and is the "coverage" the switch's lists
leave to the builder (see the lists under "Layout"): the switch checks that
the lists parse and that their programs are where they must be, never that
they cover what the release ships. Exit status: 0 ok (`ok: release <id>`),
1 a check failed, 2 usage. Each failure is one line,
`FAIL: <code> <fixed reason>`, with, after a colon, the one name it is
about when that name is plain; at most 50 lines are printed (the last says
how many more there are), and no line holds the content of a file. It
reads, never writes, and needs no root.

It checks, in this order, all of them (a pass is no finding):

1. **The folder.** Its name is `<counter>-<short commit>` and it is a real
   folder; it holds only folders and regular files with one link each and
   plain names, nested at most 16 deep and at most 20000 in all; nothing at
   its top but `MANIFEST`, `SHA256SUMS`, `SHA256SUMS.sig`, `bin`, `etc` and
   `share`; nothing writable by group or others and no set-user-id,
   set-group-id or sticky bit. Every file of `bin/` has an execute bit, and
   `bin/ah-noaicheck` and `bin/ah-switch` are there.
2. **`SHA256SUMS`.** The switch's own parser reads it; every file but
   itself and `SHA256SUMS.sig` is listed and matches, none listed is
   missing, and the text is exactly what the builder writes (one line per
   file, sorted by path, two spaces, no `*`).
3. **`MANIFEST`.** The switch's own parser reads it, it describes the
   folder (id, counter, commit) and its `built_on` and `go_version` are
   what the builder writes.
4. **Component coverage** (the lists, the switch's parser reading them with
   `{release}` as the folder's path on the host):
   - `health.cmd` holds a command. `validators.list`, `reload.list` and
     `stop.list` do too, unless `MANIFEST` says `components: static`: then
     `stop.list` may be comments only, and the other two hold what the
     rules below ask for of what is shipped (nothing is asked of a
     release that ships no file in the family).
   - `validators.list` has, for every file shipped under `etc/caddy`,
     `etc/nftables` and `etc/systemd`, one validator that names it by its
     path, exactly once: `caddy validate --config {release}/PATH`,
     `nft -c -f {release}/PATH`, `systemd-analyze verify {release}/PATH`
     (options such as `--adapter caddyfile` may follow; a command that is
     none of these is allowed, and covers nothing). A file under a `*.d`
     folder of `etc/systemd` (a drop-in) is covered by the validator of its
     unit and needs none; a validator that names a path the release does
     not ship in that family is a finding too.
   - `reload.list` restarts or reloads what the release ships: for caddy
     (`etc/caddy`), `systemctl restart caddy` or `reload caddy`; for
     `etc/systemd`, `systemctl daemon-reload` and a `systemctl restart` of
     every service unit; for `etc/nftables`, `nft -f
     /opt/authority-host/current/etc/nftables/PATH` of a shipped rule
     file or `systemctl restart nftables`.
   - Every `{release}/PROGRAM` a list names is a file of the release with
     an execute bit.
   - A static release has no unit that starts a program: no file under
     `etc/systemd`, a drop-in included, with a `[Service]` section and an
     `ExecStart=` line. Any other release has at least one (a *decision
     service*), and for each: every `ExecStart=` runs a binary of the
     release, `/opt/authority-host/current/bin/NAME` (or the release's own
     `/opt/authority-host/releases/<id>/bin/NAME`) with `bin/NAME` shipped
     and executable; `stop.list` has a `systemctl stop` of it; `reload.list`
     has a `systemctl restart` of it (a template unit `x@.service` is
     matched by any instance, `x@1`). No unit gates its start on
     `decisions.inhibit` (`ConditionPathExists=` and the like naming it): the
     contract under "The decision inhibit" is that a service starts while
     it is there and refuses decisions itself, and the builder cannot see
     the application, so that is for its own tests; what it does check is
     that no unit file breaks it.
5. **The no-AI check.** The release's own
   `bin/ah-noaicheck --phase pre --release DIR/<id>` runs as a child of its
   own process group (killed whole at the time limit) with the switch's
   environment (`PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C`), the
   folder `/` and no standard input, and must exit 0 within 60 seconds
   and print no line starting `HIT `; nothing it prints is shown, only
   `noaicheck-exit`, `noaicheck-hit`, `noaicheck-timeout` or
   `noaicheck-not-started`. `--root DIR` is given on to it (default: the
   host it runs on, so on a build host that has agent tools of its own it
   reports them: run `check` where the no-AI check is meant to pass, or
   give a `--root`); `--require-policy` is given on to it too, which
   turns an empty allowlist or a missing `share/nft.digest` into a hit:
   `share/*.allow` ship empty from this tree and are filled from the
   host's baseline at deploy time, so it is for a release that has been
   filled. `--no-noaicheck` skips this step (for tests).

### The source tree of the first release

The first release is static (`--components static`): it runs no decision
service, only the static status page, so its source tree is the front end
and its lists.

- `etc/caddy/Caddyfile`: Caddy with `admin off` and `auto_https off`
  (no certificate handling, and no admin endpoint to reach), server timeouts of 10 s for the header and
  for the body, 30 s for a write and 60 s idle; one site, `:80`, that sends
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`, a `Content-Security-Policy` that allows
  nothing but styles from the site itself (`default-src 'none'; style-src
  'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`) and
  `Cache-Control: no-store`, drops the `Server` header, answers `/healthz`
  with `ok` and serves everything else from `/srv/authority/public`.
- `share/validators.list`: `caddy validate --config
  {release}/etc/caddy/Caddyfile --adapter caddyfile`.
- `share/reload.list`: `systemctl restart caddy`. With the admin endpoint
  off Caddy cannot be reloaded (`systemctl reload caddy` asks that endpoint),
  so it is restarted; the comment in the file says so.
- `share/health.cmd`: `curl -fsS --max-time 5 http://127.0.0.1/healthz`.
- `share/stop.list`: comments only, which is what `components: static`
  allows.
- `share/ai-names.list`, `forbidden-packages.list`, `packages.allow`,
  `ports.allow` and `units.allow` are the no-AI check's lists, as before;
  the allowlists are filled from the host's baseline at deploy time.

`ah-release build --src deploy/authority-host --components static ...`
followed by `ah-release check` passes on this tree; a test builds it with
stand-in binaries and checks it, and the switch's own dry run accepts the
result. The validators (`caddy validate`) are not run by the tests: they
need Caddy, which is not a developer prerequisite; the switch runs them
on the host.
