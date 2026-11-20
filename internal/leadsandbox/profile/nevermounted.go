package profile

import (
	"path"
	"strconv"
)

// neverMounted is the caller's list plus the other runtime's config folder.
func (in Inputs) neverMounted() []string {
	never := append([]string(nil), in.NeverMounted...)
	if other := in.OtherConfigDir(); other != "" {
		never = append(never, other)
	}
	return never
}

// CheckNeverMounted refuses a bind source that is equal to, below or above any
// path of never. The comparison is lexical: both sides must be clean absolute
// paths with their symbolic links resolved by the caller. It fails closed: it
// refuses an empty list, and an entry that is not a clean absolute path
// (CheckPath), because such an entry would protect nothing.
func CheckNeverMounted(source string, never []string) error {
	if len(never) == 0 {
		return refuse("never-mounted paths", "none given: an empty list protects nothing")
	}
	if err := CheckPath("bind source", source); err != nil {
		return err
	}
	for i, n := range never {
		if err := CheckPath("never-mounted path "+strconv.Itoa(i), n); err != nil {
			return err
		}
	}
	for _, n := range never {
		switch {
		case source == n:
			return refuse("bind source", "%s is a never-mounted path", source)
		case below(source, n):
			return refuse("bind source", "%s lies below the never-mounted path %s", source, n)
		case below(n, source):
			return refuse("bind source", "%s lies above the never-mounted path %s", source, n)
		}
	}
	return nil
}

// CheckSource is CheckNeverMounted with the list of the inputs: the caller's
// never-mounted paths and the other runtime's config folder. It also holds the
// source to the rules about other state ([StateFolders.Reason]), as the bind
// table does for its own sources, so a path that did not come from the table
// (the real path of a host row, which the plan binds) is held to the same
// rules. Such a path is no row of the layout, so the lead's own mailbox and
// the project's own published folder are refused too; [Inputs.CheckRowSource]
// is the check for the source of a row. The inputs must be valid.
func (in Inputs) CheckSource(source string) error {
	return in.CheckRowSource(source, RoleNone)
}

// CheckRowSource is CheckSource for the source of a row with the given source
// role: the lead's own mailbox is accepted as the source of the mailbox row
// ([RoleMailbox]) and the project's own published folder as the source of the
// coordinator row ([RolePublished]), and nowhere else. The inputs must be
// valid.
func (in Inputs) CheckRowSource(source, role string) error {
	if err := CheckNeverMounted(source, in.neverMounted()); err != nil {
		return err
	}
	if why := in.Layout().StateFolders().Reason(source, role); why != "" {
		return refuse("bind source", "%s %s", source, why)
	}
	return nil
}

// StateFolders are the folders under a state root that the rules about other
// state name, for one lead of one project. [Layout.StateFolders] gives them as
// the layout names them; a caller that has evaluated symbolic links gives
// their real paths in the same fields (the mountpoints package does), so the
// same rules run on real paths. Every field must be a clean absolute path.
type StateFolders struct {
	// Leads is the folder of every lead, <state>/leads; Lead is this lead's
	// own, <state>/leads/<role>; Holder is the holder's state for this lead,
	// <state>/leads/<role>/holder.
	Leads, Lead, Holder string
	// Mirrors is the folder of the exchange mirrors, <state>/mirrors; Mirror
	// is the project's own, <state>/mirrors/<project>.git.
	Mirrors, Mirror string
	// Mailboxes is the folder of every role's mailbox, <state>/mailbox;
	// Mailbox is this lead's own, <state>/mailbox/<role>.
	Mailboxes, Mailbox string
	// PublishedDir is the folder of what the coordinators of every project
	// publish, <state>/pub; Published is the project's own,
	// <state>/pub/<project>.
	PublishedDir, Published string
	// Coords is the folder of every coordinator's tree and home,
	// <state>/coords; CoordLogin is the folder of the coordinators' shared
	// logins, <state>/coord-login.
	Coords, CoordLogin string
}

// StateFolders are the folders the rules about other state name, as the
// layout names them.
func (l Layout) StateFolders() StateFolders {
	return StateFolders{
		Leads: path.Join(l.StateRoot, "leads"), Lead: l.LeadDir(), Holder: l.Holder(),
		Mirrors: l.MirrorsDir(), Mirror: l.Mirror(),
		Mailboxes: l.MailboxesDir(), Mailbox: l.Mailbox(),
		PublishedDir: l.PublishedDir(), Published: l.Published(),
		Coords: l.CoordsDir(), CoordLogin: l.CoordLoginDir(),
	}
}

// Paths returns pointers to every field, in a fixed order, for a caller that
// replaces each folder with its real path.
func (f *StateFolders) Paths() []*string {
	return []*string{
		&f.Leads, &f.Lead, &f.Holder, &f.Mirrors, &f.Mirror, &f.Mailboxes, &f.Mailbox,
		&f.PublishedDir, &f.Published, &f.Coords, &f.CoordLogin,
	}
}

// Reason says why a bind source with the given source role may not lie where
// it does, or is "" when no rule about other state refuses it. No source may
// lie in or above:
//
//   - another lead's state (its login, its home, its tree). The lead's own
//     state folder is where its own sources may lie;
//   - the lead's own holder folder, which is never bound;
//   - another project's mirror. The project's own mirror, and what lies in
//     it, is allowed;
//   - a coordinator's tree or home (anything in Coords) or the coordinators'
//     login (anything in CoordLogin);
//   - another role's mailbox, or another project's published folder.
//
// The lead's own mailbox and the project's own published folder are each the
// source of exactly one row: a source may be that folder only with the role of
// that row ([RoleMailbox], [RolePublished]), and no source may lie inside it.
// A path that is no row of the table has the role [RoleNone], and is refused
// there too. source must be a clean absolute path.
func (f StateFolders) Reason(source, role string) string {
	switch {
	case related(source, f.Leads) && source != f.Lead && !below(source, f.Lead):
		return "lies in or above another lead's state"
	case related(source, f.Holder):
		return "lies in or above the lead's holder folder, which is never bound"
	case related(source, f.Mirrors) && source != f.Mirror && !below(source, f.Mirror):
		return "lies in or above another project's mirror folder"
	case related(source, f.Coords):
		return "lies in or above a coordinator's tree or home, which are never bound"
	case related(source, f.CoordLogin):
		return "lies in or above the coordinators' login, which is never bound"
	}
	if related(source, f.Mailboxes) {
		switch {
		case source == f.Mailbox && role == RoleMailbox:
		case source == f.Mailbox || below(source, f.Mailbox):
			return "lies in the lead's own mailbox, which only the row " + MailboxInside + " binds, as a whole"
		default:
			return "lies in or above another role's mailbox"
		}
	}
	if related(source, f.PublishedDir) {
		switch {
		case source == f.Published && role == RolePublished:
		case source == f.Published || below(source, f.Published):
			return "lies in the project's own published folder, which only the row " + CoordInside + " binds, as a whole"
		default:
			return "lies in or above another project's published folder"
		}
	}
	return ""
}
