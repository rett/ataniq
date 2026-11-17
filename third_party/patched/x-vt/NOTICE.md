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
   of a megabyte. A cell now keeps a prefix of a cluster of at most
   `MaxClusterBytes` (64) bytes, cut at a rune boundary: the first rune that
   does not fit caps the cluster, and that rune and every later rune that
   extends it are dropped, so a shorter mark after a longer one that did not
   fit is not kept. The dropped bytes are added to
   `Counters().ClusterBytesDropped`, and the grapheme buffer never grows past
   64 bytes. When the buffer fills, the clusters before the last are drawn and
   the next rune is tested against the last one only, so text without ASCII
   (which fills the buffer with many clusters) is not cut. A cluster that has
   reached the cap, or that has lost a rune, is neither reopened nor grown by
   the next write: the mark is dropped and counted, and the cluster stays the
   open one and capped. (`OpenCluster` reports the cluster's text only, so a
   copy of the screen that continues it does not know that it is capped;
   `State`, patch 11, reports it.) A run of marks past the cap costs one
   segmentation of at most 68 bytes per rune: linear time, no allocation.
   This patch also adds the `Counters` struct and the `Counters` accessor on
   `Emulator` and `SafeEmulator` that the later caps use.
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
   checked before it is split, so a refused link allocates nothing. For the
   same reason an OSC 8 longer than the two caps allow together (the
   parameters, the URL and their separators) closes the open link and is
   counted also when it is malformed, that is, when it does not have three
   fields; the released module ignored such a string. A malformed OSC 8
   within the caps is ignored as before: the open link stays open and
   nothing is counted.
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
   A scanned cell costs a comparison with the cell before it, and a lookup
   (the hash of the link, up to 2.3 KiB) only where its link differs from
   the one before, so a run of cells under one link, which share their copy,
   costs one lookup. The worst case, a screen whose neighbouring cells
   hold different links, still hashes up to 2.3 KiB per scanned cell, so a
   scanned cell costs more with long links than with short ones; the time
   stays linear in the cells printed.
   A slot that frees up (overwrite, erase, scroll, delete, resize) is
   therefore available after at most that many refusals; a clear of the whole
   screen empties the table at once. A recount counts every link in a cell,
   also one that an API call such as `SetCell` put there, which the table
   does not refuse. Scrollback is not counted: it keeps the links of the
   lines it holds, and its size is the caller's (the session pane keeps none
   on either screen).
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
9. `vt: let SetScrollbackSize(0) keep no scrollback` (`scrollback.go`,
   `screen.go`, `emulator.go`, `scrollback_test.go`). `Scrollback.SetMaxLines`
   ignored zero, so the smallest scrollback the main screen could have was
   one line, and a screen with one line of scrollback keeps that line's links
   outside the 256-link table. Zero is now accepted and keeps nothing: the
   lines held are released, and lines that scroll off the top, an erase of
   the screen and a resize add none. A negative number is ignored, as zero
   was, except that a screen without a scrollback buffer still gets one of the
   default size. A size above zero works as before.
10. `vt: keep the scroll margins and the cursors inside the screen`
   (`handlers.go`, `emulator.go`, `bounds_test.go`). A few bytes of output
   could crash the process: `CSI 3;99 r` on a screen of five lines set a
   scroll region that ended at line 99, and the next insert or delete line,
   scroll or reverse index indexed past the buffer; `CSI ? 69 h` and
   `CSI 2;99 s` did the same with the right margin on the next character. A
   resize left the cursor of the screen that is not active, and the saved
   cursors of both, where they were, so a later restore followed by a tab
   stop or a character indexed past a smaller screen. The bottom and right
   margins are now cut to the screen's last line and column, and a region
   that starts below the last line (or beyond the last column) is ignored, as
   an empty one was. A region of one line on the last line, which the tests of
   the released module use, still works. A resize moves the cursor of both
   screens and both saved cursors into the new size. Nothing changes for a
   sequence that did not index past the buffer, except that the cursor and
   the region it leaves are inside the screen.
11. `vt: add State, SetState, Quiescent and ModeSettings` (`state.go`,
   `safe_emulator.go`, `state_test.go`). The session pane hands the screen of
   one emulator to another, which then follows the same output. `State`
   returns a copy of everything of an emulator that later output can depend
   on, and `SetState` replaces the whole state of an emulator, its size
   included, with one, or refuses it and leaves the emulator as it was: the
   sizes, the strings, the links (through the admission of the screen's link
   table, so the cells share one copy of each), the modes, the cursors and the
   open cluster are checked against the caps first. A cell is at most
   `MaxCellWidth` (2) wide: output draws a cluster in one or two cells (a
   placeholder has width 0), and ultraviolet's `Line.Set` rewrites the whole
   span of a wide cell it overwrites without looking at the end of the line,
   so a cell of width 3 or more made a later write panic. `SetState` does not
   check a cell against its neighbours: output makes a wide cell in the last
   column (a resize cuts a line), a cell of width 0 with no wide cell before
   it (a lone combining mark) and a wide cell with no placeholder after it,
   and a snapshot of such a screen must restore; with a width of 2 at most,
   none of them makes `Line.Set` index past the line. The state holds both
   screens (cells, cursor, saved cursor, scroll region, link table), the
   modes with a setting, the character sets and shifts, the pending wrap, the
   tab stops, the last character, the colours the session set, and the open
   cluster with whether it is capped (what `OpenCluster` leaves out). It does
   not hold what the caller gives the emulator (callbacks, handlers, logger,
   default colours), what only the API sets (the indexed colours, the
   scrollback), what is derived (damage, counters, scratch buffers) or the
   parser, which `SetState` returns to its ground state. `Quiescent` reports
   whether the parser is at its ground state, which is when a state may be
   taken: nothing is half received. `ModeSettings` and `RecordedModes` list
   the settings and the table of modes. `SafeEmulator` has locked versions of
   the first three and `ModeSettings`. The test of the pair is that an
   emulator set from the state of another stays equal to it for every write
   that follows; an inventory test lists every field of the emulator, its
   screens and its cursors, with whether the state carries it, so that a field
   added by a later version of the module is decided on.
12. `vt: bound the sequences that loop by a parameter` (`csi_cursor.go`,
   `csi_screen.go`, `limits.go`, `loops_test.go`). A parameter is a number of
   up to two billion, and four sequences looped that many times: `CSI n I`
   and `CSI n Z` (tab forward and backward) went on after the last tab stop,
   `CSI n X` (erase characters) filled past the right edge, and `CSI n b`
   (repeat the last character) printed it n times. A single sequence of
   eight bytes kept the process busy for minutes (a count of 100 million took
   40 seconds), and the pane's lock with it. The two tab loops now stop where
   the cursor cannot move on, and the erase is cut at the right edge: both
   give the result they gave. A repeat is cut to the number of cells of the
   screen and `Counters().RepeatsCut` counts the sequences that were cut; the
   screen then holds what a repeat of exactly that many gives, which is not
   what a terminal that printed two billion characters would show (the
   cursor column and the last line depend on the count).
13. `vt: move the lines of a scroll region as wide as the screen`
   (`screen.go`, `scroll_test.go`). A line feed at the bottom of the screen,
   an index, a reverse index, insert line, delete line, scroll up and scroll
   down copied every cell of the region one at a time, through the buffer's
   own insert and delete: 11 MB and 1.1 ms for each of them on a screen of 500
   by 200 cells, so that 64 KiB of line feeds kept the emulator, and the
   pane's lock, for more than a minute. When the region is as wide as the
   screen (the usual case), `Screen.InsertLine` and `Screen.DeleteLine` now
   rotate the lines of the region and blank the lines that come in, which
   costs the lines and the blanked cells (64 KiB of line feeds take 0.45 s). The
   result is the buffer's: the same cells, the same blank cell (the pen's
   background), the lines saved to the scrollback before, and every line of the
   region marked as touched, as the buffer's own functions do. A region
   narrower than the screen still uses the buffer's functions. A test compares
   the two on screens the property streams make, with every region, cursor,
   count and pen. What stays: a scroll that blanks the whole region
   (scroll by the screen, a count over the lines left) writes every cell of
   it, which takes about a millisecond on the largest screen whatever the
   code, so 64 KiB of those take several seconds.

The patches were not part of v0.1.0 (the version copied); later releases were
not checked when this copy was made.

Removal: drop this directory, the `replace` line and the matching test wiring
once an upstream release of the module contains all the changes, and
require that
release instead.
