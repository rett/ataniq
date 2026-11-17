package execconfig

// Where a rule comes from.
const (
	// SourceDesign marks the rules the design requires (section 3.9).
	SourceDesign = "design"
	// SourceFeature marks the rules the lead-mirror feature scope adds.
	SourceFeature = "feature"
	// SourceRelative marks close relatives of the rules above, added here.
	SourceRelative = "relative"
	// SourceProject marks a project addition made with Set.With.
	SourceProject = "project"
)

// ProjectReason is the reason every project addition carries.
const ProjectReason = "project addition"

func rules(source string, pairs [][2]string) []Rule {
	out := make([]Rule, len(pairs))
	for i, p := range pairs {
		out[i] = Rule{Pattern: p[0], Reason: p[1], Source: source}
	}
	return out
}

// Design returns the rules the design lists as mandatory. The caller gets a
// new slice each time.
func Design() []Rule {
	return rules(SourceDesign, [][2]string{
		{".claude/", "agent settings and hooks"},
		{".codex/", "agent settings and hooks"},
		{".mcp.json", "MCP servers start as programs"},
		{".envrc", "runs on direnv allow"},
		{".vscode/", "editor tasks and settings run programs"},
		{".idea/", "editor run configurations run programs"},
		{".githooks/", "git hooks run on git commands"},
		{"Makefile", "build entry point"},
		{"scripts/", "scripts the build and the tools run"},
		{".gitattributes", "git filters and drivers run programs"},
		{".gitmodules", "submodules fetch and run other code"},
		{"go.mod", "selects the Go toolchain and dependencies"},
	})
}

// Scope returns the rules the feature scope adds to the design list. The
// caller gets a new slice each time. The folder ".githooks/" is in the
// design list and is not repeated here.
func Scope() []Rule {
	return rules(SourceFeature, [][2]string{
		{".git", "git config and hooks run programs"},
		{".husky/", "git hooks run on git commands"},
		{".pre-commit-config.yaml", "git hooks run on git commands"},
		{"lefthook.yml", "git hooks run on git commands"},
		{".agents/", "agent settings and skills"},
		{".github/", "CI workflows and actions"},
		{".gitlab-ci.yml", "CI pipeline"},
		{".circleci/", "CI pipeline"},
		{".woodpecker/", "CI pipeline"},
		{".woodpecker.yml", "CI pipeline"},
		{"Jenkinsfile", "CI pipeline"},
	})
}

// Relatives returns the rules added here as close relatives of the two
// lists above. The caller gets a new slice each time.
//
// "makefile" is not a separate rule: matching ignores letter case, so
// "Makefile" already covers it. ".dockerignore" and ".editorconfig" are not
// included, because they select no program to run.
func Relatives() []Rule {
	return rules(SourceRelative, [][2]string{
		// Agent folders and instruction files an unsandboxed agent loads.
		{".cursor/", "agent settings and rules"},
		{".gemini/", "agent settings and hooks"},
		{".aider.conf.yml", "agent settings"},
		{"CLAUDE.md", "agent instructions"},
		{"CLAUDE.local.md", "agent instructions"},
		{"AGENTS.md", "agent instructions"},
		{"AGENTS.override.md", "agent instructions"},
		{"GEMINI.md", "agent instructions"},
		{".cursorrules", "agent instructions"},
		// Other make and task entry points.
		{"GNUmakefile", "build entry point"},
		{"*.mk", "included by make"},
		{"Justfile", "task entry point"},
		{"Taskfile.yml", "task entry point"},
		{"Taskfile.yaml", "task entry point"},
		{"magefile.go", "task entry point"},
		{"lefthook.yaml", "git hooks run on git commands"},
		// Toolchain and dependency selection.
		{"go.work", "selects Go modules and toolchain"},
		{"go.sum", "selects dependency contents"},
		{"go.work.sum", "selects dependency contents"},
		{".go-version", "selects the Go toolchain"},
		{".tool-versions", "selects tool versions"},
		{".mise.toml", "selects tools and runs tasks"},
		{"mise.toml", "selects tools and runs tasks"},
		{".npmrc", "package manager settings and scripts"},
		{"package.json", "package scripts and dependencies"},
		{".devcontainer/", "container setup commands"},
		{".devcontainer.json", "container setup commands"},
		{"devcontainer.json", "container setup commands"},
		{"Dockerfile", "container build commands"},
		// Environment files.
		{".env", "environment of every command"},
		{".env.*", "environment of every command"},
	})
}
