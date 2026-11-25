package sessiontest

import (
	a "ataniq/internal/agentsession"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// History oracle stores the bound id at each arrival. Removing or aging one
// entry can never change the binding snapshot associated with another entry.
type arrival struct {
	report         a.HookReport
	boundAtArrival string
}
type history struct {
	view    a.HookView
	limit   time.Duration
	obs     a.Obs
	entries []arrival
}

func empty(t *testing.T, limit time.Duration) history {
	t.Helper()
	v, err := a.NewHookView(limit, "printable")
	if err != nil {
		t.Fatal(err)
	}
	return history{view: v, limit: limit}
}
func (h history) add(t *testing.T, r a.HookReport) history {
	t.Helper()
	next, err := a.Bind(h.view, r)
	if err != nil {
		t.Fatal(err)
	}
	h.view = next
	if r.Kind == "session-start" && h.obs.Bound == "" {
		h.obs.Bound = r.Session
	}
	if h.obs.Bound != r.Session {
		h.obs.Foreign++
	}
	h.entries = append(slices.Clip(h.entries), arrival{r, h.obs.Bound})
	return h
}
func (h history) remove(i int) history {
	h.view = a.Drop(h.view, i)
	h.entries = slices.Delete(slices.Clone(h.entries), i, i+1)
	return h
}
func (h history) age(t *testing.T, i int, age time.Duration) history {
	h.entries = slices.Clone(h.entries)
	h.entries[i].report.Age = age
	ages := make([]time.Duration, len(h.entries))
	for j, e := range h.entries {
		ages[j] = e.report.Age
	}
	v, err := a.Reage(h.view, ages)
	if err != nil {
		t.Fatal(err)
	}
	h.view = v
	return h
}
func (h history) reason() a.DenyReason {
	var changed bool
	for _, e := range h.entries {
		r := e.report
		if r.Session != e.boundAtArrival || h.limit <= 0 || r.Age > h.limit {
			continue
		}
		if r.Kind == "drift" {
			return "pack-mode-drift"
		}
		changed = changed || r.Kind == "config-change"
	}
	if changed {
		return "pack-hooks-unverified"
	}
	return ""
}
func sevenViews(t *testing.T) []history {
	start := a.HookReport{Kind: "session-start", Session: "s"}
	forged := a.HookReport{Kind: "session-start", Session: "t"}
	foreign := a.HookReport{Kind: "drift", Session: "t"}
	late := a.HookReport{Kind: "config-change", Session: "s"}
	expired := a.HookReport{Kind: "drift", Session: "s", Age: 2}
	var views []history
	for _, bound := range []bool{false, true} {
		for _, script := range [][]a.HookReport{nil, {start}, {forged, start}, {start, forged}, {start, foreign}, {start, late}, {start, expired}} {
			h := empty(t, 1)
			// The empty history can retain the epoch binding after its start is dropped.
			if bound && len(script) == 0 {
				h = h.add(t, start).remove(0)
			}
			for _, r := range script {
				if !bound && r.Kind == "session-start" {
					r.Kind = "other"
				}
				h = h.add(t, r)
			}
			views = append(views, h)
		}
	}
	return views
}

func readinessOracle(i a.Independent, reading a.Reading, outranked bool, h history) a.Result {
	p := i.Launch.BundleB && i.Launch.DeclaresHookRows
	assertions := []a.Assertion{i.I2.A8, i.I2.A12, i.I2.A13}
	if i.Build.DeclaresDenyRow {
		assertions = append(assertions, i.I2.A14)
	}
	asserted := !slices.Contains(assertions, a.Assertion("fail"))
	experiment := i.I5 == (a.I5{Attempted: true, EffectAbsent: true, Independent: true})
	evidence := i.I3 && (reading == "matches" || reading == "unavailable" && experiment)
	var reasons []a.DeliveryReason
	if !i.I1 {
		reasons = append(reasons, "baseline")
	}
	if p && !asserted {
		reasons = append(reasons, "assertion")
	}
	if p && i.Gates.ReadbackActive && reading == "contradicts" {
		reasons = append(reasons, "readback-contradicts")
	}
	if p && i.Gates.S3Active && i.Build.DenyMeant && !evidence {
		reasons = append(reasons, "deny-evidence-missing")
	}
	checks := []struct {
		fail bool
		why  a.DenyReason
	}{
		{!p, "pack-no-hook-port"}, {!i.Build.DenyMeant, "pack-hooks-fail-open"}, {!i.Gates.S3Active, "pack-not-in-slice"},
		{!asserted, "pack-hooks-not-loaded"}, {!i.I3, "pack-unqualified"}, {reading == "contradicts" && outranked, "pack-policy-outranked"},
		{reading == "contradicts", "pack-hooks-not-loaded"}, {reading == "unavailable" && !experiment, "pack-hooks-unverified"},
	}
	why := h.reason()
	for _, c := range checks {
		if c.fail {
			why = c.why
			break
		}
	}
	return a.Result{Delivery: a.DeliveryResult{Ready: len(reasons) == 0, Reasons: reasons}, Deny: a.DenyResult{InForce: why == "", Reason: why}, Obs: h.obs}
}
func equalDelivery(x, y a.DeliveryResult) bool {
	return x.Ready == y.Ready && slices.Equal(x.Reasons, y.Reasons)
}
func compareReady(t *testing.T, i a.Independent, reading a.Reading, outranked bool, h history, d a.DeliveryResult) a.DenyResult {
	got, err := a.Readiness(i, h.view)
	want := readinessOracle(i, reading, outranked, h)
	if err != nil || got.Obs != want.Obs || got.Deny != want.Deny || !equalDelivery(got.Delivery, want.Delivery) || !equalDelivery(d, got.Delivery) {
		t.Fatalf("independent %+v, hook %+v, got %+v (%v), want %+v", i, h, got, err, want)
	}
	return got.Deny
}

type sample struct {
	i       a.Independent
	reading a.Reading
	out     bool
}

func sampleInput(f Fixture, g a.Gates, n int) sample {
	i := f.Independent()
	i.Gates = g
	if !g.ReadbackOffered || !g.ReadbackActive {
		i.Readback = f.Unavailable("not-performed")
		return sample{i, "unavailable", false}
	}
	switch n {
	case 0:
		return sample{i, "matches", false}
	case 1:
		i.Readback = f.Contradicts("folder-trust")
		return sample{i, "contradicts", false}
	case 2:
		i.Readback = f.Contradicts("folder-trust", "outranking-policy")
		return sample{i, "contradicts", true}
	default:
		i.Readback = f.Unavailable("partial")
		return sample{i, "unavailable", false}
	}
}
func experiment(mask int) a.I5 {
	return a.I5{Attempted: mask&1 != 0, EffectAbsent: mask&2 != 0, Independent: mask&4 != 0}
}
func assertions(mask int, row, port bool) a.I2 {
	var x a.I2
	for n, field := range [...]*a.Assertion{&x.A8, &x.A12, &x.A13, &x.A14} {
		if !port {
			*field = [...]a.Assertion{"pass", "fail", "not-checked"}[mask%3]
			mask /= 3
			continue
		}
		*field = "pass"
		if mask&(1<<n) != 0 {
			*field = "fail"
		}
	}
	if !row && port {
		x.A14 = "not-checked"
	}
	return x
}
func TestTR1TR22ReadinessProduct(t *testing.T) {
	views := sevenViews(t)
	for launch := range 4 {
		for gates := range 8 {
			g := a.Gates{ReadbackActive: gates&1 != 0, ReadbackOffered: gates&2 != 0, S3Active: gates&4 != 0}
			if g.S3Active && !g.ReadbackActive {
				continue
			}
			for build := range 3 {
				f := Beta()
				if build == 2 {
					f = Alpha()
				}
				for reading := range 4 {
					if reading > 0 && (!g.ReadbackActive || !g.ReadbackOffered) {
						continue
					}
					s := sampleInput(f, g, reading)
					i := s.i
					i.Build.DeclaresDenyRow = build != 0
					i.Launch = a.Launch{BundleB: launch&1 != 0, DeclaresHookRows: launch&2 != 0}
					count := 81
					if launch == 3 {
						count = 16
						if build == 0 {
							count = 8
						}
					}
					for mask := range count {
						i.I2 = assertions(mask, build != 0, launch == 3)
						for inputs := range 32 {
							i.I1 = inputs&1 != 0
							i.I3 = inputs&2 != 0
							i.I5 = experiment(inputs >> 2)
							d := a.DeliveryReady(i)
							none := a.DenyInForce(i, views[0].view)
							for _, h := range views {
								got := compareReady(t, i, s.reading, s.out, h, d)
								if got.InForce && !none.InForce {
									t.Fatal("V7", i, h)
								}
							}
						}
					}
				}
			}
		}
	}
}
func verifyHistory(t *testing.T, s sample, h history) bool {
	d := a.DeliveryReady(s.i)
	r := compareReady(t, s.i, s.reading, s.out, h, d)
	if r != a.DenyInForce(s.i, h.view) {
		t.Fatal("deny result", s, h)
	}
	return r.InForce
}
func TestTR27HistoryProperties(t *testing.T) {
	kinds := a.HookKind("").All()
	var reports []a.HookReport
	for _, k := range kinds {
		for _, id := range []string{"s", "t"} {
			for age := range 3 {
				reports = append(reports, a.HookReport{Kind: k, Session: id, Age: time.Duration(age)})
			}
		}
	}
	s := sampleInput(Alpha(), a.Gates{ReadbackActive: true, ReadbackOffered: true, S3Active: true}, 0)
	// Exhaust the short histories, including every two-report order and every
	// one-report deletion, expiration and return to the live interval.
	for limit := range 2 {
		for _, r := range reports {
			for _, r2 := range reports {
				h := empty(t, time.Duration(limit)).add(t, r)
				before := verifyHistory(t, s, h)
				h = h.add(t, r2)
				if verifyHistory(t, s, h) && !before {
					t.Fatal("append raised deny", h)
				}
				for i := range h.entries {
					verifyHistory(t, s, h.remove(i))
					for age := range 3 {
						verifyHistory(t, s, h.age(t, i, time.Duration(age)))
					}
				}
			}
		}
	}
	rng := rand.New(rand.NewSource(2813))
	for range 100000 {
		f := Alpha()
		if rng.Intn(4) == 0 {
			f = Beta()
		}
		g := a.Gates{ReadbackActive: true, ReadbackOffered: rng.Intn(4) != 0, S3Active: rng.Intn(4) != 0}
		s := sampleInput(f, g, rng.Intn(4))
		s.i.I1 = rng.Intn(2) != 0
		s.i.I3 = rng.Intn(4) != 0
		s.i.I5 = experiment(rng.Intn(8))
		s.i.Launch.BundleB = rng.Intn(4) != 0
		h := empty(t, time.Duration(rng.Intn(3)))
		none := verifyHistory(t, s, h)
		for range rng.Intn(9) {
			h = h.add(t, reports[rng.Intn(len(reports))])
		}
		before := verifyHistory(t, s, h)
		h = h.add(t, reports[rng.Intn(len(reports))])
		after := verifyHistory(t, s, h)
		if after && (!none || !before) {
			t.Fatal("V7 append", h)
		}
		i := rng.Intn(len(h.entries))
		verifyHistory(t, s, h.remove(i))
		verifyHistory(t, s, h.age(t, i, time.Duration(rng.Intn(4))))
	}
}
func TestTR36MissingSources(t *testing.T) {
	f := Alpha()
	s := sampleInput(f, f.Independent().Gates, 3)
	h := empty(t, 1)
	for _, e := range []bool{false, true} {
		s.i.I5.Independent = e
		s.i.I5.Attempted = true
		s.i.I5.EffectAbsent = true
		r, err := a.Readiness(s.i, h.view)
		if err != nil || r.Delivery.Ready != e || r.Deny.InForce != e || !e && !slices.Equal(r.Delivery.Reasons, []a.DeliveryReason{"deny-evidence-missing"}) {
			t.Fatal(r, err)
		}
	}
	s.i.Readback = f.Contradicts("folder-trust")
	s.i.Readback.Obs.Sources.Seen = false
	if verifyHistory(t, sample{s.i, "contradicts", false}, h) {
		t.Fatal(s)
	}
}
