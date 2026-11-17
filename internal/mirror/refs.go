package mirror

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ataniq/internal/exchange/gitenv"
)

var (
	// ErrNoRef is returned by [Mirror.Resolve] when the mirror has no ref
	// of that exact name.
	ErrNoRef = errors.New("mirror: no such ref")
	// ErrTooManyRefs is returned by a listing when the mirror holds more
	// refs than [Limits.MaxRefs]. No cut list is returned.
	ErrTooManyRefs = errors.New("mirror: more refs than the limit")
	// ErrBadRef is returned by a listing when the mirror holds a ref that
	// does not fit the layout: a name that fails the ref rules or the bounds,
	// a symbolic ref, or an object id or type that is not one git knows. The
	// owner's code writes only valid refs, so this means a hand edit or a
	// bug. The error text does not hold the name.
	ErrBadRef = errors.New("mirror: the mirror holds a ref that does not fit the layout")
)

// Ref is one ref of the mirror.
type Ref struct {
	// Name is the full ref name, raw. It can hold any text a lead chose
	// within the ref rules: the caller must escape it before it shows it.
	Name string
	// ID is the full object id the ref points to.
	ID string
	// Type is the type of that object: commit, tag, tree or blob.
	Type string
}

// recordMax is the output bound per ref: an id, a type, a name and
// separators. A listing's output cap is this times the ref cap.
const recordMax = 2048

// listFormat prints, for each ref, "<id> <type>", the name and the symbolic
// target, each ended by NUL, then the newline that git adds. A ref name holds
// no NUL, so the fields cannot be confused.
const listFormat = "--format=%(objectname) %(objecttype)%00%(refname)%00%(symref)%00"

// list runs for-each-ref over the patterns and returns at most max refs,
// sorted by name. The patterns are fixed prefixes or checked full names:
// they hold none of the characters that for-each-ref reads as a glob.
// accept decides whether a listed name fits the layout.
func (m *Mirror) list(ctx context.Context, patterns []string, max int, accept func(name string) bool) ([]Ref, error) {
	args := append([]string{"for-each-ref", listFormat, "--count=" + strconv.Itoa(max)}, patterns...)
	out, err := runGit(ctx, m.git, gitenv.Cmd{GitDir: m.path, Args: args}, nil,
		m.limits.timeout(), int64(max)*recordMax)
	if err != nil {
		return nil, err
	}
	return parseRefs(out, accept)
}

// parseRefs reads the output of for-each-ref with [listFormat].
func parseRefs(out []byte, accept func(name string) bool) ([]Ref, error) {
	text, ok := strings.CutSuffix(string(out), "\n")
	if !ok && len(out) != 0 {
		return nil, ErrBadRef
	}
	if text == "" {
		return nil, nil
	}
	fields := strings.Split(text, "\x00")
	// Each record has three fields, each ended by NUL, so the split ends
	// with an empty string. The newline of a record comes before the next
	// record's first field.
	if len(fields)%3 != 1 || fields[len(fields)-1] != "" {
		return nil, ErrBadRef
	}
	var refs []Ref
	for i := 0; i+2 < len(fields); i += 3 {
		head := fields[i]
		if i > 0 {
			var cut bool
			if head, cut = strings.CutPrefix(head, "\n"); !cut {
				return nil, ErrBadRef
			}
		}
		id, typ, found := strings.Cut(head, " ")
		name, symref := fields[i+1], fields[i+2]
		if !found || !gitenv.IsObjectID(id) || !knownType(typ) || symref != "" || !accept(name) {
			return nil, ErrBadRef
		}
		refs = append(refs, Ref{Name: name, ID: id, Type: typ})
	}
	slices.SortFunc(refs, func(a, b Ref) int { return strings.Compare(a.Name, b.Name) })
	return refs, nil
}

func knownType(t string) bool {
	switch t {
	case "commit", "tag", "tree", "blob":
		return true
	}
	return false
}

// upstreamName reports whether name is a full name in the upstream namespace
// that passes the ref rules and the bounds.
func upstreamName(name string) bool {
	if rest, ok := strings.CutPrefix(name, headsPrefix); ok {
		return validShort(headsPrefix, rest)
	}
	if rest, ok := strings.CutPrefix(name, tagsPrefix); ok {
		return validShort(tagsPrefix, rest)
	}
	return false
}

// leadName reports whether name is a full name in a lead's namespace.
func leadName(name string) bool {
	_, _, ok := SplitLeadRef(name)
	return ok
}

// limited lists with the ref cap and turns a list that is longer than the cap
// into [ErrTooManyRefs].
func (m *Mirror) limited(ctx context.Context, patterns []string, accept func(string) bool) ([]Ref, error) {
	max := m.limits.maxRefs()
	refs, err := m.list(ctx, patterns, max+1, accept)
	if err != nil {
		return nil, err
	}
	if len(refs) > max {
		return nil, ErrTooManyRefs
	}
	return refs, nil
}

// Upstream lists the upstream refs: refs/heads/* and refs/tags/*, sorted by
// name, with full names. More refs than the cap is [ErrTooManyRefs].
func (m *Mirror) Upstream(ctx context.Context) ([]Ref, error) {
	return m.limited(ctx, []string{headsPrefix, tagsPrefix}, upstreamName)
}

// LeadBranches lists the branches of one lead, refs/leads/<role>/heads/*,
// sorted by name, with full names ([SplitLeadRef] gets the branch name). The
// role must pass [ValidName]. A lead without refs has an empty list. More refs
// than the cap is [ErrTooManyRefs].
func (m *Mirror) LeadBranches(ctx context.Context, role string) ([]Ref, error) {
	prefix, err := LeadPrefix(role)
	if err != nil {
		return nil, err
	}
	return m.limited(ctx, []string{prefix}, func(name string) bool {
		r, _, ok := SplitLeadRef(name)
		return ok && r == role
	})
}

// Roles lists the roles that have at least one lead ref, sorted. The refs of
// all leads count against the cap: more than the cap is [ErrTooManyRefs].
func (m *Mirror) Roles(ctx context.Context) ([]string, error) {
	refs, err := m.limited(ctx, []string{leadsPrefix}, leadName)
	if err != nil {
		return nil, err
	}
	// The refs come sorted by full name, in which the role is followed by a
	// slash. A role that continues with a dash ("a-b") sorts before the role
	// ("a") because a dash sorts before a slash, so the roles are sorted
	// again here, by themselves.
	var roles []string
	for _, r := range refs {
		role, _, _ := SplitLeadRef(r.Name)
		roles = append(roles, role)
	}
	slices.Sort(roles)
	return slices.Compact(roles), nil
}

// Resolve returns the ref with exactly this full name: refs/heads/<branch>,
// refs/tags/<tag> or refs/leads/<role>/heads/<branch>. It does no search: a
// short name such as "develop", a name in another namespace or a name that
// fails the ref rules is [ErrNoRef], and so is a name of which the mirror holds
// only longer names below it.
func (m *Mirror) Resolve(ctx context.Context, ref string) (Ref, error) {
	if !upstreamName(ref) && !leadName(ref) {
		return Ref{}, fmt.Errorf("%w: not a full name of the mirror's layout", ErrNoRef)
	}
	// A pattern matches the name and every name below it. The name sorts
	// before those, so the first ref is the one asked for if it exists.
	refs, err := m.list(ctx, []string{ref}, 1, func(name string) bool { return upstreamName(name) || leadName(name) })
	if err != nil {
		return Ref{}, err
	}
	if len(refs) == 0 || refs[0].Name != ref {
		return Ref{}, ErrNoRef
	}
	return refs[0], nil
}
