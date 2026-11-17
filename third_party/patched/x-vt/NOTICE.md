# Patched github.com/charmbracelet/x/vt

This directory is a copy of the module `github.com/charmbracelet/x/vt` at
version v0.1.0 with patches applied. The module is MIT licensed; its
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
3. `vt: keep no scrollback for the alternate screen` (`emulator.go`,
   `scrollback_test.go`). The alternate screen had the default scrollback of
   10000 lines, which nothing could read or limit (`SetScrollbackSize`
   changes the main screen only), so a full-screen program that scrolled a
   lot grew the emulator by hundreds of megabytes. The alternate screen now
   keeps no scrollback, as in real terminals; `Scrollback` and
   `ScrollbackLen` still report the main screen's scrollback.
4. `vt: keep at most 64 bytes of a grapheme cluster` (`limits.go`, `utf8.go`,
   `emulator.go`, `safe_emulator.go`, `utf8_test.go`). The patched code kept a
   cluster whole however long, so a megabyte of combining marks made one cell
   of a megabyte. A cell now keeps the first `MaxClusterBytes` (64) bytes of a
   cluster, cut at a rune boundary; the rest is dropped and added to
   `Counters().ClusterBytesDropped`, and the grapheme buffer never grows past
   64 bytes. When the buffer fills, the clusters before the last are drawn and
   the next rune is tested against the last one only, so text without ASCII
   (which fills the buffer with many clusters) is not cut. A cluster that has
   reached the cap is neither reopened nor grown by the next write: the mark
   is dropped and counted, and the cluster stays the open one. A run of marks
   past the cap costs one segmentation of at most 68 bytes per rune: linear
   time, no allocation. This patch also adds the `Counters` struct and the
   `Counters` accessor on `Emulator` and `SafeEmulator` that the later caps use.
5. `vt: record only a fixed table of modes` (`mode.go`, `csi_mode.go`,
   `limits.go`, `mode_test.go`). The emulator kept every mode number a session
   set in a map, so a session could grow it without bound (100 000 distinct
   numbers cost megabytes). `recordedModes` in `mode.go` is now the one table
   of recorded modes: every mode `x/ansi` names, plus DEC 12 and 1007. Setting
   or resetting any other mode is ignored, adds to `Counters().ModesIgnored`
   and calls no callback; DECRQM for it answers "not recognised", as for a
   mode never set. `EnableMode` and `DisableMode` fire for the table's modes
   exactly as before.

The patches were not part of v0.1.0 (the version copied); later releases were
not checked when this copy was made.

Removal: drop this directory, the `replace` line and the matching test wiring
once an upstream release of the module contains all the changes, and
require that
release instead.
