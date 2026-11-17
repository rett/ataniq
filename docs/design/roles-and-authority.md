# Roles and authority split

Status: proposed, owner-approved direction; none of it is implemented or
qualified. Role assignment, handoff, the supervisor, the coordinator and the
lead registry exist only as this specification. Their contracts are still to be designed and
reviewed before any runtime work.

## Lead and supervisor roles

Each project has exactly one assigned lead. A single supervisory lead
(the supervisor) works across projects. Both are assignable roles. A role is
separate from agent identity, provider, model and effort: the owner can assign
a role to a different or new agent, with the model and effort the owner
selects, and the role's records stay with the role. A launch profile
records provider, model, effort, permission mode and access mode for the agent
that holds a role; a requested value is not an observed one.

Until a dedicated supervisor exists, a single agent may hold the supervisor role
in addition to a project lead role. That interim arrangement is not a
qualified separation. It carries no approval authority: no grant is honoured
yet, so every approval is the owner's direct words.

The supervisor acts only within gates the owner has delegated, and never on
its own project's gates. It does not run another project's lead-only commands;
it can send that project's lead a grant-backed approval, which the gate
verifies (see [approvals](approvals.md)).

## Coordinator

A coordinator is an ephemeral delegate of a project lead for exactly one
feature, not an assignable role. It holds no seat (qargi's record that a lead
holds the project) and no epoch, ends with its lead, is never a handoff target
and never talks to the owner except through its lead. Nothing it says is an
approval (see [approvals](approvals.md)).

Within its own feature a coordinator may prepare runs, verify, apply and merge
them into the feature, sync the feature and run the feature's whole-suite
verify. Under a seat that verify is advisory: only a pass the host side ran
itself counts toward finishing the feature. It may clean any run of its own
feature, including a stopped predecessor's, and never a run of another feature.
Finishing a feature, push, release, prune and every approval stay with the lead
and the owner.

Its identity is a declared label. A label only narrows what the coordinator
may do and never widens it. Real identity comes only from the bound file that
Ataniq writes for the unit; a label given in the environment or on the command
line is never honoured as identity.

A lead quiesces its coordinators before a handoff: they are confirmed stopped,
and the open runs are listed for the successor. A stalled coordinator is
confirmed stopped before its runs are adopted or a new instance starts; two
instances are never alive for one feature.

## Handoff

A role changes hands only by a verified handoff, in this order:

1. Reconcile state: the outgoing holder's work, records and uncertain outcomes.
2. Establish exclusive ownership for the successor.
3. Fence the old holder, so it can no longer act under the role.

If any step fails, the outgoing holder keeps the role; an outcome that cannot
be resolved goes to the owner. Who verifies each step, and how a failure after
exclusive ownership has passed is undone, are settled in the handoff contract,
which is designed before any managed lead and is not decided here. A handoff
grants no authority: the successor receives the role, not the predecessor's
approvals, grants or plan tokens, and each gate is decided on its own evidence.
Fencing follows the execution-boundary rule in [runtime](runtime.md): without
confirmation that the old holder is fenced, no replacement acts.

## Authority split

Each record has one authoritative system. Ataniq and qargi each own a set, and
the other displays it without becoming a second writable authority.

| Record or concern | Authoritative system |
| --- | --- |
| Processes, sessions, lifecycle, attach | Ataniq |
| Role assignment (who holds a lead or supervisor role) | Ataniq; the qargi lease follows it |
| Messaging, scheduling, heartbeats, escalation routing | Ataniq |
| Lead registry and launch profile | Ataniq |
| Development tasks, the dependency graph, member dispatch | qargi |
| Evidence, review, merge and release gates, campaigns | qargi |
| Delegation grants, their revocation list and use audit | qargi (one store) |

Ataniq displays qargi's records read-only and routes escalations; it does not
decide a qargi gate. Until Ataniq manages leads, an earlier session manager
remains authoritative for process records, and role assignment is recorded by
the owner.
