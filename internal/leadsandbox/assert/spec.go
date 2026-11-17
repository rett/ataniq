package assert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"time"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/leadsandbox/profile"
)

// SpecVersion is the version of the spec format.
const SpecVersion = 1

// Bounds on a spec. The holder writes it and the probe reads it from a
// read-only folder, but the decoder still treats it as input.
const (
	// MaxSpecBytes is the largest spec DecodeSpec reads and EncodeSpec writes.
	MaxSpecBytes = 256 << 10

	MaxEnvEntries      = 128
	MaxPathEntries     = 64
	MaxMountSubstrings = 64
	MaxOptional        = NumAssertions
	MaxPathBytes       = 4096
	MaxEnvNameBytes    = 64
	MaxEnvValueBytes   = 4096
	MaxSubstringBytes  = 256
	MaxHostNameBytes   = 253
	MaxMarkerBytes     = 256
	MaxAbstractBytes   = 100

	// The limits of the optional time limits, in milliseconds, and their
	// defaults when the spec leaves them out (zero).
	MinTimeoutMS     = 100
	MaxTimeoutMS     = 10000
	DefaultConnectMS = 1000
	DefaultResolveMS = 5000
)

// EnvEntry is one variable of the expected environment.
type EnvEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Spec is what the holder writes for the probe: what the inside checks look
// for. It is JSON, versioned, and decoded strictly (see DecodeSpec). Paths
// are inside paths. A path in a list that "must not exist" is written as it
// would appear inside if it were mounted: a host path bound at the same place,
// or the place the profile would put it.
type Spec struct {
	Version int `json:"version"`
	// Digest is the profile digest the holder computed (profile.Inputs.Digest).
	// The report carries it back.
	Digest  string          `json:"digest"`
	Runtime profile.Runtime `json:"runtime"`

	// Env is the expected environment, names and values (assertion 2).
	Env []EnvEntry `json:"env"`

	// HolderPaths must not exist inside: the attach and control sockets,
	// the holder's state (assertion 1).
	HolderPaths []string `json:"holder_paths"`
	// MountSubstrings must not appear in the mount table, in a mount point,
	// a root or a source (assertion 1).
	MountSubstrings []string `json:"mount_substrings"`

	// OwnerPaths must not exist inside: the owner's real provider folders,
	// .ssh, the owner's checkout (assertion 6).
	OwnerPaths []string `json:"owner_paths"`
	// OtherConfigDir is the other runtime's config folder as it appears
	// inside, in the lead's home. A lead can make an empty folder there; the
	// check is that nothing is mounted at or below it (assertion 6).
	OtherConfigDir string `json:"other_config_dir"`

	// LoopbackTCP is one host loopback address (ip:port) and AbstractSocket
	// one host abstract socket name (without the leading @) that must be
	// unreachable (assertion 3).
	LoopbackTCP    string `json:"loopback_tcp"`
	AbstractSocket string `json:"abstract_socket"`

	// ProviderHost is a provider host name that must resolve (assertion 4).
	ProviderHost string `json:"provider_host"`

	// SeccompExpected says a reviewed seccomp filter is part of the
	// profile, so it must be loaded (assertion 5).
	SeccompExpected bool `json:"seccomp_expected"`

	// Tree is the work tree inside and GitReadOnly its .git/config,
	// .git/hooks and .git/info, in that order (assertion 7).
	Tree        string   `json:"tree"`
	GitReadOnly []string `json:"git_read_only"`

	// ManagedSettings is the managed-settings file inside, and
	// ManagedMarker text it must contain: for claude a name that is in the
	// guard hook's entry (assertion 8). Required for claude. The check is a
	// plain search for the text; it does not parse the file or show that the
	// hook is active.
	ManagedSettings string `json:"managed_settings"`
	ManagedMarker   string `json:"managed_marker,omitempty"`

	// Optional lists assertions (1 to 8) whose not-checked result does not
	// fail the start. A failed result always does.
	Optional []int `json:"optional,omitempty"`

	// ConnectTimeoutMS and ResolveTimeoutMS limit a dial and a name
	// resolution, in milliseconds, 100 to 10000; zero means the default.
	ConnectTimeoutMS int `json:"connect_timeout_ms,omitempty"`
	ResolveTimeoutMS int `json:"resolve_timeout_ms,omitempty"`
}

// ConnectTimeout is the limit of one dial.
func (s Spec) ConnectTimeout() time.Duration {
	if s.ConnectTimeoutMS == 0 {
		return DefaultConnectMS * time.Millisecond
	}
	return time.Duration(s.ConnectTimeoutMS) * time.Millisecond
}

// ResolveTimeout is the limit of one name resolution.
func (s Spec) ResolveTimeout() time.Duration {
	if s.ResolveTimeoutMS == 0 {
		return DefaultResolveMS * time.Millisecond
	}
	return time.Duration(s.ResolveTimeoutMS) * time.Millisecond
}

// OptionalSet says whether the spec marks assertion n optional.
func (s Spec) OptionalSet(n int) bool {
	for _, o := range s.Optional {
		if o == n {
			return true
		}
	}
	return false
}

// DecodeSpec reads a spec, strictly: at most MaxSpecBytes (a larger input is
// refused, not truncated), one JSON object, no unknown field and no trailing
// data, and then Validate. The error is a *Refusal.
func DecodeSpec(r io.Reader) (Spec, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSpecBytes+1))
	if err != nil {
		return Spec{}, fmt.Errorf("assert: read spec: %w", err)
	}
	return DecodeSpecBytes(data)
}

// DecodeSpecBytes is DecodeSpec for bytes already read.
func DecodeSpecBytes(data []byte) (Spec, error) {
	if len(data) > MaxSpecBytes {
		return Spec{}, refuse("spec", "is larger than %d bytes", MaxSpecBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, refuse("spec", "is not a spec: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Spec{}, refuse("spec", "has data after the spec")
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// EncodeSpec validates the spec and returns it as indented JSON ending in a
// newline. It refuses a spec over MaxSpecBytes.
func EncodeSpec(s Spec) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("assert: encode spec: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxSpecBytes {
		return nil, refuse("spec", "is larger than %d bytes", MaxSpecBytes)
	}
	return data, nil
}

// Validate refuses a spec the probe cannot use: a wrong version, an invalid
// runtime or digest, an empty list that would make an assertion vacuous, an
// entry or list over its bound, a relative or unclean path, a control
// character, an address that is not loopback, a host name that is an address,
// an other-runtime folder that is not the profile's, git paths that are not
// the tree's three, and a claude spec without a marker or with another
// managed-settings path. The error is a *Refusal.
func (s Spec) Validate() error {
	if s.Version != SpecVersion {
		return refuse("version", "is %d, want %d", s.Version, SpecVersion)
	}
	if !validDigest(s.Digest) {
		return refuse("digest", "must be 64 lower-case hex digits")
	}
	if !s.Runtime.Valid() {
		return refuse("runtime", "must be %q or %q", profile.Claude, profile.Codex)
	}
	if err := s.checkEnv(); err != nil {
		return err
	}
	if err := checkPaths("holder_paths", s.HolderPaths); err != nil {
		return err
	}
	if err := checkPaths("owner_paths", s.OwnerPaths); err != nil {
		return err
	}
	if err := checkSubstrings(s.MountSubstrings); err != nil {
		return err
	}
	if want := profile.ConfigInside(s.Runtime.Other()); s.OtherConfigDir != want {
		return refuse("other_config_dir", "must be %s, the other runtime's config folder inside", want)
	}
	if err := s.checkNet(); err != nil {
		return err
	}
	if err := checkHostName(s.ProviderHost); err != nil {
		return err
	}
	if err := s.checkTree(); err != nil {
		return err
	}
	if err := s.checkManaged(); err != nil {
		return err
	}
	if err := s.checkLimits(); err != nil {
		return err
	}
	return nil
}

func (s Spec) checkEnv() error {
	if len(s.Env) == 0 {
		return refuse("env", "is empty: an empty allowlist would be unchecked")
	}
	if len(s.Env) > MaxEnvEntries {
		return refuse("env", "more than %d entries", MaxEnvEntries)
	}
	seen := map[string]bool{}
	for i, e := range s.Env {
		field := fmt.Sprintf("env[%d]", i)
		if !validEnvName(e.Name) {
			return refuse(field, "name must match [A-Za-z_][A-Za-z0-9_]* and be at most %d bytes", MaxEnvNameBytes)
		}
		if len(e.Value) > MaxEnvValueBytes || hasControl(e.Value) {
			return refuse(field, "value of %s is longer than %d bytes or holds a control character", e.Name, MaxEnvValueBytes)
		}
		if seen[e.Name] {
			return refuse(field, "name %s is listed twice", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}

func validEnvName(n string) bool {
	if n == "" || len(n) > MaxEnvNameBytes {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
		if i > 0 {
			ok = ok || c >= '0' && c <= '9'
		}
		if !ok {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func checkPaths(field string, paths []string) error {
	if len(paths) == 0 {
		return refuse(field, "is empty: an empty list would check nothing")
	}
	if len(paths) > MaxPathEntries {
		return refuse(field, "more than %d paths", MaxPathEntries)
	}
	for i, p := range paths {
		if err := profile.CheckPath(fmt.Sprintf("%s[%d]", field, i), p); err != nil {
			return refuse(fmt.Sprintf("%s[%d]", field, i), "%s", reasonOf(err))
		}
		if p == "/" {
			return refuse(fmt.Sprintf("%s[%d]", field, i), "must not be the root")
		}
	}
	return nil
}

// reasonOf returns the reason of a profile refusal, else the error text.
func reasonOf(err error) string {
	if r, ok := err.(*profile.Refusal); ok {
		return r.Reason
	}
	return err.Error()
}

func checkSubstrings(list []string) error {
	if len(list) == 0 {
		return refuse("mount_substrings", "is empty: an empty list would check nothing")
	}
	if len(list) > MaxMountSubstrings {
		return refuse("mount_substrings", "more than %d entries", MaxMountSubstrings)
	}
	for i, v := range list {
		if v == "" || len(v) > MaxSubstringBytes || hasControl(v) || !utf8.ValidString(v) {
			return refuse(fmt.Sprintf("mount_substrings[%d]", i), "must be 1 to %d bytes of text without control characters", MaxSubstringBytes)
		}
	}
	return nil
}

func (s Spec) checkNet() error {
	ap, err := netip.ParseAddrPort(s.LoopbackTCP)
	if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
		return refuse("loopback_tcp", "must be a loopback ip:port with a non-zero port")
	}
	if !validAbstractName(s.AbstractSocket) {
		return refuse("abstract_socket", "must be 1 to %d bytes of [A-Za-z0-9._-], without the leading @", MaxAbstractBytes)
	}
	return nil
}

func validAbstractName(n string) bool {
	if n == "" || len(n) > MaxAbstractBytes {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func checkHostName(h string) error {
	if h == "" || len(h) > MaxHostNameBytes {
		return refuse("provider_host", "must be 1 to %d bytes", MaxHostNameBytes)
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return refuse("provider_host", "is an address: an address needs no resolution")
	}
	labelLen := 0
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c == '.':
			if labelLen == 0 {
				return refuse("provider_host", "has an empty label")
			}
			labelLen = 0
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-':
			labelLen++
			if labelLen > 63 {
				return refuse("provider_host", "has a label longer than 63 bytes")
			}
		default:
			return refuse("provider_host", "must hold only letters, digits, '-' and '.'")
		}
	}
	if labelLen == 0 {
		return refuse("provider_host", "has an empty label")
	}
	return nil
}

func (s Spec) checkTree() error {
	if err := profile.CheckPath("tree", s.Tree); err != nil {
		return refuse("tree", "%s", reasonOf(err))
	}
	if s.Tree == "/" {
		return refuse("tree", "must not be the root")
	}
	rels := profile.GitReadOnlyRel()
	if len(s.GitReadOnly) != len(rels) {
		return refuse("git_read_only", "must list exactly the %d paths %v below the tree", len(rels), rels)
	}
	for i, rel := range rels {
		if want := s.Tree + "/" + rel; s.GitReadOnly[i] != want {
			return refuse(fmt.Sprintf("git_read_only[%d]", i), "must be %s", want)
		}
	}
	return nil
}

func (s Spec) checkManaged() error {
	if err := profile.CheckPath("managed_settings", s.ManagedSettings); err != nil {
		return refuse("managed_settings", "%s", reasonOf(err))
	}
	if s.Runtime == profile.Claude {
		if s.ManagedSettings != profile.ClaudeManagedInside {
			return refuse("managed_settings", "must be %s for claude, the only place it reads policy from", profile.ClaudeManagedInside)
		}
		if s.ManagedMarker == "" {
			return refuse("managed_marker", "is required for claude: the text of the guard hook the file must contain")
		}
	}
	if len(s.ManagedMarker) > MaxMarkerBytes || !cleanMarker(s.ManagedMarker) {
		return refuse("managed_marker", "must be at most %d bytes of printable text", MaxMarkerBytes)
	}
	return nil
}

func cleanMarker(m string) bool {
	if !utf8.ValidString(m) {
		return false
	}
	for _, r := range m {
		if r != ' ' && !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func (s Spec) checkLimits() error {
	if len(s.Optional) > MaxOptional {
		return refuse("optional", "more than %d entries", MaxOptional)
	}
	seen := map[int]bool{}
	for _, n := range s.Optional {
		a, ok := ByNumber(n)
		if !ok || !a.HasInside() {
			return refuse("optional", "%d is not an assertion with an inside half (1 to 8)", n)
		}
		if seen[n] {
			return refuse("optional", "%d is listed twice", n)
		}
		seen[n] = true
	}
	for _, t := range []struct {
		field string
		v     int
	}{{"connect_timeout_ms", s.ConnectTimeoutMS}, {"resolve_timeout_ms", s.ResolveTimeoutMS}} {
		if t.v != 0 && (t.v < MinTimeoutMS || t.v > MaxTimeoutMS) {
			return refuse(t.field, "must be 0 (the default) or %d to %d", MinTimeoutMS, MaxTimeoutMS)
		}
	}
	return nil
}
