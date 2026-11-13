# Ataniq

Ataniq is a planned orchestration service for managing projects,
agents, tools, and local or remote sessions through extensible feature packs.

The project is in its foundation stage. The service, CLI, UI, authentication,
and execution adapters are not implemented yet.

## Architecture decisions

- Go for the service and CLI; TypeScript for the management UI.
- Unified Ataniq clients with a shared command and event contract; MCP remains
  an agent-facing integration and federation surface.
- OAuth authorization for remote MCP access and mTLS support for trusted
  service and worker connections. Authorization remains project-scoped.
- Versioned feature packs for providers, services, tools, authentication,
  session backends, and project-management integrations.
- A proposed Ataniq runner and systemd lifecycle on Linux, subject to sandbox
  qualification; tmux is optional diagnostics, not the session authority.
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
using Go's verified mechanism. `make doctor` checks prerequisites offline; both
`make test-dev` (development tooling) and `make test` (the complete offline suite)
run doctor first.

## License

MIT. Copyright (c) 2026 Everett C. Haimes III.
See [LICENSE](LICENSE).
