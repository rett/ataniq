package appcontract

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"sync"
)

// Cursor processing supplies no authentication or authorization. Its consumer
// must enforce readiness, authentication and disclosure before reporting reset.
var errCursor = errors.New("invalid cursor")

type cursorContext interface {
	cursorFields(Instance) ([][]byte, error)
}
type eventsCursor struct{ viewer, project, conversation string }
type causesCursor struct {
	viewer, project, conversation, authority, client, command string
	anchor                                                    [32]byte
	scope                                                     string
	ack, head                                                 uint64
}
type adminCursor struct{ viewer, actor, command string }

func (c eventsCursor) cursorFields(i Instance) ([][]byte, error) {
	if !validID(c.viewer) || !validID(c.project) || !validID(c.conversation) {
		return nil, errInvalid
	}
	return [][]byte{byteField(2), []byte(c.viewer), []byte("events-queue"), []byte(c.project), []byte(c.conversation)}, nil
}
func (c causesCursor) cursorFields(i Instance) ([][]byte, error) {
	if !validID(c.viewer) || !validID(c.project) || !validID(c.conversation) || c.authority != i.Authority || !validID(c.client) || !validID(c.command) || (c.scope != "enrollment" && c.scope != "enqueue") || c.ack > c.head || c.head > math.MaxInt64 {
		return nil, errInvalid
	}
	return [][]byte{byteField(2), []byte(c.viewer), []byte("causes"), []byte(c.project), []byte(c.conversation), []byte(c.authority), []byte(c.client), []byte(c.command), c.anchor[:], []byte(c.scope), number(c.ack), number(c.head)}, nil
}
func (c adminCursor) cursorFields(i Instance) ([][]byte, error) {
	if !validID(c.viewer) || !validID(c.actor) || !validID(c.command) {
		return nil, errInvalid
	}
	return [][]byte{byteField(2), []byte(c.viewer), []byte("admin-lookup"), nil, nil, []byte(c.actor), []byte(c.command)}, nil
}
func cursorDigest(i Instance, c cursorContext) ([32]byte, error) {
	if !validID(i.Authority) || c == nil {
		return [32]byte{}, errInvalid
	}
	var f [][]byte
	var err error
	switch v := c.(type) {
	case eventsCursor:
		f, err = v.cursorFields(i)
	case causesCursor:
		f, err = v.cursorFields(i)
	case adminCursor:
		f, err = v.cursorFields(i)
	default:
		return [32]byte{}, errInvalid
	}
	if err != nil {
		return [32]byte{}, errInvalid
	}
	return sha256.Sum256(frame("CURSOR-CONTEXT", append(i.fields(), f...)...)), nil
}

const cursorEpochLimit = 1 << 20

type cursorAllocator struct {
	mu      sync.Mutex
	self    *cursorAllocator // copied values fail closed, including copies before first use
	epoch   [32]byte
	used    uint64
	entropy func(*[32]byte) error
}

func newCursorAllocator() (*cursorAllocator, error) {
	return newCursorAllocatorWithEntropy(func(e *[32]byte) error { _, err := rand.Read(e[:]); return err })
}
func newCursorAllocatorWithEntropy(entropy func(*[32]byte) error) (*cursorAllocator, error) {
	if entropy == nil {
		return nil, errInvalid
	}
	a := &cursorAllocator{entropy: entropy}
	a.self = a
	if err := entropy(&a.epoch); err != nil {
		return nil, errCursor
	}
	return a, nil
}
func (a *cursorAllocator) reserve() ([32]byte, error) {
	if a == nil || a.self != a {
		return [32]byte{}, errCursor
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used >= cursorEpochLimit {
		var next [32]byte
		if err := a.entropy(&next); err != nil {
			return [32]byte{}, errCursor
		}
		a.epoch, a.used = next, 0
	}
	a.used++
	return a.epoch, nil
}

// A composition must construct a fresh allocator; memory/VM/RNG clones must
// restart with fresh independent entropy before minting.
type cursorCodec struct {
	master    [32]byte
	allocator *cursorAllocator
	makeAEAD  func([]byte) (cipher.AEAD, error)
}

func newCursorCodec(master [32]byte) (*cursorCodec, error) {
	a, err := newCursorAllocator()
	if err != nil {
		return nil, err
	}
	return &cursorCodec{master: master, allocator: a, makeAEAD: cursorAEAD}, nil
}
func cursorAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(b)
}
func cursorKey(master [32]byte, i Instance, c [32]byte, epoch []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, master[:], epoch, string(frame("CURSOR-KEY-V2", append(i.fields(), c[:])...)), 32)
}
func cursorAAD(i Instance, c [32]byte, header []byte) []byte {
	return frame("CURSOR-AAD-V2", append(i.fields(), c[:], header)...)
}

// Bounds are supplied by the already-authorized consumer. Causes include the
// anchored acknowledged/head interval; admin observations have positive IDs.
func cursorBounds(c cursorContext, position, generation, tail uint64) bool {
	if position > math.MaxInt64 || generation > math.MaxInt64 || tail > math.MaxInt64 || position > tail {
		return false
	}
	switch v := c.(type) {
	case eventsCursor:
		return generation > 0 && position > 0
	case causesCursor:
		return generation == 0 && tail == v.head && position >= v.ack
	case adminCursor:
		return generation == 0 && position > 0
	default:
		return false
	}
}
func (codec *cursorCodec) mint(i Instance, c cursorContext, position, generation, tail uint64) (string, error) {
	if codec == nil || codec.allocator == nil || codec.makeAEAD == nil || !cursorBounds(c, position, generation, tail) {
		return "", errInvalid
	}
	digest, err := cursorDigest(i, c)
	if err != nil {
		return "", errInvalid
	}
	epoch, err := codec.allocator.reserve()
	if err != nil {
		return "", errCursor
	}
	key, err := cursorKey(codec.master, i, digest, epoch[:])
	if err != nil {
		return "", errCursor
	}
	aead, err := codec.makeAEAD(key)
	if err != nil || aead == nil {
		return "", errCursor
	}
	header := append([]byte{2}, epoch[:]...)
	plain := append(number(position), number(generation)...)
	plain = append(plain, digest[:]...)
	sealed := aead.Seal(nil, nil, plain, cursorAAD(i, digest, header))
	if len(sealed) != 76 {
		return "", errCursor
	}
	return base64.RawURLEncoding.EncodeToString(append(header, sealed...)), nil
}
func (codec *cursorCodec) decode(i Instance, c cursorContext, token string, generation, tail uint64) (uint64, error) {
	if codec == nil || codec.allocator == nil || codec.allocator.self != codec.allocator || codec.makeAEAD == nil {
		return 0, errCursor
	}
	probe := uint64(1)
	switch v := c.(type) {
	case causesCursor:
		probe = v.ack
	case adminCursor:
		probe = 1
	}
	if !cursorBounds(c, probe, generation, tail) {
		return 0, errCursor
	}
	digest, err := cursorDigest(i, c)
	if err != nil || len(token) != 146 {
		return 0, errCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 109 || raw[0] != 2 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return 0, errCursor
	}
	key, err := cursorKey(codec.master, i, digest, raw[1:33])
	if err != nil {
		return 0, errCursor
	}
	aead, err := codec.makeAEAD(key)
	if err != nil || aead == nil {
		return 0, errCursor
	}
	plain, err := aead.Open(nil, nil, raw[33:], cursorAAD(i, digest, raw[:33]))
	if err != nil || len(plain) != 48 || subtle.ConstantTimeCompare(plain[16:], digest[:]) != 1 {
		return 0, errCursor
	}
	pos, gen := binary.BigEndian.Uint64(plain[:8]), binary.BigEndian.Uint64(plain[8:16])
	if gen != generation || !cursorBounds(c, pos, gen, tail) {
		return 0, errCursor
	}
	return pos, nil
}
