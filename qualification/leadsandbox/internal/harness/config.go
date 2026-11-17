//go:build linux && amd64

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
)

// ConfigVersion is the version of the configuration format.
const ConfigVersion = 1

// Bounds on the configuration.
const (
	// MaxConfigBytes is the largest configuration file LoadConfig reads.
	MaxConfigBytes = 64 << 10

	minTimeLimitSeconds = 1
	maxTimeLimitSeconds = 3600
	maxTerminalDim      = 500
	maxScratchArgs      = 64
	maxScratchArgBytes  = 4096
)

// The descriptor numbers the harness gives the plan. They are constants of
// the harness, not configuration: the pass-through step opens the files at
// these numbers.
const (
	// SeccompFD is the descriptor of the seccomp file, when one is named.
	SeccompFD = 3
	// ReportFD is the descriptor of the report channel.
	ReportFD = 4
)

// ScratchMarker is what the default scratch command prints.
const ScratchMarker = "ataniq-leadsandbox-scratch-marker"

// DefaultScratchCommand is the command run inside when the configuration
// names none: a shell that prints ScratchMarker and exits.
func DefaultScratchCommand() []string {
	return []string{"/bin/sh", "-c", "echo " + ScratchMarker}
}

// AutomatedCases are the cases of the design (section 7.3) that the probe and
// the outside checks speak to. Every other case is attended (see the manual).
// G1 is not in the design's table: it is the case of rule G1 (no bind source a
// sandbox can write, every bind source checked without following links).
var AutomatedCases = []string{"S1", "S2", "S3", "S4", "S7", "S11", "G1"}

// Config is the configuration file. Nothing in it is discovered or defaulted
// from the environment or the home folder of the caller: every path is the
// file's own. Unknown keys, a key written twice, a key in another case and
// trailing data are refused.
type Config struct {
	Version int `json:"version"`
	// Campaign is the private (0700) folder that must exist; every attempt
	// folder is made below it. It must not be a never-mounted path, because
	// two bind sources of the harness lie below it.
	Campaign string `json:"campaign"`
	// CoordinatorReviewed is the statement that a coordinator reviewed this
	// configuration and the plan it gives. A run without it is refused.
	CoordinatorReviewed bool `json:"coordinator_reviewed"`
	// NoLeadHasRun is the statement that no lead has run in the scratch
	// folders: only then does a run make the mount points (Prepare).
	NoLeadHasRun bool `json:"no_lead_has_run"`

	Executables Executables `json:"executables"`

	// Runtime is "claude" or "codex"; RuntimeVersion its pinned version.
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`

	State State `json:"state"`
	// Identity is "root" (the default) or "owner"; the latter needs OwnerUser.
	Identity  string `json:"identity,omitempty"`
	OwnerUser string `json:"owner_user,omitempty"`

	Sources Sources `json:"sources"`

	// NeverMounted are the host paths no bind source may touch.
	NeverMounted []string `json:"never_mounted"`

	// ScratchCommand is the command run inside, as an absolute argv. Absent
	// means DefaultScratchCommand.
	ScratchCommand []string `json:"scratch_command,omitempty"`

	// ProviderHost is the host name the DNS assertion resolves inside.
	ProviderHost string `json:"provider_host"`

	Terminal Terminal `json:"terminal"`

	// TimeLimitSeconds bounds the wait for the report and for the exit, from
	// the moment of the launch.
	TimeLimitSeconds int `json:"time_limit_seconds"`

	// Cases lists which automated cases to record (see AutomatedCases).
	Cases []string `json:"cases"`
}

// Executables are the three programs the plan runs: absolute, regular, not
// group- or other-writable. The SHA-256 of each is recorded.
type Executables struct {
	Pasta string `json:"pasta"`
	Bwrap string `json:"bwrap"`
	Probe string `json:"probe"`
}

// State is the scratch state root and the role and project under it. The
// folders of the layout below the root have no key (profile.Layout names
// them): the lead's home, config folder, tree, records folder (qargi),
// requests folder and mail folder under leads/<role>, the lead's mailbox
// mailbox/<role>, the folder the project's coordinators publish under,
// pub/<project>, and the mirror. They must exist before a run; the plan binds
// them for every lead.
type State struct {
	Root    string `json:"root"`
	Role    string `json:"role"`
	Project string `json:"project"`
}

// Sources are the profile's host sources.
type Sources struct {
	AgentInstall string `json:"agent_install"`

	ManagedSettings       string `json:"managed_settings"`
	ManagedSettingsSHA256 string `json:"managed_settings_sha256"`
	// ManagedSettingsInside is required for codex and must be absent or the
	// provider's path for claude.
	ManagedSettingsInside string `json:"managed_settings_inside,omitempty"`
	// ManagedMarker is the text the managed-settings file must contain
	// (required for claude).
	ManagedMarker string `json:"managed_marker,omitempty"`

	LeadSkills      string `json:"lead_skills"`
	InstructionFile string `json:"instruction_file"`
	// GitIdentityDir is the folder that holds the git identity file, which
	// has the fixed name "config" (profile.GitIdentityFileName) and must
	// exist. The whole folder is bound read-only, so it holds that file and
	// nothing else.
	GitIdentityDir string `json:"git_identity_dir"`
	QargiInstall   string `json:"qargi_install"`

	// QargiRecordsInside is where the lead's working Qargi records appear
	// inside: the path Qargi's layout names. It is required, because every
	// lead has the records and nothing here knows a default for the path. The
	// host folder is not a key: it is the layout's own,
	// <state root>/leads/<role>/qargi (rule G1: a folder the lead writes at
	// any other place could be, or lie above, a source of another lead). It
	// must exist, as a real folder of mode 0700 owned by the caller, like the
	// lead's home.
	QargiRecordsInside string `json:"qargi_records_inside"`

	AssignmentDir string `json:"assignment_dir"`
	ProjectionDir string `json:"projection_dir"`

	Seccomp           *Seccomp      `json:"seccomp,omitempty"`
	ExtraPlaceholders []Placeholder `json:"extra_placeholders,omitempty"`
}

// Seccomp names a reviewed BPF file. Without ReviewedBPF the file is refused:
// the statement is the coordinator's, and nothing here reviews a filter.
type Seccomp struct {
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	ReviewedBPF bool   `json:"reviewed_bpf"`
}

// Placeholder is an extra bind target in the work tree (profile.Placeholder).
type Placeholder struct {
	Rel    string `json:"rel"`
	Source string `json:"source"`
	Dir    bool   `json:"dir,omitempty"`
}

// Terminal holds the values the profile takes from a terminal, and the size
// of the pseudo-terminal the launcher makes.
type Terminal struct {
	Term      string `json:"term"`
	ColorTerm string `json:"colorterm,omitempty"`
	Lang      string `json:"lang,omitempty"`
	TZ        string `json:"tz,omitempty"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

// ConfigError is a refusal of the configuration file or of a value in it.
type ConfigError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *ConfigError) Error() string {
	if e.Field == "" {
		return "config: " + e.Reason
	}
	return "config: " + e.Field + ": " + e.Reason
}

func configErr(field, format string, args ...any) error {
	return &ConfigError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// LoadConfig reads and validates the configuration file at path. The file
// must be a regular file, not reached through a final symbolic link, owned by
// the calling user, with mode exactly 0600, at most MaxConfigBytes long, valid
// UTF-8, one JSON object with exactly the known keys, and Validate must pass.
func LoadConfig(file string) (*Config, error) {
	fd, err := unix.Open(file, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, configErr("file", "cannot open %s: %v", file, err)
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, configErr("file", "cannot stat: %v", err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return nil, configErr("file", "is not a regular file")
	case st.Mode&0o7777 != 0o600:
		return nil, configErr("file", "has mode %04o, must be 0600", st.Mode&0o7777)
	case st.Uid != uint32(unix.Geteuid()):
		return nil, configErr("file", "is not owned by the calling user")
	case st.Size > MaxConfigBytes:
		return nil, configErr("file", "is larger than %d bytes", MaxConfigBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, configErr("file", "cannot read: %v", err)
	}
	return ParseConfig(data)
}

// ParseConfig decodes and validates configuration bytes, strictly. It is
// LoadConfig without the checks of the file.
func ParseConfig(data []byte) (*Config, error) {
	if len(data) > MaxConfigBytes {
		return nil, configErr("file", "is larger than %d bytes", MaxConfigBytes)
	}
	if !utf8.Valid(data) {
		return nil, configErr("file", "is not valid UTF-8")
	}
	if err := checkKeys(data, reflectType[Config]()); err != nil {
		return nil, configErr("file", "%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, configErr("file", "is not a configuration: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, configErr("file", "has data after the configuration")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Command is the scratch command: the configured one, else the default.
func (c *Config) Command() []string {
	if len(c.ScratchCommand) == 0 {
		return DefaultScratchCommand()
	}
	return append([]string(nil), c.ScratchCommand...)
}

// DefaultCommand says the configuration names no scratch command.
func (c *Config) DefaultCommand() bool { return len(c.ScratchCommand) == 0 }

// Validate checks every value of the configuration without touching the file
// system: the lexical checks of the profile on the inputs it gives, the
// campaign against the other folders, the seccomp statement, the scratch
// command, the terminal, the time limit and the cases. The error is a
// *ConfigError or the profile's, plan's or assert's own refusal, which names
// its reason.
func (c *Config) Validate() error {
	if c.Version != ConfigVersion {
		return configErr("version", "is %d, want %d", c.Version, ConfigVersion)
	}
	if err := profile.CheckPath("campaign", c.Campaign); err != nil {
		return err
	}
	for _, e := range []struct{ name, p string }{{"executables.pasta", c.Executables.Pasta}, {"executables.bwrap", c.Executables.Bwrap}, {"executables.probe", c.Executables.Probe}} {
		if err := profile.CheckPath(e.name, e.p); err != nil {
			return err
		}
	}
	if c.Executables.Pasta == c.Executables.Bwrap || c.Executables.Pasta == c.Executables.Probe || c.Executables.Bwrap == c.Executables.Probe {
		return configErr("executables", "the three executables must be three different files")
	}
	if c.Sources.Seccomp != nil && !c.Sources.Seccomp.ReviewedBPF {
		return configErr("sources.seccomp", "a seccomp file is refused unless the configuration states \"reviewed_bpf\": true; nothing here reviews a filter")
	}
	if c.Identity != "" && c.Identity != string(profile.IdentityRoot) && c.Identity != string(profile.IdentityOwner) {
		return configErr("identity", "must be %q or %q", profile.IdentityRoot, profile.IdentityOwner)
	}
	if err := c.checkTerminal(); err != nil {
		return err
	}
	if c.TimeLimitSeconds < minTimeLimitSeconds || c.TimeLimitSeconds > maxTimeLimitSeconds {
		return configErr("time_limit_seconds", "must be %d to %d", minTimeLimitSeconds, maxTimeLimitSeconds)
	}
	if err := c.checkCases(); err != nil {
		return err
	}
	if err := c.checkScratch(); err != nil {
		return err
	}
	// The profile's own checks, on inputs whose harness-owned paths are
	// placeholders below the campaign.
	in, err := c.inputs(path.Join(c.Campaign, "placeholder/bound/resolv.conf"), path.Join(c.Campaign, "placeholder/bound/spec"))
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if err := c.checkCampaign(in); err != nil {
		return err
	}
	// A trial spec: the provider host, the managed-settings marker and the
	// names the spec checks.
	_, err = assert.NewSpec(in, []profile.EnvVar{{Name: "PWD", Value: profile.TreeInside(in.Project)}}, assert.HostFacts{
		HolderPaths:    []string{path.Join(c.Campaign, "placeholder/report.fifo")},
		LoopbackTCP:    "127.0.0.1:1",
		AbstractSocket: "ataniq-config-trial",
		ProviderHost:   c.ProviderHost,
		ManagedMarker:  c.Sources.ManagedMarker,
	})
	return err
}

func (c *Config) checkTerminal() error {
	t := c.Terminal
	if t.Rows < 1 || t.Rows > maxTerminalDim || t.Cols < 1 || t.Cols > maxTerminalDim {
		return configErr("terminal", "rows and cols must be 1 to %d", maxTerminalDim)
	}
	return nil
}

func (c *Config) checkCases() error {
	if len(c.Cases) == 0 {
		return configErr("cases", "none given: name at least one of %s", strings.Join(AutomatedCases, ", "))
	}
	seen := map[string]bool{}
	for _, name := range c.Cases {
		ok := false
		for _, a := range AutomatedCases {
			ok = ok || a == name
		}
		if !ok {
			return configErr("cases", "%q is not an automated case (the automated cases are %s; the others are attended)", clip(name), strings.Join(AutomatedCases, ", "))
		}
		if seen[name] {
			return configErr("cases", "%s is listed twice", name)
		}
		seen[name] = true
	}
	return nil
}

func (c *Config) checkScratch() error {
	cmd := c.Command()
	if len(cmd) > maxScratchArgs {
		return configErr("scratch_command", "more than %d arguments", maxScratchArgs)
	}
	if err := profile.CheckPath("scratch_command[0]", cmd[0]); err != nil {
		return err
	}
	for i, a := range cmd {
		if strings.IndexByte(a, 0) >= 0 || len(a) > maxScratchArgBytes || !utf8.ValidString(a) {
			return configErr(fmt.Sprintf("scratch_command[%d]", i), "must be valid UTF-8 of at most %d bytes without a NUL", maxScratchArgBytes)
		}
	}
	return nil
}

// checkCampaign refuses a campaign folder that overlaps the state root, a
// source of the profile or a never-mounted path: the campaign holds the
// evidence and the report channel, and the lead must not be able to reach
// either.
func (c *Config) checkCampaign(in profile.Inputs) error {
	if related(c.Campaign, c.State.Root) {
		return configErr("campaign", "overlaps the state root %s: the lead can write there", c.State.Root)
	}
	for _, n := range in.NeverMounted {
		if related(c.Campaign, n) {
			return configErr("campaign", "overlaps the never-mounted path %s: two bind sources of the harness lie below the campaign, so it cannot be listed there", n)
		}
	}
	for name, p := range map[string]string{
		"sources.agent_install": c.Sources.AgentInstall, "sources.managed_settings": c.Sources.ManagedSettings,
		"sources.lead_skills": c.Sources.LeadSkills, "sources.instruction_file": c.Sources.InstructionFile,
		"sources.git_identity_dir": c.Sources.GitIdentityDir, "sources.qargi_install": c.Sources.QargiInstall,
		"sources.assignment_dir": c.Sources.AssignmentDir,
		"sources.projection_dir": c.Sources.ProjectionDir, "executables.probe": c.Executables.Probe,
	} {
		if p != "" && related(p, c.Campaign) {
			return configErr(name, "overlaps the campaign folder")
		}
	}
	return nil
}

// inputs builds the profile inputs from the configuration, with the two
// harness-owned paths given.
func (c *Config) inputs(resolv, specDir string) (profile.Inputs, error) {
	rt, err := profile.ParseRuntime(c.Runtime)
	if err != nil {
		return profile.Inputs{}, err
	}
	in := profile.Inputs{
		StateRoot: c.State.Root, Role: c.State.Role, Project: c.State.Project,
		Runtime: rt, RuntimeVersion: c.RuntimeVersion,
		Identity:     profile.IdentityRoot,
		GitMeta:      c.gitMeta(),
		AgentInstall: c.Sources.AgentInstall, ProbeBinary: c.Executables.Probe, SpecDir: specDir,
		ManagedSettings: c.Sources.ManagedSettings, ManagedSettingsInside: c.Sources.ManagedSettingsInside,
		ManagedSettingsSHA256: c.Sources.ManagedSettingsSHA256,
		LeadSkills:            c.Sources.LeadSkills, InstructionFile: c.Sources.InstructionFile,
		GitIdentityDir: c.Sources.GitIdentityDir, QargiInstall: c.Sources.QargiInstall,
		QargiRecordsInside: c.Sources.QargiRecordsInside,
		AssignmentDir:      c.Sources.AssignmentDir, ProjectionDir: c.Sources.ProjectionDir,
		ResolvConf:   resolv,
		NeverMounted: append([]string(nil), c.NeverMounted...),
		Term:         c.Terminal.Term, ColorTerm: c.Terminal.ColorTerm, Lang: c.Terminal.Lang, TZ: c.Terminal.TZ,
	}
	if c.Identity == string(profile.IdentityOwner) {
		in.Identity = profile.IdentityOwner
		in.OwnerUID, in.OwnerGID, in.OwnerUser = uint32(os.Getuid()), uint32(os.Getgid()), c.OwnerUser
	} else if c.OwnerUser != "" {
		return profile.Inputs{}, configErr("owner_user", "is given for the %q identity only", profile.IdentityOwner)
	}
	if s := c.Sources.Seccomp; s != nil {
		in.SeccompFile, in.SeccompSHA256 = s.File, s.SHA256
	}
	for _, p := range c.Sources.ExtraPlaceholders {
		in.ExtraPlaceholders = append(in.ExtraPlaceholders, profile.Placeholder{Rel: p.Rel, Source: p.Source, Dir: p.Dir})
	}
	return in, nil
}

// layout is the host layout of the scratch lead.
func (c *Config) layout() profile.Layout {
	return profile.Layout{StateRoot: c.State.Root, Role: c.State.Role, Project: c.State.Project}
}

// gitMeta is the git metadata folder, the layout's own below the scratch state
// root: the configuration has no key for it.
func (c *Config) gitMeta() string { return c.layout().GitMeta() }

func related(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func below(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }

// clip cuts text a user or a host gave before it goes into a message.
func clip(s string) string {
	if len(s) > 48 {
		s = s[:48] + "..."
	}
	return assert.EscapeDetail(s)
}
