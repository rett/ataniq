// Package leadattach is the console's attach client: a
// [terminalconsole.LiveSource] that shows a lead's session, which a holder
// keeps, by speaking the attach protocol of [wire] over the holder's attach
// socket (the lead-hosting design, sections 2.2 to 2.5). The cockpit package
// may not import net or os; this package does, and gives the cockpit only the
// types it names.
//
// # Use
//
//	c := leadattach.New(leadattach.Config{
//		Socket:  path,                          // the holder's attach.sock
//		Mode:    leadattach.ModeWriter,         // or ModeViewer
//		Name:    "console",                     // shown when the writer changes
//		Tickets: leadattach.FileTickets(file),  // a fresh single-use ticket, or ErrNoTicket
//	})
//	// hand c to the cockpit as its Options.Live; the cockpit calls Start.
//
// # States
//
// Start never blocks. A goroutine asks Config.Tickets for a ticket, at once
// and then every [TicketPoll] (500 ms), and shows "waiting for a ticket" in
// the status; with a ticket it dials (connecting), sends the Hello and reads
// the Welcome. The state is attached only once the first snapshot of the
// connection is applied: until then it is connecting, and input is dropped and
// counted, because the pane still shows another screen and the modes are not
// the holder's yet. A refusal before the
// Welcome becomes the reason of the disconnect: refused-ticket and refused-busy
// send the client back to waiting for a ticket, refused-version (and a Welcome
// of another protocol version) stops it. A lost connection goes back to
// waiting for a ticket, and so does one the holder broke the protocol on, after
// a wait that doubles with each such connection (up to 30 s). The Exit frame
// ends the pane and the client: disconnected, "session ended", no reattach.
// So does a State frame whose stream-ended fact is yes when the connection is
// lost before the Exit.
//
// A check mismatch or a gap in the output positions makes the client close the
// connection, with a Detach, and attach again with a new ticket: the state is
// resyncing until the new snapshot is applied. The mismatch and the resync are
// counted. The first resync is followed at once; from the second the wait
// doubles (up to 30 s) and the reason says so, so that a holder whose checks
// never match is not asked for a snapshot in a tight loop. The count goes back
// to zero only when a Check past the snapshot's position matched on a
// connection: the Check the holder sends right after each snapshot matches the
// screen just restored and shows nothing about following its output. A resync Notice from the
// holder, which drops output this client could not take and sends a new
// snapshot on the same connection, also makes the state resyncing until that
// snapshot is applied.
//
// # What the client feeds
//
// The frames are applied in order, on one goroutine, to the pane that the
// cockpit made and gave to Start: Snapshot to Restore, Output to Write (its
// start position must be the position the client has reached), Size to
// Resize, Exit to End. The mode set of the holder's Modes frame is kept in
// full for the key encoding, and the mouse, paste and focus part of it is
// LiveStatus.Modes: the cockpit decides what to ask of the outer terminal from
// that and not from the pane. Until a Modes frame comes on a connection, the
// modes of its first snapshot stand in. State fills the end facts, the idle
// word and the title, and ends the pane only when it says the stream ended.
// The Role frame only updates the role; the holder's Notice is the one notice
// of a writer change. The Notice with the reason input-broken (the agent's
// input is broken for good) is shown once, in a text of this client's own.
//
// The pane's Options.Replies must stay nil in live mode: the holder alone
// answers the session's queries, and a replica's answers would reach the
// session as typed input. The cockpit's startLive makes the pane without a
// Replies function (a test here keeps it so), and nothing in this package
// takes anything out of the pane to send.
//
// The status counters are LiveStatus.CheckMismatches (digest mismatches and
// position gaps), Resyncs (this client's reattachments and snapshots the
// holder pushed on its own) and RefusedInput (input dropped because the client
// is not attached as the writer, and viewer-input-refused notices from the
// holder). LiveStatus.Counters lists the client's own, all named client-*
// (snapshots, position-gaps, checks-skipped for a Check at another position,
// input-dropped for a full queue and for input queued when a connection was
// given up, keys-unencodable, pastes-refused, notices-dropped), then the holder's from its Exit in name order. The
// holder's State frame carries no counters.
//
// # Input
//
// The sink that Start returns sends input only while the state is attached and
// the client both wants to write (Config.Mode) and was granted the writer
// role; otherwise the input is dropped and counted. A key is encoded for the
// modes in the last Modes frame ([EncodeKey]), a mouse event likewise
// ([EncodeMouse]), and both go in Input frames; a paste goes in a Paste frame
// whole (the holder wraps it for the session's paste mode), or is refused with
// a notice when it is over the 64 KiB a frame holds, because several frames
// would be several pastes. Resize sends the newest size only, as a Resize
// frame, and not the size the holder already has. A size asked for while
// connecting or resyncing is sent as the writer whenever a snapshot moves the
// state to attached (the first of a connection, and the one after the holder's
// own resync Notice), if it differs from the holder's.
//
// Sending never blocks the cockpit: frames wait in a queue of at most 256, a
// run of keys shares frames of up to 64 KiB, and what does not fit is dropped,
// counted and, once for each overflow, shown in a notice. A holder that does
// not take a frame within Config.WriteTimeout is given up.
//
// # Key encoding
//
// The encoding is the legacy terminal one, never an enhanced one: the holder
// reports no keyboard enhancement mode, a session that did not ask for one
// reads the legacy bytes, and the holder holds back Ctrl+Z and the signal
// bytes by their value, so every Ctrl+letter is its C0 byte. The modes that
// change the bytes are DEC 1 (application cursor keys: arrows, Home and End as
// SS3 forms), DEC 66 (application keypad), DEC 67 (Backspace sends BS) and ANSI
// 20 (Enter sends CR LF). Alt puts ESC before a key, or is part of the
// modifier parameter of the keys that have one (CSI 1;m final, CSI n;m ~).
//
// Keys that differ between the legacy encoding and the enhanced ones, and so
// need an attended check against each agent: Shift+Enter (see
// [Config.ShiftEnter]: ESC CR by default, or LF, CSI 13;2u or backslash CR),
// Ctrl+Enter (CR here), Ctrl+Tab and Ctrl+Shift+Tab (HT and CSI Z), Ctrl+
// Backspace (BS), Ctrl+digit and Ctrl+punctuation (dropped, except the
// classic ones), Ctrl+Shift+letter (the same byte as Ctrl+letter), Alt+letter
// (ESC letter), and the lone Escape key.
//
// # Untrusted input
//
// Every frame comes from the holder, which parses hostile terminal bytes. The
// reader of [wire] refuses a frame of an unknown type or over its cap from its
// header, the payloads decode strictly, the snapshot is restored by the pane's
// own bounded decoder, notices and counters are bounded and cleaned, and a
// protocol error ends the connection with a reason in the status.
package leadattach
