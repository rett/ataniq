package desk

import (
	"inventory.test/internal/leadcaps"
	"inventory.test/internal/seatgate"
)

var registered seatgate.HandlerFunc = serve

var caps = map[string]string{leadcaps.OpFacts: "view"}

func serve(op string) int {
	if _, ok := caps[op]; !ok {
		return 0
	}
	switch op {
	case leadcaps.OpFacts:
		return 1
	}
	return 0
}
