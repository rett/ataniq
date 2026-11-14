# Application command contracts

Status: implemented pure model, JSON codec and binary binding in
[`internal/appmodel`](../../internal/appmodel/model.go). This package has no
authentication, storage, service, transport or execution implementation. Its
fixtures establish codec behavior and canonical arithmetic; they do not qualify
durable admission, authorization, adapters or provider execution. Read this with
the [client protocol](client-protocol.md), [security](security.md),
[runtime](runtime.md) and [guarded journal](journal.md) contracts.

## Normal API and identities

`Decode([]byte)` accepts one bounded version 1 wire object and returns an
`Envelope`. `Encode(Envelope)` validates and returns owned JSON bytes.
`Envelope.Validate()` checks typed values. `Bind(CompositionIdentity, Envelope)`
validates both inputs and returns an owned immutable `Binding`. Use these
entrypoints at composition boundaries: ordinary `encoding/json` marshaling or
unmarshaling of the library structs is not the application wire contract.

`Envelope.Operation` is a closed union of `Enqueue` and `Cancel` values. Nil,
pointers and other typed variants are rejected. `Enqueue` carries a
`ConversationRef` and `EnqueuePayload{Text}`; `Cancel` carries the exact original
`CommandKey`. `ProjectRef` and conversation/command targets include their
authority. All these library types are data, not proofs of identity or permission.

`CompositionIdentity` means trusted composition supplies the authority and
enrolled client independently of client JSON. Merely constructing this value or
passing validation makes no authentication decision. A future service must
authenticate, verify enrollment and check current policy. The codec does not
assume that a syntactically valid foreign authority is authorized or supported.

The command key is `(authority, enrolled client ID, client command ID)`.
Cross-surface duplicates must use the same enrolled client. A different client
changes the key and immutable binding. A cancellation target includes the
original command's client ID, which need not equal the submitting client's ID;
the future service must authorize that exact target. Command IDs, internal
effect/attempt IDs and transport/RPC request IDs have separate lifetimes and
namespaces. This package neither assigns effect IDs nor turns RPC IDs into
command identities.

## Version 1 wire schema

Every key shown below is required, including `expected_revision` and `payload`.
Every object is closed: unknown or irrelevant keys are refused even if their
value is empty. Key matching is case-sensitive after JSON escape decoding.

```json
{
  "schema_version": 1,
  "command_id": "cmd-a",
  "project": {"authority": "auth-a", "id": "project-a"},
  "target": {"authority": "auth-a", "kind": "conversation", "id": "conv-a"},
  "operation": "input.enqueue",
  "expected_revision": "r7",
  "payload": {"text": "hello"}
}
```

`queue.cancel` instead requires this target and empty payload, with its own
stable command ID and the expected revision of the targeted command status:

```json
{
  "schema_version": 1,
  "command_id": "cancel-a",
  "project": {"authority": "auth-a", "id": "project-a"},
  "target": {"authority": "auth-a", "kind": "command", "client_id": "client-a", "id": "cmd-a"},
  "operation": "queue.cancel",
  "expected_revision": "s1",
  "payload": {}
}
```

The conversation target forbids `client_id`; it cannot be supplied as an empty
string or null. Enqueue requires exactly `payload.text`; cancel forbids it.
There are no client-JSON principal, enrolled-client, credential, policy-proof or
RPC fields. The `client_id` within a command target is an addressed identity,
not a claim about who submitted the request. Missing and null required values,
wrong types and unsupported operation/target/payload combinations are rejected.

Application IDs and revisions use ASCII `[A-Za-z0-9._:-]`, from 1 through 128
bytes. They are opaque, not numeric ordering tokens. The journal's existing,
broader raw-byte identity domain remains unchanged. Text is valid Unicode scalar
text, including empty text, whitespace, escaped NUL and newlines. No whitespace,
line-ending or Unicode normalization occurs. Composed and decomposed text remain
different. A literal unescaped control byte is invalid JSON; an escaped control
character is retained exactly after decoding.

## Strict parser and limits

The [scanner](../../internal/appmodel/scan.go) checks size and strict UTF-8 before
walking JSON with bounded nesting and token count. It rejects BOM prefixes,
unpaired surrogate escapes, duplicate **decoded** keys at every object level,
malformed grammar and trailing values or data. Thus `id` and `\u0069d` collide.
It keeps only the key sets of open objects, not a generic decoded object tree.
After scanning, shallow typed schema decoding rejects unknown fields and wrong
types. Errors contain stable domain codes, never copies of submitted text or
credentials. The codec uses no timers or goroutines.

| Named default | Limit and unit |
| --- | --- |
| `MaxWireBytes` | 1 MiB (1,048,576 received bytes), including whitespace |
| `MaxTextBytes` | 64 KiB (65,536 decoded UTF-8 bytes), not runes |
| `MaxBindingBytes` | 128 KiB (131,072 complete canonical bytes), including header and lengths |
| `MaxIDBytes` | 128 ASCII bytes per ID or revision |
| `MaxDepth` | 16 simultaneously open objects/arrays; root counts as one |
| `MaxTokens` | 16,384 keys, scalar values and container opening/closing tokens; commas and colons do not count |

Bounds are inclusive engineering defaults, not protocol throughput claims.
Callers receiving a stream must cap acquisition at `MaxWireBytes` before building
the byte slice; `Decode` bounds its own work but does not own transport framing.
Typed encoding and binding validate before allocation proportional to text.
Canonical length arithmetic checks remaining capacity before adding or converting
lengths to u32. Current v1 fields cannot reach the independent binding ceiling;
an internal guard test exercises that ceiling directly. Depth and token ceilings
are tested on structurally valid JSON before v1's shallower schema rejection.

Error precedence is explicit. Full JSON syntax, Unicode, duplicate-key and
resource checks run first, then the required closed non-null outer shape.
`schema_version` must be an integer token: `1.0`, `1e0`, strings and booleans are
`invalid_input`. Literal `1` selects v1. Other syntactically valid JSON integer
tokens, including zero, negative integers and integers larger than a machine
word, return `unsupported_version` without interpreting their inner schema as
v1. A malformed JSON document cannot become an unsupported-version response.
Within v1, an unknown nonempty operation name in the ID character domain returns
`unsupported_capability`; invalid known-operation shapes return `invalid_input`.
Neither response is an admission or a lookup result.

## Exact binary binding

The encoding is `ataniq-command-binary-v1`, independent of JSON formatting.
The header is `ATANIQ-COMMAND`, followed by NUL and byte `0x01`, exactly
`4154414e49512d434f4d4d414e440001` in hexadecimal. Each subsequent scalar is a
four-byte unsigned big-endian UTF-8 **byte** length followed by those exact bytes.
The fixed common order is:

1. `authority_id` from trusted composition
2. `enrolled_client_id` from trusted composition
3. `command_id`
4. `project_authority`
5. `project_id`
6. `target_authority`
7. `target_kind`
8. `target_client_id`
9. `target_id`
10. `operation`
11. `expected_revision`

`target_client_id` has length zero only for a conversation target; the
corresponding wire field is forbidden. `input.enqueue` appends one length-prefixed
text field, even when empty. `queue.cancel` appends no payload fields. JSON key
order, insignificant whitespace and equivalent escape spelling have no effect.
Credentials, current policy generation and transport/RPC IDs are excluded;
routine credential rotation preserves the same enrolled client and binding.

`Binding.Bytes()` returns a copy of all canonical bytes; `Digest()` returns the
SHA-256 array by value, and `Key()` returns a value copy. `Equal` compares complete
canonical bytes, never digest alone. Retaining a digest is not a substitute for
retaining and comparing the full binding. The zero binding is invalid and never
equal to another binding. Decoding retains no mutable caller buffer.

The eight portable [golden vectors](../../internal/appmodel/testdata/canonical-v1.json)
cover ASCII, composed/decomposed text, newline, NUL, changed revision, changed
enrolled client and exact cancellation. Tests verify every vector's canonical
hex, byte length and SHA-256, then round-trip through normal Encode/Decode/Bind
entrypoints. These vectors are synthetic arithmetic evidence, not authentication
or persistence qualification. Tests also cover rejection boundaries, ownership,
full-byte equality and a bounded standard-library fuzz seed corpus.

## Admission and status contract for later composition

`AdmissionReceipt` records an immutable command key, content digest, admitted
revision and cursor. `CurrentStatus` is a separate revisioned projection with
queued, held, cancelled, dispatching, completed or uncertain state. These value
types describe records; constructing one proves no storage outcome. A cursor is
opaque data, never an authorization credential. The original receipt and full
binding must survive subsequent status changes.

The future service must read authentication/enrollment, credential validity and
relevant policy, resolve duplicate lookup under **current disclosure authority**,
then check a new command's expected revision and allocate FIFO in one storage
transaction. Admission writes, receipt, FIFO, status and causal application event
share that boundary, serialized with revocation. An exact duplicate returns the
original receipt and currently disclosable status **before** comparing its old
expected revision with the resource's current revision. A changed immutable
binding under the same key conflicts and creates no second admission. Only a
confirmed commit can return one new admission. A database transaction cannot
atomically commit an external provider effect.

A lost response is an adapter observation of unknown admission, requiring
stable-key lookup and reconciliation. It creates no authority-issued receipt.
An unknown storage commit remains `storage_unknown`; it cannot be reported as a
proved rejection or an invitation to submit a different command ID. Public lookup
must use indexed, currently authorized application queries. Existing trusted
journal Inspect/Snapshot calls do not establish public disclosure rights.

Explicit client revocation or loss of permissions required by queued work holds
the affected queued work and pauses its FIFO until owner disposition. Routine
credential rotation, unrelated policy changes and permission widening do not by
themselves invalidate valid admissions. Recovery or restored/wider permission
does not automatically revive held work. Explicit owner disposition must retain
the original immutable receipt and causal history.

Durable `queue.cancel` is a separate stable command, authorized for an exact
original command and resolved against queue state transactionally. Already
cancelled and too-late outcomes remain distinct; a queue cancellation does not
claim to terminate an executing provider. RPC cancellation, request timeout or
EOF ends adapter observation and never becomes durable queue cancellation.

Busy input is FIFO input, never implicit steering. Provider-supported concurrency
remains a requirement for later qualified composition. Every actual effect needs
a fresh qualified `dispatch.Boundary` authority check and real runtime/grant
identities. Queue-only composition must have zero Boundary reachability, checked
by dependency tests when that composition exists. No fake grant or runner identity
can make queued commands executable. Enabling execution for earlier queue-only
work requires explicit disposition. Internal dispatch and journal behavior and
schema are unchanged by this pure package.

## Stable domain outcomes and fixture boundaries

| Code | Meaning |
| --- | --- |
| `invalid_input` | Malformed, oversized or invalid typed/wire command |
| `unsupported_version` | Well-formed integer schema token other than v1 |
| `unsupported_capability` | Operation is not implemented by this contract |
| `unauthenticated` | No currently valid authenticated enrollment |
| `permission_denied` | Current operation authority denied |
| `hidden_or_absent` | Lookup cannot disclose whether the target exists |
| `binding_conflict` | Same key with changed immutable content or scope |
| `revision_conflict` | New command's expected revision does not match |
| `queue_held` | Queue cannot advance pending explicit disposition |
| `cancel_too_late` | Target passed the queue-cancellation transition |
| `already_cancelled` | Exact target is already cancelled |
| `storage_unknown` | Storage outcome requires reconciliation |
| `reset_required` | Cursor/projection cannot continue; obtain authorized snapshot |
| `capacity_exceeded` | Configured finite capacity prevents the operation |

Only input/version/capability validation is executed here. Remaining codes and
receipt/status types define service contracts, not simulated service behavior.
Successful cancellation is represented by cancelled status; execution completion
requires provider evidence outside this codec.

Later auth/admission acceptance must exercise two adapters sharing one enrolled
client and store, duplicate disclosure after revision advance, changed-binding
conflicts, absent enrollment and forged identity fields, revocation on an open
connection, revocation versus admission/dispatch races, lost-acknowledgement
lookup, FIFO holds and explicit owner disposition, exact cancellation races and
EOF detachment. Queue-only tests must count zero provider calls. Authorized
snapshot/replay watermarks, projection resets and capacity recovery need their
own storage fixtures. None of those scenarios is a passing gate supplied by the
codec tests, nor does the model settle a storage schema or migration path.
