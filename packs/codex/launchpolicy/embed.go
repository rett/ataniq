// Package launchpolicy embeds the codex policy and its captured help.
package launchpolicy

import "embed"

//go:embed 0.172.0.json 0.172.0.help.txt
var files embed.FS

// Files returns the read-only policy documents for this pack.
func Files() embed.FS { return files }
