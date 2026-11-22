# Seat gate protocol, version 1

Status: design only; nothing described here as new is built, and none of it is
qualified. It is the contract from which the daemon's side of phone decisions
and the seat app's approve and deny are to be built. A paragraph that begins
"Owner decision" records a decision the owner has made. Everything else is
proposed, for the lead to sign off before the build starts; the change to the
decision record is also part of the joint contract with the planner's project
and needs the planner's agreement. Names of packages, files and functions that
exist today are given as they are in the repository; a name marked "planned"
does not exist yet.

The seat gate is the link between the seat app, which serves the owner's phone
from the authority host, and the daemon on an AI host. It carries the pairing of
a phone, the reads of the cards, a login, and the owner's decisions. Phone
decisions remain scratch-only (see [limits](#limits-that-stay)): until the
owner channel and the sandbox that keeps leads off it are qualified, approvals
remain the owner's direct words (see [approvals](approvals.md#owner-channel)).

## What exists today

- The transport, `internal/seatgate` (standard library only): pinned-key TLS
  1.3 with ALPN `ataniq-gate/1`, length-prefixed strict JSON frames of at most
  64 KiB, a `seat.hello` with a capability list, `seat.ping`, coded errors of
  the grammar `[a-z0-9-]{1,40}` and no text on the wire, and the server limits
  in its package comment. `ServerTLS` takes a list of pinned client keys;
  `ClientTLS` takes one pinned server key. `WriteIdentityFiles` and
  `LoadIdentity` keep an identity in a folder (`cert.pem`, `key.pem`, key mode
  0600, folder 0700, never overwritten). No command calls any of it.
- The seat app, `internal/seatapp` (standard library only), and the command
  `ataniq-seat`. They serve the pairing page and its API. `seatapp.Daemon` has
  one method, `Pair`; `ataniq-seat` wires it to a stand-in that always answers
  `ErrDaemonUnavailable`.
- The owner channel's service, `internal/approval/ownerchannel`, and the words
  of its connection, `internal/approval/ownerwire`. `Service.PairPresent` takes
  a source whose surface is `ownerchannel.SurfaceSeat` (`"seat"`). The daemon
  serves the owner connection on its Unix socket only in a build with the tag
  `ataniq_ownerchannel` (see
  [the lead daemon guide](../lead-daemon.md#the-owner-channel-tagged-build-only)).
  There the seat source is nil, so `seat.pair` is refused `unavailable` to every
  connection.
- A decision can be made only at the desk. `Service.Decide` refuses a source
  that is not the decider (`not-decider`) or whose surface has no owner activity
  inside the presence window (`owner-away`), and the record package's decision
  check (`checkDecision`, run by `record.DecodeDecision` and
  `record.EncodeDecision`) requires `confirmed` to be `"code"` and a console
  block with a process id. No WebAuthn assertion is verified anywhere.

## The listener: inside the daemon

The gate is a TCP listener inside `ataniqd`, compiled only under the existing
tag `ataniq_ownerchannel` and opened only when the owner asks for it with a
flag of `run` (planned, G4: `--seat-gate ADDR`, beside `--owner-channel`).
ADDR is one explicit IP address and port; a wildcard address and a host name
are refused. Without the flag nothing listens. The owner's firewall lets only
the authority host's address reach the port.

The listener serves `seatgate.Serve` with a handler from a new untagged package
(planned, G3: `internal/approval/gatehost`), which only tagged code imports.
The handler hands the service the source

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
below, is answered `unknown-op`.

| Capability | Operation | Params | Result | Built by |
| --- | --- | --- | --- | --- |
| `pair` | `seat.pair` | `ownerwire.SeatPairParams` | `ownerwire.SeatPairResult` | G3 (service exists) |
| `read` | `owner.list` | none | `ownerwire.Listing` | G3 (service exists) |
| `read` | `owner.show` | `ownerwire.ShowParams` | `ownerwire.ShowResult` | G3 (service exists) |
| `read` | `owner.paths` | `ownerwire.PageParams` | `ownerwire.PathsPage` | G3 (service exists) |
| `read` | `owner.patch` | `ownerwire.PageParams` | `ownerwire.PatchPage` | G3 (service exists) |
| `login` | `seat.login-challenge` | none | `SeatChallengeResult` | G8 |
| `login` | `seat.login` | `SeatLoginParams` | `SeatLoginResult` | G8 |
| `decide` | `seat.challenge` | `SeatChallengeParams` | `SeatChallengeResult` | G8 |
| `decide` | `seat.decide` | `SeatDecideParams` | `ownerwire.DecideResult` | G8 |

A gate built before G8 answers `["pair", "read"]`; after G8,
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

All are in `ownerwire`, with the strict decoders and the size caps of that
package; every byte string is standard base64 in the JSON, as in
`SeatPairParams`.

- `Assertion`: `credential_id` (1 to `MaxCredentialIDBytes`),
  `authenticator_data` (37 to 1024 bytes), `client_data_json` (1 to 2048
  bytes), `signature` (a strict DER ECDSA signature, at most 72 bytes). These
  are the four fields of a WebAuthn assertion response; the user handle is not
  carried.
- `SeatChallengeParams`: `frame` (a `DecisionFrame`, decoded by its own
  decoder) and `credential` (the display id of the session's credential).
- `SeatChallengeResult`: `challenge_id` (16 lower-case hex digits),
  `challenge` (32 bytes), `nonce` (32 bytes) and `expires_at` (a record's
  time).
- `SeatDecideParams`: `challenge_id`, `frame` (a `DecisionFrame`) and
  `assertion`.
- `SeatLoginParams`: `challenge_id` and `assertion`.
- `SeatLoginResult`: `credential` (the display id), `credential_id` (the raw
  id, which the seat puts in `allowCredentials`) and `kind` (the stored kind
  word).

The params of `seat.challenge` are capped at `MaxFrameBytes` plus 1 KiB, those
of `seat.decide` at `MaxFrameBytes` plus 8 KiB, and those of `seat.login` at
8 KiB. A result has the caps of the result types it reuses.

### Frame size

`ownerwire.MaxPageBytes` (256 KiB) caps the listing, a show and a page, while a
`seatgate` frame is at most 64 KiB (`seatgate.MaxFrame`). A result larger than
a frame is answered `too-large` by the transport. Version 1 needs both of these:

- The seat treats `too-large` from a read as "this card is too large for the
  phone; decide it at the desk", and offers no decision for that card.
- The frame cap of the transport rises to at least `MaxPageBytes` plus 1 KiB,
  so that every result the service may answer fits. This is a change of
  `internal/seatgate` and belongs to the task that owns that package; the lead
  places it.

## Errors

Every refusal on the gate is a code alone, of the grammar `[a-z0-9-]{1,40}`.
No text, no detail of a `*record.Error`, and nothing the peer sent goes on the
wire. The seat maps each code to fixed words of its own.

The transport's codes are those of `internal/seatgate` (`failed`,
`proto-unsupported`, `bad-hello`, `hello-required`, `hello-repeated`,
`rate-limited`, `busy`, `deadline-exceeded`, `too-large`, `shutting-down`).

The handler answers:

- A `*record.Error` with its own code, unchanged (`record-invalid`,
  `record-too-large`, `decision-invalid`, `binding-mismatch`, `review-changed`,
  `approval-expired`, `approval-consumed`, `approval-void`, `previous-epoch`
  and the others of `internal/approval/record/code.go`).
- The pairing and key errors of `ownerchannel` with the codes the daemon already
  gives them, which are also the console's (`consoleclient.PairError`). Of
  these, `seat.pair` can meet `keys-unavailable`, `pairing-refused` and
  `credential-invalid`, and the new operations reuse `key-unknown` and
  `keys-unavailable`. The other pairing codes belong to the desk's operations,
  which the gate does not serve.
- The daemon's own: `unavailable` (the service is closed, stopped or stopping),
  `door-unavailable`, `journal-reopened`, `failed` (anything else; the error
  goes to the log, never the params).
- New codes for version 1:

| Code | Meaning |
| --- | --- |
| `unknown-op` | The operation is not one the gate serves, or its capability is not served. |
| `challenge-stale` | No outstanding challenge has this id (unknown, used, expired, dropped by a restart), or the frame does not match the challenge. The seat says to tap again. |
| `challenges-full` | 32 challenges are outstanding and none has expired. |
| `assertion-invalid` | The assertion did not verify, for any reason (client data, origin, relying party, flags, signature). |
| `key-revoked` | The credential is on the list and revoked. |
| `kind-refused` | The credential's kind is not in the project's accepted kinds. |
| `origin-unset` | The daemon has no seat origin to check client data against. |

`key-unknown` answers a credential that is not on the list. `ErrNotSeat` cannot
occur on the gate, whose source is always a seat; it is answered `failed` and
logged.

The handler bounds the refusals of one peer as the Unix socket bounds a
connection that is not the decider (`leaddaemon.DecisionRefusalBound`, 16): the
first 16 refused `seat.pair`, `seat.login` and `seat.decide` calls of a
connection are logged, and the next one closes the connection.

## The challenge

A decision from the phone is a WebAuthn assertion over a challenge that the
daemon draws and binds to its own record. The seat never chooses a challenge
byte.

### Issue: `seat.challenge`

The daemon checks, in order: the source is a seat; the params are of their form;
an origin is set (`origin-unset`); the pending record exists and is pending (the
codes of `Service.Decide`); the credential named is on the list (`key-unknown`)
and not revoked (`key-revoked`), and its kind is accepted by the record's
project (`kind-refused`); `frame.binding_sha256` equals the binding digest of
the daemon's own pending record (`binding-mismatch`); and for an approve, the
sign-offs equal the record's flagged list (`decision-invalid`). So the phone is
never asked to sign what would be refused. None of these refusals is journaled:
no decision was made.

Then it draws a 32-byte nonce and an 8-byte challenge id from `crypto/rand`,
and computes

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
- `signed_off_sha256` is the lower-case hex SHA-256 of the canonical form (the
  record package's encoding) of the `signed_off` list as the decision record
  will hold it; `[]` when there is none.
- `reason_sha256` is the lower-case hex SHA-256 of the reason's bytes, of the
  empty string when there is none.
- `nonce` is the 32 raw bytes, last.

One function computes it, in `ownerwire` (planned, G8), and both the daemon and
the seat link call it; G8 adds fixed test vectors for it.

The daemon keeps the entry {challenge id, nonce, challenge, credential display
id, pending id, the record's project, kind "decision", expiry} in memory only. An entry lives 5
minutes, measured with the monotonic reading of the clock as the pairing's
times are. At most 32 entries are outstanding over both kinds; expired ones are
dropped first, and then a 33rd is refused `challenges-full`. A new decision
challenge for the same credential and pending id replaces and burns the older
one. A restart drops every entry, which fails safe: the owner taps again.

The result gives the challenge id, the challenge, the nonce and the expiry. The
seat link recomputes the challenge from the card it showed, the gate's pin and
the nonce, and when it differs it shows a fixed error and asks nothing of the
phone.

### Decide: `seat.decide`

The daemon checks, in this order:

1. The source is a seat, and the params are of their form.
2. It takes the entry of `challenge_id` out of the store, burning it, whatever
   happens next. No entry, an expired one or one of the login kind is
   `challenge-stale`.
3. The display id of `assertion.credential_id` is the entry's credential, which
   is on the list (`key-unknown`), not revoked (`key-revoked`, checked again
   here, so a revoke between challenge and decide holds), and its kind is
   accepted by the entry's project (`kind-refused`).
4. It recomputes the challenge from the frame as sent, its own instance and the
   entry's nonce, and compares it in constant time with the entry's challenge
   (`challenge-stale`). The frame cannot change between challenge and decide.
5. It verifies the assertion against the stored public key with the entry's
   challenge, the origin from the daemon's own owner-local seat origin file and
   the relying party id that is that origin's host (`assertion-invalid`, see
   [verification](#verifying-an-assertion)).
6. From here the path is the desk's: the frame's form, the record's state, the
   binding digest against the daemon's own record, the sign-offs, the day's
   limit with "held", the journal, the door and the outcome. The decision's
   activity entry names the surface `seat`, the gate session and no last
   activity. G8 refactors `Service.Decide` so that the desk and the seat share
   everything after authentication.

Refusals at steps 1 to 5 are not journaled: before step 5 passes, nothing shows
that the owner acted. They are logged without params and counted. A refusal
after step 5 is journaled as refused with surface `seat`, as the desk's are,
with the codes the journal accepts today (`decision-invalid`,
`binding-mismatch`). The result is the desk's `DecideResult`; its line is the
host's, and the seat renders it with `seatapp.Text`.

The plan of this work also asked that the issue of a challenge be journaled. The
journal's kinds are a fixed list (`internal/approval/ownerjournal`), and its
refused entries accept four codes only, so either needs a change of
`ownerjournal`. Version 1 journals neither: the decision record names the
challenge it used (below), and a challenge that was never used decided nothing.
If the lead wants the issue journaled, `ownerjournal` joins G8's scope.

### Login: `seat.login-challenge` and `seat.login`

A login proves that the phone holds a paired credential; it decides nothing. The
challenge is

```text
challenge = SHA-256( "ataniq.seat.login/1" || F(instance) || nonce )
```

with the same store, life, cap and burning (entry kind "login"). `seat.login`
takes the entry (`challenge-stale`), finds the credential by
`assertion.credential_id` on the list (`key-unknown`, `key-revoked`), and
verifies the assertion (`assertion-invalid`). A login belongs to no project, so
the kind policy is not asked here; it is asked at each challenge and decision. It answers the
display id, the raw credential id and the kind. The seat makes its session from
that answer. The daemon keeps no session.

The two domain strings differ, so an assertion made for a login can never be
taken as a decision, and the reverse.

### Verifying an assertion

The verifier is the credential kinds' (planned, G5:
`internal/approval/credkind`, standard library only), after WebAuthn Level 3,
section 7.2, "Verifying an Authentication Assertion":

- the client data's `type` is `webauthn.get`;
- its `challenge` decodes (base64url, no padding) to the expected challenge,
  compared in constant time;
- its `origin` equals the seat origin, and `crossOrigin` is absent or false;
- the authenticator data's relying party hash is SHA-256 of the relying party
  id;
- user presence and user verification are both set, and backup state implies
  backup eligibility;
- the signature is a strict DER ECDSA P-256 signature over
  `authenticator_data || SHA-256(client_data_json)`, checked against the stored
  public key.

A sign counter that does not grow is logged, not refused: synced passkeys
report 0.

## Presence

The desk's rule stays: a decision at the console needs the decider's place and
the owner's activity inside the presence window.

For the surface `seat` only, a verified assertion with user verification
replaces the presence window. The decider's place is the desk console's (one
connection on the Unix socket) and a seat never holds it, so `seat.decide` does
not ask it either; this second point is proposed with the first. A source whose surface is `seat` gets nothing by its surface alone: the
existing `Service.Decide` still refuses it `not-decider`, and only the path in
[decide](#decide-seatdecide), after step 5, skips the window. A seat decision is
not owner activity at the desk, and a desk beat is not presence at the phone.

The phone and the desk may both hold a card open. The journal takes one decision
per card; the second is refused as today.

## A deny needs an assertion

Owner decision: a deny from the phone needs a fresh passkey assertion, as an
approve does. Both go through `seat.challenge` and `seat.decide`. Otherwise any
process on the AI host of the daemon's user that can append a pin to the gate's
pin file could deny cards. The owner's passkey asks for user verification at each
use anyway, so a deny costs one more verification.

Leaving a card undecided sends nothing.

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
  with F as above. The journal keeps it in the record, so the record says which
  assertion decided.
- `challenge_id` is the id of the challenge the assertion signed (the plan's
  "nonce id"). The nonce itself is not recorded.
- `credential` is the credential's display id (`ownerjournal.KeyDisplayID`).
- `session` is the gate session: the first 16 hex digits of the seat's pinned
  fingerprint, the same as the journal's activity entry.

The record package's decision check holds a record to one of the two shapes.
G8 chooses the Go form (for example an optional block for each), as long as the
bytes are as here. The assertion itself is not kept: a later audit can match a digest, not
check a signature again. Keeping the whole assertion is a possible later change.

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
The interface (planned, G5) is:

```text
Kind{ Name() string
      Register(RegistrationInput) (Credential, error)
      Verify(Credential, AssertionInput, Expect) (Result, error) }
```

A stored kind is a word of the key list's grammar (1 to 32 lower-case letters,
digits and dashes, starting with a letter). Keys paired before kinds exist keep
the word `unverified`, which is what `PairConfirm` stores today. The project's
policy names the accepted kind words and accepts all by default; its place is a
field of the project's settings (planned, G6, in the host adapter's scratch
settings), and until it exists every kind is accepted. G6 records the kind at
pairing; until then every key is `unverified`.

## The pairing fingerprint

Owner decision: the fingerprint of a paired credential is 8 words, 64 bits: the
first 8 bytes of the digest `seatpair.FingerprintOf` already takes, each an index
into the same 256-word list. Today it is 6 words in `ownerwire.PairWords`,
`seatpair.FingerprintWords`, `seatapp.PairResult`, `pairview.State`, the script
`internal/seatapp/assets/words.js`, and the key list's check of a row's words
(`ownerjournal`, six words). The key list keeps the words each key was paired
with: a row written with six stays readable. The lead assigns the change to the
task that owns those packages.

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
  file changes on both hosts.

The owner carries every fingerprint by hand, both ways, at the first exchange.

## Limits that stay

- Same user. A lead that runs as the daemon's user outside a sandbox can read
  the gate key, append a pin and edit the key list (`owner/keys.log`). Until the
  sandbox is qualified and the key list is protected, phone decisions are
  scratch-only, with the STAND-IN banner, and approve nothing real.
- The wrong card. A taken-over authority host can show one card while asking a
  challenge for another; the assertion binds the binding digest, not what the
  phone showed. Showing a short tag of the binding at the desk and on the phone
  is a later change.
- The lead's sandbox must not reach the gate's port. A qualification case that a
  connection from inside the sandbox fails is needed before any real use.
- Nothing is queued while the phone or the gate is offline.

## The build tasks it feeds

G8, the daemon's side of phone decisions, implements: the new `ownerwire` types,
the challenge function and the separate list of gate operations; `confirmed:
"passkey"` and the seat block in `record`; `Service.SeatChallenge`,
`Service.SeatDecide`, the login operations and the shared decision path in
`ownerchannel`; and the gate handler's decide and login operations. Its tests
use the planned software authenticator (G5, `internal/approval/webauthntest`)
and show, each failing before the code: a replayed challenge id; a challenge for
another card, decision, sign-off set or reason; a revoked key, revoked between
challenge and decide too; a key not on the list; user verification off; a wrong
origin; a login assertion offered as a decision; a restart dropping the
outstanding challenges; and a passkey record that `record.DecodeDecision` and
`record.EncodeDecision` accept while a record with both blocks, or neither, is
refused.

G9, the seat app's approve and deny, implements: the decide form on the Card
page only, offered when the card package's `Card.MayApprove` allows it, with one
sign-off per flagged path; `navigator.credentials.get` with user verification
"required" and `allowCredentials` from the session's credential, asked only
after the owner tapped approve or deny with the sign-offs complete, never
submitted by itself; the challenge recomputed by the seat link before the phone
is asked; a deny through the same steps; the daemon's line through `Text`; and
each code above mapped to fixed words.

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
