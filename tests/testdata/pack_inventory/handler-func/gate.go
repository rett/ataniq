package desk

import (
	"inventory.test/internal/leadcaps"
	"inventory.test/internal/seatgate"
)

var handler seatgate.Handler = seatgate.HandlerFunc(serve)

var caps = map[string]string{leadcaps.OpRead: "view"}

func serve(op string) int {
	if _, ok := caps[op]; !ok {
		return 0
	}
	switch op {
	case leadcaps.OpRead:
		return 1
	}
	return 0
}
