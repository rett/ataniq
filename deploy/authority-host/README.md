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

## The no-AI check

`ah-noaicheck` (source in `cmd/ah-noaicheck` and
`internal/authorityhost/noaicheck`) is a small static program, standard
library only, shipped in every release. The deploy runs the new release's
copy before it switches, and a timer runs it every 15 minutes and at boot.
It reads only: it writes nothing, opens no network connection and runs no
program; the processes and listening sockets are read from `/proc`. It is
meant to run as root, which can read every folder it looks into.

```
ah-noaicheck [--root DIR] [--release DIR] [--require-policy] [--nft-listing FILE] [--json]
ah-noaicheck ports [--root DIR] [--release DIR] [--require-policy] [--json]
ah-noaicheck nft-digest FILE
```

It prints one line per hit, `HIT <kind> <path-or-name> [detail]`, a
`NOTE` line for a policy that is not enforced, `RUN complete` or
`RUN incomplete`, and a final `ok` or `FAILED n hit(s)`; `--json` writes
the same as one JSON object (with `"complete"`). A field
that holds anything but printable ASCII is quoted with Go's escapes, so a
planted file name cannot forge a line. It never prints a file's content or
a variable's value. Exit status: 0 clean, 1 any hit, 2 a usage error or a
read error it cannot continue past (a missing or invalid list, a root or
listing it cannot open).

`--require-policy` turns each policy the release leaves unset into a hit,
`HIT policy <list> empty` or `HIT policy nft.digest missing`: an empty
`units.allow`, `packages.allow` or `ports.allow`, and a release without
`share/nft.digest`. The switch always passes it, with the live firewall
listing: `ah-noaicheck --root R --release DIR --require-policy
--nft-listing FILE`. Without it (a check by hand) an empty
`packages.allow` and a missing `nft.digest` are `NOTE` lines instead, and
the run is `RUN incomplete`: it exits 0 when nothing else is found, but it
did not enforce those policies. `RUN complete` means every policy was
enforced and nothing stopped a check short.

`--root` (default `/`) checks the tree beneath another folder, for tests;
links inside it are resolved within it. `--release` names the release
folder whose `share/` holds the lists; by default it is the folder above
the program's own `bin/`, so `ah-switch` running the new release's copy
checks against the new release's lists.

What it detects, by kind:

- `binary`: a file, link or folder named exactly like an executable name of
  `ai-names.list`:
  - in `/usr/bin`, `/usr/sbin`, `/bin`, `/sbin`, `/usr/local/bin` or
    `/usr/local/sbin` (a fixed list, not the PATH of whoever runs it);
  - anywhere in `/usr/local` and `/opt`, at every depth;
  - in any `node_modules/.bin` there, in `/usr/lib/node_modules` or in a
    home;
  - in every home (the homes of `/etc/passwd`, `/root` and every folder in
    `/home`; a home in `/proc`, `/sys` or `/dev` is not walked): in any
    folder named `bin` at most three levels below it (`bin`, `.local/bin`,
    `.cargo/bin`, `go/bin`, `src/tool/bin` ...), also when `bin`,
    `.local/bin`, `.cargo/bin` or `go/bin` is a link to elsewhere, anywhere
    in the mise, npm, pnpm, volta, nvm, uv and pipx install folders, and,
    as a regular executable file, anywhere at most six levels below it.

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
  drop-in systemd loads, a home's profiles, or a file an
  `EnvironmentFile=` line names. The unit files are those at the top of
  `/etc/systemd/system`, `/run/systemd/system`, `/usr/lib/systemd/system`,
  the matching `user` folders and a home's `.config/systemd/user`, the
  units their `*.wants`, `*.requires` and `*.upholds` folders link to, and
  the files of their `*.d` drop-in folders. A unit that is a link (into
  `current/etc/systemd`, say) is read where it leads within the root, each
  file once; a masked unit (a link to `/dev/null`) is not read. The detail
  is the variable's name, and only ever a valid identifier
  (`[A-Za-z_][A-Za-z0-9_]*`, at most 128 bytes): no part of a value is
  printed.

  Each file is read in its own syntax. A shell profile is read as shell
  words: an assignment at the start of a command, the names `export`,
  `declare`, `typeset`, `readonly`, `local` and `env` set, `setenv` (csh)
  and, in a `.fish` file, `set`; comments, quoted values, substitutions and
  here-documents are never read for names, and a file a profile sources by
  a literal absolute path is read too. A unit or manager file is read by
  its `Environment=`, `DefaultEnvironment=`, `ManagerEnvironment=` and
  `PassEnvironment=` keys (systemd's quoting) and `EnvironmentFile=` (an
  optional `-`, quoted or not, `%h` and `%t` resolved, a glob in the last
  component matched within the root). `/etc/environment`, `environment.d`
  and the files `EnvironmentFile=` names are `KEY=VALUE` lines;
  `~/.pam_environment` is read by its first word. What the check cannot
  parse (an unclosed quote or substitution, a here-document that never
  ends, an `export` whose name is an expansion, another specifier, a line
  that is not `KEY=VALUE`) is `HIT incomplete env-syntax <file>`.
- `process`: a running process whose name or executable is named like an
  executable of `ai-names.list`. A process that ends while it is read and
  a kernel thread are passed over. A missing or empty `/proc` is
  `HIT incomplete proc <why>`. A process whose name or executable is
  refused for lack of permission is a `HIT unreadable` of its own when the
  check runs as root, and otherwise counted into one
  `HIT incomplete proc-access <n>`, `n` the number of such processes: run
  by hand as another user, the check cannot see other users' processes
  and says so.
- `unit`: an enabled unit (an entry of a `*.wants`, `*.requires` or
  `*.upholds` folder of `/etc/systemd/system`, `/run/systemd/system`,
  `/etc/systemd/user`, `/run/systemd/user` or a home's
  `.config/systemd/user`) not in `units.allow`. An enabled unit that is
  masked (its entry, or a unit of its name at the top of the same folder,
  links to `/dev/null`) is not enabled: it is the line
  `NOTE unit masked <path>`, which is not a hit.
- `unit-link`: a unit link that loops (`loop`) or whose target climbs
  above the root (`escape`).
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
- `port`: a listening TCP socket, in `/proc/net/tcp` or
  `/proc/net/tcp6`, on a port not in `ports.allow`. The main run checks
  it; the `ports` subcommand checks only it.
- `policy` (with `--require-policy`): a policy list of the release is
  empty, or `nft.digest` is missing.
- `unreadable`: a place the check must look into but cannot read, a link
  loop, a file too large to read, or a file replaced between its check and
  its opening. The check fails closed.
- `incomplete`: a check could not cover all it must, so the run proves
  nothing there: `HIT incomplete <folder> depth` when a walk stopped at its
  depth limit above a folder it had to enter, and
  `HIT incomplete budget <limit>` when the run's budget is spent (4 million
  folder entries, 1 GiB read, 4 minutes, 256 MiB held), after which every
  later read stops. Neither is ever a pass.

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

Limits, said once:

- It is detection, not prevention. The prevention is that nobody logs in,
  the egress filter and the package allowlist.
- A renamed binary is not caught by name; the package allowlist and the
  egress filter cover that. Names are matched exactly, and the lists need
  upkeep as tools appear.
- A tool outside the places above (say in `/srv`, or an executable file
  more than six levels down in a home and outside its bin folders) is not
  caught by name; it is left to the package allowlist and the egress
  filter.
- A process that renames itself is not caught by name.
- Ports are those of the network namespace the check runs in; UDP is not
  checked.
