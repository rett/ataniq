# Authority host deployment

The authority host holds the owner's deciding seat and must never carry an
AI agent tool or credential. This folder holds what a release of the host
ships besides its binaries; `share/` becomes the release's `share/`
(`/opt/authority-host/releases/<id>/share/`). No owner-local value (a host
name, an address, a user name, a secret) belongs here: those live only in
`/etc/authority-host/` on the host.

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
