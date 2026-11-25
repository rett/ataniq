package sessiontest

import (
	a "ataniq/internal/agentsession"
	"bytes"
	"encoding/json"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"
)

// Change both the backing array and its spare capacity. An accidental append
// into caller-owned storage can otherwise be invisible to value comparisons.
func recopy[T any](s []T, spare int) []T {
	if s == nil {
		return nil
	}
	return append(make([]T, 0, len(s)+spare), s...)
}
func digestSources(t *testing.T, reverse bool) [32]byte {
	t.Helper()
	one, err := a.PolicySourceFromBytes("config", "one", []byte("data\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.PolicySourceFromBytes("config", "two", nil)
	if err != nil {
		t.Fatal(err)
	}
	set := []a.PolicySource{one, two}
	if reverse {
		slices.Reverse(set)
	}
	d, err := a.PolicySourcesDigest(set)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func evaluate(t *testing.T, spare int) []any {
	f := Alpha()
	i := f.Independent()
	i.Readback = f.Contradicts("folder-trust", "outranking-policy")
	i.Readback.Exp.Facts = recopy(i.Readback.Exp.Facts, spare)
	i.Readback.Obs.Facts = recopy(i.Readback.Obs.Facts, 16-spare)
	if spare > 0 {
		slices.Reverse(i.Readback.Exp.Facts)
		slices.Reverse(i.Readback.Obs.Facts)
	}
	h := empty(t, 1)
	for _, r := range []a.HookReport{{Kind: "session-start", Session: "s"}, {Kind: "config-change", Session: "s"}, {Kind: "other", Session: "t"}} {
		h = h.add(t, r)
	}
	before := h.view
	v, err := a.Reage(before, recopy([]time.Duration{4, 2, 1}, spare))
	if err != nil {
		t.Fatal(err)
	}
	after := a.Drop(v, 1)
	in := a.Inputs{Transport: a.Claim{State: "idle", Basis: "screen", Detail: recopy([]string{"prompt"}, spare)}, Hook: self("blocked", "hook"), Veto: recopy([]string{"dialog"}, spare), Sight: "rules"}
	r, err := a.Readiness(i, v)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.ClassifyReadback(i.Readback.Exp, i.Readback.Obs)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{a.ValidateIndependent(i), a.ValidateExpectation(i.Readback.Exp), a.ValidateObs(i.Readback.Exp, i.Readback.Obs), a.ValidateInputs(in), a.ValidateHooks(v)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return []any{r, c, a.DeliveryReady(i), a.DenyInForce(i, v), a.Observation(v), a.Compose(in), digestSources(t, spare > 0), before, v, after}
}
func TestTR30Determinism(t *testing.T) {
	want := evaluate(t, 0)
	golden, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	for _, procs := range []int{1, 8} {
		runtime.GOMAXPROCS(procs)
		for n := range 1000 {
			got := evaluate(t, (n%2)*16)
			b, err := json.Marshal(got)
			if err != nil || !bytes.Equal(b, golden) || !reflect.DeepEqual(got, want) {
				t.Fatal(procs, n, string(b), err)
			}
		}
	}
}
