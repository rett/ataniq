package codex

import (
	"ataniq/internal/agentsession"
	"ataniq/internal/pack"
	"ataniq/packs/codex/hook"
)

// Declaration validates the embedded hook rows against the pack's build list.
func (Pack) Declaration() (*pack.Declaration, error) {
	return pack.DecodeDeclaration(map[string][]byte{
		pack.ManifestPath: declaration,
		pack.HooksPath:    hook.RowsJSON(),
	}, pack.BindLayout{})
}

// DecodeHook reads one reduced frame. It performs no provider or host I/O.
func (Pack) DecodeHook(build string, frame []byte) (*hook.Observation, error) {
	return hook.Decode(build, frame)
}

// HookDecisionLimits lists the unavailable decision capabilities on this path.
// A Codex hook may fail open even when a helper attempts to deny a tool call.
func (Pack) HookDecisionLimits() map[string]agentsession.DenyReason {
	return map[string]agentsession.DenyReason{
		"permission.deny":    "pack-hooks-fail-open",
		"permission.route":   "pack-hooks-fail-open",
		"permission.partial": "pack-hooks-fail-open",
	}
}
