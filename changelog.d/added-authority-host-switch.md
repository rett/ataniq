- Add `ah-switch`, the release switch of the authority host.
  `ah-switch --from INCOMING_DIR ID` imports a release folder into
  `/opt/authority-host/releases/ID` as new, read-only, root-owned files
  (folders and regular files only, within limits of entries, size and
  time), and `ah-switch ID` activates an imported release: it checks its
  `SHA256SUMS` over every file and, when the root-owned switch policy
  `/etc/authority-host/switch-policy.json` asks for it, its SSH signature by
  the policy's principal; refuses a counter not above the highest ever
  attempted; runs the release's package step (before the check), its no-AI
  check (given the root and a listing of the firewall) and its validators;
  journals the switch in `txn.json` before it renames a new `current` link
  over the old one, reloads and checks every health command for up to 60
  seconds, going back to the old release with that release's own reload
  and health lists, or stopping the decision services when it cannot.
  `ah-switch --rollback-to previous` goes back under the same policy,
  `ah-switch recover --counter N` restores a missing counter,
  `ah-switch status` shows the state under a shared lock, `--dry` prints
  every step and changes nothing, and `--root DIR` works on another folder
  (running programs there only with `--allow-exec`). The outcome is
  written to `/var/lib/authority-host/deploy.json` as stable failure codes,
  never a program's output; an interrupted switch is completed or put back
  by the next command. Exit codes are 0 ok, 1 failed, 2 usage and 3
  locked. See `deploy/authority-host/README.md`.
