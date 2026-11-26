package switcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Keep the inventories of both verified trees. Either can become the
// departing release if activation fails or an interrupted switch goes back.
const systemUnits = "etc/systemd/system"

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9_.@:+-]+\.service$`)

// installedUnits recovers names for legacy state without an inventory.
// Read the target text even when current has moved and the link dangles.
func (s *Switcher) installedUnits(id string) ([]string, error) {
	dir, err := s.openTrusted(systemUnits, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 || !strings.HasSuffix(entry.Name(), ".service") {
			continue
		}
		target, err := dir.Readlink(entry.Name())
		if err != nil {
			return nil, err
		}
		if !path.IsAbs(target) {
			target = path.Join("/", systemUnits, target)
		}
		parent := path.Dir(target)
		if parent == path.Join("/", BaseDir, "current", systemUnits) || parent == path.Join("/", BaseDir, "releases", id, systemUnits) {
			names = append(names, path.Base(target))
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no installed release unit links for %s", id)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

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

// stopDeparting runs both stop lists, then checks the actual shipped units.
// Old journals can lack inventory. Save recovered names before relying on
// them so a later retry can proceed even if the tree or a link disappears.
func (s *Switcher) stopDeparting(ctx context.Context, t *txn, restoring bool) error {
	var rel *release
	var err error
	id := t.j.ID
	names := &t.j.CandidateUnits
	if restoring {
		rel, err = s.candidate(ctx, t)
	} else {
		if t.j.OldCurrent == "" {
			return nil
		}
		old, _ := s.targetID(t.j.OldCurrent)
		id = old.Name
		names = &t.j.OldUnits
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
	if *names == nil {
		if rel != nil {
			*names = rel.units
		} else {
			units, err := s.installedUnits(id)
			if err != nil {
				return failf(CodeStopFailed, "cannot identify services of %s: %s; decisions stay inhibited", id, q(err.Error()))
			}
			*names = units
		}
		if err := s.save(t); err != nil {
			return err
		}
	}
	if err := s.checkInactive(ctx, *names, nil); err != nil {
		return err
	}
	if len(failures) > 0 {
		return failf(CodeStopFailed, "departure stops failed (%s); decisions stay inhibited", joinFailures(failures))
	}
	return nil
}
