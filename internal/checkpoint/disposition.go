package checkpoint

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
)

// DispositionSigner belongs to the witness backend. It signs a response to
// one exact request using the backend's own epoch and witness key.
type DispositionSigner interface {
	SignDisposition(context.Context, [32]byte, string) ([]byte, error)
}

func dispositionBody(epoch uint64, digest [32]byte, result string) []byte {
	b := binary.BigEndian.AppendUint64([]byte("checkpoint disposition v2\x00"), epoch)
	b = append(b, digest[:]...)
	return append(b, result...)
}

func (w *Memory) SignDisposition(ctx context.Context, digest [32]byte, result string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return witnessProfile.sign(w.key, dispositionBody(w.epoch, digest, result))
}

// VerifyDisposition uses the independently provisioned epoch and public key.
func VerifyDisposition(pin ed25519.PublicKey, epoch uint64, digest [32]byte, result string, sig []byte) bool {
	return VerifySignature(WitnessNamespace, pin, dispositionBody(epoch, digest, result), sig) == nil
}
