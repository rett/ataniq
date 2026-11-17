package profile

import "path"

// DirMode is the mode of every folder of the host layout.
const DirMode = 0o700

// Layout is the host layout of one lead of one project under a state root
// (the lead-hosting design, section 3.3):
//
//	<state>/leads/<role>/holder
//	<state>/leads/<role>/home
//	<state>/leads/<role>/config/<runtime>
//	<state>/leads/<role>/tree
//	<state>/leads/<role>/gitmeta
//	<state>/leads/<role>/qargi
//	<state>/leads/<role>/requests
//	<state>/leads/<role>/mail
//	<state>/mailbox/<role>
//	<state>/pub/<project>
//	<state>/mirrors/<project>.git
//
// <state>/leads/<role> itself is Ataniq's and is never bound, and neither is
// holder, the holder's own state. home, the config folder of the runtime,
// tree, qargi, requests and mail are bound read-write, each as a whole, so the
// lead writes them. gitmeta is Ataniq's copy of the tree's protected git files
// (see GitMeta); qargi is the lead's working Qargi records (see
// QargiRecords). The mailbox and the published folder lie
// outside the lead's folder and are bound read-only. The coordinators' trees
// and homes (CoordsDir) and their shared login (CoordLoginDir) lie under the
// same state root and are never bound into a lead's sandbox. Every folder has
// mode DirMode and lies outside every repository. The methods build strings
// only; call Validate first.
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

// Holder is the folder of the holder's own state for the lead (its sockets
// and what it records): <state>/leads/<role>/holder. It is never bound: no
// bind source may lie in or above it.
func (l Layout) Holder() string { return path.Join(l.LeadDir(), "holder") }

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

// The entries of the folder GitMeta names: a file and two folders, one for
// each path of GitReadOnlyRel.
const (
	GitMetaConfig = "config"
	GitMetaHooks  = "hooks"
	GitMetaInfo   = "info"
)

// GitMeta is the host folder of Ataniq's copy of the tree's protected git
// files: <state>/leads/<role>/gitmeta, with config (a file), hooks/ and info/
// inside. It lies outside the home, the config folder and the tree, so the
// lead cannot write it. Inputs.GitMeta must be this path.
func (l Layout) GitMeta() string { return path.Join(l.LeadDir(), "gitmeta") }

// QargiRecords is the host folder of the lead's working Qargi records:
// <state>/leads/<role>/qargi. Every lead has it, bound read-write at the
// inside path the inputs give (Inputs.QargiRecordsInside). It lies in the
// lead's own state folder like the other folders the lead writes, so that the
// profile's rules about another lead's state cover it. It is no input: a
// caller cannot name another folder.
func (l Layout) QargiRecords() string { return path.Join(l.LeadDir(), "qargi") }

// Requests is the host folder bound read-write at RequestsInside:
// <state>/leads/<role>/requests. In this version it is only a bound folder:
// nothing in Ataniq reads it.
func (l Layout) Requests() string { return path.Join(l.LeadDir(), "requests") }

// Mail is the host folder bound read-write at MailInside:
// <state>/leads/<role>/mail, with one subfolder per feature.
func (l Layout) Mail() string { return path.Join(l.LeadDir(), "mail") }

// MailboxesDir is the folder that holds one mailbox per role: <state>/mailbox.
func (l Layout) MailboxesDir() string { return path.Join(l.StateRoot, "mailbox") }

// Mailbox is the lead's mailbox on the host, <state>/mailbox/<role>, bound
// read-only at MailboxInside. It is Ataniq's: the lead does not write it.
func (l Layout) Mailbox() string { return path.Join(l.MailboxesDir(), l.Role) }

// PublishedDir is the folder that holds one published folder per project:
// <state>/pub.
func (l Layout) PublishedDir() string { return path.Join(l.StateRoot, "pub") }

// Published is the folder the project's coordinators publish under,
// <state>/pub/<project>, bound read-only at CoordInside. It holds only
// <feature>/qargi, <feature>/out and <feature>/archive.
func (l Layout) Published() string { return path.Join(l.PublishedDir(), l.Project) }

// CoordsDir is the folder of the coordinators' trees and homes:
// <state>/coords, with <project>/<feature>/tree and <project>/<feature>/home
// below it. Nothing in it is bound into a lead's sandbox.
func (l Layout) CoordsDir() string { return path.Join(l.StateRoot, "coords") }

// CoordLoginDir is the folder of the coordinators' shared logins:
// <state>/coord-login, with <project>/<runtime> below it. Nothing in it is
// bound into a lead's sandbox.
func (l Layout) CoordLoginDir() string { return path.Join(l.StateRoot, "coord-login") }

// GitMetaSource is the source, below the folder gitMeta, of one path of
// GitReadOnlyRel: the entry of the folder with the same last element.
func GitMetaSource(gitMeta, rel string) string { return path.Join(gitMeta, path.Base(rel)) }
