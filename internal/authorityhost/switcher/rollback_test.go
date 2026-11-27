package switcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
			e.write(filepath.Join(e.relDir(rel2), "etc/systemd/system/ataniq-witness.service"), "[Service]\n", 0644)
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
			e.ran(map[string]int{"systemctl": 5, "reload-units": 1, "health-probe": 1})
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

func TestWitnessRollbackProvesDeparture(t *testing.T) {
	const unit = "ataniq-witness.service"
	for _, name := range []string{"recorded", "installed link", "legacy-partial-links", "legacy-intact-tree"} {
		legacy := name != "recorded"
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.release(rel1, "s")
			if e.switchTo(rel1) != ExitOK {
				t.Fatal(e.last)
			}
			dir := e.release(rel2, "s")
			e.write(filepath.Join(dir, "etc/systemd/system", unit), "[Service]\n", 0644)
			e.write(filepath.Join(dir, "etc/systemd/system/ataniq-seat.service"), "[Service]\n", 0644)
			e.withLists(rel2, "start-witness", "health-witness", "systemctl stop "+unit)
			if e.switchTo(rel2) != ExitOK {
				t.Fatal(e.last)
			}
			if legacy {
				path := filepath.Join(e.root, StateDir, DeployFile)
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(e.stateFile(DeployFile)), &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "current_units")
				data, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				e.write(path, string(data), 0644)
				link := filepath.Join(e.root, "etc/systemd/system", unit)
				if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
					t.Fatal(err)
				}
				if name == "legacy-partial-links" {
					link = filepath.Join(filepath.Dir(link), "ataniq-seat.service")
				}
				if err := os.Symlink("/opt/authority-host/current/etc/systemd/system/"+filepath.Base(link), link); err != nil {
					t.Fatal(err)
				}
			}
			// Set the original import aside so repair can restore its provenance.
			removed := dir
			if name == "legacy-partial-links" {
				removed = filepath.Join(dir, "etc/systemd/system", unit)
			}
			original := filepath.Join(t.TempDir(), "original")
			if name != "legacy-intact-tree" {
				if err := os.Rename(removed, original); err != nil {
					t.Fatal(err)
				}
			}
			host := filepath.Join(e.root, PolicyDir, HostStopList)
			e.write(host, "systemctl stop ataniq-witnes.service\n", 0644)
			active := true
			queried := false
			e.runner.gate = func(a []string) error {
				if a[0] == "systemctl" && a[1] == "stop" {
					if a[2] != unit {
						return &RunError{Status: 5}
					}
					active = false
				}
				return nil
			}
			e.runner.unitState = func(name string) (Output, error) {
				if name == unit {
					queried = true
					if active {
						return Output{Stdout: []byte("active\n")}, nil
					}
				}
				return Output{Stdout: []byte("inactive\n")}, &RunError{Status: 3}
			}
			e.runner.reset()
			if legacy {
				for range 2 {
					code := e.rollBack("previous")
					if _, held := e.inhibited(); code != ExitFailed || !held || e.runner.called("reload-units") != 0 ||
						!strings.Contains(e.last, "missing service inventory") || !strings.Contains(e.last, "recover --services-stopped") {
						t.Fatalf("missing inventory: exit %d, inhibited %t, queried %t\n%s", code, held, queried, e.last)
					}
					if j, ok := e.journal(); !ok || j.OldUnits != nil {
						t.Fatalf("inferred inventory: %+v", j)
					}
				}
				if name != "legacy-intact-tree" {
					if e.recover(RecoverOptions{ServicesStopped: true}) != ExitFailed {
						t.Fatal("recovered from an absent release")
					}
					if name == "legacy-partial-links" {
						// The hashes match, but this replacement was never imported.
						e.write(removed, "[Service]\n", 0644)
						if e.recover(RecoverOptions{ServicesStopped: true}) != ExitFailed || !strings.Contains(e.last, CodeProvenanceMismatch) {
							t.Fatal(e.last)
						}
					}
					if err := os.Rename(original, removed); err != nil {
						t.Fatal(err)
					}
				}
				active = false // the owner stops the service by hand
				if e.recover(RecoverOptions{ServicesStopped: true}) != ExitOK {
					t.Fatal(e.last)
				}
				if _, held := e.inhibited(); !held {
					t.Fatal("recording names cleared the inhibit")
				}
				// If the service returns, the recorded name must still be checked.
				active, queried = true, false
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			code := e.rollBack("previous")
			if _, held := e.inhibited(); code != ExitFailed || !held || !queried || !strings.Contains(e.last, unit) || e.runner.called("reload-units") != 0 {
				t.Fatalf("active departure: exit %d, held %v, queried %v", code, held, queried)
			}
			if _, ok := e.journal(); !ok {
				t.Fatal("no retry journal")
			}
			e.write(host, "systemctl stop "+unit+"\n", 0644)
			if e.rollBack("previous") != ExitOK || active {
				t.Fatal(e.last)
			}
			if _, held := e.inhibited(); held {
				t.Fatal("inhibit after successful retry")
			}
		})
	}
}

func TestReleaseDepartureManagerEvidence(t *testing.T) {
	for _, tc := range []struct {
		state     string
		status    int
		cut, pass bool
	}{
		{"inactive", 3, false, true}, {"unknown", 4, false, true}, {"inactive", 4, false, true},
		{"active", 0, false, false}, {"activating", 3, false, false}, {"deactivating", 3, false, false},
		{"failed", 3, false, false}, {"", 3, false, false}, {"inactive", 1, false, false}, {"inactive", 3, true, false},
	} {
		e := newEnv(t)
		e.runner.unitState = func(string) (Output, error) {
			out := Output{Stdout: []byte(tc.state + "\n"), Truncated: tc.cut}
			if tc.status == 0 {
				return out, nil
			}
			return out, &RunError{Status: tc.status}
		}
		err := e.switcher().checkInactive(t.Context(), []string{"app.service"}, nil)
		if (err == nil) != tc.pass {
			t.Errorf("%+v: %v", tc, err)
		} else if err != nil && !strings.Contains(err.Error(), "state "+q(tc.state)+" for app.service") {
			t.Errorf("missing manager observation for %+v: %v", tc, err)
		}
	}
}

func TestReleaseDepartureRetryAfterResetFailed(t *testing.T) {
	e := active(t)
	state := "failed"
	e.runner.unitState = func(string) (Output, error) {
		return Output{Stdout: []byte(state + "\n")}, &RunError{Status: 3}
	}
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("rollback exited %d", code)
	}
	for _, want := range []string{CodeStopFailed, "state failed", "systemctl reset-failed app.service"} {
		if !strings.Contains(e.last, want) {
			t.Errorf("rollback output missing %q:\n%s", want, e.last)
		}
	}
	if _, held := e.inhibited(); !held || e.runner.called("reload-units") != 0 {
		t.Fatal("rollback advanced while app.service was failed")
	}

	state = "inactive" // The owner ran systemctl reset-failed.
	if e.rollBack("previous") != ExitOK {
		t.Fatal(e.last)
	}
	e.links(rel1, rel2)
	if _, held := e.inhibited(); held {
		t.Fatal("successful retry left decisions inhibited")
	}
}

func TestReleaseDepartureTemplateInstances(t *testing.T) {
	for _, states := range []string{"", "inactive\n\ninactive\n", "inactive\n\nactive\n", "failed\n"} {
		e := newEnv(t)
		queried := false
		e.runner.unitState = func(name string) (Output, error) {
			if name == "worker@*.service" {
				queried = true
				return Output{Stdout: []byte(states)}, nil
			}
			return Output{Stdout: []byte("inactive\n")}, &RunError{Status: 4}
		}
		err := e.switcher().checkInactive(t.Context(), []string{"worker@.service"}, nil)
		if !queried || (err != nil) != (strings.Contains(states, "\nactive\n") || states == "failed\n") {
			t.Errorf("states %q: queried %v, err %v", states, queried, err)
		}
		if states == "failed\n" && (err == nil || !strings.Contains(err.Error(), "systemctl reset-failed INSTANCE")) {
			t.Errorf("missing instance repair: %v", err)
		}
	}
}

func TestWitnessForwardDeparture(t *testing.T) {
	const unit = "ataniq-witness.service"
	for _, failure := range []string{"ignored stop", "restarted during reload", "interrupted settlement"} {
		t.Run(failure, func(t *testing.T) {
			e := newEnv(t)
			dir := e.release(rel1, "s")
			e.write(filepath.Join(dir, "etc/systemd/system", unit), "[Service]\n", 0644)
			e.withLists(rel1, "start-witness", "health-witness", "release-stop")
			if e.switchTo(rel1) != ExitOK {
				t.Fatal(e.last)
			}
			e.release(rel2, "s")
			e.hostList("systemctl stop " + unit + "\n")
			active, fixed := true, false
			var sequence []string
			e.runner.gate = func(argv []string) error {
				switch strings.Join(argv, " ") {
				case "systemctl stop " + unit:
					sequence = append(sequence, "host")
					if failure != "ignored stop" || fixed {
						active = false
					}
				case "release-stop":
					sequence = append(sequence, "release")
				case "reload-units":
					sequence = append(sequence, "reload")
					if failure == "restarted during reload" && !fixed {
						active = true
					}
				}
				return nil
			}
			e.runner.unitState = func(name string) (Output, error) {
				if name == unit {
					sequence = append(sequence, "query")
					if active {
						return Output{Stdout: []byte("active\n")}, nil
					}
				}
				return Output{Stdout: []byte("inactive\n")}, &RunError{Status: 3}
			}
			if failure == "interrupted settlement" {
				e.crashAt(CrashJournalMoved, false, rel2)
				active = true
				// The manager accepts a stop request but the service survives.
				e.runner.gate = func([]string) error { return nil }
			}
			if code := e.switchTo(rel2); code != ExitFailed {
				t.Fatalf("exit %d, witness active %v, calls %v", code, active, sequence)
			}
			if _, held := e.inhibited(); !held || !strings.Contains(e.last, unit) {
				t.Fatal(e.last)
			}
			if failure != "interrupted settlement" && !strings.HasPrefix(strings.Join(sequence, " "), "host release query") {
				t.Fatalf("departure order %v", sequence)
			}
			if failure == "ignored stop" && slices.Contains(sequence, "reload") {
				t.Fatal("reloaded with old witness active")
			}
			fixed = true
			if failure == "interrupted settlement" {
				e.runner.gate = func(a []string) error {
					if strings.Join(a, " ") == "systemctl stop "+unit {
						active = false
					}
					return nil
				}
			}
			if e.switchTo(rel2) != ExitOK || active {
				t.Fatal(e.last)
			}
			if _, held := e.inhibited(); held {
				t.Fatal("qualified target still inhibited")
			}
		})
	}
}

func TestWitnessEmergencyStopExitFiveNeedsObservation(t *testing.T) {
	e := newEnv(t)
	dir := e.release(rel1, "s")
	e.write(filepath.Join(dir, "etc/systemd/system/ataniq-witness.service"), "[Service]\n", 0644)
	e.withLists(rel1, "reload-units", "health-probe", "systemctl stop ataniq-witnes.service")
	e.runner.fail["health-probe"] = -1
	e.runner.gate = func(a []string) error {
		if a[0] == "systemctl" && a[1] == "stop" {
			return &RunError{Status: 5}
		}
		return nil
	}
	e.runner.unitState = func(name string) (Output, error) {
		if name == "ataniq-witness.service" {
			return Output{Stdout: []byte("active\n")}, nil
		}
		return Output{Stdout: []byte("inactive\n")}, &RunError{Status: 3}
	}
	if e.switchTo(rel1) != ExitFailed {
		t.Fatal(e.last)
	}
	if j, ok := e.journal(); !ok || j.Phase != PhaseStopping || !strings.Contains(j.StopFailures, "ataniq-witness.service") {
		t.Fatalf("no retryable stop: %+v", j)
	}
}

func TestReleaseDepartureDropInOnly(t *testing.T) {
	const unit = "worker.service"
	const units = "etc/systemd/system"
	e := newEnv(t)
	dir := e.release(rel1, "s")
	host := filepath.Join(e.root, units)
	e.write(filepath.Join(host, unit), "[Service]\nExecStart=/usr/bin/true\n", 0644)
	if err := os.Symlink("/opt/authority-host/current/"+units+"/"+unit+".d", filepath.Join(host, unit+".d")); err != nil {
		t.Fatal(err)
	}
	drop := filepath.Join(dir, units, unit+".d", "10-start.conf")
	e.write(drop, "[Service]\nExecStart=\nExecStart=/opt/authority-host/current/bin/app\n", 0644)
	e.write(filepath.Join(dir, units, unit+".d", "20-limit.conf"), "[Service]\nLimitNOFILE=1024\n", 0644)
	e.write(filepath.Join(dir, units, "other.service.d/10-limit.conf"), "[Service]\nLimitNOFILE=1024\n", 0644)
	e.withLists(rel1, "start-worker", "health-probe", "systemctl stop "+unit)
	if e.switchTo(rel1) != ExitOK {
		t.Fatal(e.last)
	}
	inventory := e.record().CurrentUnits
	if err := os.Remove(drop); err != nil {
		t.Fatal(err)
	}
	e.release(rel2, "s")
	e.hostList("systemctl stop workr.service\n")
	active := true
	e.runner.gate = func(argv []string) error {
		switch strings.Join(argv, " ") {
		case "systemctl stop workr.service":
			return &RunError{Status: 5}
		case "systemctl stop " + unit:
			active = false
		}
		return nil
	}
	queries := 0
	e.runner.unitState = func(name string) (Output, error) {
		if name == unit {
			queries++
			if active {
				return Output{Stdout: []byte("active\n")}, nil
			}
		}
		return Output{Stdout: []byte("inactive\n")}, &RunError{Status: 3}
	}
	e.runner.reset()
	code := e.switchTo(rel2)
	if _, held := e.inhibited(); code != ExitFailed || !held || queries == 0 || e.runner.called("reload-units") != 0 {
		t.Fatalf("drop-in departure: exit %d, inhibited %t, inventory %v, queries %d, reloads %d", code, held, inventory, queries, e.runner.called("reload-units"))
	}
	if want := []string{"app.service", "other.service", unit}; !slices.Equal(inventory, want) {
		t.Fatalf("recorded inventory %v, want %v", inventory, want)
	}
	e.hostList("systemctl stop " + unit + "\n")
	if e.switchTo(rel2) != ExitOK || active {
		t.Fatal(e.last)
	}
	if _, held := e.inhibited(); held {
		t.Fatal("successful retry left decisions inhibited")
	}
}
