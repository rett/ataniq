- Correct two fix lines of `canary-preflight.sh`. The line for a stale profile
  digest pin now also runs `ataniq-canary check-inputs`, as `canary-prepare.sh`
  does after it pins. The line for a root that is not prepared names a fresh
  `--root` when the folder already holds files, which prepare refuses; for an
  empty or missing folder it still names the same root.
