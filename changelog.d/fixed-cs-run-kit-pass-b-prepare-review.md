- Run kit, pass B: the checklist and the printed next steps of
  `canary-prepare.sh --pass B` now tell the owner to run `qualify-mirror` for
  the new root (the commands with its paths) or to give the preflight
  `--mirror-report`, since the preflight wants that root's report. They say how
  to add a model when `--model` was not given; step 15 asks for a no when no
  model argument is shown. `netcheck.sh` binds `/etc/ssl`,
  `/etc/ca-certificates`, `/etc/nsswitch.conf` and `/lib64` only where the host
  has them. The dry text of step 9 names the check that `argv[0]` is under
  `/ataniq/agent`.
