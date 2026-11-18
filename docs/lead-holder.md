# Lead holder and attach path

This guide is for a developer of this repository who wants to run a scratch
agent by hand: start a holder for it, attach a console, watch what the holder
records and stop it. It describes provisional code. Nothing in it is qualified,
and it is not a way to run a real lead. The design it follows is the
[terminal data plane](design/client-protocol.md#terminal-data-plane) amendment
of the client protocol and the
[`ready` amendment](design/runtime.md#lifecycle) of the runtime contract. The
cockpit's own behaviour (layout, keys, selection, flags) is in
[the console guide](terminal-console.md) and is not repeated here.

## What it is, and what it is not

A holder is one process for one hosted agent. It owns the agent's pseudo-terminal
(the only process that holds the master side), a terminal emulator that mirrors
the screen (the patched `x/vt` in `third_party/patched`, wrapped by the same
code as the console's session pane, so that holder and console cannot drift),
two unix sockets, an event log and a final record. The agent runs as the
holder's child with the terminal as its controlling terminal. A console attaches
to the holder, gets a snapshot of the screen and then follows the output. The
console can go away and come back; the agent keeps running, and the holder
keeps reading its output, answering its terminal queries and updating its
emulator.

Three commands take part:

- `ataniq-holder`, the holder. It refuses to run without `--scratch`.
- `ataniq-scratch`, a development launcher that stands in for the daemon. It
  starts a holder, mints attach tickets and asks the holder to stop. The
  daemon skeleton `ataniqd` ([the lead daemon guide](lead-daemon.md)) does none
  of that: it starts no holder and issues no ticket.
- `ataniq-console --attach`, the console's attach client.

Status: provisional and unqualified, for a scratch agent only. A scratch agent
has no project, no lead role and a disposable configuration. Not built, or
built elsewhere in the tree and not used by the holder:

- the sandbox: the agent is an ordinary process of your user. The profile of
  [the lead sandbox guide](lead-sandbox.md) is an unqualified proposal, and the
  holder does not launch it;
- prompt injection: the input lock that a later injection will take exists, and
  the control socket answers an `inject` request with `disabled`. The holder
  does not call the library of [the lead injection guide](lead-injection.md);
- idle classification: the holder always reports `unknown`, and does not use
  that library's classifier;
- a daemon that starts holders, and with it the typed command that issues
  tickets (`ataniq-scratch` issues them instead);
- any part in the exchange mirror and the intake of
  [the lead intake guide](lead-intake.md), and real leads.

## The same-user limit

Everything here runs as one OS user. The tickets and the peer check do not tell
your console from another unsandboxed process of the same user: such a process
can ask for a ticket, read the console's memory, or talk to the control
socket by writing its own process ID into the daemon-lock file, as
`ataniq-scratch` does. [The client protocol](design/client-protocol.md#terminal-data-plane) and
[the security notes](design/security.md) state the same limit, and version 1
accepts it. What the holder does about it: one writer and no takeover, and every
writer change is recorded in the event log with the client's process ID and
command line and shown as a notice in every attached pane.

The agent itself is not confined either. A scratch agent can read and write
whatever your user can. The disposable home only changes what `HOME` says. Never
run a real lead this way, and give a provider agent nothing you would not give
any other program that runs as you.

## Try it

Run these from the repository root after `make doctor`. The helper builds
offline with the selected toolchain; each command is built on its own, because
`go build -o` takes one package:

```sh
mkdir -p build
sh scripts/go-tool.sh build -o build/ataniq-holder ./cmd/ataniq-holder
sh scripts/go-tool.sh build -o build/ataniq-scratch ./cmd/ataniq-scratch
sh scripts/go-tool.sh build -o build/ataniq-console ./cmd/ataniq-console
```

`ataniq-scratch` looks for `ataniq-holder` next to itself, so keep both in
`build/`, or name the holder with `--holder /absolute/path`. `build/` is
ignored by git.

The holder refuses to start, with exit status 2 and a line on its standard
error, in these cases, among others, each checked before the agent starts:

- `/proc/sys/dev/tty/legacy_tiocsti` is not 0, or cannot be read. The sysctl
  does not exist before Linux 6.2, and the `TIOCSTI` ioctl is then always
  available, which the holder takes as a failed check. Where it exists and is 1,
  set it to 0 first (`sysctl dev.tty.legacy_tiocsti=0`, as root). With
  it on, a process on a terminal can push input into that terminal.
- It was started with SIGHUP, SIGINT, SIGQUIT, SIGPIPE or SIGTERM ignored,
  because the agent would inherit that. Start it with the default signal
  handling.
- Its state folder or runtime folder is not a real folder of your user with
  mode 0700 (a link is refused too).

In unit mode `start` then reports only that the holder did not answer, and the
reason is in the journal of the unit (`journalctl --user -u
ataniq-scratch-NAME`); with `--direct` the holder's line is on your terminal.

Once those checks have passed, the holder takes these signals:

- SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and SIGUSR2 stop it. It stops the agent as
  `stop` does (SIGTERM, then SIGKILL after the grace), writes the final record
  and exits 0. SIGQUIT and the two user signals do not end it at once. SIGKILL
  cannot be handled and leaves no final record.
- SIGHUP and SIGPIPE are ignored, so that the holder outlives the terminal of
  whoever started it.

A stop signal that comes while the holder is starting, before the agent has
started, ends the holder with exit status 2 and a line on its standard error,
and the agent is never started. The event log records a `refusal` with the
reason `signal-before-start`, `holder.json` is removed, and no final record is
written for that epoch.

Start a scratch agent. NAME is 1 to 31 characters of `a-z`, `0-9` and `-`, and
the program must be an absolute path, because the holder does no `PATH` search
(`start` refuses any other with exit status 2):

```sh
./build/ataniq-scratch start demo -- "$(command -v bash)" --norc --noprofile
```

`start` prints the name, the role (`scratch/NAME`), the epoch, the runtime
folder and the state folder, then returns. Mint a ticket and attach:

```sh
./build/ataniq-scratch ticket demo
./build/ataniq-console --attach "$XDG_RUNTIME_DIR/ataniq/scratch/demo" \
    --ticket-file "$XDG_RUNTIME_DIR/ataniq/scratch/demo/ticket"
```

`ticket` registers a single-use ticket with the holder, writes it to the ticket
file (mode 0600) and prints the file's path. It never prints the ticket. The
ticket is valid for 30 seconds and for one attach. The holder keeps one ticket
for a place at a time: while a ticket for a place has not been used and
has not expired, `ticket` is refused with `busy`, so after a ticket nobody used,
run it again only when the 30 seconds are over. The console polls the ticket
file every half second while it has no ticket, so the order of the two commands
does not matter, but the second must follow within the 30 seconds. The attach
folder is the runtime folder that `start` printed.

A second console can watch. Wait until the first is attached, then mint a viewer
ticket and attach read-only:

```sh
./build/ataniq-scratch ticket demo --viewer
./build/ataniq-console --attach "$XDG_RUNTIME_DIR/ataniq/scratch/demo" \
    --ticket-file "$XDG_RUNTIME_DIR/ataniq/scratch/demo/ticket" --viewer
```

The wait matters: a console reads the ticket file and empties it, so whichever
console is polling first gets the ticket. A console that has to attach again (see
[Attach protocol in brief](#attach-protocol-in-brief)) shows that it is waiting
for a ticket; run `ticket` again for it. `--shift-enter esc-cr|lf|csi-u|backslash-cr`
chooses what Shift+Enter sends; the default is `esc-cr`.

The other commands:

```sh
./build/ataniq-scratch status demo          # facts, size, who is attached, counters
./build/ataniq-scratch release-writer demo  # remove the writer
./build/ataniq-scratch stop demo            # end the agent and the holder
```

`stop` asks the holder to stop, which sends the agent SIGTERM and, after a grace
of 2 seconds, SIGKILL, then waits. If the unit is still active 4 seconds later,
`stop` stops it with `systemctl`. Once the holder has exited its sockets are
gone and `status` cannot reach it: read `final.json` and `events.jsonl` in the
state folder instead.

A program that runs in cooked mode ignores Ctrl+C from the console, because the
holder holds that byte back (see [Input rules](#input-rules)). `cat` is such a
program: end it with Ctrl+D, or with `stop`.

### Unit mode and direct mode

By default the holder runs in a transient systemd user unit, `ataniq-scratch-NAME`
(so systemd refuses a second unit for the same NAME), with `KillMode=control-group`,
`Restart=no`, `TimeoutStopSec=2`, `NoNewPrivileges=yes` and `--collect`, and no
other hardening: the rest is for qualification. Unit mode needs systemd 254 or
newer, because `ataniq-scratch` passes `--expand-environment=no`. Without it
systemd would expand `$VAR` in every argument from the user manager's
environment, which would hand your real `HOME` to the agent and rewrite the
program's arguments.

On a machine with no user systemd, pass `--direct`. The holder is then a child of
`ataniq-scratch start`, which stays in the foreground until the holder exits,
forwards Ctrl+C, SIGTERM and SIGHUP to it, and exits 0 when the holder exits 0.
Run the other commands from a second terminal. What is weaker with `--direct`:
no unit cgroup holds the process tree, so the process-tree proof is the session
proof and not the cgroup proof (see [Evidence](#evidence)). `--direct` also gives
the holder only `PATH` and `LANG`, where in unit mode it has the user manager's
environment; the agent's environment is the same either way.

### What to run

Any terminal program works: a shell, `cat`, `top`, an editor. A provider agent
(`claude`, `codex`) only with a disposable configuration, given through `--env`,
for example a throwaway folder for the variable that names the program's
configuration folder (`CODEX_HOME` for `codex`). Never point it at your real
configuration or home. An agent started this way has to log in on its own.

The agent's environment is exactly `TERM=xterm-256color`, `HOME` (the disposable
home, `home/` in the state folder), `PATH=/usr/local/bin:/usr/bin:/bin`, `LANG`
(yours if it is plain locale text, else `C.UTF-8`) and each `--env KEY=VALUE`.
Nothing else is inherited. `--env` may not set those four names and may not
repeat a key.

`--env` values are visible: they are part of the holder's command line, so they
show in the process list and in the unit. Put no secret in them. The program's
own arguments are visible in the same way.

`--cols` and `--rows` set the first screen size (default 100 by 30, within 20 by
5 and 500 by 200). The writer's console sets the real size when it attaches.

When `start` fails because a unit of that name exists, or because the holder
found the role lock held (its exit status 3), a holder for that NAME is running
already; run `stop` first. Each start takes the next epoch (1 for the first). A
new epoch is a new holder and a new agent; nothing is continued.

## Files and folders

`ataniq-scratch` makes every folder with mode 0700 and refuses one that is a
link or is not yours. The names below are those of NAME `demo`.

The runtime folder, `$XDG_RUNTIME_DIR/ataniq/scratch/demo/`:

| File | Mode | What |
| --- | --- | --- |
| `attach.sock` | 0600 | The consoles' socket. |
| `control.sock` | 0600 | The daemon's socket, here used by `ataniq-scratch`. |
| `ticket` | 0600 | The current attach ticket. It is replaced, not appended to, and a console empties it when it reads it. |

The state folder, `$XDG_STATE_HOME/ataniq/scratch/demo/` (`~/.local/state` when
`XDG_STATE_HOME` is not set):

| File | Mode | What |
| --- | --- | --- |
| `holder.json` | 0600 | What the holder was started with (role, epoch, launch intent, runtime and version, profile digest, tree), written before the agent starts, and which process it is (its pid and start time), the screen size, the agent's program (`ARGV[0]` only, never its arguments or environment) and `scratch: true`. |
| `events.jsonl` | 0600 | The event log. At 4 MiB it is renamed to `events.jsonl.1` (replacing an older one) and a new file starts, so the two together stay under 8 MiB. |
| `final.json` | 0600 | The final record, written when the session is over. |
| `final.json.previous` | 0600 | The final record of an earlier epoch. At the next start that takes the role lock, a `final.json` that is there is renamed to this name (replacing an older one) before the new `holder.json` is written, so that `final.json` is never the record of an epoch other than the last one that ended. A start that fails or is stopped by a signal leaves no `final.json`, and the last record is then only here. |
| `epoch` | 0600 | The epoch of the last start, a counter. |
| `role.lock` | 0600 | Held by the holder, with an exclusive lock, for as long as it runs. A second holder for the same NAME gets exit status 3. |
| `daemon.lock` | 0600 | The stand-in daemon's lock. `ataniq-scratch` takes it for one conversation and writes its own process ID in it, which is what the holder checks on `control.sock`. |
| `home/` | 0700 | The agent's disposable home, which `start` also names as the tree. |

Nothing removes these folders for you. Delete the state folder by hand when you
are done with a NAME; the runtime folder is under `XDG_RUNTIME_DIR` and goes
with your session. The socket path is limited to 107 bytes, so a long NAME or a
long `XDG_RUNTIME_DIR` is refused.

With its default folders, `ataniqd` uses `$XDG_STATE_HOME/ataniq` and
`$XDG_RUNTIME_DIR/ataniq` (see
[the lead daemon guide](lead-daemon.md#folders-and-files)), so the `scratch`
folders lie inside them. Neither command uses the other's files: `ataniqd`
reads nothing under `scratch`, and the `daemon.lock` and `control.sock` above
are not its lock and not its socket.

## Input rules

All bytes to the agent pass through one input lock, from three sources: the
writer, the holder's own terminal replies, and (later) an injection.

- One writer and one read-only viewer, nothing else. A writer ticket is issued
  only while there is no writer and no other writer ticket is waiting to be used
  (a ticket waits up to 30 seconds), and there is no takeover. `release-writer`
  removes the writer: it becomes the viewer if there is none, and is otherwise
  closed. The cockpit shows a notice, and the event log records the change.
- Input, paste and resize from the viewer are refused with a notice and counted
  (`viewer_input_refused`). Only the writer sets the size. The viewer draws the
  holder's size, cropped or padded.
- Ctrl+Z (0x1A) is never forwarded. The writer gets the notice
  `Ctrl+Z is not sent to the session`.
- While the terminal has ISIG set (cooked mode), the holder also holds back the
  bytes that the terminal's settings give to its interrupt, quit and suspend
  characters, read at every writer input. Agents run in raw mode, where 0x03 is
  an ordinary byte and is forwarded. A held key is counted
  (`held_keys_suspend`, `held_keys_signal`) and logged by kind and count, never
  as the byte.
- Paste: the console sends the text in one piece; the holder takes the held
  keys out of it as it does for typed input (Ctrl+Z, and in cooked mode the
  signal bytes), removes every paste-end sequence from it and wraps it in paste
  markers when the agent has turned on bracketed paste (mode 2004). When the
  agent has not, the text is written as it is, less what was taken out.
- Terminal replies (device attributes, cursor position, mode queries, colours)
  come from the holder's emulator only. The attach client discards its own
  replies, so a console never types answers into the session. The colour queries
  (OSC 10, 11 and 12) are answered with the colours the writer's console sent when
  it attached, else white on black. Replies that wait for the input lock are
  bounded at 64 KiB; one that does not fit is dropped and counted.
- A fence (the critical section an injection runs in) takes the same lock for
  its whole length. Writer input waits for it, in the order it arrived, and is
  delivered when the fence ends; the emulator's replies wait in their queue.
  While a fence holds the lock the holder keeps reading the terminal but does
  not apply the output to its screen until the fence says so, so that what the
  fence reads and what it writes follow each other with nothing between; it
  holds at most 4 MiB, and when that is full it waits and drops nothing
  (`held_output_full`).
- OSC 52, the clipboard sequence, is dropped and counted
  (`clipboard_requests_blocked`, `clipboard_blocked`). Nothing the agent prints
  reaches the outer terminal's clipboard.
- Input broken: if a write to the agent fails or times out (5 seconds), a prefix
  of it may have been delivered, so the holder writes nothing more from any
  source, and later input is refused (`input_refused`). `input_broken` in the
  counters is then 1. The agent keeps running and the holder keeps reading. The
  only way out is `stop` and a new start.

## Caps

Everything a hosted program prints is untrusted, so the holder bounds what it
keeps. A cap that is hit is counted, not silent.

| What | Cap | Counter when hit |
| --- | --- | --- |
| Screen | 500 columns by 200 rows at most, 20 by 5 at least; a size outside is clamped | none; the size in force is in `status` and in the Size frame |
| Title | 256 bytes | `titles_cut` |
| Grapheme cluster | 64 bytes kept for a cell; the rest of the cluster is dropped | `cluster_bytes_dropped` |
| Recorded modes | a fixed table of 33 mode numbers; setting or resetting another is ignored | `modes_ignored` |
| Hyperlink | URL 2048 bytes, parameters 256 bytes; a longer link is dropped | `links_over_length` |
| Live links | 256 distinct links on each screen; text under a further link is drawn without it | `links_over_limit` |
| OSC, DCS and APC string data | 64 KiB a string; the rest is dropped | `strings_truncated` |
| Repeat (`CSI n b`) | a count over two lines and two characters is done in bulk or cut | `repeats_cut` |
| One PTY read | 64 KiB; a longer burst is several chunks | none |
| Terminal replies waiting for the input lock | 64 KiB | `reply_dropped_chunks`, `reply_dropped_bytes` |
| Output the reader holds while a fence has the input lock | 4 MiB; the reader waits for room and drops nothing | `held_output_full` |
| Connections | one writer and one viewer; at most 4 connections waiting for their Hello; at most 4 tickets waiting for their Hello, and in practice 2, because the holder keeps one ticket for each place; 8 control requests at once | `attach_refused_busy` (the place is taken), `attach_refused_handshake_full`, `control_refused_other` (a control request that is refused, a ticket refused with `busy` and the 8-request cap included, and a request that cannot be read; a wrong epoch and a peer that is not the daemon have counters of their own, and a refused `inject` is logged but not counted) |
| Send buffer | 1 MiB of output for each client, plus one queued snapshot outside it | `resyncs`, `too_slow_disconnects`, `stalled_disconnects` |
| Frame payload | 64 KiB from a client, 1 MiB from the holder, 64 KiB on the control socket | `bad_frames`; the connection is closed |
| Snapshot frame | 19 MiB | the snapshot is not taken (`snapshot_errors`) |
| Scrollback | none, on either screen | not applicable |
| Last screen in the final record | 256 KiB | `final_screen_truncated` |
| Event log | 4 MiB a file, one rotation; 16 KiB an event; a queue of 512 events | `events_dropped`, `event_log_errors` |

The snapshot frame is 19 MiB where the design said 16 MiB. The design counted
one screen. A snapshot carries both screens, the main and the alternate, and the
largest the format can hold for the largest pane (a cluster of 64 bytes and a
style of its own in every cell, 256 links of the longest size on each screen) is
18 991 628 bytes (18.11 MiB). That bound is stated in
`internal/terminalconsole/sessionpane/snapenc.go`, and a test in that package
computes it field by field, builds the largest state and checks it. The frame's payload is capped at
the next whole MiB, 19 MiB, which holds the 8-byte position and a snapshot of up
to 19 MiB less 8 bytes; the same test checks that the pane's constant and the
frame's agree.

A hit cap shows in two places. While the holder runs, `ataniq-scratch status`
lists the counters. When the session is over, the final record's `counters`
object holds the same numbers (and so does the `exit` event). The session's own
counters and the pane's are always listed, with zeros. The attach service's
counters (`attach_refused_*`, `resyncs`, `bad_frames` and the like) are listed
only after their first hit.

### What is still slow

The holder bounds how much it keeps, and not every way an agent's output can
make the emulator spend time. The whole-screen clears, the full reset, the
alternate-screen switches and the scrolls of a whole region cost the lines they
change and not every cell of the screen. Output that scrolls a region with left
and right margins set (mode 69) is still slow: on the largest screen (500 by
200) a line feed at the bottom of such a region takes about 1 ms, so 64 KiB of
line feeds, which is 64 KiB of PTY read, can take about a minute, and a slice
of 4 KiB about 4 s. Other scrolls in a margin region take 7 to 22 s for 64 KiB.
The figures and the cases are in the NOTICE of the patched emulator,
`third_party/patched/x-vt/NOTICE.md` (patch 15). A program that sets those
margins is rare, and nothing bounds this yet.

The holder feeds the output to the emulator in slices of at most 4 KiB and
releases its output lock between two slices. An attach, a snapshot or a resize
therefore waits for one slice, not for the whole chunk of up to 64 KiB. A slice
that holds the lock for longer than 250 ms is counted in `slow_output_slices`,
so that a stall is not silent. The reading of the agent's output, and with it
the position of the Output frames, still advances only as fast as the emulator
takes the slices.

## Evidence

**The event log**, `events.jsonl`: one JSON line for each event, with a time, a
sequence number, a kind and a name. The names are `start`, `exit`, `attach`,
`detach`, `writer-change`, `resize`, `state`, `inject`, `refusal`, `counters`,
`check-mismatch`, `resync` and `key-held`. A line carries codes and identities
only: the role and epoch, the client's name, process ID and command line, sizes,
a reason code, counters, the command ID of a control command, and the three
facts. It holds no keystrokes and no output text; the logging API has no field
that could take them. Its sequence number starts at 1 in each holder process, so
a restart shows as the number going back to 1. An event is one write and
survives a crash of the holder, but is not synced to disk each time.

**The final record**, `final.json`, is written once, when the agent's direct child
has exited and the waits are over. It is written through a temporary file and a
rename. It holds:

- the three lifetime facts and the exit status;
- the status of the exit: how (`exit`, `signal` or `unknown`), the code or 128
  plus the signal number, the signal's name, and whether it dumped core;
- the last screen as text, cut at a row boundary at 256 KiB, with its size and
  the output position it stands for;
- the counters, as of the end.

The last screen is output: it can hold anything the agent showed, including a
secret. The file is mode 0600 and is not the event log.

**The three facts** are kept apart, each `yes`, `no` or `unknown`:

- the terminal stream ended (the kernel said so). `no` also covers a stream that
  did not end within 2 seconds of the exit of the child, because a grandchild
  that still holds the terminal keeps it open;
- the TUI exited: the direct child of the holder;
- the agent's process tree is gone. `unknown` when the 5-second wait ran out.


The holder exits 0 when the session ended and the final record was written, 1 on
a holder failure, 2 on usage or a start-up refusal and 3 when the role is already
held.

**The two proofs of "process tree gone"**, named in the final record
(`tree_method`, and `tree_proof_is_weaker` for the second):

- `cgroup`, used when the holder starts in a control group that holds only
  itself and has no child group (unit mode). The tree is gone when that group
  and every group below it hold nothing but the child, and no live process is
  left in the child's session.
- `session`, the fallback (`--direct`, or any other case where the holder
  shares its group). The tree is gone when no process is left in the child's
  session. It is weaker: a descendant that starts its own session escapes it.

The limits of both: the walk is not atomic, and a process that is allowed to
write `cgroup.procs` can move between groups during it, or leave the unit (for
example into a transient scope it starts with `systemd-run --user`), so the
cgroup proof is strong only for an agent that cannot migrate. A scratch agent
runs as you and can. Read "tree gone: yes" as "nothing the holder could see is
left", not as proof about a hostile program. The direct child is also not reaped
when it exits, so that its process ID and session ID stay unused until the tree
is proven empty: a `defunct` child in a process listing after the agent exited is
expected.

## Attach protocol in brief

`internal/leadholder/wire/doc.go`, with the package it documents, is the
authority; this is the shape.

A frame is one type byte, a four-byte big-endian length and the payload.
Structured payloads are JSON, decoded strictly; output, input and snapshots are
binary. An unknown type, a payload over its cap or a payload that does not
decode closes the connection; there is no error frame.

| Client to holder | Holder to client |
| --- | --- |
| Hello (version, ticket, name, size, colours) | Welcome (version, role, lead, epoch, runtime, caps) |
| Input | Snapshot, Output (start position and raw bytes) |
| Paste | Modes (the full mode set, on every change) |
| Resize | Size, State (the three facts, idle, title), Role (who writes) |
| Detach | Check (a digest at a position), Notice, Exit (the final record) |

The flow:

1. The console sends a Hello with its ticket within 5 seconds. The ticket is
   used up by the first Hello that presents it, valid or not, and the peer must
   be your user. A refusal is a Notice (`refused-ticket`, `refused-busy`,
   `refused-version`) and a close.
2. The holder sends a Welcome, then a Snapshot of the visible screen and
   everything an emulator needs to go on from it, taken only when the parser is
   between sequences. With synchronized output on, it waits up to 150 ms for the
   frame to end and else takes the last complete one. After 1 second without a
   point the client gets a `snapshot-waiting` notice. A client gets the visible
   screen only; there is no scrollback.
3. Output frames follow, each carrying the position (the count of PTY bytes
   before it), so the client can tell that none is missing. Modes decides what
   the console asks of the outer terminal (mouse, paste, focus); the console's
   own emulator does not.
4. After a snapshot, and then every 5 seconds, the holder sends a Check. The
   client compares the digest of its own cells, cursor, modes and title. A
   mismatch, or a gap in the positions, makes the client detach and attach
   again with a new ticket: run `ataniq-scratch ticket` again. The mismatch is
   counted and logged. An Exit frame ends the pane and the client for good.
5. A slow client never stalls the PTY loop. When its buffer is over 1 MiB, its
   queued output is dropped, it gets a `resync` notice and one fresh snapshot. A
   second overflow within 10 seconds, or an overflow before the snapshot was sent,
   disconnects it with `too-slow`. A client that takes no bytes for 30 seconds is
   disconnected as `stalled`. A writer that disconnects leaves the lead without a
   writer, and nothing is typed on its behalf.

## What needs a check before anyone relies on it

The automated tests use a fake agent, fixtures and fake process trees. None of
the following has been qualified, and each needs an attended or qualification
check first:

- real agents (`claude`, `codex`) on the patched emulator: their output, their
  mouse, paste, focus and colour handling, and the check digests under real
  load;
- key encodings: Shift+Enter (the four choices of `--shift-enter`), Ctrl+Enter,
  Ctrl+Tab, Ctrl+Backspace, Alt combinations and a lone Escape, each against
  each agent;
- a long run: the design asks for 72 hours with periodic output, with memory and
  file sizes staying under the caps above;
- unit hardening: the unit sets only the properties listed under
  [Unit mode and direct mode](#unit-mode-and-direct-mode); the rest of the design's
  hardening is untested;
- the cgroup proof on a real delegated unit: the tests use a fake `/proc` and a
  fake cgroup tree;
- the console in the terminals and over the SSH paths that
  [the console guide](terminal-console.md) names, with a live session.

Sandbox qualification still comes before any Ataniq-managed lead. See
[the host manual](qualification-host.md) for the qualification infrastructure.
