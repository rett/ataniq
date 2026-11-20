- Fix `canary-prepare.sh` with the real tagged binaries, which it could not
  finish. It now copies the probe to `bin/ataniq-probe` in the root (mode
  0700, as it copies pasta) and the inputs file names that copy as
  `executables.probe`: the default `--bin`, `build/canary` of the checkout, is
  in `never_mounted`, so the holder's validate step refused it as a bind
  source. Step 9 of `canary-run.sh` compares the probe copy with the built
  probe, as it compares the stand-in. `ataniq-canary profile-digest` and
  `check-inputs` read the lead's copy of the managed settings once
  provisioning has made it, and before that, when prepare asks for the digest,
  the file `provision.managed_settings` names, which provisioning copies; a
  missing marker is refused in both cases. Before, they failed with
  `sources.managed_settings cannot be read: its folder cannot be opened`. A
  prepare that fails after it made the root now ends by saying how to remove
  the half-made root by hand; it deletes nothing. The second replay screen is
  now the recorded `collapsed-paste-footer-c`, which the holder reads as a
  submitted paste; `-a` carries a damaged title that reads as a busy agent, so
  the second epoch's fence ended "not submitted". The Go change is in a build
  with the `ataniq_canary` tag only.
