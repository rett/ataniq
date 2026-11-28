// Package all registers the built-in provider packs at a composition root.
package all

import (
	"ataniq/internal/pack"
	"ataniq/packs/claude"
	"ataniq/packs/codex"
)

// Register adds the built-ins during setup. The caller freezes the registry
// after successful registration.
func Register(r *pack.Registry) error {
	for _, p := range []pack.Pack{claude.Pack{}, codex.Pack{}} {
		if err := r.Register(p); err != nil {
			return err
		}
	}
	return nil
}
