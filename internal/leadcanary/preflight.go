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
// carries the reserved prefix of its kind (the throwaway prefix for the canary,
// the scratch prefix for a scratch lead), and refuses every other. It checks no
// qualification record and no login, and records nothing: the evidence lines
// say so ([QualificationText], [LoginText]).
type Preflight struct {
	role leadrole.Role
	kind leadrole.Reserved
}

var _ leadlifecycle.Preflight = (*Preflight)(nil)

// NewPreflight returns the preflight of the canary role. It fails when the
// role is not the lead of a throwaway project ([Names] refuses it). It is
// [NewPreflightFor] for leadrole.ReservedThrowaway.
func NewPreflight(role leadrole.Role) (*Preflight, error) {
	return NewPreflightFor(leadrole.ReservedThrowaway, role)
}

// NewPreflightFor returns the preflight of a role whose project carries the
// reserved prefix of kind k. It fails when the role is not the lead of such a
// project ([NamesFor] refuses it).
func NewPreflightFor(k leadrole.Reserved, role leadrole.Role) (*Preflight, error) {
	if _, _, err := NamesFor(k, role); err != nil {
		return nil, err
	}
	return &Preflight{role: role, kind: k}, nil
}

// Check passes when role and the registry's record are both the preflight's
// role, whose project carries the reserved prefix of the preflight's kind, and
// refuses anything else with an error that wraps [ErrPreflightRefused].
func (p *Preflight) Check(_ context.Context, role leadrole.Role, entry leadregistry.Entry) error {
	if p == nil || p.role.IsZero() || role != p.role || entry.Role != p.role ||
		!leadrole.IsReserved(role.Project(), p.kind) {
		return fmt.Errorf("%w", ErrPreflightRefused)
	}
	return nil
}
