- `deploy/ai-host/ap14-level1.sh`, AP14 level 1 for an AI host, run by the
  owner as root from a copy installed as root's own file. It never moves,
  edits or removes an existing file. `--dry --user NAME` is a read-only audit:
  every sudo rule, who it gives sudo and whether it asks for a password, the
  exact `visudo -f` edit for the owner to make by hand for each passwordless
  rule, `Defaults !authenticate` or `exempt_group` that applies to the user,
  and a stop for whatever it cannot see (an unreadable include, a failed
  `sudo -l -U`, an unknown password status, an unsafe register entry, a
  foreign file at the drop-in's name) or for an edit that would leave the
  user no sudo rule that asks for the password. `--apply --user NAME
  --confirm NAME` refuses while an edit or a stop is listed, and otherwise
  writes only new files, under one lock: the drop-in
  `/etc/sudoers.d/zz-ap14-level1-NAME` (`timestamp_timeout=0`, no cached
  credentials; checked with `visudo -cf` alone, published with `ln`, then
  `visudo -c`) and the root-owned standing-grants register
  `/etc/ataniq/grants`; a failed step removes only what the run created.
  `--verify --user NAME` checks that `sudo -l -U` still lists the user's grant
  and that `sudo -n true` says a password is required. Root-equivalent
  groups, polkit rules returning YES, doas `nopass`, setuid files no package
  owns and root-run paths the user may write are reported with a quoted
  remedy, never changed. `--self-test` runs unprivileged, reads only the
  fixtures and runs `--apply` against temporary copies, failing each step in
  turn. Agents run only `--self-test` and the Go tests. See
  `deploy/ai-host/README.md`.
