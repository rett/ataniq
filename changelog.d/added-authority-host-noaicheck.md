- Add `ah-noaicheck`, the authority host's no-AI check: a small static
  program that reads a host tree (`--root`, default `/`) and prints one
  `HIT <kind> <path-or-name>` line per sign of an AI agent tool, ending with
  `ok` or `FAILED n hit(s)` (`--json` for one JSON object): an agent
  binary or an installer such as `npm` or `mise` in the bin and install
  folders, an agent credential folder in a home, a provider-key variable
  in an environment file, profile or unit (by name only), an agent
  process, an enabled unit not in `units.allow`, a package forbidden or not
  in `packages.allow` (not enforced while that list is empty) and, when
  the release pins one, a firewall rule set whose digest differs;
  `ah-noaicheck ports` checks the listening TCP ports against
  `ports.allow`. It exits 0 clean, 1 on a hit and 2 on an error, never
  prints a file's content or a value, and reads its lists from the
  release's `share/` (`deploy/authority-host/share`). A renamed binary is
  not caught by name: the package allowlist and the egress filter cover
  that.
