package holder

import (
	"ataniq/internal/agentsession/protocol"
	"ataniq/internal/pack"
	codex "ataniq/packs/codex/holder"
)

// ProtocolCodec looks up the holder-side wire codec by exact build and
// measured schema digest. Selection alone does not qualify a transport.
func ProtocolCodec(name pack.Name, version, schemaDigest string) (protocol.Codec, error) {
	if name == (codex.Part{}).Name() {
		return codex.Part{}.ProtocolCodec(version, schemaDigest)
	}
	return nil, protocol.ErrUntested
}
