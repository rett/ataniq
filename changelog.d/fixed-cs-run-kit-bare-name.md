- Fix `canary-prepare.sh`, `canary-reset.sh` and `canary-preflight.sh` run by
  their bare name from their own folder (`sh canary-preflight.sh`), which failed
  with `cd: canary-preflight.sh: Not a directory`. They now find their folder
  with no fork when `$0` holds no slash, and in `/` for `/name`.
