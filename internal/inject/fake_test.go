package inject

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ataniq/internal/inject/command"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
)

// This file is a deterministic in-memory session for the tests: a tiny model
// of an agent's screen in claude's and codex's layouts, behind a fake clock.
// It has no goroutine and no sleep. The screens are laid out as the recorded
// fixtures in internal/inject/screen/testdata are (the layout test below reads
// them with the real recognisers), and the agent reacts to writes as the
// spike's fake agent and the Q13 recordings do: a bracketed paste at the
// composer lands in it, a paste into an open dialog is swallowed, Enter at a
// composer with text submits it and Enter at a dialog approves its default.
//
// What the fake knows has two halves, as in the holder. The agent's own state
// changes at once when an event fires. What the emulator shows changes only
// when a Settle reads the output that the change put in the PTY buffer
// (frames, each with the time it arrives). Frozen reads the emulator and
// applies nothing, and Pending says whether a frame has already arrived that
// no Settle has read.

const (
	screenCols = 120
	screenRows = 36
)

// step names the places where a scripted event fires. An event fires once, at
// the start of the call it is named for.
type step string

const (
	stepStart   step = "start"   // the lock is taken, nothing read yet
	stepSettle1 step = "settle1" // the first Settle
	stepFrozen1 step = "frozen1" // the first check
	stepPaste   step = "paste"   // the paste write, before the agent gets it
	stepSettle2 step = "settle2" // the Settle after the paste
	stepFrozen2 step = "frozen2" // the second check
	stepEnter   step = "enter"   // the Enter write, before the agent gets it
)

var allSteps = []step{stepStart, stepSettle1, stepFrozen1, stepPaste, stepSettle2, stepFrozen2, stepEnter}

// mode is what the agent is doing.
type mode int

const (
	mIdle       mode = iota // idle, at its composer
	mWorking                // a turn is running
	mDialog                 // an approval dialog is open
	mBackground             // idle with a background shell running (claude)
)

var (
	errDied  = errors.New("fake: the session died")
	errWrite = errors.New("fake: write failed")
)

// frame is output in the PTY buffer: what changes in the emulator when a
// Settle reads it.
type frame struct {
	at  time.Time
	mut func(*emuState)
}

// emuState is what the emulator shows.
type emuState struct {
	snap    screen.Snapshot
	status  osc7501.Status
	term    TermState
	changed time.Time
}

type scheduled struct {
	at    step
	do    func(*fake)
	fired bool
}

// writeRec is one write the fence attempted.
type writeRec struct {
	data   string
	ok     bool
	frozen bool
	at     time.Time
	ctxErr error
	// atWrite is what the emulator showed when the write was made.
	atWrite emuState
	ended   bool
}

// fake is the session and the agent behind it.
type fake struct {
	t       *testing.T
	rt      string
	version string
	app     string

	clock  time.Time
	cancel context.CancelFunc

	// the agent
	mode        mode
	draft       string // what the composer shows as text
	marker      string // the pasted-text marker the composer shows after the draft
	pending     string // the whole text a submission would send
	echo        []string
	title       string
	submissions []string
	approvals   int
	swallowed   int
	dialogKeys  []string
	agentLog    []string // what reached the agent, in order
	echoDelay   time.Duration

	// the emulator and the PTY buffer
	emu       emuState
	pty       []frame
	ownerLast time.Time

	// the session
	events   []*scheduled
	dead     bool
	ended    bool
	failNext bool
	locked   bool
	frozen   bool
	held     []string

	writes    []writeRec
	lockCalls int
	settles   int
	frozens   int
	snapshots int
	trace     []step
	released  bool
	lastDraw  time.Time
	settleArg [][3]time.Duration
	deadline  time.Time
	earlier   int

	// frozenDeadlines are the deadlines Frozen was given, in order.
	frozenDeadlines []time.Time
}

// newFake makes an idle agent of the runtime, in the state of a session that
// has been sitting for an hour.
func newFake(t *testing.T, rt string) *fake {
	t.Helper()
	f := &fake{
		t:         t,
		rt:        rt,
		clock:     time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		echoDelay: 5 * time.Millisecond,
	}
	switch rt {
	case "claude":
		f.version, f.app = "2.1.325", "claude-code"
	case "codex":
		f.version, f.app = "0.169.0", "codex"
	default:
		t.Fatalf("no fake for runtime %q", rt)
	}
	f.ownerLast = f.clock.Add(-time.Hour)
	f.emu.term = TermState{ParserGround: true, BracketedPaste: true}
	f.emu.changed = f.clock.Add(-time.Hour)
	f.sync()
	return f
}

// runtimes are the two runtimes every scenario runs for.
var runtimes = []string{"claude", "codex"}

func (f *fake) now() time.Time { return f.clock }

// sync makes the emulator show what the agent's state is now, with no PTY
// buffer: the starting point of a scenario.
func (f *fake) sync() {
	f.pty = nil
	f.emu.snap = f.snapshot()
	f.emu.status = f.status()
}

// setMode puts the agent in a mode before the fence starts.
func (f *fake) setMode(m mode) *fake {
	f.mode = m
	f.sync()
	return f
}

// withDraft puts text the owner typed in the composer, typed ownerAgo ago.
func (f *fake) withDraft(text string, ownerAgo time.Duration) *fake {
	f.draft, f.pending = text, text
	f.ownerLast = f.clock.Add(-ownerAgo)
	f.sync()
	return f
}

// withCollapsedPaste puts a pasted-text marker the owner pasted in the
// composer.
func (f *fake) withCollapsedPaste() *fake {
	f.pending = strings.Repeat("a line of the owner's paste\n", 20)
	f.marker = f.markerFor(f.pending)
	f.sync()
	return f
}

// at schedules an event at a step.
func (f *fake) at(s step, do func(*fake)) *fake {
	f.events = append(f.events, &scheduled{at: s, do: do})
	return f
}

func (f *fake) fire(s step) {
	f.trace = append(f.trace, s)
	for _, e := range f.events {
		if e.at == s && !e.fired {
			e.fired = true
			e.do(f)
		}
	}
}

// ---- the agent's output ----

// push puts a frame in the PTY buffer, in order of arrival.
// A frame that is not early is never ahead of what the agent already drew.
func (f *fake) push(delay time.Duration, mut func(*emuState)) {
	at := f.clock.Add(delay)
	if delay >= 0 && at.Before(f.lastDraw) {
		at = f.lastDraw
	}
	f.pushAt(at, mut)
}

func (f *fake) pushAt(at time.Time, mut func(*emuState)) {
	f.pty = append(f.pty, frame{at: at, mut: mut})
	sort.SliceStable(f.pty, func(i, j int) bool { return f.pty[i].at.Before(f.pty[j].at) })
}

// draw puts the agent's screen as it is now in the PTY buffer, with its
// report when the report changed. What the agent draws arrives in the order
// it was drawn.
func (f *fake) draw(delay time.Duration, withStatus bool) {
	snap := f.snapshot()
	st := f.status()
	at := f.clock.Add(delay)
	if at.Before(f.lastDraw) {
		at = f.lastDraw
	}
	f.lastDraw = at
	f.pushAt(at, func(e *emuState) {
		e.snap = snap
		if withStatus {
			e.status = st
		}
	})
}

func (f *fake) status() osc7501.Status {
	rep := osc7501.Report{State: osc7501.StateIdle, App: f.app}
	switch f.mode {
	case mWorking:
		rep.State = osc7501.StateWorking
	case mDialog:
		rep.State = osc7501.StateBlocked
		rep.Kind = "permission"
	}
	return osc7501.Status{Report: rep, At: f.clock, Has: true}
}

// ---- events the tests use ----

func evOpenDialog(f *fake) {
	f.mode = mDialog
	f.draw(0, true)
}

func evStartWorking(f *fake) {
	f.mode = mWorking
	f.draw(0, true)
}

func evDie(f *fake)       { f.dead = true }
func evEnded(f *fake)     { f.ended = true }
func evFailWrite(f *fake) { f.failNext = true }

func evType(keys string) func(*fake) {
	return func(f *fake) {
		f.ownerLast = f.clock
		if f.locked {
			f.held = append(f.held, keys)
			return
		}
		f.toAgent(keys, "typed")
	}
}

// evClockJump moves the clock an hour on, past any deadline.
func evClockJump(f *fake) { f.clock = f.clock.Add(time.Hour) }

func evCancel(f *fake) { f.cancel() }

func evStaleReport(f *fake) {
	f.push(0, func(e *emuState) { e.status.Stale = true })
}

func evBlockedReport(f *fake) {
	f.push(0, func(e *emuState) {
		e.status.Report.State = osc7501.StateBlocked
		e.status.Report.Kind = "permission"
	})
}

func evIdleReportOnly(f *fake) {
	f.push(0, func(e *emuState) { e.status.Report.State = osc7501.StateIdle })
}

func evTerm(mut func(*TermState)) func(*fake) {
	return func(f *fake) { f.push(0, func(e *emuState) { mut(&e.term) }) }
}

func evTitle(title string) func(*fake) {
	return func(f *fake) { f.push(0, func(e *emuState) { e.snap.Title = title }) }
}

// evCursorAway moves the cursor to the top row, out of the composer.
func evCursorAway(f *fake) {
	f.push(0, func(e *emuState) { e.snap.CursorX, e.snap.CursorY = 0, 0 })
}

// evReplaceDraft is the composer showing other text than what was pasted (the
// agent redraws it, or the owner's keys got there first).
func evReplaceDraft(f *fake) {
	f.draft, f.marker = "unrelated words in the composer", ""
	f.draw(f.echoDelay, false)
}

func evUnknownScreen(f *fake) {
	f.push(0, func(e *emuState) {
		e.snap.Text = "a screen the recogniser was not written for\n\nnothing of it matches a layout"
		e.snap.Faint = nil
	})
}

// evNoise puts n identical redraws in the PTY buffer, one every interval, so that the
// screen never goes quiet.
func evNoise(n int, every time.Duration) func(*fake) {
	return func(f *fake) {
		for i := 0; i < n; i++ {
			snap := f.snapshot()
			f.push(time.Duration(i)*every, func(e *emuState) { e.snap = snap })
		}
	}
}

// closeDialog is the owner pressing Esc: the dialog closes and the agent goes
// back to its composer.
func (f *fake) closeDialog() {
	f.mode = mIdle
	f.draw(0, true)
	f.settleAll()
}

// becomeIdle is the end of the agent's turn.
func (f *fake) becomeIdle() {
	f.mode = mIdle
	f.draw(0, true)
	f.settleAll()
}

// settleAll lets everything in the PTY buffer reach the emulator and a second
// pass, as the passing of time between two fences.
func (f *fake) settleAll() {
	f.clock = f.clock.Add(time.Hour)
	for _, fr := range f.pty {
		f.applyFrame(fr)
	}
	f.pty = nil
	f.ownerLast = f.clock.Add(-time.Hour)
	f.emu.changed = f.clock.Add(-time.Hour)
}

func (f *fake) applyFrame(fr frame) {
	if f.frozen {
		f.t.Errorf("output was applied to the emulator inside Frozen")
	}
	fr.mut(&f.emu)
	f.emu.changed = fr.at
}

// ---- Session ----

func (f *fake) Runtime() (string, string) { return f.rt, f.version }

func (f *fake) WithInputLock(ctx context.Context, fn func(Locked) error) error {
	f.lockCalls++
	if f.lockCalls > 1 {
		f.t.Errorf("the lock was taken %d times in one fence", f.lockCalls)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.dead {
		return errDied
	}
	f.locked = true
	f.fire(stepStart)
	err := fn(f)
	f.locked = false
	f.released = true
	// Release: the writer's held keys reach the agent, in order, after every
	// write of the fence.
	for _, k := range f.held {
		f.toAgent(k, "typed")
	}
	f.held = nil
	return err
}

// ---- Locked ----

func (f *fake) Settle(ctx context.Context, min, quiet, max time.Duration) (bool, error) {
	if f.frozen {
		f.t.Errorf("Settle was called inside Frozen")
	}
	f.settles++
	f.settleArg = append(f.settleArg, [3]time.Duration{min, quiet, max})
	switch f.settles {
	case 1:
		f.fire(stepSettle1)
	case 2:
		f.fire(stepSettle2)
	default:
		f.t.Errorf("Settle was called %d times", f.settles)
	}
	if f.dead {
		return false, errDied
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	start := f.clock
	limit := start.Add(max)
	t := start.Add(min)
	for {
		moved := false
		for len(f.pty) > 0 && !f.pty[0].at.After(t) && !f.pty[0].at.After(limit) {
			fr := f.pty[0]
			f.pty = f.pty[1:]
			f.applyFrame(fr)
			if need := fr.at.Add(quiet); need.After(t) {
				t = need
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	drained := !t.After(limit)
	if !drained {
		t = limit
	}
	f.clock = t
	return drained, nil
}

func (f *fake) Frozen(deadline time.Time, fn func(View, func([]byte) error) error) error {
	f.frozens++
	f.frozenDeadlines = append(f.frozenDeadlines, deadline)
	switch f.frozens {
	case 1:
		f.fire(stepFrozen1)
	case 2:
		f.fire(stepFrozen2)
	default:
		f.t.Errorf("Frozen was called %d times", f.frozens)
	}
	if f.dead {
		return errDied
	}
	f.frozen = true
	defer func() { f.frozen = false }()
	return fn(f, f.write)
}

// write is the PTY master write the fence is given.
func (f *fake) write(b []byte) error {
	rec := writeRec{data: string(b), frozen: f.frozen, at: f.clock, atWrite: f.emu, ended: f.ended || f.dead}
	if !f.frozen || !f.locked {
		f.t.Errorf("a write was made outside Frozen or outside the lock")
	}
	switch {
	case strings.HasPrefix(string(b), pasteStart):
		f.fire(stepPaste)
	default:
		f.fire(stepEnter)
	}
	rec.at = f.clock
	if f.dead {
		f.writes = append(f.writes, rec)
		return errDied
	}
	if f.failNext {
		f.failNext = false
		f.writes = append(f.writes, rec)
		return errWrite
	}
	rec.ok = true
	f.writes = append(f.writes, rec)
	f.toAgent(string(b), "fence")
	return nil
}

// ---- View ----

func (f *fake) Snapshot() screen.Snapshot {
	f.snapshots++
	s := f.emu.snap
	s.Faint = append([]screen.Span(nil), s.Faint...)
	return s
}
func (f *fake) Report() osc7501.Status    { return f.emu.status }
func (f *fake) Term() TermState           { return f.emu.term }
func (f *fake) OwnerQuiet() time.Duration { return f.clock.Sub(f.ownerLast) }
func (f *fake) ScreenQuiet() time.Duration {
	return f.clock.Sub(f.emu.changed)
}
func (f *fake) Ended() bool { return f.ended || f.dead }

// Pending is true when the agent has already written output (a frame whose
// time has come) that no Settle has read: it sits in the PTY. A frame that
// arrives later is output not yet queued to the holder; the fake treats the
// kernel's hand-over to the line discipline as instant.
func (f *fake) Pending() bool {
	for _, fr := range f.pty {
		if !fr.at.After(f.clock) {
			return true
		}
	}
	return false
}

// nextFence resets what belongs to one fence, for a test that runs several on
// the same session. The writes and what reached the agent of the earlier
// fences are counted only by totalWrites.
func (f *fake) nextFence() {
	f.lockCalls, f.settles, f.frozens, f.snapshots = 0, 0, 0, 0
	f.settleArg, f.trace, f.released = nil, nil, false
	f.earlier += len(f.writes)
	f.writes, f.agentLog = nil, nil
}

// totalWrites counts the writes of every fence on this session.
func (f *fake) totalWrites() int { return f.earlier + len(f.writes) }

// touched reports whether the fence did anything with the session.
func (f *fake) touched() bool {
	return f.lockCalls+f.settles+f.frozens+f.snapshots+len(f.writes) > 0
}

// ---- the agent reacts ----

// toAgent is bytes reaching the agent. from is "fence" or "typed".
func (f *fake) toAgent(s, from string) {
	switch {
	case strings.HasPrefix(s, pasteStart) && strings.HasSuffix(s, pasteEnd):
		body := s[len(pasteStart) : len(s)-len(pasteEnd)]
		f.agentLog = append(f.agentLog, "paste:"+body)
		f.onPaste(strings.ReplaceAll(body, "\r", "\n"))
	case s == "\r":
		f.agentLog = append(f.agentLog, "enter")
		f.onEnter()
	default:
		f.agentLog = append(f.agentLog, "keys:"+s)
		if f.mode == mDialog {
			f.dialogKeys = append(f.dialogKeys, s)
			return
		}
		f.draft += s
		f.pending += s
		f.draw(f.echoDelay, false)
	}
}

// onPaste: a paste at the composer lands in it, long text as the runtime's
// marker; a paste into a dialog is swallowed.
func (f *fake) onPaste(text string) {
	if f.mode == mDialog {
		f.swallowed++
		return
	}
	f.pending += text
	if len(text) > 1000 || strings.Count(text, "\n") > 10 {
		f.marker = f.markerFor(text)
	} else {
		f.draft += text
	}
	f.draw(f.echoDelay, false)
}

func (f *fake) markerFor(text string) string {
	if f.rt == "claude" {
		return fmt.Sprintf("[Pasted text #1 +%d lines]", strings.Count(text, "\n")+1)
	}
	return fmt.Sprintf("[Pasted Content %d chars]", utf8.RuneCountInString(text))
}

// onEnter: at a dialog it approves the default; at a composer with text it
// submits.
func (f *fake) onEnter() {
	switch {
	case f.mode == mDialog:
		f.approvals++
		f.mode = mWorking
		f.draw(f.echoDelay, true)
	case f.pending != "":
		f.submissions = append(f.submissions, f.pending)
		first, _, _ := strings.Cut(f.pending, "\n")
		glyph := "❯ "
		if f.rt == "codex" {
			glyph = "› "
		}
		f.echo = append(f.echo, glyph+first)
		f.draft, f.marker, f.pending = "", "", ""
		f.mode = mWorking
		f.draw(f.echoDelay, true)
	}
}

// ---- screens ----

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func padRows(rows []string, n int) []string {
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// wrap splits text into rows of at most width characters, first by line.
func wrap(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		rs := []rune(line)
		if len(rs) == 0 {
			out = append(out, "")
		}
		for len(rs) > 0 {
			n := min(len(rs), width)
			out = append(out, string(rs[:n]))
			rs = rs[n:]
		}
	}
	return out
}

// composerRows returns the rows of the composer (with the prompt glyph on the
// first and two blanks on the others), the cursor offset in the last row, and
// whether the composer shows a hint.
func (f *fake) composerRows(glyph, hint string, showHint bool) (rows []string, cursorX int, hinted bool) {
	text := f.draft + f.marker
	if text == "" {
		if showHint {
			return []string{glyph + hint}, 2, true
		}
		return []string{glyph}, 2, false
	}
	for i, r := range wrap(text, screenCols-2) {
		if i == 0 {
			rows = append(rows, glyph+r)
		} else {
			rows = append(rows, "  "+r)
		}
	}
	last := rows[len(rows)-1]
	return rows, min(runeLen(last), screenCols-1), false
}

func (f *fake) snapshot() screen.Snapshot {
	if f.rt == "claude" {
		return f.claudeSnap()
	}
	return f.codexSnap()
}

func joinRows(rows []string) string { return strings.Join(padRows(rows, screenRows), "\n") }

var claudeRule = strings.Repeat("─", screenCols)

func (f *fake) claudeSnap() screen.Snapshot {
	rows := []string{" ▐▛███▛█   Claude Code v2.1.325", "▝▜██████▀  Haiku 5.5 · Claude Ultra", " ▝▝   ▝▝   /home/user/work", "", ""}
	rows = append(rows, f.echo...)
	snap := screen.Snapshot{Cols: screenCols, Rows: screenRows, AltScreen: true}
	if f.mode == mDialog {
		rows = append(rows, "", "● Write(approval-test.txt)", "", claudeRule, " Create file", " approval-test.txt", strings.Repeat("╌", 120),
			"  1 hello", strings.Repeat("╌", 120), " Do you want to create approval-test.txt?", " ❯ 1. Yes",
			"   2. Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)",
			"   3. No", "", " Esc to cancel · Tab to amend")
		snap.Text = joinRows(rows)
		snap.CursorHidden, snap.CursorX, snap.CursorY = true, 1, 18
		snap.Title = "✳ Approval test file creation"
		return snap
	}
	var live []string
	snap.Title = "✳ ataniq-msg acknowledgment protocol"
	switch f.mode {
	case mWorking:
		live = []string{"✽ Elucidating…", "  ⎿  Tip: Just want the answer? Run /focus now to hide the steps along the way"}
		snap.Title = "◑ ataniq-msg acknowledgment protocol"
	case mBackground:
		live = []string{"✻ Sautéed for 1s · done 1:32 PM · 1 shell still running"}
	}
	box, cx, hinted := f.composerRows("❯\u00a0", `Try "edit <filepath> to..."`, f.mode == mIdle)
	upper := 33 - len(box)
	rows = fitAbove(rows, live, upper)
	rows = append(rows, claudeRule)
	rows = append(rows, box...)
	rows = append(rows, claudeRule)
	var footer string
	switch {
	case f.marker != "":
		footer = "  paste again to expand"
	case f.mode == mWorking:
		footer = "  ⏸ manual mode on · esc to interrupt"
	case f.mode == mBackground:
		footer = "  ⏸ manual mode on · 1 shell · ↓ to manage"
	case hinted:
		footer = "  ⏸ manual mode on · ? for shortcuts"
	default:
		footer = "  ⏸ manual mode on"
	}
	rows = append(rows, footer)
	snap.Text = joinRows(rows)
	snap.CursorX, snap.CursorY = cx, upper+len(box)
	if hinted {
		snap.CursorY = upper + 1
		snap.Faint = []screen.Span{{Row: upper + 1, From: 2, To: runeLen(box[0])}}
	}
	return snap
}

// fitAbove returns the transcript rows cut or padded so that, with a blank row
// and the live rows (a spinner, a working line) directly above the bottom
// block, they fill n rows. The live rows sit against the bottom block, as the
// agents draw them.
func fitAbove(transcript, live []string, n int) []string {
	room := n - len(live) - 1
	if len(transcript) > room {
		transcript = transcript[len(transcript)-room:]
	}
	rows := padRows(append([]string(nil), transcript...), room)
	rows = append(rows, live...)
	return append(rows, "")
}

// rightAlign pads s to the right edge. The warning sign is two cells wide, so
// the row holds one character less than the screen has columns, as the
// recorded rows do.
func rightAlign(s string) string {
	return strings.Repeat(" ", max(screenCols-1-runeLen(s), 0)) + s
}

func (f *fake) codexSnap() screen.Snapshot {
	rows := []string{"", "  >_ OpenAI Codex (v0.169.0)", "     /home/user/work", "", "  To get started, describe a task or try one of these commands:", ""}
	rows = append(rows, f.echo...)
	snap := screen.Snapshot{Cols: screenCols, Rows: screenRows, AltScreen: true}
	if f.mode == mDialog {
		dlg := []string{"  Would you like to run the following command?", "", "  Environment: local", "",
			"  Reason: May I run `touch approval-test.txt` in the current folder outside the read-only sandbox?", "",
			"  $ touch approval-test.txt", "", "",
			"› 1. Yes, proceed (y)",
			"  2. Yes, and don't ask again for commands that start with `touch approval-test.txt` (p)",
			"  3. No, and tell Codex what to do differently (esc)", "", "  Press enter to confirm or esc to cancel"}
		if len(rows) > screenRows-len(dlg) {
			rows = rows[len(rows)-(screenRows-len(dlg)):]
		}
		rows = padRows(rows, screenRows-len(dlg))
		rows = append(rows, dlg...)
		snap.Text = joinRows(rows)
		snap.CursorHidden, snap.CursorX, snap.CursorY = true, 119, 35
		snap.Title = "[ . ] Action Required | Run approval test command | work"
		return snap
	}
	snap.Title = "Handle ataniq acknowledgments | work"
	var live []string
	if f.mode == mWorking {
		live = []string{"• Working (1s • esc to interrupt)"}
		snap.Title = "⠧ Acknowledge tagged messages | work"
	}
	box, cx, hinted := f.composerRows("› ", "Ask Codex to do anything", true)
	prompt := 33 - len(box)
	// Two rows above the prompt: a blank, and the weekly limit row.
	rows = fitAbove(rows, live, prompt-2)
	rows = append(rows, rightAlign("⚠ weekly limit: 11% left · /status"), "")
	rows = append(rows, box...)
	rows = append(rows, "", "  GPT-6-Luna medium · /home/user/work · Handle ataniq acknowledgments")
	hint := "  ← for agents · ? for shortcuts"
	tail := "⚠ 1 warning · f2 to view"
	rows = append(rows, hint+strings.Repeat(" ", screenCols-1-runeLen(hint)-runeLen(tail))+tail)
	snap.Text = joinRows(rows)
	snap.CursorX, snap.CursorY = cx, prompt+len(box)-1
	if hinted {
		snap.CursorY = prompt
		snap.Faint = []screen.Span{{Row: prompt, From: 2, To: runeLen(box[0])}}
	}
	return snap
}

// ---- helpers for the tests ----

// message builds a message the way the daemon does, with the command package.
func message(id, body string) Message {
	return Message{
		Text:   command.RenderText(id, command.Daemon(), "note", body),
		Header: command.HeaderLine(id, command.Daemon(), "note"),
	}
}

// pasteBytes is the bytes the fence must write for a message.
func pasteBytes(m Message) string {
	return "\x1b[200~" + strings.ReplaceAll(m.Text, "\n", "\r") + "\x1b[201~"
}

// runFence runs Deliver on the fake with a 30 s deadline.
func runFence(t *testing.T, f *fake, m Message) Result {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	f.deadline = f.clock.Add(30 * time.Second)
	return Deliver(ctx, f, m, Options{
		ExpectedMode: "manual",
		Deadline:     f.deadline,
		Now:          f.now,
	})
}

// deliver runs the fence and checks the invariants of every scenario.
func deliver(t *testing.T, f *fake, m Message) Result {
	t.Helper()
	res := runFence(t, f, m)
	checkInvariants(t, f, m, res, invariants{deadline: f.deadline})
	return res
}

// each runs a scenario for both runtimes.
func each(t *testing.T, run func(t *testing.T, rt string)) {
	t.Helper()
	for _, rt := range runtimes {
		t.Run(rt, func(t *testing.T) { run(t, rt) })
	}
}
