package wire

import (
	"crypto/sha256"
	"errors"
	"strings"
)

// PreStartVersion adds the offer and holder hello on the control socket.
// A v1 peer has only the ordinary request and response payloads.
const PreStartVersion = 2

// VersionRange includes both endpoints.
type VersionRange struct {
	Min uint16 `json:"min"`
	Max uint16 `json:"max"`
}

func (r VersionRange) valid() bool { return r.Min != 0 && r.Min <= r.Max }

// NegotiationRefusal is a machine-readable reason a pre-start check failed.
type NegotiationRefusal string

func (r NegotiationRefusal) Error() string { return string(r) }

const (
	RefuseRange         NegotiationRefusal = "invalid-version-range"
	RefuseNoVersion     NegotiationRefusal = "no-common-version"
	RefuseSelection     NegotiationRefusal = "wrong-selected-version"
	RefuseHelloRequired NegotiationRefusal = "hello-required"
	RefuseLaunchVersion NegotiationRefusal = "launch-version-unsupported"
	RefuseBuild         NegotiationRefusal = "holder-build-mismatch"
	RefusePack          NegotiationRefusal = "pack-identity-mismatch"
	RefuseCapability    NegotiationRefusal = "capability-unavailable"
	RefuseSpecDigest    NegotiationRefusal = "launch-spec-digest-mismatch"
)

// NegotiateVersion selects the highest version in both offers.
func NegotiateVersion(a, b VersionRange) (uint16, error) {
	if !a.valid() || !b.valid() {
		return 0, RefuseRange
	}
	v := min(a.Max, b.Max)
	if v < max(a.Min, b.Min) {
		return 0, RefuseNoVersion
	}
	return v, nil
}

// ControlOffer is the daemon's pre-start request. Version identifies the
// encoding of this payload, independently of the offered protocol range.
type ControlOffer struct {
	Version uint16       `json:"version"`
	Range   VersionRange `json:"range"`
}

func (o *ControlOffer) validate() error {
	if o.Version != PreStartVersion {
		return invalidf("unsupported offer version")
	}
	if !o.Range.valid() {
		return errors.Join(ErrInvalid, RefuseRange)
	}
	return nil
}

func EncodeControlOffer(o ControlOffer) ([]byte, error) { return encodeMessage(TypeRequest, &o) }

func DecodeControlOffer(payload []byte) (ControlOffer, error) {
	var o ControlOffer
	err := decodePreStart(&o, payload)
	return pick(o, err)
}

// DecodeControlOpening recognizes the first request on a control connection.
// Legacy commands and offers share a frame type but have disjoint, strict
// schemas. A valid launch spec here can only be refused.
func DecodeControlOpening(payload []byte) (Request, *ControlOffer, error) {
	if len(payload) > MaxControlPayload {
		return Request{}, nil, ErrTooLarge
	}
	req, err := DecodeRequest(payload)
	if err == nil {
		return req, nil, nil
	}
	offer, offerErr := DecodeControlOffer(payload)
	if offerErr == nil {
		return Request{}, &offer, nil
	}
	if errors.Is(offerErr, RefuseRange) {
		return Request{}, nil, offerErr
	}
	if decodePreStart(&LaunchSpec{}, payload) == nil {
		return Request{}, nil, RefuseHelloRequired
	}
	return Request{}, nil, err
}

// A direct payload decoder also bounds its input before scanning the JSON.
func decodePreStart(m message, payload []byte) error {
	if len(payload) > MaxControlPayload {
		return ErrTooLarge
	}
	return decodeMessage(m, payload)
}

// PackIdentity pairs the full built-in name (<pack>@<release>+<revision>)
// with the lower-case SHA-256 produced by the declaration digest algorithm.
type PackIdentity struct {
	Name              string `json:"name"`
	DeclarationDigest string `json:"declaration_digest"`
}

const (
	lower  = "abcdefghijklmnopqrstuvwxyz"
	upper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits = "0123456789"
)

func charsIn(s string, minLen, maxLen int, alphabet string) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := range len(s) {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

func (p PackIdentity) validate() error {
	if len(p.Name) > 256 {
		return invalidf("built-in pack identity exceeds 256 bytes")
	}
	name, build, ok := strings.Cut(p.Name, "@")
	rev := strings.LastIndexByte(build, '+')
	if !ok || rev <= 0 || rev == len(build)-1 {
		return invalidf("invalid built-in pack identity")
	}
	if !charsIn(name, 2, 40, lower+digits+"-") || name[0] < 'a' || name[0] > 'z' {
		return invalidf("invalid pack name")
	}
	for _, c := range build {
		if c < '!' || c > '~' || c == '@' {
			return invalidf("invalid pack release or revision")
		}
	}
	return checkHex("declaration_digest", p.DeclarationDigest, sha256.Size*2)
}

// HolderBuild carries the four compared fields of the holder build identity.
// Tags and toolchain version are provenance. An unstamped build can display.
type HolderBuild struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Revision string `json:"revision" wire:"required"`
	Modified bool   `json:"modified" wire:"required"`
}

func (b HolderBuild) valid() bool {
	const alnum = lower + upper + digits
	return charsIn(b.Module, 1, 256, alnum+"._~/-") &&
		charsIn(b.Version, 1, 128, alnum+"._+-()") &&
		charsIn(b.Revision, 0, 64, digits+"abcdef")
}

// ControlHello is the holder's answer to ControlOffer. HookFrames lists the
// independent hook-port frame versions in ascending order. Session reports
// support for the bounded session block in Status. SpecDigest identifies the
// exact spec bytes held before start, or is empty for a display-only hello.
type ControlHello struct {
	Version    uint16         `json:"version"`
	Range      VersionRange   `json:"range"`
	Selected   uint16         `json:"selected"`
	Build      HolderBuild    `json:"build_id"`
	Packs      []PackIdentity `json:"packs" wire:"required"`
	HookFrames []uint16       `json:"hook_frames" wire:"required"`
	Session    bool           `json:"session" wire:"required"`
	SpecDigest string         `json:"spec_digest" wire:"required"`
}

func (h *ControlHello) validate() error {
	if h.Version != PreStartVersion || !h.Range.valid() || h.Selected < h.Range.Min || h.Selected > h.Range.Max {
		return invalidf("unsupported hello version, range or selection")
	}
	if !h.Build.valid() {
		return invalidf("invalid holder build id")
	}
	if h.Packs == nil || len(h.Packs) > 64 || h.HookFrames == nil || len(h.HookFrames) > 16 {
		return invalidf("invalid pack or hook frame list")
	}
	names := make(map[string]bool, len(h.Packs))
	for _, p := range h.Packs {
		if err := p.validate(); err != nil {
			return err
		}
		name, _, _ := strings.Cut(p.Name, "@")
		if names[name] {
			return invalidf("repeated pack %q", name)
		}
		names[name] = true
	}
	var prev uint16
	for _, v := range h.HookFrames {
		if v <= prev {
			return invalidf("hook frame versions must be positive and strictly increasing")
		}
		prev = v
	}
	if h.SpecDigest != "" {
		return checkHex("spec_digest", h.SpecDigest, 2*sha256.Size)
	}
	return nil
}

func EncodeControlHello(h ControlHello) ([]byte, error) { return encodeMessage(TypeResponse, &h) }

func DecodeControlHello(payload []byte) (ControlHello, error) {
	var h ControlHello
	err := decodePreStart(&h, payload)
	return pick(h, err)
}

// DisplayVersion verifies the holder's selection against our own offer. A
// known v1 peer speaks only legacy Status and has no hello (nil). An invalid
// hello is a protocol error and must never be treated as a legacy peer.
func DisplayVersion(local VersionRange, h *ControlHello) (uint16, error) {
	if h == nil {
		return NegotiateVersion(local, VersionRange{ControlVersion, ControlVersion})
	}
	if err := h.validate(); err != nil {
		return 0, err
	}
	v, err := NegotiateVersion(local, h.Range)
	if err != nil {
		return 0, err
	}
	if h.Selected != v {
		return 0, RefuseSelection
	}
	return v, nil
}

// LaunchRequirements are independent expectations from the launch intent.
// A zero hook version or false Session means the feature is not required.
type LaunchRequirements struct {
	Build      HolderBuild
	Pack       PackIdentity
	SpecDigest string
	HookFrame  uint16
	Session    bool
}

// NegotiateLaunch verifies the hello and the exact spec bytes for this
// intent. It returns the decoded spec only on agreement. Peer authentication,
// policy recheck and authority to start belong to the caller.
func NegotiateLaunch(local VersionRange, h *ControlHello, want LaunchRequirements, payload []byte) (LaunchSpec, error) {
	v, err := DisplayVersion(local, h)
	if err != nil {
		return LaunchSpec{}, err
	}
	if v != PreStartVersion {
		return LaunchSpec{}, RefuseLaunchVersion
	}
	if h.Build.Revision == "" || h.Build != want.Build {
		return LaunchSpec{}, RefuseBuild
	}
	if !contains(h.Packs, want.Pack) {
		return LaunchSpec{}, RefusePack
	}
	if want.Session && !h.Session || want.HookFrame != 0 && !contains(h.HookFrames, want.HookFrame) {
		return LaunchSpec{}, RefuseCapability
	}
	if h.SpecDigest == "" || h.SpecDigest != want.SpecDigest {
		return LaunchSpec{}, RefuseSpecDigest
	}
	spec, err := DecodeLaunchSpec(payload, want.SpecDigest)
	if err != nil {
		return LaunchSpec{}, err
	}
	if spec.Pack != want.Pack {
		return LaunchSpec{}, RefusePack
	}
	return spec, nil
}

func contains[T comparable](values []T, want T) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
