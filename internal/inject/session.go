package inject

import (
	"context"
	"time"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
)

// TermState is what the holder's terminal parser says about the stream it has
// applied so far. The fence injects only when the parser has finished every
// sequence, no synchronized-output frame is open and the agent has asked for
// bracketed paste.
type TermState struct {
	// ParserGround is true when the parser is between sequences: it is not in
	// the middle of an escape sequence, a string or a partly received
	// character.
	ParserGround bool
	// FrameOpen is true when a synchronized-output frame (DEC private mode
	// 2026) is open: the agent began a frame and has not ended it, so the
	// screen shows half a drawing.
	FrameOpen bool
	// BracketedPaste is true when the agent turned bracketed paste on
	// (DEC private mode 2004) and has not turned it off. Without it a paste
	// would be typed as keys, and an Enter inside it would submit.
	BracketedPaste bool
}

// View is the holder's state at one instant. Every method answers from what
// the emulator has applied so far, and two calls inside one Frozen function
// see the same state, except Pending: it asks at each call, without reading
// or waiting, whether output is waiting, and must never be cached (not
// snapshotted when Frozen starts, not remembered from an earlier call).
type View interface {
	// Snapshot is the screen as a text snapshot: the text, the size, the
	// cursor, the alternate-screen flag, the window title and the faint
	// spans.
	//
	// The holder must fill Snapshot.Faint on every snapshot from the faint
	// (SGR 2) attribute of the emulator's cells, with offsets in the row's
	// text and not cell columns (see screen.Span). An empty composer of both
	// agents shows a faint hint, and plain text cannot tell the hint from
	// what the owner typed. With Faint left empty a composer that shows a
	// hint reads as holding text, so nothing would ever be injected into
	// codex.
	Snapshot() screen.Snapshot
	// Report is the current OSC 7501 status (the holder's osc7501.Tracker
	// Status). A stale status must say so: the fence never treats it as the
	// agent's state.
	Report() osc7501.Status
	// Term is the parser, frame and bracketed-paste state.
	Term() TermState
	// OwnerQuiet is the time since the writer last typed. Keys the writer
	// typed while the input lock was held count from when they were typed,
	// not from when they are released.
	OwnerQuiet() time.Duration
	// ScreenQuiet is the time since the screen last changed.
	ScreenQuiet() time.Duration
	// Ended is true when the agent's process has ended.
	Ended() bool
	// Pending is true when output of the agent is queued for the holder that
	// the emulator has not applied: bytes queued for reading on the PTY
	// master (the holder asks the kernel how many, without reading them) and
	// bytes the holder's reader has received during Frozen into the buffer it
	// applies afterwards, counted from the moment its read returns. It is
	// asked fresh at each call, so the fence reads it last, directly before
	// each write, and writes nothing while it is true: the screen it just
	// read is then not the agent's screen. It covers output already queued to
	// the holder when it is read. Output the agent has written that the
	// kernel has not yet handed to the line discipline, or that the holder's
	// reader has taken and not yet counted, is not seen: the window of design
	// 4.6 narrows to that, and does not close.
	Pending() bool
}

// Locked is the input lock, held. Writer input is held, not dropped, until it
// is released: when the function given to Session.WithInputLock returns, the
// holder delivers what the writer typed meanwhile, in order, and none of it
// falls inside a paste.
//
// Replies. The terminal's replies to the agent (the OSC 7501 query reply from
// osc7501.Reply, the answer to DA1, cursor position and colour queries) are
// made by the emulator while it applies output, and they travel the same
// serialised path as every other write to the agent. The lock order the holder
// must keep is:
//
//   - Settle applies output on the lock owner's side, and it writes the
//     replies that output causes itself, in the order the emulator made them
//     (the OSC 7501 reply before the DA1 reply), before it returns. A reply
//     is written, or queued behind the lock's owner and written when its
//     writes are done; it is never dropped.
//   - Frozen applies no output, so it makes no reply. A write inside it
//     therefore follows the reading with nothing between, and no reply can
//     fall inside the paste or between the second reading and the Enter. The
//     Settle after the paste does apply output, and writes the replies that
//     output causes itself: they land between the paste and the Enter, in the
//     agent's input after the paste, in the order the emulator made them.
//   - No goroutine that carries replies may wait for the lock holder while
//     Settle waits for it. The handler that writes a reply must not block on
//     a reader that waits for the input lock, or Settle would never finish.
//
// A holder that gives replies their own goroutine and a bypass around the lock
// breaks the rule: a reply could split the paste.
//
// The fence never sleeps. Every wait is a Settle call, and the clock that
// measures it belongs to the holder.
type Locked interface {
	// Settle reads the PTY until it would block, applies everything to the
	// emulator (and writes the replies that causes) and waits until quiet has
	// passed with no new output. It waits at least min and at most max. It
	// returns drained false when max ended the wait, with output still
	// arriving. An error means the session cannot be read any more (the
	// process or the holder died) or ctx ended; an error from ctx's end must
	// wrap ctx.Err() so that errors.Is finds it.
	Settle(ctx context.Context, min, quiet, max time.Duration) (drained bool, err error)
	// Frozen runs fn with no output applied to the emulator while it runs, so
	// a write made inside fn follows the reading with nothing between. fn is
	// called at most once, on the calling goroutine, with the view of that
	// instant and a write function.
	//
	// While fn runs the holder keeps reading the PTY master into a buffer it
	// does not apply, and applies the buffer after fn returns (View.Pending is
	// true while the buffer holds bytes). An agent that is blocked writing its
	// own output (claude writes to its terminal synchronously) therefore never
	// stops reading its input because the holder stopped reading, so the write
	// below cannot deadlock against it. A paste can be larger than the tty's
	// input buffer (4096 bytes on Linux; a message is up to 4096 bytes and
	// the paste adds 12), so the write may have to wait for the agent to read.
	//
	// deadline is the fence's Options.Deadline, the time after which nothing
	// more may be written. The write function must return an error once a
	// bound no later than deadline has passed without the bytes being
	// written; the fence reports that as died with Written unknown. The
	// holder sets the bound, at most the time left before deadline. Frozen
	// returns when fn returns; it must not outlast deadline waiting for a
	// write.
	//
	// write writes the bytes to the PTY master in one write call: the paste
	// is one call and the Enter another. It is valid only inside fn. An
	// error from write means the bytes may or may not have reached the agent
	// (the bound passing is such an error). Frozen returns fn's error, or its
	// own when it could not run fn.
	//
	// A write that times out in the middle of a paste can leave an open
	// bracketed paste in the agent's input: ESC [ 200 ~ and part of the
	// text, without ESC [ 201 ~. The holder must not complete or retry the
	// write, and must not write a paste end by itself. The fence reports
	// died with Written unknown, and the operator's decision covers it. The
	// writer's held input is still delivered after the lock, and it may land
	// inside that open paste (an Enter then becomes a newline, not a
	// submit), which is why the operator must look at the session.
	Frozen(deadline time.Time, fn func(v View, write func([]byte) error) error) error
}

// Session is one hosted agent as the fence sees it. The holder implements it.
type Session interface {
	// Runtime returns the runtime and its version as the holder found them,
	// for example "claude" and "2.1.325". The fence refuses to touch a
	// session whose pair is not in the tested table of package idle.
	Runtime() (runtime, version string)
	// WithInputLock takes the input lock, runs fn and releases the lock. It
	// returns an error without running fn when it cannot take the lock or
	// ctx ends first (the error then wraps ctx.Err()). The lock is held for
	// the whole of fn, which is the whole fence: the writer's keys and every
	// other writer wait. When fn returns, the held writer input is delivered
	// in order. The error fn returns is returned.
	WithInputLock(ctx context.Context, fn func(Locked) error) error
}
