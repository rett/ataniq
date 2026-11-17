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

// ProcScript describes one fake process of a control group.
type ProcScript struct {
	// Behavior is how the process answers SIGTERM and SIGKILL.
	Behavior Behavior
	// Delay is the fake time between SIGTERM and the exit, for
	// [ExitOnTermAfter].
	Delay time.Duration
}

// Call is one entry of the call log of a [Fake]. The entry is appended when
// the call returns, so the order of the log is the order in which the calls
// took effect.
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
}

// Fake is an in-memory [Manager] for tests: units with invocation ids, control
// groups with fake processes, a boot identity and a log of every call. It is
// for tests of the code that uses a [Manager], and nothing else. It enforces
// the rules the contract fixes (a second Start for a loaded unit is
// [ErrUnitExists], a unit whose control group is empty is collected, a Signal
// for a stale invocation is refused) and lets a test script the failures:
// lost start replies, failing starts, processes that ignore SIGTERM or
// survive SIGKILL, an unavailable manager, a unit that dies by itself, a
// reboot and a foreign unit with the name of a role.
//
// Time is the injected [Clock]. A process that exits after a delay does so
// when the clock has passed it and the next call observes it. Setup methods
// are not logged. A Fake is safe for concurrent use.
//
// A unit is loaded while its control group holds a process and unloaded when
// it is empty. The state is "active" while the main process (the first one of
// the group) is alive and "deactivating" when it is gone and others remain;
// the fake does not kill those by itself.
type Fake struct {
	mu          sync.Mutex
	clock       Clock
	bootN       int
	nextInv     uint64
	nextCG      uint64
	nextPID     int
	units       map[string]*fakeUnit
	scripts     map[string]*unitScript
	unavailable map[Method]int
	calls       []Call
	specs       []Spec
}

type fakeUnit struct {
	name       string
	desc       string
	invocation string
	cgroup     string
	cgroupID   string
	mainPID    int
	procs      []*fakeProc
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
)

type startOutcome struct {
	kind startOutcomeKind
	err  error
}

type unitScript struct {
	starts []startOutcome
	procs  []ProcScript
}

var _ Manager = (*Fake)(nil)

// NewFake returns a fake with no unit, reading time from clock.
func NewFake(clock Clock) *Fake {
	return &Fake{
		clock:       clock,
		bootN:       1,
		nextPID:     1000,
		units:       map[string]*fakeUnit{},
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

// FailNextStart scripts the next Start of unit to return err without starting
// anything.
func (f *Fake) FailNextStart(unit string, err error) {
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
// and the unit unloads. It reports whether the unit was loaded.
func (f *Fake) Crash(unit string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	_, ok := f.units[unit]
	delete(f.units, unit)
	return ok
}

// KillProcess removes one process from the control group of unit, as if it
// had exited. When it was the last one the unit unloads. It reports whether
// the process was there.
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
			f.collect(u)
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

// settle applies the exits that the clock has reached and collects units
// whose control group is empty. The caller holds f.mu.
func (f *Fake) settle() {
	now := f.clock.Now()
	for _, u := range f.units {
		kept := u.procs[:0]
		for _, p := range u.procs {
			if p.terminating && !now.Before(p.exitAt) {
				continue
			}
			kept = append(kept, p)
		}
		u.procs = kept
		f.collect(u)
	}
}

// collect unloads the unit when its control group is empty
// (CollectMode=inactive-or-failed).
func (f *Fake) collect(u *fakeUnit) {
	if len(u.procs) == 0 && f.units[u.name] == u {
		delete(f.units, u.name)
	}
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
	st.InvocationID = u.invocation
	st.ControlGroup = u.cgroup
	st.ControlGroupID = u.cgroupID
	st.SetDescription(u.desc)
	return st
}

// enter does what every call does first: it logs the call when it returns,
// refuses on a done context and on a scripted unavailability, and settles the
// clock. It returns the error to return, if any. The caller holds f.mu.
func (f *Fake) enter(ctx context.Context, call *Call) error {
	call.Seq = len(f.calls) + 1
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
func (f *Fake) Start(ctx context.Context, spec Spec) (st State, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := Call{Method: MethodStart, Unit: spec.Unit}
	defer func() {
		call.Err = err
		if st.InvocationID != "" {
			call.Invocation = st.InvocationID
		}
		f.calls = append(f.calls, call)
	}()
	if err := f.enter(ctx, &call); err != nil {
		return State{}, err
	}
	if err := spec.Validate(); err != nil {
		return State{}, err
	}
	if f.units[spec.Unit] != nil {
		return State{}, ErrUnitExists
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
			return State{}, ErrReplyLost
		case outcomeFail:
			if outcome.err == nil {
				return State{}, errors.New("leadunit: scripted start failure")
			}
			return State{}, outcome.err
		}
	}
	procs := script.procs
	if len(procs) == 0 {
		procs = []ProcScript{{}}
	}
	u := f.newUnit(spec.Unit, spec.Description(), procs)
	f.units[spec.Unit] = u
	f.specs = append(f.specs, spec.clone())
	st = f.state(spec.Unit)
	if outcome != nil && outcome.kind == outcomeLoseReply {
		call.Invocation = st.InvocationID
		return State{}, ErrReplyLost
	}
	return st, nil
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
		f.calls = append(f.calls, call)
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
	defer func() {
		call.Err = err
		f.calls = append(f.calls, call)
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
	now := f.clock.Now()
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
					p.exitAt = now.Add(p.script.Delay)
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
	f.collect(u)
	return nil
}

// Empty implements [Manager]. It is true when the unit is not loaded, when it
// has another invocation than st, or when it has the invocation and control
// group of st and the group holds no process (which cannot be seen on a loaded
// unit of the fake, because it unloads an empty one). It is false with
// [ErrUnavailable] when scripted so, and false with an error when st names no
// unit or no invocation of a loaded unit, or a control group of another
// identity.
func (f *Fake) Empty(ctx context.Context, st State) (empty bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := Call{Method: MethodEmpty, Unit: st.Unit, Invocation: st.InvocationID}
	defer func() {
		call.Err = err
		call.Empty = empty
		f.calls = append(f.calls, call)
	}()
	if err := f.enter(ctx, &call); err != nil {
		return false, err
	}
	if err := ValidateUnitName(st.Unit); err != nil {
		return false, err
	}
	u := f.units[st.Unit]
	if u == nil {
		return true, nil
	}
	if st.InvocationID == "" {
		return false, ErrInvocationChanged
	}
	if st.InvocationID != u.invocation {
		return true, nil
	}
	if st.ControlGroupID != u.cgroupID || st.ControlGroup != u.cgroup {
		return false, ErrControlGroupMismatch
	}
	return len(u.procs) == 0, nil
}
