//go:build linux && ataniq_canary

package sandboxstart

import (
	"ataniq/internal/leadsandbox/profile"
)

// layout is the host layout of the lead.
func (in Inputs) layout() profile.Layout {
	return profile.Layout{StateRoot: in.StateRoot, Role: in.LayoutRole, Project: in.Project}
}

// profileInputs builds the profile's inputs from the file's values. The two
// files the sequence writes, resolv.conf and the spec folder, are the places
// of the bound folder in the runtime folder of the role; the git metadata
// folder is the layout's own, as the profile requires. uid and gid are the
// calling user's, for the "owner" identity.
func (in Inputs) profileInputs(uid, gid uint32) (profile.Inputs, error) {
	rt, err := profile.ParseRuntime(in.Runtime)
	if err != nil {
		return profile.Inputs{}, err
	}
	pin := profile.Inputs{
		StateRoot: in.StateRoot, Role: in.LayoutRole, Project: in.Project,
		Runtime: rt, RuntimeVersion: in.RuntimeVersion,
		Identity: profile.IdentityRoot,
		GitMeta:  in.layout().GitMeta(),

		AgentInstall: in.Sources.AgentInstall, ProbeBinary: in.Executables.Probe,
		SpecDir:         boundSpecDir(in.RuntimeRoot, in.LayoutRole),
		ManagedSettings: in.Sources.ManagedSettings, ManagedSettingsInside: in.Sources.ManagedSettingsInside,
		ManagedSettingsSHA256: in.Sources.ManagedSettingsSHA256,
		LeadSkills:            in.Sources.LeadSkills, InstructionFile: in.Sources.InstructionFile,
		GitIdentityDir: in.Sources.GitIdentityDir, QargiInstall: in.Sources.QargiInstall,
		QargiRecordsInside: in.Sources.QargiRecordsInside,
		AssignmentDir:      in.Sources.AssignmentDir, ProjectionDir: in.Sources.ProjectionDir,
		ResolvConf:   boundResolv(in.RuntimeRoot, in.LayoutRole),
		NeverMounted: append([]string(nil), in.NeverMounted...),
		Term:         in.Terminal.Term, ColorTerm: in.Terminal.ColorTerm, Lang: in.Terminal.Lang, TZ: in.Terminal.TZ,
		ExtraEnv: append([]profile.EnvVar(nil), in.AgentEnv...),
	}
	if in.Identity == string(profile.IdentityOwner) {
		pin.Identity = profile.IdentityOwner
		pin.OwnerUID, pin.OwnerGID, pin.OwnerUser = uid, gid, in.OwnerUser
	}
	if s := in.Sources.Seccomp; s != nil {
		pin.SeccompFile, pin.SeccompSHA256 = s.File, s.SHA256
	}
	for _, p := range in.Sources.ExtraPlaceholders {
		pin.ExtraPlaceholders = append(pin.ExtraPlaceholders, profile.Placeholder{Rel: p.Rel, Source: p.Source, Dir: p.Dir})
	}
	return pin, nil
}
