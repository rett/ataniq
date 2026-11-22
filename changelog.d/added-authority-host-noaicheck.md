- Add `ah-noaicheck`, the authority host's no-AI check: a small static
  program that reads a host tree (`--root`, default `/`) and prints one
  `HIT <kind> <path-or-name>` line per sign of an AI agent tool, then
  `RUN complete` or `RUN incomplete` and `ok` or `FAILED n hit(s)`
  (`--json` for one JSON object): an agent binary or an installer such as
  `npm` or `mise` in the bin and install folders, an agent credential
  folder in a home, a provider-key variable in an environment file,
  profile or unit (by name only), an agent process, an enabled unit not in
  `units.allow`, a listening TCP port not in `ports.allow`, a package
  forbidden or not in `packages.allow` and, when the release pins one, a
  firewall rule set whose digest differs. `--require-policy`, which the
  switch always passes, makes an empty `units.allow`, `packages.allow` or
  `ports.allow` and a release without `share/nft.digest` a hit
  (`HIT policy`); without it the unenforced policies are `NOTE` lines and
  the run is incomplete. `ah-noaicheck ports` checks only the listening
  ports. It exits 0 clean, 1 on a hit and 2 on an error, never prints a
  file's content or a value, and reads its lists from the release's
  `share/` (`deploy/authority-host/share`). A renamed binary is not caught
  by name: the package allowlist and the egress filter cover that.
