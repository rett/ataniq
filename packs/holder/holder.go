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

// DeclarationDigest identifies the full declaration without linking sandbox
// data into the holder. TestRegisteredIdentities hashes the source files.
func DeclarationDigest(name pack.Name) (string, bool) {
	switch name {
	case (claude.Part{}).Name():
		return "c78cee3a4cb9fa3476b47caa19784edce0203a82b9a9ac971ff47e80b5d8213e", true
	case (codex.Part{}).Name():
		return "bea377e58e41ad2f5930be8c0655b75fafc6bf4c2b50975985810c24e85db5f3", true
	default:
		return "", false
	}
}
