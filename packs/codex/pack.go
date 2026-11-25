// Package codex provides the built-in Codex runtime pack.
package codex

import (
	_ "embed"

	"ataniq/internal/pack"
)

//go:embed declaration.json
var declaration []byte

type Pack struct{}

func (Pack) Name() pack.Name { return "codex" }

func (Pack) Manifest() pack.Manifest {
	// Each caller owns the slices decoded from the built-in declaration.
	d, err := pack.DecodeDeclaration(map[string][]byte{pack.ManifestPath: declaration}, pack.BindLayout{})
	if err != nil {
		panic(err)
	}
	return d.Manifest
}
