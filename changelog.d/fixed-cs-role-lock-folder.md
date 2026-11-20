- `ataniq-canary provision` now makes the folder of the lead's role lock,
  `<state root>/roles/<project>` (and `<state root>/roles` above it), with mode
  0700, owned by the caller, checked like the other folders and reported as
  `made folder` or `kept folder`. The holder takes its role lock with an open
  that makes the file and not its folder, so on a provisioned root it refused
  at its first start with `--role-lock cannot be taken as the role lock: no
  such file or directory` and the unit ended at once. The folder must be empty
  when a run keeps it, so a lock left by an earlier holder is refused as a
  leftover; remove `<state root>/roles/<project>` with the other folders when
  you provision again. New tests start the holder from a tree that
  provisioning made, with no folder made by hand. This is in a build with the
  `ataniq_canary` tag only.
