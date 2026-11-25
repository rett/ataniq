package launchpolicy

import (
	"fmt"

	"ataniq/internal/pack"
)

// ProtocolExercise identifies one profile's case in the independent
// qualification record referenced by a protocol observation's sitting field.
type ProtocolExercise struct {
	Pack          pack.Name
	Build         string
	FixtureSHA256 string
	Observation   string
	Mode          string
	ProfileDigest string
}

// CheckProtocolSittings checks a decoded policy during qualification. The
// caller supplies the IDs of successful per-profile sittings from its own
// records. A policy's embedded list alone cannot establish what was exercised.
func (p *Policy) CheckProtocolSittings(records map[ProtocolExercise]string) error {
	profileBySitting := make(map[string]string)
	for _, m := range p.doc.Slots.Permission.Values {
		v := m.Vector.Protocol
		if v == nil || v.observation == nil {
			continue
		}
		for _, digest := range v.Profiles {
			exercise := ProtocolExercise{
				Pack: p.doc.Pack, Build: v.Build, FixtureSHA256: v.Fixture,
				Observation: v.Sitting, Mode: m.Name, ProfileDigest: digest,
			}
			id := records[exercise]
			if id == "" {
				return fmt.Errorf("missing protocol sitting for mode %q profile %q", m.Name, digest)
			}
			if previous, ok := profileBySitting[id]; ok && previous != digest {
				return fmt.Errorf("protocol sitting reused for profiles %q and %q", previous, digest)
			}
			profileBySitting[id] = digest
		}
	}
	return nil
}
