//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func reopenEntryFixture(t *testing.T, f *entryFixture) {
	t.Helper()
	f.c.Close()
	c, err := OpenCampaign(f.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	f.c, f.a = c, c.Records[f.a.ID]
	if f.a == nil {
		t.Fatal("original entry authority unreadable")
	}
}

func requireEntryCleanupOnly(t *testing.T, f *entryFixture) {
	t.Helper()
	if entryObserved(f.a) || f.a.EntryRuntime.Natural || f.a.EntryRuntime.ExpectedExit {
		t.Fatal("cleanup promoted entry evidence")
	}
	if _, err := os.Lstat(filepath.Join(f.c.Path, f.a.ID, "release.json")); !os.IsNotExist(err) {
		t.Fatal("recovery published wrapper entry", err)
	}
	assertNoEntryPressure(t, f)
}

func TestR1EntryAdmissionFaultRetainsOwnedCleanup(t *testing.T) {
	for _, fault := range []string{"effective", "initial-pidfd", "population"} {
		t.Run(fault, func(t *testing.T) {
			f := entryTestFixture(t)
			cause := errors.New("initial " + fault + " unavailable")
			switch fault {
			case "effective":
				f.h.budget = func(u *Unit) error {
					if u.Boundary == f.service.Boundary {
						return cause
					}
					return nil
				}
			case "initial-pidfd":
				f.h.handle = func(Process) (*os.File, error) { return nil, cause }
			case "population":
				population := f.h.population
				failed := false
				f.h.population = func(b Boundary) (bool, error) {
					if f.a.Workload != nil && !failed {
						failed = true
						return false, cause
					}
					return population(b)
				}
			}
			if err := f.c.run(f.a, f.h); err == nil {
				t.Fatal("fault ignored")
			}
			if !f.serviceStarted || !f.dead || !f.parentStopped || f.a.Phase != "complete" || !f.a.TargetExited || !f.a.EntryRuntime.Terminal || f.a.PopulatedLive {
				t.Fatal("admission progress was required for cleanup", f.a.Phase, f.a.Reason)
			}
			requireEntryCleanupOnly(t, f)
			reopenEntryFixture(t, f)
			if f.a.Phase != "complete" || f.a.EntryRuntime.FailureReason == "" || f.a.Reason == "" || validateStored(f.a) != nil {
				t.Fatal("cleanup or original failure lost on reopen", f.a.Reason)
			}
		})
	}
}

func TestR1EntryParentIntentSurvivesRepeatedReconcile(t *testing.T) {
	for _, fault := range []string{"lost-start-ack", "population", "service-absence"} {
		t.Run(fault, func(t *testing.T) {
			f := entryTestFixture(t)
			call := f.h.call
			if fault == "lost-start-ack" {
				f.h.call = func(path string, args []string) (string, error) {
					out, err := call(path, args)
					if path == f.a.Config.Busctl && err == nil {
						return "", errors.New("lost parent acknowledgement")
					}
					return out, err
				}
				if err := f.c.run(f.a, f.h); err == nil || f.a.Parent != nil {
					t.Fatal("expected uncertain parent delivery", err)
				}
			} else {
				if err := f.c.publishEntryContext(f.a); err != nil {
					t.Fatal(err)
				}
				f.parentStarted, f.a.Parent = true, f.parent
				f.a.EntryRuntime.ParentStartIntent = true
				if err := f.c.Save(f.a, "slice-bound", ""); err != nil {
					t.Fatal(err)
				}
			}
			reopenEntryFixture(t, f)
			f.h.call = call
			population := f.h.population
			switch fault {
			case "lost-start-ack", "service-absence":
				name := f.a.Slice
				if fault == "service-absence" {
					name = f.a.Service
				}
				f.h.call = func(path string, args []string) (string, error) {
					if args[len(args)-1] == name && strings.Contains(strings.Join(args, " "), " show ") {
						return "", syscall.EIO
					}
					return call(path, args)
				}
			case "population":
				f.h.population = func(Boundary) (bool, error) { return false, syscall.EIO }
			}
			if err := f.c.reconcile(f.a, f.h); err == nil || f.a.Phase != "cleanup-unknown" || f.parentStopped {
				t.Fatal("first failed read not fenced", err)
			}
			reopenEntryFixture(t, f)
			if !f.a.EntryRuntime.ParentStartIntent || f.a.EntryRuntime.ToolDispatchIntent {
				t.Fatal("dispatch facts lost")
			}
			f.h.call, f.h.population = call, population
			f.calls = nil
			if err := f.c.reconcile(f.a, f.h); err != nil || !f.parentStopped || !f.a.EntryRuntime.ParentOnly || f.a.Phase != "complete" {
				t.Fatal("repeated parent-only cleanup failed", err)
			}
			for _, c := range f.calls {
				if strings.Contains(c, "StartTransientUnit") || strings.Contains(c, "--service-type") {
					t.Fatal("recovery relaunched", c)
				}
			}
			requireEntryCleanupOnly(t, f)
			reopenEntryFixture(t, f)
			if f.a.Reason == "" {
				t.Fatal("reconciliation failure disappeared")
			}
		})
	}
}

func TestR1EntryPopulationUnavailableStillPermitsGenerationStop(t *testing.T) {
	f := entryTestFixture(t)
	population := f.h.population
	f.h.population = func(b Boundary) (bool, error) {
		if f.a.Workload != nil {
			return false, syscall.EACCES
		}
		return population(b)
	}
	if err := f.c.run(f.a, f.h); err == nil || !f.dead || f.parentStopped || f.a.TargetExited || f.a.Empty || f.a.EntryRuntime.Terminal {
		t.Fatal("missing population blocked the owned stop or fabricated completion", err)
	}
	reopenEntryFixture(t, f)
	if !f.a.EntryRuntime.ServiceStopIntent || f.a.PopulatedLive || !f.c.Blocked {
		t.Fatal("partial stop authority lost")
	}
	f.h.population = population
	if err := f.c.reconcile(f.a, f.h); err != nil || f.a.Phase != "complete" || !f.parentStopped || f.a.PopulatedLive {
		t.Fatal("later empty evidence could not finish owned cleanup", err)
	}
	reopenEntryFixture(t, f)
	requireEntryCleanupOnly(t, f)
}

func TestR1EntryToolIntentRemainsUncertainAcrossRetries(t *testing.T) {
	f := entryTestFixture(t)
	if err := f.c.publishEntryContext(f.a); err != nil {
		t.Fatal(err)
	}
	f.parentStarted, f.a.Parent = true, f.parent
	f.a.EntryRuntime.ParentStartIntent, f.a.EntryRuntime.ToolDispatchIntent = true, true
	f.a.Stage = "tool-start-intent"
	if err := f.c.Save(f.a, "launch-unknown", "delivery uncertain"); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		reopenEntryFixture(t, f)
		if err := f.c.reconcile(f.a, f.h); err == nil || f.parentStopped || f.a.EntryRuntime.ParentOnly || !f.c.Blocked {
			t.Fatal("in-flight Tool became prelaunch absence", err)
		}
	}
	requireEntryCleanupOnly(t, f)
}

func TestR1EntryMissingInjectionStillStopsExactService(t *testing.T) {
	for _, fault := range []string{"missing", "invalid", "replaced-generation", "adoption-save"} {
		t.Run(fault, func(t *testing.T) {
			f := entryTestFixture(t)
			if err := f.c.publishEntryContext(f.a); err != nil {
				t.Fatal(err)
			}
			f.parentStarted, f.serviceStarted, f.a.Parent = true, true, f.parent
			f.a.EntryRuntime.ParentStartIntent, f.a.EntryRuntime.ToolDispatchIntent = true, true
			if err := f.c.Save(f.a, "launching-service", ""); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.c.Path, f.a.ID, "injection.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if fault == "invalid" {
				if err := exclusiveFile(path, []byte("{}"), 0400); err != nil {
					t.Fatal(err)
				}
			}
			reopenEntryFixture(t, f)
			if fault == "replaced-generation" {
				f.a.Workload = &Unit{Boundary: f.service.Boundary, Main: f.service.Main, Properties: map[string]string{}}
				for k, v := range f.service.Properties {
					f.a.Workload.Properties[k] = v
				}
				f.a.Workload.Properties["InvocationID"] = strings.Repeat("c", 32)
			}
			if fault == "adoption-save" {
				f.c.syncDir = func(*os.File) error { return syscall.EIO }
			}
			call := f.h.call
			f.h.call = func(tool string, args []string) (string, error) {
				if strings.Contains(strings.Join(args, " "), " stop ") && args[len(args)-1] == f.a.Service {
					f.requireIntent(t, "cancelling")
					b, err := os.ReadFile(filepath.Join(f.c.Path, f.a.ID, "intent.json"))
					var saved Attempt
					if err != nil || decodeHostRecord(bytes.NewReader(b), &saved) != nil || saved.Workload == nil || saved.Injector != nil || saved.Target != nil || !saved.EntryRuntime.ServiceStopIntent {
						t.Fatal("partial stop authority not saved")
					}
				}
				return call(tool, args)
			}
			if err := f.c.reconcile(f.a, f.h); err == nil {
				t.Fatal("missing original evidence accepted")
			}
			if fault == "replaced-generation" || fault == "adoption-save" {
				if f.dead || f.parentStopped {
					t.Fatal("stop without durable original generation")
				}
				return
			}
			if !f.dead || f.parentStopped || f.a.Empty || f.a.TargetExited || !f.a.EntryRuntime.ServiceStopped || f.a.EntryRuntime.Terminal || !f.c.Blocked {
				t.Fatal("partial service stop lost or promoted")
			}
			requireEntryCleanupOnly(t, f)
			reopenEntryFixture(t, f)
			if err := f.c.reconcile(f.a, f.h); err == nil || !f.a.EntryRuntime.ServiceStopped || f.a.Empty {
				t.Fatal("retry invented originals", err)
			}
			// Additional authenticated original evidence can finish cleanup, never
			// reconstruct entry. No replacement, publication or launch is allowed.
			if fault == "invalid" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := exclusiveFile(path, original, 0400); err != nil {
				t.Fatal(err)
			}
			if err := f.c.reconcile(f.a, f.h); err != nil || f.a.Phase != "complete" || !f.parentStopped {
				t.Fatal("additional original evidence could not finish cleanup", err)
			}
			reopenEntryFixture(t, f)
			if f.a.EntryRuntime.FailureReason == "" {
				t.Fatal("partial-cleanup reason lost")
			}
			requireEntryCleanupOnly(t, f)
		})
	}
}

func TestR1EntryFailureReasonSurvivesCleanupAndInspect(t *testing.T) {
	f := entryTestFixture(t)
	f.h.entryEndpoints = func(Process, Process) (R1EntryEndpoints, error) {
		return R1EntryEndpoints{}, errors.New("selected endpoint metadata denied")
	}
	call := f.h.call
	f.h.call = func(tool string, args []string) (string, error) {
		if strings.Contains(strings.Join(args, " "), " stop ") && args[len(args)-1] == f.a.Service {
			return "", errors.New("cleanup transport interrupted")
		}
		return call(tool, args)
	}
	err := f.c.run(f.a, f.h)
	if err == nil || !strings.Contains(err.Error(), "selected endpoint metadata denied") || !strings.Contains(err.Error(), "cleanup transport interrupted") {
		t.Fatal("original or cleanup error lost", err)
	}
	reopenEntryFixture(t, f)
	if f.a.EntryRuntime.FailureReason != "selected endpoint metadata denied" || f.a.Reason != "cleanup transport interrupted" {
		t.Fatal("failure reasons overwritten", f.a.Reason)
	}
	f.h.call = call
	if err := f.c.reconcile(f.a, f.h); err != nil || f.a.Phase != "complete" {
		t.Fatal(err)
	}
	reopenEntryFixture(t, f)
	f.c.Close()
	var out bytes.Buffer
	if err := InspectHost(f.c.Path, f.a.ID, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"failure_reason":"selected endpoint metadata denied"`, `"reason":"cleanup transport interrupted"`, `"phase":"complete"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("inspect lost failure after cleanup", out.String())
		}
	}
	if entryObserved(f.a) {
		t.Fatal("failed observation promoted")
	}
}

func TestR1EntryEffectiveHighAndOOMGuards(t *testing.T) {
	for _, fault := range []string{"", "parent-high", "tool-high", "tool-group", "tool-group-denied"} {
		t.Run(fault, func(t *testing.T) {
			f := entryTestFixture(t)
			f.h.budget = nil
			seen := map[string]bool{}
			f.h.entryRead = func(path, key string) ([]byte, error) {
				parent := path == f.parent.Boundary.Path
				if !parent && path != f.service.Boundary.Path {
					t.Fatal("unexpected kernel boundary")
				}
				seen[path+"/"+key] = true
				values := map[string]string{"memory.max": "100663296", "memory.swap.max": "0", "memory.high": "max", "memory.oom.group": "1", "pids.max": "32", "cpu.max": "100000 100000"}
				if parent {
					values["memory.max"], values["pids.max"] = "268435456", "64"
					if key == "memory.oom.group" {
						t.Fatal("service OOM policy applied to parent")
					}
				}
				if key == "memory.high" && (parent && fault == "parent-high" || !parent && fault == "tool-high") {
					return []byte("100663296"), nil
				}
				if !parent && key == "memory.oom.group" {
					if fault == "tool-group" {
						return []byte("0"), nil
					}
					if fault == "tool-group-denied" {
						return nil, syscall.EACCES
					}
				}
				value, ok := values[key]
				if !ok {
					t.Fatal("unexpected kernel property", key)
				}
				return []byte(value + "\n"), nil
			}
			if err := f.c.run(f.a, f.h); fault == "" {
				if err != nil || !entryObserved(f.a) {
					t.Fatal("fixed safety profile failed", err)
				}
				for _, key := range []string{f.parent.Boundary.Path + "/memory.high", f.service.Boundary.Path + "/memory.high", f.service.Boundary.Path + "/memory.oom.group"} {
					if !seen[key] {
						t.Fatal("missing effective guard", key)
					}
				}
			} else {
				if err == nil {
					t.Fatal("unsafe effective guard accepted")
				}
				requireEntryCleanupOnly(t, f)
				if strings.HasPrefix(fault, "tool-") && (!f.parentStopped || f.a.Phase != "complete" || f.a.PopulatedLive) {
					t.Fatal("guard failure prevented owned cleanup")
				}
			}
		})
	}
}
