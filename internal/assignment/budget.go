package assignment

import "fmt"

// The largest required proof set is a rollback capture: one barrier, enabled
// and retired routes from two full inventories (qargi is shared), and every
// outstanding effect. Aborted needs at most one proof per route and effect.
// Eight routes and eight outstanding effects per project are operating limits.
// They apply to capture, refreshed inventories and reported corrections.
const (
	MaxRoutes             = 8
	MaxOutstandingEffects = 8
	maxFollowupProofs     = 2*MaxRoutes + MaxOutstandingEffects
	// The signed LF frame's command, result and public projection contain
	// at most 35 variable identifier slots outside evidence. Each proof adds
	// 10 (including its scoped head) and one variable disposition. The fixed
	// allowances cover JSON members, hashes, enums, full-width counters,
	// evidence_digests and the SSHSIG. TestFollowupFrameBudget measures both
	// against the encoded schema with empty variable slots.
	baseFrameAllowance  = 2300
	proofFrameAllowance = 610
	MaxReferenceBytes   = (recordLimit - baseFrameAllowance - maxFollowupProofs*proofFrameAllowance) / (35 + 11*maxFollowupProofs)
)

func checkCount(resource string, count, limit int) error {
	if count > limit {
		return fmt.Errorf("%w: %s: observed %d, limit %d", ErrCapacity, resource, count, limit)
	}
	return nil
}

// Check the intake counts before encoding the request so a large inventory
// can still tell the adapter which operating limit it exceeded. A refresh
// also checks the merged inventory in inventoryEffects.
func (l Ledger) intakeDigest(c Command) (string, error) {
	var routeOutcome string
	switch c.Kind {
	case Seed, Freeze, RollbackFreeze:
		routeOutcome = "enabled"
	case Inventory:
		routeOutcome = "frozen"
	case FenceReceipt, ConfirmReceipt:
		target, report := routeReport(c)
		if prior, ok := l.receipts[target]; !ok || prior.Issuer != report.Issuer {
			return requestDigest(c)
		}
	default:
		return requestDigest(c)
	}
	var routes, effects int
	for _, e := range c.Evidence {
		switch e.Kind {
		case "route":
			if routeOutcome == "" || e.Outcome == routeOutcome {
				routes++
			}
		case "effect":
			if e.Outcome == "outstanding" {
				effects++
			}
		}
	}
	if err := checkCount("routes", routes, MaxRoutes); err != nil {
		return "", err
	}
	if err := checkCount("outstanding effects", effects, MaxOutstandingEffects); err != nil {
		return "", err
	}
	return requestDigest(c)
}

// Measure the canonical JSON string content, excluding the two quotes. The
// encoder validates UTF-8 separately. ASCII controls can cost six bytes each.
func referencesFit(refs ...string) bool {
	for _, s := range refs {
		size := len(s)
		if size > MaxReferenceBytes {
			return false
		}
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				size++
			default:
				if s[i] < 0x20 {
					size += 5
				}
			}
		}
		if size > MaxReferenceBytes {
			return false
		}
	}
	return true
}

func (c Command) checkReferences() error {
	k, b := c.Expected.Key, c.Candidate
	if !referencesFit(k.Home, k.Project, k.Role, b.Authority, b.Conversation, b.ID, b.Incarnation, b.LaunchIntent, b.Store,
		c.TransferID, c.RequestID, c.Actor, c.Authorization) {
		return ErrLimit
	}
	for _, e := range c.Evidence {
		if !referencesFit(e.Issuer, e.Revision, e.Subject, e.Route, e.Outcome) {
			return ErrLimit
		}
	}
	return nil
}
