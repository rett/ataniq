# Work, relationships and authority

Status: proposed model and conformance requirements, not a claim that every
methodology or qargi action is supported. The console presents a linked graph,
activity timeline, task boards and evidence drill-down over authorized records.

## Typed graph

Nodes include project, lead, bounded attempt, conversation, turn, task, feature,
campaign, backlog item, product, evaluation, approval, artifact and grant.
Edges have identity, type, source, target, authority, revision and provenance.
Types include parent/child, depends-on, assigned-to, requested-by, request-for,
admitted-as, fulfilled-by, produces, evaluates, approves and returned-evidence.
Dependency is not delegation, and a descriptive related-project edge is not a
grant. Preserve historical edges and attempt identities when workers exit.
Cycles in dependency graphs are rejected or explicitly reported; they must not
silently create runnable work. Views distinguish observed, reported and
inferred relationships and avoid exposing unauthorized nodes through edges.

## Operations views

The full operations console must provide equivalent authorized graph and table
views, with navigation between related records. Its inventory includes:

- An overview of project and runtime health, including unknown and stale state.
- Lead workspaces with their conversations, queued work and bounded workers.
- Campaign and backlog boards with dependencies, ownership and status.
- Attempt timelines with decisions, delivery, failures and uncertainty.
- Product and evidence lineage through attempts, evaluations and artifacts.
- An inbox of decisions available to the designated approver or role.
- Provider, model and qualified capabilities, observed or estimated usage, and
  budget limits with their actual enforcement status.

Each view identifies the source authority and revision, observation time or
watermark, and freshness or unknown state. Projections must not present stale
data as current. Apply current authorization to nodes, edges, counts and
navigation; hidden nodes must not leak through placeholders or aggregates.
Permission changes follow the projection reset rules in
[client protocol](client-protocol.md).

An immutable product identifies its exact content or revision and producing
attempt. Evaluation identifies product, criteria revision, evaluator and
version, result and evidence; a new iteration creates a new attempt and product.
Acceptance and release decisions belong to their designated authority, not to
an agent's completion text. Retain failure, uncertainty and rejected evidence
alongside success, subject to project retention and sharing policy.

## Profiles and adapters

A versioned methodology profile declares eligible types, authority owners,
transitions, roles, scheduling owner, provider requirements, approvals,
context, retry and evaluation rules, limits, evidence and conformance fixtures.
Profiles cover lead/team delegation and review, bounded implementation and
evaluation loops, dependency graphs and campaign/feature/backlog workflows.
Support is stated per profile and version; no universal framework compatibility
is implied. Unsupported profiles can be shown read-only when authorized, but
cannot execute. A loop has finite iteration, time and resource bounds, an
explicit evaluator and criteria, and a terminal accepted, rejected, exhausted
or uncertain state. Fresh worker context comes from a bounded evidence packet;
worker output is untrusted data, not authority to change policy.

Each record has one declared authoritative system. Adapters map its public,
versioned actions and evidence into the common graph without creating a second
writable ledger. Changes use source revisions and surface conflicts. For
qargi-owned work, qargi retains its governance and acceptance authority; Ataniq
must not independently schedule the same attempt. A qargi adapter enables an
action only when a released API and version explicitly support it and its
behavior has been checked. Internal files, run directories and helper scripts
are not assumed public APIs. Show unsupported actions explicitly; no specific
released qargi version or action is qualified by this design.

## Cross-project work and federation

A directed, versioned grant binds source project and permitted leads, target
project, request kinds, data and evidence scope, resource and budget bounds,
expiry and revocation. A request records source lead and work, requirements,
rationale, dependencies, requested acceptance and authorized inputs. Receiving
a request does not authorize execution. The target project owns prioritization,
implementation and acceptance; its lead may accept and schedule within the
grant, local scope and enforceable budget, and escalates exceptions to the
designated authority. Acceptance links the request to target work and records
policy and revisions. Changes to material terms require renewed admission.

Status, dependencies and returned evidence remain linked with source and target
provenance. Share only permitted data; a digest or metadata does not grant read
access to the underlying artifact. Request permission grants neither repository
write nor merge, release or approval authority. Grants are directional and
non-transitive: A to B and B to C do not authorize A to C. Any separate B to C
request requires its own grant and permitted data, preserving origin and
restricting onward disclosure to the intersection of applicable policies.
Across hosts, each local authority independently admits work under the
[federation contract](../federation.md). The initial cross-host federation
refuses onward delegation, including a further request for federated work;
separate local relationships do not override that restriction. Remote success is evidence, not local
verification or release approval. Revocation stops new admission and disclosure;
already accepted work follows its recorded disposition and remote termination
is not assumed during a partition.

Usage records distinguish provider reports, estimates and unknowns. Budgets
declare owner, reservation, charges and enforcement scope; capacity and usage
are not automatically transferable across projects. Where limits cannot be
enforced during execution, label them accordingly and refuse work that requires
a hard limit. Limit and reconcile child work against the lead's allocation.

## Acceptance scenarios

With deterministic fake authorities and a bounded profile:

1. Produce two iterations and evaluate the first product. Assert the result
   remains bound to its exact digest and criteria; it cannot accept the second.
2. Exhaust the iteration limit without acceptance. Assert terminal exhausted,
   no further dispatch, and retained evidence for each attempt.
3. Configure A to request from B but no reverse grant. A's in-scope request
   may be admitted by B; B's reverse request and A to C are denied. A related
   edge alone authorizes neither request.
4. Submit an out-of-budget request to B. Assert escalation, no scheduling and
   no budget charge; an in-scope acceptance records B's policy and work link.
5. Revoke sharing while work is running. Assert new disclosures stop, the
   recorded disposition governs the work, and disconnected termination is
   unknown until confirmed.
6. Present a qargi release lacking a requested public action. Assert the action
   is unsupported and no internal helper is invoked. Conflicting source
   revisions produce a conflict rather than a silent overwrite.
7. Under explicitly approved local A to B and B to C relationships, submit a
   separate B to C request: assert its own grant, original provenance and
   intersected disclosure are recorded. Repeat for work received through
   initial cross-host federation: assert onward delegation is refused.
8. Populate each operations view with authorized and hidden records and a stale
   source revision. Assert required inventory is navigable, freshness is
   visible, and hidden nodes are absent from edges, counts and navigation.

These scenarios are test specifications and have not been executed. Profile
transitions, adapter versions, limits and external APIs require validation.
