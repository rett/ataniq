- Fix the attended canary run's set-up for the guard hook's marker. The holder
  refuses a claude start without `sources.managed_marker` and its assertion 8
  searches the managed-settings file for that text, but `canary-prepare.sh`
  wrote neither, so step 11 would end with the holder exiting at once and the
  step saying only that the unit was not active. Prepare now writes a
  managed-settings file that names the guard hook, `/ataniq/qargi/guard-hook`,
  as its PreToolUse hook command, and `sources.managed_marker` `guard-hook` in
  the inputs file; the hash the inputs file records is that of the file as
  written. `canary-preflight.sh` runs `ataniq-canary check-inputs` and looks for
  the marker (and for it in the managed settings), and says to use a fresh
  root when the inputs file is from an earlier prepare. The guide's example of
  the inputs file and the slice's gain the key.
