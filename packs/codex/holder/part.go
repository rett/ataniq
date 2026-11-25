// Package holder provides the Codex part linked into the holder.
package holder

import "ataniq/internal/pack"

type Part struct{}

func (Part) Name() pack.Name { return "codex" }

// Manifest exposes the build rows the holder can look up. The composition
// test compares them with the full pack's embedded declaration.
func (p Part) Manifest() pack.Manifest {
	return pack.Manifest{
		Name: p.Name(),
		Builds: []pack.Build{{
			Version: "0.169", Aliases: []string{"0.169.0"},
			Transports: []pack.Transport{pack.Terminal},
			Source:     "internal/inject/screen/testdata/codex-0.169",
		}},
	}
}
