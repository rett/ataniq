- The owner's runbook for an attended proof of real lead replies on the phone,
  `docs/scratch-seat-proof.md`, linked from the scratch leads guide: one
  sitting with the daemon of both tags on the phone trial's state folder, so
  that the trial's gate key, seat pins and paired phones are reused, first
  with the stand-in lead and then with one real Claude lead, in three
  terminals (daemon, console, desk), with every step that starts a process,
  changes a unit or defines a lead marked OWNER ONLY. The daemon refuses that
  folder while it holds the trial's settings; the trial's unit is stopped and
  masked for the sitting, and only `owner/scratch.json` is swapped, within
  `owner/`, by the owner's `prepare.sh --swap-in` and `--swap-out`, which
  check that the unit is inactive and masked and the gate's port free,
  record the trial settings file's identity and digest in the proof's
  folder, rename only to names that are free (a dangling link counts as
  taken) and verify each step; `--status` reads the layout and prints one
  next step, also after an interruption or a reboot. The owner's
  `prepare.sh --record-trial`, run while the trial runs, reads the trial's
  unit (`systemctl --user show` only) and records it in the proof's folder:
  its properties as evidence, its kind (a transient unit of `systemd-run`, or
  a unit file) and, for a transient unit, `trial-restart.sh`, the
  `systemd-run` command that re-creates it, with the command line of its
  main process read from `/proc` and every argument single-quoted; it
  refuses, writing nothing, a `MainPID` of 0, a command line it cannot read
  or keep exactly and a program that is not the unit's. The daemon's output
  ends with its exit status. `qualification/scratch-seat/prepare.sh` writes
  the scratch config (mode 0600) and the files it names in a folder of the
  proof's own, with leads fresh for each sitting (`standin-N`, `live-N`):
  `--dry` prints from the values alone, `--self-test` computes only, and
  `--write` checks the trial's settings, the gate's material, the tools and
  the root (physical paths, an existing parent, an empty or new folder)
  first, runs git in a cleared environment, writes the owner's network check
  `netcheck.sh`, never writes in the trial's state folder, never deletes and
  starts nothing. It takes an IPv4 gate only and one `NAME.service` as the
  trial's unit, which the printed commands name as `"$TRIAL_UNIT"`. A test
  in `cmd/ataniqd` takes a trial-shaped state folder to the scratch daemon
  and back on the files `--write` makes and with the swaps, and shows the
  gate's key, the pins, the paired keys and the trial's settings unchanged;
  another holds the script's gate check to the daemon's.
