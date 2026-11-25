- The owner's runbook for an attended proof of real lead replies on the phone,
  `docs/scratch-seat-proof.md`, linked from the scratch leads guide: one
  sitting with the daemon of both tags on the phone trial's state folder, so
  that the trial's gate key, seat pins and paired phones are reused, first
  with the stand-in lead and then with one real Claude lead, in three
  terminals (daemon, console, desk), with every step that starts a process,
  changes a unit or defines a lead marked OWNER ONLY. The daemon refuses that
  folder while it holds the trial's settings; the trial's unit is stopped for
  the sitting (a unit file also masked for the boot), and only
  `owner/scratch.json` is swapped, within `owner/`, by the owner's
  `prepare.sh --swap-in` and `--swap-out`, which check the gate's port free
  and the trial's unit inactive and, by the kind `--record-trial` recorded,
  unable to start: a transient unit gone, a unit file masked or, when a
  unit file of higher priority shadows the runtime mask, without an
  activation source (the fallback written in the evidence),
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
  or keep exactly and a program that is not the unit's. `--status` and the
  printed steps name the way back for the kind: the restart script for a
  transient unit, unmask and start for a unit file. The daemon's output
  ends with its exit status. `qualification/scratch-seat/prepare.sh` writes
  the scratch config (mode 0600) and the files it names in a folder of the
  proof's own, with leads fresh for each sitting (`standin-N`, `live-N`):
  `--dry` prints from the values alone, `--self-test` computes only, and
  `--write` checks the trial's settings, the gate's material, the tools and
  the root (physical paths, an existing parent, an empty or new folder)
  first, runs git in a cleared environment, writes the owner's network check
  `netcheck.sh` (its connect test in the network shape of a scratch lead's
  sandbox: the plan's pasta arguments and the profile's bubblewrap flag set
  for the lead's identity, which a test holds; it probes the gate's numeric
  address with curl, no configuration file and no proxy, from this host,
  from the lead's network in this shell and in a throwaway user unit with
  the lead unit's fixed properties, classifies each probe as connected,
  refused, or filtered or timed out, passes the lead's network only on a
  refusal or timeout the probe itself reports, takes a probe that did not
  run for a finding, and the runbook records its exit status), never
  writes in the trial's
  state folder, never deletes and starts nothing. It takes an IPv4 gate only and one `NAME.service` as the
  trial's unit, which the printed commands name as `"$TRIAL_UNIT"`. A test
  in `cmd/ataniqd` takes a trial-shaped state folder to the scratch daemon
  and back on the files `--write` makes and with the swaps, and shows the
  gate's key, the pins, the paired keys and the trial's settings unchanged;
  another holds the script's gate check to the daemon's.
  The daemon of both tags takes `run --scratch CONFIG --owner-channel
  --owner-settings DIR`: the owner channel reads its settings from
  `DIR/scratch.json` and never from `owner/scratch.json` of the state root,
  so a phone trial's settings, gate identity, pins and paired phones stay as
  they are, read and never moved. It refuses to start (exit 10), naming every
  rule that fails, a folder that is not private or that is, lies in or holds
  the trial's settings folder `owner/`, the state or runtime root, a path a
  lead's sandbox reaches (each lead's folder, mirror, mailbox, published and
  holder runtime folders, the agent install, the probe, the host paths every
  sandbox binds) or the gate's folder, decided on the resolved path and on
  the folders' identities, and a settings file that is a link, not private,
  has a second name, is the key list, the journal, a gate file or the trial's
  settings, or holds a trial's settings; the folder is opened by its handle
  with no link followed and the file read beneath it, so a link planted
  after the check changes nothing. `--check` with the same flags prints every
  check the daemon makes before its locks, pass or FAIL with the reason, and
  what it would read, then exits 0 or 10, binding, locking, making and
  starting nothing.
