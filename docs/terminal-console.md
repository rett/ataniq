# Terminal console prototype

The terminal-first Bubble Tea console is the planned primary operator client; a
web console with the same information architecture comes later. Both sit on the
shared typed command and event contract. Today the console is a provisional,
read-only fixture cockpit. Everything it shows is fictional fixture data. The
terminal pane replays a recording. It is not a live session. The console has no
production backend. It does not send instructions, steer, approve or interrupt
leads, and nothing you type reaches a process. It integrates no provider and no
durable command and receipt path, and it opens no network listener. Without
`--demo` the command reports that the backend is unavailable and exits with
status 1.

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
discards all of it.

The return key leaves session focus. The default is Ctrl+\. The console reads
it from both of its encodings: the byte 0x1c and the kitty keyboard form
`CSI 92;5u`. Without the kitty keyboard protocol, a terminal sends the same
byte 0x1c for Ctrl+4. Then Ctrl+4 also returns. In the kitty form Ctrl+4 is
`CSI 52;5u` and does not return. Ctrl+Enter is never bound.

F12 toggles zoom in either focus state. In zoom the session terminal fills the
window under a one-line header. The header names the focus and the key that
leaves zoom, F12. Tab and the move keys do nothing while zoomed. Enter focuses
the session, and `r` replays the recording. Zoom draws no help, action list or
notice.

### Selecting and copying

While the cockpit is shown, in console focus or session focus, zoomed or not,
the console asks the outer terminal for mouse reporting (cell motion, or all
motion when the session has the focus and asked for all motion). The outer
terminal's own selection then no longer spans the window, and a selection is
confined to the session terminal. The single-column pages below 100x30 do not
ask for the mouse, so the terminal's own selection works there.

Unless the session has the focus and asked for the mouse, drag with the left
button in the session terminal to select text. The selection runs like a
terminal's: from the cell where you pressed to the cell where you are, across
rows, whole rows in between. It stays inside the session terminal when the
pointer leaves it. When you release the button, the console copies the text
to the outer terminal's clipboard with OSC 52, and the selection stays
highlighted. The text is the pane's screen: a wide character or a grapheme
cluster is copied once, blanks at the end of each row are dropped, and rows
are joined with a newline. The emulator keeps no record of where a long line
wrapped, so a wrapped line is copied as two lines. A selection of more than
1 MiB of text is not copied at all, and the key bar says so. A terminal that
does not accept OSC 52 writes, or has them turned off, copies nothing, and the
console cannot tell.

A press outside the session terminal starts no selection, a click without a
drag copies nothing, and the wheel does nothing. A press anywhere clears the
selection. So do a key or a paste sent to the session, a window resize, F12,
`r`, and output that changes the selected text, such as the session writing
over it, scrolling or clearing the screen. Output elsewhere on the screen
keeps it. While help or the action list is open, a drag selects nothing.

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

`ataniq-console --demo [--theme dark|light] [--return-key ctrl+KEY] [--recording NAME] [--linger DURATION]`

- `--demo` starts the fixture cockpit. Without it the command reports that the
  backend is unavailable.
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

A bad flag value prints a diagnostic and the usage line and exits with
status 2.

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
"fixture recording, not a live session". Output from a recording never reaches
the outer terminal except as drawn cells. The pane swallows clipboard requests
and counts them, and the Session block shows the count. A recording's own
resize records are ignored: the layout sets the pane's size.

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
`ssh localhost` and SSH from another machine: see
[Qualification](#qualification).

## Qualification

Nothing in the console is qualified. It hosts no live session: it only replays
fixture recordings. It has no live backend, and every control is unavailable.
Use over SSH, terminal restoration after a startup error and any live control
are not qualified. The automated tests check keys, layout, the replay and the
input handling. They do not qualify the console in a real terminal.

One person made one attended check, on 2026-11-16, in one terminal (Ghostty),
on one machine, with fixture recordings only. It is not a qualification. The
check ran locally, over `ssh localhost` and over SSH from another machine,
including with `TERM=xterm-256color`. It passed for the layout, focus and the
return key (with CapsLock and NumLock), F12 zoom, Ctrl+K, a resize across the
100x30 boundary, the ended state, emoji alignment, the `mouse-held.jsonl`
recording and restoring the terminal after quit. It found one problem: mouse
selection was not confined to the session pane, because the terminal's own
selection spans the window. The console now confines it (see
[Selecting and copying](#selecting-and-copying)); that change has not been
checked by hand yet.

The pane does not track the kitty keyboard flags a session may set. The
console sets no keyboard enhancement of its own. Bubble Tea always asks the
outer terminal for basic key disambiguation, which is how the kitty form of the
return key can arrive. The snapshot is taken once at start. There is no
refresh.

## Dependencies

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
