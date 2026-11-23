//go:build linux && ataniq_canary

package leadcanary

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadrole"
)

// The scratch config is the owner-only file that a daemon of scratch mode reads
// once, at start, and that names every scratch lead it may define. The client
// never sends a path or a value of it: a lead is named by its key.

// Bounds of the scratch config.
const (
	// MaxScratchLeads is the most leads one scratch config lists, and the
	// largest max_running.
	MaxScratchLeads = 8
	// MaxScratchKeyLen is the longest lead key, in bytes. The layout name
	// "scratch-<key>-lead" is then 63 bytes, the layout's own bound.
	MaxScratchKeyLen = 50
	// UnpinnedDigest stands in a derived [Config] for the profile digest of a
	// lead whose scratch config has no pin: 64 zeros, a value that is a digest
	// by form and is never the digest of a profile. The daemon computes the
	// digest when it defines the lead and sets it with
	// [Config.WithProfileDigest]; until then a holder started with it would
	// refuse the start on the mismatch.
	UnpinnedDigest = "0000000000000000000000000000000000000000000000000000000000000000"

	// ScratchDirName is the folder of the state root that holds one folder per
	// scratch lead (<state root>/scratch/<layout role>), with what the owner's
	// side keeps for the lead that the lead's sandbox never sees: the sandbox
	// inputs file and the record that provisioning is done.
	ScratchDirName = "scratch"
	// SandboxInputsName is the name of the sandbox inputs file of a scratch
	// lead in its folder of [ScratchDirName].
	SandboxInputsName = "sandbox-inputs.json"

	maxHostText     = 253
	maxTermText     = 64
	maxNeverMounted = 64
	// MinTimeLimitSeconds and MaxTimeLimitSeconds bound sandbox.time_limit_seconds.
	// They are the bounds of the sandbox inputs file's time_limit_seconds, and
	// a test of the provision package holds that they are equal.
	MinTimeLimitSeconds = 1
	MaxTimeLimitSeconds = 3600
)

// ScratchConfig is the scratch config. Every field is required except the ones
// named below; none has a default in code. The JSON keys are the ones in the
// tags.
type ScratchConfig struct {
	// Version is 1.
	Version int `json:"version"`
	// StateRoot and RuntimeRoot are the two roots of the scratch place, shared
	// by every lead. They are absolute clean paths and neither is a default
	// root of the account.
	StateRoot   string `json:"state_root"`
	RuntimeRoot string `json:"runtime_root"`
	// MaxRunning is the most leads that may run at once: 1 to
	// [MaxScratchLeads].
	MaxRunning int `json:"max_running"`
	// Holder says how every lead's holder is started.
	Holder ScratchHolder `json:"holder"`
	// Sandbox holds the values of the sandbox inputs file that do not depend on
	// the lead.
	Sandbox ScratchSandbox `json:"sandbox"`
	// Provision names what provisioning copies and sets, for every lead.
	Provision ScratchProvision `json:"provision"`
	// Entries are the leads, 1 to [MaxScratchLeads], each with its own key (the
	// JSON key is "leads"; the method [ScratchConfig.Leads] gives them derived).
	Entries []ScratchLead `json:"leads"`
}

// ScratchHolder is the holder part of the scratch config: the holder binary and
// the size of its screen, the same for every lead.
type ScratchHolder struct {
	Executable string `json:"executable"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

// ScratchSandbox is what a scratch lead's sandbox inputs file takes from the
// scratch config. The file's other values come from the lead's layout and agent
// (the project, the roots, the runtime and its version) or from provisioning
// (the sources, the digest of the managed settings).
type ScratchSandbox struct {
	// AgentInstall is the agent's install folder for the pinned version.
	AgentInstall string `json:"agent_install"`
	// Pasta, Bwrap and Probe are the three executables of the plan: three
	// different files.
	Pasta string `json:"pasta"`
	Bwrap string `json:"bwrap"`
	Probe string `json:"probe"`
	// ManagedMarker is the text the managed-settings file must hold
	// (sources.managed_marker); required for the claude runtime, so absent only
	// for a runtime that names no guard. Optional here, checked by the sandbox
	// inputs.
	ManagedMarker string `json:"managed_marker,omitempty"`
	// NeverMounted are the host paths no bind source may touch: 1 to 64.
	NeverMounted []string `json:"never_mounted"`
	// ProviderHost is the host name assertion 4 resolves inside.
	ProviderHost string `json:"provider_host"`
	// Terminal holds the values the profile takes from a terminal.
	Terminal ScratchTerminal `json:"terminal"`
	// TimeLimitSeconds is how long the start waits for the report line:
	// [MinTimeLimitSeconds] to [MaxTimeLimitSeconds].
	TimeLimitSeconds int `json:"time_limit_seconds"`
}

// ScratchTerminal is the terminal part of the sandbox inputs: "term" is
// required, the others are left out when the host has none.
type ScratchTerminal struct {
	Term      string `json:"term"`
	ColorTerm string `json:"colorterm,omitempty"`
	Lang      string `json:"lang,omitempty"`
	TZ        string `json:"tz,omitempty"`
}

// ScratchProvision is the provisioning part of the scratch config, the same for
// every lead.
type ScratchProvision struct {
	GitName         string `json:"git_name"`
	GitEmail        string `json:"git_email"`
	ManagedSettings string `json:"managed_settings"`
	GuardHook       string `json:"guard_hook"`
	ReplayScreens   string `json:"replay_screens"`
}

// ScratchLead is one lead of the scratch config.
type ScratchLead struct {
	// Key is the lead's name: [a-z0-9][a-z0-9-]* of at most
	// [MaxScratchKeyLen] bytes. The project is "scratch-<key>" and the role
	// "scratch-<key>/lead".
	Key string `json:"key"`
	// Source is the absolute path of the local repository the lead's mirror is
	// made from, which the owner chose; UpstreamBranch is the branch that is
	// fetched from it and that the tree starts from.
	Source         string `json:"source"`
	UpstreamBranch string `json:"upstream_branch"`
	// Agent, Inject and Limits are the canary config's, for this lead.
	Agent  AgentConfig `json:"agent"`
	Inject bool        `json:"inject"`
	Limits Limits      `json:"limits"`
	// ProfileDigest is the optional pin of this lead's sandbox profile digest,
	// [DigestLen] lower-case hexadecimal characters and not all zeros. The
	// daemon computes the digest itself when it defines the lead (with the
	// holder's own code); a pin that is there must equal it or the define is
	// refused. Without a pin the digest is the daemon's own computation, which
	// the holder then compares with its own: the two share one code, so the
	// holder's comparison no longer catches a change of the profile that is
	// made before the daemon computes.
	ProfileDigest string `json:"profile_digest,omitempty"`
	// RecordScreens is optional and false when absent: when true, the daemon
	// starts this lead's holder with --record-dir <the lead's runtime
	// folder>/record ([RecordDirName]), and the holder records the agent's
	// screens, titles and OSC 7501 payloads there, raw, for the owner's
	// fixtures. The daemon never reads the folder.
	RecordScreens bool `json:"record_screens,omitempty"`
}

// Validate checks every rule of the scratch config that does not need the file
// system, and derives and validates the config of each lead ([ScratchConfig.Leads]).
// The error is a *ConfigError.
func (s ScratchConfig) Validate() error {
	_, err := s.Leads()
	return err
}

// Leads returns the derived, validated config of each lead, in the order of the
// file, or the first refusal. Each is a [Config] of the scratch kind (Reserved
// is leadrole.ReservedScratch) whose role is "scratch-<key>/lead", whose
// sandbox inputs file is <state root>/scratch/<layout role>/sandbox-inputs.json
// and whose mirror is the layout's, <state root>/mirrors/scratch-<key>.git,
// made from the lead's source. Its holder profile digest is the lead's pin, or
// [UnpinnedDigest] when it has none ([Config.PinnedDigest]). Every rule of the
// canary config applies to each derived config as it does to the canary's.
func (s ScratchConfig) Leads() ([]Config, error) {
	if err := s.validateShared(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(s.Entries))
	out := make([]Config, 0, len(s.Entries))
	for i, l := range s.Entries {
		field := fmt.Sprintf("leads[%d]", i)
		if !keyOK(l.Key) {
			return nil, bad(field+".key", "must be [a-z0-9][a-z0-9-]* of 1 to %d bytes", MaxScratchKeyLen)
		}
		if seen[l.Key] {
			return nil, bad(field+".key", "repeats a key")
		}
		seen[l.Key] = true
		if l.ProfileDigest != "" &&
			(len(l.ProfileDigest) != DigestLen || !lowerHex(l.ProfileDigest) || l.ProfileDigest == UnpinnedDigest) {
			return nil, bad(field+".profile_digest", "must be %d lower-case hexadecimal characters and not all zeros", DigestLen)
		}
		cfg, err := s.derive(l)
		if err != nil {
			return nil, bad(field+".key", "is not the key of a valid scratch project")
		}
		if err := cfg.Validate(); err != nil {
			return nil, prefixed(field, err)
		}
		out = append(out, cfg)
	}
	return out, nil
}

// derive builds the config of one lead. It validates nothing but the role.
func (s ScratchConfig) derive(l ScratchLead) (Config, error) {
	role, err := leadrole.Lead(leadrole.ScratchPrefix + l.Key)
	if err != nil {
		return Config{}, err
	}
	layoutRole, _, err := NamesFor(leadrole.ReservedScratch, role)
	if err != nil {
		return Config{}, err
	}
	digest := l.ProfileDigest
	if digest == "" {
		digest = UnpinnedDigest
	}
	return Config{
		Version:     ConfigVersion,
		Role:        role,
		StateRoot:   s.StateRoot,
		RuntimeRoot: s.RuntimeRoot,
		Holder: HolderConfig{
			Executable:    s.Holder.Executable,
			Cols:          s.Holder.Cols,
			Rows:          s.Holder.Rows,
			SandboxInputs: path.Join(s.StateRoot, ScratchDirName, layoutRole, SandboxInputsName),
			ProfileDigest: digest,
		},
		Agent:         l.Agent,
		Inject:        l.Inject,
		RecordScreens: l.RecordScreens,
		Limits:        l.Limits,
		Provision: ProvisionConfig{
			Mirror:          path.Join(s.StateRoot, "mirrors", role.Project()+".git"),
			UpstreamBranch:  l.UpstreamBranch,
			Source:          l.Source,
			GitName:         s.Provision.GitName,
			GitEmail:        s.Provision.GitEmail,
			ManagedSettings: s.Provision.ManagedSettings,
			GuardHook:       s.Provision.GuardHook,
			ReplayScreens:   s.Provision.ReplayScreens,
		},
		Reserved: leadrole.ReservedScratch,
	}, nil
}

// validateShared checks what no lead decides: the version, the roots, the cap,
// the holder, the sandbox and the provisioning part, and the number of leads.
func (s ScratchConfig) validateShared() error {
	if s.Version != ConfigVersion {
		return bad("version", "must be %d", ConfigVersion)
	}
	if err := checkAbs("state_root", s.StateRoot); err != nil {
		return err
	}
	if err := checkAbs("runtime_root", s.RuntimeRoot); err != nil {
		return err
	}
	if s.MaxRunning < 1 || s.MaxRunning > MaxScratchLeads {
		return bad("max_running", "must be 1 to %d", MaxScratchLeads)
	}
	if err := s.Holder.validate(); err != nil {
		return err
	}
	if err := s.Sandbox.validate(); err != nil {
		return err
	}
	if err := s.Provision.validate(); err != nil {
		return err
	}
	if n := len(s.Entries); n < 1 || n > MaxScratchLeads {
		return bad("leads", "must hold 1 to %d leads", MaxScratchLeads)
	}
	return nil
}

func (h ScratchHolder) validate() error {
	if err := checkAbs("holder.executable", h.Executable); err != nil {
		return err
	}
	if h.Cols < MinCols || h.Cols > MaxCols {
		return bad("holder.cols", "must be %d to %d", MinCols, MaxCols)
	}
	if h.Rows < MinRows || h.Rows > MaxRows {
		return bad("holder.rows", "must be %d to %d", MinRows, MaxRows)
	}
	return nil
}

func (b ScratchSandbox) validate() error {
	for _, f := range []struct{ name, val string }{
		{"sandbox.agent_install", b.AgentInstall}, {"sandbox.pasta", b.Pasta},
		{"sandbox.bwrap", b.Bwrap}, {"sandbox.probe", b.Probe},
	} {
		if err := checkAbs(f.name, f.val); err != nil {
			return err
		}
	}
	if b.Pasta == b.Bwrap || b.Pasta == b.Probe || b.Bwrap == b.Probe {
		return bad("sandbox", "pasta, bwrap and probe must be three different files")
	}
	if b.ManagedMarker != "" && !plainText(b.ManagedMarker, 1, maxModeText) {
		return bad("sandbox.managed_marker", "must be plain text of 1 to %d bytes, or absent", maxModeText)
	}
	if len(b.NeverMounted) < 1 || len(b.NeverMounted) > maxNeverMounted {
		return bad("sandbox.never_mounted", "must hold 1 to %d paths", maxNeverMounted)
	}
	for i, p := range b.NeverMounted {
		if err := checkAbs(fmt.Sprintf("sandbox.never_mounted[%d]", i), p); err != nil {
			return err
		}
	}
	if !plainText(b.ProviderHost, 1, maxHostText) || strings.ContainsAny(b.ProviderHost, " /") {
		return bad("sandbox.provider_host", "must be a host name of plain text of 1 to %d bytes", maxHostText)
	}
	if !plainText(b.Terminal.Term, 1, maxTermText) {
		return bad("sandbox.terminal.term", "must be plain text of 1 to %d bytes", maxTermText)
	}
	for _, f := range []struct{ name, val string }{
		{"sandbox.terminal.colorterm", b.Terminal.ColorTerm}, {"sandbox.terminal.lang", b.Terminal.Lang},
		{"sandbox.terminal.tz", b.Terminal.TZ},
	} {
		if f.val != "" && !plainText(f.val, 1, maxTermText) {
			return bad(f.name, "must be plain text of 1 to %d bytes, or absent", maxTermText)
		}
	}
	if b.TimeLimitSeconds < MinTimeLimitSeconds || b.TimeLimitSeconds > MaxTimeLimitSeconds {
		return bad("sandbox.time_limit_seconds", "must be %d to %d", MinTimeLimitSeconds, MaxTimeLimitSeconds)
	}
	return nil
}

func (p ScratchProvision) validate() error {
	return ProvisionConfig{
		Mirror: "/x", UpstreamBranch: "x", GitName: p.GitName, GitEmail: p.GitEmail,
		ManagedSettings: p.ManagedSettings, GuardHook: p.GuardHook, ReplayScreens: p.ReplayScreens,
	}.validate()
}

// keyOK reports a lead key: [a-z0-9][a-z0-9-]* of 1 to [MaxScratchKeyLen] bytes.
func keyOK(k string) bool {
	if len(k) < 1 || len(k) > MaxScratchKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && c == '-')) {
			return false
		}
	}
	return true
}

// prefixed returns err with prefix and a dot before the field of its
// *ConfigError; any other error is returned as it is.
func prefixed(prefix string, err error) error {
	var ce *ConfigError
	if errors.As(err, &ce) {
		return &ConfigError{Field: prefix + "." + ce.Field, Reason: ce.Reason}
	}
	return err
}

// PinnedDigest returns the profile digest that the owner pinned for a scratch
// lead in the scratch config, and true; for a lead without a pin, and for a
// config that is not a scratch lead's, it returns "" and false.
func (c Config) PinnedDigest() (string, bool) {
	if c.Reserved != leadrole.ReservedScratch || c.Holder.ProfileDigest == UnpinnedDigest {
		return "", false
	}
	return c.Holder.ProfileDigest, true
}

// WithProfileDigest returns a copy of the config whose holder profile digest is
// digest, which must be [DigestLen] lower-case hexadecimal characters. The
// daemon uses it to set the digest it computed when it defined a scratch lead
// that has no pin, so that the holder is started with it.
func (c Config) WithProfileDigest(digest string) (Config, error) {
	if len(digest) != DigestLen || !lowerHex(digest) {
		return Config{}, bad("holder.profile_digest", "must be %d lower-case hexadecimal characters", DigestLen)
	}
	c.Holder.ProfileDigest = digest
	return c, nil
}

// CheckRoots runs the shared check of the two roots of the scratch place
// (canaryroot.CheckRoots): neither may be, lie inside or contain a default
// state or runtime folder of the account, and the two must not nest. It does
// not look at the root marker; the owner's command writes that and
// canaryroot.Check judges it.
func (s ScratchConfig) CheckRoots(getenv func(string) string) error {
	return canaryroot.CheckRoots(s.StateRoot, s.RuntimeRoot, getenv)
}

// scratchRequiredKeys are the keys of the scratch config that must be present,
// outside the leads. Optional: sandbox.managed_marker and the terminal's
// colorterm, lang and tz.
var scratchRequiredKeys = [][]string{
	{"version"}, {"state_root"}, {"runtime_root"}, {"max_running"}, {"leads"},
	{"holder", "executable"}, {"holder", "cols"}, {"holder", "rows"},
	{"sandbox", "agent_install"}, {"sandbox", "pasta"}, {"sandbox", "bwrap"}, {"sandbox", "probe"},
	{"sandbox", "never_mounted"}, {"sandbox", "provider_host"}, {"sandbox", "terminal", "term"},
	{"sandbox", "time_limit_seconds"},
	{"provision", "git_name"}, {"provision", "git_email"}, {"provision", "managed_settings"},
	{"provision", "guard_hook"}, {"provision", "replay_screens"},
}

// scratchLeadKeys are the keys every element of "leads" must hold. Optional:
// profile_digest, record_screens and agent.env.
var scratchLeadKeys = [][]string{
	{"key"}, {"source"}, {"upstream_branch"}, {"inject"},
	{"agent", "kind"}, {"agent", "runtime"}, {"agent", "runtime_version"},
	{"agent", "footer_mode"}, {"agent", "argv"},
	{"limits", "memory_max_bytes"}, {"limits", "swap_max_bytes"}, {"limits", "tasks_max"},
	{"limits", "memory_floor_bytes"}, {"limits", "memory_share_percent"},
}

// missingLeadKey returns the field name of the first key an element of "leads"
// lacks, or "".
func missingLeadKey(tree map[string]any) string {
	leads, _ := tree["leads"].([]any)
	for i, el := range leads {
		m, ok := el.(map[string]any)
		if !ok {
			return fmt.Sprintf("leads[%d]", i)
		}
		for _, key := range scratchLeadKeys {
			if !present(m, key) {
				return fmt.Sprintf("leads[%d].%s", i, strings.Join(key, "."))
			}
		}
	}
	return ""
}

// ReadScratchConfig reads the scratch config at path and checks it, with the
// rules of [ReadConfig] for the file: one regular file that the caller owns,
// mode 0600 exactly, one link, at most [MaxConfigBytes], opened once by its
// handle without following a link at its last element, and lying outside the
// state root and the runtime root it names. The content is one JSON object:
// valid UTF-8, no unknown field, no repeated key, no null, nothing after the
// object, version 1, and every key of the schema present (the optional ones are
// named in the types) with values that [ScratchConfig.Validate] accepts, the
// derived config of every lead included. Every refusal is a *ConfigError that
// never repeats a value, except for a failure to open or read the file, which
// wraps the system error.
//
// ReadScratchConfig does not look at the default roots or at the root marker;
// [ScratchConfig.CheckRoots] and the daemon's start do.
func ReadScratchConfig(path string) (ScratchConfig, error) {
	data, real, err := readConfigFile(path)
	if err != nil {
		return ScratchConfig{}, err
	}
	var cfg ScratchConfig
	if err := decodeStrict(data, &cfg, scratchRequiredKeys, missingLeadKey); err != nil {
		return ScratchConfig{}, err
	}
	if err := cfg.Validate(); err != nil {
		return ScratchConfig{}, err
	}
	if err := checkOutsideRoots(real, cfg.StateRoot, cfg.RuntimeRoot); err != nil {
		return ScratchConfig{}, err
	}
	return cfg, nil
}

// Validate checks the sandbox values that need no file system: the same rules
// [ScratchConfig.Validate] applies to the config's sandbox section. The error is
// a *ConfigError.
func (b ScratchSandbox) Validate() error { return b.validate() }
