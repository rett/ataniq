package agentsession

import (
	"encoding/json"
	"strings"

	"ataniq/internal/pack"
)

const SessionRefSchema = "ataniq.session-ref/1"

// SessionRef identifies a launch at its home instance. The schema name is
// external to the fifteen-member JSON object.
type SessionRef struct {
	Authority               string `json:"authority"`
	StoreIncarnation        string `json:"store_incarnation"`
	Project                 string `json:"project"`
	HomeGeneration          uint64 `json:"home_generation"`
	Role                    string `json:"role"`
	Conversation            string `json:"conversation"`
	Launch                  string `json:"launch"`
	ConversationIncarnation uint64 `json:"conversation_incarnation"`
	LaunchEpoch             uint64 `json:"launch_epoch"`
	BindingGeneration       uint64 `json:"binding_generation"`
	Pack                    string `json:"pack"`
	PackDigest              string `json:"pack_digest"`
	SandboxDigest           string `json:"sandbox_digest"`
	Transport               string `json:"transport"`
	Custody                 string `json:"custody"`
}

// RefIdentity comes from the header of an open, admitted enrollment.
type RefIdentity struct {
	Authority, Incarnation string
}

// LaunchIntent carries the registry entry's role and its recorded launch.
type LaunchIntent struct {
	Project, Role, ID string
	Epoch             uint64
}

// LaunchedPack carries the holder hello's built-in pack identity and
// declaration digest, together with the sandbox profile digest of this start.
type LaunchedPack struct {
	Name, DeclarationDigest, SandboxDigest string
}

// BuildSessionRef constructs the S1 reference. Every launch starts a
// conversation, so both identifiers come from the same intent.
func BuildSessionRef(identity RefIdentity, intent LaunchIntent, launched LaunchedPack) (SessionRef, error) {
	ref := SessionRef{
		Authority: identity.Authority, StoreIncarnation: identity.Incarnation,
		Project: intent.Project, Role: intent.Role, HomeGeneration: 1,
		Launch: intent.ID, LaunchEpoch: intent.Epoch, Conversation: intent.ID,
		ConversationIncarnation: 1, BindingGeneration: 1,
		Pack: launched.Name, PackDigest: launched.DeclarationDigest, SandboxDigest: launched.SandboxDigest,
		Transport: "terminal", Custody: "local",
	}
	if err := ref.validate(); err != nil {
		return SessionRef{}, err
	}
	return ref, nil
}

// PerLaunchKey identifies per-launch state within the current incarnation.
type PerLaunchKey struct{ Authority, Launch string }

// GroupingKey identifies the conversation across its launches.
type GroupingKey struct{ Authority, Conversation string }

// Keys returns the per-launch and grouping keys for a reference in the
// current store. Callers keep each store incarnation's rows together and
// retire them when the incarnation changes. A stale reference yields no keys.
func (r SessionRef) Keys(current RefIdentity) (PerLaunchKey, GroupingKey, bool) {
	if current.Authority == "" || current.Incarnation == "" ||
		r.Authority != current.Authority || r.StoreIncarnation != current.Incarnation {
		return PerLaunchKey{}, GroupingKey{}, false
	}
	return PerLaunchKey{r.Authority, r.Launch}, GroupingKey{r.Authority, r.Conversation}, true
}

// Keep these wire limits in sync with assignment.MaxReferenceBytes and
// leadrole.MaxProjectLen. The session model has no runtime dependency on either.
const (
	maxAuthorityBytes = 162
	maxNameBytes      = 63
)

func validRefName(s string) bool {
	return len(s) > 0 && len(s) <= maxNameBytes && s[0] != '-' && s[len(s)-1] != '-' &&
		!strings.ContainsFunc(s, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
		})
}

func validRefRole(project, role string) bool {
	if role == "supervisor" || role == project+"/lead" {
		return true
	}
	feature, ok := strings.CutPrefix(role, project+"/coord/")
	return ok && validRefName(feature)
}

func validRefHex(s string, digits int) bool {
	return len(s) == digits && !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	})
}

func validRefPack(s string) bool {
	name, build, ok := strings.Cut(s, "@")
	sep := strings.LastIndexByte(build, '+') // the version may include build metadata
	if !ok || sep < 0 {
		return false
	}
	_, err := pack.BuiltinIdentity(pack.Name(name), build[:sep], build[sep+1:])
	return err == nil
}

func (r SessionRef) validate() error {
	var member string
	switch {
	case r.Authority == "" || len(r.Authority) > maxAuthorityBytes ||
		strings.ContainsFunc(r.Authority, func(c rune) bool { return c < ' ' || c > '~' }):
		member = "authority"
	case !validRefHex(r.StoreIncarnation, 32):
		member = "store_incarnation"
	case !validRefName(r.Project):
		member = "project"
	case r.HomeGeneration != 1:
		member = "home_generation"
	case !validRefRole(r.Project, r.Role):
		member = "role"
	case !validRefHex(r.Conversation, 32):
		member = "conversation"
	case !validRefHex(r.Launch, 32) || r.Launch != r.Conversation:
		member = "launch"
	case r.ConversationIncarnation != 1:
		member = "conversation_incarnation"
	case r.LaunchEpoch == 0:
		member = "launch_epoch"
	case r.BindingGeneration != 1:
		member = "binding_generation"
	case !validRefPack(r.Pack):
		member = "pack"
	case !validRefHex(r.PackDigest, 64):
		member = "pack_digest"
	case !validRefHex(r.SandboxDigest, 64):
		member = "sandbox_digest"
	case r.Transport != "terminal":
		member = "transport"
	case r.Custody != "local":
		member = "custody"
	default:
		return nil
	}
	return invalid(SessionRefSchema+"."+member, "inconsistent")
}

func (r SessionRef) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	type wire SessionRef
	return json.Marshal(wire(r))
}

func (r *SessionRef) UnmarshalJSON(b []byte) error {
	var next SessionRef
	err := object(b, SessionRefSchema, nil,
		value("authority", &next.Authority), value("store_incarnation", &next.StoreIncarnation),
		value("project", &next.Project), value("home_generation", &next.HomeGeneration),
		value("role", &next.Role), value("conversation", &next.Conversation),
		value("launch", &next.Launch), value("conversation_incarnation", &next.ConversationIncarnation),
		value("launch_epoch", &next.LaunchEpoch), value("binding_generation", &next.BindingGeneration),
		value("pack", &next.Pack), value("pack_digest", &next.PackDigest),
		value("sandbox_digest", &next.SandboxDigest), value("transport", &next.Transport), value("custody", &next.Custody))
	if err != nil {
		return err
	}
	// Each member is required and has a grammar that excludes its zero value.
	if err := next.validate(); err != nil {
		return err
	}
	*r = next
	return nil
}
