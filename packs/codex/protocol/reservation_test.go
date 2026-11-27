package protocol

import (
	"errors"
	"maps"
	"testing"

	core "ataniq/internal/agentsession/protocol"
)

// An observation describes the meaning of its wire fixture in the history.
type observation struct {
	kind, requestID, turn string
}

func messageAllowed(history []observation) bool {
	current := ""
	idle, usable := true, true
	// Encode("first") reserved the thread. The answer identifies the turn
	// whose completion settles the request, in either arrival order.
	awaitingAnswer, awaitingCompletion := "first", ""
	completed := map[string]bool{}
	for _, obs := range history {
		if !usable {
			continue
		}
		switch obs.kind {
		case "started":
			current, idle = obs.turn, false
		case "completed":
			completed[obs.turn] = true
			if obs.turn == current || obs.turn == awaitingCompletion {
				current, idle = "", true
			}
			if obs.turn == awaitingCompletion {
				awaitingCompletion = ""
			}
		case "success", "error":
			if obs.requestID != awaitingAnswer {
				continue
			}
			awaitingAnswer = ""
			if obs.kind == "success" && !completed[obs.turn] {
				awaitingCompletion = obs.turn
				current, idle = obs.turn, false
			}
		case "idle", "notLoaded":
			if awaitingCompletion == "" || current != awaitingCompletion {
				current, idle = "", obs.kind == "idle"
			}
		case "active":
			idle = false
		case "closed", "systemError":
			usable = false
			awaitingAnswer, awaitingCompletion = "", ""
			current, idle = "", false
		case "completed on another thread", "forget":
			// Neither is an observation of this thread's turn or its outcome.
		}
	}
	return awaitingAnswer == "" && awaitingCompletion == "" && idle && usable
}

func TestTurnReservationSequences(t *testing.T) {
	events := []struct {
		observation
		frame string
	}{
		{observation{kind: "success", requestID: "first", turn: "a"}, `{"id":"first","result":{"turn":{"id":"a"}}}`},
		{observation{kind: "success", requestID: "unrelated", turn: "b"}, `{"id":"unrelated","result":{"turn":{"id":"b"}}}`},
		{observation{kind: "error", requestID: "first"}, `{"id":"first","error":{"code":-1,"message":"failed"}}`},
		{observation{kind: "started", turn: "a"}, `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"inProgress"}}}`},
		{observation{kind: "started", turn: "b"}, `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"inProgress"}}}`},
		{observation{kind: "completed", turn: "a"}, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"completed"}}}`},
		{observation{kind: "completed", turn: "b"}, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"completed"}}}`},
		{observation{kind: "completed on another thread"}, `{"method":"turn/completed","params":{"threadId":"other","turn":{"id":"a","items":[],"status":"completed"}}}`},
		{observation{kind: "idle"}, `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"idle"}}}`},
		{observation{kind: "active"}, `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"active","activeFlags":[]}}}`},
		{observation{kind: "notLoaded"}, `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"notLoaded"}}}`},
		{observation{kind: "closed"}, `{"method":"thread/closed","params":{"threadId":"t"}}`},
		{observation{kind: "systemError"}, `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"systemError"}}}`},
		{observation{kind: "forget"}, ""},
	}
	msg := core.Request{Operation: core.Message, Text: "hello"}
	c := codec{thread: "t", idle: true}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	var path [5]observation
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
			path[depth] = event.observation
			if event.kind == "forget" {
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
