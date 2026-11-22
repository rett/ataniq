package switcher

import (
	"context"
	"path/filepath"
)

// openFallback opens and verifies the release current points at before the
// switch, as the switch policy says, and keeps it with its reload and health
// lists: going back after a failure uses that release's own lists, never the
// candidate's. A release that cannot be verified, or a current link whose
// release is missing, is no fallback: a failure after the switch then stops
// the decision services instead of going back to it.
func (s *Switcher) openFallback(ctx context.Context, a *attempt) {
	if !a.oldExists {
		s.step(1, "fallback: none: this is the first switch")
		return
	}
	id, err := s.targetID(a.oldTarget)
	if err == nil {
		b := &budget{lim: s.cfg.Limits, deadline: s.cfg.Now().Add(s.cfg.Limits.Time), now: s.cfg.Now}
		var rel *release
		rel, err = openRelease(id, s.relRoot, id.Name, filepath.Join(s.base(), "releases", id.Name), s.entryTrusted, b)
		if err == nil {
			if err = s.checkRelease(ctx, rel); err != nil {
				rel.Close()
			} else {
				a.fallback = rel
			}
		}
	}
	if err != nil {
		a.fallbackErr = err
		s.step(1, "fallback: none: current %s cannot be used: %s; a failure after the switch stops the decision services", q(a.oldTarget), describe(err))
		return
	}
	s.step(1, "fallback: %s verified; going back uses its reload and health lists", id.Name)
}

// goBack is called when the reload or the health check failed after current
// was moved to the candidate. With a fallback it puts current and previous
// back, runs the fallback's reload list and checks the fallback's health:
// the result is rolled-back with restored_health ok only when every one of
// those succeeded. Otherwise, and when there is no fallback, the candidate's
// stop list stops the decision services (the static status page stays) and
// the result is failed-recovery, or failed on a first switch.
func (s *Switcher) goBack(ctx context.Context, a *attempt, step string, cause error) int {
	n := 7
	if step == "reload" {
		n = 6
	}
	s.setFailure(a, step, cause)
	s.step(n, "FAILED: %s", describe(cause))
	failedID := a.rec.CurrentID
	if a.fallback == nil {
		a.rec.Result = ResultFailedRecovery
		if a.oldExists {
			a.rec.Detail += "; the earlier release cannot be used (" + describe(a.fallbackErr) + "), so current stays at " + failedID
		} else {
			a.rec.Result = ResultFailed
			a.rec.Detail += "; there is no earlier release to go back to, so current stays at " + failedID
		}
		s.stopServices(ctx, a)
		if s.record(a) == nil {
			s.dropJournal()
		}
		s.printf("result: %s at %s; current stays at %s; the decision services are stopped", a.rec.Result, step, failedID)
		return ExitFailed
	}
	keepJournal, err := s.restore(ctx, a)
	if err != nil {
		a.rec.Result = ResultFailedRecovery
		a.rec.RestoredHealth = "failed"
		a.rec.Detail += "; going back failed: " + describe(err)
		s.stopServices(ctx, a)
		if s.record(a) == nil && !keepJournal {
			s.dropJournal()
		}
		s.printf("result: failed-recovery: %s failed at %s and going back failed; current -> %s; the decision services are stopped", failedID, step, a.rec.CurrentID)
		return ExitFailed
	}
	a.rec.RestoredHealth = "ok"
	a.rec.Result = ResultRolledBack
	if s.record(a) == nil {
		s.dropJournal()
	}
	s.printf("result: rolled-back: %s failed at %s, current -> %s (health after going back: ok)", failedID, step, a.rec.CurrentID)
	return ExitFailed
}

// restore points current back at the fallback, puts previous back as it was,
// runs the fallback's reload list and checks its health. keepJournal says
// that current could not be put back, so the journal must stay for the next
// command.
func (s *Switcher) restore(ctx context.Context, a *attempt) (keepJournal bool, err error) {
	if err := replaceLink(s.baseRoot, "current", a.oldTarget); err != nil {
		return true, failf(CodeLinkFailed, "current: %s", q(err.Error()))
	}
	a.rec.CurrentID = a.fallback.id.Name
	s.restorePrevious(a)
	s.printf("back: current -> %s", q(a.oldTarget))
	if err := s.reload(ctx, a.fallback.reloads); err != nil {
		return false, err
	}
	if err := s.checkHealth(ctx, a.fallback.health); err != nil {
		return false, err
	}
	return false, nil
}

// stopServices runs the candidate's stop list. A command that fails is noted
// in the record; the others still run.
func (s *Switcher) stopServices(ctx context.Context, a *attempt) {
	if len(a.stops) == 0 {
		s.step(7, "no stop command to run")
		return
	}
	for _, c := range a.stops {
		if _, err := s.cfg.Runner.Run(ctx, Command{Argv: c, Timeout: ReloadTimeout}); err != nil {
			s.step(7, "stop FAILED: %s: %s", qargv(c), summary(err))
			a.rec.Detail += "; stop failed: " + q(c[0]) + ": " + summary(err)
			continue
		}
		s.step(7, "stopped: %s", qargv(c))
	}
}
