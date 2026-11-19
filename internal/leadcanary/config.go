//go:build linux && ataniq_canary

package leadcanary

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/profile"
)

// ConfigVersion is the only version of the config that is read.
const ConfigVersion = 1

// Bounds of the config file and of what it carries.
const (
	// MaxConfigBytes is the largest config file, in bytes.
	MaxConfigBytes = 64 << 10
	// MinCols, MaxCols, MinRows and MaxRows bound the holder's screen. They are
	// the bounds of the holder's own command line (internal/leadholder/session
	// clamps to them, and a test holds that they are equal).
	MinCols = 20
	MaxCols = 500
	MinRows = 5
	MaxRows = 200

	maxPath       = 4096
	maxArgv       = 64
	maxArg        = 4096
	maxEnv        = 64
	maxEnvKey     = 128
	maxEnvValue   = 4096
	maxModeText   = 128
	maxGitText    = 128
	maxBranchText = 128
	// DigestLen is the length of holder.profile_digest, in lower-case
	// hexadecimal characters: a SHA-256, as the registry's profile digest.
	DigestLen = 64
)

// AgentKind says what runs inside the sandbox. It goes into the evidence.
type AgentKind string

const (
	// AgentStandIn is the stand-in agent of the tests and the first pass.
	AgentStandIn AgentKind = "stand-in"
	// AgentLive is a real agent.
	AgentLive AgentKind = "live"
)

// ErrConfig is wrapped by every refusal of a config, by ReadConfig and by
// [Config.Validate]. The error is a *ConfigError that names the field and a
// fixed reason, and never repeats a value.
var ErrConfig = errors.New("leadcanary: invalid config")

// ConfigError is the error of a refused config.
type ConfigError struct {
	// Field is the JSON name of the field, such as "limits.tasks_max", or the
	// word "file" for a fault of the file itself.
	Field string
	// Reason is a fixed text.
	Reason string
}

// Error returns "leadcanary: invalid config: <field>: <reason>".
func (e *ConfigError) Error() string {
	return "leadcanary: invalid config: " + e.Field + ": " + e.Reason
}

// Unwrap returns [ErrConfig].
func (e *ConfigError) Unwrap() error { return ErrConfig }

func bad(field, format string, args ...any) error {
	return &ConfigError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Config is the canary config. See the package comment and ReadConfig. Every
// field is required except Agent.Env, which may be absent or empty; none has a
// default in code.
type Config struct {
	// Version is 1.
	Version int `json:"version"`
	// Role is the one role of the run: the lead of a throwaway project.
	Role leadrole.Role `json:"role"`
	// StateRoot and RuntimeRoot are the two roots of the run. They are
	// absolute clean paths, and neither is a default root of the account
	// ([Config.CheckRoots]).
	StateRoot   string `json:"state_root"`
	RuntimeRoot string `json:"runtime_root"`
	// Holder says how the holder is started.
	Holder HolderConfig `json:"holder"`
	// Agent says what runs in the sandbox.
	Agent AgentConfig `json:"agent"`
	// Inject opens the injection gate of the config; the holder gets
	// "--inject" only when it is true.
	Inject bool `json:"inject"`
	// Limits are the unit's resource limits.
	Limits Limits `json:"limits"`
	// Provision names what the provisioning command copies and sets.
	Provision ProvisionConfig `json:"provision"`
}

// HolderConfig is the holder part of the config.
type HolderConfig struct {
	// Executable is the absolute path of the holder binary.
	Executable string `json:"executable"`
	// Cols and Rows are the size of the holder's screen: MinCols to MaxCols
	// and MinRows to MaxRows.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	// SandboxInputs is the absolute path of the sandbox inputs file the
	// holder reads (--sandbox-inputs). The daemon never reads it.
	SandboxInputs string `json:"sandbox_inputs"`
	// ProfileDigest is the pinned digest of the sandbox profile, DigestLen
	// lower-case hexadecimal characters. The holder compares it with the digest
	// its start sequence computes and refuses on a mismatch.
	ProfileDigest string `json:"profile_digest"`
}

// AgentConfig is the agent part of the config.
type AgentConfig struct {
	// Kind is AgentStandIn or AgentLive.
	Kind AgentKind `json:"kind"`
	// Runtime and RuntimeVersion are given to the holder as --runtime and
	// --runtime-version: plain text of 1 to 64 bytes that the holder's attach
	// protocol accepts.
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	// FooterMode is the permission mode the fence expects in the agent's
	// footer (--expected-mode): plain text of 1 to 128 bytes.
	FooterMode string `json:"footer_mode"`
	// Argv is the agent's command, an absolute executable first. It goes
	// after "--" on the holder's command line.
	Argv []string `json:"argv"`
	// Env is the agent's whole environment, KEY=VALUE, at most 64 entries.
	// It becomes one --env flag each; the holder inherits nothing for it.
	Env []string `json:"env,omitempty"`
}

// Limits are the resource limits of the unit, in the config's words. The first
// three go to the unit; the floor and the share are for the script that
// balances the memory limit against what the host has free. All five are
// required: SwapMaxBytes may be 0 (no swap), the others are above zero, the
// floor is at most the ceiling and the share is 1 to 100.
type Limits struct {
	MemoryMaxBytes     uint64 `json:"memory_max_bytes"`
	SwapMaxBytes       uint64 `json:"swap_max_bytes"`
	TasksMax           uint64 `json:"tasks_max"`
	MemoryFloorBytes   uint64 `json:"memory_floor_bytes"`
	MemorySharePercent int    `json:"memory_share_percent"`
}

// ProvisionConfig is what the provisioning command uses. The daemon ignores
// it beyond checking it.
type ProvisionConfig struct {
	// Mirror is the absolute path of the exchange mirror the tree is cloned
	// from.
	Mirror string `json:"mirror"`
	// UpstreamBranch is the branch the tree starts from.
	UpstreamBranch string `json:"upstream_branch"`
	// GitName and GitEmail are the lead's git identity.
	GitName  string `json:"git_name"`
	GitEmail string `json:"git_email"`
	// ManagedSettings and GuardHook are the absolute paths of the files the
	// provisioning copies into place; ReplayScreens is the folder of the
	// stand-in's replay fixtures.
	ManagedSettings string `json:"managed_settings"`
	GuardHook       string `json:"guard_hook"`
	ReplayScreens   string `json:"replay_screens"`
}

// Validate checks every rule of the config that does not need the file system:
// the version, the role, the paths, the holder and agent fields, the five
// limits and the provisioning fields. ReadConfig calls it after it has read the
// file; a caller that builds a Config in memory calls it before it uses one.
// The error is a *ConfigError.
func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return bad("version", "must be %d", ConfigVersion)
	}
	if _, _, err := Names(c.Role); err != nil {
		return bad("role", "must be the lead of a throwaway project whose layout name fits")
	}
	if err := checkAbs("state_root", c.StateRoot); err != nil {
		return err
	}
	if err := checkAbs("runtime_root", c.RuntimeRoot); err != nil {
		return err
	}
	if err := c.Holder.validate(); err != nil {
		return err
	}
	if err := c.Agent.validate(); err != nil {
		return err
	}
	if err := c.Limits.validate(); err != nil {
		return err
	}
	return c.Provision.validate()
}

func (h HolderConfig) validate() error {
	if err := checkAbs("holder.executable", h.Executable); err != nil {
		return err
	}
	if h.Cols < MinCols || h.Cols > MaxCols {
		return bad("holder.cols", "must be %d to %d", MinCols, MaxCols)
	}
	if h.Rows < MinRows || h.Rows > MaxRows {
		return bad("holder.rows", "must be %d to %d", MinRows, MaxRows)
	}
	if err := checkAbs("holder.sandbox_inputs", h.SandboxInputs); err != nil {
		return err
	}
	if len(h.ProfileDigest) != DigestLen || !lowerHex(h.ProfileDigest) {
		return bad("holder.profile_digest", "must be %d lower-case hexadecimal characters", DigestLen)
	}
	return nil
}

func (a AgentConfig) validate() error {
	switch a.Kind {
	case AgentStandIn, AgentLive:
	default:
		return bad("agent.kind", "must be %q or %q", AgentStandIn, AgentLive)
	}
	if !wireAccepts(func(w *wire.Welcome) { w.Runtime = a.Runtime }) || a.Runtime == "" || a.Runtime == "--" {
		return bad("agent.runtime", "must be plain text of 1 to %d bytes", wire.MaxRuntime)
	}
	if !wireAccepts(func(w *wire.Welcome) { w.RuntimeVersion = a.RuntimeVersion }) || a.RuntimeVersion == "" || a.RuntimeVersion == "--" {
		return bad("agent.runtime_version", "must be plain text of 1 to %d bytes", wire.MaxRuntime)
	}
	if !plainText(a.FooterMode, 1, maxModeText) || a.FooterMode == "--" {
		return bad("agent.footer_mode", "must be plain text of 1 to %d bytes", maxModeText)
	}
	if len(a.Argv) == 0 || len(a.Argv) > maxArgv {
		return bad("agent.argv", "must hold 1 to %d elements", maxArgv)
	}
	if err := checkAbs("agent.argv[0]", a.Argv[0]); err != nil {
		return err
	}
	for i, arg := range a.Argv[1:] {
		if !plainText(arg, 0, maxArg) {
			return bad(fmt.Sprintf("agent.argv[%d]", i+1), "must be plain text of at most %d bytes", maxArg)
		}
	}
	if len(a.Env) > maxEnv {
		return bad("agent.env", "must hold at most %d entries", maxEnv)
	}
	seen := make(map[string]bool, len(a.Env))
	for i, kv := range a.Env {
		key, ok := checkEnvPair(kv)
		if !ok {
			return bad(fmt.Sprintf("agent.env[%d]", i), "must be KEY=VALUE: KEY of letters, digits and _ and not starting with a digit, VALUE of plain text")
		}
		if seen[key] {
			return bad(fmt.Sprintf("agent.env[%d]", i), "repeats a key")
		}
		seen[key] = true
	}
	return nil
}

func (l Limits) validate() error {
	if l.MemoryMaxBytes < MinMemoryMax || l.MemoryMaxBytes > MaxMemoryMax {
		return bad("limits.memory_max_bytes", "must be %d to %d", MinMemoryMax, MaxMemoryMax)
	}
	if l.SwapMaxBytes > MaxSwapMax {
		return bad("limits.swap_max_bytes", "must be %d to %d", MinSwapMax, MaxSwapMax)
	}
	if l.TasksMax < MinTasksMax || l.TasksMax > MaxTasksMax {
		return bad("limits.tasks_max", "must be %d to %d", MinTasksMax, MaxTasksMax)
	}
	if l.MemoryFloorBytes < MinMemoryFloor || l.MemoryFloorBytes > l.MemoryMaxBytes {
		return bad("limits.memory_floor_bytes", "must be %d up to memory_max_bytes", MinMemoryFloor)
	}
	if l.MemorySharePercent < MinSharePercent || l.MemorySharePercent > MaxSharePercent {
		return bad("limits.memory_share_percent", "must be %d to %d", MinSharePercent, MaxSharePercent)
	}
	return nil
}

func (p ProvisionConfig) validate() error {
	if err := checkAbs("provision.mirror", p.Mirror); err != nil {
		return err
	}
	if !branchOK(p.UpstreamBranch) {
		return bad("provision.upstream_branch", "must be a plain branch name of 1 to %d bytes", maxBranchText)
	}
	if !plainText(p.GitName, 1, maxGitText) || strings.ContainsAny(p.GitName, "<>") {
		return bad("provision.git_name", "must be plain text of 1 to %d bytes without < or >", maxGitText)
	}
	if !emailOK(p.GitEmail) {
		return bad("provision.git_email", "must be one address of 3 to %d bytes", maxGitText)
	}
	for _, f := range []struct{ name, val string }{
		{"provision.managed_settings", p.ManagedSettings},
		{"provision.guard_hook", p.GuardHook},
		{"provision.replay_screens", p.ReplayScreens},
	} {
		if err := checkAbs(f.name, f.val); err != nil {
			return err
		}
	}
	return nil
}

// CheckRoots runs the shared root check of canary mode
// (canaryroot.Check) on the config's roots and project: the project must carry
// the throwaway prefix, neither root may be, lie inside or contain a default
// state or runtime folder of the account (the environment's, through getenv,
// and the account's), the two roots must not nest, and the state root must
// carry the marker that provisioning wrote for the project. The daemon calls it
// before it listens. The error is a *canaryroot.RefusedError.
func (c Config) CheckRoots(getenv func(string) string) error {
	return canaryroot.Check(c.StateRoot, c.RuntimeRoot, c.Role.Project(), getenv)
}

// Layout returns the host layout of the role under the state root. It fails
// when the role is not the canary role or the roots are not valid for the
// layout.
func (c Config) Layout() (profile.Layout, error) {
	layoutRole, _, err := Names(c.Role)
	if err != nil {
		return profile.Layout{}, err
	}
	l := profile.Layout{StateRoot: c.StateRoot, Role: layoutRole, Project: c.Role.Project()}
	if err := l.Validate(); err != nil {
		return profile.Layout{}, err
	}
	return l, nil
}

// HolderRuntimeDir returns the holder's runtime folder,
// "<runtime root>/leads/<layout role>", which the holder is given as
// --runtime-dir. Its "report" folder is where the probe's report FIFO lies.
func (c Config) HolderRuntimeDir() (string, error) {
	layoutRole, _, err := Names(c.Role)
	if err != nil {
		return "", err
	}
	return path.Join(c.RuntimeRoot, "leads", layoutRole), nil
}

// --- text and path rules ---

// plainText reports valid UTF-8 of min to max bytes without a control, format,
// line-separator or replacement character. It is the wire's rule for a name
// plus the replacement character, which a JSON string gets for a lone
// surrogate escape.
func plainText(s string, min, max int) bool {
	if len(s) < min || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == utf8.RuneError || r < 0x20 || (r >= 0x7f && r < 0xa0) ||
			unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// checkAbs refuses a path that is not absolute, clean, plain text of at most
// 4096 bytes, or is the root folder.
func checkAbs(field, p string) error {
	if !absClean(p) {
		return bad(field, "must be an absolute clean path of plain text, at most %d bytes", maxPath)
	}
	return nil
}

func absClean(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p && plainText(p, 1, maxPath)
}

func lowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// wireAccepts reports whether the attach protocol accepts the Welcome that
// set builds, as the holder's command line does for its role, runtime and
// runtime version.
func wireAccepts(set func(*wire.Welcome)) bool {
	w := wire.Welcome{Version: wire.ProtocolVersion, Role: wire.RoleViewer, Lead: "x", Epoch: 1, Runtime: "x", RuntimeVersion: "x", Caps: wire.DefaultCaps()}
	set(&w)
	_, err := wire.EncodeWelcome(w)
	return err == nil
}

// checkEnvPair checks KEY=VALUE as the holder's --env does and returns the key.
func checkEnvPair(kv string) (string, bool) {
	key, val, ok := strings.Cut(kv, "=")
	if !ok || len(key) < 1 || len(key) > maxEnvKey || len(val) > maxEnvValue || !plainText(val, 0, maxEnvValue) {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return key, true
}

// branchOK accepts a plain branch name: letters, digits and . _ - /, starting
// with a letter or digit, with no "..", no "//" and no ending in "/" or ".".
func branchOK(s string) bool {
	if len(s) < 1 || len(s) > maxBranchText {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._-/", rune(c))) {
			return false
		}
	}
	return !strings.Contains(s, "..") && !strings.Contains(s, "//") && !strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".")
}

// emailOK accepts one plain address: no space, no angle bracket, one "@" with
// text on both sides.
func emailOK(s string) bool {
	if !plainText(s, 3, maxGitText) || strings.ContainsAny(s, " <>") || strings.Count(s, "@") != 1 {
		return false
	}
	return !strings.HasPrefix(s, "@") && !strings.HasSuffix(s, "@")
}
