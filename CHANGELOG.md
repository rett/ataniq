# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.12] - 2026-11-20

### Added

- Add the console's approvals client for the daemon: `ataniq-console --daemon DIR`
  shows the daemon's approvals when the daemon serves the owner channel. It lists
  the daemon's cards, opens one, reads a path list or a patch larger than one
  response in pages, sends the owner's decision once and shows the outcome the
  daemon gives, and reads and sets the daily limit. The cockpit's approval
  controls and the approvals view follow what the daemon's hello lists. A
  release daemon has no owner channel, so no approval is possible with it: the
  console then says "no owner channel in this build". A lost connection drops
  what was typed and decides nothing, and a decision that got no answer is
  never sent again until the card is opened again. `--no-bell` now also works
  with `--daemon`.
- Add the daemon's side of the owner connection, in a build with the
  `ataniq_ownerchannel` tag only: `ataniqd run --owner-channel` registers the
  `owner.*` operations of the console's approvals through the client protocol's
  extension seam, on the same socket and the same protocol (`ataniq.client/1`).
  The place of the one connection that may decide goes to a console of the
  owner that names itself and has a controlling terminal, checked through an
  injected inspector (the real one reads the process with a pidfd); a second
  console is read-only, and a client with no terminal and a decision frame from
  any other connection are refused and counted. The flag is refused until the
  host's side of a card is built into the command. Release builds have no such
  flag and register no owner operation, and `hello` lists what it lists today.
  The connection that holds the place of the one that may decide is kept
  for up to ten minutes without a request (other connections keep the
  server's 30 seconds), so the place is not lost while the owner reads a card;
  a connection that is not the decider and sends more than 16 decision frames
  is closed, and the frames past the 16th are not journaled. The bound is per
  connection: a process of the same user can connect again.
- Add contract tests of the daemon's owner connection against the real service,
  journal and host adapter, in a build with the `ataniq_ownerchannel` tag only:
  every `owner.*` operation answered over the client protocol and read back
  through the wire package's decoders, the code each failure takes on the wire
  (a connection that is not the decider, a door that cannot be reached, a
  service that stopped), the decision of a stopped service delivered once by the
  next daemon, and a second daemon that cannot open a journal the first holds.
  Nothing changes in a release build.
- Add end-to-end tests of the owner channel for a host with no seated lead, in
  a build with the `ataniq_ownerchannel` tag only: the console's approvals
  model, the console's client, the daemon with its owner connection, the
  service, the real journal, the host adapter and a temporary bare mirror in one
  test, with the stand-in for the planner's door and a fake verification suite.
  One request is followed from the file the lead drops to the ref that moves,
  and the cases that need no seat are run on it: a branch that moves or a
  request that is withdrawn with the card open, a target that moves after the
  decision, a decision used twice, expiry, a stop at each crash point and a
  restart with a held decision, the summary of a known change set and of 5,000
  paths read in pages, missing and surplus sign-offs, a wrong digest, epoch,
  revision or id, a merge made by the lead, restarts of the console and the
  daemon, a journal and a door that disagree, attributes in the tree, failed and
  cut verifications, and hostile commit headers. The confirm code is found in
  no place of the host or on the wire, and the plan token only on the card and
  in the journal's copy of the decision record. Cases that need a seated lead
  or the real door stay for step 2b. Nothing changes in a release build.
- Add the host adapter of the owner channel, in a build with the
  `ataniq_ownerchannel` tag only: the service's interfaces joined to the real
  merge builder, the intake (merge-mode review, summary, one ref transaction
  with the old value, no checkout and no hook), the journal in the daemon's
  state folder, and a verifier that keeps each run as a verification record.
  A merge the host built stays pinned under `refs/ataniq/pending/`, so the
  mirror's own maintenance cannot remove it or its parents; the adapter adds no
  maintenance. A request enters through a file-drop collector that exists for
  the scratch pass only: a request file in a private folder, refused before
  anything is built when it is not a request or its feature name is outside the
  grammar, with a reply file beside it as the only reply this build writes.
  `ataniqd run --owner-channel` now builds the host's side of a card from
  `owner/scratch.json` in the state folder, over a stand-in for the planner's
  door that keeps nothing across a restart. It is provisional and unqualified,
  and no suite runner is installed yet, so the flag is still refused until one
  is. Release builds contain none of it.
- The intake can review a merge the host built, in a new merge mode of the
  `internal/intake` package: the base is the target's tip, the tip is the
  merge under a pin in `refs/ataniq/pending/`, and `Integrate` moves the target
  from the one to the other and writes no other ref. It adds a fuller summary
  for the owner's approval card (binary by content, forced-text line counts
  with renames off, new bytes, and lists of binary files, symbolic links, mode
  changes and submodules), an `odd-path` flag for paths the matcher cannot read
  safely, a check of the repository's `info/attributes` before each diff of a
  merge, and a digest of the execution-config rule set. No command uses it yet,
  and the lead-branch review is unchanged.
- Add `make test-ownerchannel`, which runs the tests of code behind the
  `ataniq_ownerchannel` build tag, alone and together with `ataniq_canary`, as
  a third CI job, and a guard in `tests/` that keeps that tag out of release
  builds: tagged files only in the daemon, its command and the host adapter,
  no command depending on the owner channel's service, journal, host adapter
  or stand-in without the tag, no module used only with the tag, and a
  workflow that names the target and never the tag.
- The intake's verification has a merge mode, for the merge the host builds
  under `refs/ataniq/pending/`: the request and the stored record carry the
  merge and the base (the target's tip) it was built against, the verification
  script refuses to run a gate unless the fetched commit is a merge of two
  parents whose first is the base (status 93), and `Integrate` of a merge
  counts only merge-mode records of that merge on that base. Records of a lead
  branch are unchanged and still read. No command uses it yet.
- The inject fence's result line now carries the shape of the screen it read,
  numbers and flags only, no text, after the failed conditions: `screen WxH
  alt B cursor X,Y last-row N` (the size, the alternate-screen flag, the
  cursor, and the last row that holds anything). When the screen is not one the recogniser knows, the line says
  `composer not read` instead of `composer false empty false ...`, which read
  as a composer that holds text. A `not-now` on a live claude now says whether
  the screen was on the alternate screen and where the cursor was. The
  recogniser's rules and fixtures are unchanged.
- The holder's counters gain `alt_screen_on` and `alt_screen_off`, the times
  the emulator entered and left the alternate screen. They are listed in the
  status answer, on the `exit` event and in `final.json` with the other
  counters. `alt_screen_on` above zero says the agent drew with a fullscreen
  renderer; both at zero says it drew on the main screen, which the inject
  fence never reads. The session carries them in its own counters, so the
  final record and the `exit` event hold them even though the pane is where
  they are counted.

### Changed

- The host adapter of the owner channel (tagged build only) now verifies the
  merge it builds in the merge mode of verification: the record names the
  merge as the tip, the target's tip it was built on as the base and the pin
  under `refs/ataniq/pending/` that keeps it alive, instead of a made-up lead
  ref, and only such a record counts for the approval. A lead-mode record of
  the same commit, or a merge record on another base, is no evidence, and an
  approval with no counting record is not applied, with a notice that says so
  instead of a moved ref. `ataniqd run --owner-channel` now has a real suite
  runner (the intake's verification in the sandbox template, run through the
  pin): `owner/scratch.json` gains the keys `sandbox` (the template file) and
  `work` (the runs' folder), both under a scratch root and outside the state
  folder, and the daemon refuses the flag without them. Building the runner
  starts nothing; a sandbox starts when a request is verified.
- The owner-channel guides (daemon, console, intake) now say, case by case,
  which of the plan's cases the end-to-end test ran on the stand-in, which it ran
  in part, and which need a seated lead or the real door, in place of "to be
  confirmed". They also state where the confirm code and the plan token are, that
  the verification sandbox was never run for real, and that an interrupted
  request is answered request-invalid. The reason for keeping the sandbox
  template out of the state folder is corrected in the intake guide.
- Correct the docs on the owner channel: the release console contains the
  client side of it and only a release daemon has none (README, architecture,
  lead daemon and terminal console guides), and the list of what ran on a
  stand-in puts the coverage of OC3, OC13, OC28 and OC29 where it is.
- The tagged daemon's scratch guard (`ataniqd run --owner-channel`) now also
  refuses a drop folder that is, lies inside or contains a default state or
  runtime folder or the state folder (with its `owner/` folder), a sandbox
  template inside the drop folder, the mirror or the work folder, and a work
  folder that is, lies inside or contains the drop folder. The reason given for
  keeping the template out of the state folder is corrected in the guide.
- The guides now describe the owner channel of the tagged daemon: release builds contain no owner channel, the tag `ataniq_ownerchannel` and `make test-ownerchannel`, the daemon flag `--owner-channel` with the scratch settings (`door`, `roots`, `sandbox`, `work`) and the STAND-IN banner, and the console, which shows the daemon's approvals only when the daemon's `hello` lists the owner operations. A list says which cases ran on a stand-in and which wait for a seated lead, to be confirmed against the end-to-end test. None of it is qualified. The console guide also says that `--no-bell` works with `--daemon`, and the intake guide lists the `odd-path` flag and says that merge mode covers the review as well as the verification.
- `ataniq-canary check-inputs` now counts the holder's two sockets
  (`attach.sock` and `control.sock` in the runtime folder) in the trial of the
  spec, as the holder does. An inputs file whose `never_mounted` list had 60 or
  61 entries was accepted by `check-inputs` and then refused by the holder at
  its first step (`mount_substrings: more than 64 entries`); it is now refused
  by `check-inputs`. This is in a build with the `ataniq_canary` tag only.
- `ataniq-canary check-inputs` (and `profile-digest`, which runs it first) now
  refuses what the holder refuses at its first step, `validate-config`, so that
  the owner's step 8 catches it and not step 11. It runs the holder's own trial
  of the profile and the spec on the inputs file (`sandboxstart.TrialValidate`,
  the code the holder runs), with the agent command line, environment and
  pinned digest of the config, and for the runtime `claude` it requires
  `sources.managed_marker`, which the holder requires too, and a
  managed-settings file that holds that text, which only assertion 8 inside
  the sandbox checks, after the launch, so that this is earlier than the
  holder. An inputs file without the key, as the run
  kit's prepare script wrote it, was accepted by step 8 and refused by the
  holder, which then ended the unit at once. A refusal names the field and
  never a value or a path. `docs/canary-slice.md` lists what it checks and
  shows the key. A new test derives the keys the holder requires from its own
  trial and fails when `check-inputs` accepts a file without one. This is in a
  build with the `ataniq_canary` tag only.
- Fix the attended canary run kit's texts after passes A and B. Step 5 of pass B
  now asks for `confirmed` only when the account used for the sandbox login
  lists that login as its own revocable entry, or a separate throwaway account is
  used (pass B run 1 showed Claude Code logins listed as Unknown rows), and step
  23 says how to identify the login and accepts `cannot-identify: <what you see>`
  as a finding. Step 20 prints the review's `Digest:` and `Tip:` lines and asks
  `integrate succeeded` up to three times. The clean-up chain no longer
  overwrites the first proof of the stop between the epochs (the files it writes that
  already exist get the suffix `-chain`). The preflight and the checklists list the daemon at step 10
  and say why. `docs/canary-slice.md` records what pass B showed.
- Bound the attended canary run's wait for the daemon. Each ask of the daemon's
  status is cut off after 5 seconds by `timeout(1)`, so a socket that accepts
  and never answers no longer holds each ask for about 10 seconds, and a wait
  ends after its 60 seconds plus one ask, about 65 seconds at most. Step 14 no
  longer says the ticket was taken when the ticket result names no file. The
  three variables that shorten the wait are read by the test entry only, and the
  preflight checks that `timeout` is on `PATH`.
- `canary-prepare.sh --pass B` now puts `CLAUDE_CODE_NO_FLICKER=1` in the live
  agent's environment beside `DISABLE_AUTOUPDATER=1`. It selects claude's
  fullscreen (alternate-screen) renderer, the only one the screen recogniser
  has fixtures for; without it claude chooses the renderer per process and the
  first pass B drew on the main screen, where the fence answered `not-now`.
  The preflight of a live root now requires both names in `agent.env` and gives
  a FIX naming the one that is missing. The environment is part of the profile
  digest, so a root prepared earlier needs a new root and a new pin. The kit's
  README, the checklist and `docs/canary-slice.md` say so, and
  `docs/lead-injection.md` says that every recorded claude screen is from the
  fullscreen renderer. Step 22 prints the two alternate-screen counters from
  `final.json`, and step 16 names `alt_screen_on` as the thing the lead reads;
  `alt_screen_on` above zero in the final record is what confirms the
  renderer choice, which is likely the cause and not yet confirmed by a run.
- Make the attended canary run easier on the owner, without the script ever
  answering for him. A step that waits for something he starts or stops in
  another terminal (the daemon at steps 10 and 26, the console's ticket at step
  14, the listeners at steps 6 and 24) now asks for it every second for up to 60
  seconds instead of failing at once. Step 26 says when the daemon still answers
  or a root still exists and asks again, up to 5 more times, before it fails.
  `canary-prepare.sh` writes `answers.hint`, suggested paths that the run shows
  as "(Enter for: PATH)" at its four path prompts and takes for an empty line (a
  typed path wins; no word such as `yes` or `read` is ever a hint), and
  `CHECKLIST.txt`, one page with the real paths and what each prompt expects;
  `--template-review`, `--unit-report` and `--plan-summary` name the files to
  suggest. A wrong file is refused before it is recorded and asked again up to
  3 more times: a qualify-mirror report where the template's review is asked
  for, or a report of another template. The new `canary-preflight.sh --root DIR`
  is a read-only look before the run: one line per check, `ok:` or `FIX:` with
  the command to type, and `READY` with the three things to type or `NOT READY`.
  The guide is `qualification/canary/run/README.md`.
- Let the owner carry on in pass B of the attended canary run when the fence
  answers `not-now` having written nothing: step 17 now takes the same path as
  a refusal and pass A's first epoch (the owner clears the pane and pastes the
  message himself). Step 16 ends with the owner's checks of the pane (the empty
  composer, no dialog, the last row reading the mode the config expects with no
  notice, then Ctrl+\ and no key or mouse for 3 seconds), and step 14 says what
  the window size means to the fence. Step 15 tells the owner how to carry the
  login URL out of the pane, accepts `logged-in` or `failed`, and lists changed
  files even when the folder was empty before. Step 6 lists the listeners once
  more before it fails, step 23 names the login by its time and warns that a
  wrong row ends the lead's own session, and step 26 also looks for this
  runtime's token and code shapes. Step 25 now counts the changes of the real
  config folder, which the owner's own sessions write, and judges only the files
  of the login's kind at the top of the folder. The preflight checks for `ss`
  and prints the pass the root was made for.
- Tell the owner of the attended canary run two things the console and the unit
  need. At the paste step the script, the page and the guide now say to give the
  session pane the focus first (select it and press Enter): a paste while the
  console has the focus itself is dropped without a word, and the first Enter
  only moves the focus. Between the two epochs of pass A the script now also
  reverts the unit with `systemctl --user revert`, so that the second start does
  not load the `MemoryMax` drop-in of the first. The guide lists three limits it
  does not remove: the script cannot see that the first epoch's console is
  closed, step 12 may read the waiver before the holder has written it, and a
  stop may leave no final record.
- Prepare pass B of the attended canary run the way pass A is prepared:
  `canary-prepare.sh --pass B --claude PATH [--model NAME]` copies the tested
  claude (one regular file, never a link) to `ROOT/agent-install/claude` after
  its `--version`, run with an empty environment, names 2.1.325, and writes a
  live config (`agent.argv` the executable itself, `agent.env`
  `DISABLE_AUTOUPDATER=1`, `footer_mode` `manual`), the inputs file with the
  terminal values, and a pin computed last, after the environment is in place.
  It also writes `netcheck.sh`, which the owner runs by hand to check DNS,
  HTTPS and `claude --version` in the sandbox's shape and under the unit's
  limits, and a pass B `CHECKLIST.txt`. The preflight checks a pass B root's
  claude copy, version, `DISABLE_AUTOUPDATER` and footer mode, and
  `ataniq-canary show` prints the names of `agent.env`, never a value. The
  README gains "The set-up of pass B".
- Change step 14 of the attended canary run so that the owner is not racing the
  30 seconds of the attach ticket. The script now prints the exact console
  command, with its real folder and ticket file, before it mints; the owner
  starts the console first (it waits for a ticket) and presses Enter, and only
  then does the script mint, check that the ticket went to the file it printed,
  and watch that file. The old question whether the used ticket was refused is
  gone, because the console empties the file and the second console never
  presents the ticket: in the first epoch the owner now starts the console once
  more in a fourth terminal, sees that it does not attach and says "waiting for
  a ticket", and the script reads that the ticket file is empty and that the
  holder's event log shows no second attach. The owner is told to close the
  first epoch's console before the second epoch, because a console left open
  takes the next ticket. The page, the preflight's READY text and the guide say
  the same.
- Correct the terminals the attended canary run's page and its preflight name:
  the daemon stays in front of its terminal until Ctrl-C at step 26, so the
  owner needs four, T1 for the script, T2 for the daemon, T3 for the console
  and T4 for the second console of step 14 and the integrate command of step
  20. `CHECKLIST.txt`, the preflight's READY text, the run's own prompts at
  steps 10 and 14 and the guide now agree.
- Make the attended canary run say why a holder ended at its start. The holder
  reports a refused start on its standard error, which is the unit's journal;
  the event log holds only a code. Step 11 now prints the last 40 lines of the
  journal (`journalctl --user --unit UNIT --since` the first start) before it
  fails because the unit is not active or the role is not starting, and step 22
  keeps up to 500 lines of it in the evidence (`out/22-e1-unit-journal.out`,
  with the epoch in its name).
- The canary run script's step 14 mints a read-only viewer ticket in the second
  epoch of pass A and keeps the writer ticket in the first. A writer's attach
  resizes the session to the console's pane, and the screen the stand-in draws
  once is recognised by the fence only at its recorded size, so at most window
  sizes the second epoch's inject ended `not-now, not-ready`. The owner only
  watches in the second epoch. The message text the script prints to compare is
  now between two marker lines with no prefix (text copied from the screen
  carried the prefix and the stand-in found no token), and the guide, the
  checklist and step 14 say the first epoch's window limits (a paste is dropped
  below 100 by 30), that a first-epoch refusal by the fence is expected, and that
  pasta may write two lines on the agent's pane. This is in a build with the
  `ataniq_canary` tag only.

### Fixed

- The console's owner client asks the daemon again when the owner leaves the
  approvals view and opens it again, after an answer that the daemon has no
  owner channel, so a daemon restarted with `--owner-channel` is found without
  restarting the console. Between those actions it still remembers the answer
  and does not dial for each reading.
- The tagged daemon's `owner/scratch.json` now also refuses a repeated key that
  differs only in case (`drop` and `Drop`), which the decoder takes for the
  same key.
- The file-drop collector of the tagged daemon now answers a request that a
  stop left taken but never handled: on its first scan after the start, a
  request in the `collected` folder with no reply that neither the service nor
  the door knows gets one `request-invalid` reply asking for a new request id,
  instead of waiting for an answer that never came. A reply that exists is never
  written over, and the request is not submitted again.
- The tagged daemon's `owner/scratch.json` now refuses a key that appears twice
  in one object, as its documentation said; the decoder used to keep the last
  value without a word.
- The tagged daemon's host adapter now tells a lead that a verification could
  not run, not that its gates failed, when the verification script ended with
  one of its own statuses 90 to 93 (the pin moved, the fetch or the checkout
  failed, or the commit is not a merge on the base). The run is still kept as
  a record and is no pass.
- Fix the review findings of the host adapter of the owner channel (builds with
  the tag `ataniq_ownerchannel` only). A second request file for an id that was
  taken is set aside in the `collected` folder and never answered, so it no
  longer takes the only reply file of the first request. A change with an odd
  path now makes a card that flags the path (the new flag kind `odd-path`, which
  needs a sign-off) instead of a silent refusal. The refusal codes
  `feature-name`, `feature-moved`, `record-too-large` and `binding-mismatch` of
  the intake reach the lead's reply instead of `request-invalid`. `Apply` says
  `not-applied` where nothing was written (the repository's attributes, a ref
  lock held by another writer). `ataniqd run --owner-channel` now refuses, with
  the configuration's exit code, a state folder that is, lies in or holds the
  account's default state or runtime folder, a scratch root of that kind, and a
  mirror that is not under a scratch root; `owner/scratch.json` must carry
  `"door": "stand-in"` and `"roots"`; and the daemon prints "owner channel:
  STAND-IN door, scratch state: no real approval" at start.
- `ataniqd canary start` now refuses, before it changes anything, when the
  holder's state folder or the folder of the role lock
  (`<state root>/roles/<project>`) is missing or not private, as it already
  did for the holder's runtime folder. Each has its own fixed refusal text and
  the path goes to the daemon's log only. Before, the start answered
  `starting` for a unit that ended at once, and the reason was in the journal
  alone. This is in a build with the `ataniq_canary` tag only.
- Refuse, at `ataniq-canary show` and `check-inputs`, a live canary agent with
  `inject` true whose `agent.footer_mode` is not `manual` or `auto`, the
  screen recogniser's names of the mode, so that the footer's text (`manual
  mode on`, or the old sample `sample mode on`) is caught before the message
  is spent instead of ending every inject `not-now`. A live agent's
  `agent.argv[0]` must also be a file under `/ataniq/agent/` that exists as a
  regular file in `sources.agent_install` (no wrapper). This is in a build with
  the `ataniq_canary` tag only.
- Fix `canary-prepare.sh` with the real tagged binaries, which it could not
  finish. It now copies the probe to `bin/ataniq-probe` in the root (mode
  0700, as it copies pasta) and the inputs file names that copy as
  `executables.probe`: the default `--bin`, `build/canary` of the checkout, is
  in `never_mounted`, so the holder's validate step refused it as a bind
  source. Step 9 of `canary-run.sh` compares the probe copy with the built
  probe, as it compares the stand-in. `ataniq-canary profile-digest` and
  `check-inputs` read the lead's copy of the managed settings once
  provisioning has made it, and before that, when prepare asks for the digest,
  the file `provision.managed_settings` names, which provisioning copies; a
  missing marker is refused in both cases. Before, they failed with
  `sources.managed_settings cannot be read: its folder cannot be opened`. A
  prepare that fails after it made the root now ends by saying how to remove
  the half-made root by hand; it deletes nothing. The second replay screen is
  now the recorded `collapsed-paste-footer-c`, which the holder reads as a
  submitted paste; `-a` carries a damaged title that reads as a busy agent, so
  the second epoch's fence ended "not submitted". The Go change is in a build
  with the `ataniq_canary` tag only.
- `ataniq-canary provision` now makes the folder of the lead's role lock,
  `<state root>/roles/<project>` (and `<state root>/roles` above it), with mode
  0700, owned by the caller, checked like the other folders and reported as
  `made folder` or `kept folder`. The holder takes its role lock with an open
  that makes the file and not its folder, so on a provisioned root it refused
  at its first start with `--role-lock cannot be taken as the role lock: no
  such file or directory` and the unit ended at once. The folder must be empty
  when a run keeps it, so a lock left by an earlier holder is refused as a
  leftover; remove `<state root>/roles/<project>` with the other folders when
  you provision again. New tests start the holder from a tree that
  provisioning made, with no folder made by hand. This is in a build with the
  `ataniq_canary` tag only.
- Fix `canary-prepare.sh`, `canary-reset.sh` and `canary-preflight.sh` run by
  their bare name from their own folder (`sh canary-preflight.sh`), which failed
  with `cd: canary-preflight.sh: Not a directory`. They now find their folder
  with no fork when `$0` holds no slash, and in `/` for `/name`.
- Fix two details of the attended canary run kit. In the second epoch the run
  now prints the console command with `--viewer`, matching the read-only ticket
  it mints. Step 12's wait no longer takes an empty or failed answer from
  `systemctl` for the unit's end: it asks again within the same asks. The
  first-time prepare message for a non-empty root no longer gives a delete
  command for a folder that does not carry the prepare mark.
- Fix the attended canary run's set-up for the guard hook's marker. The holder
  refuses a claude start without `sources.managed_marker` and its assertion 8
  searches the managed-settings file for that text, but `canary-prepare.sh`
  wrote neither, so step 11 would end with the holder exiting at once and the
  step saying only that the unit was not active. Prepare now writes a
  managed-settings file that names the guard hook, `/ataniq/qargi/guard-hook`,
  as its PreToolUse hook command, and `sources.managed_marker` `guard-hook` in
  the inputs file; the hash the inputs file records is that of the file as
  written. `canary-preflight.sh` runs `ataniq-canary check-inputs` and looks for
  the marker (and for it in the managed settings), and says to use a fresh
  root when the inputs file is from an earlier prepare. The guide's example of
  the inputs file and the slice's gain the key.
- Fix the findings of the advisor review of the pass B run kit. Step 17 asks
  again (up to three times) on an empty answer instead of ending the pass after
  the login is spent, the dry run names the word `pasted`, and the pass B
  checklist now agrees with the run's prompts at steps 16, 17, 25 and 26 and
  with prepare's order (mirror qualification, then `netcheck.sh`). Step 25 takes
  only a credential file as proof that the canary wrote a login (not the state
  file), compares against the canary's own folder as it is at step 25 (also when
  step 15 left no manifest), and, when no login was completed, asks about a
  difference instead of counting it as a finding. The preflight of a live root says FIX
  when `agent.argv` has no `--model` (and prepare's note no longer says to
  prepare the same root again), and asks for `ss` only for a live root.
  `netcheck.sh` binds resolved's upstream resolv.conf only when the host's file
  is the local stub, as the holder does. AGENTS.md and CLAUDE.md name
  `netcheck.sh` as run only by the owner's hand. The README and the slice guide
  say that `show` prints the names of the agent's environment.
- Reword the message of the attended canary run so that a live agent writes the
  value only: the file `canary-token.txt` in the top folder of the repository
  holds exactly one line, the value after the label on the next line, then a
  line feed. The `canary-token: ` line stays for the stand-in and the daemon's
  admission. When the gate of step 20 fails, the script now also prints the
  first 3 lines the file holds, so that the wrong reading is seen; the gate
  itself is unchanged.
- Run kit, pass B: the checklist and the printed next steps of
  `canary-prepare.sh --pass B` now tell the owner to run `qualify-mirror` for
  the new root (the commands with its paths) or to give the preflight
  `--mirror-report`, since the preflight wants that root's report. They say how
  to add a model when `--model` was not given; step 15 asks for a no when no
  model argument is shown. `netcheck.sh` binds `/etc/ssl`,
  `/etc/ca-certificates`, `/etc/nsswitch.conf` and `/lib64` only where the host
  has them. The dry text of step 9 names the check that `argv[0]` is under
  `/ataniq/agent`.
- Fix the findings of the review of the run script's pass B fixes. Step 16's
  question also takes `go-on` (the fence then answers not-now and the owner
  pastes the message), and any other answer ends the pass with the reason.
  Step 15 asks again until `logged-in` or `failed`. Step 25 counts a changed
  login file as a finding only when its new hash is one the canary's own config
  folder holds; any other change becomes a question to the owner and the lead,
  and a new `projects/-work-*` path (the sandbox's working folder) is always a
  finding. Step 20's display of the token file opens the file once, checks the
  opened file, is bounded and timed, and never follows a symlink or waits on a
  FIFO. Step 23 prints the login time in local time too, as about. Step 26 looks
  for `code=` only in the form of a URL query.
- Fix step 25 of pass B of the attended canary run: the owner's question about
  a changed login file now reads the owner's answer (it read the next line of
  the changes file), the file must match the canary's own file under the same
  name, a change is a finding without a question when the canary's own login
  wrote no file of its kind in its own folder, and the result line names the
  files the owner explained. Step 20 no longer opens a file at the filesystem
  root when the lead's folder cannot be resolved. The README names `go-on` at
  step 16 and the new judgement of step 25.
- Correct two fix lines of `canary-preflight.sh`. The line for a stale profile
  digest pin now also runs `ataniq-canary check-inputs`, as `canary-prepare.sh`
  does after it pins. The line for a root that is not prepared names a fresh
  `--root` when the folder already holds files, which prepare refuses; for an
  empty or missing folder it still names the same root.
- The canary run script's step 12 now waits for the holder's second write of
  `holder.json` (the epoch named and `sandbox_started` true), for up to 90
  seconds, or until the unit is no longer active, and prints the unit's journal
  before it fails. A quick Enter could read the file between the holder's two
  writes and fail the pass. This is in a build with the `ataniq_canary` tag only.
- Fix the attended canary run's step 26 so that an end of input (Ctrl-D, or a
  terminal that hangs up) at one of its Enters still records a failure when a
  daemon still answers or a root still exists, as it did before the step began
  to ask again. A missing key no longer hides either.
- The canary stand-in agent now gives its git the `GIT_CONFIG_GLOBAL` of its own
  environment (a new `GitConfigGlobal` field of `standin.Config`, filled by
  `ataniq-canary-standin` from the environment; it is the only variable besides
  `HOME` that git gets). The sandbox names the identity file that provisioning
  wrote with that variable, and the stand-in had dropped it, so inside the
  sandbox its commit failed with `unable to auto-detect email address` (the
  second epoch's `result.txt` said `failed: git commit`) and the pass could not
  show a commit. New tests lay the world out as the sandbox does: a repository
  with no identity of its own, an empty home, and the identity only in the file
  the variable names. This is in a build with the `ataniq_canary` tag only.
- The lead unit no longer sets `NoNewPrivileges`. With it, the command that
  pasta runs in its user namespace has no `CAP_SETUID`, so the nested
  bubblewrap could not set up its uid map and the sandbox start was refused.
  Bubblewrap sets no-new-privs on the agent it execs, as observed with
  bubblewrap 0.13.1; no probe assertion checks that yet, and a later one
  should. The holder, pasta and the probe's pass-through step now run without
  no-new-privs (they are Ataniq's own code and distro programs that are not
  set-uid).

## [0.0.11] - 2026-11-19

### Added

- The provisional terminal console has an approvals view. `g` opens it from
  console focus (never from session focus, where every key goes to the
  session), and Enter on the pending gate in the right pane opens the oldest
  waiting card. The view lists the waiting cards, the recent decisions and the
  standing rules, shows a card with its plan, diff summary, flagged paths and
  evidence, and opens the full patch in a pager. `L` in the list opens the
  daily card limit of the project under the cursor. A decision is made only on
  the card: an approval needs a code typed from the card's title, a denial
  needs no code; while a field is open every key goes to the field, and Esc
  goes back one step at a time. The pending gate, the timeline (asked,
  decided, applied) and the status line (waiting cards and the age of the
  oldest) follow the cards. When you return
  after a while the console shows a short summary of what happened while you
  were away, and rings the terminal's bell for a card that arrives while you
  are away; `--no-bell` turns the bell off. In this release only `--demo` has
  cards: they are sample cards, the header says "sample cards: a decision
  changes nothing", and a decision on one changes nothing outside the console.
  No approval is possible in a release build: without `--demo` the approvals
  view says "no owner channel in this build". The view is provisional and
  claims no qualification.
- The terminal console guide now describes the approvals view, the card and its
  confirm step, the daily limit's screen, the arrival summary and the bell, and
  presence, and lists plainly what is a sample and what is not qualified. No
  approval is possible in a release build, the sample cards change nothing, and
  the daemon view is read-only.
- The provisional terminal console can show the daemon's leads:
  `ataniq-console --daemon DIR`, where DIR is the folder that holds the
  daemon's socket, shows the projects with their lead and coordinators and
  each one's state, and asks the daemon again every few seconds. The view is
  read-only: every control stays unavailable with its reason, and there is no
  session terminal in it. With no daemon, a slow one or one that speaks
  another protocol, it shows that plainly with the next step in place of the
  leads, and keeps asking. `--daemon` cannot be combined with `--demo`,
  `--attach` or `--return-key`. The view is provisional and is not a
  qualification.
- Add four subcommands to `ataniq-canary`, the provisioning command of the
  canary slice, which exists only in a build with the `ataniq_canary` tag:
  `show --config CONFIG` prints, as `key=value` lines, the names, numbers and
  paths of the config that the attended run script needs (the role, the layout
  role, the unit, both roots, the holder's folders, the evidence file, the
  limits, the agent kind and arguments, the mirror, the lead's home and tree,
  the sandbox inputs path and the sources provisioning makes) and never the
  agent's environment, the git identity or the profile digest; `check-inputs
  --config CONFIG --inputs FILE` checks the sandbox inputs file against the
  config and names the field that differs, never its value; `fields --file
  FILE` prints every value of one JSON object as a sorted `path=value` line,
  with a string that is not plain ASCII printed quoted; `scrub-screen --file
  FILE` replaces the screen text in a copy of the holder's final record by its
  SHA-256 and prints the hash, never the text. The first three change nothing
  on the host, and the last rewrites only the file it is given.
- Add the canary slice, one short attended session that joins the parts built
  side by side: `ataniqd run --canary CONFIG` serves the one throwaway lead of
  its config and `ataniqd canary define|start|ticket|inject|stop` drive it, the
  daemon starts a holder in a transient user unit with memory, swap and task
  limits, `ataniq-holder --canary` starts the lead sandbox with the waiver of
  assertion 10 (`assertion 10 waived: canary slice, no qualification record`,
  the text in `holder.json` and the daemon's evidence, a code in the holder's event log), a
  stand-in agent receives one injected message through the fence, and one commit
  comes back through `ataniq-intake`. `ataniq-canary` provisions the folders and
  `qualification/canary/run/canary-run.sh` is the attended run. Every result of
  the message spends the epoch's one message. It is not a qualification, passes
  no case and is not the week-long canary lead: its result is a list of facts,
  and a real run is the owner's act. Canary mode exists only in a build with
  the `ataniq_canary` tag and under `--canary`, so release builds do not
  contain it: they have no such flag or command, and their holder answers
  `inject` with `disabled`. It accepts only the project prefix `throwaway-`,
  which is now reserved, so a daemon that defines real leads must refuse a
  project that carries it, and only the three gates of the binary, the place
  (roots that are not the defaults, with a marker file) and the invocation let
  the waiver or the injection open. While tagged code exists the whole suite is
  `make test` and then `make test-canary`. The guide is `docs/canary-slice.md`.
- Add canary mode to the lead holder, in a build with the `ataniq_canary` tag
  only: `ataniq-holder --canary --sandbox-inputs FILE` starts the agent through
  the start sequence of the lead sandbox with no controlling terminal, writes
  the waiver of assertion 10 into `holder.json` and the event log, and shows the
  OSC 7501 counters in its status; `--inject` opens injection through the
  control socket and `--expected-mode` names the footer mode a delivery needs.
  A start the sequence refuses exits with status 4 and leaves no agent and no
  `holder.json`. `ataniq-holder --version` prints the name and the version, and
  in a tagged build the tag. Without the tag the holder is unchanged apart from
  `--version`: the four flags are unknown and it hosts a scratch agent only.
  Canary mode also refuses `--inject` without a non-empty `--expected-mode`, and
  a `--runtime`, `--runtime-version`, `--tree`, `--state-dir` or `--runtime-dir`
  that does not match the sandbox inputs and the lead's layout; it starts the
  plan's first process in `/`; and it makes its `lead-has-run` mark only just
  before that process starts, so a start that fails earlier leaves none.
- Add `ataniq-canary profile-digest --config CONFIG --inputs FILE` to the
  canary slice's command, which exists only in a build with the
  `ataniq_canary` tag. It prints the sandbox profile digest the holder will
  compute for the config and the inputs file, as one line of 64 lower-case hex
  digits, so the owner can pin it in `holder.profile_digest` before the first
  start; the holder refuses a start whose digest is not the pin, so a wrong
  pin fails the real start at step 12. It refuses a file that is not the
  config's `holder.sandbox_inputs`, the file the holder reads (the resolved
  paths are compared), checks the file against the config as `check-inputs`
  does, computes the digest with the holder's own code, starts and writes
  nothing, and prints no environment value and no path of its own (a refusal
  to read the file repeats the path given to `--inputs`, as `check-inputs`
  does).
- Add two helper scripts to the canary run kit, in
  `qualification/canary/run/`. `canary-prepare.sh [--root DIR] [--dry]` writes
  the set-up of pass A under a new root (default `ataniq-canary-a` in the cache
  folder): the roots, a throwaway mirror, the config, the sandbox inputs, the
  replay screens and the stand-in copy, and it pins `holder.profile_digest`
  with `ataniq-canary profile-digest`. `canary-reset.sh --root DIR [--dry]`
  deletes only `ROOT/state` and `ROOT/runtime` of a prepared root after the
  owner types `reset` on a terminal, refuses while an `ataniq-*` unit is loaded
  or an `ataniqd` or `ataniq-holder` of the root runs, and makes the two roots
  and the mirror again. Both are run only by the owner's hand, and neither
  starts a unit, a daemon, a sandbox or an agent; `--dry` prints the plan and
  writes nothing.
- The run script now reads the facts of steps 1, 2, 3, 8, 12 and 20 itself,
  where the lead used to type `yes`, and fails the step when the fact is false.
  Step 1 fails on an open Qargi run of the checkout or a loaded `ataniq-*`
  unit, step 2 unless the two fact reports passed, step 3 unless HEAD is the
  commit of `develop` and no tracked file has a change, and steps 8, 12 and 20
  unless the mirror lists the upstream branch, `holder.json` says the sandbox
  started with the pinned profile version and digest, and the upstream branch
  is at the lead's commit. When it cannot read a fact (no `qargi`, a list it
  does not know, an open run whose `repo.path` cannot be read) it asks the
  lead as before and says so in the log. The `--self-test` entry gains
  `open-runs`, `unread-runs`, `list-scan`, `loaded-units`, `tracked-changes`,
  `unit-facts`, `plan-facts`, `holder-start`, `upstream-id` and `lead-id`.
- Add the kit of the attended canary run. `make canary-build` runs doctor and
  builds, with the `ataniq_canary` tag, into `build/canary/` (ignored) the
  binaries the run uses (`ataniqd`, `ataniq-holder`, `ataniq-canary`,
  `ataniq-canary-standin`, `ataniq-console`, `ataniq-intake`, `ataniq-probe`;
  the stand-in and the probe static) and runs none of them; it is the one target
  that builds binaries with the tag, and no release and no CI job uses it.
  `qualification/canary/run/canary-run.sh --pass A|B --config CONFIG [--dry]
  [--evidence DIR]` is the run script, a POSIX shell script that starts a real
  unit, sandbox and agent and is run only by the owner's hand with the lead
  present. It prints each of the 26 steps with what it does, the expected
  result and the gate it serves, waits for Enter before every step that runs
  anything (the start, the read-back of the unit's limits and the balancing of
  the memory limit are one step with one key), never answers a prompt, and
  records each of the owner's own acts with the answer and the time. `--dry`
  prints everything, runs no command and needs no binary and no config;
  `--self-test NAME [VALUE...]` is the test entry, which gives sample values to
  one of the script's own functions, prints its answer and starts nothing. The
  evidence goes to a dated folder of mode 0700 with a manifest of names, sizes,
  times and hashes. A failure runs the clean-up chain: in pass B steps 22, 24,
  25 and 26 from step 6 on, and 21 and 23 as well from step 15 on; in pass A
  steps 22 and 26 from step 11 on. `qualification/canary/run/README.md` says
  what each pass needs beforehand, with a filled example of the config and of
  the sandbox inputs file, what the evidence folder holds, and the undo by
  hand. `tests/canary_runkit_test.go` checks the target, the `AGENTS.md` lines
  and the script, which it runs with `--dry` and `--self-test` only, over stubs
  that must not be called.
- The lead holder's session has the critical section an injection fence needs:
  it holds the input lock for a whole function, holds writer input in the order
  it came and delivers it afterwards (unless a write inside the fence failed,
  which breaks the input like any failed write and refuses the held input),
  applies the agent's output only when the fence says so (at most 4 MiB is
  held, counted in `held_output_full`; the reader waits for room and drops
  nothing, and the end of the fence applies the backlog for at most 250 ms and
  leaves the rest to one goroutine, in order), answers the OSC strings the
  console pane does not know through handlers the holder installs, and shows the
  fence the screen as text with its faint runs, the parser and mode state, and
  the time since the screen last changed. Nothing in a release build calls it.
  The session's input lock and output reader were reworked in every build,
  release holders included: with no fence the reader applies each slice as it
  did, a failed write breaks the input as before, the pty host starts the agent
  as before unless a caller asks otherwise, and the session's counters gain
  `held_output_full` (always 0 without a fence). No release code path uses the
  fence, so nothing a release's callers see is meant to change, but the lock and
  the reader are new code in those builds.
- Add `make test-canary` and a second CI job, `test-canary`, for the tests of
  code behind the build tag `ataniq_canary`, which compiles canary mode. The
  target runs doctor, then the tests with the tag over every package that holds
  a Go file whose build constraint names it, found at run time from every form
  of constraint line Go builds with the tag and skipping the folders `./...`
  skips, and over `./tests/`. `make test` stays untagged and does not build that
  code, so while tagged code exists the whole suite is `make test` and then
  `make test-canary`. The job runs the target after the same checkout, bootstrap
  and doctor steps, has read-only permissions and keeps or uploads nothing. No
  release and no other target builds with the tag. `tests/canary_tag_test.go`
  holds the line: reading every folder but `.git`, the top-level `build` and a
  nested module inside a folder `./...` skips (so a module the build uses, such
  as a directory replacement, is read), a file whose constraint names the tag
  lies only under the paths set for it and in no folder `./...` skips
  (`testdata`, a dot or an underscore), every file that names the tag in a
  constraint does so in a `//go:build` line that the go command applies (a plus
  build line alone is refused, and the header is not read past code that follows
  a block comment), and every file of a package that exists only with the tag
  carries a `//go:build` line that needs it, each constraint read as the go
  command reads it; the tag is named only in the recipes of `test-canary` and
  `canary-build` in the `Makefile`, which names those two targets nowhere else
  and includes no other file, and nowhere under `.github/` or `scripts/`;
  without the tag no command depends on those packages and `ataniqd` depends on
  nothing under `internal/inject`; tagged code uses no module that untagged code
  does not; and no file under `.github/` names `canary-build` or the tag, and no
  run line there names `qualification/canary/`, no workflow uses a local action
  outside `.github/`, also when it is written in a flow mapping or a block
  scalar, while the `test-canary` job runs the make target, uses only
  `actions/checkout` and `actions/setup-go`, has no `if` or `continue-on-error`,
  holds only listed keys, as `test.yml` holds only listed top-level keys (so
  that no `env`, `shell`, `defaults` or `working-directory` changes what the
  target does or whether it runs), and is written without flow style, complex or
  merge keys, anchors, aliases or tags. The keys of the workflow files are read
  the same quoted or not and with white space before the colon.
- Add memory, swap and task limits to the lead unit specification
  (`leadunit.Limits`, `Spec.Limits`). A specification with limits starts its
  unit with `MemoryMax=`, `MemorySwapMax=` and `TasksMax=` set in the start
  call; one without limits is unchanged. The values come from configuration and
  are held to bounds (memory 256 MiB to 64 GiB, swap 0 to 64 GiB where 0 means
  no swap, tasks 16 to 65,536). The in-memory manager records them, and the
  lifecycle's new optional `Options.Limits` function gives the limits of a role
  at each launch; the start checks them with `Limits.Validate` before the
  registry changes and refuses limits outside the bounds like a preflight
  refusal.
  `leadunit-qualify` starts its unit with limits from its flags
  (`--memory-max`, `--swap-max`, `--tasks-max`, `--memory-lowered`; a memory or
  swap size must be a whole number of 4096-byte pages), reads them back from
  `systemctl --user show` and the control group files, lowers the memory limit
  once, checks that the runtime folders hold the unit's drop-in folder while it
  runs and that nothing is left after the stop; it has not been run.
- Add the message and the result to the control wire of the lead holder: an
  `inject` request may carry a `message_id` and a `text`, both or neither and on
  no other operation, and an ok response may carry an `inject` result with a
  closed outcome, reason and written amount. The holder still answers `inject`
  with `disabled`; the lead injection guide describes the format.

### Changed

- Owner-side git reads now ignore attributes from any tree for diffs, logs and
  merges: the pinned git environment sets the attribute source to the empty
  tree for `diff`, `diff-tree`, `log` and `merge-tree`, so a `.gitattributes`
  a lead committed can no longer mark a file as binary in a diff or change
  how a merge joins two sides. `log` now gets `--no-ext-diff` and
  `--no-textconv` as `diff` does, so a patch in a log starts no textconv
  program named in the repository's config. The environment also accepts
  `merge-tree` in one form only (`--write-tree`, the output flags
  `--name-only`, `--no-messages` and `-z`, `--end-of-options` and two full
  object ids, so no option can change what is merged), and `hash-object` in
  the one form that writes a commit, for a merge built by the host, and gains
  a check that refuses a repository whose own `info/attributes` is not empty,
  since git reads that file whatever the attribute source is. Provisional:
  shown by tests on the installed git only, and not part of any
  qualification.
- Let the client protocol package carry extension operations that the program
  serving it registers. A request may carry `params`, one JSON object; `hello`
  lists the extension operations after `hello` and `status` and says
  `mutations: true` only when a registered extension changes something, and the
  package gains a `Call` method for them. No release build registers one, so
  `hello` still lists two operations and says `mutations: false`.
- Change the lead sandbox proposal to revision 4 of the lead-hosting design. It
  is still a proposal that awaits qualification and the owner's decision on the
  profile; nothing here is qualified, safe or supported, and nothing uses it
  yet. The layout: every lead has the folder of its working Qargi records
  (`<state>/leads/<role>/qargi`, always bound read-write, at an inside path the
  inputs give) and four parent folders, `/ataniq/requests`, `/ataniq/mailbox`,
  `/ataniq/mail` and `/coord`, which are only bound folders; the git identity is
  bound as the folder that holds it, at `/ataniq/git`; the profile version is 4.
  There are eleven start-up assertions: assertion 11 looks at the four parent
  folders and that no coordinator's tree or home is visible, and assertion 7 is
  checked on both sides. Rule B1 of the design is applied to the checks: they
  walk by handle without following links, from the state root the caller owns
  or, for a source outside it, from the root of the file system. A bind source
  Ataniq supplies now reaches bubblewrap as a descriptor (`--bind-fd`,
  `--ro-bind-fd`): the pass-through step, `ataniq-probe pass-fds`, walks to it
  by handle (`--walk-dir FD ROOT REL`, `--walk-file FD ROOT REL`), and a walk it
  refuses is exit 96 before bubblewrap starts. The step now takes at most 128
  descriptors in all, no longer 8 files. A row of the host (`/usr`, the `/etc`
  files other than `resolv.conf`) is still bound by path. Whether bubblewrap
  mounts the object behind a descriptor is a qualification item, and the harness
  takes a bubblewrap release of 0.10.0 as its assumed minimum. In the harness
  configuration `git_identity_file` is now `git_identity_dir`, a folder that
  holds the file `config` and nothing else, and `qargi_records_inside` is
  required; the evidence files have format 3 (format 2 added the walks and the
  descriptors of the bind sources, format 3 the facts of case S21). The harness
  gains the opt-in case S21, planted bind paths: at every path the start walk
  looks at (the mount points and the tree's `.git` folder) it plants a symbolic
  link to a path outside and then an entry of the wrong type, runs the checks of
  a start, records whether the start was refused, by which check and with which
  problem and path, puts the original entry back and looks at the result, in its
  own step, `planted-bind-paths`, before the probe spec. It never launches (a
  run whose only case is S21 skips the launch phase), and it needs `--run`,
  `coordinator_reviewed` and the new configuration key
  `scratch_folders_disposable`, the statement that the folders under the state
  root are disposable scratch folders and not a live lead's; a dry run does
  nothing for it and says so. Its results are `planted-paths-refused`,
  `planted-paths-refused-other-runtime` (the other runtime's start, by the walk
  alone, at the paths in folders the configuration names) and
  `planted-paths-restored`, each ok, failed or not-checked; its facts are
  `planted_paths` in `summary.json` (evidence format 3). It is made from
  outside, as a stand-in for a lead that plants from inside. `AGENTS.md` names
  the qualification build, `make qualification-leadsandbox-build`.

## [0.0.10] - 2026-11-17

### Added

- Add `ataniq-holder`, the per-agent holder process: one process for one
  hosted agent. It owns the agent's terminal (the PTY master and the emulator
  that mirrors its screen), the attach and control sockets, the event log and
  the final record, takes the role lock, and writes a state file (`holder.json`)
  before it starts the agent. The agent's environment is exactly the `--env`
  list and nothing of the holder's own. It refuses to run without `--scratch`
  (exit 2), because it launches no sandbox, so it hosts only a scratch agent
  (the lead sandbox proposal is built beside it, unqualified, and not joined to
  it); it also refuses to start when `dev.tty.legacy_tiocsti` is not 0, when a
  signal the agent needs is ignored, or when the state or runtime folder is not
  a private directory of the user. SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and
  SIGUSR2 stop it with a final record, and the record of an earlier epoch is
  kept as `final.json.previous`. A second holder for a held role exits 3 and
  changes nothing of the first. It is provisional and unqualified, and hosts no
  real lead; `ataniq-scratch` starts it. It is built with the Go runtime's
  cgroup-aware `GOMAXPROCS` turned off (`containermaxprocs=0`), so it keeps no
  descriptor of the cgroup's `cpu.max` open and its `GOMAXPROCS` does not follow
  a cgroup CPU quota.
- Add `ataniq-intake`, a provisional owner command for the reviewed intake of
  a managed lead's work through the exchange mirror, a bare repository in the
  state folder that is the only way out of a lead. `init` creates the mirror,
  `fetch` brings a lead's branches into it, `branches` lists them, `review`
  shows the change and lists under "Needs your sign-off" each path that needs
  its own approval, with its flag kind (and, with `--records`, the gates of
  the verification records of the tip). The kinds are `exec-config` (a path in
  the execution-config set), `symlink`, `submodule`, `aliased` (a changed path
  in a folder that a symbolic link with an execution-config name stands for)
  and `history-only` (a path of one of those kinds in some commit of the
  branch that the net change does not show, such as one added and removed
  again; it still enters the history). `verify` runs
  gates in a sandbox and stores the result in the records folder, and
  `integrate` moves an upstream branch to a reviewed, verified tip,
  fast-forward only. The sign-off is typed as arguments (`--accept-change`,
  and one `--accept-path` for each flagged path); the command never asks a
  question. Everything a lead wrote is shown escaped and labelled "unverified
  lead data". The same `--path-rule` list must be given to `review` and to
  `integrate`. The command does not touch a working checkout, which gets the
  commits by its own fetch from the mirror. No sandbox is shipped or
  qualified: `fetch` and `verify` run the commands of a template file that the
  owner writes, and refuse without a usable one. A verification pass is
  evidence, not proof, and after the sign-off the code runs as the owner like
  any merged code. See [the lead intake guide](docs/lead-intake.md).
- Add `ataniq-probe`, a static helper command of the lead sandbox proposal. It
  is unqualified, runs on Linux only (elsewhere it builds and exits with a
  message), and only the opt-in lead sandbox qualification harness launches it:
  no holder and no daemon uses it. It has two modes. `ataniq-probe --spec PATH
  --report-fd N -- AGENT ARGV...` runs the inside checks of the proposal, writes
  exactly one report line to descriptor N (and nothing else to it: the holder
  takes that line as its signal and fails the start on any further byte), and
  exits 97 without starting the agent when the report does not allow a start.
  Otherwise it gives the agent a new session with descriptor 0 as its
  controlling terminal when that is a terminal, makes every descriptor above 2
  close-on-exec so the exec closes it, and executes the agent with the
  environment as Go reads it (os.Environ: a name that appears twice is passed on
  once, with its first value, and an entry without `=` as it is). A usage error
  or any failure to read the spec, write the report, get the terminal or execute
  the agent is exit 98. The agent path is checked first, before the report is
  written, to be an executable regular file, otherwise it is exit 98 with no
  report; a file that the kernel then refuses to execute is exit 98 after a good
  report, which a holder cannot tell from an agent that exited 98. `ataniq-probe
  pass-fds [--read FD=PATH]... [--write FD=PATH]... -- COMMAND ARGV...` runs on
  the host between pasta and bubblewrap, because pasta closes the descriptors it
  inherits: it takes descriptor numbers in decimal digits without a leading
  zero, opens up to 8 files (read-only regular files, or write-only appending
  regular files and FIFOs, never created or truncated, links not followed; every
  path must lie in folders only the holder can write, as the checks on a path
  are not atomic with the open), places each at exactly its number (a FIFO that
  no process has open for reading is refused at once and does not wait), makes
  every other descriptor above 2 close-on-exec and executes the command with the
  environment as Go reads it, as above; any error is exit 98 and nothing is
  executed. Both modes refuse (exit 98, one line) a number that is one of the Go
  runtime's own descriptors, which it holds when a standard stream was
  non-blocking at start: a target number in the pass-through mode, the report
  descriptor in the inside mode. Like `ataniq-holder`, it is built with the Go
  runtime's cgroup-aware `GOMAXPROCS` turned off, so the runtime holds no
  descriptor of the cgroup's `cpu.max` at a number the caller may use.
- Add `ataniq-scratch`, a development launcher that hosts a scratch agent (no
  project, no lead role, a disposable home) in a holder, in a transient systemd
  user unit or as a direct child with `--direct`, and stands in for the daemon
  to issue attach tickets over the holder's control socket. `start`, `ticket`,
  `status`, `release-writer` and `stop` are its subcommands. `start` takes the
  agent as an absolute path, because the holder does no `PATH` search, and
  refuses any other with exit status 2. It is provisional and unqualified, and
  not for a real lead.
- Add `ataniqd`, a provisional and unqualified daemon skeleton. It is
  read-only: it starts, stops and manages no unit and defines no lead, and no
  flag changes that. `ataniqd run [--state-dir DIR] [--runtime-dir DIR]` (state
  defaults to `$XDG_STATE_HOME/ataniq`, else `$HOME/.local/state/ataniq`;
  runtime to `$XDG_RUNTIME_DIR/ataniq`, with no default and no fallback to a
  shared folder when `XDG_RUNTIME_DIR` is unset) refuses to run as root,
  creates the state folder with mode 0700 and refuses one that is not the
  caller's or is open to group or others, takes `daemon.lock` there (a second
  daemon exits with its own code and names the lock as held, with the pid in
  the file shown as a hint, not as proof), takes a second lock,
  `daemon.sock.lock` in the runtime folder, opens the registry journal in
  `registry/` (a corrupt one stops the daemon and nothing is repaired),
  reconciles once, removes a stale socket only once it holds both locks, and
  serves `hello` and `status` on `daemon.sock` until SIGTERM or SIGINT. The
  runtime folder lock keeps two daemons with different state folders and one
  runtime folder from taking each other's socket: the second exits with code 17
  and says that another daemon serves that folder, with the pid in the lock
  shown as a hint. It is refused before it opens its registry, so it has
  reconciled nothing and written nothing to its journal. A socket path that is
  too long for a Unix socket is refused before the runtime folder or the lock
  file is made. A socket or lock file that a crashed daemon left does not block
  the next start. At shutdown it stops accepting, waits for the connections for
  a bounded time, closes the listener (which removes the socket) under the
  runtime folder lock, closes the registry, releases the runtime folder lock
  and releases the daemon lock last;
  when the server does not end in that time it exits with code 19, says what is
  left and closes the listener; the registry and both locks are left to the end
  of the process. It never stops a lead or any unit. The reconcile reads the
  units and may write the registry's journal, the states stopped, ended or
  unknown with their proof or reason and the outcomes adopted or abandoned of
  launch intents, and, once it has acted on every role of a registry whose
  journal was cut when it was opened, the acknowledgement of that cut, which
  is a record of its own kind in the journal; it starts, stops and signals
  nothing. A cut that was acknowledged shows no more in `status`; a cut that
  stays unacknowledged (the manager did not answer for a role, or the
  reconcile stopped first) shows as the reconcile not completed, and the next
  start acts on it again. When systemd cannot be
  asked (also when the registry has no role to ask about), when any call to it
  failed during the reconcile, or when the reconcile ran out of time (also in
  its last step), the daemon still serves the roles as the registry has them,
  with the reconcile shown as not completed, which is also how an
  unacknowledged recovery of the registry shows, as the protocol has no field
  for the reason. An answer such as "not loaded" is not a failure. Reconcile
  runs at start only, not on a timer. Each start-up step that fails has its own
  exit code (10 to 18) and one line on standard error. The socket is mode 0600
  in a 0700 folder, and a peer is accepted only when its user id equals the
  daemon's, which does not tell one process of that user from another. `ataniqd
  status [--socket PATH] [--json]` prints one line per role (role, kind, state,
  reason, epoch, desired, manager, and for a coordinator its parent and
  instance) and one for the daemon, or the status result as JSON; every string
  from the socket is escaped, so a hostile one cannot act on the terminal. Only
  `run` takes SIGTERM and SIGINT as the request to stop; they end `status` at
  once. `status` exits 0 when the daemon answered, 20 when no daemon answers,
  21 when it refuses the user and 22 for an answer that is not valid or an
  error the daemon answered. The requested and the observed profile stay apart,
  and a field that was not observed is shown as not observed. The protocol has
  no place for the proof of a stopped or ended state or for the unit evidence,
  so `status` shows neither. `ataniqd version` prints the version, `0.0.0-dev`
  unless set with a linker flag, because the `VERSION` file cannot be embedded
  from the command's folder. Leads, coordinators, the holder, the sandbox and
  the console are not part of it, and nothing in it has run against systemd.
  The same change adds `leadunit-qualify`, an opt-in harness for the systemd
  unit manager that `ataniqd` uses, and the one new command that can start a
  unit. Without `--run` it prints its plan and touches nothing. With `--run
  --state-dir DIR` it starts one transient user unit,
  `ataniq-qualify-<random>.service`, and one bound to it,
  `ataniq-qualify-<random>-child.service`, never under the name of a role,
  checks the manager against them, prints one JSON report and cleans its units
  up. It is run by hand: no test and no make target runs it, and no run of it
  is recorded. See [the lead daemon guide](docs/lead-daemon.md).
- `ataniq-console --attach DIR --ticket-file FILE [--viewer]` shows a live
  scratch session from a holder in the cockpit: one writer, one read-only
  viewer, no takeover. A console that waits for a ticket does not connect, so
  it does not touch the holder, and a refused ticket file shows its reason. It
  is provisional and unqualified, and for a scratch agent only.
- Add the `internal/inject` packages for fenced message injection into a
  terminal-hosted lead, as a library with no command using it yet. They
  classify a session as idle, working, blocked, ended or unknown for claude
  2.1.325 and codex 0.169 (also spelled 0.169.0) from OSC 7501 reports, window
  titles and screen recognisers built from recorded sessions; any other version
  is refused. The fence pastes a message only into a composer it reads as empty
  and idle, writes nothing while it sees an approval dialog, and presses Enter
  only when a second check after the paste finds the composer idle and holding
  the header, or showing a pasted-text marker that was not there before.
  Durable command states keep a command `uncertain`, and its lead's queue
  frozen, until an operator decides that it was delivered, or drops it, or
  sends it again as a new command; nothing retries. Message admission fixes
  the header, the senders and edges (never `owner`), the text and marker rules,
  and the caps of 16 messages and 64 KiB for a target's queue (a project lead
  or the supervisor), 6 a minute for a sender and 4 KiB for a text. It is
  provisional and unqualified, built from recorded screens of an earlier
  prototype and never run against a real agent: neither the `ataniqd` skeleton
  nor `ataniq-holder`, which are built beside it, uses it (the daemon has no
  queue and delivers nothing, and the holder answers an `inject` request with
  `disabled`), nothing reads delivery from the conversation file, and no live
  agent was used.
  See [lead injection](docs/lead-injection.md).
- Add `docs/lead-sandbox.md`, a guide to the lead sandbox proposal: the launch
  plan, nested sandboxes under owner decision R17, the file-system view, the
  clone rules, the environment allowlist, network and DNS, the probe, the ten
  start-up assertions, symlink-safe reads, the profile digest and the open
  points. It is a proposal that awaits qualification and the owner's decision on
  the profile; nothing starts a sandbox in ordinary use, and no command uses it
  yet. Add the opt-in qualification harness `qualify` for the proposal, with its
  guide `docs/qualification-lead-sandbox.md`, and the target `make
  qualification-leadsandbox-build`, which runs the doctor and builds the harness
  and `ataniq-probe`, and starts no sandbox and launches nothing. Both are
  unqualified infrastructure: the harness records facts for a coordinator and
  says nothing about the sandbox being qualified, safe or supported.
- Add `qualify-mirror`, an opt-in harness that records what a real sandbox
  template does with a hostile lead clone: the fetch into the exchange mirror
  and the review of a lead branch, and, if the template has a verification
  section, two gates that probe it. It is run by hand with a config and a new
  output file, is not part of `make test` and is not built by any Makefile
  target. It works in a scratch state root and writes one JSON report of facts
  with no pass, supported or qualified field: a person reads the facts and
  decides. It qualifies no sandbox by itself. See
  [the lead mirror qualification manual](docs/qualification-lead-mirror.md).

### Changed

- Link the lead guides from `README.md` and `docs/architecture.md`, which now
  name the lead injection guide, the lead intake guide and the two opt-in
  qualification manuals (lead sandbox, lead mirror) beside the holder, sandbox
  and daemon guides. The guides point at each other and no longer say that a
  holder or a daemon does not exist: each says what the others built, and that
  none of these parts calls another yet. `docs/lead-intake.md` adds that the
  owner's git reads a git bundle file found on the host at the template's
  `serve.repository` path, so that path must not exist on the host.
  `docs/lead-daemon.md` no longer says that the daemon's packages build on
  Linux only: the daemon runs on Linux only, and for another system `ataniqd`
  builds as a stub and the packages compile without their Linux files.
  The status notes of the two amendments of 2026-11-17 in `docs/design` no
  longer say that the scratch holder and attach client are being built and that
  no recogniser exists: they name what is built, and say that it is not joined
  and that nothing sets `ready`. No contract text changes.
  The maturity section of `README.md` describes the tree without naming a
  release; it named 0.0.8.
  `docs/terminal-console.md` says of `--attach` that the mode is not managed
  by the daemon, where it said that there is no daemon.
  Documentation only.
- Amend the runtime and client protocol contracts for a lead hosted as a
  terminal program (owner decision of 2026-11-17). Its `ready` state is an
  Ataniq observation of the hosted terminal, never a provider report: the
  holder runs for the current epoch, the stream is open, the runtime version is
  the pinned one, login is checked or says `login: not checked`, and the screen
  is an idle composer. The screen then gates each delivery and never proves
  health, authority or completion. Its attach stream is a ticketed terminal
  data plane, not a second command path: Ataniq admits no command, decision or
  grant from it. It has one writer, one viewer and no takeover, accepts a peer
  of the holder's own OS user only, and carries the stated same-user limit.
  The amendment is a specification. Beside it, `ataniq-holder`,
  `ataniq-scratch` and `ataniq-console --attach` build the attach stream for a
  scratch agent only, with tickets from a development stand-in and not from a
  typed command. Nothing sets `ready`: the holder reports the idle
  classification as `unknown` and does not use the screen recognisers of
  `internal/inject`. None of it is joined to the daemon, and none is qualified.
- Bound what the console's session pane keeps of a session's output, in the
  patched `x/vt`. A cell keeps at most 64 bytes of a grapheme cluster, cut at
  a character boundary; the rest is dropped and counted, and a megabyte of
  combining marks costs bounded memory and linear time. Only a fixed table
  of known terminal modes is recorded (the ones the emulator acts on, the ones
  the pane and console follow, and the ones a full-screen agent commonly sets);
  setting or resetting any other mode number is ignored and counted, a request
  for it is answered "not recognised", and setting 100 000 distinct numbers
  does not grow memory. A hyperlink whose URL is over 2 KiB or whose
  parameters are over 256 bytes is dropped and counted, and the text after it
  has no link; a screen (the main and the alternate each) keeps at most 256
  distinct live links, and text printed under a further link is drawn without
  it and counted; and the data of an OSC, DCS or APC string is cut at 64 KiB
  (it was 4 MiB), with the rest dropped and counted. The emulator exposes the
  counts of every cap it hit through `Counters`.
  Clearing or resetting the screen, switching the alternate screen and
  scrolling a whole region cost the lines they change and no longer every
  cell, so a program that does these in a loop (for example a full-screen
  program that clears the screen on every frame) no longer stalls the pane for
  seconds or minutes, and the pane feeds the emulator in slices of 4 KiB, so
  that a draw waits for one slice of output at most. Output that scrolls a
  region with left and right margins set can still take about a minute for
  64 KiB on the largest screen; see `third_party/patched/x-vt/NOTICE.md`.

## [0.0.9] - 2026-11-17

### Added

- Add `ataniq-console`, a provisional terminal console built on Bubble Tea.
  It is a fixture console, not a qualification: `ataniq-console --demo
  [--theme dark|light]` shows fictional, read-only projects, persistent leads,
  subagents, tasks and conversations, and without `--demo` it reports that the
  backend is unavailable. Dark is the default theme. Nothing typed reaches a
  process, and every control (approve, deny, full attach, send, interrupt,
  detach) is unavailable and listed with its reason under Ctrl+K; `a` says why
  full attach is unavailable. Live control, provider integration, use over SSH
  and terminal restoration after a startup error remain unqualified.
- Show the console as a cockpit in a window of 100x30 or more, on the alternate
  screen: three panes (projects and leads with a context block; a session
  block, an embedded session terminal and a focus bar; evidence and approvals
  with the attachment contract, the pending gate and the lead's binding), a
  timeline and a key bar. Below that size it shows single-column pages. The
  session terminal replays a synthetic recording through an embedded emulator
  and is labelled "fixture recording, not a live session" in every state.
  Tab cycles the panes and Enter on the session focuses it. In session focus
  every key goes to the session, `q` and Ctrl+C included, except the return
  key (Ctrl+\ by default, set with `--return-key ctrl+KEY`) and F12, which
  zooms the pane to the window in either focus. Both work with CapsLock,
  NumLock or ScrollLock on. The return key is read from the byte 0x1c and from
  `CSI 92;5u`; without the kitty keyboard protocol Ctrl+4 sends the same byte
  and returns too. Ctrl+Enter is never bound. A recording that ends shows its
  last screen, dimmed, under a banner with the exit status or "unknown",
  saying that the terminal stream ended, not necessarily the agent; in session
  focus any key but F12 returns to the console, and in console focus `r`
  replays the recording. `--recording NAME` picks a fixture recording and
  `--linger DURATION` (default 30s) keeps the session open after its end so
  the keys can be tried; `mouse-held.jsonl` sets mouse reporting and focus
  events and never resets them, to try the mouse by hand during the linger
  time. The console asks the outer terminal for mode 2027 at start, also over
  SSH, so that the pane can draw grapheme clusters as the terminal measures
  them, and for focus events only in session focus and only when the session
  asked.
- Confine mouse selection in the cockpit to the session terminal. While the
  cockpit is shown the console asks the outer terminal for mouse reporting, so
  the terminal's own selection no longer spans the window; the single-column
  pages below 100x30 leave the mouse to the terminal. In session focus a
  session that asked for the mouse still gets it. Otherwise a left-button drag
  in the session terminal selects its text, stays inside the pane, and on
  release copies the text to the outer terminal's clipboard with OSC 52 (wide
  characters and grapheme clusters once, trailing blanks dropped, rows joined
  with newlines, a wrapped line as two lines). A selection over 1 MiB is not
  copied and the key bar says so. A click alone, a selection of blanks, a
  press outside the pane and the wheel copy nothing, and a press, a key or
  paste sent to the session, a resize, F12, `r` or output that changes the
  selected text clears the selection. Shift+drag gives the terminal's own
  selection in most terminals, Ghostty included. A terminal that refuses OSC 52
  writes copies nothing.
- Pin the Bubble Tea v2.1.1 terminal console toolkit and its module
  dependencies, and preserve their dependency notices in
  `third_party/terminal-console`. The session terminal is built on
  `charmbracelet/x/vt` v0.1.0, a new dependency, which with
  `charmbracelet/x/ansi` v0.11.9 is carried as a patched, in-tree copy under
  `third_party/patched`, selected by `replace` lines in `go.mod`. The patches
  keep a grapheme cluster whole when it arrives in two writes, keep a UTF-8
  character such as U+2733 from ending a window title early, and keep no
  scrollback for the alternate screen, whose default of 10000 lines let a
  full-screen program grow memory by hundreds of megabytes. Each copy has a
  `NOTICE.md` and is dropped once an upstream release contains the fixes.
  `go mod verify` does not cover directory replacements, so the doctor says so
  and git protects the copies, and `make test` also runs their own tests
  (`make test-patched` runs only those; it covers x/vt and x/ansi with its
  parser, kitty and iterm2 subpackages, not the sixel subpackage, which needs
  an uncached module).
- Record each change's changelog entry as a fragment file in `changelog.d`,
  named `<section>-<slug>.md` (section: added, changed, deprecated, removed,
  fixed or security), in the same commit as the change, so parallel features no
  longer collide on `CHANGELOG.md`. `scripts/changelog-fold.sh` (`make
  changelog-fold`) folds the fragments under their headings in the Keep a
  Changelog order, each section's fragments sorted by name, and removes them;
  `--into VERSION` folds into the dated `## [VERSION]` section that `qargi
  release start VERSION` makes, and refuses when it does not exist, while
  without it the fold writes to `[Unreleased]`. It refuses an unknown section
  or a malformed fragment (including one with Windows line ends) without
  changing anything, replaces `CHANGELOG.md` in one rename so an interrupted
  run leaves it intact, is safe to repeat, and `--dry-run` writes nothing.
  `make changelog-check` fails while any fragment is left. The release flow is
  `qargi release start VERSION`, then `make changelog-fold ARGS='--into
  VERSION'` as the first fix commit on the release branch, then `make
  changelog-check`, then `qargi release push`; a hotfix does the same before
  its pull request, and CI runs the check on both and on manual runs of those
  branches. Entries already in `CHANGELOG.md` stay where they are.

### Changed

- Add the feature coordinator to the design documents. A coordinator is an
  ephemeral delegate of a project lead for exactly one feature: it holds no
  seat or lead assignment epoch, is never a handoff target and never approves.
  Its questions go to its lead; the owner may watch it and type into it as its
  single writer, with the lead notified and its powers unchanged. It may
  prepare, verify, apply and merge runs into its own feature, sync it and clean
  any run of its feature; the lead runs the whole-suite verify, and start and
  finish of a feature, push, release, hotfix, prune and adoption of other
  actors' runs stay with the lead, and approval with the owner. Its label only
  narrows what it may do and is never identity; identity comes only from a
  bound file Ataniq writes. A lead quiesces its coordinators before a handoff,
  and a stalled coordinator is confirmed stopped before its runs are adopted.
  The approvals document states that no approval, plan token or grant passes to
  or through a coordinator, that a lead's feature delegation to one is neither a
  grant nor an approval, and that the owner typing into one is not an approval.
  Specification only; nothing is implemented.
- Record the order of work in the design documents. Qualification comes first
  and sandbox qualification precedes any Ataniq-managed lead; a provider's
  remote control stays an optional launch profile during the transition; a
  Codex lead is an interactive `codex` in its own session. Session hosting is
  decided by an attended spike and documented after it. Nothing here is
  implemented.
- Add a design document for approvals and delegation. Only the owner's direct
  words, or a delegation-chain grant whose signatures verify back to the
  owner's root key, approve anything. Grants have scope, expiry, a use count
  (single use for a sub-grant), revocation and audit; a message is never the
  authority, though a signed grant may travel by message and the gate verifies
  it; an owner channel is unreachable by leads; and relayed direction is never
  an approval. Delegation grants are kept in one qargi store and are honoured
  for qargi gates only in the first version. The designated-approver wording in
  the design baseline and client protocol is corrected to match. Specification
  only; nothing is implemented.
- Add a design document for roles and the authority split. Each project has
  exactly one assigned lead and a single supervisor spans projects; both are
  assignable roles separate from agent identity, provider, model and effort.
  A role changes hands only by a verified handoff (reconcile, exclusive
  ownership, fence the old holder) that grants no authority. If any step
  fails the outgoing holder keeps the role and an unresolvable outcome goes
  to the owner; who verifies each step is left to the handoff contract. The
  supervisor never acts on its own project's gates. Ataniq owns processes,
  sessions, lifecycle, attach, messaging, scheduling and escalation routing,
  qargi owns development tasks, evidence and gates, and each record has one
  authoritative system. Specification only; nothing is implemented.
- Record the owner-approved interface direction in the design documents: a
  terminal-first Bubble Tea console is the primary operator client, a web
  console with the same information architecture comes later, a shared typed
  command and event contract sits under both with MCP as the agent-facing
  surface, dark is the default theme, and unavailable or unqualified controls
  are shown with their reason. This replaces the earlier assistant-ui web
  console first direction. Only the fixture console is implemented.
- `make doctor` checks offline that every package and test for the host
  platform loads from the module cache with hashes matching `go.sum`, and that
  cached modules are unchanged since download. It prints the Go diagnostic,
  which names the failing module when the failure is in a module, and leaves
  out Go's `go: downloading` lines, which are misleading offline; when nothing
  is left it prints `(no diagnostic from go)`. A missing module is fixed with
  `make bootstrap`. For a checksum mismatch, restore `go.sum`, or run
  `chmod -R u+w` on the named module directory in the module cache, remove it
  and run `make bootstrap`. A modified module is handled the same way: its
  files are read-only, so run `chmod -R u+w` on it first, remove it, then run
  `make bootstrap`. Any other load error is in the failing package and is
  fixed there. It also checks that `grep` is installed, which the Go helper
  uses. When it is not, doctor prints `missing: grep` and skips the Go checks,
  and bootstrap, test and build stop with `missing: grep`.

## [0.0.8] - 2026-11-15

### Added

- Add a finite synthetic controller-handoff ledger, proof and generation model,
  independent evidence verifier and 36 frozen rows, including four
  mutated-evidence controls, plus five supplemental verifier checks.
  Real controller authority, receiving-gate exclusivity, native attachment,
  credential mediation and durable native admission remain unqualified.

### Changed

- Update README maturity and qualification scope to match this release.

## [0.0.7] - 2026-11-15

### Added

- Add private internal administration, resource, status and conflict contracts,
  bounded canonical bindings and projection helpers, preserving the public v1
  command contract and vectors.
- Add a private v2 encrypted cursor codec with canonical bounded decoding,
  bounded per-codec epoch reservations, rotation, failed-mint and fresh-construction tests.
  Authentication, service authorization and durable receipt integration remain
  pending.
- Extend SQLite default-VFS qualification with checked directory synchronization,
  explicit FULL checkpoint and close checks, descriptor-selected sync denial on
  the harness's pinned directory alias and SQLite's live main descriptor, and
  bounded lifecycle, identity-replacement and refusal controls. The directory
  denial target is the harness-owned alias. Directory-entry persistence for main-file creation and WAL creation/removal, production
  lifecycle, pooling, power-loss behavior and broader durability remain unqualified.

### Changed

- Update the README to describe the internal contracts, cursor codec and bounded
  SQLite lifecycle checks in this release, with production limits and native
  terminal priority retained.

## [0.0.6] - 2026-11-14

### Added

- Add a bounded Linux/amd64 SQLite default-VFS settlement qualification harness with
  child-only sync-failure injection, strict capability controls and relational
  evidence. Production durability remains unqualified, including directory lifecycle,
  pooling and live admission. See the qualification guide for the full scope.

### Changed

- Add a review of README and any website claims for every release, including hotfixes,
  to the contributing workflow.
- Correct the README maturity overview and clarify qualification limits.

## [0.0.5] - 2026-11-14

### Added

- Document journal recognition's read-only and recovery boundaries and its
  synthetic qualification fixtures.

- Add bounded trusted internal journal v1 recognition under existing-store
  ownership, preserving committed WAL state and pending recovery without writer
  SQL or journal recovery. Share the read-only preflight with ordinary Open;
  recognition grants no migration or public disclosure authority.

## [0.0.4] - 2026-11-14

### Added

- Add pure typed application command contracts for queued input and exact queued
  cancellation, bounded strict JSON codecs, owned canonical binary bindings and
  eight portable golden vectors. Keep authentication, durable admission and
  provider execution outside this package.

- Document application wire and canonical contracts, parser limits, immutable
  receipts and current status, and the boundary to future authentication and
  durable admission, including owner disposition for held queued work.

### Fixed

- Distinguish applied cancellation commands from cancelled queued inputs and
  specify the resource revision and historical cursor in admission receipts.

- Clarify authority ownership per independent Unix-user instance and the journal
  schema v1 shipped in 0.0.3, retaining explicit future schema evolution and
  unchanged ordinary opening behavior.

## [0.0.3] - 2026-11-14

### Fixed

- Retain one exact grant-conflict event per immutable attempt, preserve context
  errors on retiring Release retries, and preserve proven retired generations
  during Close without changing their evidence or durable uncertainty.

- Preserve exact empty journal BLOB outcomes when saving Prepared observations;
  reconcile a lost Release response only from the exact retired generation.
  Return unknown on failed grant-conflict commits, verify the pinned DQS probe
  diagnostic, and reject new lifetime admission promptly during Close.
  Add actual-driver, retirement, conflict, cancellation and explicit bounded
  capacity-recovery regressions without changing the schema or dispatch policy.

- Update the design index to describe the implemented guarded dispatch journal.

- Validate journal policy and grant expiry bounds by UTC instant across caller
  and host timezones.

- Preserve admitted journal failures across Release/Close retirement, reject
  late calls without changing retired activations, and capture coherent bounded
  snapshot failure projections. Reject foreign Prepare bindings before waiting
  or cancellation, retaining foreign Save isolation. Cover retirement response
  loss, cancellation, dirty reopen and independently counted no-replay behavior.

- Require native main-file OFD locking before journal ownership acquisition,
  and verify it after a consumed SQLite schema read on preflight and every
  physical connection before writer settings. Rejected competing opens and
  unrelated main-file descriptor closure preserve the live owner's SQL locks;
  processes already using POSIX SQLite locking are refused before acquisition.

### Added

- Document exact-generation Release response reconciliation, empty receipt BLOB
  preservation, failed conflict status, precise DQS diagnostics and prompt Close
  admission refusal. Describe fixed-bound FULL recovery refusal and the existing
  explicit larger-MaxBytes reopen path with preserved evidence and no automatic
  bound increase or capacity guarantee.

- Document journal retirement admission/publication ordering, coherent volatile
  snapshot capture, foreign-call isolation and the narrow settled-Denied
  retirement response-loss case, distinguishing draft and final regressions
  and graceful-close evidence from waited SIGKILL recovery.
- Document guarded journal composition, exact policy/grant binding, failure and
  retirement semantics, bounded eager inspection, and offline crash evidence
  with its limits; retain the accepted lifecycle and OFD ownership contract.
- Add an internal offline guarded dispatch journal on the owned SQLite lifecycle:
  exact-attempt capabilities, immutable trusted policy registration, atomic
  one-use grants, monotonic revocation, durable activation/recovery fences,
  bounded retirement, exact receipts and contradictions, and eager inspection
  with stable event identities. Exercise the frozen coordinator with fake
  boundaries, bounded SQLite faults and waited SIGKILL/reopen fixtures.
- Document journal OFD startup ordering, post-read verification and the separate
  SQLite-managed POSIX shared-memory lock domain, with caller path/filesystem
  responsibilities and the limits of the lifecycle regression evidence.
- Pin the SQLite journal driver and preserve its dependency notices.
- Add Linux journal storage Create/Open/Close/Info with exclusive inode ownership,
  transactional authority and stable identities, read-only identity preflight,
  verified SQLite connection settings, and finite operation and page bounds.
  This lifecycle foundation does not yet implement guarded dispatch journaling.
- Document journal lifecycle APIs, defaults, ownership, connection verification,
  refusal behavior and the distinction between ordinary reopen and unsupported
  restore; link the bounded foundation from the design contract index.

## [0.0.2] - 2026-11-14

### Added

- Add an internal deterministic effect-dispatch coordinator with explicit
  journal and current-authority start boundary ports. This is source-only
  infrastructure and does not qualify a provider or supply durable storage.
- Document the coordinator entrypoint, port obligations and integration limits.

### Changed

- Reject ASCII and Unicode whitespace in dispatch identities and approval IDs.
- Keep cancellation-independent saving an optional integration strategy.
- Clarify dispatch uncertainty projections and future durable recovery obligations.
- Cover cancelled outcome saves and separately retained completion evidence.
- Fence ambiguous dispatch preparation, retain conflicting receipt evidence,
  and recheck grant authority at the actual start boundary.
- Document receipt identity, recovery fencing and fresh grant checks.
- Cover grant target, payload and epoch binding conflicts.
- Validate dispatch journal receipts and isolate payload ownership across ports.
- Clarify unresolved dispatch evidence and the bounded boundary test scope.

## [0.0.1] - 2026-11-14

### Changed

- Clarify inspection marker ordering, cleanup authority and persistence limits.

- Keep the validated FD1 attempt directory open while inspection records an
  integrity incident, avoiding a second acquisition before marker publication.

- Clarify that host inspection can durably record an integrity incident and
  describe the separate durability-failure limitation.
- Persist inspection-detected typed FD1 integrity incidents in the owned attempt
  directory, preserving the start fence and original cleanup authority after
  artifact restoration and reopening.
- Record sanitized source-pinned FD1 and no-pressure entry host observations
  in the host manual while retaining their separate historical provenance.
- Describe the developer qualification foundation and pending production work
  in the README, including the provider-specific Codex mock tests.
- Add the opt-in `--r1-memory` probe mode with separate strict fixed R1
  request/context/control/evidence schemas, exact byte-hash binding, pinned
  read-only control reads, bounded anonymous-memory helper and private self-exec
  child protocol. Test ordering, retention, accounting, deadlines and failures
  with fake allocation and bounded child-only handshake checks. Ordinary probe,
  hold, FD1 and host launch behavior remained unchanged at that source stage.
  Pressure R1 host launches remain unsupported. The later `r1-entry` host case
  admits no-pressure child entry. No pressure run or runtime qualification
  support is claimed by this source change.
- Preserve nonblocking mode on R1 input/output and private child pipes by caching
  raw descriptors before setup. A bounded stalled-handshake regression covers
  pipe timeout and actual abort/reap without running the pressure actor.
- Reject contradictory R1 failure phases, consumed hashes and touch accounting
  in both wire directions while preserving partial touch and failed cleanup.
  Withhold uncorroborated child handshake identities on metadata failure while
  retaining the owned process for bounded cleanup. Document the R1-only
  `/dev/null` qualification dependency for future host integration; no launcher,
  bind, actual pressure run or runtime support claim is added.
- Latch typed FD1 release/collection authentication incidents before fallback
  saves or finalizer reads, so immediate artifact restoration cannot erase a
  detected failure. Keep the original authenticated cleanup authority and start
  fence across restart without trusting never-adopted evidence. Preserve both
  the original cause and save error when an unknown transition cannot persist;
  a failed save cannot establish cleanup. No live qualification claim is added.
- Retain every detected typed FD1 collection integrity incident across failed
  observations, entry authentication and both completion saves, preserving raw
  evidence and original errors. Retain authenticated stop authority before
  budget/handle checks and through refused bound/released saves. Exact cleanup
  remains available with a persistent start fence; failed durable saves retain
  filesystem poisoning. These recovery corrections add no live matrix claim.
- Preserve authenticated typed FD1 bindings during cleanup-only recovery of a
  lost service-start acknowledgement, including interruption during cancellation.
  Reject inconsistent typed cleanup saves. Retain original inspect/stop authority
  after artifact loss or change, exposing `fd1_integrity_failure` and reconciliation
  reason `typed-runtime-integrity-failed`; keep new starts fenced after cleanup and
  restart. Artifact restoration cannot clear the recorded failure or qualify the
  attempt. Recheck integrity after durable observation saves so mid-save artifact
  changes remain inconclusive while retaining raw evidence. This adds no live FD1
  matrix claim.
- Add the explicit optional `descriptor_profile: fd1-typed` host selection,
  frozen directory/leaf plan challenge and metadata-only read-only bind.
  Authenticate separate runtime artifact evidence before durable binding and
  release, preserving historical absent-profile encodings and plan hashes.
  This supplies no live qualification or dynamic backend closure claim.
- Integrate fixed directory128/pipe256/Unix-stream384/regular512 capabilities
  through the owned injector, released final wrapper and typed observation
  collection. Authenticate and retain runtime bindings separately from the
  immutable plan; close every temporary alias/peer before publication, validate
  without channel consumption and preserve strict regular-file link checks.
  Require scoped fresh direct controls, exact typed inventories and complete
  operation evidence. Retain failed observations and explicit socket-admission
  test gaps; the reviewed live FD1 matrix remains a separate lead action.

- Require exact descriptor inventory rows and durable owned stdio regular-file
  identities before accepting complete post-exec host qualification evidence.
  Reject empty or contradictory tables while retaining failed raw observations
  separately from lifecycle cleanup; this adds no runtime support claim.
- Add a versioned final descriptor policy to new bounded host qualification
  attempts: use a private static-driver reexec, pinned-thread close_range
  CLOEXEC/UNSHARE, raw role revalidation and fail-closed syscall.Exec. Keep
  sparse synthetic aliases and restricted weakened controls, separate planned
  receipts from post-exec fake/BPF identity observations, and preserve legacy
  command digests and failed isolation evidence for inspect and exact cleanup.
  This is backend-chain hardening infrastructure, not runtime/provider support.
- Reject qualification ELF runtime metadata touched by overlapping PT_LOAD
  mapping pages, including logically disjoint segments; check host-page rounding
  overflow for both dynamic and runtime string extents.
- Keep qualification campaign fences sticky across early save/create failures
  and later successful completions. Publish phase/reason only after durable
  replacement, validate ownership before failure markers, and retain restart
  fencing without changing cleanup or launch authority.
- Read qualification subject ELF dependencies from loader-visible program headers
  and bounded runtime tables; reject RPATH/RUNPATH tags and malformed dependency
  strings even with absent or mismatched section headers, sharing checks across
  static and dynamic subjects and every frozen launch-chain check.
- Distinguish sandbox probe directory pin and Unix socket/setup failures from
  target open/connect results; reject overlong Unix paths and insufficient nonce
  budgets before fixture access, and require exact nonce replies followed by EOF
  under a bounded deadline while preserving successful open/connect facts.
- Preserve sandbox probe open/connect effects and exact failed stages with
  known errno, while keeping incomplete exchanges inconclusive; specify Unix
  fixture UID projection and complete frozen-path caller requirements.
- Preserve Codex fixture ledger diagnostics on failure and strengthen shutdown
  and oversized request regression checks.
- Check Codex proxy attempts and upstream receipts after bounded fixture shutdown,
  and detect continuations that cross the offline gate before observation.
- Bound Codex qualification subprocess output drains and report cleanup failures.
- Reject extra or malformed Codex mock receipts and unsuccessful terminal turns.
- Check every Codex mock request against the recorded complete tool profile,
  and document the request-gate fixture's admission limits.
- Restore fixture checks for isolated Go environment settings.
- Confine documentation source reads to the checked root.
- Record the verified bootstrap toolchain for offline checks with older Go drivers.
- Isolate Go module mode and confine documentation link checks to their root.
- Enforce the exact Go pin and isolate offline gates from inherited Go settings.

### Added

- Raw `fake-dir-fd`, `fake-pipe-fd` and `fake-socket-fd` observations with an
  optional immutable `fd1-typed` challenge and a strict bounded four-record
  `inherited-bindings.json` schema. Validate context and fixed object identities
  before content access, bound nonblocking channel reads and require exact
  synthetic frames plus EOF while preserving intermediate effects. Owned child
  regressions cover rejection without consumption. The opt-in host integration
  is described above; live FD1 qualification remains separate and existing
  serialized request meanings are preserved.
- Explicit Linux/amd64 bubblewrap qualification driver with durable campaign
  ownership and restart fencing, exact service/slice reconciliation, private
  reexec fake-FD injection, finite ready/hold cancellation evidence and offline
  static builds. Deterministic transcript checks cover crash/identity failures;
  live host behavior and full profile support remain separate verification gates.
- Bounded Linux/amd64 sandbox observation protocol and static probe for passive
  inventory and owned fake file, descriptor and Unix fixtures, plus a flat
  trusted-side reader requiring independent workload-stop and frozen-tree proof.
  Backend execution and qualification decisions remain separate gates.
- An experimental offline Codex mock probe and request-gate fixture; the
  exact external subject is opt-in and remains unqualified.
- Offline checks for local documentation links, references and heading anchors.
- Pinned Go development toolchain, bootstrap, offline doctor and test gates.
- Clarified dispatch fencing, single-use approvals, authorization projections,
  operations views and federation limits in the proposed design contract.
- README entry point to the design contract and its selected client and runtime direction.
- Design contract baseline for authority, runtime recovery, client protocol,
  security qualification, work relationships and executable acceptance scenarios.
- MIT license and copyright attribution to Everett C. Haimes III.
- Initial MCP-first architecture brief and Go core/TypeScript UI decision.
- Federation architecture with directional grants, MCP task and message
  exchange, local admission, revocation, recovery, and acceptance criteria.
- Initial two-host federation target and load-balancing requirements covering
  eligibility, shared quotas, capacity reservations, and safe reassignment.
