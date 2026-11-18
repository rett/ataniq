package assert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"ataniq/internal/leadsandbox/profile"
)

// MaxManagedSettingsBytes is the most of the managed-settings file that is
// read. A larger file is a failure: Ataniq writes a small one.
const MaxManagedSettingsBytes = 1 << 20

// Files the probe reads to check assertion 5.
const (
	tiocstiSysctl = "/proc/sys/dev/tty/legacy_tiocsti"
	procStatus    = "/proc/self/status"

	maxSysctlBytes = 64
	maxStatusBytes = 64 << 10
)

// RunInside runs the inside checks, assertions 1 to 8 and 11, in order, over
// the system, and returns the report. It never panics on what a system returns
// and it never writes: it asks, with Lstat, ReadFile, Mounts, MountID,
// Identity, DirEmpty, DirNames, Environ, Dial, LookupHost and Writable. A check
// that cannot be made is not-checked, with the reason. The probe never issues
// TIOCSTI: assertion 5 reads the sysctl and the seccomp mode of the process.
//
// The spec should have passed Validate (DecodeSpec does it). If it has not,
// every result is not-checked, with the refusal as the reason.
func RunInside(spec Spec, sys System) Report {
	rep := Report{Version: ReportVersion, Digest: spec.Digest}
	if err := spec.Validate(); err != nil {
		for n := 1; n <= NumAssertions; n++ {
			if a, _ := ByNumber(n); a.HasInside() {
				rep.Results = append(rep.Results, result(n, StatusNotChecked, "the spec is refused: %v", err))
			}
		}
		return rep
	}
	c := &checker{spec: spec, sys: sys}
	rep.Results = []Result{
		c.holderHidden(), c.environment(), c.loopback(), c.provider(),
		c.tiocsti(), c.ownerPaths(), c.treeGit(), c.managed(),
		c.parentFolders(),
	}
	return rep
}

type checker struct {
	spec Spec
	sys  System

	loaded    bool
	mounts    []Mount
	mountsErr error

	// ids keeps the identity of each path that was asked for, so that two
	// assertions that compare the same path read it once.
	ids map[string]identityAnswer
}

// identityAnswer is what the system said about the identity of a path.
type identityAnswer struct {
	id  FileIdentity
	err error
}

// identityOf reads the identity of a path, once.
func (c *checker) identityOf(p string) (FileIdentity, error) {
	a, ok := c.ids[p]
	if !ok {
		a.id, a.err = c.sys.Identity(p)
		if c.ids == nil {
			c.ids = map[string]identityAnswer{}
		}
		c.ids[p] = a
	}
	return a.id, a.err
}

// mountTable reads the mount table once.
func (c *checker) mountTable() ([]Mount, error) {
	if !c.loaded {
		c.mounts, c.mountsErr = c.sys.Mounts()
		c.loaded = true
	}
	return c.mounts, c.mountsErr
}

// result builds a result for assertion n with the ID the table gives it and a
// detail made safe by EscapeDetail.
func result(n int, st Status, format string, args ...any) Result {
	return Result{Number: n, ID: idOf(n), Status: st, Detail: EscapeDetail(fmt.Sprintf(format, args...))}
}

// tally collects what a check found: facts that fail the assertion, and
// questions it could not ask.
type tally struct {
	failed    []string
	unchecked []string
}

func (t *tally) fail(format string, args ...any) {
	t.failed = append(t.failed, fmt.Sprintf(format, args...))
}

func (t *tally) skip(format string, args ...any) {
	t.unchecked = append(t.unchecked, fmt.Sprintf(format, args...))
}

// maxListed is how many findings a detail lists before it counts the rest.
const maxListed = 3

func list(items []string) string {
	if len(items) <= maxListed {
		return strings.Join(items, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(items[:maxListed], "; "), len(items)-maxListed)
}

// result gives the status of assertion n: failed when anything failed (the
// detail also counts the questions not asked), else not-checked when anything
// could not be asked, else ok with the detail given.
func (t *tally) result(n int, ok string) Result {
	switch {
	case len(t.failed) > 0:
		d := list(t.failed)
		if len(t.unchecked) > 0 {
			d += fmt.Sprintf(" (%d more checks could not be made)", len(t.unchecked))
		}
		return result(n, StatusFailed, "%s", d)
	case len(t.unchecked) > 0:
		return result(n, StatusNotChecked, "%s", list(t.unchecked))
	}
	return result(n, StatusOK, "%s", ok)
}

// mustBeAbsent records whether path exists.
func (c *checker) mustBeAbsent(t *tally, what, path string) {
	_, err := c.sys.Lstat(path)
	switch {
	case err == nil:
		t.fail("%s %q exists", what, path)
	case absent(err):
	default:
		t.skip("%s %q: cannot tell whether it exists: %v", what, path, err)
	}
}

// readOnly records whether path is on a read-only mount, with no read-write
// mount at or below it, and the kernel says it is not writable. All must hold:
// access(W_OK) alone says yes to a process with the rights to override
// permissions, and the mount flag alone does not see a permission or a file
// system that would let a write through.
//
// The mount is found by its ID (statx of the path, matched to the first field
// of the mount table), not by the longest mount point that is a prefix of the
// path: when a later bind hides an earlier one (a read-write bind of the tree
// over a read-only bind of its .git/config, in a plan whose rows are in the
// wrong order), the prefix is the hidden mount and the path is on the other.
// A read-write mount at or below the path fails it too, whether or not it is
// hidden: a path is read-only when nothing writable is bound inside it. Where
// the kernel gives no mount ID the result is not-checked.
func (c *checker) readOnly(t *tally, what, path string) {
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("%s %q: no mount table: %v", what, path, err)
	} else {
		top := -1
		if id, err := c.sys.MountID(path); err != nil {
			t.skip("%s %q: no mount ID (statx): %v", what, path, err)
		} else if m, ok := mountByID(mounts, id); !ok {
			t.skip("%s %q: mount ID %d is not in the mount table", what, path, id)
		} else {
			top = m.ID
			if !m.ReadOnly() {
				t.fail("%s %q is on a writable mount at %q", what, path, m.Point)
			}
		}
		for _, m := range mounts {
			if m.ID != top && within(m.Point, path) && !m.ReadOnly() {
				t.fail("%s %q has a writable mount at or below it: %q", what, path, m.Point)
			}
		}
	}
	w, err := c.sys.Writable(path)
	switch {
	case err != nil:
		t.skip("%s %q: access(W_OK): %v", what, path, err)
	case w:
		t.fail("%s %q is writable by access(W_OK)", what, path)
	}
}

// 1. The attach and control sockets and the holder's state are not visible.
func (c *checker) holderHidden() Result {
	var t tally
	for _, p := range c.spec.HolderPaths {
		c.mustBeAbsent(&t, "holder path", p)
	}
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("no mount table: %v", err)
	}
	for _, m := range mounts {
		if sub, ok := mentions(m, c.spec.MountSubstrings); ok {
			t.fail("mount %q mentions %q", m.Point, sub)
			continue
		}
		if what, p, ok := relatedMount(m, c.spec.OwnerPaths, c.spec.HolderPaths); ok {
			t.fail("mount %q has %s %q, which is at, above or below the path %q", m.Point, what, clipPath(m.value(what)), p)
		}
	}
	checked := c.identities(&t, mounts)
	return t.result(1, fmt.Sprintf("none of %d holder paths exists; none of %d mounts mentions any of %d substrings; %d identities of mount points and of the lead's folders are none of the %d forbidden",
		len(c.spec.HolderPaths), len(mounts), len(c.spec.MountSubstrings), checked, len(c.spec.ForbiddenIdentities)))
}

// identities is the check of assertion 1 by identity: the identity of each
// mount point of the table (at most maxIdentityMounts of them) and of the
// lead's home, config folder and tree and of their parents is none of the
// forbidden ones. A mount root is relative to its file system, so the text of
// the table cannot see a bind of an owner folder that lies on a subvolume or a
// file system of its own; the kernel's name for the folder can. With no
// forbidden identity the check cannot be made, and that is not-checked, never
// ok. It returns how many identities it compared.
func (c *checker) identities(t *tally, mounts []Mount) int {
	ic := c.identityCheck(t, c.spec.ForbiddenIdentities, "an owner or holder path or of one of their ancestors")
	if ic == nil {
		t.skip("the spec lists no forbidden file identity: the mounts cannot be compared by identity")
		return 0
	}
	ic.mountPoints(mounts)
	folders := map[string]bool{}
	for _, folder := range []struct{ what, path string }{
		{"the lead's home", profile.HomeInside},
		{"the lead's config folder", profile.ConfigInside(c.spec.Runtime)},
		{"the lead's tree", c.spec.Tree},
	} {
		for _, p := range ancestors(folder.path) {
			what := folder.what
			if p != folder.path {
				what = "a parent of " + folder.what
			}
			if !folders[p] {
				folders[p] = true
				ic.compare(what, p, true)
			}
		}
	}
	return ic.checked
}

// identityComparison compares the identities of paths inside with a list of
// the spec: the forbidden identities of assertion 1, or the coordinators' of
// assertion 11.
type identityComparison struct {
	c         *checker
	t         *tally
	forbidden map[FileIdentity]bool
	// whose says what the listed identities are of, for a failure.
	whose string
	// checked counts the paths whose identity was read and compared.
	checked int
	counted map[string]bool
}

// identityCheck starts a comparison with a list. It is nil for an empty list:
// the comparison cannot be made, and the caller says so.
func (c *checker) identityCheck(t *tally, list []FileIdentity, whose string) *identityComparison {
	if len(list) == 0 {
		return nil
	}
	ic := &identityComparison{c: c, t: t, forbidden: make(map[FileIdentity]bool, len(list)), whose: whose, counted: map[string]bool{}}
	for _, id := range list {
		ic.forbidden[id] = true
	}
	return ic
}

// compare fails the path when its identity is one of the list. A path that is
// not there is passed over unless it must exist (a mount point that cannot be
// reached cannot be used); an identity that cannot be read is a question not
// asked.
func (ic *identityComparison) compare(what, p string, mustExist bool) {
	id, err := ic.c.identityOf(p)
	if err == nil && !ic.counted[p] {
		ic.counted[p] = true
		ic.checked++
	}
	switch {
	case err != nil && absent(err) && !mustExist:
		return
	case err != nil:
		ic.t.skip("%s %q: identity: %v", what, p, err)
		return
	}
	if ic.forbidden[id] {
		ic.t.fail("%s %q has the identity %s of %s", what, p, id, ic.whose)
	}
}

// mountPoints compares each mount point of the table, once, at most
// maxIdentityMounts of them: the rest is counted as not read.
func (ic *identityComparison) mountPoints(mounts []Mount) {
	seen := map[string]bool{}
	for i, m := range mounts {
		if i >= maxIdentityMounts {
			ic.t.skip("the mount table has %d mounts: the identity of the last %d was not read", len(mounts), len(mounts)-i)
			break
		}
		if !seen[m.Point] {
			seen[m.Point] = true
			ic.compare("mount point", m.Point, false)
		}
	}
}

// mentions returns the first substring that the root, the mount point or the
// source of the mount contains.
func mentions(m Mount, subs []string) (string, bool) {
	for _, sub := range subs {
		if strings.Contains(m.Point, sub) || strings.Contains(m.Root, sub) || strings.Contains(m.Source, sub) {
			return sub, true
		}
	}
	return "", false
}

// relatedMount says the root, the source or the mount point of the mount is a
// listed path, lies below it, or is one of its ancestors, comparing whole
// components. A mount whose root is the folder above an owner path (the
// owner's home bound at the lead's home) puts the owner path inside, though
// no listed text appears in the table. It names which of the three fields
// matched. The root "/" of a file system, the source of a mount that is not a
// path ("tmpfs") and the mount point "/" are related to nothing. A mount root
// is relative to its file system, so a bind taken from a file system that is
// not mounted at "/" (a separate /home, a btrfs subvolume) has a root that is
// not related to the host path: it is not found here, and the comparison by
// identity (identities) is the one that finds it.
func relatedMount(m Mount, lists ...[]string) (what, listed string, ok bool) {
	for _, field := range []string{"root", "source", "mount point"} {
		v := m.value(field)
		for _, list := range lists {
			for _, p := range list {
				if related(v, p) {
					return field, p, true
				}
			}
		}
	}
	return "", "", false
}

// clipPath cuts a path for a detail; EscapeDetail makes it visible.
func clipPath(p string) string {
	if len(p) > 120 {
		return p[:120] + "..."
	}
	return p
}

// 2. The environment equals the allowlist.
func (c *checker) environment() Result {
	want := make([]profile.EnvVar, len(c.spec.Env))
	for i, e := range c.spec.Env {
		want[i] = profile.EnvVar{Name: e.Name, Value: e.Value}
	}
	diffs, err := profile.DiffEnvironment(want, c.sys.Environ())
	if err != nil {
		return result(2, StatusNotChecked, "cannot compare: %v", err)
	}
	if len(diffs) > 0 {
		parts := make([]string, len(diffs))
		for i, d := range diffs {
			parts[i] = d.String()
		}
		return result(2, StatusFailed, "%d differences: %s", len(diffs), list(parts))
	}
	return result(2, StatusOK, "the environment equals the allowlist of %d variables", len(want))
}

// dialEvidence says what a failed dial shows. proves is true when the error is
// evidence that the address cannot be reached, and how names the evidence for
// the detail. Any other error says the question was not asked: out of
// descriptors, a listener whose backlog is full (EAGAIN: something is there to
// accept), a reset, no free local port.
func dialEvidence(err error) (how string, proves bool) {
	if errors.Is(err, syscall.EAGAIN) {
		// A unix connect to a listener with a full backlog fails with EAGAIN,
		// which net.Error calls a timeout. A listener exists: not unreachable.
		return "", false
	}
	for _, e := range []struct {
		errno syscall.Errno
		how   string
	}{
		{syscall.ECONNREFUSED, "refused the connection"},
		{syscall.ENOENT, "has no such socket"},
		{syscall.ENETUNREACH, "is on an unreachable network"},
		{syscall.EHOSTUNREACH, "is an unreachable host"},
		{syscall.EACCES, "was refused by policy"},
		{syscall.EPERM, "was refused by policy"},
		{syscall.ETIMEDOUT, timedOut},
	} {
		if errors.Is(err, e.errno) {
			return e.how, true
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, os.ErrDeadlineExceeded) {
		return timedOut, true
	}
	return "", false
}

// timedOut is the evidence of a dial that got no answer. It is weaker than a
// refusal: something may have dropped the packets of a path that is routed, so
// the canary count after the probe has run decides (CheckCanary).
const timedOut = "timed out (weaker than a refusal: the canary count decides)"

// 3. One host loopback port (two when the spec has a second address) and one
// host abstract socket are unreachable.
func (c *checker) loopback() Result {
	var t tally
	var shown []string
	targets := []struct{ what, network, addr string }{
		{"host loopback port", "tcp", c.spec.LoopbackTCP},
	}
	if c.spec.LoopbackTCP2 != "" {
		targets = append(targets, struct{ what, network, addr string }{"second host loopback port", "tcp", c.spec.LoopbackTCP2})
	}
	targets = append(targets, struct{ what, network, addr string }{"host abstract socket", "unix", "@" + c.spec.AbstractSocket})
	for _, target := range targets {
		err := c.sys.Dial(target.network, target.addr, c.spec.ConnectTimeout())
		if err == nil {
			t.fail("%s %s is reachable: a connection was made", target.what, target.addr)
			continue
		}
		how, proves := dialEvidence(err)
		if !proves {
			t.skip("%s %s: the dial failed in a way that does not show it is unreachable: %v", target.what, target.addr, err)
			continue
		}
		shown = append(shown, fmt.Sprintf("%s %s %s", target.what, target.addr, how))
	}
	return t.result(3, strings.Join(shown, "; "))
}

// 4. A provider host name resolves.
func (c *checker) provider() Result {
	addrs, err := c.sys.LookupHost(c.spec.ProviderHost, c.spec.ResolveTimeout())
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		// No answer is not an answer: the question was not asked. This is the
		// result the spec's optional list can excuse.
		return result(4, StatusNotChecked, "%q: the lookup got no answer: %v", c.spec.ProviderHost, err)
	case err != nil:
		return result(4, StatusFailed, "%q does not resolve: %v", c.spec.ProviderHost, err)
	case len(addrs) == 0:
		return result(4, StatusFailed, "%q resolved to no address", c.spec.ProviderHost)
	}
	return result(4, StatusOK, "%q resolves to %d addresses", c.spec.ProviderHost, len(addrs))
}

// sysctlState is what legacy_tiocsti says.
type sysctlState int

const (
	sysctlZero sysctlState = iota
	sysctlNonZero
	sysctlAbsent
	sysctlUnreadable
)

// readSysctl reads dev.tty.legacy_tiocsti and describes it for a detail.
func readSysctl(sys System) (sysctlState, string) {
	data, err := sys.ReadFile(tiocstiSysctl, maxSysctlBytes)
	switch {
	case err == nil:
		v := strings.TrimSpace(string(data))
		if v == "0" {
			return sysctlZero, "legacy_tiocsti is 0"
		}
		return sysctlNonZero, fmt.Sprintf("legacy_tiocsti is %q, not 0", v)
	case absent(err):
		return sysctlAbsent, "legacy_tiocsti is absent (the kernel does not restrict TIOCSTI, or /proc/sys is hidden)"
	}
	return sysctlUnreadable, fmt.Sprintf("legacy_tiocsti cannot be read: %v", err)
}

// seccompState is what /proc/self/status says about seccomp.
type seccompState struct {
	// Mode is the Seccomp: line: 0 off, 1 strict, 2 filter. A status without
	// the line (a kernel without seccomp) is mode 0.
	Mode int
	// Filters is the Seccomp_filters: line, the number of filters the
	// process carries; HasFilters is false when the kernel does not give it.
	Filters    int
	HasFilters bool
}

// describe is the state for a detail.
func (s seccompState) describe() string {
	if s.HasFilters {
		return fmt.Sprintf("Seccomp: %d, Seccomp_filters: %d", s.Mode, s.Filters)
	}
	return fmt.Sprintf("Seccomp: %d, Seccomp_filters: not given", s.Mode)
}

// readSeccomp reads the Seccomp: and Seccomp_filters: lines of
// /proc/self/status.
func readSeccomp(sys System) (seccompState, error) {
	var st seccompState
	data, err := sys.ReadFile(procStatus, maxStatusBytes)
	if err != nil {
		return st, err
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if v, ok := bytes.CutPrefix(line, []byte("Seccomp:")); ok {
			mode, err := strconv.Atoi(string(bytes.TrimSpace(v)))
			if err != nil {
				return seccompState{}, fmt.Errorf("the Seccomp: line is not a number")
			}
			st.Mode = mode
		} else if v, ok := bytes.CutPrefix(line, []byte("Seccomp_filters:")); ok {
			n, err := strconv.Atoi(string(bytes.TrimSpace(v)))
			if err != nil {
				return seccompState{}, fmt.Errorf("the Seccomp_filters: line is not a number")
			}
			st.Filters, st.HasFilters = n, true
		}
	}
	return st, nil
}

// 5. Without a filter in the profile, dev.tty.legacy_tiocsti must be 0: no
// other reading is accepted, and a seccomp filter that something else
// installed (a service manager's, say) is not evidence that TIOCSTI is
// blocked. With a filter in the profile, the process must carry one
// (Seccomp: 2). That is all the probe can see from inside: it does not verify
// which filter it is, and the detail says so.
func (c *checker) tiocsti() Result {
	if c.spec.SeccompExpected {
		st, err := readSeccomp(c.sys)
		switch {
		case err != nil:
			return result(5, StatusNotChecked, "the profile expects a seccomp filter and %s cannot be read: %v", procStatus, err)
		case st.Mode != 2:
			return result(5, StatusFailed, "the profile expects a seccomp filter and Seccomp: is %d, not 2", st.Mode)
		}
		return result(5, StatusOK, "a seccomp filter is loaded (%s), as the profile expects; the identity of the filter is not verified from inside", st.describe())
	}
	state, detail := readSysctl(c.sys)
	switch state {
	case sysctlZero:
		return result(5, StatusOK, "%s", detail)
	case sysctlUnreadable:
		return result(5, StatusNotChecked, "%s", detail)
	}
	return result(5, StatusFailed, "%s, and the profile names no seccomp filter: a filter that something else loaded is not evidence that TIOCSTI is blocked", detail)
}

// 6. The owner's folders and the other runtime's config folder do not exist.
func (c *checker) ownerPaths() Result {
	var t tally
	for _, p := range c.spec.OwnerPaths {
		c.mustBeAbsent(&t, "owner path", p)
	}
	note := ""
	c.otherConfig(&t, &note)
	return t.result(6, fmt.Sprintf("none of %d owner paths exists; %s", len(c.spec.OwnerPaths), note))
}

// otherConfig checks the other runtime's config folder. It is ok only when
// the folder is absent, or is an empty folder that is not a mount point and
// has no mount below it: a lead can make an empty folder in its own home, which
// is not a failure of the boundary. Anything in it is failed, since an owner's
// folder that came in at that place by a bind of a folder above it (the
// owner's home at the lead's home, say) is not a mount at the folder, and a
// link or a file there is not the folder the lead made.
func (c *checker) otherConfig(t *tally, note *string) {
	dir := c.spec.OtherConfigDir
	info, err := c.sys.Lstat(dir)
	switch {
	case absent(err):
		*note = fmt.Sprintf("the other runtime's config folder %q does not exist", dir)
		return
	case err != nil:
		t.skip("the other runtime's config folder %q: cannot tell whether it exists: %v", dir, err)
		return
	case !info.IsDir():
		t.fail("the other runtime's config folder %q is %s, not an empty folder", dir, info.Describe())
		return
	}
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("the other runtime's config folder %q exists and there is no mount table: %v", dir, err)
		return
	}
	for _, m := range mounts {
		if within(m.Point, dir) {
			t.fail("something is mounted at or below the other runtime's config folder: %q", m.Point)
			return
		}
	}
	empty, err := c.sys.DirEmpty(dir)
	switch {
	case err != nil:
		t.skip("the other runtime's config folder %q exists and cannot be read: %v", dir, err)
		return
	case !empty:
		t.fail("the other runtime's config folder %q holds entries: only an empty folder is not a failure of the boundary", dir)
		return
	}
	*note = fmt.Sprintf("the other runtime's config folder %q is an empty folder that is not a mount point and has nothing mounted below it; "+
		"a folder the lead made itself is not a failure of the boundary", dir)
}

// 7. The tree is the lead's own clone and its three git paths are read-only.
func (c *checker) treeGit() Result {
	var t tally
	git := c.spec.Tree + "/.git"
	info, err := c.sys.Lstat(git)
	switch {
	case absent(err):
		t.fail("%q does not exist: the tree is not a clone", git)
	case err != nil:
		t.skip("%q: %v", git, err)
	case !info.IsDir():
		t.fail("%q is %s, not a folder: the tree is not the lead's own clone", git, info.Describe())
	}
	wantDir := []bool{false, true, true} // .git/config, .git/hooks, .git/info
	for i, p := range c.spec.GitReadOnly {
		info, err := c.sys.Lstat(p)
		switch {
		case absent(err):
			t.fail("%q does not exist, so nothing read-only covers it", p)
			continue
		case err != nil:
			t.skip("%q: %v", p, err)
			continue
		case info.IsDir() != wantDir[i] || info.IsSymlink() || !info.IsDir() && !info.IsRegular():
			t.fail("%q is %s", p, info.Describe())
			continue
		}
		c.readOnly(&t, "git path", p)
	}
	return t.result(7, fmt.Sprintf("%q is a folder and its config, hooks and info are read-only", git))
}

// 8. The managed-settings file is present and read-only, and contains the
// marker text. The match is a plain search for the text: a file that holds it
// in a place that does not make the guard hook active (inside a string, next
// to a setting that disables hooks) passes, and the detail says only that the
// file contains the text. That the hook runs is for the qualification case
// that exercises it (S18).
func (c *checker) managed() Result {
	var t tally
	p := c.spec.ManagedSettings
	info, err := c.sys.Lstat(p)
	switch {
	case absent(err):
		return result(8, StatusFailed, "%q does not exist", p)
	case err != nil:
		return result(8, StatusNotChecked, "%q: %v", p, err)
	case !info.IsRegular():
		return result(8, StatusFailed, "%q is %s, not a regular file", p, info.Describe())
	}
	c.readOnly(&t, "managed settings", p)
	data, err := c.sys.ReadFile(p, MaxManagedSettingsBytes)
	switch {
	case errors.Is(err, ErrTooLarge):
		t.fail("%q is larger than %d bytes", p, MaxManagedSettingsBytes)
	case errors.Is(err, ErrNotRegular):
		t.fail("%q is not a regular file", p)
	case err != nil:
		t.skip("%q cannot be read: %v", p, err)
	case c.spec.ManagedMarker != "" && !bytes.Contains(data, []byte(c.spec.ManagedMarker)):
		t.fail("%q does not contain the marker text %q", p, c.spec.ManagedMarker)
	}
	if c.spec.ManagedMarker == "" {
		return t.result(8, fmt.Sprintf("%q is a regular file and read-only; no marker is required for %s", p, c.spec.Runtime))
	}
	return t.result(8, fmt.Sprintf("%q is a regular file, read-only, and contains the marker text", p))
}

// MaxCoordEntries is the most entries of the published folder (/coord, one per
// feature) that assertion 11 lists. A folder with more is not looked at, and
// the assertion is not-checked.
const MaxCoordEntries = 4096

// coordinatorPrivate are the names, in a coordinator's own folder on the host
// (<state>/coords/<project>/<feature>), of what no lead may see: the
// coordinator's tree and its home. A published folder
// (<state>/pub/<project>/<feature>) holds neither.
var coordinatorPrivate = []string{"tree", "home"}

// parentFolder is one of the four parent folders of the design (section 3.3),
// at its fixed inside path.
type parentFolder struct {
	what, path string
	// writable says the lead writes it; the others are read-only.
	writable bool
}

// parentFolders are the four, in the order of the bind table.
func parentFolders() []parentFolder {
	return []parentFolder{
		{"the requests folder", profile.RequestsInside, true},
		{"the mailbox", profile.MailboxInside, false},
		{"the mail folder", profile.MailInside, true},
		{"the published folder", profile.CoordInside, false},
	}
}

// 11. The four parent folders are present, the mailbox and the published
// folder are not writable (and the requests and the mail folder are), and no
// coordinator's tree or home is visible.
//
// Present means a folder at the fixed inside path that is a mount of its own:
// a bind cannot be added to a running sandbox, so a plain folder there, which
// is not the bound one, never becomes it. Not writable is proved as assertion
// 7 proves it (readOnly); writable is its mirror, and both the mount flag and
// access(W_OK) must say so.
func (c *checker) parentFolders() Result {
	var t tally
	for _, f := range parentFolders() {
		info, err := c.sys.Lstat(f.path)
		switch {
		case absent(err):
			t.fail("%s %q does not exist", f.what, f.path)
			continue
		case err != nil:
			t.skip("%s %q: %v", f.what, f.path, err)
			continue
		case !info.IsDir():
			t.fail("%s %q is %s, not a folder", f.what, f.path, info.Describe())
			continue
		}
		c.ownMount(&t, f.what, f.path)
		if f.writable {
			c.readWrite(&t, f.what, f.path)
		} else {
			c.readOnly(&t, f.what, f.path)
		}
	}
	mounts, entries, compared := c.coordinatorsHidden(&t)
	return t.result(11, fmt.Sprintf("the four parent folders are present, each a mount of its own; %s and %s are writable, %s and %s read-only; "+
		"none of %d coordinator paths exists or is at, above or below a root, source or mount point of %d mounts; "+
		"%d identities are none of the %d of the coordinators' folders; none of %d entries of %s holds a tree or a home",
		profile.RequestsInside, profile.MailInside, profile.MailboxInside, profile.CoordInside,
		len(c.spec.CoordinatorPaths), mounts, compared, len(c.spec.CoordinatorIdentities), entries, profile.CoordInside))
}

// ownMount records whether the folder is a mount of its own: the mount that
// statx names for it is mounted at the folder itself, not above it.
func (c *checker) ownMount(t *tally, what, path string) {
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("%s %q: no mount table: %v", what, path, err)
		return
	}
	id, err := c.sys.MountID(path)
	if err != nil {
		t.skip("%s %q: no mount ID (statx): %v", what, path, err)
		return
	}
	m, ok := mountByID(mounts, id)
	switch {
	case !ok:
		t.skip("%s %q: mount ID %d is not in the mount table", what, path, id)
	case m.Point != path:
		t.fail("%s %q is not a mount of its own: it is part of the mount at %q", what, path, m.Point)
	}
}

// readWrite records whether path is on a mount that is not read-only and the
// kernel says it is writable: the mirror of readOnly, and both must hold, for
// the same reasons. Where the kernel gives no mount ID the result is
// not-checked.
func (c *checker) readWrite(t *tally, what, path string) {
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("%s %q: no mount table: %v", what, path, err)
	} else if id, err := c.sys.MountID(path); err != nil {
		t.skip("%s %q: no mount ID (statx): %v", what, path, err)
	} else if m, ok := mountByID(mounts, id); !ok {
		t.skip("%s %q: mount ID %d is not in the mount table", what, path, id)
	} else if m.ReadOnly() {
		t.fail("%s %q is on a read-only mount at %q", what, path, m.Point)
	}
	w, err := c.sys.Writable(path)
	switch {
	case err != nil:
		t.skip("%s %q: access(W_OK): %v", what, path, err)
	case !w:
		t.fail("%s %q is not writable by access(W_OK)", what, path)
	}
}

// coordinatorsHidden is the last part of assertion 11: no coordinator's tree
// or home is visible. It looks in the ways assertions 1 and 6 look for the
// owner's and the holder's paths: no coordinator path of the spec exists
// inside; no mount has a root, a source or a mount point that is one of them,
// lies below one or is an ancestor of one; and the identity of no mount point,
// of none of the four parent folders and of no entry of the published folder
// is one of the coordinators' (with no identity listed that cannot be asked,
// and the assertion is not-checked, never ok). It also lists the published
// folder, which holds one folder per feature, and fails an entry that holds a
// tree or a home: a coordinator's own folder bound in the place of what it
// publishes. It returns how many mounts and entries it looked at and how many
// identities it compared.
func (c *checker) coordinatorsHidden(t *tally) (mountCount, entryCount, compared int) {
	for _, p := range c.spec.CoordinatorPaths {
		c.mustBeAbsent(t, "coordinator path", p)
	}
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("no mount table: %v", err)
	}
	for _, m := range mounts {
		if what, p, ok := relatedMount(m, c.spec.CoordinatorPaths); ok {
			t.fail("mount %q has %s %q, which is at, above or below the coordinator path %q", m.Point, what, clipPath(m.value(what)), p)
		}
	}
	ic := c.identityCheck(t, c.spec.CoordinatorIdentities, "a coordinator's folder or of one of its ancestors")
	if ic == nil {
		t.skip("the spec lists no coordinator identity: the mounts and the parent folders cannot be compared by identity")
	} else {
		ic.mountPoints(mounts)
		for _, f := range parentFolders() {
			// One that is not there has failed already.
			ic.compare(f.what, f.path, false)
		}
	}
	names, err := c.sys.DirNames(profile.CoordInside, MaxCoordEntries)
	switch {
	case errors.Is(err, ErrTooLarge):
		t.skip("%s %q holds more than %d entries: none was looked at", "the published folder", profile.CoordInside, MaxCoordEntries)
	case absent(err):
		// It is not a folder, or not there: failed already.
	case err != nil:
		t.skip("%s %q cannot be listed: %v", "the published folder", profile.CoordInside, err)
	}
	for _, name := range names {
		entry := profile.CoordInside + "/" + name
		for _, private := range coordinatorPrivate {
			p := entry + "/" + private
			_, err := c.sys.Lstat(p)
			switch {
			case err == nil:
				t.fail("the published entry %q holds %q: a coordinator's %s is visible", entry, private, private)
			case absent(err):
			default:
				t.skip("%q: cannot tell whether it exists: %v", p, err)
			}
		}
		if ic != nil {
			ic.compare("the published entry", entry, false)
		}
	}
	if ic != nil {
		compared = ic.checked
	}
	return len(mounts), len(names), compared
}
