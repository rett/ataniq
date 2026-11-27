package claude

import (
	_ "embed"

	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/pack"
)

//go:embed hook/rows.json
var hookRows []byte

// HookRows returns caller-owned rows for an exact build. As for Manifest,
// malformed embedded data is a pack construction error.
func (Pack) HookRows(build string) (hookwire.Table, bool) {
	d, err := pack.DecodeDeclaration(map[string][]byte{
		pack.ManifestPath: declaration,
		pack.HooksPath:    hookRows,
	}, pack.BindLayout{})
	if err != nil {
		panic(err)
	}
	rows, ok := d.Hooks[build]
	return hookwire.Table(rows), ok
}
