# Design contract baseline

Status: proposed contracts and acceptance specifications; no runtime or provider
has been qualified. These documents specify observable behavior for subsequent
implementation, not existing commands or passing tests. The Go core and CLI
and the MIT license are the selected direction. The primary operator client is
a terminal-first Bubble Tea console; a web console with the same information
architecture comes later. Both consoles and the CLI sit on one shared typed
command and event contract, and MCP is the agent-facing integration surface.
Dark is the default theme, and a control that is unavailable or unqualified is
shown with its reason. Native compatibility is optional and requires direct
provider support and evidence for each version.

The [guarded dispatch journal](journal.md) documents the implemented internal
offline Linux journal, its durable attempt guards, retained receipt evidence,
crash/reopen tests and qualification limits.

Two amendments of 2026-11-17 (an owner decision of that date, from a
lead-hosting design not yet in this repository) cover a lead hosted as a
terminal program: its `ready` state in [runtime](runtime.md), and its attach
stream as a terminal data plane in [client protocol](client-protocol.md).
Neither is implemented as specified: nothing sets `ready`, and no typed command
issues an attach ticket. A scratch-only holder and attach client are built, with
tickets from a development stand-in (see
[the lead holder guide](../lead-holder.md)), and screen recognisers for one
version each of two runtimes are built in a library that the holder does not
use (see [the lead injection guide](../lead-injection.md)). All of it is
provisional and unqualified, and none of it is used for a real lead.

The [application command contracts](application-contracts.md) document the
implemented pure typed model, bounded JSON codec and canonical binary bindings,
with authentication and durable admission kept as explicit service contracts.

The [conversation and terminal history design](conversation-history.md) defines
what desk and seat can read from a conversation and how the desk recovers
recent terminal output after a disconnect.

The [seat gate](seat-gate.md) specifies version 1 of the link between the seat
app on the authority host and the daemon: pairing a phone, reading the cards,
a login, and the owner's decisions by passkey. Its handler for pairing and
reads and its assertion verifier are built and used by no command; the
decision path is design only, and phone decisions stay scratch-only until the
owner channel, the sandbox and the desk's enrollment channel are qualified.

Read [runtime](runtime.md), [client protocol](client-protocol.md),
[security](security.md), [roles and authority](roles-and-authority.md),
[approvals](approvals.md), and [work model](work-model.md) together. The
existing [federation contract](../federation.md) supplies the inter-instance
boundary; directional grants and authoritative homes remain independent in each
direction. Where older milestone language differs, this baseline governs the
proposed runtime and client design. Implementation details and interoperability
remain to be validated.

## Authority and identity

Each independent Unix-user instance has one authoritative local database and
authority for its own projects; federation exchanges authorized records, not a
shared database or automatic failover. A project owns policy, work, grants, and
evidence. A project's lead role has a stable identity, separate from the agent
that currently holds it, and may retain a resumable conversation.
Only project leads and the supervisor are persistent agents; see
[roles and authority](roles-and-authority.md) for role assignment, handoff and
which system owns each record. A runtime binding identifies the provider,
credentials, adapter and workspace boundary; an execution epoch identifies one
incarnation of that binding. Neither a process ID nor a transport connection is
a durable identity. A runtime may host multiple conversations only when its
qualified isolation and provider capabilities permit it.

A conversation is a logical history with provider recovery references. A turn
is a provider execution in that history; a command is a durable client intent,
which may queue, target a turn, or request another operation. A bounded child
attempt belongs to a lead and parent work/turn and has its own identity,
limits, status and evidence; it is not a persistent public agent. Task,
product, evaluation, approval and artifact identities are distinct: an attempt
produces a particular product, an evaluator assesses that product against
versioned criteria, the owner, or an owner-rooted grant, decides a particular
operation, and artifacts carry content and provenance. A completed process,
successful tool, positive evaluation, accepted product and approved release are
different facts.

IDs are stable, opaque and scoped by their issuing authority. Records retain
origin and authority alongside external identifiers and revisions. Projections
may be cached, but never become a second writable authority for a record. Each
operation checks the authenticated client, project, resource, action and current
policy; possession of an ID is not permission. The owner enrolls separately
scoped, revocable clients. Only the owner's direct words or an owner-rooted
delegation-chain grant approve an operation, as specified in
[approvals](approvals.md); connection to a conversation grants no approval
authority.

## Evidence and validation

By default retain metadata, durable input intents and delivery evidence. Full
transcripts are opt-in, with explicit access and retention policy. Audit and
evidence distinguish provider reports, Ataniq observations, user decisions,
test results and independent reviews. A requested model or capability is not
an observed one. Unknown state is displayed as unknown, never inferred from a
missing event. Usage reports distinguish provider observations and estimates;
limits are promised only where their enforcement path is qualified.

Acceptance scenarios in the companion documents are executable test
specifications: fixtures must control clocks, identities and injected faults,
and assertions describe required outcomes. They are not claims that tests exist
or have passed. Provider adapters, the runner boundary, database durability,
replay retention, UI integration, qargi release APIs and security qualification
still require implementation and version-specific validation.
