# Ataniq

Ataniq is a planned MCP-first orchestration service for managing projects,
agents, tools, and local or remote sessions through extensible feature packs.

The project is in its foundation stage. The service, CLI, UI, authentication,
and execution adapters are not implemented yet.

## Architecture decisions

- Go for the service and CLI; TypeScript for the management UI.
- MCP as the primary agent-facing interface, with adapters for other protocols.
- OAuth authorization for remote MCP access and mTLS support for trusted
  service and worker connections. Authorization remains project-scoped.
- Versioned feature packs for providers, services, tools, authentication,
  session backends, and project-management integrations.
- tmux-manager as the first session backend, explicitly requiring Linux and
  systemd. The orchestration core must permit other platforms and backends.
- qargi for governed project development and development evidence.
- Project-specific improvement policies, experiments, and outcome history;
  promotion into shared packs is a separate reviewed decision.

See [the architecture brief](docs/architecture.md) for the planned boundaries
and initial milestones.

## Development

Development follows qargi's feature, review, and verification workflow.
See [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md).
The generated bootstrap and doctor targets are placeholders; the toolchain
and test harness must be implemented before feature acceptance.

## License

MIT. Copyright (c) 2026 Everett C. Haimes III.
See [LICENSE](LICENSE).
