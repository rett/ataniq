//go:build linux && ataniq_canary

package leadcanary

import (
	"errors"
	"fmt"
	"strings"

	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/profile"
)

// ErrNotCanaryRole is wrapped by every refusal of a role that is not the lead
// of a project of the reserved kind asked for (a throwaway project, or a
// scratch project for [NamesFor] and [RoleOfFor] with leadrole.ReservedScratch),
// and of a layout name that maps to none.
var ErrNotCanaryRole = errors.New("leadcanary: not the lead of a throwaway project")

// layoutSuffix follows the project in the layout and intake role.
const layoutSuffix = "-lead"

// Names maps the role of canary mode, the lead of a throwaway project, to the
// two names that other parts use. layoutRole is the role of the layout and the
// intake, "<project>-lead" (throwaway-one/lead gives throwaway-one-lead), and
// unit is the systemd unit name, role.UnitName() (ataniq-lead-throwaway-one.service).
// It refuses any role that is not a lead (the supervisor, a coordinator, the
// zero Role), a lead whose project does not carry [leadrole.ThrowawayPrefix],
// and a layout name longer than 63 bytes, the layout's own bound. The error
// wraps [ErrNotCanaryRole] for the first two. It is [NamesFor] for
// leadrole.ReservedThrowaway.
func Names(role leadrole.Role) (layoutRole, unit string, err error) {
	return NamesFor(leadrole.ReservedThrowaway, role)
}

// NamesFor is [Names] for the reserved kind k: the lead's project must carry
// the prefix of that kind (leadrole.ThrowawayPrefix or leadrole.ScratchPrefix).
// The layout and unit names follow the same rule for both kinds. A value of k
// that is not a kind refuses every role.
func NamesFor(k leadrole.Reserved, role leadrole.Role) (layoutRole, unit string, err error) {
	if role.Kind() != leadrole.KindLead || !leadrole.IsReserved(role.Project(), k) {
		return "", "", notRole(k)
	}
	layoutRole = role.Project() + layoutSuffix
	if !profile.ValidName(layoutRole) {
		return "", "", fmt.Errorf("leadcanary: the layout name of the role is longer than %d bytes", leadrole.MaxProjectLen)
	}
	return layoutRole, role.UnitName(), nil
}

// notRole is the refusal of a role or a layout name that is not of kind k. The
// throwaway kind keeps the text of [ErrNotCanaryRole] as it is.
func notRole(k leadrole.Reserved) error {
	if k == leadrole.ReservedThrowaway {
		return fmt.Errorf("%w", ErrNotCanaryRole)
	}
	return fmt.Errorf("%w: the reserved kind is %s", ErrNotCanaryRole, k)
}

// RoleOf is the inverse of [Names]: it maps a layout role such as
// "throwaway-one-lead" to the role "throwaway-one/lead". It refuses a name
// that [Names] would not give for any role: one without the "-lead" ending,
// one whose project is not throwaway, and one that is not a valid layout name.
// The error wraps [ErrNotCanaryRole]. It is [RoleOfFor] for
// leadrole.ReservedThrowaway.
func RoleOf(layoutRole string) (leadrole.Role, error) {
	return RoleOfFor(leadrole.ReservedThrowaway, layoutRole)
}

// RoleOfFor is [RoleOf] for the reserved kind k, the inverse of [NamesFor].
func RoleOfFor(k leadrole.Reserved, layoutRole string) (leadrole.Role, error) {
	project, ok := strings.CutSuffix(layoutRole, layoutSuffix)
	if !ok || !profile.ValidName(layoutRole) || !leadrole.IsReserved(project, k) {
		return leadrole.Role{}, notRole(k)
	}
	role, err := leadrole.Lead(project)
	if err != nil {
		return leadrole.Role{}, fmt.Errorf("%w: %w", ErrNotCanaryRole, err)
	}
	return role, nil
}
