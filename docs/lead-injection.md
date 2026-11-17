# Lead injection

This guide describes the fenced message injection library, under
`internal/inject`. It is for a contributor who will build the holder or the
daemon, and for the owner, who must know what is established and what is not.

The library is provisional and unqualified. It is built from recorded sessions
of one version of each agent. No live qualification of this code has been run.

## What this is

Ataniq sometimes has to put a message into the composer of a lead that runs in
a terminal, and to submit it. Doing that blind can answer an approval dialog,
join the message to the owner's half-typed draft, or send Enter into the wrong
screen. The library puts a message in only when the screen shows that the lead
is idle, and presses Enter only when the screen then shows the message in the
composer. When it cannot tell, it writes nothing, or it leaves the pasted text
where it is and does not press Enter.

It is a library only. No daemon, no holder and no command uses it yet. Nothing
here starts an agent, opens a terminal or touches a real one. The tests use
recorded screens and a fake session.

The packages are:

| Package | What it does |
| --- | --- |
| `internal/inject/osc7501` | Reads the OSC 7501 status reports of the agents, builds the reply to claude's support query, and keeps the last report. |
| `internal/inject/screen` | Reads a text snapshot of the agent's screen: composer, dialog marks, footer and the rest. |
| `internal/inject/title` | Reads the window title of the agent. |
| `internal/inject/idle` | Holds the tested table, and joins the three readings into one classification. |
| `internal/inject` | The fence (`Deliver`) and the interface a holder implements for it. |
| `internal/inject/command` | Message admission and the durable command state machine. |

The queue lives in the daemon and the fence lives in the holder, so `command`
and the fence do not import each other. Only a test joins them. The daemon
passes the fence result to the command machine as plain values.

## The rules

These are the nine rules of the design, in our words, with the place that
enforces each.

1. **The daemon is the only injector.** A supervisor or a lead asks the daemon;
   it never reaches a holder. This library does not enforce it, because the
   daemon is not built. It fixes the shape: `command` is for the daemon and
   `Deliver` is for the holder, and nothing else joins them.
2. **A message is data, with a fixed header.** The first line is
   `[ataniq-msg id=<id> from=<from> kind=<kind>]`. `command.HeaderLine` and
   `command.RenderText` make it. `inject.Deliver` refuses a text whose header
   is blank or is not the first line.
3. **`from` is set from the authenticated sender and is never `owner`.** The
   sender is a typed value: `command.Daemon()` renders `ataniq`,
   `command.Supervisor()` renders `supervisor`, `command.Lead(project)` renders
   `lead:<project>`. No constructor gives `owner`. `command.ParseSender`
   refuses `owner` with `ErrOwnerSender`, and so does `command.Restore` for a
   journal that holds it. The owner speaks as the writer of the terminal.
4. **A body that carries the header marker is refused.** `command.CheckBody`,
   called by `Machine.Submit`, refuses it with `forged-header`. The rule is
   wider than "a line that starts with the marker"; see
   [Admission](#admission).
5. **Who may send is a short list, and caps refuse, not drop.**
   `command.EdgeAllowed` and `Machine.Submit` enforce the edges and the caps.
   A refused message is journaled with its reason and the sender.
6. **Ataniq never sends a key to an approval prompt.** `Deliver` writes
   nothing when the report is `blocked` or the screen shows a dialog (the
   first check), and writes no Enter when a dialog is there after the paste
   (the second check). The only bytes it ever writes are one bracketed paste
   and, at most once, one carriage return. `idle.Classifier.Classify` gives
   `blocked` for a `blocked` report or a dialog on the alternate screen,
   whatever else it reads.
7. **Never write Enter unless the composer is known to be idle and known to
   hold the text.** The second check of `Deliver` (see
   [The fence](#the-fence)).
8. **When the guard cannot tell, the answer is "not idle".** The signals that
   cannot be read give `unknown` in `idle`; a screen no recogniser knows gives
   `Known` false in `screen`; `Deliver` treats every state but `idle` as a
   reason to stop.
9. **Tested versions only.** `idle.Lookup` is the one table. `Deliver` looks
   the session's runtime and version up before it takes the input lock, and a
   pair that is not in the table gives `untested version` with the session
   untouched. `screen.For` and `title.For` know the same two pairs and no
   other. `command` records the refusal as `untested-version`.

## The tested table

| Runtime | Version | Spelled also | App name in reports |
| --- | --- | --- | --- |
| claude | 2.1.325 | | `claude-code` |
| codex | 0.169 | 0.169.0 | `codex` |

`idle.Tested` returns this table. Any other runtime or version is refused. A
neighbouring version is not guessed from: claude 2.1.326 and codex 0.170 are
refused.

"Tested" here means two things, and no more:

- The recognisers were built from recorded sessions of these versions, and
  every recorded screen is replayed in the tests with the reading expected for
  it.
- Two attended checks (the Q13 runs) showed that both agents kept an open
  approval dialog unchanged when a paste arrived with no Enter. The claude
  dialog was a file write, and the codex dialog was a command to run. One run
  for each agent.

It does not mean the code has been run against a live agent. See
[Limits](#limits-and-what-is-not-established).

## Idle classification

`idle.Classifier.Classify` takes the last OSC 7501 status, the screen
snapshot (whose `Title` is the window title), whether the process has ended,
and, for claude, the permission mode the footer must show. It returns one of
five states and the list of signals that said so (`Classification.Signals`, in
the order report, title, screen, process).

The states are `idle`, `working`, `blocked`, `ended` and `unknown`. In this
order, for both runtimes:

1. A process that has ended gives `ended`.
2. A usable report with state `blocked`, or an open dialog on the alternate
   screen, gives `blocked`, whatever else is read. The signals tell a blocked
   report from a dialog.
3. A stale report, a report from another app and a report whose state cannot
   be read give `unknown`. A stale report is one that an unreadable payload
   followed.
4. A screen the recogniser does not know, or a screen that is not the
   alternate screen, gives `unknown`.
5. The rules of the runtime, below.

The state is `idle` only when every signal required for the runtime says so.
`Classification.ComposerEmpty` is separate: a composer that holds the owner's
draft at an idle prompt is `idle` with `ComposerEmpty` false. The caller checks
emptiness itself.

### claude 2.1.325

| Signal | Role | What it says |
| --- | --- | --- |
| Screen recogniser | Required | The composer at its place, the cursor in it, the footer showing the expected mode, no dialog mark, no background task, no working line. The caller names the mode in `Input.ExpectedMode`; an empty one never matches. |
| OSC 7501 report | Only vetoes | `working` gives `working`; `error` and `clear` give `unknown`; `idle`, `done` or no report at all leave the screen's answer. |
| Window title | Only vetoes | A busy glyph gives `working`. The glyph `✳` proves nothing: it shows both when idle and at an approval prompt. |

A busy title, a working line or a background task gives `working`. With the
holder answering claude's query, claude 2.1.325 showed no glyph in its title
even while working, so the claude title will rarely veto anything. Nothing may
count on it.

When a collapsed paste is in the composer, claude's footer says "paste again to
expand" and shows neither the working hint, nor the shell count, nor a mode.
The screen reading then has `StateHidden` true, and the state comes from the
report: it is `idle` only with an `idle` or `done` report, and otherwise
`unknown`. That `idle` sets `Classification.ReportOnly`: it rests on the report
alone and does not rule out a background task. Only the fence's check after
its own paste may accept it. Every other consumer (a status, a notice, the
check before a paste) must treat `ReportOnly` as not idle.

### codex 0.169

| Signal | Role | What it says |
| --- | --- | --- |
| OSC 7501 report | Required | App `codex`. `idle` or `done` says idle, `working` says working, `blocked` says blocked. No report, `clear`, `error` and another app give `unknown`. |
| Window title | Required to agree | Neither a busy glyph (a braille spinner, or a leading symbol that was not recorded) nor the words `Action Required`. |
| Screen recogniser | Required to agree | The composer, the cursor in it, no dialog, no working line. |

When the report says idle and the title or the screen says otherwise, the
state is `unknown`, not the other signal's state (a report of `idle` with
`Action Required` in the title is `unknown`). A `working` report gives
`working` once the screen is a known one on the alternate screen.

### Unknown screens

A screen is unknown when the recogniser cannot place it. That includes an empty
screen, one smaller than 20 columns by 5 rows or larger than 500 by 200, text that is not valid UTF-8 or holds a
control character, a row wider than the screen, and a layout the recorded
screens do not show. An unknown screen gives `unknown` and nothing is written.

## OSC 7501

OSC 7501 is a private escape sequence: `ESC ] 7501 ;` then a payload, ended by
BEL or `ESC \`. The agent writes it, so every byte is untrusted. `osc7501.Parse`
reads at most 4096 bytes and 32 fields, refuses what it cannot read exactly,
and never guesses.

A payload is either a query (it starts with `?`) or a report. A report is
fields separated by `:`, each `key=value`, with no key twice. `state` is
required: `idle`, `working`, `done`, `blocked`, `error` or `clear`. `app` and
`kind` are tokens of letters, digits, `-` and `_` up to 64 bytes. `msg` and
`title` are base64 of valid UTF-8, decoded with control and bidirectional
characters replaced and cut to 1024 bytes. Other keys are ignored and counted.

The report forms recorded from the two agents:

| Agent | Payloads recorded |
| --- | --- |
| claude 2.1.325 | `state=idle:app=claude-code`, `state=working:app=claude-code`, `state=done:app=claude-code`, `state=blocked:app=claude-code:kind=permission:msg=<base64>`, `state=clear` |
| codex 0.169 | `state=idle:app=codex`, `state=working:app=codex`, `state=done:app=codex`, `state=blocked:app=codex:kind=permission`, `state=clear` |

`state=clear` has no app. The tracker keeps it as the last report.

`osc7501.Tracker` remembers the last valid report. `Tracker.Status` returns it
with `Has` (a valid report arrived) and `Stale` (an unreadable payload arrived
after the last valid one, or with none). A stale report is not the agent's
state, and the classifier never treats it as one.

### The query reply

claude asks first whether the terminal supports OSC 7501, by sending the query
`ESC ] 7501 ; ?` and then a DA1 query (`ESC [ c`) as a sentinel. In the Q13
runs claude 2.1.325 reported once the reply to the query reached it before the
reply to DA1. The holder must answer:

```text
ESC ] 7501 ; ? ESC \
```

`osc7501.Reply(payload)` returns those bytes (`osc7501.QueryReply`) for a
query payload and `false` for anything else. The holder calls it from its
emulator's OSC 7501 handler, while the emulator is parsing the query, and
writes the bytes at once, so that they are queued ahead of the reply the
emulator makes to the DA1 that follows. A reply written later from another
goroutine can lose the race, and claude then does not report.

The reply is also a write to the agent, and the agent decides when a query
comes. So it must travel the same serialised input path as every other write,
under the lock order described in
[The holder-facing interface](#the-holder-facing-interface). A reply must
never fall between a paste and its Enter, or inside the paste.

## The screen recognisers

`screen.For(runtime, version)` returns the recogniser for one tested pair.
`Recogniser.Read(Snapshot)` is pure: the same snapshot always gives the same
`Reading`. A `Snapshot` has the text of the screen, its size, the cursor, the
alternate-screen flag, the title, and the faint spans.

A `Reading` says whether it is `Known`, and then what it saw:

- **Composer.** claude: a box between two full-width rules of `─`, with the
  prompt glyph `❯` and, below the lower rule, the footer as the last row.
  codex: a prompt row that starts with `› `, a blank row above it, and the
  status rows below. The reading says where the cursor is (`CursorInComposer`)
  and what the composer holds (`ComposerText`).
- **Empty composer.** `ComposerEmpty` is true when the composer holds nothing
  with the cursor at its first input column, or only the runtime's hint in
  faint style. A consumer tests `ComposerEmpty` and never
  `ComposerText == ""`.
- **Placeholder and faint text.** Both agents draw a hint in an empty composer
  in faint style, in characters an owner could type: codex's "Ask Codex to do
  anything", and claude's suggestions and the ghost of the last prompt. Plain
  text cannot tell a hint from typed text. So the composer counts as empty
  with a hint only when every character of the hint is faint and the cursor is
  on the first one. For codex the hint must also be exactly the recorded text;
  claude's ghost text is the last prompt and cannot be pinned. `Placeholder`
  says that the hint case applied.
- **Dialog marks.** Looked for only in the bottom rows (20), and only at the
  start of a row. claude: `Esc to cancel`, `Do you want to`, and a numbered
  choice that starts with `❯`. codex: `Press enter to confirm`, `Would you like
  to`, and a numbered choice that starts with `›`. A dialog replaces the
  composer: `Dialog` is true and `Composer` false.
- **Background task.** claude only: a shell count in the footer, or a status
  line ending in "shells still running".
- **Working.** claude's footer hint `esc to interrupt`, and codex's working row
  ("Working (... esc to interrupt)").
- **Footer mode.** claude's footer: the recorded modes are manual
  (`manual mode on`) and auto (`auto mode on (shift+tab to cycle)`). Any other
  footer makes the screen unknown.
- **Pasted-text marker.** `[Pasted text #` for claude and `[Pasted Content` for
  codex, in the composer. `PastedMarker` says it is there.
- **The paste footer.** claude's footer "paste again to expand", shown while a
  collapsed paste is in the composer. `StateHidden` is true, `Working` and the
  shell count are not observed, and the consumer takes the working state from
  the report.

The text of a screen is controlled by the agent. Prose of an answer that wraps
into the bottom rows can at worst make a screen look like a dialog, which is the
safe direction: a screen then looks less idle, never more.

### What the holder must fill

The holder must fill `Snapshot.Faint` on every snapshot, from the faint (SGR 2)
attribute of the emulator's cells. The offsets count characters in the row of
`Text`, not cells (`screen.Span`). With `Faint` left empty, a composer that
shows a hint reads as holding text, so nothing would ever be injected into
codex. This fails safe, but it makes the library useless.

### The fixtures

The fixtures are recorded screens, scrubbed, in
`internal/inject/screen/testdata/<runtime>-<version>/`. Each is a pair: a
`<name>.txt` with the screen text and a `<name>.json` with the size, the
cursor, the alternate-screen flag, the title, the faint spans, a description,
where it was recorded, and the expected reading (`want`). They come from the
recorded sessions of the attended runs. `internal/inject/osc7501/testdata` and
`internal/inject/title/testdata` hold the recorded report payloads and titles in
the same way. A home path in a fixture must be the placeholder user's, and a test
fails on any other.

To add a fixture:

1. Scrub the recorded screen and copy it to `<name>.txt` in the folder of its
   runtime and version.
2. Write `<name>.json` with the snapshot values, a description and the source.
3. Run the replay once with `-update` to fill `want`, for example
   `sh scripts/go-tool.sh test ./internal/inject/screen/ -run TestReplay -update`,
   and read the diff. `-update` writes whatever the code reads now.
4. Add a row for it to `replayWant` in `internal/inject/idle/replay_test.go`,
   which holds the classification each fixture must give. The `idle` tests read
   the same files and fail on a fixture without a row.

The coverage test fails if the set loses a kind of screen it was written from
(an empty composer, a hint, a dialog, a working screen, a background shell, a
collapsed paste, a pasted header, and so on).

## The fence

`inject.Deliver(ctx, session, Message{Text, Header}, Options)` makes one
attempt to put one message into one session. It never waits for the lead to
become idle, never retries, never writes anything to clean up, and never
sleeps. Every wait is a `Settle` call of the holder, and the only clock it has
is the deadline it was given.

The steps, in order:

1. **The text rule.** Valid UTF-8; printable characters and newlines only; 1 to
   4096 bytes; a header that is not blank and equals the first line. Otherwise
   the outcome is `refused text` and the session is not touched.
2. **The table.** The runtime and version must be in the tested table.
   Otherwise `untested version`, and no lock is taken.
3. **Lock and settle.** With the input lock held, `Settle` with no minimum, a
   50 ms quiet and at most 1 s. If it does not drain, the outcome is `not idle`.
4. **The first check**, in one `Frozen` call. All of these must hold:
   - the session has not ended;
   - the parser is at ground, no synchronized-output frame (mode 2026) is
     open, and bracketed paste is on;
   - the classification is `idle`; a `blocked` classification, from the report
     or from a dialog on the screen, gives the outcome `blocked` instead of
     `not idle` (the Q13 veto);
   - the composer is empty with the cursor in it, with no pasted-text marker,
     and the screen does not hide the state (so an idle that rests on the
     report alone is not accepted here);
   - the writer has typed nothing for 2 s, and the screen has not changed for
     300 ms;
   - the context is alive and the deadline has not passed. The clock is read
     last, directly before the write.

   If all hold, the paste is written inside this same call, as one write:
   `ESC [ 200 ~`, the text with each newline as a carriage return, `ESC [ 201 ~`.
5. **Settle after the paste.** At least 150 ms, 50 ms quiet, at most 1 s. If it
   does not drain, the outcome is `not submitted`.
6. **The second check**, in one `Frozen` call. The session has not ended; the
   parser is at ground with no frame open and bracketed paste still on; the
   classification is `idle` again; the composer is on screen with the cursor
   in it; the composer holds the header, or shows the pasted-text marker where
   there was none before the paste; and the deadline has not passed. If all
   hold, a carriage return is written, as its own write, inside this same call.
   Otherwise nothing is written, the outcome is `not submitted`, and the paste
   stays where it is.
7. **Release.** The lock is released, and the holder delivers the writer's
   held input.

The timing values are those of `inject.DefaultTiming()`. A zero `Timing` means
these values:

| Value | Default |
| --- | --- |
| Owner quiet | 2 s |
| Screen quiet | 300 ms |
| Check quiet (every `Settle`) | 50 ms |
| First settle, longest | 1 s |
| Settle after the paste | at least 150 ms, at most 1 s |

The design gives no maximum for the first settle. The 1 s is this package's
choice.

What the fence never does: it never writes a key into a dialog, never writes
anything but the paste and one Enter, never writes the Enter without a second
check that passed, never writes after the deadline, never retries and never
cleans up. The context ending, or the deadline passing, gives `not idle` before
the paste and `not submitted` after it. An error of the session or of a write
gives `died`, with `Written` as `nothing` when no write was attempted and
`unknown` after one was.

The outcome `submitted` is not delivery. It says that the paste and the Enter
were written while the screen showed an idle composer that held the header.
Only the agent's conversation file can show that the agent took the message.

A `Result` carries `Written`, `Outcome`, `State`, both readings (`Before` and
`After`) and a short `Detail`. `Result.Readings()` renders one bounded line of
fixed words and short tokens. It never holds the message, the owner's draft or
a line the agent drew. The daemon maps a `Result` to the command machine's
`FenceResult` in a few lines. The spellings match, and a test fails if they
drift:

```go
res := inject.Deliver(ctx, session, inject.Message{Text: d.Text, Header: d.Header}, opts)
update, err := machine.RecordFence(d.ID, command.FenceResult{
	Written:  command.Written(res.Written),
	Outcome:  command.FenceOutcome(res.Outcome),
	Readings: res.Readings(),
}, now)
```

Here `d` is the `command.Dispatch` that `BeginDispatch` returned before the
fence ran. The daemon gives `Options.Deadline` a value shorter than the command
machine's fence timeout (`Limits.FenceTimeout`, 30 s by default).

## The holder-facing interface

The holder owns the PTY and the emulator. It implements three interfaces,
which are in `internal/inject/session.go` and whose comments are written for
the author of a holder.

- **`Session`**: `Runtime()` returns the runtime and version as the holder
  found them, for example `claude` and `2.1.325`. `WithInputLock(ctx, fn)`
  takes the input lock, runs `fn` and releases the lock. It returns an error
  without running `fn` when it cannot take the lock or the context ends first.
- **`Locked`**: the lock, held. `Settle(ctx, min, quiet, max)` reads the PTY
  until it would block, applies everything to the emulator, writes the replies
  that causes, and waits until `quiet` has passed with no new output, for at
  least `min` and at most `max`. It returns `drained` false when `max` ended the
  wait with output still arriving. An error from the context ending must wrap
  `ctx.Err()`. `Frozen(fn)` runs `fn` once, on the calling goroutine, with no
  output applied to the emulator while it runs, and passes it a `View` and a
  `write` function. `write` is one write call to the PTY master and is valid
  only inside `fn`; the paste is one call and the Enter another. An error from
  `write` means the bytes may or may not have reached the agent.
- **`View`**: the state at one instant, with no waiting and no reading of the
  PTY. `Snapshot()` is the screen, with `Faint` filled. `Report()` is the
  `osc7501.Status`, with `Stale` set when it applies. `Term()` is a `TermState`.
  `OwnerQuiet()` is the time since the writer last typed, counted from when a
  key was typed even if it was held. `ScreenQuiet()` is the time since the
  screen last changed. `Ended()` says the agent's process has ended.
- **`TermState`**: `ParserGround` (the parser is between sequences, not in the
  middle of an escape sequence, a string or a partly received character),
  `FrameOpen` (a synchronized-output frame is open) and `BracketedPaste` (the
  agent turned mode 2004 on and has not turned it off). Without bracketed paste
  a paste would be typed as keys, and an Enter inside it would submit.

The rules the holder must keep:

- **One write.** The paste is one `write` call and the Enter is another, both
  inside `Frozen`, with nothing between the reading and the write.
- **Held writer input.** While the lock is held, what the writer types is held,
  not dropped. When the function given to `WithInputLock` returns, the holder
  delivers it in order. None of it falls inside a paste.
- **The reply rule.** The terminal's replies (the OSC 7501 query reply, DA1,
  cursor position and colour queries) travel the same serialised path as every
  other write. `Settle` writes the replies that the output it applied caused,
  before it returns and in the order the emulator made them (the OSC 7501 reply
  before the DA1 reply). `Frozen` applies no output, so it makes no reply, and
  none can fall between the paste and the Enter. No goroutine that carries
  replies may wait for the lock while `Settle` waits for it. A holder that gives
  replies their own goroutine and a bypass around the lock breaks the rule.
- **`Frozen`.** `fn` is called at most once; two calls to `View` inside it see
  the same state; the screen cannot move between the check and the write.
- **`Faint`.** Fill it on every snapshot.

## Durable commands

`command.Machine` is the daemon's queue and state machine. It is pure: no I/O,
no goroutine, no clock (each method takes the time). It is not safe for
concurrent use, and the daemon serialises calls to it.

A message becomes a command that moves through these states:

| From | To | Caused by | Reason code |
| --- | --- | --- | --- |
| none | refused | `Submit` refuses the message. A command is not made. | an admission reason |
| none | accepted | `Submit` admits the message and journals its text. | |
| accepted | queued | `Submit`, in the same call; or `Tick` or `Restore` if that record failed. | |
| queued | dispatching | `BeginDispatch` for the head of an unfrozen queue. This record is the intent. | |
| dispatching | queued | The fence wrote nothing: not idle, or blocked. | `not-idle`, `blocked` |
| dispatching | refused | The fence wrote nothing: untested version, text refused, or not idle after the queue time limit. | `untested-version`, `text-refused`, `not-idle` |
| queued | refused | `Tick`: the head waited longer than the queue time limit. | `not-idle` |
| dispatching | dispatching | The paste and the Enter were written. Enter is recorded once. | `enter-written` |
| dispatching | delivered | `ConfirmDelivery` with evidence, after Enter. | `confirmed` |
| dispatching | uncertain | The paste was written and Enter was not; or the fence died or what was written is unknown; or the result contradicts itself; or no fence result in `FenceTimeout`; or no evidence in `ConfirmTimeout` after Enter. | `not-submitted`, `holder-or-agent-died`, `inconsistent-result`, `no-fence-result`, `submitted-not-confirmed` |
| uncertain | uncertain | A late fence result or late evidence, kept as a note. | `late-fence-result`, `late-evidence` |
| uncertain | delivered | An operator decides it was delivered. | `operator-delivered` |
| uncertain | resolved | An operator decides it was not delivered, and drops it or sends it again. | `operator-drop`, `operator-resend` |

**The journal rule.** Every move first appends a `Record` to the `Journal` the
daemon gives the machine, and the machine changes state only if the append
succeeded. `BeginDispatch` appends the `dispatching` record, the intent, before
it returns the text. So no byte may be written without the intent in the
journal. `Journal.Append` must have made the record durable when it returns nil.
The journal is the truth: `command.Restore(limits, journal, records, now)`
rebuilds the machine from it, and refuses a journal that does not follow the
state machine.

**FIFO and the freeze.** The queue of each target is first in, first out, and
only the head may dispatch. An `uncertain` command freezes that target's queue:
`BeginDispatch` refuses it until an operator decides.

**What makes a command uncertain.** `uncertain` means nobody knows whether the
message reached the agent. The reasons:

- `not-submitted`: the paste is in the composer and Enter was not written.
- `holder-or-agent-died`: the fence reported a death or an unknown write, or
  `Restore` found the command `dispatching`. Even a death with nothing written
  is uncertain.
- `inconsistent-result`: the fence result contradicted itself.
- `no-fence-result`: no fence result came within `FenceTimeout` (30 s) of the
  dispatching record. Nobody saw anything die. The fence may still be running.
- `submitted-not-confirmed`: Enter was written and no evidence came within
  `ConfirmTimeout` (30 s).

**Operator resolution.** Only an operator decision leaves `uncertain`.
`Machine.Resolve` takes one of three decisions, with the operator's name:

- `delivered`: the command becomes `delivered`, on the owner's decision.
- `not delivered, drop`: the command becomes `resolved`.
- `not delivered, send again`: a new command is made, with a new id that the
  caller supplies, the same sender, target, kind and body, and a header with
  the new id. It is queued at the head of the queue and names the old one. The
  old command becomes `resolved` and names the new one. The admission caps do
  not apply to it, because it takes the old one's place. A resend is a new
  command, never a retry of the old one.

Any of the three unfreezes the queue.

**No automatic retry.** Nothing in the package resends. A command that is
`uncertain` stays so until an operator decides. `Tick` never dispatches and
never resends.

**Late notes.** A fence result or delivery evidence that arrives after the
command became `uncertain` (after a `FenceTimeout`, say) is journaled as a note
on the command, and `View` shows it to the operator. At most eight are kept. It
never ends the `uncertain` state.

**Restore.** After it replays the records, `Restore` writes two kinds of record.
A command found `dispatching`, with or without Enter recorded, becomes
`uncertain` (`holder-or-agent-died`) and is never sent again by itself. A
command found `accepted` is queued. It rebuilds the rate windows from the
journal.

**Blocked notice.** A result of `blocked` returns the command to the queue and
gives the daemon the notice "this lead is waiting for an approval", once for a
blocked spell. The daemon delivers the notice; it is not built.

## Admission

`Machine.Submit` takes a `Message`: an id, a sender, a target, a kind and a
body. It makes the header and the text. It refuses with a reason and journals
the refusal, and queues nothing, in this order: sender or target not set; a
project name outside the name grammar; an edge that is not allowed; an id or
kind outside the name grammar; a duplicate id; a whole text over the size cap;
the body rules; the target's queue caps; the sender's rate cap.

**Header.** `[ataniq-msg id=<id> from=<from> kind=<kind>]`, then a newline,
then the body. The id, the kind and the project names are 1 to 64
bytes of lower-case letters, digits, `.`, `_` and `-`, starting with a letter or
digit, so a header cannot be forged through them.

**Senders and edges.** The sender is one of `ataniq`, `supervisor` and
`lead:<project>`, and `from` is never `owner`. The target is `supervisor` or
`lead:<project>`. The allowed edges are:

- the daemon to the supervisor or to any project lead;
- the supervisor to a project lead;
- a project lead to the supervisor.

Everything else is refused: lead to lead (a lead to itself too), supervisor to
supervisor, and any value that is not set.

**The text rules.** The body must be valid UTF-8 (`invalid-utf8`). Every
character must be a newline or a printable character (`bad-character`). That
refuses ESC, and with it the paste-end sequence, every other control character,
carriage return, tab, the Unicode line and paragraph separators, non-ASCII
spaces, and the bidirectional and other format controls. The body must hold
something besides spaces and newlines (`empty-body`). The whole text, header
included, must be at most 4096 bytes (`text-too-long`).

**The marker rule.** A body is refused (`forged-header`) when its fold contains
`ataniqmsg`. The fold lower-cases the body, drops the characters that render as
nothing (the default-ignorable ones, such as the Hangul fillers), maps fullwidth
forms and mathematical letters and digits to ASCII, and keeps only letters and
digits. So `ataniq-msg`, `ATANIQ MSG`, `[ataniq_msg`, a marker split by a
combining mark or a line break, and a marker in fullwidth letters are all
refused. This is wider than "a line that starts with `[ataniq-msg`": it refuses
the marker anywhere in the body. The rule is about the body only.

The known limit: look-alike letters of other scripts are not folded. Cyrillic
and Greek look-alikes, the dotless i, modifier letters and small capitals pass.
A body that spells the marker with them is admitted, so a body can still show a
line that looks like a header to a human or to a model. Closing that needs a
decision, for example a fixed prefix on every body line. It is an open question
for the owner.

**The caps.**

| Cap | Value |
| --- | --- |
| Messages in one target's queue | 16 |
| Bytes of text in one target's queue | 64 KiB |
| Messages from one sender | 6 in a minute |
| One whole text | 4 KiB |

A command holds its place in a queue from admission until it is delivered,
refused or resolved, and an `uncertain` command keeps its place. A refused
message does not count toward the sender's rate. The queue caps are per target,
which is a project lead or the supervisor.

**Values to configure.** `command.DefaultLimits()` gives the caps above, the
30 s confirmation time (`ConfirmTimeout`) and the 30 s fence timeout
(`FenceTimeout`). It leaves `QueueTimeLimit` at zero. The queue time limit is
how long a queued head may wait for an idle lead before it is refused as not
idle. The design gives no number, so there is no default, and `command.New`
fails until the caller sets it. `FenceTimeout` must be longer than the fence's
worst case (both settles, the wait for the input lock, and the call to the
holder), and the fence deadline must be shorter than it.

## Outcomes

What was written, how the fence ended, and what the command becomes:

| Case | Written | Fence result | Command |
| --- | --- | --- | --- |
| The guard fails before the paste | nothing | `not idle` | Back to `queued`, with the readings kept. Once it has waited longer than `QueueTimeLimit`, `refused` as `not-idle`. |
| The runtime version is not in the tested table | nothing | `untested version` | `refused`, `untested-version`. |
| The text rule fails | nothing | `refused text` | `refused`, `text-refused`. |
| The state is `blocked` | nothing | `blocked` | Back to `queued`, `blocked`, with the notice for the owner once per blocked spell. |
| The check after the paste fails | paste | `not submitted` | `uncertain`, `not-submitted`. Nothing is sent to clean up. |
| Enter was written | paste and Enter | `submitted` | Stays `dispatching` with Enter recorded. With evidence from the agent's conversation file, `delivered`. With none in 30 s, `uncertain`, `submitted-not-confirmed`. |
| The holder or the agent died during the fence | nothing or unknown | `died` | `uncertain`, `holder-or-agent-died`. |
| No fence result in 30 s | unknown | none | `uncertain`, `no-fence-result`. |
| The daemon restarts with the command `dispatching` | unknown | none | `uncertain`, `holder-or-agent-died`. No resend by itself. |
| A result that contradicts itself | | any | `uncertain`, `inconsistent-result`. |

The library takes delivery evidence as a plain value from the daemon
(`ConfirmDelivery`). It does not read the conversation file.

## Limits and what is not established

The library narrows risks. It does not remove them. These are the limits, as
bluntly as we can state them.

- **The window cannot be closed.** The agent can open a dialog on its own
  between the last check and the Enter, for example when a background task
  finishes. Then the Enter answers that dialog with its default. The window is
  the time of one write. Treating a background task as not idle narrows it.
  Nothing closes it.
- **Dialogs ignore a paste on one observation.** That a paste into an open
  dialog does nothing rests on one recorded observation per agent, in one
  version: a claude file-write dialog and a codex command dialog. codex binds
  letters and claude binds digits in its dialogs. It is a live case for each
  version, not an assumption.
- **Remote Control.** With a provider's remote control on, input can arrive
  that the holder cannot see or hold.
- **claude during a blocking tool call** is untested. claude is injected only
  when idle.
- **Few screens were recorded.** For claude, only the manual and auto footers
  and one dialog kind (a file write). For codex, one dialog kind (a command to
  run). Any other footer or dialog reads as an unknown screen, or, if its
  marks match, as a dialog.
- **A codex composer taller than the screen is unrecorded.** The recorded tall
  composers fit the screen.
- **Look-alike letters.** A body can still show a line that looks like a
  header (see [Admission](#admission)).
- **A body line that begins with a dialog mark.** The recognisers read a mark
  at the start of a row. A codex composer indents its continuation rows by two
  blanks, which the codex rule accepts, so a body line such as `Press enter to
  confirm` shown in the composer reads as a dialog. The second check then sees
  `blocked`, and the command ends `uncertain: not submitted`, with the text
  pasted and no Enter. This is read from the code. It fails safe and needs an
  operator.
- **Blanks typed and the cursor moved home.** A composer of blanks that the
  owner typed, with the cursor then moved back to the first column, shows the
  same as an empty composer and reads as empty.
- **Header-only check.** The check after the paste looks for the header, or
  the runtime's pasted-text marker where there was none before. It does not
  check the whole body.
- **A claude idle that rests on the report.** Under the collapsed-paste footer
  the screen shows neither the working hint nor the shell count, so the check
  after a paste accepts `idle` from the report alone and cannot rule out a
  background task.
- **Journal growth.** Refused submissions are journaled without a bound, and
  ended commands stay in memory with no compaction. The daemon must rate-limit
  before `Submit` and trim.
- **The queue time limit and a blocked lead.** Today a head that waits longer
  than `QueueTimeLimit` is refused as not idle even when the lead was blocked
  all that time. Whether it should be is an open question for the owner.
- **No live qualification.** No run of this code against a live agent has been
  made. Every test uses recorded screens and a fake session.

## Not built here

- The daemon: the queue process, the journal store, the deadline and fence
  timeout that must agree, the queue time limit, and the owner's decisions.
- The holder and its PTY and emulator, which implement `Session`, `Locked` and
  `View`.
- Delivery confirmation from the agent's conversation file. Before dispatching
  an operator's resend, the daemon must look in that file for the ids of the
  whole resend chain, and not dispatch when one is found. That is not built.
- The control socket operation that carries an injection.
- The notices to the owner, including "this lead is waiting for an approval".
- The sandbox.

### What must be checked live before use

These cases of the design must be run against live agents, for each agent and
version, before any lead is injected:

- **I-1.** Inject at an idle composer, three times for each agent. Each
  message is delivered once, byte for byte, and confirmed in the conversation
  file.
- **I-2.** Inject while the agent works, and while only a background task runs.
  Nothing is written until idle, and the running turn is unharmed.
- **I-3.** Inject while an approval prompt is open. Nothing is written, the
  prompt is still open, and the owner is told that the lead is blocked.
- **I-4b.** Paste texts into an open approval prompt with no Enter: digits,
  `y`, `p` and a full message. The dialog is unchanged and nothing is approved.
  A version that fails is not in the tested table.

Until then the table says only that the recognisers match the recorded screens.
