- Fix the review findings of the host adapter of the owner channel (builds with
  the tag `ataniq_ownerchannel` only). A second request file for an id that was
  taken is set aside in the `collected` folder and never answered, so it no
  longer takes the only reply file of the first request. A change with an odd
  path now makes a card that flags the path (the new flag kind `odd-path`, which
  needs a sign-off) instead of a silent refusal. The refusal codes
  `feature-name`, `feature-moved`, `record-too-large` and `binding-mismatch` of
  the intake reach the lead's reply instead of `request-invalid`. `Apply` says
  `not-applied` where nothing was written (the repository's attributes, a ref
  lock held by another writer). `ataniqd run --owner-channel` now refuses, with
  the configuration's exit code, a state folder that is, lies in or holds the
  account's default state or runtime folder, a scratch root of that kind, and a
  mirror that is not under a scratch root; `owner/scratch.json` must carry
  `"door": "stand-in"` and `"roots"`; and the daemon prints "owner channel:
  STAND-IN door, scratch state: no real approval" at start.
