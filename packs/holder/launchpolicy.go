package holder

import (
	"embed"

	"ataniq/internal/pack"
	claude "ataniq/packs/claude/launchpolicy"
	codex "ataniq/packs/codex/launchpolicy"
)

// LaunchPolicy returns the embedded policy and help for an exact, resolved
// build. A missing pair yields nil bytes and false. The caller owns the bytes.
func LaunchPolicy(name pack.Name, version string) (policy, help []byte, ok bool) {
	var files embed.FS
	switch {
	case name == "claude" && version == "2.1.332":
		files = claude.Files()
	case name == "codex" && version == "0.172.0":
		files = codex.Files()
	default:
		return nil, nil, false
	}
	policy, err := files.ReadFile(version + ".json")
	if err != nil {
		return nil, nil, false
	}
	help, err = files.ReadFile(version + ".help.txt")
	if err != nil {
		return nil, nil, false
	}
	return policy, help, true
}
