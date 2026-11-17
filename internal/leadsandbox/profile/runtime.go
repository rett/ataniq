package profile

// The per-runtime configuration of this package (the names below, the
// per-runtime inside paths in inside.go and the managed-settings path in
// inputs.go) is existing configuration that is to move into provider packs
// (owner decision PQ1, after 0.0.10). Add no new runtime-specific branch here.

// Runtime is the agent runtime of an epoch.
type Runtime string

// The two runtimes.
const (
	Claude Runtime = "claude"
	Codex  Runtime = "codex"
)

// Runtimes returns both runtimes, claude first.
func Runtimes() []Runtime { return []Runtime{Claude, Codex} }

// Valid reports whether r is one of the two runtimes.
func (r Runtime) Valid() bool { return r == Claude || r == Codex }

// Other returns the other runtime, or "" when r is not valid.
func (r Runtime) Other() Runtime {
	switch r {
	case Claude:
		return Codex
	case Codex:
		return Claude
	}
	return ""
}

// ParseRuntime returns the runtime named s.
func ParseRuntime(s string) (Runtime, error) {
	r := Runtime(s)
	if !r.Valid() {
		return "", refuse("runtime", "must be %q or %q", Claude, Codex)
	}
	return r, nil
}

// ConfigDirName is the name of the runtime's config folder in the home
// (".claude" or ".codex"), or "" for an invalid runtime.
func (r Runtime) ConfigDirName() string {
	switch r {
	case Claude:
		return ".claude"
	case Codex:
		return ".codex"
	}
	return ""
}

// ConfigEnvName is the environment variable that names the runtime's config
// folder ("CLAUDE_CONFIG_DIR" or "CODEX_HOME"), or "" for an invalid runtime.
func (r Runtime) ConfigEnvName() string {
	switch r {
	case Claude:
		return "CLAUDE_CONFIG_DIR"
	case Codex:
		return "CODEX_HOME"
	}
	return ""
}

// InstructionFileName is the name of the user-level instruction file in the
// config folder ("CLAUDE.md" or "AGENTS.md"), or "" for an invalid runtime.
func (r Runtime) InstructionFileName() string {
	switch r {
	case Claude:
		return "CLAUDE.md"
	case Codex:
		return "AGENTS.md"
	}
	return ""
}

// SkillsDirName is the name of the lead skills folder in the config folder.
const SkillsDirName = "skills"
