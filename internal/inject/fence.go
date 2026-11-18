package inject

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/inject/idle"
)

// MaxTextBytes is the most bytes of one whole text, header included
// (design 1.3: 4 KiB).
const MaxTextBytes = 4096

// The only bytes the fence ever writes: the two ends of a bracketed paste,
// and Enter.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
	enterKey   = "\r"
)

// Written says what the fence wrote to the agent. The spellings are those of
// command.Written, and a test fails if they drift apart.
type Written string

const (
	// WrittenNothing: no write was attempted.
	WrittenNothing Written = "nothing"
	// WrittenPaste: the paste was written, and no Enter.
	WrittenPaste Written = "paste"
	// WrittenPasteAndEnter: the paste and the Enter were written.
	WrittenPasteAndEnter Written = "paste and Enter"
	// WrittenUnknown: a write was attempted and failed, or the session
	// failed after a write was attempted, so what reached the agent is not
	// known.
	WrittenUnknown Written = "unknown"
)

// Outcome is how the fence ended. The spellings are those of
// command.FenceOutcome, and a test fails if they drift apart.
type Outcome string

const (
	// OutcomeNotIdle: the guard did not hold before the paste (or the
	// context ended or the deadline passed first); nothing was written.
	OutcomeNotIdle Outcome = "not idle"
	// OutcomeBlocked: the session is waiting for an approval; nothing was
	// written.
	OutcomeBlocked Outcome = "blocked"
	// OutcomeUntestedVersion: the runtime and version are not in the tested
	// table; nothing was written and the session was not touched.
	OutcomeUntestedVersion Outcome = "untested version"
	// OutcomeRefusedText: the text broke the text rule; nothing was written
	// and the session was not touched.
	OutcomeRefusedText Outcome = "refused text"
	// OutcomeNotSubmitted: the paste was written and the check after it
	// failed, so no Enter was written.
	OutcomeNotSubmitted Outcome = "not submitted"
	// OutcomeSubmitted: the paste and the Enter were written. It is not
	// delivery: only the agent's conversation file can show that the agent
	// took the message, and the daemon reads it.
	OutcomeSubmitted Outcome = "submitted"
	// OutcomeDied: the session failed, or a write failed. Written says what
	// was attempted.
	OutcomeDied Outcome = "died"
)

// Message is the text to deliver and its header.
type Message struct {
	// Text is the whole text to paste: the header line, a newline and the
	// body. It must be valid UTF-8 of printable runes and newlines only,
	// 1 to 4096 bytes.
	Text string
	// Header is the first line of Text, which the composer must hold before
	// the fence presses Enter. It must not be empty or blank.
	Header string
}

// Timing is the waits and quiet periods of the fence. DefaultTiming has the
// design's values. The fence itself never sleeps; Settle does the waiting and
// these values are what it is asked for.
//
// Each zero field takes its default on its own. No quiet and no minimum is
// below the design's value: a smaller one is raised to it (owner quiet 2 s,
// screen quiet 300 ms, check quiet 50 ms, at least 150 ms after the paste). A
// value above the design's is used, but a Settle is never asked to wait past
// the deadline (see Deliver). A maximum may be lower than the default, since a
// lower maximum only makes the Settle give up sooner (not idle, or not
// submitted); it is not below the quiet that ends its Settle, and the maximum
// after the paste is not below its minimum.
type Timing struct {
	// OwnerQuiet is how long the writer must have typed nothing (2 s).
	OwnerQuiet time.Duration
	// ScreenQuiet is how long the screen must have stayed unchanged
	// (300 ms).
	ScreenQuiet time.Duration
	// CheckQuiet is the quiet that ends every Settle (50 ms with no new
	// output).
	CheckQuiet time.Duration
	// FirstSettleMax is the longest the first Settle may take. The design
	// gives no maximum for the first check; 1 s is this package's choice,
	// the same as the maximum after the paste.
	FirstSettleMax time.Duration
	// PasteSettleMin and PasteSettleMax bound the Settle after the paste
	// (at least 150 ms and at most 1 s).
	PasteSettleMin time.Duration
	PasteSettleMax time.Duration
}

// DefaultTiming returns the values of design 4.4: owner quiet 2 s, screen
// quiet 300 ms, check quiet 50 ms, the settle after the paste at least 150 ms
// and at most 1 s, and 1 s at most for the first settle (the design gives no
// maximum for the first check; this value is this package's choice).
func DefaultTiming() Timing {
	return Timing{
		OwnerQuiet:     2 * time.Second,
		ScreenQuiet:    300 * time.Millisecond,
		CheckQuiet:     50 * time.Millisecond,
		FirstSettleMax: time.Second,
		PasteSettleMin: 150 * time.Millisecond,
		PasteSettleMax: time.Second,
	}
}

// normalised returns t with each zero or negative field set to its default
// and no field below the design's value: a smaller value is raised to the
// design's value. A maximum is never below the quiet that ends its Settle, and
// the maximum after the paste is never below its minimum.
func (t Timing) normalised() Timing {
	def := DefaultTiming()
	t.OwnerQuiet = max(t.OwnerQuiet, def.OwnerQuiet)
	t.ScreenQuiet = max(t.ScreenQuiet, def.ScreenQuiet)
	t.CheckQuiet = max(t.CheckQuiet, def.CheckQuiet)
	t.PasteSettleMin = max(t.PasteSettleMin, def.PasteSettleMin)
	if t.FirstSettleMax <= 0 {
		t.FirstSettleMax = def.FirstSettleMax
	}
	if t.PasteSettleMax <= 0 {
		t.PasteSettleMax = def.PasteSettleMax
	}
	t.FirstSettleMax = max(t.FirstSettleMax, t.CheckQuiet)
	t.PasteSettleMax = max(t.PasteSettleMax, t.PasteSettleMin, t.CheckQuiet)
	return t
}

// Options are the settings of one Deliver call.
type Options struct {
	// ExpectedMode is the permission mode the footer of a claude screen must
	// show (see idle.Input). The caller says which mode it expects.
	ExpectedMode string
	// Deadline is the time after which nothing more is written. It must be
	// set: the daemon always sets one, shorter than command.Limits.FenceTimeout.
	// A zero Deadline is refused (outcome not idle, detail "no deadline
	// given", nothing written and the session not touched). It is also what
	// the holder is given as the bound of a write (Locked.Frozen).
	Deadline time.Time
	// Now is the clock the deadline is read on. Nil means time.Now. It is
	// the only clock the fence has.
	Now func() time.Time
	// Timing is the waits and quiet periods. A zero field is its default and
	// no field goes below the design's value (see Timing).
	Timing Timing
}

// Result is what one Deliver call did.
//
// A Result of outcome submitted is not delivery. It says that the paste and the
// Enter were written to the agent's terminal while the screen showed an idle
// composer holding the header. Whether the agent took the message is known only
// from its conversation file, which the daemon reads, and the command stays
// dispatching until it does.
type Result struct {
	// Written is what the fence wrote, Outcome how it ended.
	Written Written
	Outcome Outcome
	// State is the classification of the last check that was made, and empty
	// when no check was made.
	State idle.State
	// Before and After are the first and the second check. Before is nil
	// when the fence ended before the first check; After is nil when it
	// ended before the second.
	Before, After *Reading
	// Detail is a short fixed phrase for the first reason the fence ended as
	// it did, or an error text of the session, cut to printable ASCII.
	Detail string
}

// Deliver runs the fence of design 4.4 once, on the session s, for one message.
// It makes one attempt: it does not wait for the session to become idle, never
// retries, and never writes anything to clean up. The only bytes it ever writes
// are one bracketed paste and, after the check that follows the paste has
// passed, one Enter as its own write. It never writes a key into a dialog.
//
// The steps:
//
//  1. The text rule, before anything else: valid UTF-8, printable runes and
//     newlines only, 1 to 4096 bytes, a header that is not blank and equals
//     the first line. Otherwise outcome refused text, and the session is not
//     touched.
//  2. The runtime and version must be in the tested table (idle.Lookup).
//     Otherwise untested version, and the session is not touched again: no
//     lock is taken.
//  3. With the input lock held, Settle (no minimum, the check quiet, the
//     first settle maximum). If it does not drain, not idle.
//  4. One Frozen call, the first check. The session must not have ended; the
//     parser must be at ground with no 2026 frame open and bracketed paste on;
//     the classification must be idle (a blocked state, from the report or
//     the screen, gives outcome blocked, any other state not idle); the
//     composer must be empty with the cursor in it, with no pasted-text marker
//     and no hidden state, and the idle must not rest on the report alone
//     (Classification.ReportOnly); the owner and the screen must have been
//     quiet long enough; the context must be alive and the deadline not
//     passed; and, read last, the PTY must hold no output the holder has not
//     read and applied (View.Pending). If all hold, the paste is written inside
//     this same call, as one write: ESC [ 200 ~, the text with each newline as
//     a carriage return, ESC [ 201 ~.
//  5. Settle after the paste (at least 150 ms, at most 1 s). If it does not
//     drain, not submitted.
//  6. One Frozen call, the second check. The session must not have ended,
//     the parser must be at ground with no frame open and bracketed paste still
//     on, the classification must be idle again, the composer must be on
//     screen with the cursor in it and must hold the header, or show the
//     pasted-text marker where it did not before the paste (an idle that rests
//     on the report alone is accepted only with that new marker), the deadline
//     must not have passed and, read last, View.Pending must be false. If all
//     hold, a carriage return is written as its
//     own write inside this same call. Otherwise nothing is written and the
//     outcome is not submitted: the paste stays where it is.
//  7. The lock is released.
//
// At most once. The function the session runs and the two Frozen functions are
// each run once. If the holder runs the lock function, the first check or the
// second check a second time, the fence writes nothing more, returns an error
// from the run and the result is died with Written unknown, whatever the first
// run concluded.
//
// Settle bounds. The fence asks no Settle to wait past the deadline: the
// maximum given to each is cut to the time left before it. When less than the
// minimum or the quiet of a Settle is left, the fence does not settle: before
// the paste the outcome is not idle (nothing written, detail "the deadline is
// too near to settle"), after it not submitted. A large Timing value therefore
// cannot hold the input lock, and the writer's keys, past the deadline.
//
// A zero Options.Deadline is refused before the session is touched: not idle,
// detail "no deadline given", nothing written.
//
// Errors. An error from a write, or from the session once a write was
// attempted, gives outcome died and Written unknown; an error from the session
// before anything was attempted gives died and Written nothing. A cancelled
// context, or a passed deadline, gives not idle with nothing written before the
// paste and not submitted after it. Nothing is written after the deadline: it
// is read inside the Frozen call directly before each write. A session error
// after the paste that is not the context ending is died, not submitted: the
// session cannot say what became of the paste.
//
// What the fence cannot close is written in design 4.6: the agent can open a
// dialog after the last reading and before the Enter lands, and then the Enter
// answers it. The fence narrows that window to output that is not yet queued to
// the holder when Pending is read (written by the agent but not yet handed to
// the line discipline, or taken by the holder's reader and not yet counted),
// and cannot remove it.
func Deliver(ctx context.Context, s Session, msg Message, opts Options) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if why := checkText(msg); why != "" {
		return Result{Written: WrittenNothing, Outcome: OutcomeRefusedText, Detail: why}
	}
	if s == nil {
		return Result{Written: WrittenNothing, Outcome: OutcomeDied, Detail: "no session"}
	}
	runtime, version := s.Runtime()
	cl, ok := idle.Lookup(runtime, version)
	if !ok {
		return Result{
			Written: WrittenNothing, Outcome: OutcomeUntestedVersion,
			Detail: "runtime " + clipToken(runtime) + " version " + clipToken(version) + " is not in the tested table",
		}
	}
	if opts.Deadline.IsZero() {
		return Result{Written: WrittenNothing, Outcome: OutcomeNotIdle, Detail: "no deadline given"}
	}
	r := &run{ctx: ctx, cl: cl, msg: msg, timing: opts.Timing.normalised(), mode: opts.ExpectedMode, deadline: opts.Deadline, now: opts.Now}
	if r.now == nil {
		r.now = time.Now
	}
	if why := r.stopped(); why != "" {
		return Result{Written: WrittenNothing, Outcome: OutcomeNotIdle, Detail: why}
	}
	err := s.WithInputLock(ctx, r.fence)
	return r.finish(err)
}

// checkText applies the text rule and returns the reason it is broken, or an
// empty string.
func checkText(m Message) string {
	switch {
	case len(m.Text) == 0:
		return "text is empty"
	case len(m.Text) > MaxTextBytes:
		return "text is over 4096 bytes"
	case !utf8.ValidString(m.Text):
		return "text is not valid UTF-8"
	}
	for _, c := range m.Text {
		if c != '\n' && !unicode.IsPrint(c) {
			return "text holds a character that is not printable"
		}
	}
	first, _, _ := strings.Cut(m.Text, "\n")
	switch {
	case strings.TrimSpace(m.Header) == "":
		return "header is empty"
	case m.Header != first:
		return "header is not the first line of the text"
	}
	return ""
}

// run is one Deliver call in progress.
type run struct {
	ctx      context.Context
	cl       idle.Classifier
	msg      Message
	timing   Timing
	mode     string
	deadline time.Time
	now      func() time.Time

	entered bool // the function given to WithInputLock was called

	// firstRuns and secondRuns count the calls of the two checks. A repeat is
	// a holder that broke "at most once": violation says so, and it decides
	// the result.
	firstRuns, secondRuns int
	violation             string

	pasteTried, pasteOK, enterTried, enterOK bool

	res Result
}

// stopped returns a reason when the context has ended or the deadline has
// passed. It is the only place the fence reads the clock.
func (r *run) stopped() string {
	switch {
	case r.ctx.Err() != nil:
		return "cancelled"
	case !r.deadline.IsZero() && !r.now().Before(r.deadline):
		return "deadline passed"
	}
	return ""
}

// settleBound returns the longest a Settle may be asked to wait: most, cut to
// the time left before the deadline, so that the input lock is never held past
// the deadline by a large Timing value. It returns false when less than least or
// the quiet is left: the fence does not settle then.
func (r *run) settleBound(least, quiet, most time.Duration) (time.Duration, bool) {
	left := r.deadline.Sub(r.now())
	if left < least || left < quiet {
		return 0, false
	}
	return min(most, left), true
}

// nearDetail is the detail of a refused Settle: the reason the fence has
// stopped when it has (the deadline already passed, the context ended), else
// the given phrase.
func (r *run) nearDetail(phrase string) string {
	if why := r.stopped(); why != "" {
		return why
	}
	return phrase
}

// written says what was written, from the attempts so far.
func (r *run) written() Written {
	switch {
	case r.enterTried && r.enterOK:
		return WrittenPasteAndEnter
	case r.enterTried, r.pasteTried && !r.pasteOK:
		return WrittenUnknown
	case r.pasteTried:
		return WrittenPaste
	}
	return WrittenNothing
}

// end sets the result. A result that was set stays: the first ending wins.
func (r *run) end(o Outcome, detail string) {
	if r.res.Outcome != "" {
		return
	}
	r.res.Outcome = o
	r.res.Detail = detail
	r.res.Written = r.written()
}

// died ends the run for an error of the session or of a write. What was
// written is unknown once a write was attempted.
func (r *run) died(err error, what string) {
	if r.res.Outcome != "" {
		return
	}
	r.res.Outcome = OutcomeDied
	r.res.Detail = what + ": " + clipText(err.Error(), 200)
	r.res.Written = WrittenNothing
	if r.pasteTried {
		r.res.Written = WrittenUnknown
	}
}

// cancelled reports whether err is the end of the fence's own context. An
// error that wraps some other context's end (a holder's own) is not: it is
// the session failing.
func (r *run) cancelled(err error) bool {
	return r.ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// repeated records that the holder ran the fence or a check a second time and
// returns the error that run gives back. It writes nothing.
func (r *run) repeated(what string) error {
	if r.violation == "" {
		r.violation = what + " was run twice"
	}
	return errors.New("inject: " + what + " was run twice")
}

// sessionError ends the run for an error that Settle, Frozen or the lock
// returned. The context ending is not a death.
func (r *run) sessionError(err error, what string) {
	if r.cancelled(err) {
		if r.pasteTried {
			r.end(OutcomeNotSubmitted, "cancelled")
		} else {
			r.end(OutcomeNotIdle, "cancelled")
		}
		return
	}
	r.died(err, what)
}

// finish turns the end of WithInputLock into the result.
func (r *run) finish(err error) Result {
	ctxEnded := r.cancelled(err)
	switch {
	case !r.entered && err != nil:
		r.sessionError(err, "input lock")
	case !r.entered:
		r.died(errors.New("the session did not run the fence"), "input lock")
	case err != nil && !ctxEnded && r.pasteTried:
		// The lock failed after a write was attempted: what became of it is
		// not known, whatever the fence had concluded.
		r.res.Outcome = OutcomeDied
		r.res.Written = WrittenUnknown
		r.res.Detail = "releasing the input lock: " + clipText(err.Error(), 200)
	case err != nil && r.res.Outcome == "":
		r.sessionError(err, "input lock")
	}
	r.died(errors.New("the fence ended without a result"), "session")
	if r.violation != "" {
		// The holder broke "at most once": what reached the agent is not
		// known, and nothing the first run concluded can be trusted.
		r.res.Outcome = OutcomeDied
		r.res.Written = WrittenUnknown
		r.res.Detail = "holder: " + r.violation
	}
	switch {
	case r.res.After != nil:
		r.res.State = r.res.After.State
	case r.res.Before != nil:
		r.res.State = r.res.Before.State
	}
	return r.res
}

// fence is the function that runs with the input lock held.
func (r *run) fence(l Locked) error {
	if r.entered {
		return r.repeated("the fence")
	}
	r.entered = true
	t := r.timing

	bound, ok := r.settleBound(0, t.CheckQuiet, t.FirstSettleMax)
	if !ok {
		r.end(OutcomeNotIdle, r.nearDetail("the deadline is too near to settle"))
		return nil
	}
	drained, err := l.Settle(r.ctx, 0, t.CheckQuiet, bound)
	if err != nil {
		r.sessionError(err, "settle")
		return nil
	}
	if !drained {
		r.end(OutcomeNotIdle, "the screen did not settle in time")
		return nil
	}

	err = l.Frozen(r.deadline, r.firstCheck)
	if r.violation != "" {
		return errors.New("inject: " + r.violation)
	}
	if r.res.Outcome != "" {
		return nil
	}
	if err != nil {
		r.sessionError(err, "first check")
		return nil
	}
	if !r.pasteTried {
		return nil
	}

	bound, ok = r.settleBound(t.PasteSettleMin, t.CheckQuiet, t.PasteSettleMax)
	if !ok {
		r.end(OutcomeNotSubmitted, r.nearDetail("the deadline is too near to settle after the paste"))
		return nil
	}
	drained, err = l.Settle(r.ctx, t.PasteSettleMin, t.CheckQuiet, bound)
	if err != nil {
		r.sessionError(err, "settle after the paste")
		return nil
	}
	if !drained {
		r.end(OutcomeNotSubmitted, "the screen did not settle after the paste")
		return nil
	}

	err = l.Frozen(r.deadline, r.secondCheck)
	if r.violation != "" {
		return errors.New("inject: " + r.violation)
	}
	if r.res.Outcome != "" {
		return nil
	}
	if err != nil {
		r.sessionError(err, "second check")
		return nil
	}
	return nil
}

// read classifies the view and returns the reading and the classification.
func (r *run) read(v View, header string) (*Reading, idle.Classification) {
	snap := v.Snapshot()
	rep := v.Report()
	cl := r.cl.Classify(idle.Input{Report: rep, Screen: snap, Ended: v.Ended(), ExpectedMode: r.mode})
	return newReading(v, cl, rep, header), cl
}

// termFailures adds the failed conditions of the parser state.
func termFailures(rd *Reading) {
	if !rd.Term.ParserGround {
		rd.fail("parser not at ground")
	}
	if rd.Term.FrameOpen {
		rd.fail("2026 frame open")
	}
	if !rd.Term.BracketedPaste {
		rd.fail("bracketed paste off")
	}
}

// firstCheck is the first Frozen call: it reads, and writes the paste when
// every condition holds.
func (r *run) firstCheck(v View, write func([]byte) error) error {
	if r.firstRuns++; r.firstRuns > 1 {
		return r.repeated("the first check")
	}
	rd, cl := r.read(v, "")
	r.res.Before = rd
	t := r.timing

	if v.Ended() {
		rd.fail("session ended")
	}
	termFailures(rd)
	switch cl.State {
	case idle.StateIdle:
	case idle.StateBlocked:
		rd.fail("blocked")
	default:
		rd.fail("state " + string(cl.State))
	}
	if !cl.Screen.ComposerEmpty {
		rd.fail("composer not empty")
	}
	if !cl.Screen.CursorInComposer {
		rd.fail("cursor outside the composer")
	}
	if cl.Screen.StateHidden {
		rd.fail("screen hides the state")
	}
	if cl.Screen.PastedMarker {
		rd.fail("pasted-text marker present")
	}
	if cl.ReportOnly {
		rd.fail("idle rests on the report alone")
	}
	if rd.OwnerQuiet < t.OwnerQuiet {
		rd.fail("the owner typed too recently")
	}
	if rd.ScreenQuiet < t.ScreenQuiet {
		rd.fail("the screen changed too recently")
	}
	// The clock and the PTY are read last, directly before the write.
	if why := r.stopped(); why != "" {
		rd.fail(why)
	}
	if v.Pending() {
		rd.fail("output not yet read")
	}
	if len(rd.Failed) > 0 {
		if cl.State == idle.StateBlocked {
			r.end(OutcomeBlocked, rd.Failed[0])
		} else {
			r.end(OutcomeNotIdle, rd.Failed[0])
		}
		return nil
	}

	r.pasteTried = true
	if err := write([]byte(pasteStart + strings.ReplaceAll(r.msg.Text, "\n", "\r") + pasteEnd)); err != nil {
		r.died(err, "paste write")
		return nil
	}
	r.pasteOK = true
	return nil
}

// secondCheck is the second Frozen call: it reads, and writes Enter when every
// condition holds.
func (r *run) secondCheck(v View, write func([]byte) error) error {
	if r.secondRuns++; r.secondRuns > 1 {
		return r.repeated("the second check")
	}
	rd, cl := r.read(v, r.msg.Header)
	r.res.After = rd

	if v.Ended() {
		rd.fail("session ended")
	}
	termFailures(rd)
	switch cl.State {
	case idle.StateIdle:
	case idle.StateBlocked:
		rd.fail("blocked")
	default:
		rd.fail("state " + string(cl.State))
	}
	if !cl.Screen.Composer {
		rd.fail("no composer")
	}
	newMarker := cl.Screen.PastedMarker && (r.res.Before == nil || !r.res.Before.Screen.PastedMarker)
	if !rd.Screen.HeaderHeld && !newMarker {
		rd.fail("the composer does not hold the header")
	}
	if !cl.Screen.CursorInComposer {
		rd.fail("cursor outside the composer")
	}
	if cl.ReportOnly && !newMarker {
		rd.fail("idle rests on the report alone without a new marker")
	}
	// The clock and the PTY are read last, directly before the write.
	if why := r.stopped(); why != "" {
		rd.fail(why)
	}
	if v.Pending() {
		rd.fail("output not yet read")
	}
	if len(rd.Failed) > 0 {
		r.end(OutcomeNotSubmitted, rd.Failed[0])
		return nil
	}

	r.enterTried = true
	if err := write([]byte(enterKey)); err != nil {
		r.died(err, "Enter write")
		return nil
	}
	r.enterOK = true
	r.end(OutcomeSubmitted, "Enter written; delivery is confirmed only by the conversation file")
	return nil
}
