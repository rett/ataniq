# Lead daemon skeleton

This guide covers `ataniqd` and the packages behind it. It describes what the
code does today. It is not a design: the planned boundaries are in the
[design contract baseline](design/README.md).

Nothing described here is qualified. No part of it has run against a real
systemd user manager, and no test starts a unit. The tests run against an
in-memory fake of the unit manager and against private sockets. Every package
named here is provisional: its names, its file formats and its behaviour may
change until a qualification run has covered it.

## What this is and is not

`ataniqd` is a provisional, unqualified, read-only daemon skeleton. It comes
with the packages a later daemon that manages leads will use: a durable
registry of lead roles, role names, locks, a unit manager for systemd, and the
lifecycle logic that starts, stops and reconciles roles.

Today the daemon starts, stops and manages no lead. It starts, signals and
stops no unit, at start-up, while it serves and at shutdown. No flag and no
protocol operation can make it. Against a fresh state folder it knows no role
at all, because no command of this build defines one. It answers two read-only
questions on a local socket, `hello` and `status`, and by default it opens no
network listener. **The one network listener is the seat gate**, in a build with
the `ataniq_ownerchannel` tag and only under `ataniqd run --owner-channel
--seat-gate ADDR`: a TCP listener on one address of the host, for the seat app of
the owner's phone (see [the seat gate](#the-seat-gate) and
[the seat gate operator guide](seat-gate.md)). Without that flag nothing listens
on the network.

**The exception is canary mode.** In a build with the `ataniq_canary` tag, and
only under `ataniqd run --canary CONFIG`, the daemon serves five more
operations (`define`, `start`, `ticket`, `inject` and `stop`) for the one
throwaway role that the config names, and then starts and stops that lead's
unit. [The canary slice guide](canary-slice.md) describes it. **Scratch mode is
the second exception**, in the same tagged build and under `ataniqd run
--scratch CONFIG` alone (it excludes `--canary`): the daemon serves six more
operations, `lead.list`, `lead.define`, `lead.start`, `lead.stop`, `lead.ticket`
and `lead.inject`, for up to eight scratch leads, with a durable receipt for each
command. [The scratch leads guide](scratch-leads.md) describes it. Release
builds contain neither mode, and a tagged build without `--canary` and
`--scratch` is the read-only skeleton this guide describes. Everything below is
about that skeleton unless it says otherwise.

These are not part of it: a real lead, the holder, a sandbox, injection of
instructions into a session, any console integration and any Qargi
integration. The list of what is not built is at the [end](#not-built).

The tree has provisional code for some of these, and the daemon uses none of
it, except in a build with the `ataniq_canary` tag, under `--canary` or
`--scratch` (see [the canary slice guide](canary-slice.md) and [the scratch
leads guide](scratch-leads.md)): the scratch-only holder and the
console's attach client ([the lead holder guide](lead-holder.md)), the sandbox
proposal ([the lead sandbox guide](lead-sandbox.md)), the injection library
([the lead injection guide](lead-injection.md)), and the exchange mirror with
its intake command ([the lead intake guide](lead-intake.md)). Without that tag
and a flag, none of that code imports a package of this guide, and no package of
this guide imports it. In a tagged build the daemon's canary files import the
holder's control wire, and the canary package they use for the admission
imports the injection library's message rules.

| Package or command | What it is | What the daemon does with it today |
| --- | --- | --- |
| [`cmd/ataniqd`](../cmd/ataniqd/doc.go) | The command: `run`, `status`, `version`; in a build with the `ataniq_canary` tag also `run --canary`, `run --scratch`, `canary` and `--version`; in a build with the `ataniq_ownerchannel` tag also `run --owner-channel`, `run --seat-gate` and `seat-gate`. | It is the daemon. |
| [`internal/leaddaemon`](../internal/leaddaemon/doc.go) | Start-up order, shutdown, exit codes, the status handler and the status client. | All of it. |
| [`internal/clientapi`](../internal/clientapi/doc.go) | The client protocol `ataniq.client/1`: socket, server, client. | Serves `hello` and `status`. |
| [`internal/leadregistry`](../internal/leadregistry/doc.go) | The durable journal of lead roles. | Opens it and reads it. The reconcile at start writes what it finds about a role; nothing defines a role or launches one. |
| [`internal/leadrole`](../internal/leadrole/doc.go) | Role names, unit names, lock paths, coordinator identity. | Reads names. |
| [`internal/leadlock`](../internal/leadlock/doc.go) | The two kinds of file lock. | Takes the daemon lock and the runtime folder lock. Takes no role lock. |
| [`internal/leadunit`](../internal/leadunit/doc.go) | The unit manager interface, the unit specification and an in-memory fake. | The interface only. |
| [`internal/leadunit/systemdmgr`](../internal/leadunit/systemdmgr/doc.go) | The unit manager for the systemd user manager. | Reads units for the one reconcile at start. |
| [`internal/leadlifecycle`](../internal/leadlifecycle/doc.go) | Start, stop and reconcile of roles, coordinators included (see [below](#coordinators-in-the-lifecycle)). | Calls reconcile once, at start. Never start, stop, mark or clear a stall. In scratch mode (tagged build) a timer also settles one role's pending launch when its window ends ([below](#the-late-start-window)). |
| [`internal/pidfd`](../internal/pidfd/doc.go) | Process identity and boot identity primitives. | Reads the boot identity. |
| [`internal/systemd`](../internal/systemd/doc.go) | Control group, `systemctl show` and bounded command helpers. | Used by the unit manager. |
| [`qualification/leadunit/cmd/leadunit-qualify`](../qualification/leadunit/cmd/leadunit-qualify/doc.go) | An opt-in harness for the unit manager. | None. It is run by hand. |

## Running it

### Build

Run `make doctor` first, then build with the repository's Go wrapper:

```sh
mkdir -p build
sh scripts/go-tool.sh build -o build/ataniqd ./cmd/ataniqd
./build/ataniqd version
```

`ataniqd version` prints `ataniqd 0.0.0-dev`. The repository's `VERSION` file
cannot be embedded from the command's folder and the daemon reads no file at
run time to learn it, so a build that wants the real number sets it:

```sh
sh scripts/go-tool.sh build -ldflags "-X main.version=1.2.3" -o build/ataniqd ./cmd/ataniqd
```

The command is Linux only. On another platform it prints that and exits 1.

### Commands and flags

```text
ataniqd run [--state-dir DIR] [--runtime-dir DIR] [--owner-channel [--seat-gate ADDR]]
ataniqd status [--socket PATH] [--json]
ataniqd version
ataniqd seat-gate init|pins [--state-dir DIR]
```

`--owner-channel`, `--seat-gate` and `seat-gate` are in the tagged build only
(see [the owner channel](#the-owner-channel-tagged-build-only)).

| Flag | Default | Meaning |
| --- | --- | --- |
| `run --state-dir DIR` | `$XDG_STATE_HOME/ataniq`, else `$HOME/.local/state/ataniq` | The state folder. |
| `run --runtime-dir DIR` | `$XDG_RUNTIME_DIR/ataniq` | The folder that holds `daemon.sock`. |
| `run --owner-channel` | off | Tagged build only (tag `ataniq_ownerchannel`): serve the owner connection. See [the owner channel](#the-owner-channel-tagged-build-only). |
| `run --seat-gate ADDR` | off | Tagged build only, with `--owner-channel`: also serve the seat gate on ADDR. See [the seat gate](#the-seat-gate). |
| `status --socket PATH` | `$XDG_RUNTIME_DIR/ataniq/daemon.sock` | The socket to ask. |
| `status --json` | off | Print the status result as JSON. |

A `$XDG_STATE_HOME`, `$HOME` or `$XDG_RUNTIME_DIR` that is not an absolute
path is ignored. There is no default runtime folder without an absolute
`XDG_RUNTIME_DIR`, and never a shared folder such as `/tmp` in its place:
use `--runtime-dir`. Both folders must be clean absolute paths. The socket
path, `daemon.sock` included, must fit in 107 bytes.

### Folders and files

| Path | Mode | What it is |
| --- | --- | --- |
| `<state>` | 0700 | Created if missing (with a missing parent, also 0700). An existing one is accepted only if it is a real folder (a symbolic link is refused), owned by the caller, and not open to group or others. |
| `<state>/daemon.lock` | 0600 | The daemon lock. It holds the daemon's pid as decimal text and a newline. It is never removed, so it stays after the daemon ends. |
| `<state>/registry/` | 0700 | The registry folder. |
| `<state>/registry/registry.jsonl` | 0600 | The registry journal. |
| `<runtime>` | 0700 | Created if missing, right after the daemon lock is taken; its parent must exist. Checked as `<state>` is. The socket path must fit in 107 bytes, which is checked before the folder is made. |
| `<runtime>/daemon.sock.lock` | 0600 | The runtime folder lock: one daemon serves a runtime folder. It is taken right after the daemon lock, before the registry is opened, and held to the end of shutdown (see [Locks](#locks)). |
| `<runtime>/daemon.sock` | 0600 | The client socket. Closing the listener at shutdown removes it. |

`<state>/roles/<project>/lead.lock` and the other role lock paths are named in
[Roles](#roles) and [Locks](#locks). This build creates none of them.

With the default folders, `ataniq-scratch` keeps the files of a scratch holder
in a folder `scratch` inside `<state>` and inside `<runtime>` (see
[the lead holder guide](lead-holder.md#files-and-folders)). The daemon reads
nothing there, and the `daemon.lock` of a scratch holder is the launcher's own
file, not the daemon lock.

### Start-up order

`ataniqd run` takes these steps in this order and stops at the first that
fails, with one line on standard error:

1. **config**: refuse to run as root; check that both folders are clean
   absolute paths.
2. **state**: create or check the state folder.
3. **lock**: take `daemon.lock`. A second daemon fails here and the message
   shows the holder's pid text as a hint, not as proof.
4. **runtime lock**: make the runtime folder and take `daemon.sock.lock` in
   it, before the registry is opened. A socket path that is too long is
   refused first, before the folder or the file is made. A second daemon on
   the same runtime folder fails here with exit code 17 and a message that
   another daemon serves the folder. It has opened no registry and asked the
   manager nothing by then, so it writes nothing to its journal. A start that
   fails at the registry, the manager or the reconcile step leaves the folder
   and the lock file behind, and touches no socket.
5. **registry**: open `<state>/registry/`. A corrupt registry stops the daemon.
   Nothing is repaired.
6. **manager**: read the boot identity and build the unit manager and the
   lifecycle controller. When `busctl` or `systemctl` cannot be found, the
   daemon still runs: it prints that the systemd tools are unavailable and
   uses a manager that answers nothing.
7. **reconcile**: run the lifecycle's reconcile once, bounded to 30 s. It
   starts nothing and signals nothing. It is shown as not completed when the
   unit manager is known to be unavailable (also with an empty registry, when
   nothing was asked of it), when any unit-manager call failed during it, when
   the manager did not answer for a role, when it ran out of time, and while a
   cut of the registry is unacknowledged. None of these is fatal; a failure of
   the registry is (see [Reconcile](#reconcile-and-what-starts-by-itself)).
   Reconcile does not run again on a timer; in scratch mode (a build with the
   `ataniq_canary` tag) a timer settles a launch that is still pending at its
   deadline (see [the late-start window](#the-late-start-window)).
8. **stale socket**: with both locks held, remove a socket file an earlier
   daemon left. It removes a socket owned by the caller and refuses anything
   else at that name.
9. **socket**: bind `daemon.sock`. The runtime lock stays held while the
   daemon serves.
10. **serve**, until SIGTERM or SIGINT.
11. **shutdown**.

The daemon writes a line to standard error when it listens, and when it
removes a stale socket or finds a reason to show the reconcile as not
completed. It writes no log file.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Done: clean shutdown, or `status` was answered. |
| 1 | A failure with no code of its own: a `status` call that timed out, or an unsupported platform. |
| 2 | Usage: the command line is not valid. |
| 10 | Config: root, a folder that is not a clean absolute path, or no default for one (`run`; `status` without a default socket exits 2). |
| 11 | State folder: cannot be made, not the caller's, or open to group or others. |
| 12 | The daemon lock is held by another process. |
| 13 | The daemon lock could not be taken for another reason. |
| 14 | Registry: cannot be opened, corrupt, or locked by another writer. |
| 15 | Manager: the boot identity cannot be read, or the manager or controller cannot be built. |
| 16 | Reconcile: failed in the registry. |
| 17 | Socket: another daemon serves the runtime folder (its lock is held; refused before the registry is opened), or the socket path is too long, the folder is not usable, or the stale socket or the bind failed. |
| 18 | The server stopped by itself. |
| 19 | The server did not end within the shutdown grace (5 s). The registry is left open and both locks held, and all end with the process. |
| 20 | `status`: no daemon answers at the socket (missing or refused). |
| 21 | `status`: the daemon refused this user. |
| 22 | `status`: an answer that is not valid, or an error the daemon answered. |

### What `status` prints

`ataniqd status` calls `hello` and then `status`, and prints one line for the
daemon and one per role, with its times in UTC. The values below are
synthetic:

```text
daemon version=0.0.0-dev pid=4242 started=2026-01-01T00:00:00Z revision=6 boot=00000000-0000-4000-8000-000000000000 reconcile=completed reconcile_at=2026-01-01T00:00:00Z
role=demo/lead kind=lead state=starting reason=launch-intent epoch=1 desired=running manager=ataniq
```

A coordinator line adds `parent=` and `instance=`; a reason detail adds
`detail="..."`. `reconcile=` is `incomplete` when the reconcile did not
complete. With `--json` it prints the `status` result alone, indented. Every
string that came from the socket is escaped (see below), so a hostile or
faulty daemon cannot put a control sequence on the terminal. `status` gives up
after 10 s.

### What shutdown does and does not do

On SIGTERM or SIGINT the daemon stops accepting, closes the listener (which
removes the socket, still under the runtime lock), closes the connections and
waits for them for at most 5 s, then closes the registry, releases the runtime
lock, and releases the daemon lock last.

If the server has not ended within those 5 s, the daemon does not close the
registry or release either lock under a goroutine that may still use them. It
says what is left, leaves all three (the registry, the runtime lock and the
daemon lock) to the end of the process and exits with code 19. The listener
was closed by then, so the socket is gone.

It does not stop a lead or any unit, and it signals nothing. A lead that runs
when the daemon goes away keeps running. A SIGKILL or a crash skips all of it;
the kernel releases the locks, and a socket file may stay behind, which the
next daemon removes after it holds both locks. A lock file that stays behind is
no obstacle: the lock is the `flock`, not the file.

## The owner channel (tagged build only)

Release builds of the daemon contain no owner channel: no flag, no `owner.*` operation in
`hello`, no code. It exists only in a build made with the tag
`ataniq_ownerchannel`, for a scratch pass. It is provisional, and **none of it is
qualified**: the door behind it is a stand-in that gives no real approval. The
tests of that code run with `make test-ownerchannel` (the tagged packages with
the tag, and again with the tags `ataniq_ownerchannel` and `ataniq_canary` for the
packages that hold both); `make test` stays untagged and skips them.

`make ownerchannel-build` builds the daemon with the tag, and nothing else, into
`build/owner/ataniqd` (the folder is ignored by git). It runs `make doctor`
first and starts nothing. It is the one target that builds a binary with the
tag: no release, no CI job and no other target uses it or depends on it, and the
tests in `tests/ownerchannel_tag_test.go` refuse any other recipe that names
the tag. The binary is for the owner's own trial of the unqualified channel.

### Starting it

```text
ataniqd run --owner-channel [--state-dir DIR] [--runtime-dir DIR]
```

`--owner-channel` is a flag of `run` in the tagged build only. With it the
daemon serves the owner connection, the `owner.*` operations of the console's
approvals, on the same socket and the same protocol, and `hello` lists them.
Without it the tagged build lists none, as a release build does. The place of
the one connection that may decide goes to a console of the owner that has a
controlling terminal. The daemon refuses to start with the flag when its build
cannot build the host's side of a card or when the settings below are wrong,
with exit code 10 and before it takes a lock. At start it prints one line on
standard error, the STAND-IN banner:

```text
ataniqd: owner channel: STAND-IN door, scratch state: no real approval
```

### The seat gate

```text
ataniqd seat-gate init [--state-dir DIR]
ataniqd seat-gate pins [--state-dir DIR]
ataniqd run --owner-channel --seat-gate ADDR [--state-dir DIR] [--runtime-dir DIR]
```

The seat gate is the link of the seat app, which serves the owner's phone from
the authority host, to this daemon (see
[the seat gate's design](design/seat-gate.md); the operator's steps, the firewall
and proxy rules and the troubleshooting are in
[the seat gate operator guide](seat-gate.md)). It is a TCP listener inside the
daemon, in the tagged build only, and opened only with `--seat-gate`, which
needs `--owner-channel`. Without the flag nothing listens on the network: this
is the daemon's only network listener, and a build without the tag has none. It
is provisional and **not qualified**, and it serves the same scratch service as
the owner connection, behind the STAND-IN banner.

- ADDR is one IP address of this host and a port, such as `192.0.2.10:7443` or
  `[2001:db8::10]:7443`. A host name, a wildcard address (`0.0.0.0`, `[::]`), a
  multicast or broadcast address, an address with a zone and a zero port are
  refused with exit code 10 before the daemon takes a lock. The owner's
  firewall must let only the authority host reach the port.
- The gate's key is in `owner/gate/` of the state folder (`cert.pem` and
  `key.pem`, the key 0600, the folder 0700). `ataniqd seat-gate init` makes it
  and prints its fingerprint, the pin the seat is given; a key already there is
  never overwritten, so a new key is made only after the old one is moved away
  by hand.
- The seat keys the gate lets in are pinned in `owner/gate/seat-pins`: a regular
  file (not a link) of the daemon's user, mode 0600, at most 4 KiB, one key
  fingerprint (64 lower-case hexadecimal digits) per line, at most 4. A blank
  line and a line that starts with `#` are skipped. `ataniqd seat-gate pins`
  prints the pins as the daemon reads them. The owner edits the file by hand;
  the daemon reads it at start, so a change takes effect at the next start.
- The daemon refuses to start, with exit code 10, without the key, without the
  pin file, with a file that is not as above, or with no pin; the message names
  the line at fault by its number and never prints a pin.
- The seat origin, the address of the seat app such as
  `https://seat.example`, is read once at start from the owner-local file
  `ataniq/owner-local/seat-origin` in the configuration folder
  (`$XDG_CONFIG_HOME`, else `~/.config`), with the rules of
  `internal/ownerlocal` (a regular file, not a link, at most 1 KiB, not
  writable by group or others; its first line). It must be `https://` and a
  lower-case domain name, `:443` at most, with no address literal, other port,
  path, query, fragment or user information. A change of the file takes effect
  at the next start. Without the file, or with one that is refused or holds no
  such origin, the daemon starts as before and the gate serves pairing and the
  reads only.
- The gate starts once the service is built and stops, before the service is
  closed, when the daemon shuts down; a gate that cannot listen (the port is
  taken) stops the daemon with exit code 18. At start the daemon prints one
  line with the address, the gate's key fingerprint and what it serves, and no
  pin. With an origin:

```text
ataniqd: seat gate: listening on 192.0.2.10:7443, key fingerprint <64 hex digits>, seat origin https://seat.example; it serves pair, read, login, decide and watch
```

  Without one, the line says why:

```text
ataniqd: seat gate: listening on 192.0.2.10:7443, key fingerprint <64 hex digits>; it serves pair, read and watch only: no seat origin is configured (the owner-local seat-origin file is missing or empty)
```

  `watch` is in the line because the daemon's service can wait for a change of
  the pending cards, which every real start does; a service that cannot would
  leave it out, and the line would end in "login and decide" or "pair and read
  only".

The gate speaks TLS 1.3 with pinned keys, strict frames of at most 257 KiB
(the largest listing, card or page and the response around it) and the limits
of `internal/seatgate`. It serves the capabilities `pair`, `read` and
`watch`: the seat's pairing call, `seat.pair`, and the reads `owner.list`, `owner.show`,
`owner.paths` and `owner.patch`, through `internal/approval/gatehost`, with a
seat source that is never the decider. With a seat origin it also serves
`login` (`seat.login-challenge`, `seat.login`) and `decide`
(`seat.challenge`, `seat.decide`): a login and a decision from the phone,
each a passkey assertion with user verification over a challenge the daemon
draws from its own record of the card, verified against a key on
`owner/keys.log` that is not revoked and the seat origin (see
[the seat gate's design](design/seat-gate.md#the-challenge)). A challenge is
kept in memory for 5 minutes and one use; a restart drops every one. A
decision from the phone is journaled as confirmed by passkey, with the
evidence of the assertion, and needs no activity at the desk; it is not
activity at the desk either. With `watch` it serves the seat's long wait,
`seat.wait`, which answers when the set of pending cards changes (see
[live refresh](design/seat-gate.md#live-refresh-seatwait)): it decides nothing,
it carries only each card's id, kind, binding digest and held mark, never card
text, and it is served with or without a seat origin. The gate holds at most
two waits at once for a seat. Every other operation is refused `unknown-op`.
A seat whose key is not pinned is refused in the handshake and never reaches
the service. On the daemon's socket `seat.pair` stays refused and the five
seat operations (the four of login and decision, and `seat.wait`) are not served: no connection there is a seat. `owner.show`
marks a card whose decision is journaled and not yet taken by the door
(`"on_its_way": true`), on the socket and the gate alike.

Same-user limit: a process of the daemon's user that is not in a sandbox can
read the gate's key, add a pin, rewrite the seat origin file and edit the key
list. Until the sandbox is qualified, the gate and every decision from the
phone are for scratch state only, behind the STAND-IN banner.

### The scratch settings

The host's side of a card is built from `owner/scratch.json` in the state
folder, over the real journal in `owner/` and the stand-in for the planner's
door, which keeps nothing across a restart (its card numbers start from the
time of day and past the journal's highest of the day, so that a restart does
not give a new card the id of one the journal holds a decision for). Its keys:

- `"door"`: must be `"stand-in"`. Any other value, or none, is refused.
- `"roots"`: the scratch folders, one of which holds the mirror. The daemon
  refuses a state folder, a root or a mirror that is, lies inside or contains
  the account's default state or runtime folder. It refuses a drop folder that
  is, lies inside or contains a default folder or the state folder (so its
  `owner/` folder too).
- `"sandbox"`: the clean absolute path of the sandbox template file, the same
  template `ataniq-intake verify --sandbox` takes (see
  [lead-intake.md](lead-intake.md)). It lies under a scratch root and not in
  the state folder, which is kept for the host's records and journal and free
  of configuration, and not in the drop folder, the mirror or the work folder.
- `"work"`: the clean absolute path of the folder the runs work in, under a
  scratch root, outside the state folder and apart from the mirror and the drop
  folder. The daemon
  makes it with mode 0700 when it is not there (its parent must exist).
- `"kinds"` (optional): the kinds of credential the project accepts for a
  phone's login and decision, by the names `"platform-passkey"`,
  `"vault-passkey"` and `"passkey"`, each at most once. Absent or empty, every
  kind is accepted (`"unverified"`, the stored word of a key paired without a
  claim, is not a kind). A name that is not one of the three, or a name twice,
  refuses the settings. A kind is what the phone's authenticator claimed, relayed
  by the seat and not attested, so the list is advisory: it does not hold
  against a taken-over seat. The settings carry the list as the credential
  policy of `internal/approval/credkind` (`Scratch.KindPolicy` in
  `internal/approval/hostadapter`), which the owner channel's service applies
  when a phone's decision challenge is asked and when the decision is made; a
  login checks only that the daemon knows the key's kind.

- `"trial"` (optional): `true` marks the settings of the phone trial, which
  `ataniqd trial init --state-dir DIR` writes and `ataniqd run --owner-channel
  --trial` serves (see [the phone trial](seat-gate.md#phone-trial)): sample
  cards only, made by `ataniqd trial submit`, decided through the real decision
  path and applied to nothing. Trial settings name the project, the role, the
  target, the seat, the door and the kinds, and no mirror, identity, gate,
  drop folder, root, sandbox or work folder. The daemon refuses trial settings
  without `--trial` and other settings with it, and checks a trial's state
  folder against the account's default folders as above.

The settings also name the project, its mirror, the role, the target, the
commit identity, the gates and the folder requests are dropped in. `sandbox`
and `work` are required: the daemon refuses `--owner-channel` without them or
with paths that are not scratch paths. A request is a file named after its
request id with the suffix `.request` in the drop folder, and its reply is a
file beside it, the only reply this build writes. A request that a stop left taken but never
handled gets one `request-invalid` reply, asking for a new request id, from the
first scan after the next start. The suite is the intake's
merge-mode verification (`internal/intake/verify` with its real runner): the
merge is fetched through the pin under `refs/ataniq/pending/`, checked to have
two parents with the target's tip first, and the gates run in the sandbox. The
daemon builds it without starting anything: the template is read, and a
sandbox is started, only when a request is verified, and each run reads the
template again. The record of the run is kept in `owner/records` as a
merge-mode record, which is the only kind the host's approval counts. An
approval applies by one ref transaction with the old value, with no checkout and
no hook; a target that moved gives `not-applied` and changes nothing.

### What ran on a stand-in, and what waits for a seated lead

The end-to-end test is in `internal/approval/hostadapter` (`e2e_test.go` and
`e2e_oc_test.go`, run with the tag `ataniq_ownerchannel` and `-run OwnerChannel`)
and `internal/leaddaemon/ownerchannel_contract_test.go`. It joins the console's
approvals model, the console's client, the daemon on a private socket, the
service, the real journal, the host adapter and a temporary bare mirror, over the
stand-in door and a fake verification suite. No sandbox, no unit and no lead
runs in it. A case that passed there proves the host's side only, against a door
that is kinder than the real one. **None of this is qualified**: every case is
run again against the real door on a seated lead, and counts as qualified only
then.

Covered by the end-to-end test on the stand-in:

- OC4, the host's part: the branch moved or the request withdrawn with the card
  open (`TestOwnerChannelOC4TheFeatureMovesOrTheRequestIsWithdrawnWithTheCardOpen`).
  Not covered there: the typed text dropped when the card ends under the owner's
  hands, which is the console's own test.
- OC5, a target that moves after the decision (`TestOwnerChannelOC5TheTargetMovesAfterTheCard`).
- OC6, one decision used twice, over the wire, at the door and at the ref
  (`TestOwnerChannelOC6OneDecisionUsedTwice`). Not covered: a store file restored
  from a copy, which the stand-in cannot show because it keeps nothing on disk.
- OC7, expiry of a pending and of an approved request
  (`TestOwnerChannelOC7ExpiryOfAPendingAndOfAnApprovedRequest`).
- OC9, the host's part: a stop at each crash point and a restart with a held
  decision, which delivers nothing (`TestOwnerChannelOC9StopsAtEachCrashPoint`,
  `TestOwnerChannelOC9AHeldDecisionIsNotDeliveredByARestart`). Not covered: the
  stop of the whole daemon on a seated lead.
- OC15, the summary of a known change set (`TestOwnerChannelOC15TheSummaryOfAKnownChangeSet`).
- OC16, a missing or surplus sign-off (`TestOwnerChannelOC16MissingAndSurplusSignOff`).
- OC17, a wrong digest, epoch, revision or id (`TestOwnerChannelOC17WrongDigestEpochRevisionOrId`).
- OC18, a lead-made merge named as the tip (`TestOwnerChannelOC18ALeadMadeMergeNamedAsTheTip`).
- OC21, restarts of the console and the daemon with a card pending and a
  request being verified (`TestOwnerChannelOC21RestartsOfTheConsoleAndTheDaemon`).
  A verification that the stop interrupted leaves no card and no pin and is
  answered `request-invalid`; the lead must ask again, and the new request runs
  the verification from the start.
- OC23, the host's part: the full cycle and where the confirm code and the plan
  token are (`TestOwnerChannelOC23FullCycleAndWhereTheCodeAndTheTokenAre`).
- OC24, the journal and the store made to disagree (`TestOwnerChannelOC24TheJournalAndTheDoorDisagree`).
- OC27, the attribute cases (`TestOwnerChannelOC27AttributesOfTheTreeChangeNothing`).
- OC31, the outcomes of a verification, failed, stopped or cut, and the voiding
  of an approval by a later failed run (`TestOwnerChannelOC31VerificationOutcomes`).
  Not covered: the sandbox's reach (a suite that reads the lead's home, reaches
  the network or writes outside its checkout), because the suite is a fake.
- OC33, hostile commit headers (`TestOwnerChannelOC33HostileCommitHeaders`),
  and a commit in another encoding
  (`TestOwnerChannelOC33ACommitInAnotherEncodingIsRefused`). A feature commit
  whose `encoding` header names a charset other than UTF-8 is refused
  `request-invalid` with the line "a commit declares an encoding other than
  UTF-8", and no card: git would convert the author's bytes from the charset
  that header names when it prints the commit, so the owner would not be shown
  the bytes the lead stored, and the intake refuses such a commit (see
  [lead-intake.md](lead-intake.md)). The line is fixed and does not repeat the
  commit's id or the header's value. The lead commits the branch again in
  UTF-8 and asks again.

Covered only on the daemon side: OC13, who may send a decision frame, in
`ownerchannel_contract_test.go` and `owner_decider_linux_test.go` (a second
console, a client with no terminal, a client that does not name the console).
A holder attach connection and Remote Control as
a sender are not covered.

Not covered by the end-to-end test, only by the package tests of the part
concerned: bad or reused ids, bad feature names and extra fields (OC3, in the
record package's test vectors and the collector's tests in
`internal/approval/hostadapter`; that the card then shows only the host's plan
and token is not tested); hostile text and terminal answers at the confirm
field (OC10, OC11), in the console's tests; ask, read, withdraw, ask again
(OC28: the "differs from" block in the console's tests, and the
`request-duplicate` refusal only on the stand-in door, in
`internal/approval/door/doortest`; the real door's is not covered); one verification at a time
(OC14, the host's part, in `internal/approval/ownerchannel`; the real door's caps
and throttle are not covered); a clock moved back (OC22, in the same package's
restart test); the pin against a rewritten branch (OC25, in
`internal/approval/hostadapter`, where maintenance is run by hand on the test
mirror; the build has no maintenance entry point to refuse); the `odd-path` flag
(OC29, in the intake's tests and the host adapter's review test). For OC29's
registry edit between plan and use, only the stand-in door's refusal of a
changed rule-set digest at use is tested; an actual edit is not testable,
because the host reads the project's rules once at start, so it is not
covered.

Not covered at all, because they need a seated lead or the real door: the
sandbox cannot reach the socket, journal or store (OC1); `--approved` from a
managed seat (OC2); seat transfer with requests open (OC8); a drawn copy of the
card, which is the owner's judgement (OC12); the real door's caps and throttle
(OC14); a same-user process as the decider, which is the stated limit of this
build (OC20); requests across a handoff (OC26); and a member inside the lead's
sandbox (OC30). OC19 is withdrawn, and prune requests are out of scope (OC32).

What the tests found about the places of secrets:

- The confirm code appears in no place, on the wire or off it. The console draws
  it and keeps it in its model.
- The plan token appears in the host's own journal, only inside the one `decision`
  record, which repeats it so that a refusal can name the field that differs. The
  design's OC23 lists the places a sandbox can read (the reply, the request and
  the note in the drop folder, the intake's records and the mirror); the host's
  journal is not one of them, and none of those holds the token.
- The owner channel's verification runs the project's gates in the verification
  sandbox. It was never run for real: the tests use a fake runner.

## The client socket and protocol

The protocol is `ataniq.client/1`. [`internal/clientapi`](../internal/clientapi/doc.go)
holds both sides. The socket is `$XDG_RUNTIME_DIR/ataniq/daemon.sock`, in a
folder of mode 0700, with mode 0600. The socket is bound inside the 0700
folder and then set to 0600, so no other user can reach the file while it is
briefly wider.

Which transport the clients will use is an owner decision that is still open;
see [the client protocol](design/client-protocol.md). This socket is the
transport of the read-only skeleton and settles nothing about that decision.
The execution boundary a sandboxed lead needs is in
[the security contract](design/security.md). This guide does not restate
either.

### Who may connect

For every connection the server reads the peer's user id with `SO_PEERCRED`
before it reads a byte. When the id differs from the daemon's effective user
id, or cannot be read, the server answers `unauthorized` and closes.

**This does not tell the owner's console from any other unsandboxed process of
the same user.** Any such process can open the socket and is served. Version 1
accepts that by owner decision. Reading `hello` and `status` therefore tells
every process of the user that can reach the socket what is in the registry
summary. A managed lead that runs in a sandbox must never see this socket.
Keeping it away is the sandbox's job; nothing here asserts or tests it, and the
daemon launches no sandbox, except that in a build with the `ataniq_canary`
tag, under `--canary` or `--scratch`, the holder it starts launches one (see
[the canary slice guide](canary-slice.md) and
[the scratch leads guide](scratch-leads.md)). The profile in
[the lead sandbox guide](lead-sandbox.md) is an unqualified proposal that no
lead runs in, except there.

### Wire format

One JSON object per line, in UTF-8, each line ended by one LF. Unknown fields
in a request are ignored, including `params` on `hello` and `status`; a
top-level key that appears twice makes the request invalid. A connection may
carry several requests, answered in order.

A request:

```json
{"protocol":"ataniq.client/1","request_id":"r1","op":"hello"}
```

`request_id` is 1 to 64 printable ASCII characters, chosen by the client and
echoed. The answer to `hello`, in one line:

```json
{"protocol":"ataniq.client/1","request_id":"r1","ok":true,"result":{"protocols":["ataniq.client/1"],"limits":{"max_request_bytes":65536,"max_response_bytes":1048576,"idle_timeout_seconds":30,"max_connections":32},"operations":["hello","status"],"mutations":false,"version":"0.0.0-dev"}}
```

A request with `"op":"status"` gets an answer of the same shape. The result
below is one lead. The wire form is one line; it is broken across lines here
to read it, and `observed` is cut to its first member (the full object has six,
listed after the example). The values are synthetic. The daemon writes its
times as RFC 3339 in its own local offset (`2026-01-01T02:00:00+02:00` for a
daemon two hours east of UTC), with fractions of a second; they are shown in
UTC here, as the status text prints them:

```json
{"protocol":"ataniq.client/1","request_id":"r1","ok":true,"result":{
  "daemon":{
    "pid":4242,
    "started_at":"2026-01-01T00:00:00Z",
    "registry_revision":6,
    "boot_id":"00000000-0000-4000-8000-000000000000",
    "last_reconcile_completed":true,
    "last_reconcile_at":"2026-01-01T00:00:00Z"},
  "leads":[{
    "role":"demo/lead","kind":"lead","project":"demo","manager":"ataniq",
    "epoch":1,"desired":"running","state":"starting",
    "reason_code":"launch-intent","reason_detail":"",
    "evidence_kind":"ataniq-observation","since":"2026-01-01T00:00:00Z",
    "requested":{"provider":"claude","version":"2.1.0","model":"model-requested",
      "effort":"high","permission_mode":"default","access_mode":"workspace"},
    "observed":{"provider":{"observed":false}},
    "launch_intent":{"id":"00000000000000000000000000000001","epoch":1,
      "unit":"ataniq-lead-demo.service","outcome":"launched"}}]}}
```

`observed` has the members `provider`, `version`, `model`, `effort`,
`permission_mode` and `access_mode`. Each is `{"observed":false}` or
`{"observed":true,"value":"..."}`. A value nobody observed is never filled in
from the requested profile.

An entry of `leads` is a lead, the supervisor or a coordinator, told apart by
`kind`. A coordinator entry also carries `feature`, `parent` and `instance`,
and `parent_epoch` and `identity` once it has a launch intent; a coordinator
that was defined and never launched has no intent, so it has neither. They are
left out of the entry of a lead and of the supervisor. The shown states are
`desired`, `starting`, `ready`, `degraded`, `stalled` (a coordinator only),
`stopping`, `stopped`, `ended` and `unknown`. The wire allows `null` for
`last_reconcile_at`; this daemon always sends a time, the time its one reconcile
ended. Compare an `identity` whole; it is an identifier and never a path or
ref component.

The wire has no place for the proof text of a `stopped` or `ended` state, for
the unit evidence (invocation, control group and boot identity) or for the
launch intent's boot identity and times. They stay in the registry, so a client
sees `stopped` or `ended` without the proof it rests on.

An error:

```json
{"protocol":"ataniq.client/1","request_id":"r1","ok":false,"error":{"code":"unsupported-operation","message":"the operation is not supported"}}
```

### Limits

| Limit | Value |
| --- | --- |
| Request line | 64 KiB, without its LF. |
| Response line | 1 MiB, with its LF. A larger response is replaced by an `internal` error; the client refuses to read one. |
| `request_id` | 1 to 64 printable ASCII characters. |
| Connections | 32 at once. |
| Idle | A connection has 30 s to deliver a complete request line, counted from the end of the previous response; then it is closed without a response. |
| Write | 5 s for one response. |

`hello` advertises the request and response caps, the idle time and the
connection cap. The server bounds connections, request size, idle time and
write time. It does not bound what the handler does.

### Error codes

| Code | When | Connection |
| --- | --- | --- |
| `invalid-request` | Not UTF-8, not a JSON object, a missing or mistyped field, a bad `request_id`, a repeated key. | Stays open. |
| `too-large` | The request line is over the cap. | Closed. |
| `unsupported-protocol` | Another protocol name; the error lists the supported ones. | Stays open. |
| `unsupported-operation` | Any operation but `hello`, `status` and the operations of a registered extension (see below). | Stays open. |
| `unauthorized` | The peer's user id differs, or cannot be read. | Closed. |
| `busy` | The server is at its connection cap. | Closed. |
| `internal` | The handler failed or panicked, or the response would be too large. | Stays open. |

Messages are fixed texts and never echo what the peer sent. The one peer value
in a response is the `request_id`, and only when it is valid.

### No mutation

Version 1 is read-only. In every release build `hello` says
`"mutations":false` and lists its two operations. There is no command, no
command id, no approval and no event stream. Every other operation, whatever its
name, gets `unsupported-operation`. A version that adds commands to every build
would be a different protocol name.

The server can carry extension operations that the program serving it
registers (`Options.Extra`), each named with lower-case letters, digits and
dashes around one dot. No release build registers one; a build with the
`ataniq_canary` tag, under `--canary`, registers the five canary operations and
adds the error codes `refused` and `unavailable` (see
[the canary slice guide](canary-slice.md#the-commands)), and, under
`--scratch`, registers six more tagged operations, `lead.list`, `lead.define`,
`lead.start`, `lead.stop`, `lead.ticket` and `lead.inject`, and adds the error
code `conflict` (see [the scratch leads guide](scratch-leads.md#the-operations)).
The two modes exclude each other, and release builds still register none.
When one is registered,
`hello` lists the two base operations and then the extensions', and says
`"mutations":true` only when a registered extension declares that it changes
something. A request for such an operation may carry `params`, one JSON object
inside the same line cap, and passes the same peer check, caps, timeouts and
counters; a `params` that is not an object is `invalid-request`. An extension
may answer with codes of its own, never `unauthorized`, `busy`, `too-large` or
`unsupported-protocol`; a malformed error body from it is answered `internal`,
and so is a panic in it, also while its result is marshalled. The protocol
stays provisional.

## The registry

[`internal/leadregistry`](../internal/leadregistry/doc.go) is the daemon's
durable record of every lead role. The package doc is the reference for the
rules; this section says what they come to.

### The journal file

The record is one append-only file, `<state>/registry/registry.jsonl`, with one
JSON object per line. The first line is a header that names the format
(`ataniq-lead-registry`, version 1).

- **One writer.** `Open` takes an exclusive, non-blocking `flock` on the file,
  so one process writes at a time. This lock is the registry's own and is
  separate from the daemon lock. A second opener fails with `ErrLocked`.
- **One line per mutation, fsynced before the call returns.** The in-memory
  state is rebuilt only by replaying the file when it is opened. After a
  failed write or sync the failed record may or may not be on disk
  (`ErrBroken`); the registry must be reopened and read.
- **A hash chain.** Each line carries a SHA-256 over the previous line's hash
  and its own content. It finds accidental damage and a line that was changed,
  deleted or inserted before further lines. It has no key, so it does not stop
  someone who can write the file from rewriting all of it.
- **Bounds.** A line is at most 16 KiB; the registry holds at most 256 roles.
- **Refusals at open.** A symbolic link, a file the caller does not own, or
  wider permission bits than 0600 (0700 for the folder) fail with
  `ErrInsecure`. Damage other than an unfinished last line fails with
  `ErrCorrupt` and names the line; nothing is repaired, and the daemon exits 14.

Format version 1 has no compatibility promise until a first deployment: a rule
may be tightened so that a journal written by an earlier build is no longer
accepted, and there is no migration.

### Recovery and its limit

A last line without its newline is an append that never returned. `Open` cuts
it off and journals the cut in a `recovered` record (lines, bytes, offset).
Recovery reports every cut that no later `recovery-acknowledged` record
follows, also after a reopen. Only `AcknowledgeRecovery`, which takes a
required note, journals the acknowledgement. In this build the one caller is
the lifecycle's reconcile: it acknowledges a cut once it has acted on every
role, with the unit manager answering for each role it had to ask about. The
cut is **not** acknowledged, and is reported again after a restart, in each of
these cases:

- the registry failed, in the reconcile or in writing the acknowledgement
  itself (the daemon then exits with code 16);
- the reconcile's context ended before it had been through every role: its 30 s
  bound ran out, or the daemon was asked to stop;
- the unit manager did not answer for a role. A cut asks the manager about a
  role recorded `desired`, `stopped` or `ended`, and about a role with a
  pending launch intent; a failed `Inspect`, a control group that could not be
  read, or the systemd tools missing leaves such a role not acted on. A role
  that is shown unknown without a question, and one run by the tmux-manager,
  do not hold it back;
- the daemon ended or crashed between acting on the roles and writing the
  acknowledgement.

The next daemon that can act on every role acknowledges it. While a cut is
unacknowledged the daemon shows the reconcile as not completed.

A cut shows every role run by ataniq as unknown with the reason
`registry-recovered`, except a role recorded `desired`, `stopped` or `ended`
whose unit is proven absent (not loaded, with its control group proven empty or
the boot identity changed): that role is left as it is. A role run by the
tmux-manager is never touched. Acknowledging the cut does not resolve a role
shown unknown: that needs a stop with proof or the owner's decision (see
[`unknown`](#unknown-and-how-a-role-leaves-it)).

The limits, stated plainly:

- A cut anywhere in the file is read as an unfinished append. The committed
  records after it are lost, and their epochs may be given out again.
- A cut exactly at a line boundary leaves a valid shorter journal and is not
  detected or reported.
- The cut and its `recovered` record are written before a single sync. A crash
  between them can lose the record, and then the cut is not reported.
- The wire has no field for a recovery. While one is unacknowledged the daemon
  shows the reconcile as not completed and writes the reason to its log; a
  client cannot tell that apart from an unavailable manager by the daemon
  block. The roles themselves show the reason `registry-recovered`.

### What a record holds

For each role the registry keeps:

- the manager that runs it (`ataniq`, or the older `tmux-manager`, which the
  daemon does not touch);
- the **requested** profile: provider, version, model, effort, permission mode
  and access mode;
- the **observed** profile, field by field: a value, or explicitly not
  observed. The two are different types and nothing converts one into the other;
- the epoch, the desired state (`running` or `stopped`), and the shown state
  with its reason code, reason detail, evidence kind and time, and a proof text
  where one is required;
- the current launch intent (with the boot identity it was made on) and the
  unit evidence of the current epoch (invocation id, control group path and
  identity, boot identity).

### Epochs and the launch intent

Every launch takes the next epoch. Epochs only grow and are never reused,
whether the launch failed or not and also after a reopen (apart from the loss a
cut can cause, above). A call that names another epoch than the role's current
one is refused as stale.

The launch intent (a new epoch, a random 128-bit intent id, the unit name and
the boot identity) is **journaled before any unit call**. A launch is admitted
only from the shown states `desired`, `stopped` or `ended`, with no intent
pending. The intent stays `pending` until an outcome (`launched`, `adopted`,
`failed` or `abandoned`) is recorded; while it is pending, `stopped`, `ended`
and an owner decision are refused.

### Shown states and the transition table

The shown states are `desired`, `starting`, `ready`, `degraded`, `stalled`,
`stopping`, `stopped`, `ended` and `unknown`, each with a reason code and a
detail. `starting` is never set by a state change: only a launch sets it. The
table is the rule for a state change, from the row to the column. The
[package doc](../internal/leadregistry/doc.go) is the reference.

| from \ to | desired | ready | degraded | stalled | stopping | stopped | ended | unknown |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| desired | Y | Y | Y | N | Y | P | P | Y |
| starting | N | Y | Y | C | Y | P | P | Y |
| ready | N | Y | Y | C | Y | P | P | Y |
| degraded | N | Y | Y | C | Y | P | P | Y |
| stalled | N | Y | Y | N | Y | P | P | Y |
| stopping | N | Y | Y | N | Y | P | P | Y |
| stopped | Y | Y | Y | N | Y | P | P | Y |
| ended | Y | Y | Y | N | Y | P | P | Y |
| unknown | N | N | N | N | N | P | P | Y |

- **Y**: admitted.
- **P**: admitted only with a non-empty proof text, with evidence other than
  `owner-decision`, and while the launch intent is not pending. With a pending
  intent it is refused, and the caller records the outcome first.
- **C**: admitted for a coordinator, refused for every other role.
- **N**: refused.

So `stopped` and `ended` are only ever recorded with proof, and a role shown
`unknown` can leave it only to `stopped` or `ended` with proof, or by an owner
decision. The registry never calls systemd and never looks at a process, a
control group or a lock: the caller supplies the unit evidence and the proof
text, and they are stored as given.

### `ResolveUnknown`

`ResolveUnknown` is the owner's decision on a role shown `unknown`. It is the
only way besides a proven stop to leave `unknown`. It moves the role to
`stopped` or `ended` only, from `unknown` only, with a required decision text,
and shows the evidence kind `owner-decision`. It is journaled as its own record
kind. A state change cannot carry owner evidence for `stopped` or `ended` from
any state, so the owner's word enters the journal only this way. The same rule
about a pending intent applies. **No command and no protocol operation of this
build reaches it.**

### Why its own file

The registry is its own file and not part of the dispatch journal's database
(the SQLite store in `internal/journal`, described in
[the journal design](design/journal.md)). The two share no code: the registry
imports no other package of the repository than the role package. They differ
in what they are:

- The dispatch journal is a SQLite database for attempts, grants and receipts,
  recognised by its exact schema, with a configured authority, and never
  migrated on open.
- The registry is a line-oriented JSON file with a hash chain and its own
  record kinds, replay rules and recovery rule, as described above.

The consequence is that no transaction spans both: a lead launch and a dispatch
attempt are not committed together. The packages do not record a separate
decision for this split, and [the journal design](design/journal.md) does not
cover the lead registry. Treat the split as the current state of the code, not
as a settled architecture.

## Roles

[`internal/leadrole`](../internal/leadrole/doc.go) defines three roles. A role
is a name with a fixed mapping to a unit name and a lock path. It proves
nothing: not that a unit exists, not that a lock is held, not that anyone may
act as the role.

| Role | Canonical string | Unit | Role lock |
| --- | --- | --- | --- |
| Project lead | `<project>/lead` | `ataniq-lead-<project>.service` | `<state>/roles/<project>/lead.lock` |
| Supervisor | `supervisor` | `ataniq-supervisor.service` | `<state>/roles/supervisor.lock` |
| Coordinator | `<project>/coord/<feature>` | `ataniq-coord-<project>_<feature>.service` | `<state>/roles/<project>/coord-<feature>.lock` |

A project and a feature are 1 to 63 characters from `a-z`, `0-9` and hyphen,
starting and ending with a letter or digit. An underscore cannot occur in
either, so the coordinator's unit name is unambiguous.

A coordinator is the role of one feature of one project. At most one exists per
feature. Its record carries:

- the **parent**: the lead of its project, and the parent's epoch at launch;
- the **feature**;
- the **launch profile**: runtime and model, `claude` and `fable` when left
  empty; the other profile fields are optional;
- the **instance number** `n`, which is the coordinator's epoch: it grows by
  one for every launch and is never reused;
- the **identity** `<project>-lead@e<parent epoch>:coord:<feature>:<n>`, for
  example `demo-lead@e1:coord:feat-x:1`. It is an identifier only: never a path
  or ref component, compared whole or segment by segment, never by prefix;
- the shown states of a lead, and `stalled`.

**A coordinator is not a lead.** It has no seat, gives no approval and is no
handoff target. Nothing here gives it authority. These are records and
interfaces, exercised by tests against a fake.

The registry enforces two rules about them today. A coordinator is launched
only under a parent lead that is defined, run by ataniq, shown `starting`,
`ready` or `degraded`, and whose own launch did not fail and was not abandoned.
And a new epoch of a lead is refused while a coordinator recorded under an
earlier epoch of that lead is not stopped, ended or never launched. The
registry does not stop a coordinator when its parent's epoch ends; it keeps the
record and refuses those launches. What the lifecycle does with coordinators
(it starts one under a live parent, stops a lead's coordinators with the lead,
and ends one with its parent's epoch) is in
[Coordinators in the lifecycle](#coordinators-in-the-lifecycle).

## Locks

[`internal/leadlock`](../internal/leadlock/doc.go) holds two kinds of lock: the
daemon lock, `<state>/daemon.lock`, and a role lock for each role. Both are the
same mechanism: `flock(LOCK_EX|LOCK_NB)` on a regular file of mode 0600 in a
folder of mode 0700. The lock is the `flock` and nothing else. It belongs to an
open file description, so it is held as long as a descriptor of it is open
anywhere; the kernel releases it when the last one closes, a SIGKILL included.
The file is opened close-on-exec, and is never removed.

Only the daemon's own locks are taken today: the daemon lock and the runtime
folder lock, which use the same mechanism. No role lock is taken through this
package: the daemon starts no holder, so nothing takes one (in a build with the
`ataniq_canary` tag, under `--canary`, it starts one, and that holder takes the
role's lock itself; see [the canary slice guide](canary-slice.md)). The
scratch-only holder of [the lead holder guide](lead-holder.md) holds a `role.lock` in its
own scratch folder, taken by its own code and not at a role's path. The daemon
never probes a role lock by taking and releasing it, and the package offers no
such call.

The daemon takes two locks, which exclude different things:

- `<state>/daemon.lock` enforces **one daemon per state folder**. It is taken
  first and released last.
- `<runtime>/daemon.sock.lock` enforces **one daemon per runtime folder**. The
  daemon takes it right after the daemon lock, before it opens the registry,
  and holds it until after the listener and the registry are closed, so a
  second daemon cannot remove the socket of a live one. A second daemon on the
  same runtime folder exits with code 17 and a message that another daemon
  serves the folder, and it is refused before it reconciles anything.

At shutdown the locks are released in the reverse order of taking, the registry
being closed before the runtime lock. After exit code 19 none of the three is
released: the registry and both locks are left to the end of the process.

Neither lock covers two daemons of one user that have different state folders
**and** different runtime folders. Nothing prevents that, and they would share
unit names (see [the limits](#what-proven-empty-means)).

**The pid text is a hint for display.** After the lock is taken, the holder
writes its pid as decimal text and a newline. That text is written after the
lock, so a reader can see an empty or stale value in the window around an
acquire. A pid says nothing about a process that has exited and had its pid
reused. It appears in the message of a second daemon so that an operator knows
whom to ask; it is never proof that a process holds the lock and never a way to
authenticate a peer.

## Units and the replacement rule

[`internal/leadunit`](../internal/leadunit/doc.go) defines the manager:
`Start`, `Inspect`, `Signal` (to the whole control group of one invocation) and
`Empty` (true only on positive proof). [`systemdmgr`](../internal/leadunit/systemdmgr/doc.go)
is its implementation for the systemd user manager, and
[`internal/leadlifecycle`](../internal/leadlifecycle/doc.go) is the logic on
top. **The daemon calls none of `Start`, `Signal` or the lifecycle's `Start`
and `Stop`,** except that in a build with the `ataniq_canary` tag, under
`--canary`, the operations `canary.start` and `canary.stop` call the
lifecycle's `Start` and `Stop` for the config's one lead role (see
[the canary slice guide](canary-slice.md)). What follows is what the packages do
and what their tests check against the fake.

The scratch holder's unit is not one of these. `ataniq-scratch` starts its
holder in a transient user unit `ataniq-scratch-NAME` with `systemd-run`, not
through this manager, and that name is no role's unit name (see
[the lead holder guide](lead-holder.md#unit-mode-and-direct-mode)).

### Properties of a unit

Every unit is a transient user unit with these fixed properties: `Type=exec`,
`CollectMode=inactive-or-failed`, `KillMode=control-group`, `Restart=no`
and `TimeoutStopSec=2s`. A lead unit has no `NoNewPrivileges` on purpose: with it,
the command that pasta runs in its user namespace keeps no `CAP_SETUID`, so the
nested bubblewrap cannot set up its uid map and the sandbox start is refused.
Bubblewrap sets no-new-privs on the agent it execs, as observed with
bubblewrap 0.13.1; no probe assertion checks that yet, and a later one should.
The description is
`ataniq lead epoch=<n> intent=<id>`, and a reader gets the epoch and the intent
back from it. The command is an absolute executable with its arguments, a
working directory and an explicit environment. The manager sends the command
with systemd's no-expand flag so that `$VAR` in an argument is not expanded.

A unit specification may also carry three **limits**, `MemoryMax=`,
`MemorySwapMax=` and `TasksMax=`. They come from configuration, never from a
value in the code, and are absent when none is given: a specification with no
limits sets none of the three and its start call is the call without them.
When limits are given all three are set, each as an unsigned 64-bit number on
the bus, and a swap limit of 0 means no swap (`MemorySwapMax=0`), not "not
set". The specification refuses a memory limit outside 256 MiB to 64 GiB, a
task limit outside 16 to 65,536 and a swap limit outside 0 to 64 GiB, and
`Limits.Validate` asks the same question of limits alone. The fake records the
limits of a unit it starts.

The lifecycle's options have an optional function that gives the limits of a
role. Start asks it once, after the preflight and before the registry changes,
checks the answer with `Limits.Validate` and puts a copy of it in the unit
specification. With no function, or an answer of nil, the unit has no limits.
Limits outside the bounds are refused like a preflight refusal: nothing is
changed, no epoch is used, no launch intent is journaled and the error wraps
`ErrInvalidSpec`.

A unit specification may name a parent unit. The unit is then also **bound** to
it with `BindsTo=`, `PartOf=` and `After=`, so that stopping the parent stops
it, and the fake and the systemd manager both do this. It is how a
coordinator's unit is bound to its lead's unit: the lifecycle sets the parent
when it starts a coordinator (see
[Coordinators in the lifecycle](#coordinators-in-the-lifecycle)). What systemd
does with these three, in particular for a start under a parent that is
missing or deactivating, is not confirmed (see
[the qualification harness](#the-qualification-harness)).

### Start

A start is one request, **never retried**. Before anything is journaled, the
replacement rule applies: the unit name must not be loaded, and the control
group of the previous epoch must be proven empty (a changed boot identity is
proof in itself). Without that proof nothing starts: the start is refused and
the role is left as it is. Only then does the lifecycle journal the launch
intent, with the boot identity when the manager reported one, and call `Start`
once. It never waits for readiness, and it never sets `ready`. (In scratch
mode a background observer sets `ready` afterwards, on the holder's word: see
[the scratch leads guide](scratch-leads.md#ready).)

A reply that is lost is its own error. The caller inspects and does not start
again. A unit with exactly this epoch and intent is adopted. Otherwise the
intent stays pending and the role is shown `unknown` with the reason
`launch-reply-lost`. Finding the unit not loaded is not final at once: a request
already on the bus can still be executed a little later.

### Stop

Stop sets the desired state to stopped and sends SIGTERM to the control group
of the invocation it recorded. It waits 2 s for the group to empty, sends
SIGKILL if it did not, and then waits up to 5 s for the proof: the unit not
loaded (or loaded with another invocation) and the control group of the stopped
invocation proven empty. With the proof the role is `stopped`, with a proof
text naming the invocation and the group. Without it by the bound, the role is
`unknown`. Stop never types into a session. A unit that is loaded and is not
the role's recorded one is never signalled, except the stale unit described
below.

### What "proven empty" means

`Empty` is true only on positive proof, in the cases listed in
[`systemdmgr`](../internal/leadunit/systemdmgr/doc.go):

- the unit is not loaded, or has another invocation in a control group of
  another identity, and the control group the state names is gone or has
  another identity;
- the control group the state names has the same identity and holds no process
  (for a loaded unit that further needs the same invocation and a state of
  inactive, failed or deactivating);
- for a state with no invocation, the unit is not loaded and no directory of
  the unit's name exists below the user manager's control group.

Everything else is false, and a new invocation alone is never proof. The limits:

- **It proves the unit's own control group empty, nothing more.** A process of
  the unit can move itself into another control group of the user's delegated
  subtree before its own group is removed. Nothing here sees it. Whoever relies
  on `ended` or `stopped` must know that.
- **An answer is a fact about the instant it was read.** A process can enter
  later.
- **The description is the identity.** A unit is "ours" only because its
  description has the exact form with an epoch and an intent. The check also
  compares the recorded invocation, but systemd does not vouch for the
  description.
- **One installation per user is assumed.** Unit names do not include the state
  folder or the runtime folder. One daemon per runtime folder is enforced by
  `daemon.sock.lock`, and one daemon per state folder by `daemon.lock`. Two
  daemons of one user with different state folders **and** different runtime
  folders are not prevented. They would use the same unit names and would not
  exclude each other. Nothing checks this.
- The control group file system is read at the fixed mount `/sys/fs/cgroup`;
  nothing proves that it is the manager's own beyond the identity checks.

### `unknown` and how a role leaves it

`unknown` means the daemon cannot say what a role is doing, and it is the
conservative answer. Nothing starts for a role shown `unknown`. A role leaves it
only through a stop that ends with the proof (`stopped`), or through the owner's
`ResolveUnknown`. A role that was shown `unknown` because the registry's
journal was cut is never resolved by a reconcile, since the registry may have
lost what happened to it; the owner decides, or a stop moves it on. A running
unit of ours that matches the journal is adopted, but a role shown `unknown`
stays `unknown` until it is stopped with proof or the owner decides, because
the registry's table does not let it return to a live state.

### Reconcile and what starts by itself

Reconcile compares the registry with the manager for every role the daemon
runs, and **starts nothing and signals nothing**, ever. A role run by the
tmux-manager is never touched; the report says it was not managed. After a
daemon restart:

- a running unit of ours whose epoch and intent equal the journal's is adopted,
  and the shown state is kept, never raised to `ready`;
- a unit that is another epoch, another invocation, not ours, or one whose
  intent was abandoned or failed, is reported (`stale-unit`, `foreign-unit`)
  and the role is `unknown`; it is never signalled;
- a unit that is gone is resolved when it can be proven: `stopped` with the
  reason `host-rebooted` when the boot identity changed, `ended` with the
  reason `holder-gone` when the control group of the record is proven empty on
  the same boot, and `unknown` when it cannot be proven;
- a loaded unit for a role shown `desired`, `stopped` or `ended` is a competing
  or leftover unit, and the role is `unknown`;
- when the manager does not answer, nothing changes for the role and the report
  says unavailable, since a role that runs should not look lost because the
  daemon could not ask;
- after a cut of the registry journal, every role run by ataniq is shown
  `unknown` with the reason `registry-recovered`, except a role recorded
  `desired`, `stopped` or `ended` whose unit is proven absent, which is left as
  it is. A pending launch intent of a role shown `unknown` for the cut is still
  settled: adopted when its unit runs, abandoned at once when the boot changed
  (see [the late-start window](#the-late-start-window)), else abandoned once
  the late-start window has passed. The role stays `unknown` in every case.
  Once reconcile has acted on every role it acknowledges the cut; in the cases
  where it does not, the cut is reported again after a restart (see
  [Recovery and its limit](#recovery-and-its-limit)).

The daemon shows the reconcile as not completed when the unit manager is known
to be unavailable (also with an empty registry), when any unit-manager call
failed during it, when the manager did not answer for a role, when it ran out
of time, and while a cut is unacknowledged.

After a reboot every unit is gone, so reconcile shows a role that was live as
`stopped`, with the reason `host-rebooted`. **No lead starts by itself, after a
reboot or ever:** nothing in this build starts a unit, and there is no boot
start.

### The late-start window

When a start's reply is lost and no unit is loaded, the intent is not closed at
once, unless the boot changed (below). Reconcile waits until the **late-start
window** (30 s) and a margin (10 s) have passed since the registry stamped the
intent. Then, with the unit still not loaded and its control group empty, it
closes the intent as abandoned and records the role as stopped, with a proof
text naming the facts used.

The launch intent carries the boot identity the manager reported when the
launch was journaled. A lost launch is settled at once, without the window,
when all of these hold: no unit is loaded, the intent has a boot identity, the
manager reports one now, and the two differ. Nothing that was on the bus
before a reboot can land after it. An intent with no boot identity (the manager
gave none), a manager that reports none now, or the same boot leave the
decision to the window alone. For a role shown `unknown` for a cut of the
registry, a changed boot closes the intent in the same way, and the role stays
`unknown`.

The daemon reconciles at start, and in scratch mode settles pending launches at
their deadline. An intent that is still inside its window at the reconcile
leaves the role `unknown` with the reason `launch-unsettled`, and the reconcile
is shown as completed. A role keeps the reason it already had when it was shown
`unknown` with `launch-reply-lost`, or for a cut of the registry with
`registry-recovered`; only another role is given `launch-unsettled`.

In scratch mode (`ataniqd run --scratch`, a build with the `ataniq_canary` tag)
the daemon settles that intent itself, with no restart. For each role shown
`unknown` with the reason `launch-unsettled` or `launch-reply-lost` and its
intent pending, found when it starts serving or left by a start of its own that
lost its reply, a timer waits until the intent's deadline (the intent's time,
the window and the margin: 40 s) on the clock the registry uses. It then asks the
lifecycle to settle that one role (`SettleLaunch`), which runs the code of the
reconcile for it and no other rule: the same checks (no unit loaded, its
control group proven empty, the window and margin passed; a changed boot
identity settles at once) decide, and with them the intent is abandoned and the
role recorded `stopped`, with the proof, so that it leaves the `max_running`
count and may be started again, with the next epoch. A role shown `unknown` for
`registry-recovered` has no timer and is left to the owner.

What the timer does when it cannot settle:

- **A unit is loaded at the deadline.** The role is not settled to `stopped`.
  A unit with this epoch and intent is adopted (the intent is closed as
  `adopted`), another unit is reported (`stale-unit`, `foreign-unit`) and the
  intent is closed as `abandoned`; in both cases the role stays `unknown`,
  nothing is signalled, and an explicit stop is the way out, as before. A
  request that lands after the window is the stale-unit path above and is never
  signalled by the timer.
- **The manager does not answer, or the control group is not proven empty.**
  The role is not settled: when the manager does not answer nothing changes;
  when the control group is not proven empty the role stays `unknown` and its
  reason becomes `control-group-not-proven-empty`, which keeps the timer armed
  (it is one of the reasons that arm it, while the intent is pending). The timer
  tries again after 2 s, then at doubling waits up to a minute, until the intent
  is settled or the role no longer shows it. A repeated try with the same reason
  writes nothing to the registry, so the retries do not grow its journal; each
  try is a line in the daemon's log.
- **The role's operation lock is held** (a start or a stop of the role is
  running) or the role changed (another epoch, a proven stop): the timer waits
  and tries again, or ends. It never waits for the lock, and holds it only for
  one read of the registry, never across a call to the manager; the
  lifecycle's own lock for the role keeps its settle from overlapping a start or
  a stop, and a start is admitted only from `stopped`, `ended` or `desired`, so a
  new unit can follow only a settle that recorded the role `stopped`.

A timer ends when the intent is no longer pending, on a proven stop, on a new
start of the role, and at shutdown, where the daemon joins it. It also ends when
the lifecycle answers that the reconcile at start did not act on the registry's
cut journal (a reconcile that ran out of time or failed for a role during a
cut): nothing in a running daemon reconciles again, so no later try would be
answered otherwise. The daemon logs one line, naming the restart that settles
the role, and the role stays as it is shown. The daemon that is not in scratch
mode has no timer, and the settle still needs a restart.

**The window is a heuristic and not a bound.** The manager's call timeout limits
how long the caller waits for a reply, not when systemd executes a request that
is already on the bus. A request can land after the window, even after a newer
epoch has run. The window and the registry's clock must be one time source, and
a step of the wall clock (a time adjustment, a suspend and resume) can close it
early or hold it open; the margin covers a small step only.

The **stale-unit path** is what handles a late landing. Reconcile reports a
unit of ours that landed after its intent was abandoned, and never signals it.
An explicit stop ends a unit of ours that has the record's epoch and intent or
an older epoch, since epochs only grow and an older one cannot be newer work.
A unit of a newer epoch, of the same epoch with another intent, or that is not
ours is never signalled.

A request of an abandoned launch can also land after the timer has settled the
role to `stopped`. Until then the role is not told of it: the list shows the
role `stopped` (`launch-abandoned`) and does not count it under `max_running`
while a unit of the old epoch runs. A start of the role is refused
(`unit-loaded`, nothing is started or signalled) because the unit's name is
loaded, and it changes nothing the list shows. The role is shown `unknown`
(`stale-unit`) when the daemon is restarted and the reconcile finds the unit,
and an explicit stop ends the unit and records the role `stopped`.

### Coordinators in the lifecycle

`internal/leadlifecycle` handles coordinators in start, stop, the stalled
state and reconcile. A coordinator is a role of its own there, with its own
unit, epoch (the instance number) and evidence. What it relies on:

- the registry: the coordinator record, the instance number, the shown state
  `stalled`, and the parent rules (a coordinator is launched only under a live
  parent, and a new lead epoch is refused while a coordinator of an earlier one
  is not proven stopped);
- the role package: the role name, the unit name, the lock path and the identity;
- the unit specification: the parent unit and the bound properties `BindsTo=`,
  `PartOf=` and `After=` (see [Properties of a unit](#properties-of-a-unit));
- the fake, and the systemd manager's rendering of those properties;
- the status protocol: the entry of a coordinator and its members.

What the lifecycle does with a coordinator, as the code and its tests against
the fake have it:

- **Start** of a coordinator checks its parent first, so that a refusal
  changes nothing: the lead must be defined, run by ataniq, shown `starting`,
  `ready` or `degraded`, and its own launch must not have failed or been
  abandoned. Otherwise the error wraps `ErrParentNotLive`. The unit is started
  with the lead's unit as its parent, so that it is bound to it. The rest is as
  for a lead: the replacement rule on the coordinator's own unit and evidence,
  the intent, the single request, and the outcomes. A start that the manager
  accepts but that never loads a unit, as when the parent unit is no longer
  live, is a start that failed: the role is `stopped` with the proof of the
  absent unit, or `unknown` without it. A refusal that names the missing parent
  is treated as a lost reply, because the real manager cannot tell it from a
  request that may still land: the intent stays pending and the role is
  `unknown` until a reconcile or a stop proves the absence. A lead is not
  started while a coordinator recorded under an earlier epoch of it is not
  proven stopped (`ErrCoordinatorNotStopped`).
- **Stop** of a coordinator stops only that coordinator, by the sequence of a
  lead's stop. The unit is bound to its lead's, so it may already be stopping
  when it is signalled; the manager's answers that the unit is not loaded or
  has another invocation lead to the proof as for a lead.
- **Stop** of a lead stops each of its coordinators that is not proven
  stopped (one run by ataniq and launched), one after the other, each by the
  same sequence with its own quiesce, and records each in the registry. Then
  it stops the lead, whatever became of them, and then lists the coordinators
  once more, because one may have been started in between: the lead's lock
  does not serialise a start of its coordinator, but once the lead has left its
  live states the registry admits no new one. A coordinator that cannot be
  proven stopped is shown `unknown`, or is left as recorded when the manager or
  the registry did not answer its stop, and is named in the error (a
  `*CoordinatorsError`, which wraps `ErrCoordinatorNotStopped`). The lead is
  stopped all the same, and a later start of the lead is refused until the
  coordinator is stopped with proof or the owner decides. Once the lead is
  proven stopped, the second listing runs on a context of its own, which the
  caller's cannot end, so the result is nil only when that listing was done.
  When the lead's own stop failed, the result is an error in any case. A
  listing that fails partway, because the caller's context ended or the
  registry failed, does not drop the coordinators it had already named: the
  error still holds them.
- **Reconcile** judges a coordinator as it judges a lead, and also against its
  parent, which it settles first in the same call. When the parent's epoch is
  not the one the coordinator recorded, or the parent is not live, the
  coordinator has ended with its parent: with its unit proven absent it is
  `ended` with the reason `parent-epoch-ended` and the proof; with its unit
  still loaded it is reported `orphan-coordinator` and shown `unknown`, because
  reconcile signals nothing and an explicit stop ends it. A parent that cannot
  be read from the registry gives "cannot tell": the coordinator is left as
  recorded (reason `parent-unreadable`) and reconcile returns the registry's
  error. After a reboot a coordinator is `stopped` as a lead is. The report
  carries the kind of each role and, for a coordinator, its feature and parent.
- **`MarkStalled` and `ClearStalled`** record and clear `stalled`. Marking is a
  record of an observation that a detector outside the package supplies; the
  method is the whole interface. Clearing judges the parent first, as reconcile
  does, and then returns the coordinator to the live state it was stalled from
  (`ready` or `degraded`; `degraded` for one that was `starting`) only when the
  unit is loaded, ours and running with the recorded epoch, intent and
  invocation; otherwise the coordinator is shown `unknown`. A stalled
  coordinator is never replaced by reconcile or by start: it is stopped, with
  proof, and then started again as the next instance. After a daemon restart
  reconcile keeps a stalled coordinator stalled while its unit is live and ours.

A coordinator has no seat and gives no approval. The daemon calls none of the
above but the reconcile, once at start-up; it has no stall detector either (see
[Not built](#not-built)).

## Runtime requirements

These are runtime requirements of the daemon, checked when it runs, and not
developer tools. `make doctor` does not check them, and
[the development environment](../AGENTS.md) lists the developer prerequisites.

- Linux. The daemon runs on Linux only. For another system `ataniqd` builds as
  a stub that prints one line and exits 1, and the packages of this guide
  compile without their Linux files. What compiles there is what no system
  decides, such as the socket name and path, the default folders and the exit
  codes; the daemon, the listener and the server, the registry, the locks, the
  lifecycle and the systemd calls are in the Linux files. No test and no CI job
  builds for another system.
- A systemd user manager of the calling user, with `busctl` and `systemctl`.
  The manager finds them in `/usr/bin`, `/bin`, `/usr/sbin` and `/sbin`, never
  through `PATH`, runs them with a fixed environment (the user manager's runtime
  folder and bus address derived from the real user id), and cuts each call
  off after 5 s. Starting a unit uses `ExecStartEx`, which the package
  documentation says needs systemd 243 or newer; that is from the
  documentation and not from a run. Without the tools the daemon still serves;
  the reconcile is shown as not completed, also when the registry holds no
  role, since the manager is known to be unavailable.
- A unified control group hierarchy (cgroup v2) mounted at `/sys/fs/cgroup`.
- The kernel's boot identity, `/proc/sys/kernel/random/boot_id`. This is all the
  daemon takes from the `pidfd` package today. That package also opens a pidfd
  with `pidfd_open` and reads process start times, which need a kernel that
  has the call; nothing in the daemon uses them yet.

The daemon refuses to run as root.

## The qualification harness

`leadunit-qualify` is the explicit, opt-in harness for the real unit manager.
The lead runs it by hand. **No test and no make target runs it.** Build it with
the repository's Go wrapper:

```sh
mkdir -p build
sh scripts/go-tool.sh build -o build/leadunit-qualify ./qualification/leadunit/cmd/leadunit-qualify
./build/leadunit-qualify
```

The limits come from the flags `--memory-max` (default 1G), `--swap-max`
(default 0, no swap), `--tasks-max` (default 128) and `--memory-lowered`
(default 768M, the value the running unit is lowered to), whose defaults the
help states; a value outside the bounds of the unit specification is a usage
error, and so is a memory or swap size that is not a whole number of 4096-byte
pages, because the kernel keeps such a limit as whole pages and the control
group files are compared with the limit exactly, with no direction of rounding
assumed. The harness's checks of them read the host only through two seams, one
that runs `systemctl` and one that reads files and folders, which every test
replaces with a fake, so that no test runs `systemctl` or reads a control
group. Each check is a named line in the report.

Without `--run` it prints what it would do and exits 0, without touching
systemd. With `--run --state-dir DIR` (an existing folder of the caller, mode
0700) it starts one transient user unit with a unique name,
`ataniq-qualify-<random>.service`, never the name of a role, and checks the
manager against it:

- the start, and the description read back;
- the arguments reach the unit unchanged, one with `$HOME` and one with `%h`
  among them;
- a second start with the same name is refused;
- `Inspect` matches the start;
- the unit's memory, swap and task limits, set when the unit was created, read
  back with `systemctl --user show` (normalised from `infinity`, size suffixes
  and counts to bytes and tasks) and from `memory.max`, `memory.swap.max` and
  `pids.max` in the unit's control group folder, each compared with the
  specification exactly;
- the memory limit lowered once with `systemctl --user set-property --runtime`
  and read back both ways;
- while the unit runs, after that, `systemd/transient` or `systemd/user.control`
  of the user's runtime folder holds the drop-in folder `<unit>.d` that
  `set-property` wrote (the check `runtime-folders-hold-the-unit`, which fails,
  and so skips the check after the stop, when neither does; the unit's own file
  does not count), so that the check after the stop has something to find gone;
- a second unit bound to the first (`BindsTo`, `PartOf`, `After`) starts, and
  `Inspect` reads the first unit back as its parent;
- SIGTERM to a process that ignores it leaves the control group populated;
- after 2 s, SIGKILL empties it, `Empty` becomes true and the unit is not
  loaded;
- the bound unit stops with its parent, with no signal sent to it, and `Empty`
  is true for its group;
- nothing for either unit remains under `systemd/transient` and
  `systemd/user.control` of the user's runtime folder after the stop, and the
  check names the entries that were seen while the unit ran and are gone;
- `Empty` is true for a fresh unit name that was never used.

It prints one JSON report with a line per check and a verdict, and always
cleans up its own units: it signals only a unit whose description is this
run's own epoch and intent. It exits 0 on a pass, 1 on a failed check, 2 on a
usage error and 3 on a runtime error, and gives up after 90 s. It needs the user
manager of the calling user. It does not simulate a lost start reply, a crash of
the daemon, a reboot, a unit replaced under the same name, or a populated group
that SIGKILL cannot empty.

**A passing run would say that these checks held once, on one host, for one
unit, at one systemd version.** It would not qualify the manager in general.
This guide records no run and claims no result.

What only a real run can confirm:

- the output formats and the error texts of `busctl` and `systemctl`, which the
  manager parses. The two refusals it recognises from text (an unavailable
  manager and a unit name already taken) fall back to a lost reply when the
  text changes, which is the safe direction;
- `ExecStartEx` with the no-expand flag and literal arguments, including the
  `--` that ends `busctl`'s options before the verb;
- where a transient user unit is placed in the control group tree;
- when a bound unit stops: when its parent turns deactivating or when it
  unloads;
- what a start does under a parent that is missing or deactivating;
- what `PartOf=` does on a restart of the parent;
- that `BindsTo` is printed as one space-separated list;
- how long a failed unit stays loaded before it is collected;
- that the manager's limit properties (`MemoryMax`, `MemorySwapMax` and
  `TasksMax` as unsigned 64-bit numbers) are accepted by the user manager, that
  the control group files carry them (which needs the memory and pids
  controllers delegated to the unit), how `systemctl show` prints them, and
  where `set-property --runtime` writes on a transient unit and whether the
  stop removes it;
- that a unit gets, besides the explicit environment, only what systemd adds
  (`HOME`, `LOGNAME`, `USER`, `SHELL`, `INVOCATION_ID` and the like).

## Not built

The daemon and the packages of this guide have none of the following, except
in a build with the `ataniq_canary` tag, under `--canary`, which joins some of
them for one throwaway role (see [the canary slice guide](canary-slice.md); each
item says where it differs). Where another part of the tree has provisional
code for an item, the item names its guide; the daemon uses none of that code
without the tag and the flag.

- a holder that the daemon starts, and the command line for it. `ataniq-holder`
  refuses to run without `--scratch`, and `ataniq-scratch` is what starts it
  (see [the lead holder guide](lead-holder.md)); in canary mode the daemon
  starts `ataniq-holder --canary`;
- a sandbox for a lead: the daemon launches none. The profile of
  [the lead sandbox guide](lead-sandbox.md) is an unqualified proposal; in
  canary mode the holder launches it, with assertion 10 waived;
- readiness: nothing sets `ready` in this skeleton; for scratch mode see
  [the scratch leads guide](scratch-leads.md#ready);
- delivery of instructions and the wait for an idle turn, and the quiesce of
  delivery before a stop (the interface exists, nothing implements it). The
  library of [the lead injection guide](lead-injection.md) is not joined to
  the daemon; in canary mode the daemon sends one message per epoch through
  the holder, with no queue;
- tickets: `ataniq-scratch` issues the attach tickets of a scratch holder; in
  canary mode `canary.ticket` issues them;
- the exchange mirror and the intake: `ataniq-intake` is run by hand (see
  [the lead intake guide](lead-intake.md)), and the daemon does not call it,
  canary mode included;
- any mutating command: nothing defines a role, starts, stops or resolves an
  unknown role. The one write the daemon makes is the reconcile's: it records
  what it finds about a role, and acknowledges a registry recovery. In canary
  mode `canary.define`, `canary.start` and `canary.stop` define, start and stop
  the config's one role;
- the assignment record and Qargi's side of it;
- observation of the tmux-manager's leads;
- start at boot, and a unit file for the daemon itself: `ataniqd run` is
  started by hand;
- a coordinator's holder and sandbox;
- a caller of the lifecycle's coordinator handling: nothing in the daemon
  calls `Start`, `Stop`, `MarkStalled` or `ClearStalled`, so it starts, stops,
  marks and clears no coordinator; only its one reconcile at start-up runs;
- the stall detector: the lifecycle's `MarkStalled` is an interface that a
  detector would call, and nothing calls it;
- a coordinator's bound assignment file, Qargi's delegation record of it and
  its adoption;
- a periodic reconcile.

A start also needs a qualification record and a command line for the holder.
The daemon passes a check and a command builder that always refuse, and they are
never reached because nothing calls start. In canary mode the daemon passes the
canary preflight, which passes for the config's role only and records that
there is no qualification record, and the canary holder command; `canary.start`
calls start.
