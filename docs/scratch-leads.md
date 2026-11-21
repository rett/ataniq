# Scratch leads

This guide is for the owner and for contributors. It describes the lead control
plane as built: a daemon that, under `ataniqd run --scratch CONFIG`, defines,
starts, stops, messages and hands out attach tickets for up to eight scratch
leads on throwaway repositories, and the console that drives it. It describes
provisional code, and nothing in it is qualified. It builds on
[the canary slice](canary-slice.md), which says what the holder, the waiver and
the injected message are; this guide does not repeat that.

## What it is, and what it is not

A scratch lead is a lead whose project is `scratch-<key>`, run in the real
sandbox by the real holder, in a clone of a repository that the owner made for
the purpose. The daemon provisions it, starts it in a transient user unit and
sends it one message per epoch (a message that was not delivered does not count),
and it does all of that on the console's
command, with a durable receipt for each command.

It exists only in a build with the `ataniq_canary` tag. It is **not** a real
project's lead: no real project, no real repository and no real credential
belong in it, and the `scratch-` prefix is reserved so that no real project
carries it. It is **not** a qualification. It passes no case of the lead-hosting
design's section 7, no part of its output says that something passed, is
qualified or is supported, and it uses the same assertion 10 waiver as the
canary slice, for the same reason: no qualification record exists. It lets
several scratch leads be driven from the console; what it shows is a list of
facts, not a result.

**It is unrelated to the holder's `--scratch` mode.** `ataniq-holder --scratch`
and the `ataniq-scratch` launcher host a scratch *agent*: no project, no lead
role and a disposable home, started by hand, with no daemon ([the lead holder
guide](lead-holder.md)). A scratch *lead* is a lead of a project named
`scratch-<key>`, started by the daemon under the canary tag with the holder's
`--canary` mode. The two share a word and nothing else; no flag, file or folder
of one is read by the other.

What it does not do:

- No Qargi inside the sandbox, no operator decision on an uncertain command and
  no confirmation from the conversation file: these are the canary slice's
  limits, and they hold here. The canary's `ready` limit does not: a scratch lead
  is shown `ready` once its holder reports up (see [Ready](#ready)), which says
  nothing about the agent's login.
- No intake by the daemon: the owner takes a commit in by hand with
  `ataniq-intake`.
- No unit for the daemon and no start at boot: the daemon is started by hand.
- No removal by the daemon. A define that was cut short is removed by the
  owner's own command (see [Failure handling](#failure-handling-and-owner-recovery)).
- No client identity. The daemon's socket admits the daemon's own user, as it
  does for the read-only skeleton, and the `client` string of a command is a
  namespace for command ids, not an identity (see [The command
  table](#the-command-table)).

## Where it exists: the tag, the prefix and the three gates

**The build tag.** Every file of the daemon's side of scratch mode (the
operations, the router, the config, provisioning, the owner's two commands and
the flag) starts with `//go:build linux && ataniq_canary`, and the package that
holds the command table, `internal/leadcontrol`, has no untagged file, like the
canary's four library packages. The wire values and the console's side (the
client and the leads view) are untagged and do nothing until a daemon's `hello`
lists the operations, and a release daemon lists none. A release build has no
scratch mode: `hello` lists two operations and says `mutations: false`, and
`ataniqd` has no `--scratch` flag. Only `make canary-build` builds binaries with
the tag. The tests of the tagged code run with `make test-canary`.

**The reserved prefix is `scratch-`** (`leadrole.ScratchPrefix`), beside the
canary's `throwaway-`. Each mode accepts its own prefix and refuses the other's:
scratch mode accepts only a project that carries `scratch-`, with the role
`scratch-<key>/lead`, and canary mode only `throwaway-`. Name no real project
with either prefix.

**Three gates stand before the waiver and the injection**, as they do for the
canary slice, independent of each other:

1. **The binary.** It was built with the tag, by `make canary-build`.
2. **The place.** The state root and the runtime root are not the account's
   default folders and neither lies in or contains one (the same check as the
   canary's), and the state root carries the root marker `scratch-root.json`.
   That file holds exactly

   ```text
   {"marker":"ataniq-scratch-root","version":1}
   ```

   and a newline, is the caller's, mode 0600, at most 256 bytes, and names no
   project, because one scratch root holds several leads. **Only `ataniq-canary
   scratch-init` writes it.** The daemon never does, so a daemon cannot make a
   place into a scratch place by itself. A root carries one kind of marker: a
   state root that has `canary-root.json` is refused for a scratch lead, and one
   that has `scratch-root.json` is refused for a throwaway lead, whatever else
   it holds. The daemon, the holder and provisioning share the one check.
3. **The invocation.** The daemon runs with `--scratch CONFIG`, a config that
   lists the lead, and the holder is started with `--canary`, with
   `--inject` only when that lead's config says `"inject": true`, and with
   `--record-dir` only when it says `"record_screens": true`. The holder's
   flag keeps its name: it selects the waiver path, not the prefix.

## The owner's steps

In order, by the owner's hand. The daemon never runs `ataniq-canary`.

1. Build with `make canary-build`.
2. Write the scratch config (below). It is the owner's file: mode 0600, outside
   both roots.
3. Run `ataniq-canary scratch-init --config CONFIG`. It refuses the default
   roots, makes the state root with mode 0700 when it is not there (its parent
   folder must exist), writes `scratch-root.json` in it, and prints one line for
   each folder or file it made or kept. Run again, it keeps what is right; a root
   that carries the canary's marker, or a marker that is not the one this
   command writes, is refused. It makes no lead folder, no mirror and no runtime
   root. Exit status: 0 done, 1 failed, 2 usage.
4. Start `ataniqd run --scratch CONFIG`. The daemon makes the runtime folder
   itself, like the skeleton does.
5. Drive the leads from the console ([the console guide](terminal-console.md)),
   which defines, starts, stops, messages and attaches.

Nothing about a lead is made until its `lead.define` runs: the daemon provisions
it then, in the folders below, with the git of the host.

## The scratch config

One JSON file, read once by `ataniqd run --scratch` and by `ataniq-canary
scratch-init` and `scratch-clean`. The client never sends a path or a value of
it: a lead is named by its key. It follows the canary config's file rules: the
caller's, mode 0600, one link, at most 64 KiB, outside both roots (judged by its
resolved path), valid UTF-8, no unknown key, no repeated key and no `null`, every
key required except the optional ones named below, every path absolute and
clean, and no limit has a default in code. The values are samples: every path is
yours to choose, and nothing here is a real path.

```json
{
  "version": 1,
  "state_root": "/srv/scratch/state",
  "runtime_root": "/srv/scratch/runtime",
  "max_running": 2,
  "holder": {"executable": "/srv/checkout/build/canary/ataniq-holder", "cols": 120, "rows": 40},
  "sandbox": {
    "agent_install": "/srv/scratch/agent-install",
    "pasta": "/usr/bin/pasta",
    "bwrap": "/usr/bin/bwrap",
    "probe": "/srv/checkout/build/canary/ataniq-probe",
    "managed_marker": "guard-hook",
    "never_mounted": ["/srv/checkout", "/srv/sample/dot-ssh"],
    "provider_host": "provider.example.invalid",
    "terminal": {"term": "xterm-256color"},
    "time_limit_seconds": 60
  },
  "provision": {
    "git_name": "Sample Lead",
    "git_email": "lead@example.invalid",
    "managed_settings": "/srv/scratch/conf/managed-settings.json",
    "guard_hook": "/srv/scratch/conf/guard-hook",
    "replay_screens": "/srv/scratch/conf/replay-screens"
  },
  "leads": [
    {
      "key": "alpha",
      "source": "/srv/scratch/upstream/alpha.git",
      "upstream_branch": "develop",
      "agent": {
        "kind": "stand-in",
        "runtime": "claude",
        "runtime_version": "2.1.325",
        "footer_mode": "manual",
        "argv": ["/ataniq/agent/ataniq-canary-standin"],
        "env": []
      },
      "inject": true,
      "limits": {
        "memory_max_bytes": 4294967296,
        "swap_max_bytes": 0,
        "tasks_max": 512,
        "memory_floor_bytes": 1073741824,
        "memory_share_percent": 50
      }
    }
  ]
}
```

| Key | Meaning |
| --- | --- |
| `version` | 1. |
| `state_root`, `runtime_root` | The two roots of the scratch place, shared by every lead. Neither may be a default root of the account, and they must differ and not nest. They are the daemon's roots: `--state-dir` and `--runtime-dir` are refused with `--scratch`. |
| `max_running` | 1 to 8: the most leads that count as running at once (see [max_running](#max_running)). |
| `holder.executable` | The holder binary of `build/canary/`. |
| `holder.cols`, `holder.rows` | The holder's screen, 20 to 500 columns and 5 to 200 rows, for every lead. |
| `sandbox.agent_install` | The agent's install folder, the `sources.agent_install` of the sandbox inputs file. |
| `sandbox.pasta`, `sandbox.bwrap`, `sandbox.probe` | The three executables of the sandbox: three different files. |
| `sandbox.managed_marker` | Optional. The text the managed-settings file must hold (`sources.managed_marker` of the inputs file); the runtime `claude` needs it. |
| `sandbox.never_mounted` | 1 to 64 host paths that no bind source may touch. |
| `sandbox.provider_host` | The host name assertion 4 resolves inside, plain text. |
| `sandbox.terminal` | `term` is required; `colorterm`, `lang` and `tz` are optional. |
| `sandbox.time_limit_seconds` | 1 to 3600: how long the start waits for the probe's report line. |
| `provision.git_name`, `provision.git_email` | The leads' git identity. |
| `provision.managed_settings`, `provision.guard_hook`, `provision.replay_screens` | The files and the folder that provisioning copies, the same for every lead. |
| `leads` | 1 to 8 leads. |
| `leads[].key` | Lower-case letters, digits and dashes, starting with a letter or a digit, at most 50 bytes, unique in the file. |
| `leads[].source` | The absolute path of the local repository the lead's mirror is made from. It is the owner's, and a throwaway one. |
| `leads[].upstream_branch` | The branch fetched from `source` into the mirror, and the one the lead's tree starts from. |
| `leads[].agent`, `leads[].inject`, `leads[].limits` | As the canary config's `agent`, `inject` and `limits`, for this lead; [the canary slice guide](canary-slice.md#the-config-file) has each field and its bounds. |
| `leads[].profile_digest` | Optional. A pin of the lead's sandbox profile digest: 64 lower-case hex digits, not all zeros. See [The profile digest](#the-profile-digest-and-the-pin). |
| `leads[].record_screens` | Optional, `false` when absent: `true` or `false` and nothing else (a string, a number or `null` is refused, as for `inject`). With `true` the lead's holder records the agent's screens, window titles and OSC 7501 payloads, raw, for the owner who makes fixtures by hand. See [Recording screens](#recording-screens). |

The names follow from the key. For the key `alpha` the project is
`scratch-alpha`, the role `scratch-alpha/lead`, the layout and intake role
`scratch-alpha-lead` (63 bytes at most, which is why a key has 50), the unit
`ataniq-lead-scratch-alpha.service` and the mirror
`<state root>/mirrors/scratch-alpha.git`. Each lead's config is the canary
config's rules applied to it: the same validation, per lead.

## What a lead has on disk

Provisioning makes the folders of [the canary slice](canary-slice.md#the-commands)
for each lead, in the same two roots, and these files, which are scratch mode's
own:

| Path | What it is |
| --- | --- |
| `<state root>/scratch-root.json` | The place gate, written only by `scratch-init`. |
| `<state root>/scratch/<layout role>/sandbox-inputs.json` | The sandbox inputs file of the lead, written by the define (mode 0600) from the config's `sandbox` part, the lead's layout and agent, and the SHA-256 of the copied managed settings. The holder reads it. |
| `<state root>/scratch/<layout role>/provisioned.json` | The record that provisioning is done, written last. It holds a marker, a version, the project and the profile digest, and nothing else; the daemon reads it back and refuses a record that is not exactly what provisioning writes. |
| `<state root>/evidence/<layout role>.jsonl` | The lead's evidence file, one per lead (the canary's `canary-evidence.jsonl` is not used), with the events and the rules of [the canary slice](canary-slice.md#the-result-and-its-evidence). |
| `<state root>/lead-commands.jsonl` | The command table, one for the daemon. |
| `<state root>/mirrors/scratch-<key>.git` | The lead's mirror, made by the define from `source`; kept when a later step fails. |
| `<runtime root>/leads/<layout role>/` | The holder's runtime folder, with its attach socket and the lead's ticket file (`ticket`, mode 0600). One per lead. |
| `<runtime root>/leads/<layout role>/record/` | Only for a lead whose config says `"record_screens": true`, once it has started: the raw recording of that start, made by the holder (mode 0700, files 0600). Nothing of Ataniq reads it back. See [Recording screens](#recording-screens). |

Every lead's per-lead files are in its own folder, so leads share no evidence
file, no inputs file and no ticket file.

## The operations

The daemon registers one extension with six operations, listed by `hello` after
`hello` and `status`, in this order: `lead.list`, `lead.define`, `lead.start`,
`lead.stop`, `lead.ticket` and `lead.inject`. `hello` then says
`"mutations":true`. The protocol name stays `ataniq.client/1`, and the daemon
serves no canary operation under `--scratch`. A client takes all six as lead
control, none as a daemon without it, and some as a version error.

The shapes are the structs of [`internal/leadwire`](../internal/leadwire/doc.go),
which has no build tag and imports only the standard library, so the daemon and
the console's client share one definition. Params are decoded strictly: every key
of the operation, spelled exactly, none `null`, nothing else. A command's params
begin with `client` and `command_id`, then `role`, then the rest.

| Operation | Params | Result |
| --- | --- | --- |
| `lead.list` | none, or `{}` | `{"max_running","running","leads":[{"key","role","defined","provision","state","state_reason","epoch","unit","inject","message_spent","message_result","last"}]}`, one row per configured lead. `provision` is `done`, `incomplete` or `none`. `state_reason` is the reason code the registry shows with `state` (`holder-ready` with `ready`, see [Ready](#ready)). `message_result` is `{"outcome","reason","written"}`, the holder's three words about the epoch's message (see [Message and attach](#message-and-attach)). `last` is the role's latest receipt in summary form (no `result` and no `message`), or `null`. Not bound to a command id; it takes no lock and answers while a define runs. |
| `lead.define` | `client`, `command_id`, `role` | A receipt whose result is `{"role","state","profile_digest"}`. The state is `stopped`. |
| `lead.start` | `client`, `command_id`, `role` | A receipt whose result is `{"role","state","epoch","unit","waiver"}`. The state is `starting`, always: the receipt does not wait for [`ready`](#ready); `waiver` is the waiver's text. |
| `lead.stop` | `client`, `command_id`, `role`, `epoch` | A receipt whose result is `{"role","state","proof"}`. An `epoch` of 0 means the current epoch. |
| `lead.ticket` | `client`, `command_id`, `role`, `epoch`, `attach_role` (`writer` or `viewer`) | A receipt whose result is `{"role","epoch","ticket_file","folder"}`. The ticket is never in an answer: it is in the ticket file. |
| `lead.inject` | `client`, `command_id`, `role`, `epoch`, `message_id`, `body` | A receipt whose result is `{"role","epoch","message_id","result":{"outcome","reason","written","detail"}}`, the holder's own words as in [the canary slice](canary-slice.md#the-result-and-its-evidence). |

What each does:

- **`lead.define`** provisions the lead when that is not done (the mirror from
  `source`, the folders, the clone, the copies, the inputs file, the record, in
  that order, with the record last), then defines the role in the registry and
  gives it its holder command. A lead whose record is already there skips
  provisioning and is defined again, which updates it. Provisioning runs inside
  the daemon, so the daemon runs `/usr/bin/git`, with the explicit minimal
  environment that provisioning gives it and not the daemon's. It is bounded at
  ten minutes. A define a client stops waiting for goes on; the receipt shows it
  as `in-progress` until it ends.
- **`lead.start`** starts the lead's unit through the lifecycle, after the check
  that the lead's folders exist and are private and the `max_running` check. It
  refuses a lead that has no holder command, which is a lead whose define has not
  finished (`not-provisioned`). A start that failed is `unavailable`
  (`start-failed`, "nothing was started") only when the lifecycle proved the
  unit absent afterwards; when it could not (the unit could not be inspected,
  one is loaded, or the manager says the name is taken) the lead is shown
  `unknown` and the start is `uncertain` (`launch-uncertain`).
- **`lead.stop`** is the owner's way out of every state, `unknown` included: it
  signals the role's own units. With an `epoch` that is not the registry's it is
  refused `stale-epoch`, so a stop meant for one epoch never stops the next. It
  is `done` **only when the stop is proven**: for a lead that is stopped with a
  stop proof (the text names the unit's invocation and its empty control group),
  for a lead the reconcile ended with its proof (the unit is proven gone, and
  the answer is `done` on every press), and for a lead that was **defined and
  never started** (epoch 0, no launch intent): no launch was ever recorded and
  the lifecycle has just found no unit of the role's name loaded, so the answer
  is `done` with the fixed proof "no launch was ever recorded and no unit of the
  role is loaded". The stop always reaches the lifecycle, so a lead that is shown
  stopped is asked about too. Where a stop could not be proven (a unit that may
  be loaded, a state that is unknown) the answer stays `uncertain` with the code
  `stop-unproven`; `uncertain` means that what the stop did is not known.
- **`lead.ticket`** asks the holder for a ticket for the epoch and writes it to
  the lead's ticket file. An `epoch` of 0, or one that is not the lead's current
  one, is refused `stale-epoch`.
- **`lead.inject`** sends the epoch's message: one per epoch, and a message that
  was not delivered does not use it up (see [Message and attach](#message-and-attach)).
  It names the epoch, and a
  mismatch is `stale-epoch`, so a delayed command cannot land in a newer epoch;
  an `epoch` of 0 is refused too. The message and its admission are the canary's:
  the id and the body grammar, a body of at most 2048 bytes (the wire's cap on the
  params, 4096, is the larger), the header `[ataniq-msg id=<id> from=ataniq
  kind=canary]`. When the holder itself refuses the frame as `wrong-epoch` (the
  lead was stopped and started between the daemon's check and the holder's
  read), the answer is `stale-epoch` like the daemon's own check: no text
  reached the lead, and the refusal frees the old epoch's message like any
  other message that was not delivered (see [Message and attach](#message-and-attach)).

### Ready

A started lead is shown `starting`. The daemon moves it to `ready` by itself,
on the holder's word, and in no other way:

- **When it asks.** After `lead.start` has returned for an epoch, and once for
  every lead that is shown `starting` (with its launch recorded and a holder
  command) when the daemon starts, as for a lead it adopted after a restart, a
  background observer asks the holder's control socket for its `status`. It asks
  again, with a wait that grows from 200 ms to 1.6 s, for up to 60 s. The dial
  and the wait for each answer are bounded by 5 s each, and by the 60 s. A
  lead that the 60 s did not settle stays `starting`, the daemon logs that
  once, and it is observed again the next time the daemon starts. A `lead.start`
  never waits for the observer, and no client call does: the start's receipt is
  answered with `starting` at once.
- **What it takes.** The holder answers `ok` with a status for the **same epoch
  and the same role** as the registry shows. A refusal (`wrong-epoch` included),
  no answer, an answer with no status and a status of another epoch or role are
  ignored: the lead stays `starting`, and nothing else changes.
- **What it sets.** The shown state `ready` for that epoch, with the reason code
  `holder-ready`, the evidence kind `ataniq-observation` and the detail "the
  holder is up and the agent was started; the agent's login is not observed".
  The change is made only if the registry still shows the lead `starting` in
  that epoch, as one compare-and-set under the role's operation lock. The lock
  is **not** held while the holder is asked: it is taken, as a try-lock, for the
  one check and change, and a promotion that finds it held (a define, a start or
  a stop of the role is running) is tried again a moment later. A stop, or a
  newer epoch, therefore wins: the observer then sets nothing, and a `lead.stop`
  that is proven ends the lead's observer. A stop that is refused (a stale
  epoch, a role that is busy), that fails or that proves nothing leaves the
  epoch running and its observer with it, so the lead is still shown `ready`
  when its holder answers. A stop, start or define that arrives while the
  observer is writing the registry (about one write) is refused `role-busy` and
  may be tried again. An observer that waits for the holder never holds up a
  stop.
- **What `ready` means here.** The holder is up, and the agent was started in
  the lead's sandbox. The agent's login is **not** observed, nothing was read
  from its screen, and nothing says the agent works or will answer. `ready` is
  not a claim that the lead is usable: it replaces "nothing says anything" with
  "the holder said it is up", and no more. A lead that is `ready` stays so until
  a stop. This build adds no way from `ready` back to `starting` or to
  `degraded`: a holder that goes away after it said it was up is not noticed
  by this observer, and a unit that ends is found by the next reconcile when
  the daemon starts.
- **It does not change** what counts against `max_running` (a `starting` lead
  and a `ready` one both count), the receipts, the command table or the
  evidence file.
- **At shutdown** the daemon ends the observers and waits for them before it
  closes the registry, so a daemon stopped while a holder does not answer still
  exits 0 and promptly. When the server itself did not stop within the grace the
  daemon is already abandoning it (exit 19) and does not wait for the observers
  a second grace. A failure of the observer's own (a panic is recovered
  and logged) leaves the lead as it is shown.

`ready` is for scratch leads only. The canary has no observer and its lead stays
`starting` (see [the canary slice](canary-slice.md)).

`lead.list` shows the state and the reason together: a promoted lead's row says
`"state":"ready"` and `"state_reason":"holder-ready"`, both from the registry's
entry, so a client that draws `ready (holder-ready)` draws what the daemon
shows and guesses nothing. `state_reason` is a fixed word (lower-case letters,
digits and dashes, at most 32 bytes: the registry's own codes are at most 30),
and is `""` for a lead the registry does not know and for a code that is not
one; the key is always present.

### Receipts

Every mutating operation is answered by a **receipt**, and acceptance is the
admission of the command, not its completion:

```json
{"command_id":"3f2c0b5a9e1d4c77a1b2c3d4e5f60718","op":"lead.start","role":"scratch-alpha/lead","status":"done","result":{"role":"scratch-alpha/lead","state":"starting","epoch":1,"unit":"ataniq-lead-scratch-alpha.service","waiver":"assertion 10 waived: canary slice, no qualification record"},"admitted_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:05Z"}
```

The `status` is one of five words:

| Status | Meaning |
| --- | --- |
| `in-progress` | Admitted and not finished. Not final. |
| `done` | Carried out; `result` says what it did. |
| `refused` | The daemon changed nothing that matters; `code` says why. |
| `unavailable` | It could not be carried out; `code` says why. |
| `uncertain` | The daemon does not know what happened: it stopped between admitting the command and finishing it, or what the lifecycle said does not say. The client reads `lead.list` and never resends. |

`code` is one short token and `message` a fixed text that echoes nothing a peer
sent. The codes that scratch mode adds to the canary's are listed with each
operation; every code is a word of lower-case letters, digits and dashes.

| Operation | Refused | Unavailable | Uncertain |
| --- | --- | --- | --- |
| `lead.define` | `role-busy`, `record-invalid`, `provision-incomplete`, `digest-pin`, `provision-refused`, `registry-refused` | `provision-failed` | `registry-broken`, `failed` |
| `lead.start` | `role-busy`, `not-provisioned`, `folder-missing`, `max-running`, `role-not-defined`, `not-managed`, `preflight`, `foreign-unit`, `unit-loaded`, `limits`, `launch-refused`, `epoch-exhausted`, `registry-refused` | `start-failed`, `previous-unit-not-gone`, `intent-pending` | `launch-uncertain`, `daemon-stopping`, `registry-check`, `registry-broken`, `failed` |
| `lead.stop` | `role-busy`, `stale-epoch`, `role-not-defined`, `not-managed` | `intent-pending`, `failed` | `stop-unproven`, `daemon-stopping`, `registry-broken`, `failed` |
| `lead.ticket` | `stale-epoch`, `not-running`, `ticket-refused` | `holder-unreachable`, `ticket-failed`, `ticket-lost`, `ticket-file` | |
| `lead.inject` | `stale-epoch`, `inject-off`, `not-running`, `second-message`, `message-in-flight`, and the admission codes of the canary slice (`bad-id`, `body-too-long`, `empty-body` and the like) | `holder-unreachable`, and `holder-` with the holder's refusal (`disabled`, `busy`, `bad-request`; the epoch's message stays free after `disabled` and `bad-request`, and stays spent after `busy`) | |

Every operation can also end `refused` with `daemon-broken` (the registry or the
table cannot be written, and the daemon admits no new command until it is
restarted), and `uncertain` with `panic` (the code of the command
panicked; the daemon recovered and finished it) or `receipt-not-recorded` (the command ran
and its answer was too large or not well formed to keep). A refused inject that
reached no holder spends nothing. A start or a stop that was carried out may
still end `uncertain`: the state of the lead, not the code, is what to read.

Errors are answered only **before** a command is admitted, and nothing was
admitted when one comes: `invalid-request` (the params), `conflict` (the command
id is held for another command, or no longer held; see below), `unavailable`
(the table is full or cannot be written) and `refused` (the role is not a lead of
the daemon; no command is admitted for it). After admission the answer is always
a result that holds a receipt.

### Message and attach

A lead's `inject` flag and its message follow the canary slice, with one
difference that is the lead's own: **the epoch's one message is spent by a message
that may have reached the lead, and not by one that was not delivered.** Once the
request frame is written the message is spent, and stays spent unless the holder
wrote nothing to the lead, which it says in one of two ways:

- its answer decodes, is a valid result, and says `not-now` (reason `not-ready`
  or `blocked`) or `refused` (reason `untested` or `text`) with `written`
  `nothing`; the daemon then writes the `inject-result` line of the message to the
  evidence; or
- it refuses the request itself, with the code `disabled`, `wrong-epoch` or
  `bad-request` (an answer with no result: the receipt is `unavailable` with
  `holder-` and the code, or `refused` `stale-epoch` for `wrong-epoch`); the
  daemon then writes the `inject-refused` line of the message, with its message id
  and the code `holder-` and the holder's code.

Then the daemon makes the epoch's message free again, once the line is in the
evidence, and the lead may send another message in the same epoch, with the same
message id or another (an id that was used before is not refused: the daemon does
not remember ids, and a replay of a command's id is the command table's).
Everything else leaves it spent: `submitted`, every `uncertain` result (the `died`
result with `written` `nothing` too, since the lead's end during the send leaves
open what reached it), no answer within the wait, a closed connection, an answer
that does not decode, an ok answer without a result, a refusal code that the
daemon does not know, a line (the result or the refusal) that could not be
written, and a holder's refusal `busy`. The last is a refusal of the request, and
the receipt is `unavailable` with `holder-busy` all the same, but the daemon sends
one message at a time, so a holder that says a delivery is running is delivering
something the daemon has no record of (the inject residual below), which may have
reached the lead: the daemon writes the `inject-refused` line of the message, the
row shows the epoch's message spent with no words of it (the console says
`sent-unsure`) and the daemon's log says why, and a later message is refused
`second-message`. The holder's text is not read to tell this `busy` from another.
A refusal before the
frame is written (a bad message, a stopped role, a second message, a holder that
cannot be dialled) spends nothing. A second message while the first is
spent is refused `second-message`, and one while the first is in flight
`message-in-flight`. The canary's own rule is unchanged: there every result spends
the message, `not-now` included.

The spent epochs are rebuilt at every daemon start from the lead's evidence file,
and `lead.list` shows `message_spent`. The reader sees the lines of one epoch in
the order of the file, as a series of messages, an `inject-sent` line and then
the `inject-result` line of the same message id, or the `inject-refused` line of
the same message id whose code is `holder-disabled`, `holder-wrong-epoch`,
`holder-busy` or `holder-bad-request`. The epoch is free when every message of
the series is closed by a result of the four that release the message or by a
refusal other than `holder-busy` (a `holder-busy` line closes its message and
spends the epoch); it is spent when a message has no closing line (the daemon
was killed after the `inject-sent` line, which is written before the wait for the
answer), when a result line or a holder refusal has no `inject-sent` line before
it, names another message id or is the second of its message, when an
`inject-sent` line follows one that has no closing line, and when a result is not
a release or is not a combination the holder sends, and when a message is closed
by `holder-busy`. An `inject-refused` line with
any other code (a message that never reached the holder: `inject-off`,
`second-message`, `holder-unreachable` and the like) is not part of the series
and changes nothing, so it does not close an open message. Whatever is in doubt
is spent. The file is read as strictly as the canary's: a line that is damaged or
partial refuses the start, and so does a line of the lead that lacks the
`message_id` (and, of a result, the `outcome` and `written`) that the reader
needs. The canary's reader sees no refusal line: there every message that was
sent stays spent. The one-message state and the in-flight flag are per lead, so
one lead's inject never blocks another's.

The row also says what became of the epoch's message. `message_result` has the
holder's three words, `outcome`, `reason` and `written` (the words of the
`lead.inject` result, each at most 16 bytes; no detail), once the message of the
current epoch's last message was delivered and the holder answered; `reason` is
`""` for `submitted`. The words are the last message's, and they do not depend on
`message_spent`: after a message that was not delivered `message_spent` is `false`
and the words still say what happened to it, so that the console can show it. All
three are `""` when no message was sent in the epoch, when the last one was sent
and no answer is known, and for a lead the registry does not know. When the holder
refused the request itself as `disabled`, `wrong-epoch` or `bad-request` (see
above) the row says `outcome` `refused` with `reason` and `written` `""`: the
message was not delivered, and the words of an earlier message of the epoch are
dropped, since they are not this message's. After `busy` the three words are
`""` and `message_spent` is `true`: the message that was refused was not written,
and the delivery that the holder is running is not known to have reached the lead
nor known not to have. A restarted
daemon has no words for a message that the holder refused (its receipt is not a
result), and the row then shows the epoch's message free with no words. The daemon keeps the answer in memory when it delivers the message,
and after a restart reads it from the command table while the message's receipt is
still the role's last; a later command (a ticket, say) hides it, and the row then
says nothing of the answer, whatever `message_spent` says. The rows of
`lead.list` stay under 8 KiB for eight leads with the longest values, which is
why the row's receipt is the summary form without its `message`, and a replay of
the command's id gives the full receipt.

The console's client cleans both members to fixed words (a word outside the
grammar becomes `unrecognised`), and its leads view draws the `ready` line and
the MSG column from them (`free`, `free (not delivered)`, `sent`, `sent-unsure`
or `spent, not delivered`), so it guesses neither. The decoding is strict about
members the client does not know, which it refuses, and not about members it has,
so a row without `state_reason` and `message_result` is read as a row with both
empty: the view then shows
the plain state, and the MSG column says `sent-unsure` for a spent message
(nothing says what became of it) and `free` for a free one. A message's
receipt carries the same three words, which the view draws as the answer to the
press.

The ticket file is **per lead** (`<runtime root>/leads/<layout role>/ticket`) and
has one name, so there is **one ticket at a time per role**: the tickets of one
lead are made one after the other, and a later ticket replaces the file. A
ticket is single use and the holder grants a writer ticket only while there is
no writer and no other ticket for that place is waiting ([the lead holder
guide](lead-holder.md)), so a second writer ticket may be refused
(`ticket-refused`). The console reads the file when it attaches; mint a ticket
and attach before minting another for the same lead. Tickets of two leads do
not cross.

## The command table

The table is what makes a command run at most once. It is its own file,
`<state root>/lead-commands.jsonl`, owned by `internal/leadcontrol`; there is
no second store of lead state, because the registry and the evidence stay the
authority on what a lead is and on what was sent, and the table does not
replace either. The registry's own journal is untouched, so a release daemon
never reads a command record.

**The key and the binding.** A command is named by `client` and `command_id`. The
`client` is 1 to 64 printable ASCII bytes without a space, the `command_id` 1 to
64 bytes of `A-Z`, `a-z`, `0-9`, `.`, `_`, `:` and `-`. The table binds the key
to the operation, the role and a digest of the payload (SHA-256 of the operation
name, a NUL and the JSON of the decoded params with `client` and `command_id`
left out). The console makes a command id of 128 random bits for each command and
a client string from its name and a random run id.

**The client string is a namespace, not an identity.** The daemon's socket admits
the daemon's own user and the client API gives the handler no peer identity, so
nothing here authenticates a caller: any process of that user can choose any
client string, and can replay a command id it knows. The client string only keeps
two consoles' ids apart.

**Replay.** The same key with the same operation, role and payload returns the
stored receipt, whatever its status, and never runs the command again, even while
the first call is still running: a replay takes no lock and has no effect. The
same key with anything else is `conflict`, and so is a command id of another
client that still has an open command (the id of an open command is unique). So
a lost answer is asked for by repeating the same command, and a lost `start` is
never a second start.

**Durability.** The table is one append-only JSON-lines file, mode 0600, the
caller's, opened by handle with one link and an exclusive lock, every line at
most 4 KiB and fsynced before the call returns. The command is admitted, that is
durably recorded, before it acts, so acceptance is durable admission. The kinds
of line are `admit`, `finish`, `interrupted` and `tombstone`.

**Restart rules.** When the daemon starts it opens the table and:

- cuts a final line without its newline (an append that never returned);
- refuses to start, at the state step with exit status 11, on any other damage: a
  line that is not valid, an unknown field, a record that breaks the rules, a
  line over the cap. Nothing is repaired. The refusal says that the table holds
  every command that was admitted, so **set it aside only when every scratch unit
  is stopped**: a daemon with a new table forgets what was admitted, and a client
  that asks again would run the command again;
- gives every command that was admitted and never finished an `interrupted` line.
  Its receipt then reads `uncertain` with the code `interrupted` (for a ticket,
  `unavailable`, since a ticket repeats harmlessly), and the client is never told
  to send it again. It reads `lead.list`, where the registry and the evidence are
  the authority.

**Bounds.** The table holds at most 1024 receipts and 4096 tombstones. Over the
bound the oldest receipts that are final are dropped, and the file is compacted
when it is next opened: a temporary file, fsynced and renamed over the table,
keeping every receipt that is not final and the newest final ones. A key that
was dropped leaves a tombstone (the SHA-256 of client and command id), and a
replay of it is answered `conflict` ("no longer held; read `lead.list`"): a
dropped command is never run again. When every slot holds a
command that is not final, a new command is refused `unavailable` and nothing is
admitted. One residual risk: a key older than the newest 4096 tombstones looks
new. The console makes ids from 128 random bits, so only a client that replays a
very old id can meet it.

A failed write or sync closes the table for good: the daemon logs it, admits no
new command until it is restarted, and still answers a replay of a key it holds.

## max_running

`max_running` caps the leads that run at once. `lead.start` counts, under one
lock, the leads that are shown in any state but `desired`, `stopped` and `ended`
(so `starting`, `ready`, `degraded`, `stalled`, `stopping` and **`unknown`**,
because a unit may run), those with a launch intent that is still pending, and
the starts in flight. At the cap the start is refused `max-running`; a lead
shown `starting` that has ended is freed by `lead.stop`. The cap bounds the
number of units. Each unit carries its own memory, swap and task limits; the
daemon does not look at the host's memory across leads, and the `memory_floor`
and `memory_share_percent` of a lead are for the run script's balancing, not the
daemon's.

A second define, start or stop of one lead while one runs is not queued: it is
refused `role-busy`. A define does not hold up another lead.

## The profile digest and the pin

The sandbox profile digest depends on the lead's layout and on the provisioned
copy of its managed settings, so it cannot be known before the define has made
them. **The daemon therefore computes the digest itself, with the holder's own
code** (`sandboxstart.ProfileDigest`), from the inputs file it writes. It puts
the digest in the lead's `provisioned.json`, returns it in the define receipt,
and starts the holder pinned to it.

**This weakens a check of the canary slice.** There, the owner computes the digest
once with `ataniq-canary profile-digest` and pins it in the config, and the
holder compares it with its own computation: two readings of the profile, made at
two times by two commands. Here, unless the owner pins one, the digest the holder
is given and the one it computes are the same code applied to the same files, so
the holder's comparison no longer catches a change of the profile that is made
before the define computes it.

An owner who wants the check back may set `profile_digest` in a lead's config
entry. The define then refuses, before it writes anything (`digest-pin`), a
digest that differs, and a daemon start refuses a lead whose record is not the
pin. **But that pin is not an independent check:** the subcommands `show` and
`profile-digest` of `ataniq-canary` read the canary config and no scratch config,
so no separate tool of the owner computes a scratch lead's digest. The only way
to obtain the digest to pin is the daemon's own computation (the define receipt
or the record), which is the same code. The pin catches a change made after it
was taken; it cannot show that the first digest was the right one. Read the
profile and the managed settings yourself before the first define.

## Recording screens

The screen recorder is for the owner who makes the fixtures of a new agent
version by hand ([the lead injection guide](lead-injection.md#adding-a-version),
step 1). It is owner-only and code behind the `ataniq_canary` tag, like the
rest of this guide.

**How to record.** Set `"record_screens": true` in the lead's entry of the
scratch config and start the daemon again (it reads the config at start). Each
`lead.start` of that lead then starts its holder with
`--record-dir <runtime root>/leads/<layout role>/record`. The holder refuses
that flag without `--canary`, for any other folder, and when the folder exists
already: **a start of a recording lead whose `record` folder is still there is
refused** (the holder exits 2, "--record-dir exists already"), so that an
earlier recording is neither added to nor replaced. Move or delete the folder
first, or set the field back to `false`. Once the sandbox has started, the
holder makes the folder, mode 0700, and says one line in its journal,
`recording screens to record`, with no host path. A start that fails makes no
folder. The folder is one of the holder's paths that assertion 1 holds the
sandbox does not show, so the agent cannot see it or name anything in it.

**What it writes**, every file mode 0600:

- `README.txt`, which says the files are raw: scrub before use; do not commit.
- `0001.txt` and `0001.json`, `0002.txt` and `0002.json`, and so on: one pair
  for each distinct screen, in the fixture format of
  `internal/inject/screen/testdata` (the `.txt` is the screen text; the `.json`
  holds the size, the cursor, the alternate-screen flag, the title and the faint
  spans, `"source": "recorded"`, an empty description and no `want`). The
  screen is the snapshot the fence reads. The holder looks at it at most every
  500 ms, and only after output or a resize, so a screen that was shown for
  less than that may be missing.
- `titles.jsonl`, one JSON line for each window title as it changed, and
  `osc7501.jsonl`, one JSON line for each OSC 7501 payload (the data after
  `7501;`, the support query included). Each line has `ms` (milliseconds since
  the start), `bytes` and `cut`; a title or payload is cut at 4096 bytes, stored
  as a JSON string and never interpreted. The title is read every 100 ms, so a
  title the agent replaced sooner is not seen.
- `LIMIT.txt`, only when needed: a line when the recording stopped at its bounds
  (400 pairs, or 8 MiB written in all), and `dropped N` when the recorder's
  small queue dropped its oldest items to keep up.

The recorder never blocks the agent or its screen: when it reaches a bound or a
write fails, it stops recording, says so in one line of the journal (by errno,
with no path), and the lead goes on. The last screen before the agent ends may
be missing; the holder's `final.json` has it.

**The files are raw.** Nothing scrubs them: they may hold a login URL, a login
code, a session id, a home path or a host name, whatever the agent showed. The
daemon, the console and the evidence never read the folder, and no receipt,
evidence line or daemon log names it (the holder's command line in its unit
carries the flag, as it carries the runtime folder). Scrub by hand what you take into a
fixture, as [the fixtures](lead-injection.md#the-fixtures) of the injection
guide say, and never commit the folder.

**A recording of the login screen holds a login URL and code.** Treat the
folder as you would a credential: delete it
(`<runtime root>/leads/<layout role>/record`) once the fixtures are made, and
set `record_screens` back to `false`. If a copy of a recording that holds a
login code or a session id left the machine, take the session as leaked: sign
the agent out of it, so that it is revoked, before the lead goes on.

## Failure handling and owner recovery

The daemon refuses to start when the config, a root or the root marker is not
right (exit status 10, the config step), and when an evidence file, a lead's
record or the command table cannot be read (exit status 11, the state step). A
lead's record that is not what provisioning writes, or whose digest is not the
lead's pin, refuses the start; the refusal says that the owner checks the lead's
folders. An evidence file that cannot be read back refuses the start for the
canary's reason: set it aside only once the lead's unit is stopped, because a new
file spends no epoch's message (and frees every one whose message was sent).

**A define that was cut short** (the daemon was stopped, git ran out of time,
the ten minutes ended) leaves the lead's folders and **no** `provisioned.json`.
`lead.list` shows `provision: "incomplete"`, and every later define is refused
`provision-incomplete`, because the console cannot repair what it never named by
path. Deletion is the owner's act:

```sh
ataniq-canary scratch-clean --config CONFIG --lead KEY
```

It runs the same root check as a define (so the scratch root marker must be
there), and removes, each only when it is a real folder and not a link, the
lead's folder of the state root's `scratch` folder (the inputs file), its lead
folder, mailbox and published folders, the folder of its role lock and the
holder's runtime folder. It does **not** remove the mirror, the evidence, the
roots or any other lead's folders. It refuses a lead whose record is there (only
an incomplete one is cleaned) and a lead that has started (its holder's state or
runtime folder holds something), and a lead none of whose folders is there is
left as it is ("nothing to remove"). It may be run again if it was cut short
itself. After it, the next define starts again from the mirror. Exit status: 0
done, 1 failed, 2 usage; a `--lead` the config does not list is a failure.

**Two rules about `scratch-clean`.** It takes **no daemon lock**, so run it only
**while no define runs**: it cannot tell a define that is running from one that
was cut short, and it would pull the folders from under the first. Stop the
daemon, or make sure that no define is pending. While a define runs in the
daemon, `lead.list` shows its lead as `none` and never as `incomplete`, so an
`incomplete` row is a lead whose define is not running in that daemon.

**A daemon restart during a define leaves that lead incomplete** (or, if it had
made nothing yet, untouched). Its receipt reads `uncertain` (`interrupted`),
and the owner runs `scratch-clean` and defines again.

Failures of a running lead are read from `lead.list` and the receipts; the
lifecycle's rules apply unchanged ([the lead daemon
guide](lead-daemon.md#units-and-the-replacement-rule)): a start whose outcome is
not known leaves the lead `unknown` and counted against `max_running`, a unit may
still run, and `lead.stop` is the way out. A start that was `uncertain` after a
daemon restart is settled as the lead daemon guide says of the [reconcile at
start](lead-daemon.md#reconcile-and-what-starts-by-itself) and [the late-start
window](lead-daemon.md#the-late-start-window); the console never sends it again.

## What scratch mode leaves open

- **The profile digest is the daemon's own computation** unless pinned, and the
  pin is not independent (above).
- **A lead removed from the config while its unit runs is out of the daemon's
  reach.** The daemon knows only the leads of its config. If the owner removes a
  lead from the config while its unit runs, and restarts the daemon, the
  registry still holds the role, but `lead.stop` for it is refused ("the role is
  not a lead of this daemon"), `lead.list` does not show it, and it does **not**
  count against `max_running`: the cap can then be exceeded by that unit. Stop a
  lead, and see it `stopped`, **before** removing it from the config. If it was
  removed first, stop its unit by hand (the user unit
  `ataniq-lead-scratch-alpha.service` for a key `alpha`), or put the lead back
  in the config and restart the daemon. The daemon does not refuse to start with
  such a lead: that needs a new startup check of the registry in both builds and
  a decision on what blocks the owner who is trying to clean up, so it is left
  documented only.
- **The inject residual.** The order of an inject is: admit the command (durable),
  the admission checks and the epoch, write the request frame, write the
  `inject-sent` line of the evidence, then finish the receipt. A daemon killed
  **between the frame and the `inject-sent` line** (a window of a few
  milliseconds) leaves the epoch **unspent** after the restart, though the
  message is on its way to the lead, and the command's receipt reads `uncertain`.
  A killed daemon after that line leaves the epoch spent, because the line has no
  `inject-result` line after it: only a message whose result line is in the
  evidence, with `not-now` or `refused` and nothing written, or whose
  `inject-refused` line holds a holder refusal other than `holder-busy`, is read
  back as not spent. A replay of the command is never sent again. Read the lead's
  screen before a second message.

  The same window is why a holder's `busy` leaves the epoch spent: a restarted
  daemon with the epoch unspent that sends a message finds the holder still
  delivering the one that was lost, and the holder answers `busy`. The daemon
  takes that for what it is, a delivery it has no record of, and keeps the epoch
  spent, so that no third message follows. The message that was refused was not
  written, and the lost one is not known to have been delivered nor known not to
  have been: read the lead's screen.
- **One scratch root per account for a key.** Unit names come from the key and
  live in the user's one systemd manager. Two scratch roots on one account that
  both define `alpha` name the same unit, `ataniq-lead-scratch-alpha.service`,
  and `lead.stop` signals an older unit of the same name, which may be the other
  root's. Keep one scratch root per account for a key, and keep the canary's
  root and a scratch root apart by their prefixes.
- **The client string is a namespace, not an identity**, and any process of the
  owner's user can drive the daemon, mint a ticket and type into a session: the
  same-user limit of [the canary slice](canary-slice.md#what-the-slice-relies-on-and-what-it-proves-none-of)
  holds.
- **The git runs in the daemon.** A define runs `/usr/bin/git` in the daemon's
  process, with the explicit environment of provisioning.
- **Host resources.** `max_running` bounds the units; what N sandboxes cost the
  host (memory across units, N instances of pasta and bubblewrap) is not measured.
- **Everything else the canary slice relies on and proves none of** holds here as
  it does there: see [that list](canary-slice.md#what-the-slice-relies-on-and-what-it-proves-none-of).
  The waiver, in particular, means a sandbox starts with no qualification record.

## Where the code is

| Part | Where |
| --- | --- |
| The wire values (untagged) | [`internal/leadwire`](../internal/leadwire/doc.go) |
| The command table (tagged) | [`internal/leadcontrol`](../internal/leadcontrol/doc.go) |
| The operations and scratch mode of the daemon (tagged) | `internal/leaddaemon`: `lead_ext_linux.go`, `lead_mode_linux.go`, `canary_router_linux.go` |
| The scratch config, names and per-lead evidence (tagged) | [`internal/leadcanary`](../internal/leadcanary/doc.go) |
| Provisioning of a scratch lead, `scratch-clean` (tagged) | [`internal/leadcanary/provision`](../internal/leadcanary/provision/doc.go) |
| The screen recorder (tagged) | [`internal/leadholder/screenrec`](../internal/leadholder/screenrec/doc.go), and `--record-dir` in `cmd/ataniq-holder/canary_linux.go` |
| The owner's commands | [`cmd/ataniq-canary`](../cmd/ataniq-canary/main.go) (`scratch-init`, `scratch-clean`), [`cmd/ataniqd`](../cmd/ataniqd/doc.go) (`run --scratch`) |
| The console's client | [`internal/consoleclient`](../internal/consoleclient/doc.go) |
| The console's view | [`internal/terminalconsole/leadctl`](../internal/terminalconsole/leadctl/doc.go) |

The targeted checks, after `make doctor`:

```sh
sh scripts/go-tool.sh test -tags ataniq_canary ./internal/leadcontrol/ ./internal/leadcanary/... ./internal/leaddaemon/ ./cmd/ataniqd/ ./cmd/ataniq-canary/
sh scripts/go-tool.sh test ./internal/leadwire/ ./internal/consoleclient/ ./internal/terminalconsole/... ./cmd/ataniq-console/
```

The whole suite is `make test` and then `make test-canary` (and `make
test-ownerchannel` while code behind that tag exists).
