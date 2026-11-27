package desk

import "inventory.test/internal/leadcaps"

type gate struct{}

var caps = map[string]string{leadcaps.OpRead: "view"}

func (gate) Handle(op string) int {
	if _, ok := caps[op]; !ok {
		return 0
	}
	switch op {
	case leadcaps.OpRead:
		return 1
	}
	return 0
}
