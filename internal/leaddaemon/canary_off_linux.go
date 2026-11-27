//go:build linux && !ataniq_canary

package leaddaemon

import (
	"context"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
)

// CanaryConfig is empty in a build without the ataniq_canary tag: there is
// nothing to set (no Canary and no Scratch field), so no caller can put the
// daemon in canary mode or in scratch mode, and this build has no code that
// could act on either. The tagged build defines the same name with the two
// modes' parts (canary_linux.go).
type CanaryConfig struct{}

// canaryMode is the canary and scratch parts of one [Run]. In this build it is
// always nil and every method does nothing.
type canaryMode struct{}

// canarySetup is where [Run] checks the canary config in a tagged build.
func (d *daemon) canarySetup() (*canaryMode, error) { return nil, nil }

func (*canaryMode) openState() error                 { return nil }
func (*canaryMode) closeEvidence() error             { return nil }
func (*canaryMode) configure(*leadlifecycle.Options) {}
func (*canaryMode) announce() error                  { return nil }
func (*canaryMode) extensions(*leadlifecycle.Controller, *leadregistry.Registry) ([]clientapi.Extension, error) {
	return nil, nil
}

func (*canaryMode) startObservers(context.Context)   {}
func (*canaryMode) joinObservers(time.Duration) bool { return true }
