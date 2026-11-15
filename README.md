# Ataniq

Ataniq is a planned orchestration service for managing projects,
agents, tools, and local or remote sessions through extensible feature packs.

## Maturity

The 0.0.5 release is a developer foundation with offline development checks and
internal libraries.

| Area | Current status |
| --- | --- |
| Dispatch | Shipped internal deterministic coordinator exercised with fake boundaries. |
| Journal | Shipped guarded persistent Linux SQLite journal and trusted read-only preflight for existing stores. See the [journal design](docs/design/journal.md). |
| Commands | Shipped pure typed command contracts, strict JSON codecs and canonical binary bindings. See the [application contracts](docs/design/application-contracts.md). |
| Service and clients | Production service, runner, provider conversations, authenticated durable admission, execution adapters, provisioning, and released CLI and console remain pending. CLI and console prototypes are separate unreleased source. |

The design intent is provider and platform neutral. Current qualification
coverage includes Codex-specific mock tests and Linux/amd64 host observations.
See [the Codex qualification guide](docs/qualification-codex.md) and
[the host manual](docs/qualification-host.md) for their scope and limits.

## Architecture decisions

These decisions describe the planned design.

- Go for the service and CLI; TypeScript for the management UI.
- Unified Ataniq clients with a shared command and event contract; MCP remains
  an agent-facing integration and federation surface.
- OAuth authorization for remote MCP access and mTLS support for trusted
  service and worker connections. Authorization remains project-scoped.
- Versioned feature packs for providers, services, tools, authentication,
  session backends, and project-management integrations.
- A proposed Ataniq runner and systemd lifecycle on Linux, subject to sandbox
  qualification.
- qargi for governed project development and development evidence.
- Permissions-based bidirectional federation for project coordination, task
  exchange, communications, and permission-aware task load balancing between
  independent Ataniq instances.
- Project-specific improvement policies, experiments, and outcome history;
  promotion into shared packs is a separate reviewed decision.

See [the design contract baseline](docs/design/README.md) and
[architecture brief](docs/architecture.md) for the planned boundaries.

## Development

Development follows qargi's feature, review, and verification workflow.
See [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md).
Install Git, Make, a POSIX shell and a Go bootstrap driver with toolchain
selection support (Go 1.21 or newer). The driver selects the developer Go
version pinned in `.go-version`; see [Go toolchains](https://go.dev/doc/toolchain).
Review `make -n bootstrap` and `scripts/go-tool.sh` before running `make bootstrap`.
The helper selects the exact pinned toolchain and downloads it and module dependencies
using Go's verified mechanism. It records the resolved executable in ignored local
build state. If the pin changes or that executable disappears, run bootstrap again.
An already-exact Go driver works without this state. `make doctor` and the tests
disable toolchain and dependency acquisition; they do not confine the trusted Go
executable or change the host's Go telemetry policy. `make doctor` checks prerequisites offline; both
`make test-dev` (development tooling) and `make test` (the complete offline suite)
run doctor first.

## License

MIT. Copyright (c) 2026 Everett C. Haimes III.
See [LICENSE](LICENSE).
