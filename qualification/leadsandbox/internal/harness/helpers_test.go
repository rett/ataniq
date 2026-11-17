//go:build linux && amd64

package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// run executes the harness against the fixture with the fake launcher and
// returns the outcome, the launcher and the canary the run made.
type result struct {
	out      *harness.Outcome
	err      error
	fake     *harness.FakeLauncher
	canary   *assert.Canary
	canaries int
}

func newFake(b harness.FakeBehavior) *harness.FakeLauncher {
	return &harness.FakeLauncher{Behavior: b, Versions: harness.ToolVersions{Kernel: "6.0-fake", Bwrap: "bubblewrap 0.0-fake", Pasta: "pasta fake"}}
}

// execute runs a real (not dry) attempt against the fake world.
func execute(t *testing.T, f *harnesstest.Fixture, b harness.FakeBehavior, tune func(*harness.Options)) *result {
	t.Helper()
	fake := newFake(b)
	return executeWith(t, f, fake, true, tune)
}

func executeWith(t *testing.T, f *harnesstest.Fixture, fake *harness.FakeLauncher, run bool, tune func(*harness.Options)) *result {
	t.Helper()
	cfg := f.Load()
	r := &result{fake: fake}
	opt := f.Options(fake)
	opt.Run = run
	opt.TimeLimit = 2 * time.Second
	opt.StopTermWait, opt.StopKillWait, opt.StopPoll = 50*time.Millisecond, 50*time.Millisecond, 2*time.Millisecond
	opt.NewCanary = func() (*assert.Canary, error) {
		c, err := assert.NewCanary()
		r.canary = c
		r.canaries++
		return c, err
	}
	if tune != nil {
		tune(&opt)
	}
	r.out, r.err = harness.Execute(context.Background(), cfg, opt)
	return r
}

// okProbe plays a probe that reports every inside check with the status,
// prints the scratch marker and exits with the code.
func okProbe(status assert.Status, failedNumber, code int) harness.FakeBehavior {
	return func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		_ = harness.WriteReportLine(req, harnesstest.ProbeLine(req.Plan.Digest, status, failedNumber))
		p.WriteTerminal([]byte(harness.ScratchMarker + "\r\n"))
		return harness.Exit{Code: code}
	}
}

func step(t *testing.T, o *harness.Outcome, name string) harness.StepRecord {
	t.Helper()
	for _, s := range o.Summary.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step %s", name)
	return harness.StepRecord{}
}

func wantStep(t *testing.T, o *harness.Outcome, name string, st harness.StepStatus) {
	t.Helper()
	if got := step(t, o, name); got.Status != st {
		t.Errorf("step %s is %s (%s), want %s", name, got.Status, got.Error, st)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasSignal(sigs []syscall.Signal, want syscall.Signal) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}

func mustNoErr(t *testing.T, r *result) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("Execute: %v", r.err)
	}
	if r.out == nil {
		t.Fatal("no outcome")
	}
}

var _ = profile.Claude
var _ = strings.Contains

func sha(t *testing.T, s string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
