// Package sessionpane is the console's embedded session pane: a terminal
// emulator (the patched x/vt) that a caller feeds with a session's output and
// draws into a region of its own cell buffer.
//
// # Boundary
//
// The pane has no file, process or network access: its own code opens
// nothing, starts no process and reads no environment, and an import guard
// test keeps it that way.
// The bytes it is given are untrusted, so its size and everything it keeps
// are bounded, and nothing the session writes reaches the outer terminal
// except as cells: the pane draws cells, never escape sequences, drops
// hyperlinks from them and draws no cell content longer than
// [MaxCellBytes].
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
// position, modes). The emulator writes its answers to a pipe, which one
// goroutine owned by the pane drains from [New] to [Pane.Close]. The answers
// are discarded unless [Options.Replies] is set, so in the fixture console no
// reply goes anywhere; a holder can later set it to send them to the session.
// Write and Resize never wait for a reply to be consumed by anything but that
// goroutine.
package sessionpane
