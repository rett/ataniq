# Authority host deployment

The authority host is a separate host with no AI tools on it. This folder
describes how a release of it is laid out and activated. Owner-local values
(names, addresses, keys, secrets) are never in a release or in this folder;
they live in `/etc/authority-host/` on the host.

## The switch: ah-switch

`ah-switch` (command `cmd/ah-switch`, package `internal/authorityhost/switcher`)
is the one way a release becomes live, used by hand on the manual path and by
the updater later. It needs only the Go standard library and opens no network
connection.

### Layout

```
/opt/authority-host/
  releases/<id>/          id = <counter>-<short commit>, e.g. 0007-ab12cd3
    MANIFEST              key: value lines
    SHA256SUMS            every other file of the folder
    SHA256SUMS.sig        optional; checked only with --mode signed
    bin/  etc/  share/
  current  -> releases/<id>
  previous -> releases/<id>
/var/lib/authority-host/
  deploy.json             the outcome of the last switch
  highest-counter         the highest counter ever activated
  switch.lock             the lock of a running switch
```

- The id is the counter, zero-padded to at least four digits, a hyphen and the
  short commit in lowercase hex (7 to 40 characters).
- `MANIFEST` holds the keys `id`, `counter`, `commit`, `built_on` (the class of
  the build host, never its name), `go_version` and `package_snapshot`, one
  `key: value` per line. `id` must be the folder's name, `counter` its counter
  and `commit` must start with its short commit. Blank lines and `#` lines are
  skipped; other keys are ignored.
- `SHA256SUMS` is in the format of `sha256sum`: lowercase hex, two spaces (or a
  space and `*`) and the path relative to the release folder, without `./`,
  `.` or `..`. It lists every file but itself and `SHA256SUMS.sig`. Build it
  from inside the folder, for example
  `find . -type f ! -name 'SHA256SUMS*' -printf '%P\0' | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS`.
- A release holds only folders and regular files. A symbolic link anywhere in
  it is refused, so hashing never follows one out of the folder.
- In signed mode `SHA256SUMS.sig` is an SSH signature in the namespace
  `authority-host-release`:
  `ssh-keygen -Y sign -f KEY -n authority-host-release SHA256SUMS`.
- `share/` holds the command lists the switch runs:
  - `validators.list` (optional): one validator per line, such as
    `systemd-analyze verify {release}/etc/systemd/app.service`,
    `caddy validate --config {release}/etc/caddy/Caddyfile` or
    `nft -c -f {release}/etc/nftables/rules.nft`. `{release}` becomes the
    absolute path of the release folder, so a validator checks the new
    release, not the live one.
  - `reload.list` (optional): one reload command per line, such as
    `systemctl daemon-reload` or `systemctl reload caddy`. These act on what
    `current` points at and may not use `{release}`.
  - `health.cmd` (required): exactly one command, usually a `curl` of the
    health endpoints, which exits 0 when the host is healthy. It may not use
    `{release}` either.

  In each list, blank lines and `#` lines are skipped. A command is its words
  separated by spaces; no shell runs it, so quotes, pipes, redirections, globs
  and variables are refused. The program is an absolute path, `{release}/...`
  in a validator, or a bare name looked up in `/usr/sbin:/usr/bin:/sbin:/bin`
  (never in the caller's `PATH`). Every program the switch runs gets the
  environment `PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C` and nothing else,
  the working folder `/`, no standard input, and its own process group, killed
  whole at its time limit.
- `bin/ah-noaicheck` is the release's no-AI check, required. `bin/ah-pkgstep`
  is its package step, optional.

### Commands

```
ah-switch [--root DIR] [--dry] [--mode manual|signed] [--allowed-signers FILE] [--noaicheck PATH] ID
ah-switch [--root DIR] [--dry] [--noaicheck PATH] --rollback-to previous
ah-switch [--root DIR] status
```

- `--root DIR` is the folder that holds `opt/authority-host` and
  `var/lib/authority-host`; it is `/` on the host and a scratch folder in tests.
- `--mode manual` (the default) checks `SHA256SUMS` only. `--mode signed` also
  checks `SHA256SUMS.sig` against `--allowed-signers FILE` (usually
  `/etc/authority-host/allowed_signers`), which only that mode accepts.
- `--noaicheck PATH` runs another no-AI check program instead of the
  release's `bin/ah-noaicheck`.
- `--rollback-to previous` goes back to the release `previous` points at. It
  accepts that release's id too, and refuses any other.
- `--dry` prints every step and what it would do, runs no program and changes
  nothing: no link, no state file, no lock file. It still reads and checks the
  release (sums, manifest, lists, counter), and it is refused while a switch
  holds the lock.
- `status` prints `current`, `previous`, the highest counter and the last
  outcome from `deploy.json`.

Flags may come before or after the id.

### Steps

`ah-switch ID` prints each step and stops at the first failure. Up to the
rename in step 6, a failure leaves `current` as it was.

1. **verify**: `releases/ID` must be a folder, not a symbolic link. Every file
   in it must be listed in `SHA256SUMS` and match it; a file not listed, a
   listed file missing or changed, a listed path that escapes the folder or is
   not clean, or a symbolic link in the folder is a failure. In signed mode the
   signature must verify: `ssh-keygen -Y find-principals` names the signer in
   the allowed signers file and `ssh-keygen -Y verify` checks the sums in the
   namespace `authority-host-release`. Then `MANIFEST` must match the id and
   the command lists must parse.
2. **counter**: the counter must be above the highest ever activated
   (`highest-counter`; none means 0). A rollback skips this rule but accepts
   only the release `previous` points at.
3. **noaicheck**: the new release's `bin/ah-noaicheck --release DIR` must exit
   0, within 5 minutes.
4. **validate**: each command of `share/validators.list` must exit 0, within
   30 seconds each. A missing list is allowed.
5. **packages**: when the manifest's `package_snapshot` differs from the one
   recorded in `deploy.json`, the release's
   `bin/ah-pkgstep --release DIR --from OLD --to NEW` runs, if it is there,
   within 30 minutes (taking a filesystem snapshot first is that program's
   job). On success the new snapshot is recorded. Without the program nothing
   is done and the recorded snapshot stays.
6. **switch**: `previous` is pointed at the old target of `current`, then a new
   link `current.new` is made and renamed over `current`, so there is never a
   moment without `current`. Then each command of `share/reload.list` runs,
   within 30 seconds each.
7. **health**: `share/health.cmd` runs until it exits 0, every 2 seconds for up
   to 60 seconds, each try within 10 seconds.
8. **record**: on a healthy switch `highest-counter` is set to the new counter
   (not on a rollback) and `deploy.json` is written, each by writing a
   temporary file and renaming it over the old one.

If the reload or the health check fails, the switch goes back: `current` is
renamed back to the old release, `previous` is put back as it was, the reload
list runs again and the health check runs again; the result is
`rolled-back`, and `restored_health` says whether the old release came back
healthy. On the very first switch there is nothing to go back to: `current`
stays at the new release and the result is `failed`.

`deploy.json` holds `id` (the release asked for), `previous_id` (what `current`
pointed at before), `current_id` (what it points at after), `counter`, `mode`,
`rollback`, `time` (UTC), `result` (`ok`, `failed` or `rolled-back`), `check`
(the no-AI check: `passed`, `failed` or `not run`), `failed_step` (`state`,
`verify`, `counter`, `noaicheck`, `validate`, `packages`, `switch`, `reload`,
`health` or `record`; empty when ok), `error`, `package_snapshot` and
`restored_health`. A dry run writes nothing.

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
| Wrong sum, file missing or not listed, path escape, link in the release, release folder a link | Refused at verify; `current` unchanged |
| Signature missing or bad in signed mode | Refused at verify |
| Counter not above the highest ever activated | Refused at counter |
| `--rollback-to` anything but previous | Refused at counter |
| No-AI check fails | Refused at noaicheck; no validator runs |
| A validator fails | Refused at validate; nothing reloaded |
| Package step fails | Refused at packages; `current` unchanged |
| Reload or health fails after the switch | Back to the old release; `rolled-back` |
| The old release is not healthy either | `rolled-back` with `restored_health: failed`; the console is needed |
| Crash or power loss during the switch | `current` points at a whole release, the old or the new one; `previous` may already point at the old one; run `status` and switch again |
| A second switch while one runs | Exit 3; nothing done |

The switch never deletes a release. Removing old releases (keeping, say, the
last few and never the targets of `current` or `previous`) is future work.
