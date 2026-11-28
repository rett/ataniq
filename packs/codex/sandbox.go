package codex

import (
	"bytes"
	"io/fs"

	"ataniq/packs/codex/hook"
	"ataniq/packs/codex/sandbox"
)

// SandboxData supplies the full pack's declaration inputs and read-only templates.
func (Pack) SandboxData() (manifest, hooks []byte, templates fs.FS) {
	return bytes.Clone(declaration), hook.RowsJSON(), sandbox.Files()
}
