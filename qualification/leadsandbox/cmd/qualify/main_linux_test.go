//go:build linux && amd64

package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

type world struct {
	f      *harnesstest.Fixture
	fake   *harness.FakeLauncher
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newWorld(t *testing.T, b harness.FakeBehavior) *world {
	t.Helper()
	f := harnesstest.New(t, profile.Claude)
	f.Config.NoLeadHasRun = true
	return &world{f: f, fake: &harness.FakeLauncher{Behavior: b}}
}

// run runs the command against the fake world. The seams are the fixture's;
// terminal says whether standard input is a terminal.
func (w *world) run(t *testing.T, terminal bool, limit time.Duration, args ...string) int {
	t.Helper()
	d := deps{
		launcher:        w.fake,
		stdinIsTerminal: func() bool { return terminal },
		options: func(o *harness.Options) {
			base := w.f.Options(w.fake)
			base.Run, base.Unattended, base.StdinIsTerminal = o.Run, o.Unattended, o.StdinIsTerminal
			base.TimeLimit = limit
			base.StopTermWait, base.StopKillWait, base.StopPoll = 50*time.Millisecond, 50*time.Millisecond, 2*time.Millisecond
			*o = base
		},
	}
	return run(context.Background(), args, &w.stdout, &w.stderr, d)
}

func probe(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
	_ = harness.WriteReportLine(req, harnesstest.ProbeLine(req.Plan.Digest, assert.StatusOK, 0))
	return harness.Exit{}
}

func TestUsageErrors(t *testing.T) {
	w := newWorld(t, probe)
	cfg := w.f.WriteConfig()
	for _, args := range [][]string{
		nil,
		{"--run"},
		{"--config"},
		{"--config", cfg, "extra"},
		{"--config", cfg, "--unknown"},
		{"--config", cfg, "--unattended"},
		{"--config", w.f.Base + "/missing.json"},
	} {
		w.stdout.Reset()
		w.stderr.Reset()
		if code := w.run(t, true, time.Second, args...); code != exitUsage {
			t.Errorf("%v: exit %d, stderr %q", args, code, w.stderr.String())
		}
		if w.stderr.Len() == 0 || w.stdout.Len() != 0 {
			t.Errorf("%v: stderr %q stdout %q", args, w.stderr.String(), w.stdout.String())
		}
	}
	if w.fake.Calls() != 0 {
		t.Errorf("the launcher was called %d times", w.fake.Calls())
	}
}

func TestDryRunPrintsThePlanAndDigestAndStartsNothing(t *testing.T) {
	w := newWorld(t, probe)
	code := w.run(t, true, time.Second, "--config", w.f.WriteConfig())
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q, stdout %q", code, w.stderr.String(), w.stdout.String())
	}
	out := w.stdout.String()
	if !strings.Contains(out, "dry run: nothing was started") || !regexp.MustCompile(`(?m)^digest: [0-9a-f]{64}$`).MatchString(out) {
		t.Errorf("stdout %q", out)
	}
	for _, want := range []string{`"argv"`, `"--die-with-parent"`, `"notes"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan is missing %s", want)
		}
	}
	if w.fake.Calls() != 0 {
		t.Errorf("the dry run called the launcher %d times", w.fake.Calls())
	}
}

func TestRunWithoutTheConfigurationKeyIsRefused(t *testing.T) {
	w := newWorld(t, probe)
	code := w.run(t, true, time.Second, "--config", w.f.WriteConfig(), "--run")
	if code != exitUsage || !strings.Contains(w.stderr.String(), "coordinator_reviewed") {
		t.Fatalf("exit %d, stderr %q", code, w.stderr.String())
	}
	if w.fake.Calls() != 0 || w.stdout.Len() != 0 {
		t.Errorf("calls %d, stdout %q", w.fake.Calls(), w.stdout.String())
	}
	if got := w.f.Names(w.f.Campaign); len(got) != 0 {
		t.Errorf("campaign: %v", got)
	}
}

func TestRunNeedsATerminalUnlessUnattended(t *testing.T) {
	w := newWorld(t, probe)
	w.f.Config.CoordinatorReviewed = true
	cfg := w.f.WriteConfig()
	if code := w.run(t, false, time.Second, "--config", cfg, "--run"); code != exitUsage || !strings.Contains(w.stderr.String(), "terminal") {
		t.Fatalf("exit %d, stderr %q", code, w.stderr.String())
	}
	if w.fake.Calls() != 0 {
		t.Fatal("launched")
	}
	w.stderr.Reset()
	if code := w.run(t, false, time.Second, "--config", cfg, "--run", "--unattended"); code != exitOK {
		t.Fatalf("exit %d, stderr %q, stdout %q", code, w.stderr.String(), w.stdout.String())
	}
	if w.fake.Launches() != 1 || !strings.Contains(w.stdout.String(), "attempt folder: ") || !strings.Contains(w.stdout.String(), "report: line-read") {
		t.Errorf("launches %d, stdout %q", w.fake.Launches(), w.stdout.String())
	}
	// What the command says of a run is about steps and facts, never a verdict.
	lower := strings.ToLower(w.stdout.String() + w.stderr.String())
	for _, bad := range []string{"pass", "qualified", "supported"} {
		if strings.Contains(lower, bad) {
			t.Errorf("the output says %q: %q", bad, lower)
		}
	}
	if code := w.run(t, true, time.Second, "--config", cfg, "--run"); code != exitOK || w.fake.Launches() != 2 {
		t.Errorf("with a terminal: exit %d, %d launches", code, w.fake.Launches())
	}
}

func TestAStepThatTimesOutGivesTheStepExitStatus(t *testing.T) {
	w := newWorld(t, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		<-p.Stop()
		return harness.Exit{Code: -1, Signal: "SIGTERM"}
	})
	w.f.Config.CoordinatorReviewed = true
	code := w.run(t, true, 60*time.Millisecond, "--config", w.f.WriteConfig(), "--run")
	if code != exitStep {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, w.stdout.String(), w.stderr.String())
	}
	if !strings.Contains(w.stdout.String(), "step read-report: timeout") {
		t.Errorf("stdout %q", w.stdout.String())
	}
}

func TestAnExitStatusOfTheProbeIsNotAStepFailure(t *testing.T) {
	w := newWorld(t, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		_ = harness.WriteReportLine(req, harnesstest.ProbeLine(req.Plan.Digest, assert.StatusOK, 3))
		return harness.Exit{Code: 97}
	})
	w.f.Config.CoordinatorReviewed = true
	if code := w.run(t, true, time.Second, "--config", w.f.WriteConfig(), "--run"); code != exitOK {
		t.Fatalf("exit %d: the exit status says whether the harness did its work", code)
	}
	if !strings.Contains(w.stdout.String(), "code 97") {
		t.Errorf("stdout %q", w.stdout.String())
	}
}

func TestNoFolderForTheAttemptGivesTheEvidenceExitStatus(t *testing.T) {
	w := newWorld(t, probe)
	w.f.Config.CoordinatorReviewed = true
	cfg := w.f.WriteConfig()
	// A random source that repeats itself finds the folder of the first
	// attempt taken, 8 times over: the run has nowhere to write, and says so.
	repeat := func(n int) func(*harness.Options) {
		return func(o *harness.Options) {
			base := w.f.Options(w.fake)
			base.Run = true
			base.Rand = bytes.NewReader(bytes.Repeat([]byte{7}, 16*n))
			*o = base
		}
	}
	d := deps{launcher: w.fake, stdinIsTerminal: func() bool { return true }, options: repeat(1)}
	if code := run(context.Background(), []string{"--config", cfg, "--run"}, &w.stdout, &w.stderr, d); code != exitOK {
		t.Fatalf("first attempt: exit %d, stderr %q", code, w.stderr.String())
	}
	d.options = repeat(8)
	w.stderr.Reset()
	if code := run(context.Background(), []string{"--config", cfg, "--run"}, &w.stdout, &w.stderr, d); code != exitEvidence || !strings.Contains(w.stderr.String(), "no unused random name") {
		t.Errorf("second attempt: exit %d, stderr %q", code, w.stderr.String())
	}
}
