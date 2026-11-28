// Package hook decodes reduced Codex hook frames into unverified observations.
package hook

import (
	_ "embed"
	"errors"
	"regexp"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
)

//go:embed rows.json
var rows string

// RowsJSON returns an owned copy of the hook companion for the pack declaration.
func RowsJSON() []byte { return []byte(rows) }

var tables = func() map[string]hookwire.Table {
	tables, err := hookwire.DecodeTable(RowsJSON())
	if err != nil {
		panic(err) // invalid compiled-in pack data
	}
	return tables
}()

// The native id grammar is unverified. Use the session contract's printable form.
var sessionID = regexp.MustCompile(`^[ -~]{1,128}$`)

// Observation contains only claims reported from inside the sandbox. Report
// and Claim are suitable for the session binder and the hook slot respectively.
type Observation struct {
	Row                   string
	Report                agentsession.HookReport
	Claim                 agentsession.Claim
	Model, PermissionMode string
	Tool, InputDigest     string
	StopHookActive        *bool
}

// Decode accepts one complete hook-port frame for the exact build. A cut
// input has no session or event evidence. Refusals return no observation.
func Decode(build string, frame []byte) (*Observation, error) {
	table, ok := tables[build]
	if !ok {
		return nil, errors.New("no hook rows for build")
	}
	r, err := table.DecodeRequest(frame)
	if err != nil {
		return nil, err
	}
	row, _ := table.Lookup(r.Row) // DecodeRequest admitted this row.
	if r.Cut || r.Kind != hookwire.Event || r.Fields["event"] != row.Event {
		return nil, errors.New("incomplete or mismatched hook event")
	}
	session := r.Fields["session"]
	if !sessionID.MatchString(session) {
		return nil, errors.New("invalid hook session")
	}

	o := Observation{
		Row: r.Row, Report: agentsession.HookReport{Kind: "other", Session: session},
		Claim: agentsession.Claim{State: "unknown", Basis: "self", Source: "hook"},
		Model: r.Fields["model"], PermissionMode: r.Fields["mode"],
		Tool: r.Fields["tool"], InputDigest: r.Digests["input"],
	}
	switch row.Event {
	case "SessionStart":
		o.Report.Kind = "session-start"
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		o.Claim.State = "busy"
	case "PermissionRequest":
		o.Claim.State = "blocked"
	case "Stop":
		o.Claim.State = "idle"
		if value, present := r.Fields["stop_active"]; present {
			if value != "true" && value != "false" {
				return nil, errors.New("invalid stop flag")
			}
			active := value == "true"
			o.StopHookActive = &active
		}
	case "SessionEnd":
		// Only host evidence can mark a session ended.
	default:
		return nil, errors.New("unsupported hook event")
	}
	return &o, nil
}
