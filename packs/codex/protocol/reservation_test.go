package protocol

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	core "ataniq/internal/agentsession/protocol"
)

// An observation describes the meaning of its wire fixture in the history.
type observation struct {
	kind, requestID, turn string
}

type reservationVerdict struct {
	control string
	admit   bool
	stop    string
	live    map[string]int // turn ID -> position of its latest started notification
}

// The v2 schema in ../testdata/appserver identifies a particular Turn in
// TurnStartResponse, TurnStartedNotification and TurnCompletedNotification.
// ThreadStatusChangedNotification identifies only a thread. Keep its latest
// status independently of the evidence about individual turns.
type oracleState struct {
	answer    observation
	completed map[string]bool
	status    string
	live      map[string]int
}

func reservationOracle(history []observation) oracleState {
	s := oracleState{status: "idle", completed: make(map[string]bool), live: make(map[string]int)}
	for i, obs := range history {
		switch obs.kind {
		case "success", "error":
			if obs.requestID == "first" && s.answer.kind == "" {
				s.answer = obs
				if obs.kind == "success" && !s.completed[obs.turn] {
					if _, seen := s.live[obs.turn]; !seen {
						s.live[obs.turn] = 0 // known from the response only
					}
				}
			}
		case "started":
			if !s.completed[obs.turn] {
				s.live[obs.turn] = i + 1
			}
		case "completed", "interrupted":
			delete(s.live, obs.turn)
			s.completed[obs.turn] = true
		case "idle", "active", "notLoaded", "waitingOnApproval":
			s.status = obs.kind
		case "closed", "systemError":
			return oracleState{status: "closed"}
		}
	}
	// Only the relative positions of live starts matter to future verdicts.
	// Keep position zero for a turn known only from its answer.
	order := slices.SortedFunc(maps.Keys(s.live), func(a, b string) int { return s.live[a] - s.live[b] })
	for i, turn := range order {
		if s.live[turn] > 0 {
			s.live[turn] = i + 1
		}
	}
	return s
}

func (s oracleState) verdict() reservationVerdict {
	var stop, control string
	for turn, position := range s.live {
		if stop == "" || position > s.live[stop] {
			stop = turn
		}
	}
	settled := s.answer.kind == "error" || (s.answer.kind == "success" && s.completed[s.answer.turn])
	switch {
	case s.answer.kind == "":
		control = stop
	case s.answer.kind == "success" && !settled:
		control = s.answer.turn
	}
	return reservationVerdict{admit: settled && len(s.live) == 0 && s.status == "idle", stop: stop, live: s.live, control: control}
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
	stop, control         string
	deltaA, deltaB, admit bool
}{
	{"complete accepted turn", []observation{successA, startedB, completedA}, "b", "", false, true, false},
	{"late success", []observation{startedB, successA, completedA}, "b", "", false, true, false},
	{"complete notified turn", []observation{successA, startedB, completedB}, "a", "a", true, false, false},
	{"both turns live", []observation{startedB, idle, successA}, "b", "a", true, true, false},
	{"late error", []observation{successA, startedB, idle, errorFirst, completedA}, "b", "", false, true, false},
	{"rejected start", []observation{startedB, idle, errorFirst}, "b", "", false, true, false},
	{"both completed", []observation{successA, startedB, completedB, idle, completedA}, "", "", false, false, true},
	{"unloaded thread", []observation{successA, {kind: "notLoaded"}, completedA}, "", "", false, false, false},
	{"late start after completion", []observation{successA, completedA, {kind: "started", turn: "a"}}, "", "", false, false, true},
	{"late start before answer", []observation{completedB, startedB}, "", "", false, false, false},
	{"provisional control", []observation{startedB, idle}, "b", "b", false, true, false},
}

func TestTurnReservationOracle(t *testing.T) {
	for _, tc := range overlapHistories {
		v := reservationOracle(tc.history).verdict()
		if v.admit != tc.admit || v.stop != tc.stop || v.control != tc.control || v.routes("t", "a") != tc.deltaA || v.routes("t", "b") != tc.deltaB {
			t.Errorf("%s: got %+v, want admit=%t stop=%q control=%q deltaA=%t deltaB=%t", tc.name, v, tc.admit, tc.stop, tc.control, tc.deltaA, tc.deltaB)
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
	{kind: "success", requestID: "first", turn: "b"}: `{"id":"first","result":{"turn":{"id":"b","items":[],"status":"inProgress"}}}`,
	{kind: "error", requestID: "unrelated"}:          `{"id":"unrelated","error":{"code":-1,"message":"failed"}}`,
	{kind: "started on another thread"}:              `{"method":"turn/started","params":{"threadId":"other","turn":{"id":"a","items":[],"status":"inProgress"}}}`,
	{kind: "waitingOnApproval"}:                      `{"method":"thread/status/changed","params":{"threadId":"t","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`,
	{kind: "interrupted", turn: "a"}:                 `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"a","items":[],"status":"interrupted"}}}`,
	{kind: "thread started"}:                         `{"method":"thread/started","params":{"thread":{"id":"other"}}}`,
}

var deltaProbes = []struct {
	thread, turn, text string
	frame              []byte
}{
	{"t", "a", "from a", []byte(`{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"a","itemId":"x","delta":"from a"}}`)},
	{"t", "b", "from b", []byte(`{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"b","itemId":"y","delta":"from b"}}`)},
	{"other", "a", "foreign", []byte(`{"method":"item/agentMessage/delta","params":{"threadId":"other","turnId":"a","itemId":"x","delta":"foreign"}}`)},
}

func replayReservation(t *testing.T, history []observation) *codec {
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

	return c.(*codec)
}

func checkReservation(t *testing.T, c *codec, history []observation, want reservationVerdict) {
	t.Helper()
	for _, turn := range []string{"a", "b"} {
		frame, err := c.Steer("steer", "t", turn, "text")
		controls := turn == want.control
		if controls && (err != nil || frame == nil) || !controls && (!errors.Is(err, ErrTurn) || frame != nil) {
			t.Fatalf("%v: Steer(%s): got %s, %v; want control %q", history, turn, frame, err, want.control)
		}
		req := fmt.Appendf(nil, `{"id":%q,"method":"item/permissions/requestApproval","params":{"threadId":"t","turnId":%q,"itemId":"item","cwd":"/work","startedAtMs":0,"permissions":{}}}`, turn, turn)
		got, err := c.Decode(req)
		if controls {
			want := []core.Event{{Kind: "question", Text: "Permission requested"}}
			if err != nil || got.AnswerTo != nil || !slices.Equal(got.Events, want) {
				t.Fatalf("%v: permission(%s): got %+v, %v; want question", history, turn, got, err)
			}
		} else {
			var decline *Decline
			if !errors.As(err, &decline) || decline.Method != permissionMethod || got.AnswerTo != nil || len(got.Events) != 0 {
				t.Fatalf("%v: permission(%s): got %+v, %v; want decline", history, turn, got, err)
			}
		}
	}
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

	frame, err = c.Encode("second", core.Request{Operation: core.Message, Text: "hello"})
	if want.admit && (err != nil || frame == nil) || !want.admit && (!errors.Is(err, ErrTurn) || frame != nil) {
		t.Fatalf("%v: second message allowed=%t, frame=%s, err=%v", history, want.admit, frame, err)
	}
}

func TestOverlappingTurnReservations(t *testing.T) {
	for _, tc := range overlapHistories {
		t.Run(tc.name, func(t *testing.T) {
			checkReservation(t, replayReservation(t, tc.history), tc.history, reservationOracle(tc.history).verdict())
		})
	}
}

func TestTurnReservationSequences(t *testing.T) {
	// Replay from the bound thread so each history and its destructive probes
	// have private state. Expand a representative of each combined state once.
	queue := [][]observation{nil}
	seen := make(map[string]bool)
	maxDepth := 0
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		c, model := replayReservation(t, path), reservationOracle(path)
		// Go-syntax formatting includes every field and orders map keys.
		key := fmt.Sprintf("%#v | %#v", c, model)
		if seen[key] {
			continue
		}
		seen[key] = true
		maxDepth = len(path)
		checkReservation(t, c, path, model.verdict())
		for obs := range reservationFrames {
			queue = append(queue, append(slices.Clip(path), obs))
		}
	}
	t.Logf("visited %d combined states, greatest shortest history %d", len(seen), maxDepth)
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
	if c.turnStartID != "" || c.ownTurn != "" || c.completed != nil {
		t.Fatal("systemError retained the start reservation")
	}
}

func TestTurnStartAnswerReleasesReservation(t *testing.T) {
	for _, answer := range []observation{successA, errorFirst} {
		c := replayReservation(t, []observation{startedB, answer})
		if c.turnStartID != "" {
			t.Fatalf("%v retained reservation %q", answer, c.turnStartID)
		}
	}
}

func TestCompletedTurnLimitFailsClosed(t *testing.T) {
	c := replayReservation(t, []observation{startedB})
	complete := func(i int) core.Decoded {
		t.Helper()
		d, err := c.Decode(fmt.Appendf(nil, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"old-%d","items":[],"status":"completed"}}}`, i))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for i := range core.MaxPending {
		complete(i)
	}
	complete(0) // repeats need no extra space
	if _, err := c.Steer("steer", "t", "b", "text"); err != nil {
		t.Fatal("lost control at capacity", err)
	}
	if d := complete(core.MaxPending); !slices.Equal(d.Events, []core.Event{{Kind: "state", State: "unknown"}}) {
		t.Fatal("overflow", d)
	}
	for _, obs := range []observation{idle, successA, startedB, completedB} {
		if _, err := c.Decode([]byte(reservationFrames[obs])); err != nil {
			t.Fatal(err)
		}
	}
	checkReservation(t, c, nil, reservationVerdict{})
}
