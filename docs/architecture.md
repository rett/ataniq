# Architecture brief

Status: planned direction, pending implementation and interoperability tests.
The [design contract baseline](design/README.md) specifies the selected runtime,
client, security and work-model boundaries and supersedes older assumptions.

## Core and integrations

Ataniq owns job identities, durable state, project-scoped permissions,
idempotency, admission limits, event cursors, and restart reconciliation.
A session, an agent conversation, and a job attempt are distinct identities.
A live terminal session is not evidence that an agent completed its task.

Agents may use MCP tools and resources. The terminal console, the later web
console and the CLI invoke the same typed Ataniq command and event contract and
authorization rules. The terminal-first Bubble Tea console is the primary
operator client; the web console follows with the same information
architecture. Dark is the default theme, and unavailable or unqualified controls
are shown with their reason. Provider APIs use adapters.
MCP transport sessions must not become durable job identities or authorization
credentials.

Packs declare a versioned manifest, typed capabilities, configuration schema,
credential references, permissions, compatibility, and health checks. Local
executables and remote endpoints run across explicit process or network
boundaries. The core must not load arbitrary executable packs in-process.
Provider-specific capabilities use namespaced, validated extensions.
Declared support and independently observed support are recorded separately.

Session hosting for a lead run as a terminal program follows the amendment in
[runtime](design/runtime.md#lifecycle) and the one in the
[client protocol](design/client-protocol.md#terminal-data-plane): a per-lead
holder owns the PTY and a console attaches to it. Only a provisional,
scratch-only holder and attach client exist, except in a build with the
`ataniq_canary` tag, under `--canary`; see
[the lead holder guide](lead-holder.md). Sandbox qualification comes before any
Ataniq-managed lead. A provider's remote control stays an optional launch
profile during the transition, and a Codex lead is an interactive `codex` in its
own session. Codex is the first live provider target, subject to version-specific
capability and security evidence; qargi integration is limited to qualified
public APIs. Unknown or disconnected state must remain explicit.
The [lead sandbox guide](lead-sandbox.md) describes the sandbox profile for a
managed lead as a proposal, unqualified; nothing uses it yet, except in a build
with the `ataniq_canary` tag, under `--canary`. Its opt-in harness is in
[the lead sandbox qualification guide](qualification-lead-sandbox.md).

[Lead injection](lead-injection.md) describes the provisional library that
pastes a message into a terminal-hosted lead and submits it only when its
recognisers read the screen as idle; it is unqualified, and it is a library
that no daemon and no holder uses yet, except that a build with the
`ataniq_canary` tag, under `--canary`, uses it for one message.

[Lead intake](lead-intake.md) describes the exchange mirror and the reviewed,
fast-forward-only intake by which a managed lead's work enters an upstream
branch, with the provisional command `ataniq-intake`. No sandbox is shipped or
qualified with it, and no daemon or holder calls it: the owner runs it by hand,
and in the canary slice too (see [the canary slice guide](canary-slice.md)).
The opt-in harness for a real sandbox template is in
[the lead mirror qualification manual](qualification-lead-mirror.md).

## Roles and authority split

Each project has exactly one assigned lead, and one supervisory lead spans
projects. Ataniq owns processes, sessions, lifecycle, attach, messaging,
scheduling and escalation routing; qargi owns development tasks, the dependency
graph, member dispatch, evidence, review, merge and release gates, and
campaigns. Each record has one authoritative system. See
[roles and authority](design/roles-and-authority.md).

A provisional, unqualified, read-only daemon skeleton, `ataniqd`, and the
registry, lock, unit-manager and lifecycle packages a later managed-lead daemon
will use are in the tree. They start, stop and manage no lead. See
[the lead daemon guide](lead-daemon.md). The daemon, the holder, the sandbox
proposal, the injection library and the intake are built side by side, and
none calls another (the daemon starts no holder, the holder launches no sandbox
and injects nothing, and the intake is run by hand), except in a build with the
`ataniq_canary` tag, under `--canary`. There, one attended session on a
throwaway repository joins the daemon, the holder, the sandbox and the injection
library, and the owner still runs the intake by hand, as
[the canary slice guide](canary-slice.md) describes: it is not a qualification
and not the week-long canary lead, and release builds do not contain it.

## Authentication and authority

Remote MCP access uses OAuth protected-resource discovery and scoped token
validation. User sign-in can use OpenID Connect through an identity provider.
mTLS authenticates selected service or worker connections; certificate-bound
OAuth tokens are an optional policy when supported by both ends.

Every operation checks the principal, project, resource, and requested action.
Tool annotations and pack descriptions are not permission grants. Do not pass
an Ataniq access token through to an upstream provider. Credential references
are resolved under separate, scoped provider credentials.

Only the owner's direct words, or a delegation-chain grant whose signatures
verify back to the owner's root key, approve anything; see
[approvals](design/approvals.md).

Session launch, cancellation, pack installation, project adoption, and release
are separate permissions. Ataniq must preserve qargi's verification, review,
and release authority rather than interpreting terminal output as approval.

## Federation

Instances support permissions-based bidirectional federation for project
coordination, task exchange, and communications. Each direction has separate
grants and each instance retains local admission and execution authority.
The initial deployment uses two hosts with permission-aware queued-task
load balancing, shared-capacity accounting, and safe partition recovery.
See [the federation contract](federation.md) for identity, MCP surfaces,
revocation, recovery, and implementation acceptance criteria.

## First milestones

This list predates the terminal-first direction and does not order the
terminal console or session hosting; the
[design contract baseline](design/README.md) governs where they differ.

1. Establish the Go toolchain, development gates, and canonical pack contracts.
   Qualification, including the sandbox, comes before any managed lead.
2. Test MCP transport, OAuth discovery, token validation, and project isolation
   using fake identity and execution backends.
3. Add durable jobs, idempotent launch, event pagination, cancellation, and
   crash/restart reconciliation. Store explicit completion evidence.
4. Qualify a runner boundary with private-socket tests and precise job targets.
5. Add Codex and qargi integrations as independently reviewed adapters.
6. Implement federation in the staged slices defined by its contract.
7. Add the web console and project-local outcome measurement and experiments.

Persistence and the internal adapter wire protocol remain design decisions.
The Go core and CLI were selected after independent review; the terminal
console comes first and the web console later. No benchmark or SDK
interoperability result is claimed yet.

## References

- [MCP authorization](https://modelcontextprotocol.io/specification/latest/basic/authorization)
- [OAuth security best practice](https://www.rfc-editor.org/rfc/rfc9700.html)
- [OAuth mutual TLS](https://www.rfc-editor.org/rfc/rfc8705.html)
- [Official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)
- [Codex App Server](https://learn.chatgpt.com/docs/app-server)

Copyright (c) 2026 Everett C. Haimes III. Licensed under the MIT License;
see [LICENSE](../LICENSE).
