package protocol

import (
	"encoding/json/v2"
	"errors"
	"slices"
	"testing"

	core "ataniq/internal/agentsession/protocol"
)

// An observation describes the meaning of its wire fixture in the history.
type observation struct {
	kind, requestID, turn string
}

type reservationVerdict struct {
	admit bool
	stop  string
	live  map[string]int // turn ID -> position of its latest started notification
}

// The v2 schema in ../testdata/appserver identifies a particular Turn in
// TurnStartResponse, TurnStartedNotification and TurnCompletedNotification.
// ThreadStatusChangedNotification identifies only a thread. Keep its latest
// status independently of the evidence about individual turns.
func reservationOracle(history []observation) reservationVerdict {
	live := make(map[string]int)
	completed := make(map[string]bool)
	status := "idle" // the initial thread/start result
	var answer observation
	for i, obs := range history {
		switch obs.kind {
		case "success", "error":
			if obs.requestID == "first" && answer.kind == "" {
				answer = obs
				if obs.kind == "success" && !completed[obs.turn] {
					if _, seen := live[obs.turn]; !seen {
						live[obs.turn] = 0 // known from the response only
					}
				}
			}
		case "started":
			live[obs.turn] = i + 1
		case "completed":
			delete(live, obs.turn)
			completed[obs.turn] = true
		case "idle", "active", "notLoaded":
			status = obs.kind
		case "closed", "systemError":
			return reservationVerdict{}
		}
	}

	// TurnInterruptParams requires one turnId. Prefer the most recent live
	// started notification. A possibly late response only supplies a fallback.
	var stop string
	for turn, position := range live {
		if stop == "" || position > live[stop] {
			stop = turn
		}
	}
	settled := answer.kind == "error" || (answer.kind == "success" && completed[answer.turn])
	return reservationVerdict{admit: settled && len(live) == 0 && status == "idle", stop: stop, live: live}
}

// AgentMessageDeltaNotification carries its own threadId and turnId.
func (v reservationVerdict) routes(thread, turn string) bool {
	_, live := v.live[turn]
	return thread == "t" && live
}

var (
	successA   = observation{kind: "success", requestID: "first", turn: "a"}
	errorFirst = observation{kind: "error", requestID: "first"}
	startedB   = observation{kind: "started", turn: "b"}
	completedA = observation{kind: "completed", turn: "a"}
	completedB = observation{kind: "completed", turn: "b"}
	idle       = observation{kind: "idle"}
)

var overlapHistories = []struct {
	name                  string
	history               []observation
	stop                  string
	deltaA, deltaB, admit bool
}{
	{"complete accepted turn", []observation{successA, startedB, completedA}, "b", false, true, false},
	{"late success", []observation{startedB, successA, completedA}, "b", false, true, false},
	{"complete notified turn", []observation{successA, startedB, completedB}, "a", true, false, false},
	{"both turns live", []observation{startedB, idle, successA}, "b", true, true, false},
	{"late error", []observation{successA, startedB, idle, errorFirst, completedA}, "b", false, true, false},
	{"rejected start", []observation{startedB, idle, errorFirst}, "b", false, true, false},
	{"both completed", []observation{successA, startedB, completedB, idle, completedA}, "", false, false, true},
	{"unloaded thread", []observation{successA, {kind: "notLoaded"}, completedA}, "", false, false, false},
}

func TestTurnReservationOracle(t *testing.T) {
	for _, tc := range overlapHistories {
		v := reservationOracle(tc.history)
		if v.admit != tc.admit || v.stop != tc.stop || v.routes("t", "a") != tc.deltaA || v.routes("t", "b") != tc.deltaB {
			t.Errorf("%s: got %+v, want admit=%t stop=%q deltaA=%t deltaB=%t", tc.name, v, tc.admit, tc.stop, tc.deltaA, tc.deltaB)
		}
	}
}

var reservationFrames = map[observation]string{
	successA: `{"id":"first","result":{"turn":{"id":"a","items":[],"status":"inProgress"}}}`,
	{kind: "success", requestID: "unrelated", turn: "b"}: `{"id":"unrelated","result":{"turn":{"id":"b","items":[],"status":"inProgress"}}}`,
	errorFirst:                            `{"id":"first","error":{"code":-1,"message":"failed"}}`,
	{kind: "started", turn: "a"}:          `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"inProgress"}}}`,
	startedB:                              `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"inProgress"}}}`,
	completedA:                            `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"completed"}}}`,
	completedB:                            `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"b","items":[],"status":"completed"}}}`,
	{kind: "completed on another thread"}: `{"method":"turn/completed","params":{"threadId":"other","turn":{"id":"a","items":[],"status":"completed"}}}`,
	idle:                                  `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"idle"}}}`,
	{kind: "active"}:                      `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"active","activeFlags":[]}}}`,
	{kind: "notLoaded"}:                   `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"notLoaded"}}}`,
	{kind: "closed"}:                      `{"method":"thread/closed","params":{"threadId":"t"}}`,
	{kind: "systemError"}:                 `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"systemError"}}}`,
	{kind: "forget"}:                      "",
}

var deltaProbes = []struct {
	thread, turn, text string
	frame              []byte
}{
	{"t", "a", "from a", []byte(`{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"a","itemId":"x","delta":"from a"}}`)},
	{"t", "b", "from b", []byte(`{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"b","itemId":"y","delta":"from b"}}`)},
	{"other", "a", "foreign", []byte(`{"method":"item/agentMessage/delta","params":{"threadId":"other","turnId":"a","itemId":"x","delta":"foreign"}}`)},
}

func checkReservationHistory(t *testing.T, history []observation) {
	t.Helper()
	c, err := New(Version, SchemaDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartThread("bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode([]byte(`{"id":"bind","result":{"thread":{"id":"t","status":{"type":"idle"}}}}`)); err != nil {
		t.Fatal(err)
	}
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	for _, obs := range history {
		if obs.kind == "forget" {
			c.Forget("first")
		} else if _, err := c.Decode([]byte(reservationFrames[obs])); err != nil {
			t.Fatalf("%v: decode %v: %v", history, obs, err)
		}
	}

	want := reservationOracle(history)
	for _, delta := range deltaProbes {
		got, err := c.Decode(delta.frame)
		var events []core.Event
		if want.routes(delta.thread, delta.turn) {
			events = []core.Event{{Kind: "progress", Text: delta.text}}
		}
		if err != nil || got.AnswerTo != nil || !slices.Equal(got.Events, events) {
			t.Fatalf("%v: delta for %s/%s: got %+v, %v; want %v", history, delta.thread, delta.turn, got, err, events)
		}
	}

	frame, err := c.Encode("stop", core.Request{Operation: core.StopTurn})
	if want.stop == "" {
		if frame != nil || !errors.Is(err, ErrTurn) {
			t.Fatalf("%v: StopTurn without a live turn: %s, %v", history, frame, err)
		}
	} else {
		var interrupt struct {
			Method string `json:"method"`
			Params struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			} `json:"params"`
		}
		if err != nil || json.Unmarshal(frame, &interrupt) != nil || interrupt.Method != "turn/interrupt" ||
			interrupt.Params.ThreadID != "t" || interrupt.Params.TurnID != want.stop {
			t.Fatalf("%v: StopTurn: got %s, %v; want turn %q", history, frame, err, want.stop)
		}
	}

	frame, err = c.Encode("second", msg)
	if want.admit && (err != nil || frame == nil) || !want.admit && (!errors.Is(err, ErrTurn) || frame != nil) {
		t.Fatalf("%v: second message allowed=%t, frame=%s, err=%v", history, want.admit, frame, err)
	}
}

func TestOverlappingTurnReservations(t *testing.T) {
	for _, tc := range overlapHistories {
		t.Run(tc.name, func(t *testing.T) { checkReservationHistory(t, tc.history) })
	}
}

func TestTurnReservationSequences(t *testing.T) {
	var path [5]observation
	var visit func(int)
	visit = func(depth int) {
		checkReservationHistory(t, path[:depth])
		if depth < len(path) {
			for obs := range reservationFrames {
				path[depth] = obs
				visit(depth + 1)
			}
		}
	}
	visit(0)
}

func TestSystemErrorDiscardsPendingStart(t *testing.T) {
	c := codec{thread: "t", threadState: "idle"}
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
