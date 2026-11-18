//go:build linux && amd64

package harness

import "ataniq/internal/leadsandbox/profile"

// InputsForTest gives a test of the package the profile inputs the harness
// derives from a configuration, with the two paths it makes in the attempt
// folder: the tests compare what a run did with what the inputs give.
func InputsForTest(c *Config, resolvConf, specDir string) (profile.Inputs, error) {
	return c.inputs(resolvConf, specDir)
}
