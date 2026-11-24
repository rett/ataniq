# The attended proof of real replies

This is the owner's runbook for one sitting that shows a reply written by a
lead in the real sandbox reaching the phone's and the desktop's Leads pages
through the real daemon, gate and seat: first the stand-in lead, then, if all
is well, one real Claude lead. It uses the daemon of
[Running real leads with the seat](scratch-leads.md#running-real-leads-with-the-seat)
on the phone trial's state folder, so that the gate's key, its seat pins and
the paired phones' keys stay the trial's and the phone stays paired.

Like everything it runs, it is provisional and unqualified: it passes no case
of a qualification, nothing in it is an approval, and no message goes from the
seat or the desk to a lead. It records what was seen, not a result.

**Who runs what.** Every step that starts a real process (a unit, the daemon,
the console, a sandbox, an agent, the claude program), changes a unit (stop,
mask, unmask, start) or defines a lead (the daemon runs git) is marked
**OWNER ONLY** and is run by the owner's hand, in his terminal. An agent may
run `qualification/scratch-seat/prepare.sh` with `--dry` or `--self-test`
only, and the tests. `--write`, `--status`, `--swap-in` and `--swap-out` are
the owner's: `--write` starts nothing and writes only in the proof's own
folder, and `--status` only reads, but they name his folders; the swaps
rename his settings.

**What is removed, and what is not.** No step deletes a file of the trial or
of the proof. Two things go by design: the chat service consumes each reply
file a lead writes (it takes the file into its store), and turning a lead's
chat off clears the replies it stored. A swap renames by making the new name
first (a hard link, which never replaces a name) and then removing the old
name of the same file; the file itself stays. `--swap-in` also writes one new
file in `owner/`, a copy of the proof's settings, under a dot name that it
then renames to `owner/scratch.json`.

## The owner's decisions (2026-11-24)

- **Reuse the trial's gate identity and pairing keys.** The scratch config's
  `state_root` is the trial's state folder, so the gate's key and pins
  (`owner/gate/`) and the phones' keys (`owner/keys.log`, `owner/keys.head`)
  are the ones the seat and the phone already use. The trial's user unit is
  stopped and masked first and never runs at the same time as the proof's
  daemon.
- **Stand-in first, then one real lead.** The stand-in lead in the real
  sandbox (the owner gives it a token, it commits and writes its one reply);
  then, in the same sitting if all is well, one real Claude lead with the
  cheapest model, the limits and a fresh login, as in the canary's pass B
  ([the run kit's guide](../qualification/canary/run/README.md)), with its
  chat read on.

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
TRIAL_UNIT='name-of-the-trial.service'              # the user unit that runs the trial daemon
CLAUDE_FILE='/path/of/the/tested/claude'            # claude 2.1.325, one regular file, as for pass B
CLAUDE_MODEL='name-of-the-cheapest-model'
export OWNER_STATE_DIR ROOT SEAT_GATE SITTING TRIAL_UNIT CLAUDE_FILE CLAUDE_MODEL
S=$OWNER_STATE_DIR CONFIG=$ROOT/scratch.json D=build/scratch-seat/ataniqd C=build/canary
P=qualification/scratch-seat/prepare.sh
```

`prepare.sh` refuses a value it cannot use before it prints anything: a
`TRIAL_UNIT` that is not exactly one `NAME.service` (no option, no pattern,
no space, no shell character, not a lead's unit), a sitting that is not 1 to
999, a gate that is not one IPv4 address of a host and a port (an IPv6
address is refused for the proof; the daemon would take one, the proof's
checks do not), and a `HOME` that cannot give the never-mounted paths
`~/.ssh`, `~/.claude` and `~/.codex`. The commands it prints name the unit as
`"$TRIAL_UNIT"`, which your shell fills in, never by its value.

The sitting's number names the two leads (`standin-N`, `live-N`), so that
each sitting has leads of its own: their folders, their roles in the registry
and their units are new. `--write` and `--swap-in` refuse a sitting whose
leads the state folder already has, and `--swap-in` one whose lead unit the
user manager knows (another scratch root may use the same key). A fresh
`ROOT` alone does not make a fresh Claude login: the provider's config of a
lead lives in the state folder (`leads/scratch-live-N-lead/config/claude`),
and a defined lead keeps its record; a new sitting number does.

## What was proved in process

`TestScratchSeatTakesThePhoneTrialsStateFolder` in
`cmd/ataniqd/scratch_seat_proof_linux_test.go` makes a state folder as the
owner made his (`ataniqd trial init`, `ataniqd seat-gate init`, one seat pin,
one paired key, the trial daemon run with one sample card), runs
`prepare.sh --write` into a private folder (the binaries it copies are stubs
it never runs), then takes the state folder through the steps below with the
real daemon of both tags, the real `ataniq-canary scratch-init` (built with
`scripts/go-tool.sh`), `prepare.sh --status`, `--swap-in` and `--swap-out`,
and a pinned seat on the loopback address. The unit manager is a fake one,
`systemctl` is a stub that answers `show`, and both leads are defined,
not started. `TestScratchSeatProofScriptWritesWhatTheDaemonReads` holds that
the config and the settings `prepare.sh` writes pass the daemon's strict
readers, and `TestScratchSeatProofGateCheckMatchesTheDaemon` that every gate
address the script takes is one the daemon takes, as the same address. What
they show:

- **The scratch daemon refuses the trial's state folder as it is.** With the
  trial's settings in `owner/scratch.json` (`"trial": true`), `run --scratch
  CONFIG --owner-channel --seat-gate IP:PORT` exits 10 with `ataniqd run:
  --owner-channel: the scratch settings are a phone trial's ("trial": true),
  which serves sample cards only: run it with --trial`. This comes before the
  root marker is looked at and before any lock: nothing is written, not even
  the runtime root.
- **`--swap-in` waits for the trial.** It refuses while the trial's unit is
  active or not masked and while anything listens on the gate's port (the
  test's trial daemon does).
- **Behind prepare, the daemon's own guards hold.** With the proof's settings
  put in place while the trial runs, the daemon refuses for the missing root
  marker (`no marker <state>/scratch-root.json`, exit 10, before any lock),
  and with the marker the state folder's daemon lock refuses it (exit 12), so
  the two can never serve the folder at once.
- **`scratch-init` takes the trial's private state folder as it is.** It prints
  `kept folder` and `made file .../scratch-root.json`, and when run again
  `kept file`. It writes nothing in `owner/`.
- **Only `owner/scratch.json` is swapped.** `--swap-in` sets the trial's
  settings file aside within `owner/` (to `scratch.json.trial`, the same file)
  and puts the proof's in its place. Nothing else of the trial is moved:
  `owner/gate/` (the gate's key, its certificate and `seat-pins`),
  `owner/keys.log` and `owner/keys.head`, `owner/journal.db` with the trial's
  sample card, and `owner/genesis/` stay where they are, and the non-trial
  service opens the trial's journal and its list of keys as they are.
- **The keys are safe.** The gate's start line names the trial's key
  fingerprint, the trial's pinned seat connects, and `owner.keys` lists the
  paired credential (its id). Both leads of the sitting are defined (each has
  its `provisioned.json`), a reply of the stand-in reaches the seat once its
  chat is on, and the live lead's chat turns on and off.
- **A restart returns off.** Restarted with the stopped stand-in's chat on,
  the daemon comes back with every lead's chat off.
- **The way back works.** `--swap-out` puts the same trial settings file back
  (the proof's go to `scratch.json.leads-N`); the trial daemon starts again on
  the same folder, with what the proof's daemon left in it, with the same key
  fingerprint and the same paired credential, and serves its STAND-IN sample
  rows (`daemon:alpha` first) and its one sample card. The gate's key and
  certificate, the pins, `keys.log`, `keys.head` and the trial's settings are
  the same bytes as at the start.

The byte comparison holds because no phone logs in during the test. In the
sitting, a login on the phone may advance a key's counter in the key journal
(`keys.log`, `keys.head`): those files may then differ afterwards, which is
expected, and nothing restores them.

`tests/scratch_seat_prepare_test.go` holds the script's refusals and swaps
in sample folders: a swap stopped half way is named by `--status` and
finished by `--swap-out`, and a layout of none of the three states is refused
by every mode with nothing moved.

What none of it shows: the real unit, its mask, holder, sandbox and stand-in,
the paste, claude and its login, the seat on the authority host, the phone,
the firewall and the network. The sitting shows those.

**The smallest safe change is the swap of one file**, not a change of the
daemon: the refusal of trial settings is a rule of the daemon, kept so that a
daemon with leads never serves the trial's samples. The swaps are prepare's
own modes, which the owner runs; `--write` never makes them.

### What the proof's daemon leaves in the trial's state folder

`scratch-root.json`; the two scratch roles of the sitting in the registry
(`registry/`, which the trial daemon made); `leads/`, `mirrors/`, `mailbox/`,
`pub/`, `roles/`, `scratch/`, `chat/`, `evidence/` and `lead-commands.jsonl`;
`owner/records/`; and, after the way back, `owner/scratch.json.leads-N` (and
a `.scratch.json.proof.*` copy if a swap-in was cut short). The trial daemon
starts with all of it there and serves as before (its registry, which
`ataniqd status` shows, also holds the scratch roles). They are left where
they are. The live lead's folder `leads/scratch-live-N-lead/` keeps the
provider's config and login (`config/claude`, which the sandbox binds at
`/home/lead/.claude`), its home and the holder's last screen
(`holder/final.json`): the login is revoked at step 11, and the files stay,
private (see [What is recorded](#what-is-recorded)).

## Before the sitting

None of these starts the daemon or a lead; c runs the claude copy, to read its
version, and e runs pasta, bubblewrap and curl. `prepare.sh` prints c to g
with your values.

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
   sh "$P" --dry --claude "$CLAUDE_FILE" --model "$CLAUDE_MODEL"
   sh "$P" --write --claude "$CLAUDE_FILE" --model "$CLAUDE_MODEL"
   ```

   Without `--claude` and `--model` the config has the stand-in lead only. It
   takes `OWNER_STATE_DIR`, `ROOT`, `SEAT_GATE`, `SITTING` and `TRIAL_UNIT`
   from the environment (or `--state`, `--root`, `--gate`, `--sitting` and
   `--unit`). `--dry` prints from the values alone and reads none of your
   folders, so what depends on them (pasta, bubblewrap) is printed as a
   condition. `--write` first checks, and refuses before it makes anything:
   a state folder that is a link or not at its physical path; an
   `owner/scratch.json` that is not the trial's private regular settings
   (`"trial": true`, mode 0600); an `owner/scratch.json.trial` (an earlier
   set-aside: run `--status` with that sitting's root); a gate key,
   certificate or pin file that is missing, empty, a link or (key and pins)
   readable by others; anything of this sitting's leads in the state folder;
   a missing binary, daemon, pasta, bubblewrap or `/usr/bin/git`; a root
   whose parent is missing or reached through a link, that is not new or
   empty or cannot be read or listed, or that lies physically in or around
   the state folder, the checkout, `/tmp`, `/var/tmp` or `$TMPDIR`; and a
   `HOME` that is not a real folder at its physical path. It makes only the
   root folder itself, never a parent. It writes, in `$ROOT` only:

   | File | What it is |
   | --- | --- |
   | `scratch.json` | The scratch config (mode 0600): `state_root` the trial's state folder, `runtime_root` `$ROOT/runtime` (the daemon makes it), `max_running` 1, and the leads below. |
   | `owner-scratch.json` | The owner channel's non-trial settings (mode 0600), which `--swap-in` puts in place: the stand-in door, the sample project `proof-project`, the scratch root `$ROOT/owner-channel` with the drop folder in it, and a sandbox template that is never written, so that no request can be verified and no suite sandbox starts. |
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
   to watch (step 10), and the memory floor and share are inputs of the
   canary run script, not of this daemon
   ([scratch leads](scratch-leads.md)). `record_screens` is off: no
   recording is made, but the holder still keeps the last screen it showed
   (see [What is recorded](#what-is-recorded)). Neither lead has a
   `profile_digest` pin: the daemon computes the digest at the define with
   the holder's own code (see
   [the profile digest and the pin](scratch-leads.md#the-profile-digest-and-the-pin)).
   `sandbox.never_mounted` holds the checkout, `~/.ssh`, `~/.claude`,
   `~/.codex` and `owner/` of the state folder.
- **c. OWNER ONLY: the claude copy's version** (it runs the claude program,
   with an empty environment and a new empty home, as pass B's prepare does):

   ```sh
   env -i HOME="$(mktemp -d)" "$ROOT/agent-install/claude" --version
   ```

   It must name 2.1.325, the version the recorded screens are of.
- **d. The firewall is your rule.** The gate's port admits TCP from the
   authority host only, as [the firewall](seat-gate.md#the-firewall) says.
- **e. OWNER ONLY: the lead's network, with the trial running** (it starts
   pasta, bubblewrap and curl, as the canary kit's `netcheck.sh` does; it
   uses no credential and starts no agent):

   ```sh
   sh "$ROOT/netcheck.sh" | tee -a "$ROOT/evidence/netcheck.txt"
   ```

   It connects to the gate's address from this host (the trial's gate must
   answer, or the next line proves nothing) and from the lead's network in
   the shape of the sandbox, where no connection may be made: `PASS ... does
   not reach the gate's port`. A `FINDING` there ends the sitting before it
   starts. It then lists this host's listeners on non-loopback addresses and
   says what stays open: the sandbox's network is open (pasta gives it this
   host's outbound access, to your LAN and the Internet), `provider_host` is
   a DNS check and not a rule, and the guard hook guards nothing. Before a
   live login, write in `$ROOT/evidence/notes.txt` that you accept this open
   network (and the listeners it named), or stop after the stand-in.
- **f. Read and record, nothing more:** the pins the gate will use, the
   trial's unit as it is, and the trial's fingerprint line of its current
   run:

   ```sh
   $D seat-gate pins --state-dir "$S"
   systemctl --user show -p Id -p ExecStart -p ActiveState -p SubState -p LoadState -p UnitFileState -p TriggeredBy -p InvocationID -- "$TRIAL_UNIT" >"$ROOT/evidence/trial-unit-before.txt"
   journalctl --user --unit "$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1
   ```

   `trial-unit-before.txt` is what step 15 restores: whether the unit was
   active, its unit-file state (enabled or not) and what triggers it. Look at
   `TriggeredBy`: a socket or timer named there can start the trial again,
   which the mask of step 1 prevents. The seat origin file
   (`ataniq/owner-local/seat-origin` of your configuration folder) is the
   trial's and stays as it is.
- **g. The settings, read:** `sh "$P" --status` prints `status: trial in
   place (not swapped)` and the next step.

## The sitting

Three terminals in the checkout, each with the values above: **A** for the
daemon, **B** for the console, **C** for the desk's commands (prepare, chat,
systemctl). The phone at hand. Keep the console window large (100 by 30 or
more) and do not resize it while attached.

### Switch from the trial to the proof

1. **OWNER ONLY (C): stop and mask the trial.**

   ```sh
   systemctl --user stop -- "$TRIAL_UNIT"
   systemctl --user mask --runtime -- "$TRIAL_UNIT"
   systemctl --user show -p ActiveState -p LoadState -- "$TRIAL_UNIT"
   ```

   The last says `ActiveState=inactive` and `LoadState=masked`. The mask is
   for this boot only (`--runtime`): no socket, timer or other unit can start
   the trial while its settings are swapped. The phone now finds no desk. If
   `LoadState` is not `masked`, the runtime mask did not take (a unit file in
   your configuration folder can outrank the runtime folder): run
   `systemctl --user unmask --runtime -- "$TRIAL_UNIT"`, start the trial
   again (step 15) and stop the sitting here; `--swap-in` refuses without
   the mask.
2. **The root marker (C):**

   ```sh
   $C/ataniq-canary scratch-init --config "$CONFIG"
   ```

   It prints `kept folder` and `made file .../scratch-root.json` (`kept
   file` when run again). It refuses a state folder that is a default one or
   carries the canary's marker.
3. **Swap the settings in (C):**

   ```sh
   sh "$P" --swap-in
   ```

   It refuses, with nothing moved, while the trial's unit is not inactive
   and masked, while anything listens on the gate's port, while the user
   manager knows a unit of this sitting's leads, and when the settings are
   not the trial's alone. Then it records the trial's settings file (its
   identity and digest) in `$ROOT/swap-record`, renames it within `owner/`
   to `scratch.json.trial`, puts a copy of `$ROOT/owner-scratch.json` at
   `owner/scratch.json`, checks each step, and prints `status: swapped in`.
   Nothing else in `owner/` moves.
4. **OWNER ONLY (A): the daemon.**

   ```sh
   sh -c 'trap : INT; "$@" 2>&1; echo "ataniqd: exit status $?"' sh $D run --scratch "$CONFIG" --owner-channel --seat-gate "$SEAT_GATE" | tee -ai "$ROOT/evidence/daemon.log"
   ```

   Look for the owner channel's banner (`owner channel: STAND-IN door,
   scratch state: no real approval`: the door of cards, not the leads), the
   gate's line `seat gate: listening on` your address `, key fingerprint`
   the trial's `, seat origin ...; it serves pair, read, login, decide,
   watch, genesis, chat and wait-chat`, and after it `seat gate: chat:
   replies the scratch leads wrote to their outboxes; read only`. No `TRIAL`
   line. The fingerprint is compared with the one of step f on this line of
   this run, never an older one. The last line, on the screen and in the log,
   is always `ataniqd: exit status N`: 0 after `Ctrl+C`; a refusal ends the
   daemon at once with one line and its status: the trial's settings named
   (10: step 3 is not done), `no marker .../scratch-root.json` (10: step 2),
   the daemon lock held (12: the trial still runs), another daemon serving
   `$ROOT/runtime` (17), and `seat gate: cannot listen on ...` (18:
   something holds the gate's port). On any refusal go to
   [Recovery](#recovery). `tee -i` keeps the log's copy running through
   `Ctrl+C`, so the daemon's last lines are written. The seat dials the gate
   again by itself.
5. **OWNER ONLY (B): the console.**

   ```sh
   $C/ataniq-console --daemon "$ROOT/runtime"
   ```

   `L` opens the leads view ([the console guide](terminal-console.md#lead-control-scratch-daemon-only)):
   the rows `standin-N` and `live-N` (`standin-N` alone when prepare was given
   no claude), `max_running` 1. On the phone, log in again if asked, and open
   Leads: `daemon:standin-N` and `daemon:live-N`, both Off, and no STAND-IN
   banner.

### The stand-in lead

6. **OWNER ONLY (B): define it; then (C) turn its chat on.** In the leads
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
7. **OWNER ONLY (B): start it and give it the token.** `s` on `standin-N`:
   `starting`, then `ready (holder-ready)`; the unit is
   `ataniq-lead-scratch-standin-N.service`, with the holder, pasta,
   bubblewrap and the stand-in. Then `m` and type

   ```text
   canary-token: proof-standin-0001
   ```

   and Enter, once. Read the answer and act on it alone:

   - `submitted`: the fence put it in. Do not paste.
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
     not send again. Attach as a viewer (`v`) to look, then stop the lead
     (`x`, `y`) and write down what was seen; the delivery is reconciled by
     what the pane and the evidence (`evidence/scratch-standin-N-lead.jsonl`
     of the state folder) show, before anything else is sent.
8. **Look, then stop it.** Within a few seconds (a reply is taken once it is 2
   seconds old, and the folder is read every 2 seconds) the phone's Leads page
   of `daemon:standin-N` shows `Stand-in: handled` and 7 hex digits, marked
   Lead, with no STAND-IN banner; the desktop's Leads page shows the same.
   Write down yes or no for each in `$ROOT/evidence/notes.txt`. Then `x` and
   `y` on `standin-N`: `stopped`, with a proof. Its chat stays on and readable
   on the phone, shown stopped; it stays on until the restart of step 13.

### The live lead (same sitting, only if all was well)

Only after step e's `PASS` for the gate and your written acceptance of the
open network.

9. **Before it starts.** Note the login rows the provider's account page lists
   now (as pass B's step 5). **OWNER ONLY (B):** `d` on `live-N`. Then (C):

   ```sh
   $D chat set "live-$SITTING" read --config "$CONFIG"
   ```
10. **OWNER ONLY (B): start it, check the model, log in, give it the
    message.** `s` on `live-N` (`starting`, then `ready (holder-ready)`: the
    holder is up and claude was started; its login is not observed). Then
    (C):

    ```sh
    systemctl --user status -- "ataniq-lead-scratch-live-$SITTING.service"
    ```

    lists the unit's processes with their command lines: the claude line must
    read `/ataniq/agent/claude --model` and your model; otherwise stop the
    lead (`x`, `y`). Note the time: from now you keep the session to at most
    30 minutes and 10 turns, by your clock; nothing in the daemon or the unit
    bounds the time or the price. `w` attaches; Enter gives the pane the
    focus: claude asks for a login in its fresh home. Log in in the pane; the
    login's address and code are shown there and are copied nowhere else.
    Note the new login row on the provider's account page.

    claude asks before it uses a tool. Approve only the one expected
    operation: writing this one reply as a file into its outbox
    (`/home/lead/.ataniq-replies`), as its instructions about replies say;
    never "do not ask again". Stop the lead (`x`, `y`) on any other request:
    another file, a command, a network fetch, a change of settings. A
    project's approvals go through Cards on the phone, never through a
    pane. `Ctrl+\` then `q` returns to the leads view. There, `m` and type,
    as one line:

    ```text
    Please write one short reply to the owner now, the way your instructions about replies say, saying that you received this message. Do nothing else.
    ```

    Act on the answer as in step 7: `submitted` means the fence put it in;
    only after `NOT delivered: nothing was written` attach with `w`, look at
    the empty input line, paste the same line once and press Enter once; any
    other answer: no paste, stop and reconcile.
11. **Look, then stop it.** The phone's and the desktop's Leads pages show
    `daemon:live-N` and its reply, marked Lead, with no STAND-IN banner. Then
    `x` and `y` on `live-N`: `stopped`. Revoke the login made in step 10 on
    the provider's account page (the new row, last active during the
    sitting), and confirm that the row is gone. Do the same whenever the
    sitting ends after step 10: on success, on a failure, at the time limit
    or after an interruption.

### Off, restart and the way back

12. **Off (C).** With the live lead:

    ```sh
    $D chat set "live-$SITTING" off --config "$CONFIG"
    $D chat list --config "$CONFIG" >>"$ROOT/evidence/chat-list.jsonl"
    ```

    The phone's Leads page of `daemon:live-N` says "Chat is off for this
    lead. Turn it on at the desk.", and the directory shows it Off; the
    stopped `standin-N` keeps its chat on (`"chat":"read"` in the list).
    Without the live lead, turn `standin-N` off, check the same page, and
    turn it on again (`read`) for the restart.
13. **OWNER ONLY: a restart returns off.** In the console, check that every
    lead is `stopped`, and quit it. `Ctrl+C` in A: the daemon writes its
    shutdown lines and `ataniqd: exit status 0`. Start it again as in step
    4. Then (C):

    ```sh
    $D chat list --config "$CONFIG" >>"$ROOT/evidence/chat-list.jsonl"
    ```

    `standin-N` is `"chat":"off"` now, though it was on before the restart,
    and every lead is Off on the phone. `Ctrl+C` in A again (`exit status
    0`). `systemctl --user list-units --all -- "ataniq-lead-scratch-standin-$SITTING.service" "ataniq-lead-scratch-live-$SITTING.service"`
    lists no unit.
14. **Swap the settings back (C):**

    ```sh
    sh "$P" --swap-out
    ```

    It refuses while the trial's unit is not inactive or anything listens on
    the gate's port (the proof's daemon still runs). It renames the proof's
    settings within `owner/` to `scratch.json.leads-N` and the recorded trial
    settings file back to `owner/scratch.json`, checks that it is the same
    file with the same digest, and prints `status: trial in place (the way
    back is done)`.
15. **OWNER ONLY (C): unmask and start the trial again.**

    ```sh
    systemctl --user unmask --runtime -- "$TRIAL_UNIT"
    systemctl --user start -- "$TRIAL_UNIT"
    systemctl --user show -p ActiveState -p InvocationID -p UnitFileState -- "$TRIAL_UNIT" >"$ROOT/evidence/trial-unit-after.txt"
    journalctl --user --unit "$TRIAL_UNIT" -I | grep 'key fingerprint' | tail -n 1
    ```

    Start it only when `trial-unit-before.txt` says `ActiveState=active`.
    `trial-unit-after.txt` must say `active`, an `InvocationID` other than the
    one recorded before (a new run), and the `UnitFileState` recorded before.
    The fingerprint line (`-I`: of this new run only) names the fingerprint
    of step f. On the phone, the Leads pages show the trial's STAND-IN samples
    again (`daemon:alpha` first); open one, and the trial's sample card.
16. **The settings, read:** `sh "$P" --status` prints `status: trial in place
    (the way back is done)`. The trial's settings, gate key, pins and paired
    phones are the same files (the key journal may have advanced with a
    login).

## Recovery

- **Anything cut short after step 1** (a refusal at step 3 or 4, a terminal
  closed, a power cut): keep the trial's unit masked and both daemons
  stopped, and run `sh "$P" --status`. It prints one of `trial in place`,
  `swapped in` or `interrupted or ambiguous`, the identity and digest of
  `owner/scratch.json`, `owner/scratch.json.trial` and
  `$ROOT/owner-scratch.json`, the swap record, and exactly one next step.
  For a swap cut short (a name missing, or one file with two names after a
  rename stopped between its link and its unlink) the next step is
  `--swap-out`, which puts the recorded trial settings back. Any other
  layout (a link at a name, a dangling one included; settings that are
  neither the trial's nor this proof's; a set-aside this root did not
  record) is refused by every mode with nothing moved, and `--status` names
  the step that recovers it; usually that is `--status` with the root of
  the sitting that swapped. Then step 15.
- **A reboot mid-sitting** removes the runtime mask. If the trial's unit is
  enabled, it starts at boot and finds the proof's settings, or none, in
  `owner/scratch.json`: it refuses them and ends (exit 10; it fails closed and
  serves nothing). The leads' units do not survive a reboot. Stop the trial's
  unit again (`systemctl --user stop -- "$TRIAL_UNIT"`), mask it as in step
  1, run `--status`, then `--swap-out`, then step 15.
- **A lead that is `unknown`** after a killed daemon settles by itself 40 s
  after its start, or is stopped with `x` (see
  [a lead left unknown](scratch-leads.md#a-lead-left-unknown-after-a-killed-daemon)).
  Stop every lead from the console (`x`, then `y`) before the daemon: a lead
  that is stopped is proven stopped.
- **OWNER ONLY, a last resort:** a lead's unit that the daemon can no longer
  reach (the daemon is gone and will not start) is
  `ataniq-lead-scratch-standin-N.service` or
  `ataniq-lead-scratch-live-N.service`; the sitting's number makes it this
  sitting's alone, and `--swap-in` checked that no other unit had the name.
  `systemctl --user stop -- "ataniq-lead-scratch-live-$SITTING.service"`
  stops it.
- **`ataniqd chat set KEY read` answers exit 31** for a lead that is not
  defined yet (define it first) and for a full quarantine (each turn-on sets
  the earlier reply folder aside, in four names at most). A full quarantine
  ends the sitting: leave it as it is, stop the leads and the daemon, and go
  back to the trial (steps 14 to 16).

## What is recorded

Evidence of what was seen, and no conversation text. These files, and only
these, are the sitting's evidence:

- `$ROOT/evidence/netcheck.txt`: the network check of step e.
- `$ROOT/evidence/trial-unit-before.txt` and `trial-unit-after.txt`: the
  trial's unit before the sitting and after it (they name your unit and its
  command line: keep them with your own files).
- `$ROOT/evidence/daemon.log`: the daemon's output, each run ending with its
  exit status line. Its chat lines hold counts and fixed words, never a
  reply's text; its provisioning lines name folders of the state folder.
- `$ROOT/evidence/chat-list.jsonl`: `chat list` lines, which hold each lead's
  switch, its flags and counts, never a reply's text.
- `$ROOT/swap-record`: the trial settings file's identity and digests.
- `evidence/scratch-standin-N-lead.jsonl` and `evidence/scratch-live-N-lead.jsonl`
  of the state folder: each lead's inject events (sent, result, refused),
  with the message id and never the body; and `lead-commands.jsonl` of the
  state folder, the receipts of the console's commands, which bind a digest
  of each command's params.
- `$ROOT/evidence/notes.txt`: your yes or no per observation (the daemon's
  start lines and fingerprint, `daemon:standin-N` and `daemon:live-N` in the
  Leads list, no STAND-IN banner, the reply shown on the phone and on the
  desktop, the off page's words, Off after the restart, the trial back), the
  network you accepted, the model seen in step 10 and the times.

**Private, never evidence.** In the state folder, `leads/scratch-live-N-lead/`
and `leads/scratch-standin-N-lead/` hold the provider's config and login
(`config/claude`), the lead's home and outbox, and the holder's
`holder/final.json` and `holder/final.json.previous`, which keep the last
screen the holder showed even with `record_screens` off: it can hold a reply,
a prompt or a login code. `chat/` holds the stored replies while a chat is
on. Keep them where they are and share none of them; never archive the state
folder, or the proof's root, as evidence.

Not recorded: the replies' text, screenshots of a conversation, the message
to the live lead beyond what the console shows, and the login's address and
code.
