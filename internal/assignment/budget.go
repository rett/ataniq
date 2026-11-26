package assignment

// The largest required proof set is a rollback capture: one barrier, enabled
// and retired routes from two full inventories (qargi is shared), and every
// outstanding effect. Aborted needs at most one proof per route and effect.
// A refreshed inventory can add routes or effects, so it obeys the same caps.
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
