package checkpoint

import (
	"crypto/ed25519"
	"fmt"
	"io"
	"slices"
)

// Only the signing closure captures the private bytes. Structural inspection
// of the capability can see its public key and function value.
type memoryKey struct {
	public ed25519.PublicKey
	sign   func([]byte) ([]byte, error)
}

func newMemoryKey(k ed25519.PrivateKey, p profile) *memoryKey {
	private := slices.Clone(k)
	return &memoryKey{private.Public().(ed25519.PublicKey), func(body []byte) ([]byte, error) { return p.sign(private, body) }}
}
func (memoryKey) String() string                 { return "[redacted checkpoint key]" }
func (k memoryKey) GoString() string             { return k.String() }
func (k memoryKey) Format(out fmt.State, _ rune) { io.WriteString(out, k.String()) }
