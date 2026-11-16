# Ataniq

Ataniq is a planned orchestration service for managing projects,
agents, tools, and local or remote sessions through extensible feature packs.

## Maturity

The 0.0.8 release is a developer foundation with offline development checks and
internal libraries. It adds a finite synthetic controller-handoff model and
independent evidence verifier. Its 32 lawful rows and four mutated-evidence
controls matched the frozen expectations. Five supplemental verifier checks
also passed. These results cover bounded model consistency and detection of the four frozen
mutated-evidence controls only. Real controller authority, exclusive receiving routes and native
attachment remain unqualified.

Earlier releases provide private application contracts, an encrypted cursor
codec with a bounded per-codec epoch reservation allocator, and Linux/amd64
SQLite default-VFS qualification. SQLite qualification requires the strict gate
on a capable host. Production storage lifecycle remains unqualified. Pooling
and authenticated live admission remain pending implementation. See the
[settlement qualification guide](qualification/sqlitesettlement/README.md).

| Area | Current status |
| --- | --- |
| Dispatch | Shipped internal deterministic coordinator exercised with fake boundaries. |
| Journal | Shipped guarded persistent Linux SQLite journal and trusted read-only preflight for existing stores. Production durability remains unqualified. See the [journal design](docs/design/journal.md) and [settlement limits](qualification/sqlitesettlement/README.md). |
| Commands | Shipped public typed command contracts, strict JSON codecs and canonical binary bindings. Private internal administration, resource, status and conflict contracts add bounded canonical arithmetic. Public v1 contracts and vectors are retained. See the [application contracts](docs/design/application-contracts.md) and [private contract source](internal/appcontract/admin.go). |
| Cursors | Internal v2 encrypted cursor codec with strict canonical decoding and a bounded epoch reservation allocator per codec. Tests cover rotation, concurrent reservations, failed-mint consumption and fresh construction without received-epoch adoption. See the [cursor source](internal/appcontract/cursor.go). |
| Controller qualification | Finite synthetic ledger, proof and generation checks with an independent verifier. Broader model behavior and real authority, gate and provider composition remain unqualified. See the [controller qualification scope](qualification/controllerhandoff/README.md). |
| Service and clients | Production service, runner, provider conversations, authenticated durable admission, execution adapters, provisioning, and released CLI and console remain pending. A fixture-only terminal console (`ataniq-console --demo`) shows fictional, read-only data with no live control or provider integration, and is outside this release. The CLI prototype remains on a separate development branch. |

Cursor service composition must provide caller authentication and authorization,
key provisioning and nonzero key/identity validation, one codec per composition,
service error mapping, durable receipt integration and live delivery. These remain
pending. Cryptographic assurance and conformance remain unqualified. Memory, VM
or RNG-state cloning requires fresh-entropy restart before minting.

The design intent is provider and platform neutral. Current provider-specific
coverage consists of Codex mock tests, alongside Linux/amd64 host and SQLite
qualification fixtures. Live Claude and Codex attachment remain unqualified.
See [the Codex qualification guide](docs/qualification-codex.md) and
[the host manual](docs/qualification-host.md) for their scope and limits.

## Architecture decisions

These decisions describe the planned design.

- Go for the service and CLI, with native terminal access prioritized. Later
  browser interface options remain an open design choice.
- Unified Ataniq clients with a shared command and event contract; MCP remains
  an agent-facing integration and federation surface.
- OAuth authorization for remote MCP access and mTLS support for trusted
  service and worker connections. Authorization is planned to be project-scoped.
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
