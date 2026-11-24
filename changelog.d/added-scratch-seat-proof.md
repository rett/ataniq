- The owner's runbook for an attended proof of real lead replies on the phone,
  `docs/scratch-seat-proof.md`, linked from the scratch leads guide: one
  sitting with the daemon of both tags on the phone trial's state folder, so
  that the trial's gate key, seat pins and paired phones are reused, first
  with the stand-in lead and then with one real Claude lead, with every step
  that starts a process marked OWNER ONLY. The daemon refuses that folder
  while it holds the trial's settings; the runbook moves only
  `owner/scratch.json` aside to a free name and back, by the owner's hand,
  with the trial's unit stopped meanwhile (the state folder's daemon lock
  refuses the second daemon, exit 12). `qualification/scratch-seat/prepare.sh`
  writes the scratch config (mode 0600) and the files it names in a folder of
  the proof's own: `--dry` writes nothing, `--self-test` computes only, and
  `--write` never writes in the trial's state folder, never deletes and starts
  nothing. A test in `cmd/ataniqd` takes a trial-shaped state folder to the
  scratch daemon and back and shows the gate's key, the pins and the paired
  keys unchanged.
