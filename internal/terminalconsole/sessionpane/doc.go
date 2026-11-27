// Package sessionpane is the console's embedded session pane: a terminal
// emulator (the patched x/vt) that a caller feeds with a session's output and
// draws into a region of its own cell buffer.
//
// # Boundary
//
// The pane has no file, process or network access: its own code opens
// nothing, starts no process and reads no environment, and an import guard
// test keeps it that way.
// The bytes it is given are untrusted, so the pane bounds what it keeps of
// them: the screen is at most [MaxCols] by [MaxRows], and scrollback is
// disabled by default. With [Options.RecentOutput] the main screen keeps
// [RecentRowLimit] scrolled rows and paging can hold one such window while
// new rows arrive. The alternate screen keeps none. A frame held for synchronized
// output is one copy of the screen, and the title is at most [MaxTitle]
// bytes. The patched x/vt bounds
// the rest: a cell keeps at most 64 bytes of a grapheme cluster, only a fixed
// table of known modes is recorded, a hyperlink whose URL is over 2 KiB or
// whose parameters are over 256 bytes is dropped, a screen keeps at most 256
// distinct links in its cells (text printed under a further link is drawn
// without it), and the data of an OSC, DCS or APC string is cut at 64 KiB.
// Each of these drops and counts what is over its cap in the emulator's
// counters.
// Nothing the session writes reaches the outer terminal except as cells:
// the pane draws cells, never escape sequences, drops hyperlinks from them
// and draws no cell content longer than [MaxCellBytes]. [Pane.Text] returns
// the text of a part of the screen, up to a limit the caller gives, for the
// caller to copy where it decides.
//
// The pane is push-only. The caller writes output with [Pane.Write], sets the
// size with [Pane.Resize] and reports the end of the session with [Pane.End].
// The pane has no stream of its own and knows nothing of where the bytes come
// from: in the fixture console a replay player feeds it, later a holder will.
// Input going the other way (keys, paste, mouse) leaves through an
// [InputSink] that the caller supplies.
//
// # Replies
//
// A session can query its terminal (device attributes, colours, cursor
// position, modes). The pane answers the query for synchronized output
// (mode 2026), which it follows itself; the emulator answers the rest. The
// answers go to a pipe, which one
// goroutine owned by the pane drains from [New] to [Pane.Close]. The answers
// are discarded unless [Options.Replies] is set, so in the fixture console no
// reply goes anywhere; a holder can later set it to send them to the session.
// Write and Resize never wait for a reply to be consumed by anything but that
// goroutine. The replies of a pane that was restored from a snapshot are made
// as any pane's are: a client that attaches to a holder discards them (see
// [Options.Replies]), because only the holder answers the session. A handler
// set with [Pane.HandleOSC] can answer an OSC string the pane does not know;
// its reply goes to the same pipe, in order.
//
// # State transfer
//
// The holder and the attach client wrap the same patched emulator with this
// package, so that they cannot drift (the lead-hosting design, section 1.3).
// A client that attaches is given a snapshot of the holder's pane, restores
// it into its own pane, and then is fed the same output bytes; a periodic
// digest of both panes proves that they are equal (section 2.3).
//
// [Pane.AppendSnapshot] appends a binary snapshot of the pane's real current
// state, never the frame held for synchronized output, and fails with
// [ErrNotQuiescent] unless the parser is at ground ([Pane.Quiescent]: no
// sequence, string or UTF-8 character is half received; an open grapheme
// cluster does not matter, the snapshot carries it). [Pane.Restore] replaces
// the pane's whole emulator state and its size with a snapshot's. After it,
// writing the bytes the first pane received after the snapshot gives a pane
// equal to the first for ever; the property test of this package (design
// cases H5 and A5) cuts many streams at many points to check it. The snapshot
// carries what later output can depend on: the size, both screens (cells with
// their styles and links, the table of links, the cursor and the saved
// cursor, the scroll region), the modes, the character sets and shifts, the
// pending wrap, the tab stops, the last character, the colours the session
// set, the open grapheme cluster and whether it is capped, the title, and
// mode 2026 as set. It does not carry the pane's options, handlers, counters
// or the end of the session, nor the frame held for synchronized output: a
// restored pane shows its real screen until the session ends the update.
//
// The snapshot is at most [MaxSnapshotBytes] bytes, and the decoder is for
// untrusted input: strict, bounded by the limits of the pane and of the
// emulator, and an error never leaves the pane changed. Restore is refused
// after [Pane.End] or [Pane.Close].
//
// [Pane.Digest] is a SHA-256 of the real state: the size, every cell, the
// cursor, the modes that are set and the title. See its documentation for
// the exact bytes.
//
// [Pane.Counters] counts the caps that were hit, the emulator's and the
// pane's own, and [Pane.ModeSet] lists every mode that is set.
package sessionpane
