# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.15] - 2026-11-28

### Added

- Added the conversation page contract shared by the desk and the seat:
  message IDs that are stable within one viewer's projection, backward and
  forward paging with opaque cursors, the end, gap, reset and withheld states,
  and the desk's `conversation.read` request and response values. No daemon
  serves it yet.
- The seat card shows the four-word binding tag beside the short digest, and
  the decide answer carries it as `binding_tag`. Decisions still compare the
  full digest.
- The seat's card list shows the four-word binding tag on each waiting row
  when the daemon's listing carries the binding digest, and "Open for binding
  tag." when it does not. The card page already shows it.

### Changed

- The leads view shows why a scratch action is unavailable beside its key,
  including separate reasons for writer and viewer tickets. Pressing that key
  shows the reason without sending a command or opening a prompt.
- The console's live attach client now drops input still waiting to be sent
  when it loses the writer role or its connection is replaced, instead of
  letting it reach a screen the user no longer sees. It counts the drop as
  `client-input-fenced` (also `InputFenced` in the live status) and says so in
  one notice. `Config.ReconnectAsViewer` makes the client reconnect as a viewer
  after such a loss, with `Config.ViewerTickets` for the viewer tickets; the
  default keeps the scratch `--attach` behaviour. Queued input given up with a
  connection is now counted as fenced, no longer as `client-input-dropped`,
  which counts only a full queue.
- The live console now shows "EPOCH CHANGED · select the lead again" with
  the old lead and epoch when the attach client stops because the lead's epoch
  changed, shows the role, state and reason while resyncing or waiting for a
  renewed ticket, and no longer gives a stopped session the focus.
- The console's leads view is now viewer-only unless it is started with
  `ataniq-console --daemon DIR --scratch-writer`. Without the flag the hint has
  no `[w]` and says "writer off: start with --scratch-writer", the `w` key
  sends nothing, and the console never asks the daemon for a writer ticket. With
  the flag, `w` is offered for scratch leads only, when the daemon lists the
  writer role in `ticket_roles` or, with no `ticket_roles`, the whole legacy
  scratch interface; a hello without `ticket_roles` no longer offers a writer by
  itself. A writer attach made from the view becomes a viewer for good when it
  loses the writer role, with viewer tickets from the daemon.
- A viewer attach from the leads view asks the daemon for a new viewer ticket
  each time it connects, so a viewer that the holder dropped connects again
  without the owner asking. It is pinned to the lead and epoch of the selected
  row and stops, asking the owner to select the lead again, when the epoch
  changed or the lead is gone. A busy holder or a refusal is shown in the
  status, and an unreachable daemon is waited out. The writer attach is
  unchanged.
- Correct the handoff design and owner runbook to describe fencing before
  commit, rollback at a new generation and recovery from quarantine. The
  runbook now explains which parts of a live cutover are still unbuilt.
- The console's leads view and its guide now describe when a lead's one message
  per epoch is spent: as soon as the daemon has recorded the message, before
  the request is written to the holder, and not when the request is written. A
  scratch lead gets it back in three cases only: the holder answered not
  delivered, the holder refused the request itself (not busy), or the request
  was never sent because the holder's socket could not be dialled or its write
  deadline could not be set.

### Fixed

- The console's live attach pin now treats epoch 0 and an empty lead as
  pinned values, completes the pin only after a Welcome passes the role
  check, and stops when a ticket source reports a stale selection on the
  scratch attach path too, instead of polling for tickets.
- A writer attach from the leads view is now pinned to the lead and epoch of
  the selected row, as a viewer attach is, and so is the viewer it becomes when
  it loses the writer role: if another epoch answers, it stops and asks the
  owner to select the lead again, instead of attaching to it. Quitting the
  console while that viewer fallback is asking the daemon for a ticket now
  cancels the request at once, instead of waiting for it to time out, so the
  lead is not left with an unused viewer ticket outstanding.
- Releasing the writer of a lead's holder now also voids a writer ticket that
  was issued but not yet used, so a ticket from before the release can no
  longer attach a writer after it.
- Scratch and canary leads record an inject before sending it. The inject-sent
  line, which now carries an effect id that no restart of the daemon repeats, is
  written before the request frame, and nothing is sent when it cannot be
  written: the inject is refused as unavailable and spends nothing. A daemon
  killed in between can no longer have a message delivered that its evidence
  does not know. A holder socket that cannot be dialled leaves an
  `inject-refused` line with the code `unsent`, which frees a scratch lead's
  message; the canary's message stays spent. The canary slice, scratch leads and
  runbook guides describe this order.
- Stopping a lead's unit now signals its control group by the identity the
  daemon recorded, not the unit by name. SIGKILL writes to the group's
  `cgroup.kill` through the group's open directory, SIGTERM goes to pidfds of
  the processes the group lists, and a group that was removed or replaced is
  never written to. A state without a recorded control group identity is
  refused, and `systemctl kill` is no longer run.
- Witness configuration files accept trailing ASCII whitespace, including a
  final newline. Configuration errors identify the field or reason without
  printing its value.

## [0.0.14] - 2026-11-27

### Added

- Added `make home-build` and the Linux `ataniq-home` command for the
  checkpoint home identity ceremony. Use `identity-init` to create the home ledger and TLS keys
  and `pins` to print the two public values for the witness operator.
  The read-only `check` verifies the home identity and the witness credentials.
- Added `ataniq-home observe` to append synthetic events for one disposable
  mirror and compare the replayed home head, a challenged witness receipt
  and an optional owner archive.
  Home retirement preserves the ledger and blocks further use of that
  installation. Offline status reports retirement and the last comparison.
- Added the home witness listener and its offline `serve --check` preflight.
  The home can prepare, resume and finalize enrollment with an owner-carried
  receipt. `ataniq-home status` verifies the binding and replays the local
  ledger without contacting the witness.
- `make canary-build` now builds the static hook helper at
  `build/canary/ataniq-hook`. The run kit lists it separately from the
  binaries that `canary-run.sh` uses and hashes.
- Console approval cards show a four-word tag of the card's binding digest.
  Waiting and recent cards show the tag in the list when the backend supplies
  the digest. For daemon listings, open the card to read its tag.
- A client API server can opt into `ataniq.client/2`. Its authenticated
  `hello` lists the operations available for each visible target. Managed
  requests require a verified client identity and a current target permission.
- The Codex holder pack includes an app-server stdio codec for the pinned
  0.172.0 schema. The codec requests a read-only thread with on-request
  approval and binds to the thread the server returns. Each message needs an
  idle thread and reserves its turn start until the server rejects the request
  or names a turn in its answer and reports that turn completed. Forgetting a
  timed-out start keeps the reservation for a late answer. If the outcome never
  arrives, further messages return `ErrTurn` until the session restarts.
  Closing the thread or reporting a system error prevents further messages on
  that thread. Steering checks the target turn.
  A turn-scoped permission reply grants only the selected subset
  and answers the original, unaltered request once. The codec returns an
  ID-matched decline for stale, invalid or excess permission requests, command
  and file approvals, user input, MCP elicitation and unsupported requests.
  Protocol sessions escape backslashes and non-printable characters in event
  text, including message deltas and error details, while preserving newlines
  and tabs.
- The console's leads view has words for the refusal `holder-version-skew`
  of a start, a ticket and a message: the holder and the daemon are other
  builds, so the owner runs the holder and the daemon from one checkout (a
  holder built again, or the daemon restarted with the matching build), and
  restarts a lead that is running. A refused start leaves the lead stopped, so
  starting it again alone is refused again. It also has words for a lead the
  daemon shows `degraded`, with the reason `holder-version-skew`, because its
  running holder is another build: the daemon leaves its unit running, and
  the owner stops the lead, then starts it again.
- Added a durable Linux checkpoint witness that journals receipts and settlement
  fences and lets the local owner recover an interrupted genesis publication.
- The internal desk link authenticates both ends with pinned TLS keys and gives
  each connection its enrolled device principal and role. Revoking a principal
  closes its live connections and removes its device pins.
- Split the enrollment library into home preparation, owner submission and home
  finalization. An interrupted ceremony can resume with the original request.
  The home can save an owner-only binding and verify it against the ledger.
- The daemon serves the host's side of the guided new-project interview over
  the seat gate: the capability `genesis` and its one operation,
  `seat.genesis`, with the verbs start, round, answer, change, summary, finish
  and discard. The questions are the host's own, from the versioned catalog,
  and the answers fill a typed project brief and nothing else: an answer is a
  message from the owner and never an approval. The threads are kept by a new
  store (`internal/genesis/thread`), one file each (mode 0600, written whole
  and synced) in `owner/genesis/` of the state folder, a folder the daemon
  makes with mode 0700 when the gate starts and refuses when it is a link, not
  the daemon user's or writable by group or others. A refused folder does not
  stop the daemon: it logs one line with a fixed reason word (such as
  `folder-link`) and starts the gate without `genesis`, so the phone offers no
  "New project" until the folder is fixed and the daemon restarted. At most two threads are open (a third start is `genesis-busy`), eight
  files are kept, a thread untouched for 14 days is discarded, text that looks
  like a key or a password is refused `genesis-secret-shaped` (a pasted key
  or an obvious assignment such as `password=...`; names, web addresses and
  paths pass, and a bare git commit id is refused), and a thread
  that is finished is frozen with the digest of its brief (an answer, a
  change or a discard of it is `genesis-done`). A file that is not as
  the daemon wrote it is not served and is logged once, in fixed text, and
  never repaired; no log line holds an answer. A restart serves the same
  threads. The gate's start line now lists every capability it serves in words,
  `genesis` last (for example `pair, read, login, decide, watch and genesis`).
  An answer to a round the thread has already taken (a double tap, a second
  tab) is `genesis-stale-round`, the thread's state and not a bad answer. The
  refusal bound counts only `genesis-bad-answer` and `bad-params` of the
  interview, and not `genesis-stale-round`, `genesis-busy`, `genesis-done`,
  `genesis-unknown-thread`, `genesis-secret-shaped` or `genesis-over-cap`, so
  that a few taps never shut the seat out of approvals.
- The tagged build's daemon command gains `ataniqd genesis list` and
  `ataniqd genesis handoff TAG` (`--state-dir DIR` as the other commands), for
  the desk: `list` prints one line per finished guided interview that holds a
  plan (tag, kind, plan digest, finish day), and `handoff` prints, for a
  software plan, comment lines and one command line,
  `qargi project new NAME --methodology gitflow`, with a name made by the host
  from the kind of project and the thread's tag. Ataniq never runs it: the
  owner runs it himself, Qargi prints its plan and token, and creating the
  project needs his own words. The command never carries a URL, a provider, a
  folder, gates, a deployment, a plan token, approval words or any of the
  owner's own text, and is held to a closed grammar. Other kinds are refused
  "A hand-off for this kind of project is not available yet." (exit 3). Both
  read the thread folder without writing anything, whether or not the daemon
  runs.
- The seat shows the project plan of the guided new-project interview on the
  phone. The summary links "See the draft plan" when every required answer is
  given and a plan template serves the kind of project, and otherwise says how
  many answers are filled in (or that there is no template for the kind yet);
  the finished page links "See the plan" and says that handing a plan to a
  project is a later step; it does not say the plan is kept, since the link
  shows what the desk has when it is followed. The plan page
  (`/projects/<id>/plan`) shows "Draft plan" or "Plan, revision N" with the
  title and the first six digits of the project's id, the plan's short digest,
  the fixed lines "A plan binds nothing: nothing has started, and nothing
  starts from this page." and, for a draft, "This is a draft; finishing the
  interview keeps it as revision 1.", the first demo, what waits for you (your
  review points and always "Publishing, releases, pushing code, credentials
  and raising any limit always wait for you, whatever the plan says."), the
  outside services, where the files live, the budget band (advisory), the
  estimate (an estimate, not a promise), the warnings, the milestones and their
  tasks in sections you open, your own notes quoted as your words, each with
  the question it answers above it ("A note" for a note of your own), and "What
  a charter would cover", a draft of words that allows nothing: its list holds
  the work areas, the outside services, where it would stop for you and the
  line "Always stopping for you before publishing, releases, pushing code,
  credentials and raising any limit.", and the monthly budget band follows it
  as a sentence of its own (advisory; nothing enforces it yet). The page has
  two links and no button, form or script, and nothing is signed or started from
  it. A plan that cannot be drawn yet, or a kind with no template, is a page of
  fixed words. The seat's link reads the plan in one call of the interview's
  room and keeps a draft 10 seconds and a kept plan 5 minutes; a change of an
  answer forgets the draft.
  The summary of a brief with answers missing says, above the finish button,
  that finishing now draws no plan and that a finished brief cannot be
  changed.
- The daemon serves the project plan of the guided new-project interview over
  the seat gate: `seat.genesis` gains the verb `plan` (the thread alone) and
  the result state `plan`, which carries the plan record member by member,
  its digest (`plan_digest`, which the wire holds the plan's bytes to) and
  `stored`. On a thread at its summary whose brief is complete, `plan` answers
  a preview, compiled on each read and kept nowhere; `finish` of such a
  thread compiles revision 1 and stores it in the thread's file, in the same
  write as the frozen brief, and `plan` of the finished thread answers that
  stored plan, the same after a restart. A stored plan is never compiled
  again: a later change of a template never alters it. A thread finished
  before this change is given its plan the first time one is asked of it,
  stored once; its file needs no migration. A brief that is not complete
  (the owner finished early) is refused `genesis-not-ready`, and a kind in
  the owner's own words, which no template serves, `genesis-no-template`;
  neither counts against the refusal bound. A result's progress says
  `plan_ready` when `plan` would answer a plan. The log says
  `genesis: a plan was drawn` and holds nothing of the plan.
- A finished project brief can be compiled into a project plan
  (`ataniq.project-plan/1`, `internal/genesis/plan`): milestones and tasks
  chosen from a versioned template of the brief's kind (software, publishing
  or media), with gates, a first demo, every external service named, an
  advisory budget band and an estimate band. The plan is a proposal that
  binds nothing; the owner's own words are kept in it for the owner's eyes
  only, choose nothing and are never given to an agent.
- The seat offers the guided new-project interview on the phone when the
  daemon's gate lists `genesis`: a "New project" button on the card list and
  the projects in progress, then pages of one to three multiple-choice
  questions of the desk's own catalog (each option with a one-line example,
  "Other" in your own words, a short note of about 200 letters (200 bytes), "You decide for me" and
  "Skip" where the question allows them), a summary grouped by what you chose,
  what was decided for you and the defaults, a "Change" on each row, "Looks
  right: finish" and "Discard this project". The pages are made on the server
  and each act is a plain form answered with a redirect; a refusal is a fixed
  line, and text that looks like a password or a key is refused and never
  shown back. Nothing starts and no agent is given the answers: they are kept
  by the daemon in `owner/genesis/` of its state folder and sent nowhere else.
  `seat.genesis` has a new verb, `list`, which answers the open projects, so a
  restarted seat finds them again. The seat keeps 12 of the gate's 60 calls a
  minute (in its 80-second window) for the interview, taken from the reads and
  the decisions (with the Leads pages' room below, the reads now have 12 and
  the decisions 8, 4 decisions). `ataniq-seat` needs nothing
  configured for it, and logs the capabilities each hello of the gate lists,
  with whether the phone offers "New project". Every response of the seat now
  carries `Referrer-Policy: same-origin` (it was `no-referrer`), under which a
  browser keeps the `Origin` header of a form's post to the seat and still
  sends no address to another site; a proxy in front must not set a policy of
  its own on the seat's site, or the project pages' forms are refused. When the seat refuses a call because its own room is spent, or the project pages' limit of one credential is reached, it writes a fixed-text line to its log naming which, at most one in 30 seconds for each, so that "The desk is busy" can be traced.
- The lead holder records its own build identity, read from the Go build
  information of the executable that actually runs (`/proc/<pid>/exe`, never
  the path it was started by): the main module and its version, the VCS
  revision and whether the checkout was modified, and as provenance the Go
  version and the build tags. It is the member `build` of `holder.json` and the
  field `build` of the `start` event of the holder's event log. A holder that
  cannot read it refuses to start, with exit status 2. The member is added,
  and nothing older refuses it: a daemon or `ataniq-scratch` of an earlier
  release never reads `holder.json`, and the canary run kit's
  `ataniq-canary fields` prints any member.
- The daemon (canary and scratch mode) and `ataniq-scratch` refuse a holder
  that is not their own build, with the named reason `holder-version-skew` and
  a message that names what was found and both builds: before a start, from
  the build of the holder binary, and before an attach ticket and (in the
  daemon) a message, from the build the running holder recorded in its
  `holder.json`; a `holder.json` of another epoch than the one to be attached
  is refused first, with `stale-epoch`, as it is not the record of the holder
  that would be asked, and so is, in the daemon, a role shown `starting` whose
  holder has not written its `holder.json` yet (the console's leads view words
  `stale-epoch` as "the epoch changed or the lead is not ready yet"). The same
  build is the same main module path, module version, VCS revision and
  modified flag (the message names both module paths when they differ); build
  tags and the Go version are not compared, and a build without a VCS revision
  is never the expected one. A holder that records no build, as every holder
  before this release, is refused as an older holder. Nothing changes on a
  refusal (a refused message is not spent), and stop and status are not
  checked, nor are the status questions of the daemon's ready observer, so a
  skewed lead can still be stopped. In scratch mode the code of the refused
  receipt of `lead.start`, `lead.ticket` and `lead.inject` is
  `holder-version-skew`. A holder that starts as another build than the one
  checked before the start (its executable replaced in between, which the
  start looks up again) records that build in the `holder.json` it writes
  from its own running executable. `ataniq-scratch start` reads that file,
  of the epoch and of the launch intent it gave the holder, before it reports
  the holder started, also when the holder did not answer its status in time,
  and refuses the start `holder-version-skew` when it records another build;
  a unit is never stopped by it: it is left running and the refusal names
  `systemctl --user stop` with the unit (a `--direct` holder, its own child,
  is ended as after any failed start). In the daemon's scratch mode the
  start is not refused: `lead.start` has already returned a `done` receipt with the state
  `starting` when the ready observer reads the file, after the holder answers
  its status or, when it does not, after each question. A lead whose file
  records another build is never shown `ready`: it is shown `degraded` with
  the reason `holder-version-skew`, whether this daemon launched it or
  adopted it, and a file that is missing or cannot be read changes nothing
  and is read again. A lead an earlier daemon showed `ready` is checked again
  before the new daemon serves: one whose `holder.json` records another build
  than the new daemon's is shown `degraded` the same way, never `ready`, and
  a `degraded` state that cannot be written ends the daemon's start at the
  reconcile step (exit 16), before anything is served. The
  daemon never stops a holder for its build: the unit is left running, its
  ticket and message are refused, and the owner stops the lead and starts it
  again; the next `lead.start` is refused `holder-version-skew` while the
  executable is the other build. The canary's holder is refused at its ticket
  and message.
- The canary-tagged sandbox command `ataniq-hook <row>` reports one bounded
  hook observation using the fields and digests declared for that row. It
  exits silently, including when the hook port is unavailable.
- Initialize each daemon state root with `ataniqd init --state-dir DIR` before
  running it. The registry records the instance authority and store incarnation.
  Use the same command on a stopped scratch root to migrate its existing journal.
- Under the canary tag, a daemon in scratch mode now takes its leads' written
  replies to the owner, read only. Each lead's chat is switched at the desk with
  `ataniqd chat list --config CONFIG` and `ataniqd chat set KEY off|read
  --config CONFIG`, and its quarantine emptied with `ataniqd chat purge KEY
  --config CONFIG` (the operations `chat.list`, `chat.set` and `chat.purge`,
  which the daemon lists after the six lead operations, so the console's check
  of lead control is unchanged, and which the seat gate does not serve); every
  lead is off whenever the daemon starts, a switch takes effect at once, and
  sending is not built (`send` is refused `chat-send-not-built`). A turn-on
  needs one of the four names of the lead's quarantine free, and is refused
  `chat-quarantine-full` (exit 31, with one fixed line) otherwise; the purge
  runs only while the lead is proven stopped (`chat-lead-not-stopped`, exit
  30, otherwise) and removes what it can in bounded steps, each of which
  removes something, wide and deep folders over repeated runs, never
  following a link or leaving the quarantine. While a lead's chat is on,
  one scanner of its own takes its replies every 2 seconds, stops with the
  switch, the daemon or a store that fails, and logs counts only. Every
  `lead.define` and `lead.start` writes the reply instructions into the lead's
  empty instruction file while the lead is proven stopped, by an exchange that
  puts back an owner's version saved meanwhile, never over the owner's own
  text, and deletes no owner's version: one an upgrade displaced or a crash
  left is kept beside the file as `instructions.md.displaced-N`, which the
  daemon reports in its log and never deletes; neither touches the reply folder
  (`/home/lead/.ataniq-replies`), which
  each turn-on of the chat makes afresh. The instructions say that Cards are
  the only path of an approval and a chat message grants no authority, that
  the daemon makes a fresh folder at each turn-on, and that a full folder or
  an odd entry stops chat until the owner turns it on again. When the
  instruction file cannot be installed safely, the define or start is refused
  `chat-folder`, which the console words as such. A new
  read-only chat port, `leaddaemon.LeadChat`, answers the seat's `seat.chat`
  from the same chats (off leads as bare rows and `chat-off`, a stopped chat or
  a failed store as `chat-busy`, at most 40 calls a minute, and `chat-busy`
  until the daemon binds it). See `docs/scratch-leads.md`, "Lead replies".
- Under the canary tag, the stand-in agent now writes one short reply to the
  lead's reply folder (`~/.ataniq-replies`) after each commit it makes
  (`Stand-in: handled <7-character commit id>.`, written to a dot-name and
  renamed to `<seq>-standin.txt`, below one held descriptor of the folder it
  listed), so the outbox can be proved with no AI. It writes nothing when the
  folder is missing, is a link, cannot be written or holds more than 64 names,
  or when three of its own replies are waiting, and never makes or changes the
  folder. See `docs/scratch-leads.md`, "Lead replies".
- Under the canary tag, `internal/leadchat` holds the daemon's side of a
  scratch lead's written replies to the owner, read only and not wired into
  the daemon or the seat yet: a reader of the lead's outbox
  (`/home/lead/.ataniq-replies`, one plain-text file per reply, closed and
  renamed from a dot-name to `NAME.txt`), reached by descriptor from the state
  root with `openat2` and no fallback, without following a link or crossing a
  mount point, that moves each reply it takes into a claim folder of the
  daemon's by one rename that replaces nothing and reads only that claim,
  takes only a settled regular file of the daemon's user with one link,
  readable by its owner, no write bit for group or other and at most 2000
  bytes, drops a claim it cannot read whole within three tries or 30 seconds,
  never claims or removes drafts and other names, stops the lead's chat with
  one line when the outbox holds more than 32 entries, holds a folder, a link
  or a special file, or is replaced, removed or no longer private, and takes
  at most 6 a minute and 60 an hour; and a chat store per lead
  (`<state root>/chat/<layout role>.jsonl`, strict synced JSON lines, at most
  8 MiB, locked and checked by handle, that replays after every compaction)
  that stores each claim at most once in a conversation, drops text that is
  not plain or too long, withholds text shaped like a key, a token or a full
  commit id (any bare word of 40 or 64 hexadecimal digits), keeps 500 entries or
  14 days, and has a per-lead switch `off` or
  `read` that, when turned on, makes a fresh outbox (mode 0700) in a private
  staging folder (`<state root>/leads/<layout role>/staging`), holds it open
  and proves with a probe there that the file system can claim, sets the
  earlier outbox aside unread, an empty one included, in the lead's quarantine
  (`<state root>/leads/<layout role>/quarantine`, at most four, which the
  owner purges), and publishes the held folder at the outbox's name by a
  rename that replaces nothing, refusing the turn-on when the name was taken
  or replaced meanwhile; it deletes the stored replies when turned off, and
  is off again after every restart. See `docs/scratch-leads.md`, "Lead
  replies".
- The live attach pane keeps up to 64 rows that scroll off the main screen
  on this client. In the narrow layout, Page Up and Page Down move through
  those rows. A page stays fixed while more output arrives. Restoring a
  holder snapshot starts a fresh window of recent output.
- Added an internal decoder library for Qargi schema-1 marshal status, check,
  handoff and refusal evidence. It checks evidence per feature and distinguishes
  a held feature from an unreachable remote.
- A guide to the outward keys (`docs/outward-keys.md`): the deploy, push and
  signing keys live in the owner's laptop agent with a confirmation for each
  use, forwarded to the development host only for a deploy, push or release
  session, on a connection of its own (no shared connection, whose forwarded
  agent may be another), and torn down when the step is done (`ssh-add -D`
  on the outward agent, then close the login); a confirmation is accepted
  only right after the owner started the step that causes it, and any other
  prompt is refused as an incident; destination constraints (`ssh-add -h`)
  need OpenSSH 8.9 at both ends of every hop, a FIDO2 key 8.2 on the laptop
  and the authority host as well; the repository fetches through a
  read-only route and pushes with the laptop's key; commits and tags are
  signed with a public-key literal (`gpg.format ssh`), so the private part
  stays in the agent; a cutover replaces every exposed key with a new,
  distinct keypair made on the laptop, and withdraws the old ones from all
  three key stores, the git host, the authority host's `authorized_keys` and
  the release signers file `ah-switch` verifies against, checking there that
  the old signing key is rejected and the new one accepted; for git, the old
  signing key is revoked (`gpg.ssh.revocationFile`) and history is verified
  by object ids recorded beforehand, since `valid-before` is judged by the
  signing time the object itself records; branch and tag protection is the
  owner's setting on the git host.
- The owner can correct an enrolled home with `ataniqd project-home --replace`
  before any lead is defined. A conflict prints the recorded authority.
- Daemon startup and scratch-seat `run --check` list each project missing a
  home and the leads it blocks. Missing homes allow the daemon to start.
- Enroll each project's home with `ataniqd project-home` after initializing
  the daemon state root. Lead define, launch and coordinator admission now refuse
  an unset or foreign home with a distinct code. The registry journals the
  qualified assignment and checks its generation and revision.
- `make scratch-seat-build` builds the daemon with both build tags,
  `ataniq_ownerchannel` and `ataniq_canary`, into the ignored folder
  `build/scratch-seat/`, for the owner's own trial of real scratch leads with
  the seat gate. It runs doctor first and starts nothing, and it is the one
  target that builds with both tags: `make ownerchannel-build` stays the one
  with the owner channel's tag alone and `make canary-build` the one with the
  canary tag alone. No release, no CI job and no other target uses it, and the
  guard tests hold its recipe to exactly the mkdir of its folder and one build
  of `./cmd/ataniqd`, with `doctor` its sole prerequisite.
- The daemon of `make scratch-seat-build` (both build tags) now gives the seat
  gate the scratch leads' replies: `build/scratch-seat/ataniqd run --scratch
  CONFIG --owner-channel --seat-gate IP:PORT` serves the capability `chat`, read
  only, from `leaddaemon.LeadChat`, and the gate's start line is followed by
  `seat gate: chat: replies the scratch leads wrote to their outboxes; read
  only`. The phone's Leads pages show what each lead wrote to its reply folder
  once the desk turned its chat on with `ataniqd chat set KEY read --config
  CONFIG`; every lead is off whenever the daemon starts, and a lead turned off
  is refused `chat-off` at once. `--trial` is refused with `--scratch` and
  with `--canary` (exit status 2, from the flags before any config is read),
  so a daemon with leads never serves the phone trial's samples; the trial
  alone and the owner channel without `--scratch` are unchanged. The owner
  prepares the scratch roots, the non-trial `owner/scratch.json` of the state
  root, the gate's key and pinned seat key and the seat origin. See
  `docs/scratch-leads.md`, "Running real leads with the seat".
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
  then exits 0 or 10 (a refusal says the sitting failed preflight and names
  the start that would fail: the daemon's, or for a holder's socket path
  alone the later start of that lead's holder), binding, locking, making and
  starting nothing. `qualification/scratch-seat/prepare.sh`
  writes the scratch config and the settings folder (modes 0600 and 0700)
  and the files they name in a folder of the proof's own, with leads fresh
  for each sitting (`standin-N`, `live-N`): `--dry` prints from the values
  alone, `--self-test` computes only (an exported `CDPATH` is cleared
  before the script finds its own folder), `--write` checks the state folder, the
  tools and the root first (physical paths, an existing parent that is not
  and lies not in the state folder, its `owner/`, the checkout or a
  temporary folder, by path and by identity, and a root that is not there
  yet), makes the root and every folder below it with `mkdir` of a new name
  and writes only into folders it has just made (a root that is there is
  refused, an earlier run's included), runs git in a
  cleared environment, never writes in the trial's state folder, never
  deletes and starts nothing, and `--status`
  refuses a `--state` that is not the config's `state_root` (the same device
  and inode), passes `--expect-state` with it to each of the daemon's
  checks, which refuses (exit 10) before reading a file of the trial when
  it is not the state root it decoded from the config in that invocation,
  and runs the daemon's `--check` (the printed steps that run
  `--status` carry the selected `--daemon`); with `--record` (before the
  sitting) it then takes the baseline, the digests of the trial's gate key,
  certificate, pins and settings and the daemon's prefix of the key list
  and digest of its head, both of one snapshot (prepare opens neither file
  of the key list), and writes them, whole, into `evidence/step-g` (synced
  to disk, published by a rename that replaces nothing, not even an empty
  folder made meanwhile, and the evidence folder synced before it says
  `recorded`; a refusal of the key check writes nothing, and the step is
  taken again);
  with `--compare` it then refuses a record of
  the digests taken before the sitting that does not name each of the six
  files once (the names compared as text, checked as bytes so that a NUL
  is refused rather than dropped, read once and compared from that one
  reading, before any listed file is opened), compares the trial's gate key, certificate, pins and settings
  with it (any exit status of `sha256sum -c` but 0 is CHANGED), and has the daemon judge the key list. For that the daemon of
  both tags takes `run --scratch CONFIG --owner-channel --check
  --keys-prefix`, which checks the key list and its head with the journal's
  own checks read only, on the files themselves (the ownership, mode and
  link-count checks of the journal's opener and its replay, with an exact
  head; nothing is created, repaired or written, anywhere; a pass is a
  snapshot of the files' identity, metadata, content and head agreement,
  and whether the list can be opened for writing and locked is checked
  only at the daemon's start), and prints the
  list's length and SHA-256 and the SHA-256 of its head, from the bytes it
  checked, and `--check --keys-since
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
  passes the lead's network only on a timeout the probe itself reports
  after the sandbox showed the route it takes to the gate's address and
  the source address the kernel selects for it, both from the kernel's own
  lookup, `ip -4 route get` (a unicast route: not local, a loopback gate
  included, not a reject, unreachable or blackhole route, not on lo; a
  lookup that fails or cannot be made is indeterminate; without them the
  probe did not run), takes curl's "could not connect" (status 7,
  no time to connect), a probe that did not run and a wrapper that ended
  with a status other than 0 for findings, and the runbook
  records its exit status). The runbook's last steps compare the trial's
  files byte for byte with the record and start the trial again. A test in
  `cmd/ataniqd` takes a trial-shaped state folder to the scratch daemon and
  back on the files `--write` makes and shows the gate's key, the pins, the
  paired keys and the trial's settings the same bytes after it; another
  holds the script's gate check to the daemon's.
- The scratch security-key verifier checks console decision signatures against
  the enrolled key, TLS connection and displayed card. Hardware qualification
  and decision dispatch are pending.
- A scratch daemon's gate (`ataniqd run --scratch CONFIG --owner-channel
  --seat-gate ADDR`, both tags) lists `wait-chat` too: its chat cursor covers
  the leads whose chat the desk set to `read` and that a scan has not stopped
  (each lead's switch generation and head), sampled together as one state of
  the desk, and moves when such a lead gets a new entry (a reply, or the line
  that says replies were dropped) or a lead's chat is turned on or off; a
  lead whose chat is off moves nothing, and a reply that arrives while it is
  off leaves no gap in its sequence numbers. Its start line ends
  `... genesis, chat and wait-chat`.
- The seat's log now has a line of counts, at most every 10 minutes and only
  after a period in which the seat refused a call for want of room: `ataniq-seat:
  calls of the link in the last N minutes, taken/refused by kind: public T/R,
  read T/R, decide T/R, genesis T/R, wait T/R`. It holds numbers and fixed
  words only. The rooms are unchanged. A measurement of them is recorded in the
  seat gate design: three card lists reloading every 20 seconds and a lead's
  conversation refreshed once in 10 seconds left 42 of 90 card list reloads busy.
- The seat's card list reads its hints over a stream (`POST /api/watch/stream`,
  server-sent events read with a same-origin fetch), with its long poll as
  the fallback. Both wake as soon as the seat hears of a change; the stream
  holds none of the session's three held polls and names the kind of each
  hint. It says only that the cards moved, with the version the poll
  answers; the list reloads by the same rule as before (at most once every
  20 seconds, never over typed text, and only while it is shown: a hidden
  list reloads once for a change it missed when it is shown again). It asks
  the gate nothing, does not keep the session alive, ends with the session,
  a logout or the seat's shutdown, and lasts 25 seconds at most from its
  request, its opening included, before the list opens another. At most 3
  streams are open for one passkey (a fourth list falls back to the poll),
  12 are opened a minute, 32 are open in all, and 3 openings of one passkey
  (8 in all) are in progress, bounded before the body is read. Each stream
  the shown list opens holds the notices back for 30 seconds and counts the
  cards it shows as seen, as a poll does; nothing the seat writes on it
  counts, so a card the list has not shown yet still brings its notice. Any
  refusal or failure of the stream (its first heartbeat not within 8
  seconds, a hint it cannot read, a stream cut inside an event), or a
  browser that cannot read one, falls back to the long poll, which the Card
  page keeps. A proxy in front must pass the stream on unbuffered and
  uncompressed; not yet qualified on the deployed path or on a phone.
- The phone trial now shows a Leads directory and read-only sample conversations
  after login. Open a shared lead to read, page through its history or refresh.
  The Leads pages carry their own STAND-IN banner: "These leads and
  conversations are samples held by the phone trial. No real lead is read, and
  nothing here is an approval." Every sample line, the system lines too (such as
  "Sample: lead restarted, epoch 3"), begins "Sample:". The Leads pages' calls
  are reads of the desk, counted in a room of their own at the gate, apart
  from the card lists' reads (see the entry on the Leads pages' room).
- The seat gate has a read-only view of the leads' conversations, served in
  the phone trial only from STAND-IN samples: the gate operation `seat.chat`
  under the capability `chat`, with the verbs `leads` (up to 8 leads, each
  with its chat switch `off`, `read` or `send`, a state word, an epoch and a
  head; an off lead's row carries nothing but its name and switch) and `read`
  (one page of a lead's retained conversation after a sequence number, at
  most 16 entries, paged by encoded bytes within 16 KiB, with `more` and
  `reset`). No verb sends. An entry's text is up to 2000 bytes of plain text
  (line feeds allowed; other controls and bidirectional and format controls
  refused), a lead's text is untrusted and shown to the owner only, and a
  shortened entry is marked `cut`. The refusals `chat-off`,
  `chat-unknown-lead` and `chat-busy` are not counted by the refusal bound.
  `ataniqd run --owner-channel --trial --seat-gate ADDR` gives its gate the
  STAND-IN (`internal/chat/standin`: three sample leads and conversations
  held in memory, every result marked `stand_in`), and its start line ends
  `... genesis and chat` followed by a line that says the chat is the
  STAND-IN's; every other gate lists no `chat`. No lead, conversation file or
  other port of the daemon is read or reached. The seat's lead pages are not
  built yet.
- Repeat `ataniq-seat --gate-entry NAME,HOST:PORT,PIN_FILE` with one
  `--identity DIR` to see cards, projects and leads from several gates at
  one seat. Log in to each instance separately. Rows name their instance
  and retain its pinned identity when opened. An offline gate shows the
  session's last directory rows marked stale while other gates remain usable.
- The seat's Leads pages follow the chat hint over the card list's stream
  (`POST /api/watch/stream`, opened with `{"leads":"<cursor>"}`), which
  carries one kind of event, `chat-changed`, with the cursor alone: never a
  lead, a count or a text, and nothing of a lead whose chat is off. A
  conversation open at its newest message shows new replies by itself, read
  again through its ordinary page at most once every 40 seconds (2 calls of
  the Leads pages' 5 in 80 seconds: one following conversation fits, and a
  refresh by hand or a second following tab beside it can meet the busy
  page) and only while shown, never over typed text, an open dialog or a
  link or other control that has the focus; scrolled away, holding the
  focus or on older messages it shows "New replies." with a link **Show
  newest**, which loads the page that holds the newest message, and the
  Leads page shows "Leads changed." with **Refresh**. A read set off by
  another lead's reply, with nothing new for this lead, shows the
  conversation's newest messages again, as its own address does, and keeps
  following, rather than an empty page. Nothing takes the
  focus, and the status line is the page's one live region. The stream asks
  the gate nothing, shares the card list's limits (3 streams of one
  passkey, card lists and Leads pages together, 32 in all), takes no
  notices' foreground lease and counts no card as seen. With no live hint
  (a gate without `wait-chat`, a lost link), in a browser that cannot read
  the stream, or on any failure the page says "Not live", moves no more and
  keeps its Refresh link; there is no poll of the chat. In the phone trial
  the samples never change, so the pages never move. Not yet qualified on
  the deployed path or on a phone.
- The seat's link asks for the chat hint: its hello lists `wait-chat`, and its
  one watcher sends the extended `seat.wait` to a gate that lists `wait-chat`
  and `chat` (the plain one to any other, and to one that refuses the
  extended params), with no call or room beyond the waits'. Each wait is
  bound to one connection: it is not sent when the connection changed after
  it was made, and never carries another connection's cursor. The hint (the
  cursor and the connection it came on) is dropped on a lost link, also
  while the watcher pauses, a new connection, a gate that no longer lists it,
  an answer without a cursor and any failed wait, and is never read as live
  for a connection that is not the current one. The seat's pages do not use
  it yet.
- The seat's pages behind the login have a main navigation: a short row of
  plain links at the top of every card and project page, "Cards" and, when
  the desk runs new projects, "Projects", with the current part marked in
  bold with a bar and announced as the current page, and a "Skip to content"
  link that the first Tab reaches. The status, pairing, login and error pages
  have none, the row adds no Log out, and the plan page still has links only.
  In a browser window at least about 960 pixels wide (60rem) the row becomes
  a column on the left that stays in place while the page scrolls, beside a
  column of comfortable width (wider for the card list and the project
  list); narrower windows keep the phone layout.
- The seat gate's live wait can carry a chat hint, opt-in on both sides: a gate
  that serves `watch` and `chat` and whose chat port gives a cursor lists the
  new capability `wait-chat`, last, and then also takes `seat.wait` params
  with `chat` (the cursor the seat last saw, 0 for none) and answers with
  `chat`, the cursor now, an integer up to 2^53-1 and never any text. The
  cursor is a keyed digest (HMAC-SHA-256 under a random key the daemon makes
  for each binding of its source and never sends) over the leads whose chat
  the desk switched on (their key, switch generation and head) and nothing
  else: a lead whose chat is off moves no cursor and changes no answer or its
  timing, and a cursor says nothing a seat can recognise across a restart.
  Cursor equality is only a change hint and never authorizes a read. A chat
  change answers a held wait no sooner than 18 seconds after it began, a Card
  change at once, and a cursor source that fails gives the plain Card result
  with no `chat`. The extended wait never recovers an uncertain journal: it
  answers `unavailable` and leaves the recovery to the daemon's tick. A seat
  or gate that does not know the extension sends and receives exactly the old
  shapes; the plain form now refuses a `chat` member. The phone trial's gate
  lists it (its start line ends `... genesis, chat and wait-chat`); its
  STAND-IN cursor never moves within a run.
- The federation guide lists the state root's identity files, journal,
  migration backup and daemon lock, with their contents and recovery roles.
- Add `ataniq-witness enroll-submit` so the local owner can compare the
  Prepared digest with the home device, submit enrollment over the witness
  owner socket and archive the signed receipt and witness status.
- Added witness owner commands to archive a freshly challenged slot head,
  release a fenced writer's session, and retire the slot with a disposition.
- Ship `ataniq-witness` as an optional authority-host release component. It
  polls one pinned home, records durable checkpoints and serves signed status
  and storage acknowledgement over a private owner socket. The release switch
  checks its health and stops it on rollback. The operator guide covers pin
  exchange, recovery, backup limits and re-enrollment.

### Changed

- Instance authority fingerprints now include the domain and version of the
  Ed25519 authority in their SHA-256 input.
- The outward keys guide documents authority-host release signing with a
  passphrase-protected Ed25519 key held on the owner's laptop, including
  per-use confirmation and a 30-minute agent lifetime.
- The daemon console opens the lead directory whenever the daemon offers
  `lead.list`. The client checks each command against the current connection's
  offer and keeps the original command ID if a reconnect prevents a retry.
- Every existing daemon state root needs `ataniqd init` once. Back it up with
  the daemon stopped. Older builds cannot open the journal after init. The
  daemon refuses hard-linked journals and journals replaced while opening.
  Scratch preflight now reports identity refusals with the daemon's exit code.
- Scratch lead instructions now direct executors to return results and patches
  to the home lead and name the daemon's `project-home-foreign` refusal.
- Managed leads now carry an instance-qualified Qargi lead name. Coordinator
  labels use the same parent name. The sandbox profile digest changes, so
  existing profile digest pins must be recomputed. Running leads keep their
  old `QARGI_LEAD` until their next start.
- At 80x24 and phone SSH sizes such as 60x20, `ataniq-console --attach`
  shows the live terminal below the role, connection state and `q` detach
  hint. A writer requests the viewport size. A viewer sees the top left
  of the holder's screen.
- Claiming a project locally after an executor-only designation warns and
  requires `ataniqd project-home --confirm-self`.
- Existing daemon state roots need a project home set once per project. With
  the daemon stopped, run `ataniqd init --state-dir DIR`, then
  `ataniqd project-home --state-dir DIR --home self PROJECT` on its home
  instance. Use that instance's authority ID on each executor.
- The lead registry reads a journal written by another build of the daemon
  when that build added record members and nothing else: replay ignores a
  record member it does not know instead of refusing the whole journal as
  corrupt, and a launch intent's profile digest is taken from the requested
  profile as its define record stores it, so recorded launch digests stay
  valid when a later build adds a member. A value it does not know in a member
  it knows (a provider, a desired or shown state, an outcome) is still damage,
  as are an unknown record kind and another format version: the journal does
  not open and the daemon exits 14. A role whose stored profile has a member
  this build does not know, or names a pack, or whose define record repeats
  a member anywhere, its envelope's `payload` and the line's final `hash`
  included (exactly or as a case
  alias encoding/json matches to the same field, which no build writes),
  replays but is not launched
  (`ErrLaunchProfile`) until it is defined again: in scratch mode its start is
  refused with the code `profile-not-launchable`, and the console's leads view
  says to define the lead again. A fixture journal written by an earlier build
  is read back byte for byte by the tests.
- Init reports journal creation, migration with a record count, or an existing
  binding. Migration keeps the exact v1 bytes in `registry/registry.jsonl.v1`.
  Older builds cannot open the migrated journal.
- The seat's Leads pages now have a room of their own at the gate, 5 calls in
  80 seconds (opening Leads, selecting a lead and refreshing it once), 2 taken
  from the reads, which keep 12 for the card lists, 2 from the decisions (now
  8: 4 decisions in 80 seconds) and 1 from the watcher's waits (now 5: an idle
  seat's 4 and one for a reconnect, which was measured to meet no `wait-full`
  and no wait for room); pairing and login keep 12 (6 logins), which pairing a
  phone and then logging in on it needs, and the total stays 60 requests a
  minute. A Leads page past its room says "The desk is busy. Try again in a
  minute." and never takes a card list's read: measured with three card lists
  reloading every 20 seconds and a conversation refreshed once in 10 seconds,
  no list reload is busy (42 of 90 were while the Leads pages shared the
  reads), and the owner's visit of the Leads pages once in 80 seconds never
  is. The seat's log names the Leads pages' calls kind `chat`, and its line of
  counts gains `chat T/R` after `read T/R`.
- The seat's Leads pages now show a read-only cockpit with lead context, a
  paged conversation, a link to Cards and a timeline. On a phone the page opens
  on the lead's name and the conversation, then the other panes. The Leads page
  says "Chat is off for this lead. Turn it on at the desk." under an off lead.
  The trial labels its sample project, gate and events STAND-IN.
- The authority-host seat unit reads one or more named gates from
  `SEAT_GATE_ARGS` in `seat.env`. Replace the old `SEAT_GATE` address with
  one `--gate-entry NAME,ADDR:PORT,PIN_FILE` per gate. Seat notices are off
  with named entries, including a single entry.
- `ataniqd init --restored` preserves the authority of a restored key and
  creates a fresh `store.incarnation`. The old journal belongs to the previous
  incarnation and is refused.
- `make test-canary` and both runs of `make test-ownerchannel` no longer run
  the repository checks in `./tests/`, which build the same under every tag
  set; they run once per suite, untagged, in `make test`, and a tagged run with
  no tagged package runs `./tests/` alone, so that `go test` is never given an
  empty package list. A new check, `TestCanaryTagTestsClosure`, lists the
  `./tests/` test closure under each of the four tag sets and fails when a
  file of it, a Go file or another source (assembly, C and the like), has a
  build constraint that names `ataniq_canary` or `ataniq_ownerchannel`, or
  when a tag set selects other sources or other cgo flags (compiler and linker
  flags and `pkg-config` packages, in order, package by package) than the
  untagged build, so that running `./tests/` under a tag again is a deliberate
  change. It reads only the headers of the sources the go command reads on
  the host, each once. The targeted checks of both tags are now run once,
  without `-tags`.
- Witness identifiers require printable ASCII. Initialization and offline
  publication each print one success line after closing the store.
- The witness unit documents owner-local provisioning. The host no-AI
  check reports a missing egress drop-in even when local health passes.
- Witness release lists document the inherited seat and notifier commands
  and check their order when the overlays change.

### Fixed

- Recover an interrupted first stage write at the confirmed enrollment head.
  The home clears the unsent stage after claiming a fresh witness session.
  Both home preflight commands check retirement and the installation directory.
  The witness certificate can be one raw DER certificate or a single PEM
  CERTIFICATE block.
  Inhibited enrollment and observation now direct the owner to switch status.
- Home observation reports the original interrupted advance and its owner
  action so the owner can reconcile the session before retrying. A restart can
  reclaim the session after an intent was staged before its ledger append.
  Status marks stale comparisons and shows the latest restore or retirement
  refusal. Observe prints start and launch counts only with a comparison verdict.
  Identity initialization syncs the seed, TLS files and their directory
  entries before it reports success.
- Attach retries back off when the holder repeatedly grants the wrong role.
  Each mismatch still releases the granted place immediately.
- A console that redeems a ticket for the wrong role now detaches immediately,
  freeing the holder's place and reporting the roles it requested and received.
  The attach client also supports renewing viewer tickets in memory after a
  lost connection or a resync.
- When a service departure cannot be confirmed, `ah-switch` reports the unit
  and its observed state. For a failed service it gives the owner the
  `systemctl reset-failed` command to run before retrying.
- Authority host switches record the owning service of each shipped service
  drop-in. A release that configures a host service through a drop-in must
  leave that service inactive or absent before the next release reloads.
- Authority host switches and rollbacks keep decisions inhibited when a legacy
  deployment lacks its service inventory. After stopping the services, the owner
  can record the inventory from the original import with
  `ah-switch recover --services-stopped` and retry the departure.
- Forward switches stop and check every departing service before reloading
  the target, including on retry. Notifier and witness overlays now stop the
  notifier. Add its stop to the host list when upgrading an existing host.
- Authority-host release validation, the standalone no-AI check and switch
  import now refuse user services and their drop-ins under `etc/systemd/user/`
  with `user-service`. The switch checks departing services through the system
  manager.
- Authority-host rollback and recovery verify departing services with systemd
  before reloading the target and keep decisions inhibited on failure. Witness
  releases validate the host’s canonical witness stop before activation.
- An explicit authority-host rollback stops services from both the host and
  departing release stop lists before reloading the target. A failed stop keeps
  decisions inhibited and the rollback available for retry.
- Both authority-host release checks report `unit-syntax` for systemd units
  and drop-ins that begin with a UTF-8 byte-order mark.
- The authority-host release and host checks report `unit-syntax` for
  non-ASCII whitespace and invisible format characters in systemd units and
  drop-ins, so those characters cannot hide a service from release validation.
- A `project-assignment-busy` reply from the daemon now gives the canary
  command the same refusal exit status (30) as other assignment refusals.
- Scratch-seat `--status --record` gives the same refusal across GNU `mv`
  versions when another folder takes the baseline name. It keeps that folder
  intact and reports where the staged baseline was left.
- The client API counter snapshot now counts requests answered `forbidden`,
  including refusals during parsing, dispatch, and the delivery recheck.
- A late Codex start answer for a completed turn now preserves later activity.
  The holder keeps a second message blocked while the thread is busy and can
  still report progress and interrupt the newer turn.
- The Codex codec keeps progress from each live turn when turns overlap.
  Interrupting targets the latest live turn announced by the server. A new
  message waits for all live turns to finish and an idle thread status.
- The Codex holder accepts the next message when a turn completes before its
  start answer arrives. A stale idle status for an accepted turn no longer
  hides its progress or prevents the holder from interrupting it.
- The console keeps directory selections and conversation scroll positions with
  their home, project, lead, conversation and launch epoch. Offline rows stay
  visible as stale. A changed target clears its selection and open draft.
- The console's leads view now explains why a lead is degraded when the holder
  gives no usable status or reports that the agent exited.
- An offline cockpit refresh keeps the leads view open with its message draft
  or stop confirmation intact.
- The leads view names the operation or ticket role the daemon does not offer.
  A message refused for a missing operation leaves the editor available for
  another message in the same epoch.
- After a lead list outage, a receipt that confirms nothing was sent lets the
  operator try the message again in the same epoch.
- The terminal console no longer exits when it opens a branch-prune approval
  card. A prune changes no file and so has no diff summary; the console now
  opens the card and can approve or deny it from the desk.
- The leads view shows why a scratch lead is degraded and lets the owner stop
  its current epoch. Unreadable rows still refuse every command.
- A change on the new-project summary that clears later answers now has their
  questions asked again, after the last round too, so the brief can still be
  completed and planned.
- The new-project interview now asks every required question within its six
  rounds, so a finished interview can be planned.
- A new-project interview that answers "You decide for me" on the language,
  the sign-in or where a web app runs can now be planned: those answers count
  as given, and the plan says some answers were decided for you. Finishing
  accepts them as your answers, as the summary now says.
- The holder requires `ATANIQ_AUTHORITY_TAG` to contain exactly 16 lowercase
  hexadecimal digits before a managed lead can start.
- `ah-noaicheck` reads continued directives in installed systemd units,
  drop-ins and manager configuration. The stock systemd-pcrosseparator
  service passes the host scan, and a provider key set on a continuation
  line is reported by name.
- A damaged `store.incarnation` now reports how to remove the damaged file
  and restore the original key with a fresh incarnation.
- `ataniqd init` checks a pre-identity registry journal before creating an
  authority key. A corrupt record leaves the original journal available for
  repair and the root uninitialized.
- Daemon identity and journal refusals identify the affected path. Supplying
  `--label` after the first `ataniqd init` now reports an error.
- The terminal lead-control table keeps an open message or stop confirmation
  on its lead across renames and reordering. If the lead disappears or its
  epoch changes, the table clears the selection and field. Use j or k to
  select a lead again. A ticket for a selection that changed cannot attach.
- One malformed lead row or last receipt no longer hides the whole leads list.
  The console shows the reason on that row and keeps other leads usable.
- A key from the pre-release instance format now reports its origin and
  directs the owner to run init on a fresh root.
- A malformed `--home` prints the accepted format and exits with usage
  status 2. Missing-home refusals explain that enrollment needs a stopped daemon.
- Init validates the legacy journal under the migration lock and removes stale
  `.registry-check-*` replay copies left by earlier builds.
- Restoring an incarnation with a journal in place now requires explicit
  acknowledgement with `--leave-old-journal`. Scratch preflight reads the
  journal header and reports a foreign binding with exit 14. Missing-key
  messages distinguish an intact root from a restore.
- The authority-host switch counts exit 5 from a single-unit systemctl stop as
  already stopped, including when retrying or settling a rollback.
- Rollback can leave an unverifiable release after the host stop list succeeds.
  The switch skips the untrusted stop list and qualifies the target.
- The sandbox guide now describes identity validation at daemon startup and
  the public tag passed to the holder at launch.
- A scratch lead stays ready through a single missed health poll. Two
  consecutive misses in one epoch mark the holder unreachable, and a confirmed
  agent exit is shown immediately. Successful status polls no longer fill the
  holder's event log.
- Scratch leads now show `degraded` at the desk and `unknown` on the phone
  when a health poll loses contact with the holder or finds the agent has
  exited. A fresh healthy status for the same launch restores `ready`.
- The scratch guides list the registry header check and its exit code, and
  explain when the daemon takes the journal lock and when holders check their
  socket paths.
- In scratch mode, the daemon's scan at start of the leads it adopted
  `starting` could, when a lead was stopped and started again while the scan
  ran, start the observer of the old epoch in place of the new epoch's,
  which then ended without looking at the new holder: the lead stayed
  `starting`. An observer is no longer started for a lead that has one of a
  later epoch.
- Scratch-seat step f enrolls every configured project before the trial
  restarts. The switch to the proof daemon confirms those homes.
- The seat no longer reuses a real lead directory for 20 seconds. Each Leads
  page asks the desk for the directory and the transcript, so a lead whose chat
  the desk turns off shows as off on the next page, with nothing left of its
  state. Only the phone trial's sample directory and pages are still reused.
  The "Lead restarted" notice no longer carries over a reset, a change of the
  lead's chat setting or a fall of its epoch, where the name may belong to
  another lead.
- A seat conversation opened without a page cursor (`/leads/daemon/KEY`, the
  directory's link, **Refresh** and **Jump to newest**) now ends at the
  lead's latest message: when long messages did not all fit on the page, or
  replies arrived while it was read, it reads on toward the latest message,
  at most twice more (up to 4 of the Leads pages' 5 calls in 80 seconds),
  instead of stopping short with **Show later**. **Jump to newest** links to
  that address rather than to a window counted back from the head, which the
  same limit could cut short. A further read that meets a busy desk keeps
  the page read so far.
- The daemon's seat gate no longer stops for good when accepting a connection
  fails, as it does when descriptors or memory run out: it tries again after
  a wait that starts at 5 ms and doubles to at most 1 s, so phone approvals
  come back by themselves once the cause is gone. The daemon logs
  `seat gate: accepting a connection failed; retrying until one succeeds` when
  such failures begin.
- When the desk answers that a passkey is no longer paired, the seat ends
  every session that passkey logged in (another tab's or another phone's
  login, and their open card-list streams), not only the session whose tap
  learnt it. A logout still ends its own session only.
- The seat keeps phone-shaped text plain on iPhone, gives pages and answer
  links clearer names, and offers a fresh read on temporary errors and
  pending decisions. Status messages, touch targets and scrolling navigation
  are more accessible. Rejected answers lead back to the question to fix.
  The interview shows actual progress and explains when answers become final.
  Plans explain their limits and let you expand the draft charter details.
- Daemon identity checks follow symbolic links in parent directories of the
  state root. A linked identity file is refused with its name.
- A viewer using a ticket service waits for a fresh ticket before dialing.
  Empty polls no longer register refused handshakes at the holder.
- The authority-host witness guide uses port 7444 for the home endpoint,
  leaving 7443 for the seat gate.
- The witness link reports a refused owner read as refused when the owner
  certificate is missing or untrusted, even if the request write fails.
- Witness preflight reports an occupied or unusable owner socket path and
  rejects overlapping state, key, guard and runtime locations.
- The recovery guide names the switch guard checks before acknowledgement
  and separates offline signing-seed custody from the store backup.
- The witness unit stops retrying fixed refusals and limits repeated starts
  after a crash. The operator guide covers attended restart.
- The witness names the obsolete `guard` key on refusal. Service startup checks
  on-disk egress settings without invoking systemctl. Owner preflight and the
  host no-AI check still verify the manager’s effective grants.
- The witness recovery guide directs interrupted publication to offline
  completion immediately after signed status. It reserves guard clearing and
  acknowledgement for recovery from storage errors or an unclean process end.

### Security

- AP14 level 1 for an AI host (`deploy/ai-host/README.md`): the owner takes
  passwordless and cached sudo away from the everyday user by hand, from the
  guide's steps: the edits with `visudo -f` (remove the `NOPASSWD:` tag in
  place, keep one password-asked grant, never delete a rule), the drop-in
  `/etc/sudoers.d/zz-ap14-level1-NAME` (`Defaults:NAME timestamp_timeout=0`,
  checked with `visudo -c`) and the root-owned standing-grants register
  `/etc/ataniq/grants`, one fresh root-owned file per grant; a register
  folder found not root's or writable by others is set right in place, never
  moved or replaced.
  `deploy/ai-host/ap14-level1.sh --check --user NAME` is the read-only check
  of the result, run by the owner as root from a copy installed as root's own
  file: it runs `sudo -n -l -U NAME` once and judges only what sudo prints
  (a `NOPASSWD` command; `targetpw`, `rootpw`, `runaspw`, `!authenticate` or
  `exempt_group`, global or command-specific, judged in every word of a
  setting so that a quoted value with blanks is read; a non-zero
  `timestamp_timeout`; no `timestamp_timeout=0`), checks the register and every folder above it by
  stat rules, and prints plain steps for the owner, never an edit command. It
  changes nothing. It stops, never clean (exit 3), on a failed or unknown
  sudo output, anything on sudo's error output, an unreadable register, and a
  user with no sudo grant until the owner answers that root can log in
  another way (`--other-root-login`). Verification is `--check` again and the
  owner's `sudo -k; sudo -n true` in a new login, whose limit the guide states.
  Groups, polkit, doas, setuid files and root-run paths are a checklist for the
  owner; the user's own shell start files and `PATH` folders are a known limit
  of level 1. `--self-test` runs unprivileged and judges saved `sudo -l`
  outputs and register listings in place, never running sudo and writing
  nothing. Agents run only `--self-test` and the Go tests.
- Authority-host checks reject systemd line continuations and control characters
  in units and drop-ins with a `unit-syntax` finding.
- Managed desk streams accept only `ataniq.client/2` and check the enrolled
  principal's permission for the requested target at dispatch and delivery.
  A request that supplies its own principal is refused. The local version 1
  socket serves its own scratch inventory and extensions.
- The authority host's `deploy.sh` logs in with the checked ssh agent's key
  only, isolated from the development host's ssh configuration: every ssh and
  rsync runs with `-F /dev/null` (no `IdentityFile`, `IdentityAgent`,
  `ProxyJump` or alias of a config applies) and explicit options: batch mode,
  no key file, `IdentityAgent=SSH_AUTH_SOCK`, public-key authentication only
  (no password, keyboard-interactive, GSSAPI, host-based or PKCS#11), no proxy,
  no shared connection (`ControlMaster=no`, `ControlPath=none`), no agent
  forwarded further, and `StrictHostKeyChecking=yes` with only the known-hosts
  file the owner names. The destination comes from the owner's environment:
  `AUTHORITY_HOST_SSH` must now be `USER@HOST` (an ssh alias no longer works),
  `AUTHORITY_HOST_KNOWN_HOSTS` is required, and `AUTHORITY_HOST_PORT` sets a
  port other than 22. Except for `--self-test`, which prints the options, it
  refuses when the known-hosts file is missing, or `SSH_AUTH_SOCK` is unset or
  its agent holds no key, and `--dry` ends with a reminder that the use must
  have popped a confirmation on the laptop. DEPLOY.md has the owner's
  procedure, "The deploy key": kept on the laptop and loaded with
  `ssh-add -c -h -t`, the agent forwarded for the deploy session only, a
  `restrict,from=` line on the authority host, how to prove it (the negative
  check tests each identity under review, one key file at a time, on the
  driver's own connection: the same destination, port and known-hosts file,
  no ssh configuration, no proxy and no agent), and the same with a FIDO2
  key; the git push command of `docs/outward-keys.md` uses the same options.
- Authority-host release checks require an unprefixed executable path for each
  systemd service or socket command, as used by the shipped units.
- The phone's `kinds` setting accepts only WebAuthn kinds. Listing
  `security-key` raises a settings error. Scratch security-key verification
  requires an explicit opt-in, a laptop desk, and a disposable local
  `ref-move` card for `feature finish`.
- Scratch security-key verification requires witnessed enrollment. A stored
  `attested` claim is rejected until enrollment can verify attestation.
- Scratch security-key enrollment and signature verification reject malformed
  Ed25519 keys and points of small order, including the identity.
- The seat refuses a POST that carries `Sec-Fetch-Site` or `Content-Type`
  twice, and any request with two `__Host-seat` cookies, now counted before
  they are parsed, so a second cookie with a malformed value no longer hides.
  The card-list stream takes a body of exactly one string member `version`
  (absent, null or repeated is 400) and opens only once the seat's watcher
  has a version.
- The no-AI check detects TCP listeners owned by the witness account
  after its unit link is removed, including on baseline-approved ports.
- Require a literal home endpoint for witness polling and document the
  single-destination output filter and matching home source restriction.
- Release checks reject references to the witness binary in other units’
  executable commands, including quoted paths and shell arguments.
- The witness now uses the switch guard at `/var/lib/authority-host` and
  refuses an unusable guard directory during preflight. Remove the former
  `guard` key from the witness configuration.
- Witness preflight and the host no-AI check validate the owner’s single
  address grant against the canonical numeric home endpoint.
- Both authority-host release checks verify the executable path in unit
  commands and reject links inside the release. Installation checks the
  witness unit link target. Witness preflight and the host no-AI check
  verify inherited address grants and the running system manager’s policy.
  Group- or world-writable witness TLS directories fail preflight.
- Release checks require every shipped witness unit setting and reject
  release drop-ins that override the witness sandbox.

## [0.0.13] - 2026-11-23

### Added

- Add `deploy/authority-host/bootstrap/host-bootstrap.sh`, the one-time
  setup of the authority host, run on the host as root. `--dry` (the
  default) prints every step and changes nothing; `--apply` creates
  `/opt/authority-host/releases`, `/etc/authority-host` and
  `/var/lib/authority-host` (0755, root:root), installs the host's
  `stop.list` (the single line `/usr/bin/true`, an existing list is kept)
  and installs the switch binary given with `--switch-bin` as
  `/usr/local/sbin/ah-switch` only when its SHA-256 is the one given with
  `--switch-sha256`. It writes no switch policy (the first scratch deploys
  run in manual mode), links the Caddyfile to the current release only with
  `--link-caddy` (keeping the old file once as
  `Caddyfile.pre-authority-host`), refuses any folder above what it touches
  that is not a real folder owned by root and closed to group and others,
  and never starts, restarts or enables a service.
  Add `deploy/authority-host/deploy.sh`, the deploy driver run on the
  development host: with `AUTHORITY_HOST_SSH` (never printed; the output says
  `the authority host`) it runs `ah-release check --no-noaicheck` on a built
  release folder (never the release's no-AI check, which would report this
  machine's own agent tools),
  copies it to the host's incoming folder with `rsync -a --delete`, and runs
  `sudo /usr/local/sbin/ah-switch --from INCOMING/ID ID` over `ssh -o
  BatchMode=yes`. `--dry` (the default) only lists what rsync would copy and
  prints the ssh command (it contacts the host read-only, through ssh and
  `rsync -n`; the remote rsync reads the destination, no files are copied and
  the switch is not run), `--go` does it and is refused unless
  `--confirm ID` equals the release id, and `--self-test` computes the
  commands and starts nothing (no ah-release, rsync or ssh).
  The driver never runs a file of the incoming folder: the no-AI check is
  the switch's own, on the verified copy it imports, before anything
  changes. The switch prints no `HIT` line of that check, only that it failed;
  `DEPLOY.md` has the owner run the checker by hand to read them, and says what
  a repeated `--go` of an imported release (`release-exists`) and a `rolled-back`
  result against a `failed-recovery` one mean.
  `DEPLOY.md` links the Caddyfile before the first switch (the shipped
  release restarts caddy in the switch and requires `/healthz` from it) and
  has the owner acknowledge the outcome (`ah-switch ack --txn ID[,ID...]
  --asked WORDS`, the ids copied from `ah-switch status`) before a later
  switch or a rollback, which the switch refuses while an outcome is owed.
  `DEPLOY.md` ("Who may switch") gives the sudo rule for the deploying
  user, limited to the switch form (`--from INCOMING/ID ID`, an argument
  regex for sudo 1.9.10 or newer) and checked with `sudo -l -U`:
  `inhibit-clear`, `ack` and `recover` run only from the owner's own root
  session, and a broader rule for the same account defeats that.
  `DEPLOY.md` has root copy the bootstrap script and the switch binary
  into a new root-only folder, compare their hashes with those typed from the
  build host, and run only those copies (even for `--dry`); the README's
  baseline collection does the same for the release whose checker it runs
  before any switch has verified a release.
  `DEPLOY.md` adds the front end's port 80 to the collected listeners before
  the baseline (caddy is stopped while it is taken, and the switch's check
  after the reload refuses a listener `ports.allow` does not name), carries
  `--link-caddy` in both bootstrap commands, and says that a failed check
  after the reload happens after the activation: inspect `status` and the
  installed release, run the check by hand, acknowledge, deploy a higher
  counter.
  `deploy/authority-host/DEPLOY.md` gives the first-deploy sequence, the
  rollback and what to do when a step fails.
- The authority host's no-AI check follows the `import` lines of a release's
  Caddyfile that name an absolute path (a glob in the last component too) and
  reads each file on the host, as it reads an owner-local `EnvironmentFile=`:
  the shipped Caddyfile's `/etc/authority-host/seat/site.caddy` is no longer
  outside the check. An imported file gets the placeholder scan of the
  release's own files (a name like a provider key is a `HIT env`) and a new
  one, `HIT proxy <file> outbound-target`, for a `reverse_proxy` upstream that
  is not a Unix socket, `localhost`, a loopback address or a bare port. A file
  that is not there is `HIT incomplete caddy-import <path>`; the import is a
  pass only when the file was read.
- Add `ah-noaicheck`, the authority host's no-AI check: a small static
  program that reads a host tree (`--root`, default `/`) and prints one
  `HIT <kind> <path-or-name>` line per sign of an AI agent tool, then
  `RUN complete` or `RUN incomplete` and `ok` or `FAILED n hit(s)`
  (`--json` for one JSON object): an agent binary or an installer such as
  `npm` or `mise` in the folders of an explicit PATH inventory (the
  system bin folders, their immediate subfolders and every folder the
  system and home startup files, and the files they source, put on PATH),
  anywhere in `/usr/local` and
  `/opt`, in any `node_modules/.bin`, in a home's bin and install folders
  or as an executable file anywhere in a home, an agent credential folder
  in a home, a provider-key variable in an environment file, profile or
  unit (each read in its own syntax, by name only: a function's plain
  local declarations passed over but its global or exported ones
  (`declare -gx`, `typeset -x`) kept, home-relative sources resolved per
  home, a configured `XDG_CONFIG_HOME` followed, a source in a function
  body that it cannot resolve only a `NOTE deferred-source` line, and
  the units read through each systemd manager's effective unit view, with
  the attached user folders, lookup precedence, masks by link or empty
  file, dependency entries resolved by name, linked dependency folders,
  `EnvironmentFile=` resets and each manager's own specifiers), an agent
  process, an enabled unit not in
  `units.allow`, a listening TCP port not in `ports.allow`, a package
  forbidden or not in `packages.allow` and, when the release pins one, a
  firewall rule set whose digest differs. `--phase pre`, before a switch,
  also reads the candidate release's own units and the variables its
  front-end configuration refers to, by format (in a Caddyfile Caddy's
  `{env.NAME}` and `{$NAME}`, in Caddy's JSON, read as JSON, only
  `{env.NAME}`; shell-style `$NAME` and `${NAME}` are literal text, never
  matched, and so is every comment and literal, a `$` in a JSON value
  included; a reference it cannot read (a `{$NAME:default}` without its
  closing brace included, which names nothing), or JSON that does not
  parse, as
  `HIT incomplete release-syntax <file>`, never its text), merges each
  candidate unit with its drop-ins (an empty `EnvironmentFile=` dropping
  the ones before it) before opening the files they name, one unit at a
  time, charging what it keeps to the run's memory budget and stopping,
  incomplete, when that is spent, and leaves out
  the ports, the digest and the installed packages' `packages.allow`
  check (the old host's packages; forbidden packages are still checked);
  `--phase post`, after it, runs everything; `full`, the default, checks
  the host as it is. `--require-policy`, which the switch passes, makes an
  empty `units.allow`, `packages.allow` or `ports.allow` and a release
  without `share/nft.digest` a hit (`HIT policy`); without it the
  unenforced policies are `NOTE` lines and the run is incomplete.
  Whatever the check cannot cover (a walk at its depth limit, an entry of
  unknown type, its budget of entries, bytes, time, memory and nesting
  spent (charged before the work, a profile's nested expansions and loop
  fan-out included, and each path a source or PATH element stands for in
  each home and configured `XDG_CONFIG_HOME` folder, streamed one at a
  time and no longer built once a budget is spent), an
  unusable `/proc`, a process whose `/proc` folder stays without a name,
  a socket table without its own protocol's header, a missing or empty
  pacman database or a package entry without a name, a pinned firewall
  digest without a listing, a file it cannot parse, a source or PATH
  folder it cannot resolve, a missing required `EnvironmentFile=`, a
  list, listing or `nft-digest` FILE that is not a regular file, never
  read or waited on: `HIT incomplete input <name>`)
  is a `HIT incomplete` line, and every hit that is not a finding makes
  the run incomplete, never a pass. `ah-noaicheck ports` checks only the
  listening ports. It exits 0 clean, 1 on a hit and 2 on an error, never
  prints a file's content or a value, escapes every path in its errors,
  and reads its lists from the release's `share/`
  (`deploy/authority-host/share`), whose forbidden-package list names, by
  exact name, `nodejs` and each of its LTS series, the package managers
  that install agent tools, and `ollama` with its accelerator variants. A
  renamed binary is not caught by
  name: the package allowlist and the egress filter cover that. Two
  stated limits are accepted: bash-completion's array-driven startup
  loop stays `HIT incomplete env-source` (the library is outside the
  host's package baseline), and an enabled unit whose dependency link
  leads to a mask is a `NOTE unit masked` line even when a higher folder
  holds a unit of that name.
- The authority host's release source has the notifier's part in
  `deploy/authority-host/notify/`, laid over the base tree and the seat's part:
  `ataniq-seat-notify.service` (user and group `seat-notify`, a group of its own that the seat's unit joins as a supplementary group, socket
  `/run/seat-notify/notify.sock` with mode 0660 in `RuntimeDirectory=`, key and
  subscriptions in `/var/lib/seat-notify`, `IPAddressDeny=any` and no allow in
  the file: the owner's drop-in allows the resolver and the push service's
  range, the seat's hardening, never enabled) and the validators and reload
  lists with its lines added (`test -L` of its link, a restart before the
  seat's). The seat's unit takes an optional word, `$SEAT_NOTIFY_ARGS`, from
  its environment file (empty by default), so `--notify-socket` is the
  owner's to set. `ah-release check` excuses this one unit, by name, from the
  stop list (it decides nothing), only while its `ExecStart` runs the release's
  `ataniq-seat-notify` and no other binary, and does not count it as the
  decision service a release that is not static must have. The README ("The notifier") and
  DEPLOY.md ("Deploying the notifier") say what it installs, the owner's
  one-time steps in order, how to test it with `send-test`, what breaks when
  it is down (nothing but notices) and how to roll back; the README's seat
  section now says the seat dials the notifier's local socket, and both
  documents say that the front proxy's read timeout must exceed 30 seconds
  and its buffering be off for `/api/watch`, which the phone's pages hold up
  to 25 seconds.
- `ah-release build` replaces `{release}` in every file under the source's
  `etc/systemd` (units and drop-ins) with the release's own folder on the
  host, `/opt/authority-host/releases/<id>`, so a decision service's unit can
  run the binary of its own release (`ExecStart={release}/bin/NAME`). A unit
  that ran `/opt/authority-host/current/bin/NAME` failed the switch's
  `systemd-analyze verify` on the first switch to a release shipping `NAME`,
  since `current` was still the release before it. Such a file over 4 MiB is
  refused (`too-large`), and `ah-release check` reports a unit that still
  holds `{release}` (`unit-placeholder`).
- Add `ah-release`, the release builder of the authority host.
  `ah-release build` makes the release folder `ah-switch` imports,
  `DIR/<counter>-<short commit>`, from the given binaries (`ah-noaicheck` and
  `ah-switch` are required) and the `etc/` and `share/` of
  `deploy/authority-host`: folders 0755, files 0644 (0755 for `bin/*`), no
  link, a `MANIFEST` and a `SHA256SUMS` in the format the switch reads, and no
  time in any content, so two builds of one input are byte-identical. It
  refuses an existing release, a counter below 1, a commit or value with
  characters the switch would not read, and a link, fifo, device, socket or
  odd name in the source. A `--components static` build writes a no-op
  `bin/ah-pkgstep` of its own (a POSIX `sh`
  script that checks its arguments and exits 0 on a first switch or an
  unchanged snapshot, and refuses with exit 1 a snapshot change it cannot
  make, which the switch refuses before the step ever runs), so a first
  switch of a static release needs no hand-made package step; the build
  refuses a `--bin ah-pkgstep` with a static release (`static-package-step`,
  exit 2), because a release that must move the package snapshot is not
  static. `ah-release check DIR/<id>` reads a built folder
  back: its sums, manifest and shape, the coverage of its command lists that
  the switch leaves to the builder (a validator for every shipped Caddy,
  nftables and systemd file, a restart or reload of each shipped family, a
  stop and a restart of every decision service, no decision service in a
  static release), and the release's own no-AI check, which must exit 0 and
  print no hit and is run only when nothing else was found (a checker whose
  hash does not match `SHA256SUMS` is never run; what runs is a private
  copy of the bytes it verified, started as the open file that was hashed
  and not as a path, so a renamed temporary folder cannot swap it; a checker
  changed since is not run, `noaicheck-changed`; a relative `DIR/<id>`, a
  relative `--root` and a relative `TMPDIR` are made absolute, since the
  checker runs from the folder `/`). Both print only fixed reasons and plain names, never a
  file's content. `ah-release baseline --src DIR --units FILE --ports FILE
  --packages FILE --nft FILE [--out DIR] [--stamp TEXT]
  [--require-default-deny]` makes a scratch copy
  of the source tree whose allowlists are filled from files the owner
  collected on the host (the unit names of the no-AI check's `HIT unit`
  lines, `ss -H -ltn` ports, `pacman -Qq`, `nft list ruleset`): it refuses
  anything but a regular file of at most 2 MiB, an invalid line, an empty
  result and an output folder that exists or lies in the source, drops
  duplicates, sorts, takes `nft.digest` with the function of `ah-noaicheck
  nft-digest`, and never writes into the source. It enrolls any firewall
  listing, but for each of the hooks input, output and forward without a
  base chain of type `filter` with `policy drop` for IPv4 and IPv6 (a `nat`
  or `route` chain does not count, nor one whose first rule accepts every
  packet, with a plain or a named counter; no other rule is read; a listing
  that ends inside a block counts none) it prints a `WARNING:
  no-default-deny HOOK` line and writes the same words into the header of
  `nft.digest`; `--require-default-deny` refuses such a listing instead. See the README, "Filling the
  allowlists". `deploy/authority-host` now holds the source tree of the
  first, static release: a hardened Caddyfile (admin endpoint off, server
  timeouts, security headers, `/healthz`, a static site) and its validator,
  reload, health and stop lists. See `deploy/authority-host/README.md`, "The
  release builder".
- The seat release's `share/validators.list` has `test -L
  /etc/systemd/system/ataniq-seat.service`: the switch refuses the release
  before anything changes when the owner's link of the seat's unit (setup
  step 5) is missing, instead of failing the reload's restart and the stop
  list's stop (exit 5) after `current` moved. DEPLOY.md has the
  troubleshooting row.
- The authority host's release source has the seat app's part in
  `deploy/authority-host/seat/`, laid over the base tree to make a release
  that is not static: `ataniq-seat.service` (user `seat`, socket
  `/run/seat/http.sock` in `RuntimeDirectory=seat`, its key in
  `/var/lib/seat`, no network but the gate's address from the owner's
  drop-in, no listener, the design's hardening, never enabled: the reload
  list starts it), a Caddyfile with the snippet `(seat)`
  (`reverse_proxy unix//run/seat/http.sock`, the Host passed as it came,
  `Origin` and `Sec-Fetch-Site` untouched, `X-Seat-Client-Addr` set on every
  request, nothing cached) and the import of the owner's site file
  `/etc/authority-host/seat/site.caddy`, and lists that validate the unit,
  restart the seat, check it on its socket and stop it
  (`systemctl stop ataniq-seat.service`). The README ("The seat app") and
  DEPLOY.md ("Deploying the seat app") say how to make the release, the
  owner's one-time setup of the host, how the switch starts and stops the
  seat, and how to roll back.
- Add `ah-switch`, the release switch of the authority host.
  `ah-switch --from INCOMING_DIR ID` imports a release folder into
  `/opt/authority-host/releases/ID` as new, read-only, root-owned files
  (folders and regular files only, within limits of entries, size and
  time; staged in a new folder of its own, the only one it ever removes,
  and refused before anything is created or removed when the incoming
  folder is, contains or lies beneath `releases/` or anything in it; the
  incoming folder is opened once, without blocking or following a link,
  so a FIFO swapped in never holds the lock),
  recording the digest of the tree it made (paths, inodes, sizes,
  hashes), and `ah-switch ID` activates an imported release, refusing a
  folder without that record or that no longer matches it: it checks its
  `SHA256SUMS` over every file and, when the root-owned switch policy
  `/etc/authority-host/switch-policy.json` asks for it, its SSH signature by
  the policy's principal; refuses a counter not above the highest ever
  attempted or any counter the host shows (its links, its last recorded
  switch, its release folders, the append-only
  `/var/lib/authority-host/counters.log` of every counter attempted), and
  a `highest-counter` below the counters used, before recording anything;
  runs its no-AI check before activation (`--phase pre`: the release's
  units and the host, without ports and firewall) before and after the
  release's package step (a static release whose snapshot is not the
  host's is refused before anything begins, and so is one while the
  package state is unknown or on a host with a `current` and no recorded
  snapshot, which its no-op step could only certify; a step that fails or
  is interrupted leaves the package state unknown, refusing further
  switches until the owner states the snapshot or a release with its own
  step runs it again to the end; the step holds the
  owner-only switch lock and dies with it, and one that outlives its
  switch is named by its process id), then its validators;
  renames a new `current` link over the old one, reloads and checks every
  health command within one 60-second deadline, runs the no-AI check
  again (`--phase post`, with the live listeners and a listing of the
  loaded firewall) before recording success, going back to the old
  release (after stopping the failed release's services) with that
  release's own reload and health lists and its no-AI check, or stopping
  the decision services, with the host's own
  `/etc/authority-host/stop.list` first (its programs are bare names or
  protected host programs like a release's; any other absolute path
  refuses the list, loudly, without blocking a journal's recovery), when it cannot (that list runs
  before the stopping phase is saved, and, while a journal is stopping,
  first in every command, under the lock, even when `releases/`, the
  counter or the record cannot be used). Every transaction
  first writes `/var/lib/authority-host/decisions.inhibit`, the one file
  a decision-capable service looks up: it may start while the file is
  there (and answer its readiness probe) but refuses every decision unless
  one lookup before the decision finds no such file (the package
  `internal/authorityhost/decisionguard` is that lookup as code, `Check`,
  which refuses on the file in any form, on a missing or linked state
  folder or one it may not open, and on one removed or replaced between
  its lookups, and looks the file up in the folder it pinned, so that a
  folder moved away and put back meanwhile still answers with its guard; the journal `txn.json` is never there without it: a healthy end renames the journal
  over the inhibit, then removes the inhibit, the one step that lets
  decisions through; a power loss between the two leaves the journal in
  the inhibit, which the next command writes back and settles and which
  `inhibit-clear` and `ack` refuse); the switch removes it only
  when the transaction ends healthy, after running the reload list,
  health check and post-activation no-AI check (`--phase post`, the
  firewall and listeners as they are now) of `current` again when it
  finds a healthy transaction not yet settled (a reboot may have come;
  when any of it fails, the release not verifying again included, the
  host's stop list runs, the failure is journaled so that an interrupted
  settling never qualifies it away, a failed no-AI check is
  recorded as the check's verdict, the command and every retry of the
  same request exit 1 and the outcome stays owed with the failure until
  acknowledged); a
  failure keeps it across a reboot until a healthy switch or
  `ah-switch inhibit-clear --asked WORDS` (recorded with the time). All
  of it is one transaction journaled in `txn.json`, phase by phase
  (packages, links, activating, restoring, stopping, done), with the trust
  it began with;
  the next command resumes an interrupted one from its phase, a link
  counts as moved only once its folder is synced, a failed stop keeps the
  transaction open (so does a journal that cannot be saved as restoring,
  after the stops of the failed release and of the host ran), and every settled outcome is owed in its own file,
  `/var/lib/authority-host/owed.json` (written before the journal is
  removed, never in `deploy.json`, and not taken back by a printed result
  line): the same request run again reports it instead of doing it again
  and leaves it owed (a report that cannot be written exits 1),
  a rollback bound to its request id so that a retry never resolves
  `previous` anew, any other switch or rollback reports it and is refused,
  and only the owner's `ah-switch ack --txn ID[,ID...] --asked WORDS` takes
  it back (naming the transactions of every outcome owed, as `status` and
  the switch print them, `status` and `boot` only once no transaction is still open,
  since settling one may change its outcome, so that an ack repeated
  after a newer outcome became owed is never taken for that one: a
  mismatch is refused and changes nothing, a repeat keeps the first
  receipt; recorded with the time; when the outcomes it prints cannot be written or flushed it
  records nothing, keeps them owed and exits 1, as a retry does; an ack
  interrupted after its record is finished by the next ack, which keeps
  the recorded words and time); `--dry` only shows it.
  `ah-switch --rollback-to previous` goes back under the same policy,
  `ah-switch recover --counter N` restores a missing counter,
  `ah-switch recover --package-state SNAPSHOT` settles an unknown package
  state, a journal that does not match the host (its links, or a counter
  below those attempted since) is never resumed until
  `ah-switch recover --discard-journal TXN` sets it aside (only a journal
  proven stale: one whose evidence cannot be read is neither resumed nor
  discarded; decisions stay inhibited under a plain record of the
  discard, and every command that finds a journal writes a missing
  inhibit back first, before it runs a stop or refuses anything: the
  transaction's, or beside a journal that cannot be read a plain record
  no boot takes over, which also replaces boot's own record found there;
  an inhibit that is there, even one that cannot be read, is kept as
  evidence by every command that refuses),
  `ah-switch recover --services-stopped` settles a transaction whose stop
  commands failed on the owner's word,
  `ah-switch status` shows the state under a shared lock, `--dry` prints
  every step and changes nothing, `--root DIR` works on another folder
  (running programs there only with `--allow-exec`), and
  `--noaicheck PATH` runs another no-AI check only when it is protected
  root-owned code and the switch is not run through sudo (any protected
  host program, `/usr/bin/true` included, would pass, so a sudo rule for
  the switch never chooses it), bound to the transaction by its SHA-256
  and checked again before every run, from a private copy made from the checked
  descriptor and hashed again. A program of a release's lists is a file
  of the release (`{release}/bin/...`, in any list), a protected
  root-owned host program such as `/usr/bin/systemctl`, or a bare name in
  the fixed search path; any other absolute path is refused when the
  release is verified. The outcome is written to
  `/var/lib/authority-host/deploy.json` as stable failure codes, never a
  program's output. `ah-switch boot`, run as root at every boot by the
  shipped unit `deploy/authority-host/bootstrap/ah-switch-boot.service`
  (a oneshot that fails closed, which only the owner installs and
  enables, by hand), keeps decisions refused after a reboot until the
  host is qualified since that boot: under the lock, before it runs anything, it writes the
  decision inhibit as its own record when none is there and no journal is
  pending (or takes over the record of a boot that did not finish; beside
  a pending journal it never writes its own record, only the
  transaction's or a plain one no boot takes over), qualifies `current` again as a
  settling transaction does (verified with the switch policy's trust, its
  reload list, health check and post-activation no-AI check) and removes
  only that record, only when all of it passed; a failure is first
  written into that record (which no later boot takes over, also once its
  outcome is acknowledged; when that write fails, the `ack` of the outcome
  writes it first, and refuses, the outcome still owed, when it cannot), then runs the host's stop list, is owed
  (`direction: boot`, `requalify_failed`, shown by `status` with its
  `ack`) and keeps decisions inhibited; any other
  inhibit it finds (a transaction's, a held journal, a failure, one left
  for `inhibit-clear`, one it cannot read strictly) stays as found while
  it settles an open journal as any command does, and it never
  acknowledges anything, and its own qualification writes nothing to
  `deploy.json` (settling an open transaction records that transaction
  there, as any command does); a crash or a kill at its time limit during
  the stop list leaves that list unfinished, not resumed, for the owner
  to finish by hand while decisions stay refused; exits 2, 3 and 4, and a
  kill before its first write, leave no inhibit of its own (it ran
  nothing then); a host with nothing
  deployed (no `current`, release folder, `deploy.json`, journal or
  inhibit it cannot read, which may hold one) is refused with exit 4 and
  nothing changed, an outcome owed beside such an inhibit is printed
  without its `ack` command, while a deployed host whose
  `current` is missing, or cannot be read, fails closed with exit 1
  (saying that nothing was qualified). Exit codes are 0
  ok, 1 failed, 2 usage, 3 locked and 4 nothing deployed (`boot`).
  Known limits (an old journal restored by root can replay a settled
  transaction; a package step's descendant that keeps the lock open
  keeps every command out until it is stopped) are in
  `deploy/authority-host/README.md`.
- `ataniq-console --daemon DIR` now opens the pair-a-phone view with `P`. It
  pairs over the same owner connection as the approvals view, so the daemon
  still sees one console as the decider, and it reads the approval app's
  address from the first line of `ataniq/owner-local/seat-origin` in
  `$XDG_CONFIG_HOME` (or `~/.config`) once, when the console starts. The file
  must be a regular file, not a link, at most 1 KiB and not writable by its
  group or by others; otherwise the view says "pairing origin not configured"
  and asks the daemon for nothing, and the address is never in a message or a
  log. A pending pairing whose fingerprint is not exactly eight words is
  refused as a bad answer, and a call whose context is already over is not
  made. See `docs/terminal-console.md`.
- The console's pair-a-phone view shows, under the eight words and before the
  owner confirms, what the phone's authenticator claimed when its backend gives
  it: "Claimed, not proven: vault passkey, synced." The line uses the view's own
  words for the known kinds and backup states, and a change of the claim drops
  the confirm question. A confirmed key of a known kind reads "Paired. Kind,
  claimed: vault passkey." A key paired without a claim reads "Paired. Kind:
  unverified (no claim made)."
- The console has a pair-a-phone view, opened with `P`, for adding a phone as an
  approving device from the desk. It appears only when the console is given a
  pairing backend and the approval app's address (`Options.WithPairing`):
  without one, `P` shows
  "pairing is not configured" and does nothing, and the help page does not list
  it. Enter asks the daemon for a one-time code and the view draws a QR code
  that holds the app's address and the code in its fragment, black on white
  with a white margin in either theme, with the seconds left; a terminal too
  small for it (even with the smallest margin that scanners read) gets
  "terminal too small: need W x H" and never a clipped code.
  The code appears only inside the QR, never as text. The view reads the
  daemon's state every two seconds; when the phone's key arrives it shows the
  eight words (64 bits of the key's fingerprint), which the phone shows too, and
  `c` then `y` confirms (a `y` typed with the `c` is not taken; it is refused
  while any of the eight words is off screen or cut, and the view says the
  terminal is too small) or `x` rejects; both name the pending key's id, and
  the question is dropped when the words,
  the id or the terminal size change; Esc closes the view at any time
  and confirms nothing, and a code left waiting ends by itself when it
  expires. Nothing is trusted until the owner confirms at the desk. The
  address must be `https` with a host name, no port but 443, no path and no
  userinfo; with any other value the view says "pairing origin not configured"
  and draws no QR. What the daemon sends (words, id, kind) is cleaned before it
  is drawn, and its error text is never drawn. The fingerprint is eight words
  (64 bits) from its first release, not six: nothing is paired in production
  yet, so the owner's key list accepts exactly eight words and there is no
  migration. See `docs/terminal-console.md`.
- `make ownerchannel-build` builds the daemon with the owner channel's build tag
  into the ignored folder `build/owner/`, for the owner's own trial of the
  unqualified channel. It runs doctor first and starts nothing. No release, no
  CI job and no other target uses it, and the guard tests refuse any other
  recipe that names the tag.
- Pairing a phone records the kind of its passkey as a claim. The seat relays
  the registration's authenticator data and relying party id with `seat.pair`;
  the daemon registers the credential from them, refusing one whose data does
  not name the same credential and key or lacks user verification, and, on a
  seat gate with a seat origin, one for another relying party than the
  origin's host (`credential-invalid`, the code left unused), and keeps
  the kind its AAGUID claims (`platform-passkey`, `vault-passkey` or
  `passkey`), the AAGUID, the backup flags and the sign counter. The pending
  `owner.pair-status` and the key list show the kind, the AAGUID and the
  backup flags as optional members, and not the sign counter, which is kept
  internally: `keys.log` keeps all of them, with a counter record that only
  raises the highest sign counter. A key paired without them stays of the kind
  `unverified`, which can neither log in nor decide.
- The phone trial, a STAND-IN rig for scratch state in the tagged build
  (`make ownerchannel-build`): `ataniqd trial init --state-dir DIR` writes the
  trial's settings, `ataniqd run --owner-channel --trial` serves sample cards,
  and `ataniqd trial submit --kind finish|branch-prune [--count N]` makes them on
  demand, so the owner can try the phone end to end against a real daemon. A
  decision goes through the real path (challenge, passkey assertion,
  verification, record, journal evidence) and applies nothing: an approved
  card ends not-applied, "applied: nothing (trial)". See the "Phone trial"
  section of `docs/seat-gate.md`.
- The scratch settings (`owner/scratch.json`) take an optional `"kinds"`: the
  kinds of credential the project accepts for a phone's decision
  (`platform-passkey`, `vault-passkey`, `passkey`), every kind when absent or
  empty. The policy applies when a decision's challenge is asked and when the
  decision is made; a login belongs to no project, so it checks only that the
  daemon knows the key's kind. A name that is not a kind, or a kind named
  twice, refuses the settings. Kinds are claims of the phone, so the policy is
  advisory.
- Add the lead control plane for scratch leads, in a build with the
  `ataniq_canary` tag only: `ataniqd run --scratch CONFIG` serves six
  operations (`lead.list`, `lead.define`, `lead.start`, `lead.stop`,
  `lead.ticket` and `lead.inject`) through the client protocol's extension
  seam, for up to eight leads of throwaway `scratch-` projects named in an
  owner-only config, with a `max_running` cap. Each command is bound to a client
  string and a command id in a durable table (`lead-commands.jsonl`) and answered
  with a receipt (`in-progress`, `done`, `refused`, `unavailable` or
  `uncertain`); a replay returns the stored receipt and never runs the command
  again, and a daemon restart ends an unfinished command as `uncertain` (a
  ticket as `unavailable`). The
  place gate for a scratch root is the marker `scratch-root.json`, written only
  by the new `ataniq-canary scratch-init`; `ataniq-canary scratch-clean` removes
  what a define left when it was cut short, by the owner's hand. The daemon
  computes each lead's sandbox profile digest with the holder's own code unless
  the owner pins one, which weakens the canary's independent pin; the client
  string is a namespace, not an identity. `ataniq-console --daemon` offers a
  leads view on `L` when the daemon lists the six operations (define, start,
  stop, message and attach, with the receipt of each command, then back to the
  list) and says "no lead control in this daemon" otherwise. A release daemon
  contains none of it and registers no operation; the release console carries
  the inert leads view. See `docs/scratch-leads.md`.
- A scratch lead is shown `ready` once its holder reports up, in a build with
  the `ataniq_canary` tag under `ataniqd run --scratch`: after `lead.start`,
  and at daemon start for a lead shown `starting` that the daemon adopted, a
  background observer asks the holder's control socket for its status (for up
  to 60 s, retrying with a growing wait) and, when the holder answers for the
  same epoch and role, sets the shown state `ready` with the reason
  `holder-ready` and `ataniq-observation` evidence. `ready` means the holder
  is up and the agent was started; the agent's login is not observed, and it
  is not a claim that the agent works. `lead.start` answers `starting` at
  once. A proven stop or a newer epoch wins over the observer, a stop that is
  refused or fails leaves it, a slow or silent holder leaves the lead
  `starting`, and the daemon joins the observer at shutdown. The canary has no
  observer. See `docs/scratch-leads.md`.
- A scratch lead's `lead.list` rows carry two members, always present:
  `state_reason`, the reason code the registry shows with the shown state (a
  promoted lead reads `ready` with `holder-ready`), and `message_result`, the
  holder's three words (`outcome`, `reason`, `written`) about the current
  epoch's last message, empty when no message was sent or the answer is not
  known (the daemon keeps it while it runs and reads it from the command table
  after a restart while the message's receipt is the lead's last). The summary
  receipt of a row leaves out its `message` as well as its `result`, which
  keeps the largest list under 8 KiB. A row without the members is read by the
  console as one with both empty. See `docs/scratch-leads.md`.
- The epoch of a scratch lead takes one message, and a message that was not
  delivered does not spend it. When the holder answers `not-now` or `refused`
  with nothing written, or refuses the request itself (`disabled`,
  `wrong-epoch` or `bad-request`), the epoch's message stays free,
  `lead.list` shows `message_spent` as `false`, and the lead may send another
  message in the same epoch (the same message id is not refused). Everything
  that may have reached the lead stays spent: a submitted or uncertain result,
  no answer, a closed connection, an answer that does not decode, a refusal
  code the daemon does not know, a result or refusal that could not be written
  to the evidence, and a holder's `busy` (a holder that says a delivery is
  running is delivering something the daemon has no record of; the console
  shows the message as `sent-unsure`). A restarted daemon reads the evidence
  back and keeps the epoch free only when the message has its `inject-result`
  line with `not-now` or `refused` and nothing written, or its `inject-refused`
  line with `holder-disabled`, `holder-wrong-epoch` or `holder-bad-request`; a
  daemon killed after the `inject-sent`
  line and before the closing line leaves the epoch spent, as does any closing
  line without a matching `inject-sent` line or one that comes twice. The
  canary's rule that every sent message spends the epoch is unchanged. See
  `docs/scratch-leads.md`.
- The console's leads view says plainly what became of a message. A done
  message is drawn from the holder's three words (outcome, reason, written):
  "NOT delivered: nothing was written" with the reason (the lead's screen is
  not idle or is blocked, the program version is untested, the text was
  refused), "the message was submitted", or "result unknown" with what is
  known of what was written, and the line says whether the epoch's message is
  free (a message that was not delivered, as far as the list's row says) or
  spent; the line of a message that was not delivered is not drawn "free" while
  the daemon holds the epoch spent, and says so until the list is read. The MSG
  column agrees:
  `free`, `free (not delivered)`, `sent` (only when the words say the message
  was submitted), `sent-unsure` (something may have reached the lead, the
  words are not known, or the daemon holds the epoch spent with none) or
  `spent, not delivered` (the daemon holds the epoch spent after a message that
  was not delivered because it could not write the evidence line), and `m`
  sends another message while the epoch's message is free. A lead whose state
  is `ready` with the reason `holder-ready` gets a line that says the holder is
  up and the agent started and that its login is not observed. See
  `docs/terminal-console.md`.
- The console's leads view keeps what became of a message on its status line
  when the list is read again. The daemon's list holds each lead's last command
  as a summary without its result, and the view keeps the answer's three words
  (outcome, reason, written) of the message just sent, and a ticket's folder and
  ticket file, while the list shows the same command; a list receipt that has
  words of its own is shown as it is. The list's summary has no epoch, so the
  line also keeps the epoch the answer was for: when the lead has moved to
  another epoch since, the line of a message that was not delivered says that
  the lead is at another epoch now and that epoch's message is not known, and
  does not call the new epoch's message free or spent. See
  `docs/terminal-console.md`.
- The console's leads view reads the list again every two seconds while a lead
  is `starting` or `stopping`, as it already did while a receipt is in
  progress, so a started lead turns `ready` without `r` and the reading stops
  when none is. A lead that is `unknown` is not read by a timer. See
  `docs/terminal-console.md`.
- The console's leads view draws `-` in the MSG column of a lead that is not
  running (state stopped, ended or desired, or unknown with the reason
  `launch-unsettled` or `launch-reply-lost`, or no epoch), where it drew the
  words of the lead's last epoch's message; `off` stays for a lead whose
  messages are off. A lead that is unknown for another reason (for example
  `control-group-not-proven-empty` or `competing-unit`) may have a unit that
  runs with a message sent, so its column shows `free`, `sent`, `sent-unsure`
  or `spent, not delivered` as for any other lead. See
  `docs/terminal-console.md`.
- The console's leads view says what settles a lead whose launch is not
  settled. A lead that is `unknown` with the reason `launch-unsettled` or
  `launch-reply-lost` gets the line "launch settles by itself 40 s after the
  start; if not, stop the lead or restart", the same words answer a stop or a start
  refused with `intent-pending` (which does not tell the owner to read the
  list, because reading it does not settle the launch), and a start refused for
  the limit of running leads while such a lead is listed says that the
  unknown lead holds a slot until its launch settles by itself, 40 s after the
  start, and that stopping the lead or a restart of the daemon is what is left
  if it is still unknown after that (a lead that is unknown for another reason,
  which the daemon has no timer for, is said to hold a slot until it is
  settled, with no promise of a self-settle) (a launch's own unit that was adopted
  leaves the lead `unknown`, and a stop ends it). `docs/scratch-leads.md` has the recovery after a killed
  daemon. See `docs/terminal-console.md`.
- The console's leads view words a refused attach ticket "the holder refused
  the ticket; a writer may already be attached", because the receipt carries no
  reason beyond `ticket-refused` and the holder grants one writer at a time. See
  `docs/terminal-console.md`.
- The daemon in scratch mode settles a launch that is not settled itself when
  the late-start window ends, so a lead left `unknown` by a daemon killed in
  the middle of a start (reason `launch-unsettled`) or by a start whose reply
  was lost (`launch-reply-lost`) needs no daemon restart. A timer per such
  lead waits until the intent's deadline (the late-start window of 30 s and its
  margin of 10 s, on the registry's clock) and asks the lifecycle to settle
  that one role by the rules of the reconcile: with no unit loaded and its
  control group empty, the intent is closed as abandoned and the lead reads
  `stopped`, with a proof, and leaves the `max_running` count. A unit that is
  loaded at the deadline does not settle the lead and is never signalled (the
  launch's own unit is adopted, another is reported and the intent abandoned,
  and the lead stays `unknown`), a manager that does not answer, or a control
  group that is not proven empty, is tried again at growing waits (the lead
  stays `unknown`, with the reason `control-group-not-proven-empty` in the
  second case), and a lead shown `unknown` for `registry-recovered` has no
  timer. The timer starts nothing, never waits for the role's lock, ends on a
  stop, a new start and at shutdown, and ends with one log line that names the
  restart when the daemon's reconcile at start did not act on a cut of the
  registry's journal. See `docs/lead-daemon.md` and
  `docs/scratch-leads.md`.
- Add a screen recorder for scratch leads, in a build with the `ataniq_canary`
  tag only, for the owner who makes the fixtures of a new agent version by
  hand: a lead whose scratch config says `"record_screens": true` (optional,
  `false` when absent) is started with `ataniq-holder --record-dir`, and its
  holder writes, raw, into the folder `record` of the lead's runtime folder one
  fixture pair (`NNNN.txt`, `NNNN.json`) per distinct screen that stood still
  for about 300 ms, at most every 500 ms, the screen shown at the end, and one
  every 3 seconds of a screen that never stands still, such as the working
  screen (marked `"sampled": true`), each window title (`titles.jsonl`) and
  each OSC 7501 payload (`osc7501.jsonl`).
  The folder is 0700 and its files 0600, with a `README.txt` that says the
  files are raw (scrub before use; do not commit); a start whose folder exists
  is refused, with the code `record-folder-left`, which the leads view of
  `ataniq-console` shows as "an earlier record folder is there: move or delete
  it, or turn record_screens off"; the screens stop at 400 pairs and the recording at 8 MiB, each
  with a line in `LIMIT.txt`, and it never blocks the agent. A recording of
  the login screen holds a login URL and code: delete the folder once the
  fixtures are made. Without the field nothing changes.
- `ataniq-seat` approves and denies on the phone, from the Card page only
  (`/cards/{id}`), when the seat gate offers decisions (on a gate that does
  not, the Card page holds no form and allows no passkey): one sign-off box
  per flagged path and Approve, offered when the card model allows it and the
  page showed every part of the card whole (a part with a path or a ref of
  more than 1024 characters, a name in the header of more than 64, or a line
  of the plan or of the lead's note of more than 4000, cut on the page, is
  not read; Approve is then withheld and the page says which part, approve
  at the desk, and a page of the whole path list that cuts a path does not
  count) and enabled once every box is ticked; Deny,
  with an optional reason, available with no box ticked. A tap asks `POST
  /api/decide-challenge`; the seat builds the decision from the card it showed
  (the browser sends only the id of the page's form, the action, the indexes
  of the boxes and the reason), takes the tap only when that page showed the
  binding the session read last (a tab left open on a card replaced since is
  told the card changed, before any challenge or passkey), recomputes the
  daemon's challenge before the phone is asked, and shows the action, the
  card, its project, target and revision; a second tap asks one passkey
  assertion with user verification for the session's own credential, sent once
  by `POST /api/decide`. A cancel sends nothing. A lost answer shows "Decision
  sent. Outcome unknown." and only "Look again", and the card is not decided
  again until its Card page is read again by a GET whose read began after the
  answer was lost (a read made while the decision was on its way, or begun
  before, may show the card as it was, and then says to look again); nothing
  is queued. A page counts as read, and a Card page settles a lost answer,
  only once it was written and flushed to the connection (a proxy in front may
  still lose it). The pages of a card's whole path list count for the review they were read for, so pages read before a card
  was replaced do not count for the replacement. A held approve shows the
  host's line and the held words. Each refusal is fixed words; a passkey no
  longer paired ends the session. Decision challenges are limited to 10 a
  minute per credential, and an expired card gets none. A decision's words go
  to the seat gate exactly as the seat checked them against the gate's cap (a
  flagged path full of `&`, `<` or `>` is no longer escaped on the way, so it
  does not grow past the cap); a decision whose words are over the cap is not
  sent and the phone says "This is too large to send."; it is decided at the
  desk. So is one whose decision record the daemon could not journal (its
  sign-offs past the record's 8 KiB): the daemon refuses its challenge
  before the phone is asked, and nothing was decided. A request body over 8 KiB sent without a declared length is now
  refused 413, as one with it is.
  Scratch state and the STAND-IN door only; unqualified.
- `ataniq-seat` shows the cards on the phone behind a session, read-only as
  the first step (the decide control of the Card page is described in the
  entry on approving and denying on the phone): the card list
  (`/cards/`), a card (`/cards/{id}`) and the pages of a card's whole path
  list (`/cards/{id}/paths/{n}`), each a GET (a `HEAD` is refused 405 and asks
  the daemon nothing). A page without a session goes to the login page and
  asks the daemon nothing. The seat builds each card from the record's bytes
  with the card model after checking their digest, shows a lead's text only as
  escaped plain text, shows a prune's ref line with the branch its deleted ref
  was merged into and that branch's tip, as the desk does, and says on every
  page that the desk runs a STAND-IN door on scratch state. The list and the
  pages of a card's path list have no decide control. Reads are limited to 20
  a minute per paired credential, and the reads of every session together to
  20 calls of the seat gate in 80 seconds (the first page of a card's path
  list may call it twice); a read past them says the desk is busy and asks
  nothing.
- `ataniq-seat` follows the authority host's decision contract: before it
  relays a decision's challenge or a decision to the gate, every time, it
  asks `decisionguard.Check` of the release switch's state folder,
  `--decision-guard DIR` (an absolute folder, `/var/lib/authority-host` when
  not given). While `decisions.inhibit` is there, or the folder or the file
  cannot be looked up, nothing is relayed and the phone shows "Decisions are
  paused on this host. Nothing was sent." under the code
  `decisions_inhibited`, and may tap again once decisions are taken again; a
  refused decision's room at the gate is given back. It fails closed: on a
  host without that folder no decision is relayed. Pairing, login and the
  reading of cards decide nothing and go on. Each refusal is logged with the
  guard's fixed code.
- The seat gate of `ataniqd run --owner-channel --seat-gate ADDR` serves a
  login and a decision from the owner's phone (the capabilities `login` and
  `decide`: `seat.login-challenge`, `seat.login`, `seat.challenge`,
  `seat.decide`) when the seat origin is configured: the owner-local file
  `ataniq/owner-local/seat-origin` in the configuration folder, holding
  `https://` and a lower-case domain name, read once at start. Each login and
  each decision, a deny included, is a passkey assertion with user
  verification over a challenge the daemon draws from its own record of the
  card (and only for a decision whose record fits the journal's 8 KiB; one
  that would not is refused `record-too-large` before the phone is asked), kept in memory for 5 minutes and one use by the seat that asked (at
  most 32 decision and 16 login challenges; a restart drops them), verified
  against a key on `owner/keys.log` that is not revoked and of a kind the
  verifier knows, and the seat origin. A login belongs to no project, so the
  scratch settings' `"kinds"` apply when a decision's challenge is asked and
  when the decision is made, not at a login. The key is checked
  again right before the decision is journaled, so a revoke at the desk that
  returned first wins. A phone decision is journaled as confirmed by passkey
  with the assertion's evidence (a decision record confirmed by passkey that
  denies and carries sign-offs is refused `decision-invalid`), needs no
  activity at the desk, and goes through the day's limit as at the desk (an
  approve past it is held, a deny never). The highest sign counter is kept,
  and one that does not grow is reported as a possible copy, not refused; two
  verifications at once are compared in turn. When `owner/keys.log` has no
  room left for counter records (some four thousand raises, shared with
  rejected pairings), the daemon's log says so once per key and the counter
  is kept in memory until a restart; a phone's synced passkey, which reports
  0, needs none. Without an origin, or with a file
  that is refused or holds no canonical origin, the gate serves pairing and
  the reads only, as before. The start line names the origin and what the gate
  serves, or why it serves pairing and the reads only. Scratch state and the
  STAND-IN door only; unqualified. See `docs/lead-daemon.md`.
- `ataniq-seat gate-init --identity DIR` makes the seat's own key for the seat
  gate: `cert.pem` and `key.pem` in DIR, the key with mode 0600, DIR made with
  mode 0700 when it is missing. It prints one line, the key's fingerprint (64
  lower-case hex digits) for the gate's pin list, and nothing else. It never
  overwrites: an existing key file, or a folder that others can write, is
  refused and nothing is written.
- `ataniq-seat` can relay pairing to the daemon through the seat gate, with
  `--gate HOST:PORT`, `--gate-pin-file PATH` and `--identity DIR`, given all
  three or none. The pin file holds the gate key's fingerprint, exactly one,
  64 lower-case hex digits; it must be a regular file, not a link, of at most
  1 KiB, that group and other cannot write. `--identity` is the folder
  `ataniq-seat gate-init` wrote. The link uses pinned-key TLS at both ends and
  redials when it drops; a wrong pin never connects. While the link is down
  the phone is told `daemon_unavailable`; a code the daemon refused is
  `code_refused`, and any other refusal is `daemon_error`. The pairing code is
  never printed. Without the three flags pairing answers `daemon_unavailable`,
  as before. The seat hands the daemon at most 6 pairings in 11 minutes from
  all clients together, besides 5 tries per client in 15 minutes; a pairing
  past it is answered `rate_limited`. The gate does not count refused
  pairings against the seat, so strangers cannot shut out the owner's logins,
  reads and decisions with them; the bound keeps what strangers can make the
  seat send (the daemon's log lines, tries at an open code) small. Its cost:
  strangers who spend it keep the owner's own pairing on the phone waiting
  while they keep it up, never the desk.
- The daemon built with the owner channel's tag can serve the seat gate, the
  link of the phone's seat app, with `ataniqd run --owner-channel --seat-gate
  ADDR`. ADDR is one IP address of the host and a port; a host name, a wildcard,
  multicast or broadcast address and a zero port are refused. The gate is TLS
  1.3 with pinned keys: `ataniqd seat-gate init` makes the gate's key in
  `owner/gate/` of the state folder and prints its fingerprint, and the seat
  keys it lets in are pinned in `owner/gate/seat-pins` (mode 0600, one
  fingerprint per line, at most 4), which `ataniqd seat-gate pins` lists and the
  daemon reads at start. It serves the pairing call and the reads of the cards
  in frames of up to 257 KiB, so the largest card fits. With the seat origin
  configured it also serves a login and a decision from the phone, and without
  one it serves only the pairing call and the reads and decides nothing (see
  the entry on the login and the decision). A seat whose key is not pinned
  never reaches the service. A seat refused 16 times within 10 minutes for what
  it sent is answered `too-many-refusals` until those refusals age out; a
  refused pairing, which the seat relays for anyone who reaches its pairing
  page, is not counted, so a stranger cannot shut the owner's seat out with
  wrong codes. At start one line
  gives the address and the gate's key fingerprint, never a pin. Without the
  flag nothing listens on the network. Provisional and unqualified, for scratch
  state only. See `docs/lead-daemon.md`.
- The phone's pages of `ataniq-seat` update by themselves. The card list
  reloads when the set of waiting cards changes (at most once every 20 seconds,
  never over typed text), and a Card page, which never reloads by itself, says
  "This card changed or was decided elsewhere. Look again." and disables
  Approve, Deny and their controls when its card was decided elsewhere or
  changed. A status line says "Not live" when the page cannot be kept current,
  "Not live: the desk is out of reach" while the link to the desk fails, and
  "Session ended. Log in again." when the session is gone. The seat keeps one
  long call, `seat.wait` (capability `watch`), at the gate for the whole seat,
  and each page asks the seat, never the gate: more tabs add no call of the
  gate. A poll does not extend a session's idle time, and a stale Approve is
  still refused by the seat and the daemon; the page only withdraws the offer
  sooner. The seat's reads now share 20 calls of the gate in 80 seconds (the
  long call has 6), and a front proxy for the seat must allow a held request of
  up to 25 seconds on `/api/watch` (read timeout above 30 seconds, buffering
  off). A new card that comes in through the daemon's door shows up within
  about 30 seconds. See "Live refresh" in docs/seat-gate.md.
- `ataniq-seat` has a login page, `/login/`, for a phone with a passkey paired
  at the desk. The seat asks the daemon through the seat gate for a login
  challenge and its allow-list, recomputes the challenge from the gate's pin
  before the phone is asked, and keeps it with the browser that asked; the
  phone signs it with user verification, and the daemon verifies the
  assertion. A login starts a session: a `__Host-seat` cookie (Secure,
  HttpOnly, SameSite=Strict) holding a fresh 256-bit value kept server-side,
  ended after 30 minutes idle, 12 hours at most, or by `POST /api/logout`; the
  Log out button goes to the login page only when the seat answered the
  logout with its own acknowledgement (no redirect is followed), and
  otherwise says "Logout not confirmed" and stays. Login
  challenges are limited to 10 per client in 15 minutes and 8 a minute in all.
  The seat counts every call it sends the gate, by kind, over 80 seconds, and
  keeps each kind to its room there: pings 6, pairings and logins 12 (6
  logins), reads 26, decisions 16 (8 decisions), which make the gate's 60 a
  minute; a call without room is not sent and the phone says the desk is busy.
  A decision's challenge is asked only with room for the decision too, kept
  for that one challenge, so a decision the phone signed is never refused for
  the gate's rate, from any session of the passkey. A login challenge is
  likewise asked only with room for its login, so the owner's login is never
  refused as busy after the passkey; strangers who draw login challenges
  can still use up the room for challenges, the owner's too, for up to 5
  minutes at a time, and with it the room for pairings.
  A login belongs to no project and to no card: it proves that the phone holds
  a paired key of a kind the daemon knows, and decides nothing. The login
  page, and the Card page when it offers a decision, alone allow
  `publickey-credentials-get`, to the seat's own origin. A gate that has the
  seat origin serves the login; a gate without one answers that the desk does
  not take phone logins.
- The seat gate guide and design now describe the phone's notices. A notice is
  a push to the Home Screen app that says "N waiting", sent by the notifier
  (`ataniq-seat-notify`) when the count of waiting cards rises above what was
  last told, with no page of the seat polling in the last 60 seconds, at most
  once in 2 minutes when the count does not rise, at most 30 a day, and never
  while the seat is not live; it shows nothing of a card, has no action and a
  tap opens the card list. The guide ("Notices" in docs/seat-gate.md) gives the
  steps to turn notices on at the phone (Add to Home Screen, open it from there,
  Enable notices, allow), to stop them and to test with `send-test`, the status
  texts, and troubleshooting rows; the design ("Notices" in
  docs/design/seat-gate.md) gives the protocol bounds, the service worker's
  rules, the content security policy's `manifest-src` and `worker-src`, the
  Trusted Types policy `seat-sw` for the service worker's registration, the
  notifier's egress and limits, the privacy cost (Apple's push service sees
  the timing and size of each push) and what a taken-over seat or notifier can
  do. Both say that the seat reaches no network address but the gate's, and, for
  notices, the notifier's local socket. Notices are built, not deployed and not
  tried on a phone.
- The seat can now turn notices on from the phone. When `ataniq-seat` runs
  with `--notify-socket` (a notifier that also takes subscriptions), the card
  list shows a Notices section with Enable notices and Stop notices buttons and
  a status line: notices work only from the Home Screen app on iPhone, so the
  page says to add it to the Home Screen, open it from there and tap Enable.
  The tap asks the phone's permission, registers a service worker (`/sw.js`)
  and hands the notifier the phone's push subscription through
  `POST /api/push/subscribe`; Stop notices drops it through
  `POST /api/push/unsubscribe` (the subscription is named by the SHA-256 of its
  address), and `POST /api/push/info` gives the push key. These calls need a
  session and are limited to 6 a minute for a credential. The seat serves a web
  app manifest (`/manifest.webmanifest`, the Home Screen app opens on
  `/cards/`) and its icons. A notice shows only "N waiting", or "Cards are
  waiting" for anything else, has no buttons and carries nothing of a card, and
  a tap on it opens the card list. The content security policy gains
  `manifest-src 'self'` and `worker-src 'self'`. Without `--notify-socket`
  these routes answer 404 and the pages show nothing about notices.
- `ataniq-seat-notify` (command `cmd/ataniq-seat-notify`, static, the standard
  library and packages `webpush` and `seatnotice` only) is the notifier of the
  seat's notices: the one unit that holds the VAPID key and the push
  subscriptions and talks to a push service. `vapid-init --key PATH` makes a
  new key file of mode 0600, never overwriting, and prints the public key
  only. `run` serves the seatnotice protocol on a Unix socket (mode 0660) to
  one user, the seat's, looked up once at the start and checked by the
  kernel's peer credentials; it keeps at most 4 subscriptions in a strict,
  atomically written file of mode 0600, accepts only endpoints on the hosts
  of `--push-hosts` (exactly `web.push.apple.com` by default, https and port
  443), sends `{"n":N}` with the topic `waiting` to each in turn, drops a
  subscription the push service says is gone, and sends at most 30 pushes in
  any hour. It logs fixed text and counts, never an endpoint or a key.
  `send-test --socket PATH N` is the owner's one test push, run as the seat
  user.
- Add `docs/seat-gate.md`, the operator guide of the phone approval path, for
  the scratch build with the owner channel's tag: what runs on the lead host and
  on the authority host, the keys of both ends (`ataniqd seat-gate init|pins`,
  `ataniq-seat gate-init`) and the pin file on each side, the owner-local origin
  file on both hosts, the firewall rule (one gate port, from the seat host only)
  and the reverse proxy rules (original Host kept, `Origin` and
  `Sec-Fetch-Site` untouched, the client-address header overwritten on every
  request, no caching, no redirects), rotation of a seat key, the gate key, the
  origin and a passkey, with what breaks when the order is wrong, pairing,
  login and the approve or deny of a card with a fresh passkey, the credential
  kinds and the accepted-kinds policy, what the STAND-IN banner and "scratch
  only" mean, the known limits and a troubleshooting table of the refusals the
  app shows. `docs/lead-daemon.md` no longer says the daemon has no network
  listener: the seat gate is its one, only with `--owner-channel --seat-gate`,
  and the accepted-kinds policy is applied, as the guide now says. Provisional
  and unqualified, for scratch state only.
- The seat's pairing page, once it shows the eight words and they match,
  also shows a fixed line, "When the desk has confirmed these words, log in:",
  with a "Log in" link to the login page. The phone is not told when the desk
  confirms, so the page does not redirect by itself. The line and link are not
  shown before the words, on a failure or when the words do not match.
- `owner.show`, on the owner connection and over the seat gate, marks a card
  whose decision is journaled and not yet taken by the door with
  `"on_its_way": true` (left out when false), so that a phone whose answer to
  a decision was lost can tell a decision on its way from none by reading the
  card again.

### Changed

- The intake no longer lets a lead choose a converter or a path in the owner's
  process. `ataniq-intake review` reads each commit as the raw object the lead
  wrote (`git rev-list` and `git cat-file`, not `git log`) and parses the
  headers itself, so git never converts a message from the charset that the
  commit's own `encoding` header names. A commit with an `encoding` header other
  than UTF-8 (`utf8` and any case of `UTF-8` pass) is refused, with its id and
  the value escaped, and so is a commit object whose headers are not in the form
  git writes (exit status 1; the lead commits the branch again). Commit text
  that is not valid UTF-8 is still shown escaped. A header or subject over
  32 KiB, a commit over 64 KiB, or commits over 8 KiB each on average are a
  limit error. A header of many continuation lines is parsed in time linear in
  its size, and the call's time limit and context are checked between the
  commits that are parsed. The `gitenv` package no longer allows the `git log`
  subcommand at all, so no owner-side command can convert a message.
  `ataniq-intake fetch` names the source of the fetch to the owner's git as a
  `file://` URL (a `%` in the path is written `%25`; a path with `@[` in it
  cannot be passed so, as git changes it, and a template whose `serve.repository`
  holds one is refused when it is loaded), so git never reads a
  bundle file at the template's `serve.repository` path itself and always starts
  the sandboxed upload-pack, also for a bundle that appears at the path after
  the checks. As a second layer it refuses a template whose `serve.repository`
  path exists on the host (a regular file, a symlink, any folder, or a path that
  cannot be checked), before the measure runs and again before git starts; a
  path that does not exist passes, as does the clone's own path while it is a
  real folder (a clone that was replaced by a symlink or a file in between is a
  hostile tree, exit status 1). The refusal's hint says that the path exists or
  cannot be checked and that the mirror was not changed, which holds for both
  checks. The `qualify-mirror` report gets the fact `repository_on_host` in
  `s16-fetch.clone`. See `docs/lead-intake.md` and
  `docs/qualification-lead-mirror.md`.
- The owner channel (tagged build only) now says why it refuses a merge whose
  feature has a commit with an `encoding` header other than UTF-8: the request
  is still refused `request-invalid` with no card, but the reply the lead reads
  and the request's status carry the line "a commit declares an encoding other
  than UTF-8" instead of "the host could not review the merge". The line is
  fixed and repeats neither the commit's id nor the header's value; the lead
  commits the branch again in UTF-8. See `docs/lead-daemon.md`.
- The seat's card page asks the decision guard too (read-only, nothing is
  logged): while decisions are paused on the authority host a pending card
  shows "Decisions are paused on this host." and no Approve or Deny form
  instead of buttons that every tap would refuse. The card stays readable;
  the refusal of a challenge and of a decision, pairing, login and the reads
  are unchanged.
- Tighten the supervisor, approval and security design pages. A nomination from
  a remote supervisory agent names a request that already exists in an instance
  and supplies no part of the card or of what is approved, and no agent text
  appears on a card in the first version; a card that is not decided expires
  and the request stays as it was. Each instance assigns and fences a remote
  supervisory agent alone, by a port credential and a client class the instance
  binds. The session that reads lead transcripts is never the session that
  acts, enforced by client identity in the instance, and the instance records
  what was sent to a provider. The idea that no grantee holds a signing key is marked as a
  proposal, and the joint request to Qargi as a request. Specification only;
  nothing is implemented.

### Fixed

- Pairing a phone refuses a credential id of 1024 bytes, which WebAuthn does
  not allow and the assertion verifier refuses: the owner wire and the list of
  deciding keys now take at most 1023 bytes, as the verifier does, so no
  credential is paired that could never approve.
- The scratch pass of the tagged build (`make ownerchannel-build`) numbers the
  cards of its stand-in door from the time of day and past the highest number
  its journal holds for the day, not from 1 on every start, so that a restart
  on the same state folder no longer makes a card with the id of one that was
  decided, which could not be decided itself.
- The seat's notices back off exponentially after a failed call to the notifier
  (a minute, then 2, 4, 8, 16 and 30 minutes, ended by a call the notifier
  took) and count only the calls the notifier answered, a count taken or a
  refusal with a code such as `rate_limited`, toward the 30 a day. A notifier
  that is down for half an hour no longer uses up the day's notices; calls that
  got no answer are held to 200 a day. Before, every call counted and a fixed
  minute's wait spent the 30 in half an hour.
- The seat does not send the phone a notice, a minute after the Cards page is
  closed, for cards the owner already saw on it: the count a live poll found is
  taken as seen and raises the count last announced to at least it. A card that
  arrives after the last poll still brings its notice.
- The notifier's `notify` call has its own deadline of 15 seconds on both sides
  (`seatnotice` `NotifyDeadline`), where `info`, `subscribe` and `unsubscribe`
  keep 2: a push to the phone may take up to 5 seconds, with up to four
  subscriptions, and a call cut at 2 seconds was counted as failed after the
  push service had accepted the message, so the seat sent it again a minute
  later. The notifier bounds all the pushes of one notify to 12 seconds, and the
  seat's own bound on the call is 20 seconds, so the client's deadline ends a
  slow call.
- The authority host's README and DEPLOY.md no longer say that a notifier that
  cannot start fails the switch's reload: the unit is `Type=simple`, so the
  restart succeeds once the process is forked, the health check is the seat's,
  and the switch ends `ok` while systemd restarts the notifier every 5
  seconds. They say how the owner sees it (`systemctl status`, the journal, the
  missing socket, `send-test`, the page's Enable notices failing) and what to
  do. The design document's limits also say what a taken-over seat can do to
  the notifier within its host policy: subscribe its own push-host endpoints up
  to the four places, replace or remove the owner's subscription, and cause up
  to 30 pushes an hour.
- Tapping a notice from the seat opens the Cards list also before the service
  worker controls any page: the worker looks at uncontrolled windows too,
  navigates one before it focuses it (a worker cannot navigate a page it does
  not control, and focusing first could use up the tap's permission to open a
  window) and otherwise opens the list in a window of its own. It still takes
  no control of pages and handles no requests.

### Security

- The notifier's unit runs in a group of its own, `seat-notify`, not in the
  seat's group, so the notifier, the one unit with egress, cannot reach the
  seat's socket in `/run/seat` (and so cannot send the seat a client address
  of its choosing). Its socket folder and socket belong to
  `seat-notify:seat-notify` (0750 and 0660), and the seat's unit joins that
  group with `SupplementaryGroups=seat-notify`, so the group must exist on the
  host before the seat's release is switched to (the README's setup makes it,
  with the seat's user as a member and Caddy not). Tests hold that the
  notifier's unit names no group `seat` and the seat's names `seat-notify`.
- The seat's content security policy pins the Trusted Types policy names with
  `trusted-types seat-sw`, the one policy `push.js` makes to register the
  service worker, with no `default` and no `allow-duplicates`, so a script that
  runs on the origin cannot make a policy of its own. A test holds that every
  policy any script creates is named, and no other.

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
  notice, then `Ctrl+\` and no key or mouse for 3 seconds), and step 14 says what
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
  and T4 for the second console of step 14 and the integrate command of
  step 20. `CHECKLIST.txt`, the preflight's READY text, the run's own prompts at
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
  any merged code. See [the lead intake guide](https://github.com/rett/ataniq/blob/master/docs/lead-intake.md).
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
  is recorded. See [the lead daemon guide](https://github.com/rett/ataniq/blob/master/docs/lead-daemon.md).
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
  See [lead injection](https://github.com/rett/ataniq/blob/master/docs/lead-injection.md).
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
  [the lead mirror qualification manual](https://github.com/rett/ataniq/blob/master/docs/qualification-lead-mirror.md).

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
  key (`Ctrl+\` by default, set with `--return-key ctrl+KEY`) and F12, which
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
