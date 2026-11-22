- `ataniq-console --daemon DIR` now opens the pair-a-phone view with `P`. It
  pairs over the same owner connection as the approvals view, so the daemon
  still sees one console as the decider, and it reads the approval app's
  address from the first line of `ataniq/owner-local/seat-origin` in
  `$XDG_CONFIG_HOME` (or `~/.config`) once, when the console starts. The file
  must be a regular file, not a link, at most 1 KiB and not writable by its
  group or by others; otherwise the view says "pairing origin not configured"
  and asks the daemon for nothing, and the address is never in a message or a
  log. A pending pairing whose fingerprint is not exactly eight words is
  refused as a bad answer, and a call whose context is already over is not
  made. See `docs/terminal-console.md`.
