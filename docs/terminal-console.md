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

`github.com/charmbracelet/x/vt` v0.1.0 is the terminal emulator the console's
session pane is built on. It and `github.com/charmbracelet/x/ansi` v0.11.9,
which Bubble Tea and ultraviolet also use, are carried in this repository as
patched copies in `third_party/patched/x-vt` and `third_party/patched/x-ansi`,
and `go.mod` replaces both modules with those directories. The patches keep a
grapheme cluster (a ZWJ emoji family, for example) whole when it arrives in two
writes, keep a UTF-8 character such as U+2733 inside a window title or
other string sequence from ending it early, and keep no scrollback for the
alternate screen, so a full-screen program that scrolls a lot does not grow
the emulator's memory without bound. Each directory has a `NOTICE.md` with the
upstream version, the patches and the condition for removing it: drop the copy
and its `replace` line once an upstream release contains the fixes.

`go mod verify`, and so `make doctor`, does not cover directory replacements.
Git protects the copies instead: they are reviewed source, and changing them is
a change like any other. Their own tests run with `make test` (or
`make test-patched`), from the main module so that they use its dependency set;
the packages run are x/vt, x/ansi and its `parser`, `kitty` and `iterm2`
subpackages, and the x/ansi `sixel` package is left out because it needs a
module that is not cached.

The console's input tests (keyboard, paste and mouse) run with the patched
x/ansi linked in. On Unix, ultraviolet scans input itself, so these tests check
that the console's input still decodes with the patched module; they do not
exercise the patch. A known limit remains: ultraviolet's own input decoder
still ends an OSC reply at a 0x9C byte, so an input reply that contains a
character such as U+2733 would be cut there, and the x/ansi patches do not
change that. The console never requests replies that could contain one.

The modules'
[upstream license notices](../third_party/terminal-console/README.md) are kept
with the source; the notices of the patched copies are their own `LICENSE` and
`NOTICE.md` files.
