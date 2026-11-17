package plantest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

	"ataniq/internal/exchange/sandboxplan"
)

// Fake is a [sandboxplan.Planner] for tests. Set the fields before the first
// call. It is safe for concurrent calls. The zero value has no plans: each
// call that needs a field the test did not set fails.
//
// Each call records its request, also when it fails. The plans are checked
// with Validate, so a test cannot return a plan that the real code would
// refuse.
type Fake struct {
	// ServeErr, when set, is returned by every Serve call.
	ServeErr error
	// VerifyErr, when set, is returned by every Verify call.
	VerifyErr error

	// UploadPack is the serve plan's upload-pack argv. See
	// [GitUploadPack]. It is required.
	UploadPack []string
	// Repository is the serve plan's repository path. When empty it is
	// the request's Tree.
	Repository string
	// Measure is the serve plan's measure command. When its Argv is empty
	// the plan uses a command that prints MeasureBytes in the style of du.
	Measure sandboxplan.Command
	// MeasureBytes is the number the default measure command prints.
	MeasureBytes int64

	// VerifyPrefix is the verify plan's prefix, for example
	// []string{"/usr/bin/env"}. It is required.
	VerifyPrefix []string
	// VerifyEnv is the verify plan's environment.
	VerifyEnv []string
	// VerifyMirror, VerifyScratch and VerifyHome are the folders as seen
	// inside. Each one that is empty is the request's own path.
	VerifyMirror, VerifyScratch, VerifyHome string

	mu       sync.Mutex
	serves   []sandboxplan.ServeRequest
	verifies []sandboxplan.VerifyRequest
}

var _ sandboxplan.Planner = (*Fake)(nil)

// Serve records the request and returns the plan the fields describe.
func (f *Fake) Serve(r sandboxplan.ServeRequest) (sandboxplan.ServePlan, error) {
	f.mu.Lock()
	f.serves = append(f.serves, r)
	f.mu.Unlock()
	if f.ServeErr != nil {
		return sandboxplan.ServePlan{}, f.ServeErr
	}
	p := sandboxplan.ServePlan{
		UploadPack: append([]string(nil), f.UploadPack...),
		Repository: f.Repository,
		Measure: sandboxplan.Command{
			Argv: append([]string(nil), f.Measure.Argv...),
			Env:  append([]string(nil), f.Measure.Env...),
		},
	}
	if p.Repository == "" {
		p.Repository = r.Tree
	}
	if len(p.Measure.Argv) == 0 {
		p.Measure = MeasureCommand(f.MeasureBytes)
	}
	if err := p.Validate(); err != nil {
		return sandboxplan.ServePlan{}, fmt.Errorf("plantest: serve plan: %w", err)
	}
	return p, nil
}

// Verify records the request and returns the plan the fields describe.
func (f *Fake) Verify(r sandboxplan.VerifyRequest) (sandboxplan.VerifyPlan, error) {
	f.mu.Lock()
	f.verifies = append(f.verifies, r)
	f.mu.Unlock()
	if f.VerifyErr != nil {
		return sandboxplan.VerifyPlan{}, f.VerifyErr
	}
	p := sandboxplan.VerifyPlan{
		Prefix:  append([]string(nil), f.VerifyPrefix...),
		Env:     append([]string(nil), f.VerifyEnv...),
		Mirror:  f.VerifyMirror,
		Scratch: f.VerifyScratch,
		Home:    f.VerifyHome,
	}
	if p.Mirror == "" {
		p.Mirror = r.Mirror
	}
	if p.Scratch == "" {
		p.Scratch = r.Scratch
	}
	if p.Home == "" {
		p.Home = r.Home
	}
	if err := p.Validate(); err != nil {
		return sandboxplan.VerifyPlan{}, fmt.Errorf("plantest: verify plan: %w", err)
	}
	return p, nil
}

// ServeRequests returns a copy of the serve requests received, in order.
func (f *Fake) ServeRequests() []sandboxplan.ServeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sandboxplan.ServeRequest(nil), f.serves...)
}

// VerifyRequests returns a copy of the verify requests received, in order.
func (f *Fake) VerifyRequests() []sandboxplan.VerifyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sandboxplan.VerifyRequest(nil), f.verifies...)
}

// MeasureCommand returns a command that prints n and a path in the style of
// du, for a [Fake]'s measure field.
func MeasureCommand(n int64) sandboxplan.Command {
	return sandboxplan.Command{
		Argv: []string{"/bin/sh", "-c", `printf '%s\t%s\n' "$0" "$1"`, strconv.FormatInt(n, 10), "/objects"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	}
}

// GitUploadPack returns the argv that runs the host's git-upload-pack
// without a sandbox, as an absolute path. It looks on PATH first, then in
// the directory that git --exec-path names.
func GitUploadPack() ([]string, error) {
	if p, err := exec.LookPath("git-upload-pack"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return []string{abs}, nil
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("plantest: neither git-upload-pack nor git found")
	}
	cmd := exec.Command(git, "--exec-path")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("plantest: git --exec-path: %w", err)
	}
	dir := string(out)
	for len(dir) > 0 && (dir[len(dir)-1] == '\n' || dir[len(dir)-1] == '\r') {
		dir = dir[:len(dir)-1]
	}
	p := filepath.Join(dir, "git-upload-pack")
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("plantest: %w", err)
	}
	return []string{p}, nil
}
