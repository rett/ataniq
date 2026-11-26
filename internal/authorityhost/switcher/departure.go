package switcher

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
)

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9_.@:+-]+\.service$`)

// checkInactive accepts only a complete, single-unit manager observation.
// Exit 3 alone also covers failed and transitional states.
func (s *Switcher) checkInactive(ctx context.Context, units, restarted []string) error {
	for _, unit := range units {
		if !serviceName.MatchString(unit) || len(unit) > 255 {
			return failf(CodeStopFailed, "invalid service unit name %s; decisions stay inhibited", q(unit))
		}
		if slices.Contains(restarted, unit) {
			continue
		}
		// A template file has no process of its own. Ask the manager about
		// every loaded instance of that exact template, including inactive ones.
		if prefix, template := strings.CutSuffix(unit, "@.service"); template {
			out, err := s.cfg.Runner.Run(ctx, Command{Argv: []string{"systemctl", "show", "--all", "--property=ActiveState", "--value", "--", prefix + "@*.service"}, Timeout: ReloadTimeout})
			if err != nil || out.Truncated || len(out.Stderr) > 0 {
				return failf(CodeStopFailed, "cannot query instances of %s; decisions stay inhibited", q(unit))
			}
			for _, state := range strings.Fields(string(out.Stdout)) {
				if state != "inactive" {
					return failf(CodeStopFailed, "%s has an instance not confirmed inactive; decisions stay inhibited", q(unit))
				}
			}
			continue
		}
		out, err := s.cfg.Runner.Run(ctx, Command{Argv: []string{"systemctl", "is-active", "--", unit}, Timeout: ReloadTimeout})
		state := strings.TrimSuffix(string(out.Stdout), "\n")
		var re *RunError
		if !out.Truncated && errors.As(err, &re) && !re.NotStarted && re.Timeout == 0 {
			if (state == "inactive" && (re.Status == 3 || re.Status == 4)) || (state == "unknown" && re.Status == 4) {
				continue
			}
		}
		return failf(CodeStopFailed, "%s not confirmed inactive or absent by systemctl is-active; decisions stay inhibited", q(unit))
	}
	return nil
}

// departing selects the recorded inventory for the direction of activation.
// OldCurrent is empty on first deployment and otherwise a checked releases/<id>.
func (j *journal) departing(restoring bool) (string, *[]string) {
	if restoring {
		return j.ID, &j.CandidateUnits
	}
	return strings.TrimPrefix(j.OldCurrent, "releases/"), &j.OldUnits
}

// stopDeparting runs both stop lists, then checks the recorded services.
func (s *Switcher) stopDeparting(ctx context.Context, t *txn, restoring bool) error {
	id, names := t.j.departing(restoring)
	if id == "" {
		return nil
	}
	var rel *release
	var err error
	if restoring {
		rel, err = s.candidate(ctx, t)
	} else {
		rel, err = s.fallback(ctx, t)
	}
	var failures []string
	host, hostErr := s.hostStopList()
	if hostErr != nil {
		failures = append(failures, "host stop list: "+describe(hostErr))
	} else {
		failures = s.runStops(ctx, "host", host)
	}
	if err != nil {
		s.printf("stop: skipped the departing release stop list because verification failed (%s)", describe(err))
	} else {
		failures = append(failures, s.runStops(ctx, id, rel.stops)...)
	}
	if err := s.confirmStopped(ctx, id, *names); err != nil {
		return err
	}
	if len(failures) > 0 {
		return failf(CodeStopFailed, "departure stops failed (%s); decisions stay inhibited", joinFailures(failures))
	}
	return nil
}

// confirmStopped also serves the terminal stopping phase, which runs all
// executable stop lists before checking either release.
func (s *Switcher) confirmStopped(ctx context.Context, id string, names []string) error {
	if names == nil {
		return failf(CodeStopFailed, "missing service inventory for %s; decisions stay inhibited. Stop the decision services, restore the original imported release, run ah-switch recover --services-stopped and retry", id)
	}
	return s.checkInactive(ctx, names, nil)
}
