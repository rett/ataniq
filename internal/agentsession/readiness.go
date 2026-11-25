package agentsession

func (i Independent) port() bool { return i.Launch.BundleB && i.Launch.DeclaresHookRows }
func (a I2) passes(denyRow bool) bool {
	return a.A8 == "pass" && a.A12 == "pass" && a.A13 == "pass" && (!denyRow || a.A14 == "pass")
}
func (i I5) met() bool { return i.Attempted && i.EffectAbsent && i.Independent }

func independentReading(i Independent) (I4, error) {
	if i.Gates.S3Active && !i.Gates.ReadbackActive {
		return I4{}, invalid("Independent.Gates.S3Active", "inconsistent")
	}
	if i.Build.DenyMeant && !i.Build.DeclaresDenyRow {
		return I4{}, invalid("Independent.Build.DenyMeant", "declaration-omits-required-row")
	}
	for _, a := range [...]struct {
		name string
		v    Assertion
	}{{"A8", i.I2.A8}, {"A12", i.I2.A12}, {"A13", i.I2.A13}, {"A14", i.I2.A14}} {
		if !a.v.Valid() {
			return I4{}, enum("Independent.I2."+a.name, a.v)
		}
		if i.port() && (a.v == "not-checked") != (a.name == "A14" && !i.Build.DeclaresDenyRow) {
			return I4{}, invalid("Independent.I2."+a.name, "inconsistent")
		}
	}
	rb := i.Readback
	if !i.Gates.ReadbackActive || !i.Gates.ReadbackOffered {
		if err := ValidateExpectation(rb.Exp); err != nil {
			return I4{}, nestedReadbackError(err)
		}
		if err := enum("Independent.Readback.Obs.Attempt", rb.Obs.Attempt); err != nil {
			return I4{}, err
		}
		if rb.Obs.Attempt != "not-performed" {
			return I4{}, invalid("Independent.Readback.Obs.Attempt", "inconsistent")
		}
		if len(rb.Obs.Facts) != 0 {
			return I4{}, invalid("Independent.Readback.Obs.Facts", "inconsistent")
		}
		if rb.Obs.Sources.Seen {
			return I4{}, invalid("Independent.Readback.Obs.Sources.Seen", "inconsistent")
		}
	}
	v, err := ClassifyReadback(rb.Exp, rb.Obs)
	if err != nil {
		return I4{}, nestedReadbackError(err)
	}
	return v, nil
}
func ValidateIndependent(i Independent) error { _, err := independentReading(i); return err }
func independentDenyEvidence(i Independent, v I4) bool {
	return i.I3 && (v.Reading() == "matches" || v.Reading() == "unavailable" && i.I5.met())
}
func delivery(i Independent, v I4) DeliveryResult {
	var reasons []DeliveryReason
	if !i.I1 {
		reasons = append(reasons, "baseline")
	}
	if i.port() {
		if !i.I2.passes(i.Build.DeclaresDenyRow) {
			reasons = append(reasons, "assertion")
		}
		if i.Gates.ReadbackActive && v.Reading() == "contradicts" {
			reasons = append(reasons, "readback-contradicts")
		}
		if i.Gates.S3Active && i.Build.DenyMeant && !independentDenyEvidence(i, v) {
			reasons = append(reasons, "deny-evidence-missing")
		}
	}
	return DeliveryResult{len(reasons) == 0, reasons}
}
func DeliveryReady(i Independent) DeliveryResult {
	v, err := independentReading(i)
	if err != nil {
		return DeliveryResult{Reasons: []DeliveryReason{"invalid-input"}}
	}
	return delivery(i, v)
}

// The first failed independent check supplies the operator's reason. Only a
// passing row is eligible to be lowered by the current hook history.
func independentDeny(i Independent, v I4) DenyReason {
	switch {
	case !i.port():
		return "pack-no-hook-port"
	case !i.Build.DenyMeant:
		return "pack-hooks-fail-open"
	case !i.Gates.S3Active:
		return "pack-not-in-slice"
	case !i.I2.passes(i.Build.DeclaresDenyRow):
		return "pack-hooks-not-loaded"
	case !i.I3:
		return "pack-unqualified"
	case v.Outranked():
		return "pack-policy-outranked"
	case v.Reading() == "contradicts":
		return "pack-hooks-not-loaded"
	case v.Reading() == "unavailable" && !i.I5.met():
		return "pack-hooks-unverified"
	default:
		return ""
	}
}
