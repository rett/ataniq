package profile

import (
	"path"
	"strconv"
	"strings"
)

// IdentityMode says who the lead is inside the sandbox.
type IdentityMode string

const (
	// IdentityRoot is UID and GID 0 inside, mapped to the owner outside, as
	// tested for the design. Tools that run as UID 0 are untested.
	IdentityRoot IdentityMode = "root"
	// IdentityOwner is the stated fallback: the owner's own ids inside.
	IdentityOwner IdentityMode = "owner"
)

// Placeholder is an extra bind target in the lead's work tree, bound read-only
// from a host path over the tree. It is a lead-writable target, so it is made
// by Ataniq before the lead's first run and never by bubblewrap at start.
type Placeholder struct {
	// Rel is the target relative to the tree: clean, not above the tree, not
	// the .git folder or below it.
	Rel string
	// Source is the host path bound over it.
	Source string
	// Dir says the target is a folder (else a file).
	Dir bool
}

// Inputs is every host-side source the profile needs. Nothing here is read
// from the environment: the caller gives all of it, and Validate refuses what
// the profile cannot use. All host paths are absolute and clean, and the
// caller has resolved any symbolic link in them.
type Inputs struct {
	// StateRoot, Role and Project give the host layout (see Layout).
	StateRoot string
	Role      string
	Project   string

	// Runtime is the epoch's runtime and RuntimeVersion its pinned version.
	Runtime        Runtime
	RuntimeVersion string

	// Identity is the identity mode. OwnerUID, OwnerGID and OwnerUser are
	// given only for IdentityOwner (and then are required, and non-zero).
	Identity  IdentityMode
	OwnerUID  uint32
	OwnerGID  uint32
	OwnerUser string

	// AgentInstall is the agent's install folder for the pinned version.
	AgentInstall string
	// ProbeBinary is the static probe, bound at ProbeInside.
	ProbeBinary string
	// SpecDir is the folder that holds the probe spec, bound at SpecDirInside.
	SpecDir string

	// ManagedSettings is the managed-settings file written by Ataniq.
	// ManagedSettingsInside is where it appears: for claude it is
	// ClaudeManagedInside (empty means that, and no other path is accepted,
	// because claude reads its policy only there); for codex it must be
	// given, because the design names no path for codex's managed
	// configuration, and it must lie under /etc, outside every lead-writable
	// folder.
	ManagedSettings       string
	ManagedSettingsInside string
	// ManagedSettingsSHA256 is the SHA-256 (64 lower-case hex digits) of the
	// managed-settings file's content: required, and part of the digest,
	// because the file is the policy half of the profile. The package does
	// not read the file or compare the digest.
	ManagedSettingsSHA256 string

	// LeadSkills is the Ataniq-owned folder bound over the skills folder of
	// the config folder; InstructionFile is the Ataniq-owned instruction file
	// bound over its place in the config folder.
	LeadSkills      string
	InstructionFile string

	// GitIdentityFile is bound at GitIdentityInside and named by
	// GIT_CONFIG_GLOBAL.
	GitIdentityFile string

	// GitMeta is the Ataniq-owned folder of the tree's protected git files:
	// config (a file), hooks and info (folders). The tree's .git/config,
	// .git/hooks and .git/info are bound read-only over the tree from the
	// entries of this folder, not from the tree itself, because bubblewrap
	// follows a symbolic link in a bind source and the lead writes the tree.
	// It is required and must be Layout.GitMeta, <state>/leads/<role>/gitmeta:
	// beside the lead's home, config folder and tree, which are never bound
	// as a whole, so the lead cannot reach it, and below the state root, so
	// the holder's lookup from the state root without links covers its whole
	// path.
	GitMeta string

	// QargiInstall is the Qargi install, bound at QargiInside.
	QargiInstall string

	// QargiRecords is the lead's working Qargi records (optional): the host
	// folder and the inside path it is bound at, read-write. When present the
	// host folder must be Layout.QargiRecords, <state>/leads/<role>/qargi: below
	// the lead's own state folder, so that every rule about another lead's
	// state covers it (a folder the lead writes at any other place could be,
	// or lie above, a source that another lead's sandbox binds). The inside
	// path is the caller's, under the rules below.
	QargiRecords       string
	QargiRecordsInside string

	// AssignmentDir and ProjectionDir are written outside the sandbox and
	// bound read-only, each as a folder.
	AssignmentDir string
	ProjectionDir string

	// ResolvConf is the resolv.conf file the holder wrote.
	ResolvConf string

	// SeccompFile and SeccompSHA256 name a reviewed BPF file and its SHA-256
	// (64 lower-case hex digits). Both or none. The package does not read the
	// file or compare the digest.
	SeccompFile   string
	SeccompSHA256 string

	// ExtraPlaceholders are extra bind targets in the work tree.
	ExtraPlaceholders []Placeholder

	// NeverMounted are the host paths no bind source may equal, lie below or
	// lie above: the owner's checkout, the real provider folders, .ssh, and
	// whatever else the caller names. At least one is required. The other
	// runtime's config folder is always added.
	NeverMounted []string

	// Environment inputs. Term is required; ColorTerm, Lang and TZ are left
	// empty when the host terminal has none, and then the variable is not set.
	// Path and Shell override the profile's defaults. LeadIdentity is the
	// value of QARGI_LEAD and defaults to Role. ExtraEnv is what a profile
	// adds to the allowlist.
	Term         string
	ColorTerm    string
	Lang         string
	TZ           string
	Path         string
	Shell        string
	LeadIdentity string
	ExtraEnv     []EnvVar
}

// Layout is the host layout the inputs name.
func (in Inputs) Layout() Layout {
	return Layout{StateRoot: in.StateRoot, Role: in.Role, Project: in.Project}
}

// OtherConfigDir is the host path of the other runtime's config folder, which
// is never mounted. It is "" when the runtime is not valid.
func (in Inputs) OtherConfigDir() string {
	dir, err := in.Layout().ConfigDir(in.Runtime.Other())
	if err != nil {
		return ""
	}
	return dir
}

// ManagedInside is the managed-settings path inside: the given path, else the
// default of the runtime ("" for codex).
func (in Inputs) ManagedInside() string {
	if in.ManagedSettingsInside != "" {
		return in.ManagedSettingsInside
	}
	return ManagedSettingsDefaultInside(in.Runtime)
}

// SeccompEnabled says a reviewed BPF file is named.
func (in Inputs) SeccompEnabled() bool { return in.SeccompFile != "" }

// Validate refuses inputs the profile cannot use: a missing input, a relative
// or unclean path, an invalid name, a source that a never-mounted path
// covers, the other runtime's config folder, a read-only source that overlaps
// a folder the lead can write, or a bind table that is not valid for
// bubblewrap. The error is a *Refusal.
func (in Inputs) Validate() error {
	if err := in.check(); err != nil {
		return err
	}
	return in.checkTable()
}

// check validates the fields one by one, before the table is built.
func (in Inputs) check() error {
	if err := in.Layout().Validate(); err != nil {
		return err
	}
	if !in.Runtime.Valid() {
		return refuse("runtime", "must be %q or %q", Claude, Codex)
	}
	if err := checkVersion("runtime version", in.RuntimeVersion); err != nil {
		return err
	}
	if err := in.checkIdentity(); err != nil {
		return err
	}
	required := []struct{ field, p string }{
		{"agent install folder", in.AgentInstall},
		{"probe binary", in.ProbeBinary},
		{"spec folder", in.SpecDir},
		{"managed-settings file", in.ManagedSettings},
		{"lead skills folder", in.LeadSkills},
		{"instruction file", in.InstructionFile},
		{"git identity file", in.GitIdentityFile},
		{"git metadata folder", in.GitMeta},
		{"Qargi install", in.QargiInstall},
		{"assignment folder", in.AssignmentDir},
		{"projection folder", in.ProjectionDir},
		{"resolv.conf file", in.ResolvConf},
	}
	for _, r := range required {
		if err := CheckPath(r.field, r.p); err != nil {
			return err
		}
	}
	if err := in.checkManaged(); err != nil {
		return err
	}
	if err := in.checkRecords(); err != nil {
		return err
	}
	if err := checkSHA256("managed-settings digest", in.ManagedSettingsSHA256); err != nil {
		return err
	}
	if err := in.checkSeccomp(); err != nil {
		return err
	}
	if err := in.checkPlaceholders(); err != nil {
		return err
	}
	if err := in.checkNeverList(); err != nil {
		return err
	}
	if err := in.checkGitMeta(); err != nil {
		return err
	}
	return in.checkEnv()
}

func checkVersion(field, v string) error {
	if v == "" || len(v) > maxVersionLen {
		return refuse(field, "must be 1 to %d characters", maxVersionLen)
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if i > 0 {
			ok = ok || c == '.' || c == '-' || c == '_' || c == '+'
		}
		if !ok {
			return refuse(field, "must start with a letter or digit and hold only letters, digits and . - _ +")
		}
	}
	return nil
}

func (in Inputs) checkIdentity() error {
	switch in.Identity {
	case IdentityRoot:
		if in.OwnerUID != 0 || in.OwnerGID != 0 || in.OwnerUser != "" {
			return refuse("identity", "owner ids are given for the %q mode", IdentityRoot)
		}
	case IdentityOwner:
		if in.OwnerUID == 0 || in.OwnerGID == 0 {
			return refuse("identity", "the %q mode needs the owner's non-zero uid and gid (use %q for UID 0)", IdentityOwner, IdentityRoot)
		}
		if !validLogin(in.OwnerUser) {
			return refuse("identity", "the %q mode needs the owner's login name ([a-z_][a-z0-9_-]{0,31})", IdentityOwner)
		}
	default:
		return refuse("identity", "must be %q or %q", IdentityRoot, IdentityOwner)
	}
	return nil
}

func validLogin(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c == '_'
		if i > 0 {
			ok = ok || c >= '0' && c <= '9' || c == '-'
		}
		if !ok {
			return false
		}
	}
	return true
}

func (in Inputs) checkManaged() error {
	inside := in.ManagedInside()
	if inside == "" {
		return refuse("managed-settings path", "the lead-hosting design names no managed-settings path for %s: the caller must give one", in.Runtime)
	}
	if err := CheckPath("managed-settings path", inside); err != nil {
		return err
	}
	// The provider reads its policy only at its system path: a file bound
	// anywhere else delivers nothing. The table binds the policy before
	// every lead-writable folder, so ValidateTable refuses a path inside one.
	if in.Runtime == Claude && inside != ClaudeManagedInside {
		return refuse("managed-settings path", "claude reads its managed settings only at %s", ClaudeManagedInside)
	}
	if !below(inside, "/etc") {
		return refuse("managed-settings path", "must lie under /etc, where the provider reads system policy")
	}
	return nil
}

func (in Inputs) checkRecords() error {
	if in.QargiRecords == "" && in.QargiRecordsInside == "" {
		return nil
	}
	if err := CheckPath("Qargi records folder", in.QargiRecords); err != nil {
		return err
	}
	if err := CheckPath("Qargi records inside path", in.QargiRecordsInside); err != nil {
		return err
	}
	// Rule G1 holds across leads only if every folder a lead writes lies in
	// that lead's own state: the other leads' checks refuse a source in or
	// above it. A records folder at any other place could be, or lie above, a
	// read-only source of another lead (its agent, its probe, its spec), and
	// the lead that writes it could plant a link there.
	if want := in.Layout().QargiRecords(); in.QargiRecords != want {
		return refuse("Qargi records folder", "must be %s (Layout.QargiRecords): a folder the lead writes anywhere else could be, or lie above, a source another lead's sandbox binds (rule G1)", want)
	}
	// The digest is independent of the project and of the host: it replaces
	// the project only in the rows it builds itself, so a caller-given path
	// must not depend on the project, hold a token, or touch the other
	// runtime's config folder.
	ri := in.QargiRecordsInside
	switch {
	case ri == WorkRootInside || below(ri, WorkRootInside):
		return refuse("Qargi records inside path", "must not lie in %s (the work tree's path depends on the project)", WorkRootInside)
	case strings.ContainsAny(ri, "<>"):
		return refuse("Qargi records inside path", "must not hold '<' or '>' (the digest uses them for tokens)")
	}
	// An ancestor of that folder is refused by the table (a parent comes
	// before what is bound over it, and no inside path is used twice).
	if other := in.Runtime.Other(); other != "" && (ri == ConfigInside(other) || below(ri, ConfigInside(other))) {
		return refuse("Qargi records inside path", "must not lie in the %s config folder %s", other, ConfigInside(other))
	}
	return nil
}

// checkGitMeta refuses a git metadata folder that is not the layout's own,
// <state>/leads/<role>/gitmeta. The folder is Ataniq's copy of the tree's
// protected git files and is bound read-only by path, so it lies beside the
// lead's folders under the lead's state folder, where the lookup from the
// state root without links reaches every component of its path, and where the
// rules about another lead's state apply to it. A place the caller chooses
// could lie anywhere, above or below a folder the lead can write, and only its
// last component could be looked at. A never-mounted path that covers it is
// refused with the rows that bind it, like any source.
func (in Inputs) checkGitMeta() error {
	if want := in.Layout().GitMeta(); in.GitMeta != want {
		return refuse("git metadata folder", "must be %s (Layout.GitMeta): a folder elsewhere could lie in, above or beside a folder the lead can write, or have a link above it that nothing looks for", want)
	}
	return nil
}

func (in Inputs) checkSeccomp() error {
	if in.SeccompFile == "" && in.SeccompSHA256 == "" {
		return nil
	}
	if err := CheckPath("seccomp file", in.SeccompFile); err != nil {
		return err
	}
	return checkSHA256("seccomp digest", in.SeccompSHA256)
}

// checkSHA256 refuses a value that is not 64 lower-case hex digits.
func checkSHA256(field, v string) error {
	if len(v) != 64 {
		return refuse(field, "must be 64 lower-case hex digits")
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return refuse(field, "must be 64 lower-case hex digits")
		}
	}
	return nil
}

func (in Inputs) checkPlaceholders() error {
	if len(in.ExtraPlaceholders) > maxExtras {
		return refuse("extra placeholders", "more than %d", maxExtras)
	}
	seen := map[string]bool{}
	for _, p := range in.ExtraPlaceholders {
		if err := checkRel(p.Rel); err != nil {
			return err
		}
		if seen[p.Rel] {
			return refuse("extra placeholder "+p.Rel, "is listed twice")
		}
		seen[p.Rel] = true
		if err := CheckPath("extra placeholder "+p.Rel+" source", p.Source); err != nil {
			return err
		}
	}
	return nil
}

// checkRel refuses a placeholder target that is not a clean path below the
// tree, or that is the .git folder or below it (those are bound by the
// profile itself).
func checkRel(rel string) error {
	field := "extra placeholder " + rel
	switch {
	case rel == "" || len(rel) > maxPathLen:
		return refuse("extra placeholder", "target must be 1 to %d bytes", maxPathLen)
	case hasControl(rel):
		return refuse(field, "holds a control character")
	case strings.HasPrefix(rel, "/"):
		return refuse(field, "must be relative to the tree")
	case rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || cleanRel(rel) != rel:
		return refuse(field, "must be a clean path below the tree")
	case rel == ".git" || strings.HasPrefix(rel, ".git/"):
		return refuse(field, "must not be the .git folder or below it")
	}
	return nil
}

func cleanRel(rel string) string {
	return strings.TrimPrefix(path.Clean("/"+rel), "/")
}

func (in Inputs) checkNeverList() error {
	if len(in.NeverMounted) == 0 {
		return refuse("never-mounted paths", "none given: name the owner's checkout, the real provider folders and .ssh")
	}
	if len(in.NeverMounted) > maxNever {
		return refuse("never-mounted paths", "more than %d", maxNever)
	}
	for i, p := range in.NeverMounted {
		if err := CheckPath("never-mounted path "+strconv.Itoa(i), p); err != nil {
			return err
		}
	}
	return nil
}
