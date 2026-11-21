//go:build linux && ataniq_canary

package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/safefile"
	"ataniq/internal/mirror"
)

// Scratch mode provisions a lead of a scratch place: one state root and one
// runtime root, made by the owner's command (ataniq-canary scratch-init),
// that hold the folders of several leads. The steps are the canary's, with
// these differences:
//
//   - the root check is the scratch one: the root marker scratch-root.json, no
//     project in it, must be there (canaryroot.Check), and the canary's
//     marker must not;
//   - the mirror is made here, from the lead's source, when it is not there;
//   - the sandbox inputs file is written here, from the scratch config's
//     sandbox values, and the profile digest is computed here with the
//     holder's own code (sandboxstart.ProfileDigest);
//   - the last act is the record <state root>/scratch/<layout role>/provisioned.json,
//     which holds the digest, and not a marker of the state root.

// LeadRecordName is the name of the record of a provisioned scratch lead, in
// the lead's folder of the state root's scratch folder.
const LeadRecordName = "provisioned.json"

// leadRecordMarker is the "marker" value of the record.
const leadRecordMarker = "ataniq-scratch-lead"

// maxLeadRecordBytes is the longest record that is read.
const maxLeadRecordBytes = 1024

// The refusals of the scratch entry points.
var (
	// ErrScratchKind is wrapped when an entry point is given a config of the
	// other reserved kind: ProvisionScratch takes a scratch lead only and
	// Provision a throwaway one only.
	ErrScratchKind = errors.New("provision: the config is of the other reserved kind")
	// ErrIncomplete is wrapped when the lead's folders are there without the
	// record that a define finished: an earlier define was cut short (the daemon
	// stopped, git ran out of time). The owner removes what it left with
	// "ataniq-canary scratch-clean --config FILE --lead KEY" ([CleanScratch]),
	// and the next define starts again; nothing is removed by a define itself.
	ErrIncomplete = errors.New("provision: an earlier define of this lead was cut short and left its folders")
	// ErrDigestPin is wrapped when the digest the daemon computes is not the
	// profile_digest the owner pinned for the lead in the scratch config.
	ErrDigestPin = errors.New("provision: the profile digest is not the one pinned for the lead")
	// ErrRecord is wrapped when the record of a provisioned lead is not what
	// provisioning writes for it.
	ErrRecord = errors.New("provision: the record of the lead is not the one provisioning writes")
	// ErrProvisioned is wrapped by [CleanScratch] for a lead whose record is
	// there: only a lead whose define was cut short is cleaned.
	ErrProvisioned = errors.New("provision: the lead is provisioned, and only an incomplete one is cleaned")
)

// ScratchResult is what a finished [ProvisionScratch] reports.
type ScratchResult struct {
	// ProfileDigest is the sandbox profile digest, 64 lower-case hexadecimal
	// characters, computed with sandboxstart.ProfileDigest from the inputs
	// file as the holder reads it. It is also in the lead's record.
	ProfileDigest string
}

// scratchRun is the state that a scratch provisioning adds to a run.
type scratchRun struct {
	sandbox leadcanary.ScratchSandbox
	// inputs and digest are made in memory before anything is written.
	inputs sandboxstart.Inputs
	digest string
}

// ProvisionScratch makes the folders of one scratch lead and writes its record
// last. cfg is one of the configs of [leadcanary.ScratchConfig.Leads] and sb
// the scratch config's sandbox values. It does what [Provision] does for the
// canary, with the differences in the package's scratch notes above, in this
// order:
//
//  1. checks the roots and the root marker of the scratch place
//     (canaryroot.Check) and the lead's state: a lead that is provisioned is
//     refused with [ErrFirstStart] (the caller skips provisioning for it), and
//     one that is cut short with [ErrIncomplete];
//  2. reads and checks the files it copies, builds the sandbox inputs in memory
//     and checks them as the holder's validate step does ([CheckInputs] without
//     the file), computes the profile digest, and refuses a digest that is not
//     the lead's pin ([ErrDigestPin]). Nothing has been written yet;
//  3. makes the mirror from the lead's source when it is not there, or its
//     upstream branch when the mirror lacks it ([mirror.Mirror.ImportUpstream]);
//     a mirror that is there is kept, so a define that failed after this step
//     starts again from it;
//  4. looks at every path it will make, as [Provision] does, and makes them,
//     the sandbox inputs file ([WriteInputs]) included;
//  5. checks the inputs file it wrote ([CheckInputs]) and that its digest is
//     the one of step 2, and writes the record, the last act.
//
// A failure after step 3 leaves what was made and no record: see
// [ErrIncomplete] and [CleanScratch].
func ProvisionScratch(ctx context.Context, cfg leadcanary.Config, sb leadcanary.ScratchSandbox, opts Options) (ScratchResult, error) {
	if cfg.Reserved != leadrole.ReservedScratch {
		return ScratchResult{}, fmt.Errorf("%w: ProvisionScratch takes a scratch lead", ErrScratchKind)
	}
	r, err := newRun(cfg, opts)
	if err != nil {
		return ScratchResult{}, err
	}
	r.scratch = &scratchRun{sandbox: sb}
	if err := r.provision(ctx); err != nil {
		return ScratchResult{}, err
	}
	return ScratchResult{ProfileDigest: r.scratch.digest}, nil
}

// BuildInputs builds the sandbox inputs of a scratch lead, in memory, from the
// config's paths, the scratch config's sandbox values and the SHA-256 of the
// managed-settings file (64 lower-case hexadecimal characters). The sources the
// layout does not name are the folders and files provisioning makes ([Paths]).
func BuildInputs(cfg leadcanary.Config, sb leadcanary.ScratchSandbox, settingsSHA256 string) (sandboxstart.Inputs, error) {
	if cfg.Reserved != leadrole.ReservedScratch {
		return sandboxstart.Inputs{}, fmt.Errorf("%w: BuildInputs takes a scratch lead", ErrScratchKind)
	}
	if err := cfg.Validate(); err != nil {
		return sandboxstart.Inputs{}, err
	}
	if err := sb.Validate(); err != nil {
		return sandboxstart.Inputs{}, err
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return sandboxstart.Inputs{}, err
	}
	return sandboxstart.Inputs{
		Version: sandboxstart.InputsVersion, Canary: true,
		Project: p.Layout.Project, LayoutRole: p.Layout.Role,
		StateRoot: cfg.StateRoot, RuntimeRoot: cfg.RuntimeRoot,
		Runtime: cfg.Agent.Runtime, RuntimeVersion: cfg.Agent.RuntimeVersion,
		Executables: sandboxstart.Executables{Pasta: sb.Pasta, Bwrap: sb.Bwrap, Probe: sb.Probe},
		Sources: sandboxstart.Sources{
			AgentInstall:    sb.AgentInstall,
			ManagedSettings: p.ManagedSettings, ManagedSettingsSHA256: settingsSHA256,
			ManagedMarker: sb.ManagedMarker,
			LeadSkills:    p.LeadSkills, InstructionFile: p.InstructionFile,
			GitIdentityDir: p.GitIdentityDir, QargiInstall: p.QargiInstall,
			QargiRecordsInside: RecordsInside,
			AssignmentDir:      p.AssignmentDir, ProjectionDir: p.ProjectionDir,
		},
		NeverMounted: append([]string(nil), sb.NeverMounted...),
		ProviderHost: sb.ProviderHost,
		Terminal: sandboxstart.Terminal{
			Term: sb.Terminal.Term, ColorTerm: sb.Terminal.ColorTerm, Lang: sb.Terminal.Lang, TZ: sb.Terminal.TZ,
		},
		TimeLimitSeconds: sb.TimeLimitSeconds,
	}, nil
}

// WriteInputs writes the sandbox inputs file of a scratch lead, at the config's
// holder.sandbox_inputs, which must be <state root>/scratch/<layout role>/sandbox-inputs.json
// ([Paths].SandboxInputs): mode 0600, created exclusively beneath the state
// root's handle without following a link, so that anything already there is an
// error. The folder must exist and be a folder as provisioning makes it
// (the caller's, mode 0700). The content is [BuildInputs] as JSON; reading it
// back ([CheckInputs]) is the caller's check.
func WriteInputs(cfg leadcanary.Config, sb leadcanary.ScratchSandbox, settingsSHA256 string) error {
	in, err := BuildInputs(cfg, sb, settingsSHA256)
	if err != nil {
		return err
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return err
	}
	if cfg.Holder.SandboxInputs != p.SandboxInputs {
		return fmt.Errorf("provision: holder.sandbox_inputs must be %s, the lead's file in its folder of the state root", p.SandboxInputs)
	}
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	root, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer root.Close()
	return (&area{root: root, base: p.StateRoot}).file(p.stateRel(p.SandboxInputs), append(data, '\n'), 0o600)
}

// scratchDigest is the profile digest of inputs as the holder computes it for
// the config: the agent's environment set from the config, the caller's ids.
func scratchDigest(cfg leadcanary.Config, in sandboxstart.Inputs) (string, error) {
	in.AgentEnv = sandboxstart.AgentEnvOf(cfg.Agent.Env)
	d, err := sandboxstart.ProfileDigest(in, uint32(unix.Geteuid()), uint32(unix.Getegid()))
	if err != nil {
		return "", fmt.Errorf("the profile digest cannot be computed: %w", err)
	}
	return d, nil
}

// prepareScratch is step 2 of [ProvisionScratch]: the inputs and the digest in
// memory, checked as the holder's validate step checks them, and the pin.
func (r *run) prepareScratch() error {
	if r.cfg.Holder.SandboxInputs != r.p.SandboxInputs {
		return fmt.Errorf("provision: holder.sandbox_inputs must be %s, the lead's file in its folder of the state root", r.p.SandboxInputs)
	}
	in, err := BuildInputs(r.cfg, r.scratch.sandbox, hashHex(r.settings))
	if err != nil {
		return err
	}
	if err := checkInputs(r.cfg, in); err != nil {
		return err
	}
	d, err := scratchDigest(r.cfg, in)
	if err != nil {
		return err
	}
	if pin, ok := r.cfg.PinnedDigest(); ok && pin != d {
		return ErrDigestPin
	}
	r.scratch.inputs, r.scratch.digest = in, d
	return nil
}

// finishScratch is step 5 of [ProvisionScratch]: the inputs file as written must
// pass the check of the holder's validate step and give the digest of step 2,
// and then the record is written.
func (r *run) finishScratch() error {
	if err := CheckInputs(r.cfg, r.p.SandboxInputs); err != nil {
		return err
	}
	in, err := sandboxstart.ReadInputs(r.p.SandboxInputs)
	if err != nil {
		return err
	}
	d, err := scratchDigest(r.cfg, in)
	if err != nil {
		return err
	}
	if d != r.scratch.digest {
		return fmt.Errorf("%w: the inputs file as written gives another digest than the one computed first", ErrInputs)
	}
	data, err := leadRecordBytes(r.p.Layout.Project, d)
	if err != nil {
		return err
	}
	return r.state.file(r.p.stateRel(r.p.LeadRecord), data, 0o600)
}

// leadRecord is the JSON of the record.
type leadRecord struct {
	Marker        string `json:"marker"`
	Version       int    `json:"version"`
	Project       string `json:"project"`
	ProfileDigest string `json:"profile_digest"`
}

func leadRecordBytes(project, digest string) ([]byte, error) {
	data, err := json.Marshal(leadRecord{Marker: leadRecordMarker, Version: 1, Project: project, ProfileDigest: digest})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// LeadRecord is what the record of a provisioned scratch lead holds that a
// caller may use.
type LeadRecord struct {
	// ProfileDigest is the digest provisioning computed and the holder is
	// pinned to when the config has no pin of its own.
	ProfileDigest string
}

// ReadLeadRecord reads the record of a provisioned scratch lead,
// <state root>/scratch/<layout role>/provisioned.json, beneath the state root's
// handle: a regular file of the caller with mode 0600, at most 1 KiB, with the
// exact keys provisioning writes, for this lead's project. It does not look at
// the roots or the marker. A record that is absent wraps fs.ErrNotExist; any
// other fault wraps [ErrRecord]. The record is the daemon's own memory of the
// digest: it is no check that the profile is unchanged since.
func ReadLeadRecord(cfg leadcanary.Config) (LeadRecord, error) {
	if cfg.Reserved != leadrole.ReservedScratch {
		return LeadRecord{}, fmt.Errorf("%w: ReadLeadRecord takes a scratch lead", ErrScratchKind)
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return LeadRecord{}, err
	}
	root, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return LeadRecord{}, fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer root.Close()
	return readLeadRecord(root, p)
}

func readLeadRecord(root *safefile.Root, p Paths) (LeadRecord, error) {
	res, err := root.ReadFileInfo(p.stateRel(p.LeadRecord), maxLeadRecordBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LeadRecord{}, fmt.Errorf("the record of the lead: %w", err)
		}
		return LeadRecord{}, fmt.Errorf("%w: it cannot be read: %w", ErrRecord, err)
	}
	if !owned(res.Info) || res.Info.Mode&fs.ModePerm != 0o600 || !res.Info.IsRegular() {
		return LeadRecord{}, fmt.Errorf("%w: it is not a regular file of the caller with mode 0600", ErrRecord)
	}
	var rec leadRecord
	dec := json.NewDecoder(bytes.NewReader(res.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return LeadRecord{}, fmt.Errorf("%w: it is not a JSON object with the keys provisioning writes", ErrRecord)
	}
	want, err := leadRecordBytes(p.Layout.Project, rec.ProfileDigest)
	if err != nil || !bytes.Equal(res.Data, want) || len(rec.ProfileDigest) != leadcanary.DigestLen || !lowerHexOnly(rec.ProfileDigest) {
		return LeadRecord{}, fmt.Errorf("%w: its content is not what provisioning writes for this lead", ErrRecord)
	}
	return LeadRecord{ProfileDigest: rec.ProfileDigest}, nil
}

func lowerHexOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// State is how far the provisioning of a scratch lead has come.
type State int

// The states of a scratch lead's provisioning.
const (
	// StateNone: none of the lead's folders is there.
	StateNone State = iota
	// StateIncomplete: some of them are there and the record is not.
	StateIncomplete
	// StateDone: the record is there and valid.
	StateDone
)

// String returns "none", "incomplete" or "done".
func (s State) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateIncomplete:
		return "incomplete"
	case StateDone:
		return "done"
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// ScratchStateOf says how far the provisioning of a scratch lead has come. It
// reads the file system beneath the state root's handle and the runtime root's
// and changes nothing. A record that is there but not valid is an error
// ([ErrRecord]), never "done". The mirror is not one of the lead's folders: it
// is kept across a failed define.
func ScratchStateOf(cfg leadcanary.Config) (State, error) {
	if cfg.Reserved != leadrole.ReservedScratch {
		return StateNone, fmt.Errorf("%w: ScratchStateOf takes a scratch lead", ErrScratchKind)
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return StateNone, err
	}
	root, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return StateNone, fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer root.Close()
	return scratchState(root, p)
}

// leadFolders are the folders a define makes for one lead, in the state root,
// as paths relative to it: the ones [CleanScratch] removes.
func (p Paths) leadStateFolders() []string {
	return []string{
		p.stateRel(p.ScratchDir), p.stateRel(p.LeadDir), p.stateRel(p.Mailbox),
		p.stateRel(p.Published), p.stateRel(p.RoleDir),
	}
}

func scratchState(root *safefile.Root, p Paths) (State, error) {
	if _, err := readLeadRecord(root, p); err == nil {
		return StateDone, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return StateNone, err
	}
	for _, rel := range p.leadStateFolders() {
		if _, err := root.Lstat(rel); err == nil {
			return StateIncomplete, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return StateNone, fmt.Errorf("%s: %w", p.StateRoot+"/"+rel, err)
		}
	}
	// The holder's runtime folder lies in the runtime root.
	parent, err := safefile.OpenRootNoLinks(parentDir(p.RuntimeRoot))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return StateNone, nil
		}
		return StateNone, fmt.Errorf("the folder of the runtime root %s: %w", p.RuntimeRoot, err)
	}
	defer parent.Close()
	rel := baseName(p.RuntimeRoot) + "/leads/" + p.Layout.Role
	if _, err := parent.Lstat(rel); err == nil {
		return StateIncomplete, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return StateNone, fmt.Errorf("%s: %w", p.RuntimeFolder, err)
	}
	return StateNone, nil
}

func parentDir(p string) string { return filepath.Dir(p) }

func baseName(p string) string { return filepath.Base(p) }

// ensureMirror is step 3 of [ProvisionScratch]: the lead's mirror, made from the
// lead's source when it is not there, and its upstream branch imported when it
// is not in it. What it makes stays when a later step fails.
func (r *run) ensureMirror(ctx context.Context) error {
	git, err := gitenv.New(GitPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMirror, err)
	}
	root, err := safefile.OpenRootNoLinks(r.p.StateRoot)
	if err != nil {
		return fmt.Errorf("state root %s: %w", r.p.StateRoot, err)
	}
	defer root.Close()
	a := &area{root: root, base: r.p.StateRoot, log: r.say}
	if err := a.dir(r.p.stateRel(r.p.MirrorsDir)); err != nil {
		return err
	}
	var m *mirror.Mirror
	if _, err := root.Lstat(r.p.stateRel(r.p.Mirror)); errors.Is(err, fs.ErrNotExist) {
		if m, err = mirror.Create(ctx, git, r.p.Mirror); err != nil {
			return fmt.Errorf("%w: %w", ErrMirror, err)
		}
		r.say("made the mirror " + r.p.Mirror)
	} else if err != nil {
		return fmt.Errorf("%s: %w", r.p.Mirror, err)
	} else if m, err = mirror.Open(ctx, git, r.p.Mirror); err != nil {
		return fmt.Errorf("%w: %w", ErrMirror, err)
	}
	branch := r.cfg.Provision.UpstreamBranch
	ref, err := mirror.UpstreamRef(branch)
	if err != nil {
		return fmt.Errorf("%w: provision.upstream_branch: %w", ErrMirror, err)
	}
	if _, err := m.Resolve(ctx, ref); err == nil {
		return nil
	} else if !errors.Is(err, mirror.ErrNoRef) {
		return fmt.Errorf("%w: %w", ErrMirror, err)
	}
	if err := m.ImportUpstream(ctx, r.cfg.Provision.Source, branch); err != nil {
		return fmt.Errorf("%w: the branch %q of the lead's source: %w", ErrMirror, branch, err)
	}
	r.say("imported the upstream branch " + branch + " into the mirror")
	return nil
}

// CleanScratch removes what a define of a scratch lead left when it was cut
// short: the owner's way to make the lead defined again, and the only one,
// because deletion is the owner's act and no define deletes anything. It
// requires the same root check as a define (canaryroot.Check: the scratch
// root marker must be there), refuses a lead whose record is there
// ([ErrProvisioned]) and one that has started ([ErrFirstStart]: its holder's
// state folder or runtime folder holds something), and removes, each only when
// it is a real folder and not a link, in this order: the lead's folder of the
// state root's scratch folder (the sandbox inputs file), the lead's folder, its
// mailbox and published folders, the folder of its role lock, and the holder's
// runtime folder. It does not remove the mirror, the evidence, the roots or any
// other lead's folder, and a lead none of whose folders is there is left as it
// is. It may be run again after it was cut short itself.
func CleanScratch(cfg leadcanary.Config, opts Options) error {
	if cfg.Reserved != leadrole.ReservedScratch {
		return fmt.Errorf("%w: CleanScratch takes a scratch lead", ErrScratchKind)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := canaryroot.Check(cfg.StateRoot, cfg.RuntimeRoot, cfg.Role.Project(), opts.getenv()); err != nil {
		return err
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return err
	}
	say := func(s string) {
		if opts.Log != nil {
			opts.Log(s)
		}
	}
	root, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer root.Close()
	st, err := scratchState(root, p)
	switch {
	case err != nil:
		return err
	case st == StateDone:
		return fmt.Errorf("%w", ErrProvisioned)
	case st == StateNone:
		say("nothing to remove: none of the lead's folders is there")
		return nil
	}
	a := &area{root: root, base: p.StateRoot}
	if name, err := a.empty(p.stateRel(p.Holder)); err != nil {
		return err
	} else if name != "" {
		return fmt.Errorf("%w: the holder's state folder %s holds %q", ErrFirstStart, p.Holder, markName(name))
	}
	parent, err := safefile.OpenRootNoLinks(parentDir(p.RuntimeRoot))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the folder of the runtime root %s: %w", p.RuntimeRoot, err)
	}
	if err == nil {
		defer parent.Close()
		ra := &area{root: parent, base: parentDir(p.RuntimeRoot)}
		if name, err := ra.empty(baseName(p.RuntimeRoot) + "/leads/" + p.Layout.Role); err != nil {
			return err
		} else if name != "" {
			return fmt.Errorf("%w: the holder's runtime folder %s holds an entry", ErrFirstStart, p.RuntimeFolder)
		}
	}
	for _, rel := range p.leadStateFolders() {
		if err := removeFolder(root, p.StateRoot, rel, say); err != nil {
			return err
		}
	}
	if parent != nil {
		if err := removeFolder(parent, parentDir(p.RuntimeRoot), baseName(p.RuntimeRoot)+"/leads/"+p.Layout.Role, say); err != nil {
			return err
		}
	}
	return nil
}

// removeFolder removes the folder rel of root when it is there. Anything that
// is not a real folder (a link, a file) is refused and left. Every component
// above it was opened without following a link, by root's handle.
func removeFolder(root *safefile.Root, base, rel string, say func(string)) error {
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s/%s: %w", base, rel, err)
	}
	if info.IsSymlink() || !info.IsDir() {
		return fmt.Errorf("%s/%s: is not a real folder, and is left as it is", base, rel)
	}
	if err := os.RemoveAll(base + "/" + rel); err != nil {
		return fmt.Errorf("%s/%s: %w", base, rel, err)
	}
	say("removed folder " + base + "/" + rel)
	return nil
}
