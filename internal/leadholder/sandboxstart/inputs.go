//go:build linux && ataniq_canary

package sandboxstart

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
)

// InputsVersion is the version of the inputs file's format.
const InputsVersion = 1

// MaxInputsBytes is the largest inputs file ReadInputs reads.
const MaxInputsBytes = 64 << 10

// The bounds of TimeLimitSeconds, in seconds.
const (
	MinTimeLimitSeconds = 1
	MaxTimeLimitSeconds = 3600
)

// Inputs is everything the start sequence needs as values. It is one JSON file,
// read by ReadInputs, and nothing in it is discovered or defaulted from the
// environment or from the home folder of the caller: every path is the file's
// own. The file is the caller's own (owned by the user who runs the holder,
// mode 0600, at most MaxInputsBytes), unknown keys, keys in another case, a key
// written twice and trailing data are refused, and every path is absolute and
// clean. Other tasks write this file (the canary configuration, provisioning,
// the run kit); this comment is their specification, key by key.
//
// The file holds no argument vector and no environment of the agent. They come
// on the holder's command line and reach Run in the last fields of the struct,
// which no file can set (their tag is "-"): the holder fills them before it
// calls Run. Two statements about the start itself, NoLeadHasRun and
// PreviousInstanceStopped, are such fields too: the file is written once, and
// they are true or false per epoch, so they are not keys, and ReadInputs
// refuses a file that names either.
//
// With "canary": false the file is read and Run goes on: there is no root
// check and no waiver, so the agent is launched and the start is refused only
// at the step outside-checks, on assertion 10, for which no qualification
// record exists. A start that cannot succeed therefore still launches the
// agent briefly; the holder under --canary is to refuse such a file before
// it calls Run. In canary mode the layout role must be "<project>-lead", which
// Run checks before any step.
//
// Nothing here names a runtime, a vendor or a model: the runtime is the value
// of "runtime", and so are the version, the provider host and every path.
//
// The keys, in order:
//
//	version                 InputsVersion (1).
//	canary                  true in canary mode. Only then does Run check the
//	                        roots and the marker (package canaryroot) and apply
//	                        the waiver of assertion 10; with false there is no
//	                        root check and no waiver, and the start is refused at
//	                        assertion 10, for which no qualification record
//	                        exists; that refusal comes only after the launch.
//	project                 the project's name; in canary mode it carries the
//	                        reserved prefix.
//	layout_role             the role's name in the host layout, the "<role>" of
//	                        <state>/leads/<role> (profile.Layout.Role), and of
//	                        <runtime root>/leads/<role>. In canary mode it is
//	                        "<project>-lead", which Run checks before any step.
//	                        The role lock is asked about it, not about the
//	                        daemon's role text (assertion 9).
//	state_root              the state root, an absolute real path, below which
//	                        profile.Layout puts the lead's folders. In canary
//	                        mode it carries the marker.
//	runtime_root            the runtime root, an absolute real path that is not
//	                        the state root, does not lie in it and does not
//	                        contain it. The folder <runtime_root>/leads/<layout_role>
//	                        must exist, a real folder of the caller with no
//	                        access for group or others: it is the holder's
//	                        runtime folder. Run makes two folders in it,
//	                        "report" (the report FIFO's, holding nothing else)
//	                        and "bound" (resolv.conf and the spec, which the
//	                        plan binds read-only), removes both when the start
//	                        ends, and clears the leftovers of an earlier holder
//	                        that was killed, which can only be the names it made.
//	runtime, runtime_version
//	                        the epoch's runtime and its pinned version, as the
//	                        profile takes them.
//	identity                "root" (the default) or "owner" (the stated
//	                        fallback), as profile.IdentityMode.
//	owner_user              the owner's login name, for "owner" only. It is also
//	                        the user lingering is asked about for assertion 9;
//	                        when it is empty (the "root" identity) the user is
//	                        System.Username.
//	executables             "pasta", "bwrap" and "probe": absolute paths of three
//	                        different regular files that group and others cannot
//	                        write. The probe is the static probe (the plan's
//	                        pass-through step is the same binary).
//	sources                 the profile's host sources, see Sources.
//	never_mounted           the host paths no bind source may touch: the owner's
//	                        checkout, the real provider folders, ".ssh" and the
//	                        like. At least one.
//	provider_host           the host name assertion 4 resolves inside.
//	terminal                the values the profile takes from a terminal, see
//	                        Terminal. The size of the pseudo-terminal is the
//	                        holder's, not a value here.
//	time_limit_seconds      how long Run waits, from the launch, for the report
//	                        line: MinTimeLimitSeconds to MaxTimeLimitSeconds.
type Inputs struct {
	Version int  `json:"version"`
	Canary  bool `json:"canary"`

	Project     string `json:"project"`
	LayoutRole  string `json:"layout_role"`
	StateRoot   string `json:"state_root"`
	RuntimeRoot string `json:"runtime_root"`

	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`

	Identity  string `json:"identity,omitempty"`
	OwnerUser string `json:"owner_user,omitempty"`

	// NoLeadHasRun is the statement that no lead has run in the lead's
	// folders yet. Only then does Run make the mount points (the step prepare,
	// which is idempotent); it also means there is no previous instance. It is
	// not a key of the file, and ReadInputs refuses a file that names it. The
	// holder fills it before every Run from a durable mark, not from the epoch
	// number: it is true only while the file "lead-has-run" is absent from the
	// holder's state folder (--state-dir), and the holder writes that mark, by
	// handle and exclusively, before it calls Run. (The epoch counter is not
	// the history of the folders: a start that failed after the launch and is
	// retried is epoch 1 again, over folders a lead has run in.) Seams note,
	// section 8, "Per-epoch inputs".
	NoLeadHasRun bool `json:"-"`
	// PreviousInstanceStopped is the statement that no process of an earlier
	// instance of the lead remains. The start sequence has no unit and no
	// control group to prove it, so the holder does. It is not a key of the
	// file, and ReadInputs refuses a file that names it. The holder sets it
	// true only from its own observation: its control group (read from
	// /proc/self/cgroup) holds no process but the holder itself. The unit name
	// is fixed, so a new epoch cannot start while the last unit exists, and the
	// daemon's stop proof stands behind that; the role lock proves only that no
	// other holder runs, and is not this proof. A walk of the bind destinations
	// that finds nothing is "ok" for assertion 7 only with this field or with
	// NoLeadHasRun (the harness's rule); without both that walk is not-checked,
	// and the start is refused. The start of a second epoch has NoLeadHasRun
	// false and this true.
	PreviousInstanceStopped bool `json:"-"`

	Executables  Executables `json:"executables"`
	Sources      Sources     `json:"sources"`
	NeverMounted []string    `json:"never_mounted"`

	ProviderHost     string   `json:"provider_host"`
	Terminal         Terminal `json:"terminal"`
	TimeLimitSeconds int      `json:"time_limit_seconds"`

	// AgentArgv is the agent's command line as seen inside: the first element
	// is an absolute path. The holder fills it from the words after "--" on its
	// own command line. It is not a key of the file.
	AgentArgv []string `json:"-"`
	// AgentEnv is what the holder adds to the profile's environment allowlist
	// (profile.Inputs.ExtraEnv), from its "--env" arguments: the only way a
	// variable reaches the agent, because the plan clears the environment. The
	// names are checked by the profile (no credential, loader or session-bus
	// name). It is not a key of the file.
	AgentEnv []profile.EnvVar `json:"-"`
	// ExpectedDigest is the profile digest the holder was pinned to, from its
	// "--profile-digest". When it is not empty, a plan whose digest differs is
	// refused at the step verify-plan, before anything is launched; the holder
	// compares the digest in Started as well. It is not a key of the file.
	ExpectedDigest string `json:"-"`
	// HolderPaths are the paths the holder owns beyond the ones Run knows (the
	// report folder and FIFO, and the lead's holder folder of the layout): its
	// attach and control sockets, say. No bind source may touch one, and none
	// may be visible inside (assertion 1). Each is an absolute clean path that
	// is not above a source of the plan: in particular they are not the
	// runtime folder itself, which holds the "bound" folder the plan binds. The
	// holder fills them from its own names. It is not a key of the file.
	HolderPaths []string `json:"-"`
}

// Executables are the three programs the plan runs.
type Executables struct {
	Pasta string `json:"pasta"`
	Bwrap string `json:"bwrap"`
	Probe string `json:"probe"`
}

// Sources are the profile's host sources, each an absolute real path of the
// kind named: a folder, or a regular file. The profile says which row binds
// each and the rules it has to meet (docs/lead-sandbox.md); the keys are:
//
//	agent_install           the agent's install folder for the pinned version.
//	managed_settings        the managed-settings file Ataniq wrote, a regular file;
//	managed_settings_sha256 its SHA-256, 64 lower-case hex digits. Run compares
//	                        it with the file and refuses a difference, because
//	                        the file is the policy half of the profile and its
//	                        digest is part of the profile's digest.
//	managed_settings_inside where it appears inside: absent for the default of
//	                        the runtime, required for a runtime that has none.
//	managed_marker          the text the managed-settings file must contain
//	                        (assertion 8), for a runtime whose policy names a
//	                        guard; else absent.
//	lead_skills             the folder bound over the skills folder of the config
//	                        folder.
//	instruction_file        the instruction file bound over its place there.
//	git_identity_dir        the folder that holds the git identity file, named
//	                        "config", and nothing else (it is bound whole).
//	qargi_install           the Qargi install folder.
//	qargi_records_inside    where the lead's working Qargi records appear inside.
//	                        The host folder is the layout's own,
//	                        <state_root>/leads/<layout_role>/qargi, and must
//	                        exist as a 0700 folder of the caller.
//	assignment_dir, projection_dir
//	                        two folders written outside the sandbox and bound
//	                        read-only.
//	seccomp                 optional: a reviewed BPF file, see Seccomp.
//	extra_placeholders      optional extra bind targets in the work tree, see
//	                        Placeholder.
type Sources struct {
	AgentInstall          string `json:"agent_install"`
	ManagedSettings       string `json:"managed_settings"`
	ManagedSettingsSHA256 string `json:"managed_settings_sha256"`
	ManagedSettingsInside string `json:"managed_settings_inside,omitempty"`
	ManagedMarker         string `json:"managed_marker,omitempty"`
	LeadSkills            string `json:"lead_skills"`
	InstructionFile       string `json:"instruction_file"`
	GitIdentityDir        string `json:"git_identity_dir"`
	QargiInstall          string `json:"qargi_install"`
	QargiRecordsInside    string `json:"qargi_records_inside"`
	AssignmentDir         string `json:"assignment_dir"`
	ProjectionDir         string `json:"projection_dir"`

	Seccomp           *Seccomp      `json:"seccomp,omitempty"`
	ExtraPlaceholders []Placeholder `json:"extra_placeholders,omitempty"`
}

// Seccomp names a reviewed BPF file: "file" (absolute), "sha256" (the file's
// SHA-256, which Run compares) and "reviewed_bpf" (true: the statement that
// someone reviewed the filter; without it the file is refused, because nothing
// here reviews one).
type Seccomp struct {
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	ReviewedBPF bool   `json:"reviewed_bpf"`
}

// Placeholder is an extra bind target in the work tree (profile.Placeholder):
// "rel" the target relative to the tree, "source" the host path bound over it,
// "dir" true for a folder.
type Placeholder struct {
	Rel    string `json:"rel"`
	Source string `json:"source"`
	Dir    bool   `json:"dir,omitempty"`
}

// Terminal holds the values the profile takes from a terminal: "term" is
// required; "colorterm", "lang" and "tz" are left out when the host has none,
// and the variable is then not set.
type Terminal struct {
	Term      string `json:"term"`
	ColorTerm string `json:"colorterm,omitempty"`
	Lang      string `json:"lang,omitempty"`
	TZ        string `json:"tz,omitempty"`
}

// InputsError is a refusal of the inputs file or of a value in it. Err is the
// profile's own refusal when the value was refused by one.
type InputsError struct {
	Field  string
	Reason string
	Err    error
}

// Unwrap returns the profile's refusal, if there is one.
func (e *InputsError) Unwrap() error { return e.Err }

// Error implements error.
func (e *InputsError) Error() string {
	if e.Field == "" {
		return "sandbox inputs: " + e.Reason
	}
	return "sandbox inputs: " + e.Field + ": " + e.Reason
}

func inputsErr(field, format string, args ...any) error {
	return &InputsError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// fromProfile makes an *InputsError of a refusal of the profile's.
func fromProfile(err error) error {
	if err == nil {
		return nil
	}
	return &InputsError{Reason: err.Error(), Err: err}
}

// ReadInputs reads and checks the inputs file at file. The file is opened
// without following a symbolic link at its name and judged by its handle: a
// regular file, owned by the calling user, with mode exactly 0600, at most
// MaxInputsBytes long, valid UTF-8, one JSON object with exactly the known
// keys. The values are then checked as far as the file alone allows (the names,
// the paths, the bounds); what needs the host or the profile is checked by Run,
// at its first step. The error is an *InputsError.
func ReadInputs(file string) (Inputs, error) {
	fd, err := unix.Open(file, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return Inputs{}, inputsErr("file", "cannot open %s: %v", clipText(file), err)
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Inputs{}, inputsErr("file", "cannot stat: %v", err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return Inputs{}, inputsErr("file", "is not a regular file")
	case st.Mode&0o7777 != 0o600:
		return Inputs{}, inputsErr("file", "has mode %04o, must be 0600", st.Mode&0o7777)
	case st.Uid != uint32(unix.Geteuid()):
		return Inputs{}, inputsErr("file", "is not owned by the calling user")
	case st.Size > MaxInputsBytes:
		return Inputs{}, inputsErr("file", "is larger than %d bytes", MaxInputsBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxInputsBytes+1))
	if err != nil {
		return Inputs{}, inputsErr("file", "cannot read: %v", err)
	}
	return parseInputs(data)
}

// parseInputs decodes and checks inputs bytes, strictly: ReadInputs without
// the checks of the file.
func parseInputs(data []byte) (Inputs, error) {
	if len(data) > MaxInputsBytes {
		return Inputs{}, inputsErr("file", "is larger than %d bytes", MaxInputsBytes)
	}
	if !utf8.Valid(data) {
		return Inputs{}, inputsErr("file", "is not valid UTF-8")
	}
	if err := checkKeys(data, reflect.TypeFor[Inputs]()); err != nil {
		return Inputs{}, inputsErr("file", "%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in Inputs
	if err := dec.Decode(&in); err != nil {
		return Inputs{}, inputsErr("file", "is not an inputs file: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Inputs{}, inputsErr("file", "has data after the inputs")
	}
	if err := in.checkFields(); err != nil {
		return Inputs{}, err
	}
	return in, nil
}

// checkFields checks the values the file alone decides: the version, the
// names, the paths (absolute and clean), the roots against each other, the
// runtime, the identity and the time limit. The profile's own checks need the
// owner's ids and the paths of the files Run writes, and run in the first step.
func (in Inputs) checkFields() error {
	if in.Version != InputsVersion {
		return inputsErr("version", "is %d, want %d", in.Version, InputsVersion)
	}
	if err := profile.CheckName("project", in.Project); err != nil {
		return fromProfile(err)
	}
	if err := profile.CheckName("layout_role", in.LayoutRole); err != nil {
		return fromProfile(err)
	}
	for _, p := range []struct{ field, v string }{
		{"state_root", in.StateRoot}, {"runtime_root", in.RuntimeRoot},
		{"executables.pasta", in.Executables.Pasta}, {"executables.bwrap", in.Executables.Bwrap}, {"executables.probe", in.Executables.Probe},
	} {
		if err := profile.CheckPath(p.field, p.v); err != nil {
			return fromProfile(err)
		}
	}
	if in.StateRoot == "/" || in.RuntimeRoot == "/" {
		return inputsErr("state_root", "the roots must not be the file system root")
	}
	if related(in.StateRoot, in.RuntimeRoot) {
		return inputsErr("runtime_root", "is the state root, lies in it or contains it: the report FIFO must not be where the lead's state is")
	}
	if a, b, c := in.Executables.Pasta, in.Executables.Bwrap, in.Executables.Probe; a == b || a == c || b == c {
		return inputsErr("executables", "the three executables must be three different files")
	}
	if _, err := profile.ParseRuntime(in.Runtime); err != nil {
		return fromProfile(err)
	}
	switch in.Identity {
	case "", string(profile.IdentityRoot):
		if in.OwnerUser != "" {
			return inputsErr("owner_user", "is given for the %q identity only", profile.IdentityOwner)
		}
	case string(profile.IdentityOwner):
	default:
		return inputsErr("identity", "must be %q or %q", profile.IdentityRoot, profile.IdentityOwner)
	}
	if in.TimeLimitSeconds < MinTimeLimitSeconds || in.TimeLimitSeconds > MaxTimeLimitSeconds {
		return inputsErr("time_limit_seconds", "must be %d to %d", MinTimeLimitSeconds, MaxTimeLimitSeconds)
	}
	if in.ProviderHost == "" {
		return inputsErr("provider_host", "is empty")
	}
	if s := in.Sources.Seccomp; s != nil && !s.ReviewedBPF {
		return inputsErr("sources.seccomp", "a seccomp file is refused unless the file states \"reviewed_bpf\": true; nothing here reviews a filter")
	}
	return nil
}

// related says a is b, lies below it or contains it (clean absolute paths).
func related(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// below says p lies below dir.
func below(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }

// The places of the files Run makes, all in the runtime folder of the role.
// They follow from the two values runtime_root and layout_role, and they are
// the same every start.
const (
	reportDirName  = "report"
	reportFIFOName = "probe.fifo"
	boundDirName   = "bound"
	resolvName     = "resolv.conf"
	specDirName    = "spec"
)

// RuntimeFolder is the holder's runtime folder of the role,
// <runtime root>/leads/<layout role>. It must exist before Run.
func RuntimeFolder(runtimeRoot, layoutRole string) string {
	return path.Join(runtimeRoot, "leads", layoutRole)
}

// ReportFolder is the folder of the report FIFO, "report" in the runtime
// folder. It holds nothing else.
func ReportFolder(runtimeRoot, layoutRole string) string {
	return path.Join(RuntimeFolder(runtimeRoot, layoutRole), reportDirName)
}

// ReportFIFO is the report FIFO, <runtime root>/leads/<layout role>/report/probe.fifo.
// It is under the runtime root, never under <state root>/leads, which the plan
// refuses for a report path.
func ReportFIFO(runtimeRoot, layoutRole string) string {
	return path.Join(ReportFolder(runtimeRoot, layoutRole), reportFIFOName)
}

func boundFolder(runtimeRoot, layoutRole string) string {
	return path.Join(RuntimeFolder(runtimeRoot, layoutRole), boundDirName)
}

// paths of the two files the plan binds from the bound folder.
func boundResolv(runtimeRoot, layoutRole string) string {
	return path.Join(boundFolder(runtimeRoot, layoutRole), resolvName)
}

func boundSpecDir(runtimeRoot, layoutRole string) string {
	return path.Join(boundFolder(runtimeRoot, layoutRole), specDirName)
}
