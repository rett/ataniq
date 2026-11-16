# Patched github.com/charmbracelet/x/vt

This directory is a copy of the module `github.com/charmbracelet/x/vt` at
version v0.1.0 with two patches applied. The module is MIT licensed; its
upstream `LICENSE` file is kept here unchanged. The root `go.mod` replaces the
module with this directory.

Patches, applied in this order to the files of the module as released:

1. `vt: keep a grapheme cluster open across writes` (`emulator.go`, `utf8.go`,
   `utf8_test.go`). A write that ended inside a grapheme cluster committed it,
   so a ZWJ emoji family sent in two writes drew as separate wide cells. The
   emulator now draws the cluster and keeps it open for the next write.
2. `vt: add OpenCluster` (`safe_emulator.go`, `utf8.go`, `utf8_test.go`).
   Reports the open cluster and its cell so that a copy of the screen can
   continue it.

The patches were not part of v0.1.0 (the version copied); later releases were
not checked when this copy was made.

Removal: drop this directory, the `replace` line and the matching test wiring
once an upstream release of the module contains both fixes, and require that
release instead.
