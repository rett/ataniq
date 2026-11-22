- Add `ah-noaicheck`, the authority host's no-AI check: a small static
  program that reads a host tree (`--root`, default `/`) and prints one
  `HIT <kind> <path-or-name>` line per sign of an AI agent tool, then
  `RUN complete` or `RUN incomplete` and `ok` or `FAILED n hit(s)`
  (`--json` for one JSON object): an agent binary or an installer such as
  `npm` or `mise` in the folders of an explicit PATH inventory (the
  system bin folders, their immediate subfolders and every folder the
  system and home profiles put on PATH), anywhere in `/usr/local` and
  `/opt`, in any `node_modules/.bin`, in a home's bin and install folders
  or as an executable file anywhere in a home, an agent credential folder
  in a home, a provider-key variable in an environment file, profile or
  unit (each read in its own syntax, by name only: function-local
  declarations passed over, home-relative sources resolved per home, and
  the units read through each systemd manager's effective unit view, with
  lookup precedence, masks, linked dependency folders and each manager's
  own specifiers), an agent process, an enabled unit not in
  `units.allow`, a listening TCP port not in `ports.allow`, a package
  forbidden or not in `packages.allow` and, when the release pins one, a
  firewall rule set whose digest differs. `--phase pre`, before a switch,
  also reads the candidate release's own units and the variables its
  front-end configuration refers to (Caddy's `{env.NAME}` and `{$NAME}`
  and `$NAME`/`${NAME}`, never a comment or a literal, and a reference it
  cannot read as `HIT incomplete release-syntax <file>`, never its text)
  and leaves out the ports and the digest;
  `--phase post`, after it, runs everything; `full`, the default, checks
  the host as it is. `--require-policy`, which the switch passes, makes an
  empty `units.allow`, `packages.allow` or `ports.allow` and a release
  without `share/nft.digest` a hit (`HIT policy`); without it the
  unenforced policies are `NOTE` lines and the run is incomplete.
  Whatever the check cannot cover (a walk at its depth limit, an entry of
  unknown type, its budget of entries, bytes, time and memory spent, an
  unusable `/proc`, a process whose `/proc` folder stays without a name,
  a socket table without its own protocol's header, a missing or empty
  pacman database or a package entry without a name, a pinned firewall
  digest without a listing, a file it cannot parse, a source or PATH
  folder it cannot resolve, a missing required `EnvironmentFile=`)
  is a `HIT incomplete` line, and every hit that is not a finding makes
  the run incomplete, never a pass. `ah-noaicheck ports` checks only the
  listening ports. It exits 0 clean, 1 on a hit and 2 on an error, never
  prints a file's content or a value, escapes every path in its errors,
  and reads its lists from the release's `share/`
  (`deploy/authority-host/share`). A renamed binary is not caught by
  name: the package allowlist and the egress filter cover that.
