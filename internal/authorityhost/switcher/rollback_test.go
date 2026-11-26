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
