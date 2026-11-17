//go:build linux && amd64

package harness

import (
	"context"
	"syscall"

	"ataniq/internal/leadsandbox/plan"
)

// Launcher starts the plan and records the versions of the tools. The one
// real implementation is [ExecLauncher]; [FakeLauncher] is for tests. The
// launcher decides nothing: it starts what the plan says and reports what
// happened.
type Launcher interface {
	// Launch starts plan.Argv with a pseudo-terminal as its standard streams,
	// in a session and process group of its own, and returns at once. The
	// process has no controlling terminal, neither the coordinator's nor the
	// pseudo-terminal: it is not made the controlling terminal here, the probe
	// inside does it (the plan's SetControllingTerminal is false).
	Launch(req LaunchRequest) (Process, error)
	// ToolVersions asks each tool for its version and reads the kernel
	// release. A tool that cannot be asked is recorded in Errors; no error is
	// returned, because a missing version is a fact and not a reason to stop.
	ToolVersions(ctx context.Context, tools ToolPaths) ToolVersions
}

// LaunchRequest is what Launch starts.
type LaunchRequest struct {
	// Plan is the verified plan; its Argv is the command line. The launcher
	// opens no descriptor for it: the plan's pass-through step does.
	Plan *plan.Plan
	// Env is the environment of the first process (pasta). The harness gives
	// none: the plan sets everything inside, and no variable of the harness's
	// own environment reaches the sandbox or its tools. A nil Env is an empty
	// environment, never the caller's.
	Env []string
	// Rows and Cols are the size of the pseudo-terminal.
	Rows, Cols uint16
}

// ToolPaths are the executables to ask for a version.
type ToolPaths struct {
	Pasta string
	Bwrap string
}

// ToolVersions are the versions recorded before a launch. Each is the first
// line the tool printed, escaped and bounded.
type ToolVersions struct {
	Kernel string   `json:"kernel"`
	Bwrap  string   `json:"bwrap"`
	Pasta  string   `json:"pasta"`
	Errors []string `json:"errors,omitempty"`
}

// MaxTerminalBytes is how much terminal output a process keeps; the rest is
// counted.
const MaxTerminalBytes = 1 << 20

// Exit is how a process ended.
type Exit struct {
	// Code is the exit code, or -1 when a signal ended the process.
	Code int `json:"code"`
	// Signal names the signal that ended it (for example "SIGKILL"), or "".
	Signal string `json:"signal,omitempty"`
	// Err is the error of the wait, if any.
	Err string `json:"error,omitempty"`
}

// TerminalOutput is what the process wrote to the pseudo-terminal.
type TerminalOutput struct {
	// Data is at most MaxTerminalBytes of the output, from the start.
	Data []byte
	// Total is every byte the process wrote, Dropped the part not in Data.
	Total   int64
	Dropped int64
}

// Process is a started process group.
type Process interface {
	// PID is the process id of the first process, which is also the id of
	// the group.
	PID() int
	// Done is closed when the first process has exited.
	Done() <-chan struct{}
	// Exit is how it ended; valid after Done is closed.
	Exit() Exit
	// Signal sends a signal to the process group. delivered is false when no
	// process is left in the group; sig 0 only asks.
	Signal(sig syscall.Signal) (delivered bool, err error)
	// Terminal is a snapshot of the captured output. After Done it waits a
	// bounded time for the last of the output.
	Terminal() TerminalOutput
	// Close releases the pseudo-terminal. It is safe to call twice.
	Close() error
}
