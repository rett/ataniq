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

The proposed execution backend is a native per-lead holder, a systemd unit
that is also the sandbox parent, contingent on security qualification and
provisional pending an attended emulator spike; tmux is not a planned backend.
See [session hosting](design/runtime.md#session-hosting-provisional).
Sandbox qualification comes before any Ataniq-managed lead. A provider's remote
control stays an optional launch profile during the transition, and a Codex
lead is an interactive `codex` in its own session.
Codex is the first live provider target, subject to version-specific capability
and security evidence; qargi integration is limited to qualified public APIs.
Unknown or disconnected state must remain explicit.

## Roles and authority split

Each project has exactly one assigned lead, and one supervisory lead spans
projects. Ataniq owns processes, sessions, lifecycle, attach, messaging,
scheduling and escalation routing; qargi owns development tasks, the dependency
graph, member dispatch, evidence, review, merge and release gates, and
campaigns. Each record has one authoritative system. See
[roles and authority](design/roles-and-authority.md).

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
