// Package agentsession models session state and readiness from caller-supplied evidence.
package agentsession

import "slices"

type State string

func (State) All() []State  { return []State{"idle", "busy", "blocked", "asking", "ended", "unknown"} }
func (v State) Valid() bool { return slices.Contains(v.All(), v) }

type Basis string

func (Basis) All() []Basis  { return []Basis{"host", "transport", "self", "screen"} }
func (v Basis) Valid() bool { return slices.Contains(v.All(), v) }

type Sight string

func (Sight) All() []Sight  { return []Sight{"rules", "blind", "none"} }
func (v Sight) Valid() bool { return slices.Contains(v.All(), v) }

type Permit string

func (Permit) All() []Permit { return []Permit{"transport", "self-checked"} }
func (v Permit) Valid() bool { return slices.Contains(v.All(), v) }

type Assertion string

func (Assertion) All() []Assertion { return []Assertion{"pass", "fail", "not-checked"} }
func (v Assertion) Valid() bool    { return slices.Contains(v.All(), v) }

type Reading string

func (Reading) All() []Reading { return []Reading{"matches", "contradicts", "unavailable"} }
func (v Reading) Valid() bool  { return slices.Contains(v.All(), v) }

type Cause string

func (Cause) All() []Cause {
	return []Cause{"", "not-performed", "error", "timeout", "unparsable", "partial"}
}
func (v Cause) Valid() bool { return slices.Contains(v.All(), v) }

type Fact string

func (Fact) All() []Fact {
	return []Fact{"hooks-loaded", "folder-trust", "mode-admitted", "outranking-policy", "policy-sources"}
}
func (v Fact) Valid() bool { return slices.Contains(v.All(), v) }

type Want string

func (Want) All() []Want   { return []Want{"yes", "no", "trusted", "untrusted"} }
func (v Want) Valid() bool { return slices.Contains(v.All(), v) }

type Outcome string

func (Outcome) All() []Outcome {
	return []Outcome{"read", "not-performed", "error", "timeout", "unparsable"}
}
func (v Outcome) Valid() bool { return slices.Contains(v.All(), v) }

type HookKind string

func (HookKind) All() []HookKind {
	return []HookKind{"session-start", "drift", "config-change", "other"}
}
func (v HookKind) Valid() bool { return slices.Contains(v.All(), v) }

type IDForm string

func (IDForm) All() []IDForm { return []IDForm{"uuid", "printable"} }
func (v IDForm) Valid() bool { return slices.Contains(v.All(), v) }

type Class string

func (Class) All() []Class  { return []Class{"bound", "foreign"} }
func (v Class) Valid() bool { return slices.Contains(v.All(), v) }

type DeliveryReason string

func (DeliveryReason) All() []DeliveryReason {
	return []DeliveryReason{"baseline", "assertion", "readback-contradicts", "deny-evidence-missing", "invalid-input"}
}
func (v DeliveryReason) Valid() bool { return slices.Contains(v.All(), v) }

type DenyReason string

func (DenyReason) All() []DenyReason {
	return []DenyReason{"pack-no-hook-port", "pack-hooks-fail-open", "pack-not-in-slice", "pack-hooks-not-loaded", "pack-unqualified", "pack-policy-outranked", "pack-hooks-unverified", "pack-mode-drift", "", "invalid-input"}
}
func (v DenyReason) Valid() bool { return slices.Contains(v.All(), v) }

type ClaimSource string

func (ClaimSource) All() []ClaimSource { return []ClaimSource{"", "client", "hook"} }
func (v ClaimSource) Valid() bool      { return slices.Contains(v.All(), v) }

type Slot string

func (Slot) All() []Slot   { return []Slot{"transport", "self", "hook"} }
func (v Slot) Valid() bool { return slices.Contains(v.All(), v) }

type DroppedReason string

func (DroppedReason) All() []DroppedReason {
	return []DroppedReason{"basis-not-allowed", "slot-basis", "slot-source", "hook-asking"}
}
func (v DroppedReason) Valid() bool { return slices.Contains(v.All(), v) }

type InputReason string

func (InputReason) All() []InputReason {
	return []InputReason{"unknown-value", "empty-value", "inconsistent", "too-long", "not-printable", "duplicate", "negative", "declaration-omits-required-row"}
}
func (v InputReason) Valid() bool { return slices.Contains(v.All(), v) }
