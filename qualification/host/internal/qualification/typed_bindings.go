package qualification

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

const (
	FD1TypedProfile    = "fd1-typed"
	FD1BindingName     = "inherited-bindings.json"
	MaxFD1BindingBytes = 8192
	FD1DirectoryFD     = 128
	FD1PipeFD          = 256
	FD1SocketFD        = 384
	FD1RegularFD       = 512
	FD1BPFFD           = 513
)

// TypedFDPayload is an immutable challenge, not an endpoint identity supplied by
// the workload. The trusted injector will create endpoints and publish their
// identities in FD1Bindings before the host binds the receipt and releases it.
// Leaf is required only for the directory observation. Channel data is the
// fixed type-specific frame of Nonce, never a caller-defined protocol.
type TypedFDPayload struct {
	Name      string   `json:"name"`
	FD        int      `json:"fd"`
	Profile   string   `json:"profile"`
	AttemptID string   `json:"attempt_id"`
	Nonce     string   `json:"nonce"`
	Leaf      *FD1Leaf `json:"leaf,omitempty"`
}

type FD1Leaf struct {
	Name   string `json:"name"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Size   *int64 `json:"size"`
	SHA256 string `json:"sha256"`
}

// FD1Object has no owner field: global object identity/type matches across the
// user namespace. Host ownership belongs in the trusted injection receipt;
// idmapped-owner admission remains unsupported.
type FD1Object struct {
	FD       int    `json:"fd"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Type     string `json:"type"`
	Access   string `json:"access"`
	Nonblock *bool  `json:"nonblock"`
}

type FD1Directory struct {
	FD1Object
	Leaf *FD1Leaf `json:"leaf"`
}

type FD1Channel struct {
	FD1Object
	Length     int    `json:"length"`
	SHA256     string `json:"sha256"`
	PeerClosed *bool  `json:"peer_closed"`
}

type FD1Regular struct {
	FD1Object
	Size   *int64 `json:"size"`
	SHA256 string `json:"sha256"`
}

// FD1Bindings is the entire version-1 runtime artifact. Exactly four fixed
// records are allowed; there is no descriptor array, path, kind dispatch or
// transport negotiation. The BPF slot is launcher policy, not a probe record.
type FD1Bindings struct {
	Version    int           `json:"version"`
	RunID      string        `json:"run_id"`
	RequestID  string        `json:"request_id"`
	AttemptID  string        `json:"attempt_id"`
	Profile    string        `json:"profile"`
	PlanSHA256 string        `json:"plan_sha256"`
	Nonce      string        `json:"nonce"`
	Directory  *FD1Directory `json:"directory"`
	Pipe       *FD1Channel   `json:"pipe"`
	Socket     *FD1Channel   `json:"socket"`
	Regular    *FD1Regular   `json:"regular"`
}

func typedFDCase(c Case) bool {
	return c == CaseFakeDirFD || c == CaseFakePipeFD || c == CaseFakeSocketFD
}

func typedFDSlot(c Case) int {
	switch c {
	case CaseFakeDirFD:
		return FD1DirectoryFD
	case CaseFakePipeFD:
		return FD1PipeFD
	case CaseFakeSocketFD:
		return FD1SocketFD
	}
	return 0
}

func syntheticDigest(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

func fd1Frame(c Case, nonce string) []byte {
	if c == CaseFakePipeFD {
		return []byte("ataniq-fd1-pipe-v1 " + nonce + "\n")
	}
	return []byte("ataniq-fd1-unix-v1 " + nonce + "\n")
}

func validDigest(s string) bool             { return bounded(s, 64, 64, isHex) }
func validFD1Size(n *int64, limit int) bool { return n != nil && *n >= 0 && *n <= int64(limit) }
func (l *FD1Leaf) valid(limit int) bool {
	return l != nil && validName(l.Name) && l.Device != 0 && l.Inode != 0 && validFD1Size(l.Size, limit) && validDigest(l.SHA256)
}

func (p *TypedFDPayload) validate(r Request) error {
	if p.Name != FD1BindingName || p.FD != typedFDSlot(r.Case) || p.Profile != FD1TypedProfile ||
		!validHex(p.AttemptID) || !validHex(p.Nonce) {
		return errors.New("invalid fixed typed descriptor challenge")
	}
	if r.Case == CaseFakeDirFD {
		if !p.Leaf.valid(r.MaxBytes) {
			return errors.New("bounded synthetic leaf required")
		}
	} else if p.Leaf != nil || len(fd1Frame(r.Case, p.Nonce)) > r.MaxBytes {
		return errors.New("invalid typed frame budget or leaf")
	}
	return nil
}

func (o FD1Object) valid(fd int, kind, access string, nonblock bool) bool {
	return o.FD == fd && o.Device != 0 && o.Inode != 0 && o.Type == kind && o.Access == access && o.Nonblock != nil && *o.Nonblock == nonblock
}

func (c *FD1Channel) valid(fd int, kind, access string, frame []byte) bool {
	return c != nil && c.FD1Object.valid(fd, kind, access, true) && c.PeerClosed != nil && *c.PeerClosed &&
		c.Length > 0 && c.Length < 128 && c.Length == len(frame) && c.SHA256 == syntheticDigest(frame)
}

func (b FD1Bindings) Validate() error {
	if b.Version != Version1 || !validHex(b.RunID) || !validHex(b.RequestID) || !validHex(b.AttemptID) ||
		b.Profile != FD1TypedProfile || !validDigest(b.PlanSHA256) || !validHex(b.Nonce) ||
		b.Directory == nil || !b.Directory.FD1Object.valid(FD1DirectoryFD, "directory", "read-only", false) || !b.Directory.Leaf.valid(4096) ||
		!b.Pipe.valid(FD1PipeFD, "pipe", "read-only", fd1Frame(CaseFakePipeFD, b.Nonce)) ||
		!b.Socket.valid(FD1SocketFD, "unix-stream", "read-write", fd1Frame(CaseFakeSocketFD, b.Nonce)) ||
		b.Regular == nil || !b.Regular.FD1Object.valid(FD1RegularFD, "regular", "read-only", false) ||
		!validFD1Size(b.Regular.Size, 4096) || !validDigest(b.Regular.SHA256) {
		return errors.New("invalid fixed inherited bindings")
	}
	return nil
}

func (b FD1Bindings) matches(r Request) bool {
	p := r.Payload.TypedFD
	// The trusted side authenticates PlanSHA256 against the launched plan before
	// release. Putting that hash in the request would create a hash cycle because
	// the existing plan already binds request.json. Never rewrite that request.
	if b.RunID != r.RunID || b.RequestID != r.RequestID || b.AttemptID != p.AttemptID || b.Profile != p.Profile || b.Nonce != p.Nonce {
		return false
	}
	if r.Case == CaseFakeDirFD {
		a, c := b.Directory.Leaf, p.Leaf
		return a.Name == c.Name && a.Device == c.Device && a.Inode == c.Inode && *a.Size == *c.Size && a.SHA256 == c.SHA256
	}
	return true
}

func DecodeFD1Bindings(r io.Reader) (FD1Bindings, error) {
	var b FD1Bindings
	if err := decode(r, MaxFD1BindingBytes, &b); err != nil {
		return FD1Bindings{}, err
	}
	if err := b.Validate(); err != nil {
		return FD1Bindings{}, err
	}
	return b, nil
}
