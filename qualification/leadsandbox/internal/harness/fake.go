//go:build linux && amd64

package harness

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"

	"ataniq/internal/leadsandbox/plan"
)

// FakeBehavior plays the part of the launched process group. It runs in its
// own goroutine, and the process counts as exited, with the Exit it returns,
// when it returns.
type FakeBehavior func(req LaunchRequest, p *FakeProcess) Exit

// FakeLauncher is a [Launcher] for tests: it starts nothing. Each Launch
// records the request and runs the behavior in a goroutine. It is safe for
// concurrent use.
type FakeLauncher struct {
	// Behavior is what a launched process does. Nil means: exit 0 at once.
	Behavior FakeBehavior
	// LaunchErr, when set, makes Launch fail (after recording the call).
	LaunchErr error
	// Versions is what ToolVersions returns.
	Versions ToolVersions

	mu       sync.Mutex
	launches []LaunchRequest
	versions int
	procs    []*FakeProcess
}

var _ Launcher = (*FakeLauncher)(nil)

// Launch implements Launcher.
func (f *FakeLauncher) Launch(req LaunchRequest) (Process, error) {
	f.mu.Lock()
	f.launches = append(f.launches, req)
	f.mu.Unlock()
	if f.LaunchErr != nil {
		return nil, f.LaunchErr
	}
	p := &FakeProcess{
		pid: 4242, done: make(chan struct{}), sigs: make(chan syscall.Signal, 8),
		buf: newBoundedBuffer(MaxTerminalBytes),
	}
	f.mu.Lock()
	f.procs = append(f.procs, p)
	f.mu.Unlock()
	behave := f.Behavior
	go func() {
		var e Exit
		if behave != nil {
			e = behave(req, p)
		}
		p.mu.Lock()
		p.exit = e
		p.finished = true
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// ToolVersions implements Launcher; it counts the call and returns Versions.
func (f *FakeLauncher) ToolVersions(context.Context, ToolPaths) ToolVersions {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions++
	return f.Versions
}

// Launches is the number of Launch calls, failed ones included.
func (f *FakeLauncher) Launches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.launches)
}

// Requests is a copy of the recorded launch requests.
func (f *FakeLauncher) Requests() []LaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LaunchRequest(nil), f.launches...)
}

// VersionCalls is the number of ToolVersions calls.
func (f *FakeLauncher) VersionCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versions
}

// Calls is every call of any kind, for a test that wants none.
func (f *FakeLauncher) Calls() int { return f.Launches() + f.VersionCalls() }

// Process returns the n-th process the fake started.
func (f *FakeLauncher) Process(n int) *FakeProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[n]
}

// FakeProcess is the process of a [FakeLauncher].
type FakeProcess struct {
	pid  int
	done chan struct{}
	sigs chan syscall.Signal
	buf  *boundedBuffer

	mu       sync.Mutex
	exit     Exit
	finished bool
	signals  []syscall.Signal
	// ignoreTerm makes SIGTERM leave the behavior running.
	ignoreTerm bool
	closed     int
}

var _ Process = (*FakeProcess)(nil)

// PID implements Process.
func (p *FakeProcess) PID() int { return p.pid }

// Done implements Process.
func (p *FakeProcess) Done() <-chan struct{} { return p.done }

// Exit implements Process.
func (p *FakeProcess) Exit() Exit {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

// Signal implements Process: it records the signal and passes SIGTERM (unless
// IgnoreTerm was called) and SIGKILL to the behavior through Signals. The
// group is gone once the behavior has returned.
func (p *FakeProcess) Signal(sig syscall.Signal) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return false, nil
	}
	if sig == 0 {
		return true, nil
	}
	p.signals = append(p.signals, sig)
	if sig == syscall.SIGKILL || (sig == syscall.SIGTERM && !p.ignoreTerm) {
		select {
		case p.sigs <- sig:
		default:
		}
	}
	return true, nil
}

// Terminal implements Process.
func (p *FakeProcess) Terminal() TerminalOutput { return p.buf.snapshot() }

// Close implements Process.
func (p *FakeProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed++
	return nil
}

// Signals is the signals received so far, in order (sig 0 questions are not
// recorded).
func (p *FakeProcess) Signals() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]syscall.Signal(nil), p.signals...)
}

// Closed is the number of Close calls.
func (p *FakeProcess) Closed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Stop delivers the signals the behavior should end on: SIGTERM (unless
// ignored) and SIGKILL.
func (p *FakeProcess) Stop() <-chan syscall.Signal { return p.sigs }

// IgnoreTerm makes the process survive SIGTERM, as a stuck one would.
func (p *FakeProcess) IgnoreTerm() {
	p.mu.Lock()
	p.ignoreTerm = true
	p.mu.Unlock()
}

// WriteTerminal adds to the captured terminal output.
func (p *FakeProcess) WriteTerminal(b []byte) { p.buf.Write(b) }

// ReportPath finds the report channel in the request: the path of the
// plan's write-only file at the report descriptor.
func ReportPath(req LaunchRequest) (string, error) {
	if req.Plan == nil {
		return "", errors.New("no plan")
	}
	for _, f := range req.Plan.Files {
		if f.Purpose == plan.PurposeReport {
			return f.Path, nil
		}
	}
	return "", errors.New("the plan has no report file")
}

// WriteReportLine opens the report channel of the request write-only, as the
// plan's pass-through step does, writes the bytes and closes it. It is what a
// fake probe does.
func WriteReportLine(req LaunchRequest, line []byte) error {
	path, err := ReportPath(req)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
