- `ataniq-canary check-inputs` (and `profile-digest`, which runs it first) now
  refuses what the holder refuses at its first step, `validate-config`, so that
  the owner's step 8 catches it and not step 11. It runs the holder's own trial
  of the profile and the spec on the inputs file (`sandboxstart.TrialValidate`,
  the code the holder runs), with the agent command line, environment and
  pinned digest of the config, and for the runtime `claude` it requires
  `sources.managed_marker` and a managed-settings file that holds that text,
  which assertion 8 searches for. An inputs file without the key, as the run
  kit's prepare script wrote it, was accepted by step 8 and refused by the
  holder, which then ended the unit at once. A refusal names the field and
  never a value or a path. `docs/canary-slice.md` lists what it checks and
  shows the key. A new test derives the keys the holder requires from its own
  trial and fails when `check-inputs` accepts a file without one. This is in a
  build with the `ataniq_canary` tag only.
