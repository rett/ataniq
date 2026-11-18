package mergebuild

import (
	"fmt"
	"strings"
	"time"

	"ataniq/internal/approval/record"
)

// Identity is who the merge commit names as its author and its committer.
// The host reads it from the identity file Ataniq owns; it is never taken
// from a commit, a branch or a request.
type Identity struct {
	// Name is the person's name. It is 1 to 256 bytes and holds no "<", no
	// ">" and no control character (so no newline and no NUL).
	Name string
	// Email is the address, under the same rule as Name.
	Email string
}

// Inputs are the values a merge commit is made of. [Build] writes a commit
// from them and [Check] recomputes one, so the same Inputs give the same
// commit, byte for byte, whenever and wherever they are used.
type Inputs struct {
	// TargetTip is the commit the target branch points at, as a full object
	// id of the repository's object format. It becomes the first parent.
	TargetTip string
	// FeatureTip is the commit of the feature that is merged, as a full
	// object id. It becomes the second parent. It is a different commit
	// from TargetTip.
	FeatureTip string
	// Target is the name of the target branch as the message shows it, for
	// example "develop": 1 to 200 bytes of ASCII letters, digits, ".", "_",
	// "/" and "-", without "..".
	Target string
	// Feature is the feature's name, without "feature/" before it. It
	// passes [record.ValidFeatureName].
	Feature string
	// RequestID is the id of the request the merge answers. It passes
	// [record.ValidRequestID] and stands in the message's trailer.
	RequestID string
	// Identity is the author and the committer.
	Identity Identity
	// At is the time the host collected the request. Both dates of the
	// commit are this time, to the second, written in UTC; the clock is
	// never read for them. It lies between 1970 and the end of the year
	// 9999.
	At time.Time
}

const (
	// maxTargetBytes is the longest target branch name.
	maxTargetBytes = 200
	// maxIdentityBytes is the longest name and the longest address.
	maxIdentityBytes = 256
	// maxKeyBytes is the longest key of a pin.
	maxKeyBytes = 64
	// maxUnix is the last second of the year 9999, the last time a record
	// can state.
	maxUnix = 253402300799
)

// refuse returns a refusal of a value. The text names the value by what it
// is and never repeats it.
func refuse(what string) error {
	return fmt.Errorf("%w: %s", ErrValue, what)
}

// check refuses Inputs with a value that must not enter a commit's text.
// It reads no repository; that an id has the length of the repository's
// object format, and names a commit, is checked by the caller.
func (in Inputs) check() error {
	switch {
	case !record.ValidObjectID(in.TargetTip):
		return refuse("the target's tip is not a full object id")
	case !record.ValidObjectID(in.FeatureTip):
		return refuse("the feature's tip is not a full object id")
	case in.TargetTip == in.FeatureTip:
		return refuse("the feature's tip is the target's tip, so there is nothing to merge")
	case !validTarget(in.Target):
		return refuse("the target branch name is not 1 to 200 ASCII letters, digits, dots, underscores, slashes and dashes without two dots in a row")
	case !record.ValidFeatureName(in.Feature):
		return refuse("the feature name is not 1 to 48 lower-case letters, digits and dashes")
	case !record.ValidRequestID(in.RequestID):
		return refuse("the request id is not rq- and 1 to 48 lower-case letters, digits and dashes")
	case !validIdentityPart(in.Identity.Name):
		return refuse("the identity's name is empty, too long, or holds an angle bracket or a control character")
	case !validIdentityPart(in.Identity.Email):
		return refuse("the identity's address is empty, too long, or holds an angle bracket or a control character")
	case in.At.Unix() < 0 || in.At.Unix() > maxUnix:
		return refuse("the collected time is before 1970 or after the year 9999")
	}
	return nil
}

// validTarget is the rule for the target branch name.
func validTarget(s string) bool {
	if len(s) < 1 || len(s) > maxTargetBytes || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '/', c == '-':
		default:
			return false
		}
	}
	return true
}

// validIdentityPart is the rule for a name and for an address. A newline
// would start a header line of its own and an angle bracket would end the
// name or the address early; the other control characters have no place in
// either.
func validIdentityPart(s string) bool {
	if len(s) < 1 || len(s) > maxIdentityBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || c == '<' || c == '>' {
			return false
		}
	}
	return true
}

// ValidKey reports whether s can be the key of a pin: 1 to 64 lower-case
// ASCII letters, digits and dashes. The host makes the key itself; it is
// never the request id or anything else a lead chose.
func ValidKey(s string) bool {
	if len(s) < 1 || len(s) > maxKeyBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// checkKey refuses a key outside [ValidKey].
func checkKey(key string) error {
	if !ValidKey(key) {
		return refuse("the key of the pin is not 1 to 64 lower-case letters, digits and dashes")
	}
	return nil
}
