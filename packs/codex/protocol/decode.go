package protocol

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"

	"ataniq/internal/agentsession"
	core "ataniq/internal/agentsession/protocol"
)

func (c *codec) Decode(payload []byte) (core.Decoded, error) {
	e, err := parse(payload)
	if err != nil {
		return core.Decoded{}, err
	}
	// A pending decision belongs to the turn that raised the question.
	before := c.controlTurn()
	defer func() {
		if c.controlTurn() != before {
			clear(c.pending)
		}
	}()
	if e.Method == nil {
		return c.answer(e)
	}
	method := e.method
	if e.ID != nil {
		if _, exists := c.pending[serverID(e.ID)]; exists {
			return core.Decoded{}, core.ErrInvalidFrame
		}
	}
	if method == permissionMethod {
		if e.ID == nil {
			return core.Decoded{}, ErrGrant
		}
		if _, err := c.permissionRequest(e); err != nil || len(c.pending) >= core.MaxPending {
			return core.Decoded{}, decline(e)
		}
		c.pending[serverID(e.ID)] = sha256.Sum256(payload)
		return event(core.Event{Kind: "question", Text: "Permission requested"}), nil
	}
	switch method {
	case "thread/started", "thread/closed", "thread/status/changed", "turn/started", "turn/completed", "item/agentMessage/delta":
		if e.ID != nil {
			return core.Decoded{}, core.ErrInvalidFrame
		}
	default:
		if e.ID != nil {
			return core.Decoded{}, decline(e)
		}
		return core.Decoded{}, nil
	}
	var p struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ThreadID string       `json:"threadId"`
		Status   threadStatus `json:"status"`
		Turn     struct {
			ID     string           `json:"id"`
			Status string           `json:"status"`
			Items  []jsontext.Value `json:"items"`
		} `json:"turn"`
		TurnID string  `json:"turnId"`
		ItemID string  `json:"itemId"`
		Delta  *string `json:"delta"`
	}
	if json.Unmarshal(e.Params, &p) != nil {
		return core.Decoded{}, core.ErrInvalidFrame
	}
	if method == "thread/started" {
		if p.Thread.ID == "" {
			return core.Decoded{}, core.ErrInvalidFrame
		}
		// This notification may precede our answer or describe a sub-agent.
		// Only the result of our thread/start establishes identity and status.
		return core.Decoded{}, nil
	}
	if p.ThreadID == "" {
		return core.Decoded{}, core.ErrInvalidFrame
	}
	if c.closed || c.thread != p.ThreadID {
		return core.Decoded{}, nil
	}
	switch method {
	case "thread/closed":
		c.closeThread()
		return event(core.Event{Kind: "ended"}), nil
	case "thread/status/changed":
		return c.status(p.Status)
	case "item/agentMessage/delta":
		if p.TurnID == "" || p.ItemID == "" || p.Delta == nil {
			return core.Decoded{}, core.ErrInvalidFrame
		}
		if slices.Contains(c.liveTurns, p.TurnID) {
			return event(core.Event{Kind: "progress", Text: *p.Delta}), nil
		}
	case "turn/started", "turn/completed":
		if p.Turn.ID == "" || p.Turn.Items == nil {
			return core.Decoded{}, core.ErrInvalidFrame
		}
		if method == "turn/started" {
			if p.Turn.Status != "inProgress" {
				return core.Decoded{}, core.ErrInvalidFrame
			}
			c.removeLiveTurn(p.Turn.ID)
			c.liveTurns = append(c.liveTurns, p.Turn.ID)
			return state("busy"), nil
		}
		switch p.Turn.Status {
		case "completed", "interrupted", "failed":
		default:
			return core.Decoded{}, core.ErrInvalidFrame
		}
		if c.reservation.completed != nil {
			c.reservation.completed[p.Turn.ID] = true
		}
		if p.Turn.ID == c.reservation.turnID {
			c.reservation = turnReservation{}
		}
		if c.removeLiveTurn(p.Turn.ID) && len(c.liveTurns) == 0 {
			return state(c.threadState), nil
		}
	}
	return core.Decoded{}, nil
}

func (c *codec) removeLiveTurn(turn string) bool {
	i := slices.Index(c.liveTurns, turn)
	if i < 0 {
		return false
	}
	c.liveTurns = slices.Delete(c.liveTurns, i, i+1)
	return true
}

func (c *codec) closeThread() {
	c.reservation = turnReservation{}
	c.liveTurns = nil
	c.closed = true
	c.threadState = "unknown"
}

type threadStatus struct {
	Type  string   `json:"type"`
	Flags []string `json:"activeFlags"`
}

func (c *codec) status(s threadStatus) (core.Decoded, error) {
	var observed agentsession.State
	switch s.Type {
	case "idle":
		observed = "idle"
	case "systemError":
		c.closeThread()
		return state("unknown"), nil
	case "notLoaded":
		observed = "unknown"
	case "active":
		if s.Flags == nil {
			return core.Decoded{}, core.ErrInvalidFrame
		}
		observed = "busy"
		if slices.Contains(s.Flags, "waitingOnApproval") {
			observed = "blocked"
		}
		if slices.Contains(s.Flags, "waitingOnUserInput") {
			observed = "asking"
		}
	default:
		return core.Decoded{}, core.ErrInvalidFrame
	}
	c.threadState = observed
	if s.Type == "idle" && len(c.liveTurns) != 0 {
		return core.Decoded{}, nil
	}
	return state(observed), nil
}

func event(e core.Event) core.Decoded         { return core.Decoded{Events: []core.Event{e}} }
func state(s agentsession.State) core.Decoded { return event(core.Event{Kind: "state", State: s}) }

func (c *codec) answer(e envelope) (core.Decoded, error) {
	var id string
	if json.Unmarshal(e.ID, &id) != nil || id == "" {
		return core.Decoded{}, core.ErrInvalidFrame
	}
	d := core.Decoded{AnswerTo: &id}
	if e.Error != nil {
		var detail struct {
			Code    *int64  `json:"code"`
			Message *string `json:"message"`
		}
		if json.Unmarshal(e.Error, &detail) != nil || detail.Code == nil || detail.Message == nil {
			return core.Decoded{}, core.ErrInvalidFrame
		}
		d.Events = []core.Event{{Kind: "progress", Text: "Request failed: " + *detail.Message}}
	} else if e.Result.Kind() != '{' {
		return core.Decoded{}, core.ErrInvalidFrame
	}
	if id == c.reservation.requestID && c.reservation.turnID == "" {
		if e.Error == nil {
			var result struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(e.Result, &result) != nil || result.Turn.ID == "" {
				return core.Decoded{}, core.ErrInvalidFrame
			}
			if !c.reservation.completed[result.Turn.ID] {
				c.reservation.turnID = result.Turn.ID
				c.reservation.completed = nil
				// An answer may arrive after a newer turn/started. Keep that
				// interrupt target ahead of a turn known only from its answer.
				if !slices.Contains(c.liveTurns, result.Turn.ID) {
					c.liveTurns = slices.Insert(c.liveTurns, 0, result.Turn.ID)
				}
				return d, nil
			}
		}
		c.reservation = turnReservation{}
	}
	if id == c.startID {
		if e.Error == nil {
			var result struct {
				Thread struct {
					ID     string       `json:"id"`
					Status threadStatus `json:"status"`
				} `json:"thread"`
			}
			if json.Unmarshal(e.Result, &result) != nil || result.Thread.ID == "" {
				return core.Decoded{}, core.ErrInvalidFrame
			}
			if _, err := c.status(result.Thread.Status); err != nil {
				return core.Decoded{}, err
			}
			c.thread = result.Thread.ID
		}
		c.startID = ""
	}
	return d, nil
}
