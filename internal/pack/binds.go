package pack

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
)

type BindRole string

const (
	ProviderPolicy BindRole = "provider-policy"
	ConfigOverlay  BindRole = "config-overlay"
	TreeOverlay    BindRole = "tree-overlay"
	SessionStore   BindRole = "session-store"
)

type Bind struct {
	Role     BindRole `json:"role"`
	Kind     string   `json:"kind"`   // file or folder
	Inside   string   `json:"inside"` // tree overlays alone are tree-relative
	Optional bool     `json:"optional,omitempty"`
}

// BindLayout comes from the consumer, independently of the declaration.
// CoreRows are the inside paths already occupied by the profile, including
// the skills folder, instruction file, and writable mounts. Config and Tree
// identify the two mounts allowed to contain additional rows.
type BindLayout struct {
	Config, Tree string
	CoreRows     []string
	// CheckExtraEnv applies the consumer's profile rules to the complete
	// launch.env map. It is required for a nonempty environment.
	CheckExtraEnv func(map[string]string) error
}

func cleanInside(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !strings.ContainsFunc(p, unicode.IsControl)
}

func below(p, parent string) bool {
	return parent != "" && strings.HasPrefix(p, strings.TrimSuffix(parent, "/")+"/") && p != parent
}

func overlap(p, q string) bool { return p == q || below(p, q) || below(q, p) }

func (b Bind) destination(layout BindLayout) (string, error) {
	p := b.Inside
	if !cleanInside(p) || (b.Kind != "file" && b.Kind != "folder") {
		return "", fmt.Errorf("invalid bind path or kind for %q", p)
	}
	var parent string
	switch b.Role {
	case TreeOverlay:
		if path.IsAbs(p) || p == ".." || below(p, "..") || p == ".git" || below(p, ".git") {
			return "", fmt.Errorf("invalid tree overlay %q", p)
		}
		parent = layout.Tree
		p = path.Join(parent, p)
	case ConfigOverlay, SessionStore:
		if !below(p, layout.Config) || (b.Role == SessionStore && b.Kind != "folder") {
			return "", fmt.Errorf("invalid %s at %q", b.Role, p)
		}
		parent = layout.Config
	case ProviderPolicy:
		if b.Kind != "file" || !below(p, "/etc") {
			return "", fmt.Errorf("invalid provider policy at %q", p)
		}
	default:
		return "", fmt.Errorf("unknown bind role %q", b.Role)
	}
	for _, core := range layout.CoreRows {
		// The overlay may be nested in its designated mount, which may
		// itself be nested in a core row (e.g. the config under the home).
		if core == parent || below(parent, core) {
			continue
		}
		if overlap(p, core) {
			return "", fmt.Errorf("%q overlaps core row %q", p, core)
		}
	}
	return p, nil
}

func (s Sandbox) validate(layout BindLayout) error {
	if len(s.Binds) > 12 {
		return errors.New("more than 12 bind rows")
	}
	if len(s.Binds) != 0 {
		if !cleanInside(layout.Config) || !path.IsAbs(layout.Config) || !cleanInside(layout.Tree) || !path.IsAbs(layout.Tree) ||
			overlap(layout.Config, layout.Tree) || len(layout.CoreRows) == 0 {
			return errors.New("bind validation needs a core layout")
		}
		for _, core := range layout.CoreRows {
			if !path.IsAbs(core) || !cleanInside(core) {
				return fmt.Errorf("invalid core row %q", core)
			}
		}
	}
	destinations := make(map[string]Bind)
	for _, row := range s.Binds {
		p, err := row.destination(layout)
		if err != nil {
			return err
		}
		for other := range destinations {
			if overlap(other, p) {
				return fmt.Errorf("bind %q overlaps %q", p, other)
			}
		}
		destinations[p] = row
	}
	if s.Launch != nil {
		return s.Launch.validate(layout, destinations)
	}
	return nil
}

type Agent struct {
	Name       string `json:"name"`
	Builtin    bool   `json:"builtin,omitempty"`
	Definition string `json:"definition,omitempty"` // absolute inside path of a rendered definition
}

func (a Agent) validSource(layout BindLayout, destinations map[string]Bind) bool {
	if a.Builtin {
		return a.Definition == ""
	}
	if !path.IsAbs(a.Definition) || !cleanInside(a.Definition) {
		return false
	}
	for p, row := range destinations {
		if row.Optional || (row.Role != ConfigOverlay && row.Role != TreeOverlay) {
			continue
		}
		// A writable intermediate folder could be renamed and replaced.
		if parent := path.Dir(p); parent != layout.Config && parent != layout.Tree {
			continue
		}
		if (row.Kind == "file" && a.Definition == p) || (row.Kind == "folder" && below(a.Definition, p)) {
			return true
		}
	}
	return false
}
