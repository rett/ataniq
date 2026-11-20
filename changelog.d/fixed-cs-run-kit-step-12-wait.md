- The canary run script's step 12 now waits for the holder's second write of
  `holder.json` (the epoch named and `sandbox_started` true), for up to 90
  seconds, or until the unit is no longer active, and prints the unit's journal
  before it fails. A quick Enter could read the file between the holder's two
  writes and fail the pass. This is in a build with the `ataniq_canary` tag only.
