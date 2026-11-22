- Add `ah-switch`, the release switch of the authority host: `ah-switch ID`
  activates `/opt/authority-host/releases/ID` after checking its `SHA256SUMS`
  over every file (and, with `--mode signed --allowed-signers FILE`, its SSH
  signature), refusing a counter not above the highest ever activated, and
  running the release's no-AI check, its validators and its package step; it
  then renames a new `current` link over the old one, reloads, runs the health
  check for up to 60 seconds and goes back to the old release if it fails.
  `ah-switch --rollback-to previous` goes back to the release `previous` points
  at, `ah-switch status` shows the state, `--dry` prints every step and changes
  nothing, and `--root DIR` works on another folder. The outcome is written to
  `/var/lib/authority-host/deploy.json`; exit codes are 0 ok, 1 failed, 2
  usage and 3 locked. See `deploy/authority-host/README.md`.
