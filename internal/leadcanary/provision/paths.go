//go:build linux && ataniq_canary

package provision

import (
	"fmt"
	"path"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/standin"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadrole"
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

// RecordsInside is the inside path of the Qargi records that provisioning
// prepares the mount points for. The records folder is the layout's own
// whatever this says, but the mount points depend on it: a path in the home
// would need a mount point there, and this one, outside the home, needs none.
// The sandbox inputs file must name it as sources.qargi_records_inside, and
// [CheckInputs] refuses a file that names another.
const RecordsInside = "/ataniq/records"

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

	// RolesDir is <state root>/roles, which holds the role locks, and RoleDir
	// the folder of the lead's own lock, <state root>/roles/<project>: the
	// holder takes RoleLock in it with the flag that makes the file and not
	// the folder, so RoleDir must be there before the holder starts. RoleLock
	// is the lock file itself, which the holder makes at its first start;
	// provisioning does not.
	RolesDir string
	RoleDir  string
	RoleLock string

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

	// For a scratch lead only (empty for the canary's): ScratchRoot is
	// <state root>/scratch, which holds one folder per scratch lead,
	// ScratchDir the lead's own folder <state root>/scratch/<layout role>
	// (the sandbox inputs file and the record that provisioning is done lie in
	// it, outside the lead's folder so that the lead's sandbox never sees
	// them), SandboxInputs the inputs file in it and LeadRecord the record.
	ScratchRoot   string
	ScratchDir    string
	SandboxInputs string
	LeadRecord    string
}

// PathsOf works out every path of the config. It fails when the role is not
// the role of the config's reserved kind, the layout is not valid or
// agent.runtime is not a runtime the sandbox profile knows, and does not look
// at the file system.
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
	roleLock := cfg.Role.LockPath(cfg.StateRoot)
	roleDir := path.Dir(roleLock)
	replay := path.Join(l.Home(), standin.ReplayDir)
	p := Paths{
		Layout: l, Runtime: rt,
		StateRoot: cfg.StateRoot, RuntimeRoot: cfg.RuntimeRoot,
		Marker:   path.Join(cfg.StateRoot, canaryroot.MarkerName),
		LeadsDir: path.Join(l.StateRoot, "leads"), LeadDir: lead, Holder: l.Holder(),
		Home: l.Home(), ConfigRoot: l.ConfigRoot(), ConfigDir: cfgDir, Tree: l.Tree(),
		GitMeta: l.GitMeta(), Records: l.QargiRecords(), Requests: l.Requests(), Mail: l.Mail(),
		MailboxesDir: l.MailboxesDir(), Mailbox: l.Mailbox(),
		PublishedDir: l.PublishedDir(), Published: l.Published(),
		MirrorsDir: l.MirrorsDir(), Mirror: l.Mirror(),
		RolesDir: path.Dir(roleDir), RoleDir: roleDir, RoleLock: roleLock,

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
	}
	if cfg.Reserved == leadrole.ReservedScratch {
		p.ScratchRoot = path.Join(cfg.StateRoot, leadcanary.ScratchDirName)
		p.ScratchDir = path.Join(p.ScratchRoot, l.Role)
		p.SandboxInputs = path.Join(p.ScratchDir, leadcanary.SandboxInputsName)
		p.LeadRecord = path.Join(p.ScratchDir, LeadRecordName)
	}
	return p, nil
}

// stateRel gives abs relative to the state root, for a lookup beneath its
// handle.
func (p Paths) stateRel(abs string) string {
	return abs[len(p.StateRoot)+1:]
}
