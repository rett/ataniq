// Package launchpolicy embeds the claude policy and its captured help.
package launchpolicy

import "embed"

//go:embed 2.1.332.json 2.1.332.help.txt
var files embed.FS

// Files returns the read-only policy documents for this pack.
func Files() embed.FS { return files }
