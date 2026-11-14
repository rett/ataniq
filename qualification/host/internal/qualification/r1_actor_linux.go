//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"io"
	"syscall"
	"time"
)

var errR1Deadline = errors.New("R1 deadline")

// Only an absent initial flat lookup is a wait. ENOENT from a lost directory,
// open, or post-read lookup is an integrity failure, not this sentinel.
var errR1Absent = errors.New("R1 fixed record absent")
var errR1EarlyFinish = errors.New("R1 finish before retained stage")

type r1Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}
type r1WallClock struct{}

func (r1WallClock) Now() time.Time        { return time.Now() }
func (r1WallClock) Sleep(d time.Duration) { time.Sleep(d) }

func r1Deadline(c r1Clock, overall time.Time, span time.Duration) time.Time {
	d := c.Now().Add(span)
	if overall.Before(d) {
		return overall
	}
	return d
}
func r1Expired(c r1Clock, d time.Time) bool { return !c.Now().Before(d) }
func r1Pause(c r1Clock, d time.Time) error {
	left := d.Sub(c.Now())
	if left <= 0 {
		return errR1Deadline
	}
	if left > 5*time.Millisecond {
		left = 5 * time.Millisecond
	}
	c.Sleep(left)
	if r1Expired(c, d) {
		return errR1Deadline
	}
	return nil
}

// Every retry consumes a deadline and a call budget, including EINTR and
// zero-progress I/O. The production endpoints use raw nonblocking syscalls.
// Timers cannot preempt a blocking regular-file/kernel syscall or scheduling.
func r1Read(c r1Clock, d time.Time, read func([]byte) (int, error), cap int) ([]byte, error) {
	b := make([]byte, cap+1)
	off := 0
	for calls := 0; calls < 8192; calls++ {
		if r1Expired(c, d) {
			return nil, errR1Deadline
		}
		n, err := read(b[off:])
		if n < 0 || n > len(b)-off {
			return nil, errors.New("invalid read count")
		}
		off += n
		if r1Expired(c, d) {
			return nil, errR1Deadline
		}
		if off > cap {
			return nil, errors.New("R1 frame cap")
		}
		if err == io.EOF {
			return b[:off], nil
		}
		if err != nil && err != syscall.EINTR && err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			return nil, err
		}
		if n == 0 || err != nil {
			if err := r1Pause(c, d); err != nil {
				return nil, err
			}
		}
	}
	return nil, errors.New("R1 read budget")
}

func r1Write(c r1Clock, d time.Time, write func([]byte) (int, error), b []byte) error {
	for calls := 0; calls < 8192 && len(b) > 0; calls++ {
		if r1Expired(c, d) {
			return errR1Deadline
		}
		n, err := write(b)
		if n < 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
		if r1Expired(c, d) {
			return errR1Deadline
		}
		if err != nil && err != syscall.EINTR && err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			return err
		}
		if n == 0 || err != nil {
			if err := r1Pause(c, d); err != nil {
				return err
			}
		}
	}
	if len(b) != 0 {
		return io.ErrShortWrite
	}
	return nil
}

type r1File struct {
	Data    []byte
	Subject string
}
type r1Control interface {
	Directory() R1ObjectIdentity
	Check() error
	Read(string, time.Time) (r1File, error)
	Close() error
}
type r1Memory interface {
	Size() int
	PageSize() int
	TouchPage(int) error
	Release() error
}
type r1Child interface {
	Ready(time.Time) (R1ActorIdentity, error)
	Alive() error
	End(string, time.Time) (R1ChildResult, error)
}
type r1Deps struct {
	clock    r1Clock
	read     func([]byte) (int, error)
	write    func([]byte) (int, error)
	open     func() (r1Control, error)
	self     func() (R1ActorIdentity, error)
	spawn    func() (r1Child, error)
	allocate func() (r1Memory, error)
	overall  time.Time
}

type r1Actor struct {
	d                       r1Deps
	overall                 time.Time
	store                   r1Control
	context                 r1File
	release                 *r1File
	finish                  *r1File
	binding                 R1Binding
	child                   r1Child
	childID                 *R1ActorIdentity
	childResult             R1ChildResult
	memory                  r1Memory
	touched, page, sequence int
	stage                   string
	failureCause            error
	cleanupFailed           bool
	memoryReleased          bool
	reapDeadline            time.Time
}

func (a *r1Actor) emit(e R1Evidence, deadline time.Time) error {
	e.R1Binding = a.binding
	var b bytes.Buffer
	if err := EncodeR1Evidence(&b, e); err != nil {
		return err
	}
	return r1Write(a.d.clock, deadline, a.d.write, b.Bytes())
}

func (a *r1Actor) check(deadline time.Time) error {
	if r1Expired(a.d.clock, deadline) {
		return errR1Deadline
	}
	if err := a.store.Check(); err != nil {
		return err
	}
	for _, entry := range []struct {
		name string
		old  *r1File
	}{{R1ContextName, &a.context}, {R1ReleaseName, a.release}, {R1FinishName, a.finish}} {
		name, old := entry.name, entry.old
		if old == nil {
			continue
		}
		f, err := a.store.Read(name, deadline)
		if err != nil {
			return err
		}
		if f.Subject != old.Subject || !bytes.Equal(f.Data, old.Data) {
			return errors.New("R1 control drift")
		}
	}
	if a.child != nil {
		if err := a.child.Alive(); err != nil {
			return err
		}
	}
	if r1Expired(a.d.clock, deadline) {
		return errR1Deadline
	}
	return nil
}

func (a *r1Actor) noFinish(deadline time.Time) error {
	_, err := a.store.Read(R1FinishName, deadline)
	if err == nil {
		return errR1EarlyFinish
	}
	if !errors.Is(err, errR1Absent) {
		return err
	}
	if r1Expired(a.d.clock, deadline) {
		return errR1Deadline
	}
	return nil
}

func (a *r1Actor) wait(name string, deadline time.Time) (r1File, error) {
	for {
		if err := a.check(deadline); err != nil {
			return r1File{}, err
		}
		if name == R1ReleaseName {
			if err := a.noFinish(deadline); err != nil {
				return r1File{}, err
			}
		}
		f, err := a.store.Read(name, deadline)
		if r1Expired(a.d.clock, deadline) {
			return r1File{}, errR1Deadline
		}
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, errR1Absent) {
			return r1File{}, err
		}
		if err := r1Pause(a.d.clock, deadline); err != nil {
			return r1File{}, err
		}
	}
}

func (a *r1Actor) fail(code string) error {
	if errors.Is(a.failureCause, errR1Deadline) {
		code = "deadline"
	}
	if errors.Is(a.failureCause, errR1Metadata) {
		code = "metadata-unsupported"
	}
	if errors.Is(a.failureCause, errR1EarlyFinish) {
		code = "early-finish"
	}
	cleanup := "not-allocated"
	if a.memoryReleased {
		cleanup = "released"
	}
	if a.cleanupFailed {
		cleanup = "failed"
	}
	if a.memory != nil {
		cleanup = "released"
		if err := a.memory.Release(); err != nil {
			cleanup = "failed"
		}
		a.memory = nil
	}
	result := a.childResult
	if a.child != nil && result.Reap != "reaped" {
		var err error
		result, err = a.child.End("abort", a.childDeadline())
		if err != nil && result.Reap != "reaped" {
			result.Termination, result.Reap, result.ExitCode = "unknown", "unknown", nil
			result.Signal = nil
		}
	}
	if result.Termination == "" {
		result = R1ChildResult{Termination: "unknown", Reap: "unknown", Token: "none"}
	}
	if result.Identity == nil {
		result.Identity = a.childID
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			cleanup = "failed"
		}
		a.store = nil
	}
	f := &R1Failure{Stage: a.stage, Error: code, TouchedBytes: a.touched, PageSize: a.page, Cleanup: cleanup, Child: result}
	if a.release != nil {
		f.ReleaseSHA256 = syntheticDigest(a.release.Data)
	}
	if a.finish != nil {
		f.FinishSHA256 = syntheticDigest(a.finish.Data)
	}
	e := R1Evidence{Stage: "failure", Sequence: a.sequence, Failure: f, R1Binding: a.binding}
	// One nonblocking best-effort write remains possible after a deadline. Never
	// retry a partial frame: an output failure cannot fabricate a complete record.
	var b bytes.Buffer
	if a.binding.valid() && EncodeR1Evidence(&b, e) == nil {
		_, _ = a.d.write(b.Bytes())
	}
	return errors.New("R1 actor incomplete")
}

// Kept separately so fixed error labels never leak filesystem/private errors.
func (a *r1Actor) failed(err error, code string) error { a.failureCause = err; return a.fail(code) }

func (a *r1Actor) childDeadline() time.Time {
	if a.reapDeadline.IsZero() {
		a.reapDeadline = r1Deadline(a.d.clock, a.overall, R1ReapWait)
	}
	return a.reapDeadline
}

// runR1 is deliberately private. The public entrypoint fixes every dependency
// and control path; tests replace seams without ever mapping pressure memory.
func runR1(d r1Deps) error {
	a := &r1Actor{d: d, overall: d.clock.Now().Add(R1MainLifetime), stage: "context"}
	if !d.overall.IsZero() && d.overall.Before(a.overall) {
		a.overall = d.overall
	}
	readyDeadline := r1Deadline(d.clock, a.overall, R1ReadyWait)
	data, err := r1Read(d.clock, readyDeadline, d.read, R1MaxFrame)
	if err != nil {
		if errors.Is(err, errR1Deadline) {
			return r1StartupFailure(d.write, "deadline", nil)
		}
		return r1StartupFailure(d.write, "input", nil)
	}
	r, err := DecodeR1Request(data)
	if err != nil {
		return r1StartupFailure(d.write, "input", nil)
	}
	a.store, err = d.open()
	if err != nil {
		return r1StartupFailure(d.write, "control", a.store)
	}
	// Establish context before any child creation or pressure allocation.
	a.context, err = a.store.Read(R1ContextName, readyDeadline)
	if err != nil {
		return r1StartupFailure(d.write, "control", a.store)
	}
	c, err := DecodeR1Context(a.context.Data)
	if err != nil || c.R1Request != r || c.RequestSHA256 != syntheticDigest(data) || c.ControlDirectory != a.store.Directory() {
		return r1StartupFailure(d.write, "binding", a.store)
	}
	a.binding = R1Binding{r, c.RequestSHA256, c.PlanSHA256, syntheticDigest(a.context.Data)}
	if err := a.check(readyDeadline); err != nil {
		return a.failed(err, "control")
	}
	if err := a.noFinish(readyDeadline); err != nil {
		return a.failed(err, "control")
	}
	// A release published before readiness is stale, even if its bytes match.
	if _, err := a.store.Read(R1ReleaseName, readyDeadline); !errors.Is(err, errR1Absent) {
		return a.failed(err, "control")
	}
	self, err := d.self()
	if err != nil || !self.valid() {
		return a.failed(err, "child")
	}
	if err := a.check(readyDeadline); err != nil {
		return a.failed(err, "control")
	}
	a.stage = "child-ready"
	a.child, err = d.spawn()
	if err != nil {
		return a.failed(err, "child")
	}
	childID, err := a.child.Ready(readyDeadline)
	if childID.valid() {
		a.childID = &childID
	}
	if err != nil || !childID.valid() || childID.PID == self.PID || childID.Executable != self.Executable || childID.PIDNamespace != self.PIDNamespace {
		return a.failed(err, "child")
	}
	if err := a.check(readyDeadline); err != nil {
		return a.failed(err, "child")
	}
	if err := a.noFinish(readyDeadline); err != nil {
		return a.failed(err, "control")
	}
	if _, err := a.store.Read(R1ReleaseName, readyDeadline); !errors.Is(err, errR1Absent) {
		return a.failed(err, "control")
	}
	a.stage = "ready"
	if err := a.emit(R1Evidence{Stage: "ready", Ready: &R1Ready{a.store.Directory(), self, childID}}, readyDeadline); err != nil {
		return a.failed(err, "io")
	}
	a.stage, a.sequence = "release", 1
	releaseDeadline := r1Deadline(d.clock, a.overall, R1ReleaseWait)
	f, err := a.wait(R1ReleaseName, releaseDeadline)
	if err != nil {
		return a.failed(err, "control")
	}
	release, err := DecodeR1Release(f.Data)
	if err != nil || release.R1Binding != a.binding {
		return a.failed(err, "binding")
	}
	releaseFile := f
	a.release = &releaseFile
	if err := a.check(releaseDeadline); err != nil {
		return a.failed(err, "control")
	}
	if err := a.noFinish(releaseDeadline); err != nil {
		return a.failed(err, "control")
	}
	sha := syntheticDigest(f.Data)
	a.stage = "pressure-start"
	if err := a.emit(R1Evidence{Stage: "pressure-start", Sequence: 1, Pressure: &R1Pressure{sha}}, releaseDeadline); err != nil {
		return a.failed(err, "io")
	}
	touchDeadline := r1Deadline(d.clock, a.overall, R1TouchWait)
	if err := a.check(touchDeadline); err != nil {
		return a.failed(err, "control")
	}
	if err := a.noFinish(touchDeadline); err != nil {
		return a.failed(err, "control")
	}
	a.stage = "allocate"
	a.memory, err = d.allocate()
	if err != nil {
		return a.failed(err, "allocation")
	}
	if a.memory == nil {
		return a.failed(nil, "allocation")
	}
	a.page = a.memory.PageSize()
	if a.memory.Size() != R1MemoryBytes || !r1Page(a.page) {
		a.page = 0
		return a.failed(nil, "allocation")
	}
	if err := a.check(touchDeadline); err != nil {
		return a.failed(err, "control")
	}
	if err := a.noFinish(touchDeadline); err != nil {
		return a.failed(err, "control")
	}
	a.stage = "touch"
	for off := 0; off < R1MemoryBytes; off += a.page {
		if r1Expired(d.clock, touchDeadline) {
			return a.failed(errR1Deadline, "touch")
		}
		if err := a.memory.TouchPage(off); err != nil {
			return a.failed(err, "touch")
		}
		a.touched += a.page
		if r1Expired(d.clock, touchDeadline) {
			return a.failed(errR1Deadline, "touch")
		}
	}
	if err := a.check(touchDeadline); err != nil {
		return a.failed(err, "control")
	}
	if err := a.noFinish(touchDeadline); err != nil {
		return a.failed(err, "control")
	}
	a.stage, a.sequence = "retained", 2
	if err := a.emit(R1Evidence{Stage: "retained", Sequence: 2, Touched: &R1Touched{sha, R1MemoryBytes, a.touched, a.page, true}}, touchDeadline); err != nil {
		return a.failed(err, "io")
	}
	a.stage = "finish"
	finishDeadline := r1Deadline(d.clock, a.overall, R1FinishWait)
	f, err = a.wait(R1FinishName, finishDeadline)
	if err != nil {
		return a.failed(err, "control")
	}
	finish, err := DecodeR1Finish(f.Data)
	if err != nil || finish.R1Binding != a.binding || finish.ReleaseSHA256 != sha {
		return a.failed(err, "binding")
	}
	a.finish = &f
	if err := a.check(finishDeadline); err != nil {
		return a.failed(err, "control")
	}
	// Release retained pages only after exact finish validation, or in fail().
	a.stage = "cleanup"
	if err := a.memory.Release(); err != nil {
		a.memory = nil
		a.cleanupFailed = true
		return a.failed(err, "cleanup")
	}
	a.memory = nil
	a.memoryReleased = true
	a.stage, a.sequence = "child-reap", 3
	a.childResult, err = a.child.End("finish", a.childDeadline())
	if err != nil || !a.childResult.valid() || a.childResult.Identity == nil || *a.childResult.Identity != childID || a.childResult.Reap != "reaped" || a.childResult.ExitCode == nil || *a.childResult.ExitCode != 0 || a.childResult.Token != "finish" {
		return a.failed(err, "child")
	}
	// The child is now actually reaped; do not use the living-child check.
	a.child = nil
	if err := a.check(a.overall); err != nil {
		return a.failed(err, "control")
	}
	if err := a.store.Close(); err != nil {
		a.store = nil
		a.cleanupFailed = true
		return a.failed(err, "cleanup")
	}
	a.store = nil
	a.stage = "completion"
	if err := a.emit(R1Evidence{Stage: "completion", Sequence: 3, Completion: &R1Completion{sha, syntheticDigest(f.Data), a.touched, true, a.childResult, "completed"}}, a.overall); err != nil {
		return a.failed(err, "io")
	}
	return nil
}

func r1StartupFailure(write func([]byte) (int, error), code string, store r1Control) error {
	// There is no authenticated context to echo at this point.
	stage := "context"
	if code == "input" || code == "deadline" {
		stage = "request"
	}
	cleanup := "not-opened"
	if store != nil {
		cleanup = "closed"
		if store.Close() != nil {
			cleanup = "failed"
		}
	}
	_, _ = write([]byte("{\"version\":1,\"kind\":\"r1-startup-failure\",\"stage\":\"" + stage + "\",\"error\":\"" + code + "\",\"cleanup\":\"" + cleanup + "\"}\n"))
	return errors.New("R1 startup incomplete")
}
