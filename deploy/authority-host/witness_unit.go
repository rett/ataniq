// Package authorityhost provides the unit template shipped with the release tools.
package authorityhost

import _ "embed"

//go:embed witness/etc/systemd/system/ataniq-witness.service
var witnessUnit string

// WitnessUnit returns the trusted witness unit before release path substitution.
func WitnessUnit() string { return witnessUnit }
