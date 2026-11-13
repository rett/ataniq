# Client command and event protocol

Status: proposed, not implemented. The console and CLI use one versioned
Ataniq contract and authorization path. WebSocket is primary; HTTP commands
plus resumable SSE are the fallback. Transport changes do not change authority,
capabilities, command identities or durable cursor semantics. One authoritative
provider binding is multiplexed to authorized Ataniq clients. Optional native
coexistence requires direct, version-specific provider evidence; no second
connection, terminal scraping or routing workaround creates support.

## Commands

Every mutation has a client-generated stable command ID, project and target,
operation, canonical payload and expected revision when applicable. The server
binds ID to authenticated client, scope and payload digest in durable storage
before acknowledging acceptance. A retry with the same binding returns the
original receipt and current status; reuse with different content or scope is a
conflict and has no effect. Stale expected revisions also conflict and return
the current revision where authorized. Acceptance means durable admission, not
provider delivery, completion, approval or success.

After a lost response, either transport queries the command ID before any retry;
fallback never blindly resends an unacknowledged provider action. Capabilities
and unsupported operations are explicit, versioned and scoped to the target.
Both transports apply identical checks and produce the same receipts and events.
Inputs have size limits; rejection occurs before durable admission, with a
machine-readable reason. Limits and supported protocol versions are advertised.

## Events, replay and snapshots

Events carry a schema version, stable event ID, authority and project, resource
identity and revision, type, causal command or attempt where applicable, and
opaque durable cursor. The cursor orders the authority's journal, not provider
wall clocks or cross-host events. Consumers deduplicate by event ID and tolerate
replay. Authorization filters both snapshot and event delivery; filtering must
not expose hidden payloads or imply that missing events never occurred.

A consistent authorized snapshot includes a watermark cursor from the same
logical database view. Subscribe or replay strictly after that watermark;
events committed during snapshot construction must appear in snapshot or
subsequent replay. On reconnect, resume after the last durably applied cursor.
If retention has expired or a cursor is invalid, return an explicit reset
requirement and obtain a fresh snapshot; never silently start at the current
tail. Specify bounded retention, page size, payload and subscriber buffers in
deployment policy, advertise effective bounds, and disconnect a slow consumer
with a resumable position rather than silently dropping events. A cursor is not
an authorization credential.

## Approvals and revocation

An approval identifies the exact operation, target revision, designated
approver or role, expiry and current state. Decision submission is authorized
against current policy and atomically resolves an outstanding approval once.
The receipt records decision and actor; duplicate identical commands replay
their receipt, while a competing or stale decision is rejected with the
resolved state if the caller may see it. Provider connection-bound ceremonies
must be qualified; if a native path can bypass arbitration, that path cannot
claim Ataniq approval enforcement. Other clients may observe only within scope.

Revocation takes effect on new commands, subscription delivery, snapshots and
artifact access. Terminate or refilter existing streams on policy change, and
recheck authorization at decision and dispatch, not just connection time.
Already accepted or executing work follows its recorded revocation disposition;
remote cancellation stays pending or unknown until confirmed. Audit the decision
without exposing revoked content to the client.

## Acceptance scenarios

Use two enrolled clients, a deterministic journal and both transports:

1. Submit the same ID and payload on WebSocket, lose the receipt, then query
   over HTTP. Expect one admission and the original receipt. Reuse the ID with
   a different payload: conflict and no additional effect.
2. Capture a snapshot while committing an event. Replay after its watermark:
   assert each authorized change appears in the snapshot or replay, with no
   gap; duplicate event IDs are applied once.
3. Resume an expired cursor: expect reset required, then a fresh snapshot;
   never report a successful empty replay as complete history.
4. Race two decisions by designated and undesignated clients. Only the
   designated decision may resolve the approval; a later decision is stale.
   Race two eligible decisions and assert exactly one durable resolution.
5. Revoke a client during an active subscription. Assert subsequent protected
   events, commands and artifact reads are denied, including after reconnect.
6. Saturate a bounded subscriber buffer. Assert explicit disconnection and
   replay from its last applied cursor, or reset if retention has expired.

These are test specifications; transport, provider and UI conformance remain
unvalidated. Desktop and phone sharing through unified clients must be tested;
native Desktop/phone coexistence remains unverified and optional.
