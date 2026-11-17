# Client command and event protocol

Status: proposed, not implemented. The terminal console, the later web console
and the CLI use one versioned typed Ataniq command and event contract and
authorization path. The first client transport is an owner decision still to
be made; WebSocket, and HTTP commands plus resumable SSE, are candidates.
Transport changes do not change authority, capabilities, command identities or
durable cursor semantics. One authoritative provider binding is
multiplexed to authorized Ataniq clients. Optional native coexistence requires
direct, version-specific provider evidence; no second connection, terminal
scraping or routing workaround creates support.

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

## Terminal data plane

Amended 2026-11-16 (owner decision Q20 of the lead-hosting design, section
0.4). The holder that owns a terminal-hosted lead's PTY and the attach client
that implement this section are provisional and unqualified; see
[runtime](runtime.md) for the lead's `ready` state.

The attach stream of a terminal-hosted lead is a terminal data plane, not a
second command path. It carries terminal output to a console and the writer's
typed input back, and nothing else. Every other mutation stays a typed command
with a command ID under the rules above; nothing sent on the attach stream is a
command, an approval or an authority. The stream grants no approval authority
by connecting to a conversation, as [approvals](approvals.md) states.

The stream is opened only with a single-use ticket that the typed contract
issues. A console asks with a typed command carrying a command ID, the lead and
the role wanted, writer or viewer. The ticket is bound to that lead, its
execution epoch and that role, and expires after a short time; a ticket that is
used, expired or bound to another lead, epoch or role is refused and the
refusal is recorded. The holder accepts a connection only with a valid ticket.

There is at most one writer and one read-only viewer, and no takeover. A writer
ticket is issued only while there is no writer. A writer is replaced only by a
typed command, recorded like any other, that removes it; the removed client is
told and becomes the viewer or is closed. Input from the viewer is refused and
counted, never forwarded. Detaching never ends the lead, and a writer that
disconnects leaves the lead with no writer; nothing is typed on its behalf.

Typed writer input is the owner's direct words in the sense of
[approvals](approvals.md). The same limit applies as in [security](security.md)
for same-user execution: neither the ticket nor the peer check tells the
owner's console from another unsandboxed process of the same OS user, which
could ask for a ticket or drive the console or the daemon. Version 1 accepts
that limit. It records every writer change with the client and shows it as a
notice in every attached pane. A separate OS user for the console and daemon is
the exit, and it is not part of version 1.

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
an authorization credential. Bind it to project, query, schema and
authorization projection generation. A stale or unauthorized binding produces
an explicit reset, not a successful replay under different visibility.

Each authorization projection has a generation. On narrowing permissions,
purge now-unauthorized client state and close or refilter streams before further
delivery. On widening, establish a fresh authorized snapshot and watermark, or
an equivalent explicit authorized backfill, before tailing: previously hidden
records may predate the old cursor. Apply the same filtering to graph edges,
counts and cached artifacts so they do not reveal hidden records.

## Approvals and revocation

Who may approve is defined in [approvals](approvals.md): the owner's direct
words or an owner-rooted delegation-chain grant, never a relayed message. The
sections below describe the command-level record of a decision. An owner
decision arrives on the owner channel defined there, not on a transport a lead
can reach.

An approval identifies an immutable command, action, target revision, payload
and execution epoch, the approving owner decision or grant, expiry and current
state. Decision submission is authorized against current policy and atomically
resolves an outstanding approval once.
The receipt records decision and actor; duplicate identical commands replay
their receipt, while a competing or stale decision is rejected with the
resolved state if the caller may see it. Provider connection-bound ceremonies
must be qualified; if a native path can bypass arbitration, that path cannot
claim Ataniq approval enforcement. A stale provider-specific ceremony cannot
authorize a changed action. Other clients may observe only within scope.

An affirmative decision grants one use under current policy. At the durable
dispatch-prepared transition, atomically link and consume the grant with the
command state, rechecking policy, expiry and revocation. Reconnect or retry
cannot produce a second effect using that grant. If delivery is ambiguous,
retain the consumed grant and uncertain command state pending reconciliation;
any subsequent action requires an explicit disposition and applicable new
authorization. Admission or an earlier decision alone is not dispatch authority.

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
4. Race a decision backed by the owner's direct words or a verifying grant
   against one backed by neither. Only the backed decision may resolve the
   approval; a later decision is stale. A grant-backed decision applies only
   once grants are honoured for that gate. Race two backed decisions and
   assert exactly one durable resolution.
5. Revoke a client during an active subscription. Assert subsequent protected
   events, commands and artifact reads are denied, including after reconnect.
6. Saturate a bounded subscriber buffer. Assert explicit disconnection and
   replay from its last applied cursor, or reset if retention has expired.
7. Narrow visibility while a client holds a snapshot and stream. Assert cached
   unauthorized records are purged, the stream closes or refilters, and its
   old-generation cursor returns reset without leaking hidden data.
8. Hide a record, take a snapshot and cursor, then widen visibility. Assert the
   old cursor returns reset and a fresh snapshot or explicit backfill includes
   the older record before tailing resumes; subsequent events appear once.
9. Consume an approval at dispatch preparation, lose the delivery receipt and
   reconnect. Assert consumed/uncertain state and no second effect. Change the
   payload, revision or epoch, or expire or revoke the grant before preparation:
   assert dispatch is denied and an old provider ceremony cannot authorize it.

10. Attach to a terminal-hosted lead with no ticket, a used ticket, an expired
    ticket, and a ticket for another lead, epoch or role. Assert each is refused
    and recorded. With a writer attached, ask for a second writer ticket and
    assert none is issued; ask for a viewer ticket and assert it is issued.
    Send input from the viewer and assert it is refused and counted, with
    nothing reaching the terminal.
11. Replace the writer by the typed command. Assert it is recorded with its
    command ID and the client, the removed client's input is refused afterwards,
    and every attached pane shows the notice. Send a command, an approval and a
    decision on the attach stream and assert none takes effect.

These are test specifications; transport, provider and UI conformance remain
unvalidated. Desktop and phone sharing through unified clients must be tested;
native Desktop/phone coexistence remains unverified and optional.
