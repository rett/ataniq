//go:build linux && ataniq_canary

package leadcanary

import (
	"context"
	"errors"
	"fmt"

	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// What canary mode says it does not do. The evidence carries both on its
// define and start lines (see [Evidence]).
const (
	// QualificationText is the "qualification" field: there is no
	// qualification record, and the start declares the waiver instead.
	QualificationText = "none (canary waiver)"
	// LoginText is the "login" field: nothing checks that a login is present.
	LoginText = "not checked"
)

// ErrPreflightRefused is wrapped by every refusal of the preflight. Its text is
// fixed: it names no role and repeats no value.
var ErrPreflightRefused = errors.New("leadcanary: the preflight passes only for the canary role, the lead of a throwaway project")

// Preflight is the preflight of canary mode. It satisfies
// leadlifecycle.Preflight. It passes for one role, the config's, whose project
// carries the throwaway prefix, and refuses every other. It checks no
// qualification record and no login, and records nothing: the evidence lines
// say so ([QualificationText], [LoginText]).
type Preflight struct {
	role leadrole.Role
}

var _ leadlifecycle.Preflight = (*Preflight)(nil)

// NewPreflight returns the preflight of the canary role. It fails when the
// role is not the lead of a throwaway project ([Names] refuses it).
func NewPreflight(role leadrole.Role) (*Preflight, error) {
	if _, _, err := Names(role); err != nil {
		return nil, err
	}
	return &Preflight{role: role}, nil
}

// Check passes when role and the registry's record are both the preflight's
// role, whose project carries the throwaway prefix, and refuses anything else
// with an error that wraps [ErrPreflightRefused].
func (p *Preflight) Check(_ context.Context, role leadrole.Role, entry leadregistry.Entry) error {
	if p == nil || p.role.IsZero() || role != p.role || entry.Role != p.role ||
		!leadrole.IsThrowaway(role.Project()) {
		return fmt.Errorf("%w", ErrPreflightRefused)
	}
	return nil
}
