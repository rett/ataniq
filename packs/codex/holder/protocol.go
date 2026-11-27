package holder

import "ataniq/packs/codex/protocol"

// ProtocolCodec selects a fresh codec for one measured schema.
func (Part) ProtocolCodec(version, schemaDigest string) (protocol.Codec, error) {
	return protocol.New(version, schemaDigest)
}
