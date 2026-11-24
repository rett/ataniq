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
the console, a sandbox, an agent, the claude program) is marked **OWNER ONLY**
and is run by the owner's hand, in his terminal. An agent may run
`qualification/scratch-seat/prepare.sh` with `--dry` or `--self-test` only, and
the tests. `prepare.sh --write` starts nothing and writes only in the proof's
own folder, but it is the owner's step too, because it names his folders. No
step deletes anything: a file is moved only to a name that is free.

## The owner's decisions (2026-11-24)

- **Reuse the trial's gate identity and pairing keys.** The scratch config's
  `state_root` is the trial's state folder, so the gate's key and pins
  (`owner/gate/`) and the phones' keys (`owner/keys.log`) are the ones the
  seat and the phone already use. The trial's user unit is stopped first and
  never runs at the same time as the proof's daemon.
- **Stand-in first, then one real lead.** The stand-in lead in the real
  sandbox (the owner gives it a token, it commits and writes its one reply);
  then, in the same sitting if all is well, one real Claude lead with the
  cheapest model, the limits and the fresh login of the canary's pass B
  ([the run kit's guide](../qualification/canary/run/README.md)), with its
  chat read on.

## Placeholders

The owner supplies these in his shell; no file of the repository holds them.

| Placeholder | What it is |
| --- | --- |
| `<TRIAL_STATE_DIR>` | The trial daemon's `--state-dir`. |
| `<TRIAL_RUNTIME_DIR>` | The trial daemon's `--runtime-dir`. The proof does not use it. |
| `<TRIAL_UNIT>` | The user unit that runs the trial daemon. |
| `<IP:PORT>` | The trial daemon's `--seat-gate`: the gate's address, which the seat dials. |
| `<ROOT>` | A new or empty folder for the proof's own files, outside `/tmp`, outside the checkout and outside the state folder, for example in your cache folder. |
| `<CLAUDE_FILE>` | The tested claude (2.1.325), one regular file, as for pass B. |
| `<MODEL>` | The cheapest model's name. |

Set them once, in the checkout, and the commands below can be pasted as they
are:

```sh
export OWNER_STATE_DIR=<TRIAL_STATE_DIR> ROOT=<ROOT> SEAT_GATE=<IP:PORT> TRIAL_UNIT=<TRIAL_UNIT>
export CLAUDE_FILE=<CLAUDE_FILE> CLAUDE_MODEL=<MODEL>
S=$OWNER_STATE_DIR CONFIG=$ROOT/scratch.json D=build/scratch-seat/ataniqd C=build/canary
```

## What was proved in process

`TestScratchSeatTakesThePhoneTrialsStateFolder` in
`cmd/ataniqd/scratch_seat_proof_linux_test.go` makes a state folder as the
owner made his (`ataniqd trial init`, `ataniqd seat-gate init`, one seat pin,
one paired key, the trial daemon run with one sample card), then takes it
through the steps below with the real daemon of both tags, the real
`ataniq-canary scratch-init` and a pinned seat on the loopback address. The
unit manager is a fake one and the leads are defined, not started.
`TestScratchSeatProofScriptWritesWhatTheDaemonReads` holds that the config and
the settings `prepare.sh` writes pass the daemon's strict readers. What they
show:

- **The scratch daemon refuses the trial's state folder as it is.** With the
  trial's settings in `owner/scratch.json` (`"trial": true`), `run --scratch
  CONFIG --owner-channel --seat-gate IP:PORT` exits 10 with `ataniqd run:
  --owner-channel: the scratch settings are a phone trial's ("trial": true),
  which serves sample cards only: run it with --trial`. This comes before the
  root marker is looked at and before any lock: nothing is written, not even
  the runtime root.
- **Only `owner/scratch.json` must move.** Moved aside within `owner/` (to
  `scratch.json.trial`) and replaced by non-trial settings, the daemon starts.
  Nothing else of the trial is moved: `owner/gate/` (the gate's key, its
  certificate and `seat-pins`), `owner/keys.log` and `owner/keys.head`,
  `owner/journal.db` with the trial's sample cards, and `owner/genesis/` stay
  where they are, and the non-trial service opens the trial's journal and its
  list of keys as they are.
- **The keys are safe.** The gate's start line names the trial's key
  fingerprint, the trial's pinned seat connects, and `owner.keys` lists the
  paired key. At the end of the whole cycle, out and back, the gate's key and
  certificate, the pins, `keys.log` and the trial's settings are the same
  bytes as at the start.
- **The new `owner/scratch.json` is not all it needs.** It also needs the root
  marker `scratch-root.json`, written by `ataniq-canary scratch-init` (without
  it the daemon exits 10 at the config step with `no marker
  <TRIAL_STATE_DIR>/scratch-root.json`, before any lock), the scratch config, a
  drop folder that exists (prepare makes both), and the trial daemon stopped:
  with the settings swapped and the marker written while the trial still runs,
  the state folder's daemon lock refuses the proof's daemon (exit 12), so the
  two can never serve the folder at once.
- **`scratch-init` takes the trial's private state folder as it is.** It prints
  `kept folder <TRIAL_STATE_DIR>` and `made file
  <TRIAL_STATE_DIR>/scratch-root.json`, and when run again `kept file`. It
  writes nothing in `owner/`.
- **The gate's folder is never bound into a sandbox.** The config prepare
  writes lists `<TRIAL_STATE_DIR>/owner` as a never-mounted path, and the
  define's check of the sandbox profile accepts it: no bind source of a lead
  is, lies in or lies above it.
- **The way back works.** With the proof's settings moved aside (to
  `scratch.json.leads`) and the trial's moved back, the trial daemon starts
  again on the same folder, with what the proof's daemon left in it, with the
  same key fingerprint and the same paired key, and serves its STAND-IN
  samples.

What it does not show: the real unit, holder, sandbox and stand-in, the paste,
claude and its login, the seat on the authority host, the phone and the
firewall. The sitting shows those.

**The smallest safe change is the owner's two moves**, not a change of the
daemon: the refusal of trial settings is a rule of the daemon, kept so that a
daemon with leads never serves the trial's samples. The moves are the owner's
commands (steps 3, 4, 15 and 16); prepare never makes them, and nothing is
deleted.

### What the proof's daemon leaves in the trial's state folder

`scratch-root.json`; the two scratch roles in the registry (`registry/`, which
the trial daemon made); `leads/`, `mirrors/`, `mailbox/`, `pub/`, `roles/`,
`scratch/`, `chat/`, `evidence/` and `lead-commands.jsonl`; `owner/records/`;
and, after the way back, `owner/scratch.json.leads`. The trial daemon starts
with all of it there and serves as before (its registry, which `ataniqd status`
shows, also holds the two scratch roles). They are left where they are. The
live lead's
home (`leads/scratch-live-lead/home`) keeps the agent's login data after the
sitting: the login is revoked at step 12, and moving that folder elsewhere is
the owner's choice.

## Before the sitting

None of these starts the daemon, a unit or a sandbox; only c runs a program
of the proof, the claude copy, to read its version. `prepare.sh` prints c, d
and e with your values.

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
   sh qualification/scratch-seat/prepare.sh --dry --claude "$CLAUDE_FILE" --model "$CLAUDE_MODEL"
   sh qualification/scratch-seat/prepare.sh --write --claude "$CLAUDE_FILE" --model "$CLAUDE_MODEL"
   ```

   Without `--claude` and `--model` the config has the stand-in lead only. It
   takes `OWNER_STATE_DIR`, `ROOT` and `SEAT_GATE` from the environment (or
   `--state`, `--root` and `--gate`). It refuses a state folder without the
   gate's key and pins, one whose trial settings are already set aside
   (`owner/scratch.json.trial`), a root that is not new or empty or that lies
   in `/tmp`, the checkout or the state folder, and a gate that is not one IP
   address and a port. It writes, in `<ROOT>` only:

   | File | What it is |
   | --- | --- |
   | `scratch.json` | The scratch config (mode 0600): `state_root` `<TRIAL_STATE_DIR>`, `runtime_root` `<ROOT>/runtime` (the daemon makes it), `max_running` 1, and the leads below. |
   | `owner-scratch.json` | The owner channel's non-trial settings (mode 0600), for step 4: the stand-in door, the sample project `proof-project`, the scratch root `<ROOT>/owner-channel` with the drop folder in it, and a sandbox template that is never written, so that no request can be verified and no suite sandbox starts. |
   | `conf/` | The managed settings with the marker `guard-hook`, a no-op guard hook, and the replay screens of the checkout. |
   | `agent-install/` | A copy of the stand-in and of `<CLAUDE_FILE>` (`claude`, mode 0700). The copy is not run. |
   | `bin/ataniq-probe` | A copy of the probe (the checkout is never mounted), and a copy of pasta when `/usr/bin/pasta` is a link. |
   | `upstream/` | A throwaway repository, one commit on `develop`, that both leads' mirrors are made from. |
   | `evidence/`, `owner-channel/drop/` | Empty folders. |

   The two leads of `scratch.json`:

   | Key | `agent` | `inject` | `limits` |
   | --- | --- | --- | --- |
   | `standin` | `"kind": "stand-in"`, runtime `claude` 2.1.325, `footer_mode` `manual`, argv `["/ataniq/agent/ataniq-canary-standin"]`, no env | `true` | 4 GiB, no swap, 512 tasks, 1 GiB floor, 50 percent |
   | `live` | `"kind": "live"`, runtime `claude` 2.1.325, `footer_mode` `manual`, argv `["/ataniq/agent/claude", "--model", "<MODEL>"]`, env `DISABLE_AUTOUPDATER=1` and `CLAUDE_CODE_NO_FLICKER=1` | `true` | the same |

   `max_running` is 1: one sandbox at a time, so the stand-in is stopped before
   the live lead starts (a start while it runs is refused `max-running`).
   `record_screens` is off. Neither lead has a `profile_digest` pin: the daemon
   computes the digest at the define with the holder's own code (see
   [the profile digest and the pin](scratch-leads.md#the-profile-digest-and-the-pin)).
   To pin it for a later sitting, read `"profile_digest"` from
   `<TRIAL_STATE_DIR>/scratch/scratch-<key>-lead/provisioned.json`, add it to
   that lead's entry of `scratch.json` (keep the mode 0600) and start the
   daemon again; a define whose digest differs (`digest-pin`), and a daemon
   start with a lead whose record is not the pin, are then refused. The pin is
   not an independent check: it is the daemon's own computation, taken once.
   `sandbox.never_mounted` holds the checkout, `~/.ssh`, `~/.claude`,
   `~/.codex` and `<TRIAL_STATE_DIR>/owner`.
- **c. OWNER ONLY: the claude copy's version** (it runs the claude program, with
   an empty environment and a new empty home, as pass B's prepare does):

   ```sh
   env -i HOME="$(mktemp -d)" "$ROOT/agent-install/claude" --version
   ```

   It must name 2.1.325, the version the recorded screens are of.
- **d. The firewall is the owner's rule.** The gate's port of `<IP:PORT>` admits
   TCP from the authority host only, as [the firewall](seat-gate.md#the-firewall)
   says. A lead's sandbox can still reach the host's own addresses: the pinned
   keys refuse its calls, not its connections.
- **e. Read, nothing more:** the pins the gate will use, and the trial's
   fingerprint to compare at step 5 of the sitting:

   ```sh
   $D seat-gate pins --state-dir "$S"
   journalctl --user --unit "$TRIAL_UNIT" | grep 'key fingerprint' | tail -n 1
   ```

   The seat origin file (`ataniq/owner-local/seat-origin` of your configuration
   folder) is the trial's and stays as it is.

## The sitting

Two terminals in the checkout, A for the daemon and B for the console, both
with the variables above, and the phone at hand. Keep the console window large
(100 by 30 or more) and do not resize it while attached.

### Switch from the trial to the proof

1. **OWNER ONLY: stop the trial.**

   ```sh
   systemctl --user stop "$TRIAL_UNIT"
   systemctl --user is-active "$TRIAL_UNIT"
   ```

   The second says `inactive`. The phone now finds no desk. If this is
   forgotten, step 5 refuses with exit 12 (the state folder's daemon lock).
2. **The root marker:**

   ```sh
   $C/ataniq-canary scratch-init --config "$CONFIG"
   ```

   It prints `kept folder <TRIAL_STATE_DIR>` and `made file
   <TRIAL_STATE_DIR>/scratch-root.json` (`kept file` when run again). It
   refuses a state folder that is a default one or carries the canary's marker.
3. **Set the trial's settings aside**, in `owner/`, to a free name:

   ```sh
   test ! -e "$S/owner/scratch.json.trial" && mv "$S/owner/scratch.json" "$S/owner/scratch.json.trial"
   ```

   Nothing else in `owner/` moves.
4. **Put the proof's settings in their place:**

   ```sh
   test ! -e "$S/owner/scratch.json" && cp "$ROOT/owner-scratch.json" "$S/owner/scratch.json" && chmod 0600 "$S/owner/scratch.json"
   ```
5. **OWNER ONLY (terminal A): the daemon.**

   ```sh
   $D run --scratch "$CONFIG" --owner-channel --seat-gate "$SEAT_GATE" 2>&1 | tee -ai "$ROOT/evidence/daemon.log"
   ```

   Look for the owner channel's banner (`owner channel: STAND-IN door, scratch
   state: no real approval`: the door of cards, not the leads), the gate's line
   `seat gate: listening on <IP:PORT>, key fingerprint <the trial's>, seat
   origin ...; it serves pair, read, login, decide, watch, genesis, chat and
   wait-chat` and after it `seat gate: chat: replies the scratch leads wrote to
   their outboxes; read only`. No `TRIAL` line. `tee -i` keeps the log's copy
   running through `Ctrl+C`, so the daemon's last lines are written and it
   ends cleanly. A refusal ends the daemon at once with one line (the pipe
   hides its exit status): the trial's settings named (exit 10: step 3 or 4 is
   not done), `no marker .../scratch-root.json` (exit 10: step 2 is not done),
   the daemon lock held (exit 12: the trial still runs, step 1), another daemon
   serving `<ROOT>/runtime` (exit 17). The seat dials the gate again by
   itself.
6. **OWNER ONLY (terminal B): the console.**

   ```sh
   $C/ataniq-console --daemon "$ROOT/runtime"
   ```

   `L` opens the leads view ([the console guide](terminal-console.md#lead-control-scratch-daemon-only)):
   the rows `standin` and `live` (`standin` alone when prepare was given no
   claude), `max_running` 1. On the phone, log in again if asked, and open
   Leads: `daemon:standin` and `daemon:live`, both Off, and no STAND-IN
   banner.

### The stand-in lead

7. **Define it and turn its chat on.** In the leads view, `d` on `standin`:
   `accepted (done)`. The daemon provisions the lead in the state folder (the
   mirror from `<ROOT>/upstream`, the folders, the clone, the instruction text
   with the reply rules). Then at the desk:

   ```sh
   $D chat set standin read --config "$CONFIG"
   $D chat list --config "$CONFIG" >> "$ROOT/evidence/chat-list.jsonl"
   ```

   The first prints the lead's row with `"chat":"read"`; the phone's Leads list
   shows `daemon:standin` with its state and the line "Chat was turned on.
   Replies written from now on show here."
8. **OWNER ONLY: start it and give it the token.** `s` on `standin`: `starting`,
   then `ready (holder-ready)`; the unit is `ataniq-lead-scratch-standin.service`,
   with the holder, pasta, bubblewrap and the stand-in. Then `m` and type

   ```text
   canary-token: proof-standin-0001
   ```

   and Enter. The stand-in draws a screen the fence does not know, so the
   answer is expected to be `NOT delivered: nothing was written, the lead's
   screen is not idle`, and the epoch's message stays free: the token is
   given by paste instead, as in the first epoch of pass A. `w` attaches as the
   writer; give the session pane the focus, paste that one line with the
   terminal's paste (a bracketed paste, not typed keys) and press Enter once.
   The pane shows the stand-in's commit. Leave with the return key
   (`Ctrl+\`); the view comes back. (If the answer is `submitted` instead, the
   stand-in had the token already: do not paste.)
9. **Look, then stop it.** Within a few seconds (a reply is taken once it is 2
   seconds old, and the folder is read every 2 seconds) the phone's Leads page
   of `daemon:standin` shows `Stand-in: handled <7 hex digits>.`, marked Lead,
   with no STAND-IN banner; the desktop's Leads page shows the same. Write down
   yes or no for each in `<ROOT>/evidence/notes.txt`. Then `x` and `y` on
   `standin`: `stopped`, with a proof. Its chat stays readable on the phone,
   shown stopped.

### The live lead (same sitting, only if all was well)

10. **Before it starts.** Note the login rows the provider's account page lists
    now (as pass B's step 5). Define it and turn its chat on:
    `d` on `live`, then

    ```sh
    $D chat set live read --config "$CONFIG"
    ```
11. **OWNER ONLY: start it, log in, give it the message.** `s` on `live`
    (`starting`, then `ready (holder-ready)`: the holder is up and claude was
    started; its login is not observed). `w` attaches: claude asks for a
    login in its fresh home. Log in in the pane; the login's address and code
    are shown there and are copied nowhere else. Answer claude's questions with
    a plain Yes, never "do not ask again". Note the new login row on the
    provider's account page. Back in the view, `m` and type, as one line:

    ```text
    Please write one short reply to the owner now, the way your instructions about replies say, saying that you received this message. Do nothing else.
    ```

    `submitted` means the fence put it in. When it is NOT delivered (the screen
    was not idle), attach with `w`, paste the same line with the terminal's
    paste and press Enter once. claude writes the reply with a tool: approve
    that tool use in the pane with a plain Yes.
12. **Look, then stop it.** The phone's and the desktop's Leads pages show
    `daemon:live` and its reply, marked Lead, with no STAND-IN banner. Keep the
    lead to at most 30 minutes or 10 turns. Then `x` and `y` on `live`:
    `stopped`. Revoke the login made in step 11 on the provider's account page
    (the new row, last active during the sitting), and confirm the row is gone.

### Off, restart and the way back

13. **Off.** At the desk:

    ```sh
    $D chat set standin off --config "$CONFIG"
    $D chat set live off --config "$CONFIG"
    $D chat list --config "$CONFIG" >> "$ROOT/evidence/chat-list.jsonl"
    ```

    The phone's Leads page of each says "Chat is off for this lead. Turn it on
    at the desk.", and the directory shows it Off.
14. **OWNER ONLY: a restart returns off.** In the console, check that both
    leads are `stopped`, and quit it. `Ctrl+C` in terminal A: the daemon
    writes its shutdown lines and ends. Start it again as in step 5: every
    lead is Off on the phone. `Ctrl+C` again. `systemctl --user list-units 'ataniq-lead-scratch-*'` lists no unit.
15. **Set the proof's settings aside**, to a free name:

    ```sh
    test ! -e "$S/owner/scratch.json.leads" && mv "$S/owner/scratch.json" "$S/owner/scratch.json.leads"
    ```
16. **Put the trial's settings back:**

    ```sh
    test ! -e "$S/owner/scratch.json" && mv "$S/owner/scratch.json.trial" "$S/owner/scratch.json"
    ```
17. **OWNER ONLY: start the trial again.**

    ```sh
    systemctl --user start "$TRIAL_UNIT"
    journalctl --user --unit "$TRIAL_UNIT" | grep 'key fingerprint' | tail -n 1
    ```

    The fingerprint is the one of before the sitting, and the phone's Leads
    pages show the trial's STAND-IN samples again. The trial is as it was: its
    settings, its gate key, its pins and its paired phones are the same files.

A `test ! -e` that fails moves nothing: the name is taken, by a file of an
earlier sitting. Look at it, choose another free name for the move, and use
that name in the step that moves it back.

## Stopping everything cleanly

- Stop every lead from the console (`x`, then `y`) before the daemon: a lead
  that is stopped is proven stopped. A lead that is `unknown` after a killed
  daemon settles by itself 40 s after its start, or is stopped with `x` (see
  [a lead left unknown](scratch-leads.md#a-lead-left-unknown-after-a-killed-daemon)).
- Quit the console, then `Ctrl+C` the daemon in terminal A; it ends cleanly. The
  proof's daemon has no unit and does not start at boot.
- **OWNER ONLY, a last resort:** a lead's unit that the daemon can no longer
  reach (the daemon is gone and will not start) is
  `ataniq-lead-scratch-standin.service` or `ataniq-lead-scratch-live.service`:
  `systemctl --user stop` it by hand.
- `ataniqd chat set KEY read` answers exit 31 for a lead that is not defined yet
  (define it first) and for a full quarantine: each turn-on sets the earlier
  reply folder aside, in four names at most. `$D chat purge KEY --config
  "$CONFIG"` empties it while the daemon runs and the lead is stopped (see
  [the quarantine and its purge](scratch-leads.md#lead-replies-read-only)).

## What is recorded

Evidence of what was seen, and no conversation text:

- `<ROOT>/evidence/daemon.log`: the daemon's standard error. Its chat lines
  hold counts and fixed words, never a reply's text; its provisioning lines
  name folders of the state folder.
- `<ROOT>/evidence/chat-list.jsonl`: `chat list` lines, which hold each lead's
  switch, its flags and counts, never a reply's text.
- `<TRIAL_STATE_DIR>/evidence/scratch-<key>-lead.jsonl`: each lead's inject
  events (sent, result, refused), with the message id and never the body; and
  `<TRIAL_STATE_DIR>/lead-commands.jsonl`, the receipts of the console's
  commands, which bind a digest of each command's params.
- `<ROOT>/evidence/notes.txt`, the owner's yes or no per observation: the
  daemon's start lines and fingerprint, `daemon:standin` and `daemon:live` in
  the Leads list, no STAND-IN banner, the reply shown on the phone and on the
  desktop, the off page's words, Off after the restart, the trial back.

Not recorded: the replies' text, screenshots of a conversation, the message to
the live lead beyond what the console shows, and the login's address and code.
`record_screens` stays off, so no screen of the login is kept.
