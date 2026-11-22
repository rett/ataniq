# Authority host deployment

The authority host is a separate host that holds the owner's deciding seat and must never
carry an AI agent tool or credential. This folder describes how a release of it is laid
out and activated and holds what a release ships besides its binaries: `share/` becomes
the release's `share/` (`/opt/authority-host/releases/<id>/share/`). No owner-local value
(a host name, an address, a user name, a key, a secret) belongs here or in a release: those
live only in `/etc/authority-host/` on the host.

## The switch: ah-switch

`ah-switch` (command `cmd/ah-switch`, package `internal/authorityhost/switcher`)
is the one way a release becomes live, used by hand on the manual path and by
the updater later. It needs only the Go standard library and opens no network
connection.

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
/var/lib/authority-host/
  deploy.json             the outcome of the last switch
  highest-counter         the highest counter a switch ever attempted
  txn.json                the journal of a switch under way
  switch.lock             the lock of a running switch
```

- The switch trusts `/var/lib/authority-host`, `/opt/authority-host` and
  `/opt/authority-host/releases` only when each of them, and every folder
  above it up to the root, is a real folder (not a symbolic link), owned by
  root and not writable by its group or by others; otherwise it stops
  before it changes or records anything. (When `ah-switch` does not run as
  root, as in tests, the folders of the user running it are accepted too.)
  It creates the state folder, mode 0755, when it is missing, holds each
  folder open while it works, and opens the lock, `highest-counter` and
  `deploy.json` relative to that folder without following a link. It reads
  only regular files of a bounded size there.
- The id is the counter, zero-padded to at least four digits, a hyphen and the
  short commit in lowercase hex (7 to 40 characters).
- `MANIFEST` holds the keys `id`, `counter`, `commit`, `built_on` (the class of
  the build host, never its name), `go_version` and `package_snapshot`, one
  `key: value` per line, and optionally `components: static` (no other
  value is accepted). `id` must be the folder's name, `counter` its counter
  and `commit` must start with its short commit. Blank lines and `#` lines are
  skipped; other keys are ignored.
- `SHA256SUMS` is in the format of `sha256sum`: lowercase hex, two spaces (or a
  space and `*`) and the path relative to the release folder, without `./`,
  `.` or `..`. It lists every file but itself and `SHA256SUMS.sig`. Build it
  from inside the folder, for example
  `find . -type f ! -name 'SHA256SUMS*' -printf '%P\0' | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS`.
- A release holds only folders and regular files with one link each. A
  symbolic link, device, fifo, socket or hard-linked file anywhere in it is
  refused, so hashing never follows one out of the folder.
- A release in `releases/` is made by the switch's import (see below), never
  by copying into `releases/` directly: without `--from` the switch accepts
  a release folder only when nothing in it is writable by its group or by
  others, nothing has a set-user-id, set-group-id or sticky bit, and
  everything is owned by root (or by the user running the switch when that
  is not root), as the import leaves it.
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
    `current` points at and may not use `{release}`.
  - `health.cmd`: one or more health commands, one per line, each of which
    exits 0 when its part is healthy: usually the front end (`/healthz` on
    Caddy) and the application's readiness (`/readyz`). Every line must
    pass. It may not use `{release}` either.
  - `stop.list`: the commands that stop the decision services, such as
    `systemctl stop ataniq-seat`, run when no healthy release is left (see
    the failure cases); the static status page stays. No `{release}`.

  In each list, blank lines and `#` lines are skipped. A command is its words
  separated by spaces; no shell runs it, so quotes, pipes, redirections, globs
  and variables are refused. The program is an absolute path, `{release}/...`
  in a validator, or a bare name looked up in `/usr/sbin:/usr/bin:/sbin:/bin`
  (never in the caller's `PATH`). Every program the switch runs gets the
  environment `PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C` and nothing else,
  the working folder `/`, no standard input, and its own process group, killed
  whole at its time limit.

  A signed release contains code that runs as root (its no-AI check, its
  package step, its units), so these lists are consistency checks of an
  honest release, not a privilege boundary: the signature is what decides
  what may run.
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

`--from` copies the incoming folder into a fresh folder of `releases/`
(`releases/.import-<id>` while it is being made) as new files: folders and
regular files only (a symbolic link, device, fifo, socket or hard-linked
file is refused), each file opened without following a link and without
blocking, checked to be the regular file with one link that was listed, and
read to its size (one that changes while it is copied is refused). Modes are
set, not copied: folders 0755 and files 0644, or 0755 when the incoming file
had any execute bit; no set-user-id, set-group-id or sticky bit, extended
attribute or ACL is carried over, and when the switch runs as root the copy
is owned by root:root. Only that copy is then verified, signature-checked,
run and linked; the incoming folder can change afterwards without touching
it. Once the copy is verified it is made read-only (every write bit
removed) and renamed to `releases/<id>`. A copy that fails to import or to
verify is removed; `releases/<id>` must not exist already, or `--from` is
refused. A release that was imported stays when a later step fails: switch
to it again without `--from`.

The work of reading a release is bounded, for the import and for the
verification of any release: at most 20000 entries (folders and files),
2 GiB in all and 512 MiB per file, and 10 minutes for the import and the
hashing together. Going over a limit fails with its own code
(`too-many-entries`, `too-large`, `file-too-large`, `time-budget`), removes
the import's copy and releases the lock. Folders are read in batches with
one folder open at a time, every file is opened without blocking, and a
file that changes size or time, or stops being a regular file, while it is
read is a failure (`file-changed`).

### The switch policy

`/etc/authority-host/switch-policy.json` (below `--root`) says how strict
every switch must be: a forward switch, `--rollback-to previous`, and the
automatic return to the old release after a failed health check.

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
- No other key is accepted. The policy file, the signers file and every
  folder above them must be owned by root and not writable by group or
  others (the same check as the state folder); otherwise, or when the
  policy does not parse, the switch stops before it does anything.
- Without the file the policy is manual: unsigned releases are accepted.
  `deploy.json` then records `trust: manual`, and `status` says
  `trust: manual: no switch policy, unsigned releases are accepted`.

### Commands

```
ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] [--from INCOMING_DIR] ID
ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] --rollback-to previous
ah-switch [--root DIR] status
ah-switch [--root DIR [--allow-exec]] [--dry] recover --counter N
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
  release's `bin/ah-noaicheck`.
- `--rollback-to previous` goes back to the release `previous` points at. It
  accepts that release's id too, and refuses any other.
- `--dry` prints every step and what it would do, runs no program and changes
  nothing: no link, no state file, no lock file. It still reads and checks the
  release (sums, manifest, lists, counter), and it is refused while a switch
  holds the lock.
- `status` prints `current`, `previous`, the highest counter, the trust of
  the switch policy and the last outcome from `deploy.json`.

Flags may come before or after the id.

### Steps

`ah-switch ID` prints each step and stops at the first failure. Up to the
rename in step 6, a failure leaves `current` as it was.

0. **import** (only with `--from`): the incoming folder is copied into
   `releases/.import-ID`, as described above.
1. **verify**: `releases/ID` (or the import's copy) must be a folder, not a
   symbolic link, and pass the check of an imported release above. Every
   file in it must be listed in `SHA256SUMS` and match it; a file not
   listed, a listed file missing or changed, a listed path that escapes the
   folder or is not clean, or a link or special file in the folder is a
   failure. In signed mode the signature must verify:
   `ssh-keygen -Y verify -f ALLOWED_SIGNERS -I PRINCIPAL -n authority-host-release`
   checks the sums, with the signers file and the principal of the switch
   policy (the principal is never discovered from the signature, so another
   identity of a shared signers file cannot authorize a release). Then
   `MANIFEST` must match the id and the command lists must parse.
2. **counter**: the counter must be above the highest ever activated
   (`highest-counter`; none means 0 only on a host that has no `current`
   yet: a missing or broken `highest-counter` while `current` exists is
   refused with `counter-missing` or `counter-invalid`, see `recover`). A
   rollback skips this rule but accepts
   only the release `previous` points at.
3. **packages**: when the manifest's `package_snapshot` differs from the one
   recorded in `deploy.json`, the release's
   `bin/ah-pkgstep --release DIR --from OLD --to NEW` runs, within 30 minutes
   (taking a filesystem snapshot first is that program's job). It runs
   before the no-AI check and the validators, so that they see the host as
   it will be. A changed snapshot and no `bin/ah-pkgstep` is a failure
   (`package-step-missing`). The step's progress is journaled in
   `txn.json` (`running`, then `done`) before and after it runs, and on
   success the new snapshot is what `deploy.json` records from then on,
   even when a later step fails. A step that was interrupted is not taken
   as done: the next switch runs it again with the same `--from`, so
   `bin/ah-pkgstep` must be idempotent: run twice with the same `--from` and
   `--to` it must end in the same state.
4. **noaicheck**: the switch runs `nft list ruleset` (within 30 seconds; its
   output at most 4 MiB) and writes what it printed to a file of mode 0600
   in a new temporary folder of mode 0700, then runs the new release's
   `bin/ah-noaicheck --root ROOT --release DIR --require-policy --nft-listing FILE`,
   which must exit 0 within 5 minutes; the folder is removed afterwards.
   Any other exit status is a failure, including the check's own report
   that it could not finish (`HIT incomplete`): it is never a warning. A
   failing `nft` is `nft-listing-failed`, and the check does not run.
5. **validate**: each command of `share/validators.list` must exit 0, within
   30 seconds each.
6. **switch**: first the journal `txn.json` is written (the release, its
   counter, the old targets of `current` and `previous`) and synced with its
   folder, and, on a forward switch, `highest-counter` is set to the new
   counter: it holds the highest counter ever attempted, so a release whose
   switch got this far cannot be switched to again, even after it failed or
   was put back; build the next counter. Then `previous` is pointed at the
   old target of `current`, and a new link `current.new` is made and renamed
   over `current`, so there is never a moment without `current`. Then each
   command of `share/reload.list` runs, within 30 seconds each.
7. **health**: the commands of `share/health.cmd` run in order, each within
   10 seconds, until one round in which every one of them exits 0, every 2
   seconds for up to 60 seconds. A static release with no health command is
   healthy at once.
8. **record**: `deploy.json` is written, by writing a temporary file and
   renaming it over the old one, and then the journal is removed.

Before the switch, in step 1, the release `current` points at (the
fallback) is opened and verified as the switch policy says, and its own
reload and health lists are read. A fallback that is missing or no longer
verifies is not used, and the switch says so (`fallback: none: ...`).

If the reload or the health check fails, the switch goes back: `current` is
renamed back to the fallback, `previous` is put back as it was, and the
fallback's own `reload.list` and `health.cmd` run (never the failed
release's). The result is `rolled-back` with `restored_health: ok` only when
all of that succeeded. When putting the links back, the fallback's reload
or its health fails, the failed release's `stop.list` runs to stop the
decision services (the static status page stays), the result is
`failed-recovery` with `restored_health: failed`, and the console is needed.
With no fallback (the very first switch, or one that cannot be used),
`current` stays at the new release, its `stop.list` runs, and the result is
`failed` on a first switch and `failed-recovery` otherwise. A stop command
that fails is noted in `detail`; the others still run.

### Interrupted switches and recover

A switch that dies (a crash, a power loss, a kill) after it wrote the
journal leaves `txn.json`. Every switch, rollback and `recover` takes the
lock and settles such a journal before anything else; `status` shows it
(`interrupted: ...`) and changes nothing, and `--dry` refuses to go on
while it is there.

- When `current` already points at the journal's release, that release
  verifies (as the switch policy says), its reload list passes and it is
  healthy, the switch is completed: `deploy.json` records `ok` with the
  detail `completed after an interruption`.
- Otherwise `current` and `previous` are put back exactly as the journal
  says they were (so a retried switch never leaves `previous` pointing at
  the active release), and `deploy.json` records `rolled-back` with the
  code `interrupted`.

Either way `highest-counter` is at least the journal's counter afterwards
and the journal is removed. When the links cannot be put back the journal
stays and the command fails.

`ah-switch recover --counter N` writes `highest-counter` as `N` when it is
missing or does not hold a number. The switch never guesses it: on a host
with a `current` link a missing counter could let an old release be
replayed. `N` must be at least the counter of the releases `current` and
`previous` point at; take it from the newest release ever deployed. It
refuses to overwrite a counter that is there.

`deploy.json` holds `id` (the release asked for), `previous_id` (what `current`
pointed at before), `current_id` (what it points at after), `counter`, `mode`
(how the release was verified), `trust` (the policy's minimum, `manual`
without a policy), `rollback`, `time` (UTC), `result` (`ok`, `failed`, `rolled-back` or `failed-recovery`), `check`
(the no-AI check: `passed`, `failed` or `not run`), `failed_step` (`state`,
`verify`, `counter`, `noaicheck`, `validate`, `packages`, `switch`, `reload`,
`health`, `record` or `interrupted`; empty when ok; `import` for step 0), `error`, `detail`, `package_snapshot`
and `restored_health`. A dry run writes nothing.

`error` is a stable failure code, such as `sums-mismatch`,
`file-not-listed`, `bad-entry`, `manifest-mismatch`, `signature-bad`,
`counter-not-above`, `counter-invalid`, `noaicheck-failed`,
`validator-failed`, `package-step-failed`, `link-failed`, `reload-failed`,
`health-failed` or `record-failed` (the full list is in
`internal/authorityhost/switcher/failure.go`). `detail` adds fixed words and
identifiers: a name that holds anything but printable ASCII, or a space, is
quoted with Go's escapes and every identifier is cut at 160 bytes, so a
planted file name cannot forge a line. What a program the switch runs
writes (the no-AI check, a validator, a reload or health command) is never
printed or recorded, since a diagnostic can hold a secret: only how it
ended (`exit status N`, killed, time limit) is. Run the program by hand to
see its messages. The text of a broken state file is not repeated either.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Switched and healthy, or a dry run whose checks passed |
| 1 | Failed; outside a dry run the outcome is in `deploy.json` |
| 2 | Bad arguments; nothing was done |
| 3 | Another switch holds the lock (`flock`, not waited for); nothing was done |

### Failure cases

| Case | What happens |
| --- | --- |
| Incoming folder holds a link, special or hard-linked file, or `releases/ID` exists | Refused at import; nothing is left in `releases/` |
| Wrong sum, file missing or not listed, path escape, link in the release, release folder a link | Refused at verify; `current` unchanged; an import's copy is removed |
| A release folder or file writable by group or others, set-id, or not root's | Refused at verify |
| Signature missing or bad in signed mode, on a switch or a rollback | Refused at verify |
| Switch policy or signers file not root's, writable by others, or not parsing | Refused before anything; nothing recorded |
| `--mode` weaker than the switch policy | Exit 2; nothing done |
| Counter not above the highest ever activated | Refused at counter |
| `--rollback-to` anything but previous | Refused at counter |
| Package step fails, or the snapshot changes and there is none | Refused at packages; `current` unchanged |
| No-AI check fails | Refused at noaicheck, after the package step; no validator runs |
| A validator fails | Refused at validate; nothing reloaded |
| Crash during the package step | The next command records `package-step-interrupted`; the next switch runs the step again from the same snapshot |
| Reload or health fails after the switch | Back to the old release, with its own reload and health lists; `rolled-back` |
| Going back fails, or the old release is not healthy either | The failed release's `stop.list` runs; `failed-recovery` with `restored_health: failed`; the console is needed |
| First switch, or the old release missing or no longer verifying, and the new one fails | `current` stays; its `stop.list` runs; `failed` or `failed-recovery` |
| `--rollback-to` when `previous` points at a missing release | Refused at counter (`no-previous`) |
| Crash or power loss during the switch | `current` points at a whole release, the old or the new one, and the journal is there; the next command settles it (see below) |
| `highest-counter` missing or broken while `current` exists | Refused (`counter-missing`, `counter-invalid`); run `recover --counter N` |
| A second switch while one runs | Exit 3; nothing done |

The switch never deletes a release. Removing old releases (keeping, say, the
last few and never the targets of `current` or `previous`) is future work.

## The no-AI check

`ah-noaicheck` (source in `cmd/ah-noaicheck` and
`internal/authorityhost/noaicheck`) is a small static program, standard
library only, shipped in every release. The deploy runs the new release's
copy before it switches, and a timer runs it every 15 minutes and at boot.
It reads only: it writes nothing, opens no network connection and runs no
program; the processes and listening sockets are read from `/proc`. It is
meant to run as root, which can read every folder it looks into.

```
ah-noaicheck [--root DIR] [--release DIR] [--nft-listing FILE] [--json]
ah-noaicheck ports [--root DIR] [--release DIR] [--json]
ah-noaicheck nft-digest FILE
```

It prints one line per hit, `HIT <kind> <path-or-name> [detail]`, a
`NOTE` line for what is not enforced, and a final `ok` or
`FAILED n hit(s)`; `--json` writes the same as one JSON object. A field
that holds anything but printable ASCII is quoted with Go's escapes, so a
planted file name cannot forge a line. It never prints a file's content or
a variable's value. Exit status: 0 clean, 1 any hit, 2 a usage error or a
read error it cannot continue past (a missing or invalid list, a root or
listing it cannot open).

`--root` (default `/`) checks the tree beneath another folder, for tests;
links inside it are resolved within it. `--release` names the release
folder whose `share/` holds the lists; by default it is the folder above
the program's own `bin/`, so `ah-switch` running the new release's copy
checks against the new release's lists.

What it detects, by kind:

- `binary`: a file, link or folder named exactly like an executable name of
  `ai-names.list` in `/usr/bin`, `/usr/local/bin`, `/usr/local/sbin`,
  `/usr/sbin`, `/bin`, `/sbin` or `/opt` (a fixed list, not the PATH of
  whoever runs it), in `/opt` down to four levels, in any
  `node_modules/.bin`, and in the install folders of every home (the homes
  of `/etc/passwd`, `/root` and every folder in `/home`): `.local/bin`,
  `.cargo/bin` and the mise, npm, pnpm, volta, nvm, uv and pipx trees.
- `tool`: `mise`, `npm`, `npx`, `node`, `nodejs`, `uv`, `uvx`, `pipx`,
  `pnpm`, `yarn`, `bun` or `deno` in the same places.
- `credential`: a credential or config path of `ai-names.list` (the
  entries starting with `.`, such as `.claude` or `.config/opencode`) in
  any home, in `/root` or in `/`.
- `env`: a variable named like a provider key (`*_API_KEY`,
  `*_AUTH_TOKEN`, `ANTHROPIC_*`, `OPENAI_*`, `GEMINI_*`, `CLAUDE_*`,
  `CODEX_*`) set or passed in `/etc/environment`, `/etc/profile` and the
  shells' system profiles, `/etc/profile.d`, the `environment.d` folders,
  the systemd manager configuration and its drop-ins, any unit file or
  drop-in under `/etc/systemd` and `/usr/lib/systemd`, a home's profiles
  and user units, or a file an `EnvironmentFile=` line names. The detail is
  the variable's name.
- `process`: a running process whose name or executable is named like an
  executable of `ai-names.list`.
- `unit`: an enabled unit (an entry of a `*.wants`, `*.requires` or
  `*.upholds` folder of `/etc/systemd/system`, `/etc/systemd/user` or a
  home's `.config/systemd/user`) not in `units.allow`.
- `package`: an installed package (the pacman database) named in
  `forbidden-packages.list`, or, once `packages.allow` is filled, any
  package not in it. While it is empty the check prints
  `NOTE packages not enforced` instead.
- `nft`: when the release has `share/nft.digest`, the SHA-256 of the
  normalised listing passed with `--nft-listing` (the caller runs
  `nft list ruleset`; the check never does) differs, or no listing was
  passed. `ah-noaicheck nft-digest FILE` prints the digest to pin.
  Normalising drops blank lines, trailing spaces, counter values, set
  element expiry, quota use and handle comments.
- `port` (the `ports` subcommand): a listening TCP socket, in
  `/proc/net/tcp` or `/proc/net/tcp6`, on a port not in `ports.allow`.
- `unreadable`: a place the check must look into but cannot read, a link
  loop, or a file too large to read. The check fails closed.

The lists in `share/`, one entry per line, `#` comments and blank lines
ignored; a line with a path separator (except between the components of a
home path in `ai-names.list`), a control byte or an inner space is refused:

- `ai-names.list`: executable names and home credential paths of known
  agent tools.
- `forbidden-packages.list`: packages the host never needs.
- `packages.allow`, `units.allow`, `ports.allow`: the allowlists, empty in
  the repository; the lead fills them from the host's baseline at deploy
  time. An empty `units.allow` or `ports.allow` allows nothing.
- `nft.digest` (optional): the pinned firewall digest.

Limits, said once:

- It is detection, not prevention. The prevention is that nobody logs in,
  the egress filter and the package allowlist.
- A renamed binary is not caught by name; the package allowlist and the
  egress filter cover that. Names are matched exactly, and the lists need
  upkeep as tools appear.
- `/opt` is looked into four levels deep, install folders and the search
  for `node_modules` eight; a tool placed deeper, or anywhere outside the
  places above, is left to the package allowlist.
- A process that renames itself is not caught by name.
- Ports are those of the network namespace the check runs in; UDP is not
  checked.
