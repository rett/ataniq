package agentsession

import (
	"reflect"
	"slices"
	"testing"
)

func passing() Independent {
	return Independent{Launch: Launch{true, true}, Gates: Gates{true, true, true}, Build: Build{true, true}, I1: true, I2: I2{"pass", "pass", "pass", "pass"}, I3: true, Readback: Readback{Obs: ReadbackObs{Attempt: "read", Sources: SourcesObs{Seen: true}}}}
}
func TestTR2ToTR7AndTR15ToTR21Independent(t *testing.T) {
	for _, row := range []struct {
		name    string
		edit    func(*Independent)
		why     DenyReason
		reasons []DeliveryReason
	}{
		{"baseline", func(i *Independent) { i.I1 = false }, "", []DeliveryReason{"baseline"}},
		{"A8", func(i *Independent) { i.I2.A8 = "fail" }, "pack-hooks-not-loaded", []DeliveryReason{"assertion"}},
		{"A12", func(i *Independent) { i.I2.A12 = "fail" }, "pack-hooks-not-loaded", []DeliveryReason{"assertion"}},
		{"A13", func(i *Independent) { i.I2.A13 = "fail" }, "pack-hooks-not-loaded", []DeliveryReason{"assertion"}},
		{"A14", func(i *Independent) { i.I2.A14 = "fail" }, "pack-hooks-not-loaded", []DeliveryReason{"assertion"}},
		{"no deny", func(i *Independent) { i.Build = Build{}; i.I2.A14 = "not-checked" }, "pack-hooks-fail-open", nil},
		{"unoffered", func(i *Independent) {
			i.Gates.ReadbackOffered = false
			i.Readback.Obs = ReadbackObs{Attempt: "not-performed"}
		}, "pack-hooks-unverified", []DeliveryReason{"deny-evidence-missing"}},
		{"unoffered independently met", func(i *Independent) {
			i.Gates.ReadbackOffered = false
			i.Readback.Obs = ReadbackObs{Attempt: "not-performed"}
			i.I5 = I5{true, true, true}
		}, "", nil},
		{"contradiction despite experiment", func(i *Independent) { i.Readback.Obs.Sources.Digest[0] = 1; i.I5 = I5{true, true, true} }, "pack-hooks-not-loaded", []DeliveryReason{"readback-contradicts", "deny-evidence-missing"}},
		{"unverified", func(i *Independent) { i.Readback.Obs = ReadbackObs{Attempt: "timeout"} }, "pack-hooks-unverified", []DeliveryReason{"deny-evidence-missing"}},
		{"unqualified", func(i *Independent) { i.I3 = false; i.I5 = I5{true, true, true} }, "pack-unqualified", []DeliveryReason{"deny-evidence-missing"}},
		{"before slice", func(i *Independent) { i.Gates.S3Active = false; i.Readback.Obs = ReadbackObs{Attempt: "error"} }, "pack-not-in-slice", nil},
		{"no port", func(i *Independent) {
			i.Launch = Launch{}
			i.I2 = I2{"not-checked", "not-checked", "not-checked", "not-checked"}
		}, "pack-no-hook-port", nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			i := passing()
			row.edit(&i)
			v, err := independentReading(i)
			d := DeliveryReady(i)
			if err != nil || independentDeny(i, v) != row.why || !slices.Equal(d.Reasons, row.reasons) || d.Ready != (len(row.reasons) == 0) {
				t.Fatal(i, v, d, err)
			}
		})
	}
}
func TestTR5TR6TR21bTR34InvalidIndependent(t *testing.T) {
	for _, row := range []struct {
		edit   func(*Independent)
		member string
		why    InputReason
	}{
		{func(i *Independent) { i.Gates.ReadbackActive = false }, "Gates.S3Active", "inconsistent"},
		{func(i *Independent) { i.Build.DeclaresDenyRow = false }, "Build.DenyMeant", "declaration-omits-required-row"},
		{func(i *Independent) { i.Launch = Launch{}; i.Build.DeclaresDenyRow = false }, "Build.DenyMeant", "declaration-omits-required-row"},
		{func(i *Independent) { i.Build = Build{} }, "I2.A14", "inconsistent"},
		{func(i *Independent) { i.Gates = Gates{} }, "Readback.Obs.Attempt", "inconsistent"},
		{func(i *Independent) { i.Launch = Launch{}; i.Readback.Obs.Attempt = "x" }, "Readback.Obs.Attempt", "unknown-value"},
		{func(i *Independent) { i.I2.A12 = "" }, "I2.A12", "empty-value"},
		{func(i *Independent) {
			i.Readback.Exp.Facts = []FactExp{{"folder-trust", true, "trusted"}}
			i.Readback.Obs.Facts = []FactObs{{Fact: "mode-admitted"}, {"folder-trust", true, "no"}}
		}, "Readback.Obs.Facts[1].Got", "inconsistent"},
	} {
		i := passing()
		row.edit(&i)
		requireError(t, ValidateIndependent(i), "Independent."+row.member, row.why)
		if !reflect.DeepEqual(DeliveryReady(i), DeliveryResult{Reasons: []DeliveryReason{"invalid-input"}}) {
			t.Fatal(i)
		}
	}
}
