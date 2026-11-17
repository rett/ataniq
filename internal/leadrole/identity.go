package leadrole

import (
	"fmt"
	"strconv"
	"strings"
)

// identityLead follows the project in an identity: the parent lead and the
// start of its epoch.
const identityLead = "-lead@e"

// Identity returns the identity of one instance of a coordinator:
// "<project>-lead@e<parentEpoch>:coord:<feature>:<n>", where parentEpoch is
// the epoch of the parent lead at launch and n is the instance number of the
// coordinator. Both are plain decimal numbers. The identity is only an
// identifier: never use it as a path or a ref component, and compare it whole
// (or through ParseIdentity, segment by segment), never by prefix. Every role
// that is not a coordinator gives "".
func (r Role) Identity(parentEpoch, n uint64) string {
	if r.kind != KindCoordinator {
		return ""
	}
	return r.project + identityLead + strconv.FormatUint(parentEpoch, 10) +
		":" + coordSegment + ":" + r.feature + ":" + strconv.FormatUint(n, 10)
}

// ParseIdentity reads an identity written by Role.Identity. It is strict: the
// project and the feature must be valid names, the epoch and the instance
// number plain decimal numbers of at least 1 without sign, leading zero or
// space, and nothing may precede or follow the identity. It returns the
// coordinator role, the parent epoch and the instance number.
func ParseIdentity(s string) (role Role, parentEpoch, n uint64, err error) {
	bad := func(why string) (Role, uint64, uint64, error) {
		return Role{}, 0, 0, fmt.Errorf("%w: identity: %s", ErrInvalid, why)
	}
	// The project is the part before the first "-lead@e"; a project cannot
	// contain "@", so the cut is unambiguous.
	at := strings.IndexByte(s, '@')
	if at < 0 {
		return bad("no \"@\"")
	}
	project, ok := strings.CutSuffix(s[:at], "-lead")
	if !ok || !strings.HasPrefix(s[at:], "@e") {
		return bad("want \"<project>-lead@e<parent epoch>:coord:<feature>:<n>\"")
	}
	segs := strings.Split(s[at+2:], ":")
	if len(segs) != 4 || segs[1] != coordSegment {
		return bad("want \"<project>-lead@e<parent epoch>:coord:<feature>:<n>\"")
	}
	if parentEpoch, ok = decimal(segs[0]); !ok {
		return bad("parent epoch is not a plain decimal number of at least 1")
	}
	if n, ok = decimal(segs[3]); !ok {
		return bad("instance number is not a plain decimal number of at least 1")
	}
	role, err = Coordinator(project, segs[2])
	if err != nil {
		return Role{}, 0, 0, err
	}
	return role, parentEpoch, n, nil
}

// decimal reads a canonical decimal uint64 of at least 1.
func decimal(s string) (uint64, bool) {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
