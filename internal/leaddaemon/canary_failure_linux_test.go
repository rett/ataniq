//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
)

// TestCanaryFailureCodes: a refusal of the registry or of the lifecycle
// answers refused, what could not be done or is not known answers unavailable,
// and the answer never carries the error's own text.
func TestCanaryFailureCodes(t *testing.T) {
	cause := errors.New("sample cause /sample/path")
	for name, tc := range map[string]struct {
		err  error
		code clientapi.ErrorCode
		msg  string
	}{
		// The errors as the lifecycle makes them.
		"a foreign unit at a start or a stop": {
			fmt.Errorf("%w: %w: %s", leadlifecycle.ErrUnitLoaded, leadlifecycle.ErrForeignUnit, "sample unit"),
			CodeRefused, msgForeignUnit,
		},
		"a foreign unit that holds the name": {
			fmt.Errorf("%w: %w", fmt.Errorf("%w: %w", leadlifecycle.ErrStartFailed, cause), leadlifecycle.ErrForeignUnit),
			CodeRefused, msgForeignUnit,
		},
		"a loaded unit of ours": {
			fmt.Errorf("%w: %s", leadlifecycle.ErrUnitLoaded, "sample unit"),
			CodeRefused, "a unit with the role's name is loaded",
		},
		// The registry's refusals.
		"a stale epoch":         {leadregistry.ErrStaleEpoch, CodeRefused, msgRegistryNo},
		"a wrapped stale epoch": {fmt.Errorf("sample: %w", leadregistry.ErrStaleEpoch), CodeRefused, msgRegistryNo},
		"a state change":        {fmt.Errorf("sample: %w", leadregistry.ErrStateTransition), CodeRefused, msgRegistryNo},
		"an exhausted epoch":    {leadregistry.ErrEpochExhausted, CodeRefused, msgRegistryNo},
		"invalid input":         {leadregistry.ErrInvalid, CodeRefused, msgRegistryNo},
		"no role":               {leadregistry.ErrNoRole, CodeRefused, "the role is not defined"},
		// What is not known stays unavailable, a refusal of the registry beside
		// it or not.
		"a stop that is not proven": {
			errors.Join(fmt.Errorf("%w: %w", leadlifecycle.ErrStopUnproven, cause), leadregistry.ErrStateTransition),
			CodeUnavailable, "the stop is not proven; the role is shown unknown",
		},
		"a start whose outcome is not known": {
			errors.Join(leadlifecycle.ErrLaunchUncertain, leadregistry.ErrStaleEpoch),
			CodeUnavailable, "the start's outcome is not known; the role is shown unknown",
		},
		"a start that failed": {
			fmt.Errorf("%w: %w", leadlifecycle.ErrStartFailed, cause),
			CodeUnavailable, "the start failed and nothing was started",
		},
		"the daemon stops":   {fmt.Errorf("sample: %w", context.Canceled), CodeUnavailable, msgStopping},
		"anything else":      {cause, CodeUnavailable, msgFailed},
		"a registry failure": {leadregistry.ErrBroken, CodeUnavailable, msgFailed},
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			x := &canaryExt{logf: func(format string, a ...any) { fmt.Fprintf(&log, format, a...) }}
			got := x.failure("start", tc.err)
			if got == nil || got.Code != tc.code || got.Message != tc.msg {
				t.Fatalf("answer %+v, want %s %q", got, tc.code, tc.msg)
			}
			if strings.Contains(got.Message, "sample") {
				t.Fatalf("the error's text is in the answer %q", got.Message)
			}
			// The error itself is the log's.
			if !strings.Contains(log.String(), "canary start: ") {
				t.Fatalf("log %q", log.String())
			}
		})
	}
}
