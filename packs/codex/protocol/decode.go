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
		if _, err := c.permissionRequest(e); err != nil {
			if err == ErrTurn {
				return core.Decoded{}, decline(e)
			}
			return core.Decoded{}, err
		}
		if len(c.pending) >= core.MaxPending {
			return core.Decoded{}, core.ErrPendingLimit
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
		c.setTurn("")
		c.closed, c.idle = true, false
		return event(core.Event{Kind: "ended"}), nil
	case "thread/status/changed":
		return c.status(p.Status)
	case "item/agentMessage/delta":
		if p.TurnID == "" || p.ItemID == "" || p.Delta == nil {
			return core.Decoded{}, core.ErrInvalidFrame
		}
		if c.current(p.ThreadID, p.TurnID) {
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
			c.setTurn(p.Turn.ID)
			c.idle = false
			return state("busy"), nil
		}
		switch p.Turn.Status {
		case "completed", "interrupted", "failed":
		default:
			return core.Decoded{}, core.ErrInvalidFrame
		}
		if c.current(p.ThreadID, p.Turn.ID) {
			c.setTurn("")
			c.idle = true
			return state("idle"), nil
		}
	}
	return core.Decoded{}, nil
}

func (c *codec) setTurn(turn string) {
	if turn != c.turn {
		clear(c.pending)
		c.turn = turn
	}
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
	case "systemError", "notLoaded":
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
	if s.Type != "active" {
		c.setTurn("")
	}
	c.idle = s.Type == "idle"
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
