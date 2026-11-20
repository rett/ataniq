//go:build linux && ataniq_canary

package sandboxstart

import (
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// digestOf is ProfileDigest of the world's inputs with the calling ids of the
// fake system.
func digestOf(t *testing.T, w *world) string {
	t.Helper()
	uid, gid := w.sys.IDs()
	d, err := ProfileDigest(w.in, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestProfileDigestIsTheDigestOfTheStart(t *testing.T) {
	for name, mod := range map[string]func(*world){
		"plain": func(*world) {},
		"with an agent environment": func(w *world) {
			w.in.AgentEnv = []profile.EnvVar{{Name: "SAMPLE_FLAG", Value: "one"}, {Name: "SAMPLE_OTHER", Value: "two"}}
		},
		"owner identity": func(w *world) { w.in.Identity, w.in.OwnerUser = string(profile.IdentityOwner), "sampleowner" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			mod(w)
			want := digestOf(t, w)
			if len(want) != 64 || want != strings.ToLower(want) {
				t.Fatalf("digest %q is not 64 lower-case hex digits", want)
			}
			st, err := w.run_()
			if err != nil {
				t.Fatal(err)
			}
			if st.ProfileDigest != want || st.Plan.Digest != want {
				t.Fatalf("the start's digest is %s (plan %s), ProfileDigest says %s", st.ProfileDigest, st.Plan.Digest, want)
			}
			// A start pinned to it is not refused.
			w2 := newWorld(t)
			mod(w2)
			w2.in.ExpectedDigest = want
			if _, err := w2.run_(); err != nil {
				t.Fatalf("a start pinned to the digest was refused: %v", err)
			}
		})
	}
}

func TestProfileDigestIsStableAndFollowsItsInputs(t *testing.T) {
	w := newWorld(t)
	base := digestOf(t, w)
	if again := digestOf(t, w); again != base {
		t.Fatalf("not stable: %s then %s", base, again)
	}
	// What the digest does not cover does not change it: the roots.
	other := newWorld(t)
	if d := digestOf(t, other); d != base {
		t.Errorf("another host root changed the digest: %s, %s", base, d)
	}
	for name, mod := range map[string]func(*Inputs){
		"managed settings content": func(in *Inputs) { in.Sources.ManagedSettingsSHA256 = strings.Repeat("cd", 32) },
		"runtime version":          func(in *Inputs) { in.RuntimeVersion = "2.2.0" },
		"identity mode":            func(in *Inputs) { in.Identity, in.OwnerUser = string(profile.IdentityOwner), "sampleowner" },
		"agent environment":        func(in *Inputs) { in.AgentEnv = []profile.EnvVar{{Name: "SAMPLE_FLAG", Value: "one"}} },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			mod(&w.in)
			if d := digestOf(t, w); d == base {
				t.Errorf("the digest did not change")
			}
		})
	}
}

func TestProfileDigestRefusesWrongInputsByField(t *testing.T) {
	w := newWorld(t)
	w.in.Runtime = "no-such-runtime"
	uid, gid := w.sys.IDs()
	if d, err := ProfileDigest(w.in, uid, gid); err == nil || d != "" {
		t.Fatalf("digest %q, error %v", d, err)
	}
}
