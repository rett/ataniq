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
// never-mounted paths and the other runtime's config folder. It also refuses
// a source in or above another lead's state or another project's mirror
// folder, as the bind table does for its own sources, so a path that did not
// come from the table (the real path of a host row, which the plan binds) is
// held to the same rules. The inputs must be valid.
func (in Inputs) CheckSource(source string) error {
	if err := CheckNeverMounted(source, in.neverMounted()); err != nil {
		return err
	}
	if why := in.otherStateReason(source); why != "" {
		return refuse("bind source", "%s %s", source, why)
	}
	return nil
}

// otherStateReason says why a source lies where another lead's state or
// another project's mirror folder is (in or above it), or is "" when it does
// not. The lead's own state folder and the project's own mirror are the
// places a source may lie in.
func (in Inputs) otherStateReason(source string) string {
	l := in.Layout()
	if leads := path.Join(l.StateRoot, "leads"); related(source, leads) && source != l.LeadDir() && !below(source, l.LeadDir()) {
		return "lies in or above another lead's state"
	}
	if mirrors := l.MirrorsDir(); related(source, mirrors) && source != l.Mirror() && !below(source, l.Mirror()) {
		return "lies in or above another project's mirror folder"
	}
	return ""
}
