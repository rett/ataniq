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
// of a throwaway project, and of a layout name that maps to none.
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
// wraps [ErrNotCanaryRole] for the first two.
func Names(role leadrole.Role) (layoutRole, unit string, err error) {
	if role.Kind() != leadrole.KindLead || !leadrole.IsThrowaway(role.Project()) {
		return "", "", fmt.Errorf("%w", ErrNotCanaryRole)
	}
	layoutRole = role.Project() + layoutSuffix
	if !profile.ValidName(layoutRole) {
		return "", "", fmt.Errorf("leadcanary: the layout name of the role is longer than %d bytes", leadrole.MaxProjectLen)
	}
	return layoutRole, role.UnitName(), nil
}

// RoleOf is the inverse of [Names]: it maps a layout role such as
// "throwaway-one-lead" to the role "throwaway-one/lead". It refuses a name
// that [Names] would not give for any role: one without the "-lead" ending,
// one whose project is not throwaway, and one that is not a valid layout name.
// The error wraps [ErrNotCanaryRole].
func RoleOf(layoutRole string) (leadrole.Role, error) {
	project, ok := strings.CutSuffix(layoutRole, layoutSuffix)
	if !ok || !profile.ValidName(layoutRole) || !leadrole.IsThrowaway(project) {
		return leadrole.Role{}, fmt.Errorf("%w", ErrNotCanaryRole)
	}
	role, err := leadrole.Lead(project)
	if err != nil {
		return leadrole.Role{}, fmt.Errorf("%w: %w", ErrNotCanaryRole, err)
	}
	return role, nil
}
