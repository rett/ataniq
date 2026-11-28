# Roles and authority split

Status: owner-approved direction. The assignment reducer and ledger implement
the handoff rules below. Managed handoff at runtime remains unqualified.

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

No gate is delegated to a supervisor today. It will act only within gates the
owner delegates, and never on its own project's gates. It does not run another
project's lead-only commands.
It relays no approval: an approval reaches a lead only as the owner's direct
words or as a grant the gate verifies (see [approvals](approvals.md)). Whether a
locally held supervisor is a grantee of such a grant at all is open, and it is
the owner's decision; until then the supervisor holds no grant.

The agent that holds the supervisor role may run on any provider, local or
cloud. A holder that runs on a provider's cloud, or anywhere else outside an
execution boundary Ataniq qualifies on the owner's hosts, holds, sees and passes
no approval token and no grant, and no grant is issued to or held under its
role. Reading and nominating are described here for a cloud holder only; any
other holder outside a qualified boundary is unsupported as a remote
supervisory agent.

A cloud holder is an owner-side facilitator. Each instance assigns it as its own
remote supervisory client, an instance-owned role record, and fences it alone;
no instance depends on another's record of it. Through the scoped port of each
instance it is assigned, it reads status and requests from that instance's
leads, and from a local supervisor of that instance where one is assigned. It
summarises across instances for the owner only: the summary is made outside
every instance and is served to no instance, and a nomination or proposal to an
instance names only that instance's records. It nominates requests that become
owner cards; a nomination names a request that already exists in the instance
and carries no field of what is approved. It makes proposals only and never
edits a plan, a backlog or a dependency manifest; the record's own system
accepts or refuses a proposal. Which of its sessions reads lead transcripts and
which acts is enforced by the instance, by client identity; see
[security](security.md).
See [approvals](approvals.md) for the card and [security](security.md) for the
data that may reach a provider. The supervisor port, its port credential, the
client class and the gateway are not built, so no remote holder is supported
yet.

## Feature coordinator

A feature coordinator (coordinator below) is an ephemeral delegate of a project
lead for exactly one feature, not an assignable role and not the internal
dispatch coordinator of [runtime](runtime.md). It holds no seat (the qargi
lease by which a lead holds the project) and no lead assignment epoch of its
own, ends with its lead and is never a handoff target. Its questions go to its
lead, never to the owner directly. The owner may watch a managed coordinator
and type into it as its single writer, and the lead is notified each time;
this changes none of the coordinator's powers. Nothing it says is an approval
(see [approvals](approvals.md)).

Within its own feature a coordinator may prepare runs, verify, apply and merge
them into the feature and sync the feature. The lead runs the feature's
whole-suite verify; a pass a coordinator reports never counts toward finishing
the feature by itself. It may clean any run of its own feature, including a
stopped predecessor's, and never a run of another feature. Starting or
finishing a feature, push, release, hotfix, the orphan sweep, prune and
adoption of another actor's runs stay with the lead. Approval stays with
the owner, as [approvals](approvals.md) states; the lead relays the
coordinator's questions.

A coordinator declares a label, which attributes its runs and only narrows what
it may do (no finish, push or approval); it never widens anything and is never
identity. Identity comes only from a bound file that Ataniq writes: a managed
coordinator's own, or, for one running inside its lead's session, the lead's,
so the gate sees the lead. A label or lead id given in the environment or on
the command line is never honoured as identity.

A lead quiesces its coordinators before a handoff: they are confirmed stopped,
background verifies are stopped and the open runs are listed for the
successor. A stalled coordinator is confirmed stopped before its runs are
adopted or a new instance starts; two instances are never alive for one
feature.

## Handoff

The ledger freezes admission at the current generation `g`. The inventory must
cover the frozen routes and outstanding work, the handoff note must be verified,
and the successor's account must match the inventory. The reducer then requires
these steps in order.

1. Reserve the successor under the owner's decision for this transfer ID.
2. Drain outstanding work on each route and record its sealed-and-drained receipt.
3. Fence the old holder once every route has a receipt, no work is outstanding
   and the inventory has been revalidated.
4. Commit by transfer ID after revalidating the decision. The ledger assigns
   the successor at generation `g+1`. Admission stays closed until every route
   confirms that it installed the new binding and refuses the stale one, and
   the reducer accepts current enrollment evidence and opens the row.

Before commit, an abort requires proof that the candidate never executed and
that outstanding work is settled. Reopening the old holder also requires that
it was open before the freeze and every route is safe to reopen. An irreversibly
fenced holder stays closed.

After commit, a rollback is a new higher-generation transfer with its own
authorization and ID. It fences the current holder and commits the previous
holder at `g+2`. The original commit stays in the ledger.

If the same issuer later corrects or contradicts an accepted fence or
confirmation receipt, the reducer quarantines the assignment key, even during
a later transfer. Both sides stay closed, including after the owner's one-time
acknowledgement. Recovery requires retiring the doubtful identity and fresh
enrollment in a new store. The reducer refuses
a contradictory receipt from a different issuer, which the adapter must report
to the owner.

A handoff grants no authority. The predecessor's approval tokens, grants and
plan tokens never pass to the successor. Each gate uses its own evidence.
A handoff to a holder outside a qualified boundary, a cloud agent for example,
first revokes every grant held under the role, because no such holder may hold
one. This is a second safeguard: a grant never passes to a successor in any
handoff. The revocation is not undone if the handoff fails. Ataniq owns role
assignment, so it asks for the revocation; the revocation itself lives in
qargi's store. Fencing a remote holder is done by each instance alone, which
stops honouring that holder's epoch; this is not built.
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

## Joint request to Qargi

Qargi is not changed by this document, and this section is a request, not a
decision for Qargi. As understood at the time of writing, and not verified here
against Qargi's current records, a remote supervisory holder needs two things on
Qargi's side, which are the Qargi lead's to agree or decline:

- A supervisor role record with no repository mounted, no repository token and
  no permission to push. It must not reuse the lead record, which carries them.
  Its class is neither write nor read-only, both of which are about a clone, but
  a third class with no clone at all.
- A long-lived session kind. Qargi's cloud sessions are task-scoped, one task
  then collect then close; a supervisor's session lives for days and is not a
  stuck run.

Until Qargi has both, no supervisor is provisioned on a provider's cloud.
