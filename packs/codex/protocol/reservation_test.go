package protocol

import (
	"errors"
	"slices"
	"testing"

	core "ataniq/internal/agentsession/protocol"
)

// Read the history independently of the codec. Find the first answer and
// the point where the reservation ended, then consider later observations.
func messageAllowed(history []string) bool {
	if slices.Contains(history, "closed") || slices.Contains(history, "systemError") {
		return false
	}
	answer := slices.IndexFunc(history, func(s string) bool { return s == "success" || s == "error" })
	if answer < 0 {
		return false
	}
	later := history[answer+1:]
	if history[answer] == "success" {
		completed := slices.Index(later, "completed")
		if completed < 0 {
			return false
		}
		later = later[completed+1:]
	}
	idle, started := true, false
	for _, observation := range later {
		switch observation {
		case "started":
			idle, started = false, true
		case "completed":
			if started {
				idle, started = true, false
			}
		case "idle":
			idle, started = true, false
		case "active":
			idle = false
		}
	}
	return idle
}

func TestTurnReservationSequences(t *testing.T) {
	events := []struct{ name, frame string }{
		{"success", `{"id":"first","result":{"turn":{"id":"a"}}}`},
		{"error", `{"id":"first","error":{"code":-1,"message":"failed"}}`},
		{"started", `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"inProgress"}}}`},
		{"completed", `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"completed"}}}`},
		{"completed other", `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"completed"}}}`},
		{"idle", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"idle"}}}`},
		{"active", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"active","activeFlags":[]}}}`},
		{"closed", `{"method":"thread/closed","params":{"threadId":"t"}}`},
		{"systemError", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"systemError"}}}`},
		{"forget", ""},
	}
	msg := core.Request{Operation: core.Message, Text: "hello"}
	c := codec{thread: "t", idle: true}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	var path [6]string
	var visit func(codec, int)
	visit = func(atPrefix codec, depth int) {
		// No path carries permission requests. A value copy isolates both
		// the probe's Encode and each sibling's event from this prefix.
		probe := atPrefix
		frame, err := probe.Encode("second", msg)
		allowed := messageAllowed(path[:depth])
		if allowed && (err != nil || frame == nil) || !allowed && (!errors.Is(err, ErrTurn) || frame != nil) {
			t.Fatalf("%v: second message allowed=%t, frame=%s, err=%v", path[:depth], allowed, frame, err)
		}
		if depth == len(path) {
			return
		}
		for _, event := range events {
			next := atPrefix
			path[depth] = event.name
			if event.name == "forget" {
				next.Forget("first")
			} else if _, err := next.Decode([]byte(event.frame)); err != nil {
				t.Fatalf("%v: %v", path[:depth+1], err)
			}
			visit(next, depth+1)
		}
	}
	visit(c, 0)
}

func TestSystemErrorDiscardsPendingStart(t *testing.T) {
	c := codec{thread: "t", idle: true}
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode([]byte(`{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"systemError"}}}`)); err != nil {
		t.Fatal(err)
	}
	if c.reservation != (turnReservation{}) {
		t.Fatal("systemError retained the start reservation")
	}
}
