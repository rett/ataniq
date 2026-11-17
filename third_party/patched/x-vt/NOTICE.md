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
6. `vt: drop hyperlinks over 2 KiB or with parameters over 256 bytes`
   (`osc.go`, `limits.go`, `osc_test.go`). `handleHyperlink` copied the URL
   and parameters of any length into the pen, and every cell printed under the
   link copied them again. A link whose URL is over `MaxLinkURL` (2048) bytes or
   whose parameters are over `MaxLinkParams` (256) bytes is now dropped: the
   pen gets no link, as if the link had been closed, and
   `Counters().LinksOverLength` rises. The length of the whole string is
   checked before it is split, so a refused link allocates nothing.
7. `vt: allow at most 256 distinct live links per screen` (`links.go`,
   `screen.go`, `utf8.go`, `limits.go`, `links_test.go`). Each screen (main
   and alternate) has a table of the links its cells may reference, at most
   `MaxLiveLinks` (256) entries and so about 0.6 MiB. Each OSC 8 makes new
   strings for the pen, so the table keeps one copy of every admitted link
   and gives each cell that copy (`admitLink` returns it): a link sent again
   before every character is still one copy, not one per cell (a 500 x 200
   screen of such cells holds about 22 MiB, its cells, where a copy per cell
   would hold about 240 MiB). Cells that are moved by scroll, insert, delete,
   resize or the change of screen copy the cell value and share the strings.
   Erasing and filling use blank cells that carry no link. A cell that an API
   call such as `SetCell` made keeps the link it was given, which the table
   does not intern. Text
   printed under a link that is not in a full table is drawn without the link
   and `Counters().LinksOverLimit` counts the cell. Design: the cells are
   changed by code this module does not own (the `ultraviolet` buffer, in
   every way a terminal changes cells), so reference counts are not
   maintained at each change. A full table is instead recounted exactly from
   the cells when a link that is not in it arrives, which drops every link no
   cell references, and the recount is rate-limited: after a recount, the
   next one waits for as many refused cells as the screen's cells divided by
   256, so a session that prints a new link on every cell of a full table
   costs at most 256 cells scanned per cell printed, whatever the screen.
   A slot that frees up (overwrite, erase, scroll, delete, resize) is
   therefore available after at most that many refusals; a clear of the whole
   screen empties the table at once. A recount counts every link in a cell,
   also one that an API call such as `SetCell` put there, which the table
   does not refuse. Scrollback is not counted: it keeps the links of the
   lines it holds, and its size is the caller's (the session pane keeps one line on the main screen and none on
   the alternate).
8. `vt: keep at most 64 KiB of an OSC, DCS or APC string` (`emulator.go`,
   `limits.go`, `strings_test.go`; it needs patch 3 of the patched `x/ansi`).
   The parser's data buffer was 4 MiB, so one string could make the handlers
   copy megabytes. It is now `MaxStringBytes` (64 KiB), for OSC, DCS, SOS, PM
   and APC alike. The rest of a longer string is dropped by the parser, the
   handler of the string sees the 64 KiB that fitted, and
   `Counters().StringsTruncated` counts the string, using `Parser.Truncated`.
   A cut can fall inside a UTF-8 character, which a handler gets as invalid
   UTF-8; a 0x9C byte of a character stays string data after the cut, so the
   string ends at its real terminator and none of it reaches the screen.

The patches were not part of v0.1.0 (the version copied); later releases were
not checked when this copy was made.

Removal: drop this directory, the `replace` line and the matching test wiring
once an upstream release of the module contains all the changes, and
require that
release instead.
