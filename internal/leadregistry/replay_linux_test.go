package leadregistry

import (
	"errors"
	"reflect"
	"testing"

	"ataniq/internal/leadrole"
)

// TestReplayEqualsLiveProperty drives random operation sequences, valid and
// refused, and checks after every call that a refused call changed nothing
// and wrote nothing, that a successful call is exactly one new line, that
// epochs never go backwards, and that replaying a copy of the file gives
// exactly the live state. Coordinators are in it, with the stalled state and
// the launches that depend on their parent lead.
func TestReplayEqualsLiveProperty(t *testing.T) {
	roles := []leadrole.Role{lead(t, "alpha"), lead(t, "beta"), lead(t, "gamma"), leadrole.Supervisor(),
		coord(t, "alpha", "one"), coord(t, "alpha", "two"), coord(t, "beta", "one"), coord(t, "gamma", "one")}
	coordLaunches, stalledSeen, leadRefusedForCoordinator := 0, 0, 0
	for seed := int64(1); seed <= 12; seed++ {
		seed := seed
		t.Run("", func(t *testing.T) {
			rng := newRNG(seed)
			dir := regDir(t)
			r, err := Open(dir, testOpts(seed))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			// Two roles exist up front so most operations have a target.
			if _, err := r.Define(roles[0], ManagerAtaniq, goodProfile()); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Define(roles[1], ManagerTmux, goodProfile()); err != nil {
				t.Fatal(err)
			}
			for _, c := range []leadrole.Role{roles[4], roles[5], roles[6]} {
				if _, err := r.Define(c, ManagerAtaniq, Requested{}); err != nil {
					t.Fatal(err)
				}
			}
			maxEpoch := map[leadrole.Role]uint64{}
			coordSeen := map[leadrole.Role]uint64{}
			launched := 0
			for step := 0; step < 250; step++ {
				before := snapshot(t, r)
				size := journalSize(t, dir)
				rev, _ := r.Revision()
				role := roles[rng.Intn(len(roles))]
				err := randomOp(t, r, rng, role)
				after := snapshot(t, r)
				if errors.Is(err, ErrLaunchCoordinator) {
					leadRefusedForCoordinator++
				}
				if err != nil {
					if !sameEntries(before, after) || journalSize(t, dir) != size {
						t.Fatalf("seed %d step %d: refused call (%v) changed state or file", seed, step, err)
					}
				} else if rev2, _ := r.Revision(); rev2 != rev+1 || journalSize(t, dir) <= size {
					t.Fatalf("seed %d step %d: a successful call is not exactly one new line", seed, step)
				}
				for _, e := range after {
					if e.Epoch < maxEpoch[e.Role] {
						t.Fatalf("seed %d step %d: epoch of %s went backwards", seed, step, e.Role)
					}
					maxEpoch[e.Role] = e.Epoch
					launched = max(launched, int(e.Epoch))
					if e.Role.Kind() == leadrole.KindCoordinator {
						coordLaunches += int(e.Epoch) - int(coordSeen[e.Role])
						coordSeen[e.Role] = e.Epoch
						if e.Shown.State == StateStalled {
							stalledSeen++
						}
					} else if e.Shown.State == StateStalled {
						t.Fatalf("seed %d step %d: %s is shown stalled", seed, step, e.Role)
					}
					if e.Role.Kind() == leadrole.KindCoordinator && e.Intent != nil {
						if want := e.Role.Identity(e.Intent.ParentEpoch, e.Intent.Epoch); e.Intent.Identity != want || e.Intent.ParentEpoch == 0 {
							t.Fatalf("seed %d step %d: identity %q of %s, want %q", seed, step, e.Intent.Identity, e.Role, want)
						}
					}
					if e.Intent != nil && e.Intent.Epoch != e.Epoch {
						t.Fatalf("seed %d step %d: intent epoch %d != epoch %d", seed, step, e.Intent.Epoch, e.Epoch)
					}
				}
				cp := copyJournal(t, dir)
				r2, err := Open(cp, testOpts(seed))
				if err != nil {
					t.Fatalf("seed %d step %d: replay: %v", seed, step, err)
				}
				replayed := snapshot(t, r2)
				rev2, _ := r2.Revision()
				r2.Close()
				if !reflect.DeepEqual(replayed, after) {
					t.Fatalf("seed %d step %d: replay differs from live state\nlive:   %+v\nreplay: %+v", seed, step, after, replayed)
				}
				if liveRev, _ := r.Revision(); rev2 != liveRev {
					t.Fatalf("seed %d step %d: revision %d != %d", seed, step, rev2, liveRev)
				}
			}
			if launched == 0 {
				t.Fatalf("seed %d: the sequence never launched; the property test is not exercising launches", seed)
			}
		})
	}
	if coordLaunches == 0 || stalledSeen == 0 || leadRefusedForCoordinator == 0 {
		t.Fatalf("the property test did not exercise coordinators: %d launches, %d stalled, %d lead launches refused for a coordinator",
			coordLaunches, stalledSeen, leadRefusedForCoordinator)
	}
}
