# Patched github.com/charmbracelet/x/ansi

This directory is a copy of the module `github.com/charmbracelet/x/ansi` at
version v0.11.9 with three patches applied. The module is MIT licensed; its
upstream `LICENSE` file is kept here unchanged. The root `go.mod` replaces the
module with this directory, so Bubble Tea, ultraviolet and the patched
`x/vt` all use it.

The upstream `sixel/.gitattributes` file, which names Git LFS filters for
fixtures that are not part of this copy, is not kept.

`fixtures/UTF-8-demo.txt` and `fixtures/demo.vte` are test inputs that the
upstream module ships and its parser tests read (`parser_test.go`). They are
kept unchanged and are not linked into any binary. They are not Charmbracelet's
work: the header of `UTF-8-demo.txt` names its author, Markus Kuhn, and
`demo.vte` is a terminal test stream. The module's MIT licence is kept as
upstream gave it, and this notice makes no licence claim for these two files.

Patches, applied in this order to the files of the module as released:

1. `ansi: do not end string data on a 0x9C byte inside a UTF-8 character`
   (`parser.go`, `parser/transition_table.go`, `parser_osc_test.go` and new
   tests). The parser ended OSC, DCS, SOS, PM and APC strings on any 0x9C
   byte, which is also a continuation byte of characters such as U+2733, so a
   window title containing one was cut and its tail was printed.
2. `ansi: do not end decoded string data on a 0x9C byte inside a UTF-8
   character` (`parser_decode.go` and new tests). The same fix for
   `DecodeSequence`.
3. `ansi: count the string data a full data buffer drops` (`parser.go` and
   `parser_truncate_test.go`). The parser already stopped keeping bytes of an
   OSC, DCS, SOS, PM or APC string when its data buffer was full, and dropped
   the rest without a trace. `Parser.Truncated` now returns the number of
   strings that lost data, counted once per string when its first byte is
   dropped; the patched `x/vt` reads it to count the strings it truncates.
   Nothing else changes: the data handlers see is the same.

The patches were not part of v0.11.9 (the version copied); later releases were
not checked when this copy was made.

Removal: drop this directory, the `replace` line and the matching test wiring
once an upstream release of the module contains all the changes, and require that
release instead.
