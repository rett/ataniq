# Terminal console prototype

The terminal-first Bubble Tea console is the planned primary operator client; a
web console with the same information architecture comes later. Both sit on the
shared typed command and event contract. Today the console is a provisional,
read-only Bubble Tea fixture viewer. It has no production backend and does not
send instructions, steer, approve, or interrupt leads. The persistent leads,
subagents, tasks, and conversations shown in demo mode are fictional. No
provider or durable command and receipt integration is implemented, and the
command opens no network listener. Without `--demo` the command reports that the
backend is unavailable.

In a local terminal, run `make doctor`, then build with the selected offline
toolchain and start the explicit fixture:

```sh
mkdir -p build
sh scripts/go-tool.sh build -o build/ataniq-console ./cmd/ataniq-console
./build/ataniq-console --demo
```

Dark appearance is the default; use `--theme light` or `--theme dark` explicitly.
Use arrows or j/k to select, Enter to open, t for project tasks, Escape to go
back, ? for help, and q or Ctrl-C to quit. In longer views arrows scroll.
The s, a, and i keys explain why live controls are unavailable.
The demo identity takes priority at height one. At height two the selected
project or lead takes the remaining line; at greater heights a control notice
also has a reserved line. Narrow widths truncate each line. Display text is
limited to 4096 inspected bytes and 512 emitted bytes per field.

## Qualification

Normal quit was checked in a local Linux pseudo-terminal with the dark and
light themes. Use over SSH, terminal restoration after a startup error, and any
live control remain unqualified.

## Dependencies

The console uses the Go module `charm.land/bubbletea/v2` v2.1.1 and its
transitive module dependencies. `make bootstrap` downloads them and
`make doctor` checks offline that they load for the host platform from the
module cache with hashes matching `go.sum` and are unchanged since download.
If doctor reports a modified module, or a checksum mismatch that restoring
`go.sum` does not fix, run `chmod -R u+w` on the named module directory in the
module cache, remove it and run `make bootstrap`.

Their [upstream license notices](../third_party/terminal-console/README.md) are kept
with the source.
