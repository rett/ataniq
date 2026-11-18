//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"fmt"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadsandbox/profile"
)

// placeholderRoot stands in the profile inputs below for the host sources
// that provisioning does not make and that do not decide which mount points
// there are: the agent install, the probe, the resolv.conf and the spec
// folder. The profile only checks them as paths here; nothing is read.
const placeholderRoot = "/nonexistent/ataniq-provision"

// profileInputs builds the profile inputs that the mount point work needs. The
// mount points and the git metadata folder depend on the layout, the runtime
// and the placeholders in the tree, and on where the Qargi records appear
// inside; the sources that provisioning does not make are placeholders (see
// placeholderRoot). There are no extra placeholders in the tree: the git
// metadata folder lists none, and [CheckInputs] refuses an inputs file that
// names one.
func profileInputs(cfg leadcanary.Config, p Paths, settingsSHA256 string) profile.Inputs {
	return profile.Inputs{
		StateRoot: p.Layout.StateRoot, Role: p.Layout.Role, Project: p.Layout.Project,
		Runtime: p.Runtime, RuntimeVersion: cfg.Agent.RuntimeVersion,
		Identity: profile.IdentityRoot,
		GitMeta:  p.GitMeta,

		AgentInstall: placeholderRoot + "/agent", ProbeBinary: placeholderRoot + "/probe",
		SpecDir:         placeholderRoot + "/spec",
		ManagedSettings: p.ManagedSettings, ManagedSettingsSHA256: settingsSHA256,
		LeadSkills: p.LeadSkills, InstructionFile: p.InstructionFile,
		GitIdentityDir: p.GitIdentityDir, QargiInstall: p.QargiInstall,
		QargiRecordsInside: recordsInside,
		AssignmentDir:      p.AssignmentDir, ProjectionDir: p.ProjectionDir,
		ResolvConf:   placeholderRoot + "/resolv.conf",
		NeverMounted: []string{placeholderRoot + "/never"},
		Term:         "xterm",
	}
}

// ErrInputs is wrapped by every refusal of [CheckInputs] that is about the
// content of a valid inputs file and not about reading it.
var ErrInputs = errors.New("provision: the sandbox inputs do not match the config")

// CheckInputs reads the sandbox inputs file at file with
// sandboxstart.ReadInputs, which holds it to its own rules (the caller's, mode
// 0600, strict keys, bounds), and checks it against the config: canary mode
// is on, and the project, the state and runtime roots, the layout role, the
// runtime and its version are the config's. The sources that the layout does
// not name must be the folders and files that provisioning made (see
// [PathsOf]), and no extra placeholder may be named, because the git metadata
// folder lists none. The run kit and the daemon call it before a start.
//
// Every refusal names the field and never repeats a value of the file. A
// refusal of ReadInputs is returned as it is (an *sandboxstart.InputsError); a
// mismatch wraps [ErrInputs].
func CheckInputs(cfg leadcanary.Config, file string) error {
	in, err := sandboxstart.ReadInputs(file)
	if err != nil {
		return err
	}
	return checkInputs(cfg, in)
}

func checkInputs(cfg leadcanary.Config, in sandboxstart.Inputs) error {
	p, err := PathsOf(cfg)
	if err != nil {
		return err
	}
	bad := func(field string) error { return fmt.Errorf("%w: %s", ErrInputs, field) }
	for _, c := range []struct {
		field string
		ok    bool
	}{
		{"canary", in.Canary},
		{"project", in.Project == p.Layout.Project},
		{"layout_role", in.LayoutRole == p.Layout.Role},
		{"state_root", in.StateRoot == cfg.StateRoot},
		{"runtime_root", in.RuntimeRoot == cfg.RuntimeRoot},
		{"runtime", in.Runtime == cfg.Agent.Runtime},
		{"runtime_version", in.RuntimeVersion == cfg.Agent.RuntimeVersion},
		{"sources.managed_settings", in.Sources.ManagedSettings == p.ManagedSettings},
		{"sources.lead_skills", in.Sources.LeadSkills == p.LeadSkills},
		{"sources.instruction_file", in.Sources.InstructionFile == p.InstructionFile},
		{"sources.git_identity_dir", in.Sources.GitIdentityDir == p.GitIdentityDir},
		{"sources.qargi_install", in.Sources.QargiInstall == p.QargiInstall},
		{"sources.assignment_dir", in.Sources.AssignmentDir == p.AssignmentDir},
		{"sources.projection_dir", in.Sources.ProjectionDir == p.ProjectionDir},
		{"sources.extra_placeholders", len(in.Sources.ExtraPlaceholders) == 0},
	} {
		if !c.ok {
			return bad(c.field)
		}
	}
	return nil
}
