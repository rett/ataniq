package assert

// Where says where an assertion is checked.
type Where string

const (
	// Inside: the probe checks it, in the sandbox, before it execs the agent.
	Inside Where = "inside"
	// Outside: the holder or the daemon checks it, on the host.
	Outside Where = "outside"
	// Both: the probe checks the inside half and the holder the outside half.
	Both Where = "both"
)

// Assertion is one row of the table of start-up assertions (the lead-hosting
// design, section 3.8).
type Assertion struct {
	// Number is the position in the design, 1 to 10.
	Number int
	// ID is a stable name for the assertion. It is the ID of the result of
	// the inside half (or of the only half). The result of the outside half
	// of an assertion that is checked on both sides has ID+OutsideSuffix.
	ID string
	// Text is the assertion as the design words it.
	Text string
	// Where says who checks it.
	Where Where
	// Cases are the qualification cases of the design (section 7) that
	// exercise it.
	Cases []string
}

// OutsideSuffix is appended to the ID of an assertion that is checked on both
// sides, to name the result of its outside half.
const OutsideSuffix = ".outside"

// NumAssertions is the number of assertions.
const NumAssertions = 10

// The IDs of the assertions.
const (
	IDHolderHidden        = "holder-hidden"
	IDEnvironment         = "environment-exact"
	IDLoopbackUnreach     = "host-loopback-unreachable"
	IDProviderResolves    = "provider-resolves"
	IDTIOCSTIBlocked      = "tiocsti-blocked"
	IDOwnerPathsAbsent    = "owner-paths-absent"
	IDTreeGitReadOnly     = "tree-git-readonly"
	IDManagedSettings     = "managed-settings"
	IDRoleLockLingering   = "role-lock-lingering"
	IDQualificationRecord = "qualification-record"
)

var assertions = [NumAssertions]Assertion{
	{1, IDHolderHidden, "The attach and control sockets and the holder's state are not visible inside.", Both, []string{"S1"}},
	{2, IDEnvironment, "The environment inside equals the profile's allowlist.", Inside, []string{"S3"}},
	{3, IDLoopbackUnreach, "One host loopback port and one host abstract socket are unreachable.", Both, []string{"S2"}},
	{4, IDProviderResolves, "A provider host name resolves.", Inside, []string{"S4"}},
	{5, IDTIOCSTIBlocked, "dev.tty.legacy_tiocsti is 0, or the seccomp filter is loaded.", Both, []string{"S7"}},
	{6, IDOwnerPathsAbsent, "The owner's real provider folders, ~/.ssh, the owner's checkout and the other runtime's config folder do not exist inside.", Inside, []string{"S1"}},
	{7, IDTreeGitReadOnly, "The tree is the lead's own clone, and its .git/config, .git/hooks and .git/info are not writable inside.", Inside, []string{"S16"}},
	{8, IDManagedSettings, "The managed-settings file is present and read-only; for claude it contains the guard hook.", Inside, []string{"S18"}},
	{9, IDRoleLockLingering, "The role lock is held, and lingering is on.", Outside, []string{"H7", "L3"}},
	{10, IDQualificationRecord, "A current qualification record exists for this runtime, version and profile digest. A missing or stale record blocks the start and shows as unsupported.", Outside, []string{"S11"}},
}

// Assertions returns the table, in order of number. The caller may change the
// returned slice and its Cases without changing the table.
func Assertions() []Assertion {
	out := make([]Assertion, len(assertions))
	for i, a := range assertions {
		a.Cases = append([]string(nil), a.Cases...)
		out[i] = a
	}
	return out
}

// ByNumber returns the assertion with the number n (1 to 10).
func ByNumber(n int) (Assertion, bool) {
	if n < 1 || n > NumAssertions {
		return Assertion{}, false
	}
	a := assertions[n-1]
	a.Cases = append([]string(nil), a.Cases...)
	return a, true
}

// HasInside says the probe checks the assertion (Where is Inside or Both).
func (a Assertion) HasInside() bool { return a.Where == Inside || a.Where == Both }

// HasOutside says the holder checks the assertion (Where is Outside or Both).
func (a Assertion) HasOutside() bool { return a.Where == Outside || a.Where == Both }

// validResult says whether (number, id) names a result the table allows: the
// ID of the assertion, or its ID with OutsideSuffix when it is checked on both
// sides.
func validResult(number int, id string) bool {
	a, ok := ByNumber(number)
	if !ok {
		return false
	}
	return id == a.ID || (a.Where == Both && id == a.ID+OutsideSuffix)
}

// idOf returns the ID of assertion n, "" when n is not a number of the table.
func idOf(n int) string {
	if n < 1 || n > NumAssertions {
		return ""
	}
	return assertions[n-1].ID
}
