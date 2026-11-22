# Terminal console prototype

The terminal-first Bubble Tea console is the planned primary operator client; a
web console with the same information architecture comes later. Both sit on the
shared typed command and event contract. Today the console is a provisional
cockpit with three modes. With `--demo` it shows sample cards and a recording:
everything it shows is fictional fixture data, the terminal pane replays a
recording, it is not a live session, and nothing you type reaches a process.
With `--attach` it shows a live scratch session that a holder keeps, for a
scratch agent only (see
[Attaching to a holder](#attaching-to-a-holder)). With `--daemon` it shows the
daemon's leads, read-only (see [The daemon view](#the-daemon-view)), and, only
when the daemon is a tagged one that serves lead control, a leads view that
drives scratch leads (see [Lead control](#lead-control-scratch-daemon-only)).
No mode is a production backend. Apart from that leads view, the console does
not send instructions, steer or interrupt leads. It has an approvals view, but
in this release a decision there changes nothing (see [Approvals](#approvals) and
[What is a sample](#what-is-a-sample-and-what-is-not-qualified)). It
integrates no provider, and the only durable command and receipt path it has is
that of a scratch daemon's lead control; it opens no network listener. Without `--demo`, `--attach` and `--daemon` the
command reports that the backend is unavailable and exits with status 1.

In a local terminal, run `make doctor`, then build with the selected offline
toolchain and start the explicit fixture:

```sh
mkdir -p build
sh scripts/go-tool.sh build -o build/ataniq-console ./cmd/ataniq-console
./build/ataniq-console --demo
```

Dark appearance is the default. Use `--theme light` or `--theme dark` to
choose one. The console draws on the alternate screen.

## Layout

A window of at least 100 columns by 30 rows shows the three-pane cockpit.
A smaller window shows single-column pages instead (see
[Small windows](#small-windows)).

The cockpit has these parts:

- The header names the demo and carries the label "fixture recording, not a
  live session".
- The left pane lists projects, leads and their delegates, with a Context block
  under it. Delegates are shown but cannot be selected.
- The centre pane has a Session block (project, lead, provider, host, session
  id, attachment, window title and the number of blocked clipboard requests),
  the session terminal, and a focus bar. The focus bar always shows the
  current focus and the return key.
- The right pane, Evidence & approvals, shows the attachment contract, the
  pending gate and the binding of the lead and its delegates. The pending gate
  is the oldest waiting sample card; Enter on it opens the card (see
  [Approvals](#approvals)). A decision on a sample card changes nothing.
- The timeline runs along the bottom and the key bar under it.

The session terminal frame carries the fixture label in every state. In zoom
the header carries it.

The console draws at most 512 columns by 256 rows. In a larger window it draws
in the top left corner and leaves the rest blank. The session terminal is at
most 500 columns by 200 rows. The pane follows a window resize after the size
has stayed the same for a short pause (120 ms). It follows a zoom at once. The
replay continues at the new size.

### Small windows

Below 100x30 the console shows single-column pages: projects, leads, a lead's
conversation, project tasks, help and the action list. Use arrows or j/k to
select, Enter to open, t for project tasks, Escape to go back, ? for help,
Ctrl+K for the action list and q or Ctrl+C to quit. In longer views the arrows
scroll. The a key explains why full attach is unavailable. These pages have no
session terminal, no zoom and no replay key.

The demo identity takes priority at height one. At height two the selected
project or lead takes the remaining line on the projects and leads pages, and
a notice takes it on the other pages. At greater heights a control notice also
has a reserved line. Narrow widths truncate each line. Display text is
limited to 4096 inspected bytes and 512 emitted bytes per field.

If you shrink the window below the minimum while the session has focus, focus
returns to the console.

## Keys

The cockpit has two focus states: console focus and session focus. The focus
bar and the key bar show which one is active.

### Console focus

| Key | Action |
| --- | --- |
| Tab, Shift+Tab | Move to the next or previous pane. |
| Up, Down, j, k | Move in the tree, or scroll the evidence pane or the timeline. |
| Enter | Select the project or lead in the tree. On the session terminal, focus the session. On the pending gate in the right pane, open the oldest waiting card. |
| ? | Show help. |
| Ctrl+K | Show the action list. |
| a | Explain why full attach is unavailable. |
| r | Replay the recording from its start, in a new pane. |
| F12 | Zoom the session terminal to the whole window, or leave zoom. |
| g | Open the approvals view (see [Approvals](#approvals)). |
| L | Open the leads view, `--daemon` only (see [Lead control](#lead-control-scratch-daemon-only)). |
| P | Open the pair-a-phone view, `--daemon` only (see [Pair a phone](#pair-a-phone-when-pairing-is-configured)); without a pairing backend it shows "pairing is not configured" and does nothing. |
| Esc | Close help or the action list. |
| q, Ctrl+C | Quit. |

In help and the action list, the arrows or j/k scroll, and Esc, Enter, ? and
Ctrl+K close them. The q and Ctrl+C keys still quit.

### Session focus

Press Enter on the session terminal to focus it. In session focus every key
goes to the session terminal, with two exceptions: the return key and F12.
That includes q and Ctrl+C. They never quit the console from here. Paste goes
to the session. When the session asked for mouse reporting, the mouse inside
the session terminal goes to the session, as the session asked, and the
console makes no selection. Otherwise the mouse selects text in the session
terminal (see [Selecting and copying](#selecting-and-copying)). The console
asks the outer terminal for focus events only in session focus, and only when
the session asked for them. In this fixture console the session's input sink
discards all of it. With `--attach` the input goes to the holder instead (see
[Keys and what is held back](#keys-and-what-is-held-back)).

The return key leaves session focus. The default is Ctrl+\. The console reads
it from both of its encodings: the byte 0x1c and the kitty keyboard form
`CSI 92;5u`. Without the kitty keyboard protocol, a terminal sends the same
byte 0x1c for Ctrl+4. Then Ctrl+4 also returns. In the kitty form Ctrl+4 is
`CSI 52;5u` and does not return. Ctrl+Enter is never bound.

F12 toggles zoom in either focus state. In zoom the session terminal fills the
window under a one-line header. The header names the focus and the key that
leaves zoom, F12. Tab and the move keys do nothing while zoomed. Enter focuses
the session, and `r` replays the recording. Zoom draws no help or action list.
A notice, such as a refused paste or a selection that was not copied, comes
first in the header. The header always keeps the fixture label, and keeps the
keys when the line has room for them too.

### Selecting and copying

While the cockpit is shown, in console focus or session focus, zoomed or not,
the console asks the outer terminal for mouse reporting (cell motion, or all
motion when the session has the focus and asked for all motion). The outer
terminal's own selection then no longer spans the window, and a selection is
confined to the session terminal. The single-column pages below 100x30 do not
ask for the mouse, so the terminal's own selection works there.

Reporting takes the mouse from the outer terminal for more than selection.
Its middle-click paste, clicking links, context menu, wheel scrolling of its
own scrollback, and a multiplexer's mouse handling are unavailable in the
cockpit unless you hold Shift, in terminals that allow that. Below 100x30
they work as usual.

Unless the session has the focus and asked for the mouse, drag with the left
button in the session terminal to select text. The selection runs like a
terminal's: from the cell where you pressed to the cell where you are, across
rows, whole rows in between. It stays inside the session terminal when the
pointer leaves it. When you release the button, the console copies the text
to the outer terminal's clipboard with OSC 52, and the selection stays
highlighted. The text is the pane's screen: a wide character or a grapheme
cluster is copied once, blanks at the end of each row are dropped, and rows
are joined with a newline. The emulator keeps no record of where a long line
wrapped, so a wrapped line is copied as two lines. The text is what the pane
holds, also where it draws a placeholder (see
[Width of emoji, and SSH](#width-of-emoji-and-ssh)): a cell of more than 64
bytes, and a cluster in a terminal without mode 2027, are copied whole, not as
the placeholder. A selection of more than 1 MiB of text is not copied at all,
and the key bar, or the header in zoom, says so. In console focus any console
key clears that notice, and so does a press. In session focus only a press
does, because keys go to the session. A terminal that does not accept OSC 52
writes, or has them turned off, copies nothing, and the console cannot tell.

A press outside the session terminal starts no selection, a click without a
drag copies nothing, a selection of blanks alone copies nothing and leaves the
clipboard as it was, and the wheel does nothing. A press anywhere clears the
selection. So do a key or a paste sent to the session, a window resize (and
the session terminal following it a moment later), F12, `r`, and output that
changes the selected text, such as the session writing over it, scrolling or
clearing the screen. Output elsewhere on the screen keeps it. That output
rule holds during a drag too, while the pointer stays still: the drag ends
and its release copies nothing, so a release the console never receives
leaves no highlight over changing output. While help or the action list is
open, a drag selects nothing, and the release of a drag begun before it
opened ends the selection and copies nothing.

To select elsewhere in the window, most terminals, Ghostty among them, let you
hold Shift while you drag to use the terminal's own selection even when the
mouse is reported. Not every terminal does.

### The action list

Ctrl+K lists the controls the cockpit knows of. With `--demo`, Approve pending
gate and Deny pending gate are listed with the key g, which opens the
approvals view from the list; a decision is made there, on the card (see
[Approvals](#approvals)). Every other control is unavailable, and the list
gives its reason. In the daemon view and with `--attach` every control is
unavailable. The fixture lists these, the first two as available and the rest
with the reason shown:

| Control | Key | Reason |
| --- | --- | --- |
| Approve pending gate | g | Available: g opens the approvals view; a decision is made there, on a sample card, and changes nothing. |
| Deny pending gate | g | Available: g opens the approvals view; a decision is made there, on a sample card, and changes nothing. |
| Full attach | a | Full attach needs a qualified holder and an exclusive input owner. Unqualified: the console has no live backend. |
| Send to lead | Ctrl+K | Sending needs a qualified command and receipt path. Unqualified: the console has no live backend. |
| Interrupt lead | Ctrl+K | Interrupting needs a qualified command and receipt path. Unqualified: the console has no live backend. |
| Detach | Ctrl+K | Detach and recovery are not qualified. |

The `s` and `i` keys of the earlier fixture viewer no longer exist. There is
no search key.

## Approvals

The approvals view is where the owner decides a card. A card is one request
that waits for his approval or denial. This section says what the view does;
[What is a sample](#what-is-a-sample-and-what-is-not-qualified) says what is
not real.

### Opening the view

`g` opens the approvals view from console focus. It does nothing in session
focus, where every key goes to the session. Enter on the pending gate in the
right pane opens the oldest waiting card. The pending gate, the timeline
(asked, decided, applied) and the status line (waiting cards and the age of the
oldest) follow the cards. The status line's approvals element is also shown in
session focus, shortened to its count, or left out, when it would hide the
return hint.

The list shows the waiting cards, the recent decisions and the standing rules.
Up, Down, j and k move, Enter opens a card, `L` opens the limit's screen for
the project under the cursor and Esc closes the view. Below 80 columns by 24
rows a card is not offered for a decision and says so. While a field is open
every key goes to the field, and Esc goes back one step at a time: field, card,
list, cockpit.

### The card

The card shows its plan, diff summary, flagged paths and evidence. Text a lead
wrote is drawn apart, under the label "unverified lead data". The keys on the
card are:

| Key | Action |
| --- | --- |
| Up, Down, j, k, PgUp, PgDn | Scroll. |
| Space | Sign off the flagged path under the cursor. |
| d | Open the full patch in a pager. |
| ] and [ | Read the next or previous page of a long path list. |
| l | Open the lead. |
| a | Approve: open the confirm step. |
| x | Deny: open a field for an optional reason of at most 500 bytes. Enter sends the denial; no code is asked. |
| Esc | Go back one step. |

### The confirm step

Approve opens a field that asks for a code of four characters. The code is
drawn only in the card's title and is made again each time a card opens. An
approval is made only by typing the code and pressing Enter; a denial needs no
code. Approve does not open the field, and says why in one line, among
other cases, when:

- the card is not ready: the paths, the lists (or the refs) and the "differs
  from" block, where there is one, were not all read, a flagged path is not
  signed off, the card ended or expired, or it opened less than 400 ms ago;
- the window is below 80 columns by 24 rows (Approve and Deny are both not
  offered, and the card says why).

Once the field is open, it refuses and clears itself when:

- the input is a paste, a burst of keys less than 30 ms apart, or anything but
  one printable key press;
- the code is wrong, and then a new code is made.

A decision made while the owner is away is not accepted: the card says so and
shows a new code (see [Presence](#presence)).

The decision is sent once, the card shows "sending", and then the outcome.

After the decision the card shows the advice of the two verdict runs. The
list's header says how often the runs agreed with the owner's past decisions.
Before the decision the card shows nothing of them, except one header line on
a card that exists because a run refused. The card is read again after a
decision, so the advice shows at once. The verdict
is advice of this program and is in no record.

### The limit's screen

`L` in the list opens the daily card limit of the project under the cursor.
The limit is how many approved cards a project may have in one day. An approval
past it is recorded and held, and its card comes back as waiting. The screen
shows the card uses of the 14 days before today, the days left out of the
suggestion, the present number and the suggestion. Its rules are:

- The limit starts at 12, and is never under 1 or above 40.
- Lowering takes one key: Enter with nothing typed accepts the suggestion when
  it is no higher than the present number. A very low number asks once more.
- Raising is typed digits and then Enter, by half at most (12 rises to 18),
  once a day. No single key raises the number.
- The suggestion is twice the busiest of the 14 days, never under 6 and never
  above 40. Days that reached the limit, or had an alert, are left out.

Esc closes the screen. A refusal shows its one-line reason.

### Arrival and the bell

When you return after an away stretch, the console shows a short summary above
the cockpit: how many cards wait and the age of the oldest, and the cards that
were decided past the limit and wait again. It is shown once per
arrival, it never takes the key that woke the console, and it is not drawn in
session focus. Esc closes it, and a digit opens one of its lines. Opening a
line decides nothing.

The console rings the terminal's bell for a card that arrives while you are
away. `--no-bell` turns the bell off; it works with `--demo` and with `--daemon`.

### Presence

The console treats the owner as present while it has seen a key or a pointer
action within ten minutes. The first action after that is an arrival. A
decision is accepted only while the owner is present. The console tells the
backend of activity at most once a minute. A process of the same user can
imitate activity: presence is a convenience and a record, not a fence.

## The daemon view

`ataniq-console --daemon DIR`, where DIR is the folder that holds the daemon's
socket, shows the projects with their lead and coordinators and each one's
state, and asks the daemon again every few seconds. The view is read-only:
every control is unavailable with its reason, and it has no session terminal.
The leads view of a daemon with lead control (`L`) and the approvals of a daemon
with the owner channel are the two places that act, each on its own screen. With
no daemon, a slow one or one that speaks another protocol, it says so with the
next step and keeps asking. See [the lead daemon guide](lead-daemon.md).

## The owner channel (tagged daemon only)

The console client is the same in every build: it is not behind a build tag. With `--daemon DIR` it asks the daemon for `hello`
and shows the daemon's approvals only when the answer lists the owner
operations. A daemon from a release build lists none, so the approvals view
says "no owner channel in this build" and nothing can be decided. A release
daemon contains no owner channel; the release console contains the client
side of it, which does nothing until a daemon's hello lists every owner
operation. The client asks again when the owner leaves the approvals view
(Esc) and opens it again, so a daemon restarted with `--owner-channel` is
found without restarting the console; between those actions it remembers the
answer and does not dial for each reading.

A daemon built with the tag `ataniq_ownerchannel` and started with
`ataniqd run --owner-channel` lists them. Then the approvals view shows that
daemon's cards instead of sample cards: the list, a card, its paths and patch in
pages, the limit, and a decision, which the console sends once and shows the
outcome it is told. A lost connection drops what was typed and decides
nothing. Only one console at a time gets the place that may decide, and it
needs a controlling terminal. The daemon's section,
[The owner channel](lead-daemon.md#the-owner-channel-tagged-build-only), says what
the daemon does, how it is started and which of its settings are scratch
settings. That daemon's door is a stand-in: it prints
`owner channel: STAND-IN door, scratch state: no real approval` at start, and
an approval on it moves a ref in a scratch mirror and nothing real. The
console makes none of that real.

None of this is qualified. For each case, whether the end-to-end test ran it on
the stand-in, ran only part of it, or whether it needs a seated lead, see the
daemon's section. The console's own part of the cases (the typed text dropped when a
card ends, hostile text, terminal answers at the confirm field) is covered by the
console's tests; the end-to-end test does not run the typed-text drop. The
confirm code appears in no place: the console draws it and keeps it in its
model. If a request is interrupted, the daemon answers it request-invalid and the
lead must ask again.

## Lead control (scratch daemon only)

With `--daemon DIR` the console asks the daemon for `hello` when it starts and
offers the leads view only when the answer lists all
six operations of the lead control plane (`lead.list`, `lead.define`,
`lead.start`, `lead.stop`, `lead.ticket` and `lead.inject`). Only a daemon built
with the canary tag and started with `ataniqd run --scratch CONFIG` lists them
(see [the scratch leads guide](scratch-leads.md)); the console client is in every
build and behind no tag. **When the daemon lists none, the console has no lead
control.** `L` then opens a screen that says "no lead control in this daemon",
and Esc closes it. A daemon that lists only some of the six is treated the same
way. The console asks once, when it starts, so start it again after the daemon
was started with `--scratch`.

### Opening the view and what it shows

`L` (capital L) opens the leads view from console focus. It does nothing in
session focus, where every key goes to the session, and in the approvals list
`L` is still that view's own key (the limit's screen). Below the cockpit's size
the single-column pages take it too, but not over a live session.

The view lists the daemon's leads, one row each, with the daemon's `max_running`
and how many leads count against it:

| Column | Meaning |
| --- | --- |
| KEY | The lead's key in the daemon's config. |
| STATE | The registry's state of the lead, with `*` after it while a command for the lead has no answer yet. |
| EPOCH | The current epoch, `-` with none. |
| MSG | `off` when the lead's config turns messages off, `-` for a lead that is not running (its state is stopped, ended or desired, or it is `unknown` with the reason `launch-unsettled` or `launch-reply-lost`, or it has no epoch: the words of its last epoch's message are not drawn; a lead that is `unknown` for another reason, such as `control-group-not-proven-empty` or `competing-unit`, may have a unit that runs with the epoch's message sent, so its column follows the rule below), `free` while the epoch's message is not spent, and `free (not delivered)` while it is free after a message that was not delivered (the holder answered not-now or refused with nothing written, or refused the request itself as disabled, wrong-epoch or bad-request; the receipt line says why): another can be sent with `m`. Once the message is spent, what became of it: `sent` only when its words say it was submitted; `sent-unsure` when something may have reached the lead (look at its screen), when the words are not ones the console knows, and when the daemon holds the epoch spent and has no words for it (the holder answered busy, which says it is delivering something the daemon has no record of; the answer was lost, the message was sent before the console started or by another client, or the daemon was restarted since and a later command hides the receipt): the request was written and nothing says what became of the message; and `spent, not delivered` for a message that was not delivered but that the daemon holds as spent anyway, because it could not write the result, or the holder's refusal, down (then `m` says a message was already sent in this epoch). |
| LAST RECEIPT | The lead's last command and where it stands. |
| UNIT | The lead's unit name. |

Below the table the view draws one line about the row under the cursor when its
state needs words. For state `ready` with the reason `holder-ready` it reads
"KEY: ready (holder-ready): the holder is up and the agent started; its login is
not observed", which says what the daemon observed and no more: the agent's login
is not looked at. The line gives way to the message editor, the stop question and
a note, and a state or reason the console has no words for adds no line. A lead
that is `unknown` with the reason `launch-unsettled` or `launch-reply-lost` (a
start whose launch the daemon cannot yet say anything about, as after the daemon
was killed in the middle of a start) gets "launch settles by itself 40 s after
the start; if not, stop the lead or restart": the daemon in scratch mode settles
the launch itself when the 40 s window ends, and if the lead is still `unknown`
after that, stopping the lead (the way out when the launch's own unit was
adopted, which leaves the lead `unknown`) or a restart of the daemon is what is
left. The same words answer a `lead.stop` that is refused with `intent-pending`
(and a `lead.start` with that code): reading the list does not settle a launch,
so the line does not tell the owner to read it. While such a lead is listed (or one
that is `unknown` with the reason `control-group-not-proven-empty`, which the
daemon's timer tries again), a start refused because the limit of running leads
is reached adds that the unknown lead holds a slot until its launch settles by
itself, 40 s after the start, and that if it is still unknown after that the
lead is stopped or the daemon restarted. When a listed lead is `unknown` for any
other reason (a unit that is not the lead's own, a registry that was recovered,
a reboot, or none given), the daemon has no timer for it and the refusal adds
only that the unknown lead holds a slot until it is settled and that the lead is
stopped or the daemon restarted. The view does not read the list by a timer for an `unknown`
lead, so the settled lead shows at the next reading (`r`).
See [a lead left unknown after a killed
daemon](scratch-leads.md#a-lead-left-unknown-after-a-killed-daemon).

Everything in the view that comes from the daemon (names, states, units) is cut
to a short ASCII subset and drawn one character at a time, and the text of an
error is never drawn.

### Keys

| Key | Action |
| --- | --- |
| j, k, Up, Down | Move over the rows. |
| d | Define the lead under the cursor: the daemon provisions it and registers it. |
| s | Start it. |
| x | Ask "Stop KEY (epoch N)?"; only y stops it, any other key keeps it running. |
| m | Open a one-line editor for a message to the lead, for the epoch the row shows. Enter sends it once, Esc drops it, Ctrl+U clears it, and every printable key, q and j included, is text. It holds at most 2048 bytes, the daemon's limit, and stops taking text there. |
| w, v | Ask for an attach ticket as a writer or as a viewer (below). |
| r | Read the list again now. The view also reads it every two seconds by itself while a receipt is in progress and while a lead is `starting` or `stopping`, so a started lead turns `ready` without this key, and stops reading when none is. A lead that is `unknown` is not read by a timer: press this key to see a launch that the daemon has settled since. |
| Esc | Close the view. |
| Ctrl+C | Quit, dropping an open field first. |

While a command for a lead has no answer yet, the keys that would send another
for that lead do nothing and say so. `m` says why it does nothing when the lead
has no epoch, when messages are off, when the epoch's message was sent, and when
an earlier message of the epoch has no final answer.

### Receipts

Every command is shown with the daemon's receipt for it. The words are the
view's own, chosen by the receipt's status, and are not the daemon's text:

| Shown | Status | Meaning |
| --- | --- | --- |
| accepted (running) | in-progress | The daemon admitted the command and it has not ended. The list is read again every two seconds until it is final. |
| accepted (done) | done | The command was carried out. For a message the line says what became of it, from the holder's own three words (outcome, reason, written), and whether the epoch's message is spent (next table). |
| refused | refused | The daemon changed nothing that matters; the line gives the reason in the view's words. |
| unavailable | unavailable | The command could not be carried out; the line gives the reason. |
| uncertain | uncertain | The daemon does not know what happened, as after a restart while the command ran. Read the lead's state, then decide; nothing is sent again for you. |

The line of a done message, for every answer the lead's holder can give (each
of the NOT delivered ones ends "The epoch's message is free" when the list's row
for the lead and epoch, read after the answer, says the message is not spent,
and each other one ends "This epoch's one message is spent"). The receipt does
not carry that flag, so the NOT delivered ones are drawn from the row: until the
list has been read again they end "The epoch's message is free unless the list
says it is spent", and when the row says spent (the daemon could not write the
result down and holds the epoch spent all the same) they end "The daemon holds
this epoch's message spent all the same", as the MSG column says `spent, not
delivered`. When the row is at another epoch than the answer's (the lead was
stopped and started since, perhaps by another client), the row's flag is not that
message's, and they end "The lead is at another epoch now; that epoch's message
is not known" and say nothing of free or spent. The daemon's list summary carries
no epoch, so the status line keeps the epoch the answer was for when it keeps the
answer's words:

| Holder says (outcome, reason, written) | The line says |
| --- | --- |
| submitted, none, whole | the message was submitted: the whole of it was written and sent |
| not-now, not-ready, nothing | NOT delivered: nothing was written, the lead's screen is not idle |
| not-now, blocked, nothing | NOT delivered: nothing was written, the lead's screen is blocked |
| refused, untested, nothing | NOT delivered: nothing was written, the lead's program version is not one the holder is tested with |
| refused, text, nothing | NOT delivered: nothing was written, the holder refused the text |
| uncertain, not-submitted, partial | written but not submitted: the text reached the lead's input and Enter was not sent. Look at the lead's screen |
| uncertain, died, nothing / partial / whole | result unknown: the lead ended during the send, with what the holder says was written (nothing, part, or the whole). Look at the lead's screen |
| uncertain, died, unknown | result unknown: the lead ended or no answer came back; how much was written is not known. Look at the lead's screen |

The last sentence of a line is what the daemon does: it marks the epoch's message spent
once it has written the request to the holder, and frees it again when nothing
was written to the lead: the holder answered not-now or refused with nothing
written, or refused the request itself (`holder-disabled`, `holder-wrong-epoch`
or `holder-bad-request`). A `holder-busy` refusal does not free it: the daemon
sends one message at a time, so a busy holder is delivering something the
daemon has no record of, which may have reached the lead. So after a message
that was NOT delivered the lead may be sent another in the same epoch, and after
any other answer the epoch's message is spent until a new epoch. The list that the view
reads after the answer settles it: the MSG column follows the daemon's
`message_spent`, and `m` is refused with "a message was already sent in this
epoch" only while that is true. A refusal by the holder itself is a different
receipt: it says "unavailable" (or "refused" for `holder-wrong-epoch`, which the
daemon answers as a stale epoch), not "accepted", and the row then shows the
message as free and not delivered; after `holder-busy` the receipt says the
holder is busy with a delivery the daemon has no record of and the epoch's
message is spent, and the row shows it as `sent-unsure` (the message that was
refused was not written, and what the holder was delivering is not known to have
reached the lead nor known not to have: look at the lead's screen). A done message from a console adapter that does not pass the
holder's reason and written is drawn from its outcome alone, with the same first
words. The line stays the same when the list is read again: the daemon's list
holds the last command of each lead as a summary without its result, so the
view keeps the words of the answer to the press (the holder's three words of a
message) while the list shows the same command, and takes a list receipt's own
words when it has any.

"Accepted" means that the daemon took the command into its durable table, not
that it is done. The view has words for every reason code the daemon sends.
Only a code from a newer daemon, one that is not in that set, is drawn as "the
reason is not known to this console"; a message result it does not know, or a
reason and written that the holder does not send together, is drawn as "the
result is not known to this console", with no sentence about the epoch's
message.

**Earlier receipts.** After a lost answer the console's client asks once more,
on a new connection, with the same command id and the same arguments, and the
daemon answers with the receipt it holds: it never runs the command again. A
receipt that comes back for a command of an earlier press (the answer was lost,
or it had none that was final) is not shown as the answer to the press that
returned it, because "done" would then read as something just done, and an old
ticket may be spent. The view says instead "no answer came back: the command may
or may not have run. The list was read again; nothing was sent a second time"
(for a message: "no answer came back: the message may or may not have reached the
lead. Read the list; it shows whether the epoch's message was sent"), and reads
the list, which shows the lead's last command. The client forgets the id with the
final receipt, so the next press is a new command. A message whose answer was
lost is not made again for the epoch.

### Attach: list, attach, back

`w` and `v` send a ticket command. When its receipt is done, the leads view asks
the console to attach to that lead, and the command line does what `--attach`
does for the lead's folder and ticket file, as a writer or as a viewer: the
cockpit ends, the attach screen of [Attaching to a holder](#attaching-to-a-holder)
runs, and when it ends the cockpit opens again with the leads view open and the
list read afresh. So the owner goes list, attach, back. `--daemon` still cannot
be combined with `--attach`: the loop is internal. A ticket is single use and the
lead has one ticket file, so ask for a ticket and attach before asking for
another for the same lead. A refused ticket reads "the holder refused the ticket;
a writer may already be attached": the holder grants one writer at a time, and
the receipt carries no reason beyond the refusal, so the line says what is likely
and no more. An attach that cannot be made (for example, the
attach socket's path is too long for a unix socket address) is told in one line
and the cockpit comes back.

## Pair a phone (when pairing is configured)

The pair-a-phone view adds a phone as a device that can approve cards, from the
desk. It exists only when the console is given a pairing backend and the
approval app's address (`terminalconsole.Options.WithPairing`). `ataniq-console
--daemon DIR` gives both: the backend is the daemon's owner channel, over the
same owner connection as the approvals view (the daemon counts one connection as
the decider, so the pairing must not open a second one), and the address is the
owner-local file described [below](#the-approval-apps-address). The other modes
(`--demo`, `--attach`) give neither: `P` shows "pairing is not configured" and
does nothing, and the help page does not list it. Against a daemon with no owner
channel the view opens and its calls fail with the fixed lines below.

`P` (capital P) opens the view from console focus, like `L`: it does nothing in
session focus, in zoom, over an overlay or over another screen, and the
single-column pages take it too, but not over a live session. Every key goes to
the view until Esc closes it, Ctrl+C aside, which quits.

1. The view first reads the daemon's state. With nothing open it explains
   pairing in a few lines and offers Enter to start. A pairing that is already
   waiting for its phone cannot show its code a second time, so the view says so
   and offers Enter for a new code, which ends the open one. A pairing whose
   eight words are waiting for you is shown at once.
2. Enter asks the daemon for a one-time code and draws a QR code that holds the
   app's address with `/pair/#` and the code (the code is in the fragment, so
   it is never in a proxy log), with the seconds the code has left. The code is
   drawn black on white, with a white margin, whatever the theme is, because a
   light code on a dark ground is the inverse code that many scanners do not
   read. It needs the code's columns and its rows plus two lines of text, with
   a margin of 4 modules; when that does not fit, the view uses the smallest
   margin that scanners read (2 modules), and only a terminal too small even
   for that gets the line "terminal too small: need W x H" and never a clipped
   code. The code stays valid meanwhile. An address so long that its code
   would not fit the largest QR the console draws also gives the line "pairing
   origin not configured", not an error of the daemon's. The code appears only in the
   QR: never as text, in a status line, an error or the title.
3. The phone scans the code and makes its passkey. The view asks the daemon
   every two seconds; when the phone's key arrives the view shows eight numbered
   words, large and spaced (four to a row, or two when the width does not hold
   four, or one), and the line "Compare with your phone. Confirm only
   if they match." The phone shows the same eight words (64 bits of the key's
   fingerprint). The daemon names the
   pending key with an id (16 lowercase hex characters) that the view keeps but
   does not show. When the daemon has what the phone's authenticator said of
   itself when it made the key, the view shows it under the words, as a claim:
   "Claimed, not proven: vault passkey, synced." The kind is "platform
   passkey", "vault passkey" or "passkey of an unknown kind" (any other word
   from the daemon reads "a kind this console does not know"), and the backup
   is "synced", "can sync, not synced yet" or "kept on one device". Nothing
   about the authenticator is attested, so this is what the phone says, not a
   fact: the words are what you compare.
4. `c` asks "press y to confirm, any other key cancels", because a confirmation
   adds a deciding key; only `y`, a moment after the `c`, confirms. The
   confirmation and a rejection carry the id the words were shown for, so the
   daemon acts on that key and on no other. The question is dropped if the
   words, the id or the claim change meanwhile, or if the terminal is resized. You can
   confirm only while the whole words screen, the eight words and the prompt,
   fits the terminal with no line cut: on a smaller one `c` does nothing and
   the view says "terminal too small to show the eight words" with "x: reject
   Esc: close", and `y` checks again. An 80 x 24 terminal holds it for
   words of ordinary length, and so does one as small as 30 x 19 (the words
   two to a row); a smaller one may not, and long words need more
   columns. `x` rejects, and nothing is added.
   After a confirm the view shows the key's kind, "Paired. Kind, claimed: vault
   passkey." (or "Paired. Kind: unverified (a syncing passkey)." for a key
   paired without a claim), and the key's short id. Nothing is trusted until you confirm here.
5. Esc closes the view at any time and confirms nothing. Closing while the code
   waits does not cancel the pairing on the daemon: the code ends by itself when
   it expires. A code that expired, or a pairing the daemon no longer holds, says
   so and offers Enter for a new code.

### The approval app's address

With `--daemon` the address is the first line of the owner-local file
`ataniq/owner-local/seat-origin` in the folder named by `XDG_CONFIG_HOME` (when
it is an absolute path), or else in `~/.config`, trimmed of space around it. The
console reads it once, when it starts; change it and start the console again.
The file is read only if it is a regular file (not a link), at most 1 KiB, and
not writable by its group or by others (`chmod 600` it). A file that is missing,
empty, a link, too big or writable by others is the same as no address: the view
says "pairing origin not configured" and asks the daemon for nothing. The
console does not say which of these it was, and the value is never in a message
or a log. It is a file of your account: a program that runs as you can write it,
so it names the address the QR holds and nothing more: the passkey is bound to
the address it was made for, and the words you compare at the desk are the
fingerprint of the phone's key.

The address is configuration and is checked before anything is
drawn: it must be `https` with a host name (an IP address, `localhost` and a name
with no dot are refused), no port but 443, no path, no user name, no query and
no fragment. Otherwise the view says only "pairing origin not configured", draws
no QR and asks the daemon for nothing.

### What the daemon's answers can show

Everything the daemon sends that the view draws (the eight words, the id, the
kind) is cleaned first, and eight words that are not each one short printable
word can be rejected but not confirmed. A status that says a key is pending
with any other number of words, or with no id, is refused before it reaches the
view, as a bad answer. The text of a daemon's error is never drawn: a
fixed line says that the code was not made, that the state could not be read
(the view tries again), or that a confirmation or rejection did not complete.
See `internal/terminalconsole/pairview`.

## What is a sample and what is not qualified

- No approval is possible with a release daemon: a release daemon contains no
  owner channel, and without `--demo`, or with a daemon that lists no owner
  operation, the approvals view says "no owner channel in this build". The
  console's client side of the owner channel is in every build.
- The sample cards of `--demo` change nothing: the header says "sample cards: a
  decision changes nothing".
- The daemon view is read-only, except for the approvals of a daemon that
  serves the owner channel (see [The owner channel](#the-owner-channel-tagged-daemon-only))
  and the leads view of a daemon that serves lead control, which is a scratch
  daemon of a tagged build: it drives scratch leads on throwaway repositories
  and nothing else (see [Lead control](#lead-control-scratch-daemon-only)).
  With any other daemon `L` says "no lead control in this daemon".
- Nothing here has been qualified on a real lead.

## Flags

```
ataniq-console --demo [--theme dark|light] [--return-key ctrl+KEY] [--recording NAME] [--linger DURATION] [--no-bell]
ataniq-console --attach DIR --ticket-file FILE [--viewer] [--shift-enter esc-cr|lf|csi-u|backslash-cr] [--theme dark|light] [--return-key ctrl+KEY]
ataniq-console --daemon DIR [--theme dark|light] [--no-bell]
```

- `--demo` starts the cockpit with sample cards that change nothing. Without it, `--attach` or `--daemon` the
  command reports that the backend is unavailable.
- `--attach DIR` shows a live scratch session instead. DIR is the holder's
  runtime folder, the one that holds `attach.sock`. It needs `--ticket-file`,
  and `--demo`, `--recording` and `--linger` cannot be used with it. See
  [Attaching to a holder](#attaching-to-a-holder).
- `--ticket-file FILE` names a file, mode 0600, from which the console reads a
  single-use attach ticket each time it needs one. `ataniq-scratch ticket NAME`
  writes it.
- `--viewer` asks for the read-only place. The ticket decides which place the
  holder grants: see [Writer and viewer](#writer-and-viewer).
- `--shift-enter esc-cr|lf|csi-u|backslash-cr` chooses what Shift+Enter sends
  to the session. The default is `esc-cr`, which is what Alt+Enter sends.
  Agents differ in the key they take for a new line, and a terminal without an
  enhanced keyboard protocol cannot tell Shift+Enter from Enter, so the choice
  is yours: `lf` sends a line feed (Ctrl+J), `csi-u` sends the enhanced form
  `CSI 13;2u`, which only an agent that reads it understands, and
  `backslash-cr` sends a backslash and Enter, the line continuation.
- `--daemon DIR` shows the daemon's leads, read-only, its approvals when
  the daemon serves the owner channel, and the leads view (`L`) when the daemon
  serves lead control. DIR is the folder that
  holds the daemon's socket. It cannot be combined with `--demo`, `--attach`,
  `--recording`, `--linger`, `--return-key`, `--ticket-file`, `--viewer` or
  `--shift-enter`. See [The daemon view](#the-daemon-view).
- `--no-bell` does not ring the terminal's bell when a card arrives while
  you are away. It needs `--demo` or `--daemon`.
- `--theme dark|light` chooses the appearance. The default is dark.
- `--return-key ctrl+KEY` sets the return key. The default is `ctrl+\`. The
  value is `ctrl+` and one letter, `ctrl+\` or `ctrl+]`. The console refuses
  ctrl+c, ctrl+h, ctrl+i, ctrl+j, ctrl+k, ctrl+m and ctrl+z, which the
  terminal or the console already uses.
- `--recording NAME` picks the fixture recording to replay, by its full file
  name, such as `title.jsonl`. The default is `title.jsonl`. Run
  `ataniq-console --help` to see the names.
- `--linger DURATION` keeps the session open after the recording's end, so
  that you can try the focus and zoom keys. Use a Go duration such as `30s`. The
  default is 30s, the largest value is 1h, and 0 ends the session as recorded.

`--viewer` and `--shift-enter` without `--attach` are a usage error. A bad flag
value prints a diagnostic and the usage and exits with status 2. The
diagnostic never repeats the text of an argument. The path of the socket, DIR
and `/attach.sock`, must fit a unix socket address, which holds at most 107
bytes: a longer one is refused with its length.

## Attaching to a holder

`--attach` is provisional and unqualified, and it is for a scratch agent only:
an agent with no project, no lead role and a disposable home, such as
`ataniq-scratch` starts. Do not attach a real lead. Nothing about it has been
checked by hand in a real terminal, and no real agent has been run through it.
It has been tested against a fake agent, in process.

With `--attach` the console shows the screen that a holder keeps for a session
and sends your keys to it. A holder is the process that owns the session's
terminal and keeps it running whether or not a console is attached. The console
connects to the holder's `attach.sock` through the attach client, which is the
only part of the console that opens a socket: it makes one connection to a unix
socket and listens on nothing. The client names itself `ataniq-console` to the
holder. This mode is not managed by the daemon and has no project data: the
left pane, the Context block and the timeline say so, and every control is
unavailable. In this mode the console does not connect to `ataniqd`, the read-only
daemon skeleton of [the lead daemon guide](lead-daemon.md).
The header and the session frame carry a label in place of the fixture's, such
as `live scratch session · lead NAME · writer · epoch 7`, and the Session block
and the evidence pane show the role, the holder's runtime and version, the
holder's screen size, the connection, the holder's counters and the notices.

The connection shows as one of: connecting, attached, resyncing, waiting for a
ticket, or disconnected, with the reason when there is one. Over the session
pane a banner says WAITING FOR A TICKET, DISCONNECTED or CONNECTING, and
`SESSION ENDED` when the session is over; the last screen stays, dimmed, under
a banner. The end banner gives the three facts as the holder reports them, as
separate lines: whether the terminal stream ended, whether the TUI exited (and
with what status) and whether the agent's process tree is gone. The stream can
end while the process tree lives on, so none implies another. Nothing is
replayed, and `r` does not restart a live session. In session focus any key
then returns to console focus, and `q` leaves the console.

### Writer and viewer

A holder has at most one writer and one read-only viewer. A third client is
refused. The ticket decides the place: a writer ticket is issued only while
there is no writer and no other ticket for that place is waiting to be used,
and there is no takeover. A client that holds the writer place keeps it until
it detaches or the holder's operator removes it with
`ataniq-scratch release-writer NAME`; a console cannot take it from another.

`--viewer` says the console wants the read-only place; it does not choose it.
If the ticket is for the writer, the holder grants the writer place anyway, the
console shows a notice that it asked for a viewer and was granted the writer,
and, because it asked to read only, it sends no input and its keys and paste
are dropped and counted. If the ticket is for the viewer and the console was
not given `--viewer`, the console is a viewer all the same, with the same kind
of notice, and says so in the focus bar: keys typed in session focus are not
sent, and the reason is given. A viewer shows the screen as the holder keeps
it, at the holder's size, cut or padded at the top left corner of the session
pane. It sends no key, no paste and no size. The holder would refuse them and
count them as refused input if it got them.

Only the writer sets the size of the session's screen. When the writer's pane
changes size, after the same pause as for the fixture, or at once for zoom, the
console asks the holder for that size, and the holder decides: it clamps it to
its limits, 20 by 5 at least and 500 by 200 at most.

### Keys and what is held back

As the writer, in session focus every key goes to the session, as in the
fixture console: `q` and Ctrl+C included, except the return key and F12. The
console encodes each key in the legacy terminal form, never an enhanced form
(except Shift+Enter with `--shift-enter csi-u`), following the application
cursor, keypad and Enter modes that the session set. A paste goes to the
holder in one piece. The holder takes out of it the held keys named below and
any paste-end sequence, and wraps it for bracketed paste if the session turned
that on. The console asks the outer terminal to bracket a
paste only when the session did. A paste longer than 64 KiB is refused with a
notice, because it would arrive as several pastes. The mouse reaches the session
only when it asked for it; otherwise it selects text in the console as in the
fixture. Focus events are not forwarded.

The holder holds some keys back, and the console shows a notice when it does:

- Ctrl+Z is never forwarded. A suspended agent has no one to resume it.
- While the session's terminal has signals on, which a shell-like or cooked
  mode program has and a full-screen program in raw mode does not, the
  holder also holds back the bytes the terminal turns into signals: the
  interrupt, quit and suspend keys, usually Ctrl+C, Ctrl+\ and Ctrl+Z. Ctrl+C
  therefore does not interrupt such a program from here. In raw mode, where
  most agents run, Ctrl+C passes through.

The return key, Ctrl+\ by default, is the console's and never reaches the
session in session focus.

A client gets the visible screen only: there is no scrollback, and a console
that attaches late shows the screen as it is, not what scrolled away. What you
select and copy is that screen. The session's own requests to the clipboard
(OSC 52) are dropped and counted, as in the fixture.

### Tickets

An attach ticket is a random string of 64 hexadecimal characters, issued for
one place and one epoch (one run of the holder), single use and valid for 30
seconds. The console looks at the ticket file when it starts and then every
half second, and shows "waiting for a ticket" until there is one. The look is
one check of the file's status: it does not open the file, read it or empty it.
While the file is absent or empty the console does not connect, so a waiting
console does not touch the holder: the holder sees nothing of it, counts and
logs no refused connection, and the console cannot tell whether the holder is
running.

The file must be a regular file, not a link, owned by you and with mode exactly
0600. Otherwise the console refuses it, and the status says why, naming no
path: for example "waiting for a ticket: ticket file refused: its mode is not
0600". A refused file is not read and not emptied, and the console does not
connect. When the file is right the console connects first and reads the
ticket only then, so a connection that fails does not spend a ticket: the file
keeps it for the next connection. The console empties the file as it reads it,
whatever it holds: a ticket is spent by being read, and a file with something
else in it does not stop the console. The writer of the file replaces it (a new
0600 file renamed over the old), so a reader that still holds the old file
empties only that one. A second console that reads at the same moment finds the
file locked and sees no ticket; the console that found no ticket after it
connected closes the connection and waits again.

A ticket the holder refuses (used, expired, for another epoch or place) or a
place that is taken sends the console back to waiting for a ticket, with the
holder's reason. Writing a fresh ticket to the file attaches it. A connection
that is lost does the same. A ticket the console has read is spent, because
the file is emptied as it is read; it is not read again. When a connection
cannot be made, for example because the holder is not running or DIR is wrong,
the console shows disconnected with the reason and tries again every half
second, and the ticket stays in the file. When the console says it is waiting
for a ticket after it had read one, write a fresh one. A holder that speaks
another protocol version stops the console: it shows disconnected and does not
retry.

The holder refuses to issue a ticket for a place while another ticket for the
same place is waiting to be used, which it is for up to 30 seconds. So when a
ticket was issued and not used (the console was not started, or could not
connect), `ataniq-scratch ticket NAME` answers `busy` until that ticket has been
used or has expired.

After a connection the holder sends a snapshot of the screen, and every few
seconds a check of the screen's cells, cursor, modes and title. The console
compares it with its own copy. When they differ, or a gap shows in the output,
the console counts the mismatch, closes the connection with a detach and
attaches again to get a new snapshot. A reattach needs a fresh ticket, so with
a ticket file that someone writes by hand the console shows resyncing and then
waits for a ticket until a new one is written. The counts are in the Session
block and the evidence pane as check mismatches and resyncs.

When the console exits, by `q` in console focus or when you leave after the
session ended, it closes the client, which sends a detach so that the holder
sees a clean detach. The terminal is restored as in the fixture mode.

### Try it

`ataniq-scratch` starts a holder for a scratch agent and stands in for the
daemon: it issues the tickets. Build the three commands (the holder is the one
that `ataniq-scratch start` runs, and it looks for it next to itself), then:

```sh
mkdir -p build
sh scripts/go-tool.sh build -o build/ataniq-holder ./cmd/ataniq-holder
sh scripts/go-tool.sh build -o build/ataniq-scratch ./cmd/ataniq-scratch
sh scripts/go-tool.sh build -o build/ataniq-console ./cmd/ataniq-console
./build/ataniq-scratch start demo -- "$(command -v AGENT)" ARG...
./build/ataniq-scratch ticket demo
./build/ataniq-console --attach "$XDG_RUNTIME_DIR/ataniq/scratch/demo" \
    --ticket-file "$XDG_RUNTIME_DIR/ataniq/scratch/demo/ticket"
./build/ataniq-scratch stop demo
```

`start` runs AGENT, a scratch agent of your choice, in a holder, with an
environment of its own. The program must be an absolute path, because the
holder does no `PATH` search; `command -v` gives one for a program on your
`PATH`. By default `start` returns once the holder is running. `ticket` writes
a writer ticket to the ticket file and prints the file's path; add `--viewer`
for a viewer ticket. Leave the console with the return key and `q`. To attach
again, run `ataniq-scratch ticket demo` again: the old ticket is spent. `stop`
ends the holder. With `ataniq-scratch start --direct` the holder is a child of
`start`, which stays in the foreground, so run the other commands in another
terminal. The holder refuses to start in some cases, such as a kernel that
allows `TIOCSTI`, and the steps, the files and what is not built are in
[the lead holder guide](lead-holder.md).

## Fixture recordings

The session terminal replays a synthetic recording through an embedded
terminal emulator. A committed, deterministic generator wrote the recordings.
They are not captures of a real agent. They hold no private names or paths.
The recordings exercise the pane's behaviour: a window title, modes, colour
queries, clipboard requests, frames for synchronized output, a grapheme
cluster split across two writes, a resize record and the three kinds of exit
(status 0, a non-zero status and an unknown status). Their names are
`clipboard.jsonl`, `exit-nonzero.jsonl`, `exit-unknown.jsonl`,
`exit-zero.jsonl`, `modes.jsonl`, `mouse-held.jsonl`, `queries.jsonl`,
`resize.jsonl`, `sync-frames.jsonl`, `title.jsonl` and `zwj-split.jsonl`.

`modes.jsonl` sets mouse reporting and focus events for about half a second
and resets them, which is too short to reach session focus by hand.
`mouse-held.jsonl` sets mouse reporting (modes 1000 and 1003, with the SGR
encoding 1006) and focus events (1004) in its first 200 ms and never resets
them. With the default linger they stay on for 30 seconds after the recording's
end record, so you can enter session focus, where the mouse goes to the session
and the console asks the outer terminal for all motion and focus events, and
leave it, where the console takes the mouse back for selection and asks for no
focus events.

Everything on screen is fixture data. The recording looks like a running
session, which is why the header, the session frame and the help all say
"fixture recording, not a live session". Output from a recording reaches the
outer terminal only as drawn cells, and as the text you select and copy (see
[Selecting and copying](#selecting-and-copying)). The pane swallows clipboard
requests and counts them, and the Session block shows the count. A
recording's own resize records are ignored: the layout sets the pane's size.

### When a session ends

When the replay reaches the recording's end record and the linger time has
passed, the stream ends. The pane keeps its last screen, dimmed, under a banner. The banner says "TERMINAL
STREAM ENDED" and gives the exit status, or "unknown" when the recording has
none. It says that the terminal stream ended, not necessarily the agent. A
Codex terminal can exit while its daemon keeps running.

In session focus, any key returns to console focus, except F12, which still
zooms. In console focus, `r` replays the recording.

An ended pane is not resized. If you zoom it, leave zoom or resize the window,
the old screen keeps its old size, cut or padded under the banner.

## Width of emoji, and SSH

Terminals disagree on how wide some emoji are. A ZWJ family or an emoji with a
variation selector is one example. The console asks the outer terminal for mode
2027 (Unicode core) when it starts. It asks even when `SSH_TTY` is set, because
Bubble Tea usually skips its own query over SSH. It does not turn on
synchronized output (mode 2026) for this.

If the terminal reports mode 2027 as set, reset or permanently set, the pane
draws grapheme clusters as they are. If it gives no such report, the pane
draws a placeholder of the same cell width instead of a cluster that the
terminal might measure differently: `?`, or the full-width `？` for a wide
cell. The columns after it then stay where the pane put them. The cockpit's
own text follows the same rule. A cell holding more than 64 bytes also becomes
a placeholder, in either mode.

Use over SSH is not qualified. The SSH use checked by hand so far is
`ssh localhost`, SSH from another machine and SSH through two hops: see
[Qualification](#qualification).

## Qualification

Nothing in the console is qualified. The fixture console hosts no live
session: it only replays fixture recordings, and every control is
unavailable except Approve and Deny, which open the approvals view of sample
cards that change nothing. `--attach` shows a live scratch session, but it is provisional
and unqualified too: its tests run a fake agent in process, and no real agent
has been attached through it, in a real terminal or over SSH. Use over SSH,
terminal restoration after a startup error and any live control are not
qualified. The automated tests check keys, layout, the replay and the
input handling. They do not qualify the console in a real terminal.

One person made an attended check, on 2026-11-16, in one terminal (Ghostty),
with fixture recordings only. It is not a qualification. The check ran in a
local terminal, over `ssh localhost` and over SSH from another machine,
including with `TERM=xterm-256color`. It passed for the layout, focus and the
return key (with CapsLock and NumLock), F12 zoom, Ctrl+K, a resize across the
100x30 boundary, the ended state, emoji alignment, the `mouse-held.jsonl`
recording and restoring the terminal after quit. It found one problem: mouse
selection was not confined to the session pane, because the terminal's own
selection spans the window. The console now confines it (see
[Selecting and copying](#selecting-and-copying)); that change has not been
checked by hand yet. The attended checks here ran on the build before that
change, so they do not cover it. It still awaits the owner's attended check.

A second attended check, also with fixture recordings only, went through two
SSH hops: an SSH session from one remote machine to another, both with the
outer terminal's terminfo entry. It ran on the build before the selection
change too. On that build every case passed. The console's first output was
the width query (mode 2027), and every mode it set was restored on quit. It
says nothing about the selection change, and it is not a qualification either.

The pane does not track the kitty keyboard flags a session may set. The
console sets no keyboard enhancement of its own. Bubble Tea always asks the
outer terminal for basic key disambiguation, which is how the kitty form of the
return key can arrive. The snapshot is taken once at start. There is no
refresh.

## Dependencies

The console's session pane and cockpit draw from the console package, which
opens no socket and runs no process. The command links one other package of
this repository, the attach client, which is the one package that opens a
socket: a connection to the holder's unix socket, and no listener. A test
keeps the command from importing a network, process or terminal-device
package itself.

The console uses the Go module `charm.land/bubbletea/v2` v2.1.1 and its
transitive module dependencies. `make bootstrap` downloads them and
`make doctor` checks offline that they load for the host platform from the
module cache with hashes matching `go.sum` and are unchanged since download.
If doctor reports a modified module, or a checksum mismatch that restoring
`go.sum` does not fix, run `chmod -R u+w` on the named module directory in the
module cache, remove it and run `make bootstrap`.

`github.com/charmbracelet/x/vt` v0.1.0 is the terminal emulator the console's
session pane is built on. It and `github.com/charmbracelet/x/ansi` v0.11.9,
which Bubble Tea and ultraviolet also use, are carried in this repository as
patched copies in `third_party/patched/x-vt` and `third_party/patched/x-ansi`,
and `go.mod` replaces both modules with those directories. The patches keep a
grapheme cluster (a ZWJ emoji family, for example) whole when it arrives in two
writes, keep a UTF-8 character such as U+2733 inside a window title or
other string sequence from ending it early, and keep no scrollback for the
alternate screen, so a full-screen program that scrolls a lot does not grow
the emulator's memory without bound. Each directory has a `NOTICE.md` with the
upstream version, the patches and the condition for removing it: drop the copy
and its `replace` line once an upstream release contains the fixes.

`go mod verify`, and so `make doctor`, does not cover directory replacements.
Git protects the copies instead: they are reviewed source, and changing them is
a change like any other. Their own tests run with `make test` (or
`make test-patched`), from the main module so that they use its dependency set;
the packages run are x/vt, x/ansi and its `parser`, `kitty` and `iterm2`
subpackages, and the x/ansi `sixel` package is left out because it needs a
module that is not cached.

The console's input tests (keyboard, paste and mouse) run with the patched
x/ansi linked in. On Unix, ultraviolet scans input itself, so these tests check
that the console's input still decodes with the patched module; they do not
exercise the patch. A known limit remains: ultraviolet's own input decoder
still ends an OSC reply at a 0x9C byte, so an input reply that contains a
character such as U+2733 would be cut there, and the x/ansi patches do not
change that. The console never requests replies that could contain one.

The modules'
[upstream license notices](../third_party/terminal-console/README.md) are kept
with the source; the notices of the patched copies are their own `LICENSE` and
`NOTICE.md` files.
