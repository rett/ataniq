- Add `ataniq-canary profile-digest --config CONFIG --inputs FILE` to the
  canary slice's command, which exists only in a build with the
  `ataniq_canary` tag. It prints the sandbox profile digest the holder will
  compute for the config and the inputs file, as one line of 64 lower-case hex
  digits, so the owner can pin it in `holder.profile_digest` before the first
  start; the holder refuses a start whose digest is not the pin, so a wrong
  pin fails the real start at step 12. It refuses a file that is not the
  config's `holder.sandbox_inputs`, the file the holder reads (the resolved
  paths are compared), checks the file against the config as `check-inputs`
  does, computes the digest with the holder's own code, starts and writes
  nothing, and prints no environment value and no path of its own (a refusal
  to read the file repeats the path given to `--inputs`, as `check-inputs`
  does).
