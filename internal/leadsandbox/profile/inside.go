package profile

// Inside paths: the same for every epoch and both runtimes, because providers
// key their state by path (the lead-hosting design, section 3.3).
const (
	HomeInside        = "/home/lead"
	WorkRootInside    = "/work"
	ProbeInside       = "/ataniq/probe"
	SpecDirInside     = "/ataniq/spec"
	SpecFileName      = "spec.json"
	AgentInside       = "/ataniq/agent"
	QargiInside       = "/ataniq/qargi"
	MirrorRootInside  = "/ataniq/mirror"
	AssignmentInside  = "/ataniq/assignment"
	ProjectionInside  = "/ataniq/projection"
	GitIdentityInside = "/ataniq/git/config"
	RequestsInside    = "/ataniq/requests"
	MailboxInside     = "/ataniq/mailbox"
	MailInside        = "/ataniq/mail"
	CoordInside       = "/coord"
	TmpInside         = "/tmp"
	ResolvConfInside  = "/etc/resolv.conf"

	// ClaudeManagedInside is the managed-settings path of claude. The
	// lead-hosting design names no path for codex.
	ClaudeManagedInside = "/etc/claude-code/managed-settings.json"
)

// SpecFileInside is the probe spec file inside.
const SpecFileInside = SpecDirInside + "/" + SpecFileName

// TreeInside is the lead's work tree inside: /work/<project>.
func TreeInside(project string) string { return WorkRootInside + "/" + project }

// MirrorInside is the exchange mirror inside: /ataniq/mirror/<project>.git.
func MirrorInside(project string) string { return MirrorRootInside + "/" + project + ".git" }

// ConfigInside is the runtime's config folder inside: /home/lead/.claude or
// /home/lead/.codex. It is "" for an invalid runtime.
func ConfigInside(rt Runtime) string {
	if !rt.Valid() {
		return ""
	}
	return HomeInside + "/" + rt.ConfigDirName()
}

// SkillsInside is the lead skills folder inside the runtime's config folder.
func SkillsInside(rt Runtime) string {
	if !rt.Valid() {
		return ""
	}
	return ConfigInside(rt) + "/" + SkillsDirName
}

// InstructionInside is the user-level instruction file inside the runtime's
// config folder.
func InstructionInside(rt Runtime) string {
	if !rt.Valid() {
		return ""
	}
	return ConfigInside(rt) + "/" + rt.InstructionFileName()
}

// ManagedSettingsDefaultInside is the default managed-settings path inside.
// Only claude has one; for codex it is "" and the caller must give a path.
func ManagedSettingsDefaultInside(rt Runtime) string {
	if rt == Claude {
		return ClaudeManagedInside
	}
	return ""
}

// GitReadOnlyRel are the paths of the work tree's .git that are bound
// read-only over the tree, relative to the tree.
func GitReadOnlyRel() []string {
	return []string{".git/config", ".git/hooks", ".git/info"}
}
