- `make scratch-seat-build` builds the daemon with both build tags,
  `ataniq_ownerchannel` and `ataniq_canary`, into the ignored folder
  `build/scratch-seat/`, for the owner's own trial of real scratch leads with
  the seat gate. It runs doctor first and starts nothing, and it is the one
  target that builds with both tags: `make ownerchannel-build` stays the one
  with the owner channel's tag alone and `make canary-build` the one with the
  canary tag alone. No release, no CI job and no other target uses it, and the
  guard tests hold its recipe to exactly the mkdir of its folder and one build
  of `./cmd/ataniqd`, with `doctor` its sole prerequisite.
