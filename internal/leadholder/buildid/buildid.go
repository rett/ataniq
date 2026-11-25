// Package buildid is the build identity of a lead holder: which build of the
// code a holder executable is, read from the Go build information the
// toolchain embeds in it, and the check that a launcher (the daemon, and
// ataniq-scratch) makes before it launches a holder or attaches to one.
//
// # What a holder is
//
// The holder reads its own identity from the executable that actually runs,
// /proc/<pid>/exe ([OfProcess]), never from argv[0], and records it in its
// state file, holder.json ([RecordFileName]), and in the start event of its
// event log. A launcher reads the identity of the holder executable it is
// about to start from that file ([ReadExecutable]), and the identity of a
// running holder from its state file ([ReadRecord]), which must be of the
// epoch that is to be attached ([Checker.BeforeAttach]).
//
// # What "expected" means
//
// A launcher expects its own build ([Self]): the holder and the launcher are
// built from one checkout, as the control wire between them assumes (it has no
// negotiation yet; the holder hello of the provider packs adds protocol
// ranges). The rule is strict and simple. A holder is the expected build when
// its main module path, its main module version, its VCS revision and its
// VCS modified flag are each equal to the launcher's, and the launcher's build
// has a VCS revision ([Match]). The rest is provenance, recorded and shown but
// not compared:
//
//   - the build tags differ on purpose (the owner's daemon is built with the
//     owner channel's and the canary's tags, the holder with the canary's
//     alone, from one checkout);
//   - the Go toolchain version does not change the source the two share.
//
// A build without a VCS revision ("unstamped": built by go test, with
// -buildvcs=false, or outside a git checkout) identifies no source, so it is
// never the expected build, and a launcher that is unstamped expects nothing it
// can match. The modified flag is compared as it is: Go sets it when the
// checkout has any change, untracked files included, so a holder built from a
// clean checkout and a launcher built from the same revision with a change are
// different builds, and two modified builds of one revision are taken to be
// the same, which nothing can prove.
//
// # The refusal
//
// A holder that is not the expected build is refused before anything starts
// or attaches, with the named reason [Reason], holder-version-skew, and a
// message that names both builds. A holder whose state file records no
// identity, which is every holder built before this package, is named as such
// and is never a match. An identity that cannot be read (no file, no Go build
// information, a malformed record) is refused with the same reason, saying so.
// Before an attach, a state file of another epoch than the one to be attached
// is refused first, with the reason [EpochReason], stale-epoch: the build it
// records is not that of the holder that would be asked (see [EpochError]).
//
// # Launchers older than this package
//
// A launcher built before build identities (the daemon and ataniq-scratch of
// release 0.0.13 and before) makes no check: it never reads holder.json, and
// it talks to a holder only over the control socket, whose version-1 frames
// are the same bytes as before (internal/leadholder/wire). So it starts and
// attaches to a holder of this build as it did to any holder, and the member
// "build" of holder.json is not read by it at all. The one older reader of
// holder.json is the canary run kit's "ataniq-canary fields", which prints
// every member of any JSON object, nested ones included, as path=value lines;
// it does not refuse a member it does not know, and the run script looks up
// only the members it names. No older reader of the file refuses the new
// member. The tests of this package cover a launcher of this package that
// expects another build, not a launcher without it.
package buildid

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

// Reason is the named reason of the refusal of a holder that is not the
// expected build.
const Reason = "holder-version-skew"

// RecordFileName is the holder's state file in its state folder, which
// records its identity in the member "build".
const RecordFileName = "holder.json"

// MaxRecordBytes bounds the state file a launcher reads.
const MaxRecordBytes = 64 << 10

// Bounds of the members of an [Identity].
const (
	MaxModule    = 256
	MaxVersion   = 128
	MaxRevision  = 64
	MaxGoVersion = 64
	MaxTags      = 256
)

// MaxToken is the length of the longest [Identity.Token] of a valid identity
// (463 bytes): the longest module path and version, the longest revision and
// ":modified". A field that carries a token holds this many bytes, so that
// the revision at its end is never cut.
const MaxToken = MaxModule + len(":") + MaxVersion + len(":rev=") + MaxRevision + len(":modified")

// ErrSkew is matched by every refusal of [Checker.BeforeLaunch], and by every
// refusal of [Checker.BeforeAttach] but that of a state file of another epoch
// ([ErrOtherEpoch]).
var ErrSkew = errors.New(Reason)

// Identity is the build identity of an executable: what its embedded build
// information says of the main module and its VCS state. Module, Version,
// Revision and Modified are compared ([Match]); GoVersion and Tags are
// provenance.
type Identity struct {
	// Module is the main module's path.
	Module string `json:"module"`
	// Version is the main module's version as the toolchain stamped it (a
	// pseudo-version with "+dirty" for a modified checkout), or "(devel)".
	Version string `json:"version"`
	// Revision is the VCS revision (vcs.revision); empty when the build
	// carries none.
	Revision string `json:"revision,omitempty"`
	// Modified is vcs.modified: the checkout had changes.
	Modified bool `json:"modified,omitempty"`
	// GoVersion is the toolchain that built it.
	GoVersion string `json:"go,omitempty"`
	// Tags are the build tags (-tags).
	Tags string `json:"tags,omitempty"`
}

// FromBuildInfo returns the identity of a build from its build information.
// The provenance members are cut to their bounds and have every character
// outside letters, digits and . _ , + - : replaced by '_'; the compared
// members must be valid as they are ([Identity.Validate]).
func FromBuildInfo(bi *debug.BuildInfo) (Identity, error) {
	if bi == nil {
		return Identity{}, errors.New("no build information")
	}
	id := Identity{Module: bi.Main.Path, Version: bi.Main.Version, GoVersion: provenance(bi.GoVersion, MaxGoVersion)}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			id.Revision = s.Value
		case "vcs.modified":
			id.Modified = s.Value == "true"
		case "-tags":
			id.Tags = provenance(s.Value, MaxTags)
		}
	}
	if err := id.Validate(); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// Self returns the identity of the running executable's own build.
func Self() (Identity, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Identity{}, errors.New("the executable has no build information")
	}
	return FromBuildInfo(bi)
}

// ReadExecutable returns the identity of the executable at path, read from
// the build information in the file. A file that is not a Go executable with
// build information is an error.
func ReadExecutable(path string) (Identity, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return Identity{}, fmt.Errorf("the executable's build information cannot be read: %w", cause(err))
	}
	return FromBuildInfo(bi)
}

// OfProcess returns the identity of the executable that process pid runs, read
// through /proc/<pid>/exe: the file the process was started from, even when
// the path it was started by now names another file or none.
func OfProcess(pid int) (Identity, error) {
	if pid <= 0 {
		return Identity{}, errors.New("no process")
	}
	return ReadExecutable("/proc/" + strconv.Itoa(pid) + "/exe")
}

// Validate checks the members of an identity read from anywhere: a module
// path of 1 to [MaxModule] bytes of letters, digits and . _ ~ / -; a version of
// 1 to [MaxVersion] bytes of letters, digits and . _ + - ( ); a revision empty
// or of 1 to [MaxRevision] lower-case hex characters; provenance of at most its
// bound in letters, digits and . _ , + - : .
func (id Identity) Validate() error {
	switch {
	case !charsIn(id.Module, 1, MaxModule, "._~/-"):
		return fmt.Errorf("the module path %q is not a module path", clip(id.Module))
	case !charsIn(id.Version, 1, MaxVersion, "._+-()"):
		return fmt.Errorf("the module version %q is not a version", clip(id.Version))
	case id.Revision != "" && !isHex(id.Revision, MaxRevision):
		return fmt.Errorf("the revision %q is not a hex revision", clip(id.Revision))
	case !charsIn(id.GoVersion, 0, MaxGoVersion, "._,+-:"):
		return fmt.Errorf("the Go version %q has characters outside the set", clip(id.GoVersion))
	case !charsIn(id.Tags, 0, MaxTags, "._,+-:"):
		return fmt.Errorf("the build tags %q have characters outside the set", clip(id.Tags))
	}
	return nil
}

// Stamped reports whether the build carries a VCS revision.
func (id Identity) Stamped() bool { return id.Revision != "" }

// Match reports whether a holder of identity got is the build a launcher of
// identity expected expects (see the package documentation): the expected
// build is stamped, and the module path, the version, the revision and the
// modified flag are equal.
func Match(expected, got Identity) bool {
	return expected.Stamped() &&
		got.Module == expected.Module && got.Version == expected.Version &&
		got.Revision == expected.Revision && got.Modified == expected.Modified
}

// Short is the identity in at most about 80 bytes of printable ASCII without
// < > &, for a message: the version (cut to 48 bytes), the first 12
// characters of the revision or "unstamped", and "modified" when it is.
func (id Identity) Short() string {
	v := id.Version
	if len(v) > 48 {
		v = v[:45] + "..."
	}
	rev := "unstamped"
	if id.Revision != "" {
		rev = "rev " + id.Revision[:min(12, len(id.Revision))]
	}
	s := v + " " + rev
	if id.Modified {
		s += " modified"
	}
	return s
}

// String is the whole identity on one line, for a log.
func (id Identity) String() string {
	s := id.Module + " " + id.Version
	if id.Revision != "" {
		s += " rev " + id.Revision
	} else {
		s += " unstamped"
	}
	if id.Modified {
		s += " modified"
	}
	if id.GoVersion != "" {
		s += " " + id.GoVersion
	}
	if id.Tags != "" {
		s += " tags " + id.Tags
	}
	return s
}

// Token is the identity as one token of the characters A-Z a-z 0-9 . _ : / + = -,
// for the holder's event log: module:version:rev=REVISION (rev=none when
// unstamped) and :modified when it is. Any other character is replaced by '_'.
func (id Identity) Token() string {
	rev := id.Revision
	if rev == "" {
		rev = "none"
	}
	s := id.Module + ":" + id.Version + ":rev=" + rev
	if id.Modified {
		s += ":modified"
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == ':', c == '/', c == '+', c == '=', c == '-':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

// SkewError is a refusal of a holder that is not the expected build. It
// matches [ErrSkew].
type SkewError struct {
	// Expected is the build the launcher expects.
	Expected Identity
	// Holder is the holder's build; nil when it is not known (see Why).
	Holder *Identity
	// Why says what the check found, in a few words.
	Why string
	// Cause is the error of a read that failed, for a log; it is not part of
	// Error, which names no path.
	Cause error
}

// The texts of SkewError.Why. With the reason and two [Identity.Short] texts
// the message stays under 256 bytes.
const (
	WhyDiffer      = "the holder is another build"
	WhyNoRecord    = "the holder records no build (an older holder)"
	WhyUnstamped   = "the expected build has no VCS revision"
	WhyUnreadable  = "the holder's build cannot be read"
	WhyNoStateFile = "the holder has no state file"
)

// MaxErrorBytes bounds the message of a [SkewError], so that a receipt can
// carry it whole.
const MaxErrorBytes = 256

// Error names the reason, what was found and both builds, in at most
// [MaxErrorBytes] bytes of printable ASCII without < > &. [Identity.Short]
// leaves the module path out, so when the two module paths differ both are
// named as well, before the builds: whole when they fit, and otherwise each cut
// to an equal share of what the bound leaves (the builds are left out when
// that share would be under [minModuleShare] bytes with them), so that the
// first byte at which the two paths differ stays in both ([clipPair]). Two
// builds that differ only in their module would otherwise read the same.
func (e *SkewError) Error() string {
	holder := "unknown"
	if e.Holder != nil {
		holder = e.Holder.Short()
	}
	head := Reason + ": " + e.Why
	builds := "; holder build " + holder + ", expected build " + e.Expected.Short()
	if e.Holder == nil || e.Holder.Module == e.Expected.Module {
		return head + builds
	}
	modules := func(share int) string {
		h, x := clipPair(e.Holder.Module, e.Expected.Module, share)
		return "; holder module " + h + ", expected module " + x
	}
	const fixed = len("; holder module , expected module ")
	room := MaxErrorBytes - len(head) - len(builds) - fixed
	switch {
	case len(e.Holder.Module)+len(e.Expected.Module) <= room:
		return head + modules(MaxModule) + builds
	case room/2 >= minModuleShare:
		return head + modules(room/2) + builds
	}
	return head + modules((MaxErrorBytes-len(head)-fixed)/2)
}

// minModuleShare is the least share of a module path in a [SkewError]'s
// message that is shown together with the builds.
const minModuleShare = 32

// clipMiddle cuts s to at most n bytes by replacing its middle with "...",
// keeping its start and its end, where module paths most often differ. s is
// ASCII ([Identity.Validate]).
func clipMiddle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 5 {
		return s[:max(n, 0)]
	}
	keep := n - 3
	return s[:keep-keep/2] + "..." + s[len(s)-keep/2:]
}

// clipPair cuts each of two module paths that differ to at most n bytes so
// that the cut texts still differ: each is cut by [clipAround] around the
// first byte at which the two differ. When one path is a strict prefix of the
// other they first differ past the end of the shorter, and both can be cut to
// one text; each cut text then ends with the length of its path
// ([clipWithLength]), which differs.
func clipPair(a, b string, n int) (string, string) {
	d := 0
	for d < len(a) && d < len(b) && a[d] == b[d] {
		d++
	}
	x, y := clipAround(a, d, n), clipAround(b, d, n)
	if x == y && a != b {
		return clipWithLength(a, d, n), clipWithLength(b, d, n)
	}
	return x, y
}

// clipWithLength cuts s as [clipAround] does to what n leaves after the
// length of s, and adds the length: " (241 bytes)", or "(241)" when n leaves
// too little for the longer form.
func clipWithLength(s string, d, n int) string {
	suffix := " (" + strconv.Itoa(len(s)) + " bytes)"
	if n-len(suffix) < 9 {
		suffix = "(" + strconv.Itoa(len(s)) + ")"
		return clipMiddle(s, n-len(suffix)) + suffix
	}
	return clipAround(s, d, n-len(suffix)) + suffix
}

// clipAround cuts s to at most n bytes as [clipMiddle] does when byte d of s
// is in the start or the end that it keeps, or is past the end of s. When the
// middle that clipMiddle would replace holds byte d, s is cut instead to its
// start, "...", the bytes from a few before d on, and "..." when those do not
// reach the end of s. Two paths with one start that differ first at d, each
// cut so, have their differing bytes at one place in the two texts. s is
// ASCII ([Identity.Validate]).
func clipAround(s string, d, n int) string {
	if len(s) <= n {
		return s
	}
	keep := n - 3
	if n < 9 || d < keep-keep/2 || d >= len(s)-keep/2 {
		return clipMiddle(s, n)
	}
	head := (n - 6) / 3
	from := max(head+1, d-(n-6)/6)
	if head+3+len(s)-from <= n {
		return s[:head] + "..." + s[from:]
	}
	return s[:head] + "..." + s[from:from+n-6-head] + "..."
}

// Is makes errors.Is(err, ErrSkew) true.
func (e *SkewError) Is(target error) bool { return target == ErrSkew }

// Confirmed reports whether err, of [Checker.BeforeAttach], says what the
// state file of the epoch records and that it is not the expected build: the
// file was read, is of the epoch, and records another build ([WhyDiffer]), no
// build ([WhyNoRecord]), or any build while the expected one is unstamped
// ([WhyUnstamped]). A file that is missing ([WhyNoStateFile]) or cannot be
// read ([WhyUnreadable]), or is of another epoch ([ErrOtherEpoch]), confirms
// nothing of the holder of the epoch, and neither does nil. A launcher that
// acts on a holder without its answer (one that has not answered its status)
// acts only on a confirmed mismatch.
func Confirmed(err error) bool {
	var se *SkewError
	if !errors.As(err, &se) {
		return false
	}
	switch se.Why {
	case WhyDiffer, WhyNoRecord, WhyUnstamped:
		return true
	}
	return false
}

// Check compares a holder's identity with the expected one. holder is nil
// for a holder that records none. It returns nil for the expected build and a
// *SkewError otherwise.
func Check(expected Identity, holder *Identity) error {
	switch {
	case holder == nil:
		return &SkewError{Expected: expected, Why: WhyNoRecord}
	case !expected.Stamped():
		h := *holder
		return &SkewError{Expected: expected, Holder: &h, Why: WhyUnstamped}
	case !Match(expected, *holder):
		h := *holder
		return &SkewError{Expected: expected, Holder: &h, Why: WhyDiffer}
	}
	return nil
}

// ErrRecordBuild is matched by the error of [ReadRecord] for a state file that
// is a holder's JSON object whose member "build" is not a valid identity (of
// another JSON type, with a member this reader does not know, or with a value
// out of the bounds). The [Record] returned with it carries the file's epoch,
// so that [Checker.BeforeAttach] compares the epoch first.
var ErrRecordBuild = errors.New("the state file's build identity is not valid")

// Record is what a launcher reads of a holder's state file.
type Record struct {
	// Epoch is the epoch the holder was started for.
	Epoch uint64
	// Build is the identity the holder recorded; nil when the file has none,
	// as a holder older than build identities writes it.
	Build *Identity
	// LaunchIntent is the launch intent the holder was started with; empty
	// when the file has none or it is not a JSON string. Only
	// [Checker.BeforeAttachLaunch] compares it.
	LaunchIntent string
}

// ReadRecord reads the state file of the holder whose state folder is dir. The
// file must be a regular file (not a link), of at most [MaxRecordBytes] bytes,
// holding a JSON object (a top-level null, an array or any other value is not
// one, and is not a file of epoch 0). Only "epoch", "build" and
// "launch_intent" are read; every other member is left alone, and a launch
// intent that is not a JSON string is read as none. A build member that is present must be a valid
// identity; when it is not, the error matches [ErrRecordBuild] and the Record
// still carries the epoch.
func ReadRecord(dir string) (Record, error) {
	f, err := os.OpenFile(filepath.Join(dir, RecordFileName), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Record{}, fmt.Errorf("the state file cannot be opened: %w", cause(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("the state file cannot be looked at: %w", cause(err))
	}
	if !fi.Mode().IsRegular() {
		return Record{}, errors.New("the state file is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxRecordBytes+1))
	if err != nil {
		return Record{}, fmt.Errorf("the state file cannot be read: %w", cause(err))
	}
	if len(data) > MaxRecordBytes {
		return Record{}, fmt.Errorf("the state file is longer than %d bytes", MaxRecordBytes)
	}
	var raw struct {
		Epoch        uint64          `json:"epoch"`
		Build        json.RawMessage `json:"build"`
		LaunchIntent json.RawMessage `json:"launch_intent"`
	}
	// A top-level null decodes into the struct without an error and would
	// read as epoch 0: the file must be an object, its first byte after JSON
	// white space an opening brace.
	if t := bytes.TrimLeft(data, " \t\r\n"); len(t) == 0 || t[0] != '{' || json.Unmarshal(data, &raw) != nil {
		return Record{}, errors.New("the state file is not a JSON object of a holder")
	}
	rec := Record{Epoch: raw.Epoch}
	if len(raw.LaunchIntent) > 0 && raw.LaunchIntent[0] == '"' {
		_ = json.Unmarshal(raw.LaunchIntent, &rec.LaunchIntent)
	}
	if len(raw.Build) == 0 || bytes.Equal(raw.Build, []byte("null")) {
		return rec, nil
	}
	var id Identity
	dec := json.NewDecoder(bytes.NewReader(raw.Build))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&id); err != nil {
		return rec, fmt.Errorf("%w: it is malformed", ErrRecordBuild)
	}
	if err := id.Validate(); err != nil {
		return rec, fmt.Errorf("%w: %w", ErrRecordBuild, err)
	}
	rec.Build = &id
	return rec, nil
}

// Checker is what a launcher expects of a holder, and how it reads what a
// holder is. The zero Executable and Record are [ReadExecutable] and
// [ReadRecord]; a test gives its own.
type Checker struct {
	// Expected is the build the launcher expects, its own ([NewChecker]).
	Expected Identity
	// Executable reads the identity of a holder executable.
	Executable func(path string) (Identity, error)
	// Record reads the state file of a holder. It is given the epoch that is
	// to be attached as well: [ReadRecord] does not use it, and a test's
	// reader that stands for the holder of every epoch answers with it.
	Record func(stateDir string, epoch uint64) (Record, error)
}

// EpochReason is the named reason of the refusal of an attach to a holder
// whose state file is of another epoch than the one to be attached. It is the
// lead wire's code stale-epoch, which the daemon also answers when the holder
// itself says the epoch is not its own.
const EpochReason = "stale-epoch"

// ErrOtherEpoch is matched by the refusal of [Checker.BeforeAttach] when the
// holder's state file is of another epoch (an *EpochError).
var ErrOtherEpoch = errors.New(EpochReason)

// EpochError is the refusal of an attach to a holder whose state file is of
// another epoch than the one to be attached: the build it records is then not
// the build of the holder that would be asked. The usual case is a holder of
// the epoch that has not yet written its state file, while the file of the
// epoch before is still there; it passes once the holder has written it. It
// matches [ErrOtherEpoch], not [ErrSkew]: nothing is known of a build.
type EpochError struct {
	// Want is the epoch to be attached; Got is the epoch of the state file.
	Want, Got uint64
	// OtherLaunch is set when the file is of the epoch but of another launch
	// ([Checker.BeforeAttachLaunch]): an earlier launch of the epoch, whose
	// epoch was given back, left it.
	OtherLaunch bool
}

// Error names the reason and both epochs, in printable ASCII.
func (e *EpochError) Error() string {
	if e.OtherLaunch {
		return fmt.Sprintf("%s: the holder's state file of epoch %d is of another launch: the holder of this launch has not written it yet, or is not running",
			EpochReason, e.Want)
	}
	return fmt.Sprintf("%s: the holder's state file is of epoch %d, not of epoch %d: the holder of the epoch has not written it yet, or is not running",
		EpochReason, e.Got, e.Want)
}

// Is makes errors.Is(err, ErrOtherEpoch) true.
func (e *EpochError) Is(target error) bool { return target == ErrOtherEpoch }

// NewChecker returns the checker of the running build: it expects [Self].
func NewChecker() (*Checker, error) {
	self, err := Self()
	if err != nil {
		return nil, fmt.Errorf("this build's own identity cannot be read: %w", err)
	}
	return &Checker{Expected: self}, nil
}

// BeforeLaunch checks the holder executable at path before it is started. It
// returns nil when the file is the expected build, and an error that matches
// [ErrSkew] (a *SkewError) otherwise, also when the file's identity cannot be
// read.
func (c *Checker) BeforeLaunch(path string) error {
	read := c.Executable
	if read == nil {
		read = ReadExecutable
	}
	id, err := read(path)
	if err != nil {
		return &SkewError{Expected: c.Expected, Why: WhyUnreadable, Cause: err}
	}
	return Check(c.Expected, &id)
}

// BeforeAttach checks a running holder of the epoch, by the identity it
// recorded in its state folder, before something attaches to it. It returns
// nil when the state file is of the epoch and records the expected build. A
// state file of another epoch is refused first, with an *EpochError that
// matches [ErrOtherEpoch]: the build it records is not that of the holder of
// the epoch; so is a file whose build member is not valid ([ErrRecordBuild]),
// whose epoch is still read. Otherwise the refusal is an error that matches
// [ErrSkew] (a *SkewError): a holder that records no identity is named as
// such, and a state file that is missing or cannot be read is refused too.
func (c *Checker) BeforeAttach(stateDir string, epoch uint64) error {
	return c.attach(stateDir, epoch, nil)
}

// BeforeAttachLaunch is [Checker.BeforeAttach] for the launcher that started
// the holder of the epoch with the launch intent intent: a state file of the
// epoch that names another launch intent (or none) is refused after the
// epoch, with an *EpochError whose OtherLaunch is set, that matches
// [ErrOtherEpoch] and is not [Confirmed]. A launcher that gives an epoch back
// after a launch that failed can give it to the next launch, and the file the
// failed launch's holder left is then of the epoch but not the record of the
// holder that was started.
func (c *Checker) BeforeAttachLaunch(stateDir string, epoch uint64, intent string) error {
	return c.attach(stateDir, epoch, &intent)
}

// attach is BeforeAttach, and with intent not nil BeforeAttachLaunch.
func (c *Checker) attach(stateDir string, epoch uint64, intent *string) error {
	read := c.Record
	if read == nil {
		read = func(dir string, _ uint64) (Record, error) { return ReadRecord(dir) }
	}
	rec, err := read(stateDir, epoch)
	if err != nil && !errors.Is(err, ErrRecordBuild) {
		why := WhyUnreadable
		if errors.Is(err, os.ErrNotExist) {
			why = WhyNoStateFile
		}
		return &SkewError{Expected: c.Expected, Why: why, Cause: err}
	}
	// The epoch first: the build of a file of another epoch is not that of
	// the holder that would be asked, whether it is valid or not.
	if rec.Epoch != epoch {
		return &EpochError{Want: epoch, Got: rec.Epoch}
	}
	if intent != nil && rec.LaunchIntent != *intent {
		return &EpochError{Want: epoch, Got: rec.Epoch, OtherLaunch: true}
	}
	if err != nil {
		return &SkewError{Expected: c.Expected, Why: WhyUnreadable, Cause: err}
	}
	return Check(c.Expected, rec.Build)
}

// charsIn reports whether s is min to max bytes of ASCII letters, digits and
// the bytes of extra.
func charsIn(s string, min, max int, extra string) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(extra, c) >= 0) {
			return false
		}
	}
	return true
}

func isHex(s string, max int) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// provenance cuts s to max bytes and replaces every byte outside letters,
// digits and . _ , + - : with '_'.
func provenance(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._,+-:", c) >= 0) {
			b[i] = '_'
		}
	}
	return string(b)
}

// clip cuts a text for an error message.
func clip(s string) string {
	if len(s) > 64 {
		return s[:64] + "..."
	}
	return s
}

// cause is the error without the path a file error carries.
func cause(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
