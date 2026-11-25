// Package claude provides the built-in Claude Code runtime pack.
package claude

import (
	_ "embed"

	"ataniq/internal/pack"
)

//go:embed declaration.json
var declaration []byte

type Pack struct{}

func (Pack) Name() pack.Name { return "claude" }

func (Pack) Manifest() pack.Manifest {
	// Each caller owns the slices decoded from the built-in declaration.
	d, err := pack.DecodeDeclaration(map[string][]byte{pack.ManifestPath: declaration}, pack.BindLayout{})
	if err != nil {
		panic(err)
	}
	return d.Manifest
}
