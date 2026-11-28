package packtest

import (
	"encoding/json/jsontext"

	"ataniq/internal/pack"
)

// AlphaHookRows selects a session and event from Alpha's synthetic hook input.
func AlphaHookRows() []pack.HookRow {
	var rows []pack.HookRow
	for _, id := range []string{"start", "idle"} {
		session, event := jsontext.Pointer("/session"), jsontext.Pointer("/event")
		rows = append(rows, pack.HookRow{
			ID: id, Event: id, Output: "none", TimeoutMS: 1000,
			Fields: []pack.HookField{{Name: "session", Pointer: &session}, {Name: "event", Pointer: &event}},
		})
	}
	return rows
}
