# The attended canary run

`canary-run.sh` is the run script of the canary slice. It takes one throwaway
lead through the whole path once: the daemon defines and starts it in a
transient user unit, the holder launches the agent in the lead sandbox, the
console attaches, one message is injected, the agent commits, and the intake
takes the commit into the mirror.

**The script starts real processes.** It starts a systemd user unit, the
holder, the sandbox (pasta and bubblewrap) and an agent, and in pass B the
agent is a live one with a fresh login. It is run only by the owner's hand, in
the owner's terminal, with the lead present. No test, no Makefile target and no
CI job runs it; the tests run it with `--dry` only. It refuses to run without a
terminal on standard input, and it never answers a prompt: every key is the
owner's.

There are two passes, and pass A comes first:

- **Pass A** uses the stand-in agent that `make canary-build` builds. It needs
  no credential. It runs two epochs: in the first the fence finds the screen
  unknown, answers `not-now` with the reason `not-ready` and writes nothing,
  and the owner pastes the message; in the second the stand-in replays
  recorded screens, so the fence can accept inside the real sandbox. In the
  first epoch a refusal fails the step: `refused` with the reason `untested`
  means the config does not declare a tested version, and exit status 30 means
  the daemon refused the message before the fence was asked.
- **Pass B** uses one live agent, once, with a fresh login inside the sandbox,
  the cheapest model, and a cap of 30 minutes or 10 turns. A second run of
  pass B needs the owner's word again.

## Build

```sh
make canary-build
```

This runs doctor and builds, with the canary build tag, into `build/canary/`
(ignored by git): `ataniqd`, `ataniq-holder`, `ataniq-canary`,
`ataniq-canary-standin`, `ataniq-console`, `ataniq-intake` and `ataniq-probe`.
It runs none of them. The script takes its binaries from `build/canary/` of the
checkout it lies in and from nowhere else, and before anything else it checks
that the four that hold canary mode (`ataniqd`, `ataniq-holder`,
`ataniq-canary`, `ataniq-canary-standin`) name the build tag in `--version`.
The console, the intake and the probe hold no canary mode and have no such
output; the script records the hash of all seven at step 3.

## Run

```sh
sh qualification/canary/run/canary-run.sh --pass A --config /path/to/canary.json --dry
sh qualification/canary/run/canary-run.sh --pass A --config /path/to/canary.json
sh qualification/canary/run/canary-run.sh --pass B --config /path/to/canary-live.json
```

| Argument | Meaning |
| --- | --- |
| `--pass A` or `--pass B` | The pass. The config's `agent.kind` must be `stand-in` for A and `live` for B. |
| `--config CONFIG` | The canary config file (below). |
| `--dry` | Print every step and start nothing. No command is run and no file is read or written; the values printed are samples, so it works with no binary built and no config. Read it before each pass. |
| `--evidence DIR` | The folder in which the dated evidence folder is made, an absolute path. The default is `ataniq-canary-evidence` in your home folder. |
| `--fail-at N` | A test hook, accepted with `--dry` only: a failure is injected at step N, so the dry run prints the clean-up chain a real failure there would run. |
| `--self-test NAME [VALUE...]` | The test entry, used alone: it gives the values to one of the script's own functions, prints the answer and ends. It only computes: no step runs, nothing is started or written, and nothing is read but the file given to `pass-a-record` and, by `wait-key`, standard input. The names are listed under the check of the kit, below. |

The script prints each step with its number, what it does, the expected result
and the gate it serves. It waits for Enter before every step that runs
anything. Step 11 is one step with one key: the start, the read-back of the
unit's limits and the balancing follow each other with no pause. Where a step
is the owner's own act (stopping a listener, the provider's account page,
typing a login, attaching the console, signing off the intake, deleting the
folders), the script prints what to do, waits, and records the answer and the
time. It opens no browser and holds no credential.

The steps are those of the plan's table, with the same numbers, 1 to 26. A step
that a pass does not run is printed as skipped in that pass.

### The memory limit at step 11

The unit is created with the three limits of the config. Straight after the
start the script reads them back two ways (from `systemctl --user show`,
normalised to bytes and counts, and from `memory.max`, `memory.swap.max` and
`pids.max` in the unit's control group) and then lowers the memory limit to the
smaller of the ceiling (`limits.memory_max_bytes`) and the share
(`limits.memory_share_percent`) of `MemAvailable`, rounded down to whole pages.
It sets it with `systemctl --user set-property --runtime <unit>
MemoryMax=<bytes>` and reads it back both ways. The value is never above the
ceiling. When it is below the floor (`limits.memory_floor_bytes`) the host is
too busy: nothing is set, the unit is stopped by the clean-up chain and the
pass ends.

### The one Enter at step 17 (pass B)

Exactly one result of the injection gets the owner's Enter in the pane:
`uncertain` with the reason `not-submitted` and written `partial`. The script
then records `owner-enter` as a line of its own. On `uncertain` with the reason
`died` nothing is pressed, whatever was written: the input may hold an open
paste. The owner looks at the pane, the script records what he saw, and what
happens next is his decision. On a refusal the owner puts the message into the
pane himself.

### When the owner supplies the message himself

In the first epoch of pass A, and in pass B when the fence refuses, the owner
puts the message into the pane. He does not type it. The script prints the
path of the message file (`message-1.txt` in the evidence folder) and its
text; the owner copies the whole text of that file and pastes it into the pane
with his terminal's paste, so that it arrives as one bracketed paste, and then
presses Enter in the pane once. The console forwards the paste as a paste.
Typed keys do not do: the stand-in commits only from a paste followed by
Enter, and the message has two lines, so with a live agent the Enter after the
first line would submit half of it.

### When a step fails

- **Pass B:** a failure at step 6 or later runs the clean-up chain: steps 22
  (stop and revert the unit), 24 (restore the listeners), 25 (compare the
  manifest) and 26 (clean up). From step 15 on, steps 21 (log out) and 23
  (revoke) are run as well.
- **Pass A:** a failure at step 11 or later runs steps 22 and 26.
- A failure before that starts no chain; undo by hand what the earlier steps
  left (below).

A step of the chain that has nothing to act on says so, and that is recorded.
No step of the chain can be skipped: each waits for its key and records its
result, and an interrupt (Ctrl-C) during the run starts the chain as a failure
does. Inside the chain an interrupt skips nothing and is not a key: the script
says so and the step goes on waiting for the owner's Enter, as long as
standard input is a terminal. Only the end of the input (the terminal is gone)
ends a wait without a key, and that is recorded as such.

## Before a pass

1. **The two fact reports** are on file: the report of `leadunit-qualify` run
   with the limits (see [the daemon guide](../../../docs/lead-daemon.md)), and
   the summary of one real launch of the sandbox plan
   (see [the lead sandbox qualification manual](../../../docs/qualification-lead-sandbox.md)).
   Step 2 asks for the path of each and records its name, size, time and hash.
   Pass B also asks for the evidence folder of pass A. Its `result.txt` must
   say `pass A: complete` and `findings: 0`. A pass A that completed with
   findings (a refusal in its second epoch is one) needs a fix before pass B:
   the script prints the number, and the lead confirms that every finding has
   its fix merged, which is recorded with the time. Without that the run
   stops.
2. **The intake template** [`canary-intake.json`](../templates/canary-intake.json)
   has its review on file, the owner has read it and
   [its note](../templates/README.md), and the owner runs `qualify-mirror` with
   it once, himself, with the lead present
   (see [the mirror qualification manual](../../../docs/qualification-lead-mirror.md)).
   Step 4 asks for the review and the report. Pass B reuses pass A's record
   when the template's hash is unchanged. `/ataniq` must not exist on the host.
3. **The mirror exists** before provisioning. Make it with
   `build/canary/ataniq-intake init --state <state root> --project throwaway-one`
   and put the throwaway repository's upstream branch in it. It is then at
   `<state root>/mirrors/throwaway-one.git`, which is what `provision.mirror`
   must name. The state root must exist with mode 0700 and must not be the
   default one.
4. **The config** is written (below), mode 0600, outside both roots.
5. **The sandbox inputs file** is written (below), mode 0600, at the path the
   config's `holder.sandbox_inputs` names. Its `sources` name folders that
   provisioning makes, so the paths are known from the config alone:
   `build/canary/ataniq-canary show --config CONFIG` prints them as
   `source_...` lines, and step 8 prints them again and waits before it checks
   the file with `ataniq-canary check-inputs`.
6. **The quiet window:** no Qargi run is open, no `ataniq-lead-*` unit exists,
   and no agent session is open that has been told about the slice.

### The config

One JSON file, mode 0600, at most 64 KiB, unknown fields refused. The values
below are samples: every path is yours to choose, and nothing here is a real
path.

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

- `holder.executable` is the holder of `build/canary/` in this checkout; step 3
  checks that.
- `agent.argv` is the agent's command as seen inside the sandbox, where the
  install folder of the inputs file appears as `/ataniq/agent`. For pass A,
  copy `build/canary/ataniq-canary-standin` into that install folder; step 9
  compares the copy's hash with the built one. For pass B the config is a
  second file with `"kind": "live"`, the live agent's install folder, version,
  footer mode and arguments, the model argument among them: step 15 prints the
  arguments and the owner confirms the model.
- `holder.profile_digest` is the pinned digest of the profile, 64 hex digits;
  the holder refuses a plan whose digest differs.
- The five limits are required and have no default. The values shown are the
  proposed ones: a ceiling of 4 GiB, no swap, 512 tasks, a floor of 1 GiB and a
  share of one half.

### The sandbox inputs file

One JSON file, mode 0600, with the keys that
[`internal/leadholder/sandboxstart/inputs.go`](../../../internal/leadholder/sandboxstart/inputs.go)
documents. For the config above:

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

- The seven paths of `sources` from `managed_settings` to `projection_dir`
  (without the hash and `qargi_records_inside`) must be exactly the
  `source_...` lines of `ataniq-canary show`; `check-inputs` names the field
  that differs and never its value.
- `managed_settings_sha256` is the SHA-256 of the managed-settings file, which
  provisioning copies unchanged from `provision.managed_settings`.
- `qargi_records_inside` is `/ataniq/records`, the inside path provisioning
  prepares the folders for.
- `never_mounted` lists what no bind source may touch: your checkout, the real
  provider folders, `.ssh` and the like. The paths shown are placeholders.

## The evidence folder

The script makes `<DIR>/canary-pass-<A|B>-<UTC time>` with mode 0700 and never
publishes it. Choose `--evidence` so that the path names nothing private: the
intake's verify sandbox can see the path of its work folder, which is made in
the evidence folder for the length of step 20. It must not lie in the checkout
or in a root of the canary. It holds:

| Name | Content |
| --- | --- |
| `log.txt` | Every step, key, command, check and result, each with its time. |
| `answers.txt` | Each answer the owner or the lead typed, with the question, the step and the time. |
| `out/` | The standard output and standard error of every command the script ran, by step. |
| `facts.txt`, `template-records.txt`, `binaries.txt`, `sandbox-executables.txt`, `standin-hash.txt` | Names, sizes, times and hashes of the reports, the binaries and the executables. Never content. |
| `template.sha256` | The hash of the intake template. |
| `units-before.txt` and the `units-after` and `unit-files` lists | The user manager's `ataniq-*` units and unit files at step 1 and after each stop. |
| `message-1.txt`, `message-2.txt` | The body of each injected message, with its token. |
| `owner-enter.txt` | The `owner-enter` line of step 17, when the one result that gets it occurred. |
| `intake-records/` | The verification and intake records of step 20. |
| `kept/` | Copies made at step 26: the registry journal, the holder's event log, state file and final record, the daemon's evidence file, the stand-in's `order.txt` and `result.txt`. |
| Pass B: `listeners-*.txt`, `departures.txt`, `live-config-folder.txt`, `manifest-before.txt`, `manifest-after.txt`, `canary-config-*.txt` | The listening sockets before and after, the line that records the open network as a departure from L3, and the manifests: names, sizes, times and hashes. |
| `result.txt` | `pass A: complete` or the step the pass failed at, and the number of findings. |
| `MANIFEST.txt` | Names, sizes, times and hashes of every file of the evidence. |

The script never prints or copies the content of a credential or of a file of
the live runtime's config folder: the manifests hold names, sizes, times and
hashes. It reads the canary config only through `ataniq-canary show`, which
prints names, numbers and paths and leaves out the agent's environment, and it
records the config's hash. At step 26 the last screen kept in each copy of the
holder's final record is replaced by its hash (`ataniq-canary scrub-screen`),
and the evidence is searched for anything that looks like a login URL or code;
the script lists files and line numbers only, and the owner removes what he
finds.

## Undo by hand

When the script is stopped where no clean-up chain runs, or a chain step
failed, undo in this order. The names come from
`build/canary/ataniq-canary show --config CONFIG`.

1. **Stop.** `build/canary/ataniqd canary stop --config CONFIG`. Its result is
   `stopped` with a proof text. If the daemon is gone, stop the unit itself:
   `systemctl --user stop <unit>`.
2. **Prove the control group empty.** `systemctl --user show <unit>
   --property=ActiveState --property=ControlGroup` must show the unit inactive,
   and the folder of that control group under `/sys/fs/cgroup` must be gone or
   its `cgroup.procs` empty. Then `systemctl --user revert <unit>`, and check
   that `systemctl --user list-units --all 'ataniq-*'`, `systemctl --user
   list-unit-files 'ataniq-*'` and the folders `systemd/transient` and
   `systemd/user.control` of your runtime folder hold nothing of the unit.
3. **Stop the daemon** in its terminal (Ctrl-C).
4. **Delete the canary's two roots by hand:** the state root and the runtime
   root of the config. The script never deletes them; step 26 prints the
   command and waits for you. It prints it only when the unit is stopped
   (`ActiveState` is `inactive` or `failed`): otherwise it prints
   `systemctl --user stop <unit>`, waits for your key and asks the state
   again, and when the unit is still not stopped the step fails and the
   delete command is not shown, because the holder and the agent may still
   run with those folders bound. Then do 1 and 2 above first. Nothing else of the host was written: the
   evidence folder is yours to keep.

In pass B, also: log out inside the sandbox if the pane still runs, revoke the
login on the provider's account page whatever the logout showed, restart the
listeners you stopped, and compare the live config folder with
`manifest-before.txt`.

## What the script does not check itself

- The holder's status answer. The script has no client for the holder's
  control socket, and no command of the kit reads that answer. So at step 16
  the lead reads in the console whether the status is idle and what the OSC
  7501 counters (queries, reports, unreadable payloads) show, and the script
  records what he reads; it says so plainly and shows no counter itself there.
  While the holder runs the counters are only in that answer: the event log
  holds none. After the child ends they are in `final.json` in the holder's
  state folder, and step 22 prints them from it, or says that none is named
  for 7501 or that there is no final record.
- That `hello` lists the canary operations (step 10) is the lead's reading.
- The script does not run git in the lead's tree after a lead has run in it.
  The commit is observed through the intake at step 20.

## The check of the kit

```sh
sh scripts/go-tool.sh test ./tests/ -run CanaryRunKit
```

It reads the Makefile target and the `AGENTS.md` lines, runs `sh -n` on the
script, and runs the script with `--dry` or `--self-test` only, with a `PATH`
that holds stubs which record being called: none may be. It starts nothing.

The dry run of a step prints what the step would do; it is not the code a real
run takes. So the check also reads the script's text (the body of step 11
holds one wait for a key and no question; one arm of step 17, and no other,
asks for the owner's Enter) and asks the script's own functions through the
test entry:

| Test entry | Prints |
| --- | --- |
| `--self-test balance CEILING SHARE FLOOR AVAILABLE` | `value=<bytes>`, the balanced memory limit of step 11 for a ceiling and a floor in bytes, a share in percent and `MemAvailable` in kB; then `floor=ok` or `floor=below`. |
| `--self-test inject-action PASS OUTCOME REASON WRITTEN` | What step 17 does with that result of the injection: `none`, `owner-enter`, `look`, `owner-supplies`, `decide` or, in pass A, `wrong-config` or `unexpected`. |
| `--self-test fields-hold FIELDS PATH=VALUE...` | `yes` when the lines `FIELDS`, which are what `ataniq-canary fields` prints for one line of a record, hold every `PATH=VALUE` as a whole line, and `no` otherwise. Step 12 matches the lines of its epoch with it. |
| `--self-test wait-key` | It waits for one key as a step of the clean-up chain does, with the chain's answer to a signal and its standard input taken for a terminal, and prints `key` when a line was read or `no-key` when the input ended. |
| `--self-test pass-a-record FILE` | What step 2 of pass B reads in that `result.txt` of pass A: `clean`, `findings <N>`, `unknown` (complete, with no number of findings) or `incomplete`. It reads the file and nothing else. |
| `--self-test counters-7501 FIELDS` | The lines of `FIELDS`, which are what `ataniq-canary fields` prints for a final record, that are counters named for 7501, as step 22 prints them, or the line that says there is none. |
| `--self-test unit-stopped STATE` | `stopped` when that active state of the unit lets step 26 show the command that deletes the roots (`inactive` or `failed`), and `not-stopped` for every other one, an empty one too. |
