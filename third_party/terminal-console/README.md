# Terminal console dependency notices

These files preserve upstream license notices for the Bubble Tea terminal
toolkit used by the terminal console and for its module dependencies. The
directories map to these module versions:

- `bubbletea`: charm.land/bubbletea/v2 v2.1.1
- `colorprofile`: github.com/charmbracelet/colorprofile v0.4.3
- `ultraviolet`: github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4
- `x-ansi`: github.com/charmbracelet/x/ansi v0.11.9 (the build uses the
  patched copy in `third_party/patched/x-ansi`)
- `x-exp-ordered`: github.com/charmbracelet/x/exp/ordered v0.1.0, needed by
  github.com/charmbracelet/x/vt
- `x-term`: github.com/charmbracelet/x/term v0.2.2
- `x-termios`: github.com/charmbracelet/x/termios v0.1.1
- `x-windows`: github.com/charmbracelet/x/windows v0.2.2
- `displaywidth`: github.com/clipperhouse/displaywidth v0.11.0
- `uax29`: github.com/clipperhouse/uax29/v2 v2.7.0
- `go-colorful`: github.com/lucasb-eyer/go-colorful v1.4.1
- `go-runewidth`: github.com/mattn/go-runewidth v0.0.30
- `cancelreader`: github.com/muesli/cancelreader v0.2.2
- `uniseg`: github.com/rivo/uniseg v0.4.7
- `terminfo`: github.com/xo/terminfo v1.0.0
- `x-sync`: golang.org/x/sync v0.24.0

The console's terminal emulator, github.com/charmbracelet/x/vt v0.1.0, is
carried in patched form in `third_party/patched/x-vt`, with its own notice.

The console also links golang.org/x/sys v0.49.0, whose notices are already
preserved in `third_party/modernc-sqlite/x-sys`.

Upstream notice files are verbatim and keep their upstream names.
