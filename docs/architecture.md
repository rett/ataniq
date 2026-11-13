# Architecture brief

Status: approved direction, pending implementation and interoperability tests.

## Core and integrations

Ataniq owns job identities, durable state, project-scoped permissions,
idempotency, admission limits, event cursors, and restart reconciliation.
A session, an agent conversation, and a job attempt are distinct identities.
A live tmux session is not evidence that an agent completed its task.

Agents use MCP tools and resources. The UI and CLI invoke the same application
services and authorization rules. Provider-specific APIs live behind adapters.
MCP transport sessions must not become durable job identities or authorization
credentials.

Packs declare a versioned manifest, typed capabilities, configuration schema,
credential references, permissions, compatibility, and health checks. Local
executables and remote endpoints run across explicit process or network
boundaries. The core must not load arbitrary executable packs in-process.
Provider-specific capabilities use namespaced, validated extensions.
Declared support and independently observed support are recorded separately.

The first adapters are tmux-manager for managed execution, qargi for project
development, and Codex App Server for conversation observation. Codex's
`agents` command is an interactive command center; its underlying protocol
provides structured thread listing and status events. Observation covers only
connected servers. Unknown or disconnected state must remain explicit.

## Authentication and authority

Remote MCP access uses OAuth protected-resource discovery and scoped token
validation. User sign-in can use OpenID Connect through an identity provider.
mTLS authenticates selected service or worker connections; certificate-bound
OAuth tokens are an optional policy when supported by both ends.

Every operation checks the principal, project, resource, and requested action.
Tool annotations and pack descriptions are not permission grants. Do not pass
an Ataniq access token through to an upstream provider. Credential references
are resolved under separate, scoped provider credentials.

Session launch, cancellation, pack installation, project adoption, and release
are separate permissions. Ataniq must preserve qargi's verification, review,
and release authority rather than interpreting terminal output as approval.

## First milestones

1. Establish the Go toolchain, development gates, and canonical pack contracts.
2. Test MCP transport, OAuth discovery, token validation, and project isolation
   using fake identity and execution backends.
3. Add durable jobs, idempotent launch, event pagination, cancellation, and
   crash/restart reconciliation. Store explicit completion evidence.
4. Add a tmux-manager adapter with private-socket tests and precise job targets.
5. Add Codex observation and qargi integration as independently reviewed packs.
6. Add the management UI and project-local outcome measurement and experiments.

Persistence and the internal adapter wire protocol remain design decisions.
Go core/CLI and TypeScript UI were selected after independent Fable review;
no benchmark or SDK interoperability result is claimed yet.

## References

- [MCP authorization](https://modelcontextprotocol.io/specification/latest/basic/authorization)
- [OAuth security best practice](https://www.rfc-editor.org/rfc/rfc9700.html)
- [OAuth mutual TLS](https://www.rfc-editor.org/rfc/rfc8705.html)
- [Official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)
- [Codex App Server](https://learn.chatgpt.com/docs/app-server)

Copyright (c) 2026 Everett C. Haimes III. Licensed under the MIT License;
see [LICENSE](../LICENSE).
