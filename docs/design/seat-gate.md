# Seat gate protocol, version 1

Status: built and tested on loopback and on scratch state, and none of it is
qualified. In a build with the tag `ataniq_ownerchannel` the daemon serves the
gate (`ataniqd run --seat-gate ADDR`; the pairing and reads are served from
G3 on, and the login and decision operations when the daemon has the seat
origin, G8b), and `ataniq-seat`, given a gate, serves the pairing page, the
login page, the card pages and the Card page's approve and deny control
(G7 and G9). A phone decision is verified, journaled and written as a
passkey record by the service. Phone decisions are scratch-only, shown with
the STAND-IN banner, and approve nothing real. Also built: the release of
`ataniq-seat` for the authority host (G12: its unit, Caddyfile and command
lists in `deploy/authority-host/seat`, built and tested, not deployed), the
call of `decisionguard.Check` in `ataniq-seat` before any challenge or
decision is relayed (`decisions_inhibited`), and live refresh (G14, first
part: the gate's long wait `seat.wait` under the capability `watch`, the
seat's one watcher, and the pages' own refresh; see
[live refresh](#live-refresh-seatwait)), and notices on the phone (the rest
of G14: the notice core N1, the push library N2, the service worker and
subscription pages N3 and the notifier unit N4; see [notices](#notices)).
The notices are built and tested on loopback with fakes, not deployed and not
phone-tested: nothing of them has run on a phone, and no real browser has
checked the page's Trusted Types registration of the service worker. Not built
yet: the phone trial (G13); question cards; the seat's side of the guided new
project (its wire, the daemon's thread store and the gate's route are built,
see [guided new project](#guided-new-project)); and
the one function that makes an origin canonical (G1;
[the seat origin](#the-seat-origin) lists where the three checks in use
disagree). This document is the contract that
the code follows. A paragraph that begins "Owner decision" records a decision
the owner has made. Everything else is proposed, for the lead to sign off, and
where a paragraph says built the code does what it says; the change to the
decision record is also part of the joint contract with the planner's project
and needs the planner's agreement. Names of packages, files and functions that
exist today are given as they are in the repository; a name marked "not built"
does not exist.

The seat gate is the link between the seat app, which serves the owner's phone
from the authority host, and the daemon on an AI host. It carries the pairing of
a phone, the reads of the cards, a login, and the owner's decisions. Phone
decisions remain scratch-only (see [dogfood limits](#dogfood-limits)): until
the owner channel and the sandbox that keeps leads off it are qualified, approvals
remain the owner's direct words (see [approvals](approvals.md#owner-channel)).

## What exists today

- The transport, `internal/seatgate` (standard library only): pinned-key TLS
  1.3 with ALPN `ataniq-gate/1`, length-prefixed strict JSON frames of at most
  257 KiB (see [frame size](#frame-size)), a `seat.hello` with a capability
  list, `seat.ping`, coded errors of the grammar `[a-z0-9-]{1,40}` and no text
  on the wire, and the server limits in its package comment (among them 60
  requests a minute per connection and a 30-second deadline per request).
  `ServerTLS` takes a list of pinned client keys;
  `ClientTLS` takes one pinned server key. `WriteIdentityFiles` and
  `LoadIdentity` keep an identity in a folder (`cert.pem`, `key.pem`, key mode
  0600, folder 0700, never overwritten). `ataniqd` (the gate's listener) and `ataniq-seat` (its
  link) use it.
- The seat app, `internal/seatapp` (standard library only), and the command
  `ataniq-seat`. They serve the pairing page and its API, the login page and
  its API with the seat's sessions, the card pages behind a session (G7),
  and the Card page's decide form with its API (G9; see
  [login](#login-seatlogin-challenge-and-seatlogin) and
  [the phone's side](#the-phones-side)). `seatapp.Daemon` has
  `Pair`, `LoginChallenge`, `Login`, `List`, `Show`, `Pages`, `Offer`,
  `DecideChallenge` and `Decide`;
  `internal/seatlink` implements it over
  the gate, and `ataniq-seat` wires that in when it is given a gate (without
  one, every call answers `ErrDaemonUnavailable`). The daemon's side of the
  login and decision operations is built (G8b): a gate lists `login` and
  `decide` when the daemon has the seat origin (see
  [the seat origin](#the-seat-origin)); otherwise the phone says that the desk
  does not take phone logins.
- The owner channel's service, `internal/approval/ownerchannel`, and the words
  of its connection, `internal/approval/ownerwire`. `Service.PairPresent` takes
  a source whose surface is `ownerchannel.SurfaceSeat` (`"seat"`). The daemon
  serves the owner connection on its Unix socket only in a build with the tag
  `ataniq_ownerchannel` (see
  [the lead daemon guide](../lead-daemon.md#the-owner-channel-tagged-build-only)).
  There the seat source is nil, so `seat.pair` is refused `unavailable` to every
  connection.
- The gate's handler, `internal/approval/gatehost` (untagged; only tagged code
  imports it, the daemon's gate listener). It serves `seat.pair` and the four
  reads to the service with a seat source, answers every refusal with a code,
  and bounds the refusals of each peer (see [errors](#errors)). A handler made
  with `gatehost.NewDeciding` also serves the login and decision operations
  (G8b), with the gate's own values; `gatehost.New` makes one that does not.
- The credential kinds and the assertion verifier,
  `internal/approval/credkind` (standard library only), and a software
  authenticator for tests, `internal/approval/webauthntest`, which shares no
  code with the verifier. The owner channel's service uses the verifier; only
  tests use the software authenticator.
- The fingerprint of a paired credential is 8 words (see
  [the pairing fingerprint](#the-pairing-fingerprint)).
- `Service.Decide` still refuses a source that is not the decider
  (`not-decider`) or whose surface has no owner activity inside the presence
  window (`owner-away`), a seat's included. The service's side of phone logins
  and decisions is built (G8b): `Service.SeatLoginChallenge`,
  `Service.SeatLogin`, `Service.SeatChallenge` and `Service.SeatDecide`, the
  store of challenges, and the decision path the desk and the seat share
  after authentication (`decideAuthed`), with the key checked again under the
  revocation lock. They take the gate's own values as an `ownerchannel.Gate`
  (its key fingerprint, the seat origin and its relying party id) and the
  project's kind policy from `Config.KindPolicy`.
- The decision record can say that a decision was confirmed by passkey (G8a,
  [the record change](#the-record-change)): the record package's decision
  check (`checkDecision`, run by `record.DecodeDecision` and
  `record.EncodeDecision`) holds a record with `confirmed: "code"` to a
  console block and no seat block, as before, and one with
  `confirmed: "passkey"` to a seat block and no console block;
  `record.AssertionDigest` computes the block's `assertion_sha256`. The
  service writes a passkey record for a phone decision (G8b).
- The words of the four gate operations are in `ownerwire` (G8a, see
  [the new types](#the-new-types)): their names, apart from the list the
  socket serves (`ownerwire.GateOperations`, not `ownerwire.Operations`), their
  params and results with strict decoders and caps, the gate's new codes, and
  the challenge function with the vectors below. The gate's handler serves
  them when it is made with `gatehost.NewDeciding` (G8b).
- The owner journal takes a decision confirmed by passkey (G8a, see
  [the journal entry](#the-journal-entry-of-a-phone-decision)): its activity
  from the seat block and its evidence beside it, read back with
  `ownerjournal.Store.EvidenceOf`. The list of deciding keys keeps the highest
  sign counter (`ownerjournal.KeyFile.RaiseCounter`, through the service's
  `KeyStore` port). The service writes both for a phone decision (G8b).
- Live refresh (G14, first part; see [live refresh](#live-refresh-seatwait)).
  The words are in `ownerwire`: `seat.wait` (`ownerwire.OpSeatWait`, in
  `ownerwire.GateOperations` and not in `ownerwire.Operations`),
  `SeatWaitParams`, `SeatWaitResult`, `SeatWaitCard`, the code `wait-full`
  and `ownerwire.PendingVersion`. The daemon's side is
  `ownerchannel.Service.WaitPending` (`wait.go`); the gate's is the
  capability `gatehost.CapWatch` (`"watch"`), listed only when the service
  can wait, with at most `gatehost.MaxWaitsPerPeer` (2) held waits for each
  peer; the seat's is the watcher of `internal/seatlink` (`watch.go`), which
  `ataniq-seat` starts, and `internal/seatapp` serves the browser's long poll
  `POST /api/watch` and the script `/static/watch.js` (`watchpoll.go`,
  `watch.go`, `assets/watch.js`). Without a gate that lists `watch`, the
  pages say "Not live" and still work by reload.
- Notices on the phone (G14, the rest; see [notices](#notices)). The protocol
  between the seat and the notifier is `internal/seatnotice` (standard library
  only; the seat imports it and never the push code), the push code is
  `internal/webpush` (VAPID and the message encryption of RFC 8291, the one POST
  to a push service; only the notifier imports it), and the notifier is the
  command `ataniq-seat-notify` (`cmd/ataniq-seat-notify`). The seat's side is
  the notice core in `internal/seatapp` (`notice.go`), the push routes
  (`push.go`), the card list's Notices section, the service worker and the
  manifest (`assets/sw.js`, `assets/push.js`, `assets/manifest.webmanifest`),
  and the flag `--notify-socket` of `ataniq-seat`. Built and tested, not
  deployed, not phone-tested.
- The words of the guided new project (the genesis wire, G3 of the genesis
  plan; see [guided new project](#guided-new-project)): `seat.genesis`
  (`ownerwire.OpSeatGenesis`, in `ownerwire.GateOperations` and not in
  `ownerwire.Operations`), `SeatGenesisParams`, `SeatGenesisResult` and their
  parts, and the nine codes `genesis-busy`, `genesis-unknown-thread`,
  `genesis-bad-answer`, `genesis-over-cap`, `genesis-secret-shaped`,
  `genesis-done`, `genesis-stale-round`, `genesis-not-ready` and
  `genesis-no-template`. The daemon's side is built too (genesis G4): the thread
  store, `internal/genesis/thread`, which keeps the threads in
  `STATE/owner/genesis/`; the gate handler's route and the capability
  `genesis` (`gatehost.WithGenesis`); and the daemon's wiring
  (`SeatGate.GenesisDir`). The verb `plan` and the plan stored at finish are
  built on the daemon's side (genesis G11a), and the seat's link and its plan
  page are built too (genesis G11b).
- The release of the seat for the authority host, `deploy/authority-host/seat`
  (G12, built, not deployed): the unit `ataniq-seat.service`, the Caddyfile
  and the validators, reload, stop and health lists, laid over the base
  release (see
  [the authority host's README](../../deploy/authority-host/README.md#the-seat-app)).
  `ataniq-seat` asks `decisionguard.Check` of the host's state folder
  (`--decision-guard`) before it relays any decision's challenge or decision,
  and for the card page's buttons: while `decisions.inhibit` is there, or
  the folder cannot be read, nothing is relayed (`decisions_inhibited`, see
  [the phone's side](#the-phones-side)).

## The listener: inside the daemon

The gate is a TCP listener inside `ataniqd`, compiled only under the existing
tag `ataniq_ownerchannel` and opened only when the owner asks for it with a
flag of `run` (built, G4: `--seat-gate ADDR`, beside `--owner-channel`).
ADDR is one explicit IP address and port; a wildcard address and a host name
are refused. Without the flag nothing listens. The owner's firewall lets only
the authority host's address reach the port.

The listener serves `seatgate.Serve` with the handler of the untagged package
`internal/approval/gatehost` (built, G3), which only tagged code imports. The
handler asks the pin check again on every call (`not-pinned`) and hands the
service the source

```text
Surface: "seat"   Session: the first 16 hex digits of the peer's pinned fingerprint   Decider: false
```

directly. The seat source of the Unix socket stays nil: no connection on the
daemon's socket is ever a seat.

The identity of the gate is kept in the state folder (built, G4:
`STATE/owner/gate/`, loaded with `LoadIdentity`) and the pinned seat keys in a
file beside it (built, G4: `STATE/owner/gate/seat-pins`, a regular file and
not a link, mode 0600, one fingerprint per line, at most 4). The pins are read
when the gate starts, so a change of the file applies at the next start. On
start the daemon writes one log line with the gate's address and fingerprint,
the seat origin and what the gate serves (`pair, read, login and decide`), or
why it serves `pair and read only`, and no pin.

Why not the alternatives:

- A separate gate unit that relays to the daemon's socket as a new client class
  would need a peer class on the Unix socket. Every process of the daemon's
  user can open that socket (see
  [who may connect](../lead-daemon.md#who-may-connect)), so the class would be
  only as strong as the same-user limit, and the relay would be one more process
  to qualify.
- A third build tag would need its own Makefile target, its own CI job and its
  own guard checks, by the rules in `AGENTS.md`, for no gain in separation: the
  gate serves the same service the owner channel's tag already contains.

`seatgate` is already a hardened, standard-library parser with its own limits,
and the handler adds only the mapping to the service.

## Operations and capabilities

The hello of the gate answers a capability list. A capability names a group of
operations. The gate serves an operation only when its capability is in that
list; any other operation, including every `owner.*` operation not listed
below, is answered `unknown-op` before its params are read. `seat.hello` and
`seat.ping` are the transport's own and need no capability: `seatgate` answers
them and never hands them to the handler.

| Capability | Operation | Params | Result | Built by |
| --- | --- | --- | --- | --- |
| `pair` | `seat.pair` | `ownerwire.SeatPairParams` | `ownerwire.SeatPairResult` | G3 (built) |
| `read` | `owner.list` | none | `ownerwire.Listing` | G3 (built) |
| `read` | `owner.show` | `ownerwire.ShowParams` | `ownerwire.ShowResult` | G3 (built) |
| `read` | `owner.paths` | `ownerwire.PageParams` | `ownerwire.PathsPage` | G3 (built) |
| `read` | `owner.patch` | `ownerwire.PageParams` | `ownerwire.PatchPage` | G3 (built) |
| `login` | `seat.login-challenge` | none | `SeatLoginChallengeResult` | G8b (built) |
| `login` | `seat.login` | `SeatLoginParams` | `SeatLoginResult` | G8b (built) |
| `decide` | `seat.challenge` | `SeatChallengeParams` | `SeatChallengeResult` | G8b (built) |
| `decide` | `seat.decide` | `SeatDecideParams` | `ownerwire.DecideResult` | G8b (built) |
| `watch` | `seat.wait` | `ownerwire.SeatWaitParams` | `ownerwire.SeatWaitResult` | G14 (built) |
| `genesis` | `seat.genesis` | `ownerwire.SeatGenesisParams` | `ownerwire.SeatGenesisResult` | genesis G3 (wire), G4 (handler and thread store) and G11a (the verb `plan`), built |

A handler made with `gatehost.New` answers `["pair", "read"]` (`gatehost.Caps`);
one made with `gatehost.NewDeciding`, `["pair", "read", "login", "decide"]`
(`gatehost.DecidingCaps`), and the daemon makes that one only when the seat
origin is configured and its service can decide from the phone (see
[the seat origin](#the-seat-origin)). Either handler adds `watch` after them
(`Handler.Caps`) when its service can wait (`gatehost.WaitPort`, which the
owner channel's service is), with or without a seat origin: a wait decides
nothing. Either handler adds `genesis` last (`Handler.Caps`) when it was given
the daemon's thread store (`gatehost.WithGenesis`, a `gatehost.GenesisPort`),
with or without a seat origin: an answer decides nothing either. The daemon's
start line names every capability the gate lists, in its order, in words
(`pair, read, login, decide, watch and genesis`). The seat reads the list (`seatgate.Client.Caps`) and offers login and decisions
only when the gate lists them; otherwise it says that the desk does not take
phone logins, or does not take decisions from the phone. It starts its watcher
only when the gate lists `watch`; otherwise its pages say "Not live".

A handler without the thread store does not list `genesis`, and answers the
operation `unknown-op` before its params are read, like any operation whose
capability it does not list. The daemon gives its handler the store whenever
the gate starts (`SeatGate.GenesisDir`, `owner/genesis/` of the state folder);
a seat offers "New project" only when it finds `genesis` in the list, as it
does for `watch`.

The gate never serves `owner.decide`, `owner.decider`, `owner.beat`,
`owner.looked`, `owner.limit`, `owner.limit-set`, the desk's pairing operations
(`owner.pair-start`, `owner.pair-status`, `owner.pair-confirm`,
`owner.pair-reject`) or the key operations (`owner.keys`, `owner.key-revoke`,
`owner.keys-off`). A key is revoked at the desk only.

The four new operations of G8, `seat.wait` of G14 and `seat.genesis` are the
gate's alone. They are not added to `ownerwire.Operations()`, which is the list the daemon's
Unix socket serves and announces; `ownerwire.GateOperations()` lists them
separately, so the socket neither announces nor serves them.

### The new types

All are in `ownerwire` (built, G8a), each with a Decode and an Encode function
named after it. Every byte string is standard base64 in the JSON, as in
`SeatPairParams`.

- `Assertion`: `credential_id` (`MinCredentialIDBytes` to
  `MaxCredentialIDBytes`, the bounds of a pairing), `authenticator_data` (37 to
  1024 bytes), `client_data_json` (1 to 2048 bytes) and `signature` (1 to 72
  bytes). These are the four fields of a WebAuthn assertion response; the user
  handle is not carried (see [login](#login-seatlogin-challenge-and-seatlogin)).
- `SeatChallengeParams`: `frame` (a `DecisionFrame`) and `credential` (the
  display id of the session's credential).
- `SeatChallengeResult`: `challenge_id` (16 lower-case hex digits),
  `challenge` (32 bytes), `nonce` (32 bytes) and `expires_at` (a record's
  time).
- `SeatLoginChallengeResult`: the four fields of `SeatChallengeResult`, and
  `allow`: the raw ids of the keys that may log in (on the list, not revoked,
  of a kind the verifier knows), newest first, at most 16.
- `SeatDecideParams`: `challenge_id`, `frame` (a `DecisionFrame`) and
  `assertion`.
- `SeatLoginParams`: `challenge_id` and `assertion`.
- `SeatLoginResult`: `credential` (the display id), `credential_id` (the raw
  id, which the seat puts in `allowCredentials` of each decision) and `kind`
  (the stored kind word).

The params of `seat.challenge` are capped at `MaxFrameBytes` plus 1 KiB, those
of `seat.decide` at `MaxFrameBytes` plus 8 KiB, and those of `seat.login` at
8 KiB. A result has the caps of the result types it reuses: `DecideResult` and
the small results `MaxResultBytes` (16 KiB). The result of
`seat.login-challenge` may allow 16 credentials of 1023 bytes, more than that,
so it has a cap of its own, `ownerwire.MaxSeatLoginChallengeBytes` (32 KiB).

The params of `seat.decide` and `seat.login` are read in two parts, so that the
challenge is burnt before anything about the content is judged (see
[decide](#decide-seatdecide)). The handler reads the envelope with
`seatgate.DecodeStrict` and the type's decoder (`ownerwire.DecodeSeatDecideParams`,
`ownerwire.DecodeSeatLoginParams`): the members and their JSON
types, each byte string's base64 and length, the challenge id's grammar, and
the whole within its cap (`bad-params`). The service takes the entry, and only
then checks the frame's content with `ownerwire.CheckDecisionFrame`
(`bad-params`); the assertion's content, the
strict DER of the signature among it, is the verifier's (`assertion-invalid`).
The params of `seat.challenge` burn nothing and are read whole by the handler.

### Frame size

`ownerwire.MaxPageBytes` (256 KiB) caps the listing, a show and a page, and a
result embeds the encoded page as it is. A `seatgate` frame is at most
`MaxPageBytes` plus 1 KiB, 257 KiB (`seatgate.MaxFrame`): 1 KiB is room enough
for the envelope, and no read of a card is refused for its size. The same cap
holds on both ends and in both directions: what the server reads and writes,
and what the client reads and writes. There is no negotiation and no
capability for it: the server does not look at the capabilities in the
client's hello, and it holds no client to a smaller cap. A request frame over
the cap breaks the link as a violation of the wire. A result larger than a
frame is answered `too-large` by the transport.

Before the raise a frame was at most 64 KiB. A peer built before the raise
would drop the link on a frame over 64 KiB instead of seeing `too-large`. The
contract accepts that, because no such peer exists: nothing is deployed, so
the seat and the gate are always of builds with the same cap. An earlier draft
had the client list a capability `frame-large`, and had the server write a
frame over 64 KiB only to a client that listed it. That capability is dropped
from the contract and is in no code.

`too-large` from a read then remains a defence only. The seat says, for the
listing, that the list is too large for this phone and is read at the desk;
for a show or a page, that this card is too large for the phone and is
decided at the desk. It offers no decision for a card it could not read
whole: the decide form needs `Card.MayApprove`, which asks that every list
of the card was read to its end.

## Errors

Every refusal on the gate is a code alone, of the grammar `[a-z0-9-]{1,40}`.
No text, no detail of a `*record.Error`, and nothing the peer sent goes on the
wire. The seat maps each code to fixed words of its own.

The transport's codes are those of `internal/seatgate` (`failed`,
`proto-unsupported`, `bad-hello`, `hello-required`, `hello-repeated`,
`rate-limited`, `busy`, `deadline-exceeded`, `too-large`, `shutting-down`).
The transport's `too-large` is a result that does not fit a frame; it is not
the record package's `record-too-large`, which refuses a record.

The handler (`gatehost`, built for pairing and reads) answers:

- Its own codes: `unknown-op`, `bad-params` (params that the gate's strict
  reading, `seatgate.DecodeStrict`, or the operation's `ownerwire` decoder
  refuses), `not-pinned` (a peer whose key the pin check no longer knows) and
  `too-many-refusals` (below).
- A `*record.Error` with its own code, unchanged, for the codes the operations
  served can meet (`approval-void`, `approval-expired`, `approval-consumed`,
  `decision-invalid`, `previous-epoch`, `review-changed`, `record-invalid`,
  `record-too-large`); a refusal with any other code is answered `failed` and
  logged, so that only listed words reach a seat. G8b adds
  `binding-mismatch`, the one code of the decision path not already on that
  list.
- The pairing and key errors of `ownerchannel` with the codes the daemon already
  gives them, which are also the console's (`consoleclient.PairError`):
  `pairing-refused`, `credential-invalid`, `keys-unavailable`, `keys-full`,
  `key-unknown`, `key-ambiguous`, `pairing-open`, `pairing-none`,
  `pairing-expired` and `pairing-changed`.
- The daemon's own: `unavailable` (the service is closed, stopped or stopping,
  or the call was cancelled), `deadline-exceeded`, `door-unavailable`,
  `journal-reopened`, `failed` (anything else, `ErrNotSeat` included; the error
  goes to the log, never the params).
- New codes for version 1, all from G8 but the last (their words are declared
  in `ownerwire`, `ownerwire.CodeChallengeStale` and the others):

| Code | Meaning |
| --- | --- |
| `challenge-stale` | No outstanding challenge of this kind and gate session has this id (unknown, used, expired, of another session, dropped by a restart), or the frame does not match the challenge. |
| `challenges-full` | 32 decision challenges are outstanding, none has expired and none can be replaced. |
| `assertion-invalid` | The assertion names another credential than the challenge's, or did not verify, for any reason (client data, origin, relying party, flags, signature). The daemon's log names which check failed, from the verifier's fixed list, never the bytes. |
| `key-revoked` | The credential is on the list and revoked. |
| `kind-refused` | The credential's stored kind is not a kind the verifier knows (`unverified` included), or not one the project accepts. |
| `wait-full` | G14. A `seat.wait` came while the gate already holds two waits for the peer, or the daemon's service already holds its eight. It is a load answer, not a misuse: it is not counted by the refusal bound (see [live refresh](#live-refresh-seatwait)). |
| `genesis-busy` | Genesis. A `start` of `seat.genesis` came while the host already holds two open threads (the daemon's, shared by every pinned seat of the owner). A state of the host, not a misuse. |
| `genesis-unknown-thread` | Genesis. The thread of the call is none the host holds: never started, discarded, expired, pruned, or kept by another daemon. The threads are the daemon's, not a seat's: every pinned seat reaches the same ones. |
| `genesis-bad-answer` | Genesis. An answer or a change the question does not allow: an unknown node, a node whose condition does not hold, an option the question does not hold, a round the thread has not reached, a form of answer the question does not take, or other text that is empty or holds a control character. The brief's `node_unknown`, `node_not_eligible`, `answer_invalid`, `round_invalid`, `value_invalid` and `field_name`. |
| `genesis-over-cap` | Genesis. An answer that would take a field, a note or the brief past its cap. The brief's `too_large`. |
| `genesis-secret-shaped` | Genesis. Text that looks like a key, a token or a password (the start of a private key block, `-----BEGIN`; a well-known key prefix; an assignment with a value, `password=...`, or `password: ...` with a value that is not a plain word; a run of 32 or more hexadecimal digits, or of random-looking letters, digits, `_`, `+` and `=`, where a dash or a slash ends the run). It catches pasted keys and obvious assignments, not every typed password; names, web addresses, paths and the phrase "private key" pass, and a bare git commit id (40 hexadecimal digits) is refused. Credentials are never typed on the phone. The brief's `secret_shaped`. |
| `genesis-done` | Genesis. An answer, a change or a discard of a thread that is finished. |
| `genesis-stale-round` | Genesis. An answer to a round the thread has already taken, or to any round once the walk is done (the thread is at its summary): a double tap, a second tab, a post the browser sent again. A state of the thread, not a misuse; the seat goes back to the thread's page with "That round was already answered." |
| `genesis-not-ready` | Genesis. A `plan` of a thread whose brief is not complete: a required field is not filled, or an answer the host inferred is not confirmed, on an open thread or on one the owner finished early. A state of the thread, not a misuse. The plan's `brief_incomplete`. |
| `genesis-no-template` | Genesis. A `plan` of a thread whose kind no plan template serves: the kind in the owner's own words. It is answered before the brief is complete as well, since completing it would not help. A state of the thread, not a misuse. The plan's `no_template`. |

The nine genesis words are the gate's words for the refusals of the host's
genesis packages (`internal/genesis/brief`, `code.go`) and for four states of
the host's thread store (`genesis-busy`, `genesis-unknown-thread`,
`genesis-done` and `genesis-stale-round`). The other refusals of those packages (`brief_invalid`,
`domain_mismatch`, `not_inferred`, `catalog_invalid`, `catalog_mismatch`, and
of the plan `template_invalid`, `plan_invalid` and `no_change`) are a
fault of the host, not of what the owner sent, and are answered `failed`. A test
(`internal/genesis`, `wirecodes_test.go`) holds the map complete: a refusal code
added to the brief fails it until the code is mapped. Whether a genesis refusal
counts for [the refusal bound](#the-refusal-bound) is told there.

`key-unknown` answers a credential that is not on the list. There is no code
for a missing seat origin: a gate without one does not serve `login` and
`decide` (see [the seat origin](#the-seat-origin)).

### The refusal bound

The handler bounds the refusals of each peer by rate, never by a lifetime
count, and it never closes a connection for them: the seat has one connection
that carries every browser's traffic. The bound is keyed by the peer's key
fingerprint, across its connections. After `gatehost.RefusalBound` (16)
counted refusals within `gatehost.RefusalWindow` (10 minutes), every request of
the peer, a read too, is answered `too-many-refusals`, without the service,
until the oldest of those refusals is 10 minutes old. A refusal of what the
peer sent counts (`unknown-op`, `bad-params`, `not-pinned`, `key-unknown`,
the record codes passed on); a refusal for the daemon's own state does not
(`unavailable`, `deadline-exceeded`, `failed`, `door-unavailable`,
`journal-reopened`, `keys-unavailable`, `keys-full`). The count is in memory. A
connection is closed only by the transport, by its own rules (a breach of the
wire, a timeout, a shutdown; see `seatgate`), and
`leaddaemon.DecisionRefusalBound`, the Unix socket's lifetime count, does not
apply to the gate.

No refusal of `seat.pair` counts, the pairing codes and `bad-params`
included: the seat relays a pairing for anyone who reaches its pairing page,
and the seat has one key and one connection, so 16 refused pairings from
strangers would otherwise shut the owner's logins, reads and decisions out
for 10 minutes, again and again. Wrong codes stay bounded where they matter:
the daemon burns a pairing code after five wrong tries, and the seat limits
pairings per source. Likewise, for the operations of G8, no refusal of
`seat.login-challenge` or `seat.login` counts, since the seat relays them for
anyone who reaches its login page, and a stranger must not shut the owner out;
the seat limits them (see
[login](#login-seatlogin-challenge-and-seatlogin)). The two exceptions, for
the three, are of the peer, not of a browser, and count as for any operation:
`unknown-op` (a gate that does not serve `login`, which the seat does not
call) and `not-pinned`. Of `seat.challenge` and
`seat.decide`, `challenge-stale`, `challenges-full`, `key-revoked` and
`kind-refused` do not count either: they come of normal use (a double tap, a
slow owner, a card that changed, "phone off" at the desk). `bad-params`,
`key-unknown`, `assertion-invalid` and the record codes passed on count.
`wait-full`, the answer of G14's `seat.wait` to a wait past the limits of
held waits, does not count either: a seat that reconnects while the gate still
holds its old wait would meet it with no fault of its own, and counting it
would let a reconnect loop shut a good seat out for 10 minutes. Its log line is
limited instead, to one a minute for each peer.

Of `seat.genesis`, only `genesis-bad-answer` counts, with `bad-params`: an
answer the question cannot take (an unknown node, an option the question does
not hold, a form of answer it does not take, a round the thread has not
reached) is what the peer sent, the seat's own pages never send one, and a
seat that keeps sending them is the misuse the bound is for. None of the other
genesis words counts. `genesis-stale-round`, `genesis-busy`, `genesis-done`,
`genesis-unknown-thread`, `genesis-not-ready` and `genesis-no-template` answer
the state of the thread store, which a seat in good order meets: a double tap
or a second tab on a round already taken, a start while two threads are open,
an answer sent after the thread finished, a page left open after its thread
expired or was discarded, a link to the plan of a thread the owner finished
early or of a kind in the owner's own words.
`genesis-secret-shaped` and `genesis-over-cap` answer what the owner typed,
which the owner may get wrong a few times on the way to a good answer. None of
them says anything against the seat, and a few taps must not shut the owner
out of approvals for 10 minutes. They stay bounded elsewhere: by the seat's 30
project requests a minute for each credential, by the seat link's room of 12
interview calls in 80 seconds, by the gate's 60 requests a minute, and by the
store's own limits (two open threads, eight files). A test sends 40 double
taps through the gate and its real store and is never shut out. A `list` names
no thread and takes no answer, so in good order it meets none of the counted
codes: like `round`, it is a read the seat may send as often as its own room
allows. So is `plan`: its refusals, `genesis-not-ready` and
`genesis-no-template`, count for nothing (a test sends seventeen of them
through the gate and its real store and is never shut out).

## The challenge

A decision from the phone is a WebAuthn assertion over a challenge that the
daemon draws and binds to its own record. The seat never chooses a challenge
byte.

### Issue: `seat.challenge`

The daemon checks, in order: the params are of their form (`bad-params`, in
the handler); the pending record exists and is pending (the codes of
`Service.Decide`); the journal holds no decision of the card on its way to the
door (`decision-invalid`, as `Service.Decide` says it); the credential named is
on the list (`key-unknown`), not revoked (`key-revoked`), and of a kind the
verifier knows and the record's project accepts (`kind-refused`);
`frame.binding_sha256` equals the binding digest of the daemon's own pending
record (`binding-mismatch`); the sign-offs equal the record's flagged list
for an approve and are none for a deny (`decision-invalid`); and the
decision's record, as `seat.decide` would write it (the frame's fields with
the daemon's own, confirmed by passkey, with a seat block of the lengths it
will have), fits the record's cap of 8 KiB (`record-too-large`, the code
`record.EncodeDecision` gives). A pending record may flag more than a
decision record can sign off, and the params of `seat.challenge` may be
larger than a decision record, so a challenge within its cap may still be
for a decision the journal cannot hold. So the phone is never asked to sign
what would be refused. None of these refusals is an entry
of the owner journal: no decision was made (see
[what is journaled](#what-is-journaled-and-what-is-logged)).

Then it draws a 32-byte nonce from `crypto/rand`, and computes

```text
challenge = SHA-256( "ataniq.seat.decision/1"
                     || F(instance) || F(id) || F(binding_sha256) || F(decision)
                     || F(signed_off_sha256) || F(reason_sha256)
                     || nonce )

F(x) = BE32(len(x)) || x
```

- The domain string is the 22 ASCII bytes shown, with no length before it.
- `instance` is the gate's own key fingerprint, 64 lower-case hex digits: the
  pin the seat holds. It ties the challenge to one daemon.
- `id`, `binding_sha256` and `decision` are the ASCII of the pending id, the
  binding digest (lower-case hex) of the daemon's own record, and `approve` or
  `deny`.
- `signed_off_sha256` is the lower-case hex SHA-256 of

  ```text
  BE32(n) || F(kind_1) || F(path_1) || ... || F(kind_n) || F(path_n)
  ```

  over the `signed_off` list in the order the decision record will hold it,
  which is the order of the record's flagged list; n is 0 when there is none,
  as for every deny. `kind` is the flag's ASCII word. `path` is the path's raw
  bytes, the string a `record.Text` holds, not its JSON form, whether the
  record writes it as a string or as `b64`; so a path with `<`, `>` or `&`, or
  one that is not valid UTF-8, hashes the same everywhere.
- `reason_sha256` is the lower-case hex SHA-256 of the reason's bytes as the
  record will hold them, with no normalization; of the empty string when there
  is none, as for every approve.
- `nonce` is the 32 raw bytes, last.

One function computes the challenge and both digests, in `ownerwire` (built,
G8a: `ownerwire.DecisionChallenge`, with `ownerwire.SignedOffDigest` and
`ownerwire.ReasonDigest`; `ownerwire.LoginChallenge` for a login), and both the
daemon and the seat link call it. Its tests hold the vectors below.

#### Test vectors

These were computed for this document twice, by two programs that share no
code (one with Python's `hashlib`, one with `printf` and `sha256sum`), and the
two agree. The inputs they share:

- instance: `000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f`
- id: `ap-20261120-0007`
- binding_sha256:
  `12969c837bce0f6598da9ece4c5e885ec6a669e731f542dbb4a05d3313aabf6c`, the
  binding of `internal/approval/record/testdata/decision-valid.json`
- the nonce of a decision: the 32 bytes 0x20 to 0x3f; the nonce of the login:
  the 32 bytes 0x40 to 0x5f

| What | Input | Digest (hex) |
| --- | --- | --- |
| `signed_off_sha256` | none (n = 0) | `df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119` |
| `signed_off_sha256` | (`exec-config`, `Makefile`), (`symlink`, `tools/` ESC `[2Jclear`, 15 bytes, which the test record writes as `b64`) | `18b556eee20ab32197f868320c7c611c4001d680c69937b84a209bd7ec0d3899` |
| `signed_off_sha256` | (`odd-path`, `a<b>&c`) | `827975e3705c3eb04dc1f5c08de133882b3de9442fc6e1ea1c928931db07c8ff` |
| `reason_sha256` | none | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| `reason_sha256` | `not this one` | `f60aafe0413bd84763dd2d54f284df0bbf5e0cfefc84b0227f346cf492756e8a` |
| decision challenge | `approve`, the test record's two sign-offs, no reason | `8f232e82c7b8dfac8f89b1587ebd03ea0b1bfa36f683932ade5f796600e5d20c` |
| decision challenge | `deny`, no sign-offs, the reason `not this one` | `f55e409d4b13759dd6c7c4caae52bd1af767a006afd71e446e9995ce37d8184d` |
| login challenge | the instance and the login nonce | `586b941d8d6d60ec7626a362dac9e97c777664ee647ecf1688121b7b1237fa8f` |
| `assertion_sha256` | credential id the 16 bytes 0x00 to 0x0f, authenticator data 37 bytes of 0xaa, client data `{"type":"webauthn.get"}`, signature `3006020101020101` (hex) | `7d6c723c90db49417f74b3e73ebc5c8abd774f0e0d942f82ebd4fb6784b60ac0` |

In the client data of the approve's assertion the challenge is
`jyMugse436yPibFYfr0D6gsb-jb2g5Mq3l95ZgDl0gw` (base64url, no padding).

#### The store

The daemon keeps each entry in memory only: {challenge id, kind ("decision" or
"login"), gate session, nonce, challenge, expiry}, and for a decision also the
credential's display id, the pending id and the record's project. The
challenge id is 8 bytes from `crypto/rand`, drawn again when it is already in
the store. An entry lives 5 minutes, measured with the monotonic reading of the
clock as the pairing's times are, and at its expiry it is expired.

An entry belongs to the gate session that asked for it (the source's
`Session`), and only a call of that session finds it: during a rotation, with
two pins, neither seat can use or burn the other's entries.

The two kinds have separate caps. There are at most 32 decision entries: before
one is added, expired ones are dropped, and an entry of the same session,
credential and pending id is replaced and burnt (the owner tapped again); then
a 33rd is refused `challenges-full`. There are at most 16 login entries, and a
login challenge is never refused for room: when 16 are outstanding, the oldest
login entry is dropped. So login traffic, which anyone who reaches the seat can
cause, never takes a decision's room. A restart drops every entry, which fails
safe: the owner taps again.

The result gives the challenge id, the challenge, the nonce and the expiry. The
seat link recomputes the challenge from the card it showed, the gate's pin and
the nonce, and when it differs it shows a fixed error and asks nothing of the
phone.

### Decide: `seat.decide`

The daemon checks, in this order:

1. The handler reads the envelope of the params (`bad-params`, see
   [the new types](#the-new-types)); the source is a seat.
2. The service takes the entry of this id, of the kind "decision" and of the
   source's session, out of the store in one step under the store's lock,
   burning it whatever happens next. None, or an expired one, is
   `challenge-stale`. An entry of the login kind or of another session is not
   found, and not burnt. Of two calls with the same id, one takes it and the
   other is `challenge-stale`.
3. The frame's content is of its form (`bad-params`).
4. `assertion.credential_id` is, byte for byte, the credential id of the
   entry's credential (`assertion-invalid`), which is on the list
   (`key-unknown`), not revoked (`key-revoked`), and of a kind the verifier
   knows and the entry's project accepts (`kind-refused`).
5. It recomputes the challenge from the frame as sent, its own instance and the
   entry's nonce, and compares it in constant time with the entry's challenge
   (`challenge-stale`). The frame cannot change between challenge and decide.
6. It verifies the assertion with `credkind.Policy.Verify`, against the stored
   credential, with the entry's challenge, the seat origin and its relying
   party id (`assertion-invalid`; see [verification](#verifying-an-assertion)
   and [the seat origin](#the-seat-origin)).
7. From here the path is the desk's, under the lock that serialises decisions
   (`dmu`): the record's state, the binding digest against the daemon's own
   record, the sign-offs, and the day's limit with "held". Then, under the lock
   that revocation takes (`pmu`, which `KeyRevoke` and `KeysOff` hold while
   they write), the key is checked again, and the decision or the held
   decision is appended to the journal. So a revoke and the durable decision
   have one order: a revoke that returned before the append wins
   (`key-revoked`, nothing is appended), and a decision appended first stands
   and goes to the door as any other. The lock order is the decision lock, then
   the revocation lock; no code that holds the revocation lock takes the
   decision lock. Then the door and the outcome, as at the desk. The desk and
   the seat share everything after authentication (built, G8b:
   `Service.Decide` and `Service.SeatDecide` both call `decideAuthed`).

#### What is journaled and what is logged

Only an owner's decision makes an entry of the owner journal. The issue of a
challenge, an entry that expires or is dropped, and every refusal of issue, of
login and of steps 1 to 6 are bounded telemetry, not journal entries: one log
line each, without params and without anything the peer sent, naming the
operation, the code, the gate session and, for `assertion-invalid`, the check
that failed; and a count per code, in memory. Before the assertion verifies,
nothing shows that the owner acted, and a durable entry for each would let
anyone who reaches the login page fill the disk. The plan of this work asked
that the issue of a challenge be journaled; the lead decided against it: a
challenge never used decided nothing, and the journal entry of a decision keeps
the challenge it used (below).

After step 6 the refusals are the desk's. A refusal the journal has a kind for
(`decision-invalid`, `binding-mismatch`) is journaled as refused, with the
surface `seat`, the gate session and no last activity. A refusal for the card's
state (it ended, ran out or was decided) is answered and not journaled, as at
the desk. The result is the desk's `DecideResult`; its line is the host's, and
the seat renders it with `seatapp.Text`.

#### The journal entry of a phone decision

`ownerjournal` is in G8's scope, and this part is built (G8a).
`AppendDecision` and `AppendHeld` take the
activity of a decision from the record's confirmation: for `confirmed: "code"`
from the `console` block, as before, byte for byte; for `confirmed: "passkey"`
the surface `seat` (`ownerjournal.SurfaceSeat`), the seat block's session and
no last desk activity, in the same write. They refuse a passkey decision that
comes with a last desk activity.

The decision's entry, held or not, also keeps the evidence of the assertion
beside the record (`ownerjournal.SeatEvidence`, given in `Seen.Evidence`): the
four fields of the assertion, the nonce, the instance
and the seat origin the daemon checked against. A body of the journal holds at
most 4096 bytes, and the largest evidence the bounds of `Assertion` take is
more than that, so it is kept in two parts, each body within its bound: the
decision or held entry keeps what was signed (the client data, the signature
and the nonce), and its activity entry who signed it and where from (the
credential id, the authenticator data, the instance and the origin). The
journal refuses a passkey decision without the evidence, or with evidence
whose assertion digest or credential is not the record's, and
`Store.EvidenceOf` reads the two parts back and checks them against the record
again (`ErrDamaged` when they do not hold). A build from before the evidence
reads such a body as damaged. The record keeps only a digest
of the assertion, because of its 8 KiB cap (`record.MaxDecisionBytes`). With
the credential's public key from the key list (a revoked key stays on it), an
auditor can recompute the challenge from the record and the evidence, check the
signature, and match `assertion_sha256`. That holds against a mistake and in a
later dispute, not against the daemon's own user: who can rewrite the journal
can rewrite the key list too (see [limits](#limits-that-stay)).

### Login: `seat.login-challenge` and `seat.login`

A login proves that the phone holds a paired credential; it decides nothing.
The first login is a ceremony with an allow-list. `seat.login-challenge`
answers a login challenge and `allow`, the raw ids of the keys that may log in;
the seat hands them to `navigator.credentials.get` as `allowCredentials`, with
user verification "required". This works for every credential the desk
confirmed, discoverable or not (the pairing asks `residentKey: "preferred"`, so
a credential may be either), and shows no account chooser. Its cost: anyone who
reaches the login page learns the raw ids of the usable keys and how many there
are. A credential id is not a secret, and it signs nothing.

WebAuthn asks, for a user known before the ceremony, that the credential be one
of the user's. The daemon has one user, the owner, and every key on the list is
his. So `seat.login` checks that the credential id is that of a key on the
list. The user handle, which the pairing draws at random and keeps nowhere, is
not carried and not checked.

The challenge is

```text
challenge = SHA-256( "ataniq.seat.login/1" || F(instance) || nonce )
```

with the store, life and burning of a decision's, the login kind's own cap, and
the same session binding. `seat.login` takes the entry of the login kind and of
the source's session (`challenge-stale`), finds the credential by
`assertion.credential_id`, byte for byte, on the list (`key-unknown`,
`key-revoked`; a kind the verifier does not know is `kind-refused`), and
verifies the assertion with `credkind.Verify` (`assertion-invalid`). A login
belongs to no project, so no project's policy is asked here; it is asked at
each challenge and decision. It answers the display id, the raw credential id
and the kind. The daemon keeps no session.

The seat's side (G7, built): the seat link recomputes the login challenge
from the gate's pin and the nonce before it hands it to the browser, and stops
on a mismatch as for a decision. It calls `ownerwire.LoginChallenge`, the
function the daemon calls too (held to the login vector above), and reads and
writes the login's words with the decoders and encoders of `ownerwire` (G8b);
`seatlink.LoginChallengeOf` is that function as an array. The seat keeps each login
challenge id with the browser that asked for it, server-side, under a login
cookie of that browser (`__Host-seat-login`, 5 minutes), and takes the
browser's assertion for it only with that cookie, burning the entry first. It
makes the session from the answer with a fresh 256-bit value it draws itself,
never one the browser brought (a session cookie that comes with a login is
ended), and keeps the value server-side as its digest. The session's cookie
is `__Host-seat`, Secure, HttpOnly and SameSite=Strict; a session ends 30
minutes after its last request and 12 hours after the login, or at a
logout (`POST /api/logout`). The page goes to the login page only once the
seat answered the logout: a 200, not redirected (the script's fetch refuses
a redirect), whose body is the seat's JSON acknowledgement
`{"next":"/login/"}`; when the answer is lost, or is anything else (a
proxy's page, say), the session may still be live, so the page says
"Logout not confirmed" and the button stays. Every later
decision of the session is to use exactly the session's credential
(`allowCredentials` holding only its raw id; G9). The seat link checks that a
login's answer names the credential the assertion named, by its raw id and its
display id.

Anyone who reaches the seat can ask for a login challenge, and the gate takes
60 requests a minute on the seat's one connection, which every browser shares,
pings included; it answers any more `rate-limited`. So the seat limits login
challenges to 10 per source in 15 minutes and to 8 a minute from all sources
together, which keeps strangers from filling its store of them. A source is
the client address the proxy sets, as for the pairing.

Those limits do not bound the seat's calls of the gate by themselves: a login
challenge may be answered at any time in its 5 minutes, so more logins than
challenges may come in one minute; the first page of a card's path list asks
the gate twice when the seat does not know the card's review; and the seat
pings the gate every 15 seconds. So the seat link counts every call it
sends (`seatlink`'s budget), by kind, over 80 seconds (the gate's minute and
the 20 seconds a call is taken to wait at most between the seat's count and
the gate's: the 15 seconds the app gives a call and 5 of the network, so
calls counted 80 seconds apart never meet in one of the gate's minutes), and
sends none past the room of its kind:

| Kind | Calls | Room in 80 s |
| --- | --- | --- |
| Pings | `seat.ping`, every 15 s (`seatlink.PingInterval`, which `ataniq-seat` sets) | 6 |
| Anyone's | `seat.pair`, `seat.login-challenge`, `seat.login` | 12 (6 logins) |
| Reads | `owner.list`, `owner.show`, `owner.paths`, of every session together | 14 |
| Decisions | `seat.challenge`, `seat.decide` | 10 (5 decisions) |
| Waits | `seat.wait`, the watcher's one long wait (see [live refresh](#live-refresh-seatwait)) | 6 |
| Genesis | `seat.genesis`, every verb, of every session together (see [guided new project](#guided-new-project)) | 12 |

The rooms add up to the gate's 60 (6 + 10 + 12 + 14 + 6 + 12, a sum the code
checks when it is compiled), so the gate's rate never refuses a call the seat
sent. Before the watcher the reads had 26 and there was no room for waits;
before the interview's pages the rooms were 6, 16 (8 decisions), 12 (6
logins), 20 and 6, and the interview first had 10, taken from the reads (20
to 14) and the decisions (16 to 12). A phone trial ran out of those 10 at the
fifth round: an owner answers a round every 10 to 15 seconds, so 80 seconds
hold 6 to 8 taps, and the taps and the reads the pages make now and then (the
list of open projects, a summary) came to more than 10. The interview has 12
now, one call for each tap and a few reads over, taken from the decisions (12
to 10: 5 decisions, each a challenge and a decision call, in 80 seconds).
Anyone's room stays 12: strangers can hold it
with login challenges they never answer, and a smaller one would let fewer of
them keep the owner from logging in. A call without room is not sent: the phone says the desk is
busy (`desk_busy`, the problem "The desk is busy", or `rate_limited` for a
pairing), before anything is asked. A decision's challenge is asked only
when the room of decisions holds it and its `seat.decide` as well: the room
of the decision is kept for that one challenge, under a token the link gives
before the challenge is asked, until the decision is sent, the seat app
drops the challenge (a new tap of the same session takes its place, or the
tap is burnt before its decision is sent), or the challenge's life ends (5
minutes at most). Each challenge has its own room, so several sessions of
one credential (a synced passkey in two browsers), and challenges asked at
the same moment, each keep theirs. So a decision the phone signed is never
refused for the gate's rate: strangers' logins and pairings, reads and other
sessions' challenges cannot take its room, and a tap past it is told the desk
is busy before any challenge is drawn or the passkey asked.
A login challenge is asked in the same way: only when anyone's room holds it
and its `seat.login` as well, the room of the login kept for that one
challenge until the login is sent or the challenge's life ends (5 minutes at
most). So the owner's login is never refused for room once the phone has
answered. Strangers can still use up the login challenges, and with them the
owner's: 8 a minute from all sources, and anyone's room, which 6 challenges
fill for as long as they are not answered (5 minutes at most), and which
pairings share. That keeps the owner from logging in on the phone, or
pairing one, while it lasts; it never touches the desk.
The seat relays pairings for anyone who reaches its pairing page. The gate
does not count a refused pairing against the seat's refusal bound (see
[the refusal bound](#the-refusal-bound)), so strangers cannot shut the
owner's seat out with them. Besides 5 tries per source in 15 minutes, the
seat still hands the daemon at most 6 pairings in 11 minutes from all
sources together (a body the seat refuses itself is not counted): that
bounds what anyone who reaches the page can make the seat send, which is
the daemon's log lines of refused pairings, the tries at an open pairing
code, and the pairings' share of anyone's room above. Its cost: two sources
that post well-formed registrations keep the owner's own pairing answered
`rate_limited` for as long as they keep it up, and pairings and logins
share anyone's room, so strangers at either page delay the other. They can
deny pairing and logging in on the phone, never the desk (see
[limits that stay](#limits-that-stay)). The pairing code's own limit, 5
wrong codes, stays.
Only a session reads cards, and the reads of one credential's sessions are
limited to 20 a minute (a card page asks the gate once; the first page of a
card's path list may ask twice, to learn the card's review), within the
room of all reads above. When the gate answers `rate-limited`, `busy` or
`too-many-refusals`, the phone says that the desk is busy and to try again in
a minute, and nothing is retried by itself.

Reads after a revoke: the reads are the pin's, not a credential's, since the
daemon keeps no session. A seat session whose key was revoked still reads cards
until the seat learns of the revoke or the session ends (G7: 30 minutes idle,
12 hours at most). The seat ends a session at once when a call of it is
answered `key-revoked` or `key-unknown`. To cut the reads at once, the owner
removes the seat's pin (G4) and restarts the daemon.

The two domain strings differ, so an assertion made for a login can never be
taken as a decision, and the reverse.

### Verifying an assertion

The verifier is built: `credkind.Verify`, and `credkind.Policy.Verify`, which
first refuses a kind the project's policy does not accept (G5,
`internal/approval/credkind`, standard library only). It follows WebAuthn
Level 3, section 7.2, "Verifying an Authentication Assertion", for an ES256 key
on P-256. The caller gives it what it expects (`credkind.Expect`: the
challenge, the origin and the relying party id, never values the client sent),
and it checks:

- the assertion names the registered credential id, byte for byte;
- the client data is one JSON object, valid UTF-8, with nothing after it and
  no member named twice; its `type` is `webauthn.get`; its `challenge` decodes
  (base64url, no padding) to the expected challenge, compared in constant
  time; its `origin` is the expected origin, compared exactly; `crossOrigin`
  is absent or false, and `topOrigin` is absent. Other members are allowed and
  ignored, since browsers add some on purpose: the strict decoders of
  `ownerwire` and `seatgate` apply to the frame, not to these bytes;
- the authenticator data starts with the SHA-256 of the relying party id,
  has user presence and user verification set, the backup state only with
  backup eligibility, the backup eligibility flag as registered, no attested
  credential data, and after the counter only the extensions map that its flag
  announces, in strict canonical CBOR;
- the signature is a strict DER ECDSA signature, r and s in [1, n-1], over
  `authenticator_data || SHA-256(client_data_json)`, by the registered public
  key, which must be canonical DER of a P-256 key.

Each refusal is one of a fixed list of errors, holding no part of the input.
A sign counter that does not grow past the stored one while either is not zero
is reported (`Result.CounterSuspect`), not refused: synced passkeys report 0 on
every use.

What the daemon hands the verifier is the key list's row as a
`credkind.Credential`: the kind, the credential id, the public key, the AAGUID,
the backup eligibility and backup state flags of the pairing and the highest
sign counter seen. The key list holds them (built, G6). At the pairing the
seat relays the registration's authenticator data and relying party id with
`seat.pair`; the daemon registers the credential from them with
`credkind.Register`, refusing a registration that does not hold
(`credential-invalid`), and at the desk's confirm it stores the kind the
AAGUID claims, the AAGUID, the flags and the counter as the baseline of the
highest one. That counter is raised after each verified login or decision (a
counter record of `owner/keys.log`) and never lowered, across restarts. The
wire's key list (`owner.keys`, and `owner.pair-status` for the pending
credential) shows the kind, the AAGUID and the two flags, never the counter,
which is kept internally. A key paired without the authenticator data, or
before the claims were kept, is of the kind `unverified`, which is not a kind
of `credkind`: it cannot log in or decide, and is refused `kind-refused`. A
key paired before G6 is paired again; nothing is paired in production.

The counter: a credential that reports 0 at pairing and at every use, as a
synced passkey does, has no counter, and nothing is checked or logged for it.
A counter that is not zero on either side and does not grow is logged as a
possible clone. It is not refused, since a counter is a claim like the rest,
and the stored highest counter is not lowered. The service compares and raises
in one step, under a lock of its own, against the highest counter known at
that moment (the list's, read again, or the one it keeps in memory), not
against the row it read before verifying: two verifications at once are
compared in turn, so a copy that reports the counter the other just raised is
told. The counter records share the key list's spare room with rejected
pairings (`MaxKeyRecords` less two records per key, 3,968), so a key that
counts uses it up after some four thousand uses. Then the list's counter stays
where it was, and the daemon says so once per key and run, through the notice
to the operator (the daemon's log; a phone's synced passkey, which reports 0,
never needs a record), and keeps the highest counter in memory, so a copy is
still told until the daemon restarts. It does not refuse new pairings for it:
a pairing takes no counter room, and a refusal would not keep the counter. A
backup eligibility flag that differs from the paired one is refused by the
verifier (`assertion-invalid`), as section 7.2 of WebAuthn Level 3 says.

### The seat origin

The origin and the relying party id are the seat's, and the two ends must agree
on them byte for byte.

- One function makes an origin canonical, and the seat app, the daemon and
  the desk's pairing view all call it (not built, G1: the reader of the origin
  file is built, `ownerlocal.SeatOrigin` in `internal/ownerlocal`, standard
  library only, and it checks nothing of the value; the function was to sit
  beside it). It takes `https://HOST` or `https://HOST:443`, with a lower-case
  domain name and no address literal, other port, path, query, fragment or
  user information, and gives `https://HOST`. The relying party id is HOST,
  without a port.
- Three checks stand in for it today, each with rules of its own:
  `leaddaemon.CanonicalSeatOrigin` (the gate's), the check that `seatapp.New`
  makes of `Config.Origin` (built on `net/url`), and `pairview.ValidOrigin`
  (the desk's, for the QR). They disagree, and the list of known
  disagreements is:
  - `pairview.ValidOrigin` accepts an upper-case host and lower-cases it, and
    requires a dotted host. The daemon's `CanonicalSeatOrigin` refuses an
    upper-case host and accepts a single label such as `https://localhost`;
    `seatapp` accepts a single label and refuses an upper-case host too.
  - `seatapp` accepts an underscore in the host, a trailing dot and an
    all-digit last label (`https://seat.123`), which the daemon refuses.
  - A last label that reads as an address in hexadecimal (`https://seat.0x7f`)
    is refused by `pairview` and accepted by the daemon and by `seatapp`,
    which refuse only a last label of digits alone.

  None of these lets a wrong origin be accepted: the verifier compares the
  client data's origin and the relying party hash with the daemon's own
  canonical value, and a browser binds a passkey to its relying party id. A
  disagreement only makes the QR or the logins fail. Until the function is
  built, write the origin in a form all three accept: `https://` and a
  lower-case domain name of letters, digits, dots and dashes with at least one
  dot and a last label that is not a number, and no port but 443.
- The gate reads the origin once, when it starts, from the owner-local origin
  file, makes it canonical and logs it. A change of the file applies at the
  next start, so the origin never changes under a running gate. Built (G8b):
  `ataniqd run --seat-gate` reads it with `ownerlocal.SeatOrigin` before the
  daemon takes its lock, makes it canonical with
  `leaddaemon.CanonicalSeatOrigin`, and names it in the gate's start line. The
  seat app is given its origin at its start, by `--origin` or from the first
  line of the file named by `--origin-file`, and its relying party id is that
  origin's host (`--rpid`, when given, must be that host); the desk's console
  reads the same owner-local file for the pairing QR.
- Without the file, or with one that is not an origin, the gate starts with the
  capabilities `["pair", "read"]` only, and logs why. So does a gate whose
  service cannot decide from the phone. The seat then says that
  the desk does not take phone logins, or decisions from the phone. So no call
  meets a missing origin.
- The verifier compares the client data's origin with the canonical origin
  exactly, and the authenticator data's relying party hash with the SHA-256 of
  the relying party id.

The origin file is in the configuration of the daemon's user, so any process
of that user can rewrite it. That cannot forge a decision, since a passkey is
bound to its relying party id, but it can make every honest assertion fail
until the file is put right and the daemon restarted (see
[limits](#limits-that-stay)).

## Presence

The desk's rule stays: a decision at the console needs the decider's place and
the owner's activity inside the presence window.

For the surface `seat` only, a verified assertion with user verification
replaces the presence window, and only for the decision that assertion signed.
The decider's place is the desk console's (one connection on the Unix socket)
and a seat never holds it, so `seat.decide` does not ask it either; this second
point is proposed with the first. A source whose surface is `seat` gets nothing
by its surface alone: the existing `Service.Decide` still refuses it
`not-decider`, and only the path in [decide](#decide-seatdecide), after step 6,
skips the window. A seat decision is not owner activity at the desk, and a desk
beat is not presence at the phone.

So a phone decision rests on a whole chain, and on each link of it:

- a seat whose key the owner pinned by hand on the AI host (a pin alone reads
  cards and decides nothing);
- a credential that the owner confirmed at the desk while present:
  `PairConfirm` asks the decider's place and the presence window, after the
  owner compared all the fingerprint's words; a pairing that is pending,
  rejected or expired gives no key, and only a key on the list can log in or
  decide;
- a desk channel that only the owner's console holds, since that is where the
  confirm comes from (see [dogfood limits](#dogfood-limits));
- the seat origin, read by both hosts, to which the credential is bound;
- and a fresh assertion with user verification over a challenge the daemon
  drew and bound to its own record of the card.

A key put on the list without the desk is possible only through the same-user
limit (see [limits](#limits-that-stay)).

The phone and the desk may both hold a card open. The journal takes one decision
per card; the second is refused as today.

## A deny needs an assertion

Owner decision: a deny from the phone needs a fresh passkey assertion, as an
approve does. Both go through `seat.challenge` and `seat.decide`. Otherwise any
process on the AI host of the daemon's user that can append a pin to the gate's
pin file could deny cards. The owner's passkey asks for user verification at each
use anyway, so a deny costs one more verification.

A deny is offered when `Card.Deny` allows it, not `Card.MayApprove`: it needs
no reading and no sign-off, so an owner who sees a dangerous change refuses it
without first attesting that he reviewed it. Its frame carries `signed_off: []`
and the owner's reason, which may be empty; the frame's check refuses a deny
with sign-offs. A deny is never held and does not count as a use.

Leaving a card undecided sends nothing.

## The phone's side

What the seat app shows (G7, built): the card list (`GET /cards/`), the card
page (`GET /cards/{id}`) and the pages of a card's whole path list (`GET
/cards/{id}/paths/{n}`), each behind a session and, as G7 built them,
read-only (G9 adds the Card page's decide form, below). The seat link
builds the card from the record's bytes with `internal/approval/card`, after
the bytes' digest is checked and the record decodes as the card asked for, so
the seat never shows a card rendered elsewhere. A lead's text is shown as the
desk console shows it, through the lead-text escaping, and then through
`seatapp.Text`. Each page says that the desk runs a STAND-IN door on scratch
state and that nothing on it is a real approval. The list and the path pages
say that approve and deny are on a card's own page or at the desk; the Card
page holds the decide form, and no other page has a decide control.

What the seat app does with a decision (G9, built):

- The Card page is the only page with a decide form. Approve is offered when
  `Card.MayApprove` allows it, with one sign-off per flagged path; deny when
  `Card.Deny` allows it, with a reason field and no sign-offs.
- Before the phone asks for user verification, the page shows the action the
  owner chose and the card it is for (its id and project, and what it would
  do, as the card shows them). One tap asks one fresh assertion for that one
  action; nothing is submitted by itself.
- The browser never sends a path or a binding. `POST /api/decide-challenge`
  carries the id of the Card page's form, the action, the reason and the
  sign-offs as the indexes of the flagged paths. The seat builds the frame from its own copy of the card (read
  through the seat link, its digest checked), keeps the frame with the session
  and the challenge id, server-side, and answers the challenge. `POST
  /api/decide` carries the challenge id and the assertion. Both bodies fit the
  app's cap on every request (`seatapp.MaxBody`, 8 KiB): an assertion within
  the caps of `Assertion` is under 6 KiB in base64.
- The app's `Permissions-Policy` header allows `publickey-credentials-get` to
  the app's own origin on the login page (G7) and on a Card page that offers a
  decision (G9), and forbids it on every other response.
- A cancelled or failed user verification sends nothing and leaves the card as
  it was; the next tap asks a new challenge, which replaces the old one.

How it is built (G9), where the points above left a choice:

- The phone reads a card on one page. So the session's reading of a card,
  which the seat keeps server-side (`seatapp.Reading`), counts the Card
  page's rows, its four lists, its refs and its "differs from" block as read
  to their end once the page was served to the session, each only when the
  page showed it whole (`seatapp.Whole`), and a cut path list only by the
  pages of the whole list the session was served that showed every path
  whole, for the card's review: the seat keeps with the pages the review
  digest each was read for (the page's own, which the link checked), a page
  of another review or page count starts the count again, and the link
  counts the pages only for the review they were read for, so pages read
  before a card was replaced (the same id and page count, another review)
  count for nothing on the replacement. Only a GET that the seat answered
  with the page is a read: a `HEAD` of a card page is refused 405 and asks
  the daemon nothing, and a page counts (a read of the card, which settles
  a lost answer and keeps the page's form; a page of the path list) only
  once it was written whole and flushed to the connection without an
  error. A Card page's read is checked again then: a decision of the card
  sent, or an answer lost, since the read began leaves it stale, and
  nothing is noted. The page shows a path or a ref (a ref
  line's ref, the branch a prune's deleted ref was merged into, a flagged
  path and its rule, a path or ref of the "differs from" block) up to 1024
  characters of its escaped text, and cuts a longer one with a count of
  what it left out; a part with a cut item is not read, and a page of the
  path list with one says that it does not count. `seatlink` builds the card
  model's `Reading` from it, and asks `Card.MayApprove` with every flagged
  path signed off to offer Approve (a card that opened under 400 ms ago is
  offered, since the tap is judged again), `Card.Deny` to offer Deny, and
  `Card.Approve` (MayApprove with the boxes ticked) or `Card.Deny` to build
  the frame; a decision they refuse is not sent. Approve is withheld, and
  not sent, for a card the page did not show whole: the page says which
  part ("a name of the card", "a line of the plan", "a ref", "a changed
  path", "a path in a list", "a flagged path", "a line of the lead's note",
  "a path or a ref of how the card differs" is too long to show whole on the
  phone; approve at the desk). So two flagged paths that read the same on
  the page because both were cut are never signed off by their index. Deny
  stays. The names are those the header and the confirm box show, cut at
  64 characters: the project, the role, the verb, the feature, the target
  branch and the class. The project and the target are the owner's own
  configuration (the scratch settings' `project`, a name of at most 63
  characters, which is never cut, and `target`, a branch under
  `refs/heads/` of up to 200 bytes, which can be), and the feature is the
  lead's, at most 48; the record itself bounds neither the project nor an
  effect's ref, so the rule is kept for all of them rather than trusted
  to the configuration: a project whose target branch has a name over 64
  characters is approved at the desk. The plan's lines and the note's
  lines are cut at 4000 characters: the plan is the host's text under the
  binding, which a lead's request shapes, and the note is the lead's, at
  most 2 KiB of raw bytes; its escaped text (a control or an invalid byte
  becomes `\xNN`) can be several times longer, so a note line can be cut,
  and is covered like the plan. A prune's ref line shows the branch its
  deleted ref was merged into and that branch's tip (the first 12 digits),
  as the desk does. The
  Card page of a card that ended, or whose decision is on its way, has no
  form.
- One tap on Approve or Deny asks `POST /api/decide-challenge` with the card,
  the id of the page's form, the action, the indexes of the boxes ticked and
  the reason. Each Card page that holds the form has a form id of its own
  (16 random bytes), which the seat keeps with the session's reading of the
  card beside the binding that page showed (the last 8 pages of a card). The
  seat takes the binding the session read last from its own reading, and
  takes the tap only when the page of its form showed that binding: a tab
  left open on a card that was replaced since (the same id, another binding,
  read in another tab of the same session) is answered "The card changed.
  Look at it again." (`card_changed`), and a form the session was never
  served "Look at the card again first." (`look_again`), before any
  challenge is drawn or the passkey asked. A tap without a form id is
  refused (`bad_request`). The app has no option that lets a POST come
  without `Sec-Fetch-Site: same-origin`: its tests and the end-to-end rigs
  send the header, as a browser does. The link freezes the frame from the card it showed at that
  binding, asks `seat.challenge`, and recomputes the challenge before the
  browser sees it.
  The seat keeps one decision per session; a new tap replaces it. The page
  then names the action, the card, its project, what it does, its target and
  its revision (the binding's first 12 digits), and a second tap, on the
  confirm button, asks one assertion with `allowCredentials` holding only the
  session's credential. `POST /api/decide` burns the kept decision first,
  takes the assertion only for its id and the session's credential, and
  sends the kept frame.
- The largest bodies the page sends are 1230 bytes for a challenge (the
  form's id and 300 sign-off indexes; a deny's reason of 500 bytes all
  escaped is 1138) and
  5672 bytes for an assertion at the bounds of `Assertion`, under
  `seatapp.MaxBody`; a body of MaxBody bytes is read and one byte more is
  refused (413), whether its length was declared or not.
- The link checks the params of `seat.challenge` and `seat.decide` as the
  gate will read them: it encodes them with the wire's encoder, checks
  those bytes with the daemon's decoder (`ownerwire.DecodeSeatChallengeParams`
  and `DecodeSeatDecideParams`, size cap included), and hands the transport
  exactly them. The seat's transport (`seatgate.Client.Call`) sends params
  given as a `json.RawMessage` byte for byte and never encodes them again,
  so `<`, `>`, `&`, U+2028 and U+2029 in a flagged path stay one character
  each on the wire, as the encoder counted them, and are not escaped for
  HTML into six bytes (which made a frame within its cap, such as one with
  flagged paths full of `&`, grow past the gate's cap in transit). The
  transport still holds the bytes to the wire's strict form and the frame
  cap before it sends them, and the gate still reads them strictly. Params
  over the cap are not sent: the phone says "This is too large to send."
  (`too_large`), nothing was decided, and the decision is made at the desk.
  A decision whose record the daemon finds over the record's cap is refused
  `record-too-large` before its challenge is drawn (or, on `seat.decide`,
  before anything is journaled); the seat link takes that code as a certain
  refusal, and the phone says the same, "This is too large to send.".
- Decision challenges are limited to 10 a minute per credential, and the
  seat link asks one only when the room of decisions at the gate (10 calls
  in 80 seconds, see [login](#login-seatlogin-challenge-and-seatlogin)) holds
  it and its decision: a decision the phone signed is never refused for the
  gate's rate.
- `Permissions-Policy` allows `publickey-credentials-get` on a Card page that
  holds the form; the page without it forbids it as every other response
  does. A link that cannot decide (a gate whose hello did not list
  `decide`, as after a session's seat reconnected to a read-only gate, or a
  link without the gate's pin) offers neither approve nor deny, so its Card
  page holds no form, loads no deciding script, forbids the passkey, and
  says "This card cannot be decided now: the desk does not take decisions
  from the phone now."

Before the owner submits, the page tells apart: a cancel ("Cancelled. Nothing
was sent."), a challenge that ran out ("This took too long. Tap again."), and a
desk it cannot reach ("The desk cannot be reached. Nothing was sent.").

After the owner submits, only a refusal says for certain that this decision
was not taken. Every other answer to `seat.decide` (`unavailable`,
`deadline-exceeded`, `door-unavailable`, `journal-reopened`, `failed`), the
seat's own timeout, and no answer at all (`seatgate.ErrDisconnected`) mean the
outcome is unknown: the decision may be journaled and on its way to the door,
or not. Then the seat:

- never sends the decision again, by itself or on a tap, and queues nothing
  (built: the card is marked sent, and in flight, before the decision is
  sent; a refusal clears the mark, and any other answer leaves it, marks it
  again and ends the flight, until a new read of the card: a GET of its
  Card page whose read of the daemon began after the answer was lost. A
  `HEAD` is no read, and a GET made while the decision is in flight, or
  whose read began before the mark, may show the card as it was before the
  decision, so its page holds no form, says "A decision of this card was
  sent and its outcome is not known. Look at it again." and leaves the
  mark. While it stands, a tap is refused without asking the daemon);
- shows "Decision sent. Outcome unknown." and offers only "Look again", the
  Card page read afresh;
- on "Look again", once the gate is back, reads the same card with
  `owner.show` and shows what it says: decided, with the outcome; held; a
  decision on its way to the door; an outcome the door left unknown, which is
  settled at the desk; or pending, and only then may the owner decide again,
  with a new challenge.

So that reading the card can tell a decision on its way from none,
`ownerwire.ShowResult` has a mark for a card whose decision the journal holds
and the door has not yet taken (built, G8b: `on_its_way`, left out when false),
on the socket and the gate alike. G9 tests the lost answer.

A held approve (past the project's limit for the day) comes back pending with
the held mark and the host's line. The page shows, under the host's line:
"Held: the project's limit for today is reached. Nothing was applied. Raise
the limit at the desk, then approve again here or at the desk." A held approve
uses nothing and is never released by itself; a later approve needs a new tap
and a new assertion. An expired card cannot be renewed by asking a new
challenge: issue refuses it.

The words for the codes (built, G9, in `internal/seatapp`); the seat shows no
text of the gate's, and a code it does not know gets the last line:

| Codes | The phone says |
| --- | --- |
| `challenge-stale` | This took too long, or the card changed. Look at the card and tap again. |
| `binding-mismatch`, `review-changed` | The card changed. Look at it again. |
| `approval-expired` | The card's time ran out. |
| `approval-consumed`, `approval-void`, `previous-epoch`, `decision-invalid` | The card cannot be decided now. Look at it again. |
| `assertion-invalid` | The phone's answer was not accepted. If this repeats, check the seat at the desk. |
| `key-revoked`, `key-unknown` | This phone's passkey is no longer paired. Pair it again at the desk. (The session ends.) |
| `kind-refused` | This project does not take decisions from this kind of passkey. |
| `decisions_inhibited` (the seat's own, not a gate code) | Decisions are paused on this host. Nothing was sent. |
| `challenges-full`, `busy`, `rate-limited`, `too-many-refusals` | The desk is busy. Try again in a few minutes. |
| any other | Something went wrong. Look at the card again. |

`decisions_inhibited` is not an answer of the gate: the seat itself refuses
(503, nothing sent, the challenge's room given back) while the authority
host's decision inhibit is there or its guard folder cannot be read; the
card stays as it was and the owner may tap again once decisions are taken.

Built so (G9). An answer to `seat.decide` with one of the five codes that
leave the outcome unknown, the seat's own timeout and no answer say
"Decision sent. Outcome unknown."; a code of "any other" after a decision was
sent says the last line and also leaves the card marked sent until it is read
again. A challenge the seat link recomputes differently says that the desk's
challenge is not this card's and that the passkey was not asked. A held
approve shows the host's line and, under it, the held words above.

## Live refresh: seat.wait

Status: built (G14, first part) and tested on loopback; not qualified, and
scratch-only like the rest of the phone path. Without it the owner reloads a
page to see a new card. With it, the seat watches the set of pending cards at
the gate with one long call, and the pages ask the seat, not the gate, whether
it moved.

The owner's goal is to monitor and approve the running tasks as they come up.
The seat has one connection to the gate with 60 requests a minute to spend, so
the design keeps the number of calls of the gate independent of the number of
browsers: the seat holds one wait for the whole seat, and the browsers' polls
end at the seat.

### The operation

`seat.wait`, capability `watch`, the gate's alone (in `ownerwire.GateOperations`,
not in `ownerwire.Operations`; the daemon's socket neither serves nor announces
it). The gate lists `watch` in its hello when its service can wait
(`gatehost.WaitPort`, which `ownerchannel.Service` is); a seat that does not
find it there starts no watcher.

- Params, `ownerwire.SeatWaitParams`: `since`, always written, either empty (the
  seat has no version yet) or a version of 32 lower-case hexadecimal digits;
  anything else is `bad-params`.
- Result, `ownerwire.SeatWaitResult`: `version` (32 hex digits), `waiting` (how
  many cards are pending, the whole set), `cards` (a list of
  `ownerwire.SeatWaitCard`: `id`, `kind`, `binding_sha256` and `held`, never a
  line of a card's text; at most `ownerwire.MaxSeatWaitCards`, 128, none twice)
  and `more` (true when `waiting` is more than the cards listed). The result
  is at most `ownerwire.MaxSeatWaitResultBytes` (24 KiB). `kind` is a word of
  at most 16 bytes; every card today is `approval`
  (`ownerwire.CardKindApproval`), and the field is there so that a card of
  another kind can be told apart later. `held` is the daemon's mark "decided
  past the project's limit for the day".
- Answer: at once when `since` is empty or is not the pending set's version;
  otherwise the call is held until the version changes, until the hold ends, or
  until the request ends. A hold that ends with no change answers the current
  result, whose version is `since`, so the seat takes an equal version as "no
  change".

### The version

`ownerwire.PendingVersion` is the one function that makes a version: the first
16 bytes, in lower-case hexadecimal, of the SHA-256 of a canonical encoding of
the whole pending set: the domain string `ataniq.seat-wait.pending/1`, the
number of cards, and for each card in the order of its kind, id, binding digest
and held mark, each field as a 4-byte big-endian length and its bytes (the
held mark one byte). The order of the items given does not matter. The set
hashed is the whole set, not the at most 128 cards of a result, so a change
past the list changes the version too.

It is a digest and not a counter on purpose: a daemon that restarts with the
same pending set answers the same version, so a seat that held it is not woken
for nothing. A field left out of the digest would be a change a seat never
hears of; the kind, the binding digest and the held mark are in it, so a card
that was replaced under the same id, or held, wakes the seat.

### The daemon's bounds

`ownerchannel.Service.WaitPending` (`wait.go`) answers `seat.wait`:

- The hold is at most `Config.WaitHold`, 25 seconds by default, and in any case
  ends at least 1.5 seconds before the deadline of the request (the gate's 30
  seconds). The look at the end of a hold may wait for the token of another
  wait's look and for the one-second floor, and then asks the door, so a slow
  door can still run it past the deadline. The gate then cuts the request with
  `deadline-exceeded`: the margin makes that rare, and does not rule it out. The
  seat's watcher takes it like any refusal with a code, backs off (1 second,
  doubling) and asks again with the version it kept, so it costs a pause and no
  card.
- What wakes a held wait: a poke after each write of the journal and of the
  handles (a decision, a held decision, a refusal, a count, a limit, an alert,
  a delivery, a use, an outcome, and an `ExpireHeld` that closed a card); a
  poke at the end of each tick (every 30 seconds by default,
  `hostadapter.DefaultTickEvery`), which is also how a change at the door that
  the daemon did not write, such as a new card or a void by the broker, is
  noticed: such a card reaches the seat within about a tick, not at once; a timer at the
  earliest expiry of a pending card, since the door ends a card by the clock
  and writes nothing; and the end of the hold, which looks once more.
  Nothing watches the door itself. A "looked" mark wakes nobody: no version
  covers it.
- The door is listed at most once a second for the whole service (the 1-second
  floor), however many waits are held and however many pokes come. A poke
  within the second is answered by one look at its end; a look that was made
  while a poke came (its own `ExpireHeld` pokes) is followed by one more a
  second later, which finds nothing to poke, so a wait cannot spin on its own
  side effects.
- At most `Config.WaitMax` (8) waits are held at once; one more is refused
  with `ownerchannel.ErrWaitFull`, which the gate answers `wait-full`.
- A held wait takes no lock of the service while it waits, and a wait that
  ends, by an answer or by its request, leaves no goroutine and no timer.

### The gate's bounds, and wait-full

`gatehost` holds at most `gatehost.MaxWaitsPerPeer` (2) waits at once for a
peer, across its connections. The seat holds one; the second is room for a
reconnect, since the seat's old wait unwinds (up to 25 seconds) while its new
connection already asks. A third, or a call the service itself refuses with
`ErrWaitFull`, is answered `wait-full` before anything is looked at. A held
wait takes one of the connection's 16 handler places while it is held, and
counts as one request of the 60 a minute when it is sent.

`wait-full` is not counted by [the refusal bound](#the-refusal-bound). It is a
load answer, not a misuse: counting it would let a reconnect that meets its
own unwinding wait, or a few seats' waits held by the service, shut a good
seat out, with its reads and its decisions, for 10 minutes. Its log line
(`seat gate: seat.wait refused wait-full (peer <16 hex digits>)`) is written
at most once a minute for each peer instead, so a loop cannot fill the log
either. The seat answers it with a pause of 5 seconds and shows no fault.

### The seat's single watcher

One goroutine of `internal/seatlink` (`watch.go`), started once by
`ataniq-seat`, keeps one `seat.wait` in flight for the whole seat, whether or
not a browser is open. It does nothing and spends no room until the gate's
hello lists `watch`.

- One wait at a time, with the version of its last answer as `since` and a
  context of 29 seconds (the 25 of the hold and 4 of the network; the gate cuts
  a request at 30).
- After an answer that changed the version it waits 2 seconds before the next
  wait, so that a burst of changes is a few wakes and not a wait for each.
- Its room is the kind of call `kindWait`, 6 in the budget's 80 seconds. An
  idle seat sends one wait about every 25 seconds, 4 in 80 seconds, which
  leaves 2 for the waits that a change cut short. With no room the watcher
  sleeps until some frees; nothing is refused, the pages lag.
- On a lost link, or a call that fails with no answer of the gate's (its own
  29-second timeout, an answer not of the wire's form), it publishes "offline"
  and tries again after 1 second, doubled at each failure in a row to 30
  seconds, keeping its `since`; a link that is back lifts "offline" at once.
  Any answer of the gate lifts "offline" as it arrives, a coded refusal (such
  as `bad-params` or `unavailable`) too, since a code proves the link is up:
  it does not wait for a wait that the gate then holds for 25 seconds. A coded
  refusal is retried with the same doubling and publishes no "offline" of its
  own. After `wait-full` it pauses 5 seconds.
- It publishes its state (`seatapp.WatchState`: whether it watches, the
  version, the count, the cards, `more`, offline, and when) and a broadcast
  that is closed at each change. A subscriber takes the channel first and the
  state after, so it misses no change. Publishing never blocks and starts no
  goroutine.

The seat's 60 requests a minute are now split in rooms of pings 6, decisions
16, anyone's (`seat.pair`, `seat.login-challenge`, `seat.login`) 12, reads 20
and waits 6, which add up to 60. The waits' 6 come from the reads, which had 26
(see [login](#login-seatlogin-challenge-and-seatlogin)); the code checks the
sum, and that an idle watcher's waits fit their room with two to spare, when it
is compiled.

### The browser's long poll

The browser does not talk to the gate. `POST /api/watch` (`internal/seatapp`,
`watchpoll.go`) goes through the same checks as every POST of the app (the
Host, `Origin` and `Sec-Fetch-Site: same-origin` rules, a JSON body of at most
`seatapp.MaxBody`) and asks the seat's watcher, never the gate.

- Body: `version` (what the page has, up to 64 hex digits, possibly none) and,
  for a Card page, `card` (the id) and `binding` (64 hex digits).
- The session is looked at with `sessions.look`, which does not mark it used: a
  poll never extends the session's idle time (see below). A request without a
  live session is answered 200 with the state `ended`.
- A credential's polls are limited to 12 a minute, apart from the reads, since
  a poll asks the gate nothing and costs it no room; a script that polls
  without pause is answered 429 `rate_limited`.
- It is answered at once when the watcher does not watch, is offline, or has
  another version than the poll's. Otherwise it is held until the state
  changes, 25 seconds pass (`watchHold`, under the 30-second write timeouts of
  the seat and of the shipped Caddyfile), a newer poll of the same session takes
  its place (`replaced`), the browser goes away, or the session ends. At most 3
  polls of a session are held, one for each tab, and a fourth takes the place
  of the oldest, which bounds the held requests by three times the number of
  sessions. A replaced poll is answered with its own state, which the script
  waits out for 25 seconds before it asks again: an answer `same` would be
  asked again at once, and two tabs of one session would take each other's
  place in turn and use up the credential's 12 polls a minute. With the tabs
  held and the replaced one waiting, each tab polls about every 25 seconds,
  so up to four tabs stay within the limit.
- Answer: `state`, one of `same`, `changed`, `replaced`, `offline` and `ended`, the
  `version` and `waiting` (the count), and `card`, one of `live`, `changed`,
  `gone` and `unknown`. The answer holds no text of a card. `card` is `gone`
  when the card is not in the list and the list is the whole set, `changed`
  when it is listed with another binding, `unknown` when the watcher cannot say
  (more cards than listed, offline, or no card named) and `live` otherwise.

A page takes the watcher's version before it reads the daemon for its own
content (`watchVersion`), so a change between the two makes the page's version
older than what it shows and its first poll answers `changed` at once: a page
can be told of a change it already shows, and never misses one it does not.

`/static/watch.js` serves the card list and the Card page, with the element
`#watch`, whose `data-live`, `data-version`, `data-card` and `data-binding`
carry what the page was made from. The Card page names its card and binding
only when the card is pending: a decided, ended, expired or refused card is not
in the pending set, so a page that named it would be told `gone` for a card
that moved long ago, or that this session decided on the page before. Such a
page carries `data-still` instead, polls for the status line only and never
reloads. A page that sent its decision marks the document (`data-sent`) when
its confirm button is tapped, and clears the mark when the card is known to be
as it was; the script then withdraws the offer when the card is `gone` and
does not show the line "decided elsewhere", since the page itself says what
became of the decision. It polls once at a time, at least 2 seconds
apart, and not while the page is hidden; it polls at once when the page is shown
again. It puts on the page only texts that are its own constants, set as text
and never as markup, so that the page's content security policy
(`script-src 'self'`, `require-trusted-types-for 'script'`) holds and nothing of
an answer is written into the page. One status line (`#watch-status`) says:

| Line | When |
| --- | --- |
| (empty) | The page is live and the last answer was good. |
| Not live | The page was made while the seat watched nothing (the gate lists no `watch`, or the link is not up). The script polls no more; reload to try again. |
| Not live: the desk is out of reach | The answer was `offline`. It asks again after 30 seconds. |
| Session ended. Log in again. | The answer was `ended`. It polls no more. |
| This card changed or was decided elsewhere. Look again. | On a Card page: the card is `changed` or `gone`, or it is `unknown` and the version moved. With the links "Back to the cards" and "Reload". |

- The card list reloads when its version moved, at most once every 20 seconds
  since it was loaded (a reload is put off until the page's own read, which is
  the page's load, is 20 seconds old), and not while a field holds typed text.
  The gap keeps the reloads inside the reads' room: each reload is one
  `owner.list`, and the reads have 14 calls in 80 seconds. It is a stated
  limit, not a bound: three card lists open in a busy period (the watcher
  publishes up to about 6 changes in 80 seconds, each list reloads at most
  every 20 seconds, so a list takes up to 4 of the 14 reads) can still use
  most of the 14, and the owner's own taps on a card may then be answered
  `desk_busy` until the room frees. No second room is built for the reloads.
- A Card page never reloads by itself, since the owner may be ticking
  sign-offs. When its card is `changed` or `gone` it shows the line above,
  disables every control the page marks with the attribute `data-offer` (the
  sign-off boxes, the reason, Approve, Deny and the confirm button), marks the
  document so that the decide script keeps them off in every path that would turn
  a control on (a cancel at the confirm step does not enable the confirm button
  of a stale page), and polls no more.
- A 429 answer is waited out for 60 seconds; any other failure of the poll is
  retried after 5 seconds, doubling to 60, with nothing shown.

### The stale-offer rule

A wake only prompts a fresh read. The poll tells a page that something moved;
the page then reloads from the seat, or withdraws its offer. It never builds
its content, or its offer to decide, from an answer. The authority stays where
it was: the seat takes a tap on Approve or Deny only when the page's form
showed the binding the session read last (`card_changed`), the daemon draws a
challenge from its own record and refuses a decision whose binding or state
moved (`binding-mismatch`, `challenge-stale`, `approval-consumed`,
`approval-void`), and the assertion binds the card's digest. A stale Approve is
refused at the daemon whether or not the script ever ran; live refresh only
withdraws the offer sooner, so that the owner is not left to find out by a
refusal.

### A poll never extends a session

A session ends after 30 minutes without a request and at 12 hours at most. The
poll is a request that does not count: a page left open does not keep its
session alive, and the poll's answer `ended` tells the page when the session is
gone. A page that reloads itself because the list changed is a page request,
which counts, as it is the owner's own sight of a new card. That a poll does not
extend the idle time is a settled design choice, not a limit of the build.

### Why not server-sent events

A stream would update at once with no polling, and is not used. The seat's
`WriteTimeout` is 30 seconds, as is the `write` timeout of the shipped Caddyfile
(`deploy/authority-host/seat/etc/caddy/Caddyfile`); either cuts a response that
is still open at 30 seconds, so a stream would have to be reopened every 30
seconds and would need those timeouts lifted for one path, which widens what a
slow or stuck connection may hold. A poll answered within 25 seconds fits both
timeouts as they are, and a front proxy with a read timeout above 30 seconds
(see [the operator guide](../seat-gate.md#the-reverse-proxy)).

Notices, which tell the phone that a card is waiting while no page is open,
follow the watcher's state described above: they are the reason the watcher
runs when no browser is looking (see [the notices section](#notices)).

## Notices

Status: built (G14, the rest: N1 the notice core, N2 the push library, N3 the
service worker and the pages, N4 the notifier unit) and tested on loopback with
fakes. Not deployed, not phone-tested, not qualified, and scratch-only like the
rest of the phone path. Live refresh updates a page that is open; a notice
tells the owner that a card is waiting when none is. It is a hint that the
owner should look, and nothing else.

### The three processes

```text
ataniq-seat  --(Unix socket, one request and one answer)-->  ataniq-seat-notify  --(HTTPS)-->  Apple's push service  -->  the phone
 (seat user)      a count, 0 to 999, and the phone's              (own user)                      web.push.apple.com
                  subscription                                    VAPID key, subscriptions
```

- The seat (`ataniq-seat`) decides when to notify, from the state its watcher
  holds. About the cards it tells the notifier one integer, how many are
  waiting, and nothing else: no card, id, text or time crosses the socket (the
  port the app calls, `seatapp.Notifier`, has no method that could carry one).
  The same socket carries the phone's subscription, which the seat relays when
  the owner turns notices on or off and keeps nowhere. The seat reaches the
  notifier's socket and still no other network address than the gate's: the
  socket is not a network address and gives it no way out.
- The notifier (`ataniq-seat-notify`) is a separate unit with its own user. It
  holds the VAPID key and the push subscriptions, and is the only part that
  talks to a push service, through the package `internal/webpush`. The seat
  never imports that package (a test of the seat's imports holds it to that).
- Apple's push service takes the encrypted message and delivers it to the
  phone, which wakes the service worker of the Home Screen app.

The split is so that the seat, which holds the phone's session and relays its
decisions, keeps no way out to the network but the gate: a notice never
decides, and what the notifier reaches does not widen what the seat reaches.
The unit's release is `deploy/authority-host/notify`, laid over the seat's (see
[the authority host's README](../../deploy/authority-host/README.md#the-notifier)).

### The socket's protocol

`internal/seatnotice` holds the codec, the client and the server. The client's
call and the server's turn with a connection each end 2 seconds after they
begin, except a notify, which sends its pushes before it answers and has 15
seconds (the request is still read within the 2). The notifier holds one push
to 5 seconds and all the pushes of a notify to 12, so that its answer is in
before the 15 end, and the seat's own bound on the call is 20 seconds: the
client's deadline is what ends a slow call. One connection carries one request and one answer.

- A frame is a 4-byte big-endian length and then that many bytes of one JSON
  object; the length is 1 to 4096. The object is strict: valid UTF-8, no byte
  before or after it, no repeated key at any depth, no key that the operation
  does not have, every key in its exact case, nested at most 4 levels.
- The requests: `{"op":"info"}`; `{"op":"notify","n":N}`, with N an integer 0 to
  999 written as plain digits (no sign, leading zero, fraction or exponent);
  `{"op":"subscribe","sub":{"endpoint":URL,"p256dh":B64,"auth":B64}}`; and
  `{"op":"unsubscribe","endpoint_sha256":HEX}`.
- A subscription is bounded on the wire: the endpoint is an https URL of at most
  2048 bytes with a host, no user information, no fragment and only visible
  ASCII; `p256dh` is the unpadded base64url of a 65-byte uncompressed P-256
  point (the first byte 0x04); `auth` is the unpadded base64url of 16 bytes. An
  unsubscribe names a subscription by the SHA-256 of its endpoint, 64 lower-case
  hex digits, so that the endpoint travels once, in the subscribe.
- The answers: `{"ok":true}` with the fields of the operation (`info`:
  `vapid_public` and `subscriptions`; `notify`: `sent`), or
  `{"ok":false,"code":CODE}` with a code of `[a-z0-9_]{1,40}`. The notifier
  uses `bad_subscription`, `too_many`, `endpoint_refused`, `unavailable` and
  `rate_limited`. A frame that breaks a rule is a protocol violation, and what
  the client and the server report of it holds no byte of what the peer sent.
- Who may call: the server reads the peer's user id from the kernel
  (`SO_PEERCRED`) and serves only the one id it was given, the seat user's,
  looked up once at the notifier's start; a connection of any other user is
  closed unread. The socket's mode (0660, group `seat-notify`, a group of the
  notifier's own that the seat's unit joins) lets the seat connect and is not
  what decides; the notifier is in no group of the seat's, so it cannot reach
  the seat's socket. On a system that cannot read a peer's user
  id every connection is refused. At most 16 connections are served at once.

### What a notice shows, and never shows

A notice shows one line, "N waiting" (N the count, 0 to 999), or "Cards are
waiting" when the push is anything the service worker does not understand. It
has no body text, no buttons and no data. Its tag is `waiting`, so a newer
notice replaces an older one in the phone's list instead of piling up, and it
does not sound again on a replacement (`renotify` is false). Tapping it opens
the card list, `/cards/`, a fixed address, whatever the push said.

A notice never:

- decides: it has no action, and a decision needs a POST with a fresh passkey
  assertion on the Card page, as before;
- carries a card: the push holds only `{"n":N}`; no text of a lead, project,
  verb or id is in it, and none is ever given to the notifier;
- links anywhere but `/cards/`: no `next` address, no deciding or identifying
  link, and the worker, not the push, supplies the text and the link, from the
  seat's own origin.

### The trigger rules

The notice core (`internal/seatapp/notice.go`) is pure: each call is given the
time and the watcher's state. The app's goroutine, started by `seatapp.Open`
when `--notify-socket` is given, applies it. A notice is due when the cards
waiting are more than what the core last announced, and then it is sent unless
one of the holds below applies. The rules:

1. Only a live state counts. When the watcher is not running (the gate does not
   list `watch`) or is offline (the link to the gate is down), the core says
   nothing, since the count may be old. No notice is stored for later:
   when the watcher is live again the count is judged afresh, and cards still
   above what was told bring the notice then.
2. A count above the one last announced is due at once. A card decided and
   another asked is not a new notice within 2 minutes of the last one: the
   count that comes back up from a dip, to no more than what was announced, is
   due only once those 2 minutes have passed (the floor it must exceed is then
   the least the count was in the meantime). So when the count does not rise,
   there is at most one notice per 2 minutes.
3. No notice while a browser polled in the last 60 seconds: a page that polls is
   open and the owner is looking at it. Only the time of the last accepted poll
   is kept, with no part of it or of its session.
   The count of cards waiting that a live poll found is also kept (a number,
   no more), and counts as seen: the floor of rule 2 is at least that count,
   so a card the owner looked at on the page does not bring a notice a minute
   after the page is closed. A card that comes after the last poll does.
4. After a call to the notifier that failed, nothing is sent for 1 minute; each
   failure after it doubles the wait, to a longest of 30 minutes, and a call
   the notifier took ends the run. A failure is logged at most once a minute,
   with fixed text.
5. At most 30 calls to the notifier in a window of 24 hours that begins with the
   first of them, counting only the calls the notifier answered: one it took
   and one it refused with a code (`rate_limited` too). A call that got no
   answer (a timeout, a connection refused or lost) is not counted, since
   nothing was sent to the phone for it, so a notifier that is down for an hour
   does not use up the day's notices. As a safety valve, at most 200 calls of
   any end in the same kind of window. A notice that is due past either bound
   waits for its window to end.
6. The tally, the floor and the back-off are in memory only: a restart of the
   seat resets them. At worst one more notice is sent for the cards then
   waiting, if no page is polling.

The count told to the notifier is the number of cards waiting, at most 999. The
notifier has bounds of its own that do not trust the seat (below).

### The service worker

`/sw.js` is the worker the page registers (scope `/`, which the script's own
address already gives; the seat answers `Service-Worker-Allowed: /`). It
handles two events and nothing else: no handler of requests, no store, no
other script loaded.

- `push`: it shows a notification whose title is "N waiting" when the body is
  JSON, an object whose one member is `n`, an integer 0 to 999; it shows
  "Cards are waiting" for every other push (another member, another type, no
  body, a body that is not JSON). The text is the worker's own.
- `notificationclick`: it closes the notification, then takes a window of the
  seat's origin that is open (if any) to `/cards/` and focuses it, or opens a
  window on `/cards/`. The windows it looks at include those the worker does
  not control (`includeUncontrolled`): the page that registered it stays
  uncontrolled, since the worker does not take control of pages, so at the
  first notice that page may be the only one open. A worker cannot navigate
  such a window, so it navigates first, before anything that uses up the tap's
  permission to show a window, and when the navigation is refused it opens
  `/cards/` in a window of its own; a window is focused only once navigated.

`/sw.js` is served with `Cache-Control: no-cache`, the one response of the app
that is not `no-store`, so that the browser checks the script for a change with
a conditional request; the shipped Caddy sets `no-store` on everything it
forwards, which makes the browser fetch it each time. The manifest
(`/manifest.webmanifest`: name "Authority seat", start address `/cards/`,
scope `/`, display `standalone`) and the icons are served without a session;
they hold nothing of the owner's.

### The phone

On iPhone, notices work only from the Home Screen app: the page says so, and
`push.js` reads whether it runs standalone (`navigator.standalone` or the
`display-mode: standalone` query) and, when it does not, shows "Open this page
from the Home Screen app to turn on notices." and offers no button. The page
also says "This browser cannot show notices." when the browser lacks service
workers, push or notifications.

The Notices section is on the card list only, behind a session. Enable notices
does, in this order, inside the one tap that the browser accepts a permission
prompt from: ask the permission; register the worker; ask the seat for the push
key (`POST /api/push/info`); subscribe the browser with it (user-visible
only); hand the seat the subscription (`POST /api/push/subscribe`), which
relays it to the notifier. A subscription the seat did not take is dropped in
the browser again. Stop notices names the subscription by the hash of its
endpoint (`POST /api/push/unsubscribe`), has the notifier forget it, and then
drops it in the browser; if the notifier fails, the browser keeps its
subscription and the tap can be made again. The notifier keeps at most 4
subscriptions; the same endpoint again replaces its keys, and a fifth is
refused (`too_many`), which the page shows as "No room for another phone. Stop
notices on another phone first."

Every text the script puts on the page is a constant of its own, set as text,
never as markup and never taken from an answer of the seat or the browser. It
reads an answer only for its status and for the one code `too_many`.

The three routes need a session, count as a use of it (they are the owner's
taps, unlike a poll), take a body that is one JSON object of exactly the call's
fields, and are limited to 6 calls a minute for a credential, the three
together. A call to the notifier is bounded to 5 seconds. They answer only
`{"ok":true,...}` or `{"ok":false,"code":CODE}`, with the code
`no_session` (401), `bad_subscription` (400), `too_many` (409),
`endpoint_refused` (422), `unavailable` (503, which also covers a notifier that
cannot be reached or is too slow) or `notifier_error` (502, a refusal with a
code the seat does not tell apart), and `rate_limited` (429) from the limit.
Nothing the browser sent, and nothing of the notifier's text, is in an answer;
no page, answer or log of the seat shows an endpoint back. The seat checks the
subscription's form before it relays it, and the notifier checks again.

Without `--notify-socket` none of this exists: `/sw.js`, `/manifest.webmanifest`,
`/static/push.js`, the icons and the three routes answer 404, the card list
shows nothing about notices, and the pages carry no manifest link.

### The page's security

The content security policy gains two sources and one directive, and nothing
else: `manifest-src 'self'`, `worker-src 'self'` and `trusted-types seat-sw`. The policy stays closed
(`default-src 'none'`; scripts, styles, connections and images from the app's
own origin only; nothing inline) and still requires Trusted Types for script
sinks (`require-trusted-types-for 'script'`).

The sink finding. The Service Workers standard makes the `register()` call of
`navigator.serviceWorker` a script-address sink: it takes a TrustedScriptURL or
a string (see the `ServiceWorkerContainer` interface in
[the standard](https://w3c.github.io/ServiceWorker/)), so with the rule above a
page must give it the answer of a Trusted Types policy rather than a plain
string. It is the one script-address sink the
pages' scripts use (the others set text, and navigate or make links, and reach
no sink that takes markup, code or a script address). So `push.js` makes one policy,
`seat-sw`, whose `createScriptURL` answers for the address `/sw.js` and throws
for any other, and gives `register()` its answer; a test of the script's source
holds it to one policy and one `register()` call. The CSP's `trusted-types`
directive names that policy, `seat-sw`, and no other (no `default`, no
`allow-duplicates`), so the browser refuses any other policy name and a second
policy of the same name: a script that runs on the origin cannot make a policy
of its own to reach a sink. A test holds that every policy any script of the
app creates (`push.js` is the only one that creates any: `pair.js`, `words.js`,
`login.js`, `decide.js`, `session.js` and `watch.js` set text and make no
policy) is named there, and that the directive names no other. What limits the
`seat-sw` policy itself is its own function. Not checked in a real browser: that
iOS Safari, in the Home Screen app, accepts the registration under this rule
is for the phone trial to show. If the registration is refused there, Enable
notices ends in "Could not turn on notices. Try again later." and nothing else
changes.

### The notifier unit

`ataniq-seat-notify run` keeps the subscriptions in a strict file of mode 0600
(read exactly as the command writes it, written atomically, at most 4
subscriptions, at most 16 KiB) and the VAPID key in a file of mode 0600 made by
`vapid-init`, which never overwrites a key. Its bounds, which hold whatever the
seat asks:

- It keeps and posts only to an https endpoint on port 443 whose host is on its
  list: exactly `web.push.apple.com` unless the command is given another list
  (`--push-hosts`, which the unit does not pass), with no wildcard, no
  subdomain, no IP address, no redirect followed and no proxy from the
  environment. It refuses another endpoint when it is subscribed
  (`endpoint_refused`).
- A notify sends `{"n":N}` to each subscription in turn, with the topic
  `waiting`, urgency normal and a time to live of 1 hour (what a push service
  keeps a notice for when the phone cannot be reached), each push within 5
  seconds and all of them within 12. A subscription the push service reports gone (404 or 410) is
  dropped; one that failed otherwise is kept.
- At most 30 pushes in any hour, every subscription together, whatever the
  seat asks, and each attempt counts. When none could be sent for that reason
  it answers `rate_limited`, which the seat counts as an answered call and
  treats as any failure (the back-off above).
- It logs fixed lines and counts, never an endpoint, a key or the contact.

The unit's egress is the host's one outbound path besides the gate's, and the
authority host's README records that the owner approved it for Apple's push
service, for this unit and for nothing else. The unit's own
`IPAddressDeny=any` stays; the owner's drop-in allows the resolver and the push
service's address range and no other address, and the host's firewall, the
owner's own rule, keeps the unit's user to the resolver and to TCP port 443 of
that range. The host check above is the command's own and is made again by the
sender; the kernel's rule is what holds when the command does not. The unit
holds no provider key and is no AI tool (the no-AI check reads its unit and
environment file as it reads the seat's). See
[the README](../../deploy/authority-host/README.md#the-notifier) and
[DEPLOY.md](../../deploy/authority-host/DEPLOY.md#deploying-the-notifier) for
the unit, the drop-in and the order of the owner's steps.

### Privacy

The payload is encrypted to the phone's key (aes128gcm, RFC 8291), so Apple's
push service cannot read it. It does see that the notifier sent a push to a
subscription, when, and how large the message was: the body is the encryption's
fixed header and tag and the payload `{"n":N}`, so its size says at most how
many digits N has. It also sees the headers of the request, which are in the
clear: the topic `waiting`, the urgency and the time to live, and the VAPID
token's audience, its expiry and the owner's contact (`--vapid-subject`, a
`mailto:` or `https:` value of the owner's own, which is in no release), and
the notifier's network address. The seat host sees the subscription's endpoint
once, when the phone subscribes, and keeps none.

### What a taken-over seat or notifier can do

- A taken-over seat can tell the notifier any count, 0 to 999: it can fake a
  notice ("999 waiting") or send none. It can spam, but only up to the
  notifier's 30 pushes an hour. It can fill the notifier's four subscription
  places with endpoints on the allowed host so that the owner's phone is refused
  (`too_many`), and it can have the notifier forget a subscription whose
  endpoint it has seen pass through it, since the phone's calls go through
  the seat. It cannot send anything else, because it holds no VAPID key and the
  notifier takes only a count from it. It can decide nothing, as before.
- A taken-over notifier holds the VAPID key and the endpoints, so it can send
  the phone any push. The worker shows a push as "N waiting" or "Cards are
  waiting" only, with the one link `/cards/`, so it cannot put a text, a link or
  an action on the phone. It learns a count and nothing of a card. Its network
  rule is an address range and a port, not a host name: what the range holds
  besides the push service is reachable from the notifier, which is what the
  owner's rule allows. It can decide nothing.
- A notice that is false costs the owner a look. What it can lead to is a
  login: see the last bullet of [limits that stay](#limits-that-stay).

### What does not exist

Quiet hours are not built: a notice is sent at any hour the rules above allow.
There is no email or other fallback, so a phone that cannot be reached gets
nothing (the push service keeps one notice for the time to live, the latest of
the topic). There is no notice for a single card, no text of a card and no
count past 999; and no question cards, so a notice never asks the owner
anything.

## Guided new project

Status: the wire is built (`ownerwire`, `genesis.go`) and tested, with the
forms of the question catalog and the brief held in step by tests of
`internal/genesis`. The daemon's side is built too (genesis G4): the thread
store (`internal/genesis/thread`), the gate handler's route and the capability
`genesis` (`internal/approval/gatehost`), and the daemon's wiring
(`internal/leaddaemon`, `cmd/ataniqd`); an end-to-end test runs a whole
interview over the real gate with the seat's link replaced by a raw frame
sender. The seat's side is built (genesis G5): the verb `list`, the seat link's
call with a room of its own (`internal/seatlink`, `genesis.go`), the seat's
pages (`internal/seatapp`, see [the seat's pages](#the-seats-pages)) and the
command's wiring (`cmd/ataniq-seat`, which needs nothing configured). It is not
deployed and not phone-tested. The plan compiler is built as a library (genesis
G10, `internal/genesis/plan`, see [the plan](#the-plan)), and the daemon serves
it (genesis G11a): the verb `plan`, a preview of the plan of a complete brief
before finish and the plan stored at finish. The seat's link and its plan page
are built too (genesis G11b, see [the seat's pages](#the-seats-pages)); the
charter is a later stage.

The owner starts a new project on the phone by answering a few multiple-choice
questions. The questions are the host's own, from a versioned catalog
(`internal/genesis/catalog`), walked by host code: no agent chooses or words a
question. The answers fill a typed project brief (`internal/genesis/brief`) and
nothing else. An answer is a message from the owner and never an authority:
no member of these forms is, or leads to, a decision record, a contract, a plan
token or an approval, and a question page never shows Approve.

### One operation, nine verbs

`seat.genesis`, capability `genesis`, the gate's alone. Its params are
`ownerwire.SeatGenesisParams`, a union tagged by `verb`. A verb takes exactly
the members below, and a member of another verb is refused `bad-params`:

| Verb | Members | What the host does |
| --- | --- | --- |
| `start` | none | Opens a thread, chooses its id (16 lower-case hexadecimal digits) and answers its first round. At most 2 threads are open on the host, for every pinned seat together (the threads are the daemon's, not a seat's); a third is `genesis-busy`. Starting is free. |
| `list` | none | Answers the host's open threads (state `threads`), the same for every pinned seat, oldest first, each where it stands and none of its answers. A finished or discarded thread is not listed. It is how a seat that lost its own memory of the threads it started (a restart) finds them, so that `genesis-busy` never locks the owner out until a thread expires. A read, free like `round`. |
| `round` | `thread` | Answers where the thread stands. |
| `answer` | `thread`, `round`, `answers` | Applies 1 to 3 answers, each to another node, to the round named, and answers the next round or the summary. |
| `change` | `thread`, `node`, `answer` | Replaces an earlier answer. It clears the answers that depended on it (the catalog's invalidation closure), keeps them in the brief's history, and says how many in `reset`. |
| `summary` | `thread` | Answers the summary. |
| `finish` | `thread` | Freezes the brief and answers its digest. When the brief is complete and a template serves its kind, it compiles the plan, revision 1, and stores it in the same write as the frozen brief; otherwise (the owner stops early, or the kind is in the owner's own words) it freezes the brief with no plan. After it, `answer`, `change` and `discard` are `genesis-done`. |
| `discard` | `thread` | Drops an open thread. A finished thread is `genesis-done`: its brief stays as it was finished. |
| `plan` | `thread` | Answers the thread's project plan (state `plan`). On an open thread whose brief is complete, a preview: compiled for the read, `stored: false`, kept nowhere, the same plan and digest for the same brief. On a finished thread, the plan stored at finish, `stored: true`. A brief that is not complete is `genesis-not-ready`, a kind no template serves `genesis-no-template`. A read, free like `round`. |

An answer is `{node, kind, ids, text}`, and a change's `answer` is
`{kind, ids, text}`. The kind is one of `choice` (exactly one id), `choices`
(1 to 8 ids, none twice), `other` (the owner's own text, 1 to 200 bytes of plain
text, not blank), `you_decide` and `skip` (neither ids nor text). An id is 1 to
32 lower-case letters, digits, `_` and `-`, starting with a letter or a digit
(a step of a scale is `1` to `5`; a yes-or-no answer is `yes` or `no`). The wire
holds the form; whether the thread exists, the node is a question of the
round, the option is one of the question and the text looks like a secret are
the host's, and each has its code above. The text of an `other` is checked for
secrets by the host on the way in and refused `genesis-secret-shaped`: a
password, a key or a token is never typed on the phone, and the brief records
only that a credential is needed, by reference.

### The result

`ownerwire.SeatGenesisResult`, a union tagged by `state`:

| State | `round` | `nodes` | `groups` | `digest` | `reset` |
| --- | --- | --- | --- | --- | --- |
| `question` | 1 to `of` | 1 to 3 questions | none | none | 0 or more |
| `summary` | 0 to `of` | none | given | none | 0 or more |
| `finished` | 0 to `of` | none | given | given | 0 |
| `discarded` | 0 | none | none | none | 0 (progress 0 of 0) |
| `threads` | 0 (`of` 0) | none | none | none | 0 (progress 0 of 0) |
| `plan` | 0 to `of` | none | none | none | 0 (progress all filled) |

Always but for `threads`: `thread`, `of` (the most rounds, 1 to 6), `progress`
(`filled` of `total` required fields, and `plan_ready: true`, left out when
false, when `plan` would answer a plan: one is stored, or the brief is
complete and a template serves its kind), `catalog_digest` (the digest of the
catalog the thread is walked under, 64 lower-case hexadecimal digits: a thread
stays on one version) and `reset`. `threads`, the answer to `list`, names no
thread and no catalog (both are left out) and is the only state with a list,
`threads`: 0 to 2 entries, oldest first, each `{thread, state, round, of,
filled, total}`, where `state` is `question` (a round waits; `round` 1 to `of`)
or `summary` (the walk is done; `round` 0 to `of`). No entry holds a brief's
text. Every other state leaves the list out.

A question (`nodes[]`) is `{id, type, prompt, topic, options[{id, label,
example}], allow_other, allow_you_decide, can_skip}`: `type` is `single`,
`multi`, `yesno` (two options) or `scale` (none; the steps are 1 to 5), and a
single or multiple choice has 2 to 8 options. A summary groups its rows by who
set the field: `you_chose`, `decided_for_you` and `defaults`, each a list of
`{node, field, label, value, untrusted}`, at most 64 rows in all and no field
twice. `label` is the question as the catalog words it and `value` the answer
as text: the labels of the chosen options, joined, or the owner's own text.

`plan`, the answer to `plan`, is the only state with `plan`, `plan_digest`
and `stored`, which every other state leaves out. `plan` is the plan record,
member by member under the record's own names (see [the plan](#the-plan)):
`schema`, `plan` (the revision), `prev_digest`, `brief_digest`,
`catalog_digest` (the thread's), `template` (`id`, `version`),
`template_digest`, `title`, `milestones` of `tasks`, `gates`, `first_demo`,
`review_points`, `hosts`, `external_services`, `budget_band` (`id`,
`enforcement: "advisory"`), `estimate` (`band`, `label`, `estimate: true`,
`caveat`), `warnings`, `notes` (each `{field, text, untrusted: true}`) and
`notes_dropped`. The wire writes it to the record's own canonical bytes, and
`plan_digest` is their SHA-256, which the decoder computes again and holds the
plan to: a plan changed on its way is refused. `stored` is `true` for the plan
the host stored at finish and is left out (false) for a preview. The wire
cannot import the plan package; it holds the record's grammar, caps and closed
lists as forms of its own (`ownerwire`, `genesis_plan.go`), and a test of
`internal/genesis` holds them in step.

Every text in a result is plain text the host owns: the catalog's, a plan
template's, or the owner's own `other` text, which is in a row with
`untrusted: true` (at most 200 bytes), or in a note of a plan, likewise
marked. The seat escapes every text it shows, and shows an untrusted one as
quoted data. That text is for the owner's eyes only: it is never served to an
implementation agent (see [the plan](#the-plan)).

### Caps

| What | Cap |
| --- | --- |
| Params | 4 KiB (`MaxSeatGenesisParamsBytes`) |
| Result | 16 KiB (`MaxSeatGenesisResultBytes`); one over is `record-too-large` |
| Open threads on the host, for every pinned seat together | 2 (`MaxGenesisThreads`, the host's) |
| Rounds | 6 (`MaxGenesisRounds`); questions in a round 3; answers in a call 3 |
| Options of a question, choices of an answer | 8 |
| Prompt, option label, option example | 160, 60 and 100 bytes (the catalog's) |
| The owner's `other` text | 200 bytes |
| Value text of a summary row | 512 bytes |
| Summary rows | 64 |
| A plan, in its canonical form | 12 KiB (`MaxGenesisPlanBytes`, the plan record's `MaxPlanBytes`) |

The result cap is the binding one for a summary: 64 rows of short texts take
about 8 KiB, and a host with more to say shortens its texts. The v1 catalog's
largest result, over walks that answer every question with the longest text it
takes, is a few KiB (a test runs the walks of v1 through the wire's forms).
The largest plan of all 912 combinations of the answers the templates read,
with the owner's longest texts in every `other` answer and eight notes, is
about 7 KiB, in a result of about 7.3 KiB; the largest plan the record allows
at all (every count and text at its cap) is about 10.4 KiB.

### What a test pins

The decoders refuse a member of another verb or state, an answer whose kind and
members disagree, a thread id, node id, choice id or field name outside its
grammar, text over its cap, a result over its cap, and a list of more than two
threads, of a thread twice or of a thread that is not open. `internal/genesis` holds
the wire in step with the host: the caps equal the catalog's and the brief's,
the grammars accept exactly what the brief's do, every refusal code of the brief
has its gate word or none, and every result of the v1 catalog's walks
passes the wire. The wire's plan is held to the record: its caps, names and
closed lists are the plan package's, it accepts a plan exactly when
`plan.Check` does over a corpus of each grammar and text (key-shaped notes,
which the host refuses, are the one difference), and every plan of the 912
answer combinations is written by the wire to the record's own bytes, under
the record's digest, and reads back to the same record. The fuzz tests (`FuzzDecodeSeatGenesisParams`,
`FuzzDecodeSeatGenesisResult`) hold that a decoder never panics, that a refusal
is a `*record.Error` with a detail that can be shown as it is, and that what
decodes encodes to no more bytes and to itself.

### The thread store

`internal/genesis/thread`, the daemon's own record of the interviews, and the
only writer of them. It never executes anything and reads nothing outside its
folder, `STATE/owner/genesis/`, which the gate makes with mode 0700 when it
starts and opens as an `os.Root`, so no link is followed and nothing outside
the folder is reached. The store refuses a folder that is a link, is not the
daemon user's, is writable by group or other or holds more than 64 files, with
an error of fixed text and a reason word (`thread.OpenError`: `folder-link`,
`folder-owner`, `folder-mode`, `too-many-files` and the others). The daemon
then logs one line of fixed words with that reason word (`seat gate: the
guided interview is not served: its thread store did not open (REASON); ...`),
never the folder's path, and starts the gate without the store: no `genesis`
in its hello, so the phone shows no "New project", and nothing in the folder is
read or written. A folder in doubt does not stop the daemon or the rest of the
gate; it is fixed by hand and the daemon restarted.

- **A thread** is `{id, state, brief, created, updated, catalog digest}`: the id
  is 16 lower-case hexadecimal digits from `crypto/rand`, the state `open`,
  `finished` or `discarded`, the brief the `ataniq.project-brief/1` record of
  `internal/genesis/brief`, and the catalog digest that of the catalog the
  thread was started under (a thread stays on one version). One file each, named
  by the id with the suffix `.json`, mode 0600, in the canonical form of the
  brief's strict JSON with a digest of the file's own content, schema
  `ataniq.genesis-thread/1`. A finished thread's file may also hold its plan:
  the optional members `plan` (the `ataniq.project-plan/1` record in its
  canonical form) and `plan_digest`. The plan lives there, with the frozen
  brief it was drawn from, and nowhere else; a file without them is as valid as
  before (no migration and no new schema: the reader already takes optional
  members by name; a daemon older than the plan does not serve a file that
  holds one).
- **Writing** is atomic: a temporary file of the same folder, written, synced,
  renamed over the thread's file, and the folder synced. A call changes the
  thread in memory only after the file is written, so a write that fails leaves
  the old thread. One mutex serialises every call of a store, and a call holds
  it for its own work and the one write; the log lines and the removal of
  pruned files come after it is let go. A restart serves the same state.
- **Reading** is strict, at open only (the store is the only writer). A file
  that is not a regular file of the daemon's user with no mode bit but 0600's,
  is over its cap, is not strict JSON in the canonical form, names another id
  than its file, holds a brief that does not decode or was started under
  another catalog than the thread's or the store's, or whose digest (of the file
  or of a finished brief) does not match, or whose plan is on a thread that is
  not finished, does not decode under the plan record's own checks, does not
  match `plan_digest` or was not drawn from the thread's frozen brief and
  catalog, is not served: the log says so once, in
  fixed text naming the file (`genesis: the thread file <name> is not served:
  <check>`), and the file is left as it is. It is never repaired or overwritten,
  and its id is not used again.
- **The verbs** are the wire's, exactly. `start` refuses a third open thread
  `genesis-busy` and answers the first round, `catalog.Next` of an empty brief.
  `round` answers the question round the thread is at, or the summary when the
  walk is done (every required field filled, six rounds, or no question left);
  a finished thread answers its summary and digest. `answer` must name the
  thread's current round and nodes of it (a round already taken, or any round
  once the walk is done, is `genesis-stale-round`; a round it has not reached,
  or a node of another round, `genesis-bad-answer`); each
  answer goes through `catalog.Apply`, and one failing fails the whole call and
  changes nothing; text that looks like a secret is refused
  `genesis-secret-shaped`, a field or brief over its cap `genesis-over-cap`.
  `change` names a node that has an answer and replaces it through
  `catalog.Apply`, which clears the closure of the node's invalidations, keeps
  the cleared values in the brief's history and says how many in `reset`; the
  next round asks the cleared questions again, under the next round number.
  `summary` reads the summary at any time. `finish` freezes the brief and
  answers its digest, at any time on an open thread (there is no separate
  "enough": the owner reads the summary and finishes), and again on a finished
  one with the same digest; afterwards `answer`, `change` and `discard` are
  `genesis-done`. When the brief is complete and a template serves its kind,
  `finish` compiles the plan, revision 1, and stores it in the same atomic
  write as the frozen brief; a brief that is not complete, or of a kind no
  template serves, is frozen without a plan, and a compiler fault of the host
  does not stop the finish (it is logged, and the plan drawn when next asked).
  `plan` answers a preview on an open thread whose brief is complete
  (compiled on read, `stored: false`, written nowhere), and the stored plan on
  a finished thread (`stored: true`); a finished thread with no plan whose
  brief is complete (one finished before plans were stored) is compiled on the
  first `plan` and stored then, once, without touching its time. A brief that
  is not complete is `genesis-not-ready`, a kind no template serves
  `genesis-no-template` (asked before completeness). A stored plan is never
  compiled again: a template change never alters a stored plan, and a plan of a
  template no longer embedded is served as it was stored (a test stores one
  whose template digest is no embedded template's). `discard` drops an open thread and empties its brief. `list`
  answers the open threads, oldest first (by the time each was started, then
  by id), each with the state, round and progress `round` would answer and
  nothing of its brief; it expires what is due first, as `start` does, and
  touches no thread. It lists at most 2, the oldest, and never fails for
  more: the store never opens a third, but a folder may hold more open
  threads (files restored by hand from a copy); the newer ones are still
  served by id, and `start` is `genesis-busy` while two or more are open. A discarded
  or expired thread is `genesis-unknown-thread` for every call. The mapping of
  the brief's refusal codes to the gate's words is the store's own table
  (`thread.GateWord`), held equal to the wire's by a test of `internal/genesis`.
- **Limits.** At most 2 open threads; at most 8 files, the oldest finished or
  discarded thread removed first when a start would pass it, its plan with it; a thread
  untouched (no answer, change, finish or discard; a read does not count) for
  14 days is discarded on the next access or at open, and keeps no answer. The
  store's clock and its source of ids are injectable, which the tests use.
- **The log** holds fixed texts and no answer or brief content: `genesis: a
  thread was started`, `... finished`, `... discarded`, `... expired`, `genesis:
  a plan was drawn` (a plan was stored, at finish or on a late first ask),
  `genesis: a plan could not be drawn at finish`, `genesis:
  an answer was refused as secret-shaped (N since the store was opened)` (a
  count only), and the line about a file that is not served. A thread's id and
  anything of a plan are not in them.

Not built: the view of the threads for the desk, which the console will need.

### The seat's pages

The seat link (`seatlink.Daemon.Genesis`, the app's `seatapp.Genesis` port)
calls `seat.genesis` only when the hello lists `genesis`; otherwise every call
is "not offered" and nothing is sent, and the phone shows no "New project". The
link holds each call to the wire's form with the wire's encoder before it is
sent, maps the nine genesis codes to the app's errors (and `rate-limited`,
`busy` and `too-many-refusals` to "busy", `unknown-op` to "not offered"), and
takes an answer only when it is one the verb answers, of the thread called,
with every count and length checked again. Each call has the room of the
interview at the gate: 12 calls in 80 seconds, of every session together, taken
from the reads and the decisions (see the budget table under
[login](#login-seatlogin-challenge-and-seatlogin)). So that the pages do not
spend that room on reads, the link answers a `list` from the last one for 20
seconds (the card list's reload gap: card lists open in several tabs list the
open projects once in it between them) and a `round` from its own last answer
for that thread for 10 seconds (the page a form's post redirects to; after a call
that changed the thread, its own answer is that last answer), and
forgets both before any call that changes threads is sent and again when it
ends, whatever its answer: one answer of the owner's is one call. A read's
answer is kept only when no call that changes threads began or ended while it
was asked (a generation taken before the read is sent, which each such call
moves on as it starts and as it ends), so a read that overlapped a change,
even one that began after the change was sent and came back before it, is
never kept; the change's own answer is kept when nothing else changed. A
`plan` is one call of the room, and the link keeps its answer, under the same
generation rule, for the thread: a preview (`stored: false`) for 10 seconds,
as a round, and forgotten by every call that changes the thread (`answer`,
`change`, `finish`, `discard`), whatever its answer; the plan the host stored
at finish (`stored: true`), which never changes, with its digest for 5
minutes, through those calls (only `genesis-unknown-thread` forgets it). A
finish keeps nothing of the plan: the next `plan` reads the stored one. The
link converts a plan to the app's own view (`seatapp.GenesisPlanView`) and
checks it again against the app's copies of the plan record's caps and closed
lists (a test holds them equal to the wire's); a plan past them is not shown.
It carries `plan_ready` of every result into the app's view (`PlanReady`).
`genesis-not-ready` and `genesis-no-template` are the app's
`ErrGenesisNotReady` and `ErrGenesisNoTemplate`. A summary's rows get their questions, for a change, from
the catalog the seat's build holds, and only when the thread's catalog digest is
that catalog's; under another catalog the row offers no change.

The seat app's pages, behind a session like the card pages, server-made with no
script but the logout's:

| Page | What it holds |
| --- | --- |
| The card list | When genesis is offered: the projects in progress (from `list`), each a link, and a "New project" button (a form posting `POST /projects/new`); with two open, a line instead of the button. |
| `GET /projects/` | The same, on a page of its own, with the note of a post that came back to it (two open, an unknown project, a discard). |
| `GET /projects/{thread}` | The round the thread is at, its summary, or the finished brief, by the answer to `round`. |
| `GET /projects/{thread}/change/{node}` | One answer's question, from the summary's row, to answer again. |
| `GET /projects/{thread}/discard` | The step before a discard: a form posting the discard, and the way back. |
| `GET /projects/{thread}/plan` | The thread's plan, by the answer to `plan`: a draft of an open thread, or the plan stored at finish. Links only. |

A round's page shows "Round N of M" and the required answers given, and each
question as a fieldset of real inputs: radio buttons for a single choice, a
yes-or-no question and a scale of 1 to 5, check boxes for a multiple choice,
each option with its label and its example as text; "Other" with a text field
of `maxlength` 200 only when the question allows it, labelled "a short note,
about 200 letters" (the cap is 200 bytes, and a browser counts `maxlength` in
UTF-16 code units, so words with accents or emoji can be cut short of it and
still be too long; the post is then refused before the port is asked, with the
note "Your own words were too long, so nothing was sent. ..."), and "You
decide for me" and "Skip" only when the question allows them; one submit
button. The summary
shows the three groups (what you chose, what was decided for you, the
defaults), the owner's own words quoted and marked as the owner's, a "Change"
link on each row whose question the link holds, a "Looks right: finish" button
and a link to the discard step. The finished page shows the brief's digest in
short form and says that nothing has started and no agent was given the brief.
The summary links "See the draft plan" when the result's `plan_ready` says a
plan would be answered, and otherwise says "N of M answers are filled in: no
plan can be drawn yet." (or, with every answer filled in, that there is no plan
template for this kind of project yet). The finished page says that the plan
is drawn and kept with the brief and that handing it to a project is a later
step, with "See the plan", when `plan_ready`; otherwise that no plan was drawn.

The plan page, `GET /projects/{thread}/plan`, is a read: one call of `plan`
(answered from the link's memory on a reload, see above), a session, the
project pages' limit of one credential, and a thread id of its form before
anything is asked; there is no post to it. It is made on the server with no
script at all (not even the logout's), no form, no button and no input: its
only controls are two links, back to the thread's page and to the project
list, and the page inventory holds it to that and to the rule that no page but
the Card page holds a decide control. In order: the heading ("Draft plan", or
"Plan, revision N" for the stored plan), the plan's title with the first six
digits of the thread id ("A full web app, 3fa2c1"), the fixed lines "A plan
binds nothing: nothing has started, and nothing starts from this page." and,
for a draft, "This is a draft; finishing the interview keeps it as revision
1.", and the plan's digest in short form, the same for a draft and the stored
plan of the same answers; then the first demo; "What waits for you": the
review points in the app's words and always the fixed sentence "Publishing,
releases, pushing code, credentials and raising any limit always wait for you,
whatever the plan says.", whatever the review points hold; the outside
services, or none; where the files live; the budget band, marked "advisory:
nothing enforces it yet"; the estimate, marked "an estimate, not a promise";
the warnings, each a fixed sentence by its id (an id the app does not know is
"A note from the plan.", never its text); "Milestones and tasks", one native
`details` element a milestone (only the first open), each with its goal and an
ordered list of its tasks: title, kind and size in words, and "after:" the
titles of the tasks it waits for; "Your own notes", only when there are some,
each quoted with the `Text` rendering of untrusted text (bidirectional and
hidden characters shown as escapes) and marked "your words, shown as you wrote
them; never given to a tool", with the count of the notes left out; and
"What a charter would cover". Every text is escaped by the page templates.

The charter section is words about a later step, never a step. It opens with
the fixed text "Draft. Not signed. It allows nothing. Nothing starts from this
page. Signing is a later step and is not built.", then "A signed charter would
allow at most:" and a list drawn from the plan: the areas of work (the union of
the tasks' scopes, in words where the app has them, else the area's name), the
outside services, the budget band (advisory), and "stopping for you at" the
review points. Its wording rule, which a test holds: it never says "card", it
never says the plan "allows" anything (the one "allows" is the fixed "It
allows nothing."), it holds no control and no link (no Approve, no Sign, no
passkey, nothing into the card list's pending cards), and it is on the plan
page alone: never on the card list, a card, a notice or another project page.

The verb `plan` and the `plan_ready` member of a result have no capability of
their own: they ride on `genesis`, so the seat and the daemon of a build are
deployed together. A daemon older than the seat answers `plan` with
`bad-params` (the page says something went wrong), and a seat older than the
daemon refuses a result that carries `plan_ready` as one it does not
understand.

A plan that is not ready (`genesis-not-ready`) is a page of fixed words, "No
plan can be drawn yet: some answers are missing."; only then is the thread's
round read too, and a finished thread's page says instead "This project was
finished before its answers were complete, so no plan was drawn."
(`genesis-not-ready` of a finished thread). A kind with no template
(`genesis-no-template`) is "There is no plan template for this kind of project
yet." An unknown thread, a busy or unreachable desk and an interview not
offered are the problems of the other project pages.

Every act is a plain form posted to the app (`/projects/new`, and
`/projects/{thread}/answer`, `/change`, `/finish`, `/discard`), with the checks
of every changing request of the seat (Origin, Host, `Sec-Fetch-Site:
same-origin`, the body's size) and the form's content type, and a session.
A browser posts a form with the referrer policy of the page that holds it, so
every response of the seat carries `Referrer-Policy: same-origin`: nothing of
an address leaves for another origin, and a same-origin post keeps its
`Origin` header (under `no-referrer` it would be `Origin: null`, and every
form would be refused). A form's post passes with `Origin` equal to the seat
origin, or with no `Origin` header at all when `Sec-Fetch-Site` says
`same-origin`; `Origin: null` and any other origin are refused. The scripts'
JSON posts set their own policy (`strict-origin`) and always need `Origin`.
The page inventory test holds every page with a form to the policy. A
post calls the port once and is answered with a 303 to a page
(post/redirect/get), never with a page: a reload sends nothing again, and a
refusal of the gate is never retried. A form whose fields do not agree (two
kinds at once, own words with an option, words that are not plain text, words
over 200 bytes) is refused before the port is asked. Each refusal is a fixed
note on the page the post redirects to: a bad answer ("That answer was not
accepted. Look at the question again."), secret-shaped text ("That looks like
a password or key. Never type those here; say that a credential is needed
instead."), an answer over a cap, an unknown project, a finished one, a round
already answered (back to the thread's page, "That round was already
answered."), two open, a busy or unreachable desk. No page, address or log line holds what the owner
typed, and a refused answer's text is never shown back. A thread id in an
address must be 16 lower-case hexadecimal digits, and a node id of the
catalog's grammar, before it is used; otherwise the page is a 404, or the post
goes to the project list, and nothing is asked. The project pages and posts of
one credential are limited to 30 a minute, and keep the session alive as the
card pages do. No project page holds an approve or decide control or loads the
decide script; the page inventory lists the routes and their controls, and
holds every form to a plain post to a project route.

The seat's command needs nothing configured: the link behind the decision guard
is the app's port (an answer decides nothing, so the guard passes it on while
decisions are paused), and the command logs each hello's capabilities, with
whether the phone offers "New project" (`ataniq-seat: the seat gate offers
pair, read, login, decide, watch and genesis; the phone offers New project`).

### The plan

`internal/genesis/plan` compiles a finished brief into a project plan,
`ataniq.project-plan/1`: a pure library, with no clock and no wire. The
daemon's thread store calls it (see [the thread store](#the-thread-store)): a
preview on a complete open thread, the plan stored at finish, served over the
wire by the verb `plan` and shown by the seat's plan page (see [the seat's
pages](#the-seats-pages)). A plan is a proposal: it binds nothing and grants nothing. Three things
bind, each approved on its own, and none of them is the plan: the creation of
the project, the charter, and the delegation contracts. What always waits for
the owner (publishing, a release, a push, credentials, raising a cap) is not a
field of the plan: the page that shows a plan says it in fixed words,
whatever the plan holds.

The record is canonical JSON under the brief's rules, with a SHA-256 digest:
`plan` (the revision, 1 for a first compile), `prev_digest` (the digest of the
revision before; empty for 1), `brief_digest`, `catalog_digest`, `template`
(`id`, `version`) and `template_digest`, `title` (a label of the catalog, such
as "A newsletter", never the owner's text), `milestones` (at most 4) of `tasks`
(at most 6 each, 12 in all; each a `title`, a `kind` of build, write, review or
test, a `size` of small, medium or large, which chooses no model,
`depends_on`, a `scope` of area names such as `repo:src` or
`store:manuscript`, and `checks` such as `unit-tests` or `rights-manifest`),
`gates`, `first_demo` (one sentence of the template's), `review_points`
(exactly as the owner chose them), `hosts`, `external_services` (every
external service the work would use, by name, from a closed list: hosting
service, email delivery, generator service, file or code host),
`budget_band` (`none`, `low` or `medium`, never a number, with `enforcement:
"advisory"`), `estimate` (a band of the tasks' sizes, such as "about a day of
agent work", with `estimate: true` and the words "an estimate, not a
promise"), `warnings` (closed ids: `estimate-may-exceed-band`,
`rights-not-cleared`, `settle-at-desk`), `notes` and `notes_dropped`. A plan
is at most 12 KiB, under the 16 KiB of a result on the wire. A milestone and a
task keep their place in the template as their id (`m1`, `t4`), so an id means
one thing in every revision, and a plan's ids may skip numbers. A plan holds
no time: when it was made is the store's to keep, and the same brief gives the
same bytes and digest at any time. `Decode` holds a plan to its grammar, its
caps and its own consistency only, never to the templates embedded now: a
plan of a template version no longer embedded still reads, and a template
change never alters a stored plan. Where a plan lives is the thread store:
revision 1 of a finished thread's frozen brief, in the thread's own file,
written once and never compiled again; a preview is compiled on each read
and kept nowhere.

`Compile` refuses a brief that is not complete (an inferred answer the owner
did not confirm counts as missing), a brief of another catalog and a kind with
no template (`no_template`; the kind in the owner's own words is one, for
which the phone will say a fixed sentence), each with a refusal code of the
brief package. `Next` compiles the revision after a plan and refuses
`no_change` when the plan would be the one before (the brief's digest alone
differs for the same answers given again), and `Diff` names what changed
against the revision the owner last saw: milestones and tasks added, removed or
changed by id, top-level fields by name, with short texts, at most 48 entries,
never a brief digest alone and never the owner's words.

The templates are data, `ataniq.genesis-plan-template/1`, embedded in the
canonical form, one per kind: software (a web app or a one-page tool),
publishing (a newsletter or an article) and media (a podcast or a book). A
changed template is a new version: a test pins the digest of each, and a plan
records the digest it was compiled with. Every template holds the same
security block (no push, no outward step, secrets by reference). A task, a
gate, a service, a warning or a demo may hold a condition over one field of
the brief, from a closed set (eq, in, contains, set, other); the fields and
options it names are held to the catalog when the templates are loaded and
when a plan is compiled, and one the catalog does not hold is a refusal, never
a default. A field a template reads that takes the owner's own words needs an
explicit `other` branch: a language in the owner's words gives a task to settle
it at the desk, never anything drawn from the words.

The owner's own text (an `other` answer, a note of the brief) is kept in
`notes` as `{field, text, untrusted: true}`, at most 8 of 200 bytes, for the
owner's eyes only, and chooses nothing. Notes are never served to an
implementation agent: `Plan.ForAgent` is what an agent may be given, it holds
no note, and a test holds every string in it, for a plan `Compile` made, to a
constant of a template, the catalog or the package. A decoded plan is held to
its grammar only, so a plan from a store must be re-checked against its
template before it is handed to an agent. Another test compiles two briefs that differ only in
their `other` texts and notes, one reading like instructions with markup and
commands, and finds the plans byte for byte equal outside `notes` and
`brief_digest`.

## The record change

Owner decision: the decision record gains `confirmed: "passkey"` and a seat
block. The fields of the block below are those of the code. This changes the
joint contract with the planner's project (`internal/approval/record`), so the
planner must agree to it before G8 merges.
It is built (G8a): `record.ConfirmedPasskey`, the block `record.SeatBlock`,
and the passkey vector `decision-valid-passkey.json` with invalid vectors for
each way of carrying the wrong blocks, in the record package's test data.

A decision record carries exactly one of two blocks, as `confirmed` says:

- `confirmed: "code"`: the `console` block, as today, and no `seat` block. Desk
  decisions are unchanged byte for byte.
- `confirmed: "passkey"`: no `console` block, and a `seat` block:

```json
"seat": {
  "assertion_sha256": "<64 lower-case hex digits>",
  "challenge_id": "<16 lower-case hex digits>",
  "credential": "<16 lower-case hex digits>",
  "session": "<16 lower-case hex digits>"
}
```

- `assertion_sha256` is
  SHA-256(F(credential_id) || F(authenticator_data) || F(client_data_json) || F(signature)),
  with F as above (a vector is in [the test vectors](#test-vectors)). The
  record says which assertion decided; the journal entry keeps its bytes.
- `challenge_id` is the id of the challenge the assertion signed (the plan's
  "nonce id"). The nonce is kept in the journal entry, not in the record.
- `credential` is the credential's display id (`ownerjournal.KeyDisplayID`).
- `session` is the gate session: the first 16 hex digits of the seat's pinned
  fingerprint, the same as the journal's activity entry.

The record package's decision check holds a record to one of the two shapes.
The Go form is an optional block for each: `Decision.Console` and
`Decision.Seat` are struct values whose tags say `omitempty`, so a block whose
fields are all empty is left out on writing, and one written so is refused on
reading, as an empty optional string is; the Go type of the console block is
unchanged for the callers that fill it. The record keeps only the digest, because of its 8 KiB cap;
the evidence an auditor needs to check the signature again is in the journal
entry of the decision (see
[the journal entry](#the-journal-entry-of-a-phone-decision)).

## Credential kinds are claims

Owner decision: credential kinds are claims, not attested facts. The pairing
uses attestation "none", so the authenticator's AAGUID and its backup flags
reach the daemon only as the authenticator said them, relayed by the seat. A
kind recorded from them is labelled claimed wherever it is shown, and a policy
on kinds is advisory: it does not hold against a taken-over authority host. Only
a kind with verified attestation, such as a later hardware key, can be enforced.

Owner decision: kinds are pluggable behind one small interface, and two come
first: a platform passkey and a password vault's passkey. The table that maps a
claimed AAGUID to a kind is data: it names vendors and their AAGUIDs and pulls in
no vendor's code. An unknown or all-zero AAGUID gives a passkey of unknown kind.
The interface is built (G5, `credkind.Kind`):

```text
Kind{ Name() string
      Register(RegistrationInput) (Credential, error)
      Verify(Credential, AssertionInput, Expect) (Result, error) }
```

Its kinds are `platform-passkey`, `vault-passkey` and `passkey` (any other
AAGUID, shown as "passkey (claimed: unknown)"); all three verify the same way.
`credkind.Policy` names the accepted kinds and accepts every kind when it names
none.

A stored kind is a word of the key list's grammar (1 to 32 lower-case letters,
digits and dashes, starting with a letter). `PairConfirm` stores the kind that
the pairing's claim gave, with the AAGUID, the backup flags and the sign
counter (built, G6); it stores the word `unverified`, which is not a kind of
`credkind`, only for a pairing whose `seat.pair` came without the
registration's authenticator data. The project's policy is a field of the
project's settings (built, G6: `"kinds"` in the scratch settings,
`owner/scratch.json`, `hostadapter.Scratch.Kinds`, which the daemon hands to
the service as `Config.KindPolicy`); every kind is accepted when it is absent
or empty, and a project the daemon does not know accepts no kind. A login
belongs to no project and checks only that the verifier knows the kind; the
policy is applied at each challenge and each decision (`kind-refused`), so a
key of a kind the project does not accept can still log in.

## The pairing fingerprint

Owner decision: the fingerprint of a paired credential is 8 words, 64 bits: the
first 8 bytes of the digest `seatpair.FingerprintOf` takes, each an index into
the same 256-word list. It is built: the count is one constant in each package
that holds words (`seatpair.FingerprintWords`, `ownerwire.PairWords`,
`ownerjournal.FingerprintWords`, `seatapp.WordCount`, `pairview.WordCount`,
which `terminalconsole.PairWordCount` repeats), and the seat's script
(`internal/seatapp/assets/words.js`) computes the same count. Nothing is paired
in production, so there is no migration: the key list accepts exactly that many
words in an added or a rejected row, on write and on replay, and refuses a list
with a row of any other count, as it refuses any other malformed row.

## Rotation

No change of `seatgate` is needed: the gate holds a list of seat pins, and the
seat holds one gate pin. The commands named here are built (G2:
`ataniq-seat gate-init --identity DIR` makes the seat's identity and prints its
fingerprint; G4: `ataniqd seat-gate init` makes the gate's and
`ataniqd seat-gate pins` lists the pins).

- A seat key: make the new identity on the authority host in a new folder; add
  its fingerprint to the gate's pin file (at most 4 pins) and restart the
  daemon; restart the seat with the new identity; remove the old pin and restart
  the daemon. The seat is never without a working pin.
- The gate key: move the old identity out of `STATE/owner/gate/` by hand
  (`WriteIdentityFiles` never overwrites), make the new one, carry its
  fingerprint to the authority host, and restart both. The seat cannot reach the
  gate between the two restarts, and every outstanding challenge is dropped.
- A passkey: pair the new credential at the desk, then revoke the old one at
  the desk (`owner.key-revoke`). A lost phone is "phone off" at the desk
  (`owner.keys-off`), which revokes every credential.
- The seat origin: passkeys are bound to the relying party id, the origin's
  host. A new origin means a new pairing of every credential, and the origin
  file changes on both hosts, each of which reads it at its start.

The owner carries every fingerprint by hand, both ways, at the first exchange.

## Limits that stay

- Same user. A lead that runs as the daemon's user outside a sandbox can read
  the gate key, append a pin, rewrite the seat origin file and edit the key
  list (`owner/keys.log`) and the journal. Until the
  sandbox is qualified and the key list is protected, phone decisions are
  scratch-only, with the STAND-IN banner, and approve nothing real.
- The wrong card. A taken-over authority host can show one card while asking a
  challenge for another; the assertion binds the binding digest, not what the
  phone showed. Showing a short tag of the binding at the desk and on the phone
  is a later change.
- A taken-over seat can turn a login into a decision. The seat chooses the
  challenge the browser signs, and the phone's passkey sheet shows only the
  site. A taken-over seat host, or whatever terminates TLS in front of it,
  can hand a decision's challenge to the login page, so each Face ID the
  owner gives that origin, a login's included, is worth one decision of any
  pending card, with any sign-offs. The separate login and decision domains
  keep a confused honest seat from taking one for the other; they do not
  hold against a hostile one. One gesture is still one decision: it cannot
  mint more, and the desk's own decisions are untouched.
- Strangers can deny the phone. Whoever reaches the seat's pages can spend
  the limits the seat keeps for everyone: 6 pairings in 11 minutes, 8 login
  challenges a minute, and anyone's room at the gate, which 6 unanswered
  login challenges hold for up to 5 minutes. While they keep it up the owner
  cannot pair a phone, or log in on one; a login the phone already answered
  is never refused for room, and the desk is never touched.
- An ended session keeps its decision's room. A session that ends (logout,
  idle time, eviction) with a decision challenge pending does not drop it:
  the room of its decision is kept until the challenge's life ends (5
  minutes), not let go at once.
- The owner's abandoned logins cost room too. An unanswered login challenge,
  the owner's own included, keeps one call of the public room for up to 5
  minutes, so the owner's repeated cancelled logins can make a later login
  or a pairing answer `desk_busy` for that time.
- A taken-over seat host can send refused pairings without bound. The gate
  counts no refused pairing, so that strangers cannot shut the seat out
  through the honest seat app; a seat host that is taken over talks to the
  gate itself, at its 60 requests a minute, and fills the daemon's log with
  refused pairings (`credential-invalid` is answered before the code is
  looked at, so it costs the sender nothing). Guessing stays bounded: the
  daemon burns a pairing code after 5 wrong codes, and such a seat can
  burn the owner's open code, as before.
- The lead's sandbox must not reach the gate's port. A qualification case that a
  connection from inside the sandbox fails is needed before any real use.
- Nothing is queued while the phone or the gate is offline.
- A held poll costs the gate nothing. The browser's `/api/watch` is answered
  by the seat from the watcher's state and never reaches the gate, so more
  tabs or more phones add no call, and no room, of the 60 a minute. What the
  gate holds is the seat's one wait.
- A taken-over seat can fake liveness. The version, the card list and the
  states a page is told ("same", "changed", "live") come from the seat, and a
  seat that is taken over can say "same" for ever, or "live" of a card that
  is gone, so the page shows an offer that the owner cannot trust to be
  current. It cannot make a decision of it that the daemon would take: the
  binding check at the daemon stays the authority (see
  [live refresh](#live-refresh-seatwait)).
- Notices (see [notices](#notices)) tell the owner's phone that a card is
  waiting while no page of the seat is open, and nothing more. A notice carries
  no text of a card and no action, and the limit above grows with it: each
  notice is a reason to log in, and a login is a gesture that a taken-over seat
  can turn into a decision, so more logins started from notices are more
  chances for it. A taken-over seat can also fake or spam notices, up to the
  notifier's 30 pushes an hour, and can fill the notifier's subscription places.
  Within the notifier's host policy (the one push host, https on port 443) and
  its bounds, that is: it can subscribe endpoints of its own on the push host
  (made with the public VAPID key, up to the 4 places, which leaves none for
  the owner's phone: `too_many`), replace the keys of an endpoint the notifier
  holds, which breaks that phone's delivery until it subscribes again, remove
  the owner's subscription when it knows the hash of its endpoint (it sees the
  endpoint once, when the phone subscribes), and cause up to 30 pushes an hour.
  It cannot make the notifier reach any other host or send any text but the
  count. The notifier's own unit is in a group of its own and not the seat's,
  so a taken-over notifier cannot reach the seat's socket and speak to the seat
  in the front proxy's place.
- Notices have no quiet hours, no email or other fallback, no notice for a
  single card and no question cards; Apple's push service sees the timing and
  size of each push. They are not phone-tested.

## Dogfood limits

Phone decisions stay scratch-only, shown with the STAND-IN banner, and approve
nothing real, until all of these hold:

- the owner channel is qualified;
- the sandbox that keeps leads off the daemon's user is qualified: from inside
  it, a lead reaches neither the daemon's socket nor the gate's port, and can
  neither read nor write the gate key, the pin file, the seat origin file, the
  key list or the journal;
- the desk's enrollment channel is protected. A credential is enrolled by
  `PairConfirm` from the desk's decider, and today the daemon takes as the
  desk a peer of the same user by the console name it gives and the terminal
  it is inspected to hold (see
  [who may connect](../lead-daemon.md#who-may-connect); a same-user process
  as the decider is the stated limit of that build). With a trusted desk the
  confirm is sound; without one, "confirmed at the desk" means only "confirmed
  by some process of the daemon's user", and that process could enroll its
  own key without editing the key list.

Each needs its qualification case before any real use, beside the one for the
gate's port. Until then the phone path is a trust assumption that holds only on
scratch state and the stand-in door, and the shadow dogfood below is all it is
used for.

## The build tasks it feeds

G8, the daemon's side of phone decisions, implements: the new `ownerwire`
types, the challenge function with the vectors above and the separate list of
gate operations; `confirmed: "passkey"` and the seat block in `record`;
`Service.SeatChallenge`, `Service.SeatDecide`, the login operations, the store
of entries and the shared decision path in `ownerchannel`; the activity and the
evidence of a passkey decision in `ownerjournal`; and the gate handler's decide
and login operations, with their codes passed on and counted as above. Its
tests use the software authenticator (`internal/approval/webauthntest`) and
show, each failing before the code: a replayed challenge id; two calls with one
id at once, of which one is taken; a challenge for another card, decision,
sign-off set or reason; an entry of another session, which is not burnt; a
malformed frame after the take, which burns the entry; a revoked key, revoked
between challenge and decide too; a revoke against the append, in both orders;
a key not on the list; a key of the kind `unverified`; user verification off; a
wrong origin; a login assertion offered as a decision; a full login store
dropping its oldest entry, and a full decision store refusing; a restart
dropping the outstanding challenges; approve, deny and held through the real
journal, reopened and read back, with the surface `seat` and the evidence that
checks the signature again; the desk's console vectors unchanged byte for byte;
and a passkey record that `record.DecodeDecision` and `record.EncodeDecision`
accept while a record with both blocks, or neither, is refused.

Others: G1 places the reader of the owner-local origin file, which is built; the
function that makes an origin canonical is not built (see
[the seat origin](#the-seat-origin)). G6 adds the key list's kind, backup flag
and counter (built); G7 builds the seat's side of the login and allows
`publickey-credentials-get` (built); G8 also adds the mark of a decision on its
way to `ownerwire.ShowResult` (built, G8b); and the frame cap of
`internal/seatgate` was raised to 257 KiB without a capability (see
[frame size](#frame-size)).

G9, the seat app's approve and deny, implements (built)
[the phone's side](#the-phones-side): the decide form on the Card page only,
approve offered by `Card.MayApprove` with one sign-off per flagged path and deny
by `Card.Deny` with none; `navigator.credentials.get` with user verification
"required" and `allowCredentials` holding only the session's credential, asked
only after the owner tapped approve or deny, never submitted by itself; the
challenge recomputed by the seat link before the phone is asked; the frame kept
by the seat, with only indexes, the reason and the assertion from the browser;
the lost answer, the held line and the words for each code. Its tests show,
each failing before the code: a deny of a flagged card with no sign-off ticked;
an approve with a sign-off missing, which is not offered; a lost answer, which
is not sent again and is reconciled by reading the card; a held approve; and a
challenge that the seat link recomputes differently, which asks nothing of the
phone.

G14, live refresh (built, first part), implements
[live refresh](#live-refresh-seatwait): the words of `seat.wait`, the code
`wait-full` and `PendingVersion` in `ownerwire`; `Service.WaitPending` in
`ownerchannel`; the capability `watch` and the per-peer limit of two waits in
`gatehost`; the watcher, the wait room and the read room of 20 in `seatlink`;
`POST /api/watch`, `sessions.look` and the script of the pages in `seatapp`;
and the start of the watcher in `ataniq-seat`.

G14's notices (built, not deployed, not phone-tested) implement
[notices](#notices): `internal/seatnotice` (the protocol), `internal/webpush`
(VAPID, the message encryption, the one POST), the notice core and the push
routes in `internal/seatapp`, the service worker, the manifest and the card
list's Notices section, `--notify-socket` of `ataniq-seat`, and the notifier
`ataniq-seat-notify` with its release in `deploy/authority-host/notify`. The
phone trial (G13) is not built.

## Dogfood

1. On loopback, with the stand-in door, fakes and the software authenticator:
   the end-to-end decision test (built, G10b: `TestSeatDecideE2E` and its
   siblings in `internal/approval/hostadapter/seat_decide_e2e_test.go`) is the
   proof that the path works. It runs as a Go test with the tag
   `ataniq_ownerchannel`, in `make test-ownerchannel`.
2. With the owner's go-ahead for each of: a build target for a daemon with the
   tag, the firewall rule, the proxy settings, the origin file on both hosts,
   the authority host's release of the seat app, and the first pin exchange,
   the lead routes its own finish and plan requests as request files into a
   scratch mirror of this repository, and the owner decides each card on the
   phone.
3. This is shadow dogfood. It exercises and records the path on scratch state
   and a stand-in door. It approves no real finish, and the lead still needs the
   owner's words for the real merge.
