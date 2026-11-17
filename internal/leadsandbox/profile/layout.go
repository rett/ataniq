package profile

import "path"

// DirMode is the mode of every folder of the host layout.
const DirMode = 0o700

// Layout is the host layout of one lead of one project under a state root:
//
//	<state>/leads/<role>/home
//	<state>/leads/<role>/config/<runtime>
//	<state>/leads/<role>/tree
//	<state>/mirrors/<project>.git
//
// Every folder has mode DirMode and lies outside every repository. The
// methods build strings only; call Validate first.
type Layout struct {
	StateRoot string
	Role      string
	Project   string
}

// Validate refuses a relative or unclean state root and a role or project
// name that is not ValidName.
func (l Layout) Validate() error {
	if err := CheckPath("state root", l.StateRoot); err != nil {
		return err
	}
	if l.StateRoot == "/" {
		return refuse("state root", "must not be the file system root")
	}
	if err := CheckName("role", l.Role); err != nil {
		return err
	}
	return CheckName("project", l.Project)
}

// LeadDir is <state>/leads/<role>.
func (l Layout) LeadDir() string { return path.Join(l.StateRoot, "leads", l.Role) }

// Home is the lead's home on the host.
func (l Layout) Home() string { return path.Join(l.LeadDir(), "home") }

// ConfigRoot is the folder that holds one config folder per runtime.
func (l Layout) ConfigRoot() string { return path.Join(l.LeadDir(), "config") }

// ConfigDir is the lead's config folder for one runtime on the host.
func (l Layout) ConfigDir(rt Runtime) (string, error) {
	if !rt.Valid() {
		return "", refuse("runtime", "must be %q or %q", Claude, Codex)
	}
	return path.Join(l.ConfigRoot(), string(rt)), nil
}

// Tree is the lead's own clone on the host.
func (l Layout) Tree() string { return path.Join(l.LeadDir(), "tree") }

// MirrorsDir is the folder that holds the exchange mirrors.
func (l Layout) MirrorsDir() string { return path.Join(l.StateRoot, "mirrors") }

// Mirror is the exchange mirror of the project on the host.
func (l Layout) Mirror() string { return path.Join(l.MirrorsDir(), l.Project+".git") }
