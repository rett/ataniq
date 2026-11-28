package claude

import (
	"bytes"
	"io/fs"

	"ataniq/packs/claude/sandbox"
)

// SandboxData supplies the full pack's declaration inputs and read-only templates.
func (Pack) SandboxData() (manifest, hooks []byte, templates fs.FS) {
	return bytes.Clone(declaration), bytes.Clone(hookRows), sandbox.Files()
}
