//go:build linux && ataniq_canary

package provision

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
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
		QargiRecordsInside: RecordsInside,
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
// [PathsOf]), the Qargi records must appear inside at [RecordsInside], which
// the mount points were made for, and no extra placeholder may be named,
// because the git metadata folder lists none. It then makes what the holder
// makes of the file at the start of its sequence (the validate step), as far
// as that needs no launch and no host access: the same spec trial, on the
// inputs as the holder sets them from the config (the agent's command line
// and environment, the pinned digest), so that every refusal of that step
// that the file alone decides is a refusal here. For the runtime claude the
// file must also name the managed-settings marker (sources.managed_marker),
// and the managed-settings file that sources.managed_settings names must
// hold that text, which assertion 8 searches for inside. The ataniq-canary
// command calls it (its check-inputs and profile-digest verbs, which the run
// kit uses before a start); the daemon does not.
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
		{"sources.qargi_records_inside", in.Sources.QargiRecordsInside == RecordsInside},
		{"sources.extra_placeholders", len(in.Sources.ExtraPlaceholders) == 0},
		// The spec refuses a claude start without it (assert.Spec.checkManaged).
		{"sources.managed_marker", in.Runtime != string(profile.Claude) || in.Sources.ManagedMarker != ""},
	} {
		if !c.ok {
			return bad(c.field)
		}
	}
	if err := checkLiveAgentFile(cfg, in); err != nil {
		return err
	}
	if err := checkTrial(cfg, in); err != nil {
		return err
	}
	return checkMarkerInSettings(cfg, in)
}

// checkLiveAgentFile holds a live agent's command to the bind: agent.argv[0]
// must be a file under /ataniq/agent (the agent's bind inside), and the file of
// that name must exist in sources.agent_install as a regular file that is not a
// link, so that the run's version step, which strips the bind's name from
// argv[0] and runs the file in the install folder, runs the very agent that
// starts. The stand-in's tests name no such file, so only a live agent is held
// to it. The refusal names the field and never a path.
func checkLiveAgentFile(cfg leadcanary.Config, in sandboxstart.Inputs) error {
	if cfg.Agent.Kind != leadcanary.AgentLive {
		return nil
	}
	rest, ok := strings.CutPrefix(cfg.Agent.Argv[0], profile.AgentInside+"/")
	if !ok || rest == "" {
		return fmt.Errorf("%w: agent.argv[0] (not under %s)", ErrInputs, profile.AgentInside)
	}
	fi, err := os.Lstat(filepath.Join(in.Sources.AgentInstall, rest))
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: agent.argv[0] (no regular file of that name in sources.agent_install)", ErrInputs)
	}
	return nil
}

// The names of the holder's two sockets in its runtime folder, as the serving
// package names them (serve.AttachSocket and serve.ControlSocket). They are
// kept here because provision does not import the serving package, which pulls
// in the session pane and the rest of the holder's serving code; a test pins
// the two names to the serving package's constants.
const (
	attachSocket  = "attach.sock"
	controlSocket = "control.sock"
)

// holderSocketPaths are the two sockets the holder serves in the runtime
// folder of the inputs' layout, and the recording's folder when the lead
// records its screens. The holder adds them to the paths the spec checks, and
// so to the spec's list of mount substrings, whose size is limited: leaving
// them out would let a long never_mounted list through that the holder then
// refuses.
func holderSocketPaths(runtimeRoot, layoutRole string, record bool) []string {
	rt := sandboxstart.RuntimeFolder(runtimeRoot, layoutRole)
	out := []string{filepath.Join(rt, attachSocket), filepath.Join(rt, controlSocket)}
	if record {
		out = append(out, filepath.Join(rt, leadcanary.RecordDirName))
	}
	return out
}

// checkTrial runs the spec trial of the holder's validate step on the inputs
// with the fields the holder sets from the config (cmd/ataniq-holder: the
// agent's command line and environment and the pinned digest) and its two
// socket paths and, for a lead that records, the recording's folder, which
// come from the inputs' runtime folder (see [holderSocketPaths]). The refusal names the field of the profile or the
// spec, or says the text of the fixed refusals of that step that are not about
// a field; it never holds a value of the file.
func checkTrial(cfg leadcanary.Config, in sandboxstart.Inputs) error {
	in.AgentArgv = append([]string(nil), cfg.Agent.Argv...)
	in.AgentEnv = sandboxstart.AgentEnvOf(cfg.Agent.Env)
	in.ExpectedDigest = cfg.Holder.ProfileDigest
	in.HolderPaths = holderSocketPaths(in.RuntimeRoot, in.LayoutRole, cfg.RecordScreens)
	err := sandboxstart.TrialValidate(in, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err == nil {
		return nil
	}
	var ie *sandboxstart.InputsError
	var pr *profile.Refusal
	var ar *assert.Refusal
	switch {
	case errors.As(err, &ie) && ie.Field != "":
		return fmt.Errorf("%w: %s (refused by the holder's validate step)", ErrInputs, ie.Field)
	case errors.As(err, &pr):
		return fmt.Errorf("%w: %s (refused by the holder's validate step)", ErrInputs, pr.Field)
	case errors.As(err, &ar):
		return fmt.Errorf("%w: %s (refused by the holder's validate step)", ErrInputs, ar.Field)
	}
	// The other refusals of the step are fixed texts that hold no value.
	return fmt.Errorf("%w: %v (refused by the holder's validate step)", ErrInputs, err)
}

// checkMarkerInSettings checks, for the runtime claude, that the
// managed-settings file the inputs name holds the marker text: assertion 8
// searches the file inside for it, and the file bound there is this one. The
// refusals name the field and never the file's path or content.
//
// The file the inputs name is the lead's provisioned copy, which exists only
// once `ataniq-canary provision` has run. While its folder does not exist yet
// the file it will be copied from, the config's provision.managed_settings, is
// read instead: provisioning copies it unchanged, and its hash is the inputs'
// managed_settings_sha256, so the two cannot differ unseen. Once the folder is
// there the provisioned copy is read, and a copy that is missing, not regular
// or without the marker is refused as before.
func checkMarkerInSettings(cfg leadcanary.Config, in sandboxstart.Inputs) error {
	if in.Runtime != string(profile.Claude) {
		return nil
	}
	bad := func(why string) error {
		return fmt.Errorf("%w: sources.managed_settings %s", ErrInputs, why)
	}
	file := in.Sources.ManagedSettings
	dir, name := filepath.Split(file)
	root, err := safefile.OpenRootNoLinks(filepath.Clean(dir))
	if errors.Is(err, fs.ErrNotExist) {
		// Not provisioned yet: read the file provisioning copies.
		dir, name = filepath.Split(cfg.Provision.ManagedSettings)
		root, err = safefile.OpenRootNoLinks(filepath.Clean(dir))
	}
	if err != nil {
		return bad("cannot be read: its folder cannot be opened")
	}
	defer root.Close()
	data, err := root.ReadFile(name, MaxManagedSettingsBytes)
	if err != nil {
		return bad("cannot be read: it is not a regular file of the bounded size")
	}
	if !bytes.Contains(data, []byte(in.Sources.ManagedMarker)) {
		return bad("does not hold the text of sources.managed_marker, which assertion 8 searches for")
	}
	return nil
}
