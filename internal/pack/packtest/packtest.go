// Package packtest supplies the two synthetic packs used by tests of core.
// Gamma is a well-formed name deliberately left out of their registry.
package packtest

import "ataniq/internal/pack"

type fake string

const (
	Alpha fake      = "alpha"
	Beta  fake      = "beta"
	Gamma pack.Name = "gamma"
)

func (f fake) Name() pack.Name { return pack.Name(f) }

func (f fake) Manifest() pack.Manifest {
	transport := pack.Terminal
	if f == Beta {
		transport = pack.Protocol
	}
	return pack.Manifest{
		Name: f.Name(), Class: "runtime", Vendor: "Example", Display: string(f),
		NoWordsReason: "Synthetic pack used by tests",
		Builds: []pack.Build{{
			Version: "1.0", Transports: []pack.Transport{transport},
			VerifiedOn: "2026-01-01", Source: "synthetic fixture",
		}},
	}
}
