# Seat gate: operator guide

This guide is for the owner who sets up the phone approval path: how its parts
are started, keyed, connected and rotated, and what to look at when it does not
work. It states what the code does today. The contract the code follows, with
every limit and every failure case, is [the seat gate design](design/seat-gate.md);
this guide does not repeat it.

**Scratch only.** Everything here is provisional and none of it is qualified.
A decision from the phone goes to the daemon's STAND-IN door on scratch state
and approves nothing real; see [Scratch only](#scratch-only). Use placeholders
as in the examples (`seat.example`, `192.0.2.x`, `203.0.113.x`): no real name,
address or key belongs in a file of this repository.

## What runs where

| Where | What | Build |
| --- | --- | --- |
| Lead host (the host of the daemon) | `ataniqd run --owner-channel --seat-gate ADDR`: the daemon with the owner channel and the gate, a TCP listener on one address of this host. | Tag `ataniq_ownerchannel`: `make ownerchannel-build` builds `build/owner/ataniqd`. A build without the tag has no gate and no `seat-gate` command. |
| Lead host | `ataniq-console --daemon DIR`: the desk. It starts a pairing and confirms it; it holds the decider's place. | Untagged. |
| Authority host (the seat host) | `ataniq-seat`: the seat web app, which links to the gate. It listens on a Unix socket or a loopback address only. | Untagged: `sh scripts/go-tool.sh build -o build/ataniq-seat ./cmd/ataniq-seat`. No Makefile target builds it. |
| Authority host | A reverse proxy that owns port 443 and the TLS for the seat origin. | The owner's own; none is shipped. |
| The owner's phone | A browser with a passkey. It talks to the seat origin only, never to the gate. | |

The seat dials the gate; the gate never dials out. The link is TLS 1.3 with a
key pinned at each end: the gate holds a list of seat key fingerprints, the seat
holds one gate key fingerprint. The owner carries each fingerprint by hand from
one host to the other. A seat whose key is not pinned is refused in the
handshake and never reaches the service.

Not yet: a production mode of the daemon (the gate is for the scratch
`--owner-channel` build only), and the release of `ataniq-seat` on the
authority host (`deploy/authority-host` ships no seat program, unit or
configuration), so the seat is started by hand today.

## Order of a first setup

1. Build and key both ends ([Keys](#keys)).
2. Carry each fingerprint to the other end ([The pins](#the-pins)).
3. Write the origin file on both hosts ([The origin file](#the-origin-file)).
4. Open the firewall and set up the proxy ([The firewall](#the-firewall),
   [The reverse proxy](#the-reverse-proxy)).
5. Start the daemon and the seat ([Starting](#starting)).
6. Pair the phone, log in and decide ([Pairing](#pairing),
   [Login and decisions](#login-and-decisions)).

## Keys

Each end makes its own key once. A key is never overwritten by either command.

On the lead host, as the daemon's user:

```sh
./build/owner/ataniqd seat-gate init [--state-dir DIR]
```

This makes the gate's key in `STATE/owner/gate/` (`cert.pem` and `key.pem`, the
key mode 0600, the folder 0700) and prints one line: the gate's key fingerprint,
64 lower-case hexadecimal digits. `STATE` is the state folder: `--state-dir`,
else `$XDG_STATE_HOME/ataniq`, else `$HOME/.local/state/ataniq`. If either file
is already there the command refuses and writes nothing; move the old pair away
by hand to make a new key.

On the authority host, as the seat's user:

```sh
./build/ataniq-seat gate-init --identity DIR
```

This makes the seat's key in `DIR` (`cert.pem`, `key.pem`; the key mode 0600, the
folder made 0700 when missing) and prints one line: the seat's key fingerprint.
It refuses, and writes nothing, when a file is already there.

Keep each printed line. No command prints the seat's fingerprint again.

## The pins

**At the gate**: `STATE/owner/gate/seat-pins` lists the seat keys the gate lets
in. It is a regular file (not a link) of the daemon's user, mode 0600 or
narrower, at most 4 KiB, one fingerprint per line, at most 4 pins and no
repeat. A blank line and a line that starts with `#` are skipped. The owner
writes it by hand, for example:

```sh
( umask 077; printf '%s\n' SEAT_FINGERPRINT >> STATE/owner/gate/seat-pins )
```

`ataniqd seat-gate pins [--state-dir DIR]` prints the pins as the daemon will
read them. The daemon reads the file once, at start: a change applies at the
next start. A pin alone lets a seat read cards and decide nothing.

**At the seat**: a file that holds the gate's fingerprint and nothing else but
space around it, named by `--gate-pin-file`. It must be a regular file (not a
link) of at most 1 KiB that group and other cannot write. The seat reads it once,
at start.

The identity folder of the seat (`--identity`) and the gate's folder must not be
writable by group or other.

## The origin file

The seat origin is the address of the seat app, such as `https://seat.example`.
A passkey is bound to its host, so both hosts must hold the same origin, and a
change of it means pairing every credential again.

Write it in the form every part accepts today: `https://` and a lower-case
domain name of letters, digits, dots and dashes with at least one dot, a last
label that is not a number, and no port. (The one function that makes an origin
canonical is not built yet; the three checks in use differ at the edges, listed
in [the seat origin](design/seat-gate.md#the-seat-origin).)

- **Lead host.** The owner-local file `ataniq/owner-local/seat-origin` in
  `$XDG_CONFIG_HOME` (when absolute), else in `~/.config`. The daemon reads it
  once, at start, and the console reads it once, at start. Both refuse a file
  that is a link, is not a regular file, is larger than 1 KiB or can be written
  by group or other; use mode 0600. The first line is the origin.
- **Seat host.** The seat reads the origin from `--origin-file PATH`, or takes it
  from `--origin`; give exactly one. The file must be a regular file of at most
  1 KiB. The seat takes the whole file, trimmed of space, so write the origin and
  a newline and nothing more: a second line makes the seat refuse to start.
  Use mode 0600 here too; the seat does not check the mode.

Without a usable origin file the daemon still starts and its gate serves the
pairing and the reads only; the phone then says the desk does not take phone
logins. The start line says why (see [Starting](#starting)). Without an origin
the console's pairing view says "pairing origin not configured".

## The firewall

The lead host lets one port in, and only from the seat host:

- accept TCP to the gate's address and port (for example `192.0.2.10` port
  `7443`) from the seat host's address (for example `203.0.113.20`) and from no
  other source;
- nothing in the other direction is needed: the gate never dials out.

The gate is the daemon's only network listener, and it exists only with
`--seat-gate`. The daemon refuses an address that is a host name, a wildcard
(`0.0.0.0`, `[::]`), a multicast or broadcast address, one with a zone or one
with port 0, with exit code 10 before it takes a lock. The key pin keeps an
unpinned peer out of the service, but it does not replace the rule: the rule
keeps the port closed to everyone else. A lead's sandbox must not reach the port
either; no test shows that yet (see [Limits](#limits)).

## The reverse proxy

`ataniq-seat` speaks plain HTTP and never listens on a non-loopback address:
`--socket PATH` (mode `--socket-mode`, default 0660) or `--listen 127.0.0.1:PORT`
(a loopback IP literal). The proxy on the authority host owns port 443 and the
TLS for the seat origin and forwards to it. The app depends on these rules:

- **Keep the original Host.** Every POST needs a Host equal to the origin's
  host; any other is answered 403.
- **Pass `Sec-Fetch-Site` and `Origin` through untouched.** Every POST needs
  `Origin` equal to the seat origin and `Sec-Fetch-Site: same-origin`, as a
  browser sends them; a POST without them is answered 403, and no option lets
  one through. A proxy that removes or rewrites either breaks pairing, login and
  decisions.
- **Overwrite the client-address header on every request.** `--socket` needs
  `--client-addr-header NAME`, the header the proxy sets to the client's address,
  because a Unix peer has no address and every client would share one limit. The
  proxy must set it itself on every request and never pass one the client sent;
  the per-source limits (5 pairing tries and 10 login challenges per source in
  15 minutes) trust it. The app reads the last comma-separated entry; a value
  that is not an IP address is ignored, and the peer's address is used instead.
  With `--listen` the option is not required; give it only when the proxy
  overwrites the header on every request.
- **No caching.** Every response carries `Cache-Control: no-store`, and the
  security headers (content security policy, HSTS, `Permissions-Policy`) that
  the passkey calls rely on. Do not cache and do not remove or change them.
- **No redirects.** Serve the paths as asked, with no change of host, scheme or
  path. The pairing QR carries its one-time code in the address fragment, which
  must reach the app as the desk drew it. (The app itself answers `/pair`,
  `/login` and `/cards` with a 308 to the path with a slash.)

The proxy terminates the TLS, so it sees every request. A taken-over proxy or
seat host is a limit of this design (see [Limits](#limits)).

A page that fails on a Host, `Origin` or `Sec-Fetch-Site` mismatch shows the
generic failure of its page ("Pairing failed", "Logging in failed", "Something
went wrong"), not a reason: look for 403 in the proxy's log.

## Starting

On the lead host, with the scratch settings in place (see
[the scratch settings](lead-daemon.md#the-scratch-settings)):

```sh
./build/owner/ataniqd run --owner-channel --seat-gate 192.0.2.10:7443
```

`--seat-gate` needs `--owner-channel` (exit 2 without it). The daemon checks the
address, the gate's key and the pin file before it takes a lock, and once the gate
listens it prints one line with the address, the gate's key fingerprint and what
it serves, never a pin (the origin file is read at start too, and a missing or
unusable one only narrows what the gate serves):

```text
ataniqd: seat gate: listening on 192.0.2.10:7443, key fingerprint <64 hex digits>, seat origin https://seat.example; it serves pair, read, login and decide
ataniqd: seat gate: listening on 192.0.2.10:7443, key fingerprint <64 hex digits>; it serves pair and read only: no seat origin is configured (the owner-local seat-origin file is missing or empty)
```

The first line is the one to look for. Compare the printed fingerprint with the
one `seat-gate init` gave and with the seat's pin file.

On the authority host:

```sh
ataniq-seat --origin-file PATH --socket PATH --client-addr-header NAME \
  --gate 192.0.2.10:7443 --gate-pin-file PATH --identity DIR
```

`--gate`, `--gate-pin-file` and `--identity` go together; without them the seat
runs unlinked, pairing and login answer `daemon_unavailable` and no card offers a
decision. The seat prints `ataniq-seat: linking to the seat gate at ADDR` and
`ataniq-seat: serving on ADDR` to standard error. It keeps the link up in the
background and redials when it drops. Restarting the seat ends every session of
the phone.

Not yet: a unit file for either program; both are started by hand.

## Pairing

Pairing adds the phone's passkey to the daemon's key list. It needs the desk.

1. In the console (`ataniq-console --daemon DIR`, DIR the folder that holds the daemon's socket)
   press `P`, then Enter. The console draws a QR code holding
   `https://seat.example/pair/#CODE`; the code is in the fragment and in no log.
   See [Pair a phone](terminal-console.md#pair-a-phone-when-pairing-is-configured).
2. Scan it with the phone's own browser and tap "Make a passkey". The phone
   asks for its screen lock, face or finger (user verification is required).
3. The phone shows eight numbered words. The console shows eight words too, and
   the line "Compare with your phone. Confirm only if they match." Compare every
   word, in order, on the desk. The words are 64 bits of the key's fingerprint.
4. On a match press `c`, then `y` a moment later. `x` rejects. The credential is
   trusted only after this confirm; until then it can neither log in nor decide.
   If the phone says the words the seat sent do not match its own, do not
   confirm: cancel the pairing at the desk and start a new one.

The code expires by itself. The daemon burns it after five wrong tries, and the
seat allows 5 pairing tries per source in 15 minutes and 6 pairings in 11
minutes from all sources together.

## Login and decisions

**Login.** Open `https://seat.example/login/` and tap "Log in with your
passkey". Only a passkey paired at the desk is offered. The phone asks for user
verification each time. A login decides nothing; it starts a session in the
`__Host-seat` cookie (Secure, HttpOnly, SameSite=Strict) that ends after 30
minutes idle, 12 hours at most, at logout, or when the seat restarts.

**Cards.** After the login the phone shows the waiting cards (`/cards/`) and each
card's page. Each page carries the STAND-IN banner. Reload a page to read again:
live refresh is not yet built.

**Approve and deny.** A decision is made only on a card's own page, with a fresh
passkey every time: one tap on Approve or Deny names the action and the card,
and a second tap on the confirm button asks the phone for one assertion (face,
finger or passcode) over a challenge the daemon drew from its own record of the
card. Nothing is submitted by itself, and one assertion decides one card once.

- Approve is offered when the card may be approved, with one box to tick for
  each flagged path. A card the phone could not show whole (a path or ref too
  long) has Approve withheld, and the page says which part; approve it at the
  desk.
- Deny needs no box and takes an optional reason of up to 500 bytes.
- A decision needs no activity at the desk. It is journaled as confirmed by
  passkey.
- After the owner confirms, only a refusal says nothing was decided. Any other
  answer, or none, shows "Decision sent. Outcome unknown." with "Look again":
  the seat never sends the decision twice by itself. Read the card again before
  deciding again.

## Credential kinds

A paired credential has a kind, which is what the phone's authenticator
claimed: `platform-passkey`, `vault-passkey` or `passkey` (any other authenticator
identity). The pairing uses attestation `none`, so a kind is a claim and not a
fact. The desk shows it as "Claimed, not proven". A credential paired without
the registration data is stored as `unverified`, which is not a kind; it can
neither log in nor decide.

The accepted-kinds policy is the optional `"kinds"` key of `owner/scratch.json`
in the state folder: a list of those three names, each at most once. Absent or
empty, every kind is accepted. A login belongs to no project, so it checks only
that the daemon knows the kind; the policy applies when a decision's challenge is
asked and when the decision is made (`kind-refused`). A name that is not a kind
refuses the settings. The policy is advisory: it does not hold against a
taken-over seat. See [the scratch settings](lead-daemon.md#the-scratch-settings).

## Rotation

The gate holds a list of seat pins (at most 4) and the seat holds one gate pin,
so no code change is needed. Do the steps in this order.

**A seat key.**

1. On the authority host make the new key in a new folder:
   `ataniq-seat gate-init --identity NEWDIR`.
2. On the lead host add its fingerprint to `seat-pins`, beside the old one, and
   restart the daemon.
3. On the authority host restart the seat with `--identity NEWDIR`.
4. On the lead host remove the old pin and restart the daemon.

If the order is wrong: removing the old pin before the seat runs the new key, or
starting the seat on the new key before the daemon has been restarted with its
pin, leaves the seat unable to connect. Pairing and login answer "The desk could
not be reached" until the pin and the key agree and the daemon has been restarted.
Every daemon restart drops the outstanding challenges (a tap in progress says it
took too long), and the stand-in door keeps nothing across a restart. Paired
passkeys are not touched.

**The gate key.**

1. On the lead host move `cert.pem` and `key.pem` out of `STATE/owner/gate/` (keep
   `seat-pins`) and run `ataniqd seat-gate init`; note the new fingerprint.
2. On the authority host put the new fingerprint in the gate pin file.
3. Restart the daemon and the seat.

The seat cannot reach the gate between the two restarts. If only one of them is
restarted, the pinned key does not match and the seat cannot connect. Paired
passkeys are not touched: they are bound to the seat origin, not to the gate key.

**The seat origin.** A new origin is a new relying party, so every credential is
paired again. Change the origin file on both hosts and restart the daemon, the
console and the seat.

**A passkey.** Pair the new credential at the desk. Revoking the old one is an
operation of the owner connection (`owner.key-revoke`; `owner.keys-off` revokes
every credential); no console view or command of this tree calls it yet. To cut
a seat's reads at once, remove its pin and restart the daemon.

## Scratch only

The phone path is for scratch state. The phone shows the STAND-IN banner on the
card list, a card and its path pages: "Scratch only: the desk runs a stand-in door
on scratch state. Nothing here is a real approval." The daemon prints its own
STAND-IN banner at start. It means:

- the door behind the owner channel is a stand-in that gives no real approval;
- the owner channel is unqualified;
- the same-user limit stands until the sandbox that keeps leads off the daemon's
  user is qualified and the key list is protected: a process of the daemon's user
  outside a sandbox can read the gate key, add a pin, rewrite the origin file and
  edit the key list and the journal.

## Limits

The full list is [Limits that stay](design/seat-gate.md#limits-that-stay), and the
conditions before any real use are [Dogfood limits](design/seat-gate.md#dogfood-limits).
The ones an operator meets:

- A passkey assertion binds the card's digest, not what the phone showed; a
  taken-over seat host or proxy can show one card and ask for another, and can
  turn a login into a decision.
- Strangers who reach the seat can use up its limits and keep the owner from
  pairing or logging in on the phone for a while; the desk is never affected.
- The lead's sandbox must not reach the gate's port, and no qualification case
  shows it yet.
- Nothing is queued while the phone or the gate is offline.

## Troubleshooting

At the daemon's start:

| What you see | Cause | What to do |
| --- | --- | --- |
| `--seat-gate needs --owner-channel` (exit 2) | The flag was given without the owner channel. | Add `--owner-channel`. |
| `--seat-gate:` and a message about the address (exit 10) | A host name, wildcard, multicast or broadcast address, an address with a zone, or port 0. | Give one IP address of the host and a port. |
| `the gate's key in ... cannot be used` (exit 10) | No key, or the folder or files are not safe. | `ataniqd seat-gate init`; the folder must not be writable by group or other. |
| `... pins no seat key, so no seat could connect`, or a message about the pin file naming a line (exit 10) | No pin, no file, a link, a mode wider than 0600, another owner, more than 4 pins, a repeat or a line that is not a fingerprint. | Fix `seat-pins`; `ataniqd seat-gate pins` shows what is read. |
| `seat gate: cannot listen on ...` (exit 18) | The port is taken or the address is not the host's. | Free the port or change ADDR. |
| Start line says `it serves pair and read only` | No usable origin file (missing, empty, refused or not canonical), or a service that cannot decide. | Fix the origin file and restart; the phone says the desk does not take phone logins until then. |

At the seat's start (exit 2 with a message on standard error): `--gate-pin-file:`
followed by "cannot be read, or is a link", "is writable by group or other" or "does
not hold exactly one fingerprint of 64 lower-case hex digits"; `--origin-file:`
"is not a small regular file"; `--identity:` the key cannot be used; `--socket needs
--client-addr-header`.

What the phone shows, by the code the seat's API answers:

| The phone says | Code | Likely cause | What to do |
| --- | --- | --- | --- |
| "The desk could not be reached." / "cannot be reached" | `daemon_unavailable` | The link is down: daemon not running, the firewall, a wrong address, a pin on either end that does not match, or the seat started without `--gate`. | Check the daemon's start line, the firewall and both pins. |
| "The desk did not accept this code. It may have expired or been used." | `code_refused` | The code expired, was used or was tried wrongly five times. | Start a new pairing at the desk. |
| "Too many tries. Wait a while..." | `rate_limited` | The pairing or login limits of the seat. | Wait; the limits age out. |
| "The desk is busy. Try again in a minute." | `desk_busy` | The seat's room at the gate, or the gate's rate, is used up. | Wait a few minutes. |
| "The desk does not take phone logins now." | `login_unavailable` | The gate is read-only: no usable origin file at the daemon. | Fix the origin file, restart the daemon. |
| "No passkey is paired with the desk yet." | `not_paired` | The key list holds no usable credential. | Pair the phone at the desk. |
| "The desk did not take this passkey." | `login_refused` | Not paired, revoked, a kind the daemon does not know, or an assertion that did not verify (often a wrong origin on one host). | Pair again at the desk; check the origin on both hosts. |
| "The browser refused this site's name for a passkey." | `site_refused` (a browser error) | The origin is not a valid relying party for the phone's browser. | Check the origin and the proxy's name and certificate. |
| "Set a screen lock on this phone, then try again." | `user_verification_required` | The phone has no screen lock. | Set one. |
| "Pairing failed.", "Logging in failed.", "Something went wrong." | `failed` | A refusal with no code the page knows, including a 403 from the Host, `Origin` or `Sec-Fetch-Site` rules. | Look in the proxy's log for 403, and check [the reverse proxy](#the-reverse-proxy). |
| "This took too long, or the card changed. Look at the card and tap again." | `challenge_stale` | The challenge expired (5 minutes), was used, or the daemon was restarted. | Tap again. |
| "The card changed. Look at it again." | `card_changed` | The card was replaced since the page was read. | Reload the card. |
| "The card's time ran out." | `card_expired` | The card expired; a new challenge cannot renew it. | Nothing to do on the phone; see the card at the desk. |
| "The card cannot be decided now. Look at it again." | `cannot_decide` | Already decided, void or in another state. | Reload the card. |
| "The phone's answer was not accepted. If this repeats, check the seat at the desk." | `assertion_refused` | The assertion did not verify. | Check the origin on both hosts and the gate key the seat has pinned. |
| "This phone's passkey is no longer paired. Pair it again at the desk." | `key_revoked` | The key was revoked or is not on the list. The session ends. | Pair again at the desk. |
| "This project does not take decisions from this kind of passkey." | `kind_refused` | The `"kinds"` policy excludes the credential's claimed kind. | Change the policy or pair another credential. |
| "Decision sent. Outcome unknown." | `outcome_unknown` | The answer was lost after the decision was sent. | Tap "Look again" and read the card; never retry blindly. |
| "The session ended. Log in again." | `session_ended` | Idle 30 minutes, 12 hours, a seat restart, or a logout. | Log in. |
| "This is too large to send." | `too_large` | The card or decision does not fit the gate's frame. | Decide at the desk. |
