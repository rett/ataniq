//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"time"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// System is every outside read and effect of the start sequence: what it asks
// of the host and what it does to it. The real one is NewSystem, over the
// packages of the lead sandbox; tests pass a fake. Run calls the methods in the
// order of the steps, each in its own step, and makes no other call to the host
// except for the canary root and marker checks and loading the instance
// identity from the state root during validation.
//
// The paths a method gets are the ones Run derived from the inputs; the folders
// of the report channel and of the bound files are made and removed by the
// methods that return them (OpenReport, MakeBound), by handle.
type System interface {
	// Getenv reads one variable of the environment. Only the root check of
	// canary mode uses it.
	Getenv(name string) string
	// Now is the clock of the outside checks.
	Now() time.Time
	// IDs are the user and group id of the calling user: the owner's, for the
	// "owner" identity, and the user the folders of the lead must belong to.
	IDs() (uid, gid uint32)
	// Username is the login name of the calling user, the user lingering is
	// asked about when the inputs name no owner.
	Username() (string, error)

	// CheckHost looks at the host for what the inputs name, before anything is
	// made (step validate-config): the state root and the runtime folder of
	// the role as real folders, the records folder of the layout, the git
	// identity folder and what it holds, the three executables (regular, not
	// writable by group or others, hashed), the seccomp file and the
	// managed-settings file against their digests. It returns the executables
	// with the SHA-256 it took.
	CheckHost(in Inputs) ([]ExeRecord, error)

	// ToolVersions asks each tool for its version and reads the kernel release
	// (step tool-versions). A tool that cannot be asked is recorded in Errors;
	// there is no error result.
	ToolVersions(ctx context.Context, tools ToolPaths) ToolVersions

	// CheckOwnClone is mountpoints.CheckOwnClone (step own-clone).
	CheckOwnClone(in profile.Inputs, uid uint32) (mountpoints.Report, error)
	// PrepareMountPoints makes the mount points and fills the git metadata
	// folder, mountpoints.Prepare with the statement that no lead has run
	// (step prepare).
	PrepareMountPoints(in profile.Inputs) error
	// CheckMountPoints is mountpoints.Check, the start walk of the design,
	// section 3.6 (steps check and recheck).
	CheckMountPoints(in profile.Inputs) (mountpoints.Report, error)

	// DeriveResolv derives the resolv.conf the sandbox gets from the host's
	// (step resolv-conf).
	DeriveResolv() (sandboxnet.Result, error)
	// MakeBound makes the folder of the files the plan binds, "bound" in the
	// runtime folder of the role, with the folder "spec" in it, both 0700 and
	// by handle (step resolv-conf). Nothing else may be in it.
	MakeBound(runtimeRoot, layoutRole string) (Bound, error)

	// ResolveSources is mountpoints.ResolveSources over the real file system
	// (steps resolve-sources and recheck).
	ResolveSources(in profile.Inputs) (mountpoints.Resolution, error)
	// HostHas says whether the host has a path, for the optional symbolic-link
	// rows of the bind table: a no-follow stat.
	HostHas(path string) bool

	// OpenReport makes the report folder in the runtime folder of the role
	// (0700, by handle, holding nothing else) and the FIFO in it, and opens the
	// reading end first, then a write end of its own (step report-channel). The
	// FIFO's path is ReportFIFO(runtimeRoot, layoutRole).
	OpenReport(runtimeRoot, layoutRole string) (ReportChannel, error)
	// NewCanary makes the target of assertion 3 (step canary).
	NewCanary() (*assert.Canary, error)

	// CheckBindSources is mountpoints.CheckBindSources, the last check before
	// the launch (rule G1; step check-bind-sources).
	CheckBindSources(rows []mountpoints.ResolvedSource) mountpoints.Report

	// Assert is what the checks of the spec and the outside checks read through
	// (steps write-spec and outside-checks): identities, folder names, the
	// sysctl.
	Assert() assert.System
	// RoleLock is the lock the holder holds on its role. Run does not take it:
	// it asks the lock whether this process holds it, for assertion 9. Run
	// asks it with the layout role (Inputs.LayoutRole, for example
	// "throwaway-one-lead"), not with the role text of the daemon
	// ("throwaway-one/lead"): an adapter that compares the argument with its
	// own role text must compare it with the layout role, or assertion 9 fails.
	RoleLock() assert.RoleLock
	// Lingering reads whether lingering is on, from the login manager's state,
	// for assertion 9.
	Lingering() assert.Lingering
}

// Bound is the folder of the files the plan binds read-only into the sandbox:
// resolv.conf and the folder with the spec. Close removes what was made.
type Bound interface {
	// WriteResolv creates resolv.conf (mode 0644), exclusively.
	WriteResolv(content []byte) error
	// WriteSpec creates spec.json in the spec folder (mode 0600), exclusively.
	WriteSpec(content []byte) error
	// Close removes the files and the folders. It is safe to call twice.
	Close() error
}

// ReportChannel is the report FIFO the probe's one report line comes through.
type ReportChannel interface {
	// ReadLine waits for the one report line and parses it
	// (assert.ReadProbeReport). It returns an error when the context ends, when
	// exited is closed (the first process has gone) and no line came, and when
	// the line is refused. It does not wait for end-of-file, which does not
	// come while the sandbox lives.
	ReadLine(ctx context.Context, exited <-chan struct{}) (assert.Report, error)
	// Trailing says whether bytes followed the line: nil when nothing did, an
	// error when a byte, or an unanswerable question, did (assert.CheckNoMore).
	// It is asked once, without waiting, after ReadLine has returned a line.
	Trailing() error
	// Close ends the reading, closes both ends, and removes the FIFO and its
	// folder. It is safe to call twice.
	Close() error
}

// ToolPaths are the executables to ask for a version.
type ToolPaths struct {
	Pasta string
	Bwrap string
}

// ToolVersions are the versions recorded before a launch: the first line each
// tool printed and the kernel release, escaped and bounded.
type ToolVersions struct {
	Kernel string   `json:"kernel"`
	Bwrap  string   `json:"bwrap"`
	Pasta  string   `json:"pasta"`
	Errors []string `json:"errors,omitempty"`
}

// ExeRecord is one executable of the plan with the SHA-256 taken of it.
type ExeRecord struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Launcher starts the plan's argument vector and returns the child. The holder
// passes one that starts it under its pseudo-terminal, in a session of its own
// and with no controlling terminal (the probe inside takes the terminal), with
// an empty environment: no variable of the holder's reaches pasta, and the plan
// sets everything inside. The launcher opens no descriptor for the plan; the
// plan's pass-through step does. A launcher that starts nothing is a fake of a
// test: nothing in this package starts bubblewrap, pasta or a namespace itself.
type Launcher interface {
	Launch(argv []string) (Child, error)
}

// Child is the first process of a started plan with its process group and
// what it left. *ptyhost.Host is one; so is a fake.
type Child interface {
	// Exited is closed when the first process has exited.
	Exited() <-chan struct{}
	// Stop ends the child and what it left: SIGTERM to the group, a wait of up
	// to grace, then SIGKILL, repeated for a bounded time. It returns when the
	// tree is gone or that time has passed; TreeGone says which.
	Stop(grace time.Duration)
	// TreeGone says whether the first process has exited and nothing is left
	// of what it started, with the proof or the reason it is not.
	TreeGone() (gone bool, proof string)
	// Close releases the terminal and reaps the first process. It is safe to
	// call twice.
	Close() error
}

// Started is a start that succeeded: what the holder needs to serve, and what
// it records.
type Started struct {
	// Child is the launched first process. The holder owns it from here: it
	// serves it and stops it. Run has closed everything else it opened.
	Child Child
	// ProfileVersion and ProfileDigest are the profile's version (profile.Version)
	// and the digest of this plan, which the holder compares with its pin.
	ProfileVersion int
	ProfileDigest  string
	// Waived says assertion 10 was excused, and Waiver is then
	// canaryroot.WaiverText, in full. Without a waiver both are zero.
	Waived bool
	Waiver string
	// Results are the result of each assertion, as the probe reported its
	// inside half and the outside checks made the rest: the true results, the
	// result of assertion 10 included (the waiver does not change it).
	Results []assert.Result
	// Steps are the steps reached, in order.
	Steps []StepRecord
	// Executables are the executables with the SHA-256 taken, and Versions the
	// tool versions; Plan is the plan that was launched (its Argv is the
	// command line).
	Executables []ExeRecord
	Versions    ToolVersions
	Plan        *plan.Plan
	// Notes are what the sequence noticed and did not refuse on: the note of
	// the bubblewrap release check when the version line does not give a
	// release (so it is not known whether bubblewrap has --bind-fd). The
	// holder records them with the start.
	Notes []string
}
