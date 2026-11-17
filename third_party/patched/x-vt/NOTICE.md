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
   an empty one was. The margins are first tested as they were sent: a top
   margin that is not above the bottom margin (`CSI 5;5 r`) is ignored, as it
   was. Then the bottom margin is cut, and a region the cut leaves with one
   line is set, and the cursor goes home: on a screen of five lines `CSI 5;99 r`
   and `CSI 5;6 r` set a scroll region of the last line alone (and, with
   `CSI ? 69 h`, `CSI 10;99 s` on ten columns one of the last column). The
   tests of the released module use such a region (`CSI 2;3 r` on a screen of
   two lines), so it stays. **This differs from xterm**, which cuts the bottom
   margin first and then ignores a region whose bottom is not below its top,
   so it ignores these sequences (reasoned from xterm's rule for DECSTBM, not
   run against xterm); it is also not consistent inside this emulator, which
   ignores `CSI 5;5 r`. No operation can index past the buffer with such a
   region, and a test pins the behaviour, so that a change to it, if xterm's
   rule is wanted (the cut before the test, and the four tests of the released
   module that rely on the present one changed), is a decision. A resize moves the cursor of both
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
   `csi_screen.go`, `screen.go`, `limits.go`, `loops_test.go`). A parameter is
   a number of up to two billion, and four sequences looped that many times:
   `CSI n I` and `CSI n Z` (tab forward and backward) went on after the last
   tab stop, `CSI n X` (erase characters) filled past the right edge, and
   `CSI n b` (repeat the last character) printed it n times. A single sequence
   of eight bytes kept the process busy for minutes (a count of 100 million
   took 40 seconds), and the pane's lock with it. The two tab loops now stop
   where the cursor cannot move on, and the erase is cut at the right edge:
   both give the result they gave.

   A repeat is the character typed n times. A count of up to two lines and two
   characters (twice the width, and two) is still printed one character at a
   time. A larger one is done a line at a time where that gives exactly the
   state the prints would (`repeatInBulk`): the cursor's line is printed to
   its end, every further line is a wrap and a full line (the lines above the
   bottom of the region are filled one by one; the lines that scroll are
   replaced, as many as the region has lines, by lines full of the character,
   and the rest would only scroll those, so they are skipped), and the
   remainder of less than a line is printed; with autowrap off the draws
   after the last column are draws in the last column again and are cut. The
   state is every field of `State` (the cells, the cursor, the pending wrap,
   the open cluster, the link table), and a test compares it with the
   character typed n times on small screens in every state the property
   streams reach, for counts up to eight screens. What is not the same: a
   callback sees fewer moves of the cursor, the counters of the caps
   (`LinksOverLimit`, `ClusterBytesDropped`) count the prints that were done,
   and the count of the first version of this patch, which cut a repeat to the
   cells of the screen and so changed the result for every count over them
   (the cursor column and the last line depend on the count), is gone.
   The bulk needs a printable ASCII character (it is, for a repeat after
   output), no hyperlink on the pen (a refused link counts down the table's
   wait on every print), no scrollback (the lines that scroll off would have to
   be kept), and with autowrap on a cursor inside a scroll region as wide as the
   screen. In any other state a count over two lines and two characters is cut
   to it: the screen then holds what a repeat of exactly that many gives, which
   is not what a terminal that printed two billion characters would show. That
   is the documented difference, and `Counters().RepeatsCut` counts every
   repeat that was not printed one character at a time, bulk or cut. The
   cost of a repeat is a few screens whatever the count: a repeat that fills
   the screen of 500 by 200 cells writes all of its 11 MB of cells, which takes
   about a millisecond, and 64 KiB of the longest repeat (5041 repeats of 13
   bytes) took 4.8 s of CPU on a busy machine (a load of 15 on 8 cores). The
   first version took 17 ms for the first repeat of that size and 0.25 s for
   each one after it with the cursor at the bottom (25 s for 1000 bytes), so
   64 KiB of them would have taken half an hour. A repeat is bounded by the
   time it takes to write every cell it fills, which is a millisecond on the
   largest screen whatever the code: 64 KiB of the longest repeat took 3.2 s
   on the machine of the table of patch 14. Since patch 14 the lines a repeat
   fills are marked and not written, and the same 64 KiB take 0.4 s.
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
   region marked as touched (the buffer's own functions also marked the n
   lines above, or below, the region, which do not change; nothing in this
   repository reads the marks). A region narrower than the screen still uses
   the buffer's functions. A test compares the two on screens the property
   streams make, with every region, cursor, count and pen. What this patch
   left: a scroll that blanks the whole region (scroll by the screen, a count
   over the lines left) wrote every cell of it, about a millisecond on the
   largest screen whatever the code, so that 64 KiB of those took 6 to 7 s;
   the erase of the display took 23 to 33 s and the full reset (RIS, two bytes
   that clear both screens) 54 s, and were not named here. Patch 14 marks the
   lines those sequences blank and does not write them, and patch 15 does the
   same for a region narrower than the screen.
14. `vt: mark the lines a sequence blanks and write them later` (`blank.go`,
   `screen.go`, `handlers.go`, `csi_screen.go`, `csi_cursor.go`, `emulator.go`,
   `links.go`, `state.go`, `erase_test.go`, `timing_test.go`, `scroll_test.go`,
   `loops_test.go`). A cell of the
   buffer is 112 bytes, so a line of 500 cells is 56 KB and a screen of 500 by
   200 cells 11 MB, and writing all of it takes about a millisecond whatever
   the code. Erase in display (all three forms), erase in line, erase
   characters, the full reset (RIS, which clears both screens), the switch of
   the alternate screen and a scroll that blanks the whole region wrote every
   cell, through the buffer's `SetCell` one cell at a time (2 to 3 ms), and
   64 KiB of them kept the emulator, and the pane's lock with it, for 6 to 54
   seconds (the full reset the longest, two bytes that clear two screens).
   A program that clears the screen in a loop is enough. Measured on the
   largest screen (500 by 200, no scrollback, the screen full of text, a
   machine with a load of about 4 on 8 cores), 64 KiB of each sequence,
   before and after; the figures before are those of 2 KiB times 32:

   | sequence | before | after |
   | --- | --- | --- |
   | `ESC c` | 54 s | 0.36 s |
   | `ESC[2J` | 33 s | 0.03 s |
   | `ESC[J` | 29 s | 0.04 s |
   | `ESC[3J` | 23 s | 0.03 s |
   | `ESC[H ESC[2J ESC[3J` (the clear of curses) | 26 s | 0.02 s |
   | `ESC[44m ESC[2J` | 18 s | 0.015 s |
   | the same with the background changing between two clears | 15 s | 0.015 s |
   | a line of text, then `ESC[H ESC[2J` | 10 s | 0.025 s |
   | `ESC[?1049h ESC[?1049l` | 5.9 s | 0.03 s |
   | `ESC[200T` and `ESC[200S` | 7.3 s and 6.8 s | 0.03 s and 0.045 s |
   | `x ESC[2147483646b` (patch 12) | 3.2 s | 0.4 s |
   | line feeds, for comparison | 0.31 s | 0.17 s |

   A line now has a mark: the cell that every one of its cells holds. A mark
   is written (the cells are that cell, which is all the mark says) or stale
   (the cells are not written yet, and the line is that cell all the same).
   Those sequences set the mark of the lines they blank, to the blank cell
   with the pen's background that they used, and write nothing; a line that
   already holds that cell is left as it is, so a loop that clears the screen
   costs the lines that changed since the last clear, and a blank screen
   costs nothing. A scroll of the region (line feed at the bottom, index,
   reverse index, insert line, delete line, scroll up and down) moves the marks
   with the lines, and its new lines are marked; a repeat of the last
   character (patch 12) marks the lines it fills. A stale line is written when
   something reads or changes it, and the lines that are still stale are
   written when `Write` returns: no reader, which may be several under
   `SafeEmulator`'s read lock, ever finds a stale line, and no read changes
   anything. A change of some cells of a line (`SetCell`, insert and delete of
   characters, a fill of part of a line) writes the line and drops its mark;
   a resize and `SetState` start with no marks. What a sequence costs is
   the lines it changes (about 5 us a line to write), not the cells of the
   screen. The result is the same, field by field: the cells, with the pen's background in the blank ones; the cursor; both
   screens; the link tables (an erase of the whole screen empties the table as
   before; the recount of the links skips a line marked blank, which has no
   link); the lines that go to the scrollback (a scrollback that keeps nothing
   is not given the lines, nor read); and the lines marked as touched. The
   test of the patch compares every one of these sequences with the code it
   replaced, kept in the test file, on the screens the property streams make,
   with every region, cursor, count and pen, on both screens and with three
   sizes of scrollback, and checks after every write that what each mark says
   is what its line holds; a second test runs the streams with the marks
   dropped before every sequence and compares the states. One thing a caller
   must not do: write a cell through the pointer `CellAt` returns, which is
   the way to read it; `SetCell` is the way to change it. Nothing in the module
   does, and `State` and `SetState` do not carry the marks (the inventory test
   lists them).
   The timing tests (`timing_test.go`, `scroll_test.go`, `loops_test.go`)
   replace the ceilings of a minute, which the old code passed for the
   sequences that were not the worst: each sequence above and each way of
   scrolling is written to the largest screen in 16 KiB (8 KiB for most
   repeats), and the test fails when that takes more than 20 times as long as
   the same number of bytes of line feeds, measured in the same run. The old
   code took 80 times as long for erase display and 150 times for the full
   reset, and fails them.
15. `vt: erase the region of a scroll that is narrower than the screen`
   (`blank.go`, `screen.go`, `links.go`, `erase_test.go`).
   Insert line, delete line, scroll up and scroll down in a region as wide as
   the screen blank their lines by mark (patch 14), and a count of the lines
   left in the region or more is the erase of the region, which costs the
   lines. A region narrower than the screen (left and right margins, mode 69)
   still moved and filled cells one at a time through the buffer's functions,
   and `ESC[?69h ESC[2;499s` and then `ESC[200S` over and over took 4.3
   seconds per 64 KiB (the same with a count of one line, 20 s). Now:
   - A count of the lines left in the region or more is the erase of the area
     of the region from the cursor's line down, the buffer's fill of it (a
     wide cell cut by an edge of the region is repaired as before, and the
     cells outside the region that the repair writes are written), and the
     lines it covers get a mark that says only that the cells of the region
     are blank (a part of a line: the same erase again does nothing, which
     costs the lines it checks: 64 KiB of such sequences take 0.013 s).
     The lines of the region and the lines n above (or below) them are marked
     as touched, as the buffer's functions did.
   - A count of at most a tenth of the lines left, in a region that leaves at
     most an eighth as many cells of a line outside it as inside it (margins
     that cut off the edges), moves the lines of the region, as patch 13
     does, and puts the cells outside it back where they were; the lines that
     come in keep their own cells for the fill, since the repair of a wide cell
     goes by what the cell was, one cell after another, and can write outside
     the region. A scroll of one line in the margins 2 and 499 of the largest
     screen took 1 ms and takes 50 us (64 KiB of `ESC[S`: 20 s before, 1.0 s
     after). The tests compare it with the buffer's functions in every region,
     wide characters at the edges of the region included.
   - Any other count and region is moved by the buffer's functions, as it was.
     This stays slow: a scroll of more than a tenth of the lines, or margins that cut
     off more than an eighth of the line (100 to 400 of 500 columns), moves up
     to 11 MB for each sequence, which takes 0.4 to 1.7 ms. 64 KiB of `ESC[S`
     with margins 100 and 400 take 9 s; 64 KiB of `ESC[40S` with margins 2
     and 499, 22 s. Nothing but a buffer that moves the lines of a region
     narrower than the screen without moving their cells would bound this.
     The pane feeds the emulator in slices of 4 KiB, so that no draw waits
     for more than a slice of it (about 0.6 s of this).

The patches were not part of v0.1.0 (the version copied); later releases were
not checked when this copy was made.

Removal: drop this directory, the `replace` line and the matching test wiring
once an upstream release of the module contains all the changes, and
require that
release instead.
