# Terminal console prototype

The terminal-first Bubble Tea console is the planned primary operator client; a
web console with the same information architecture comes later. Both sit on the
shared typed command and event contract. Today the console is a provisional
cockpit with two modes. With `--demo` it is a read-only fixture: everything it
shows is fictional fixture data, the terminal pane replays a recording, it is
not a live session, and nothing you type reaches a process. With `--attach` it
shows a live scratch session that a holder keeps, for a scratch agent only (see
[Attaching to a holder](#attaching-to-a-holder)). Neither mode is a production
backend. The console does not send instructions, steer, approve or interrupt
leads. It integrates no provider and no durable command and receipt path, and
it opens no network listener. Without `--demo` and without `--attach` the
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
  pending gate and the binding of the lead and its delegates. The gate is data.
  No approval is possible.
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
| Enter | Select the project or lead in the tree. On the session terminal, focus the session. |
| ? | Show help. |
| Ctrl+K | Show the action list. |
| a | Explain why full attach is unavailable. |
| r | Replay the recording from its start, in a new pane. |
| F12 | Zoom the session terminal to the whole window, or leave zoom. |
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

Ctrl+K lists the controls the cockpit knows of. Every one is unavailable, and
the list gives each one's reason. The fixture lists these:

| Control | Key | Reason |
| --- | --- | --- |
| Approve pending gate | Ctrl+K | No grant is honoured until the owner channel is qualified. |
| Deny pending gate | Ctrl+K | No grant is honoured until the owner channel is qualified. |
| Full attach | a | Full attach needs a qualified holder and an exclusive input owner. Unqualified: the console has no live backend. |
| Send to lead | Ctrl+K | Sending needs a qualified command and receipt path. Unqualified: the console has no live backend. |
| Interrupt lead | Ctrl+K | Interrupting needs a qualified command and receipt path. Unqualified: the console has no live backend. |
| Detach | Ctrl+K | Detach and recovery are not qualified. |

The `s` and `i` keys of the earlier fixture viewer no longer exist. There is
no search key.

## Flags

```
ataniq-console --demo [--theme dark|light] [--return-key ctrl+KEY] [--recording NAME] [--linger DURATION]
ataniq-console --attach DIR --ticket-file FILE [--viewer] [--shift-enter esc-cr|lf|csi-u|backslash-cr] [--theme dark|light] [--return-key ctrl+KEY]
```

- `--demo` starts the fixture cockpit. Without it and without `--attach` the
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
unavailable. The console does not connect to `ataniqd`, the read-only daemon
skeleton of [the lead daemon guide](lead-daemon.md).
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
unavailable. `--attach` shows a live scratch session, but it is provisional
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
