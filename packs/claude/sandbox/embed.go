// Package sandbox carries the reviewed sandbox declaration and file templates.
package sandbox

import "embed"

//go:embed declaration.json *.tmpl
var files embed.FS

// Files returns the read-only sandbox data, keyed by exact build.
func Files() embed.FS { return files }
