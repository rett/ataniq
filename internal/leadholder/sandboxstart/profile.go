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

// checkedProfileInputs is the first part of the start's validate step and the
// whole of ProfileDigest: the checks of the file's own values, the profile's
// inputs built from them and the profile's own validation. The order is the
// start's, so a refusal is the same in both.
func (in Inputs) checkedProfileInputs(uid, gid uint32) (profile.Inputs, error) {
	if err := in.checkFields(); err != nil {
		return profile.Inputs{}, err
	}
	pin, err := in.profileInputs(uid, gid)
	if err != nil {
		return profile.Inputs{}, err
	}
	if err := pin.Validate(); err != nil {
		return profile.Inputs{}, err
	}
	return pin, nil
}

// ProfileDigest is the sandbox profile digest of the inputs: the digest the
// plan of Run carries (Started.ProfileDigest) and the one a pin
// (Inputs.ExpectedDigest) is compared with. It is computed from the inputs
// alone, with the same code as the start's validate step, and reads no file
// and starts nothing. The caller sets AgentEnv as the holder does, because the
// extra environment variables are part of the digest. uid and gid are the
// calling user's; the digest does not cover them.
func ProfileDigest(in Inputs, uid, gid uint32) (string, error) {
	pin, err := in.checkedProfileInputs(uid, gid)
	if err != nil {
		return "", err
	}
	return pin.Digest()
}
