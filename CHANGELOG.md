# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  real lead; `ataniq-scratch` starts it.
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
  descriptor in the inside mode.
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
