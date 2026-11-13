# Runtime and recovery contract

Status: proposed; no implementation or runtime qualification is claimed.
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

During control-plane outage, already dispatched work may continue within its
qualified boundary. Pause new dispatch and wait for new approvals. On return,
reconcile runner, provider, approvals, children and evidence before resuming.
Do not infer completion from a disconnect. Reserve and reconcile child usage
against the parent's enforceable budget; unknown usage is not zero.

## Fault matrix and acceptance scenarios

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
| Authority disconnects during work | Existing bounded work may continue; new dispatch and approvals wait. Reconcile on return. |
| Provider or runner dies during a turn | Retain the turn and children as interrupted or unknown until evidence resolves each; no inferred success. |
| Old epoch remains reachable | Reject its new dispatch; if boundary fencing is unproven, block replacement dispatch. |
| Host reboot | Reconstruct desired state from the database; a new epoch does not prove prior effects absent. |

Additional deterministic checks: submit A then B while busy and assert dispatch
order A, B; submit explicit steering and assert it targets only the specified
turn. With provider concurrency disabled, assert a second turn remains queued;
with it enabled but workspace policy incompatible, assert the same. Cancel a
lead with an unobservable native child and assert the child remains unknown,
not completed. These are specifications, not executed checks.
