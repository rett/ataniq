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
  the daemon refused the message before the fence was asked. In pass B too,
  exit status 30 fails step 17 and is not read as the fence's refusal.
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
checkout it lies in and from nowhere else. How it knows each one:

| Binary | Reports the tag in `--version` | Hash recorded at step 3 |
| --- | --- | --- |
| `ataniqd` | yes, required | yes |
| `ataniq-holder` | yes, required | yes |
| `ataniq-canary` | yes, required | yes |
| `ataniq-canary-standin` | yes, required | yes (and compared with the agent's copy at step 9 of pass A) |
| `ataniq-console` | no: it holds no canary mode and has no `--version` | yes |
| `ataniq-intake` | no: it holds no canary mode and has no `--version` | yes |
| `ataniq-probe` | no: it holds no canary mode and has no `--version` | yes (and the copy the inputs file names is compared with it at step 9 of pass A) |

Before anything else, and again at step 3, the script runs `--version` of each
of the four that hold canary mode and refuses to go on when one does not name
the build tag; a holder built without the tag, or one that does not know
`--version`, stops the run there. The other three are known by their hash
alone.

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
| `--self-test NAME [VALUE...]` | The test entry, used alone: it gives the values to one of the script's own functions, prints the answer and ends. It only computes: no step runs, nothing is started or written, and nothing is read but the file given to `pass-a-record` and `has-line`, standard input (the keys of `wait-key`, `stop-daemon`, `roots-deleted`, `ask-path`, `ask-file` and `affirm-word`) and the hint file of `ask-path` and `ask-file`; `has-line`, `review-verdict`, `mirror-verdict` and `ask-file` run the helper `ataniq-canary fields` of `build/canary` on the file they are given, `wait-daemon` and `stop-daemon` the `ataniqd status` of `build/canary`, and no other program. The names are listed under the check of the kit, below. |

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

Besides the tools every step names, the polling of the daemon's status uses
`timeout` of coreutils (the preflight checks that it is on `PATH`).

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
happens next is his decision. On a refusal, and on `not-now` with written
`nothing` (the screen was not ready), the owner puts the message into the pane
himself; he first clears what the pane shows (a dialog, a notice, text in the
composer). Before step 17, step 16 asks the owner to make the pane ready: the
empty composer, no dialog, the last row reading the mode the config expects
with no notice on it, then leave the session pane and touch neither key nor
mouse there for 3 seconds (the fence needs 2 seconds of quiet).
The owner answers `yes` to that check; `go-on` means the pane could not be made
ready: the fence then answers `not-now` and he pastes the message at step 17
(any other answer ends the pass).

### Step 14: the console, then the ticket

The attach ticket lives 30 seconds from its mint, and the console, while its
ticket file is absent or empty, only shows "waiting for a ticket". So the order
is the console's first. The script prints the exact command, with the real
holder runtime folder and the ticket file `ticket` in it (the file the daemon
writes into, so both are known before the mint), and waits for the owner's
Enter. He starts the console in a terminal of his own (T3), sees it wait, and
presses Enter in T1; only then does the script run `ataniqd canary ticket`. It
checks that the ticket file the mint names is the one it printed (a step whose
ticket went to another file fails), watches that file until the console empties
it (the console empties it when it takes the ticket; the script reads only its
size), and asks `yes` for the pane showing the agent. That `yes` is the owner's
attestation.

In the first epoch the owner then starts the same command once more in a fourth
terminal (T4). The first console emptied the ticket file, so the second one
finds no ticket: it does not attach and shows only "waiting for a ticket"; he
quits it with `q` and answers `yes` to what he saw. The script reads that the
ticket file is still empty and that the number of `attach` events in the holder's
event log is the same as before the second console. The kit has no command that
presents a ticket twice to the holder, so it does not claim that a used ticket
is refused: it shows that a console without a ticket does not attach. The
second console is tried in the first epoch only.

The ticket's role depends on the epoch. The first epoch's ticket is a writer's:
the owner pastes the message into that console at step 17. In the second epoch
the fence writes the message and the owner only watches, so step 14 mints a
viewer ticket (`--attach-role viewer`). That is deliberate: the writer's attach
sets the size of the session to the console's session pane, the stand-in draws
the recorded 120 by 36 screen once and does not redraw, and the fence recognises
that screen only in a pane of at most 120 columns and at least 36 rows. At most
windows a writer would make the second epoch's inject end `not-now, not-ready`
and record a finding. A viewer's attach sets no size (only the writer's Hello
does), and the console, as a viewer, sends no key, no paste and no size, so the
screen stays as recorded and the window's size does not matter in the second
epoch. The owner pastes nothing there. The used-ticket behaviour does not
change with the role: the first epoch's second console finds the ticket file
empty and does not attach, and the attach events counted before and after
(each attach, of any role, is one `attach` event) are the same.

The first epoch's window does matter: the console drops a paste when its window
is below 100 columns by 30 rows (the cockpit layout is then off), and in a
larger window the session pane becomes the agent's terminal size. The first
epoch's screen is an unknown one to the fence anyway, so the inject is expected
to end `not-now, not-ready` and the owner pastes the message himself: a
first-epoch paste into an unknown screen is expected to be refused by the fence,
not a fault. In the first epoch pasta may also write two lines on the agent's
pane above the stand-in's prompt, about an AVX2 build and a nameserver address;
that is expected.

Before the second epoch the owner closes the console of the first epoch (`Ctrl+\`
leaves the session pane, then `q`): a console left open waits on the same ticket
file and would take the ticket of the second epoch. The script says so before it
stops the unit, and again at the second epoch's step 14. It cannot see whether
the console is closed.

### When the owner supplies the message himself

In the first epoch of pass A, and in pass B when the fence refuses or answers
`not-now` having written nothing, the owner puts the message into the pane. He
does not type it. The script prints the
path of the message file (`message-1.txt` in the evidence folder) and its
text between two marker lines (`----- begin of message-1.txt -----` and
`----- end -----`, the text with no prefix); the owner copies the file's text
(cat the file), not the lines of the screen, and pastes it into the pane
with his terminal's paste, so that it arrives as one bracketed paste, and then
presses Enter in the pane once. The console forwards the paste as a paste, and
only from session focus: a paste while the console has the focus itself is
dropped without a word (and the first Enter only moves the focus), so the owner
first gives the session pane the focus (selects the session terminal and presses
Enter; the focus bar then names the session, and Ctrl+\ leaves it). The script
says so before it names the file.
The stand-in takes the token only from a line that starts with
`canary-token: `; `no token` in the pane means the paste was not the file's text,
so paste again (in plain mode that is recoverable).
Typed keys do not do: the stand-in commits only from a paste followed by
Enter, and the message has two lines, so with a live agent the Enter after the
first line would submit half of it.

### What the lead types, and what the script reads itself

The script never answers a prompt and still refuses a standard input that is
not a terminal. For a fact it can read, it reads it, records what it read in
the evidence (`log.txt`, and `out/` for the output it read) and fails the step
with the reason when the fact is not true; the lead types nothing for it:

- Step 1: no Qargi run of this project is open (`qargi local list`, the rows
  whose `repo.path` is in this checkout's repository, the main work tree of
  each side compared as resolved paths, that are not applied and merged; a row of a run whose
  directory is gone is counted and logged but not treated as open, because
  its `repo.path` cannot be read; without `qargi` on `PATH`,
  or with a list that is not what `qargi local list` prints (another header, a
  line of another kind), or when an open run has a `repo.path` that is
  missing, unreadable or empty (its repository is not known), the script says
  so in the log and asks the lead for `yes` as before), and no `ataniq-*` unit is loaded (slices are left out). A
  `git status` or `systemctl` that fails fails the step; it does not read as
  empty.
- Step 2: the report is of `leadunit-qualify` with the verdict `pass`, a `pass`
  for every entry of `checks` and passing `limits-show-read-back` and
  `limits-cgroup-read-back` checks (a report of a run without limits fails);
  the summary of the plan is of a launched plan (`exit.launched` and
  `exit.exited` true, `report_outcome` `line-read`) with the status `ok` or
  `skipped` for every step, `exit.code` 0 and `ok` or `not-checked` for every
  case result. The lead still gives the two paths (an Enter takes the hint
  shown at the prompt, a typed path wins; see Easier on the owner).
- Step 3: the checkout's commit is the commit of `develop`, and no tracked
  file has a change (untracked files are reported and do not fail the step).
- Step 8: the output of `ataniq-intake branches` names the upstream branch.
- Step 12: `holder.json` says the sandbox started, has profile version 4 and
  the profile digest of the config's `holder.profile_digest`.
- Step 20: the upstream branch is at the commit of the lead's branch, by the
  intake's list and by `git rev-parse` on the mirror.

The lead still types `yes` where no file holds the fact: that no agent session
was told about the slice (step 1); that the probe reported once and every
assertion is ok except 10 (step 12: the holder writes `sandbox_started` only
after its own judgement of the probe's report passed with every assertion
required, and the script reads that, but `holder.json` keeps no result and no
file the script reads shows that the probe reported once, so the lead's word
stays as a second reading of the report); that the daemon's start line and
hello list the canary operations (step 10); and, in pass B, that each finding
of pass A has its fix merged. The owner's prompts are as before: reading the
template, the sign-off, the paste, the deletions and every other `Owner:`
word.

### When a step fails

- **Pass B:** a failure at step 6 or later runs the clean-up chain: steps 22
  (stop and revert the unit), 24 (restore the listeners), 25 (compare the
  manifest) and 26 (clean up). From step 15 on, steps 21 (log out) and 23
  (revoke) are run as well.
- **Pass A:** a failure at step 11 or later runs steps 22 and 26.
- A failure before that starts no chain; undo by hand what the earlier steps
  left (below).

When step 11 finds that the unit is not active (the holder ended at its start),
or that the role is not starting, it prints the last 40 lines of the unit's
journal before it fails: the holder says why it ended on its standard error, and
the event log holds only a code (`sandbox-start-failed`). The step 22 that the
chain runs keeps the journal in the evidence.

A step of the chain that has nothing to act on says so, and that is recorded.
No step of the chain can be skipped: each waits for its key and records its
result, and an interrupt (Ctrl-C) during the run starts the chain as a failure
does. Inside the chain an interrupt skips nothing and is not a key: the script
says so and the step goes on waiting for the owner's Enter, as long as
standard input is a terminal. Only the end of the input (the terminal is gone)
ends a wait without a key, and that is recorded as such.

## Easier on the owner

The first two attempts of pass A failed on the process, not on the code: the
wrong file at a path prompt, Enter at step 10 before the daemon in the second
terminal was up, the daemon stopped late at step 26, and three terminals and a
paper checklist. The run is easier on the owner in these ways, and in none of
them does the script answer for him: it never answers a prompt, never starts the
daemon, a unit, a sandbox or an agent, never defaults a typed word (`yes`,
`read`, `integrate`, `confirmed`, `reset`, an attestation), and still refuses a
standard input that is not a terminal.

- **It waits for what you do elsewhere.** A step that waits for something you
  start or stop in another terminal asks for it quietly every second for up to
  60 seconds, says "waiting for ..." once and prints a dot every 5 seconds, and
  goes on as soon as it is there: the daemon after your Enter at step 10, the
  daemon's end at step 26, the console taking the ticket at step 14 (the console
  empties the ticket file when it takes the ticket; the script asks only
  whether the file is empty, never reads it, and goes on to your question
  whether or not it was seen), and the listeners at steps 6 and 24 (pass B).
  Step 10 fails, with the old message, only after the minute. Each ask of the
  daemon's status is run by `timeout(1)` (coreutils, as `sha256sum` and `stat`
  are; the preflight checks it is there) and cut off after 5 seconds, which
  reads as neither "answers" nor "gone" (its exit status 124 is named when step
  26 fails). Without that cut-off a socket that accepts and never answers would
  hold each ask for about 10 seconds, the binary's own limit. The wait as a
  whole ends after the 60 seconds plus one ask's 5 seconds, so a daemon that
  hangs costs about 65 seconds at most per wait, not 60 asks of 10 seconds.
  Step 12's wait for the holder's record asks the unit, not the daemon, and
  is allowed 90 asks one second apart: about 90 seconds at most. An empty
  answer from `systemctl` is asked again within those asks, never taken for
  the unit's end.
- **Step 26 asks again.** A daemon that still answers after your Enter is not a
  failure of the pass: the script says "the daemon still answers: stop it with
  Ctrl-C in its terminal, then press Enter" and asks again, up to 5 more times,
  each recorded in the log. A root that still exists when you say both are
  deleted is said and asked again the same way. The step fails only after the
  last ask.
- **Hints at the path prompts, and only there.** `canary-prepare.sh` writes
  `ROOT/answers.hint` (mode 0600, lines `key=path`, no secret). `canary-run.sh`
  reads the one beside the config. At the four path prompts (the two fact
  reports at step 2, the template's review and the report of `qualify-mirror`
  at step 4) it shows `(Enter for: PATH)` and takes an empty line as that path.
  A path you type always wins, the end of the input takes nothing, the file is
  still checked and recorded, and `answers.txt` and the log say whether the
  answer was a hint or typed. No word is ever a hint: an empty answer at
  `yes`, `read` or any other word is refused as before.
- **A wrong file is refused before it is recorded.** The template's review
  must not be a report of `qualify-mirror` (a JSON file with `cases` and a
  `template` is refused by name), must be a text or markdown file, and must hold
  the template's hash or the word `verdict`. The report must be one JSON
  object whose `template.sha256` is the hash step 4 just computed. The two fact
  reports are checked as before. A refused file is said with its reason and the
  question is asked again, up to 3 more times, before the step fails.
- **`canary-preflight.sh`** (below) says what to fix before the run, and what
  you type when all is well.
- **`CHECKLIST.txt`** is written by `canary-prepare.sh` into the root: one
  page with the real paths, what to start and in which of the four terminals
  (T1 the script, T2 the daemon, T3 the console, T4 the second console of step
  14 and the integrate command of step 20), and what each prompt of pass A
  expects you to type.

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
3. **The mirror exists** before provisioning (`canary-prepare.sh` makes it, below). Make it with
   `build/canary/ataniq-intake init --state <state root> --project throwaway-one`
   and put the throwaway repository's upstream branch in it. It is then at
   `<state root>/mirrors/throwaway-one.git`, which is what `provision.mirror`
   must name. The state root must exist with mode 0700 and must not be the
   default one.
4. **The config** is written (below), mode 0600, outside both roots. For pass
   B, `canary-prepare.sh --pass B --claude PATH` writes it, the inputs file and
   the install folder (see "The set-up of pass B"); the hand-made way is a
   short note there.
5. **The sandbox inputs file** is written (below), mode 0600, at the path the
   config's `holder.sandbox_inputs` names. Its `sources` name folders that
   provisioning makes, so the paths are known from the config alone:
   `build/canary/ataniq-canary show --config CONFIG` prints them as
   `source_...` lines, and step 8 prints them again and waits before it checks
   the file with `ataniq-canary check-inputs`. The inputs file comes first and
   the pin last: the config's `holder.profile_digest` is computed from the
   inputs file and the config's `agent.env`, so every edit of either is
   followed by a new pin.
6. **The quiet window:** no Qargi run is open, no `ataniq-lead-*` unit exists,
   and no agent session is open that has been told about the slice.

## Prepare and reset

Two helpers write the set-up of a pass (A with the stand-in, B with the live agent), and put it back (a third, the
preflight, looks at it before the run). Neither answers a
prompt, and neither starts a unit, a daemon, a sandbox or an agent. They lie
beside the run script and share `canary-lib.sh`, which is only sourced. They
use the tools the run script uses, and besides them `chmod`, `readlink` and
`ls`.

```sh
sh qualification/canary/run/canary-prepare.sh --dry
sh qualification/canary/run/canary-prepare.sh [--root DIR] [--bin DIR] [--pasta PATH] [--bwrap PATH] [--build]
sh qualification/canary/run/canary-prepare.sh --pass B --claude PATH [--model NAME] [the same arguments]
```

| Argument | Meaning |
| --- | --- |
| `--root DIR` | The folder that holds the whole set-up, an absolute path. The default is `ataniq-canary-a` in your cache folder. It must be new or empty. It is refused when it is a link, in `/tmp` or `/var/tmp`, or inside the repository checkout (or holds it), and when its path has a character outside letters, digits and `. _ / + -`. |
| `--pass A` or `--pass B` | The pass; A is the default. A makes the stand-in set-up; B makes the live one and needs `--claude` (see "The set-up of pass B"). The default `--root` is `ataniq-canary-a`, or `ataniq-canary-b` for B, in your cache folder. |
| `--claude PATH` | Pass B only: the tested claude executable, an absolute path to one regular file. A link, a folder or a file that is not executable is refused (the manager's `claude` is a shim or a link). It is copied to `ROOT/agent-install/claude` (mode 0700) and the copy's `--version` must name 2.1.325 (the version the config declares). |
| `--model NAME` | Pass B only: add `--model NAME` to `agent.argv` (letters, digits and `. _ : + -`, not starting with `-`, at most 64 characters). Without it there is no model flag and the agent runs its default: prepare says "Model: none chosen. Prepare again with --model NAME, or add \"--model\", \"NAME\" to agent.argv (the pin does not cover argv)", and step 15 asks you to answer no when no model argument is shown. |
| `--bin DIR` | The built binaries, `build/canary` of the checkout by default. All seven must be there; a missing one is named. |
| `--build` | Run `make canary-build` first. Without it nothing is built. |
| `--template-review PATH`, `--unit-report PATH`, `--plan-summary PATH` | An existing regular file, an absolute path, that the run suggests at the matching path prompt (a hint, below). The review of the template is named by you or the lead and taken from here and from nothing else; the docs name no fixed file for the two reports. A file that is not there is no hint, and the output says so. |
| `--pasta PATH` | A regular executable file to use as pasta. Without it `/usr/bin/pasta` is used; when that is a link, which the harness refuses, the regular file it leads to is copied to `ROOT/bin/pasta` and the output says so: the subject is then a copy of `passt`, not `pasta` itself. A link given with `--pasta` is refused. |
| `--bwrap PATH` | The bubblewrap executable. The default is the one on `PATH`. |
| `--dry` | Print what would be made, run nothing and write nothing. |

It makes, below the root, `state/` and `runtime/` (mode 0700, the two roots of
the config), `evidence/`, `upstream/` (a throwaway repository with one commit
on `develop`, by a made-up author at `example.invalid`), the mirror
`state/mirrors/throwaway-one.git` (made with `ataniq-intake init`, with
`develop` pushed into it), `conf/` (the sandbox inputs file and the config's
other files: the replay screens copied from the recorded set of claude 2.1.325,
a managed-settings file that names the guard hook as its PreToolUse hook, and a no-op guard hook; the inputs file carries `sources.managed_marker`, `guard-hook`, a text the managed settings hold), `agent-install/` with
a copy of the built stand-in, `bin/` with a copy of the built probe (mode 0700; the probe is a bind source and the checkout, where `build/canary` lies, is in `never_mounted`, which the holder refuses, so the inputs file names this copy as `executables.probe`, as it names the copy of pasta), `config.json`, `mirror-qual/config.json`,
`answers.hint` (the hints for the path prompts: `mirror-report` always, the
path `qualify-mirror` is told to write; `unit-report`, `plan-summary` and
`template-review` only for the matching argument; mode 0600, no word and no
secret), `CHECKLIST.txt` (your one page, from `checklist.txt.in` in this folder, or `checklist-b.txt.in` for pass B,
mode 0600) and the mark `.canary-prepared`. The config is the one under "The config" below,
with the stand-in agent, the runtime `claude` 2.1.325, `footer_mode` `manual`,
`inject` true and the proposed limits; the provider host is `api.anthropic.com`,
a name that resolves, because the reserved example names fail assertion 4. It
runs `ataniq-canary profile-digest` and writes the pin into
`holder.profile_digest`, then runs `ataniq-canary check-inputs`. It prints no
secret, and ends with the commands you type next.

`qualify-mirror` stays your own step: the script writes its config with
`"i_run_this_on_purpose": false`, and the acknowledgement is yours to change
([the mirror qualification manual](../../../docs/qualification-lead-mirror.md)).
A prepare that fails part way leaves what it made and no mark, and
`canary-reset.sh` refuses a root with no mark: remove the half-made root by hand
(`chmod -R u+w ROOT`, then `rm -r ROOT`) and run prepare again. A root that
carries the mark of a finished prepare is the one `canary-reset.sh` puts back.

### The set-up of pass B

`canary-prepare.sh --pass B --claude PATH` makes a pass B root the way pass A's
is made (the roots, the mirror, the throwaway repository, the probe and pasta
copies, the managed settings with the marker and the no-op guard hook, the
replay screens that pass B's provisioning needs, `answers.hint`, the config of
`qualify-mirror` and the mark), for the live agent. The one thing it does not do is
run `qualify-mirror`: that is yours, for this root (the end of step 1).

1. **Prepare.** `PATH` is the tested claude (2.1.325) as one regular file, not
   the version manager's `claude` (a shim or a link), and not the folder you use
   for your sessions. Prepare copies it to `ROOT/agent-install/claude` and runs
   `env -i HOME="$(mktemp -d)" ROOT/agent-install/claude --version` on the copy,
   as step 9 will, and stops with the output shown if it does not name 2.1.325.
   `--dry` prints what it would make and writes nothing.
   ```sh
   sh qualification/canary/run/canary-prepare.sh --pass B --claude PATH [--model NAME] [--root DIR] --dry
   sh qualification/canary/run/canary-prepare.sh --pass B --claude PATH [--model NAME] [--root DIR]
   ```
   It writes `config.json` with `agent.kind` `live`, `agent.argv`
   `["/ataniq/agent/claude"]` (plus `--model NAME` when you gave it), `agent.env`
   `["DISABLE_AUTOUPDATER=1"]`, `footer_mode` `manual` (the recogniser's name of
   the mode, `manual` or `auto`, not the footer's text) and `inject` true;
   `conf/sandbox-inputs.json` with `sources.agent_install` `ROOT/agent-install`,
   the terminal values `xterm-256color`, `truecolor` and `C.UTF-8` (the ones the
   recorded screens were taken with), and the managed settings with
   `defaultMode` `default`. The pin comes last: `holder.profile_digest` is
   computed after the environment and everything else is in place (the
   environment is part of the digest), and then `check-inputs` runs. It also
   writes `ROOT/CHECKLIST.txt` (from `checklist-b.txt.in`: the order, the pass B
   commands and the steps pass B adds, with what you type at each) and
   `ROOT/netcheck.sh`. Then run `qualify-mirror` for this root, exactly as pass A's step 2 does: set `i_run_this_on_purpose` to true in `ROOT/mirror-qual/config.json`, build `build/qualify-mirror` and run it with `--config ROOT/mirror-qual/config.json --out ROOT/mirror-qual/report.json` (prepare prints the commands with the root's paths); or give the preflight `--mirror-report FILE`, an existing report for the same template. The preflight wants that report.
2. **`netcheck.sh`, by hand.** `sh ROOT/netcheck.sh` is for you to run; prepare,
   the preflight and the run script never run it, and no agent does. It starts
   pasta and bubblewrap in the profile's shape (the root's own pasta copy and
   the real bubblewrap) and, for each of `api.anthropic.com`, `claude.ai`,
   `platform.claude.com` and `console.anthropic.com`, resolves it and makes one
   unauthenticated GET (any HTTP status is a pass; a resolve or connect error is
   the finding). It then runs `claude --version` in that shape with the install
   folder bound, and again under `systemd-run --user` with the unit's limits,
   and prints one line, `PASS` or `FINDING`, per host and per version check.
   It makes no other request.
3. **Preflight.** `sh qualification/canary/run/canary-preflight.sh --root ROOT`.
   For a pass B root it also checks that the claude copy is a regular file under
   the install folder, that its `--version` prints 2.1.325, that `agent.env`
   names `DISABLE_AUTOUPDATER` (names only, from `ataniq-canary show`; no value
   is read) and that `footer_mode` is `manual` or `auto`; its last lines name
   `--pass B`.
4. **Run.** `sh qualification/canary/run/canary-run.sh --pass B --config ROOT/config.json --evidence ROOT/evidence`
   (read its dry run first). `canary-reset.sh --root ROOT` puts a pass B root
   back as it does a pass A one.

Advice from the pre-mortem: answer the agent's approval prompts with a plain
Yes, never "do not ask again"; keep the console window large (100 by 30 or
more) and do not resize it once step 16 has been read.

If you make the files by hand instead, keep this order: the inputs file first
(it names `sources.agent_install`, the folder that holds the claude copy, and the
terminal values), then the config, and the pin last, with `profile-digest` and
then `check-inputs`; a change of `agent.env` changes the digest. `argv[0]` is the
executable itself, `/ataniq/agent/claude`, and never a wrapper.

### The preflight

```sh
sh qualification/canary/run/canary-preflight.sh --root DIR [--bin DIR] [--evidence DIR] [--mirror-report FILE]
```

`canary-preflight.sh` looks at what the run would trip over and says what to
type. It is read-only: it starts nothing (no unit, daemon, sandbox or agent session; for a pass B root it runs the claude copy once, with `--version`),
answers nothing, needs no terminal, and changes nothing of the root, the checkout
or the host (git does not even refresh the checkout's index). Its one file is a
temporary copy of the list `qargi local list` prints, in a folder made with
`mktemp` under `TMPDIR` and removed at the end. It prints one line per check,
`ok:` or `FIX:` with the exact command to type on the same line (`skip:` for a
check that needs a fix before it), and ends with `READY` or
`NOT READY (n to fix)`; the exit status is 0 when ready, 1 when not and 2 for a
wrong command line. The checks:

| Check | A fix means |
| --- | --- |
| The root is prepared (the mark and `config.json`) | Run `canary-prepare.sh` on the root when it is new or empty; a folder that holds anything is refused by prepare, so the fix then says to use a fresh `--root`. |
| The seven binaries are built, the four tagged ones name the tag, and they are built from the checkout's `HEAD`: by the commit `--version` names when it names one (it names none today), else by the time of each file against the newest commit that touches `cmd`, `internal`, `go.mod`, `go.sum` or `third_party`; the line says which | `make canary-build`. |
| The checkout is at `develop` with no tracked change (what step 3 asks) | `git checkout develop`, or look at `git status`. |
| The holder the config names exists and is the built one, and the profile digest pinned in `config.json` equals `ataniq-canary profile-digest` now, and the config shows | Write the new pin and run `ataniq-canary check-inputs` (one command, as prepare does after it pins), or prepare again on a fresh root. |
| The sandbox inputs: `ataniq-canary check-inputs` accepts them, and, for `claude`, they name `sources.managed_marker` and the managed-settings file the config names holds that text (the holder refuses a start without it, and neither shows in the digest). Skipped while the pin above needs a fix | The inputs are from an earlier prepare: use a fresh root (the line says why). |
| For a pass B root (`agent_kind` `live`): `agent.footer_mode` is `manual` or `auto`; `agent.env` names `DISABLE_AUTOUPDATER` (names only); the claude copy is an executable regular file under `sources.agent_install` and prints the tested version for `--version` (run with an empty environment and a new empty home, as step 9 does; it is the one program besides the kit's own that the preflight runs) | The command is printed (edit `footer_mode` in the config, or prepare a fresh root with `--pass B --claude PATH`). |
| `state/` and `runtime/` are empty (a mirror only) | They hold an earlier attempt: run `canary-reset.sh`. |
| No `ataniq-*` unit is loaded (slices left out) and no `ataniqd` or `ataniq-holder` runs | Stop it. |
| No open Qargi run of this repository, read with the run script's own helpers through its `--self-test` entry. Without `qargi`, with a list it cannot read, or with an open run whose `repo.path` is unreadable, the line is `ok:` and says that the run asks the lead | Verify and close the run. |
| `/ataniq` does not exist | Remove it yourself. |
| The report of `qualify-mirror` exists, parses and names the template's hash (the path: `--mirror-report`, else the hint in `answers.hint`, else `mirror-qual/report.json`) | Run `qualify-mirror` (the commands are printed; the acknowledgement stays yours). |
| `systemctl`, `loginctl`, `git`, `bwrap`, `pasta` and `ss` are on `PATH`, the sysctl `dev.tty.legacy_tiocsti` is 0, lingering can be read | The command is printed. |
| `timeout` (coreutils) is on `PATH`: the run cuts off each ask of the daemon's status with it | Install coreutils, or put `timeout` on `PATH`. |
| The evidence folder (default `ROOT/evidence`) is writable | `mkdir` it or `chmod` it. |

When ready it prints the three things you type, with the real paths and the
terminal of each: the daemon command in T2, the attach command in T3 (with its
ticket file, `ticket` in the holder's runtime folder; start it before the Enter
at step 14, which mints) and the run command in T1.
Four terminals are needed, because the daemon stays in front of its terminal
until you stop it with Ctrl-C at step 26: T1 the script, T2 the daemon, T3 the
console, and T4 for the second console of step 14 and the integrate command of
step 20.

### Reset

```sh
sh qualification/canary/run/canary-reset.sh --root DIR --dry
sh qualification/canary/run/canary-reset.sh --root DIR [--bin DIR]
```

`canary-reset.sh` puts a failed or finished attempt of a root that carries the
mark of a finished prepare back to a clean slate for a rerun (a half-made root
has no mark and is removed by hand). It is run by your hand, in your terminal: it refuses without a
terminal on standard input (`--dry` needs none). It refuses when any `ataniq-*`
unit is loaded (slices are left out; a `systemctl` that fails is a refusal
too), when an `ataniqd` or `ataniq-holder` of this root runs (a path of the root
in its arguments, as written or with links resolved and relative paths taken
from the process's folder, or the root or a folder below it as its current
folder; one of yours whose current folder cannot be read counts as of this
root, and a `/proc` that cannot be read is a refusal), when the root does
not carry the mark that its own `canary-prepare.sh` wrote, and for a root with
the refusals of prepare (a link, `/tmp`, the checkout), or a `state/` or
`runtime/` that is a link or resolves outside the root. It deletes only the two
roots, `ROOT/state` and `ROOT/runtime` (the provisioned state, and the mirror
inside `state/`). It keeps `config.json`, `upstream`, `bin`, `conf`,
`evidence`, `mirror-qual`, `agent-install` and everything else of the root.

It first shows the two folders, the number of entries below each and the
entries down to three levels, and what it keeps. It deletes only after you
type the word `reset` and Enter on the terminal; any other answer, an end of
input included, stops it with nothing deleted. The script never answers that
prompt. The prompt can wait, so the checks (the units, the processes, and that
`state/` and `runtime/` are still folders of the root, not links) are made again
after the word and before the first deletion. It then enters the root once
(links resolved, and it must be the path you gave) and names `state/` and
`runtime/` relative to that folder from there on, so a root swapped for a link
while the checks run makes it refuse. That the folder entered is still the one
the root's name leads to is checked by asking the kernel, each time: the inode
number of `.` against that of `ROOT/.`, and the root must resolve to itself. It
is not checked by comparing the output of `pwd -P` with the root, which `dash`
answers from the path it cached at the last `cd -P` and not from the kernel.
Nothing is deleted unless it holds, and nothing outside the root is ever
deleted. The folders' modes are opened with `find`, which does not follow a
link; a change in the meantime is a refusal with nothing more deleted. It then makes `state/` and `runtime/` again (mode 0700) and the mirror
with `ataniq-intake init` and `develop` pushed from `ROOT/upstream`, with the
code `canary-prepare.sh` uses, and computes nothing else: the config and the
pin stay as they were. That step is checked, not fully pinned: the `mkdir`,
the `chmod` and the push into the mirror name their folders relative to the
folder entered, but `ataniq-intake init` needs an absolute `--state`, so the
reset checks again that `ROOT/state` is still where it was and that the folder
entered is still the one `ROOT` names, right before that call. A process of your own user that swaps the root for a
link within the few milliseconds between that check and that call could still
make the folders of an empty mirror outside the root; it deletes nothing.
With `--dry` it prints the plan and deletes nothing.
After a failed pass, stop what is running first
([Undo by hand](#undo-by-hand)); the reset refuses while a unit is loaded.

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
  compares the copy's hash with the built one, and does the same for the probe
  the inputs file names (`executables.probe`, a copy of `build/canary/ataniq-probe`
  that is not below the checkout, which `never_mounted` keeps out of the sandbox). For pass B the config is a
  second file with `"kind": "live"`, the live agent's version, footer mode,
  environment and arguments, the model argument among them: step 15 prints the
  arguments and the owner confirms the model. The install folder is not in the
  config: it is `sources.agent_install` of the inputs file, and `argv[0]` is
  `/ataniq/agent/` and the name of the executable in it (no wrapper: step 9
  strips that prefix and runs the file, and `check-inputs` refuses a name that
  is not a file there).
- `agent.footer_mode` is the recogniser's name of the mode the footer must
  show, `manual` or `auto` for claude 2.1.325, not the footer's text. A live
  agent with `inject` true is refused at `show` and `check-inputs` with any
  other value.
- `agent.env` is the agent's whole environment and is part of the profile
  digest: for the live claude it is `["DISABLE_AUTOUPDATER=1"]`, so that no
  update notice reaches the screen the fence reads. The pin comes last.
- `holder.profile_digest` is the pinned digest of the profile, 64 hex digits;
  the holder refuses a plan whose digest differs. Compute it with
  `ataniq-canary profile-digest --config CONFIG --inputs FILE`, where FILE is
  the config's `holder.sandbox_inputs` (the command refuses another file).
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
    "probe": "/srv/canary/bin/ataniq-probe"
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
  "never_mounted": ["/srv/checkout", "/srv/sample/dot-ssh"],
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
- `managed_marker` is required for the runtime `claude`: the holder's start
  refuses without it (validate-config), and its assertion 8 searches the
  managed-settings file for this text, so the file must hold it. It is the name
  of the guard hook inside the sandbox, which the file names as its PreToolUse
  hook command: `canary-prepare.sh` writes
  `{"permissions":{"defaultMode":"default"},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/ataniq/qargi/guard-hook"}]}]}}`
  and the marker `guard-hook`. The marker is not part of the profile digest, so
  adding it to an inputs file needs no new pin, but changing the managed-settings
  file changes `managed_settings_sha256` and the digest. `check-inputs` and
  `profile-digest` read the file `sources.managed_settings` names, the lead's copy, once provisioning has made it, and before that (as prepare asks for the digest) the file `provision.managed_settings` names, which is the file provisioning copies. Both run the holder's trial of the spec (`checkTrial` in
  `internal/leadcanary/provision/inputs.go`), which is the refusal the holder's
  start would make; the preflight still looks for the marker itself. An inputs file from an earlier prepare lacks it:
  use a fresh root.
- `qargi_records_inside` is `/ataniq/records`, the inside path provisioning
  prepares the folders for.
- `never_mounted` lists what no bind source may touch: your checkout (so no executable of `build/canary` can be a bind source: the probe is a copy outside it), the real
  provider folders, `.ssh` and the like. The paths shown are placeholders.

## The evidence folder

The script makes `<DIR>/canary-pass-<A|B>-<UTC time>` with mode 0700 and never
publishes it. Choose `--evidence` so that the path names nothing private: the
intake's verify sandbox can see the path of its work folder, which is made in
the evidence folder for the length of step 20. It must not lie in the checkout
or in a root of the canary: the script compares the paths as written and with
every symbolic link resolved (`cd -P`), so a link that leads into one of them
is refused too, and it takes no path with a `.` or `..` part. It holds:

| Name | Content |
| --- | --- |
| `log.txt` | Every step, key, command, check and result, each with its time. |
| `answers.txt` | Each answer the owner or the lead gave, one line with the time, the step, the question, the answer and how it came: `typed`, or `hint` when an empty line took the suggested path of a path prompt. |
| `out/` | The standard output and standard error of every command the script ran, by step. Step 22 adds `22-e<epoch>-unit-journal.out`: the unit's journal since its first start (`journalctl --user --unit UNIT`, at most 500 lines), which is where the holder's standard error goes, so the reason a holder ended at its start is in the evidence. It can name the canary's folders, like the rest of the evidence, and step 26's scrub looks through it. |
| `facts.txt`, `template-records.txt`, `binaries.txt`, `sandbox-executables.txt`, `standin-hash.txt` | Names, sizes, times and hashes of the reports, the binaries and the executables. Never content. |
| `template.sha256` | The hash of the intake template. |
| `units-before.txt` and the `units-after` and `unit-files` lists | The user manager's `ataniq-*` units and unit files at step 1 and after each stop. |
| `message-1.txt`, `message-2.txt` | The body of each injected message, with its token. |
| `owner-enter.txt` | The `owner-enter` line of step 17, when the one result that gets it occurred. |
| `intake-records/` | The verification and intake records of step 20. |
| `kept/` | Copies made at step 26: the registry journal, the holder's event log, state file and final record, the daemon's evidence file, the stand-in's `order.txt` and `result.txt`. |
| Pass B: `listeners-*.txt`, `departures.txt`, `live-config-folder.txt`, `manifest-before.txt`, `manifest-after.txt`, `manifest-changes.txt`, `canary-config-*.txt` | The listening sockets before and after, the line that records the open network as a departure from L3, and the manifests: names, sizes, times and hashes. |
| `result.txt` | `pass A: complete` or the step the pass failed at, and the number of findings. |
| `MANIFEST.txt` | Names, sizes, times and hashes of every file of the evidence. |

The script never prints or copies the content of a credential or of a file of
the live runtime's config folder: the manifests hold names, sizes, times and
hashes. The owner's own running sessions write that folder all the time, so
step 25 counts what was added, removed and changed, lists the names that differ
at the top of the folder, and judges only the files the login itself would
write there (a credential, token, secret or authorisation file, the runtime's
state file and its settings files, at the top of the folder). A difference in
one of those is a finding when its new hash is one the canary's own config
folder holds under the same name (the canary's own login), and so is any new or
changed path under `projects/-work-*` (the sandbox's working folder). Any other
difference is a question to the owner and the lead (a running session of his
may have refreshed its token): their `yes` is not a finding, and the result line
names the files they explained. When the canary's login wrote no file of its
kind into its own folder, nothing proves another session wrote the difference,
and it is a finding without the question. It reads the canary config only through `ataniq-canary show`, which
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
   run with those folders bound. Then do 1 and 2 above first. Nothing else of
   the host was written: the evidence folder is yours to keep.

In pass B, also: log out inside the sandbox if the pane still runs, revoke the
login on the provider's account page whatever the logout showed, restart the
listeners you stopped, and compare the live config folder with
`manifest-before.txt`.

## What the script does not check itself

- The holder's status answer. The script has no client for the holder's
  control socket, and no command of the kit reads that answer. So at step 16
  the lead reads in the console what the idle field shows (the holder reports
  `unknown` in this feature, and the fence judges idle itself inside a
  delivery, so the script records the answer and does not judge it) and what
  the OSC 7501 counters (queries, reports, unreadable payloads) show, and the
  script records what he reads; it says so plainly and shows no counter itself
  there.
  While the holder runs the counters are only in that answer: the event log
  holds none. After the child ends they are in `final.json` in the holder's
  state folder, and step 22 prints them from it, or says that none is named
  for 7501 or that there is no final record.
- That `hello` lists the canary operations (step 10) is the lead's reading.
- That the console of the first epoch is closed before the second epoch's step
  14. The script tells the owner to close it, and cannot see whether he did; a
  console left open waits on the same ticket file and takes the second epoch's
  ticket for itself.
- Step 12 waits for the holder's second write of `holder.json`. The holder
  writes the file twice, before its start sequence (with the epoch, no waiver
  and no `sandbox_started`) and after it (with the waiver and `sandbox_started`
  true), and the first write already names the epoch. The wait ends when the
  file names the epoch of this start and `sandbox_started` is true, or when the
  unit is no longer active or activating, or after 90 asks one second apart (more
  than the 60 seconds of the inputs' report limit). When it ends without the
  second write, the script prints the tail of the unit's journal and fails the
  step, so the holder's reason is on the screen.
- A stop may leave no final record. The unit's `TimeoutStopSec` is 2 seconds
  and the stop's SIGTERM to every process of the unit is followed by SIGKILL
  after 2 seconds, while the holder waits up to 2 seconds for the stream and 5
  for the tree after its child's exit. A holder that needs more than 2 seconds
  to conclude is killed before it writes `final.json`. The script copes with the
  missing file (it says the holder left no final record), so pass A does not
  fail, but the 7501 counters of step 22 are then not printed. Reasoned, not
  seen.
- The script does not run git in the lead's tree after a lead has run in it.
  The commit is observed through the intake at step 20.

## The check of the kit

```sh
sh scripts/go-tool.sh test ./tests/ -run CanaryRunKit
```

It reads the Makefile target and the `AGENTS.md` lines, runs `sh -n` on every
script of the kit (the run script, the two helpers, the preflight and the
library; it checks that each is plain POSIX shell, and `shellcheck -x -s sh
canary-*.sh` from this folder is clean), and runs the run
script with `--dry` or `--self-test` only, with a `PATH`
that holds stubs which record being called: none may be. It starts nothing.
The tests of the polling, of the asking again and of the hints give the script's
own functions a stub `ataniqd` that counts its calls (it answers on the third
call, or never), the keys on standard input and a short wait, through the test
entries below; the preflight runs over a fake checkout (a git repository with
copies of the scripts and chosen commit times) and a prepared root, with every
program it asks stubbed, and must leave both as they were.

The two helpers are run in a folder of their own outside `/tmp` (your cache
folder, else `/dev/shm`) with stub binaries that record their calls. Prepare
runs for real there with the stubs of `ataniq-intake` and `ataniq-canary` and
makes its files; the reset runs with `--dry`, with its refusals, and through
`--self-test`. The typed word is tried under `script(1)`, which gives the
reset a terminal; that test is skipped where `script` is not there. The same
way a unit, a stand-in `ataniqd` or a link in place of `state/` or `runtime/`
appears while the prompt waits, and the reset must refuse after the word and
leave the folder outside the root as it was, modes included. A stub
`systemctl` that swaps the root, or `state/`, for a link on its second call
(after the checks of `state/` and `runtime/`, before the deletion) is refused
the same way. A stub `mkdir` that swaps the root, for a link or for a folder of
its own, just before or just after the reset makes `state/` and `runtime/`
again is refused too, and nothing is made outside the root. A stand-in
`ataniqd` (`sleep` under that name) in the root also refuses the real run.

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
| `--self-test has-line FILE PATH=VALUE...` | `yes` when one line of `FILE`, which holds one JSON object in each line, holds every `PATH=VALUE` as a whole line of what `ataniq-canary fields` prints for it, and `no` otherwise; a line that is not a whole JSON object is passed over. Step 12 finds the lines of its epoch with it. |
| `--self-test open-runs LIST REPO`, `unread-runs LIST`, `list-scan LIST`, `loaded-units FILE`, `tracked-changes FILE`, `unit-facts FIELDS`, `plan-facts FIELDS`, `holder-start FIELDS VERSION DIGEST`, `upstream-id FILE NAME`, `lead-id FILE ROLE BRANCH` | What steps 1, 2, 3, 8, 12 and 20 read in place of the lead's word: the open runs of the project in a `qargi local list` text and the open runs whose `repo.path` cannot be read, the loaded units without slices, the lines of `git status --porcelain` that are not untracked, `ok ...` or `bad ...` for a fact report or `holder.json`, and the commit a `branches` text names for the upstream branch and for the lead's branch. |
| `canary-prepare.sh --self-test pasta-plan PATH` | `use PATH` for a regular executable file, `copy SOURCE` for a link (SOURCE is the regular file it leads to), or a refusal. |
| `canary-reset.sh --self-test typed-word WORD` | `go` when WORD is exactly `reset`, and `stop` otherwise. The prompt of the reset decides with it. |
| `canary-reset.sh --self-test same-dir ENTERED NAME` | It enters `ENTERED` with `cd -P`, as the reset enters the root, and prints `same` when the folder `NAME` leads to now is that folder, and `different` otherwise: a folder moved away with a new one at the name, a link to another folder or to the folder entered, and a name that leads nowhere are `different`. `ENTERED` that cannot be entered is a refusal (exit status 1). The reset makes that check after it enters the root, after the deletion and right before `ataniq-intake`, with this function. |
| `canary-reset.sh --self-test procs-of ROOT PROCDIR` | The numbers of the processes of `PROCDIR` (the shape of `/proc`) that are an `ataniqd` or an `ataniq-holder` (the program is the whole of the first argument, so a path with a space counts) with a path of `ROOT` in their command line (as written, or resolved) or `ROOT` or a folder below it as their current folder; one of the user whose current folder cannot be read is listed too. A `PROCDIR` with no process folder is a refusal (exit status 1). |
| `--self-test wait-key` | It waits for one key as a step of the clean-up chain does, with the chain's answer to a signal and its standard input taken for a terminal, and prints `key` when a line was read or `no-key` when the input ended. |
| `--self-test pass-a-record FILE` | What step 2 of pass B reads in that `result.txt` of pass A: `clean`, `findings <N>`, `unknown` (complete, with no number of findings) or `incomplete`. It reads the file and nothing else. |
| `--self-test outside-roots FOLDER ROOT...` | `outside` when the folder, which need not exist, lies outside every root, compared as written and with symbolic links resolved, and `inside` otherwise. The script checks its evidence folder against the checkout and the canary's two roots with it. |
| `--self-test counters-7501 FIELDS` | The lines of `FIELDS`, which are what `ataniq-canary fields` prints for a final record, that are counters named for 7501, as step 22 prints them, or the line that says there is none. |
| `--self-test mirror-remote URL PROJECT` | `yes` when `URL` (with or without `file://`) is the mirror's place inside the sandbox for `PROJECT` (`/ataniq/mirror/PROJECT.git`), the one remote step 8 accepts for the lead's tree, and `no` otherwise. |
| `--self-test unit-stopped STATE` | `stopped` when that active state of the unit lets step 26 show the command that deletes the roots (`inactive` or `failed`), and `not-stopped` for every other one, an empty one too. |
| `--self-test journal-save EVIDENCE START UNIT` | What step 22 keeps of the unit's journal: it asks the `journalctl` on `PATH` (a stub in the tests) for `UNIT` since `START` and writes the lines to `EVIDENCE/out/22-e1-unit-journal.out`, a folder that exists; it says how many lines, or that the journal could not be read; with an empty `START` (no start was asked) it does nothing. |
| `--self-test count-lines FILE PATH=VALUE...` | How many lines of `FILE`, which holds one JSON object in each line, hold every `PATH=VALUE` as a whole line of what `ataniq-canary fields` prints for it; a line that is not a whole JSON object is passed over, and a file that is not there counts 0. Step 14 counts the `attach` events of the holder's event log with it, before and after the second console. |
| `--self-test holder-wait HSTATE EPOCH UNIT` | Step 12's wait for `holder.json`: it prints `result=ok end=started` when `HSTATE/holder.json` names `EPOCH` and has `sandbox_started` true (the `ataniq-canary` of `build/canary` of the current folder reads it, and the `systemctl` on `PATH` answers the unit's state), and otherwise prints the unit's journal and `result=failed end=ended` (the unit is not active or activating) or `end=timeout` (after 1.5 times `CANARY_RUN_WAIT_TRIES` asks, `CANARY_RUN_WAIT_INTERVAL` seconds apart). |
| `--self-test ticket-taken FILE` | `taken` when the ticket file `FILE` is absent or empty (the console empties it when it takes the ticket), `waiting` when it holds something, and `waiting` for an empty name: step 14 never says "the ticket was taken" for a ticket result that names no file. Only the size is looked at. |
| `--self-test wait-daemon` | Step 10's polling: it asks the `ataniqd status` of `build/canary` of the current folder as the step does and prints `listens` when it answered within the wait, `silent` when not; "waiting for the daemon to listen..." is said once and a dot follows every 5 asks. |
| `--self-test stop-daemon` | The daemon's half of step 26: the keys come from standard input, a daemon that still answers is said and asked again up to 5 more times (an end of input is a failure too when a daemon still answers), and the end prints `stopped` or `failed` (the failure that ends the step is printed and returned, not ending the run). |
| `--self-test roots-deleted STATE RUNTIME` | The roots' half of step 26 the same way: `deleted` or `failed`; a root that is still there is named and asked again up to 5 more times. |
| `--self-test ask-path HINTFILE KEY` | A path prompt with the hint of `KEY` in `HINTFILE` (it may not exist), the answer read from standard input: prints `answer=` and `source=hint` or `source=typed`. |
| `--self-test affirm-word HINTFILE WORD` | A typed word asked as every word of the run is, with the hint file in place: `accepted` for exactly `WORD`, `refused` for anything else, an empty answer and the end of the input included. |
| `--self-test review-verdict FILE HASH`, `mirror-verdict FILE HASH` | What step 4 makes of `FILE` as the template's review or as the report of `qualify-mirror` for the template hash `HASH`: `ok TEXT` or `bad REASON` (uses the `ataniq-canary fields` of `build/canary`). |
| `--self-test ask-file HINTFILE KEY review\|mirror HASH` | A path prompt of step 4 with its refusals and asking again, the paths read from standard input: prints `accepted PATH` or `failed`. |

Three variables are for the tests only, and only the test entry reads them.
`CANARY_RUN_WAIT_TRIES` (default 60), `CANARY_RUN_WAIT_INTERVAL` (seconds,
default 1) and `CANARY_RUN_STATUS_TIMEOUT` (seconds above 0, default 5) set how
long a step waits for what the owner does in another terminal: that many asks,
that many seconds apart, each ask of the daemon's status cut off after that many
seconds; a value that is not a number is refused with exit status 2. They are
read by `--self-test` and by nothing else: a real run and a `--dry` run keep 60,
1 and 5 whatever the environment holds, so nothing in the environment can
shorten what the script waits for. For
`canary-preflight.sh`, `CANARY_PREFLIGHT_PROC` (default `/proc`),
`CANARY_PREFLIGHT_TIOCSTI` (default `/proc/sys/dev/tty/legacy_tiocsti`) and
`CANARY_PREFLIGHT_ATANIQ` (default `/ataniq`) name the places it looks at on the
host, which a test cannot make.
