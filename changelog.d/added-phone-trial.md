- The phone trial, a STAND-IN rig for scratch state in the tagged build
  (`make ownerchannel-build`): `ataniqd trial init --state-dir DIR` writes the
  trial's settings, `ataniqd run --owner-channel --trial` serves sample cards,
  and `ataniqd trial submit --kind finish|branch-prune [--count N]` makes them on
  demand, so the owner can try the phone end to end against a real daemon. A
  decision goes through the real path (challenge, passkey assertion,
  verification, record, journal evidence) and applies nothing: an approved
  card ends not-applied, "applied: nothing (trial)". See the "Phone trial"
  section of `docs/seat-gate.md`.
