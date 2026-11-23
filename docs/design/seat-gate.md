# Seat gate protocol, version 1

Status: design only for phone decisions, and none of it is qualified. Since the
first draft the gate's handler for pairing and reads (G3), the assertion
verifier with its credential kinds (G5) and the seat's login and sessions (G7)
are built, and the fingerprint is eight words; no command uses the handler or the verifier yet. It is the
contract from which the daemon's side of phone decisions and the seat app's
approve and deny are to be built. A paragraph that begins "Owner decision"
records a decision the owner has made. Everything else is proposed, for the
lead to sign off before the build starts; the change to the
decision record is also part of the joint contract with the planner's project
and needs the planner's agreement. Names of packages, files and functions that
exist today are given as they are in the repository; a name marked "planned"
does not exist yet.

The seat gate is the link between the seat app, which serves the owner's phone
from the authority host, and the daemon on an AI host. It carries the pairing of
a phone, the reads of the cards, a login, and the owner's decisions. Phone
decisions remain scratch-only (see [dogfood limits](#dogfood-limits)): until
the owner channel and the sandbox that keeps leads off it are qualified, approvals
remain the owner's direct words (see [approvals](approvals.md#owner-channel)).

## What exists today

- The transport, `internal/seatgate` (standard library only): pinned-key TLS
  1.3 with ALPN `ataniq-gate/1`, length-prefixed strict JSON frames of at most
  64 KiB, a `seat.hello` with a capability list, `seat.ping`, coded errors of
  the grammar `[a-z0-9-]{1,40}` and no text on the wire, and the server limits
  in its package comment (among them 60 requests a minute per connection and a
  30-second deadline per request). `ServerTLS` takes a list of pinned client keys;
  `ClientTLS` takes one pinned server key. `WriteIdentityFiles` and
  `LoadIdentity` keep an identity in a folder (`cert.pem`, `key.pem`, key mode
  0600, folder 0700, never overwritten). No command calls any of it.
- The seat app, `internal/seatapp` (standard library only), and the command
  `ataniq-seat`. They serve the pairing page and its API, and the login page
  and its API with the seat's sessions (G7, see
  [login](#login-seatlogin-challenge-and-seatlogin)). `seatapp.Daemon` has
  `Pair`, `LoginChallenge` and `Login`; `internal/seatlink` implements it over
  the gate, and `ataniq-seat` wires that in when it is given a gate (without
  one, every call answers `ErrDaemonUnavailable`). The daemon's side of the
  login operations is G8's and is not built, so a gate today does not list
  `login`, and the phone says that the desk does not take phone logins.
- The owner channel's service, `internal/approval/ownerchannel`, and the words
  of its connection, `internal/approval/ownerwire`. `Service.PairPresent` takes
  a source whose surface is `ownerchannel.SurfaceSeat` (`"seat"`). The daemon
  serves the owner connection on its Unix socket only in a build with the tag
  `ataniq_ownerchannel` (see
  [the lead daemon guide](../lead-daemon.md#the-owner-channel-tagged-build-only)).
  There the seat source is nil, so `seat.pair` is refused `unavailable` to every
  connection.
- The gate's handler, `internal/approval/gatehost` (untagged; only tagged code
  may import it, and none does yet). It serves `seat.pair` and the four reads
  to the service with a seat source, answers every refusal with a code, and
  bounds the refusals of each peer (see [errors](#errors)).
- The credential kinds and the assertion verifier,
  `internal/approval/credkind` (standard library only), and a software
  authenticator for tests, `internal/approval/webauthntest`, which shares no
  code with the verifier. Only tests use either.
- The fingerprint of a paired credential is 8 words (see
  [the pairing fingerprint](#the-pairing-fingerprint)).
- A decision can be made only at the desk. `Service.Decide` refuses a source
  that is not the decider (`not-decider`) or whose surface has no owner activity
  inside the presence window (`owner-away`), and the record package's decision
  check (`checkDecision`, run by `record.DecodeDecision` and
  `record.EncodeDecision`) requires `confirmed` to be `"code"` and a console
  block with a process id. No command verifies a WebAuthn assertion: only the
  tests of `credkind` call the verifier.

## The listener: inside the daemon

The gate is a TCP listener inside `ataniqd`, compiled only under the existing
tag `ataniq_ownerchannel` and opened only when the owner asks for it with a
flag of `run` (planned, G4: `--seat-gate ADDR`, beside `--owner-channel`).
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

The identity of the gate is kept in the state folder (planned, G4:
`STATE/owner/gate/`, loaded with `LoadIdentity`) and the pinned seat keys in a
file beside it (planned, G4: `STATE/owner/gate/seat-pins`, a regular file and
not a link, mode 0600, one fingerprint per line, at most 4). The pins are read
when the gate starts, so a change of the file applies at the next start. On
start the daemon writes one log line with the gate's fingerprint and address and
no pin.

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
| `login` | `seat.login-challenge` | none | `SeatLoginChallengeResult` | G8 |
| `login` | `seat.login` | `SeatLoginParams` | `SeatLoginResult` | G8 |
| `decide` | `seat.challenge` | `SeatChallengeParams` | `SeatChallengeResult` | G8 |
| `decide` | `seat.decide` | `SeatDecideParams` | `ownerwire.DecideResult` | G8 |

The handler built in G3 answers `["pair", "read"]` (`gatehost.Caps`); after G8,
`["pair", "read", "login", "decide"]`. The seat reads the list
(`seatgate.Client.Caps`) and offers login and decisions only when the gate lists
them; otherwise it says that the desk does not take phone decisions.

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

All are in `ownerwire` (planned, G8). Every byte string is standard base64 in
the JSON, as in `SeatPairParams`.

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
8 KiB. A result has the caps of the result types it reuses.

The params of `seat.decide` and `seat.login` are read in two parts, so that the
challenge is burnt before anything about the content is judged (see
[decide](#decide-seatdecide)). The handler reads the envelope with
`seatgate.DecodeStrict` and the type's decoder: the members and their JSON
types, each byte string's base64 and length, the challenge id's grammar, and
the whole within its cap (`bad-params`). The service takes the entry, and only
then checks the frame's content (`bad-params`); the assertion's content, the
strict DER of the signature among it, is the verifier's (`assertion-invalid`).
The params of `seat.challenge` burn nothing and are read whole by the handler.

### Frame size

`ownerwire.MaxPageBytes` (256 KiB) caps the listing, a show and a page, while a
`seatgate` frame is at most 64 KiB (`seatgate.MaxFrame`). A result larger than
a frame is answered `too-large` by the transport. Version 1 needs all of these:

- The frame cap rises to `MaxPageBytes` plus 1 KiB on both ends:
  `seatgate.MaxFrame` bounds what the server and the client read and write, and
  a result embeds the encoded page as it is, so 1 KiB is room enough for the
  envelope. This is a change of `internal/seatgate` and belongs to the task
  that owns that package; the lead places it.
- A peer built before the raise cannot read a larger frame. So the client
  lists the capability `frame-large` (planned) in its hello, and the server
  writes a frame over 64 KiB only to a client that listed it; any other gets
  `too-large`, as today. A gate and a seat of different builds still work for
  every card that fits.
- `too-large` from a read then remains a defence only. The seat says, for the
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
  logged, so that only listed words reach a seat. G8 adds the codes the
  decision path meets (`binding-mismatch` among them) to that list.
- The pairing and key errors of `ownerchannel` with the codes the daemon already
  gives them, which are also the console's (`consoleclient.PairError`):
  `pairing-refused`, `credential-invalid`, `keys-unavailable`, `keys-full`,
  `key-unknown`, `key-ambiguous`, `pairing-open`, `pairing-none`,
  `pairing-expired` and `pairing-changed`.
- The daemon's own: `unavailable` (the service is closed, stopped or stopping,
  or the call was cancelled), `deadline-exceeded`, `door-unavailable`,
  `journal-reopened`, `failed` (anything else, `ErrNotSeat` included; the error
  goes to the log, never the params).
- New codes for version 1, all from G8:

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
peer sent counts (`unknown-op`, `bad-params`, `not-pinned`, the pairing codes,
the record codes passed on); a refusal for the daemon's own state does not
(`unavailable`, `deadline-exceeded`, `failed`, `door-unavailable`,
`journal-reopened`, `keys-unavailable`, `keys-full`). The count is in memory. A
connection is closed only by the transport, by its own rules (a breach of the
wire, a timeout, a shutdown; see `seatgate`), and
`leaddaemon.DecisionRefusalBound`, the Unix socket's lifetime count, does not
apply to the gate.

For the operations of G8: no refusal of `seat.login-challenge` or `seat.login`
counts, since the seat relays them for anyone who reaches its login page, and a
stranger must not shut the owner out; the seat limits them (see
[login](#login-seatlogin-challenge-and-seatlogin)). Of `seat.challenge` and
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

One function computes the challenge and both digests, in `ownerwire` (planned,
G8), and both the daemon and the seat link call it. Its tests hold the vectors
below.

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
   decision lock. Then the door and the outcome, as at the desk. G8 refactors
   `Service.Decide` so that the desk and the seat share everything after
   authentication.

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

`ownerjournal` is in G8's scope. `AppendDecision` and `AppendHeld` take the
activity of a decision from the record's confirmation: for `confirmed: "code"`
from the `console` block, as today, byte for byte; for `confirmed: "passkey"`
the surface `seat`, the seat block's session and no last desk activity, in the
same write. A passkey record has no console block, so the code as it stands
would write an empty surface.

The decision's entry, held or not, also keeps the evidence of the assertion
beside the record: the four fields of the assertion, the nonce, the instance
and the seat origin the daemon checked against. The record keeps only a digest
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
on a mismatch as for a decision (`seatlink.LoginChallengeOf`, held to the
login vector above). This document places that function in `ownerwire`, for
the daemon and the seat to share; `ownerwire` has no login type yet, so the
seat link has its own, and G8 is to move it there. The seat keeps each login
challenge id with the browser that asked for it, server-side, under a login
cookie of that browser (`__Host-seat-login`, 5 minutes), and takes the
browser's assertion for it only with that cookie, burning the entry first. It
makes the session from the answer with a fresh 256-bit value it draws itself,
never one the browser brought (a session cookie that comes with a login is
ended), and keeps the value server-side as its digest. The session's cookie
is `__Host-seat`, Secure, HttpOnly and SameSite=Strict; a session ends 30
minutes after its last request and 12 hours after the login. Every later
decision of the session is to use exactly the session's credential
(`allowCredentials` holding only its raw id; G9). The seat link checks that a
login's answer names the credential the assertion named, by its raw id and its
display id.

Anyone who reaches the seat can ask for a login challenge, and the gate takes
60 requests a minute on the seat's one connection, which every browser shares.
So the seat limits login challenges to 10 per source in 15 minutes and to 8 a
minute from all sources together: each login asks the gate twice, so logins
take at most 16 of the 60 a minute, and the room left is the owner's reads and
decisions. A source is the client address the proxy sets, as for the pairing.
When the gate answers `rate-limited`, `busy` or `too-many-refusals`, the phone
says that the desk is busy and to try again in a minute, and nothing is retried
by itself.

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
`credkind.Credential`: the kind, the credential id, the public key, the backup
eligibility flag of the pairing and the highest sign counter seen. Today's key
list holds neither the flag nor the counter, and its kind is `unverified`,
which is not a kind of `credkind`. G6 records the kind at pairing, adds both
fields, and keeps the highest counter, raised after each verified login or
decision and never lowered, across restarts. Until a key has them it cannot log
in or decide: it is refused `kind-refused`. A key paired before G6 is paired
again; nothing is paired in production. So the first phone decision on a real
key list needs G6's fields, and G8's tests give the service keys with them
through its key store.

The counter: a credential that reports 0 at pairing and at every use, as a
synced passkey does, has no counter, and nothing is checked or logged for it.
A counter that is not zero on either side and does not grow is logged as a
possible clone. It is not refused, since a counter is a claim like the rest,
and the stored highest counter is not lowered. A backup eligibility flag that
differs from the paired one is refused by the verifier (`assertion-invalid`),
as section 7.2 of WebAuthn Level 3 says.

### The seat origin

The origin and the relying party id are the seat's, and the two ends must agree
on them byte for byte.

- One function makes an origin canonical, and both the seat app and the daemon
  call it (planned, G1, beside the reader of the origin file in
  `internal/ownerlocal`, standard library only; the check that `seatapp.New`
  makes today moves there). It takes `https://HOST` or `https://HOST:443`,
  with a lower-case domain name and no address literal, other port, path,
  query, fragment or user information, and gives `https://HOST`. The relying
  party id is HOST, without a port.
- The gate reads the origin once, when it starts, from the owner-local origin
  file (G1), makes it canonical and logs it. A change of the file applies at
  the next start, so the origin never changes under a running gate.
- Without the file, or with one that is not an origin, the gate starts with the
  capabilities `["pair", "read"]` only, and logs why. The seat then says that
  the desk does not take phone decisions. So no call meets a missing origin.
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

What the seat app does with a decision (G9), proposed:

- The Card page is the only page with a decide form. Approve is offered when
  `Card.MayApprove` allows it, with one sign-off per flagged path; deny when
  `Card.Deny` allows it, with a reason field and no sign-offs.
- Before the phone asks for user verification, the page shows the action the
  owner chose and the card it is for (its id and project, and what it would
  do, as the card shows them). One tap asks one fresh assertion for that one
  action; nothing is submitted by itself.
- The browser never sends a path or a binding. `POST /api/decide-challenge`
  carries the action, the reason and the sign-offs as the indexes of the
  flagged paths. The seat builds the frame from its own copy of the card (read
  through the seat link, its digest checked), keeps the frame with the session
  and the challenge id, server-side, and answers the challenge. `POST
  /api/decide` carries the challenge id and the assertion. Both bodies fit the
  app's cap on every request (`seatapp.MaxBody`, 8 KiB): an assertion within
  the caps of `Assertion` is under 6 KiB in base64.
- The app's `Permissions-Policy` header allows `publickey-credentials-get` to
  the app's own origin on the login page (G7) and forbids it on every other
  response; G9 allows it on the Card page as well.
- A cancelled or failed user verification sends nothing and leaves the card as
  it was; the next tap asks a new challenge, which replaces the old one.

Before the owner submits, the page tells apart: a cancel ("Cancelled. Nothing
was sent."), a challenge that ran out ("This took too long. Tap again."), and a
desk it cannot reach ("The desk cannot be reached. Nothing was sent.").

After the owner submits, only a refusal says for certain that this decision
was not taken. Every other answer to `seat.decide` (`unavailable`,
`deadline-exceeded`, `door-unavailable`, `journal-reopened`, `failed`), the
seat's own timeout, and no answer at all (`seatgate.ErrDisconnected`) mean the
outcome is unknown: the decision may be journaled and on its way to the door,
or not. Then the seat:

- never sends the decision again, by itself or on a tap, and queues nothing;
- shows "Decision sent. Outcome unknown." and offers only "Look again";
- on "Look again", once the gate is back, reads the same card with
  `owner.show` and shows what it says: decided, with the outcome; held; a
  decision on its way to the door; an outcome the door left unknown, which is
  settled at the desk; or pending, and only then may the owner decide again,
  with a new challenge.

So that reading the card can tell a decision on its way from none, G8 gives
`ownerwire.ShowResult` a mark for a card whose decision the journal holds and
the door has not yet taken (planned), on the socket and the gate alike. G9
tests the lost answer.

A held approve (past the project's limit for the day) comes back pending with
the held mark and the host's line. The page shows, under the host's line:
"Held: the project's limit for today is reached. Nothing was applied. Raise
the limit at the desk, then approve again here or at the desk." A held approve
uses nothing and is never released by itself; a later approve needs a new tap
and a new assertion. An expired card cannot be renewed by asking a new
challenge: issue refuses it.

The words for the codes, proposed; the seat shows no text of the gate's, and a
code it does not know gets the last line:

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

## The record change

Owner decision: the decision record gains `confirmed: "passkey"` and a seat
block. The fields of the block below are proposed. This changes the joint contract with the planner's project
(`internal/approval/record`), so the planner must agree to it before G8 merges,
and G8 adds a passkey vector to the record package's test data.

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
G8 chooses the Go form (for example an optional block for each), as long as the
bytes are as here. The record keeps only the digest, because of its 8 KiB cap;
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
digits and dashes, starting with a letter). `PairConfirm` stores the word
`unverified` today, which is not a kind of `credkind`. The project's policy has
its place in a field of the project's settings (planned, G6, in the host
adapter's scratch settings), and until it exists every kind is accepted. G6
records the kind at pairing.

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
seat holds one gate pin. The commands named here are planned (G2:
`ataniq-seat gate-init`; G4: `ataniqd seat-gate init` and
`ataniqd seat-gate pins`).

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

Others: G1 places the function that makes an origin canonical; G6 adds the key
list's kind, backup flag and counter; G7 builds the seat's side of the login
and allows `publickey-credentials-get`; G8 also adds the mark of a decision on
its way to `ownerwire.ShowResult`; and the task that owns `internal/seatgate`
raises the frame cap with `frame-large`.

G9, the seat app's approve and deny, implements
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
   the end-to-end decision test (planned, G10b) is the proof that the path
   works. It runs as a Go test with the tag `ataniq_ownerchannel`.
2. With the owner's go-ahead for each of: a build target for a daemon with the
   tag, the firewall rule, the proxy settings, the origin file on both hosts,
   the authority host's release of the seat app, and the first pin exchange,
   the lead routes its own finish and plan requests as request files into a
   scratch mirror of this repository, and the owner decides each card on the
   phone.
3. This is shadow dogfood. It exercises and records the path on scratch state
   and a stand-in door. It approves no real finish, and the lead still needs the
   owner's words for the real merge.
