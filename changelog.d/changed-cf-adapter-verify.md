- The host adapter of the owner channel (tagged build only) now verifies the
  merge it builds in the merge mode of verification: the record names the
  merge as the tip, the target's tip it was built on as the base and the pin
  under `refs/ataniq/pending/` that keeps it alive, instead of a made-up lead
  ref, and only such a record counts for the approval. A lead-mode record of
  the same commit, or a merge record on another base, is no evidence, and an
  approval with no counting record is not applied, with a notice that says so
  instead of a moved ref. `ataniqd run --owner-channel` now has a real suite
  runner (the intake's verification in the sandbox template, run through the
  pin): `owner/scratch.json` gains the keys `sandbox` (the template file) and
  `work` (the runs' folder), both under a scratch root and outside the state
  folder, and the daemon refuses the flag without them. Building the runner
  starts nothing; a sandbox starts when a request is verified.
