package switcher

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
)

// HostStopList is the host's own emergency stop list, in the policy folder
// (etc/authority-host/stop.list below the root): root-owned, checked as the
// policy is, never part of a release. Whenever the decision services must
// be stopped it runs first, before the stop lists of the releases involved,
// so that stopping never depends on a release that may not verify. A switch
// or rollback is refused without it.
const HostStopList = "stop.list"

// hostStopList reads and parses the host's stop list, which must hold at
// least one command. It is read once per command.
func (s *Switcher) hostStopList() ([][]string, error) {
	if s.hostStopsRead {
		return s.hostStops, s.hostStopsErr
	}
	s.hostStopsRead = true
	s.hostStops, s.hostStopsErr = s.readHostStopList()
	return s.hostStops, s.hostStopsErr
}

func (s *Switcher) readHostStopList() ([][]string, error) {
	full := filepath.Join(s.cfg.Root, PolicyDir, HostStopList)
	dir, err := s.openTrusted(PolicyDir, false)
	if err == nil {
		defer dir.Close()
		var data []byte
		data, err = s.readTrustedFile(dir, full, HostStopList, maxSmallFile)
		if err == nil {
			cmds, err := ParseCommandList(HostStopList, data, "", false)
			if err != nil {
				return nil, err
			}
			if len(cmds) == 0 {
				return nil, failf(CodeStopListMissing, "%s holds no command", q(full))
			}
			return cmds, nil
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, failf(CodeStopListMissing, "%s is missing: the host's own stop list is required", q(full))
	}
	return nil, err
}

// openFallback opens and verifies the release current points at before the
// switch, as the switch policy says, and keeps it with its lists: going back
// after a failure uses that release's own lists, never the candidate's. A
// release that cannot be verified, or a current link whose release is
// missing, is no fallback: a failure after the switch then stops the
// decision services instead of going back to it.
func (s *Switcher) openFallback(ctx context.Context, a *attempt) {
	if !a.oldExists {
		s.step(1, "fallback: none: this is the first switch")
		return
	}
	id, err := s.targetID(a.oldTarget)
	if err == nil {
		a.fallback, err = s.openVerified(ctx, id)
	}
	if err != nil {
		a.fallbackErr = err
		s.step(1, "fallback: none: current %s cannot be used: %s; a failure after the switch stops the decision services", q(a.oldTarget), describe(err))
		return
	}
	s.step(1, "fallback: %s verified; going back uses its reload and health lists", id.Name)
}
