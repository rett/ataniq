//go:build linux && ataniq_canary

package provision

import (
	"fmt"
	"path"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/standin"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/profile"
)

// The names of what provisioning makes in the lead's own folder, beside the
// folders of the layout. Each is a source of the bind table that the layout
// does not name, so the sandbox inputs file must name these paths ([Paths],
// [CheckInputs]).
const (
	// SkillsName is the empty folder bound over the skills folder of the
	// config folder.
	SkillsName = "skills"
	// InstructionsName is the empty instruction file.
	InstructionsName = "instructions.md"
	// GitIdentityName is the folder that holds the git identity file and
	// nothing else; the file is profile.GitIdentityFileName.
	GitIdentityName = "git-identity"
	// QargiInstallName is the folder bound as the Qargi install. It holds
	// only the guard hook's executable, as GuardHookName.
	QargiInstallName = "qargi-install"
	// GuardHookName is the name of the guard hook's executable in the Qargi
	// install folder; inside the sandbox it is
	// profile.QargiInside + "/" + GuardHookName.
	GuardHookName = "guard-hook"
	// AssignmentName and ProjectionName are the empty folders bound as the
	// assignment and projection folders.
	AssignmentName = "assignment"
	ProjectionName = "projection"
	// ManagedSettingsName is the copy of the managed-settings file.
	ManagedSettingsName = "managed-settings.json"
)

// The inside path of the Qargi records that provisioning prepares the folders
// for. The records folder is the layout's own whatever this says; the value
// only has to be a valid inside path that is not in the home, so that no
// mount point is made for it.
const recordsInside = "/ataniq/records"

// Paths are the host paths that provisioning makes, from the config alone.
// The run kit reads them to write the sandbox inputs file (the sources of the
// bind table that the layout does not name), and [CheckInputs] compares a
// file with them. Every path is absolute and clean.
type Paths struct {
	// Layout is the host layout of the role under the state root, and
	// Runtime the epoch's runtime, from agent.runtime.
	Layout  profile.Layout
	Runtime profile.Runtime

	StateRoot   string
	RuntimeRoot string
	// Marker is the marker file of the state root, written last.
	Marker string

	// The layout's own folders. Holder is the holder's state folder
	// (--state-dir) and stays empty until the first start.
	LeadsDir     string
	LeadDir      string
	Holder       string
	Home         string
	ConfigRoot   string
	ConfigDir    string
	Tree         string
	GitMeta      string
	Records      string
	Requests     string
	Mail         string
	MailboxesDir string
	Mailbox      string
	PublishedDir string
	Published    string
	MirrorsDir   string
	Mirror       string

	// The sources the layout does not name.
	LeadSkills      string
	InstructionFile string
	GitIdentityDir  string
	GitIdentityFile string
	QargiInstall    string
	GuardHook       string
	AssignmentDir   string
	ProjectionDir   string
	ManagedSettings string

	// RuntimeLeads is <runtime root>/leads and RuntimeFolder the holder's
	// runtime folder <runtime root>/leads/<layout role> (--runtime-dir),
	// which must exist with mode 0700 before the holder starts.
	RuntimeLeads  string
	RuntimeFolder string

	// ReplayDir is <home>/canary-replay, where the stand-in finds its
	// screens; ReplaySwitch is the switch file in it, which only
	// [ReplayOn] writes.
	ReplayDir    string
	ReplaySwitch string
}

// PathsOf works out every path of the config. It fails when the role is not
// the canary role, the layout is not valid or agent.runtime is not a runtime
// the sandbox profile knows, and does not look at the file system.
func PathsOf(cfg leadcanary.Config) (Paths, error) {
	l, err := cfg.Layout()
	if err != nil {
		return Paths{}, fmt.Errorf("provision: %w", err)
	}
	rt, err := profile.ParseRuntime(cfg.Agent.Runtime)
	if err != nil {
		return Paths{}, fmt.Errorf("provision: agent.runtime: %w", err)
	}
	cfgDir, err := l.ConfigDir(rt)
	if err != nil {
		return Paths{}, fmt.Errorf("provision: %w", err)
	}
	rtFolder, err := cfg.HolderRuntimeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("provision: %w", err)
	}
	lead := l.LeadDir()
	replay := path.Join(l.Home(), standin.ReplayDir)
	return Paths{
		Layout: l, Runtime: rt,
		StateRoot: cfg.StateRoot, RuntimeRoot: cfg.RuntimeRoot,
		Marker:   path.Join(cfg.StateRoot, canaryroot.MarkerName),
		LeadsDir: path.Join(l.StateRoot, "leads"), LeadDir: lead, Holder: l.Holder(),
		Home: l.Home(), ConfigRoot: l.ConfigRoot(), ConfigDir: cfgDir, Tree: l.Tree(),
		GitMeta: l.GitMeta(), Records: l.QargiRecords(), Requests: l.Requests(), Mail: l.Mail(),
		MailboxesDir: l.MailboxesDir(), Mailbox: l.Mailbox(),
		PublishedDir: l.PublishedDir(), Published: l.Published(),
		MirrorsDir: l.MirrorsDir(), Mirror: l.Mirror(),

		LeadSkills:      path.Join(lead, SkillsName),
		InstructionFile: path.Join(lead, InstructionsName),
		GitIdentityDir:  path.Join(lead, GitIdentityName),
		GitIdentityFile: path.Join(lead, GitIdentityName, profile.GitIdentityFileName),
		QargiInstall:    path.Join(lead, QargiInstallName),
		GuardHook:       path.Join(lead, QargiInstallName, GuardHookName),
		AssignmentDir:   path.Join(lead, AssignmentName),
		ProjectionDir:   path.Join(lead, ProjectionName),
		ManagedSettings: path.Join(lead, ManagedSettingsName),

		RuntimeLeads:  path.Join(cfg.RuntimeRoot, "leads"),
		RuntimeFolder: rtFolder,

		ReplayDir:    replay,
		ReplaySwitch: path.Join(replay, standin.SwitchFile),
	}, nil
}

// stateRel gives abs relative to the state root, for a lookup beneath its
// handle.
func (p Paths) stateRel(abs string) string {
	return abs[len(p.StateRoot)+1:]
}
