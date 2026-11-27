# Permissions-based federation

Daemon state roots have local instance identities and enforce project lead assignments.
Peer federation is planned.

Ataniq instances can coordinate projects, exchange tasks, and communicate in
both directions over authenticated MCP connections. Each instance remains an
independent authority for its projects, workers, credentials, and resources.
Federation does not require a central instance or a shared provider.

## Instance identity

Before the first run, use `ataniqd init --state-dir DIR [--label TEXT]` as the
daemon user. Then, while the daemon is stopped, enroll each project with
`ataniqd project-home --state-dir DIR --home self PROJECT` on its chosen home.
Use that instance's full authority ID in place of `self` on each executor.

Omitting `--state-dir` from `init` selects the same default as `run`. The
command creates `DIR/instance.key`, a singly linked, user-owned 0600 file in
the private state root. The authority ID is 64 lowercase hexadecimal digits
encoding SHA-256 over the ASCII bytes of `ataniq-instance-authority/1` immediately followed by the
32 raw bytes of the Ed25519 public key (no separator or trailing newline). The
store incarnation is an independent random 16-byte value kept in the
private file `DIR/store.incarnation` and displayed as 32 lowercase
hexadecimal digits. Recreating and initializing the root generates
both values afresh. The optional label is for display only.

For an existing scratch root, stop the daemon and run the same init command
with the configuration's `state_root`. Before creating a key, init replays
the existing journal under its exclusive lock and holds the lock through migration. The
next init removes and reports any stale `.registry-check-*` replay directories
left in the root by an interrupted earlier build. A corrupt record leaves the
original journal and identity untouched. After validation, init atomically replaces the old journal with
a version 2 journal bound to the authority and incarnation, preserving its
records. Init prints `journal=created` for a new journal,
`journal=existing` for an already bound journal, or `journal=migrated` with
the number of complete records after the header and the backup path.
Before binding a v1 journal, init saves its exact bytes, including an
unfinished tail, as `registry/registry.jsonl.v1` (0600). Allow space for
the backup and the replacement journal. A retry can reuse an identical
backup. If an earlier backup differs, move that backup offline first.

Older builds cannot open the migrated journal. The daemon reads only
`registry/registry.jsonl` and refuses the v1 backup if copied into its place.
Repeating init keeps the original key and label. Supply `--label` only on the first init. Any later `--label`,
including an empty or identical value, is refused. On startup the daemon
refuses a missing, damaged or unsafe identity, an unbound journal, or a
journal from another instance or incarnation. If the key was made by the
pre-release build that embedded the incarnation in `instance.key`, keep
that root offline and re-run init on a fresh root.

A backup of `instance.key` holds the private Ed25519 seed. Protect it as a
credential. A copied root (including a copy made with `cp -a`) is the same
instance and must never run beside the original.

### Files in the state root

Paths are relative to the daemon user's 0700 state root. Init creates both
identity files with mode 0600. Each magic string below ends with a newline.
Each identity file ends with the 32 raw bytes of SHA-256 over all preceding
bytes in that file.

| File | Contents and use |
| --- | --- |
| `instance.key` | `ataniq-instance-key/2`, 32-byte Ed25519 seed, optional UTF-8 display label (up to 128 bytes), checksum. Its public key determines the authority. |
| `store.incarnation` | `ataniq-store-incarnation/1`, 16 random bytes, checksum. Restore replaces this file. |
| `registry/registry.jsonl` | Active journal in the private `registry/` directory. Its v2 header binds the authority and incarnation. The daemon locks this file and replays the hash-chained records that follow. |
| `registry/registry.jsonl.v1` | Private, byte-for-byte v1 backup saved during migration. Keep it offline with the old root when restoring. |
| `daemon.lock` | Daemon singleton lock, also held during restore. Its PID text is a hint about the holder. |

Init stages writes under temporary names. These include `.instance-*` in
the state root, and `.registry-*` and `.registry.jsonl.v1.tmp` inside
`registry/`. Use the published names in the table for recovery.

### Restore an instance

If only the key was lost and the root is intact, put the original key back as
`instance.key` and start normally. The missing-key diagnostic checks the
surviving incarnation against the journal header and names the authority
whose key is needed. A backup restore needs a fresh incarnation even when it
includes all the files.

If the daemon reports a damaged `store.incarnation`, stop the daemon, retain
the old journal offline, delete the damaged `store.incarnation` and follow
the restore steps below with the original key.

To restore, stop the daemon and keep the old root and journal offline. In a
private 0700 state directory, restore the original `instance.key` as a
singly linked 0600 file owned by the daemon user. Run
`ataniqd init --restored --state-dir DIR` once and record the printed
incarnation. It preserves the authority and draws a new incarnation each
time. If a journal is still in place, the command warns and refuses before
changing the incarnation. Move the journal offline first, or explicitly use
`--restored --leave-old-journal` to acknowledge that the old journal will
remain in place and fail its binding check. Both run and scratch
`run --check` report exit 14 for that journal. Retain it
offline for recovery and move it out of the registry directory before
running ordinary `ataniqd init --state-dir DIR` to bind a **new, empty**
registry. Reconcile the old lead history and enroll each project in the new
registry before resuming work. Only the lead registry carries this binding today.

### Sandbox identity

At managed sandbox start, the daemon reads the identity from that state root
and passes the holder only the short tag, from which the holder sets
`QARGI_LEAD` to `<project>-lead-<tag>`; the holder never reads the key. The tag is the first 16 hex
digits of the authority ID. Two independently generated keys have a 1 in
2⁶⁴ chance of sharing that prefix. The full fingerprint identifies the
authority. For project names over 42 bytes, the holder uses the first 25
bytes, a hyphen and the first 16 hex digits of SHA-256 of the full project
name. This keeps the lead name within 64 bytes while distinguishing long
projects with the same prefix. A missing, damaged or unsafe key refuses the
start before launch. The layout role used for directories and locks stays
`<project>-lead`.

When the agent environment declares a coordinator label, the sandbox derives
`QARGI_ACTOR` from the qualified parent. For example, a declaration of
`demo-lead@e7:coord:topic:1` becomes
`demo-lead-<tag>@e7:coord:topic:1`. An ordinary lead has no coordinator
label. The profile digest now includes the lead naming rule. Recompute
existing profile digest pins before the next managed start.

## Enroll a project home

Choose one home instance for the project and its long-lived branches. Its lead
owns coordination and integration. The other instance runs offered work, such
as member runs and suites, and returns results and patches to the home lead.
V1 does not detect two instances claiming the same project. The owner keeps
one home per project by running `--home self` on exactly one instance and
comparing the printed `home_authority` on every participating instance.
If this registry has ever recorded the project as executor-only (a foreign
home), claiming the local authority prints a warning and requires
`--confirm-self`. Check the designation with the other instance first.
The executor-only rule and its `project-home-foreign` refusal are spelled out
in the [scratch lead instructions](../internal/leadcanary/provision/instructions.go).

For a new or existing root, run both commands as the daemon user while the
daemon is stopped. Use the same state directory for both.

```sh
ataniqd init --state-dir DIR
ataniqd project-home --state-dir DIR --home self PROJECT
```

Use the exact registry project name, for example `scratch-alpha` for the
role `scratch-alpha/lead`. On the executor, use
`ataniqd project-home --state-dir DIR --home AUTHORITY PROJECT`, with the
home's full authority ID from `ataniqd init`. The subcommand takes the registry's
exclusive lock. It prints the project and recorded home. Repeating the same
enrollment confirms it. A conflicting enrollment fails.

The journal records the home authority, home generation, assignment revision,
qualified holder (authority and role), and executing instance together. At
enrollment the generation and revision are both 1 and the home holds and
executes its project lead. Assignment writes compare the expected generation
and revision. Define and launch record the assignment version they admitted.
A partial final assignment record is discarded on replay. A committed one
survives a lost acknowledgement.

Both new and migrated projects require explicit enrollment. Define, start and
coordinator admission return `project-home-unset` when the row is missing,
`project-home-foreign` when another authority is home, and `project-unassigned`
when the row has no holder. Scratch clients receive a refused receipt and
canary clients receive the code in the error reply. A mismatched generation
or revision yields `stale-home-generation` or `stale-assignment-revision`.
Existing launch epochs, recovery, and stop operations retain their meanings.
This version supports initial designation. Transferring the home to another
instance requires a future re-homing protocol.

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
