# Runtime and recovery contract

Status: proposed; the internal dispatch coordinator described below is
source-only infrastructure. No runtime qualification is claimed.
Ataniq owns durable identity, intent, readiness, bootstrap and reconciliation.
The proposed Linux backend uses a detachable Ataniq runner supervised by a
systemd user transient service, with cgroup v2; systemd provides lifecycle
machinery, not the security qualification in [security](security.md). tmux may
be offered for diagnostics, but is neither the authority nor proof of health.
Other platforms may run clients; execution requires a qualified backend.

## Lifecycle

Persist desired runtime, epoch, stable launch identity and launch intent before
requesting a unit. A lost launch response is reconciled against that exact unit
and runner identity, not retried under a new identity. On startup or reconnect,
subscribe to lifecycle changes and reconcile a snapshot with durable state.
Transient unit state alone is not a durable history and does not survive reboot
as an application recovery record. A runner owns the provider connection and
I/O for its binding and can remain alive across authority or client disconnects.
The authority verifies its identity and epoch before adoption. A new incarnation
gets a new epoch; stale runners cannot accept new commands.

Expose desired, starting, ready, degraded, stopping, stopped and unknown
states with reasons. Unit active, socket open and provider ready are separate
observations. Ready requires the version-qualified provider handshake,
authentication, capability discovery and usable workspace; failed or unproven
checks block dispatch. Report provider exit even if a shell or runner survives.
Connection loss makes readiness unknown until reconciled. A resumed provider
conversation is not proof that an interrupted tool had no external effect.

Bootstrap is a durable per-conversation/version operation: record its intent
and identity before delivery and completion evidence afterward. Reconnect or
restart must not silently insert it again. If the provider cannot prove whether
it was delivered, mark it uncertain and require an operator disposition before
dependent dispatch. A deliberately changed bootstrap is a new version with an
explicit policy, not an automatic replay of the old one.

For a busy serial conversation, durably accept inputs into a visible FIFO queue.
Steering is a distinct explicit command with a target turn and qualified
provider semantics; ordinary queued input is never converted into steering.
Concurrent turns require explicit provider support and compatible workspace,
credential, isolation and budget policies. Independent worker bindings and
concurrent turns are different capabilities. Preserve dispatch ordering for the
serial queue even when eligible parallel work completes out of order.

Each command moves through durable accepted, queued, dispatching and a
provider-evidenced outcome, or uncertain. Record intent before the external
effect and delivery evidence after it. Deduplication of admission does not give
exactly-once provider execution. On ambiguous delivery, freeze dependent work;
show the evidence and allow an authorized operator to mark the outcome based on
evidence or explicitly authorize a risk-aware retry. Record the decision and
new attempt; never silently retry an uncertain effect. Automatic reattachment
or resume is allowed only when evidence proves it safe.

Fencing must be enforced at the execution boundary using the current epoch or
generation, not merely a database lease. If an old runner cannot be fenced or
confirmed stopped, do not dispatch a replacement that could duplicate work.
Cancel targets an exact turn or attempt where supported; otherwise expose the
broader effect and require its authorized operation. Termination accounts for
children and remote work; disconnected or provider-native children may remain
unknown. Preserve their results, limits and usage even after process cleanup.

Dispatch means crossing the actual provider or tool boundary to start an
effect, not merely admitting a turn. Record the exact bounded effects and
limits preauthorized for a dispatch. Check current authority at each boundary
before starting an effect; an admitted turn is not open-ended permission to
start children, loop iterations, retries or queued operations. During an
authority outage, already running bounded tools may finish, but new dispatches,
children, iterations, retries and approval decisions wait for authority.
Qualify this boundary per provider and version: combinations unable to enforce
it are unsupported. Do not claim control of opaque provider internals that
have not been qualified. On return,
reconcile runner, provider, approvals, children and evidence before resuming.
Do not infer completion from a disconnect. Reserve and reconcile child usage
against the parent's enforceable budget; unknown usage is not zero.

## Fault matrix and acceptance scenarios

### Source dispatch coordinator

The normal entrypoint is `dispatch.Coordinator.Dispatch` in
`internal/dispatch`. It binds opaque identities and exact payload bytes to one
attempt, prepares them through the application journal, and calls the start
boundary only for a newly committed preparation. A duplicate returns its
existing receipt; an identity or consumed-grant conflict is rejected. The
journal must atomically consume an exact-target grant with preparation and
retain contradictions, including receipt identity and content. The coordinator
checks returned receipt bindings and states, and copies payloads across port
and caller boundaries. It must distinguish a new durable commit from a
duplicate, rejection, conflict and ambiguous result. An ambiguous preparation
must fence the exact attempt across fault clearing and coordinator recreation
until evidence-based reconciliation; an error alone cannot prove a transaction
was absent. A proved rejection is distinct. Receipt IDs identify evidence,
separately from intent IDs: identical evidence is idempotent, while changed
content under one receipt ID or contradictory outcomes under different IDs
are retained and make duplicate inspection uncertain. Evidence receipt IDs are
scoped to the immutable attempt. A different immutable
intent binding is a conflict and cannot alter the legitimate record. Start
acknowledgement and completion evidence are separate. Qualification campaign
storage is a separate mechanism and is not this journal.

The trusted boundary obtains current authority and verifies the matching epoch,
policy, qualification, authorization, bounded limits and any required
exact-target grant, including its current expiry and revocation, at actual
start, even after the journal consumed that grant. It
serializes that crossing with observed revocation and authority changes. It
returns a start acknowledgement, proved denial or uncertainty promptly, and
cannot retain a callback or start after returning, even on cancellation.
Completion is separate and does not hold an admission lock. Recovered prepared
or dispatching attempts are never automatically replayed; uncertain delivery
or failed outcome storage requires reconciliation and a separately authorized
attempt for a retry. A recovered prepared receipt is unresolved evidence, not
proof that start did not happen. A failed save may leave only that receipt;
the caller must preserve the returned uncertainty and evidence for
reconciliation. Synthetic error receipts are uncertainty projections carrying
the immutable attempt and any available evidence; their absent evidence ID does
not make them resubmittable journal evidence. A save using a cancelled caller
context may fail even after a boundary decision. Persistent recovery must account
for failed outcome saves.
Cancellation-independent saving remains a future design option.
External start cannot be atomic with the journal transaction.
The deterministic fake tests cover duplicate concurrency, target-bound grants,
receipt contradictions, payload ownership, current checks, failures, delayed
dispatch calls and completion of already-started work during outage. The fake
boundary returns after its bounded start decision and retains no callback or
permit; these tests do not establish guarantees for an adapter that violates
that obligation; the coordinator cannot secure a malicious trusted port. Their
evidence does not qualify production policy or any provider.

This package supplies no provider adapter, disk durability, host isolation,
client authentication or support attestation. A future application journal and
provider integration must establish their own durability, fencing and actual
boundary guarantees; an in-process model cannot fence an external provider.
Version 0.0.1 remains unchanged; a proposed 0.0.2 assignment is planning only.

Tests use a fake provider and deterministic fault injection. Each row defines
the required result after restart or reconciliation.

| Fault point | Required outcome |
| --- | --- |
| Before durable launch intent | No unit is authorized or started. |
| Intent saved, launch response lost | Inspect the same unit identity; adopt only a matching epoch, with no second launch. |
| Runner dies before provider readiness | Report failed or unknown readiness; dispatch nothing. |
| Bootstrap intent saved, delivery unknown | Mark uncertain; do not send bootstrap or dependent input again without a recorded disposition. |
| Command accepted, before dispatch | Recover the same queued command in FIFO order. |
| Provider receives command, acknowledgement lost | Mark uncertain; no automatic duplicate dispatch. |
| Authority disconnects during work | Running bounded tools may finish; new boundary crossings and decisions wait. Reconcile on return. |
| Provider or runner dies during a turn | Retain the turn and children as interrupted or unknown until evidence resolves each; no inferred success. |
| Old epoch remains reachable | Reject its new dispatch; if boundary fencing is unproven, block replacement dispatch. |
| Host reboot | Reconstruct desired state from the database; a new epoch does not prove prior effects absent. |

Additional deterministic checks: submit A then B while busy and assert dispatch
order A, B; submit explicit steering and assert it targets only the specified
turn. With provider concurrency disabled, assert a second turn remains queued;
with it enabled but workspace policy incompatible, assert the same. Cancel a
lead with an unobservable native child and assert the child remains unknown,
not completed. Disconnect authority after a turn starts, while a bounded tool
is running and a child, iteration, retry and queued operation are pending:
assert the tool may finish and none of the pending effects starts. Restore
authority and assert each requires a fresh check. A provider unable to enforce
that boundary is unsupported. These are specifications, not executed checks.
