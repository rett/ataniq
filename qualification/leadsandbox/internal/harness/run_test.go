//go:build linux && amd64

package harness_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

func TestDryRunStartsNothing(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.NoLeadHasRun = true
	fake := newFake(okProbe(assert.StatusOK, 0, 0))
	r := executeWith(t, f, fake, false, nil)
	mustNoErr(t, r)
	if fake.Calls() != 0 {
		t.Fatalf("the dry run called the launcher %d times", fake.Calls())
	}
	if r.canaries != 0 {
		t.Errorf("the dry run opened %d canaries", r.canaries)
	}
	if !r.out.DryRun || r.out.StepFailed {
		t.Fatalf("dry run %v, step failed %v: %+v", r.out.DryRun, r.out.StepFailed, r.out.Summary.Steps)
	}
	if len(r.out.Plan.Digest) != 64 || len(r.out.Plan.Argv) == 0 {
		t.Fatalf("the dry run printed no plan: %+v", r.out.Plan)
	}
	// Only the plan, the summary and the harness's own folder are written;
	// no FIFO, no spec, nothing of a run.
	got := f.Names(r.out.Dir)
	want := []string{"bound", "plan.json", "summary.json"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("attempt folder holds %v, want %v", got, want)
	}
	for _, n := range []string{harness.StepVersions, harness.StepPrepare, harness.StepChannel, harness.StepCanary, harness.StepSpec, harness.StepRecheck, harness.StepLaunch} {
		wantStep(t, r.out, n, harness.StepSkipped)
	}
	for _, n := range []string{harness.StepValidate, harness.StepAttempt, harness.StepOwnClone, harness.StepResolv, harness.StepResolve, harness.StepBuild, harness.StepVerify, harness.StepWriteEvidence} {
		wantStep(t, r.out, n, harness.StepOK)
	}
	// Nothing was made in the scratch folders: the mount points are not there.
	if _, err := os.Lstat(f.Layout().Tree() + "/.git/info/exclude"); err != nil {
		t.Fatal(err)
	}
	cfgDir, _ := f.Layout().ConfigDir(profile.Claude)
	if _, err := os.Lstat(cfgDir + "/skills"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dry run made a mount point: %v", err)
	}
}

func TestDryRunChecksMountPointsWhenNoStatement(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	fake := newFake(nil)
	r := executeWith(t, f, fake, false, nil)
	mustNoErr(t, r)
	if fake.Calls() != 0 {
		t.Fatalf("launcher called %d times", fake.Calls())
	}
	// The mount points are not made and the configuration does not say a run
	// would make them: Check says so, and the later steps are skipped.
	wantStep(t, r.out, harness.StepCheck, harness.StepFailed)
	wantStep(t, r.out, harness.StepBuild, harness.StepSkipped)
	if !r.out.StepFailed {
		t.Error("StepFailed is not set")
	}
}

func TestRunWithoutCoordinatorStatementIsRefused(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.CoordinatorReviewed = false
	fake := newFake(okProbe(assert.StatusOK, 0, 0))
	r := executeWith(t, f, fake, true, nil)
	var gate *harness.GateError
	if !errors.As(r.err, &gate) || !strings.Contains(gate.Reason, "coordinator_reviewed") {
		t.Fatalf("error %v, want a gate error naming coordinator_reviewed", r.err)
	}
	if r.out != nil || fake.Calls() != 0 || r.canaries != 0 {
		t.Fatalf("outcome %v, %d launcher calls, %d canaries", r.out, fake.Calls(), r.canaries)
	}
	if got := f.Names(f.Campaign); len(got) != 0 {
		t.Errorf("the refused run made %v in the campaign", got)
	}
}

func TestRunNeedsATerminalUnlessUnattended(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.CoordinatorReviewed = true
	f.Config.NoLeadHasRun = true
	fake := newFake(okProbe(assert.StatusOK, 0, 0))
	r := executeWith(t, f, fake, true, func(o *harness.Options) { o.StdinIsTerminal = func() bool { return false } })
	var gate *harness.GateError
	if !errors.As(r.err, &gate) || !strings.Contains(gate.Reason, "terminal") {
		t.Fatalf("error %v, want a gate error naming the terminal", r.err)
	}
	if fake.Calls() != 0 || len(f.Names(f.Campaign)) != 0 {
		t.Fatal("the refused run did something")
	}
	r = executeWith(t, f, newFake(okProbe(assert.StatusOK, 0, 0)), true, func(o *harness.Options) {
		o.StdinIsTerminal = func() bool { return false }
		o.Unattended = true
	})
	mustNoErr(t, r)
	if r.fake.Launches() != 1 {
		t.Fatalf("an unattended run launched %d times", r.fake.Launches())
	}
}

func approved(f *harnesstest.Fixture) {
	f.Config.CoordinatorReviewed = true
	f.Config.NoLeadHasRun = true
}

func TestFullRunWritesEveryEvidenceFile(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := harnesstest.New(t, rt)
			approved(f)
			r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
			mustNoErr(t, r)
			o := r.out
			if o.StepFailed {
				t.Fatalf("a step failed: %+v", o.Summary.Steps)
			}
			for _, s := range o.Summary.Steps {
				if s.Name == harness.StepPlanted {
					// Case S21 was not requested: the step says so, and no
					// fact of it is in the evidence.
					if s.Status != harness.StepSkipped || !strings.Contains(s.Error, "not requested") || o.Summary.PlantedPaths != nil {
						t.Errorf("step %s: %+v (planted paths %+v)", s.Name, s, o.Summary.PlantedPaths)
					}
					continue
				}
				if s.Status != harness.StepOK {
					t.Errorf("step %s is %s: %s", s.Name, s.Status, s.Error)
				}
			}
			if got := f.Names(o.Dir); strings.Join(got, ",") != "bound,outside.json,plan.json,report.json,spec.json,summary.json,terminal.log" {
				t.Fatalf("attempt folder holds %v", got)
			}
			for _, n := range []string{"plan.json", "spec.json", "report.json", "outside.json", "terminal.log", "summary.json"} {
				st, err := os.Lstat(o.Dir + "/" + n)
				if err != nil {
					t.Fatal(err)
				}
				if st.Mode() != 0o600 {
					t.Errorf("%s has mode %v, want 0600", n, st.Mode())
				}
			}
			if st, _ := os.Lstat(o.Dir); st.Mode().Perm() != 0o700 {
				t.Errorf("attempt folder has mode %v", st.Mode())
			}
			if filepath.Dir(o.Dir) != f.Campaign || len(o.Attempt) != 32 {
				t.Errorf("attempt %q at %q", o.Attempt, o.Dir)
			}
			// The launcher got the verified plan, an empty environment and the
			// terminal size of the configuration.
			reqs := r.fake.Requests()
			if len(reqs) != 1 || reqs[0].Plan == nil || reqs[0].Env != nil || reqs[0].Rows != 24 || reqs[0].Cols != 80 {
				t.Fatalf("launch requests: %+v", reqs)
			}
			if reqs[0].Plan.SetControllingTerminal {
				t.Error("the plan sets a controlling terminal")
			}
			if r.fake.VersionCalls() != 1 || o.Summary.Versions.Kernel != "6.0-fake" {
				t.Errorf("versions: %+v (%d calls)", o.Summary.Versions, r.fake.VersionCalls())
			}
			// The report is the probe's own, and the outside results join it.
			var rep harness.ReportEvidence
			readJSON(t, o.Dir+"/report.json", &rep)
			if rep.Outcome != harness.ReportLineRead || len(rep.Results) != 9 || rep.DigestMatches == nil || !*rep.DigestMatches {
				t.Errorf("report: %+v", rep)
			}
			var out harness.OutsideEvidence
			readJSON(t, o.Dir+"/outside.json", &out)
			byID := map[string]assert.Status{}
			for _, res := range out.Results {
				byID[res.ID] = res.Status
			}
			for id, want := range map[string]assert.Status{
				"holder-hidden.outside": assert.StatusOK, "host-loopback-unreachable.outside": assert.StatusOK,
				"tiocsti-blocked.outside": assert.StatusOK, "tree-git-readonly.outside": assert.StatusOK,
				"role-lock-lingering": assert.StatusNotChecked, "qualification-record": assert.StatusNotChecked,
			} {
				if byID[id] != want {
					t.Errorf("outside result %s is %q, want %q", id, byID[id], want)
				}
			}
			if len(out.Results) != 6 {
				t.Errorf("%d outside results, want 6: %+v", len(out.Results), out.Results)
			}
			// The outside half of assertion 7 is the check right before the
			// launch, and it says on what the absence of a previous instance
			// rests.
			for _, res := range out.Results {
				if res.ID == "tree-git-readonly.outside" && (res.Number != 7 || !strings.Contains(res.Detail, "step "+harness.StepRecheck) || !strings.Contains(res.Detail, "no_lead_has_run")) {
					t.Errorf("the outside half of assertion 7: %+v", res)
				}
			}
			if out.Canary.TCPHits+out.Canary.AbstractHits != 0 || out.Canary.Stopped {
				t.Errorf("canary: %+v", out.Canary)
			}
			// Each requested case lists the facts it speaks to.
			cases := map[string][]string{}
			for _, c := range o.Summary.Cases {
				for _, res := range c.Results {
					cases[c.Case] = append(cases[c.Case], res.ID+"="+string(res.Status))
				}
			}
			if !contains(cases["G1"], "bind-sources-no-symbolic-link=ok") || !contains(cases["G1"], "no-bind-source-in-read-write-source=ok") {
				t.Errorf("G1: %v", cases["G1"])
			}
			if !contains(cases["S11"], "qualification-record=not-checked") {
				t.Errorf("S11: %v", cases["S11"])
			}
			if len(o.Summary.Cases) != 7 || len(cases["S1"]) != 4 || len(cases["S2"]) != 2 || len(cases["S7"]) != 2 {
				t.Errorf("cases: %+v", o.Summary.Cases)
			}
			// S1 lists the result of assertion 11, the four parent folders.
			if !contains(cases["S1"], "parent-folders=ok") {
				t.Errorf("S1: %v", cases["S1"])
			}
			if o.Summary.Terminal.MarkerSeen == nil || !*o.Summary.Terminal.MarkerSeen {
				t.Errorf("terminal: %+v", o.Summary.Terminal)
			}
			if data, _ := os.ReadFile(o.Dir + "/terminal.log"); !strings.Contains(string(data), harness.ScratchMarker) {
				t.Errorf("terminal.log: %q", data)
			}
			if len(o.Summary.Executables) != 3 || len(o.Summary.Executables[0].SHA256) != 64 {
				t.Errorf("executables: %+v", o.Summary.Executables)
			}
			if o.Summary.Exit != (harness.ExitEvidence{Launched: true, Exited: true}) {
				t.Errorf("exit: %+v", o.Summary.Exit)
			}
			if o.Summary.Stop.State != harness.StopNothingLeft {
				t.Errorf("stop: %+v", o.Summary.Stop)
			}
			// Cleaned up: the FIFO is gone, the canary is closed, the terminal
			// of the fake was released.
			if _, err := os.Lstat(o.Dir + "/channel/report.fifo"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the report FIFO is still there: %v", err)
			}
			if !r.canary.Stopped() {
				t.Error("the canary is still listening")
			}
			if r.fake.Process(0).Closed() != 1 {
				t.Errorf("the process was closed %d times", r.fake.Process(0).Closed())
			}
			// The mount points were made, since the configuration said no lead
			// had run.
			cfgDir, _ := f.Layout().ConfigDir(rt)
			if st, err := os.Lstat(cfgDir + "/skills"); err != nil || !st.IsDir() {
				t.Errorf("the skills mount point: %v", err)
			}
			if o.Plan.Digest == "" || o.Plan.Digest != o.Summary.ProfileDigest {
				t.Errorf("digests: %q %q", o.Plan.Digest, o.Summary.ProfileDigest)
			}
		})
	}
}

func TestFullRunWithSeccompFile(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	bpf := f.Owned + "/filter.bpf"
	f.Write(bpf, "reviewed-filter-bytes", 0o644)
	f.Config.Sources.Seccomp = &harness.Seccomp{File: bpf, SHA256: sha(t, "reviewed-filter-bytes"), ReviewedBPF: true}
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	var seccomp bool
	for _, fl := range r.out.Plan.Files {
		seccomp = seccomp || (fl.Purpose == "seccomp" && fl.FD == harness.SeccompFD && fl.Mode == "read" && fl.Path == bpf)
	}
	if !seccomp {
		t.Errorf("the plan does not open the seccomp file: %+v", r.out.Plan.Files)
	}
}

func TestProbeFailedReportIsRecordedAsRead(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 3, 97), nil)
	mustNoErr(t, r)
	o := r.out
	wantStep(t, o, harness.StepReadReport, harness.StepOK)
	wantStep(t, o, harness.StepWaitExit, harness.StepOK)
	wantStep(t, o, harness.StepOutside, harness.StepOK)
	if o.Summary.Exit.Code != 97 || o.Summary.Report != harness.ReportLineRead {
		t.Errorf("exit %+v, report %s", o.Summary.Exit, o.Summary.Report)
	}
	var rep harness.ReportEvidence
	readJSON(t, o.Dir+"/report.json", &rep)
	failed := 0
	for _, res := range rep.Results {
		if res.Status == assert.StatusFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("failed results: %d in %+v", failed, rep.Results)
	}
	// The failed result shows under the case it speaks to, and nothing in the
	// evidence turns it into a verdict.
	for _, c := range o.Summary.Cases {
		if c.Case == "S2" && !contains(idsWithStatus(c.Results, assert.StatusFailed), "host-loopback-unreachable") {
			t.Errorf("S2: %+v", c)
		}
	}
	if r.fake.Process(0).Closed() != 1 || !r.canary.Stopped() {
		t.Error("not cleaned up")
	}
}

func TestProbeGarbageIsRecordedAsRefused(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		_ = harness.WriteReportLine(req, []byte("this is not a report line \x1b[31m\n"))
		return harness.Exit{Code: 97}
	}, nil)
	mustNoErr(t, r)
	o := r.out
	wantStep(t, o, harness.StepReadReport, harness.StepFailed)
	wantStep(t, o, harness.StepWaitExit, harness.StepOK)
	wantStep(t, o, harness.StepOutside, harness.StepOK)
	if o.Summary.Report != harness.ReportRefused {
		t.Fatalf("report outcome %s", o.Summary.Report)
	}
	var rep harness.ReportEvidence
	readJSON(t, o.Dir+"/report.json", &rep)
	if rep.Detail == "" || !strings.Contains(rep.LineExcerpt, `\u001b`) || strings.ContainsRune(rep.LineExcerpt, 0x1b) {
		t.Errorf("the refused line is not escaped: %+v", rep)
	}
	if len(rep.Results) != 0 {
		t.Errorf("results of a refused line: %+v", rep.Results)
	}
	if !o.StepFailed || r.fake.Process(0).Closed() != 1 || !r.canary.Stopped() {
		t.Error("not recorded or not cleaned up")
	}
}

func TestProbeNeverWritesHitsTheTimeLimit(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		<-p.Stop()
		return harness.Exit{Code: -1, Signal: "SIGTERM"}
	}, func(o *harness.Options) { o.TimeLimit = 80 * time.Millisecond })
	mustNoErr(t, r)
	o := r.out
	wantStep(t, o, harness.StepReadReport, harness.StepTimeout)
	wantStep(t, o, harness.StepWaitExit, harness.StepTimeout)
	wantStep(t, o, harness.StepOutside, harness.StepOK)
	wantStep(t, o, harness.StepStop, harness.StepOK)
	if o.Summary.Report != harness.ReportTimeLimit || o.Summary.Exit.Exited || o.Summary.Exit.Code != -1 {
		t.Errorf("report %s, exit %+v", o.Summary.Report, o.Summary.Exit)
	}
	// The stop ended it, and the evidence says how.
	if end := o.Summary.Exit.AfterStop; end == nil || end.Signal != "SIGTERM" || end.Code != -1 {
		t.Errorf("after the stop: %+v", end)
	}
	var onDisk harness.Summary
	readJSON(t, o.Dir+"/summary.json", &onDisk)
	if onDisk.Exit.AfterStop == nil || onDisk.Exit.AfterStop.Signal != "SIGTERM" {
		t.Errorf("summary.json after the stop: %+v", onDisk.Exit)
	}
	if o.Summary.Stop.State != harness.StopByTerm || !hasSignal(r.fake.Process(0).Signals(), syscall.SIGTERM) || hasSignal(r.fake.Process(0).Signals(), syscall.SIGKILL) {
		t.Errorf("stop %+v, signals %v", o.Summary.Stop, r.fake.Process(0).Signals())
	}
	if !o.StepFailed || !r.canary.Stopped() || r.fake.Process(0).Closed() != 1 {
		t.Error("not recorded or not cleaned up")
	}
	if _, err := os.Lstat(o.Dir + "/report.json"); err != nil {
		t.Errorf("report.json: %v", err)
	}
}

func TestStopEscalatesToKill(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		p.IgnoreTerm()
		<-p.Stop()
		return harness.Exit{Code: -1, Signal: "SIGKILL"}
	}, func(o *harness.Options) { o.TimeLimit = 60 * time.Millisecond })
	mustNoErr(t, r)
	sigs := r.fake.Process(0).Signals()
	if r.out.Summary.Stop.State != harness.StopByKill || !hasSignal(sigs, syscall.SIGTERM) || !hasSignal(sigs, syscall.SIGKILL) {
		t.Errorf("stop %+v, signals %v", r.out.Summary.Stop, sigs)
	}
	if strings.Join(r.out.Summary.Stop.Signals, ",") != "SIGTERM,SIGKILL" {
		t.Errorf("signals recorded: %v", r.out.Summary.Stop.Signals)
	}
	if end := r.out.Summary.Exit.AfterStop; end == nil || end.Signal != "SIGKILL" || r.out.Summary.Exit.Exited {
		t.Errorf("exit %+v", r.out.Summary.Exit)
	}
}

func TestStopOfAStuckGroupIsUnknown(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	release := make(chan struct{})
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		p.IgnoreTerm()
		<-release
		return harness.Exit{Code: 0}
	}, func(o *harness.Options) {
		o.TimeLimit = 40 * time.Millisecond
		o.StopTermWait, o.StopKillWait = 20*time.Millisecond, 20*time.Millisecond
	})
	defer close(release)
	mustNoErr(t, r)
	if r.out.Summary.Stop.State != harness.StopUnknown {
		t.Errorf("stop %+v", r.out.Summary.Stop)
	}
	// The process was still there after the stop: there is nothing to record.
	if r.out.Summary.Exit.AfterStop != nil || r.out.Summary.Exit.Exited {
		t.Errorf("exit %+v", r.out.Summary.Exit)
	}
	wantStep(t, r.out, harness.StepStop, harness.StepFailed)
}

func TestProbeExitsBeforeWriting(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		return harness.Exit{Code: 98}
	}, nil)
	mustNoErr(t, r)
	o := r.out
	wantStep(t, o, harness.StepReadReport, harness.StepFailed)
	wantStep(t, o, harness.StepWaitExit, harness.StepOK)
	if o.Summary.Report != harness.ReportExitedFirst || o.Summary.Exit.Code != 98 || !o.Summary.Exit.Exited {
		t.Errorf("report %s, exit %+v", o.Summary.Report, o.Summary.Exit)
	}
	if o.Summary.Stop.State != harness.StopNothingLeft || !r.canary.Stopped() || r.fake.Process(0).Closed() != 1 {
		t.Errorf("stop %+v", o.Summary.Stop)
	}
	if _, err := os.Lstat(o.Dir + "/channel/report.fifo"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the report FIFO is still there: %v", err)
	}
}

func TestLaunchFailureStillCleansUpAndRecords(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	fake := newFake(nil)
	fake.LaunchErr = errors.New("fork refused")
	r := executeWith(t, f, fake, true, nil)
	mustNoErr(t, r)
	o := r.out
	wantStep(t, o, harness.StepLaunch, harness.StepFailed)
	wantStep(t, o, harness.StepReadReport, harness.StepSkipped)
	wantStep(t, o, harness.StepCleanup, harness.StepOK)
	if !strings.Contains(step(t, o, harness.StepLaunch).Error, "fork refused") {
		t.Errorf("error text: %q", step(t, o, harness.StepLaunch).Error)
	}
	if o.Summary.Report != harness.ReportNotLaunched || o.Summary.Stop.State != harness.StopNotLaunched || o.Summary.Exit.Launched {
		t.Errorf("summary: %+v", o.Summary)
	}
	if !r.canary.Stopped() {
		t.Error("the canary is still listening")
	}
	if _, err := os.Lstat(o.Dir + "/channel/report.fifo"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the report FIFO is still there: %v", err)
	}
}

func TestLoopbackOnlyResolverEndsTheRunBeforeALaunch(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	f.Resolv = "nameserver 127.0.0.1\n"
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepResolv, harness.StepFailed)
	if e := step(t, r.out, harness.StepResolv).Error; !strings.Contains(e, "loopback") {
		t.Errorf("the refusal does not name its reason: %q", e)
	}
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	if r.fake.Launches() != 0 || r.out.Summary.Resolv.DroppedLoopback != 1 {
		t.Errorf("launches %d, resolv %+v", r.fake.Launches(), r.out.Summary.Resolv)
	}
	if _, err := os.Lstat(r.out.Dir + "/summary.json"); err != nil {
		t.Error(err)
	}
}

func TestSymlinkedMountPointEndsTheRunBeforeALaunch(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	cfgDir, _ := f.Layout().ConfigDir(profile.Claude)
	if err := os.Symlink(f.Base, cfgDir+"/skills"); err != nil {
		t.Fatal(err)
	}
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepPrepare, harness.StepFailed)
	wantStep(t, r.out, harness.StepCheck, harness.StepSkipped)
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

func TestWithoutTheStatementNothingIsPrepared(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	f.Config.CoordinatorReviewed = true
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepPrepare, harness.StepSkipped)
	wantStep(t, r.out, harness.StepCheck, harness.StepFailed)
	cfgDir, _ := f.Layout().ConfigDir(profile.Claude)
	if _, err := os.Lstat(cfgDir + "/skills"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a mount point was made without the statement: %v", err)
	}
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

func TestWorktreeIsNotAnOwnClone(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	tree := f.Layout().Tree()
	if err := os.RemoveAll(tree + "/.git"); err != nil {
		t.Fatal(err)
	}
	f.Write(tree+"/.git", "gitdir: /elsewhere/worktrees/x\n", 0o644)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepOwnClone, harness.StepFailed)
	wantStep(t, r.out, harness.StepPrepare, harness.StepSkipped)
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

// A source Ataniq supplies must be given as its real path: one with a link on
// it is refused by the resolution, naming the row and the real path, before
// anything else is built or checked (the plan would bind the path it names,
// and bubblewrap follows a link in it).
func TestALinkOnASourceAtaniqSuppliesIsRefused(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	real := f.Config.Sources.AssignmentDir
	link := f.Owned + "/assignment-link"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f.Config.Sources.AssignmentDir = link
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepResolve, harness.StepFailed)
	e := step(t, r.out, harness.StepResolve).Error
	for _, want := range []string{"/ataniq/assignment", "symbolic link", real} {
		if !strings.Contains(e, want) {
			t.Errorf("the error %q does not say %q", e, want)
		}
	}
	wantStep(t, r.out, harness.StepBuild, harness.StepSkipped)
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

func TestCampaignFolderIsNeverBound(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	bound := r.out.Dir + "/bound/"
	for _, m := range r.out.Plan.Mounts {
		if strings.HasPrefix(m.Source, f.Campaign) && !strings.HasPrefix(m.Source, bound) {
			t.Errorf("%s is bound from %s", m.Inside, m.Source)
		}
	}
	for _, fl := range r.out.Plan.Files {
		if fl.Purpose == "report" && fl.Path != r.out.Dir+"/channel/report.fifo" {
			t.Errorf("report path %s", fl.Path)
		}
	}
}

func idsWithStatus(rs []harness.CaseResult, st assert.Status) []string {
	var out []string
	for _, r := range rs {
		if r.Status == st {
			out = append(out, r.ID)
		}
	}
	return out
}

// Without the statement that no lead has run, the harness cannot say that the
// previous instance is stopped: it has no unit and no control group. The walk
// right before the launch found nothing, and the outside half of assertion 7
// is still not-checked, never ok.
func TestStartWalkWithoutTheStatementIsNotChecked(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	// The first run makes the mount points; the second only checks them.
	mustNoErr(t, execute(t, f, okProbe(assert.StatusOK, 0, 0), nil))
	f.Config.NoLeadHasRun = false
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepPrepare, harness.StepSkipped)
	wantStep(t, r.out, harness.StepRecheck, harness.StepOK)
	wantStep(t, r.out, harness.StepLaunch, harness.StepOK)
	var out harness.OutsideEvidence
	readJSON(t, r.out.Dir+"/outside.json", &out)
	found := false
	for _, res := range out.Results {
		if res.ID != "tree-git-readonly.outside" {
			continue
		}
		found = true
		if res.Status != assert.StatusNotChecked || !strings.Contains(res.Detail, "not made with the previous instance proven stopped") || !strings.Contains(res.Detail, "no control group") {
			t.Errorf("the outside half of assertion 7 without the statement: %+v", res)
		}
	}
	if !found {
		t.Errorf("no outside half of assertion 7: %+v", out.Results)
	}
}

// A source that resolves differently right before the launch than it did when
// the plan was built stops the run: the plan would bind something other than
// what was checked.
func TestRecheckStopsARunWhoseSourceMoved(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	other := f.Owned + "/assignment-elsewhere"
	f.Mkdir(other, 0o755)
	assignment := f.Config.Sources.AssignmentDir
	moved := false
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.Resolver
		o.Resolver = func(p string) (string, error) {
			if moved && p == assignment {
				return other, nil
			}
			return base(p)
		}
		made := o.NewCanary
		o.NewCanary = func() (*assert.Canary, error) { moved = true; return made() }
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepRecheck, harness.StepFailed)
	if e := step(t, r.out, harness.StepRecheck).Error; !strings.Contains(e, "/ataniq/assignment") {
		t.Errorf("error %q", e)
	}
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	wantStep(t, r.out, harness.StepCleanup, harness.StepOK)
	if r.fake.Launches() != 0 || !r.canary.Stopped() {
		t.Errorf("launches %d, canary stopped %v", r.fake.Launches(), r.canary.Stopped())
	}
}

// The report path is directly inside a folder of the attempt made for the
// channel alone, and the folder goes away with the FIFO.
func TestReportChannelHasAFolderOfItsOwn(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	if r.out.StepFailed {
		t.Fatalf("steps: %+v", r.out.Summary.Steps)
	}
	var path string
	for _, fl := range r.out.Plan.Files {
		if fl.Purpose == "report" {
			path = fl.Path
		}
	}
	if path != r.out.Dir+"/channel/report.fifo" {
		t.Errorf("report path %q", path)
	}
	if _, err := os.Lstat(r.out.Dir + "/channel"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the channel folder is still there: %v", err)
	}
	// The dry run only names the folder.
	r = executeWith(t, f, newFake(nil), false, nil)
	mustNoErr(t, r)
	if _, err := os.Lstat(r.out.Dir + "/channel"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dry run made the channel folder: %v", err)
	}
}

// Assertion 1 is checked by identity only when the spec lists identities, so
// the harness collects them for the owner and holder paths.
func TestSpecListsTheIdentitiesOfTheOwnerAndHolderPaths(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	data, err := os.ReadFile(r.out.Dir + "/spec.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := assert.DecodeSpec(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.ForbiddenIdentities) == 0 {
		t.Fatal("the spec lists no identity: assertion 1 would not be checked")
	}
}

// Assertion 11 is checked by identity only when the spec lists the identities
// of the coordinators' folders, so the harness collects them: of the two roots
// under the state root and their ancestors, and of the folder, tree and home of
// every coordinator and of every shared login that exists.
func TestSpecListsTheCoordinatorsPathsAndIdentities(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	l := f.Layout()
	tree := l.CoordsDir() + "/" + f.Project + "/feature-a/tree"
	home := l.CoordsDir() + "/" + f.Project + "/feature-a/home"
	otherTree := l.CoordsDir() + "/another/feature-b/tree"
	login := l.CoordLoginDir() + "/" + f.Project + "/claude"
	for _, d := range []string{tree, home, otherTree, login} {
		f.Mkdir(d, 0o700)
	}
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepSpec, harness.StepOK)
	data, err := os.ReadFile(r.out.Dir + "/spec.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := assert.DecodeSpec(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.CoordinatorPaths) != 2 || spec.CoordinatorPaths[0] != l.CoordsDir() || spec.CoordinatorPaths[1] != l.CoordLoginDir() {
		t.Errorf("coordinator paths %v, want the two roots of the layout", spec.CoordinatorPaths)
	}
	have := map[assert.FileIdentity]bool{}
	for _, id := range spec.CoordinatorIdentities {
		have[id] = true
	}
	sys := f.Options(nil).System
	for _, p := range []string{l.CoordsDir(), l.CoordLoginDir(), f.State, tree, home, otherTree, login, filepath.Dir(tree)} {
		id, err := sys.Identity(p)
		if err != nil {
			t.Fatal(err)
		}
		if !have[id] {
			t.Errorf("the identity of %s is not among the coordinators'", p)
		}
	}
	// The lead's own folders and what is published are not coordinators'.
	for _, p := range []string{l.Home(), l.Tree(), l.Mail(), l.Requests(), l.Mailbox(), l.Published()} {
		id, err := sys.Identity(p)
		if err != nil {
			t.Fatal(err)
		}
		if have[id] {
			t.Errorf("the identity of %s, which is bound, is among the coordinators'", p)
		}
	}
}

// With no coordinator at all the list still holds the ancestors of the two
// roots (the state root), so assertion 11 can be made.
func TestSpecListsCoordinatorIdentitiesWithoutCoordinators(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	data, err := os.ReadFile(r.out.Dir + "/spec.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := assert.DecodeSpec(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.CoordinatorIdentities) == 0 {
		t.Fatal("the spec lists no coordinator identity: assertion 11 would not be checked")
	}
}

func TestCoordinatorListingFailureStopsTheRunBeforeALaunch(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		o.System = harnesstest.FailingDirNames{System: o.System}
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepSpec, harness.StepFailed)
	if s := step(t, r.out, harness.StepSpec); !strings.Contains(s.Error, "list the coordinators' folders") {
		t.Errorf("step error %q", s.Error)
	}
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

func TestIdentityCollectionFailureStopsTheRunBeforeALaunch(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		o.System = harnesstest.FailingIdentity{System: o.System}
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepSpec, harness.StepFailed)
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

// The accepted line is the start: the harness does not wait for end-of-file,
// which does not come while the sandbox lives.
func TestLineIsAcceptedWhileTheProcessIsStillRunning(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		_ = harness.WriteReportLine(req, harnesstest.ProbeLine(req.Plan.Digest, assert.StatusOK, 0))
		<-p.Stop()
		return harness.Exit{Code: -1, Signal: "SIGTERM"}
	}, func(o *harness.Options) { o.TimeLimit = 150 * time.Millisecond })
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepReadReport, harness.StepOK)
	wantStep(t, r.out, harness.StepWaitExit, harness.StepTimeout)
	wantStep(t, r.out, harness.StepTrailing, harness.StepOK)
	if r.out.Summary.Report != harness.ReportLineRead {
		t.Errorf("report %s", r.out.Summary.Report)
	}
}

func trailing(t *testing.T, dir string) *bool {
	t.Helper()
	var rep harness.ReportEvidence
	readJSON(t, dir+"/report.json", &rep)
	return rep.TrailingBytes
}

func TestQuietChannelAfterTheLineIsRecordedAsQuiet(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepTrailing, harness.StepOK)
	if got := trailing(t, r.out.Dir); got == nil || *got {
		t.Errorf("report_trailing_bytes: %v", got)
	}
}

func TestBytesAfterTheLineAreAFactAndAStepFailure(t *testing.T) {
	for name, extra := range map[string]string{"a second line": string(harnesstest.ProbeLine(strings.Repeat("0", 64), assert.StatusOK, 0)), "one byte": "x"} {
		t.Run(name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			approved(f)
			r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
				line := harnesstest.ProbeLine(req.Plan.Digest, assert.StatusOK, 0)
				_ = harness.WriteReportLine(req, append(line, extra...))
				return harness.Exit{}
			}, nil)
			mustNoErr(t, r)
			// The first line is still what was read; what follows fails the step.
			wantStep(t, r.out, harness.StepReadReport, harness.StepOK)
			wantStep(t, r.out, harness.StepTrailing, harness.StepFailed)
			if !r.out.StepFailed {
				t.Error("StepFailed is not set")
			}
			if got := trailing(t, r.out.Dir); got == nil || !*got {
				t.Errorf("report_trailing_bytes: %v", got)
			}
			if e := step(t, r.out, harness.StepTrailing).Error; !strings.Contains(e, "after the report line") {
				t.Errorf("error %q", e)
			}
		})
	}
}

// A report that never came is not asked about trailing bytes.
func TestNoLineMeansNothingIsAskedAboutTrailingBytes(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		return harness.Exit{Code: 98}
	}, nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepTrailing, harness.StepSkipped)
	if got := trailing(t, r.out.Dir); got != nil {
		t.Errorf("report_trailing_bytes: %v", *got)
	}
}

// A row of the host that is reached through a symbolic link into the campaign
// folder resolves to a path inside it: the lexical check of the configuration
// cannot see that, and binding it (the plan binds a host row's real path) would
// put the evidence or the report channel into the sandbox. The plan check
// refuses it before any launch. A source Ataniq supplies with such a link is
// refused earlier, by the resolution (see TestALinkOnASourceAtaniqSuppliesIsRefused).
func TestHostRowLinkedIntoTheCampaignIsNeverBound(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	stolen := f.Campaign + "/stolen"
	f.Write(stolen, "evidence", 0o644)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), func(o *harness.Options) {
		base := o.Resolver
		o.Resolver = func(p string) (string, error) {
			if p == "/etc/localtime" {
				return stolen, nil
			}
			return base(p)
		}
	})
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepVerify, harness.StepFailed)
	if e := step(t, r.out, harness.StepVerify).Error; !strings.Contains(e, "/etc/localtime") || !strings.Contains(e, "campaign") {
		t.Errorf("the refusal does not say what it refused: %q", e)
	}
	wantStep(t, r.out, harness.StepLaunch, harness.StepSkipped)
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

// A source Ataniq supplies that is reached through a symbolic link into the
// campaign folder is refused by the resolution, naming the real path.
func TestSourceLinkedIntoTheCampaignIsRefusedByTheResolution(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	inside := f.Campaign + "/stolen"
	f.Mkdir(inside, 0o755)
	link := f.Owned + "/assignment-into-campaign"
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	f.Config.Sources.AssignmentDir = link
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	wantStep(t, r.out, harness.StepResolve, harness.StepFailed)
	if e := step(t, r.out, harness.StepResolve).Error; !strings.Contains(e, "/ataniq/assignment") || !strings.Contains(e, inside) {
		t.Errorf("the refusal does not say what it refused: %q", e)
	}
	if r.fake.Launches() != 0 {
		t.Error("launched")
	}
}

// The spec is built with the environment the sandbox shows inside, which is
// the allowlist and the PWD that bubblewrap sets; without PWD a real probe
// would report its environment check as failed.
func TestSpecHoldsTheInsideEnvironment(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	data, err := os.ReadFile(r.out.Dir + "/spec.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := assert.DecodeSpec(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	want := plan.InsideEnvironment(r.fake.Requests()[0].Plan)
	if len(want) == 0 {
		t.Fatal("the plan has no inside environment")
	}
	got := map[string]string{}
	for _, e := range spec.Env {
		got[e.Name] = e.Value
	}
	if got["PWD"] != profile.TreeInside(f.Project) {
		t.Errorf("PWD in the spec is %q", got["PWD"])
	}
	for _, e := range want {
		if got[e.Name] != e.Value {
			t.Errorf("%s is %q in the spec, the plan says %q", e.Name, got[e.Name], e.Value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the spec holds %d variables, the plan %d", len(got), len(want))
	}
}

// With no way to ask, standard input is taken not to be a terminal.
func TestNoTerminalQuestionMeansNoTerminal(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	fake := newFake(okProbe(assert.StatusOK, 0, 0))
	r := executeWith(t, f, fake, true, func(o *harness.Options) { o.StdinIsTerminal = nil })
	var gate *harness.GateError
	if !errors.As(r.err, &gate) || !strings.Contains(gate.Reason, "terminal") {
		t.Fatalf("error %v, want a gate error naming the terminal", r.err)
	}
	if r.out != nil || fake.Calls() != 0 || len(f.Names(f.Campaign)) != 0 {
		t.Errorf("the refused run did something: %v, %d calls", r.out, fake.Calls())
	}
}
