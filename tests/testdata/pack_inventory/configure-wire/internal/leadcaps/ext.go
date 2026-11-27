package leadcaps

import (
	"encoding/json"
	"inventory.test/internal/agentsession"
)

type ext struct{}
type CapsAdjustParams struct {
	agentsession.CommandEnvelope
	Lower agentsession.Lowering
}
type actualParams struct {
	agentsession.CommandEnvelope
	Active bool
}

func (ext) Operations() []string { return []string{"caps.adjust"} }
func (ext) Handle(op string, raw json.RawMessage) bool {
	switch op {
	case "caps.adjust":
		var p actualParams
		return json.Unmarshal(raw, &p) == nil && p.Active
	}
	return false
}
