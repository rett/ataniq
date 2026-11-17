package leadregistry

import (
	"reflect"
	"testing"

	"ataniq/internal/leadrole"
)

// TestReplayEqualsLiveProperty drives random operation sequences, valid and
// refused, and checks after every call that a refused call changed nothing
// and wrote nothing, that a successful call is exactly one new line, that
// epochs never go backwards, and that replaying a copy of the file gives
// exactly the live state.
func TestReplayEqualsLiveProperty(t *testing.T) {
	roles := []leadrole.Role{lead(t, "alpha"), lead(t, "beta"), lead(t, "gamma"), leadrole.Supervisor()}
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
			maxEpoch := map[leadrole.Role]uint64{}
			launched := 0
			for step := 0; step < 120; step++ {
				before := snapshot(t, r)
				size := journalSize(t, dir)
				rev, _ := r.Revision()
				role := roles[rng.Intn(len(roles))]
				err := randomOp(t, r, rng, role)
				after := snapshot(t, r)
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
}
