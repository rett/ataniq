// Package holder registers the built-in holder parts for terminal sessions.
package holder

import (
	"ataniq/internal/pack"
	claude "ataniq/packs/claude/holder"
	codex "ataniq/packs/codex/holder"
)

// Register adds each runtime's holder part. The caller owns the registry.
func Register(r *pack.Registry) error {
	for _, p := range []pack.Pack{claude.Part{}, codex.Part{}} {
		if err := r.Register(p); err != nil {
			return err
		}
	}
	return nil
}
