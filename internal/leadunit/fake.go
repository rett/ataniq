package leadunit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Method names a [Manager] method in the call log of a [Fake].
type Method string

// The methods of [Manager].
const (
	// MethodStart is [Manager.Start].
	MethodStart Method = "Start"
	// MethodInspect is [Manager.Inspect].
	MethodInspect Method = "Inspect"
	// MethodSignal is [Manager.Signal].
	MethodSignal Method = "Signal"
	// MethodEmpty is [Manager.Empty].
	MethodEmpty Method = "Empty"
)

// Behavior says how a fake process answers signals.
type Behavior int

const (
	// ExitOnTerm exits at once on SIGTERM. It is the zero value.
	ExitOnTerm Behavior = iota
	// ExitOnTermAfter exits [ProcScript.Delay] of fake time after the first
	// SIGTERM.
	ExitOnTermAfter
	// IgnoreTerm survives SIGTERM and dies on SIGKILL.
	IgnoreTerm
	// SurviveKill survives SIGKILL too: the control group never empties,
	// unless the unit is made to die by itself with [Fake.Crash].
	SurviveKill
)

// MissingParent says what a start does when its parent is not loaded and live.
// How systemd answers that is not known from the contract (it can refuse the
// call at once, or accept the job and fail it as a dependency failure), which
// is why the fake lets a test choose.
type MissingParent int

const (
	// MissingParentNeverActive is the zero value: the start request is
	// accepted and the unit never becomes active, so it is gone at once under
	// CollectMode=inactive-or-failed. Start returns no error and the State of
	// a unit that is not loaded, and nothing is started.
	MissingParentNeverActive MissingParent = iota
	// MissingParentRefused makes Start return an error wrapping both
	// [ErrReplyLost] and [ErrParentNotLive], and nothing is started: the
	// refusal of the bus is one the real manager cannot recognise, so it
	// reports a lost reply and the caller inspects.
	MissingParentRefused
)

// ErrParentNotLive is what [Fake] returns, wrapped in [ErrReplyLost], for a
// start whose parent is not loaded and live when [MissingParentRefused] is
// set. No real manager returns it by itself: it stands for whatever refusal
// systemd gives, which is not known, and which the real manager reports as a
// lost reply because it cannot recognise it.
var ErrParentNotLive = errors.New("leadunit: parent unit is not loaded and live")

// BoundStop says when the units bound to a parent are stopped, and from which
// state of the parent a start under it is accepted. What systemd does is
// written from its documentation and still to be confirmed by a qualification
// run, which is why the fake lets a test choose.
type BoundStop int

const (
	// BoundStopOnDeactivating is the zero value, and follows the documentation
	// of BindsTo=: a parent is live only while it is active or activating, a
	// bound unit is stopped when the parent turns deactivating (its main
	// process is gone, or it is itself being stopped), and a start under a
	// deactivating parent is treated as one under a parent that is not live.
	BoundStopOnDeactivating BoundStop = iota
	// BoundStopOnUnload is the timing the fake had first: a bound unit is
	// stopped only when the parent unloads, which is when its control group is
	// empty, and a parent that is deactivating still accepts a start.
	BoundStopOnUnload
)

// ProcScript describes one fake process of a control group.
type ProcScript struct {
	// Behavior is how the process answers SIGTERM and SIGKILL.
	Behavior Behavior
	// Delay is the fake time between SIGTERM and the exit, for
	// [ExitOnTermAfter].
	Delay time.Duration
}

// Call is one entry of the call log of a [Fake]. The entry is appended when
// the call takes effect, which is when it returns except for a Start with a
// scripted reply delay, so the order of the log is the order in which the
// calls took effect.
type Call struct {
	// Seq numbers the calls from 1.
	Seq int
	// Method is the method called.
	Method Method
	// Unit is the unit name the call named.
	Unit string
	// Signal is the signal of a [MethodSignal] call, 0 otherwise.
	Signal syscall.Signal
	// Invocation is the invocation id the call's State named, for Signal and
	// Empty; for Start and Inspect it is the invocation the call returned.
	Invocation string
	// At is the time of the call on the injected clock.
	At time.Time
	// Err is the error the call returned.
	Err error
	// Empty is the boolean a [MethodEmpty] call returned.
	Empty bool
	// Late is true for the entry of a Start request whose reply was lost and
	// that was executed afterwards, by [Fake.LandLateStart]. The call that
	// lost the reply has its own entry.
	Late bool
	// Implied is true for an entry that no caller made: the stop that the
	// fake applies to a unit bound to a parent when the parent ends (a
	// [MethodSignal] entry for SIGTERM, and one for SIGKILL when the unit is
	// still populated after its stop timeout). The entry is made when the
	// call that observes the stop runs, or when a setup method does, and At
	// is the time the stop took place, which can be earlier. A test that
	// counts the signals its code sent skips these.
	Implied bool
}

// Fake is an in-memory [Manager] for tests: units with invocation ids, control
// groups with fake processes, a boot identity and a log of every call. It is
// for tests of the code that uses a [Manager], and nothing else. It enforces
// the rules the contract fixes (a second Start for a loaded unit is
// [ErrUnitExists], a unit whose control group is empty is collected, a Signal
// for a stale invocation is refused) and lets a test script the failures:
// lost start replies (also by a context that ends after the request was
// sent), failing starts, processes that ignore SIGTERM or survive SIGKILL, an
// unavailable manager, a unit that dies by itself, a reboot and a foreign unit
// with the name of a role.
//
// Time is the injected [Clock]. A process that exits after a delay does so
// when the clock has passed it and the next call observes it. Setup methods
// are not logged. A Fake is safe for concurrent use.
//
// A unit is loaded while its control group holds a process and unloaded when
// it is empty. The state is "active" while the main process (the first one of
// the group) is alive and "deactivating" when it is gone and others remain;
// the fake does not kill those by itself.
//
// A unit started with a [Spec.Parent] is bound to that unit, as with
// [BoundProperties]. It can start only while the parent is loaded and active
// or activating (and not itself being stopped because of its own parent);
// otherwise it never becomes active, in the way
// [Fake.SetMissingParentOutcome] says. When the parent turns deactivating (its
// main process is gone while others remain, whatever the cause: a signal, a
// process exiting, [Fake.KillProcess]), or unloads at once ([Fake.Crash], the
// last process gone), the units bound to it are stopped at that moment of the
// fake clock, and their units' bound units in turn: every process gets
// SIGTERM and follows its script, the unit is deactivating from then on, and
// the processes that are still there TimeoutStopSec later (2 s, the value in
// [FixedProperties]) get SIGKILL. A process that survives SIGKILL stays. These
// stops are logged as calls with [Call.Implied] set. A parent that is gone
// does not stop a bound unit that was started while the parent was not live,
// because no such unit exists. [Fake.SetBoundStop] chooses the other timing,
// in which the stop waits for the unload of the parent; which one systemd
// follows is for the qualification run to confirm.
type Fake struct {
	mu          sync.Mutex
	clock       Clock
	bootN       int
	nextInv     uint64
	nextCG      uint64
	nextPID     int
	units       map[string]*fakeUnit
	late        map[string][]Spec
	leftover    map[string]bool
	lastCG      map[string]string
	scripts     map[string]*unitScript
	unavailable map[Method]int
	calls       []Call
	specs       []Spec
	missing     MissingParent
	boundStop   BoundStop
}

type fakeUnit struct {
	name       string
	desc       string
	invocation string
	cgroup     string
	cgroupID   string
	mainPID    int
	procs      []*fakeProc
	// parent is the unit this one is bound to ("" for none).
	parent string
	// limits is the limits the unit was started with; nil for none.
	limits *Limits
	// stopping is set by the stop that follows the end of the parent: the unit
	// is deactivating until its group is empty.
	stopping bool
	// killAt is when the stop timeout sends SIGKILL to the whole group; zero
	// when none is pending.
	killAt time.Time
}

type fakeProc struct {
	pid         int
	script      ProcScript
	terminating bool
	exitAt      time.Time
}

type startOutcomeKind int

const (
	outcomeLoseReply startOutcomeKind = iota
	outcomeDrop
	outcomeFail
	outcomeExpire
	outcomeDelay
	outcomeLate
)

type startOutcome struct {
	kind  startOutcomeKind
	err   error
	delay time.Duration
}

type unitScript struct {
	starts  []startOutcome
	procs   []ProcScript
	reuseCG bool
}

var _ Manager = (*Fake)(nil)

// NewFake returns a fake with no unit, reading time from clock.
func NewFake(clock Clock) *Fake {
	return &Fake{
		clock:       clock,
		bootN:       1,
		nextPID:     1000,
		units:       map[string]*fakeUnit{},
		late:        map[string][]Spec{},
		leftover:    map[string]bool{},
		lastCG:      map[string]string{},
		scripts:     map[string]*unitScript{},
		unavailable: map[Method]int{},
	}
}

func (f *Fake) script(unit string) *unitScript {
	s := f.scripts[unit]
	if s == nil {
		s = &unitScript{}
		f.scripts[unit] = s
	}
	return s
}

func (f *Fake) bootID() string { return fmt.Sprintf("fake-boot-%d", f.bootN) }

// BootID returns the current boot identity of the fake machine.
func (f *Fake) BootID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bootID()
}

// LoseNextStartReply scripts the next Start of unit that reaches the fake to
// start the unit and then return [ErrReplyLost]. Scripted outcomes for a unit
// are used in order, one per Start that passes validation and finds the name
// free.
func (f *Fake) LoseNextStartReply(unit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.script(unit)
	s.starts = append(s.starts, startOutcome{kind: outcomeLoseReply})
}

// DropNextStart scripts the next Start of unit to return [ErrReplyLost]
// without starting anything.
func (f *Fake) DropNextStart(unit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.script(unit)
	s.starts = append(s.starts, startOutcome{kind: outcomeDrop})
}

// LateNextStart scripts the next Start of unit to return [ErrReplyLost] without
// starting anything yet: the request is held, as a message still on its way to
// systemd. [Fake.LandLateStart] executes it later. Until then the unit is not
// loaded, and an Inspect answers so.
func (f *Fake) LateNextStart(unit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.script(unit)
	s.starts = append(s.starts, startOutcome{kind: outcomeLate})
}

// ErrNoLateStart is returned by [Fake.LandLateStart] when no late start is
// held for the unit.
var ErrNoLateStart = errors.New("leadunit: no late start held")

// LandLateStart executes the oldest start request that [Fake.LateNextStart]
// held for unit, as when a message whose reply was lost reaches systemd after
// the caller's Inspect answered "not loaded". The unit starts with the spec of
// that request (so with its epoch and intent), appears in
// [Fake.StartedSpecs] and gets an entry marked Late in the call log. It
// returns the state of the new unit. When the name is loaded by then the
// request is refused and dropped, as systemd refuses a second unit of a name:
// it returns [ErrUnitExists], and the log entry carries that error. It returns
// [ErrNoLateStart] when nothing is held.
func (f *Fake) LandLateStart(unit string) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	call := Call{At: f.clock.Now(), Method: MethodStart, Unit: unit, Late: true}
	held := f.late[unit]
	if len(held) == 0 {
		return State{}, ErrNoLateStart
	}
	spec := held[0]
	if len(held) == 1 {
		delete(f.late, unit)
	} else {
		f.late[unit] = held[1:]
	}
	var st State
	var err error
	switch {
	case f.units[unit] != nil:
		err = ErrUnitExists
	case !f.parentLive(spec.Parent):
		st, err = f.orphan(spec)
	default:
		st = f.launch(spec)
		call.Invocation = st.InvocationID
	}
	call.Err = err
	f.log(call)
	return st, err
}

// ReuseNextControlGroup scripts the next start of unit that launches a unit,
// whether by Start or by [Fake.LandLateStart], to give the new invocation the
// control group identity of the unit's previous invocation: the same path, as
// every invocation of a unit has, and the same identity, so that nothing shows
// the old group gone. The fake models no process left over from the old
// invocation: the new unit holds only its own scripted processes, and the only
// effect is the identity, which makes [Fake.Empty] answer false for a State of
// the old invocation. Without it every invocation gets a control group of a new
// identity. The script is spent by the start that launches, also when it has no
// effect, which is the first start of a unit and the first start after a
// reboot (the fake remembers no previous group then); a start that launches
// nothing (a refused name, a dropped, failed or held request) leaves it for
// the next one.
func (f *Fake) ReuseNextControlGroup(unit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script(unit).reuseCG = true
}

// LeaveControlGroup says whether a control group is left at the fixed path of
// unit when the unit is not loaded, so that nothing can prove it gone. While it
// is set, [Fake.Empty] answers false, without an error, for a State of that
// unit when the unit is not loaded, and a start of the unit, by Start or by
// [Fake.LandLateStart], runs in that group, as with
// [Fake.ReuseNextControlGroup]: same path and same identity. It is cleared by
// a reboot.
func (f *Fake) LeaveControlGroup(unit string, left bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if left {
		f.leftover[unit] = true
	} else {
		delete(f.leftover, unit)
	}
}

// SetBoundStop chooses when the units bound to a parent are stopped: see
// [BoundStop]. The default is [BoundStopOnDeactivating]. It applies from the
// next call on and is kept by a reboot; a unit that is already stopping stays
// so.
func (f *Fake) SetBoundStop(b BoundStop) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boundStop = b
}

// SetMissingParentOutcome chooses what a start does when the unit has a
// [Spec.Parent] that is not loaded and live: see [MissingParent]. The default
// is [MissingParentNeverActive]. It applies to every later start, by Start or
// by [Fake.LandLateStart], and is kept by a reboot. A scripted outcome that
// loses the reply still does (and starts nothing); a scripted delay is not
// applied, since nothing started.
func (f *Fake) SetMissingParentOutcome(m MissingParent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missing = m
}

// ExpireNextStart scripts the next Start of unit to start the unit and then
// return an error wrapping both [ErrReplyLost] and [context.DeadlineExceeded],
// as a real manager does when the caller's deadline passes after the request
// was sent. The context the caller passed is not touched.
func (f *Fake) ExpireNextStart(unit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.script(unit)
	s.starts = append(s.starts, startOutcome{kind: outcomeExpire})
}

// DelayNextStart scripts the next Start of unit to start the unit at once and
// to return its reply only d of fake time later (d of 0 or less: at once). A
// context that ends during the delay makes Start return a zero State and an
// error wrapping both [ErrReplyLost] and the context's error; the unit is
// running by then. Other calls are served during the delay. The call log has
// the entry of the Start from the moment the unit started, and its Err is set
// when the call returns.
func (f *Fake) DelayNextStart(unit string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.script(unit)
	s.starts = append(s.starts, startOutcome{kind: outcomeDelay, delay: d})
}

// FailNextStart scripts the next Start of unit to return err without starting
// anything. The caller of FailNextStart says by it that nothing was started, so
// err must not wrap [ErrReplyLost], and it must not wrap [context.Canceled] or
// [context.DeadlineExceeded] either: the Start contract allows a context error
// only from a call whose context was already done, or together with
// [ErrReplyLost]. FailNextStart panics on such an err, as it is a test helper
// and the script would model behaviour the contract forbids.
func (f *Fake) FailNextStart(unit string, err error) {
	if errors.Is(err, ErrReplyLost) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		panic(fmt.Sprintf("leadunit: FailNextStart: %v wraps ErrReplyLost or a context error, which a Start that started nothing cannot return; use ExpireNextStart, DelayNextStart or LateNextStart", err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.script(unit)
	s.starts = append(s.starts, startOutcome{kind: outcomeFail, err: err})
}

// SetProcs scripts the processes of every later start of unit: the first is
// the main process. Without a script a start gets one process that
// [ExitOnTerm]. SetProcs with no argument restores that.
func (f *Fake) SetProcs(unit string, procs ...ProcScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script(unit).procs = append([]ProcScript(nil), procs...)
}

// MakeUnavailable scripts the next n calls of method, on any unit, to return
// [ErrUnavailable] without effect. The count replaces an earlier one for the
// same method; n of 0 clears it.
func (f *Fake) MakeUnavailable(method Method, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n <= 0 {
		delete(f.unavailable, method)
		return
	}
	f.unavailable[method] = n
}

// Crash makes the unit die by itself, as when its holder crashes: every
// process of its control group is killed, even those that survive SIGKILL,
// and the unit unloads, which stops the units bound to it. It reports whether
// the unit was loaded.
func (f *Fake) Crash(unit string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	u, ok := f.units[unit]
	if ok {
		u.procs = nil
		f.unload(u, f.clock.Now())
	}
	return ok
}

// KillProcess removes one process from the control group of unit, as if it
// had exited. When it was the last one the unit unloads (and the units bound
// to it are stopped). It reports whether the process was there.
func (f *Fake) KillProcess(unit string, pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	u := f.units[unit]
	if u == nil {
		return false
	}
	for i, p := range u.procs {
		if p.pid == pid {
			u.procs = append(u.procs[:i], u.procs[i+1:]...)
			now := f.clock.Now()
			f.deactivated(u, now)
			f.collect(u, now)
			return true
		}
	}
	return false
}

// AddProcess adds a process to the control group of a loaded unit, as when
// the unit's command forks, and returns its pid.
func (f *Fake) AddProcess(unit string, script ProcScript) (pid int, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	u := f.units[unit]
	if u == nil {
		return 0, false
	}
	p := f.newProc(script)
	u.procs = append(u.procs, p)
	return p.pid, true
}

// Reboot makes every unit gone at once and gives the fake a new boot
// identity, which it returns. Scripts stay.
func (f *Fake) Reboot() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.units = map[string]*fakeUnit{}
	f.late = map[string][]Spec{}
	f.leftover = map[string]bool{}
	f.lastCG = map[string]string{}
	f.bootN++
	return f.bootID()
}

// AddForeign loads a unit that this package did not start: its description is
// description, which is not ours unless the caller makes it so. It has one
// process that [ExitOnTerm] unless procs says otherwise. It returns
// [ErrUnitExists] when the name is loaded.
func (f *Fake) AddForeign(unit, description string, procs ...ProcScript) error {
	if err := ValidateUnitName(unit); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	if f.units[unit] != nil {
		return ErrUnitExists
	}
	if len(procs) == 0 {
		procs = []ProcScript{{}}
	}
	f.units[unit] = f.newUnit(unit, description, procs)
	return nil
}

// Calls returns a copy of the call log, in the order the calls took effect.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallsOf returns the entries of the call log for one method.
func (f *Fake) CallsOf(method Method) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// StartedSpecs returns a copy of the spec of every unit the fake actually
// started, in order, including those whose reply was lost.
func (f *Fake) StartedSpecs() []Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Spec, len(f.specs))
	for i, s := range f.specs {
		out[i] = s.clone()
	}
	return out
}

// Processes returns the pids of the processes in the control group of unit,
// sorted; none when the unit is not loaded.
func (f *Fake) Processes(unit string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	u := f.units[unit]
	if u == nil {
		return nil
	}
	pids := make([]int, len(u.procs))
	for i, p := range u.procs {
		pids[i] = p.pid
	}
	sort.Ints(pids)
	return pids
}

// Loaded reports whether the unit is loaded.
func (f *Fake) Loaded(unit string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	return f.units[unit] != nil
}

// UnitLimits returns the limits the loaded unit was started with ([Spec.Limits])
// and true; ok is false when the unit is not loaded or was started without
// limits. A unit added by [Fake.AddForeign] has none. [State] does not carry
// them, as the real manager's inspection does not read them back.
func (f *Fake) UnitLimits(unit string) (l Limits, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	u := f.units[unit]
	if u == nil || u.limits == nil {
		return Limits{}, false
	}
	return *u.limits, true
}

func (f *Fake) newProc(script ProcScript) *fakeProc {
	f.nextPID++
	return &fakeProc{pid: f.nextPID, script: script}
}

func (f *Fake) newUnit(name, desc string, procs []ProcScript) *fakeUnit {
	f.nextInv++
	f.nextCG++
	u := &fakeUnit{
		name:       name,
		desc:       desc,
		invocation: fmt.Sprintf("%032x", f.nextInv),
		cgroup:     "/fake.slice/" + name,
		cgroupID:   fmt.Sprintf("cg-%d", f.nextCG),
	}
	for _, s := range procs {
		u.procs = append(u.procs, f.newProc(s))
	}
	u.mainPID = u.procs[0].pid
	return u
}

// stopTimeout is TimeoutStopSec of the fixed properties: how long a unit that
// is being stopped has to end before systemd sends SIGKILL to its group. A test
// checks it against [FixedProperties].
const stopTimeout = 2 * time.Second

// settle applies, in time order, what the clock has reached since the last
// call: the exits of processes that were told to end after a delay, the
// SIGKILL of a stop timeout, the collection of units whose control group is
// empty, and the stops of the units bound to a unit that unloaded. An event is
// applied at its own time, not at the time of the call, so what follows from it
// (the stop of a bound unit, its timeout) is as late as it would have been
// had a call been made then. The caller holds f.mu.
func (f *Fake) settle() {
	now := f.clock.Now()
	for {
		t, ok := f.nextEvent(now)
		if !ok {
			return
		}
		f.runEvents(t)
	}
}

// nextEvent returns the earliest time, not after now, at which a delayed exit
// or a stop timeout is due.
func (f *Fake) nextEvent(now time.Time) (time.Time, bool) {
	var at time.Time
	found := false
	consider := func(x time.Time) {
		if !x.After(now) && (!found || x.Before(at)) {
			at, found = x, true
		}
	}
	for _, u := range f.units {
		for _, p := range u.procs {
			if p.terminating {
				consider(p.exitAt)
			}
		}
		if !u.killAt.IsZero() {
			consider(u.killAt)
		}
	}
	return at, found
}

// runEvents applies every event due at t or earlier, unit by unit in name
// order, and collects the units that it emptied.
func (f *Fake) runEvents(t time.Time) {
	names := make([]string, 0, len(f.units))
	for n := range f.units {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		u := f.units[n]
		if u == nil {
			continue // unloaded by the stop of its parent meanwhile
		}
		kept := u.procs[:0]
		for _, p := range u.procs {
			if p.terminating && !p.exitAt.After(t) {
				continue
			}
			kept = append(kept, p)
		}
		u.procs = kept
		f.deactivated(u, t)
		if !u.killAt.IsZero() && !u.killAt.After(t) {
			u.killAt = time.Time{}
			f.logImplied(u, syscall.SIGKILL, t)
			f.deliver(u, syscall.SIGKILL, t)
		}
		f.collect(u, t)
	}
}

// deliver sends sig to every process of u at time at, each acting as its
// script says. Only SIGTERM and SIGKILL do anything.
func (f *Fake) deliver(u *fakeUnit, sig syscall.Signal, at time.Time) {
	kept := u.procs[:0]
	for _, p := range u.procs {
		dies := false
		switch sig {
		case syscall.SIGTERM:
			switch p.script.Behavior {
			case ExitOnTerm:
				dies = true
			case ExitOnTermAfter:
				if p.script.Delay <= 0 {
					dies = true
				} else if !p.terminating {
					p.terminating = true
					p.exitAt = at.Add(p.script.Delay)
				}
			}
		case syscall.SIGKILL:
			dies = p.script.Behavior != SurviveKill
		}
		if !dies {
			kept = append(kept, p)
		}
	}
	u.procs = kept
	f.deactivated(u, at)
}

// logImplied appends the entry of a signal that the fake sent by itself.
func (f *Fake) logImplied(u *fakeUnit, sig syscall.Signal, at time.Time) {
	f.log(Call{Method: MethodSignal, Unit: u.name, Signal: sig, Invocation: u.invocation, At: at, Implied: true})
}

// log appends call to the call log and numbers it.
func (f *Fake) log(call Call) {
	call.Seq = len(f.calls) + 1
	f.calls = append(f.calls, call)
}

// collect unloads the unit when its control group is empty
// (CollectMode=inactive-or-failed). at is when that happened.
func (f *Fake) collect(u *fakeUnit, at time.Time) {
	if len(u.procs) == 0 && f.units[u.name] == u {
		f.unload(u, at)
	}
}

// unload removes the unit, which holds no process any more, and stops the
// units that are bound to its name (when that has not happened already).
func (f *Fake) unload(u *fakeUnit, at time.Time) {
	delete(f.units, u.name)
	f.stopBound(u.name, at)
}

// deactivated stops the units bound to u when u is deactivating, under
// [BoundStopOnDeactivating]: its main process is gone, or it is itself being
// stopped. It is called wherever the processes of u can have changed, and does
// nothing again for a unit whose bound units are stopped already.
func (f *Fake) deactivated(u *fakeUnit, at time.Time) {
	if f.boundStop != BoundStopOnDeactivating || f.units[u.name] != u {
		return
	}
	if f.state(u.name).Active == ActiveDeactivating {
		f.stopBound(u.name, at)
	}
}

// stopBound applies the implied stop to every unit loaded and bound to the
// unit parent, in name order.
func (f *Fake) stopBound(parent string, at time.Time) {
	var bound []string
	for n, c := range f.units {
		if c.parent == parent {
			bound = append(bound, n)
		}
	}
	sort.Strings(bound)
	for _, n := range bound {
		if c := f.units[n]; c != nil {
			f.impliedStop(c, at)
		}
	}
}

// impliedStop is the stop of a unit whose parent ended: SIGTERM to the group
// now, SIGKILL after the stop timeout when something is left.
func (f *Fake) impliedStop(u *fakeUnit, at time.Time) {
	if u.stopping {
		return
	}
	u.stopping = true
	f.logImplied(u, syscall.SIGTERM, at)
	f.deliver(u, syscall.SIGTERM, at)
	if len(u.procs) > 0 {
		u.killAt = at.Add(stopTimeout)
	}
	f.collect(u, at)
}

// parentLive reports whether a unit with that parent can start now: the parent
// is loaded and not itself being stopped, and it is active or activating
// (under [BoundStopOnUnload]: also deactivating).
func (f *Fake) parentLive(parent string) bool {
	if parent == "" {
		return true
	}
	p := f.units[parent]
	if p == nil || p.stopping {
		return false
	}
	st := f.state(parent)
	if f.boundStop == BoundStopOnUnload {
		return st.Live()
	}
	return st.Active == ActiveActive || st.Active == ActiveActivating
}

// orphan is the result of a start whose parent is not live, by the scripted
// [MissingParent] outcome. Nothing is started.
func (f *Fake) orphan(spec Spec) (State, error) {
	if f.missing == MissingParentRefused {
		// The real manager cannot tell this refusal from any other failure
		// of the call, and reports a lost reply.
		return State{}, fmt.Errorf("%w: %w: %s", ErrReplyLost, ErrParentNotLive, spec.Parent)
	}
	return f.state(spec.Unit), nil
}

func (f *Fake) state(unit string) State {
	st := State{Unit: unit, Active: ActiveUnknown, BootID: f.bootID()}
	u := f.units[unit]
	if u == nil {
		return st
	}
	st.Loaded = true
	st.Active = ActiveDeactivating
	for _, p := range u.procs {
		if p.pid == u.mainPID {
			st.Active = ActiveActive
			st.MainPID = p.pid
		}
	}
	if u.stopping {
		st.Active = ActiveDeactivating
	}
	st.Parent = u.parent
	st.InvocationID = u.invocation
	st.ControlGroup = u.cgroup
	st.ControlGroupID = u.cgroupID
	st.SetDescription(u.desc)
	return st
}

// enter does what every call does first: it refuses on a done context and on a
// scripted unavailability, and settles the clock (which can log the stops of
// bound units, so a call is numbered when it is logged, after them). It
// returns the error to return, if any. The caller holds f.mu.
func (f *Fake) enter(ctx context.Context, call *Call) error {
	call.At = f.clock.Now()
	if err := ctx.Err(); err != nil {
		return err
	}
	if n := f.unavailable[call.Method]; n > 0 {
		if n == 1 {
			delete(f.unavailable, call.Method)
		} else {
			f.unavailable[call.Method] = n - 1
		}
		return ErrUnavailable
	}
	f.settle()
	return nil
}

// Start implements [Manager]. See [Fake] for the scripted outcomes.
func (f *Fake) Start(ctx context.Context, spec Spec) (State, error) {
	f.mu.Lock()
	call := Call{Method: MethodStart, Unit: spec.Unit}
	st, delay, err := f.start(ctx, &call, spec)
	call.Err = err
	if st.InvocationID != "" {
		call.Invocation = st.InvocationID
	}
	f.log(call)
	idx := len(f.calls) - 1
	f.mu.Unlock()
	if delay <= 0 || err != nil {
		return st, err
	}
	// The unit is started and the reply is on its way: wait for it without
	// holding the lock, and lose it when the context ends first.
	if serr := f.clock.Sleep(ctx, delay); serr != nil {
		err = fmt.Errorf("%w: %w", ErrReplyLost, serr)
		f.mu.Lock()
		f.calls[idx].Err = err
		f.mu.Unlock()
		return State{}, err
	}
	return st, nil
}

// launch starts the unit of spec, which must not be loaded, and returns its
// state. The caller holds f.mu.
func (f *Fake) launch(spec Spec) State {
	procs := f.script(spec.Unit).procs
	if len(procs) == 0 {
		procs = []ProcScript{{}}
	}
	script := f.script(spec.Unit)
	prev := f.lastCG[spec.Unit]
	u := f.newUnit(spec.Unit, spec.Description(), procs)
	u.parent = spec.Parent
	if spec.Limits != nil {
		l := *spec.Limits
		u.limits = &l
	}
	if (script.reuseCG || f.leftover[spec.Unit]) && prev != "" {
		u.cgroupID = prev
	}
	script.reuseCG = false
	f.lastCG[spec.Unit] = u.cgroupID
	f.units[spec.Unit] = u
	f.specs = append(f.specs, spec.clone())
	return f.state(spec.Unit)
}

// start is the part of Start that runs under f.mu. It returns the delay of the
// reply, for a scripted delay that the caller must wait for.
func (f *Fake) start(ctx context.Context, call *Call, spec Spec) (State, time.Duration, error) {
	if err := f.enter(ctx, call); err != nil {
		return State{}, 0, err
	}
	if err := spec.Validate(); err != nil {
		return State{}, 0, err
	}
	if f.units[spec.Unit] != nil {
		return State{}, 0, ErrUnitExists
	}
	script := f.script(spec.Unit)
	var outcome *startOutcome
	if len(script.starts) > 0 {
		outcome = &script.starts[0]
		script.starts = script.starts[1:]
	}
	if outcome != nil {
		switch outcome.kind {
		case outcomeDrop:
			return State{}, 0, ErrReplyLost
		case outcomeLate:
			f.late[spec.Unit] = append(f.late[spec.Unit], spec.clone())
			return State{}, 0, ErrReplyLost
		case outcomeFail:
			if outcome.err == nil {
				return State{}, 0, errors.New("leadunit: scripted start failure")
			}
			return State{}, 0, outcome.err
		}
	}
	if !f.parentLive(spec.Parent) {
		if outcome != nil {
			switch outcome.kind {
			case outcomeLoseReply:
				return State{}, 0, ErrReplyLost
			case outcomeExpire:
				return State{}, 0, fmt.Errorf("%w: %w", ErrReplyLost, context.DeadlineExceeded)
			}
		}
		st, err := f.orphan(spec)
		return st, 0, err
	}
	st := f.launch(spec)
	if outcome != nil {
		switch outcome.kind {
		case outcomeLoseReply:
			call.Invocation = st.InvocationID
			return State{}, 0, ErrReplyLost
		case outcomeExpire:
			call.Invocation = st.InvocationID
			return State{}, 0, fmt.Errorf("%w: %w", ErrReplyLost, context.DeadlineExceeded)
		case outcomeDelay:
			return st, outcome.delay, nil
		}
	}
	return st, 0, nil
}

// Inspect implements [Manager].
func (f *Fake) Inspect(ctx context.Context, unit string) (st State, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := Call{Method: MethodInspect, Unit: unit}
	defer func() {
		call.Err = err
		if st.InvocationID != "" {
			call.Invocation = st.InvocationID
		}
		f.log(call)
	}()
	if err := f.enter(ctx, &call); err != nil {
		return State{}, err
	}
	if err := ValidateUnitName(unit); err != nil {
		return State{}, err
	}
	return f.state(unit), nil
}

// Signal implements [Manager]. The fake acts on SIGTERM and SIGKILL only;
// any other signal reaches the processes and does nothing.
func (f *Fake) Signal(ctx context.Context, st State, sig syscall.Signal) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := Call{Method: MethodSignal, Unit: st.Unit, Signal: sig, Invocation: st.InvocationID}
	logged := false
	defer func() {
		if !logged {
			call.Err = err
			f.log(call)
		}
	}()
	if err := f.enter(ctx, &call); err != nil {
		return err
	}
	if err := ValidateUnitName(st.Unit); err != nil {
		return err
	}
	u := f.units[st.Unit]
	if u == nil {
		return ErrNotLoaded
	}
	if st.InvocationID == "" || st.InvocationID != u.invocation {
		return ErrInvocationChanged
	}
	// The call is logged before its effect, so that the stops it causes in
	// bound units follow it in the log.
	logged = true
	f.log(call)
	now := f.clock.Now()
	f.deliver(u, sig, now)
	f.collect(u, now)
	return nil
}

// Empty implements [Manager]. It is true in three cases:
//
//   - the unit is not loaded, unless [Fake.LeaveControlGroup] says a control
//     group is left (then false, without an error);
//   - the unit has another invocation than st and st names a control group
//     identity that is not the one the unit's control group has: the old group
//     is gone. When st names no identity, or the new invocation runs in the old
//     control group (see [Fake.ReuseNextControlGroup], and
//     [Fake.LeaveControlGroup] while it is set), the answer is false;
//   - the unit has the invocation and control group of st and the group holds no
//     process, which a loaded unit of the fake never shows, because it unloads
//     an empty one.
//
// A start request that [Fake.LateNextStart] holds is not seen: the answer says
// nothing about it, as the interface says. The answer is false with
// [ErrUnavailable] when scripted so, and false with an error when st names no
// invocation of a loaded unit, or a control group of another identity than the
// unit's for the same invocation.
func (f *Fake) Empty(ctx context.Context, st State) (empty bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := Call{Method: MethodEmpty, Unit: st.Unit, Invocation: st.InvocationID}
	defer func() {
		call.Err = err
		call.Empty = empty
		f.log(call)
	}()
	if err := f.enter(ctx, &call); err != nil {
		return false, err
	}
	if err := ValidateUnitName(st.Unit); err != nil {
		return false, err
	}
	u := f.units[st.Unit]
	if u == nil {
		return !f.leftover[st.Unit], nil
	}
	if st.InvocationID == "" {
		return false, ErrInvocationChanged
	}
	if st.InvocationID != u.invocation {
		// The old control group is gone only when this invocation runs in
		// another one. The fake gives every invocation its own, unless a
		// test scripted the reuse.
		return st.ControlGroupID != "" && st.ControlGroupID != u.cgroupID, nil
	}
	if st.ControlGroupID != u.cgroupID || st.ControlGroup != u.cgroup {
		return false, ErrControlGroupMismatch
	}
	return len(u.procs) == 0, nil
}
