# The attended proof of real replies

This is the owner's runbook for one sitting that shows a reply written by a
lead in the real sandbox reaching the phone's and the desktop's Leads pages
through the real daemon, gate and seat: first the stand-in lead, then, once
the drop rule is in place, one real Claude lead. It uses the daemon of
[Running real leads with the seat](scratch-leads.md#running-real-leads-with-the-seat)
on the phone trial's state folder, so that the gate's key, its seat pins and
the paired phones' keys stay the trial's and the phone stays paired. The
proof's daemon reads its own settings from a folder of the proof's
(`ataniqd run --owner-settings`), so nothing of the trial is renamed, moved,
masked or swapped.

Like everything it runs, it is provisional and unqualified: it passes no case
of a qualification, nothing in it is an approval, and no message goes from the
seat or the desk to a lead. It records what was seen, not a result.

**Who runs what.** Every step that starts a real process (a unit, the daemon,
the console, a sandbox, an agent, the claude program) or stops one, or that
defines a lead (the daemon runs git), is marked **OWNER ONLY** and is run by
the owner's hand, in his terminal. An agent may run
`qualification/scratch-seat/prepare.sh` with `--dry` or `--self-test` only,
and the tests. `--write` and `--status` are the owner's: `--write` starts
nothing and writes only in the proof's own folder, and `--status` writes
nothing and runs the daemon's `--check`, which binds, locks and starts
nothing, but both name his folders, and `--status` reads his trial's state
folder. `ROOT/netcheck.sh` is the owner's alone.

**What is removed, and what is not.** No step deletes, renames or moves a file
of the trial or of the proof. Two things go by design: the chat service
consumes each reply file a lead writes (it takes the file into its store), and
turning a lead's chat off clears the replies it stored.

## The owner's decisions (2026-11-24)

- **Reuse the trial's gate identity and pairing keys.** The scratch config's
  `state_root` is the trial's state folder, so the gate's key and pins
  (`owner/gate/`) and the phones' keys (`owner/keys.log`, `owner/keys.head`)
  are the ones the seat and the phone already use; the phone needs no new
  pairing.
- **Retire the swap.** The proof's daemon reads its own settings from
  `$ROOT/owner-settings/scratch.json` (`--owner-settings`); the trial's
  `owner/scratch.json` stays where it is and is not read. The trial's folder is
  never renamed, moved, masked or swapped. The trial is stopped and started
  again by the owner's own commands; the state folder's daemon lock keeps the
  two daemons apart.
- **Stand-in first; the real lead after the drop rule.** The stand-in lead in
  the real sandbox (the owner gives it a token, it commits and writes its one
  reply) needs no network and runs in any sitting. The real Claude lead waits
  until the drop rule is in place: a separate change (SB-1) brings the rule,
  and the owner installs it as root at the start of sitting 1. Until then a
  sitting stops after the stand-in. With the rule in place, one real Claude
  lead runs with the cheapest model, the limits and a fresh login, as in the
  canary's pass B ([the run kit's guide](../qualification/canary/run/README.md)),
  with its chat read on.

## The values

The owner gives these in his shell; no file of the repository holds them. Set
them once in each of the three terminals, in the checkout: edit the values
after each `=` (every one is a plain path, address, number or name; keep the
quotes), then paste the block.

```sh
OWNER_STATE_DIR='/path/of/the/trial/state/folder'   # the trial daemon's --state-dir
ROOT='/path/of/a/new/folder/for/the/proof'          # its parent exists; outside /tmp, the checkout and the state folder
SEAT_GATE='192.0.2.10:7443'                         # the trial daemon's --seat-gate (IPv4 only)
SITTING='1'                                         # this sitting's number, 1 to 999; the leads are standin-1 and live-1
TRIAL_UNIT='name-of-the-trial.service'              # the user unit that runs the trial daemon, for your own stop and start
CLAUDE_FILE='/path/of/the/tested/claude'            # claude 2.1.325, one regular file, as for pass B
CLAUDE_MODEL='name-of-the-cheapest-model'
export OWNER_STATE_DIR ROOT SEAT_GATE SITTING TRIAL_UNIT CLAUDE_FILE CLAUDE_MODEL
S=$OWNER_STATE_DIR CONFIG=$ROOT/scratch.json SETTINGS=$ROOT/owner-settings D=build/scratch-seat/ataniqd C=build/canary
P=qualification/scratch-seat/prepare.sh
```

For a sitting with the stand-in alone (any sitting before the drop rule is in
place), take the claude values back after the block, in each terminal, and
skip step c:

```sh
unset CLAUDE_FILE CLAUDE_MODEL
```

`prepare.sh` then writes the stand-in lead only, and it takes `--claude` and
`--model` only together. It refuses a value it cannot use before it prints
anything: a sitting that is not 1 to 999, a gate that is not one IPv4 address
of a host and a port (an IPv6 address is refused for the proof; the daemon
would take one, the proof's checks do not), and a `HOME` that cannot give the
never-mounted paths `~/.ssh`, `~/.claude` and `~/.codex`. `TRIAL_UNIT` is not
a value of prepare's: only your own commands use it, and the commands it
prints name the unit as `"$TRIAL_UNIT"`, which your shell fills in, never by
its value.

The sitting's number names the two leads (`standin-N`, `live-N`), so that
each sitting has leads of its own: their folders, their roles in the registry
and their units are new. `--write` refuses a sitting whose leads the state
folder already has. A fresh `ROOT` alone does not make a fresh Claude login:
the provider's config of a lead lives in the state folder
(`leads/scratch-live-N-lead/config/claude`), and a defined lead keeps its
record; a new sitting number does.

## What was proved in process

`TestScratchSeatTakesThePhoneTrialsStateFolder` in
`cmd/ataniqd/scratch_seat_proof_linux_test.go` makes a state folder as the
owner made his (`ataniqd trial init`, `ataniqd seat-gate init`, one seat pin,
one paired key, the trial daemon run with one sample card), runs
`prepare.sh --write` into a private folder (the binaries it copies are stubs
it never runs), then takes the state folder through the steps below with the
real daemon of both tags, the real `ataniq-canary scratch-init` (built with
`scripts/go-tool.sh`), the daemon's `--check`, and a pinned seat on the
loopback address. The unit manager is a fake one, and both leads are defined,
not started. `TestScratchSeatProofScriptWritesWhatTheDaemonReads` holds that
the config and the settings `prepare.sh` writes pass the daemon's strict
readers, and `TestScratchSeatProofGateCheckMatchesTheDaemon` that every gate
address the script takes is one the daemon takes, as the same address. What
they show:

- **Without the flag the daemon refuses the trial's state folder.** It reads
  the trial's settings in `owner/scratch.json` (`"trial": true`) and `run
  --scratch CONFIG --owner-channel --seat-gate IP:PORT` exits 10 with
  `ataniqd run: --owner-channel: the scratch settings are a phone trial's
  ("trial": true), which serves sample cards only: run it with --trial`,
  before the root marker is looked at and before any lock: nothing is
  written, not even the runtime root.
- **With `--owner-settings "$SETTINGS"` the trial's settings are not read.**
  While the trial runs, `--check` fails the missing root marker alone and
  binds, locks and makes nothing; after `scratch-init` it passes every line,
  with the trial still running. The daemon itself refuses for the missing
  marker (`no marker <state>/scratch-root.json`, exit 10, before any lock),
  and with the marker the state folder's daemon lock refuses it while the
  trial runs (exit 12), so the two can never serve the folder at once.
- **`scratch-init` takes the trial's private state folder as it is.** It prints
  `kept folder` and `made file .../scratch-root.json`, and when run again
  `kept file`. It writes nothing in `owner/`.
- **The keys are safe.** Once the trial is stopped, the daemon's start line
  says that it reads `$SETTINGS/scratch.json` and not `owner/scratch.json`,
  the gate's start line names the trial's key fingerprint, the trial's pinned
  seat connects, and `owner.keys` lists the paired credential (its id). Both
  leads of the sitting are defined (each has its `provisioned.json`), a reply
  of the stand-in reaches the seat once its chat is on, and the live lead's
  chat turns on and off.
- **A restart returns off.** Restarted with the stopped stand-in's chat on,
  the daemon comes back with every lead's chat off.
- **The way back is the trial's own start.** The trial daemon starts again on
  the same folder, with what the proof's daemon left in it, with the same key
  fingerprint and the same paired credential, and serves its STAND-IN sample
  rows (`daemon:alpha` first) and its one sample card.
- **Read, never written.** The gate's key and certificate, the pins,
  `keys.log`, `keys.head` and the trial's settings are the same bytes after
  the proof's daemon stopped as before it started, and again after the trial
  ran once more; the trial's settings are the same file throughout, and no
  `owner/scratch.json.*` name is ever made.

The byte comparison holds because no phone logs in during the test. In the
sitting, a login on the phone with a credential whose sign counter rises
appends a counter record to the key journal (`keys.log`, `keys.head`); a
passkey of the platform's keeps its counter at 0 and appends nothing. Step 13
compares the bytes, and a difference there is written down and looked at.

The tests of the flag (`cmd/ataniqd/scratch_settings_linux_test.go`) hold its
refusals, each with `--check` (the line of its rule says `FAIL` and why) and
without it (exit 10, nothing made): the trial's `owner/` folder, a folder in
it, a link to it, a link whose target climbs with `..`, the state folder, the
runtime folder, a folder in a lead's home, in the agent install, the folder
of the probe, the proof's root (which holds the agent install), the gate's
folder, a folder others can enter, and a settings file that is a link, a
hard link of the key list, readable by others, missing or a trial's; a link
planted after the folder was opened changes nothing, and a settings file
replaced between its check and its read is refused. No test makes a mount, so
the mount cases are held on the rules themselves: a bind mount of a guarded
folder itself (by its identity), and a bind mount of a folder within `owner/`
given as the folder, the settings folder bound into a lead's folder and
`owner/` bound into the settings folder (by their places in the filesystem, on
the daemon's mount table with the mount's line added), with the reading of
mount tables held on tables written out in
`cmd/ataniqd/scratch_mounts_linux_test.go` (a plain host, a bind of a
subfolder, a bind into a lead's folder, btrfs subvolumes and escaped spaces). `TestScratchLeadReachCoversEveryBind` in
`internal/leaddaemon` holds the leads' reach to every source of their bind
tables. `tests/scratch_seat_prepare_test.go` holds the script: `--dry` and
`--self-test` read and write nothing, `--write` writes in the root only and
refuses what it can judge, `--status` runs the daemon's check with exactly
the flags of step 2 and `--check` and compares the trial's digests, and no
line of the script deletes, moves, links or masks anything or runs
`systemctl`.

What none of it shows: the real unit and its stop and start, the holder,
sandbox and stand-in, the paste, claude and its login, the seat on the
authority host, the phone, the firewall, the drop rule and the network. The
sitting shows those.

### What the proof's daemon leaves in the trial's state folder

`scratch-root.json`; the two scratch roles of the sitting in the registry
(`registry/`, which the trial daemon made); `leads/`, `mirrors/`, `mailbox/`,
`pub/`, `roles/`, `scratch/`, `chat/`, `evidence/` and `lead-commands.jsonl`;
and in `owner/` the owner channel's `records/` and `genesis/` beside the
journal it shares with the trial. The trial daemon starts with all of it
there and serves as before (its registry, which `ataniqd status` shows, also
holds the scratch roles). They are left where they are. The live lead's
folder `leads/scratch-live-N-lead/` keeps the provider's config and login
(`config/claude`, which the sandbox binds at `/home/lead/.claude`), its home
and the holder's last screen (`holder/final.json`): the login is revoked at
step 10, and the files stay, private (see [What is recorded](#what-is-recorded)).

## Before the sitting

None of these starts the daemon or a lead; c runs the claude copy, to read its
version, and e runs pasta, bubblewrap, curl and a throwaway user unit.
`prepare.sh` prints c to g with your values.

- **a. Build**, in the checkout, after `make doctor`:

   ```sh
   make canary-build
   make scratch-seat-build
   ```

   `build/canary/` then holds the holder, the console, `ataniq-canary`, the
   stand-in and the probe, and `build/scratch-seat/ataniqd` is the daemon of
   both tags.
- **b. Prepare.** Read the dry run, then write:

   ```sh
   sh "$P" --dry
   sh "$P" --write
   ```

   It takes `OWNER_STATE_DIR`, `ROOT`, `SEAT_GATE`, `SITTING`, `CLAUDE_FILE`
   and `CLAUDE_MODEL` from the environment (or `--state`, `--root`, `--gate`,
   `--sitting`, `--claude` and `--model`); without the two claude values the
   config has the stand-in lead only. `--dry` prints from the values alone and
   reads none of your folders, so what depends on them (pasta, bubblewrap) is
   printed as a condition. `--write` first checks, and refuses before it makes
   anything: a state folder that is a link, not at its physical path or
   without an `owner/` folder; anything of this sitting's leads in the state
   folder; a missing binary, daemon, pasta, bubblewrap or `/usr/bin/git`; a
   root whose parent is missing or reached through a link, that is not new or
   empty or cannot be read or listed, or that lies physically in or around the
   state folder, the checkout, `/tmp`, `/var/tmp` or `$TMPDIR`; and a `HOME`
   that is not a real folder at its physical path. What only the daemon can
   judge (the settings folder against the trial's folder and the leads'
   reach, the gate's key and pins, the root marker, the socket paths) is the
   daemon's check of step g. It makes only the root folder itself, never a
   parent. It writes, in `$ROOT` only:

   | File | What it is |
   | --- | --- |
   | `scratch.json` | The scratch config (mode 0600): `state_root` the trial's state folder, `runtime_root` `$ROOT/runtime` (the daemon makes it), `max_running` 1, and the leads below. |
   | `owner-settings/scratch.json` | The owner channel's non-trial settings (the folder 0700, the file 0600), which the daemon reads with `--owner-settings`: the stand-in door, the sample project `proof-project`, the scratch root `$ROOT/owner-channel` with the drop folder in it, and a sandbox template that is never written, so that no request can be verified and no suite sandbox starts. |
   | `conf/` | The managed settings, which name a PreToolUse hook and hold the marker `guard-hook`; the hook itself, which exits 0 and guards nothing (the holder refuses a claude start without the marker, the canary kit writes the same no-op); and the replay screens of the checkout. |
   | `agent-install/` | A copy of the stand-in and of the claude file (`claude`, mode 0700). The copy is not run. |
   | `bin/ataniq-probe` | A copy of the probe (the checkout is never mounted), and a copy of pasta when `/usr/bin/pasta` is a link. |
   | `upstream/` | A throwaway repository, one commit on `develop`, that both leads' mirrors are made from. Git runs with a cleared environment, a fixed `PATH` and `/usr/bin/git`, `HOME` in the root, no system or global config, no hooks and no signing. |
   | `netcheck.sh` | Your network check (step e), mode 0700; prepare never runs it. |
   | `evidence/`, `owner-channel/drop/` | Empty folders. |

   The two leads of `scratch.json`, for sitting N:

   | Key | `agent` | `inject` | `limits` |
   | --- | --- | --- | --- |
   | `standin-N` | `"kind": "stand-in"`, runtime `claude` 2.1.325, `footer_mode` `manual`, argv `["/ataniq/agent/ataniq-canary-standin"]`, no env | `true` | 4 GiB, no swap, 512 tasks, 1 GiB floor, 50 percent |
   | `live-N` | `"kind": "live"`, runtime `claude` 2.1.325, `footer_mode` `manual`, argv `["/ataniq/agent/claude", "--model", "$CLAUDE_MODEL"]`, env `DISABLE_AUTOUPDATER=1` and `CLAUDE_CODE_NO_FLICKER=1` | `true` | the same |

   `max_running` is 1: one sandbox at a time, so the stand-in is stopped before
   the live lead starts (a start while it runs is refused `max-running`).
   `max_running` and the unit's limits bound memory and tasks; they do not
   bound the price of the model or how long a session lasts, which is yours
   to watch (step 9), and the memory floor and share are inputs of the
   canary run script, not of this daemon
   ([scratch leads](scratch-leads.md)). `record_screens` is off: no
   recording is made, but the holder still keeps the last screen it showed
   (see [What is recorded](#what-is-recorded)). Neither lead has a
   `profile_digest` pin: the daemon computes the digest at the define with
   the holder's own code (see
   [the profile digest and the pin](scratch-leads.md#the-profile-digest-and-the-pin)).
   `sandbox.never_mounted` holds the checkout, `~/.ssh`, `~/.claude`,
   `~/.codex`, `owner/` of the state folder and `$SETTINGS`.
- **c. OWNER ONLY: the claude copy's version** (a live sitting only; it runs
   the claude program, with an empty environment and a new empty home in the
   root, as pass B's prepare does):

   ```sh
   H=$(mktemp -d "$ROOT/version-home.XXXXXX") && env -i HOME="$H" "$ROOT/agent-install/claude" --version
   ```

   It must name 2.1.325, the version the recorded screens are of. The `&&`
   runs claude only when the home was made: a failed `mktemp` runs nothing.
- **d. The firewall is your rule.** The gate's port admits TCP from the
   authority host only, as [the firewall](seat-gate.md#the-firewall) says.
- **e. OWNER ONLY: the lead's network, with the trial running** (it starts
   pasta, bubblewrap, curl and a throwaway user unit, as the canary kit's
   `netcheck.sh` does; it uses no credential and starts no agent):

   ```sh
   { sh "$ROOT/netcheck.sh"; echo "netcheck: exit status $?"; } 2>&1 | tee -a "$ROOT/evidence/netcheck.txt"
   ```

   The last line is the script's own exit status, the number of findings
   (the braces keep it: a pipe into `tee` alone would give `tee`'s). It
   probes one TCP connection to the gate's address three times and names
   each probe's class: connected, refused, or filtered or timed out, or a
   probe that did not run. From this host it must be connected (the trial's
   gate answers, or the next lines prove nothing); from the lead's network,
   in the shape of the sandbox, both from this shell and in a throwaway user
   unit with the lead unit's fixed properties (where a lead's holder starts
   pasta), it must be refused or filtered: `PASS ... refused (no connection
   to the gate's port was made)`. A probe that did not run (no curl, a curl
   without `telnet://`, a sandbox or unit that did not start) is a
   `FINDING`, never a pass, and so is a connection. Any `FINDING` there ends
   the sitting before it starts. The probe is curl with no configuration
   file and no proxy, to the numeric address, and it sends no byte. The
   script then lists this host's listeners on non-loopback addresses and
   says what stays open until the drop rule is in place: the sandbox's
   network is open (pasta gives it this host's outbound access, to your LAN
   and the Internet), `provider_host` is a DNS check and not a rule, and the
   guard hook guards nothing.

   The connect test runs in the network shape of a scratch lead's sandbox:
   pasta with the plan's own arguments (`sandboxnet.PastaArgs()`: no port
   forwarding either way, the host's loopback and gateway not mapped), and
   bubblewrap with the profile's flag set for the identity a scratch lead has
   (`--unshare-all --share-net --unshare-user`, UID and GID 0, no
   capabilities, no seccomp filter, as scratch provisioning names none);
   `TestScratchSeatNetcheckMatchesTheLeadsNetwork` fails when either drifts,
   when the lead's unit gains a network property, or when the throwaway
   unit's properties are no longer the lead unit's fixed ones, and
   `TestScratchSeatNetcheckClassifiesEachProbe` holds the classes on inert
   fixtures. What differs does not decide whether the gate answers: the binds
   (the host's `/usr` and a few `/etc` files here, the profile's table in the
   sandbox) and the program inside (`sh` and `curl` here, the probe and the
   agent there); the `resolv.conf` (the host's here, one the holder derives
   there), since the gate is an address and no name is looked up. The
   throwaway unit's name is systemd-run's, not a lead unit's: a rule that
   matches lead units by name is not seen by this probe, which is why step 7
   also asks for the drop rule's own check.
- **f. The root marker (C)**, with the trial running:

   ```sh
   $C/ataniq-canary scratch-init --config "$CONFIG"
   ```

   It prints `kept folder` and `made file .../scratch-root.json` (`kept
   file` when run again). It writes nothing in `owner/`, and the trial daemon
   serves as before with the marker there. It refuses a state folder that is a
   default one or carries the canary's marker.
- **g. The daemon's check, and the record of the trial's files (C)**, while
   the trial runs, nothing more:

   ```sh
   sh "$P" --status
   $D seat-gate pins --state-dir "$S"
   journalctl --user --unit "$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1
   (cd "$S/owner" && sha256sum -- gate/key.pem gate/cert.pem gate/seat-pins keys.log keys.head scratch.json) >"$ROOT/evidence/trial-before.sha256"
   ```

   `--status` runs the daemon's own check, `$D run --scratch "$CONFIG"
   --owner-channel --seat-gate "$SEAT_GATE" --owner-settings "$SETTINGS"
   --check`, and passes its lines through. The check binds, locks, makes and
   starts nothing, so it runs beside the trial. Each line is `pass` with what
   holds, or `FAIL` with the reason: the settings folder, opened by its handle
   with no link followed, is a private folder of yours, is not, lies not in
   and holds not the trial's `owner/` folder, the state folder or the runtime
   folder, no path a lead's sandbox reaches (each lead's folder, mirror,
   mailbox, published folder and holder runtime folder, the agent install,
   the probe, the host paths every sandbox binds) and not `owner/gate`, the
   key list or the journal, by path, by identity and by place in the
   filesystem (so a bind mount of a guarded folder, of a folder within one,
   or of the settings folder into one is caught); the settings file is a
   private regular file with one name and the proof's settings; the folder
   is not, lies not in and holds not the drop folder, the mirror, the work
   folder or the sandbox template the settings name; the scratch roots and the suite; the root marker (step f); the gate's address, key
   fingerprint and pins; and the socket paths of the daemon and of every
   lead's holder. It then lists what the daemon will read (the settings
   file, the gate's key, certificate and pins, read only; the key list; the
   journal and records) and that `owner/scratch.json` is not read. It must end
   `check: no refusal`. The daemon makes the same checks at its start and
   refuses (exit 10) with the same reasons, before any lock; only the locks
   and the gate's port, which the check does not take, are left to the start.
   The pins line, the trial's fingerprint line of its current run, and the
   digests of the trial's gate key, certificate, pins, key list and settings
   are the record that step 13 compares with. Nothing of the trial is changed
   by any of it.

## The sitting

Three terminals in the checkout, each with the values above: **A** for the
daemon, **B** for the console, **C** for the desk's commands (prepare, chat,
systemctl). The phone at hand, and a desktop browser. Keep the console window
large (100 by 30 or more) and do not resize it while attached.

### Switch from the trial to the proof

1. **OWNER ONLY (C): stop the trial**, by your own command; nothing is masked
   or moved:

   ```sh
   systemctl --user stop -- "$TRIAL_UNIT"
   systemctl --user show -p ActiveState -- "$TRIAL_UNIT"
   ```

   The last says `ActiveState=inactive`. If anything starts the trial during
   the sitting (your hand, a timer, a socket), it starts on its own settings,
   which are still in place, and the state folder's lock refuses whichever
   daemon comes second (exit 12): the trial while the proof's daemon runs, the
   proof's daemon while the trial runs. Neither can serve the folder beside
   the other, and nothing needs repairing afterwards. The phone now finds no
   desk.
2. **OWNER ONLY (A): the daemon.**

   ```sh
   sh -c 'trap : INT; "$@" 2>&1; echo "ataniqd: exit status $?"' sh $D run --scratch "$CONFIG" --owner-channel --seat-gate "$SEAT_GATE" --owner-settings "$SETTINGS" | tee -ai "$ROOT/evidence/daemon.log"
   ```

   Look for the owner channel's banner (`owner channel: STAND-IN door,
   scratch state: no real approval`: the door of cards, not the leads), the
   line `owner channel: settings read from` your `$SETTINGS/scratch.json
   (--owner-settings); owner/scratch.json of the state folder is not read`,
   the gate's line `seat gate: listening on` your address `, key fingerprint`
   the trial's `, seat origin ...; it serves pair, read, login, decide,
   watch, genesis, chat and wait-chat`, and after it `seat gate: chat:
   replies the scratch leads wrote to their outboxes; read only`. No `TRIAL`
   line. The fingerprint is compared with the one of step g on this line of
   this run, never an older one. The last line, on the screen and in the log,
   is always `ataniqd: exit status N`: 0 after `Ctrl+C`; a refusal ends the
   daemon at once with one line and its status: a refusal of the check of
   step g (10, with the same reason), the daemon lock held (12: the trial
   still runs), another daemon serving `$ROOT/runtime` (17), and `seat gate:
   cannot listen on ...` (18: something holds the gate's port). On any
   refusal go to [Recovery](#recovery). `tee -i` keeps the log's copy running
   through `Ctrl+C`, so the daemon's last lines are written. The seat dials
   the gate again by itself.
3. **OWNER ONLY (B): the console, and the seat's Leads pages.**

   ```sh
   $C/ataniq-console --daemon "$ROOT/runtime"
   ```

   `L` opens the leads view ([the console guide](terminal-console.md#lead-control-scratch-daemon-only)):
   the rows `standin-N` and `live-N` (`standin-N` alone without claude),
   `max_running` 1. That is the desk's view of the leads, not the seat's. On
   the phone and in a desktop browser, open the seat's web app (the same seat,
   on the authority host), log in again if asked, and open its Leads page:
   `daemon:standin-N` and `daemon:live-N`, both Off, and no STAND-IN banner.
   Both pages stay open for the rest of the sitting.

### The stand-in lead

4. **OWNER ONLY (B): define it; then (C) turn its chat on.** In the leads
   view, `d` on `standin-N`: `accepted (done)`. The daemon provisions the lead
   in the state folder (it runs git: the mirror from `$ROOT/upstream`, the
   folders, the clone, the instruction text with the reply rules). Then at
   the desk:

   ```sh
   $D chat set "standin-$SITTING" read --config "$CONFIG"
   $D chat list --config "$CONFIG" >>"$ROOT/evidence/chat-list.jsonl"
   ```

   The first prints the lead's row with `"chat":"read"`; the phone's Leads
   list shows `daemon:standin-N` with its state and the line "Chat was turned
   on. Replies written from now on show here."
5. **OWNER ONLY (B): start it and give it the token.** `s` on `standin-N`:
   `starting`, then `ready (holder-ready)`; the unit is
   `ataniq-lead-scratch-standin-N.service`, with the holder, pasta,
   bubblewrap and the stand-in. Then `m` and type

   ```text
   canary-token: proof-standin-0001
   ```

   and Enter, once. Read the answer and act on it alone:

   - `submitted`: the fence put it in. Do not paste; go on with step 6.
   - `NOT delivered: nothing was written, the lead's screen is not idle`:
     expected, since the stand-in draws a screen the fence does not know, and
     the epoch's message stays free. Only now give the token by paste, as in
     the first epoch of pass A: `w` attaches as the writer; press Enter on
     the session pane to give it the focus; look at the pane's input line:
     it must be empty and waiting, with nothing of the token in it; paste
     that one line with the terminal's paste (a bracketed paste, not typed
     keys) and press Enter once. The pane shows the stand-in's commit. Then
     `Ctrl+\` (the return key) leaves session focus, and `q` ends the attach
     screen; the leads view comes back.
   - Any other answer (unknown, a lost answer, a partial or uncertain
     delivery, or an input line that already holds text): do not paste and do
     not send again. Write in `$ROOT/evidence/notes.txt` `uncertain delivery:`
     and what was answered. Attach as a viewer (`v`) to look, `q` to leave,
     then stop the lead (`x`, `y`) and write down what was seen; the delivery
     is reconciled by what the pane and the evidence
     (`evidence/scratch-standin-N-lead.jsonl` of the state folder) show,
     before anything else is sent.
6. **Look, then stop it.** Within a few seconds (a reply is taken once it is 2
   seconds old, and the folder is read every 2 seconds) the phone's Leads page
   of `daemon:standin-N` shows `Stand-in: handled` and 7 hex digits, marked
   Lead, with no STAND-IN banner; the desktop's Leads page shows the same.
   Write down yes or no for each in `$ROOT/evidence/notes.txt`. Then, in the
   leads view (not attached), `x` and `y` on `standin-N`: `stopped`, with a
   proof. Its chat stays on and readable on the phone, shown stopped; it stays
   on until the restart of step 12.

### The live lead (only once the drop rule is in place)

The live lead is real Claude in the sandbox, whose network is open until the
drop rule is in place. The rule comes from a separate change (SB-1): you
install it as root at the start of sitting 1, as its own guide says. Without
it, the sitting has no live lead: go on with step 11.

7. **The drop rule is in place.** Its own check, as SB-1's guide says,
   passes. **OWNER ONLY (C):** run the network check again, with the rule in
   place:

   ```sh
   { sh "$ROOT/netcheck.sh"; echo "netcheck: exit status $?"; } 2>&1 | tee -a "$ROOT/evidence/netcheck.txt"
   ```

   It must end `netcheck: exit status 0` as at step e. Write in
   `$ROOT/evidence/notes.txt` the rule's check and what you accept of what
   stays open. A `FINDING`, or a rule whose check fails: no live lead; go on
   with step 11.
8. **Before it starts.** Note the login rows the provider's account page lists
   now (as pass B's step 5). **OWNER ONLY (B):** `d` on `live-N`. Then (C):

   ```sh
   $D chat set "live-$SITTING" read --config "$CONFIG"
   ```
9. **OWNER ONLY (B): start it, check the model, log in, give it the message,
   approve its one write.** `s` on `live-N` (`starting`, then `ready
   (holder-ready)`: the holder is up and claude was started; its login is not
   observed). Then (C):

   ```sh
   systemctl --user status -- "ataniq-lead-scratch-live-$SITTING.service"
   ```

   lists the unit's processes with their command lines: the claude line must
   read `/ataniq/agent/claude --model` and your model; otherwise stop the
   lead (`x`, `y`). Note the time: from now you keep the session to at most
   30 minutes and 10 turns, by your clock; nothing in the daemon or the unit
   bounds the time or the price.

   **Log in.** `w` attaches; Enter gives the pane the focus: claude asks for a
   login in its fresh home. Log in in the pane; the login's address and code
   are shown there and are copied nowhere else. Note the new login row on the
   provider's account page. Then `Ctrl+\` leaves the focus and `q` the attach
   screen: the leads view comes back.

   **The message.** There, `m` and type, as one line:

   ```text
   Please write one short reply to the owner now, the way your instructions about replies say, saying that you received this message. Do nothing else.
   ```

   and Enter, once. Act on the answer:

   - `submitted`: the fence put it in. claude will ask to approve the write of
     its reply. `w` attaches as the writer and Enter gives the pane the focus.
   - `NOT delivered: nothing was written`: `w` attaches, Enter gives the pane
     the focus; look at the input line: empty and waiting; paste the same line
     once and press Enter once. Stay attached: claude asks next.
   - Any other answer: no paste and no second send; write `uncertain
     delivery:` and the answer in `$ROOT/evidence/notes.txt`; stop the lead
     from the leads view (`x`, `y`) and reconcile as in step 5.

   **The one approval.** claude asks before it uses a tool. Approve only the
   one expected operation: writing this one reply as a file into its outbox
   (`/home/lead/.ataniq-replies`), as its instructions about replies say;
   never "do not ask again". On any other request (another file, a command, a
   network fetch, a change of settings) refuse it, leave the pane (`Ctrl+\`,
   `q`) and stop the lead (`x`, `y`). A project's approvals go through Cards
   on the phone, never through a pane. After the approval, `Ctrl+\` leaves the
   focus and `q` the attach screen, back to the leads view.
10. **Look, then stop it, and revoke the login.** The phone's and the
    desktop's Leads pages show `daemon:live-N` and its reply, marked Lead,
    with no STAND-IN banner. Then, in the leads view (not attached), `x` and
    `y` on `live-N`: `stopped`. Revoke the login made in step 9 on the
    provider's account page: the new row, last active during the sitting.
    Confirm that the row is gone. When no row can be told apart (pass B run 1
    saw rows named Unknown), write `cannot-identify:` and what you saw in
    `$ROOT/evidence/notes.txt`, a finding, and use the run kit's recovery
    (pass B: "log out of all devices" on a throwaway account). Do the same
    whenever the sitting ends after step 9: on success, on a failure, at the
    time limit or after an interruption.

### Off, restart and the way back

11. **Off (C).** With the live lead:

    ```sh
    $D chat set "live-$SITTING" off --config "$CONFIG"
    $D chat list --config "$CONFIG" >>"$ROOT/evidence/chat-list.jsonl"
    ```

    The phone's Leads page of `daemon:live-N` says "Chat is off for this
    lead. Turn it on at the desk.", and the directory shows it Off; the
    stopped `standin-N` keeps its chat on (`"chat":"read"` in the list).
    Without the live lead, turn `standin-N` off, check the same page, and
    turn it on again (`read`) for the restart.
12. **OWNER ONLY: a restart returns off.** In the console, check that every
    lead is `stopped`, and quit it. `Ctrl+C` in A: the daemon writes its
    shutdown lines and `ataniqd: exit status 0`. Start it again as in step
    2. Then (C):

    ```sh
    $D chat list --config "$CONFIG" >>"$ROOT/evidence/chat-list.jsonl"
    ```

    `standin-N` is `"chat":"off"` now, though it was on before the restart,
    and every lead is Off on the phone. `Ctrl+C` in A again (`exit status
    0`). `systemctl --user list-units --all -- "ataniq-lead-scratch-standin-$SITTING.service" "ataniq-lead-scratch-live-$SITTING.service"`
    lists no unit.
13. **The trial's files are the same bytes (C).** With both daemons stopped:

    ```sh
    (cd "$S/owner" && sha256sum -c -- "$ROOT/evidence/trial-before.sha256")
    sh "$P" --status
    ```

    The first prints `OK` for each of `gate/key.pem`, `gate/cert.pem`,
    `gate/seat-pins`, `keys.log`, `keys.head` and `scratch.json`: the trial's
    gate key, certificate, pins, paired keys and settings are byte-identical
    to step g. `--status` makes the same comparison after the daemon's check
    and ends `status: the trial's gate key, certificate, pins, key list and
    settings are the same bytes as recorded`. A `FAILED` line is written in
    `$ROOT/evidence/notes.txt` and looked at before the trial starts: a key
    list that grew by a counter record after a phone login is explained by
    that login (see [What was proved in process](#what-was-proved-in-process));
    any other change is a finding.
14. **OWNER ONLY (C): start the trial again**, by your own command, the one
    you started it with:

    ```sh
    systemctl --user start -- "$TRIAL_UNIT"
    journalctl --user --unit "$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1
    ```

    The first is for a unit file; for a transient unit (one you made with
    `systemd-run --user --unit ...`), run that `systemd-run` command again in
    its place. The fingerprint line (`-I`: of this new invocation only) names
    the fingerprint of step g. On the phone, the Leads pages show the trial's
    STAND-IN samples again (`daemon:alpha` first); open one, and the trial's
    sample card.

## Recovery

- **Anything cut short** (a refusal at step 2, a terminal closed, a power
  cut): stop every lead from the console (`x`, then `y`) when the daemon
  still runs, then `Ctrl+C` in A. Nothing of the trial was moved, so nothing
  needs putting back: run step 13 to compare the trial's files, then step 14
  to start the trial. A refusal at step 2 names its reason, the same as
  `sh "$P" --status` prints.
- **A reboot mid-sitting.** The leads' units do not survive it. A trial unit
  file that is enabled starts at boot on its own settings and serves as
  before; the proof's daemon then refuses with the lock (exit 12) until you
  stop the trial again (step 1). A transient trial does not come back after a
  reboot: start it with your own command (step 14) when the sitting is over.
- **A lead that is `unknown`** after a killed daemon settles by itself 40 s
  after its start, or is stopped with `x` (see
  [a lead left unknown](scratch-leads.md#a-lead-left-unknown-after-a-killed-daemon)).
  Stop every lead from the console (`x`, then `y`) before the daemon: a lead
  that is stopped is proven stopped.
- **OWNER ONLY, a last resort:** a lead's unit that the daemon can no longer
  reach (the daemon is gone and will not start) is
  `ataniq-lead-scratch-standin-N.service` or
  `ataniq-lead-scratch-live-N.service`; the sitting's number makes it this
  sitting's alone.
  `systemctl --user stop -- "ataniq-lead-scratch-live-$SITTING.service"`
  stops it.
- **`ataniqd chat set KEY read` answers exit 31** for a lead that is not
  defined yet (define it first) and for a full quarantine (each turn-on sets
  the earlier reply folder aside, in four names at most). A full quarantine
  ends the sitting: leave it as it is, stop the leads and the daemon, and go
  back to the trial (steps 13 and 14).

## What is recorded

Evidence of what was seen, and no conversation text. These files, and only
these, are the sitting's evidence:

- `$ROOT/evidence/netcheck.txt`: the network checks of steps e and 7, each
  ending with its exit status.
- `$ROOT/evidence/trial-before.sha256`: the SHA-256 digests of the trial's gate
  key, certificate, pins, key list and settings at step g (digests only; a
  digest of the gate's key does not give the key).
- `$ROOT/evidence/daemon.log`: the daemon's output, each run ending with its
  exit status line. Its chat lines hold counts and fixed words, never a
  reply's text; its provisioning lines name folders of the state folder.
- `$ROOT/evidence/chat-list.jsonl`: `chat list` lines, which hold each lead's
  switch, its flags and counts, never a reply's text.
- `evidence/scratch-standin-N-lead.jsonl` and `evidence/scratch-live-N-lead.jsonl`
  of the state folder: each lead's inject events (sent, result, refused),
  with the message id and never the body; and `lead-commands.jsonl` of the
  state folder, the receipts of the console's commands, which bind a digest
  of each command's params.
- `$ROOT/evidence/notes.txt`: your yes or no per observation (the daemon's
  start lines and fingerprint, `daemon:standin-N` and `daemon:live-N` in the
  Leads list, no STAND-IN banner, the reply shown on the phone and on the
  desktop, the off page's words, Off after the restart, the trial's files the
  same, the trial back), the drop rule's check and the network you accepted,
  the model seen in step 9, the times, any uncertain delivery and any
  `cannot-identify:`.

**Private, never evidence.** In the state folder, `leads/scratch-live-N-lead/`
and `leads/scratch-standin-N-lead/` hold the provider's config and login
(`config/claude`), the lead's home and outbox, and the holder's
`holder/final.json` and `holder/final.json.previous`, which keep the last
screen the holder showed even with `record_screens` off: it can hold a reply,
a prompt or a login code. `chat/` holds the stored replies while a chat is
on. `$ROOT/version-home.*` is the claude copy's home of step c. Keep them where
they are and share none of them; never archive the state folder, or the
proof's root, as evidence.

Not recorded: the replies' text, screenshots of a conversation, the message
to the live lead beyond what the console shows, and the login's address and
code.
