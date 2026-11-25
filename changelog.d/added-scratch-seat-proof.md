- The owner's runbook for an attended proof of real lead replies on the phone,
  `docs/scratch-seat-proof.md`, linked from the scratch leads guide: one
  sitting with the daemon of both tags on the phone trial's state folder, so
  that the trial's gate key, seat pins and paired phones are reused, first
  with the stand-in lead and then, only once the drop rule (a separate
  change, installed by the owner at the start of sitting 1) is in place and
  the network check passes again, one real Claude lead, in three terminals
  (daemon, console, desk), with every step that starts or stops a process or
  defines a lead marked OWNER ONLY. Nothing of the trial is renamed, moved,
  masked or swapped: the daemon of both tags takes `run --scratch CONFIG
  --owner-channel --owner-settings DIR`, reads the owner channel's settings
  from `DIR/scratch.json` and never from `owner/scratch.json` of the state
  root (the folder sits beside the state and runtime roots, not inside
  them), and the owner stops the trial and starts it again with his own
  commands while the state folder's lock keeps the two daemons apart. The
  daemon refuses to start (exit 10), naming every rule that fails, a folder
  that is not private or that is, lies in or holds the trial's settings
  folder `owner/`, the state or runtime root, a path a lead's sandbox
  reaches (each lead's folder, mirror, mailbox, published and holder
  runtime folders, the agent install, the probe, the host paths every
  sandbox binds), the gate's folder, the key list or the journal, decided on
  the resolved path and on the folders' identities (so a bind mount of a
  guarded folder itself given as the folder is caught; the checks guard
  against mistakes, not attackers, and a mount layout the owner or root
  makes, such as a bind of a folder within a guarded one, nested mounts, a
  mount made later or another namespace, is outside them; a lead cannot
  make a mount on the host), and a settings file that
  is a link, not private, has a second name, is the key list, the journal,
  a gate file or the trial's settings, or holds a trial's settings, or
  settings whose drop folder, mirror, work folder or sandbox template the
  folder is, lies in or holds; the
  folder is opened by its handle with no link followed and the file opened
  beneath it, so a link planted after the check changes nothing, and the
  settings are decoded from the bytes read from the descriptor that was
  checked, never from a second open by name.
  `--check` with the same flags prints every check the daemon makes before
  its locks (also the root marker, the gate's key and pins and the daemon's
  socket path) and one the start does not make, the socket paths of the
  leads' holders, pass or FAIL with the reason, and what it would read,
  then exits 0 or 10, binding, locking, making and starting nothing. `qualification/scratch-seat/prepare.sh`
  writes the scratch config and the settings folder (modes 0600 and 0700)
  and the files they name in a folder of the proof's own, with leads fresh
  for each sitting (`standin-N`, `live-N`): `--dry` prints from the values
  alone, `--self-test` computes only, `--write` checks the state folder, the
  tools and the root first (physical paths, an existing parent that is not
  and lies not in the state folder, its `owner/`, the checkout or a
  temporary folder, by path and by identity, and a root that is not there
  yet), makes the root and every folder below it with `mkdir` of a new name
  and writes only into folders it has just made (a root holding only
  prepare's own mark of an earlier run is taken again), runs git in a
  cleared environment, never writes in the trial's state folder, never
  deletes and starts nothing, and `--status`
  runs the daemon's `--check`; with `--compare` it then refuses a record of
  the digests taken before the sitting that does not list each of the six
  files once, compares the trial's gate key, certificate, pins and settings
  with it, and has the daemon judge the key list. For that the daemon of
  both tags takes `run --scratch CONFIG --owner-channel --check
  --keys-prefix`, which checks the key list and its head with the journal's
  own reader on a private copy (nothing of the state root is written) and
  prints the list's length and SHA-256, and `--check --keys-since
  LEN:SHA256`, which checks them again, checks that the prefix is the
  list's beginning and names the type of each record appended since:
  counter records alone (a phone login) hold, and any other record, a
  changed old byte, a head that does not count the list or any damage is
  CHANGED. It takes an IPv4 gate only. It also writes
  the owner's network check `netcheck.sh` (its connect test in the network
  shape of a scratch lead's sandbox: the plan's pasta arguments and the
  profile's bubblewrap flag set for the lead's identity, which a test
  holds; it probes the gate's numeric address with curl, no configuration
  file and no proxy, from this host, from the lead's network in this shell
  and in a throwaway user unit with the lead unit's fixed properties,
  classifies each probe as connected, refused, or filtered or timed out,
  passes the lead's network only on a refusal or timeout the probe itself
  reports after the sandbox showed an IPv4 address and a default route
  that is up, through a gateway and not a reject or blackhole route
  (without them the probe did not run), takes a probe that did not run for
  a finding, and the runbook
  records its exit status). The runbook's last steps compare the trial's
  files byte for byte with the record and start the trial again. A test in
  `cmd/ataniqd` takes a trial-shaped state folder to the scratch daemon and
  back on the files `--write` makes and shows the gate's key, the pins, the
  paired keys and the trial's settings the same bytes after it; another
  holds the script's gate check to the daemon's.
