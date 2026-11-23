# Seat gate protocol, version 1

Status: built and tested on loopback and on scratch state, and none of it is
qualified. In a build with the tag `ataniq_ownerchannel` the daemon serves the
gate (`ataniqd run --seat-gate ADDR`; the pairing and reads are served from
G3 on, and the login and decision operations when the daemon has the seat
origin, G8b), and `ataniq-seat`, given a gate, serves the pairing page, the
login page, the card pages and the Card page's approve and deny control
(G7 and G9). A phone decision is verified, journaled and written as a
passkey record by the service. Phone decisions are scratch-only, shown with
the STAND-IN banner, and approve nothing real. Not built yet: the release of
`ataniq-seat` on the authority host (G12); the call of `decisionguard.Check`
in `ataniq-seat` before any challenge or decision is relayed; the phone trial
(G13); `owner.wait` (live refresh) and notices (G14); and the one function
that makes an origin canonical (G1; [the seat origin](#the-seat-origin) lists
where the three checks in use disagree). This document is the contract that
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

A handler made with `gatehost.New` answers `["pair", "read"]` (`gatehost.Caps`);
one made with `gatehost.NewDeciding`, `["pair", "read", "login", "decide"]`
(`gatehost.DecidingCaps`), and the daemon makes that one only when the seat
origin is configured and its service can decide from the phone (see
[the seat origin](#the-seat-origin)). The seat reads the list
(`seatgate.Client.Caps`) and offers login and decisions only when the gate lists
them; otherwise it says that the desk does not take phone logins, or does not
take decisions from the phone.

The gate never serves `owner.decide`, `owner.decider`, `owner.beat`,
`owner.looked`, `owner.limit`, `owner.limit-set`, the desk's pairing operations
(`owner.pair-start`, `owner.pair-status`, `owner.pair-confirm`,
`owner.pair-reject`) or the key operations (`owner.keys`, `owner.key-revoke`,
`owner.keys-off`). A key is revoked at the desk only.

The four new operations are the gate's alone. They are not added to
`ownerwire.Operations()`, which is the list the daemon's Unix socket serves and
announces; G8 lists them separately, so the socket neither announces nor serves
them.

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
- New codes for version 1, all from G8 (their words are declared in
  `ownerwire`, `ownerwire.CodeChallengeStale` and the others):

| Code | Meaning |
| --- | --- |
| `challenge-stale` | No outstanding challenge of this kind and gate session has this id (unknown, used, expired, of another session, dropped by a restart), or the frame does not match the challenge. |
| `challenges-full` | 32 decision challenges are outstanding, none has expired and none can be replaced. |
| `assertion-invalid` | The assertion names another credential than the challenge's, or did not verify, for any reason (client data, origin, relying party, flags, signature). The daemon's log names which check failed, from the verifier's fixed list, never the bytes. |
| `key-revoked` | The credential is on the list and revoked. |
| `kind-refused` | The credential's stored kind is not a kind the verifier knows (`unverified` included), or not one the project accepts. |

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
record (`binding-mismatch`); and the sign-offs equal the record's flagged list
for an approve and are none for a deny (`decision-invalid`). So the phone is
never asked to sign what would be refused. None of these refusals is an entry
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
seat answered the logout; when the answer is lost, or is not a success, the
session may still be live, so the page says "Logout not confirmed" and the
button stays. Every later
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
| Reads | `owner.list`, `owner.show`, `owner.paths`, of every session together | 26 |
| Decisions | `seat.challenge`, `seat.decide` | 16 (8 decisions) |

The rooms add up to the gate's 60, so the gate's rate never refuses a call
the seat sent. A call without room is not sent: the phone says the desk is
busy (`desk_busy`, the problem "The desk is busy", or `rate_limited` for a
pairing), before anything is asked. A decision's challenge is asked only
when the room of decisions holds it and its `seat.decide` as well: the room
of the decision is kept for the credential's last challenge (a new tap of the
same credential takes its place) until the decision is sent, or the
challenge's life ends (5 minutes at most). So a decision the phone signed
is never refused for the gate's rate: strangers' logins and pairings, and
reads, cannot take its room, and a tap past it is told the desk is busy
before any challenge is drawn or the passkey asked.
The seat relays pairings for anyone who reaches its pairing page, and the
gate counts each pairing the daemon refuses against the seat's refusal bound
(16 in 10 minutes; see [the refusal bound](#the-refusal-bound)), past which
it refuses every request of the seat, the owner's too. So besides 5 tries per
source in 15 minutes, the seat hands the daemon at most 6 pairings in 11
minutes from all sources together (a body the seat refuses itself is not
counted); the window is a minute longer than the gate's so that pairings
sent a little more than 10 minutes apart are not counted closer at the gate.
The pairing code's own limit, 5 wrong codes, stays.
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
  the daemon nothing. The page shows a path or a ref (a ref
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
  part ("a ref", "a changed path", "a path in a list", "a flagged path",
  "a path or a ref of how the card differs" is too long to show whole on the
  phone; approve at the desk). So two flagged paths that read the same on
  the page because both were cut are never signed off by their index. Deny
  stays. A prune's ref line shows the branch its deleted ref was merged
  into and that branch's tip (the first 12 digits), as the desk does. The
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
  refused (`bad_request`); only a request with no `Sec-Fetch-Site`, which
  the test option alone lets through and no browser sends, may leave it
  out. The link freezes the frame from the card it showed at that binding,
  asks `seat.challenge`, and recomputes the challenge before the browser
  sees it.
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
  gate will read them. The seat's transport (`seatgate.Client.Call`)
  marshals the params it is handed again with `encoding/json`, which escapes
  `<`, `>`, `&`, U+2028 and U+2029 for HTML (`\u0026` is six bytes for one),
  so a frame within its cap can grow past the gate's cap in transit: four
  flagged paths of 900 `&` each make params of 3931 bytes that are over
  9 KiB on the wire, which the gate refuses `bad-params` (a refusal it
  counts). The link escapes the params first, as the transport does, checks
  those bytes with the daemon's decoder (`ownerwire.DecodeSeatChallengeParams`
  and `DecodeSeatDecideParams`, size cap included), and hands the transport
  exactly them, which its marshal leaves as they are. Params over the cap
  on the wire are not sent: the phone says "This is too large to send."
  (`too_large`), and nothing was decided. Such a decision is made at the
  desk until the transport sends the params as given.
- Decision challenges are limited to 10 a minute per credential, and the
  seat link asks one only when the room of decisions at the gate (16 calls
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
  (built: the card is marked sent before the decision is sent, a refusal
  clears the mark, and any other answer leaves it until a new read of the
  card: a GET of its Card page whose read of the daemon began after the
  mark was set. A `HEAD` is no read, and a GET whose read began before the
  mark may show the card as it was before the decision, so its page holds
  no form, says "A decision of this card was sent and its outcome is not
  known. Look at it again." and leaves the mark. While it stands, a tap is
  refused without asking the daemon);
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
| `challenges-full`, `busy`, `rate-limited`, `too-many-refusals` | The desk is busy. Try again in a few minutes. |
| any other | Something went wrong. Look at the card again. |

Built so (G9). An answer to `seat.decide` with one of the five codes that
leave the outcome unknown, the seat's own timeout and no answer say
"Decision sent. Outcome unknown."; a code of "any other" after a decision was
sent says the last line and also leaves the card marked sent until it is read
again. A challenge the seat link recomputes differently says that the desk's
challenge is not this card's and that the passkey was not asked. A held
approve shows the host's line and, under it, the held words above.

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
- The lead's sandbox must not reach the gate's port. A qualification case that a
  connection from inside the sandbox fails is needed before any real use.
- Nothing is queued while the phone or the gate is offline.

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
