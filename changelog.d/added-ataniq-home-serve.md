- Added the home witness listener and its offline `serve --check` preflight.
  The home can prepare, resume and finalize enrollment with an owner-carried
  receipt. `ataniq-home status` verifies the binding and replays the local
  ledger without contacting the witness.
