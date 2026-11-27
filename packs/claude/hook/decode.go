// Package hook translates Claude Code hook observations into session reports.
package hook

import (
	"errors"
	"regexp"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/pack"
)

// Event describes what the sandbox reported. Mode and Model are reported
// strings. The holder classifies Report against the epoch's bound session
// before using Claim in the hook slot of agentsession.Inputs.
type Event struct {
	Kind           string
	Report         agentsession.HookReport
	Claim          *agentsession.Claim
	Mode           string
	Model          string
	Source         string // reported start or configuration-change source
	Tool           string
	InputDigest    string
	Reason         string // reported notification type or session-end reason
	StopHookActive bool   // meaningful on turn-complete
}

var sessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Decode consumes a request admitted by the selected build's
// hookwire.Table.DecodeRequest and the corresponding declared row. Wire
// admission checks field names, printable text, digest syntax and bounds.
// Cut input has no extracted fields and yields no session event.
func Decode(row pack.HookRow, req hookwire.Request) (Event, error) {
	f := req.Fields
	if req.Version != hookwire.Version || req.Cut || req.Kind != hookwire.Event || req.Row != row.ID || f["hook_event_name"] != row.Event {
		return Event{}, errors.New("incomplete or mismatched hook event")
	}
	if !sessionID.MatchString(f["session_id"]) {
		return Event{}, errors.New("invalid hook session id")
	}
	e := Event{
		Report: agentsession.HookReport{Kind: "other", Session: f["session_id"]},
		Mode:   f["permission_mode"],
	}
	var state agentsession.State
	switch row.Event {
	case "SessionStart":
		e.Kind = "session-start"
		e.Report.Kind = "session-start"
		e.Source = f["source"]
		e.Model = f["model"]
	case "UserPromptSubmit":
		e.Kind, state = "turn-start", "busy"
	case "PreToolUse", "PermissionRequest":
		e.Tool, e.InputDigest = f["tool_name"], req.Digests["tool_input"]
		if e.Tool == "" || e.InputDigest == "" {
			return Event{}, errors.New("incomplete tool observation")
		}
		if row.Event == "PermissionRequest" {
			e.Kind, state = "permission", "blocked"
		} else {
			e.Kind, state = "tool-start", "busy"
		}
	case "Notification":
		e.Kind, e.Reason = "notification", f["notification_type"]
		switch e.Reason {
		case "permission_prompt":
			state = "blocked"
		case "idle_prompt":
			state = "idle"
		}
	case "Stop":
		e.Kind, state = "turn-complete", "idle"
		switch f["stop_hook_active"] {
		case "true":
			e.StopHookActive = true
		case "false":
		default:
			return Event{}, errors.New("invalid stop flag")
		}
	case "StopFailure":
		e.Kind = "turn-failed"
	case "SessionEnd":
		e.Kind, e.Reason = "session-end", f["reason"]
	case "ConfigChange":
		e.Kind, e.Source = "config-change", f["source"]
		e.Report.Kind = "config-change"
	default:
		return Event{}, errors.New("unrecognized hook event")
	}
	if state != "" {
		e.Claim = &agentsession.Claim{State: state, Basis: "self", Source: "hook"}
	}
	return e, nil
}
