# The canary slice

This guide is for the owner and for contributors. It describes the canary
slice as built: one short, attended session in which the daemon starts a
holder, the holder starts the lead sandbox, a scratch agent receives one
injected message and one commit comes back through the intake. It describes
provisional code, and nothing in it is qualified.

## What it is, and what it is not

The slice joins pieces that are otherwise built side by side. The other guides
say what each piece does alone, and say that none calls another. In a build
with the `ataniq_canary` tag, under `--canary`, these joins exist:

1. The owner provisions the folders of one throwaway lead with
   `ataniq-canary provision`.
2. The owner starts `ataniqd run --canary CONFIG` by hand. The daemon defines
   the role and starts it in a transient user unit, with memory, swap and task
   limits.
3. The unit runs the holder, `ataniq-holder --canary`. The holder runs the
   start sequence of [the lead sandbox](lead-sandbox.md) (the same steps, in the
   order of the qualification harness), waives assertion 10 by name, and
   starts the sandbox's first process under its pseudo-terminal. That process
   is a scratch agent: the stand-in that `make canary-build` builds, or, in
   pass B, one live agent.
4. The owner mints a ticket and attaches the console as writer.
5. The daemon sends the lead one message through the holder's control socket.
   The holder's fence ([the lead injection guide](lead-injection.md)) pastes it
   only when the screen reads as idle.
6. The agent makes one commit on one branch of its clone. The owner takes it in
   by hand with `ataniq-intake` ([the lead intake guide](lead-intake.md)).
7. `ataniqd canary stop` stops the unit and proves its control group empty.

It is one short attended session on a throwaway repository.

It is **not** a qualification. It passes no case of the lead-hosting design's
section 7, and no part of its output says that something passed, is qualified
or is supported. It is not the week-long canary lead of that design's
section 6, step 7, which is a real lead on a scratch clone of a real project,
with a qualification record. Its result is a list of facts about whether the
parts join: what each step answered and what the evidence files hold. One
observation of a case is not a pass of it.

**Every result spends the epoch's one message.** A run sends one message per
epoch, and every result of that message spends it, `not-now` included: nothing
is retried, nothing is queued, and a second message in the same epoch is
refused. **A real run is the owner's attended act.** The run script starts a
real systemd unit, a real sandbox and, in pass B, a live agent with a fresh
login, and it is run only by the owner's hand with the lead present. No
Makefile target and no CI job runs it, and the tests run it only with `--dry`,
which starts nothing.

What the slice does not do, so that nobody reads it into the pieces:

- No Qargi inside the sandbox: no install, no seat, no assignment or
  projection record, no handoff note, no bootstrap. Their folders are bound
  because the bind table requires the rows; they are empty but for the guard
  hook in the install folder.
- No `ready`: nothing sets it, and no login-status check exists.
- No durable command queue, no retry, no operator decision on an uncertain
  command and no confirmation from the conversation file. The injection can end
  `submitted` at best and never `delivered`; the confirmation is that the
  commit appeared.
- No quiesce before a stop, no periodic reconcile and no daemon restart in the
  middle of a run.
- No coordinators, supervisor, mail or requests. The four parent folders are
  bound and inert.
- No owner channel and no approval card. The console attaches by folder and
  ticket file, as it does for a scratch holder.
- No raw PTY recording, no drop rule for host addresses and private ranges on
  the sandbox's network (the profile's network is open outbound, and pass B of the
  run script records that as a departure), no seccomp filter.
- No remote control, no runtime switch, no second agent kind in one run.
- No unit for the daemon and no start at boot: the daemon is started by hand.
- No removal tooling: the undo is by hand ([below](#undo)).
- No intake by the daemon: the owner types the intake commands.

## Where it exists: the tag, the prefix and the three gates

**The build tag.** Canary mode exists only in a build with the Go build tag
`ataniq_canary`. Every file of canary mode starts with
`//go:build linux && ataniq_canary`, and four library packages
(`internal/leadcanary` with its sub-packages, `internal/leadholder/sandboxstart`,
`internal/leadholder/injectbridge` and `internal/leadholder/canaryroot`) have no
untagged file, so an untagged build cannot import them. **Release builds do not contain canary mode.** Without the
tag, `ataniqd` has no `--canary` flag and no `canary` command, `ataniq-holder`
knows `--scratch` only and its control socket answers every `inject` request
`disabled`, and `hello` lists two operations and says `mutations: false`. The
waiver, the path from a control request to the terminal and the canary
operations are not switched off in such a binary; they are not in it.

Only `make canary-build` builds binaries with the tag. No release step, no
other target and no CI workflow does. `make test` stays untagged, and
`make test-canary` runs the tests of the tagged code, so while tagged code
exists the whole suite is `make test` and then `make test-canary`. CI runs the
second one as a tests-only job that keeps nothing.

A few pieces are untagged on purpose, because they are inert capability that a
release build never calls: the `ptyhost` option for a child with no controlling
terminal, the control wire's `message_id`, `text` and result fields, the
session's fence hooks and its generic OSC hook, the client protocol's
extension mechanism, and the reserved prefix with the intake template.

**The reserved project prefix is `throwaway-`.** It is `leadrole.ThrowawayPrefix`.
The grammar of a project name still accepts such a name; the prefix is
reserved by callers. Canary mode accepts only a project that carries it (at
least one more character after the prefix) and the role `<project>/lead`, and
refuses everything else. For the slice the role is `throwaway-one/lead`, the
layout and intake role is `throwaway-one-lead`, and the unit is
`ataniq-lead-throwaway-one.service`.

A daemon that defines real leads must refuse a project with this prefix, and
only canary mode accepts it. No release daemon defines a lead, so nothing
refuses the prefix yet; the reservation is the constant and this rule, so that
the refusal is written when a define exists. Name no real project `throwaway-`
anything.

**Three gates stand before the waiver and the injection, independent of each
other:**

1. **The binary.** It was built with the tag, by `make canary-build`, and not
   by CI or a release.
2. **The place.** The state root and the runtime root are not the account's
   default folders and do not lie in, or contain, one, and the state root
   carries the marker file `canary-root.json` that only `ataniq-canary
   provision` writes. The daemon, the holder and provisioning share one check.
   It works the defaults out twice, from the environment and from the account
   (the home folder in the password database, and `/run/user/<uid>`), so a
   unit's bare environment does not hide them. A real lead's state root at a
   custom path has no marker. One case is not caught: a bind mount of a folder
   strictly inside a default folder (see the package comment of
   `internal/leadholder/canaryroot`).
3. **The invocation.** The daemon runs with `--canary`, the config names the
   role and `"inject": true`, the project carries the prefix, and the holder
   is started with `--canary --inject`. These come from one daemon process and
   one file, so they count as one gate.

A real lead would need all three to go wrong at once.

## The commands

### Build

```sh
make canary-build
```

It runs doctor and builds, with the tag, into `build/canary/` (ignored by
git): `ataniqd`, `ataniq-holder`, `ataniq-canary`, `ataniq-canary-standin`,
`ataniq-console`, `ataniq-intake` and `ataniq-probe`. It runs none of them.
`ataniqd`, `ataniq-holder`, `ataniq-canary` and `ataniq-canary-standin` name the
tag in their `--version` output.

### `ataniq-canary`

The owner's tool, run by hand. The daemon never runs it.

| Command | What it does |
| --- | --- |
| `provision --config CONFIG` | Makes the folders of the lead (below), clones the throwaway repository from the mirror into the tree, copies the managed settings, the guard hook and the replay screens, and writes the marker last. It refuses the default roots, a root that already carries the marker and a lead that has started. |
| `replay-on --config CONFIG` | Writes the replay switch file and nothing else. It is the only subcommand allowed after a first start. |
| `show --config CONFIG` | Prints, as `key=value` lines, the names, numbers and paths of the config that the run script needs. It never prints the agent's environment, the git identity or the profile digest (`profile-digest` prints the digest). It changes nothing. |
| `check-inputs --config CONFIG --inputs FILE` | Checks the sandbox inputs file against the config and names the field that differs, never its value. It also runs, on the file, the trial of the profile and of the spec that the holder runs in its first step, `validate-config`, as far as that needs no launch and no host access, with the agent command line, environment and pinned digest the holder takes from the config, so that what the holder would refuse there is refused here and not at the start. For the runtime `claude` the file must also name `sources.managed_marker` (the holder refuses it without, at that step), and the managed-settings file that `sources.managed_settings` names must hold that text (the holder does not read the file then: assertion 8 finds a file without it inside, after the launch, and this check refuses it earlier). It changes nothing. |
| `profile-digest --config CONFIG --inputs FILE` | Prints the sandbox profile digest the holder will compute for the config and the inputs file, as one line of 64 lower-case hex digits, to pin in `holder.profile_digest` before the first start. It refuses a file that is not the config's `holder.sandbox_inputs` (the resolved paths are compared), because that is the file the holder reads; it then checks the file against the config as `check-inputs` does and computes the digest with the holder's own code, with the agent's environment of the config. It prints no environment value and no path of its own (a refusal to read the file repeats the path given to `--inputs`, as `check-inputs` does), and it changes nothing. |
| `fields --file FILE` | Prints every value of one JSON object as a sorted `path=value` line. |
| `scrub-screen --file FILE` | Replaces the screen text in a copy of the holder's final record by its SHA-256 and prints the hash. |
| `--version` | Prints the name and the build tag. |

Exit status: 0 done, 1 failed, 2 usage.

Before `provision` the mirror must exist: make it with `ataniq-intake init`,
and put the upstream branch in it. It lies at
`<state root>/mirrors/<project>.git`, and `provision.mirror` must name it.
The state root must exist with mode 0700. `provision` checks and reads
everything before it writes anything, makes every folder with mode 0700 by
handle, and refuses a symbolic link at any path it would make. Besides the
folders of the lead's layout it makes the holder's runtime folder
`<runtime root>/leads/<layout role>`, which must exist before the holder
starts, the folder of the lead's role lock `<state root>/roles/<project>`
(with `<state root>/roles` above it; the holder makes the lock file
`lead.lock` in it, with an open that makes the file and not the folder, so
without this folder it refuses at its first start), and
`<home>/canary-replay/` with the replay screens in it. A failed run leaves what
it made and no marker, and a second run refuses that; the log it prints says
what to remove.

### `ataniqd run --canary`

```text
ataniqd run --canary CONFIG
ataniqd --version
```

The daemon reads the config, takes both roots from it (`--state-dir` and
`--runtime-dir` are refused with `--canary`), and refuses to start, at the
config step with exit status 10, when the config, a root or the marker is not
right. It then gives the lifecycle the canary preflight, the canary holder
command and the config's limits, opens the evidence file, writes the
`daemon-start` line, and registers the canary operations. `hello` then lists
them after `hello` and `status` and says `mutations: true`. Without `--canary`
the daemon is the read-only skeleton of [the lead daemon guide](lead-daemon.md),
byte for byte.

### `ataniqd canary`

```text
ataniqd canary define|start|stop --config CONFIG
ataniqd canary ticket --config CONFIG --attach-role writer|viewer
ataniqd canary inject --config CONFIG --message-id ID --body-file FILE
```

Each command reads the config, calls the operation of the same name on the
running daemon's socket in the config's runtime root, and prints the result as
one JSON line. Every call carries the config's role, and the daemon refuses
another. `inject` waits up to 30 s and the others up to 60 s.

| Operation | Result |
| --- | --- |
| `canary.define` | `{"role","state"}`. The state is `stopped`. Defining an already defined role updates it. |
| `canary.start` | `{"role","state","epoch","unit","waiver"}`. The state is `starting`; `waiver` is the waiver's text. |
| `canary.ticket` | `{"role","epoch","ticket_file","folder"}`. The ticket itself is never in a response: it is in the ticket file (mode 0600, named `ticket`, in the holder's runtime folder), which the console reads. Attach with `ataniq-console --attach FOLDER --ticket-file TICKET_FILE`, as [the lead holder guide](lead-holder.md) shows for a scratch holder. |
| `canary.inject` | `{"role","epoch","message_id","result":{"outcome","reason","written","detail"}}`. See [the message](#the-message-and-the-token) and [the result](#the-result-and-its-evidence). |
| `canary.stop` | `{"role","state","proof"}`. The state is `stopped`; `proof` is the text naming the invocation and its empty control group. |

Exit status of `ataniqd canary`:

| Code | Meaning |
| --- | --- |
| 0 | The daemon answered a result. |
| 1 | Anything else, a timeout included. |
| 2 | Usage. |
| 10 | The config is refused. |
| 20 | No daemon answers at the socket. |
| 21 | The daemon refused this user. |
| 22 | An answer that is not valid, or an error the daemon answered with another code. |
| 30 | `refused`: the daemon changed nothing that matters. The message did not pass the admission, the role is not running, a message is already in flight, a second message in the epoch, injection is off in the config, a role other than the config's, or the registry or the lifecycle refused. One refusal may follow a start or a stop that was already carried out (a stale epoch, a state change the registry does not make): its text says to check the role's status. |
| 31 | `unavailable`: the operation could not be carried out, or its outcome is not known. A start whose outcome is not known and a stop that is not proven answer this too: the role is then shown `unknown`, a unit may still run, and its text says so. For `inject` it means nothing reached the lead: the holder's socket could not be dialled or the frame could not be written, or the holder refused with `disabled`, `wrong-epoch`, `busy` or `bad-request`. |

The two error codes `refused` and `unavailable` are new, and carry fixed texts
that never echo what the caller sent.

`ataniqd canary start` is refused, before it changes anything, when the
holder's runtime folder, the holder's state folder or the folder of the role
lock (`<state root>/roles/<project>`) is missing or not private; provisioning
makes all three. Each has its own fixed refusal text, with no path in it; the
detail is in the daemon's log. Without the check, a missing folder showed only
as a unit that ended at once, with the reason in the journal.

## The config file

One JSON file, read by `ataniqd` and by every `ataniq-canary` subcommand but
`fields` and `scrub-screen`. It is the caller's, mode 0600, with one link, at
most 64 KiB, outside both roots (judged by its resolved path). Unknown fields,
repeated keys and `null` are refused, every key is required except
`agent.env`, every path is absolute and clean, and no limit has a default in
code. The values below are samples: every path is yours to choose, and nothing
here is a real path.

```json
{
  "version": 1,
  "role": "throwaway-one/lead",
  "state_root": "/srv/canary/state",
  "runtime_root": "/srv/canary/runtime",
  "holder": {
    "executable": "/srv/checkout/build/canary/ataniq-holder",
    "cols": 120,
    "rows": 40,
    "sandbox_inputs": "/srv/canary/conf/sandbox-inputs.json",
    "profile_digest": "0000000000000000000000000000000000000000000000000000000000000000"
  },
  "agent": {
    "kind": "stand-in",
    "runtime": "<a runtime key the registry accepts for a lead>",
    "runtime_version": "1.2.3",
    "footer_mode": "sample mode on",
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
  },
  "provision": {
    "mirror": "/srv/canary/state/mirrors/throwaway-one.git",
    "upstream_branch": "develop",
    "git_name": "Sample Lead",
    "git_email": "lead@example.invalid",
    "managed_settings": "/srv/canary/conf/managed-settings.json",
    "guard_hook": "/srv/canary/conf/guard-hook",
    "replay_screens": "/srv/canary/conf/replay-screens"
  }
}
```

| Field | Meaning |
| --- | --- |
| `version` | 1. |
| `role` | The one role of the run: the lead of a throwaway project. |
| `state_root`, `runtime_root` | The two roots. Neither may be a default root, and they must differ and not nest. The daemon's roots are these. |
| `holder.executable` | The holder binary of `build/canary/`. |
| `holder.cols`, `holder.rows` | The holder's screen: 20 to 500 columns and 5 to 200 rows. |
| `holder.sandbox_inputs` | The path of the sandbox inputs file (below). The daemon never reads it; it names it to the holder. |
| `holder.profile_digest` | The pinned digest of the sandbox profile, 64 lower-case hex digits. The holder refuses a plan whose digest differs. Compute it with `ataniq-canary profile-digest --config CONFIG --inputs FILE`; a wrong pin otherwise fails the real start at step 12. |
| `agent.kind` | `stand-in` or `live`. It goes into every line of the evidence. |
| `agent.runtime`, `agent.runtime_version` | Given to the holder as `--runtime` and `--runtime-version`: plain text of 1 to 64 bytes. The runtime must be a key the registry accepts for a lead. |
| `agent.footer_mode` | The permission mode the fence expects in the agent's footer (`--expected-mode`), 1 to 128 bytes. |
| `agent.argv` | The agent's command as seen inside the sandbox, an absolute executable first, at most 64 elements. |
| `agent.env` | The agent's whole environment as `KEY=VALUE`, at most 64; one `--env` flag each. The holder inherits nothing for it. |
| `inject` | `true` opens the injection gate: the holder gets `--inject`. With `false` the daemon refuses `canary.inject` at once and spends nothing. |
| `limits` | The unit's limits (next section). |
| `provision` | What provisioning copies and sets: the mirror, the upstream branch, the lead's git identity, the managed-settings file, the guard hook's executable and the folder of replay screens. The daemon only checks it. |

## The sandbox inputs file

One JSON file that the holder reads with `--sandbox-inputs`, mode 0600, the
caller's, at most 64 KiB. Its keys are documented one by one in
[`internal/leadholder/sandboxstart/inputs.go`](../internal/leadholder/sandboxstart/inputs.go),
which is the authority. Unknown keys, keys in another case, a key written
twice and trailing data are refused. The run kit writes it after provisioning,
with the sources from `ataniq-canary show`, and checks it with
`check-inputs`. For the config above:

```json
{
  "version": 1,
  "canary": true,
  "project": "throwaway-one",
  "layout_role": "throwaway-one-lead",
  "state_root": "/srv/canary/state",
  "runtime_root": "/srv/canary/runtime",
  "runtime": "<the config's agent.runtime>",
  "runtime_version": "1.2.3",
  "executables": {
    "pasta": "/usr/bin/pasta",
    "bwrap": "/usr/bin/bwrap",
    "probe": "/srv/checkout/build/canary/ataniq-probe"
  },
  "sources": {
    "agent_install": "/srv/canary/agent-install",
    "managed_settings": "/srv/canary/state/leads/throwaway-one-lead/managed-settings.json",
    "managed_settings_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
    "managed_marker": "guard-hook",
    "lead_skills": "/srv/canary/state/leads/throwaway-one-lead/skills",
    "instruction_file": "/srv/canary/state/leads/throwaway-one-lead/instructions.md",
    "git_identity_dir": "/srv/canary/state/leads/throwaway-one-lead/git-identity",
    "qargi_install": "/srv/canary/state/leads/throwaway-one-lead/qargi-install",
    "qargi_records_inside": "/ataniq/records",
    "assignment_dir": "/srv/canary/state/leads/throwaway-one-lead/assignment",
    "projection_dir": "/srv/canary/state/leads/throwaway-one-lead/projection"
  },
  "never_mounted": ["/srv/sample/checkout", "/srv/sample/dot-ssh"],
  "provider_host": "provider.example.invalid",
  "terminal": {"term": "xterm-256color"},
  "time_limit_seconds": 60
}
```

- `canary` must be `true`: the holder refuses `false` under `--canary`.
  `layout_role` must be `<project>-lead`.
- The folder `<runtime_root>/leads/<layout_role>` must exist, a real folder of
  the caller with no access for group or others. The start sequence
  makes `report` (the report FIFO's folder) and `bound` in it and removes them
  when the start ends.
- `sources.managed_marker` is required for the runtime `claude`: the text of
  the guard hook that the managed-settings file holds (assertion 8 searches
  the file inside for it). The holder refuses a start without it at its first
  step; a file that lacks the text fails assertion 8 inside, after the launch.
  `check-inputs` refuses both before the start, naming
  `sources.managed_marker` or `sources.managed_settings`, and, for the rest of
  that step, the field the holder would name. The marker is not part of the
  profile digest.
- `time_limit_seconds` (1 to 3600) is how long the start waits for the probe's
  report line.
- Two statements about the start itself, that no lead has run in the folders
  yet and that no process of an earlier instance remains, are not keys: the
  file is refused if it names them, and the holder fills them for every epoch.
  The agent's command line and environment are not keys either; they come on
  the holder's command line, from the config's `agent` part.

## The unit and its limits

The daemon starts the lead in a transient user unit (the fixed properties are
in [the lead daemon guide](lead-daemon.md#properties-of-a-unit)). A canary
start carries the config's three limits in the start call: `MemoryMax=`,
`MemorySwapMax=` and `TasksMax=`. They are required and held to bounds, and
none has a default in code:

| Config field | Bounds |
| --- | --- |
| `limits.memory_max_bytes` | 256 MiB to 64 GiB |
| `limits.swap_max_bytes` | 0 to 64 GiB; 0 means no swap and is set as `MemorySwapMax=0` |
| `limits.tasks_max` | 16 to 65,536 |
| `limits.memory_floor_bytes` | 256 MiB up to `memory_max_bytes` |
| `limits.memory_share_percent` | 1 to 100 |

The floor and the share are not the unit's: they are for the balancing. The
sample values (a 4 GiB ceiling, no swap, 512 tasks, a 1 GiB floor and a share
of one half) are proposed, and are the owner's to change.

**Balancing** is a step of the run script, straight after the start, in the
same step with no pause: it reads the limits back (from `systemctl --user
show` and from the unit's control group files), lowers the memory limit to the
smaller of the ceiling and the share of the host's available memory, sets it
with `systemctl --user set-property --runtime` and reads it back again. It only
lowers: nothing raises the limit above the configured ceiling. When the result
is below the floor the host is too busy and the pass ends. The daemon does no
balancing, so a start typed by hand outside the script gets the configured
ceiling and no balanced value. The run script's guide has the details.

## The waiver

Assertion 10 of the sandbox's start-up assertions needs a qualification
record, and no store of records exists. Canary mode excuses that one assertion,
by name and nothing else, and only when the holder is in canary mode and the
inputs say `"canary": true`. Any other result that is failed, not checked or
missing still refuses the start. The wording is exact:

```text
assertion 10 waived: canary slice, no qualification record
```

It shows in these places, and only when the waiver was applied:

- `holder.json` in the holder's state folder, as the field `waiver`, with
  `canary`, `sandbox_started`, `profile_version` and `start_notes`.
- The holder's event log: an event `state` with the state `sandbox-started` and
  the reason `assertion-10-waived`. The event log holds codes, not text.
- The holder's own output, which prints the waiver once the start has
  succeeded.
- The daemon's evidence, on the `start` line, and the `waiver` field of the
  `canary.start` result, which `ataniqd canary start` prints.
- The `define` and `start` lines of the evidence also carry
  `"qualification": "none (canary waiver)"` and `"login": "not checked"`.

The daemon's two show what canary mode declares, not what the holder did. The
holder's two, `holder.json` and the event log, show what it did. The run
script's step 12 reads those.

A start the sequence refuses, or that fails, exits the holder with status 4,
leaves no agent and no `holder.json`, and writes a `refusal` event. The
mark file `lead-has-run` in the holder's state folder is made just before the
plan's process starts and is what tells the next start that a lead may have
run in the folders; a start that fails before the launch leaves no mark, and
the next start prepares again. The plan's first process starts in `/`.

The holder in canary mode refuses, with exit status 2 and a line on its
standard error, after reading the inputs file and before it takes the role lock
or writes anything:

- a sandbox inputs file that says `"canary": false`, a layout role that is not
  `<project>-lead`, a `--role` that is not the lead of the inputs' project;
- roots that fail the shared root check, and a `--role-lock` that is not the
  role's lock under the state root;
- a `--runtime`, `--runtime-version` or `--tree` that differs from the inputs
  file and the layout, a `--state-dir` or `--runtime-dir` that is not exactly
  the layout's holder folder and runtime folder, and `--inject` without a
  non-empty `--expected-mode`.

It then takes the role lock, `<state root>/roles/<project>/lead.lock`: it makes
the file when it is not there, and not its folder, which provisioning makes. A
folder that is missing is refused, with exit status 2, as `--role-lock cannot be
taken as the role lock`.

## The message and the token

The daemon builds the message with the command package of the injection
library. The sender is the daemon, the target is the lead of the project, and
the kind is `canary`, so the first line is

```text
[ataniq-msg id=<message id> from=ataniq kind=canary]
```

followed by a line break and the body. The id is 1 to 64 characters: a
lower-case letter or digit first, then lower-case letters, digits, `.`, `_` or
`-`. The body is at most 2048 bytes, valid UTF-8, printable characters or line
breaks, not empty and holding no header marker, and the whole text is at most
4096 bytes. A message the admission refuses is answered `refused` with a fixed
code (`bad-id`, `body-too-long`, `invalid-utf8`, `bad-character`,
`empty-body`, `forged-header`, `bad-text` and the like), and spends nothing.

**The token.** The body carries one line of the exact form

```text
canary-token: <token>
```

where the token is 16 to 64 characters of `a-z`, `0-9` and `-`. The stand-in
takes the first such line of a submitted paste (lines end in CR or LF), writes
the token and one line break to `canary-token.txt` at the top of its working
tree, and commits it on the branch `canary/one` with the subject
`canary: token`. It runs nothing from the message. The intake's gate for the
canary only checks that `canary-token.txt` holds the token.

## The result and its evidence

The daemon sends the holder one control request. The holder checks that the
text's first line starts `[ataniq-msg id=<id> ` for the same id (otherwise it
answers `bad-request`), runs one delivery at a time (a second answers `busy`)
and bounds it at 15 s. Its deliverer hands the first line to the fence as the
header, gives the fence a deadline of 10 s and the config's expected mode, and
maps the fence's result to the words of the wire:

| Fence outcome | Wire outcome | Reason | Written |
| --- | --- | --- | --- |
| not idle | `not-now` | `not-ready` | `nothing` |
| blocked | `not-now` | `blocked` | `nothing` |
| untested version | `refused` | `untested` | `nothing` |
| refused text | `refused` | `text` | `nothing` |
| not submitted | `uncertain` | `not-submitted` | `partial` |
| submitted | `submitted` | none | `whole` |
| died | `uncertain` | `died` | `nothing`, `partial`, `whole` or `unknown` |

`detail` is the fence's own words and readings, printable ASCII, at most 512
bytes, and nothing branches on it. The slice never answers `delivered`.

**Once the request frame is written the message is spent for the epoch, and
only an ok answer whose result decodes is a result.** Anything else after that
point (no answer in time, a closed connection, an answer that does not decode)
is not an error: `canary.inject` answers ok with `outcome` `uncertain`,
`reason` `died`, `written` `unknown` and the detail `no answer from the
holder`. The daemon waits 5 s to reach the holder and 20 s for its answer, and
the command's client waits 30 s, so the daemon's answer arrives first. A
refusal before the frame is written spends nothing, and an `unavailable` answer
always means that nothing was written to the lead. A refusal by the holder
(`unavailable` after the frame was written) still spends the epoch's message:
the epoch takes no other.

The result is only a result: the `uncertain` ones need the owner's decision,
and the run script says what is pressed for each. A restarted daemon keeps the
epoch spent, because it reads the evidence back; an evidence file that cannot
be read back refuses the daemon's own start (`ataniqd run --canary` exits 11),
because the daemon admits no message it cannot rule out. A role that is stopped and started again has a new epoch and a new
message.

**The evidence file** is `<state root>/canary-evidence.jsonl`, mode 0600, the
caller's, one JSON object and one line break per event, synced after each. Each
line has `at` (RFC 3339, UTC), `event`, `role`, `agent` (the config's kind),
`epoch` once the role has one (not on `daemon-start` and `define`), and by event `message_id`, `outcome`, `reason`, `written`, `detail`,
`code`, and on the `define` and `start` lines `qualification`, `login` and
(`start` only) `waiver`. The events are:

| Event | When |
| --- | --- |
| `daemon-start` | The daemon is listening in canary mode. |
| `define` | The role was defined. |
| `start` | The role was started; the line carries the waiver. |
| `ticket` | A ticket was minted, never the ticket itself; the detail names the attach role. |
| `inject-refused` | The message was refused before it reached the holder; `code` says why. |
| `inject-sent` | The request frame was written; the message is spent from here. |
| `inject-result` | The result, or the uncertain result that stands in for no answer. |
| `stop` | The role was stopped. |

The message body is never written: the entry has no field for it.

**The holder's event log** (`events.jsonl` in its state folder, see
[the lead holder guide](lead-holder.md#evidence)) holds three `inject` events
for a message at most: one `started` when the delivery is accepted, one with
the answer (its outcome and reason, and the state `written-` and the amount
written), and, only when the deliverer returned after the holder had already
answered `uncertain`, a third whose outcome is `late-` and the outcome it
reported (`late-invalid` for a result the wire cannot carry). Each carries the
message id, the command id and the SHA-256 of the text in hex, never the text.
A refusal of `inject` always means that nothing was written.

## The OSC 7501 counters

An agent of the tested runtimes writes OSC 7501 status reports, and one of
them (claude 2.1.325) turns its reports on only after the terminal answers a
support query, which must come before the reply to the terminal's DA1 query
(see [OSC 7501](lead-injection.md#osc-7501) in the injection guide). In canary
mode the holder's session offers a generic hook for OSC strings, installed
before the child starts; the injection bridge uses it to answer the support
query and to track the reports. The holder keeps three
counts of what the agent sent: `osc7501-queries`, `osc7501-reports` and
`osc7501-invalid` (unreadable payloads). It adds the increase of each to the
session's counters once a second and before each delivery. While the holder
runs they are in its control `status` answer; after the child ends the session
puts them, at most one second old, on the `exit` event and in `final.json`,
which the second epoch of the run reads. No `counters` event is written for
them, and no command of the slice prints them while the holder runs.

The order against the DA1 reply holds by construction and is not logged. The
stand-in in replay mode records it (below).

## The stand-in and its replay mode

`ataniq-canary-standin` is a static program that stands where a real agent
would. It draws a prompt on the terminal, turns bracketed paste on and, when a
pasted message is followed by Enter, commits the token as described above. It
names no runtime and takes nothing else from the message.

It chooses its mode itself at start: **replay mode** when the file `replay.on`
is in `<home>/canary-replay/`, plain mode otherwise. So the config and the
holder's command are the same in both epochs of pass A. In replay mode it draws
two recorded screens from that folder (`1.json` and `2.json`, two screen
snapshots in the injection library's JSON form, each with its text beside it as
`1.txt` and `2.txt`), sends the OSC 7501 support query and a DA1 query at
start, writes an idle report once the support query is answered, draws the
second screen after it receives a paste, and commits after the Enter. An
optional `report.txt` gives the payload of the idle report. It holds no screen
of its own. It writes two files in that folder:

- `order.txt`: `7501-first`, `da1-first` or `no-7501-reply`, the order in which
  the terminal's two replies reached it;
- `result.txt`: `committed <short hash>` or `failed: <reason>`.

Provisioning copies the screens into the home and never writes `replay.on`;
`ataniq-canary replay-on` does, between two epochs. Replay is what lets the
fence reach `submitted` inside the real sandbox with no agent and no
credential. It shows that the plumbing carries the fence there, and says
nothing about a live agent's timing.

## The run script

`qualification/canary/run/canary-run.sh` is the attended run: pass A with the
stand-in (two epochs, the second in replay mode) and, once, pass B with one
live agent, a fresh login inside the sandbox, the cheapest model and a cap of
30 minutes or 10 turns. It prints each of its 26 steps with what it does, the
expected result and the gate it serves, waits for Enter before every step that
runs anything, never answers a prompt, and writes an evidence folder of mode
0700 that it never publishes. A failure runs a clean-up chain. `--dry` prints
everything and starts nothing. In this feature the holder's reported idle field
reads `unknown` and never `idle` (the fence judges idle itself inside a
delivery), so step 16 records what the lead reads from the holder's status and
does not require `idle`. Its own guide,
[the run kit's README](../qualification/canary/run/README.md), says what each
pass needs beforehand (the fact reports, the intake template's review, the
mirror, the config and the inputs file), how a failed step is handled, what the
evidence folder holds and the undo. The intake template, which the owner
vouches for, has [its own note](../qualification/canary/templates/README.md).

## Undo

The slice makes no removal command. After a run, or after a script that stopped
where no clean-up chain runs: stop with `ataniqd canary stop --config CONFIG`
(or `systemctl --user stop` on the unit if the daemon is gone), prove the
control group empty, `systemctl --user revert` the unit and check that no
`ataniq-*` unit, unit file or runtime drop-in remains, stop the daemon, and
delete the canary's own state and runtime roots by hand. The run kit's README
lists the steps in order. In pass B also log out inside the sandbox, revoke the
login on the provider's account page, restart the listeners that were stopped
and compare the live config folder with its manifest.

## What the slice relies on, and what it proves none of

Stated plainly, because the slice proves none of these:

1. **The unit manager** has never run against a real systemd user manager. It
   parses the text output of `busctl` and `systemctl`. The rendering of the
   limits is new code in it; the second run of `leadunit-qualify`, with the
   limits, is what checks it.
2. **The holder** is provisional and was built for a scratch agent. Its
   control-group proof has only run against a fake tree.
3. **The sandbox profile, version 4,** is a candidate the owner has approved to
   qualify, not a qualified profile. Its descriptor binds, the status pasta
   passes on, tools as UID 0 and the device-number comparison are open
   qualification items.
4. **The assertion 10 waiver** means a sandbox starts with no qualification
   record. That is why canary mode exists only in a tagged build, refuses any
   project without the `throwaway-` prefix and refuses the default roots.
5. **The injection library has never run against a real agent.** Its screens
   were recorded with an earlier prototype holder, with single-line messages;
   the fence's own message form (a header line, a line break, a body) was never
   recorded. The bridge is new code under it. The automated accepting-path test
   replays recorded screens: it shows that the plumbing carries the fence to
   `submitted`, and says nothing about a live agent's timing.
6. **Login inside the sandbox** (case S15) and **the managed-settings path**
   (case S18) are untested.
7. **The intake template** is checked for form only. The owner vouches that it
   confines.
8. **The same-user limit.** Any unsandboxed process of the owner's user can
   drive the daemon, mint a ticket and type into the session; the slice
   changes nothing there. That includes the owner's other working agents. The
   daemon's runtime folder is mode 0700, and the passes run with no agent
   session open that has been told about the slice.
9. **The stand-in declares a real runtime's key,** because the profile's
   runtime enum has only two values. The evidence records `agent: stand-in`
   beside it. A neutral key is later work; the slice does not wait for it.
10. **The limits are set when the unit is created,** so no canary start can
    miss them. Only the balancing is the script's.

## Debt the slice creates

1. **The start sequence is copied.** `sandboxstart` is the harness's order
   written again over the same `leadsandbox` packages, because no production
   package may import the harness. A passing qualification of the harness says
   nothing about the code that starts a holder's sandbox, and the two can
   drift. Until one drives the other, no qualification result may be cited for
   a holder-started sandbox.
2. **Canary mode itself.** The waiver, the canary operations and the direct
   deliverer are to be deleted, not promoted: the waiver when a qualification
   store exists, the deliverer when the daemon's production injection path
   replaces it, the operations when the daemon has real ones. The tag keeps all
   three out of every release until then.
3. **The balancing lives in a script.** The limits are in the unit
   specification; the daemon does not yet rebalance running units.
4. **Two suite commands.** While tagged code exists, the whole suite is
   `make test` and then `make test-canary`.
