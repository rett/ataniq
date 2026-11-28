// Package holder provides the Claude Code part linked into the holder.
package holder

import "ataniq/internal/pack"

type Part struct{}

func (Part) Name() pack.Name { return "claude" }

// Manifest exposes the build rows the holder can look up. The composition
// test compares them with the full pack's embedded declaration.
func (p Part) Manifest() pack.Manifest {
	return pack.Manifest{
		Name: p.Name(),
		Builds: []pack.Build{{
			Version:    "2.1.325",
			Transports: []pack.Transport{pack.Terminal},
			Source:     "internal/inject/screen/testdata/claude-2.1.325",
		}, {
			Version: "2.1.332", Transports: []pack.Transport{pack.Terminal},
			Source: "packs/claude/launchpolicy/2.1.332.help.txt",
		}},
	}
}
