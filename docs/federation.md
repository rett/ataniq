# Permissions-based federation

Status: required architecture; not implemented or interoperability-tested.

Ataniq instances can coordinate projects, exchange tasks, and communicate in
both directions over authenticated MCP connections. Each instance remains an
independent authority for its projects, workers, credentials, and resources.
Federation does not require a central instance or a shared provider.

## Peer identity and directional grants

A peer relationship binds an instance identity to configured endpoints and
trusted authentication issuers or certificates. OAuth authorization and mTLS
support follow the core authentication policy. Authentication identifies a
peer; local policy decides what that peer may do.

Each direction has its own explicit grants. A may submit tasks to B while B
can only return status and results to A. Bidirectional transport does not
create reciprocal permissions. Grants bind the peer, originating principal,
local project mapping, resource, action, limits, expiry, and policy version.
Do not trust a peer-supplied principal string without a validated delegation
binding. Do not forward the originating user's access token to another peer.

Project identifiers are namespaced by instance identity. Sharing one project
never reveals another project's inventory, messages, logs, or artifacts.
Remote project names cannot choose arbitrary local paths. Sharing policies
state which metadata, artifacts, and outcome evidence may cross the boundary.

Permissions are independently assignable for discovery, project observation,
coordination updates, task proposals, task acceptance, task execution, status
reading, artifact transfer, cancellation requests, and communications.
Execution also needs local admission, available capacity, eligible packs,
credential access, and budget. Release and deployment authority stay separate.

Trust is non-transitive. A grant from A to B does not let B delegate to C.
The initial implementation refuses onward delegation; a future extension
would need explicit bounded delegation and retained origin provenance.

## Project coordination and task exchange

A project retains an authoritative home for each record or work item. Peers
exchange versioned proposals and acknowledged events rather than overwriting
local project state. Concurrent edits use expected revisions; conflicts are
reported for reconciliation, never silently resolved by last arrival.

Task offers carry origin instance/project/task identities, a content digest,
requirements, inputs, requested capabilities, resource limits, expiry, and an
idempotency key. Receiving an offer only records a proposal. Acceptance binds
a local job and attempt to the exact offer, effective policy, and pack versions.
Changes to the offer require a new revision and acceptance.

The protocol distinguishes offered, accepted, queued, running, waiting,
completed, failed, cancellation-requested, cancelled, rejected, and unknown.
Results identify the executing instance, attempt, input/candidate identities,
checks, artifacts, and provenance. Remote success is execution evidence, not
local qargi verification, review acceptance, merge, or release approval.

## Communications and MCP surface

Expose federation as a versioned canonical feature pack with typed MCP tools
for offers, acknowledgements, coordination updates, messages, and cancellation
requests, plus project-scoped resources for status and permitted artifacts.
Agents, the UI, and peers use the same authorization and audit path.

Messages have sender and recipient identities, project or task context, a
message ID, correlation ID, expiry, and delivery acknowledgements. Scope
subscriptions and resource reads as strictly as writes. Terminal output and
remote messages are untrusted content; they cannot grant authority or silently
become instructions to execute. Any message-triggered automation requires an
explicit local policy and a traceable resulting task.

Transport and identity integrations remain pluggable. Federation semantics,
permission checks, and durable task identities must not depend on a particular
MCP connection ID, OAuth issuer, hosting platform, or model provider.

## Recovery, revocation, and audit

Use durable outbox/inbox records, deduplication, bounded retries, pagination,
and resumable event cursors. A lost acknowledgement must not launch a second
job. Persist acceptance and launch intent before effects, then reconcile an
uncertain launch against the backend's stable job identity. Do not claim
exactly-once execution across crashes and network partitions.

Recheck current grants on operations, subscription delivery, and artifact
access. Revocation blocks new admissions and further disclosure. Accepted
work follows an explicit lease and revocation disposition, including whether
local work must be cancelled. A disconnected peer cannot prove remote work
stopped: report cancellation as pending or state as unknown until confirmed.
Credential rotation must preserve identity without silently widening trust.

Both sides retain correlated audit receipts for policy decisions, accepted
work, retries, messages, results, cancellation, and revocation. Bound retained
payloads and respect each project's privacy and evidence-sharing policy.
Federated measurements remain attributed to their project and environment;
they cannot automatically alter another project's model routing or packs.

## Implementation slices and acceptance

1. Define peer identities, grants, project mappings, and versioned MCP schemas.
2. Implement two-instance observation and messaging with fake credentials.
3. Add durable task offers, local admission, execution, and result receipts.
4. Add partition recovery, cancellation, revocation, and credential rotation.
5. Add operator controls for peers, grants, pending offers, and audit history.

Test asymmetric permissions, denied reverse calls, cross-project isolation,
spoofed principals, absent delegation authority, stale policies, token audience
mismatch, duplicate offers, lost acknowledgements, crashes around launch,
out-of-order events, conflicting revisions, revoked subscriptions, revoked
artifact access, disconnected cancellation, payload limits, and restart replay.
Use two independent instances and a fake backend before private-socket or
remote integration tests. No live federation peers are enrolled by this design.

Copyright (c) 2026 Everett C. Haimes III. Licensed under the MIT License;
see [LICENSE](../LICENSE).
