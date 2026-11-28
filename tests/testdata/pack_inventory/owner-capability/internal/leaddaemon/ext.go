package leaddaemon

import "inventory.test/internal/leadcaps"

type ownerExt struct{}

func (ownerExt) Operations() []string { return []string{leadcaps.OpFacts} }
func (ownerExt) Handle(op string) int {
	switch op {
	case leadcaps.OpFacts:
		return 1
	}
	return 0
}
