// Package launchpolicy checks launches against the policy for a pinned build.
package launchpolicy

import (
	"ataniq/internal/pack"
	"regexp"
)

type Snapshot struct {
	Pack                                           pack.Name
	Version, Role                                  string
	Transport                                      pack.Transport
	OwnerProtocol                                  bool
	Mode, Model, Effort, Agent, Resume, Recognizer string
	Requested                                      []byte
	ProfileDigest                                  string
}

// View contains only the independent profile fields the matcher compares.
type View struct {
	Pack                         pack.Name
	Version, Mode, Model, Effort string
}
type Intent func(requested []byte) (digest string, v View, err error)
type Spec struct {
	Role          string
	Transport     pack.Transport
	Argv          []string
	Env           map[string]string
	ProfileDigest string
}
type Verdict struct {
	Class                    pack.ModeClass
	Recognizer, PolicyDigest string
}

type Comp = any
type Entry struct {
	Path       []Comp
	Type, Text string
}
type ExpectedFile struct {
	PathRole, Dest string
	Definition     bool
	AgentName      string
}
type Inventory []ExpectedFile
type FileView struct {
	PathRole, Dest, BytesSHA256, InstalledSHA256 string
	Entries                                      []Entry
}

var (
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	recognizerPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	ceiling           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`)
	profilePattern    = regexp.MustCompile(`^[ -~]{1,128}$`)
	shaPattern        = regexp.MustCompile(`^[a-f0-9]{64}$`)
)
var slots = [...]pack.Slot{pack.PermissionModeSlot, pack.ModelSlot, pack.EffortSlot, pack.AgentSlot, pack.ResumeSlot}

func (s Snapshot) value(slot pack.Slot) string {
	switch slot {
	case pack.PermissionModeSlot:
		return s.Mode
	case pack.ModelSlot:
		return s.Model
	case pack.EffortSlot:
		return s.Effort
	case pack.AgentSlot:
		return s.Agent
	case pack.ResumeSlot:
		return s.Resume
	}
	return ""
}
