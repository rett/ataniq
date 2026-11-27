# Permissions-based federation

Status: daemon state roots have local instance identities. Peer federation is planned.

Ataniq instances can coordinate projects, exchange tasks, and communicate in
both directions over authenticated MCP connections. Each instance remains an
independent authority for its projects, workers, credentials, and resources.
Federation does not require a central instance or a shared provider.

## Instance identity

Before the first run, use `ataniqd init --state-dir DIR [--label TEXT]` as the
daemon user. Omitting `--state-dir` selects the same default as `run`. The
command creates `DIR/instance.key`, a singly linked, user-owned 0600 file in
the private state root. The authority ID is 64 lowercase hexadecimal digits encoding SHA-256 over the
ASCII bytes of `ataniq-instance-authority/1` immediately followed by the
32 raw bytes of the Ed25519 public key (no separator or trailing newline). The
store incarnation is an independent random 16-byte value encoded as 32
lowercase hexadecimal digits. Recreating and initializing the root generates
both values afresh. The optional label is for display only.

For an existing scratch root, stop the daemon and run the same init command
with the configuration's `state_root`. It validates the old journal and
atomically replaces it with a version 2 journal bound to the authority and
incarnation, preserving its records. Repeating init keeps the original key
and label. On startup the daemon refuses a missing, damaged or unsafe key,
an unbound journal, or a journal from another instance or incarnation. If
the key for a bound journal is lost, restore the original key from backup.

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

## Initial deployment and load balancing

The initial operational target is a two-host federation, with one independent
instance on each host. Both can originate and receive permitted work. Hostnames,
addresses, credentials, and directional grants belong in deployment settings
outside the public repository. Fake-backend tests precede a real two-host
acceptance run; single-host success does not complete this milestone.

Federated scheduling is capability- and permission-aware. Eligibility checks
project grants, required pack versions and observed capabilities, execution
platform, data and credential locality, budgets, policy, and peer health before
ranking candidates. The receiving instance independently admits the work and
reserves capacity; a sender's placement decision is not an execution grant.

Among eligible peers, use bounded queues, fair project shares, priorities,
locality preferences, and available execution slots. Record why a peer was
selected or excluded. Provider rate limits and subscription capacity can be
shared across hosts and must not be counted once per host. CPU, memory, model
capacity, and concurrency are distinct resources. Incomparable or missing
capacity observations must remain explicit.

One originating authority owns placement for a task revision. Reservation,
acceptance, and execution use durable job identities, idempotency keys, bounded
leases, and generation checks. Simultaneous schedulers must not independently
authorize the same job. Stale ownership must be fenced at the execution adapter;
a lease expiring in the database alone does not prove that work stopped.

The first version balances queued tasks. It does not transparently migrate
running sessions. Reassign only after rejection, a proven non-start, confirmed
termination, or an explicitly permitted duplicate-tolerant retry policy.
A partitioned peer with unknown execution state is not an available retry slot.
If fencing or liveness cannot be established, preserve the ambiguity and block
unsafe reassignment. Retry budgets and backoff prevent work from bouncing
between peers. Draining a host stops new admissions while tracking existing
jobs.

Record queue delay, reservation delay, active runtime, transfer overhead,
completion evidence, failures, rework, and observed provider usage. Report
routing outcomes by comparable project/task cohorts. Measurements may propose
policy changes through the project-local improvement workflow; they do not
automatically alter permissions, budgets, or approved provider eligibility.

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
4. Add permission-aware placement, capacity reservations, and queued-task
   balancing across the initial two-host topology.
5. Add partition recovery, cancellation, revocation, and credential rotation.
6. Add operator controls for peers, grants, pending offers, and audit history.

Test asymmetric permissions, denied reverse calls, cross-project isolation,
spoofed principals, absent delegation authority, stale policies, token audience
mismatch, duplicate offers, lost acknowledgements, crashes around launch,
out-of-order events, conflicting revisions, revoked subscriptions, revoked
artifact access, disconnected cancellation, payload limits, and restart replay.
Also test simultaneous scheduling, stale capacity, shared provider quotas,
project fairness, draining, reservation expiry, stale-owner fencing, and
ambiguous execution without duplicate dispatch. Use two independent instances
and a fake backend before private-socket or remote integration tests. No live
federation peers are enrolled by this design.

Copyright (c) 2026 Everett C. Haimes III. Licensed under the MIT License;
see [LICENSE](../LICENSE).
