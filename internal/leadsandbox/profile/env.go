package profile

import (
	"fmt"
	"sort"
	"strings"
)

// EnvVar is one environment variable.
type EnvVar struct {
	Name  string
	Value string
}

// Names of the variables the profile defines.
const (
	EnvPath       = "PATH"
	EnvHome       = "HOME"
	EnvUser       = "USER"
	EnvLogname    = "LOGNAME"
	EnvShell      = "SHELL"
	EnvTerm       = "TERM"
	EnvColorTerm  = "COLORTERM"
	EnvLang       = "LANG"
	EnvTZ         = "TZ"
	EnvTmpdir     = "TMPDIR"
	EnvXDGConfig  = "XDG_CONFIG_HOME"
	EnvXDGData    = "XDG_DATA_HOME"
	EnvXDGState   = "XDG_STATE_HOME"
	EnvXDGCache   = "XDG_CACHE_HOME"
	EnvGitCeiling = "GIT_CEILING_DIRECTORIES"
	EnvGitConfig  = "GIT_CONFIG_GLOBAL"
	EnvGoCache    = "GOCACHE"
	EnvGoModCache = "GOMODCACHE"
	EnvQargiLead  = "QARGI_LEAD"
)

const maxEnvNameLen = 64

// maxLeadIdentityLen is the longest QARGI_LEAD value.
const maxLeadIdentityLen = 64

// validLeadIdentity reports whether s has the shape of a role name ([a-z0-9]
// then [a-z0-9-]) and is at most 64 bytes.
func validLeadIdentity(s string) bool {
	if s == "" || len(s) > maxLeadIdentityLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Values of the fixed variables, in the lead's home.
const (
	GoCacheInside    = HomeInside + "/.cache/go-build"
	GoModCacheInside = HomeInside + "/go/pkg/mod"
)

// DefaultPath is the PATH of the profile when the inputs give none. The
// layout of the agent and Qargi install folders is an assumption that
// qualification settles.
const DefaultPath = "/ataniq/qargi/bin:/ataniq/agent/bin:/usr/local/bin:/usr/bin:/bin:/home/lead/.local/bin"

// DefaultShell is the SHELL of the profile when the inputs give none.
const DefaultShell = "/bin/bash"

// forbiddenEnv are names a profile may not add (compared in upper case).
var forbiddenEnv = map[string]bool{
	"SSH_AUTH_SOCK":            true,
	"DBUS_SESSION_BUS_ADDRESS": true,
	"XDG_RUNTIME_DIR":          true,
	"ANTHROPIC_API_KEY":        true,
	"OPENAI_API_KEY":           true,
}

// baseNames are the names every profile defines, optional ones included; the
// config variable of the epoch's runtime is added by baseNameSet.
var baseNames = []string{
	EnvPath, EnvHome, EnvUser, EnvLogname, EnvShell, EnvTerm, EnvColorTerm,
	EnvLang, EnvTZ, EnvTmpdir, EnvXDGConfig, EnvXDGData, EnvXDGState,
	EnvXDGCache, EnvGitCeiling, EnvGitConfig, EnvGoCache, EnvGoModCache,
	EnvQargiLead,
}

// forbiddenSuffixes are name endings that mark a credential (compared in
// upper case).
var forbiddenSuffixes = []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_KEY"}

// forbiddenPrefixes are name beginnings a profile may not add (compared in
// upper case): the loader's, git's configuration injection and the cloud
// credentials of AWS.
var forbiddenPrefixes = []string{"LD_", "GIT_CONFIG_", "AWS_"}

// allowedPrefixed are names that start with a forbidden prefix and are
// allowlisted. GIT_CONFIG_GLOBAL is a name the profile defines itself, so it
// is refused as an extra on other grounds.
var allowedPrefixed = map[string]bool{EnvGitConfig: true}

// CheckEnvName refuses a name a profile may not add: a name that is not a
// plain identifier (at most 64 bytes); SSH_AUTH_SOCK, DBUS_SESSION_BUS_ADDRESS,
// XDG_RUNTIME_DIR, ANTHROPIC_API_KEY and OPENAI_API_KEY; any name that ends
// _API_KEY, _TOKEN, _SECRET, _PASSWORD or _KEY; and any name that starts LD_,
// GIT_CONFIG_ (other than GIT_CONFIG_GLOBAL) or AWS_. Names are compared in
// upper case. The list is a starting point for the owner to review and
// extend, not a complete list of what a credential or a loader override can be
// called.
func CheckEnvName(name string) error {
	field := "environment name " + quoteName(name)
	if name == "" || len(name) > maxEnvNameLen {
		return refuse("environment name", "must be 1 to %d characters", maxEnvNameLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
		if i > 0 {
			ok = ok || c >= '0' && c <= '9'
		}
		if !ok {
			return refuse(field, "must match [A-Za-z_][A-Za-z0-9_]*")
		}
	}
	up := strings.ToUpper(name)
	if forbiddenEnv[up] {
		return refuse(field, "a profile may not add it (no agent socket, session bus or provider key)")
	}
	for _, suffix := range forbiddenSuffixes {
		if strings.HasSuffix(up, suffix) {
			return refuse(field, "a profile may not add a name that ends %s (it looks like a credential)", strings.Join(forbiddenSuffixes, ", "))
		}
	}
	if !allowedPrefixed[up] {
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(up, prefix) {
				return refuse(field, "a profile may not add a name that starts with %s (loader, git configuration or cloud credentials)", strings.Join(forbiddenPrefixes, ", "))
			}
		}
	}
	return nil
}

func quoteName(name string) string {
	if len(name) > 32 {
		name = name[:32] + "..."
	}
	return fmt.Sprintf("%q", name)
}

// checkEnv validates the environment inputs.
func (in Inputs) checkEnv() error {
	if in.Term == "" {
		return refuse("TERM", "is required: the host terminal gives it")
	}
	if in.LeadIdentity != "" && !validLeadIdentity(in.LeadIdentity) {
		return refuse("lead identity", "must be 1 to %d characters of [a-z0-9-], not starting with '-'", maxLeadIdentityLen)
	}
	for _, v := range []struct{ field, val string }{
		{"TERM", in.Term}, {"COLORTERM", in.ColorTerm}, {"LANG", in.Lang},
		{"TZ", in.TZ},
	} {
		if err := CheckValue(v.field, v.val); err != nil {
			return err
		}
	}
	if in.Path != "" {
		if err := checkPathList(in.Path); err != nil {
			return err
		}
	}
	if in.Shell != "" {
		if err := CheckPath("SHELL", in.Shell); err != nil {
			return err
		}
	}
	if len(in.ExtraEnv) > maxExtraEnv {
		return refuse("extra environment", "more than %d names", maxExtraEnv)
	}
	reserved := map[string]bool{}
	for _, n := range baseNames {
		reserved[n] = true
	}
	for _, rt := range Runtimes() {
		reserved[rt.ConfigEnvName()] = true
	}
	seen := map[string]bool{}
	for _, e := range in.ExtraEnv {
		if err := CheckEnvName(e.Name); err != nil {
			return err
		}
		field := "environment name " + quoteName(e.Name)
		if reserved[e.Name] {
			return refuse(field, "the profile defines it already, or it names a runtime's config folder")
		}
		if seen[e.Name] {
			return refuse(field, "is added twice")
		}
		seen[e.Name] = true
		if err := CheckValue("environment value of "+e.Name, e.Value); err != nil {
			return err
		}
	}
	return nil
}

func checkPathList(list string) error {
	if len(list) > maxPathLen {
		return refuse("PATH", "is longer than %d bytes", maxPathLen)
	}
	for _, p := range strings.Split(list, ":") {
		if err := CheckPath("PATH element", p); err != nil {
			return err
		}
	}
	return nil
}

// LeadIdentityValue is the value of QARGI_LEAD: the given lead identity, else
// the role.
func (in Inputs) LeadIdentityValue() string {
	if in.LeadIdentity != "" {
		return in.LeadIdentity
	}
	return in.Role
}

// Environment validates the inputs and returns the allowlist with its values,
// sorted by name. COLORTERM, LANG and TZ are present only when the inputs give
// them. Nothing is read from the environment of the calling process.
func (in Inputs) Environment() ([]EnvVar, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	return in.environment(), nil
}

func (in Inputs) environment() []EnvVar {
	user := "root"
	if in.Identity == IdentityOwner {
		user = in.OwnerUser
	}
	pathVal, shell := in.Path, in.Shell
	if pathVal == "" {
		pathVal = DefaultPath
	}
	if shell == "" {
		shell = DefaultShell
	}
	env := []EnvVar{
		{EnvPath, pathVal},
		{EnvHome, HomeInside},
		{EnvUser, user},
		{EnvLogname, user},
		{EnvShell, shell},
		{EnvTerm, in.Term},
		{EnvTmpdir, TmpInside},
		{EnvXDGConfig, HomeInside + "/.config"},
		{EnvXDGData, HomeInside + "/.local/share"},
		{EnvXDGState, HomeInside + "/.local/state"},
		{EnvXDGCache, HomeInside + "/.cache"},
		{in.Runtime.ConfigEnvName(), ConfigInside(in.Runtime)},
		{EnvGitCeiling, WorkRootInside},
		{EnvGitConfig, GitIdentityInside},
		{EnvGoCache, GoCacheInside},
		{EnvGoModCache, GoModCacheInside},
		{EnvQargiLead, in.LeadIdentityValue()},
	}
	for _, o := range []EnvVar{{EnvColorTerm, in.ColorTerm}, {EnvLang, in.Lang}, {EnvTZ, in.TZ}} {
		if o.Value != "" {
			env = append(env, o)
		}
	}
	env = append(env, in.ExtraEnv...)
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })
	return env
}

// EnvNames is every name the profile defines for the epoch, sorted: the fixed
// names (COLORTERM, LANG and TZ included, whether or not the host has them),
// the config variable of the runtime, and the names a profile added.
func (in Inputs) EnvNames() []string {
	names := append([]string(nil), baseNames...)
	if n := in.Runtime.ConfigEnvName(); n != "" {
		names = append(names, n)
	}
	for _, e := range in.ExtraEnv {
		names = append(names, e.Name)
	}
	return sortedCopy(names)
}

// EnvDiffKind says how an observed variable differs from the allowlist.
type EnvDiffKind string

const (
	// EnvMissing: the allowlist has the name and the observed environment does not.
	EnvMissing EnvDiffKind = "missing"
	// EnvUnexpected: the observed environment has a name the allowlist does not.
	EnvUnexpected EnvDiffKind = "unexpected"
	// EnvChanged: the name is in both and the values differ.
	EnvChanged EnvDiffKind = "changed"
	// EnvDuplicate: the observed environment sets the name more than once.
	EnvDuplicate EnvDiffKind = "duplicate"
	// EnvMalformed: an observed entry has no name or no '='.
	EnvMalformed EnvDiffKind = "malformed"
	// EnvTruncated: more differences than MaxEnvDiffs; its Name says how
	// many are not listed.
	EnvTruncated EnvDiffKind = "truncated"
)

// MaxEnvDiffs is the most differences DiffEnvironment returns, the
// EnvTruncated one not counted.
const MaxEnvDiffs = 64

// maxDiffName is the longest name kept in a difference.
const maxDiffName = 64

// EnvDiff is one difference. Name is clipped to 64 bytes. Want and Got are
// set only for EnvChanged, whose name is in the allowlist; the value of an
// unexpected variable is never kept, because it may be a secret.
type EnvDiff struct {
	Kind EnvDiffKind
	Name string
	Want string
	Got  string
}

// String is the difference as one line for a report. The name is quoted, so
// an observed name with a newline or another control byte cannot forge or
// split a line.
func (d EnvDiff) String() string {
	if d.Kind == EnvChanged {
		return fmt.Sprintf("changed %q: want %q, got %q", d.Name, d.Want, d.Got)
	}
	return fmt.Sprintf("%s %q", d.Kind, d.Name)
}

// DiffEnvironment compares an observed environment, in the form of
// os.Environ ("NAME=value"), with the allowlist exactly, names and values, and
// returns the differences sorted by name and then kind, at most MaxEnvDiffs
// of them followed by one EnvTruncated difference when there are more. An
// empty result means the two are equal. It refuses an allowlist that lists a
// name twice, because it would be ambiguous which value is wanted.
func DiffEnvironment(want []EnvVar, observed []string) ([]EnvDiff, error) {
	wantMap := make(map[string]string, len(want))
	for _, w := range want {
		if _, dup := wantMap[w.Name]; dup {
			return nil, refuse("environment allowlist", "the name %s is listed twice", quoteName(w.Name))
		}
		wantMap[w.Name] = w.Value
	}
	var diffs []EnvDiff
	count := map[string]int{}
	got := map[string]string{}
	for i, entry := range observed {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			diffs = append(diffs, EnvDiff{Kind: EnvMalformed, Name: fmt.Sprintf("(entry %d)", i)})
			continue
		}
		count[name]++
		if count[name] == 1 {
			got[name] = value
		}
	}
	for name, n := range count {
		display := clipName(name)
		if n > 1 {
			diffs = append(diffs, EnvDiff{Kind: EnvDuplicate, Name: display})
		}
		wantVal, listed := wantMap[name]
		switch {
		case !listed:
			diffs = append(diffs, EnvDiff{Kind: EnvUnexpected, Name: display})
		case got[name] != wantVal:
			diffs = append(diffs, EnvDiff{Kind: EnvChanged, Name: display, Want: wantVal, Got: clip(got[name])})
		}
	}
	for _, w := range want {
		if count[w.Name] == 0 {
			diffs = append(diffs, EnvDiff{Kind: EnvMissing, Name: clipName(w.Name)})
		}
	}
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].Name != diffs[j].Name {
			return diffs[i].Name < diffs[j].Name
		}
		return diffs[i].Kind < diffs[j].Kind
	})
	if len(diffs) > MaxEnvDiffs {
		more := len(diffs) - MaxEnvDiffs
		diffs = append(diffs[:MaxEnvDiffs:MaxEnvDiffs], EnvDiff{Kind: EnvTruncated, Name: fmt.Sprintf("(%d more)", more)})
	}
	return diffs, nil
}

func clipName(name string) string {
	if len(name) > maxDiffName {
		return name[:maxDiffName] + "..."
	}
	return name
}

func clip(s string) string {
	if len(s) > maxValueLen {
		return s[:maxValueLen] + "..."
	}
	return s
}
