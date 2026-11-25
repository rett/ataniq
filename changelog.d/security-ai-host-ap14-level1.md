- `deploy/ai-host/ap14-level1.sh`, AP14 level 1 for an AI host, run by the
  owner as root: `--dry --user NAME` lists every change and every route from
  that user to root it finds, and changes nothing; `--apply --user NAME
  --confirm NAME` makes only those changes, in that order: the sudoers drop-in
  `/etc/sudoers.d/zz-ap14-level1` (`timestamp_timeout=0`, no cached
  credentials), moving aside each file of `/etc/sudoers.d` whose one rule
  gives the user sudo without a password, and the root-owned standing-grants
  register `/etc/ataniq/grants`. Every sudoers change is checked with visudo
  and undone if a check fails; a passwordless rule anywhere else stops the run
  before any change. Root-equivalent groups, polkit rules returning YES, doas
  `nopass`, setuid files no package owns and root-run paths the user may write
  are reported with a remedy, never changed. `--self-test` reads only fixtures.
  Agents run it only with `--self-test`. See `deploy/ai-host/README.md`.
