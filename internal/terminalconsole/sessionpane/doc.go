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
// them: the screen is at most [MaxCols] by [MaxRows], the main screen keeps
// one line of scrollback and the alternate screen none (the patched x/vt
// gives it none), a frame held for synchronized output is one copy of the
// screen, and the title is at most [MaxTitle] bytes. A cell keeps at most
// 64 bytes of a grapheme cluster, the emulator records only a fixed table of
// known modes, it drops a hyperlink whose URL is over 2 KiB or whose
// parameters are over 256 bytes, and a screen keeps at most 256 distinct
// links in its cells; text printed under a further link is drawn without it
// (the patched x/vt drops and counts the rest of a cluster, ignores and
// counts any other mode number, and counts the links it refused).
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
// goroutine.
package sessionpane
