//go:build linux && !ataniq_canary

package leaddaemon

import (
	"ataniq/internal/clientapi"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
)

// CanaryConfig is empty in a build without the ataniq_canary tag: there is
// nothing to set, so no caller can put the daemon in canary mode, and this
// build has no code that could act on it. The tagged build defines the same
// name with the canary parts (canary_linux.go).
type CanaryConfig struct{}

// canaryMode is the canary parts of one [Run]. In this build it is always nil
// and every method does nothing.
type canaryMode struct{}

// canarySetup is where [Run] checks the canary config in a tagged build.
func (d *daemon) canarySetup() (*canaryMode, error) { return nil, nil }

func (*canaryMode) openEvidence() error              { return nil }
func (*canaryMode) closeEvidence() error             { return nil }
func (*canaryMode) configure(*leadlifecycle.Options) {}
func (*canaryMode) announce() error                  { return nil }
func (*canaryMode) extensions(*leadlifecycle.Controller, *leadregistry.Registry) []clientapi.Extension {
	return nil
}
