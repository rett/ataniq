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
| Lead host (the host of the daemon) | `ataniqd run --owner-channel --seat-gate ADDR`: the daemon with the owner channel and the gate, a TCP listener on one address of this host. | Tag `ataniq_ownerchannel`: `make ownerchannel-build` builds `build/owner/ataniqd`, the one build with that tag alone. For real scratch leads behind the gate, `make scratch-seat-build` builds `build/scratch-seat/ataniqd`, the one build with both tags, `ataniq_ownerchannel` and `ataniq_canary` (see [Builds with a tag](#builds-with-a-tag)). A build without the tag has no gate and no `seat-gate` command. |
| Lead host | `ataniq-console --daemon DIR`: the desk. It starts a pairing and confirms it; it holds the decider's place. | Untagged. |
| Authority host (the seat host) | `ataniq-seat`: the seat web app, which links to the gate. It listens on a Unix socket or a loopback address only. | Untagged, and static for a release: see [Deploying the seat app](../deploy/authority-host/DEPLOY.md#deploying-the-seat-app) (`CGO_ENABLED=0 sh scripts/go-tool.sh build -trimpath -o build/authority-host/ataniq-seat ./cmd/ataniq-seat`). No Makefile target builds it. |
| Authority host | `ataniq-seat-notify`, the notifier (optional): the one unit that sends the phone a push notice that cards wait. The seat tells it how many cards wait over a Unix socket. | Untagged, static, built with the seat's release: see [the README](../deploy/authority-host/README.md#the-notifier) and [Notices](#notices). |
| Authority host | Caddy on port 80, in front of the seat's socket: it keeps the Host, names the client's address and forwards. | Shipped in `deploy/authority-host/seat/` (see [the README](../deploy/authority-host/README.md#the-seat-app)); the owner writes its site file. |
| The owner's network | A front proxy that owns port 443 and the TLS for the seat origin and forwards plain HTTP to port 80 of the authority host. | The owner's own; none is shipped. |
| The owner's phone | A browser with a passkey. It talks to the seat origin only, never to the gate. | |

The seat dials the gate; the gate never dials out. The link is TLS 1.3 with a
key pinned at each end: the gate holds a list of seat key fingerprints, the seat
holds one gate key fingerprint. The owner carries each fingerprint by hand from
one host to the other. A seat whose key is not pinned is refused in the
handshake and never reaches the service. The seat reaches no other network
address than the gate's. With notices on it also dials the notifier's local
Unix socket, which is not a network address; the push to the phone, through
Apple's push service, is the notifier's, a separate unit (see
[Notices](#notices)).

On the authority host the seat is deployed as a release: `deploy/authority-host/seat/`
holds its unit (`ataniq-seat.service`), its Caddyfile and its lists (validators,
reload, stop, health), laid over the base release. What it installs and how the
switch runs it is [the README](../deploy/authority-host/README.md#the-seat-app);
the order of the owner's steps is
[DEPLOY.md](../deploy/authority-host/DEPLOY.md#deploying-the-seat-app). A seat
started by hand on another host needs `--decision-guard` (see
[Starting](#starting)).

Not yet: a production mode of the daemon (the gate is for the scratch
`--owner-channel` build only).

## Order of a first setup

1. Build and key both ends ([Keys](#keys)).
2. Carry each fingerprint to the other end ([The pins](#the-pins)).
3. Write the origin file on both hosts ([The origin file](#the-origin-file)).
4. Open the firewall and set up the proxy ([The firewall](#the-firewall),
   [The reverse proxy](#the-reverse-proxy)).
5. Start the daemon and the seat ([Starting](#starting)).
6. Pair the phone, log in and decide ([Pairing](#pairing),
   [Login and decisions](#login-and-decisions)).
7. Optional, on the authority host: set up the notifier and turn notices on at
   the phone ([Notices](#notices)).

To try the phone with sample cards first, without a lead, see
[Phone trial](#phone-trial).

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
(a loopback IP literal). On the authority host the release's Caddy listens on
port 80 in plain HTTP and forwards to the seat's socket; it does not terminate
TLS. Port 443 and the TLS for the seat origin belong to the owner's front proxy
on the owner's network, which forwards to port 80 of the authority host. Caddy
takes the client's address from the front proxy's `X-Forwarded-For`. The app
depends on these rules, which the front proxy and the shipped Caddy keep
together:

- **Keep the original Host.** Every POST needs a Host equal to the origin's
  host; any other is answered 403.
- **Pass `Sec-Fetch-Site` and `Origin` through untouched.** Every POST needs
  `Origin` equal to the seat origin and `Sec-Fetch-Site: same-origin`, as a
  browser sends them; a POST without them is answered 403, and no option lets
  one through. The one difference is a plain form's post of the project pages,
  which may also come with no `Origin` header at all when `Sec-Fetch-Site`
  says `same-origin`; `Origin: null` is refused there too. A proxy that removes
  or rewrites either breaks pairing, login, decisions and the project pages.
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
  security headers (content security policy, HSTS, `Permissions-Policy`,
  `Referrer-Policy: same-origin`) that the passkey calls and the forms rely
  on. Do not cache and do not remove or change them. The referrer policy in
  particular must stay `same-origin`: under `no-referrer` a browser posts the
  project pages' forms with `Origin: null`, which the seat refuses, so a proxy
  that sets its own `Referrer-Policy` on the seat's site breaks every form. The
  one exception is the notices' service worker, `/sw.js`, which the seat serves
  with `Cache-Control: no-cache` so that the phone checks it for a change; the
  shipped Caddy sets `no-store` on it as on everything else, which does no harm.
  The content security policy includes `manifest-src 'self'` and
  `worker-src 'self'`, which the notices need, and `trusted-types seat-sw`,
  which names the one policy the page makes to register the worker (and no
  other); a proxy that rewrites the policy must keep them, or the Home Screen
  app and its worker are refused.
- **Allow a held request on `/api/watch`.** The live refresh polls
  `POST /api/watch`, which the seat holds for up to 25 seconds before it
  answers. The front proxy's read timeout (how long it waits for the seat's
  answer) must be above 30 seconds, and its buffering must be off for that
  path. The shipped Caddy's `write 30s` and the seat's own 30-second write
  timeout are above the 25 seconds and need no change; do not lower them. A
  proxy that cuts the request earlier makes every page poll again and again,
  each time answered by an error that the page waits out with a growing
  pause, so the page lags without saying why.
- **No redirects.** Serve the paths as asked, with no change of host, scheme or
  path. The pairing QR carries its one-time code in the address fragment, which
  must reach the app as the desk drew it. (The app itself answers `/pair`,
  `/login` and `/cards` with a 308 to the path with a slash.)

Port 80 of the authority host must be reachable from the front proxy only: put
a firewall rule on the authority host that admits TCP port 80 from the front
proxy's address and from no other. The shipped Caddyfile trusts every private
address as a proxy (`trusted_proxies static private_ranges`) and takes the
client's address from `X-Forwarded-For`, so a client in a private range that
reaches port 80 itself chooses the address the seat's per-source limits count
and escapes them (the limits for all sources together still hold). The rule is
the owner's own (see
[the README](../deploy/authority-host/README.md#setting-the-host-up-for-it-once-by-hand),
step 6, and [DEPLOY.md](../deploy/authority-host/DEPLOY.md#deploying-the-seat-app)).

The front proxy terminates the TLS, so it sees every request, and the hop from
it to port 80 is plain HTTP on the owner's network. A taken-over proxy or seat
host is a limit of this design (see [Limits](#limits)).

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
ataniqd: seat gate: listening on 192.0.2.10:7443, key fingerprint <64 hex digits>, seat origin https://seat.example; it serves pair, read, login, decide, watch and genesis
ataniqd: seat gate: listening on 192.0.2.10:7443, key fingerprint <64 hex digits>; it serves pair, read, watch and genesis only: no seat origin is configured (the owner-local seat-origin file is missing or empty)
```

The first line is the one to look for. `watch` is the live refresh (see
[Live refresh](#live-refresh)); a daemon whose service cannot wait would leave
it out. `genesis` is the guided new-project interview, whose threads the daemon
keeps in `owner/genesis/` of the state folder (see
[the design](design/seat-gate.md#guided-new-project)); the line lists the
capabilities of the gate's hello, in words. Compare the printed fingerprint with the
one `seat-gate init` gave and with the seat's pin file.

On the authority host:

```sh
ataniq-seat --origin-file PATH --socket PATH --client-addr-header NAME \
  --gate 192.0.2.10:7443 --gate-pin-file PATH --identity DIR \
  --decision-guard DIR
```

`--gate`, `--gate-pin-file` and `--identity` go together; without them the seat
runs unlinked, pairing and login answer `daemon_unavailable` and no card offers a
decision.

`--notify-socket PATH`, an absolute path, is optional and needs the gate link:
it names the Unix socket of the notifier, and turns notices on (see
[Notices](#notices)). Without it the seat tells no one anything, the card list
shows nothing about notices and the notices' routes answer 404. On the authority
host the flag is `SEAT_NOTIFY_ARGS=--notify-socket /run/seat-notify/notify.sock`
in the owner's `seat.env`.

`--decision-guard DIR` (default `/var/lib/authority-host`) is the folder the
seat asks, with `decisionguard.Check`, before it relays any decision's challenge
or any decision, and for the card page's buttons, every time. While `decisions.inhibit` is in it, or the folder
is missing, a link or not readable, nothing is relayed and every Approve or Deny
answers `decisions_inhibited` ("Decisions are paused on this host. Nothing was
sent."). That is the authority host's state folder, and it exists there. By hand
on another host, name a folder you own that exists, is a real folder (not a
link) and that the seat can read: while no `decisions.inhibit` is in it,
decisions go through, and a file of that name pauses them. The default fails
closed there, so no decision is ever relayed.

The seat prints `ataniq-seat: linking to the seat gate at ADDR`,
`ataniq-seat: decisions follow the guard in DIR` and
`ataniq-seat: serving on ADDR` to standard error. It keeps the link up in the
background and redials when it drops. Each time the link comes up it prints
what the gate's hello listed, and whether the phone then offers "New project":
`ataniq-seat: the seat gate offers pair, read, login, decide, watch and
genesis; the phone offers New project` (without `genesis`, `the phone offers no
New project`). A gate offering `chat` adds `the phone offers read-only Leads`
to this line. Restarting the seat ends every session of the phone.

On the authority host the seat is not started by hand: its unit is started by
the switch's reload list (see [the README](../deploy/authority-host/README.md#enabling-it)).
The daemon on the lead host has no unit; it is started by hand.

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
   When they match what the phone worked out, the phone also shows the fixed
   line "When the desk has confirmed these words, log in:" and a "Log in" link
   to `/login/` on the same site. The phone is not told when the desk confirms,
   so the page does not redirect or refresh by itself: follow the link once
   the desk has confirmed (step 4).
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
card's page. Each page carries the STAND-IN banner. The card list and the Card page update
by themselves, see [Live refresh](#live-refresh).

**Moving between pages.** Every page behind the login has a short row of links
at the top: "Cards", and "Projects" when the desk runs new projects (see
[New projects](#new-projects)). When the connected desk offers chat, "Leads"
appears after the other links. The link of the part you are in is bold, has a
bar under it and is announced as the current page. With a keyboard, the first
Tab reaches "Skip to content", which jumps past the links to the page. The
status, pairing, login and error pages have no such row. The row has no Log
out: Log out stays at the foot of the pages that had it (the plan page has
none, as before). A card's page and its path pages show "Projects" by what the
card list last learnt, so the link may be missing for a moment after the desk
starts offering new projects; open the card list once and it is there.

**On a computer.** In a browser window at least about 960 pixels wide (60rem at
the default text size), the row of links becomes a column on the left that
stays in place while the page scrolls, and the page sits beside it in a
column narrow enough to read comfortably (wider for the card list and the
project list). Only the layout changes: every page shows and does the same as
on the phone. Make the window narrower, or zoom in, and the phone layout comes
back.

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

## Live refresh

What the owner sees. With the card list or a Card page open, a new card shows
up without a reload, and a card that was decided, withdrawn or replaced
(at the desk, on another phone or by the daemon) stops being offered:

- **The card list** reloads itself when the set of waiting cards changed, at
  most once every 20 seconds, and never while a field on it holds typed text.
  A new card that came in through the daemon's door is seen within about 30
  seconds, the interval at which the daemon looks at the door; a decision made
  at the desk or on the phone is seen within a few seconds, and a page that
  just reloaded waits until it is 20 seconds old before it reloads again.
  Every reload is a read of the desk, and the seat's reads share 14 calls in 80
  seconds (see below), with the Leads pages as well. With several card lists open
  at once, in a busy period, the reloads can use most of those 14, and a tap of
  yours on a card may then be
  answered "The desk is busy" for a moment: close the lists you are not looking at.
- **A Card page** never reloads by itself, since you may be ticking boxes.
  When its card was decided elsewhere or changed, the page says "This card
  changed or was decided elsewhere. Look again." with the links "Back to the
  cards" and "Reload", and the sign-off boxes, the reason field, Approve, Deny
  and the confirm button are disabled. Reload to read the card as it is now.
  A page of a card that was already decided or ended says nothing of the kind,
  and a page that sent your decision says what became of it, not "elsewhere".
- **The status line**, under the page's text, is empty while the page is live.
  It says "Not live" when the page was made while the seat watched nothing (the
  gate does not list `watch`, or the link was not up; the page polls no more,
  so reload it), "Not live: the desk is out of reach" while the link to the
  gate is down or the gate does not answer (the page asks again every 30
  seconds), and "Session
  ended. Log in again." when the session is gone.
- A tab that is hidden asks nothing, and asks at once when it is shown again.
- Up to three tabs of one session are kept live at once. A fourth takes the
  place of the oldest, whose page then waits 25 seconds before it asks again;
  with more than four tabs open the pages cannot all stay live (see
  "Troubleshooting").

What it costs. The seat keeps one long call at the gate for the whole seat,
whatever the number of pages and phones open: a held request that the gate
answers when the set of cards changes or after 25 seconds. The pages ask the
seat, not the gate. The gate takes 60 requests a minute on the seat's one link,
and the seat keeps a room for each kind of call within every 80 seconds: pings
6, decisions 10, pairing and login (anyone's) 12, reads 14, waits 6 and the
new-project pages 12, 60 in all. The Leads pages are reads: they share the
reads' 14 with the card lists until replies are built, and get a room of their
own only then, sized from what they are measured to use. Measured with three
card lists open and reloading every 20 seconds, a conversation refreshed once in
10 seconds left almost half of the lists' reloads "busy", and one refreshed
once in 30 seconds about one in ten; without chat none was. An idle seat sends one wait about every 25 seconds. A page's poll is not
a request that keeps the session alive: a session still ends 30 minutes after
the last page you loaded or tap you made, however long a page stays open.

A stale offer is never what decides. Live refresh only withdraws an offer
sooner; a tap on an old page is refused by the seat or the daemon as before
("The card changed. Look at it again.").

How to tell it works.

1. Start the daemon and read its start line: it ends in "decide, watch and
   genesis", or "pair, read, watch and genesis only" without a seat origin. A
   line without `watch` means no live refresh (the daemon's service cannot wait).
2. Open `/cards/` on the phone. The status line is empty. A line "Not live"
   means the gate does not list `watch` or the link was down when the page was
   made.
3. Bring a request to the desk and watch the list; it updates without a reload
   within about 30 seconds. Decide a card at the desk: the page of that card
   shows the banner above.
4. The daemon's log (its standard error; `journalctl` if you run the daemon
   under a unit) has one line when the gate answers a wait `wait-full`, at most
   one a minute for each seat:
   `ataniqd: seat gate: seat.wait refused wait-full (peer <16 hex digits>)`.
   One at a reconnect is harmless. The seat pauses 5 seconds after it and
   shows nothing. A line every minute means the gate holds waits for the same
   seat key from somewhere else, such as a second `ataniq-seat` using the same
   identity: stop the extra one.

Behind a proxy. A held request lasts up to 25 seconds, so the front proxy's
read timeout for the seat must be above 30 seconds and its buffering of
`/api/watch` must be off, so that the answer reaches the phone when the seat
writes it (see [The reverse proxy](#the-reverse-proxy)).

## Leads

Select **Leads** after login to see which conversations the desk shares.
In the phone trial the Leads pages carry the banner "**STAND-IN** These leads
and conversations are samples held by the phone trial. No real lead is read,
and nothing here is an approval." Every sample line begins "Sample:". Open
`alpha` or `bravo` to read its conversation. The directory lists `charlie`
with chat off and offers no link for it. On a wide screen the directory stays
beside the conversation.

The conversation opens on its most recent messages, up to 16. **Show earlier**
goes back 16 messages and **Show later** goes on from the page's last message,
through the retained history. **Jump to newest** shows the most recent
messages again and goes to the latest one. **Refresh** asks for the recent window again. Each Leads page is one or
two reads of the desk, and they share the reads' 14 calls in 80 seconds with
the card lists (see [What it costs](#live-refresh)); past it the page says "The
desk is busy. Try again in a minute." The seat reuses only the trial's sample
data, which never changes: its directory for 20 seconds and a transcript page
for 10 seconds. A real lead's directory and transcript are read again for
every page, so a lead the desk turns off shows as off on the next page. A separate
notice says "This page starts again from the oldest kept message." when the
place you asked for is no longer kept (older messages expired, or the page's
place is past the conversation the desk holds now), and another when the lead
changed epochs. The restart notice is not shown across a reset, a change of
the lead's chat setting or a fall of its epoch, where the lead may be another
under the same name.

Lead and System labels come from the seat. Links and markup in a reply appear
as text. Approvals are only on Cards. Sending and live refresh are not built,
including for a lead whose desk setting allows sending.

## New projects

When the daemon's gate lists `genesis` (its start line ends in `... watch and
genesis`), the card list on the phone has a "New project" button and lists the
projects in progress. Open "Projects" for the project list. If a page says
the desk is busy or unreachable, "Try this page again" reads the same page.
When a Card's decision is still on its way or its outcome is unknown, "Look
again" reads that Card. Nothing is configured for it on either host.

What "New project" does. It starts a short interview: a few rounds of one to
three multiple-choice questions about a project you want to make (what kind,
what it is for, who it is for, how it is built or published, by when). The
questions are the desk's own, from its fixed catalog, and so is the order they
come in. Each question offers its options, each with a one-line example, and,
where the question allows them, "Other" (your own words, a short note of
about 200 letters: the cap is 200 bytes, so accents and emoji take more room),
"You decide for me" and "Skip". On a question that accepts several choices,
use each of those three options on its own. If the phone rejects a particular
answer, it returns to that question and marks it "Check this answer."
"You decide for me" leaves the answer to the desk. On most questions it takes the question's default; on the language, the
sign-in and where a web app runs, the desk picks a value for you, shown under
"What was decided for you" on the summary. Such an answer counts as given, so
a draft plan can be drawn from it, and the plan says "Some answers were
decided for you; you can change them from the summary." (the stored plan of a
finished brief, which cannot be changed, says "Some answers were decided for
you, and finishing accepted them."). Finishing accepts
them as your answers; the summary says so above the finish button. After the
last round you read the
summary: what you chose, what was decided for you and what was left to its
default; "Change" on a row asks that question again (a change clears the later
answers that depended on it, the page says how many, and their questions are
asked again, after the last round too, as round 6 of 6), "Looks right:
finish" saves the answers as the project's brief. You cannot edit them
afterwards. If required answers are missing, finishing also leaves the
project without a plan. "Discard this project"
(after a second page) drops it with all its answers. At most two projects are
open at once; a third "New project" says so. An open project you leave for 14
days is discarded. The card list and `/projects/` list the open ones, so after
a restart of the seat you find them again.

The plan. When every required answer is given and the desk has a plan template
for the kind of project (a web app or a one-page tool, a newsletter or an
article, a podcast or a book), the summary has "See the draft plan"; otherwise
it says how many answers are filled in, or that there is no template for this
kind yet. The plan page (`/projects/<id>/plan`) shows, in this order: "Draft
plan" (or, once finished, "Plan, revision 1"), with the project's title and
the first six digits of its id, and the lines "This plan gives no permission to
do the work. Viewing it starts nothing." and, for a draft, "This is a draft;
finishing the interview keeps it as revision 1. You can change the draft by
editing your answers.";
the first demo; what waits for you (the review points you chose, and always "Publishing, releases,
pushing code, credentials and raising any limit always wait for you, whatever
the plan says."); the outside services the work would use; where the files
live; the suggested monthly budget, which the desk does not enforce; the estimate (an
estimate, not a promise); the plan's warnings; the milestones, each a section
you open, with its tasks, their kind and size and what each waits for; your
own notes, each quoted and marked as your words, never given to a tool, with
the question it answers above it ("Do you prefer a programming language?", or
"A note" for a note of your own); and "What a charter would cover", a draft of
words only ("Draft. Not signed. It allows nothing. ..."). Open "Read the
proposed limits on the work" for the work areas, the outside services, where
it would stop for you and the line "Always
stopping for you before publishing, releases, pushing code, credentials and
raising any limit." The suggested monthly budget follows that list. To tune the
plan, go back to the summary and change an answer:
the draft is drawn again from the answers. "Looks right: finish" keeps the
brief and its plan, revision 1, which never changes afterwards; the finished
page then has "See the plan" and points you to the desk for the next step.
If there is no plan, the finished page says why: required answers were
missing, or the desk has no template for that kind of project. The short digest under the
heading ("Plan 3c3c3c3c3c3c") is the same for the draft and the kept plan of
the same answers, so you can tell they match; when some answers were decided
for you, finishing accepts them, which changes the brief, so the kept plan
has the draft's content under another digest.

What it does not do. Nothing starts: no agent is run and no agent is given the
answers, the brief or the plan, and nothing is built or published. An answer
only describes the project; it never approves anything, and the project pages
hold no approve or deny control. The plan page uses links and native disclosures. It has no button,
form or script; nothing is signed, allowed or started from it, and it is
never a card, on the card list or in a notice. The answers are kept on the desk, by the
daemon, in `owner/genesis/` of its state folder (one file per project, mode
0600), and are sent nowhere else. Never type a password, a key or a token: say
that a credential is needed instead. Text that looks like one is refused ("That
looks like a password or key. ..."), and the page never shows it back.

What it costs. The pages are plain pages with forms, and each answer is one call
of the gate. The seat keeps 12 calls in 80 seconds of the gate's rate for them;
answering a round every 5 seconds stays inside it, because an answer and the
page after it are one call. Opening the plan page is one call (two when no plan can be drawn yet, since the
thread's round is read too); reopening a draft within 10 seconds, or the kept
plan within 5 minutes, is none. Past it the page says
"The desk is busy. Try again in a minute." and nothing is sent.

## Notices

A notice is a small push that the owner's phone shows when cards are waiting and
no page of the seat is open: one line, "3 waiting". It is optional and off until
the notifier is set up and the seat is started with `--notify-socket`. It is a
hint to look and nothing else: it shows no card, has no button, and a tap opens
the card list; a decision still needs the Card page and the passkey. The design
is [Notices](design/seat-gate.md#notices).

Built and tested on loopback, not deployed and not tried on a phone yet. The
steps below are written from the code; what the iPhone itself does (its
prompts, its settings, whether it accepts the service worker's registration
under the page's Trusted Types rule) is for the phone trial to confirm.

What runs. The seat tells the notifier one number over a Unix socket (how many
cards wait); the notifier, a separate unit with its own user, holds the push key
and the phone's subscription, and sends the push to Apple's push service, which
delivers it. The notifier's setup is the owner's, once, by hand, on the
authority host: the user, the key, the contact, the unit's drop-in that allows
its one outbound path, the firewall rule and the line in `seat.env`. It is in
[the README](../deploy/authority-host/README.md#the-notifier) (what it does and
how to set it up) and in
[DEPLOY.md](../deploy/authority-host/DEPLOY.md#deploying-the-notifier) (the
order of the steps).

### When a notice is sent

The seat sends one when the number of waiting cards is above what it last told
the notifier, unless:

- a page of the seat polled in the last 60 seconds (a page that is open and
  visible is one you are looking at; a hidden tab or a phone that has put the
  page to sleep asks nothing, so close the pages and wait a minute when you
  test). The cards that page found waiting count as seen: closing the page does
  not bring a notice for them, only a card that comes after its last poll does;
- the count came back up after a dip, to no more than what was last told, and
  fewer than 2 minutes have passed since the last notice (a card decided and
  another asked is not a new notice at once): so with the count not rising,
  there is at most one notice every 2 minutes;
- 30 calls to the notifier were already answered in the 24 hours since the first
  of them (a call it took or refused with a code, `rate_limited` too; a call it
  did not answer, because it was down or unreachable, does not count, so an
  outage does not use up the day; at most 200 calls of any end are made in the
  same time);
- the last call failed lately: the seat waits a minute, then 2, 4, 8, 16 and at
  most 30 minutes between tries while the notifier fails, and a call it takes
  ends that;
- the seat is not live: the gate does not list `watch` (see
  [Live refresh](#live-refresh)), or the link to the desk is down ("Not live: the
  desk is out of reach"). No notice is stored for later; once the seat is live
  again the count is judged afresh, and cards still above what was told then
  bring the notice.

Restarting the seat starts the tally anew, so a restart can send one more notice
for the cards then waiting. The notifier holds a limit of its own, 30 pushes in
any hour for all subscriptions together, whatever the seat asks. When the phone cannot
be reached, Apple's push service keeps a notice for up to 1 hour (the notifier
asks it to), and a newer notice replaces an older one not yet delivered.

### Turning notices on, on the phone

1. The notifier is set up and the seat runs with `--notify-socket`. You can see
   it: the card list has a **Notices** section at its foot. Without that
   section there is no notifier configured (see "Troubleshooting").
2. On the iPhone, open the seat's address in Safari and log in as usual.
   Notices work only from the Home Screen app, so add the seat's page to the
   Home Screen (Share, then Add to Home Screen; the app is named "Seat" and
   opens on the card list), and open it from the new icon. In Safari the
   section says "Open this page from the Home Screen app to turn on notices." and
   has no button.
3. In the Home Screen app, open the card list. If it shows the login page, log in
   there.
4. Tap **Enable notices** and allow the phone's prompt. The status line says
   "Working." and then "Notices are on."; the button becomes **Stop notices**.
   The permission is asked only by that tap.
5. Test it ([Testing it](#testing-it)).

The status line is the page's own (a fixed text, never one the seat or the
phone sent):

| Status line | Meaning |
| --- | --- |
| "This browser cannot show notices." | The browser lacks service workers, push or notifications. No button. |
| "Open this page from the Home Screen app to turn on notices." | The page is open in Safari, not from the Home Screen. No button. |
| "Working." | A tap is being carried out. |
| "Notices are on." / "Notices are off." | This phone's browser holds a push subscription or not. It is the browser's own state: it does not ask the notifier. |
| "Notices need your permission. Change it in the phone's settings." | The prompt was refused or dismissed, or notifications are off for the app. Allow them in the phone's notification settings for the Seat app, then tap Enable again. |
| "Could not turn on notices. Try again later." / "Could not stop notices. Try again later." | Any other failure; see "Troubleshooting". |
| "No room for another phone. Stop notices on another phone first." | The notifier holds 4 subscriptions already. |
| "Too many tries. Wait a minute." | More than 6 calls in a minute from one passkey's sessions. |
| "Session ended. Log in again." | The session is gone. |

### Turning notices off

Tap **Stop notices** on that phone: the notifier forgets the subscription and
the phone drops it ("Notices are off."). If the notifier cannot be reached the
phone keeps its subscription and you can tap again. To stop every notice from
the host, stop the notifier (`systemctl stop ataniq-seat-notify.service`) or
remove the `SEAT_NOTIFY_ARGS` line from `seat.env` and restart the seat (see
[the README's rollback](../deploy/authority-host/README.md#rollback-1)); the
phone's Notices section disappears (with the flag removed; with the notifier
only stopped it stays and its taps fail).

### Testing it

As the seat user, the only user the notifier serves, on the authority host:

```sh
sudo -u seat RELEASE/bin/ataniq-seat-notify send-test --socket /run/seat-notify/notify.sock 3
```

It prints `sent K`, how many pushes Apple's push service accepted. `sent 0` with
no phone subscribed shows only that the socket, the user check and the answer
work. With a phone subscribed, the phone shows "3 waiting". A test spends from
the notifier's 30 pushes an hour. The second test is real: close the seat's pages
on the phone, wait a minute, and bring a request to the desk; the notice follows
within about 30 seconds, the interval at which the daemon looks at its door. A
notice never asks for a decision: tap it, log in if asked, and the card list
is there.

### What a notice costs in privacy, and what a taken-over host can do

The text is encrypted to the phone, so Apple's push service reads no count. It
sees that the notifier sent a push to the phone's subscription, when, how large
it was and the push's headers (among them the contact the owner put in
`NOTIFY_SUBJECT`). A taken-over seat can fake or spam notices, up to the
notifier's 30 pushes an hour, and can fill the four subscription places so that
the phone is refused; a taken-over notifier can send the phone any push but the
phone shows only "N waiting" or "Cards are waiting" and a link to the card list.
Neither can decide a card. Every notice that is false is a reason to log in, and
the seat can turn a login into a decision (see [Limits](#limits)). The full
list is in [the design](design/seat-gate.md#what-a-taken-over-seat-or-notifier-can-do).

Not built: quiet hours (a notice comes at any hour the rules allow), an email or
other fallback when the phone cannot be reached, a notice for a single card and
question cards.

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

## Builds with a tag

Three Makefile targets build with a tag, each the one target for its tags, and
none is used by a release or a CI job; each runs `make doctor` first and starts
nothing:

- `make ownerchannel-build` builds the daemon with `ataniq_ownerchannel` alone
  into `build/owner/`: the owner channel and the gate, with the phone trial.
- `make scratch-seat-build` builds the daemon with both tags,
  `ataniq_ownerchannel` and `ataniq_canary`, into `build/scratch-seat/`: the
  owner channel and the gate with scratch mode (see
  [the scratch leads guide](scratch-leads.md)), for the owner's own trial of
  real scratch leads with the seat gate.
- `make canary-build` builds the binaries of the attended canary run with
  `ataniq_canary` alone into `build/canary/`.

All three folders are ignored by git. Each build is provisional and
unqualified, and [Scratch only](#scratch-only) holds for the two with the gate.

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

## Phone trial

The phone trial lets the owner try the phone end to end against a real daemon
on the lead host before any lead asks for anything: sample cards made on demand,
decided on the phone through the real decision path, and applied to nothing. It
is a STAND-IN rig for scratch state only, in the tagged build only
(`make ownerchannel-build`).

What is real: the daemon, the gate, the seat, the owner channel's service and
its journal, the key list and the pairing, and everything a decision goes
through: the challenge, the passkey assertion and its verification, the
decision record, the stand-in door's checks of the binding and the journal's
evidence. Nothing of that is skipped or loosened. What is a sample: the request
(no lead wrote it; its note says so) and what the host would find for it (the
merge, the suite record, named `trial-no-suite-ran-...`, the review and the
branches), made up and kept in memory. No git runs, no sandbox starts, and the
trial's settings name no mirror, drop folder, sandbox or work folder, so there is
nothing a trial card could be applied to.

On the lead host, in order (the folders are scratch folders of your own, never
the account's default state or runtime folder):

```sh
make ownerchannel-build
./build/owner/ataniqd trial init --state-dir /scratch/trial/state
./build/owner/ataniqd seat-gate init --state-dir /scratch/trial/state
# put the seat's key fingerprint in /scratch/trial/state/owner/gate/seat-pins
# (one line, mode 0600) and write the origin file (see The origin file)
./build/owner/ataniqd run --owner-channel --trial \
  --state-dir /scratch/trial/state --runtime-dir /scratch/trial/run \
  --seat-gate 192.0.2.10:7443
```

`trial init` writes `owner/scratch.json` of the state folder with `"trial": true`,
a sample project (`trial-project`), role (`trial-lead`) and target
(`refs/heads/develop`), the stand-in door and the three kinds of passkey
(`platform-passkey`, `vault-passkey`, `passkey`), so no settings are written by
hand. It refuses a state folder that is, lies inside or contains the account's
default state or runtime folder, and never overwrites settings that are there.
The daemon prints the STAND-IN banner and then

```text
ataniqd: owner channel: TRIAL: sample cards only, made by ataniqd trial submit; an approval applies nothing
```

On the authority host start the seat pinned to the printed gate fingerprint, as
in [Starting](#starting). Pair the phone at the desk
(`ataniq-console --daemon /scratch/trial/run`, see [Pairing](#pairing)) and log
in on the phone. Then, on the lead host, as the daemon's user:

```sh
./build/owner/ataniqd trial submit --socket /scratch/trial/run/daemon.sock --kind finish
./build/owner/ataniqd trial submit --socket /scratch/trial/run/daemon.sock --kind branch-prune
./build/owner/ataniqd trial submit --socket /scratch/trial/run/daemon.sock --kind finish --count 3
```

The kind is `finish` or `branch-prune` (nothing else, `prune` included). Each
prints one line per card (`card ap-...: finish trial-login-retry-1 (request
rq-trial-...)`) or per refusal (`no card: ...`, with the door's code, such as
`request-duplicate` for a second branch prune while one is open). With the card list
open on the phone a new card arrives by itself ([Live refresh](#live-refresh));
with the list closed and notices on, a notice says that cards wait
([Notices](#notices)).

- A finish card shows a sample feature, its plan (`qargi feature finish ...`,
  with the stand-in's plan token), three or four changed paths with their
  patch, and, on every other card, the `Makefile`, a flagged path that needs its
  own sign-off before Approve.
- A branch-prune card deletes two or three sample branches. The daemon takes no prune
  request from a lead yet, so the trial asks the stand-in door to plan it
  directly; it is decided like any card. One branch prune is open at a time.
- Approve or Deny with the passkey as for a real card. An approved trial card
  ends **not-applied**, with the line `applied: nothing (trial)`: the door took
  the approval, the use passed its checks, and nothing moved. An outcome that is
  unknown (the trial never makes one) keeps the host's own line, not the
  trial's. The project's daily limit counts trial approvals as any.
- The journal, `owner/journal.db` of the state folder, keeps each decision with
  the evidence of its assertion, and each outcome with the trial's line.

With `--seat-gate` the trial's gate, and no other, also serves the seat's lead
pages from STAND-IN samples: the capability `chat` (read only, see
[the design](design/seat-gate.md#chat)). The gate's start line then ends
`... watch, genesis and chat` and is followed by

```text
ataniqd: seat gate: chat: STAND-IN sample leads and conversations of the phone trial, held in memory; no lead is read or reached
```

The samples are three leads held in memory: `daemon:alpha` (running, a short
conversation with a long reply marked cut, replies of several lines, a reply
that looks like markup, which a page shows as text, and restart lines),
`daemon:bravo` (stopped) and `daemon:charlie`, whose chat is off and which
shows nothing but its name. Every lead reply and every system line begins
"Sample:" (`Sample: lead restarted, epoch 3`), and every result is marked as a
sample, so the seat shows a banner. No lead, conversation file or
other part of the daemon is read or reached, nothing is written, and nothing
can be sent: there is no way to send a message from the seat yet. Open **Leads** after login to read the samples.

`--trial` needs `--owner-channel` (exit 2 without it). The daemon refuses the
trial's settings without `--trial`, and other settings with it (exit 10), and a
state folder or a runtime folder that is, lies inside or contains the account's
default state or runtime folder (exit 10; so `--runtime-dir` is required in
practice, as the default runtime folder is one), before it takes a lock. `trial submit` makes 1 to 10 cards (`--count`, default 1) and
exits 0 when every card was made, 1 after a refusal or a failure, 2 for usage,
20 when no daemon answers, 21 unauthorized and 22 for a protocol error, which is
also the answer of a daemon not started with `--trial`. A call that stops after
some of its cards (it ended, or a sample failed) prints the lines of the cards
already made, which are on the phone, and then `stopped after N of M cards`,
exit 1. A refused request leaves nothing of its sample in the daemon's memory.
The operation needs no decider's place: it decides and applies nothing, and the
daemon's own-user check on its socket is its guard (any process of that user
can make sample cards, within the stand-in door's own caps). The stand-in door keeps
nothing across a restart: the sample cards are gone after one, and the journal
keeps what was decided. The card numbers start from the time of day (the UTC
seconds of the day times 1000, plus 1) and, when the journal holds ids of the
day, past the highest number it names, so that a restart, in the same second or
after the clock was set back, makes no card whose id the journal holds an entry
for. The plain scratch pass numbers its stand-in's cards the same way.

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
- A held poll costs the gate nothing, but a taken-over seat can fake liveness:
  it can say that nothing changed, or that a gone card is live. The daemon still
  checks the card's binding on every decision, so a stale offer is refused, not
  taken.
- Notices (see [Notices](#notices)) are not tried on a phone. They have no quiet
  hours and no fallback when the phone cannot be reached, none for a single card
  and no question cards. A taken-over seat can fake or spam them, and each one
  is a reason to log in, which the seat can turn into a decision.

## Troubleshooting

At the daemon's start:

| What you see | Cause | What to do |
| --- | --- | --- |
| `--seat-gate needs --owner-channel` (exit 2) | The flag was given without the owner channel. | Add `--owner-channel`. |
| `--seat-gate:` and a message about the address (exit 10) | A host name, wildcard, multicast or broadcast address, an address with a zone, or port 0. | Give one IP address of the host and a port. |
| `the gate's key in ... cannot be used` (exit 10) | No key, or the folder or files are not safe. | `ataniqd seat-gate init`; the folder must not be writable by group or other. |
| `... pins no seat key, so no seat could connect`, or a message about the pin file naming a line (exit 10) | No pin, no file, a link, a mode wider than 0600, another owner, more than 4 pins, a repeat or a line that is not a fingerprint. | Fix `seat-pins`; `ataniqd seat-gate pins` shows what is read. |
| `seat gate: cannot listen on ...` (exit 18) | The port is taken or the address is not the host's. | Free the port or change ADDR. |
| `seat gate: the guided interview is not served: its thread store did not open (REASON); ...` (the daemon runs on) | The folder `owner/genesis/` of the state folder could not be used, as REASON says: `folder-link` (a link), `folder-owner` (not the daemon user's), `folder-mode` (writable by group or other), `too-many-files` (more than 64 entries), `folder-not-folder`, `folder-make`, `folder-read`, `folder-open` or `folder-replaced` (cannot be made, read or opened, or was replaced while it was opened), `file-remove` or `file-write` (an old or expired thread file cannot be removed or written), `catalog` (the build's question catalog does not load). The gate starts without the interview: its start line has no `genesis` and the phone shows no "New project"; pairing, reads, logins and decisions are served as before. | Fix or move the folder by hand, then restart the daemon; it never repairs the folder and never reads or writes in it while it is in doubt. |
| Start line says `it serves pair, read, watch and genesis only` (before the thread store, `pair, read and watch only`) | No usable origin file (missing, empty, refused or not canonical), or a service that cannot decide. | Fix the origin file and restart; the phone says the desk does not take phone logins until then. |

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
| "Decisions are paused on this host." (on the card page, in place of the Approve and Deny buttons) or "Decisions are paused on this host. Nothing was sent." (after a tap) | `decisions_inhibited` | The authority host's decision inhibit is there (a switch under way, a boot not yet requalified, a failure), or the `--decision-guard` folder is missing, a link or not readable. The seat logs the guard's code (`decision-guard-...`). | On the authority host, `ah-switch status` says why. On another host, check `--decision-guard`. Tap again once decisions are taken. |
| "The desk is busy. Try again in a minute." | `desk_busy` | The seat's room at the gate, or the gate's rate, is used up. | Wait a few minutes. |
| "The desk is busy. Try again in a minute." on the project pages, or on any page, and the page does not say which limit refused | `desk_busy` | One of the seat's own limits refused the call, and the seat's log (standard error; the journal of its unit) has the line that says which. `ataniq-seat: the link has no room for a call of kind KIND (USED of ROOM in the last 80 seconds); the page says the desk is busy`: the link's room at the gate for that kind of call (KIND is `public`, `read`, `decide` or `genesis`; the Leads pages' calls are `read`, sharing the reads' 14 calls in 80 seconds; the interview's is 12 calls in 80 seconds) is spent, and nothing was sent. `ataniq-seat: the project pages' limit of 30 requests a minute was reached for one credential`: the phone's pages of the projects were asked for more than 30 times in a minute, which is the app's limit for one passkey, apart from the link's room. Either line is written once in 30 seconds for what it names, and the next one ends `N more since the last line` for the refusals held back in between; no line holds a project, a card, a passkey or an answer. If neither line is in the log, the gate refused (see the daemon's log). At most every 10 minutes, after a period in which the link refused a call, it also writes `ataniq-seat: calls of the link in the last N minutes, taken/refused by kind: public T/R, read T/R, decide T/R, genesis T/R, wait T/R`, the calls of each kind it sent and refused in that period. | Wait a minute and tap again; the rooms come back as the 80 seconds pass. If it repeats with no one tapping fast, tell the lead, with the lines and their times. |
| No "Leads" in the menu, or "The desk does not offer lead chat." | (a page) | The connected daemon's gate does not list `chat`: only the phone trial's gate serves it (the seat's log line of the gate's offer does not end `the phone offers read-only Leads`). While the seat cannot reach the gate, a Leads page says "The desk cannot be reached. Try again in a moment." instead, with a link to try again. | Run the phone trial's gate, or use the desk; for the second page, wait and try again. |
| No "New project" on the card list, or "New projects are not offered" | (a page) | The daemon's gate does not list `genesis` (the seat's log says `the phone offers no New project`), or the seat runs without `--gate`. | Check the daemon's start line; restart the daemon with its gate. |
| "That project is not open. ..." or "No such project" | (a page) | The project was finished, discarded, or left for 14 days, or the address is not one of the seat's. | Start a new one from the card list. |
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
| Status line "Not live" | (none: the page's own) | The page was made while the seat watched nothing: the gate does not list `watch` (the daemon's start line has no `watch`), or the link was down. | Reload. Check the daemon's start line and the link. |
| Status line "Not live: the desk is out of reach" | `offline` (answer of `POST /api/watch`) | The seat's link to the gate is down, or a wait got no answer from the gate (it timed out). The watcher retries after 1 second, doubling to 30, and any answer of the gate, even a refusal, lifts it at once. The page asks again every 30 seconds. | Check the daemon, the firewall and both pins as for `daemon_unavailable`. It clears by itself once the link is back. |
| Status line "Session ended. Log in again." | `ended` (answer of `POST /api/watch`) | Idle 30 minutes without a page load or tap (a poll does not count), 12 hours, a seat restart, a logout, or a revoked key. | Log in. |
| Nothing visible, but `POST /api/watch` answers 429 in the proxy's log or the browser's tools | `rate_limited` | More than 12 polls a minute from one passkey's sessions, as more than four tabs open at once make. The page waits 60 seconds and asks again. | Close the extra tabs. |
| "This card changed or was decided elsewhere. Look again." | `changed` or `gone` (the card's state in the poll's answer) | The card, which was waiting when the page was read, was decided on another page or phone or at the desk, replaced, expired or withdrawn since. Not shown for a card that was already decided, ended or expired when the page was read, nor on the page whose own confirm sent the decision. | Reload, or go back to the cards. |
| A line `seat.wait refused wait-full (peer ...)` in the daemon's log | `wait-full` | The gate already holds two waits for that seat key (or the daemon's eight). Normal for a moment when the seat reconnects. Not counted against the seat. | Nothing if it stops. If it repeats every minute, look for a second `ataniq-seat` with the same identity. |

Notices (see [Notices](#notices)). The notifier's own lines are in its journal
(`journalctl -u ataniq-seat-notify.service`): fixed text and counts, never an
endpoint or a key. The seat logs one fixed line, at most one a minute, when a
call to the notifier failed: `seatapp: the notifier did not take the count;
trying again later`.

| What you see | Cause | What to do |
| --- | --- | --- |
| The card list has no Notices section, and `/sw.js` answers 404 | No notifier is configured for the seat: it was started without `--notify-socket` (on the authority host, no `SEAT_NOTIFY_ARGS` line in `seat.env`, or the release has no notifier). | Set the notifier up ([the README](../deploy/authority-host/README.md#the-notifier)), add the line to `seat.env`, and switch or restart the seat. |
| The section is there with no button, and says "Open this page from the Home Screen app to turn on notices." | The page is open in Safari, not from the Home Screen. | Add it to the Home Screen and open it from there. |
| "Notices need your permission. Change it in the phone's settings." | The prompt was refused or dismissed, or notifications are off for the Seat app. The phone asks once. | Allow notifications for the Seat app in the phone's settings, then tap Enable notices again. |
| "Could not turn on notices. Try again later." | The seat could not reach the notifier (`unavailable`, 503 in the browser's tools), the notifier refused the subscription (`endpoint_refused`, 422: the phone's push service is not on the notifier's host list; `bad_subscription`, 400), the notifier answered with a code the seat does not know (`notifier_error`, 502), or the phone refused to register the service worker or to subscribe. | Check `systemctl status ataniq-seat-notify.service`, that `/run/seat-notify/notify.sock` is there (`seat-notify:seat-notify`, 0660). Read the notifier's journal. |
| "No room for another phone. Stop notices on another phone first." | `too_many`: the notifier holds 4 subscriptions. | Tap Stop notices on the other phone. A subscription that Apple's push service reports gone is dropped at the next push. Otherwise stop the notifier, move `subscriptions.json` aside, start it and subscribe again (see [the README's rollback](../deploy/authority-host/README.md#rollback-1)). |
| Nothing arrives, and `send-test` fails with `seatnotice: refused: unavailable` | The notifier cannot reach Apple's push service: the owner's drop-in (`egress.conf`) or the firewall rule is missing or wrong, the resolver is not allowed, or the endpoint's host is not on the notifier's list. Its journal says `notify: 0 of N pushes accepted, N failed`. | Check the drop-in's `IPAddressAllow=` lines and the firewall rule. See [the README](../deploy/authority-host/README.md#the-notifier), setup step 4. |
| Nothing arrives, the page says "Notices are on.", and the journal's `notify:` line has a count above 0 before `dropped as gone` | The subscription is gone (Apple's push service answered 404 or 410 for it), and the notifier dropped it. The page reads the phone's own subscription, so it still says on. | Tap Stop notices, then Enable notices again. |
| Nothing arrives, and `send-test` fails with `seatnotice: refused: rate_limited`, or the journal says `held back by the hourly bound` | The notifier already made its 30 pushes of the hour. | Wait; the hour ages out. |
| `send-test` fails with `seatnotice: the notifier is not reachable` or `the notifier did not answer` | The unit is not running, the socket is not there, or `send-test` was not run as the seat user (any other user is closed unread). | Run it as `seat` and check the unit. |
| The switch to a release with the notifier ended `ok`, but `systemctl status ataniq-seat-notify.service` says `activating (auto-restart)` and `send-test` says the notifier is not reachable | The notifier exits at its start and systemd restarts it every 5 seconds. The unit is `Type=simple`, so the switch's restart succeeds and its health check, which is the seat's, does not look at the notifier. A missing or unsafe key, a subscriptions file that is not as the command writes it, an unknown `--seat-user` or a bad subject. | Read `journalctl -u ataniq-seat-notify.service`, fix what it names, `systemctl restart ataniq-seat-notify.service`. See [the README](../deploy/authority-host/README.md#a-notifier-that-cannot-start). |
| Nothing arrives, `send-test` works, and cards wait | The rules did not call for a notice: a page of the seat polled in the last minute, or showed the cards that wait; the count did not rise above what was last told, or came back after a dip within 2 minutes; 30 calls were answered in the 24 hours; the last call failed lately (the wait grows to 30 minutes while the notifier fails); or the seat is not live (the gate does not list `watch`, or the link is down). | See [When a notice is sent](#when-a-notice-is-sent). Close the seat's pages on the phone, wait a minute, and bring a new request to the desk. |
| The notice reads "Cards are waiting" with no count | The push was not `{"n":N}` with N from 0 to 999 as the service worker reads it, so the worker shows its plain text. The notifier and the worker the phone holds do not agree, or the push did not come from this notifier. | Make sure the seat and the notifier are of one release. The phone's browser checks the service worker for a change when it starts it. |
