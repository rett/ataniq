# ataniq

Instructions for coding agents and contributors. `README.md` describes the project and `CONTRIBUTING.md` its workflow.

## Development environment

The targets below are the specification for setting up a checkout.

- Prerequisites: Git, Make, POSIX shell, and a Go bootstrap driver supporting toolchain selection (Go 1.21 or newer). The selected developer toolchain is pinned in `.go-version`; do not copy the exact pin elsewhere.
- The provisional terminal console uses the Bubble Tea v2 Go module and its transitive module dependencies; it needs no additional executable tool. The bootstrap downloads them, the doctor checks them offline, and their notices are in `third_party/terminal-console`. See [the console guide](docs/terminal-console.md).
- Preview setup with `make -n bootstrap` and review `scripts/go-tool.sh`, then run `make bootstrap` after reviewing it. It resolves the selected toolchain through Go's verified toolchain mechanism, records its executable in ignored build state, and downloads module dependencies, using process-local settings. It is idempotent and needs no root. Repeat it if the pin or resolved executable changes; an already-exact driver works without the record.
- Verify a checkout: `make doctor`. It prints one line per item and exits non-zero when something is missing: a line starting `missing`, `fail` or `error` is a problem, a line starting `expected` is a known failure that is accepted. It checks the tools and the selected toolchain, and checks offline that every package and test loads from the module cache with hashes matching `go.sum` and that cached modules are unchanged since download. It prints the Go diagnostic, which names the failing module when the failure is in a module; a missing module is repaired by `make bootstrap`, a checksum mismatch by restoring `go.sum` or removing the named module from the module cache, and a modified module by removing it from the module cache and running `make bootstrap`. Run it before the tests and after pulling.
- Tests: `make test` runs the offline whole suite; `make test-dev` runs the targeted development-tooling checks. Both test targets run doctor first. Offline disables dependency and toolchain acquisition; it does not confine the trusted Go executable or change host telemetry policy.
- Static qualification artifacts: `make qualification-host-build` uses the same selected offline Go wrapper to build the Linux/amd64 probe and explicit host driver. Targeted checks are `sh scripts/go-tool.sh test ./qualification/host/... ./tests/...` after doctor. Existing systemd/bubblewrap/BPF subjects are explicit coordinator-only qualification inputs, not installed developer prerequisites; ordinary tests never launch services or namespaces. See [the host manual](docs/qualification-host.md).
- Never run the bootstrap on a checkout you have not reviewed; read the preview first.
- When the project needs a new tool, add it to the bootstrap, to the doctor and to this list in the same change.
