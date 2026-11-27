package protocol

import (
	"errors"
	"maps"
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
	released := answer
	if history[answer] == "success" {
		completed := slices.Index(history, "completed")
		if completed < 0 {
			return false
		}
		released = max(answer, completed)
	}
	idle, turn := true, ""
	for _, observation := range history[released+1:] {
		switch observation {
		case "started":
			idle, turn = false, "a"
		case "started other":
			idle, turn = false, "b"
		case "completed":
			if turn == "a" {
				idle, turn = true, ""
			}
		case "completed other":
			if turn == "b" {
				idle, turn = true, ""
			}
		case "completed on another thread":
			// A foreign notification has no effect on this thread.
		case "idle":
			idle, turn = true, ""
		case "notLoaded":
			idle, turn = false, ""
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
		{"started other", `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"inProgress"}}}`},
		{"completed", `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"completed"}}}`},
		{"completed other", `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"completed"}}}`},
		{"completed on another thread", `{"method":"turn/completed","params":{"threadId":"other","turn":{"id":"a","items":[],"status":"completed"}}}`},
		{"idle", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"idle"}}}`},
		{"active", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"active","activeFlags":[]}}}`},
		{"notLoaded", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"notLoaded"}}}`},
		{"closed", `{"method":"thread/closed","params":{"threadId":"t"}}`},
		{"systemError", `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"systemError"}}}`},
		{"forget", ""},
	}
	msg := core.Request{Operation: core.Message, Text: "hello"}
	c := codec{thread: "t", idle: true}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	var path [5]string
	var visit func(codec, int)
	visit = func(atPrefix codec, depth int) {
		// The probe's Encode creates its own reservation on success.
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
			// Each sibling must see only its own early completions.
			next.reservation.completed = maps.Clone(atPrefix.reservation.completed)
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
	if c.reservation.requestID != "" || c.reservation.turnID != "" || c.reservation.completed != nil {
		t.Fatal("systemError retained the start reservation")
	}
}
