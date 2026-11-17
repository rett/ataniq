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

// RunInside runs the inside checks, assertions 1 to 8, in order, over the
// system, and returns the report. It never panics on what a system returns and
// it never writes: it asks, with Lstat, ReadFile, Mounts, Environ, Dial,
// LookupHost and Writable. A check that cannot be made is not-checked, with
// the reason. The probe never issues TIOCSTI: assertion 5 reads the sysctl and
// the seccomp mode of the process.
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
	}
	return rep
}

type checker struct {
	spec Spec
	sys  System

	loaded    bool
	mounts    []Mount
	mountsErr error
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

// readOnly records whether path is on a read-only mount and the kernel says it
// is not writable. Both must hold: access(W_OK) alone says yes to a process
// with the rights to override permissions, and the mount flag alone does not
// see a permission or a file system that would let a write through.
func (c *checker) readOnly(t *tally, what, path string) {
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("%s %q: no mount table: %v", what, path, err)
	} else if m, ok := mountFor(mounts, path); !ok {
		t.skip("%s %q: no mount of the table holds it", what, path)
	} else if !m.ReadOnly() {
		t.fail("%s %q is on a writable mount at %q", what, path, m.Point)
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
		for _, sub := range c.spec.MountSubstrings {
			if strings.Contains(m.Point, sub) || strings.Contains(m.Root, sub) || strings.Contains(m.Source, sub) {
				t.fail("mount %q mentions %q", m.Point, sub)
				break
			}
		}
	}
	return t.result(1, fmt.Sprintf("none of %d holder paths exists; none of %d mounts mentions any of %d substrings",
		len(c.spec.HolderPaths), len(mounts), len(c.spec.MountSubstrings)))
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

// 3. One host loopback port and one host abstract socket are unreachable.
func (c *checker) loopback() Result {
	var t tally
	var shown []string
	for _, target := range []struct{ what, network, addr string }{
		{"host loopback port", "tcp", c.spec.LoopbackTCP},
		{"host abstract socket", "unix", "@" + c.spec.AbstractSocket},
	} {
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

// otherConfig checks the other runtime's config folder. A lead can make an
// empty folder at that path in its own home, so the folder existing is not a
// failure of the boundary: a mount at it, or below it, is.
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
	*note = fmt.Sprintf("the other runtime's config folder %q exists (%s) and nothing is mounted at or below it; "+
		"a folder the lead made itself is not a failure of the boundary", dir, info.Describe())
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
