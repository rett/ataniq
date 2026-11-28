package wire

import (
	"crypto/sha256"
	"encoding/hex"
)

// LaunchSpecVersion versions the pack launch data independently of the
// control protocol and the hook-port frames.
const LaunchSpecVersion = 1

// LaunchSpec carries values from the daemon's renderer. RequestedProfile
// holds the original serialized owner request for the holder's independent
// profile and policy recheck. TrustFile is the whole rendered file. Both are
// base64 on the wire so their bytes survive a round trip. The wire codec
// doesn't interpret provider file contents, resolve paths or judge policy.
type LaunchSpec struct {
	Version          uint16        `json:"version"`
	Pack             PackIdentity  `json:"pack"`
	RegistryID       string        `json:"registry_id"`
	Epoch            uint64        `json:"epoch"`
	IntentID         string        `json:"intent_id"`
	RequestedProfile []byte        `json:"requested_profile" wire:"required"`
	ProfileDigest    string        `json:"profile_digest"`
	Argv             []string      `json:"argv" wire:"required"`
	Env              []LaunchEnv   `json:"env" wire:"required"`
	PolicyFiles      []PolicyFile  `json:"policy_files" wire:"required"`
	HelperTable      string        `json:"helper_table"`
	TreeOverlays     []TreeOverlay `json:"tree_overlays" wire:"required"`
	TrustFile        []byte        `json:"trust_file" wire:"required"`
}

type LaunchEnv struct {
	Name  string `json:"name"`
	Value string `json:"value" wire:"required"`
}

type PolicyFile struct {
	Source string `json:"source"`
	Inside string `json:"inside"`
	SHA256 string `json:"sha256"`
}

// TreeOverlay is the derived (relative path, source, directory) triple.
type TreeOverlay struct {
	Rel    string `json:"rel"`
	Source string `json:"source"`
	Dir    bool   `json:"dir" wire:"required"`
}

func (s *LaunchSpec) validate() error {
	if s.Version != LaunchSpecVersion {
		return invalidf("unsupported launch spec version %d", s.Version)
	}
	if err := s.Pack.validate(); err != nil {
		return err
	}
	if err := checkEpoch(s.Epoch); err != nil {
		return err
	}
	for _, id := range []string{s.RegistryID, s.IntentID} {
		if err := checkText("launch identity", id, 1, 256, false); err != nil {
			return err
		}
	}
	if len(s.RequestedProfile) == 0 || len(s.RequestedProfile) > MaxControlPayload || s.TrustFile == nil || len(s.TrustFile) > MaxControlPayload {
		return invalidf("missing or oversized requested profile or trust file")
	}
	if s.ProfileDigest != digestHex(s.RequestedProfile) {
		return invalidf("requested profile digest mismatch")
	}
	if len(s.Argv) == 0 || len(s.Argv) > 256 || s.Env == nil || len(s.Env) > 128 {
		return invalidf("invalid argv or environment length")
	}
	for _, arg := range s.Argv {
		if err := checkText("argument", arg, 1, 4096, false); err != nil {
			return err
		}
	}
	names := make(map[string]bool, len(s.Env))
	for _, pair := range s.Env {
		if err := checkText("env name", pair.Name, 1, 256, false); err != nil {
			return err
		}
		if names[pair.Name] {
			return invalidf("repeated environment name")
		}
		names[pair.Name] = true
		if err := checkText("env value", pair.Value, 0, 4096, false); err != nil {
			return err
		}
	}
	if s.PolicyFiles == nil || s.TreeOverlays == nil || len(s.PolicyFiles)+len(s.TreeOverlays) > 12 {
		return invalidf("invalid policy file or tree overlay list")
	}
	for _, f := range s.PolicyFiles {
		if err := checkText("policy source", f.Source, 1, 4096, false); err != nil {
			return err
		}
		if err := checkText("policy destination", f.Inside, 1, 4096, false); err != nil {
			return err
		}
		if err := checkHex("policy file sha256", f.SHA256, sha256.Size*2); err != nil {
			return err
		}
	}
	for _, row := range s.TreeOverlays {
		if err := checkText("overlay rel", row.Rel, 1, 4096, false); err != nil {
			return err
		}
		if err := checkText("overlay source", row.Source, 1, 4096, false); err != nil {
			return err
		}
	}
	return checkText("helper table", s.HelperTable, 1, 4096, false)
}

func digestHex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func EncodeLaunchSpec(s LaunchSpec) ([]byte, error) { return encodeMessage(TypeRequest, &s) }

// DecodeLaunchSpec checks the lower-case SHA-256 from the launch intent
// against the entire payload as received, including its version and pack.
// Re-encoding the object before hashing would lose changes to the bytes.
func DecodeLaunchSpec(payload []byte, expectedDigest string) (LaunchSpec, error) {
	if len(payload) > MaxControlPayload {
		return LaunchSpec{}, ErrTooLarge
	}
	if digestHex(payload) != expectedDigest {
		return LaunchSpec{}, RefuseSpecDigest
	}
	var s LaunchSpec
	err := decodeMessage(&s, payload)
	return pick(s, err)
}
