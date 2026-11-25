package agentsession

import "time"

// SessionRef carries the pack and project context for this slice.
type SessionRef struct {
	Project    string `json:"project"`
	PackDigest string `json:"pack_digest"`
}
type Claim struct {
	State  State
	Basis  Basis
	Source ClaimSource
	At     time.Time
	Detail []string
}
type Veto []string
type SelfDelivery string
type Policy struct{ Self SelfDelivery }
type Inputs struct {
	Ended      bool
	Transport  Claim
	Self, Hook *Claim
	Veto       Veto
	Sight      Sight
	Policy     Policy
}
type DroppedClaim struct {
	Slot   Slot
	Reason DroppedReason
}
type Record struct {
	State       State
	Basis       Basis
	Deliverable bool
	Permit      Permit
	Refusal     []string
	Claims      []Claim
	Veto        Veto
	Sight       Sight
	Disagree    bool
	Dropped     []DroppedClaim
}

type Independent struct {
	Launch   Launch
	Gates    Gates
	Build    Build
	I1       bool
	I2       I2
	I3       bool
	Readback Readback
	I5       I5
}
type Launch struct{ BundleB, DeclaresHookRows bool }
type Gates struct{ ReadbackActive, ReadbackOffered, S3Active bool }
type Build struct{ DeclaresDenyRow, DenyMeant bool }
type I2 struct{ A8, A12, A13, A14 Assertion }
type Readback struct {
	Exp Expectation
	Obs ReadbackObs
}
type I5 struct{ Attempted, EffectAbsent, Independent bool }
type Expectation struct {
	Facts   []FactExp
	Sources SourcesExp
}
type FactExp struct {
	Fact     Fact
	Required bool
	Want     Want
}
type SourcesExp struct{ Digest [32]byte }
type ReadbackObs struct {
	Attempt Outcome
	Facts   []FactObs
	Sources SourcesObs
}
type FactObs struct {
	Fact Fact
	Seen bool
	Got  Want
}
type SourcesObs struct {
	Seen   bool
	Digest [32]byte
}
type DeliveryResult struct {
	Ready   bool
	Reasons []DeliveryReason
}
type DenyResult struct {
	InForce bool
	Reason  DenyReason
}
type Obs struct {
	Bound   string
	Foreign int
}
type Result struct {
	Delivery DeliveryResult
	Deny     DenyResult
	Obs      Obs
}
