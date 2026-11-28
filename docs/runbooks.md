# Runbooks for the owner

This guide is for the owner, at any hour, with a tired head. Each runbook is a
list of plain steps. A step says what to do, what you see, what that proves and
what to do if it does not work. It does not explain the design: the other guides
do, and each step names the one that holds the detail.

It describes only what exists today: scratch leads, the console's leads view,
`ataniqd run --scratch`, `ataniq-canary scratch-init` and `scratch-clean`, and
the canary run kit's owner-only scripts. Everything here is provisional and
unqualified, and it all runs on throwaway repositories (see [the scratch leads
guide](scratch-leads.md)). **A step that depends on something that is not built
is labelled "Not built yet".** Read those as a plan, not as something to type.

## Read this first

**What is not built.** These do not exist, so no runbook below can use them:

- Not built yet: a production mode of the daemon. Only the tagged scratch mode
  (and the canary mode) can define and start a lead. A release daemon is
  read-only.
- Not built yet: a supervisor, and a unit or a start at boot for the daemon. You
  start the daemon by hand, and after a reboot no lead starts by itself.
- A live cutover from the tmux-manager to Ataniq is unavailable. Section 5
  describes the assignment ledger and the evidence it requires.

**Words and placeholders.** The paths below are samples. Use your own.

| Placeholder | What it is |
| --- | --- |
| `<checkout>` | The repository checkout the binaries were built in. |
| `<config>` | The scratch config file (mode 0600), the one you pass to the daemon. |
| `<state root>` | The config's `state_root`. |
| `<runtime root>` | The config's `runtime_root`. It holds `daemon.sock`. |
| `<key>` | A lead's key in the config, such as `alpha`. |
| `<unit>` | The lead's unit: `ataniq-lead-scratch-<key>.service`. |
| `<layout role>` | `scratch-<key>-lead`: the name of the lead's folders. |

The binaries are the ones `make canary-build` puts in
`<checkout>/build/canary/`. A release binary has no scratch mode. The commands
below are run from `<checkout>`.

**Which terminals.** The daemon stays in front of its terminal until you stop
it, and writes its log, when it has something to say, to that terminal's
standard error. It writes no log file. Keep one terminal for it and do not close
it. Use a second for the console and a third for anything you type by hand.

**The one picture to keep in mind.** There are three layers, and they fail
separately:

- the **lead's own processes**: the unit, the holder and the agent, in the
  user's systemd manager. They are not the daemon's children. A daemon that goes
  away does not stop them;
- the **daemon**, which keeps a registry of what each lead is, a table of the
  commands it took and one evidence file per lead, all in `<state root>`;
- the **console**, which only asks the daemon and shows what it says.

When two layers disagree, the lead's processes are the fact and the daemon's
list is what it believes. Look at both.

**What the scratch lead's `ready` means.** The holder is up and the agent was
started. It does not mean the agent is logged in or will answer.

## 1. After a crash or a reboot of the host

Do these in order. The first two steps only read.

### What survives

After the **daemon** was killed or crashed, with the host still up:

- The lead's unit, the holder and the agent keep running. Nothing stops them.
- `<state root>` stays: the registry journal, the command table
  (`lead-commands.jsonl`), the evidence file of each lead
  (`evidence/<layout role>.jsonl`), the mirrors, and the lead folders.
- The daemon's lock files stay as files. What locks is the open lock on the
  file, not the file, so the daemon is free to start again. A socket file may
  stay too. The next daemon removes it when it holds both locks.
- A command that was running when it died ends as `uncertain`
  (`interrupted`) at the next start. The console never sends it again for you.

After a **reboot** of the host:

- Every unit is gone, so the holder, the sandbox and the agent are gone, and the
  agent's screen with them.
- No lead starts by itself. The daemon does not start at boot either.
- `<state root>` stays, with everything listed above. So does the holder's own
  record of the last run, its `events.jsonl` and `final.json` (see [the lead
  holder guide](lead-holder.md#evidence)).
- The runtime root holds sockets and the ticket file. Those are stale after a
  reboot. If your runtime root is on memory-backed storage, the lead's folder
  under it (`<runtime root>/leads/<layout role>`) is gone too, and a start is
  then refused `folder-missing` ("a folder of the lead is missing or not
  private"). No guide gives a tested recovery for that, because a second define
  does not make the folder again. Keep the runtime root on storage that survives
  a reboot, as the sample config does. If it did not, go to section 6.

### 1.1 Look at the units first

- Do: `systemctl --user list-units --all 'ataniq-*'`
- You see: after a reboot, nothing. After a daemon crash, the leads' units
  (`ataniq-lead-scratch-<key>.service`) still `active`.
- It proves: whether any lead's processes are still alive. That is the fact the
  daemon's list will be compared with.
- If it does not work: if `systemctl --user` fails, the user manager is not
  answering. Do not start anything. Go to section 6.

### 1.2 Ask whether a daemon is already running

- Do: `build/canary/ataniqd status --socket <runtime root>/daemon.sock`
- You see: either one `daemon` line and one `role=` line per lead, or a
  message that no daemon answers.
- It proves: with the lines, a daemon is running and you need not start one.
  Exit status 20 means none answers (the socket is missing or refused).
- If it does not work: exit status 21 means the daemon refused your user, and 22
  an answer that is not valid. Do not start a second daemon. Go to section 6.

### 1.3 Start the scratch daemon

Only if step 1.2 found none. For a new scratch root, provision the marker with
`build/canary/ataniq-canary scratch-init --config <config>`. With the daemon
stopped, initialize a new or pre-identity root once with
`build/canary/ataniqd init --state-dir <state root>` using the config's
`state_root`. Record the authority and incarnation. Back up an existing root
before init if you may need to run the older build again.
Then run
`build/canary/ataniqd project-home --state-dir <state root> --home self scratch-<key>`
for each local lead in the config. Use the same enrollment for new lead keys
added later, while the daemon is stopped. A restart keeps the recorded homes.

- Do: `build/canary/ataniqd run --scratch <config>` in the daemon's terminal.
  Leave it there. Do not run it in the background of a terminal you will close.
- You see: the daemon prints a line when it listens, and nothing else while all
  is well.
- It proves: the config, the roots and the root marker passed, the registry and
  the command table were read, the one reconcile ran, and the socket is bound.
- If it does not work: read the exit status.

| Exit | Meaning and what to do |
| --- | --- |
| 10 | The config, a root or the root marker is wrong. Read the line it printed. A new place needs `build/canary/ataniq-canary scratch-init --config <config>` first (it keeps what is right and writes the marker). |
| 11 | Read the named file and reason. The instance identity, an evidence file, a lead's record or the command table could not be loaded. Nothing is repaired. Do not set the file aside while any lead's unit runs (step 1.1): a new file forgets what was sent or admitted. Go to section 6. |
| 12 or 17 | Another daemon holds the lock or serves the runtime folder. Run step 1.2. Do not start another. The pid in the lock file is a hint, not proof. |
| 13 | The daemon lock could not be taken, for a reason other than another daemon holding it. Read the line it printed; if it is not plain, go to section 6. |
| 14 | The registry is damaged or locked by another writer. Nothing is repaired. Go to section 6. |
| 15, 16 | The unit manager could not be set up, or the reconcile failed in the registry. Check `systemctl --user` works, then start the daemon once more. If it repeats, go to section 6. |
| 18 | The server stopped by itself after it was serving. Read the line it printed and the log, check step 1.2 and step 1.4 before another start, and if it repeats, go to section 6. |
| 19 | This is a shutdown, not a start: see section 4. |

### 1.4 Read what the daemon believes

- Do: `build/canary/ataniqd status --socket <runtime root>/daemon.sock`
- You see: a `daemon` line with `reconcile=completed` (or `incomplete`), and for
  each lead a line like `role=scratch-<key>/lead kind=lead state=stopped
  reason=host-rebooted epoch=1 desired=stopped manager=ataniq`.
- It proves: what the registry holds, after the daemon compared it with the unit
  manager once. Read the state and the reason together:

| You see | It means | Do next |
| --- | --- | --- |
| `stopped`, `host-rebooted` | The host rebooted. The unit is proven gone. | Start the lead again when you want it (step 1.5). |
| `ended`, `holder-gone` | The holder's unit is proven empty on the same boot. | Same. |
| `starting` or `ready` and the unit is active (step 1.1) | The unit of ours was adopted. The shown state is kept and never raised by the reconcile. | Look at it in the console. A `starting` lead is looked at again by the daemon for up to 60 s. |
| `unknown` | The daemon cannot say what the lead is doing. See step 1.6. | Step 1.6. |
| `desired`, never started | Defined, never started. | Nothing. |

- If it does not work: `reconcile=incomplete` means the unit manager was
  unavailable or did not answer for a lead, or the reconcile ran out of time (its
  bound is 30 seconds). Nothing was changed for that lead. Check `systemctl --user list-units`,
  then restart the daemon (section 4 says how to stop it).

### 1.5 Open the Leads view

- Do: in the console's terminal, `build/canary/ataniq-console --daemon <runtime
  root>`, then press `L` (capital).
- You see: a table with the columns KEY, STATE, EPOCH, MSG, LAST RECEIPT and
  UNIT, one row per lead in the config, and the daemon's `max_running` with how
  many leads count against it. A `*` after a state means a command for that lead
  has no answer yet. A line under the table explains the row under the cursor.
  Move with `j` and `k`, close the view with Esc, quit with Ctrl+C.
- It proves: the console reached the daemon and the daemon serves lead control.
  The console asks once, when it starts: if you started the daemon after the
  console, start the console again.
- If it does not work: "no lead control in this daemon" means the daemon is not
  a `--scratch` daemon of a tagged build. Stop it and start the right one. The
  console says "no daemon" with the next step when none answers: go back to
  step 1.2.

The keys of the view, for the steps below (from [the console
guide](terminal-console.md#lead-control-scratch-daemon-only)): `d` define, `s` start, `x` stop (then `y`,
any other key keeps it running), `m` message, `w` and `v` attach as writer or as
viewer, `r` read the list again.

**Start it again.** A lead that the host rebooted under is shown `stopped`.
Press `s` to start it. The row goes `starting` and, when the holder answers,
`ready`. A start needs the lead's record and its folders, so a lead that was
defined before needs no define again; if the row says it is not provisioned,
press `d` first. The new epoch has a new message that is free.

### 1.6 What `unknown` means and how it settles

`unknown` is the daemon's cautious answer: it cannot say what the lead is doing,
and a unit may still run. Nothing starts for an `unknown` lead, and it counts
against `max_running`, so a start of another lead can be refused for the limit.
The usual cause after a crash is a daemon killed in the middle of a start. The
reason is `launch-unsettled` or `launch-reply-lost`, and the console says
"launch settles by itself 40 s after the start; if not, stop the lead or
restart". A stop or a start of that lead is refused `intent-pending` meanwhile.

Reading the list does not settle it, and neither does the console.

- Do: wait until 40 s have passed since the start was made. The daemon settles
  it after the 40 s window. If the lead still shows `unknown` after that, stop
  the lead (step 1.7: the way out when the launch's own unit was adopted, which
  leaves the lead `unknown`), or restart the daemon: stop it with Ctrl+C in its
  terminal (step 4.3) and start it again (step 1.3), then read the list. In a
  build from before the daemon settled it by itself, nothing but the restart
  settles it.
- You see: the lead `stopped` and the slot free. If its unit had landed late,
  the daemon reports it (`stale-unit`) and the lead stays `unknown`: use step
  1.7.
- It proves: the daemon closed the launch intent as abandoned, with a proof text
  naming the facts it used, or adopted a unit that proved to be ours.
- If it does not work: while the launch is unsettled a stop is refused
  (`intent-pending`), so wait, restart the daemon and read the list again. Other
  reasons for `unknown` (`registry-recovered`, `foreign-unit`, a competing unit)
  mean something other than a lost start: a stop with proof (step 1.7) leaves
  `unknown`, and if it cannot be proven, go to section 6. Never delete the
  registry to make it go away.

The long form is in [a lead left unknown after a killed
daemon](scratch-leads.md#a-lead-left-unknown-after-a-killed-daemon) and the
rules in [the lead daemon
guide](lead-daemon.md#unknown-and-how-a-role-leaves-it).

### 1.7 Stop a lead, with proof

Stop is the owner's way out of every state, `unknown` included.

- Do: in the leads view, put the cursor on the lead, press `x`, read "Stop KEY
  (epoch N)?" and press `y`.
- You see: the status line "stop: accepted, done" (the LAST RECEIPT column
  reads "stop: accepted (done)") and the row `stopped`. The daemon
  answers `done` only when the stop is proven: the unit's invocation is gone and
  its control group is empty. A refused stop says why (`stale-epoch`: read the
  list and try again; `role-busy`: another command for that lead is running, try
  again in a moment).
- It proves: that the daemon asked the unit's own control group to end and saw
  it empty. It proves nothing about a process that left the unit's control
  group.
- Check it yourself:
  `systemctl --user show <unit> --property=ActiveState --property=ControlGroup`
  should show the unit inactive (or the unit unknown to the manager), and the
  folder named by `ControlGroup` under `/sys/fs/cgroup` should be gone or its
  `cgroup.procs` empty.
- If it does not work: the receipt may read `uncertain` (`stop-unproven`): the
  daemon does not know the unit is gone. Read the lead's row and your own check
  above, then press `x` and `y` again. It is safe to repeat: the stop always
  reaches the unit manager. If the daemon is not running, use step 4.2.

## 2. A lead that looks stuck

Start by reading, never by killing. For any of these, first run step 1.1 and
read the row in the leads view (step 1.5).

### 2.1 The lead stays `starting`

- You see: `starting` for longer than a minute. The view reads the list every
  two seconds by itself while a lead is `starting`.
- It proves: the holder has not answered the daemon's `status` for this epoch
  within the 60 s the daemon asks for. The daemon logs that once and leaves the
  row as it is. It asks again when the daemon is next started.
- Do, in this order:
  1. Step 1.1. If the unit is not `active`, the holder ended. Read why:
     `journalctl --user --unit <unit> -n 40`. The holder says on its standard
     error why it ended, and that goes to the unit's journal. Then stop the lead
     (step 1.7) and start it once more.
  2. If the unit is `active`, press `v` to attach as a viewer and read the
     agent's screen (step 2.5 has the keys). A login prompt, a dialog or an
     error is the answer.
  3. If the unit is `active` and the screen looks well, restart the daemon
     (section 4.3 to stop it, step 1.3 to start it). The new daemon asks the
     holder again for every lead shown `starting`. The restart stops no lead.
- If it does not work: if the lead stays `starting` after the restart, stop it
  (step 1.7) and go to section 6.

### 2.2 `ready`, and silent

- You see: `ready`, and a message gets no reply, or the screen does nothing.
- It proves: only that the holder is up and the agent was started. The login is
  not observed and nothing says the agent works.
- Do: press `v`, read the screen (step 2.5). The agent's own message tells you
  if it is waiting at a login, a trust dialog, or an error. Do not type into a
  session you do not need to type into. If the agent needs a login, section 3.
- If it does not work: if the screen is blank or the attach cannot connect, run
  step 1.1. A `ready` lead whose holder went away is not noticed by the daemon:
  it stays `ready` until it is stopped. Stop it (step 1.7), start it again.

### 2.3 A message was not delivered

You press `m`, type, press Enter, and the line under the table says what became
of it. The words come from the holder. The MSG column of the row agrees.

- "NOT delivered: nothing was written, the lead's screen is not idle"
  (`not-now`, `not-ready`): the agent's screen was not ready. Do: press `v`,
  look. Close any dialog and clear the composer, then leave the pane alone for
  about 3 seconds (the check needs 2 seconds of quiet), and press `m` again.
- "... the lead's screen is blocked" (`blocked`): a dialog is waiting for you.
  Answer the agent's own prompt with a plain Yes, never "do not ask again" (the
  run kit's advice), then press `m` again.
- "... the lead's program version is not one the holder is tested with"
  (`untested`): the config does not declare a version of the agent that the
  holder is tested with. Sending again will not help. Go to section 6.
- "... the holder refused the text" (`text`): the text was not accepted. Edit
  it.
- All four leave the epoch's message **free**: the MSG column says `free (not
  delivered)` and `m` works again, with the same message id or another.

What proves the message reached the lead is `sent`, shown only when the holder
said the whole message was submitted. Everything else that may have reached the
lead leaves the epoch's one message **spent**.

### 2.4 A message is spent, and you are not sure

- You see: MSG `sent-unsure`, or a line that says "written but not submitted",
  "result unknown", or `holder-busy`.
- It proves: something may have reached the lead, and nothing says what became
  of it. A second message in this epoch is refused (`second-message`). That is
  on purpose.
- Do: press `v`, and read the screen. If the text sits in the composer and was
  not submitted, you may attach as the writer (`w`) and press Enter yourself.
  After a `died` result, do not press anything: the input may hold an open
  paste. Decide from what you see.
- If it does not work: if you want a fresh message, stop the lead (step 1.7) and
  start it (`s`). The new epoch has a new message. A message the daemon lost
  between the frame and the evidence line (a window of a few milliseconds)
  leaves the epoch free after a daemon restart although the message is on its
  way: read the screen before sending again.

### 2.5 Attach is refused: "a writer may already be attached"

- Do: press `w` for the writer's place, or `v` for the read-only one. The
  console leaves the cockpit and shows the attach screen. Leave it with the
  return key, Ctrl+`\` by default, and then `q`. The cockpit returns with the
  leads view and a fresh list.
- You see: "the holder refused the ticket; a writer may already be attached".
  The holder grants one writer, and one viewer, at a time, and there is no
  takeover. A writer ticket is also refused while another ticket for that place
  is waiting to be used, which it is for up to 30 seconds.
- It proves: nothing more. The receipt carries no reason beyond the refusal, so
  the line says what is likely.
- If it does not work, in this order:
  1. Close any other console you have attached to that lead (return key, then
     `q`), wait 30 seconds, and press `w` once. A lead has one ticket file:
     attach before you ask for another ticket for the same lead.
  2. Use `v`, the read-only place, to look without waiting.
  3. If a console that held the writer place is gone (the terminal closed or the
     process died) and the place stays refused, wait a minute, then go to
     section 6. The guides do not say that a vanished console's place frees
     itself. Do not use `ataniq-scratch release-writer`: it is for the scratch
     agents of `ataniq-scratch`, and no guide says it reaches a scratch lead's
     holder.

### 2.6 A define that never finished

- You see: a define the console refuses with "an earlier define was cut short;
  the owner clears the lead's folders" (`provision-incomplete`), or a define
  that stays `*` for a long time.
- It proves: with that refusal, a define was cut short (the daemon was stopped,
  git ran out of time, the ten minutes ended) and left folders and no
  `provisioned.json`. The daemon's list shows such a lead as `provision:
  "incomplete"`, and a lead whose define is still running in the daemon as
  `none`, never as `incomplete`. A define the console stopped waiting for goes
  on, and its receipt shows `in-progress` until it ends: wait for it.
- Do: stop the daemon (section 4.3). Then run, by your own hand:
  `build/canary/ataniq-canary scratch-clean --config <config> --lead <key>`.
  Start the daemon (step 1.3) and press `d` again.
- If it does not work: `scratch-clean` takes no daemon lock and cannot tell a
  running define from a cut-short one, so never run it while the daemon runs. It
  refuses a lead whose record is there and a lead that has started, and says
  "nothing to remove" for a lead with no folders. Never run it on a lead that is
  provisioned: that is the wrong tool (see section 6). If it fails, go to
  section 6.

## 3. A revoked or expired agent login

This section covers a live agent logged in inside a lead's sandbox. The stand-in
agent has no login. It is written from the canary run's record, the run kit's
README and [the canary slice guide](canary-slice.md#what-pass-b-showed). A
scratch lead with a live agent has no recorded run in these guides, so the steps
are the canary's, applied here and not confirmed on a scratch lead.

### 3.1 What you see

- The leads view cannot see a login. A lead whose login is revoked or expired is
  still `ready` (holder up, agent started).
- Press `v` and read the screen. The agent says on its own screen that it is not
  logged in or that its session ended. The words are the agent's, and no guide
  records them.
- It proves: only what the screen says at that moment.
- If it does not work: if you cannot tell from the screen, do not guess. Go on
  to 3.2 only when you are sure the login is the problem. Otherwise section 6.

### 3.2 The before and after check on the provider's account page

Revocation is unverified so far. In pass B run 1 the account page listed Claude
Code logins as rows named Unknown and none could be matched to the login made in
the pass. The owner believes that login had already been revoked, which is why
nothing matched. That belief is not verified, and whether the page can revoke a
single login is not known. So every login is checked before and after.

1. **Before you log in:** on the provider's account page, write down the rows
   the page lists now. (The kit's step 5 asks for the word `confirmed` for
   this.)
2. **Log in** inside the sandbox: attach as the writer (`w`) and follow what the
   agent shows. The login URL and the code are secrets: do not copy them into a
   file, a message or the evidence.
3. **After the login:** reload the account page. Note the new row and its
   last-active time. It should fall in the time you logged in. (The kit's step
   15.)
4. **To revoke:** revoke only that row. If you cannot tell which row it is, say
   so plainly (the kit accepts `cannot-identify: <what you see>`, and counts it
   as a finding). A separate throwaway account for the login makes this easy: on
   it, "log out of all devices" revokes the login completely.
5. **Confirm after the revoke:** the row is gone from the page. While the lead
   is still running, also press `v` and look (send nothing): the session in the
   sandbox should no longer work. The run kit's step 23 comes after the sandbox
   was stopped, so there only the row can be confirmed.
6. **If the row is still there, or the session still works:** the revoke did not
   take. Do it again in the provider's console, and check again. If it still
   does not work, stop the lead (step 1.7) and go to section 6. Use "log out
   of all devices" only on a separate throwaway account that is used for this
   login alone, never on an account that holds your own sessions.

### 3.3 If the login simply expired

- Do: attach as the writer (`w`) and log in again as in 3.2, with a fresh before
  and after. Whether a login survives a stop and a start of the lead is not
  recorded in these guides, so check the screen after a start before you rely
  on it.
- It proves: the new row on the account page, and a screen that works.
- If it does not work: stop the lead (step 1.7). A lead with no working login
  cannot be used. Go to section 6.

## 4. Stop everything safely

Order matters: leads, then the daemon, then the check. Stop with the daemon's
help when you can, and by the unit manager when you cannot.

### 4.1 Stop each lead through the console

- Do: in the leads view, for each lead that is not `stopped`, `ended` or
  `desired`: press `x`, then `y`. One lead at a time.
- You see: the status line "stop: accepted, done" for each (the LAST RECEIPT
  column reads "stop: accepted (done)"), and the row `stopped`.
- It proves: each stop is proven (step 1.7).
- If it does not work: a stop that reads `uncertain` is not proven: repeat it. A
  stop refused `intent-pending` is a launch not settled: step 1.6.

### 4.2 Stop a lead's unit without the daemon

Use this only when the daemon is not running and you cannot start it, or the
lead is not one of the daemon's config.

- Do: `systemctl --user stop <unit>`. For a lead whose key you removed from the
  config, the unit is still `ataniq-lead-scratch-<key>.service`.
- Then check as in step 1.7: `systemctl --user show <unit>
  --property=ActiveState --property=ControlGroup` and its `cgroup.procs`.
- It proves: the unit ended. The next daemon's reconcile then shows the lead
  `ended` (`holder-gone`) when it can prove the control group empty.
- If it does not work: if the unit stays `active` or `deactivating`, read
  `journalctl --user --unit <unit> -n 40` and go to the explicit-PID rule in
  4.4.

### 4.3 Stop the daemon

- Do: in the daemon's terminal, press Ctrl+C. (Or send SIGTERM to its exact pid,
  see 4.4.)
- You see: it exits, status 0, and the socket is gone. The daemon stops
  accepting, closes its connections (it waits for them for at most 5 s), closes
  the registry and releases its locks.
- It proves: a clean shutdown. It **does not stop any lead**: a lead that ran
  keeps running.
- If it does not work: exit status 19 means the server did not end within its
  grace of 5 seconds. The daemon leaves the registry and both locks to the end of the
  process, and the process ends. The socket is already gone. That is normal
  enough: start the daemon again.

### 4.4 The explicit-PID rule

When you must end a process of your own, find its exact PID and use only that
one. Never use `pkill`, `killall`, `pkill -f` or a pattern of any kind.

- Find the pid from a thing that names it: the unit's `MainPID`
  (`systemctl --user show <unit> --property=MainPID`) or the pid in
  `<state root>/daemon.lock` for the daemon (a hint).
- Check it is the right process: `ps -o pid,ppid,etime,args -p <pid>`. Read the
  line. If it is not what you expected, stop.
- End it: `kill -TERM <pid>`. Wait. Check again. `kill -KILL <pid>` is the last
  step, not the first: see section 6.
- Why: a pattern matches more than you mean. Your tmux sessions and the
  processes the tmux-manager runs are yours and are in use. A reconcile never
  touches a role run by the tmux-manager, and neither should you. The
  tmux-manager and its sessions are outside every runbook here.

### 4.5 Check that nothing of ours is left

- Do: `systemctl --user list-units --all 'ataniq-*'`
- You see: no `ataniq-lead-scratch-*` unit. Then
  `build/canary/ataniqd status --socket <runtime root>/daemon.sock` exits 20:
  no daemon answers.
- It proves: no lead's unit and no daemon is left, as far as the user manager
  knows. A process that left its unit's control group is not seen by this.
- If it does not work: a unit still listed is stopped by step 4.2.

Do not delete `<state root>` or `<runtime root>` to finish. They are not in the
way, and deleting them while a lead runs breaks what the lead has bound.

## 5. The planned handoff and cutover, and the rollback

There is no live cutover procedure. The pure assignment reducer checks the
evidence for each step, and the ledger records the holder, generation and
result under the transfer ID. Daemon admission for handoffs, the effect broker
that would enforce fencing where work starts, and Qargi transfer verbs are
unbuilt. Keep real projects out of the scratch lead workflow.

**Reading a transfer.** A freeze closes admission at generation `g`. An
uncertain inventory or a mismatch in the successor's account blocks reservation.
Once that account matches the completed inventory, the transfer proceeds in
this order.

1. **Reserve** the successor under the owner's decision.
2. **Drain** outstanding work. Each frozen route supplies a receipt that it is
   sealed and its work is settled.
3. **Fence the old holder** once every receipt is present and the inventory is
   still current.
4. **Commit by transfer ID** after rechecking the decision. The successor now
   holds generation `g+1`. Admission stays closed until every route confirms
   it installed the new binding and rejects stale claims, and the reducer
   verifies current enrollment evidence.

**After a lost reply.** The ledger can return the recorded result for the
original transfer and request IDs. Recovery checks the replayed ledger against
the independent witness checkpoint. For an uncertain checkpoint mutation,
`Settle` at the witness uses the original mutation ID and epoch. It returns the
recorded receipt or fences an unused ID against later application. If the
witness is unreachable, the outcome stays unresolved. See the
[witness guide](authority-host-witness.md).

**Returning to the previous holder.** Before commit, the old holder can reopen
only through a verified abort. An irreversibly fenced holder stays closed.
After commit, a rollback is a new higher-generation transfer. It needs a new
transfer ID and owner authorization, drains and fences the current holder, and
commits the previous holder at `g+2`. The ledger preserves the original commit.
It can also record a hold on the stopped, disabled legacy fallback with an
earliest retirement at least 14 days after the hold starts. The hold gives no
permission to restart it.

**Conflicting receipts.** A late correction or contradiction from the issuer of
an accepted fence or confirmation receipt quarantines the key. Neither side may
act. The owner can acknowledge the quarantine once to record a disposition.
The key stays closed in that store. Recovery requires retiring the doubtful
identity and fresh enrollment in a new store. A contradiction from a different
issuer is refused and must be reported to the owner.

## 6. When you cannot tell

Do these in this order and stop at the first that settles it.

1. **Read.** Step 1.1 (units), 1.4 (the daemon's status) and 1.5 (the Leads
   view). Read the daemon's terminal. Read, and write down, what the three say.
   Reading changes nothing and costs nothing. It is the right thing to do for as
   long as you are unsure.
2. **Stop.** If a lead may be doing harm or you cannot tell what it is doing,
   stop it with step 1.7 (or 4.2 without the daemon), and check the proof. A
   stop is the owner's way out of every state, and it is safe to repeat. A
   stopped lead does nothing more, and its state and evidence stay.
3. **Restart.** If the daemon's list looks wrong (a lead `unknown` after the
   40-second window, `starting` for too long, a refused command), restart the daemon:
   Ctrl+C in its terminal (it stops no lead), then step 1.3. The reconcile runs
   once at each start, and a lead that is still running is adopted.
4. **Escalate.** If it is still wrong, stop changing things. Keep the daemon's
   terminal text, the output of steps 1.1 and 1.4, the receipt lines the console
   showed and the evidence file of the lead, and hand them to the lead session.
   Do not write the login URL or code from step 3 anywhere.

**What never to do:**

- Do not delete `<state root>` or `<runtime root>`, or a file in them, by hand
  while a daemon runs or a lead's unit is loaded. That includes the registry
  journal, `lead-commands.jsonl` and the evidence files. The next daemon would
  forget what was admitted and what was sent, and could run a command again or
  send a second message.
- Do not run `scratch-clean` on a lead whose provisioning is complete, nor while
  the daemon runs. It is for a define that was cut short, with the daemon
  stopped.
- Do not use `kill -9` as a first step. SIGTERM first, the exact pid (4.4), then
  look. A stop through the unit manager is better still.
- Do not use a pattern kill (`pkill`, `killall`) for any reason.
- Do not remove a lead from the config while its unit runs. Stop it first.
- Do not run two scratch roots with the same key on one account: they name the
  same unit.
- Do not start a second daemon on the same runtime folder.
- Do not put a real project, a real repository or a real credential in a scratch
  lead.
- Do not treat `ready` as "the agent works", or a stopped row as proof without
  step 1.7's check when the stakes are real.

### The owner-only scripts, by name

These belong to the canary run kit ([its
guide](../qualification/canary/run/README.md)) and read the canary's config, not
a scratch config. None of them is a scratch tool. Each has a rule:

- `qualification/canary/run/canary-run.sh`: starts a real unit, sandbox and
  agent. Run only by the owner's hand, in the owner's terminal, with the lead
  present.
  Never run it by an agent without `--dry` (it prints every step and starts
  nothing) or `--self-test`.
- `qualification/canary/run/canary-reset.sh`: deletes `state/` and `runtime/` of
  a root that `canary-prepare.sh` prepared, after a word the owner types. Run
  only by the owner's hand, never by an agent, and never with the word typed for
  the owner. The agent-safe forms are `--dry` and `--self-test`. It refuses
  while an `ataniq-*` unit is loaded. It does not apply to a scratch root.
- `qualification/canary/run/canary-prepare.sh`: writes files and folders under
  the cache folder. An agent runs it only with `--dry` (or `--self-test`).
- `qualification/canary/run/canary-preflight.sh --root DIR`: read-only; it
  starts nothing, and says `READY` or `NOT READY` with the fix to type.
- `<root>/netcheck.sh`, written by `canary-prepare.sh --pass B`: starts pasta,
  bubblewrap and a throwaway user unit and makes four unauthenticated requests.
  Run only by the owner's hand, never by an agent, and it has no `--dry`.

The build and the test targets are in [the scratch leads
guide](scratch-leads.md#where-the-code-is) and in `AGENTS.md`. This guide has no
code and no test of its own beyond the link check of the documentation.
