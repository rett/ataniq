package switcher

import (
	"path/filepath"
	"strings"
	"testing"
)

// The runner records commands. No unit or witness process is started.
func TestExplicitRollbackStopsDepartingWitness(t *testing.T) {
	for _, failure := range []string{"", "stop-witness", "stop-host"} {
		t.Run("failed="+failure, func(t *testing.T) {
			e := newEnv(t)
			e.release(rel1, "s")
			if code := e.switchTo(rel1); code != ExitOK {
				t.Fatalf("first switch exited %d", code)
			}
			e.release(rel2, "s")
			e.withLists(rel2, "start-witness", "health-witness", "stop-witness\nstop-departing")
			if code := e.switchTo(rel2); code != ExitOK || e.runner.called("start-witness") != 1 {
				t.Fatal("witness release did not activate")
			}
			e.write(filepath.Join(e.root, PolicyDir, HostStopList), "stop-host\nstop-other\n", 0644)
			e.runner.reset()
			e.runner.fail[failure] = 1
			code := e.rollBack("previous")
			_, inhibited := e.inhibited()
			e.ran(map[string]int{"stop-witness": 1, "stop-departing": 1, "stop-host": 1, "stop-other": 1})
			if failure == "" {
				if code != ExitOK || inhibited {
					t.Fatalf("rollback: exit %d, inhibited %v", code, inhibited)
				}
				return
			}
			if code != ExitFailed || !inhibited || !strings.Contains(e.last, failure) || !strings.Contains(e.last, "decisions stay inhibited") || e.runner.called("reload-units") != 0 {
				t.Fatalf("failed stop: exit %d, inhibited %v\n%s", code, inhibited, e.last)
			}
			if code := e.rollBack("previous"); code != ExitOK {
				t.Fatalf("retry: %s", e.last)
			}
		})
	}
}

func TestRollbackFromUnverifiableRelease(t *testing.T) {
	e := active(t)
	e.write(filepath.Join(e.relDir(rel2), "unlisted"), "extra", 0644)
	e.write(filepath.Join(e.root, PolicyDir, HostStopList), "stop-host\n", 0644)
	e.runner.fail["stop-host"] = 1
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("host stop failure: exit %d", code)
	}
	if _, inhibited := e.inhibited(); !inhibited {
		t.Fatal("failed stop cleared the inhibit")
	}
	e.ran(map[string]int{"stop-host": 1, "stop-app": 0, "reload-units": 0})
	e.runner.reset()
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("retry: %s", e.last)
	}
	e.links(rel1, rel2)
	e.ran(map[string]int{"stop-host": 1, "stop-app": 0, "reload-units": 1, "health-probe": 1})
	if _, inhibited := e.inhibited(); inhibited || !strings.Contains(e.last, CodeFileNotListed) {
		t.Fatalf("inhibited %v: %s", inhibited, e.last)
	}
}

func TestWitnessRollbackStopAfterReload(t *testing.T) {
	const stop = "systemctl stop ataniq-witness.service"
	for _, point := range []string{CrashReload, CrashDone} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t)
			e.release(rel1, "s")
			if e.switchTo(rel1) != ExitOK {
				t.Fatal(e.last)
			}
			e.release(rel2, "s")
			e.withLists(rel2, "start-witness", "health-witness", stop)
			if e.switchTo(rel2) != ExitOK {
				t.Fatal(e.last)
			}
			e.write(filepath.Join(e.root, PolicyDir, HostStopList), stop+"\n", 0644)
			loaded := true
			e.runner.gate = func(argv []string) error {
				switch strings.Join(argv, " ") {
				case stop:
					if !loaded {
						return &RunError{Status: 5}
					}
				case "reload-units":
					loaded = false // rel1's daemon-reload drops the witness unit
				case "start-witness":
					loaded = true
				}
				return nil
			}
			e.crashAt(point, true, "previous")
			if _, inhibited := e.inhibited(); !inhibited || loaded {
				t.Fatalf("after %s: inhibited %v, loaded %v", point, inhibited, loaded)
			}
			e.runner.reset()
			if e.rollBack("previous") != ExitOK {
				t.Fatal(e.last)
			}
			e.links(rel1, rel2)
			e.ran(map[string]int{"systemctl": 2, "reload-units": 1, "health-probe": 1})
			if _, inhibited := e.inhibited(); inhibited {
				t.Fatal("target still inhibited")
			}
			// The host list also stops this unloaded unit during normal rollback
			// back onto rel2, before rel2's reload starts its own witness.
			if e.rollBack("previous") != ExitOK || !loaded {
				t.Fatal(e.last)
			}
			e.links(rel2, rel1)
		})
	}
}

func TestWitnessStopExitFiveIsSpecific(t *testing.T) {
	e := newEnv(t)
	e.runner.gate = func([]string) error { return &RunError{Status: 5} }
	s := e.switcher()
	for _, argv := range [][]string{
		{"stop-app"}, {"/other/systemctl", "stop", "app"}, {"systemctl", "restart", "app"},
		{"systemctl", "stop", "app", "other"},
	} {
		if failures := s.runStops(t.Context(), "host", [][]string{argv}); len(failures) != 1 {
			t.Errorf("accepted exit 5 from %q", argv)
		}
	}
}
